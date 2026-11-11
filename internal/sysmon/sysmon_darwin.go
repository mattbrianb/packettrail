//go:build darwin

package sysmon

import (
	"bufio"
	"context"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// macOS without cgo: built from standard command-line tools. A long-running
// `iostat` supplies CPU, load and disk throughput; the rest is polled.
// Per-core CPU, disk busy time and handle counts are not available this way.

type macSource struct {
	ncpu   int
	total  uint64
	boot   int64
	prevAt time.Time
	prevIf map[string]macIf
	prevNS netstatS
	haveNS bool
	media  map[string][2]uint64
	ports  map[string]string // device -> hardware port ("Wi-Fi", "Ethernet", ...)
	slowAt time.Time
	disks  []Disk
	cancel context.CancelFunc
	mu     sync.Mutex
	io     struct {
		mbps, us, sy, id float64
		load             []float64
		ok               bool
	}
}

func run(name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	b, _ := cmd.Output()
	return string(b)
}

func newSource() source {
	s := &macSource{prevIf: map[string]macIf{}, ports: map[string]string{}}
	f := strings.Split(run("sysctl", "-n", "hw.ncpu", "hw.memsize", "kern.boottime"), "\n")
	if len(f) >= 3 {
		s.ncpu, _ = strconv.Atoi(strings.TrimSpace(f[0]))
		s.total = atou(f[1])
		if i := strings.Index(f[2], "sec = "); i >= 0 {
			s.boot = int64(atou(strings.TrimRight(strings.Fields(f[2][i+6:])[0], ",")))
		}
	}
	// "Hardware Port: Wi-Fi\nDevice: en0\n..."
	port := ""
	for _, ln := range strings.Split(run("networksetup", "-listallhardwareports"), "\n") {
		if v, ok := strings.CutPrefix(ln, "Hardware Port: "); ok {
			port = strings.TrimSpace(v)
		} else if v, ok := strings.CutPrefix(ln, "Device: "); ok && port != "" {
			s.ports[strings.TrimSpace(v)] = port
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	go s.iostat(ctx)
	return s
}

func (s *macSource) close() { s.cancel() }

// iostat keeps `iostat -w 2` running and records each interval line.
func (s *macSource) iostat(ctx context.Context) {
	for ctx.Err() == nil {
		cmd := exec.CommandContext(ctx, "iostat", "-w", "2", "-n", "8")
		cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
		out, err := cmd.StdoutPipe()
		if err != nil || cmd.Start() != nil {
			return
		}
		p := iostatParser{disks: -1}
		sc := bufio.NewScanner(out)
		first := true
		for sc.Scan() {
			mbps, us, sy, id, load, ok := p.line(sc.Text())
			if !ok {
				continue
			}
			if first { // the first line is the average since boot
				first = false
				continue
			}
			s.mu.Lock()
			s.io.mbps, s.io.us, s.io.sy, s.io.id, s.io.load, s.io.ok = mbps, us, sy, id, load, true
			s.mu.Unlock()
		}
		cmd.Wait()
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func (s *macSource) sample(now time.Time) *Snapshot {
	secs := 0.0
	if !s.prevAt.IsZero() {
		secs = now.Sub(s.prevAt).Seconds()
	}
	s.prevAt = now
	snap := &Snapshot{Elapsed: secs, Notes: []string{"Per-core CPU, disk busy time and handle counts are not available on macOS without extra tools."}}
	if s.boot > 0 {
		snap.Uptime = now.Unix() - s.boot
	}
	snap.CPU.Count = s.ncpu
	snap.DiskIO = DiskIO{Busy: -1, Latency: -1, QueueLen: -1}
	s.mu.Lock()
	if s.io.ok {
		snap.CPU.Busy = math.Max(0, 100-s.io.id)
		snap.CPU.User, snap.CPU.System, snap.CPU.Load = s.io.us, s.io.sy, s.io.load
		snap.DiskIO.Has, snap.DiskIO.Read = true, s.io.mbps*1e6
	}
	s.mu.Unlock()
	s.memory(snap)
	if now.Sub(s.slowAt) > 30*time.Second {
		s.slowAt = now
		s.disks = macDisks()
		s.media = parseIfconfigMedia(run("ifconfig", "-a"))
	}
	snap.Disks = s.disks
	s.ifaces(snap, secs)
	s.stack(snap, secs)
	s.procs(snap)
	return snap
}

func (s *macSource) memory(snap *Snapshot) {
	page, m := parseVMStat(run("vm_stat"))
	if s.total == 0 || len(m) == 0 {
		return
	}
	used := (m["Anonymous pages"] + m["Pages wired down"] + m["Pages occupied by compressor"]) * page
	if p := m["Pages purgeable"] * page; p < used {
		used -= p
	}
	used = min(used, s.total)
	snap.Mem = Mem{Total: s.total, Used: used, Avail: s.total - used, Pct: pct(float64(used), float64(s.total))}
	snap.Mem.SwapTotal, snap.Mem.SwapUsed = parseSwapUsage(run("sysctl", "-n", "vm.swapusage"))
}

func macDisks() []Disk {
	var out []Disk
	for _, d := range parseDfK(run("df", "-kl")) {
		if d.total == 0 || strings.HasPrefix(d.mount, "/System/Volumes/") || strings.HasPrefix(d.mount, "/Library/Developer") ||
			strings.HasPrefix(d.mount, "/private/var/") || strings.HasPrefix(d.fs, "map ") || d.fs == "devfs" {
			continue
		}
		label := filepath.Base(d.mount)
		if d.mount == "/" {
			label = "System"
		}
		out = append(out, Disk{Mount: d.mount, Label: label, FS: d.fs, Total: d.total, Free: d.free, Pct: pct(float64(d.total-d.free), float64(d.total))})
	}
	return out
}

func (s *macSource) ifaces(snap *Snapshot, secs float64) {
	cur := parseNetstatIbn(run("netstat", "-ibn"))
	next := map[string]macIf{}
	for _, c := range cur {
		n := c.name
		if n == "lo0" || strings.HasPrefix(n, "awdl") || strings.HasPrefix(n, "llw") || strings.HasPrefix(n, "gif") ||
			strings.HasPrefix(n, "stf") || strings.HasPrefix(n, "anpi") || strings.HasPrefix(n, "ap") {
			continue
		}
		next[n] = c
		port := s.ports[n]
		kind := "other"
		switch {
		case strings.Contains(port, "Wi-Fi"):
			kind = "wifi"
		case strings.Contains(port, "Ethernet") || strings.Contains(port, "LAN") || strings.Contains(port, "Thunderbolt Ethernet"):
			kind = "ethernet"
		case strings.HasPrefix(n, "utun") || strings.HasPrefix(n, "ipsec") || strings.HasPrefix(n, "ppp") || strings.HasPrefix(n, "tun"):
			kind = "vpn"
		case strings.HasPrefix(n, "bridge") || strings.HasPrefix(n, "vmenet") || strings.HasPrefix(n, "vnic"):
			kind = "virtual"
		}
		med, known := s.media[n]
		up := c.up && (!known || med[0] == 1 || kind == "vpn")
		phys := port != "" && kind != "vpn"
		if !phys && (!up || c.rxBytes+c.txBytes == 0) {
			continue
		}
		name := n
		if port != "" {
			name = port + " (" + n + ")"
		}
		f := Iface{ID: n, Name: name, Desc: port, Kind: kind, Physical: phys, Up: up, MTU: c.mtu, MAC: c.mac,
			RxBytes: c.rxBytes, TxBytes: c.txBytes, Errs: c.rxErrs + c.txErrs}
		if up && known && med[1] > 0 {
			f.Speed, f.SpeedTx = med[1], med[1]
		}
		if p, ok := s.prevIf[n]; ok {
			f.Rx, f.Tx = rate(c.rxBytes, p.rxBytes, secs), rate(c.txBytes, p.txBytes, secs)
		}
		f.RxUtil, f.TxUtil = utilOf(f.Rx, f.Speed), utilOf(f.Tx, f.SpeedTx)
		snap.Ifaces = append(snap.Ifaces, f)
	}
	s.prevIf = next
	sort.SliceStable(snap.Ifaces, func(i, j int) bool {
		a, b := snap.Ifaces[i], snap.Ifaces[j]
		if a.Up != b.Up {
			return a.Up
		}
		if a.Physical != b.Physical {
			return a.Physical
		}
		return a.RxBytes+a.TxBytes > b.RxBytes+b.TxBytes
	})
}

func (s *macSource) stack(snap *Snapshot, secs float64) {
	cur := parseNetstatS(run("netstat", "-s"))
	if !cur.ok {
		return
	}
	snap.TCP = TCP{Has: true, Estab: -1, RetransPct1: -1}
	if p := s.prevNS; s.haveNS && secs > 0 {
		snap.TCP.OutSegs = rate(cur.tcpSent, p.tcpSent, secs)
		snap.TCP.InSegs = rate(cur.tcpRecv, p.tcpRecv, secs)
		snap.TCP.Retrans = rate(cur.tcpRetrans, p.tcpRetrans, secs)
		snap.TCP.InErrs = rate(cur.tcpBadSum, p.tcpBadSum, secs)
		snap.TCP.RetransPct = pct(snap.TCP.Retrans, snap.TCP.OutSegs)
		snap.TCP.UDPErrs = rate(cur.udpFull, p.udpFull, secs)
		snap.TCP.UDPIn = rate(cur.udpRecv, p.udpRecv, secs)
		snap.TCP.UDPOut = rate(cur.udpOut, p.udpOut, secs)
	}
	s.prevNS, s.haveNS = cur, true
}

func (s *macSource) procs(snap *Snapshot) {
	ncpu := float64(max(1, s.ncpu))
	for _, p := range parsePs(run("ps", "-axo", "pid=,pcpu=,rss=,comm=")) {
		if p.pid == 0 {
			continue
		}
		pr := Proc{PID: p.pid, Name: filepath.Base(p.comm), CPU: math.Min(100, p.cpu/ncpu), Mem: p.rss, IO: -1, Threads: -1, Handles: -1}
		if strings.HasPrefix(p.comm, "/") {
			pr.Path = p.comm
		}
		snap.Procs = append(snap.Procs, pr)
	}
}
