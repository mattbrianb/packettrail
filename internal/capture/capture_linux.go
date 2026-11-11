//go:build linux

package capture

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Linux backend, pure Go (no cgo, no eBPF toolchain needed):
//   - TCP bytes: sock_diag netlink dumps every second, reading the kernel's
//     per-socket tcp_info bytes_acked / bytes_received counters. Works without
//     root, but then only your own processes can be mapped to a PID.
//   - Closing TCP sockets (root): sock_diag destroy notifications deliver the
//     final counters, so connections that close between polls are not lost.
//   - UDP bytes and DNS names (root): an AF_PACKET socket with a UDP-only BPF
//     filter; packets are attributed to processes through the UDP socket table.

func IsPrivileged() bool { return os.Geteuid() == 0 }

func newPlatform(opts Options) (Capturer, ProcResolver) {
	procs := &linuxProcs{cache: map[int]procEntry{}}
	priv := IsPrivileged() && !opts.Unprivileged
	return &linuxCapture{procs: procs, priv: priv, socks: map[uint64]*sockState{}, inodes: map[uint32]int{}, stop: make(chan struct{})}, procs
}

const (
	netlinkSockDiag        = 4
	sockDiagByFamily       = 20
	tcpListen              = 10
	tcpTimeWait            = 6
	sknlgrpInetTCPDestroy  = 1
	sknlgrpInet6TCPDestroy = 3
)

type sockState struct {
	acked, received uint64
	retransSegs     uint32
	bytesRetrans    uint64
	gen             uint64
}

type procEntry struct {
	info ProcInfo
	at   time.Time
}

// rawEv carries the socket owner hints until the PID is resolved.
type rawEv struct {
	ev    FlowEvent
	inode uint32
	uid   uint32
}

type linuxCapture struct {
	procs *linuxProcs
	priv  bool
	sink  Sink
	stop  chan struct{}
	wg    sync.WaitGroup
	once  sync.Once

	mu       sync.Mutex
	socks    map[uint64]*sockState
	gen      uint64
	listen   map[uint16]bool
	inodes   map[uint32]int
	inodesAt time.Time
	udpTab   map[netip.AddrPort]diagSock
	udpAt    time.Time

	events, dnsEvents atomic.Uint64
	errMu             sync.Mutex
	errs              []string
	notes             []string
}

func (c *linuxCapture) Start(sink Sink) error {
	c.sink = sink
	if _, err := dumpSockets(unix.AF_INET, unix.IPPROTO_TCP, 1<<1, false); err != nil {
		return fmt.Errorf("sock_diag unavailable: %w", err)
	}
	c.wg.Add(1)
	go c.pollLoop()
	if c.priv {
		if err := c.startDestroyWatch(); err != nil {
			c.addErr("TCP destroy notifications: " + err.Error())
		}
		if err := c.startPacket(); err != nil {
			c.addErr("UDP capture (AF_PACKET): " + err.Error())
		}
	} else {
		c.notes = append(c.notes,
			"Not running as root: UDP is not counted, DNS names are not captured, and sockets owned by other users' processes are shown per user instead of per application.",
			"TCP connections that open and close within one second may be missed.")
	}
	return nil
}

func (c *linuxCapture) Stop() {
	c.once.Do(func() { close(c.stop); c.wg.Wait() })
}

func (c *linuxCapture) addErr(s string) {
	c.errMu.Lock()
	c.errs = append(c.errs, s)
	c.errMu.Unlock()
}

func (c *linuxCapture) Status() Status {
	c.errMu.Lock()
	defer c.errMu.Unlock()
	detail := "sock_diag tcp_info polling (TCP bytes per socket)"
	if c.priv {
		detail += " + TCP destroy notifications + AF_PACKET (UDP bytes, DNS names)"
	}
	return Status{
		Backend: "sockdiag", Mode: "full", Privileged: c.priv, Detail: detail,
		Error: strings.Join(c.errs, "; "), Events: c.events.Load(), DNSEvents: c.dnsEvents.Load(),
		Notes: c.notes,
	}
}

func nlSocket() (int, error) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, netlinkSockDiag)
	if err != nil {
		return -1, err
	}
	tv := unix.Timeval{Sec: 2}
	unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv)
	return fd, nil
}

