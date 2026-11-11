//go:build windows

package capture

import (
	"encoding/binary"
	"net/netip"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modIphlpapi             = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetExtendedTcpTable = modIphlpapi.NewProc("GetExtendedTcpTable")
	procGetExtendedUdpTable = modIphlpapi.NewProc("GetExtendedUdpTable")
)

const (
	tcpTableOwnerPIDAll = 5
	udpTableOwnerPID    = 1
	mibTCPStateListen   = 2
	mibTCPStateTimeWait = 11
	errInsufficientBuf  = 122
)

// ConnRow is one row of the OS TCP/UDP socket tables.
type ConnRow struct {
	Proto  Proto
	Local  netip.AddrPort
	Remote netip.AddrPort
	State  uint32
	PID    int
}

func getTable(proc *windows.LazyProc, af, class uint32) ([]byte, error) {
	size := uint32(32 * 1024)
	for i := 0; i < 8; i++ {
		buf := make([]byte, size)
		r, _, _ := proc.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0, uintptr(af), uintptr(class), 0)
		if r == 0 {
			return buf, nil
		}
		if r != errInsufficientBuf {
			return nil, syscall.Errno(r)
		}
		size += 8 * 1024
	}
	return nil, syscall.Errno(errInsufficientBuf)
}

// ReadConnTables returns all TCP and UDP sockets with owning PIDs. It works
// without administrator rights.
func ReadConnTables(withUDP bool) ([]ConnRow, error) {
	var rows []ConnRow
	le, be := binary.LittleEndian, binary.BigEndian
	b, err := getTable(procGetExtendedTcpTable, windows.AF_INET, tcpTableOwnerPIDAll)
	if err != nil {
		return nil, err
	}
	n := int(le.Uint32(b))
	for i := 0; i < n; i++ {
		o := 4 + i*24
		if o+24 > len(b) {
			break
		}
		r := b[o : o+24]
		rows = append(rows, ConnRow{
			Proto:  TCP,
			State:  le.Uint32(r[0:]),
			Local:  netip.AddrPortFrom(netip.AddrFrom4([4]byte(r[4:8])), be.Uint16(r[8:])),
			Remote: netip.AddrPortFrom(netip.AddrFrom4([4]byte(r[12:16])), be.Uint16(r[16:])),
			PID:    int(le.Uint32(r[20:])),
		})
	}
	if b, err = getTable(procGetExtendedTcpTable, windows.AF_INET6, tcpTableOwnerPIDAll); err == nil {
		n = int(le.Uint32(b))
		for i := 0; i < n; i++ {
			o := 4 + i*56
			if o+56 > len(b) {
				break
			}
			r := b[o : o+56]
			rows = append(rows, ConnRow{
				Proto:  TCP,
				Local:  netip.AddrPortFrom(netip.AddrFrom16([16]byte(r[0:16])).Unmap(), be.Uint16(r[20:])),
				Remote: netip.AddrPortFrom(netip.AddrFrom16([16]byte(r[24:40])).Unmap(), be.Uint16(r[44:])),
				State:  le.Uint32(r[48:]),
				PID:    int(le.Uint32(r[52:])),
			})
		}
	}
	if !withUDP {
		return rows, nil
	}
	if b, err = getTable(procGetExtendedUdpTable, windows.AF_INET, udpTableOwnerPID); err == nil {
		n = int(le.Uint32(b))
		for i := 0; i < n; i++ {
			o := 4 + i*12
			if o+12 > len(b) {
				break
			}
			r := b[o : o+12]
			rows = append(rows, ConnRow{
				Proto: UDP,
				Local: netip.AddrPortFrom(netip.AddrFrom4([4]byte(r[0:4])), be.Uint16(r[4:])),
				PID:   int(le.Uint32(r[8:])),
			})
		}
	}
	if b, err = getTable(procGetExtendedUdpTable, windows.AF_INET6, udpTableOwnerPID); err == nil {
		n = int(le.Uint32(b))
		for i := 0; i < n; i++ {
			o := 4 + i*28
			if o+28 > len(b) {
				break
			}
			r := b[o : o+28]
			rows = append(rows, ConnRow{
				Proto: UDP,
				Local: netip.AddrPortFrom(netip.AddrFrom16([16]byte(r[0:16])).Unmap(), be.Uint16(r[20:])),
				PID:   int(le.Uint32(r[24:])),
			})
		}
	}
	return rows, nil
}

type listenSet struct {
	tcp map[uint16]bool
}

func readListenSet() (*listenSet, error) {
	rows, err := ReadConnTables(false)
	if err != nil {
		return nil, err
	}
	ls := &listenSet{tcp: map[uint16]bool{}}
	for _, r := range rows {
		if r.State == mibTCPStateListen {
			ls.tcp[r.Local.Port()] = true
		}
	}
	return ls, nil
}

// connCapture is the non-elevated fallback: it polls the TCP tables and
// reports which processes talk to which remote endpoints, without byte counts.
type connCapture struct {
	reason string
	sink   Sink
	stop   chan struct{}
	done   chan struct{}
	once   sync.Once
	polls  atomic.Uint64
	mu     sync.Mutex
	err    string
}

func newConnCapture(reason string) *connCapture {
	return &connCapture{reason: reason, stop: make(chan struct{}), done: make(chan struct{})}
}

func (c *connCapture) Start(sink Sink) error {
	c.sink = sink
	if _, err := ReadConnTables(false); err != nil {
		return err
	}
	go func() {
		defer close(c.done)
		t := time.NewTicker(2 * time.Second)
		dt := time.NewTicker(20 * time.Second)
		defer t.Stop()
		defer dt.Stop()
		pollDNSCache(sink)
		c.poll()
		for {
			select {
			case <-c.stop:
				return
			case <-t.C:
				c.poll()
			case <-dt.C:
				pollDNSCache(sink)
			}
		}
	}()
	return nil
}

func (c *connCapture) poll() {
	rows, err := ReadConnTables(false)
	c.mu.Lock()
	if err != nil {
		c.err = err.Error()
	} else {
		c.err = ""
	}
	c.mu.Unlock()
	if err != nil {
		return
	}
	c.polls.Add(1)
	listen := map[uint16]bool{}
	for _, r := range rows {
		if r.State == mibTCPStateListen {
			listen[r.Local.Port()] = true
		}
	}
	now := time.Now()
	evs := make([]FlowEvent, 0, len(rows))
	for _, r := range rows {
		if r.State == mibTCPStateListen || r.State == mibTCPStateTimeWait || r.PID == 0 {
			continue
		}
		if !r.Remote.Addr().IsValid() || r.Remote.Addr().IsUnspecified() {
			continue
		}
		dir := DirOut
		if listen[r.Local.Port()] {
			dir = DirIn
		}
		evs = append(evs, FlowEvent{Time: now, PID: r.PID, Proto: TCP, Local: r.Local, Remote: r.Remote, Dir: dir})
	}
	if len(evs) > 0 {
		c.sink.Flows(evs)
	}
}

func (c *connCapture) Stop() {
	c.once.Do(func() {
		close(c.stop)
		<-c.done
	})
}

func (c *connCapture) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Status{
		Backend: "conntable", Mode: "connections", Privileged: false,
		Detail: "TCP connection table polling (GetExtendedTcpTable). No byte counts and no UDP: " + c.reason,
		Error:  c.err,
		Events: c.polls.Load(),
	}
}
