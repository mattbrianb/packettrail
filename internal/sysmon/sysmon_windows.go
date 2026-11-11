//go:build windows

package sysmon

import (
	"encoding/binary"
	"fmt"
	"math"
	"net"
	"sort"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modKernel32                  = windows.NewLazySystemDLL("kernel32.dll")
	procGlobalMemoryStatusEx     = modKernel32.NewProc("GlobalMemoryStatusEx")
	procGetTickCount64           = modKernel32.NewProc("GetTickCount64")
	modNtdll                     = windows.NewLazySystemDLL("ntdll.dll")
	procNtQuerySystemInformation = modNtdll.NewProc("NtQuerySystemInformation")
	modIphlpapi                  = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetIfTable2              = modIphlpapi.NewProc("GetIfTable2")
	procFreeMibTable             = modIphlpapi.NewProc("FreeMibTable")
	procGetTcpStatisticsEx       = modIphlpapi.NewProc("GetTcpStatisticsEx")
	procGetUdpStatisticsEx       = modIphlpapi.NewProc("GetUdpStatisticsEx")
	modPdh                       = windows.NewLazySystemDLL("pdh.dll")
	procPdhOpenQueryW            = modPdh.NewProc("PdhOpenQueryW")
	procPdhAddEnglishCounterW    = modPdh.NewProc("PdhAddEnglishCounterW")
	procPdhCollectQueryData      = modPdh.NewProc("PdhCollectQueryData")
	procPdhGetFormattedCounter   = modPdh.NewProc("PdhGetFormattedCounterValue")
	procPdhCloseQuery            = modPdh.NewProc("PdhCloseQuery")
)

const (
	sysProcessInformation   = 5
	sysProcessorPerformance = 8
	statusInfoLenMismatch   = 0xC0000004

	cpuInfoSize = 48 // SYSTEM_PROCESSOR_PERFORMANCE_INFORMATION

	// SYSTEM_PROCESS_INFORMATION (x64)
	spiNext       = 0
	spiThreads    = 4
	spiPrivateWS  = 8
	spiCreateTime = 32
	spiUserTime   = 40
	spiKernelTime = 48
	spiNameLen    = 56
	spiNameBuf    = 64
	spiPID        = 80
	spiHandles    = 96
	spiReadXfer   = 232
	spiWriteXfer  = 240
	spiOtherXfer  = 248

	// MIB_IF_ROW2 (x64)
	ifRowSize    = 1352
	ifTableFirst = 8
	ifLUID       = 0
	ifAlias      = 28
	ifDesc       = 542
	ifMACLen     = 1056
	ifMAC        = 1060
	ifMTU        = 1124
	ifType       = 1128
	ifFlags      = 1152
	ifOper       = 1156
	ifTxSpeed    = 1192
	ifRxSpeed    = 1200
	ifInOctets   = 1208
	ifInDiscards = 1232
	ifInErrors   = 1240
	ifOutOctets  = 1280
	ifOutDiscard = 1304
	ifOutErrors  = 1312

	pdhFmtDouble = 0x00000200
	pdhNoCap100  = 0x00008000
)

type cpuTimes struct{ idle, kernel, user, dpc, irq uint64 }

type ifPrev struct{ in, out uint64 }

type winSource struct {
	prevAt    time.Time
	prevCPU   []cpuTimes
	prevIf    map[uint64]ifPrev
	prevProc  map[int]*procPrev
	prevTCP   [6]uint32 // out, in, retrans, resets, fails, inErrs (v4+v6 summed)
	prevUDP   [3]uint32 // in errors, in, out
	haveStack bool
	paths     map[[2]uint64]string // (pid, create time) -> image path
	pdh       uintptr
	pdhC      [5]uintptr // read B/s, write B/s, % idle, s/transfer, queue length
	pdhOK     bool
	buf       []byte
}

func newSource() source {
	s := &winSource{prevIf: map[uint64]ifPrev{}, prevProc: map[int]*procPrev{}, paths: map[[2]uint64]string{}}
	s.openPDH()
	return s
}

