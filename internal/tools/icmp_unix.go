//go:build !windows

package tools

import (
	"context"
	"encoding/binary"
	"errors"
	"math/rand/v2"
	"net"
	"net/netip"
	"os"
	"runtime"
	"syscall"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// Linux/macOS: raw ICMP sockets when running as root (full ping, traceroute
// and MTR); otherwise unprivileged "ping sockets" where the OS allows them.
// On Linux those only deliver echo replies, so traceroute/MTR need root. On
// macOS they also deliver the routers' ICMP errors, so everything works
// without root (seen on a user's Mac: all hops listed as a normal user).

var payload = []byte("packettrail-probe-abcdefghijklmnopqr")

func echoPlatform(ctx context.Context, dst netip.Addr, ttl int, timeout time.Duration) (Result, error) {
	dst = dst.Unmap()
	v6 := dst.Is6()
	rawNet, dgramNet, proto := "ip4:icmp", "udp4", 1
	if v6 {
		rawNet, dgramNet, proto = "ip6:ipv6-icmp", "udp6", 58
	}
	priv := true
	c, err := icmp.ListenPacket(rawNet, "")
	if err != nil {
		priv = false
		if c, err = icmp.ListenPacket(dgramNet, ""); err != nil {
			if errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
				return Result{Status: StatusError}, ErrNeedsPrivilege
			}
			return Result{Status: StatusError}, err
		}
	}
	defer c.Close()
	if v6 {
		c.IPv6PacketConn().SetHopLimit(ttl)
	} else {
		c.IPv4PacketConn().SetTTL(ttl)
	}
	id, seq := rand.IntN(0xffff), rand.IntN(0xffff)
	var typ icmp.Type = ipv4.ICMPTypeEcho
	if v6 {
		typ = ipv6.ICMPTypeEchoRequest
	}
	msg := icmp.Message{Type: typ, Body: &icmp.Echo{ID: id, Seq: seq, Data: payload}}
	b, err := msg.Marshal(nil)
	if err != nil {
		return Result{Status: StatusError}, err
	}
	var to net.Addr = &net.IPAddr{IP: dst.AsSlice()}
	if !priv {
		to = &net.UDPAddr{IP: dst.AsSlice()}
	}
	deadline := time.Now().Add(timeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	start := time.Now()
	if _, err := c.WriteTo(b, to); err != nil {
		return Result{Status: StatusError}, err
	}
	c.SetReadDeadline(deadline)
	buf := make([]byte, 1500)
	for {
		if ctx.Err() != nil {
			return Result{Status: StatusTimeout}, nil
		}
		n, peer, err := c.ReadFrom(buf)
		if err != nil {
			return Result{Status: StatusTimeout}, nil
		}
		rtt := time.Since(start)
		m, err := icmp.ParseMessage(proto, buf[:n])
		if err != nil {
			continue
		}
		from := peerAddr(peer)
		switch body := m.Body.(type) {
		case *icmp.Echo:
			if m.Type != ipv4.ICMPTypeEchoReply && m.Type != ipv6.ICMPTypeEchoReply {
				continue
			}
			// Ping sockets rewrite the ID, so only the sequence is ours to match.
			if body.Seq != seq || (priv && body.ID != id) {
				continue
			}
			return Result{From: from, RTT: rtt, Status: StatusReply}, nil
		case *icmp.TimeExceeded:
			if innerMatches(body.Data, v6, id, seq, priv) {
				return Result{From: from, RTT: rtt, Status: StatusTTLExpired}, nil
			}
		case *icmp.DstUnreach:
			if innerMatches(body.Data, v6, id, seq, priv) {
				return Result{From: from, RTT: rtt, Status: StatusUnreachable}, nil
			}
		}
	}
}

func peerAddr(a net.Addr) netip.Addr {
	var ip net.IP
	switch v := a.(type) {
	case *net.IPAddr:
		ip = v.IP
	case *net.UDPAddr:
		ip = v.IP
	}
	addr, _ := netip.AddrFromSlice(ip)
	return addr.Unmap()
}

// innerMatches checks the original datagram quoted in an ICMP error (IP
// header + first 8 bytes of our echo request) against our ID and sequence.
func innerMatches(d []byte, v6 bool, id, seq int, priv bool) bool {
	hl := 40
	if !v6 {
		if len(d) < 20 {
			return false
		}
		hl = int(d[0]&0x0f) * 4
	}
	if len(d) < hl+8 {
		return false
	}
	e := d[hl:]
	gotID, gotSeq := int(binary.BigEndian.Uint16(e[4:])), int(binary.BigEndian.Uint16(e[6:]))
	return gotSeq == seq && (!priv || gotID == id)
}

// PathNote explains limits of path tools on this system ("" when none).
// macOS delivers the routers' "time exceeded" answers to unprivileged ICMP
// sockets, so traceroute and MTR work there without root. Linux ping sockets
// only ever see the destination's echo replies.
func PathNote() string {
	if os.Geteuid() == 0 || runtime.GOOS == "darwin" {
		return ""
	}
	return "Not running as root: intermediate hops cannot be shown (raw ICMP sockets need root); only the destination answers."
}
