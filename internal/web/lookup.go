package web

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Target is what the Tools tab resolved a typed address or hostname to.
type Target struct {
	Query string   `json:"query"` // the cleaned-up input
	Host  string   `json:"host"`  // set when the input was a hostname
	Addrs []string `json:"addrs"` // IPv4 first
	Port  int      `json:"port"`  // from "host:port" input, else 0
}

var toolHostRe = regexp.MustCompile(`^[A-Za-z0-9_]([A-Za-z0-9_-]{0,62}[A-Za-z0-9_])?(\.[A-Za-z0-9_]([A-Za-z0-9_-]{0,62}[A-Za-z0-9_])?)*\.?$`)

// parseTarget accepts an IP, a hostname, either with a port, or a pasted URL.
func parseTarget(q string) (host string, ip netip.Addr, port int, err error) {
	q = strings.TrimSpace(q)
	if i := strings.Index(q, "://"); i >= 0 {
		q = q[i+3:]
	}
	if i := strings.IndexAny(q, "/?#"); i >= 0 {
		q = q[:i]
	}
	if i := strings.LastIndex(q, "@"); i >= 0 { // user@host
		q = q[i+1:]
	}
	if q == "" {
		return "", ip, 0, errors.New("enter an IP address or hostname")
	}
	if a, e := netip.ParseAddr(strings.Trim(q, "[]")); e == nil {
		return "", a, 0, nil
	}
	if ap, e := netip.ParseAddrPort(q); e == nil {
		return "", ap.Addr(), int(ap.Port()), nil
	}
	if h, p, e := net.SplitHostPort(q); e == nil {
		n, e := strconv.Atoi(p)
		if e != nil || n < 1 || n > 65535 {
			return "", ip, 0, errors.New("bad port")
		}
		q, port = h, n
	}
	if len(q) > 253 || !toolHostRe.MatchString(q) {
		return "", ip, 0, errors.New("not a valid IP address or hostname")
	}
	return strings.ToLower(strings.TrimSuffix(q, ".")), ip, port, nil
}

func checkUnicast(a netip.Addr) error {
	if a.IsUnspecified() || a.IsMulticast() || a == netip.AddrFrom4([4]byte{255, 255, 255, 255}) {
		return errors.New(a.String() + " is not a unicast address")
	}
	return nil
}

func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	host, ip, port, err := parseTarget(r.URL.Query().Get("q"))
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	t := Target{Port: port, Addrs: []string{}}
	if ip.IsValid() {
		ip = ip.Unmap().WithZone("")
		if err := checkUnicast(ip); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		t.Query, t.Addrs = ip.String(), []string{ip.String()}
		writeJSON(w, t)
		return
	}
	t.Query, t.Host = host, host
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		var de *net.DNSError
		if errors.As(err, &de) && de.IsNotFound {
			writeErr(w, 404, "no address found for "+host)
		} else if ctx.Err() != nil {
			writeErr(w, 504, "DNS lookup for "+host+" timed out")
		} else {
			writeErr(w, 502, "could not resolve "+host)
		}
		return
	}
	seen := map[netip.Addr]bool{}
	var list []netip.Addr
	for _, a := range ips {
		a = a.Unmap().WithZone("")
		if !seen[a] && checkUnicast(a) == nil {
			seen[a] = true
			list = append(list, a)
		}
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].Is4() && !list[j].Is4() })
	if len(list) > 8 {
		list = list[:8]
	}
	for _, a := range list {
		t.Addrs = append(t.Addrs, a.String())
	}
	if len(t.Addrs) == 0 {
		writeErr(w, 404, "no usable address for "+host)
		return
	}
	writeJSON(w, t)
}