func (s *winSource) openPDH() {
	var q uintptr
	if r, _, _ := procPdhOpenQueryW.Call(0, 0, uintptr(unsafe.Pointer(&q))); r != 0 {
		return
	}
	names := []string{
		`\PhysicalDisk(_Total)\Disk Read Bytes/sec`,
		`\PhysicalDisk(_Total)\Disk Write Bytes/sec`,
		`\PhysicalDisk(_Total)\% Idle Time`,
		`\PhysicalDisk(_Total)\Avg. Disk sec/Transfer`,
		`\PhysicalDisk(_Total)\Current Disk Queue Length`,
	}
	for i, n := range names {
		p, _ := windows.UTF16PtrFromString(n)
		if r, _, _ := procPdhAddEnglishCounterW.Call(q, uintptr(unsafe.Pointer(p)), 0, uintptr(unsafe.Pointer(&s.pdhC[i]))); r != 0 {
			procPdhCloseQuery.Call(q)
			return
		}
	}
	procPdhCollectQueryData.Call(q)
	s.pdh, s.pdhOK = q, true
}

func (s *winSource) close() {
	if s.pdhOK {
		procPdhCloseQuery.Call(s.pdh)
		s.pdhOK = false
	}
}

func (s *winSource) pdhValue(c uintptr) (float64, bool) {
	var v struct {
		status uint32
		_      uint32
		value  float64
	}
	if r, _, _ := procPdhGetFormattedCounter.Call(c, pdhFmtDouble|pdhNoCap100, 0, uintptr(unsafe.Pointer(&v))); r != 0 {
		return 0, false
	}
	return v.value, true
}

// ntQuery calls NtQuerySystemInformation, growing the buffer as needed.
func (s *winSource) ntQuery(class uintptr) []byte {
	if len(s.buf) == 0 {
		s.buf = make([]byte, 512<<10)
	}
	for {
		var need uint32
		r, _, _ := procNtQuerySystemInformation.Call(class, uintptr(unsafe.Pointer(&s.buf[0])), uintptr(len(s.buf)), uintptr(unsafe.Pointer(&need)))
		if r == 0 {
			return s.buf[:need]
		}
		if r != statusInfoLenMismatch || len(s.buf) >= 64<<20 {
			return nil
		}
		n := len(s.buf) * 2
		if int(need) > n {
			n = int(need) + 64<<10
		}
		s.buf = make([]byte, n)
	}
}

func le64(b []byte, off int) uint64 { return binary.LittleEndian.Uint64(b[off:]) }
func le32(b []byte, off int) uint32 { return binary.LittleEndian.Uint32(b[off:]) }

func (s *winSource) sample(now time.Time) *Snapshot {
	secs := 0.0
	if !s.prevAt.IsZero() {
		secs = now.Sub(s.prevAt).Seconds()
	}
	s.prevAt = now
	snap := &Snapshot{Elapsed: secs}
	if r, _, _ := procGetTickCount64.Call(); r != 0 {
		snap.Uptime = int64(r / 1000)
	}
	s.cpu(snap)
	s.memory(snap)
	snap.Disks = disks()
	s.diskIO(snap)
	s.ifaces(snap, secs)
	s.stack(snap, secs)
	s.procs(snap, secs)
	return snap
}

func (s *winSource) cpu(snap *Snapshot) {
	// This class needs a buffer that is an exact multiple of the entry size,
	// and silently truncates to it, so ask for room for 256 processors.
	b := make([]byte, cpuInfoSize*256)
	var need uint32
	if r, _, _ := procNtQuerySystemInformation.Call(sysProcessorPerformance, uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)), uintptr(unsafe.Pointer(&need))); r != 0 {
		return
	}
	n := int(need) / cpuInfoSize
	cur := make([]cpuTimes, n)
	for i := range cur {
		o := i * cpuInfoSize
		cur[i] = cpuTimes{idle: le64(b, o), kernel: le64(b, o+8), user: le64(b, o+16), dpc: le64(b, o+24), irq: le64(b, o+32)}
	}
	snap.CPU.Count, snap.CPU.HasDPC = n, true
	if len(s.prevCPU) == n {
		var tot, idle, user, kern, dpc, irq float64
		for i, c := range cur {
			p := s.prevCPU[i]
			ct := float64((c.kernel - p.kernel) + (c.user - p.user))
			ci := float64(c.idle - p.idle)
			snap.CPU.Cores = append(snap.CPU.Cores, math.Round(pct(ct-ci, ct)*10)/10)
			tot += ct
			idle += ci
			user += float64(c.user - p.user)
			kern += float64(c.kernel-p.kernel) - ci
			dpc += float64(c.dpc - p.dpc)
			irq += float64(c.irq - p.irq)
		}
		snap.CPU.Busy = pct(tot-idle, tot)
		snap.CPU.User = pct(user, tot)
		snap.CPU.System = pct(kern, tot)
		snap.CPU.DPC = pct(dpc, tot)
		snap.CPU.IRQ = pct(irq, tot)
	}
	s.prevCPU = cur
}

