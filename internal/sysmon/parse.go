package sysmon

// Text parsers for Linux /proc files and macOS command output. They have no
// build tags so they are unit-tested on every platform.

import (
	"bufio"
	"strconv"
	"strings"
)

func atou(s string) uint64 {
	n, _ := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	return n
}

func atof(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f
}

// ---------------------------------------------------------------- Linux

// cpuLine is one "cpu" row of /proc/stat, in clock ticks.
type cpuLine struct {
	user, nice, system, idle, iowait, irq, softirq, steal uint64
}

func (c cpuLine) total() uint64 {
	return c.user + c.nice + c.system + c.idle + c.iowait + c.irq + c.softirq + c.steal
}

// parseProcStat returns the aggregate line and one per core.
func parseProcStat(text string) (all cpuLine, cores []cpuLine) {
	for _, ln := range strings.Split(text, "\n") {
		f := strings.Fields(ln)
		if len(f) < 9 || !strings.HasPrefix(f[0], "cpu") {
			continue
		}
		c := cpuLine{atou(f[1]), atou(f[2]), atou(f[3]), atou(f[4]), atou(f[5]), atou(f[6]), atou(f[7]), atou(f[8])}
		if f[0] == "cpu" {
			all = c
		} else {
			cores = append(cores, c)
		}
	}
	return
}

// cpuPct turns two /proc/stat readings into percentages.
func cpuPct(c, p cpuLine) (busy, user, sys, irq, wait, steal float64) {
	t := float64(c.total() - p.total())
	if c.total() < p.total() || t <= 0 {
		return
	}
	idle := float64((c.idle + c.iowait) - (p.idle + p.iowait))
	return pct(t-idle, t), pct(float64((c.user+c.nice)-(p.user+p.nice)), t), pct(float64(c.system-p.system), t),
		pct(float64((c.irq+c.softirq)-(p.irq+p.softirq)), t), pct(float64(c.iowait-p.iowait), t), pct(float64(c.steal-p.steal), t)
}

// parseKV parses "Key:   123 kB" lines (meminfo) into bytes when a kB unit is given.
func parseKV(text string) map[string]uint64 {
	m := map[string]uint64{}
	for _, ln := range strings.Split(text, "\n") {
		k, v, ok := strings.Cut(ln, ":")
		if !ok {
			continue
		}
		f := strings.Fields(v)
		if len(f) == 0 {
			continue
		}
		n := atou(f[0])
		if len(f) > 1 && f[1] == "kB" {
			n *= 1024
		}
		m[strings.TrimSpace(k)] = n
	}
	return m
}

func parseLoadavg(text string) []float64 {
	f := strings.Fields(text)
	if len(f) < 3 {
		return nil
	}
	return []float64{atof(f[0]), atof(f[1]), atof(f[2])}
}

// netDev is one /proc/net/dev row.
type netDev struct {
	rxBytes, rxErrs, rxDrop, txBytes, txErrs, txDrop uint64
}

func parseNetDev(text string) map[string]netDev {
	m := map[string]netDev{}
	for _, ln := range strings.Split(text, "\n") {
		name, rest, ok := strings.Cut(ln, ":")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 16 {
			continue
		}
		m[strings.TrimSpace(name)] = netDev{atou(f[0]), atou(f[2]), atou(f[3]), atou(f[8]), atou(f[10]), atou(f[11])}
	}
	return m
}

// diskStat is one /proc/diskstats row.
type diskStat struct {
	reads, sectorsRead, msReading, writes, sectorsWritten, msWriting, inFlight, msIO uint64
}

func parseDiskstats(text string) map[string]diskStat {
	m := map[string]diskStat{}
	for _, ln := range strings.Split(text, "\n") {
		f := strings.Fields(ln)
		if len(f) < 14 {
			continue
		}
		m[f[2]] = diskStat{atou(f[3]), atou(f[5]), atou(f[6]), atou(f[7]), atou(f[9]), atou(f[10]), atou(f[11]), atou(f[12])}
	}
	return m
}

// parseSnmp parses /proc/net/snmp's header/value line pairs.
func parseSnmp(text string) map[string]map[string]uint64 {
	out := map[string]map[string]uint64{}
	var hdr []string
	var proto string
	for _, ln := range strings.Split(text, "\n") {
		p, rest, ok := strings.Cut(ln, ":")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			continue
		}
		if _, err := strconv.ParseInt(f[0], 10, 64); err != nil {
			hdr, proto = f, p
			continue
		}
		if p != proto || len(hdr) != len(f) {
			continue
		}
		m := map[string]uint64{}
		for i, k := range hdr {
			n, err := strconv.ParseInt(f[i], 10, 64)
			if err == nil && n >= 0 {
				m[k] = uint64(n)
			}
		}
		out[p] = m
	}
	return out
}

