package capture

import (
	"encoding/binary"
	"net/netip"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const (
	afInet       = 2
	inetDiagInfo = 2
)

type diagSock struct {
	family          uint8
	state           uint8
	local, remote   netip.AddrPort
	cookie          uint64
	uid             uint32
	inode           uint32
	acked, received uint64
	hasInfo         bool
	rttUs           uint32 // tcpi_rtt, microseconds
	retransSegs     uint32 // tcpi_total_retrans
	bytesRetrans    uint64 // tcpi_bytes_retrans (0 on old kernels)
}

// parseDiagMsg decodes struct inet_diag_msg plus the INET_DIAG_INFO attribute
// (struct tcp_info; bytes_acked at offset 120, bytes_received at 128).
func parseDiagMsg(d []byte) (diagSock, bool) {
	var s diagSock
	if len(d) < 72 {
		return s, false
	}
	ne, be := binary.NativeEndian, binary.BigEndian
	s.family, s.state = d[0], d[1]
	sport, dport := be.Uint16(d[4:]), be.Uint16(d[6:])
	var src, dst netip.Addr
	if s.family == afInet {
		src, dst = netip.AddrFrom4([4]byte(d[8:12])), netip.AddrFrom4([4]byte(d[24:28]))
	} else {
		src, dst = netip.AddrFrom16([16]byte(d[8:24])).Unmap(), netip.AddrFrom16([16]byte(d[24:40])).Unmap()
	}
	s.local, s.remote = netip.AddrPortFrom(src, sport), netip.AddrPortFrom(dst, dport)
	s.cookie = uint64(ne.Uint32(d[44:])) | uint64(ne.Uint32(d[48:]))<<32
	s.uid, s.inode = ne.Uint32(d[64:]), ne.Uint32(d[68:])
	a := d[72:]
	for len(a) >= 4 {
		l := int(ne.Uint16(a))
		t := ne.Uint16(a[2:]) & 0x7fff
		if l < 4 || l > len(a) {
			break
		}
		if t == inetDiagInfo && l-4 >= 136 {
			ti := a[4:l]
			s.acked, s.received, s.hasInfo = ne.Uint64(ti[120:]), ne.Uint64(ti[128:]), true
			s.rttUs, s.retransSegs = ne.Uint32(ti[68:]), ne.Uint32(ti[100:])
			if len(ti) >= 216 {
				s.bytesRetrans = ne.Uint64(ti[208:])
			}
		}
		next := (l + 3) &^ 3
		if next >= len(a) {
			break
		}
		a = a[next:]
	}
	return s, true
}

// parseDNSResponse extracts the queried name and A/AAAA answers.
func parseDNSResponse(b []byte) (DNSEvent, bool) {
	var p dnsmessage.Parser
	h, err := p.Start(b)
	if err != nil || !h.Response || h.RCode != dnsmessage.RCodeSuccess {
		return DNSEvent{}, false
	}
	q, err := p.Question()
	if err != nil {
		return DNSEvent{}, false
	}
	p.SkipAllQuestions()
	ev := DNSEvent{Time: time.Now(), Name: strings.TrimSuffix(q.Name.String(), ".")}
	for {
		rh, err := p.AnswerHeader()
		if err != nil {
			break
		}
		switch rh.Type {
		case dnsmessage.TypeA:
			if r, err := p.AResource(); err == nil {
				ev.Addrs = append(ev.Addrs, netip.AddrFrom4(r.A))
			}
		case dnsmessage.TypeAAAA:
			if r, err := p.AAAAResource(); err == nil {
				ev.Addrs = append(ev.Addrs, netip.AddrFrom16(r.AAAA).Unmap())
			}
		default:
			p.SkipAnswer()
		}
	}
	return ev, len(ev.Addrs) > 0
}
