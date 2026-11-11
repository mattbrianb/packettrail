//go:build linux

package sysmon

import (
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Linux: everything comes from /proc and /sys; no root needed except for
// other users' process I/O and executable paths.

const clkTck = 100 // USER_HZ; 100 on every mainstream architecture

type linuxSource struct {
	prevAt    time.Time
	prevAll   cpuLine
	prevCores []cpuLine
	prevNet   map[string]netDev
	prevDisk  map[string]diskStat
	prevSnmp  map[string]map[string]uint64
	prevProc  map[int]*procPrev
	page      uint64
	disksAt   time.Time
	disks     []Disk
}

func newSource() source {
	return &linuxSource{prevNet: map[string]netDev{}, prevDisk: map[string]diskStat{}, prevProc: map[int]*procPrev{}, page: uint64(os.Getpagesize())}
}

func (s *linuxSource) close() {}

func readText(p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}

func (s *linuxSource) sample(now time.Time) *Snapshot {
	secs := 0.0
	if !s.prevAt.IsZero() {
		secs = now.Sub(s.prevAt).Seconds()
	}
	s.prevAt = now
	snap := &Snapshot{Elapsed: secs}
	if f := strings.Fields(readText("/proc/uptime")); len(f) > 0 {
		snap.Uptime = int64(atof(f[0]))
	}
	s.cpu(snap)
	s.memory(snap)
	if now.Sub(s.disksAt) > 30*time.Second || s.disks == nil {
		s.disks, s.disksAt = mounts(), now
	}
	snap.Disks = s.disks
	s.diskIO(snap, secs)
	s.ifaces(snap, secs)
	s.stack(snap, secs)
	s.procs(snap, secs)
	return snap
}

func (s *linuxSource) cpu(snap *Snapshot) {
	all, cores := parseProcStat(readText("/proc/stat"))
	snap.CPU.Count, snap.CPU.HasWait = len(cores), true
	snap.CPU.Load = parseLoadavg(readText("/proc/loadavg"))
	if s.prevAll.total() > 0 {
		snap.CPU.Busy, snap.CPU.User, snap.CPU.System, snap.CPU.IRQ, snap.CPU.IOWait, snap.CPU.Steal = cpuPct(all, s.prevAll)
		if len(cores) == len(s.prevCores) {
			for i := range cores {
				b, _, _, _, _, _ := cpuPct(cores[i], s.prevCores[i])
				snap.CPU.Cores = append(snap.CPU.Cores, math.Round(b*10)/10)
			}
		}
	}
	s.prevAll, s.prevCores = all, cores
}

func (s *linuxSource) memory(snap *Snapshot) {
	m := parseKV(readText("/proc/meminfo"))
	total, avail := m["MemTotal"], m["MemAvailable"]
	if avail == 0 {
		avail = m["MemFree"] + m["Buffers"] + m["Cached"]
	}
	if total == 0 {
		return
	}
	snap.Mem = Mem{Total: total, Avail: avail, Used: total - min(avail, total), Pct: pct(float64(total-min(avail, total)), float64(total)),
		SwapTotal: m["SwapTotal"], SwapUsed: m["SwapTotal"] - min(m["SwapFree"], m["SwapTotal"])}
}

var realFS = map[string]bool{"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true, "zfs": true, "f2fs": true,
	"vfat": true, "exfat": true, "ntfs": true, "ntfs3": true, "fuseblk": true, "jfs": true, "reiserfs": true, "bcachefs": true}

func mounts() []Disk {
	out := []Disk{}
	seen := map[string]bool{}
	for _, ln := range strings.Split(readText("/proc/self/mounts"), "\n") {
		f := strings.Fields(ln)
		if len(f) < 3 || !realFS[f[2]] || seen[f[0]] {
			continue
		}
		mp := strings.NewReplacer(`\040`, " ", `\011`, "\t").Replace(f[1])
		if strings.HasPrefix(mp, "/snap/") || strings.HasPrefix(mp, "/var/lib/docker") || strings.HasPrefix(mp, "/boot/efi") {
			continue
		}
		var st unix.Statfs_t
		if unix.Statfs(mp, &st) != nil || st.Blocks == 0 {
			continue
		}
		seen[f[0]] = true
		bs := uint64(st.Bsize)
		total, free := st.Blocks*bs, st.Bavail*bs
		out = append(out, Disk{Mount: mp, Label: filepath.Base(f[0]), FS: f[2], Total: total, Free: free, Pct: pct(float64(total-free), float64(total))})
	}
	return out
}

// wholeDisk reports whether a diskstats name is a physical disk rather than a
// partition or a stacked device (which would double-count).
func wholeDisk(name string) bool {
	for _, p := range []string{"loop", "ram", "zram", "dm-", "md", "sr", "fd", "nbd"} {
		if strings.HasPrefix(name, p) {
			return false
		}
	}
	_, err := os.Stat("/sys/block/" + name)
	return err == nil
}

func (s *linuxSource) diskIO(snap *Snapshot, secs float64) {
	snap.DiskIO = DiskIO{Busy: -1, Latency: -1, QueueLen: -1}
	cur := parseDiskstats(readText("/proc/diskstats"))
	if len(cur) == 0 {
		return
	}
	var rd, wr, busy, ios, ms, q float64
	for name, c := range cur {
		if !wholeDisk(name) {
			continue
		}
		p, ok := s.prevDisk[name]
		if !ok || secs <= 0 {
			continue
		}
		rd += rate(c.sectorsRead, p.sectorsRead, secs) * 512
		wr += rate(c.sectorsWritten, p.sectorsWritten, secs) * 512
		busy = math.Max(busy, pct(rate(c.msIO, p.msIO, secs), 1000))
		ios += rate(c.reads+c.writes, p.reads+p.writes, 1)
		ms += rate(c.msReading+c.msWriting, p.msReading+p.msWriting, 1)
		q += float64(c.inFlight)
	}
	s.prevDisk = cur
	if secs <= 0 {
		return
	}
	snap.DiskIO = DiskIO{Has: true, Split: true, Read: rd, Write: wr, Busy: busy, Latency: -1, QueueLen: q}
	if ios > 0 {
		snap.DiskIO.Latency = ms / ios
	} else {
		snap.DiskIO.Latency = 0
	}
}

func sysInt(iface, file string) (int64, bool) {
	n, err := strconv.ParseInt(strings.TrimSpace(readText("/sys/class/net/"+iface+"/"+file)), 10, 64)
	return n, err == nil
}

func (s *linuxSource) ifaces(snap *Snapshot, secs float64) {
	cur := parseNetDev(readText("/proc/net/dev"))
	for name, c := range cur {
		if name == "lo" {
			continue
		}
		base := "/sys/class/net/" + name + "/"
		_, devErr := os.Stat(base + "device")
		_, wErr := os.Stat(base + "wireless")
		phys := devErr == nil
		typ, _ := sysInt(name, "type")
		kind := "other"
		switch {
		case wErr == nil:
			kind = "wifi"
		case strings.HasPrefix(name, "wwan") || strings.HasPrefix(name, "wwp"):
			kind = "cellular"
		case typ == 65534 || strings.HasPrefix(name, "tun") || strings.HasPrefix(name, "tap") || strings.HasPrefix(name, "wg") ||
			strings.HasPrefix(name, "ppp") || strings.HasPrefix(name, "tailscale") || strings.HasPrefix(name, "zt"):
			kind = "vpn"
		case !phys:
			kind = "virtual"
		case typ == 1:
			kind = "ethernet"
		}
		oper := strings.TrimSpace(readText(base + "operstate"))
		up := oper == "up" || (oper == "unknown" && kind == "vpn")
		if !phys && !up {
			continue
		}
		mtu, _ := sysInt(name, "mtu")
		f := Iface{ID: name, Name: name, Desc: strings.TrimSpace(readText(base + "device/uevent")), Kind: kind, Physical: phys, Up: up,
			MTU: int(mtu), MAC: strings.TrimSpace(readText(base + "address")), RxBytes: c.rxBytes, TxBytes: c.txBytes,
			Errs: c.rxErrs + c.txErrs, Discards: c.rxDrop + c.txDrop}
		f.Desc = driverName(f.Desc)
		if sp, ok := sysInt(name, "speed"); ok && sp > 0 && up {
			f.Speed, f.SpeedTx = uint64(sp)*1e6, uint64(sp)*1e6
		}
		if p, ok := s.prevNet[name]; ok {
			f.Rx, f.Tx = rate(c.rxBytes, p.rxBytes, secs), rate(c.txBytes, p.txBytes, secs)
		}
		f.RxUtil, f.TxUtil = utilOf(f.Rx, f.Speed), utilOf(f.Tx, f.SpeedTx)
		snap.Ifaces = append(snap.Ifaces, f)
	}
	s.prevNet = cur
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

// driverName pulls DRIVER= out of a device uevent file.
func driverName(uevent string) string {
	for _, ln := range strings.Split(uevent, "\n") {
		if v, ok := strings.CutPrefix(ln, "DRIVER="); ok {
			return "driver " + v
		}
	}
	return ""
}

func (s *linuxSource) stack(snap *Snapshot, secs float64) {
	cur := parseSnmp(readText("/proc/net/snmp"))
	t, u := cur["Tcp"], cur["Udp"]
	if t == nil {
		return
	}
	snap.TCP = TCP{Has: true, Estab: int(t["CurrEstab"]), RetransPct1: -1}
	if p := s.prevSnmp; p != nil && p["Tcp"] != nil && secs > 0 {
		pt, pu := p["Tcp"], p["Udp"]
		snap.TCP.OutSegs = rate(t["OutSegs"], pt["OutSegs"], secs)
		snap.TCP.InSegs = rate(t["InSegs"], pt["InSegs"], secs)
		snap.TCP.Retrans = rate(t["RetransSegs"], pt["RetransSegs"], secs)
		snap.TCP.Resets = rate(t["OutRsts"], pt["OutRsts"], secs)
		snap.TCP.Fails = rate(t["AttemptFails"], pt["AttemptFails"], secs)
		snap.TCP.InErrs = rate(t["InErrs"], pt["InErrs"], secs)
		snap.TCP.RetransPct = pct(snap.TCP.Retrans, snap.TCP.OutSegs)
		if u != nil && pu != nil {
			snap.TCP.UDPErrs = rate(u["InErrors"], pu["InErrors"], secs)
			snap.TCP.UDPIn = rate(u["InDatagrams"], pu["InDatagrams"], secs)
			snap.TCP.UDPOut = rate(u["OutDatagrams"], pu["OutDatagrams"], secs)
		}
	}
	s.prevSnmp = cur
}

func (s *linuxSource) procs(snap *Snapshot, secs float64) {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return
	}
	ncpu := float64(max(1, snap.CPU.Count))
	seen := map[int]bool{}
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		dir := "/proc/" + e.Name() + "/"
		st, ok := parsePidStat(readText(dir + "stat"))
		if !ok {
			continue
		}
		p := Proc{PID: pid, Name: st.name, Mem: st.rssPages * s.page, Threads: st.threads, Handles: -1, IO: -1}
		if exe, err := os.Readlink(dir + "exe"); err == nil {
			p.Path = strings.TrimSuffix(exe, " (deleted)")
			p.Name = filepath.Base(p.Path)
		}
		cpu := st.utime + st.stime
		io, ioOK := parseProcIO(readText(dir + "io"))
		pr := s.prevProc[pid]
		if pr != nil && pr.start == st.start && secs > 0 {
			p.CPU = math.Min(100, float64(cpu-min(pr.cpu, cpu))/(secs*clkTck*ncpu)*100)
			if ioOK {
				p.IO = rate(io, pr.io, secs)
			}
		}
		if pr == nil || pr.start != st.start {
			pr = &procPrev{}
			s.prevProc[pid] = pr
		}
		pr.start, pr.cpu, pr.io = st.start, cpu, io
		seen[pid] = true
		snap.Procs = append(snap.Procs, p)
	}
	for k := range s.prevProc {
		if !seen[k] {
			delete(s.prevProc, k)
		}
	}
	if os.Geteuid() != 0 {
		snap.Notes = append(snap.Notes, "Run as root to see I/O and paths for other users' processes.")
	}
}