// pidStat is the part of /proc/<pid>/stat we use.
type pidStat struct {
	name                string
	utime, stime, start uint64
	threads             int
	rssPages            uint64
}

func parsePidStat(text string) (pidStat, bool) {
	l, r := strings.IndexByte(text, '('), strings.LastIndexByte(text, ')')
	if l < 0 || r < l {
		return pidStat{}, false
	}
	f := strings.Fields(text[r+1:])
	// f[0] is field 3 (state); field n is f[n-3].
	if len(f) < 22 {
		return pidStat{}, false
	}
	th, _ := strconv.Atoi(f[17])
	return pidStat{name: text[l+1 : r], utime: atou(f[11]), stime: atou(f[12]), threads: th, start: atou(f[19]), rssPages: atou(f[21])}, true
}

// parseProcIO returns rchar+wchar (all reads and writes, including sockets).
func parseProcIO(text string) (uint64, bool) {
	m := parseKV(text)
	r, ok1 := m["rchar"]
	w, ok2 := m["wchar"]
	return r + w, ok1 && ok2
}

// ---------------------------------------------------------------- macOS

// iostatParser follows a long-running `iostat -w N` stream.
type iostatParser struct {
	disks int // -1 until a header has been seen
}

// line parses one line; ok is false for headers and malformed lines.
// mbps is the summed disk throughput, then CPU user/system/idle and load.
func (p *iostatParser) line(ln string) (mbps, us, sy, id float64, load []float64, ok bool) {
	f := strings.Fields(ln)
	if len(f) == 0 {
		return
	}
	if _, err := strconv.ParseFloat(f[0], 64); err != nil {
		// Header: "disk0 disk4 cpu load average" or the units line.
		for i, w := range f {
			if w == "cpu" {
				p.disks = i
				break
			}
		}
		return
	}
	if p.disks < 0 || len(f) != 3*p.disks+6 {
		return
	}
	for d := 0; d < p.disks; d++ {
		mbps += atof(f[3*d+2])
	}
	o := 3 * p.disks
	return mbps, atof(f[o]), atof(f[o+1]), atof(f[o+2]), []float64{atof(f[o+3]), atof(f[o+4]), atof(f[o+5])}, true
}

// parseVMStat returns the page size and page counts from vm_stat.
func parseVMStat(text string) (page uint64, m map[string]uint64) {
	m = map[string]uint64{}
	page = 4096
	for _, ln := range strings.Split(text, "\n") {
		if i := strings.Index(ln, "page size of "); i >= 0 {
			if n := atou(strings.Fields(ln[i+len("page size of "):])[0]); n > 0 {
				page = n
			}
			continue
		}
		k, v, ok := strings.Cut(ln, ":")
		if !ok {
			continue
		}
		m[strings.TrimSpace(k)] = atou(strings.TrimSuffix(strings.TrimSpace(v), "."))
	}
	return
}

// parseSwapUsage parses sysctl vm.swapusage ("total = 2048.00M  used = 1024.50M ...").
func parseSwapUsage(s string) (total, used uint64) {
	val := func(key string) uint64 {
		i := strings.Index(s, key+" = ")
		if i < 0 {
			return 0
		}
		f := strings.Fields(s[i+len(key)+3:])
		if len(f) == 0 {
			return 0
		}
		v := f[0]
		mult := 1.0
		switch v[len(v)-1] {
		case 'K':
			mult = 1 << 10
		case 'M':
			mult = 1 << 20
		case 'G':
			mult = 1 << 30
		}
		return uint64(atof(strings.TrimRight(v, "KMGB")) * mult)
	}
	return val("total"), val("used")
}

// macIf is one interface row from `netstat -ibn`.
type macIf struct {
	name                             string
	up                               bool
	mtu                              int
	mac                              string
	rxBytes, txBytes, rxErrs, txErrs uint64
}

func parseNetstatIbn(text string) []macIf {
	var out []macIf
	seen := map[string]bool{}
	for _, ln := range strings.Split(text, "\n") {
		f := strings.Fields(ln)
		if len(f) < 10 || !strings.HasPrefix(f[2], "<Link#") {
			continue
		}
		name := f[0]
		up := !strings.HasSuffix(name, "*")
		name = strings.TrimSuffix(name, "*")
		if seen[name] {
			continue
		}
		seen[name] = true
		mtu, _ := strconv.Atoi(f[1])
		mac := ""
		if strings.Count(f[3], ":") == 5 {
			mac = f[3]
		}
		// Count from the right (the address column is missing for some
		// interfaces): Ipkts Ierrs Ibytes Opkts Oerrs Obytes Coll.
		r := f[len(f)-7:]
		out = append(out, macIf{name: name, up: up, mtu: mtu, mac: mac, rxErrs: atou(r[1]), rxBytes: atou(r[2]),
			txErrs: atou(r[4]), txBytes: atou(r[5])})
	}
	return out
}

