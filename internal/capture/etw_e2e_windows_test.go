//go:build windows

package capture

import (
	"encoding/binary"
	"net/netip"
	"os"
	"sync"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procEventRegister   = modAdvapi32.NewProc("EventRegister")
	procEventWrite      = modAdvapi32.NewProc("EventWrite")
	procEventUnregister = modAdvapi32.NewProc("EventUnregister")
)

type eventDataDescriptor struct {
	Ptr      uint64
	Size     uint32
	Reserved uint32
}

type testSink struct {
	mu    sync.Mutex
	flows []FlowEvent
	dns   []DNSEvent
}

func (s *testSink) Flows(evs []FlowEvent) {
	s.mu.Lock()
	s.flows = append(s.flows, evs...)
	s.mu.Unlock()
}
func (s *testSink) DNS(ev DNSEvent) { s.mu.Lock(); s.dns = append(s.dns, ev); s.mu.Unlock() }

func registerProvider(t *testing.T, g *windows.GUID) uint64 {
	var h uint64
	if r, _, _ := procEventRegister.Call(uintptr(unsafe.Pointer(g)), 0, 0, uintptr(unsafe.Pointer(&h))); r != 0 {
		t.Fatalf("EventRegister: %v", windows.Errno(r))
	}
	t.Cleanup(func() { procEventUnregister.Call(uintptr(h)) })
	return h
}

func writeEvent(t *testing.T, h uint64, id uint16, payload []byte) {
	desc := eventDescriptor{Id: id, Level: 4}
	data := eventDataDescriptor{Ptr: uint64(uintptr(unsafe.Pointer(&payload[0]))), Size: uint32(len(payload))}
	if r, _, _ := procEventWrite.Call(uintptr(h), uintptr(unsafe.Pointer(&desc)), 1, uintptr(unsafe.Pointer(&data))); r != 0 {
		t.Fatalf("EventWrite: %v", windows.Errno(r))
	}
}

// TestETWEndToEnd runs the real session/consumer/callback code against two
// in-process test providers that emit payloads shaped like Kernel-Network
// and DNS-Client events. Needs admin or membership of "Performance Log Users".
func TestETWEndToEnd(t *testing.T) {
	knGUID := windows.GUID{Data1: 0x1d2c3b4a, Data2: 0x1111, Data3: 0x4222, Data4: [8]byte{1, 2, 3, 4, 5, 6, 7, 8}}
	dnsGUID := windows.GUID{Data1: 0x1d2c3b4b, Data2: 0x1111, Data3: 0x4222, Data4: [8]byte{1, 2, 3, 4, 5, 6, 7, 9}}
	knReg := registerProvider(t, &knGUID)
	dnsReg := registerProvider(t, &dnsGUID)

	c := newETWCapture(newWinProcs())
	c.name = "PacketTrail-UnitTest"
	c.providers = []etwProvider{
		{guid: knGUID, required: true, handle: (*etwCapture).onKernelNetwork},
		{guid: dnsGUID, required: true, handle: (*etwCapture).onDNS},
	}
	sink := &testSink{}
	if err := c.Start(sink); err != nil {
		t.Skipf("cannot start ETW session here: %v", err)
	}
	defer c.Stop()
	time.Sleep(700 * time.Millisecond)

	// TCPv4 receive: 1460 bytes from 142.250.1.1:443 to 192.168.1.10:50123, PID 1234
	d := make([]byte, 36)
	binary.LittleEndian.PutUint32(d[0:], 1234)
	binary.LittleEndian.PutUint32(d[4:], 1460)
	copy(d[8:], []byte{142, 250, 1, 1})
	copy(d[12:], []byte{192, 168, 1, 10})
	binary.BigEndian.PutUint16(d[16:], 443)
	binary.BigEndian.PutUint16(d[18:], 50123)
	writeEvent(t, knReg, 11, d)
	// TCPv4 send of 517 bytes on the same tuple
	binary.LittleEndian.PutUint32(d[4:], 517)
	writeEvent(t, knReg, 10, d)
	// TCPv4 retransmit of 300 bytes on the same tuple
	binary.LittleEndian.PutUint32(d[4:], 300)
	writeEvent(t, knReg, 14, d)

	var p []byte
	p = append(p, utf16z16("cdn.example.org")...)
	f := make([]byte, 16)
	binary.LittleEndian.PutUint32(f[0:], 1)
	p = append(p, f...)
	p = append(p, utf16z16("type:  5 edge.example.net;::ffff:142.250.1.1;")...)
	writeEvent(t, dnsReg, 3008, p)

	start := time.Now()
	var got []FlowEvent
	var dns *DNSEvent
	for time.Since(start) < 6*time.Second {
		time.Sleep(200 * time.Millisecond)
		sink.mu.Lock()
		got = append([]FlowEvent(nil), sink.flows...)
		for i := range sink.dns {
			if sink.dns[i].Name == "cdn.example.org" {
				dns = &sink.dns[i]
			}
		}
		sink.mu.Unlock()
		if len(got) >= 3 && dns != nil {
			break
		}
	}
	t.Logf("delivered after %v: %d flow events, status %+v", time.Since(start).Round(time.Millisecond), len(got), c.Status())
	if len(got) < 3 {
		t.Fatalf("expected 3 flow events, got %d", len(got))
	}
	var in, out, rtx, rtxp uint64
	for _, ev := range got {
		if ev.PID != 1234 || ev.Remote != netip.MustParseAddrPort("142.250.1.1:443") || ev.Local != netip.MustParseAddrPort("192.168.1.10:50123") || ev.Proto != TCP {
			t.Errorf("unexpected event %+v", ev)
		}
		if d := time.Since(ev.Time); d < 0 || d > 10*time.Second {
			t.Errorf("event timestamp off: %v (%v ago)", ev.Time, d)
		}
		in += ev.In
		out += ev.Out
		rtx += ev.Retrans
		rtxp += ev.RetransPkts
	}
	if in != 1460 || out != 517 || rtx != 300 || rtxp != 1 {
		t.Errorf("bytes in/out/retrans = %d/%d/%d (%d pkts), want 1460/517/300 (1)", in, out, rtx, rtxp)
	}
	if dns == nil {
		t.Fatal("no DNS event delivered")
	}
	if dns.PID != os.Getpid() || len(dns.Addrs) != 1 || dns.Addrs[0] != netip.MustParseAddr("142.250.1.1") {
		t.Errorf("unexpected DNS event %+v (our pid %d)", *dns, os.Getpid())
	}
}
