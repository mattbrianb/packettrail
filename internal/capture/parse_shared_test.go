package capture

import (
	"encoding/binary"
	"net/netip"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func TestParseNettop(t *testing.T) {
	out := []byte(`time,,bytes_in,bytes_out,
16:00:00.123456,launchd.1,0,0,
16:00:00.123456,Google Chrome H.4321,125000,9000,
16:00:00.123456,tcp4 192.168.1.5:50123<->142.250.1.1:443,120000,8000,
16:00:00.123456,udp4 *:5353<->*:*,10,10,
16:00:00.123456,tcp6 2a00:1450:4009:81f::200e.443<->2001:db8::5.61000,5000,1000,
16:00:00.123456,mDNSResponder.212,10,20,
16:00:00.123456,udp4 192.168.1.5:61000<->8.8.8.8:53,10,20,
`)
	rows := ParseNettop(out)
	if len(rows) != 3 {
		t.Fatalf("want 3 rows, got %d: %+v", len(rows), rows)
	}
	r := rows[0]
	if r.PID != 4321 || r.Name != "Google Chrome H" || r.Proto != TCP || r.Remote != netip.MustParseAddrPort("142.250.1.1:443") || r.In != 120000 || r.Out != 8000 {
		t.Errorf("row0 %+v", r)
	}
	if rows[1].Remote.Addr().String() != "2001:db8::5" || rows[1].Remote.Port() != 61000 || rows[1].Local.Port() != 443 {
		t.Errorf("row1 %+v", rows[1])
	}
	if rows[2].PID != 212 || rows[2].Proto != UDP || rows[2].Remote.Port() != 53 {
		t.Errorf("row2 %+v", rows[2])
	}
}

// With -J, nettop leaves the time column out: the identifier is the first
// column and every line ends in a comma. This is the format the app asks for.
func TestParseNettopSelectedColumns(t *testing.T) {
	out := []byte(`,bytes_in,bytes_out,re-tx,rtt_avg,
launchd.1,0,0,0,,
Google Chrome H.4321,125000,9000,1448,,
tcp4 192.168.1.5:50123<->142.250.1.1:443,120000,8000,1448,25.03 ms,
udp4 *:5353<->*:*,10,10,0,,
tcp6 2a00:1450:4009:81f::200e.443<->2001:db8::5.61000,5000,1000,0,12.50 ms,
mDNSResponder.212,10,20,0,,
udp4 192.168.1.5:61000<->8.8.8.8:53,10,20,0,,
`)
	rows := ParseNettop(out)
	if len(rows) != 3 {
		t.Fatalf("want 3 rows, got %d: %+v", len(rows), rows)
	}
	r := rows[0]
	if r.PID != 4321 || r.Name != "Google Chrome H" || r.Proto != TCP || r.Remote != netip.MustParseAddrPort("142.250.1.1:443") ||
		r.In != 120000 || r.Out != 8000 || r.Retrans != 1448 || r.RTT != 25.03 {
		t.Errorf("row0 %+v", r)
	}
	if rows[1].Remote.Addr().String() != "2001:db8::5" || rows[1].RTT != 12.5 {
		t.Errorf("row1 %+v", rows[1])
	}
	if rows[2].PID != 212 || rows[2].Proto != UDP || rows[2].Remote.Port() != 53 {
		t.Errorf("row2 %+v", rows[2])
	}
}

// Lines captured from nettop on macOS 26: QUIC sockets have their own kind,
// and an IPv6 socket line must not be mistaken for a "name.pid" process line.
func TestParseNettopQUIC(t *testing.T) {
	out := []byte(`,bytes_in,bytes_out,re-tx,rtt_avg,
launchd.1,0,0,0,,
tcp6 *.22<->*.*,,,,,
cloudd.253,119112,25864,0,,
quic4 192.168.64.7:53316<->17.248.192.44:443,37507,6826,,2.41 ms,
quic6 2001:db8::7.53317<->2606:4700::6810:84e5.443,8743,5884,,6.50 ms,
tcp4 192.168.64.7:52372<->17.248.192.44:443,64844,6510,0,4.72 ms,
`)
	rows := ParseNettop(out)
	if len(rows) != 3 {
		t.Fatalf("want 3 rows, got %d: %+v", len(rows), rows)
	}
	for i, r := range rows {
		if r.PID != 253 || r.Name != "cloudd" {
			t.Errorf("row %d belongs to %s.%d, want cloudd.253", i, r.Name, r.PID)
		}
	}
	if rows[0].Proto != UDP || rows[0].In != 37507 || rows[0].Remote != netip.MustParseAddrPort("17.248.192.44:443") {
		t.Errorf("quic4 row %+v", rows[0])
	}
	if rows[1].Proto != UDP || rows[1].Remote.Port() != 443 || rows[1].Remote.Addr().String() != "2606:4700::6810:84e5" {
		t.Errorf("quic6 row %+v", rows[1])
	}
	if rows[2].Proto != TCP || rows[2].RTT != 4.72 {
		t.Errorf("tcp row %+v", rows[2])
	}
}

// Without any header the identifier column is found from the row shape.
func TestParseNettopNoHeader(t *testing.T) {
	for _, out := range []string{
		"curl.77,100,200,\ntcp4 10.0.0.2:50000<->1.1.1.1:443,100,200,\n",
		"16:00:00.123456,curl.77,100,200,\n16:00:00.123456,tcp4 10.0.0.2:50000<->1.1.1.1:443,100,200,\n",
	} {
		rows := ParseNettop([]byte(out))
		if len(rows) != 1 || rows[0].PID != 77 || rows[0].In != 100 || rows[0].Out != 200 {
			t.Errorf("%q: got %+v", out, rows)
		}
	}
}

func TestParseDiagMsg(t *testing.T) {
	ne := binary.NativeEndian
	d := make([]byte, 72)
	d[0], d[1] = afInet, 1 // AF_INET, ESTABLISHED
	binary.BigEndian.PutUint16(d[4:], 50123)
	binary.BigEndian.PutUint16(d[6:], 443)
	copy(d[8:], []byte{10, 0, 0, 2})
	copy(d[24:], []byte{1, 1, 1, 1})
	ne.PutUint32(d[44:], 0xdeadbeef)
	ne.PutUint32(d[48:], 0x1)
	ne.PutUint32(d[64:], 1000)
	ne.PutUint32(d[68:], 123456)
	// an unrelated attribute first, then INET_DIAG_INFO with a 232-byte tcp_info
	other := make([]byte, 8)
	ne.PutUint16(other[0:], 5)
	ne.PutUint16(other[2:], 1)
	info := make([]byte, 4+232)
	ne.PutUint16(info[0:], uint16(len(info)))
	ne.PutUint16(info[2:], inetDiagInfo)
	ne.PutUint64(info[4+120:], 777)
	ne.PutUint64(info[4+128:], 888888)
	d = append(append(d, other...), info...)
	s, ok := parseDiagMsg(d)
	if !ok || !s.hasInfo || s.acked != 777 || s.received != 888888 || s.inode != 123456 || s.uid != 1000 ||
		s.local != netip.MustParseAddrPort("10.0.0.2:50123") || s.remote != netip.MustParseAddrPort("1.1.1.1:443") || s.cookie != 0x1deadbeef {
		t.Fatalf("parsed %+v ok=%v", s, ok)
	}
}

func TestParseDNSResponse(t *testing.T) {
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{Response: true, RCode: dnsmessage.RCodeSuccess})
	b.StartQuestions()
	name := dnsmessage.MustNewName("www.example.com.")
	b.Question(dnsmessage.Question{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET})
	b.StartAnswers()
	b.CNAMEResource(dnsmessage.ResourceHeader{Name: name, Class: dnsmessage.ClassINET, TTL: 60}, dnsmessage.CNAMEResource{CNAME: dnsmessage.MustNewName("edge.example.net.")})
	b.AResource(dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName("edge.example.net."), Class: dnsmessage.ClassINET, TTL: 60}, dnsmessage.AResource{A: [4]byte{93, 184, 216, 34}})
	msg, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	ev, ok := parseDNSResponse(msg)
	if !ok || ev.Name != "www.example.com" || len(ev.Addrs) != 1 || ev.Addrs[0].String() != "93.184.216.34" {
		t.Fatalf("got %+v ok=%v", ev, ok)
	}
}
