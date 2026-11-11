//go:build windows

package tools

import (
	"context"
	"encoding/binary"
	"net/netip"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows probes through the IP Helper ICMP API (the same one ping.exe and
// tracert.exe use). It works without administrator rights.

var (
	modIphlpapi        = windows.NewLazySystemDLL("iphlpapi.dll")
	procIcmpCreateFile = modIphlpapi.NewProc("IcmpCreateFile")
	procIcmp6Create    = modIphlpapi.NewProc("Icmp6CreateFile")
	procIcmpSendEcho   = modIphlpapi.NewProc("IcmpSendEcho")
	procIcmp6SendEcho2 = modIphlpapi.NewProc("Icmp6SendEcho2")
	procIcmpCloseHndl  = modIphlpapi.NewProc("IcmpCloseHandle")
)

const (
	ipSuccess           = 0
	ipDestNetUnreach    = 11002
	ipDestHostUnreach   = 11003
	ipDestProtUnreach   = 11004
	ipDestPortUnreach   = 11005
	ipReqTimedOut       = 11010
	ipTTLExpiredTransit = 11013
	ipTTLExpiredReassem = 11014
	ipDestUnreachable   = 11040
	ipTimeExceeded      = 11041
)

type ipOptionInformation struct {
	TTL         uint8
	TOS         uint8
	Flags       uint8
	OptionsSize uint8
	OptionsData uintptr
}

type icmpEchoReply struct {
	Address       [4]byte
	Status        uint32
	RoundTripTime uint32
	DataSize      uint16
	Reserved      uint16
	Data          uintptr
	Options       ipOptionInformation
}

var _ = [1]struct{}{}[unsafe.Sizeof(icmpEchoReply{})-40]

var (
	h4Once, h6Once sync.Once
	h4, h6         windows.Handle
)

func handle4() windows.Handle {
	h4Once.Do(func() {
		r, _, _ := procIcmpCreateFile.Call()
		h4 = windows.Handle(r)
	})
	return h4
}

func handle6() windows.Handle {
	h6Once.Do(func() {
		r, _, _ := procIcmp6Create.Call()
		h6 = windows.Handle(r)
	})
	return h6
}

func statusOf(code uint32) Status {
	switch code {
	case ipSuccess:
		return StatusReply
	case ipTTLExpiredTransit, ipTTLExpiredReassem, ipTimeExceeded:
		return StatusTTLExpired
	case ipDestNetUnreach, ipDestHostUnreach, ipDestProtUnreach, ipDestPortUnreach, ipDestUnreachable:
		return StatusUnreachable
	case ipReqTimedOut:
		return StatusTimeout
	}
	return StatusError
}

var payload = []byte("packettrail-probe-abcdefghijklmnopqr") // 32 bytes, like ping.exe

func echoPlatform(ctx context.Context, dst netip.Addr, ttl int, timeout time.Duration) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{Status: StatusTimeout}, nil
	}
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) < timeout {
		timeout = time.Until(dl)
	}
	if timeout < 50*time.Millisecond {
		timeout = 50 * time.Millisecond
	}
	dst = dst.Unmap()
	if dst.Is4() {
		return echo4(dst, ttl, timeout)
	}
	return echo6(dst, ttl, timeout)
}

func echo4(dst netip.Addr, ttl int, timeout time.Duration) (Result, error) {
	h := handle4()
	if h == windows.InvalidHandle || h == 0 {
		return Result{Status: StatusError}, ErrNeedsPrivilege
	}
	a := dst.As4()
	dest := *(*uint32)(unsafe.Pointer(&a[0])) // IPAddr is in network byte order
	opt := ipOptionInformation{TTL: uint8(ttl)}
	reply := make([]byte, 40+len(payload)+8+256)
	start := time.Now()
	n, _, e := procIcmpSendEcho.Call(uintptr(h), uintptr(dest), uintptr(unsafe.Pointer(&payload[0])), uintptr(len(payload)),
		uintptr(unsafe.Pointer(&opt)), uintptr(unsafe.Pointer(&reply[0])), uintptr(len(reply)), uintptr(timeout.Milliseconds()))
	elapsed := time.Since(start)
	if n == 0 {
		code := uint32(0)
		if en, ok := e.(syscall.Errno); ok {
			code = uint32(en)
		}
		st := statusOf(code)
		if st == StatusReply || st == StatusError {
			st = StatusTimeout
		}
		return Result{Status: st}, nil
	}
	r := (*icmpEchoReply)(unsafe.Pointer(&reply[0]))
	res := Result{From: netip.AddrFrom4(r.Address), Status: statusOf(r.Status), TTL: int(r.Options.TTL)}
	res.RTT = rtt(elapsed, r.RoundTripTime)
	return res, nil
}

// rtt prefers our own sub-millisecond measurement of the blocking call, but
// falls back to the API's whole-millisecond figure if the call was delayed.
func rtt(elapsed time.Duration, apiMs uint32) time.Duration {
	api := time.Duration(apiMs) * time.Millisecond
	if elapsed > api+5*time.Millisecond {
		return api
	}
	return elapsed
}

func echo6(dst netip.Addr, ttl int, timeout time.Duration) (Result, error) {
	h := handle6()
	if h == windows.InvalidHandle || h == 0 {
		return Result{Status: StatusError}, ErrNeedsPrivilege
	}
	// sockaddr_in6: family, port, flowinfo, addr[16], scope_id
	src := make([]byte, 28)
	binary.LittleEndian.PutUint16(src, windows.AF_INET6)
	dsa := make([]byte, 28)
	binary.LittleEndian.PutUint16(dsa, windows.AF_INET6)
	a16 := dst.As16()
	copy(dsa[8:24], a16[:])
	opt := ipOptionInformation{TTL: uint8(ttl)}
	reply := make([]byte, 64+len(payload)+8+256)
	start := time.Now()
	n, _, e := procIcmp6SendEcho2.Call(uintptr(h), 0, 0, 0, uintptr(unsafe.Pointer(&src[0])), uintptr(unsafe.Pointer(&dsa[0])),
		uintptr(unsafe.Pointer(&payload[0])), uintptr(len(payload)), uintptr(unsafe.Pointer(&opt)),
		uintptr(unsafe.Pointer(&reply[0])), uintptr(len(reply)), uintptr(timeout.Milliseconds()))
	elapsed := time.Since(start)
	if n == 0 {
		code := uint32(0)
		if en, ok := e.(syscall.Errno); ok {
			code = uint32(en)
		}
		st := statusOf(code)
		if st == StatusReply || st == StatusError {
			st = StatusTimeout
		}
		return Result{Status: st}, nil
	}
	// ICMPV6_ECHO_REPLY (packed): IPV6_ADDRESS_EX {port u16, flowinfo u32,
	// addr u16[8], scope u32} = 26 bytes, then Status u32, RoundTripTime u32.
	var from [16]byte
	copy(from[:], reply[6:22])
	res := Result{From: netip.AddrFrom16(from).Unmap(), Status: statusOf(binary.LittleEndian.Uint32(reply[26:]))}
	res.RTT = rtt(elapsed, binary.LittleEndian.Uint32(reply[30:]))
	return res, nil
}

// PathNote explains limits of path tools on this system ("" when none).
func PathNote() string { return "" }