// parseIfconfigMedia returns, per interface, whether it is active and its
// negotiated speed in bits/s (0 when unknown, e.g. Wi-Fi).
func parseIfconfigMedia(text string) map[string][2]uint64 {
	out := map[string][2]uint64{}
	cur := ""
	var active, speed uint64
	flush := func() {
		if cur != "" {
			out[cur] = [2]uint64{active, speed}
		}
	}
	for _, ln := range strings.Split(text, "\n") {
		if ln != "" && ln[0] != '\t' && ln[0] != ' ' {
			flush()
			cur, active, speed = strings.TrimSuffix(strings.Fields(ln)[0], ":"), 0, 0
			continue
		}
		t := strings.TrimSpace(ln)
		switch {
		case strings.HasPrefix(t, "status: active"):
			active = 1
		case strings.HasPrefix(t, "media:"):
			if i := strings.Index(t, "("); i >= 0 {
				m := t[i+1:]
				if j := strings.IndexAny(m, " )"); j > 0 {
					m = m[:j]
				}
				// e.g. 1000baseT, 10Gbase-T, 2500Base-T, 100baseTX
				lm := strings.ToLower(m)
				if k := strings.Index(lm, "base"); k > 0 {
					num := lm[:k]
					mult := 1e6
					if strings.HasSuffix(num, "g") {
						num, mult = strings.TrimSuffix(num, "g"), 1e9
					}
					if n := atof(num); n > 0 {
						speed = uint64(n * mult)
					}
				}
			}
		}
	}
	flush()
	return out
}

// macDisk is one `df -kl` row.
type macDisk struct {
	mount       string
	fs          string
	total, free uint64
}

func parseDfK(text string) []macDisk {
	var out []macDisk
	for i, ln := range strings.Split(text, "\n") {
		f := strings.Fields(ln)
		if i == 0 || len(f) < 9 {
			continue
		}
		mount := strings.Join(f[8:], " ")
		out = append(out, macDisk{mount: mount, fs: f[0], total: atou(f[1]) * 1024, free: atou(f[3]) * 1024})
	}
	return out
}

// macProc is one `ps -axo pid=,pcpu=,rss=,comm=` row.
type macProc struct {
	pid  int
	cpu  float64 // percent of one core
	rss  uint64  // bytes
	comm string
}

func parsePs(text string) []macProc {
	var out []macProc
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		out = append(out, macProc{pid: pid, cpu: atof(f[1]), rss: atou(f[2]) * 1024, comm: strings.Join(f[3:], " ")})
	}
	return out
}

// netstatS holds the counters we use from `netstat -s`.
type netstatS struct {
	tcpSent, tcpRetrans, tcpRecv, tcpBadSum, tcpDrops uint64
	udpRecv, udpOut, udpFull                          uint64
	ok                                                bool
}

func parseNetstatS(text string) netstatS {
	var s netstatS
	section := ""
	lead := func(t string) uint64 { return atou(strings.Fields(t)[0]) }
	for _, ln := range strings.Split(text, "\n") {
		if ln != "" && ln[0] != '\t' && ln[0] != ' ' {
			section = strings.TrimSuffix(strings.TrimSpace(ln), ":")
			continue
		}
		t := strings.TrimSpace(ln)
		if t == "" || t[0] < '0' || t[0] > '9' {
			continue
		}
		switch section {
		case "tcp":
			switch {
			case strings.HasSuffix(t, " packets sent"):
				s.tcpSent, s.ok = lead(t), true
			case strings.Contains(t, "data packets") && strings.HasSuffix(t, "retransmitted"):
				s.tcpRetrans = lead(t)
			case strings.HasSuffix(t, " packets received"):
				s.tcpRecv = lead(t)
			case strings.Contains(t, "discarded for bad checksum"):
				s.tcpBadSum += lead(t)
			case strings.HasSuffix(t, "connections dropped"):
				s.tcpDrops = lead(t)
			}
		case "udp":
			switch {
			case strings.HasSuffix(t, " datagrams received"):
				s.udpRecv = lead(t)
			case strings.HasSuffix(t, " datagrams output"):
				s.udpOut = lead(t)
			case strings.Contains(t, "dropped due to full socket buffers"):
				s.udpFull = lead(t)
			}
		}
	}
	return s
}