// dumpSockets runs one SOCK_DIAG_BY_FAMILY dump request.
func dumpSockets(family uint8, proto uint8, states uint32, info bool) ([]diagSock, error) {
	fd, err := nlSocket()
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)
	ne := binary.NativeEndian
	req := make([]byte, 16+56)
	ne.PutUint32(req[0:], uint32(len(req)))
	ne.PutUint16(req[4:], sockDiagByFamily)
	ne.PutUint16(req[6:], unix.NLM_F_REQUEST|unix.NLM_F_DUMP)
	ne.PutUint32(req[8:], 1)
	req[16], req[17] = family, proto
	if info {
		req[18] = 1 << (inetDiagInfo - 1)
	}
	ne.PutUint32(req[20:], states)
	if err := unix.Sendto(fd, req, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return nil, err
	}
	var out []diagSock
	buf := make([]byte, 256*1024)
	for {
		n, _, err := unix.Recvfrom(fd, buf, 0)
		if err != nil {
			return out, err
		}
		msgs, err := syscall.ParseNetlinkMessage(buf[:n])
		if err != nil {
			return out, err
		}
		for _, m := range msgs {
			switch m.Header.Type {
			case unix.NLMSG_DONE:
				return out, nil
			case unix.NLMSG_ERROR:
				if len(m.Data) >= 4 {
					if e := int32(ne.Uint32(m.Data)); e != 0 {
						return out, syscall.Errno(-e)
					}
				}
				return out, nil
			case sockDiagByFamily:
				if s, ok := parseDiagMsg(m.Data); ok {
					out = append(out, s)
				}
			}
		}
	}
}

func (c *linuxCapture) pollLoop() {
	defer c.wg.Done()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	first := true
	for {
		c.pollTCP(first)
		first = false
		select {
		case <-c.stop:
			return
		case <-t.C:
		}
	}
}

func (c *linuxCapture) pollTCP(baseline bool) {
	const states = 0xfff &^ (1 << tcpTimeWait)
	var socks []diagSock
	for _, fam := range []uint8{unix.AF_INET, unix.AF_INET6} {
		if s, err := dumpSockets(fam, unix.IPPROTO_TCP, states, true); err == nil || len(s) > 0 {
			socks = append(socks, s...)
		}
	}
	now := time.Now()
	c.mu.Lock()
	c.gen++
	listen := map[uint16]bool{}
	for _, s := range socks {
		if s.state == tcpListen {
			listen[s.local.Port()] = true
		}
	}
	c.listen = listen
	var raws []rawEv
	for _, s := range socks {
		if s.state == tcpListen || !s.hasInfo {
			continue
		}
		st := c.socks[s.cookie]
		if st == nil {
			st = &sockState{}
			c.socks[s.cookie] = st
			if baseline { // counters of sockets that existed before we started
				st.acked, st.received = s.acked, s.received
				st.retransSegs, st.bytesRetrans = s.retransSegs, s.bytesRetrans
			}
		}
		d := sockDelta(st, s)
		st.acked, st.received, st.retransSegs, st.bytesRetrans, st.gen = s.acked, s.received, s.retransSegs, s.bytesRetrans, c.gen
		if d.in == 0 && d.out == 0 && d.segs == 0 && !baseline {
			continue
		}
		raws = append(raws, c.tcpRaw(s, d, now, time.Second))
	}
	for k, st := range c.socks {
		if st.gen != c.gen {
			delete(c.socks, k)
		}
	}
	c.mu.Unlock()
	c.emit(raws)
}

type delta struct {
	in, out, retrans uint64
	segs             uint32
}

// sockDelta computes counter increases since the last poll (counters only grow).
func sockDelta(st *sockState, s diagSock) delta {
	var d delta
	if s.received >= st.received {
		d.in = s.received - st.received
	}
	if s.acked >= st.acked {
		d.out = s.acked - st.acked
	}
	if s.retransSegs >= st.retransSegs {
		d.segs = s.retransSegs - st.retransSegs
	}
	if s.bytesRetrans >= st.bytesRetrans {
		d.retrans = s.bytesRetrans - st.bytesRetrans
	}
	return d
}