func (s *winSource) memory(snap *Snapshot) {
	var m struct {
		length, load                                     uint32
		totalPhys, availPhys, totalPage, availPage       uint64
		totalVirtual, availVirtual, availExtendedVirtual uint64
	}
	m.length = uint32(unsafe.Sizeof(m))
	if r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&m))); r == 0 {
		return
	}
	snap.Mem = Mem{Total: m.totalPhys, Avail: m.availPhys, Used: m.totalPhys - m.availPhys,
		Pct: pct(float64(m.totalPhys-m.availPhys), float64(m.totalPhys)), CommitLimit: m.totalPage, CommitUsed: m.totalPage - m.availPage}
}

func disks() []Disk {
	buf := make([]uint16, 512)
	n, err := windows.GetLogicalDriveStrings(uint32(len(buf)), &buf[0])
	if err != nil || n == 0 {
		return nil
	}
	var out []Disk
	// The buffer is a list of NUL-terminated strings ending with an empty one.
	start := 0
	for i := 0; i < int(n); i++ {
		if buf[i] != 0 {
			continue
		}
		root := windows.UTF16ToString(buf[start:i])
		start = i + 1
		if root == "" {
			continue
		}
		rp, _ := windows.UTF16PtrFromString(root)
		if windows.GetDriveType(rp) != windows.DRIVE_FIXED {
			continue
		}
		var avail, total, free uint64
		if windows.GetDiskFreeSpaceEx(rp, &avail, &total, &free) != nil || total == 0 {
			continue
		}
		label, fs := make([]uint16, 261), make([]uint16, 261)
		windows.GetVolumeInformation(rp, &label[0], uint32(len(label)), nil, nil, nil, &fs[0], uint32(len(fs)))
		out = append(out, Disk{Mount: strings.TrimSuffix(root, `\`), Label: windows.UTF16ToString(label), FS: windows.UTF16ToString(fs),
			Total: total, Free: avail, Pct: pct(float64(total-avail), float64(total))})
	}
	return out
}

func (s *winSource) diskIO(snap *Snapshot) {
	snap.DiskIO = DiskIO{Busy: -1, Latency: -1, QueueLen: -1}
	if !s.pdhOK {
		snap.Notes = append(snap.Notes, "Disk activity counters are unavailable on this system.")
		return
	}
	if r, _, _ := procPdhCollectQueryData.Call(s.pdh); r != 0 {
		return
	}
	rd, ok1 := s.pdhValue(s.pdhC[0])
	wr, ok2 := s.pdhValue(s.pdhC[1])
	if !ok1 || !ok2 {
		return
	}
	snap.DiskIO.Has, snap.DiskIO.Split, snap.DiskIO.Read, snap.DiskIO.Write = true, true, rd, wr
	if idle, ok := s.pdhValue(s.pdhC[2]); ok {
		snap.DiskIO.Busy = math.Max(0, math.Min(100, 100-idle))
	}
	if lat, ok := s.pdhValue(s.pdhC[3]); ok {
		snap.DiskIO.Latency = lat * 1000
	}
	if q, ok := s.pdhValue(s.pdhC[4]); ok {
		snap.DiskIO.QueueLen = q
	}
}

func ifKind(typ uint32, desc string) string {
	d := strings.ToLower(desc)
	switch {
	case typ == 71:
		return "wifi"
	case typ == 243 || typ == 244:
		return "cellular"
	case typ == 53 || typ == 131 || typ == 23 || strings.Contains(d, "vpn") || strings.Contains(d, "wireguard") ||
		strings.Contains(d, "tap-") || strings.Contains(d, "tunnel") || strings.Contains(d, "openvpn") || strings.Contains(d, "anyconnect") ||
		strings.Contains(d, "fortinet") || strings.Contains(d, "zerotier") || strings.Contains(d, "tailscale"):
		return "vpn"
	case strings.Contains(d, "hyper-v") || strings.Contains(d, "virtual") || strings.Contains(d, "vmware") || strings.Contains(d, "virtualbox") || strings.Contains(d, "wsl"):
		return "virtual"
	case typ == 6:
		return "ethernet"
	default:
		return "other"
	}
}

func utf16At(b []byte, off, maxChars int) string {
	u := make([]uint16, 0, 64)
	for i := 0; i < maxChars; i++ {
		c := binary.LittleEndian.Uint16(b[off+2*i:])
		if c == 0 {
			break
		}
		u = append(u, c)
	}
	return windows.UTF16ToString(u)
}

func (s *winSource) ifaces(snap *Snapshot, secs float64) {
	var tbl unsafe.Pointer
	if r, _, _ := procGetIfTable2.Call(uintptr(unsafe.Pointer(&tbl))); r != 0 || tbl == nil {
		return
	}
	defer procFreeMibTable.Call(uintptr(tbl))
	n := int(*(*uint32)(tbl))
	if n <= 0 || n > 4096 {
		return
	}
	all := unsafe.Slice((*byte)(tbl), ifTableFirst+n*ifRowSize)
	seen := map[uint64]bool{}
	for i := 0; i < n; i++ {
		row := all[ifTableFirst+i*ifRowSize : ifTableFirst+(i+1)*ifRowSize]
		flags := row[ifFlags]
		hardware, filter := flags&1 != 0, flags&2 != 0
		typ := le32(row, ifType)
		up := le32(row, ifOper) == 1
		in, out := le64(row, ifInOctets), le64(row, ifOutOctets)
		if filter || typ == 24 || (!hardware && (!up || in+out == 0)) {
			continue
		}
		luid := le64(row, ifLUID)
		seen[luid] = true
		desc := utf16At(row, ifDesc, 257)
		f := Iface{ID: fmt.Sprintf("%x", luid), Name: utf16At(row, ifAlias, 257), Desc: desc, Kind: ifKind(typ, desc),
			Physical: hardware, Up: up, MTU: int(le32(row, ifMTU)), RxBytes: in, TxBytes: out,
			Errs: le64(row, ifInErrors) + le64(row, ifOutErrors), Discards: le64(row, ifInDiscards) + le64(row, ifOutDiscard)}
		if ml := int(le32(row, ifMACLen)); ml > 0 && ml <= 8 {
			f.MAC = net.HardwareAddr(row[ifMAC : ifMAC+ml]).String()
		}
		if up {
			rx, tx := le64(row, ifRxSpeed), le64(row, ifTxSpeed)
			if rx > 0 && rx < 1e13 {
				f.Speed = rx
			}
			if tx > 0 && tx < 1e13 {
				f.SpeedTx = tx
			}
		}
		if p, ok := s.prevIf[luid]; ok {
			f.Rx, f.Tx = rate(in, p.in, secs), rate(out, p.out, secs)
		}
		f.RxUtil, f.TxUtil = utilOf(f.Rx, f.Speed), utilOf(f.Tx, f.SpeedTx)
		s.prevIf[luid] = ifPrev{in, out}
		snap.Ifaces = append(snap.Ifaces, f)
	}
	for k := range s.prevIf {
		if !seen[k] {
			delete(s.prevIf, k)
		}
	}
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

func (s *winSource) stack(snap *Snapshot, secs float64) {
	var cur [6]uint32
	var estab int
	ok := false
	for _, fam := range []uintptr{windows.AF_INET, windows.AF_INET6} {
		var t [15]uint32 // MIB_TCPSTATS
		if r, _, _ := procGetTcpStatisticsEx.Call(uintptr(unsafe.Pointer(&t)), fam); r != 0 {
			continue
		}
		ok = true
		cur[0] += t[10] // OutSegs
		cur[1] += t[9]  // InSegs
		cur[2] += t[11] // RetransSegs
		cur[3] += t[13] // OutRsts
		cur[4] += t[6]  // AttemptFails
		cur[5] += t[12] // InErrs
		estab += int(t[8])
	}
	var udp [3]uint32
	for _, fam := range []uintptr{windows.AF_INET, windows.AF_INET6} {
		var u [5]uint32 // MIB_UDPSTATS
		if r, _, _ := procGetUdpStatisticsEx.Call(uintptr(unsafe.Pointer(&u)), fam); r == 0 {
			udp[0] += u[2]
			udp[1] += u[0]
			udp[2] += u[3]
		}
	}
	if !ok {
		return
	}
	snap.TCP = TCP{Has: true, Estab: estab, RetransPct1: -1}
	if s.haveStack {
		p := s.prevTCP
		snap.TCP.OutSegs = rate32(cur[0], p[0], secs)
		snap.TCP.InSegs = rate32(cur[1], p[1], secs)
		snap.TCP.Retrans = rate32(cur[2], p[2], secs)
		snap.TCP.Resets = rate32(cur[3], p[3], secs)
		snap.TCP.Fails = rate32(cur[4], p[4], secs)
		snap.TCP.InErrs = rate32(cur[5], p[5], secs)
		snap.TCP.RetransPct = pct(snap.TCP.Retrans, snap.TCP.OutSegs)
		snap.TCP.UDPErrs = rate32(udp[0], s.prevUDP[0], secs)
		snap.TCP.UDPIn = rate32(udp[1], s.prevUDP[1], secs)
		snap.TCP.UDPOut = rate32(udp[2], s.prevUDP[2], secs)
	}
	s.prevTCP, s.prevUDP, s.haveStack = cur, udp, true
}

func (s *winSource) procs(snap *Snapshot, secs float64) {
	b := s.ntQuery(sysProcessInformation)
	if len(b) < 256 {
		return
	}
	ncpu := float64(max(1, snap.CPU.Count))
	base := uintptr(unsafe.Pointer(&b[0]))
	seen := map[int]bool{}
	livePaths := map[[2]uint64]bool{}
	for off := 0; off+256 <= len(b); {
		e := b[off:]
		pid := int(le64(e, spiPID))
		if pid != 0 {
			created := le64(e, spiCreateTime)
			cpu := le64(e, spiUserTime) + le64(e, spiKernelTime)
			io := le64(e, spiReadXfer) + le64(e, spiWriteXfer) + le64(e, spiOtherXfer)
			name := "System Idle"
			// The name buffer points back into our own buffer.
			nl := int(binary.LittleEndian.Uint16(e[spiNameLen:]))
			if no := int(uintptr(le64(e, spiNameBuf)) - base); nl > 0 && no >= 0 && no+nl <= len(b) {
				u := make([]uint16, nl/2)
				for i := range u {
					u[i] = binary.LittleEndian.Uint16(b[no+2*i:])
				}
				name = windows.UTF16ToString(u)
			} else if pid == 4 {
				name = "System"
			}
			p := Proc{PID: pid, Name: name, Mem: le64(e, spiPrivateWS), Threads: int(le32(e, spiThreads)),
				Handles: int(le32(e, spiHandles)), IO: -1}
			if pr := s.prevProc[pid]; pr != nil && pr.start == created && secs > 0 {
				p.CPU = math.Min(100, float64(cpu-pr.cpu)/(secs*1e7*ncpu)*100)
				p.IO = rate(io, pr.io, secs)
			}
			if pr := s.prevProc[pid]; pr == nil || pr.start != created {
				s.prevProc[pid] = &procPrev{}
			}
			pr := s.prevProc[pid]
			pr.start, pr.cpu, pr.io, pr.seen = created, cpu, io, true
			seen[pid] = true
			key := [2]uint64{uint64(pid), created}
			livePaths[key] = true
			path, ok := s.paths[key]
			if !ok {
				path = imagePath(pid)
				s.paths[key] = path
			}
			p.Path = path
			snap.Procs = append(snap.Procs, p)
		}
		next := int(le32(e, spiNext))
		if next == 0 {
			break
		}
		off += next
	}
	for k := range s.prevProc {
		if !seen[k] {
			delete(s.prevProc, k)
		}
	}
	for k := range s.paths {
		if !livePaths[k] {
			delete(s.paths, k)
		}
	}
}

func imagePath(pid int) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}
