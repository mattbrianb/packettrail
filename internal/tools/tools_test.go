package tools

import (
	"context"
	"net/netip"
	"os"
	"sync"
	"testing"
	"time"
)

// Live tests send real ICMP; enable with PACKETTRAIL_LIVE=1.
func live(t *testing.T) {
	if os.Getenv("PACKETTRAIL_LIVE") == "" {
		t.Skip("set PACKETTRAIL_LIVE=1 to send real probes")
	}
}

func TestEchoLive(t *testing.T) {
	live(t)
	dst := netip.MustParseAddr("1.1.1.1")
	res, err := Echo(context.Background(), dst, 128, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("echo 1.1.1.1: %+v", res)
	if res.Status != StatusReply || res.From != dst {
		t.Fatalf("expected reply from 1.1.1.1, got %+v", res)
	}
	res, err = Echo(context.Background(), dst, 1, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("ttl 1: %+v", res)
	if res.Status != StatusTTLExpired || !res.From.IsValid() {
		t.Fatalf("expected TTL expired from first hop, got %+v", res)
	}
}

func TestMTRLive(t *testing.T) {
	live(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var last PathReport
	err := RunPath(ctx, "mtr", netip.MustParseAddr("1.1.1.1"), 3, 500*time.Millisecond, Echo, nil, func(r PathReport) { last = r })
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range last.Hops {
		t.Logf("%2d %-16s sent %d recv %d loss %5.1f%% avg %6.2f best %6.2f worst %6.2f", h.TTL, h.Addr, h.Sent, h.Recv, h.Loss, h.Avg, h.Best, h.Worst)
	}
	if !last.Reached || last.Hops[len(last.Hops)-1].Addr != "1.1.1.1" {
		t.Fatalf("destination not reached: %+v", last)
	}
}

// fakePath simulates a destination `dist` hops away that, like real hosts and
// carriers, rate-limits echo requests: it answers one, then ignores any other
// that arrives within the next 60 ms. Routers on the way always answer.
type fakePath struct {
	dist       int
	mu         sync.Mutex
	lastAnswer time.Time
	atDst      int // echo requests that reached the destination
}

func (f *fakePath) echo(ctx context.Context, dst netip.Addr, ttl int, timeout time.Duration) (Result, error) {
	if ttl < f.dist {
		return Result{From: netip.AddrFrom4([4]byte{10, 0, 0, byte(ttl)}), RTT: time.Millisecond, Status: StatusTTLExpired}, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.atDst++
	if time.Since(f.lastAnswer) < 60*time.Millisecond {
		return Result{Status: StatusTimeout}, nil // dropped by the rate limit
	}
	f.lastAnswer = time.Now()
	return Result{From: dst, RTT: 2 * time.Millisecond, Status: StatusReply}, nil
}

// A destination that rate-limits echo requests must not show packet loss:
// only one probe per round may reach it, and the hop that counts is probed
// first. (Probing all 30 TTLs at once sent it a burst of 23 and reported
// the dropped ones as loss on the last hop.)
func TestRunPathNoFalseLossAtDestination(t *testing.T) {
	const rounds = 6
	f := &fakePath{dist: 8}
	dst := netip.MustParseAddr("192.0.2.80")
	var last PathReport
	if err := RunPath(context.Background(), "mtr", dst, rounds, 100*time.Millisecond, f.echo, nil, func(r PathReport) { last = r }); err != nil {
		t.Fatal(err)
	}
	if !last.Reached || len(last.Hops) != f.dist {
		t.Fatalf("want the destination reached at hop %d, got reached=%v with %d hops", f.dist, last.Reached, len(last.Hops))
	}
	end := last.Hops[len(last.Hops)-1]
	if end.Addr != dst.String() || end.Loss != 0 || end.Recv != rounds {
		t.Errorf("last hop: addr %s sent %d recv %d loss %.1f%%, want %s with %d of %d answered", end.Addr, end.Sent, end.Recv, end.Loss, dst, rounds, rounds)
	}
	for _, h := range last.Hops[:len(last.Hops)-1] {
		if h.Sent != rounds || h.Loss != 0 {
			t.Errorf("hop %d: sent %d loss %.1f%%, want %d sent and no loss", h.TTL, h.Sent, h.Loss, rounds)
		}
	}
	// Round 1 walks out in batches of 6: TTLs 8..12 reach the destination;
	// every later round sends it exactly one probe.
	if max := 5 + (rounds - 1); f.atDst > max {
		t.Errorf("%d echo requests reached the destination, want at most %d", f.atDst, max)
	}
}

// When the destination never answers, the report ends just past the last
// router that did, and later rounds do not probe all 30 TTLs.
func TestRunPathSilentDestination(t *testing.T) {
	var mu sync.Mutex
	probes := map[int]int{}
	echo := func(ctx context.Context, dst netip.Addr, ttl int, timeout time.Duration) (Result, error) {
		mu.Lock()
		probes[ttl]++
		mu.Unlock()
		if ttl <= 4 {
			return Result{From: netip.AddrFrom4([4]byte{10, 0, 0, byte(ttl)}), RTT: time.Millisecond, Status: StatusTTLExpired}, nil
		}
		return Result{Status: StatusTimeout}, nil
	}
	var last PathReport
	if err := RunPath(context.Background(), "trace", netip.MustParseAddr("192.0.2.81"), 2, time.Millisecond, echo, nil, func(r PathReport) { last = r }); err != nil {
		t.Fatal(err)
	}
	if last.Reached || len(last.Hops) != 5 || last.Hops[3].Recv != 2 || last.Hops[4].Recv != 0 {
		t.Errorf("want 4 answering hops and one silent row, got reached=%v hops=%+v", last.Reached, last.Hops)
	}
	if probes[30] != 1 || probes[7] != 2 || probes[8] != 1 {
		t.Errorf("round 1 should walk to hop 30 and round 2 stop at hop 7; probes per TTL: %v", probes)
	}
}

func TestPingAndTCPLive(t *testing.T) {
	live(t)
	ctx := context.Background()
	dst := netip.MustParseAddr("1.1.1.1")
	var p SeriesReport
	RunSeries(ctx, "ping", dst, 0, 3, 200*time.Millisecond, PingProbe(Echo, dst), func(r SeriesReport) { p = r })
	t.Logf("ping: sent %d recv %d loss %.0f%% min %.2f avg %.2f max %.2f jitter %.2f samples %+v", p.Sent, p.Recv, p.Loss, p.Min, p.Avg, p.Max, p.Jitter, p.Samples)
	if p.Recv == 0 {
		t.Fatal("no ping replies")
	}
	var tc SeriesReport
	RunSeries(ctx, "tcp", dst, 443, 2, 200*time.Millisecond, TCPProbe(dst, 443), func(r SeriesReport) { tc = r })
	t.Logf("tcp 443: %+v", tc.Samples)
	if tc.Recv == 0 {
		t.Fatal("TCP 443 to 1.1.1.1 failed")
	}
}

func TestStats(t *testing.T) {
	var s stats
	for _, v := range []float64{10, 12, 11, 13} {
		s.sent++
		s.add(v)
	}
	s.sent++ // one lost
	if s.best != 10 || s.worst != 13 || s.mean != 11.5 || s.loss() != 20 || round2(s.jitter()) != 1.67 {
		t.Fatalf("stats wrong: %+v loss %v jitter %v", s, s.loss(), s.jitter())
	}
}