// tcpRaw builds an event for one TCP socket; c.mu must be held.
func (c *linuxCapture) tcpRaw(s diagSock, d delta, now time.Time, span time.Duration) rawEv {
	dir := DirOut
	if c.listen[s.local.Port()] {
		dir = DirIn
	}
	ev := FlowEvent{Time: now, Span: span, Proto: TCP, Local: s.local, Remote: s.remote, Dir: dir, In: d.in, Out: d.out,
		Retrans: d.retrans, RetransPkts: uint64(d.segs)}
	if s.rttUs > 0 {
		ev.RTT, ev.HasRTT = float64(s.rttUs)/1000, true
	}
	return rawEv{ev: ev, inode: s.inode, uid: s.uid}
}

// emit resolves socket inodes to PIDs and hands the events to the sink.
func (c *linuxCapture) emit(raws []rawEv) {
	if len(raws) == 0 {
		return
	}
	c.mu.Lock()
	unknown := false
	for _, r := range raws {
		if r.inode != 0 && c.inodes[r.inode] == 0 {
			unknown = true
			break
		}
	}
	c.mu.Unlock()
	if unknown {
		c.refreshInodes()
	}
	c.mu.Lock()
	evs := make([]FlowEvent, len(raws))
	var n uint64
	for i, r := range raws {
		ev := r.ev
		if pid := c.inodes[r.inode]; pid != 0 {
			ev.PID = pid
		} else if ev.PID == 0 {
			ev.Proc = c.ownerless(r.uid)
		}
		if ev.In|ev.Out != 0 {
			n++
		}
		evs[i] = ev
	}
	c.mu.Unlock()
	c.events.Add(n)
	c.sink.Flows(evs)
}

func (c *linuxCapture) ownerless(uid uint32) *ProcInfo {
	if c.priv {
		return &ProcInfo{Name: "(unknown)", Key: "(unknown)"}
	}
	name := strconv.Itoa(int(uid))
	if u, err := user.LookupId(name); err == nil {
		name = u.Username
	}
	label := "(user " + name + ")"
	return &ProcInfo{Name: label, Key: AppKey(label)}
}

// refreshInodes rebuilds the socket inode -> PID map from /proc (rate limited).
func (c *linuxCapture) refreshInodes() {
	c.mu.Lock()
	if time.Since(c.inodesAt) < time.Second {
		c.mu.Unlock()
		return
	}
	c.inodesAt = time.Now()
	c.mu.Unlock()
	m := map[uint32]int{}
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		dir := "/proc/" + e.Name() + "/fd"
		fds, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			link, err := os.Readlink(dir + "/" + fd.Name())
			if err != nil || !strings.HasPrefix(link, "socket:[") {
				continue
			}
			if ino, err := strconv.ParseUint(link[8:len(link)-1], 10, 32); err == nil {
				m[uint32(ino)] = pid
			}
		}
	}
	c.mu.Lock()
	c.inodes = m
	c.mu.Unlock()
}

// startDestroyWatch subscribes to TCP socket destroy notifications, which
// carry the final tcp_info counters of each closing socket.
func (c *linuxCapture) startDestroyWatch() error {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, netlinkSockDiag)
	if err != nil {
		return err
	}
	groups := uint32(1<<(sknlgrpInetTCPDestroy-1) | 1<<(sknlgrpInet6TCPDestroy-1))
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK, Groups: groups}); err != nil {
		unix.Close(fd)
		return err
	}
	tv := unix.Timeval{Sec: 1}
	unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv)
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer unix.Close(fd)
		buf := make([]byte, 64*1024)
		for {
			select {
			case <-c.stop:
				return
			default:
			}
			n, _, err := unix.Recvfrom(fd, buf, 0)
			if err != nil {
				continue
			}
			msgs, err := syscall.ParseNetlinkMessage(buf[:n])
			if err != nil {
				continue
			}
			now := time.Now()
			var raws []rawEv
			c.mu.Lock()
			for _, m := range msgs {
				s, ok := parseDiagMsg(m.Data)
				if !ok || !s.hasInfo {
					continue
				}
				st := c.socks[s.cookie]
				delete(c.socks, s.cookie)
				var base sockState
				if st != nil {
					base = *st
				}
				if d := sockDelta(&base, s); d.in|d.out|d.retrans != 0 || d.segs != 0 {
					raws = append(raws, c.tcpRaw(s, d, now, 0))
				}
			}
			c.mu.Unlock()
			c.emit(raws)
		}
	}()
	return nil
}

