//go:build windows

package capture

import (
	"encoding/binary"
	"errors"
	"net/netip"
	"syscall"
	"testing"
	"unicode/utf16"
)

func TestConnTables(t *testing.T) {
	rows, err := ReadConnTables(true)
	if err != nil {
		t.Fatal(err)
	}
	var tcp, udp, est int
	for _, r := range rows {
		if r.Proto == TCP {
			tcp++
			if r.State == 5 {
				est++
			}
		} else {
			udp++
		}
	}
	t.Logf("tcp=%d (established %d) udp=%d", tcp, est, udp)
	if tcp == 0 {
		t.Fatal("no TCP rows")
	}
	for i, r := range rows {
		if i < 5 && r.Proto == TCP && r.State == 5 {
			t.Logf("  %v -> %v pid %d", r.Local, r.Remote, r.PID)
		}
	}
}

func TestDNSCache(t *testing.T) {
	m, err := ReadDNSCache()
	if errors.Is(err, syscall.Errno(0)) {
		t.Skip("Windows DNS cache API returned no table and no error code on this machine")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d names in DNS cache", len(m))
	n := 0
	for name, a := range m {
		if n++; n > 5 {
			break
		}
		t.Logf("  %s -> %v", name, a)
	}
}

func TestProcs(t *testing.T) {
	p := newWinProcs()
	rows, _ := ReadConnTables(false)
	seen := map[int]bool{}
	for _, r := range rows {
		if seen[r.PID] || len(seen) > 12 {
			continue
		}
		seen[r.PID] = true
		i := p.Lookup(r.PID)
		t.Logf("pid %d -> %q key=%q path=%q", r.PID, i.Name, i.Key, i.Path)
	}
}

func TestParseKernelNetwork(t *testing.T) {
	d := make([]byte, 36)
	binary.LittleEndian.PutUint32(d[0:], 1234)
	binary.LittleEndian.PutUint32(d[4:], 1460)
	copy(d[8:], []byte{142, 250, 1, 1})
	copy(d[12:], []byte{192, 168, 1, 10})
	binary.BigEndian.PutUint16(d[16:], 443)
	binary.BigEndian.PutUint16(d[18:], 50123)
	ev, ok := ParseKernelNetwork(11, d)
	if !ok || ev.PID != 1234 || ev.Size != 1460 || ev.Daddr != netip.MustParseAddr("142.250.1.1") ||
		ev.Saddr != netip.MustParseAddr("192.168.1.10") || ev.Dport != 443 || ev.Sport != 50123 || ev.Kind != 2 || ev.Proto != TCP {
		t.Fatalf("bad v4 parse: %+v", ev)
	}
	d6 := make([]byte, 60)
	binary.LittleEndian.PutUint32(d6[0:], 99)
	binary.LittleEndian.PutUint32(d6[4:], 1200)
	a := netip.MustParseAddr("2606:4700::1111").As16()
	b := netip.MustParseAddr("2001:db8::5").As16()
	copy(d6[8:], a[:])
	copy(d6[24:], b[:])
	binary.BigEndian.PutUint16(d6[40:], 443)
	binary.BigEndian.PutUint16(d6[42:], 61000)
	ev, ok = ParseKernelNetwork(58, d6)
	if !ok || ev.Proto != UDP || ev.Kind != 1 || ev.Daddr.String() != "2606:4700::1111" || ev.Sport != 61000 {
		t.Fatalf("bad v6 parse: %+v", ev)
	}
}

func utf16z16(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 2*len(u)+2)
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[2*i:], c)
	}
	return b
}

func TestParseDNS3008(t *testing.T) {
	var d []byte
	d = append(d, utf16z16("www.example.com")...)
	f := make([]byte, 16)
	binary.LittleEndian.PutUint32(f[0:], 1)
	binary.LittleEndian.PutUint32(f[12:], 0)
	d = append(d, f...)
	d = append(d, utf16z16("type:  5 edge.example.net;::ffff:93.184.216.34;2606:2800:220:1::1;")...)
	name, status, addrs, ok := ParseDNS3008(d)
	if !ok || name != "www.example.com" || status != 0 || len(addrs) != 2 || addrs[0].String() != "93.184.216.34" {
		t.Fatalf("got %q %d %v %v", name, status, addrs, ok)
	}
}

// Without elevation StartTrace must fail with access denied; this at least
// exercises the call path and the properties layout.
func TestStartTraceUnelevated(t *testing.T) {
	if IsPrivileged() {
		t.Skip("elevated")
	}
	c := newETWCapture(newWinProcs())
	err := c.Start(nil)
	if err == nil {
		c.Stop()
		t.Fatal("expected failure when not elevated")
	}
	t.Logf("StartTrace unelevated: %v", err)
	if !errors.Is(err, syscall.Errno(5)) {
		t.Errorf("expected ERROR_ACCESS_DENIED, got %v", err)
	}
}
