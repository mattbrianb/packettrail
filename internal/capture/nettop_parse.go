package capture

import (
	"bufio"
	"bytes"
	"net/netip"
	"strconv"
	"strings"
)

// NettopRow is one socket line of `nettop -L 1 -n -x` output.
type NettopRow struct {
	PID           int
	Name          string
	Proto         Proto
	Local, Remote netip.AddrPort
	In, Out       uint64
	Retrans       uint64  // "re-tx" column, bytes (when present)
	RTT           float64 // "rtt_avg" column, ms (0 when absent)
}

// ParseNettop parses nettop's CSV logging output. The identifier sits in the
// one column whose header is empty: the first column when -J selects columns
// (",bytes_in,bytes_out,"), the second when the time column is present
// ("time,,bytes_in,bytes_out,"). Every line also ends in a comma, so the last
// (empty) field is never the identifier. Process lines look like
// "Google Chrome H.1234,in,out," and are followed by their socket lines, e.g.
// "tcp4 192.168.1.5:50123<->142.250.1.1:443,in,out,". IPv6 endpoints use
// "addr.port".
func ParseNettop(out []byte) []NettopRow {
	var rows []NettopRow
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	idCol, inCol, outCol, rtxCol, rttCol := -1, -1, -1, -1, -1
	curPID, curName := 0, ""
	for sc.Scan() {
		f := strings.Split(sc.Text(), ",")
		if len(f) < 3 {
			continue
		}
		if inCol < 0 {
			for i, h := range f {
				switch strings.TrimSpace(h) {
				case "bytes_in":
					inCol = i
				case "bytes_out":
					outCol = i
				case "re-tx":
					rtxCol = i
				case "rtt_avg":
					rttCol = i
				case "":
					if idCol < 0 && i < len(f)-1 {
						idCol = i
					}
				}
			}
			if inCol >= 0 {
				if idCol < 0 {
					idCol = 0
				}
				continue
			}
			// No header seen: the identifier is the first field unless a
			// timestamp ("16:00:00.123456") comes before it.
			idCol = 0
			if t := strings.TrimSpace(f[0]); len(t) >= 8 && t[2] == ':' && t[5] == ':' {
				idCol = 1
			}
			inCol, outCol = idCol+1, idCol+2
		}
		if idCol >= len(f) || inCol >= len(f) || outCol >= len(f) {
			continue
		}
		id := strings.TrimSpace(f[idCol])
		in, _ := strconv.ParseUint(strings.TrimSpace(f[inCol]), 10, 64)
		outB, _ := strconv.ParseUint(strings.TrimSpace(f[outCol]), 10, 64)
		// A socket line is "<kind> <local><-><remote>"; anything else is a
		// process line "name.pid". Checking for "<->" first matters: an IPv6
		// socket line also ends in ".<number>" (its port).
		kind, pair, isSocket := strings.Cut(id, " ")
		if !isSocket || !strings.Contains(pair, "<->") {
			if dot := strings.LastIndexByte(id, '.'); dot > 0 {
				if pid, err := strconv.Atoi(id[dot+1:]); err == nil {
					curPID, curName = pid, id[:dot]
				}
			}
			continue
		}
		var proto Proto
		switch kind {
		case "tcp4", "tcp6":
			proto = TCP
		case "udp4", "udp6", "quic4", "quic6": // QUIC (HTTP/3) runs over UDP
			proto = UDP
		default:
			continue // a socket kind this parser does not know
		}
		ends := strings.SplitN(pair, "<->", 2)
		if len(ends) != 2 || curPID == 0 {
			continue
		}
		l, okL := parseNettopEndpoint(ends[0])
		r, okR := parseNettopEndpoint(ends[1])
		if !okL || !okR {
			continue
		}
		row := NettopRow{PID: curPID, Name: curName, Proto: proto, Local: l, Remote: r, In: in, Out: outB}
		if rtxCol >= 0 && rtxCol < len(f) {
			row.Retrans, _ = strconv.ParseUint(strings.TrimSpace(f[rtxCol]), 10, 64)
		}
		if rttCol >= 0 && rttCol < len(f) {
			row.RTT = leadingFloat(f[rttCol])
		}
		rows = append(rows, row)
	}
	return rows
}

func parseNettopEndpoint(s string) (netip.AddrPort, bool) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "*") {
		return netip.AddrPort{}, false
	}
	if ap, err := netip.ParseAddrPort(s); err == nil { // 1.2.3.4:443 or [v6]:443
		return Unmap(ap), true
	}
	sep := strings.LastIndexByte(s, '.')
	if strings.Count(s, ":") < 2 {
		sep = strings.LastIndexByte(s, ':')
	}
	if sep <= 0 {
		return netip.AddrPort{}, false
	}
	host := s[:sep]
	if i := strings.IndexByte(host, '%'); i >= 0 {
		host = host[:i]
	}
	ip, err := netip.ParseAddr(host)
	port, err2 := strconv.ParseUint(s[sep+1:], 10, 16)
	if err != nil || err2 != nil {
		return netip.AddrPort{}, false
	}
	return netip.AddrPortFrom(ip.Unmap(), uint16(port)), true
}

// leadingFloat parses values like "12.34 ms" or "12.34".
func leadingFloat(v string) float64 {
	v = strings.TrimSpace(v)
	end := 0
	for end < len(v) && (v[end] == '.' || (v[end] >= '0' && v[end] <= '9')) {
		end++
	}
	f, _ := strconv.ParseFloat(v[:end], 64)
	return f
}