// --- UDP via AF_PACKET -------------------------------------------------------

type udpKey struct {
	local, remote netip.AddrPort
}

func htons(v uint16) uint16 { return v<<8 | v>>8 }

func (c *linuxCapture) startPacket() error {
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, int(htons(unix.ETH_P_ALL)))
	if err != nil {
		return err
	}
	// Accept only UDP over IPv4/IPv6 (cooked socket: data starts at the IP header).
	filter := []unix.SockFilter{
		{Code: 0x28, K: 0xfffff000},           // ld proto (ancillary SKF_AD_PROTOCOL)
		{Code: 0x15, Jt: 0, Jf: 2, K: 0x0800}, // jeq IPv4
		{Code: 0x30, K: 9},                    // ldb [9] (IPv4 protocol)
		{Code: 0x15, Jt: 3, Jf: 4, K: 17},     // jeq UDP -> accept : drop
		{Code: 0x15, Jt: 0, Jf: 3, K: 0x86dd}, // jeq IPv6
		{Code: 0x30, K: 6},                    // ldb [6] (IPv6 next header)
		{Code: 0x15, Jt: 0, Jf: 1, K: 17},     // jeq UDP
		{Code: 0x06, K: 0xffff},               // accept
		{Code: 0x06, K: 0},                    // drop
	}
	prog := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	if err := unix.SetsockoptSockFprog(fd, unix.SOL_SOCKET, unix.SO_ATTACH_FILTER, &prog); err != nil {
		unix.Close(fd)
		return err
	}
	tv := unix.Timeval{Usec: 250000}
	unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv)
	unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_RCVBUF, 4<<20)
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer unix.Close(fd)
		buf := make([]byte, 65536)
		acc := map[udpKey]*[2]uint64{}
		var dns []DNSEvent
		var dnsPorts []netip.AddrPort
		last := time.Now()
		for {
			select {
			case <-c.stop:
				return
			default:
			}
			n, from, err := unix.Recvfrom(fd, buf, unix.MSG_TRUNC)
			if err == nil && n > 0 {
				ll, _ := from.(*unix.SockaddrLinklayer)
				if n > len(buf) {
					n = len(buf)
				}
				if ll != nil && ll.Pkttype != unix.PACKET_OTHERHOST {
					c.onUDPPacket(buf[:n], ll.Pkttype == unix.PACKET_OUTGOING, acc, &dns, &dnsPorts)
				}
			}
			if now := time.Now(); now.Sub(last) >= time.Second {
				c.flushUDP(acc, dns, dnsPorts, now, now.Sub(last))
				acc = map[udpKey]*[2]uint64{}
				dns, dnsPorts = nil, nil
				last = now
			}
		}
	}()
	return nil
}

func (c *linuxCapture) onUDPPacket(p []byte, outgoing bool, acc map[udpKey]*[2]uint64, dns *[]DNSEvent, dnsPorts *[]netip.AddrPort) {
	var src, dst netip.Addr
	var u []byte
	if len(p) < 28 {
		return
	}
	switch p[0] >> 4 {
	case 4:
		ihl := int(p[0]&0x0f) * 4
		if len(p) < ihl+8 || p[9] != 17 || binary.BigEndian.Uint16(p[6:])&0x1fff != 0 {
			return
		}
		src, dst = netip.AddrFrom4([4]byte(p[12:16])), netip.AddrFrom4([4]byte(p[16:20]))
		u = p[ihl:]
	case 6:
		if len(p) < 48 || p[6] != 17 {
			return
		}
		src, dst = netip.AddrFrom16([16]byte(p[8:24])).Unmap(), netip.AddrFrom16([16]byte(p[24:40])).Unmap()
		u = p[40:]
	default:
		return
	}
	sport, dport := binary.BigEndian.Uint16(u[0:]), binary.BigEndian.Uint16(u[2:])
	ulen := int(binary.BigEndian.Uint16(u[4:]))
	if ulen < 8 {
		return
	}
	payload := uint64(ulen - 8)
	k := udpKey{netip.AddrPortFrom(src, sport), netip.AddrPortFrom(dst, dport)}
	if !outgoing {
		k = udpKey{netip.AddrPortFrom(dst, dport), netip.AddrPortFrom(src, sport)}
	}
	a := acc[k]
	if a == nil {
		a = &[2]uint64{}
		acc[k] = a
	}
	if outgoing {
		a[1] += payload
	} else {
		a[0] += payload
		if sport == 53 && len(u) > 8 {
			if ev, ok := parseDNSResponse(u[8:]); ok {
				*dns = append(*dns, ev)
				*dnsPorts = append(*dnsPorts, k.local)
			}
		}
	}
}

func (c *linuxCapture) udpTable(force bool) map[netip.AddrPort]diagSock {
	c.mu.Lock()
	tab, at := c.udpTab, c.udpAt
	c.mu.Unlock()
	if tab != nil && (time.Since(at) < 5*time.Second && !force || time.Since(at) < time.Second) {
		return tab
	}
	tab = map[netip.AddrPort]diagSock{}
	for _, fam := range []uint8{unix.AF_INET, unix.AF_INET6} {
		socks, _ := dumpSockets(fam, unix.IPPROTO_UDP, 0xffffffff, false)
		for _, s := range socks {
			tab[s.local] = s
		}
	}
	c.mu.Lock()
	c.udpTab, c.udpAt = tab, time.Now()
	c.mu.Unlock()
	return tab
}

func lookupUDP(tab map[netip.AddrPort]diagSock, local netip.AddrPort) (diagSock, bool) {
	if s, ok := tab[local]; ok {
		return s, true
	}
	for _, any := range []netip.Addr{netip.IPv4Unspecified(), netip.IPv6Unspecified()} {
		if s, ok := tab[netip.AddrPortFrom(any, local.Port())]; ok {
			return s, true
		}
	}
	return diagSock{}, false
}

func (c *linuxCapture) flushUDP(acc map[udpKey]*[2]uint64, dns []DNSEvent, dnsPorts []netip.AddrPort, now time.Time, span time.Duration) {
	if len(acc) == 0 && len(dns) == 0 {
		return
	}
	tab := c.udpTable(false)
	missing := false
	for k := range acc {
		if _, ok := lookupUDP(tab, k.local); !ok {
			missing = true
			break
		}
	}
	if missing {
		tab = c.udpTable(true)
	}
	var raws []rawEv
	for k, v := range acc {
		s, _ := lookupUDP(tab, k.local)
		raws = append(raws, rawEv{
			ev:    FlowEvent{Time: now, Span: span, Proto: UDP, Local: k.local, Remote: k.remote, In: v[0], Out: v[1]},
			inode: s.inode, uid: s.uid,
		})
	}
	c.emit(raws)
	for i, ev := range dns {
		if s, ok := lookupUDP(tab, dnsPorts[i]); ok {
			c.mu.Lock()
			ev.PID = c.inodes[s.inode]
			c.mu.Unlock()
		}
		c.dnsEvents.Add(1)
		c.sink.DNS(ev)
	}
}

// --- processes -----------------------------------------------------------------

type linuxProcs struct {
	mu    sync.Mutex
	cache map[int]procEntry
}

func (p *linuxProcs) Lookup(pid int) ProcInfo {
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.cache[pid]; ok && now.Sub(e.at) < 30*time.Second {
		return e.info
	}
	info := resolveLinuxProc(pid)
	if len(p.cache) > 20000 {
		clear(p.cache)
	}
	p.cache[pid] = procEntry{info, now}
	return info
}

func resolveLinuxProc(pid int) ProcInfo {
	if pid <= 0 {
		return ProcInfo{Name: "(unknown)", Key: "(unknown)"}
	}
	base := "/proc/" + strconv.Itoa(pid)
	path, _ := os.Readlink(base + "/exe")
	path = strings.TrimSuffix(path, " (deleted)")
	name := ""
	if path != "" {
		name = filepath.Base(path)
	} else if b, err := os.ReadFile(base + "/comm"); err == nil {
		name = strings.TrimSpace(string(b))
	}
	if name == "" {
		return ProcInfo{PID: pid, Name: "(exited pid " + strconv.Itoa(pid) + ")", Key: "(unknown)"}
	}
	return ProcInfo{PID: pid, Name: name, Path: path, Key: AppKey(name)}
}
