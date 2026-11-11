// Package tools implements the interactive diagnostics: MTR, traceroute,
// ping and TCP connect checks. ICMP probing is provided per OS by Echo.
package tools

import (
	"context"
	"errors"
	"math"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"
)

// Status of one probe.
type Status int

const (
	StatusReply       Status = iota // echo reply from the target
	StatusTTLExpired                // time exceeded from an intermediate hop
	StatusUnreachable               // destination/port unreachable
	StatusTimeout
	StatusError
)

func (s Status) String() string {
	switch s {
	case StatusReply:
		return "reply"
	case StatusTTLExpired:
		return "ttl-expired"
	case StatusUnreachable:
		return "unreachable"
	case StatusTimeout:
		return "timeout"
	}
	return "error"
}

// Result of one ICMP echo probe.
type Result struct {
	From   netip.Addr
	RTT    time.Duration
	Status Status
	TTL    int // TTL of the reply packet, when known
}

// ErrNeedsPrivilege is returned when the OS will not let us send the probe.
var ErrNeedsPrivilege = errors.New("ICMP probing needs administrator/root rights on this system")

// HopInfo describes an address for display (filled by the caller).
type HopInfo struct {
	Host  string `json:"host"`
	Scope string `json:"scope"`
	CC    string `json:"cc"`
	City  string `json:"city"`
	ASN   uint32 `json:"asn"`
	ASOrg string `json:"asOrg"`
}

// Describe looks up display info for an address.
type Describe func(netip.Addr) HopInfo

// stats is a running RTT summary (Welford).
type stats struct {
	sent, recv          int
	last, best, worst   float64
	mean, m2, jitterSum float64
	prev                float64
	havePrev            bool
}

func (s *stats) add(ms float64) {
	s.recv++
	s.last = ms
	if s.recv == 1 || ms < s.best {
		s.best = ms
	}
	if ms > s.worst {
		s.worst = ms
	}
	d := ms - s.mean
	s.mean += d / float64(s.recv)
	s.m2 += d * (ms - s.mean)
	if s.havePrev {
		s.jitterSum += math.Abs(ms - s.prev)
	}
	s.prev, s.havePrev = ms, true
}

func (s *stats) stdev() float64 {
	if s.recv < 2 {
		return 0
	}
	return math.Sqrt(s.m2 / float64(s.recv-1))
}

func (s *stats) jitter() float64 {
	if s.recv < 2 {
		return 0
	}
	return s.jitterSum / float64(s.recv-1)
}

func (s *stats) loss() float64 {
	if s.sent == 0 {
		return 0
	}
	return 100 * float64(s.sent-s.recv) / float64(s.sent)
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// ---------------------------------------------------------------- MTR / traceroute

// Hop is one row of an MTR/traceroute report.
type Hop struct {
	TTL    int      `json:"ttl"`
	Addr   string   `json:"addr"`
	Others []string `json:"others,omitempty"` // other addresses seen at this hop (ECMP)
	HopInfo
	Sent   int     `json:"sent"`
	Recv   int     `json:"recv"`
	Loss   float64 `json:"loss"`
	Last   float64 `json:"last"`
	Avg    float64 `json:"avg"`
	Best   float64 `json:"best"`
	Worst  float64 `json:"worst"`
	StDev  float64 `json:"stdev"`
	Status string  `json:"status"`
	// Set by markZones for hops that answer from a private address.
	Zone  string `json:"zone,omitempty"`  // ZoneLAN or ZoneProvider
	InASN uint32 `json:"inAsn,omitempty"` // the network a ZoneProvider hop sits inside, when known
	InOrg string `json:"inOrg,omitempty"`
}

// Where a hop with a private address sits on the path.
const (
	ZoneLAN      = "lan"      // the local network: before the path first leaves it
	ZoneProvider = "provider" // past the local network: a provider's router
)

// markZones says what each private-address hop belongs to.
//
// Private addresses are the local network only until the path first leaves
// it. A path cannot come back: once a hop has answered from a public (or
// carrier-NAT) address, a later router answering from a private address is
// a provider using private addressing on its own links. When the public
// hops on both sides of it are in one network, it is inside that network.
func markZones(hops []Hop) {
	left := false // the path has left the local network
	prev := -1    // the last public hop
	for i := range hops {
		h := &hops[i]
		if h.Addr == "" {
			continue
		}
		switch h.Scope {
		case "":
			left, prev = true, i
			continue
		case "cgnat":
			left = true
			continue
		case "private":
		default:
			continue // loopback, link-local and the like keep their own label
		}
		if !left {
			h.Zone = ZoneLAN
			continue
		}
		h.Zone = ZoneProvider
		if prev < 0 || hops[prev].ASN == 0 {
			continue
		}
		for _, next := range hops[i+1:] {
			if next.Addr == "" || next.Scope != "" {
				continue
			}
			if next.ASN == hops[prev].ASN {
				h.InASN, h.InOrg = hops[prev].ASN, hops[prev].ASOrg
			}
			break
		}
	}
}

// PathReport is a snapshot of a running MTR or traceroute.
type PathReport struct {
	Kind    string `json:"kind"`
	Target  string `json:"target"`
	Rounds  int    `json:"rounds"`
	Running bool   `json:"running"`
	Reached bool   `json:"reached"`
	Hops    []Hop  `json:"hops"`
	Note    string `json:"note,omitempty"`
}

type hopState struct {
	st     stats
	addrs  map[netip.Addr]int
	last   netip.Addr
	status Status
}

// RunPath runs MTR (rounds = 0: until ctx is done) or a traceroute (rounds =
// 3). A round probes every hop up to the destination in parallel; emit gets a
// snapshot after each round, and after each batch while the path is first
// being discovered.
//
// Every probe sent with a TTL beyond the destination's distance reaches the
// destination too. Hosts and carriers rate-limit a burst of echo requests, so
// probing all 30 TTLs at once made the destination drop most of them, and the
// drops showed up as packet loss on the last hop. Three things prevent that:
// the first round walks outward a few hops at a time and stops at the
// destination; probes are launched a few milliseconds apart, lowest TTL
// first, so the destination hears the probe that counts before any extra
// one; and when a lower TTL turns out to reach the destination, that hop's
// earlier (burst) results are discarded.
func RunPath(ctx context.Context, kind string, dst netip.Addr, rounds int, interval time.Duration,
	echo EchoFunc, describe Describe, emit func(PathReport)) error {
	const (
		maxHops = 30
		timeout = 2 * time.Second
		batch   = 6                     // hops per step while discovering the path
		stagger = 10 * time.Millisecond // gap between two probe launches
	)
	hops := make([]*hopState, maxHops+1)
	for i := range hops {
		hops[i] = &hopState{addrs: map[netip.Addr]int{}, status: StatusTimeout}
	}
	limit := maxHops // probe TTLs 1..limit
	reached := false
	var note string
	var mu sync.Mutex
	var fatal error

	// probe sends one echo for each TTL in from..to and waits for all of
	// them. It reports false when ctx ended before every probe was launched.
	probe := func(from, to int) bool {
		var wg sync.WaitGroup
		defer wg.Wait()
		for ttl := from; ttl <= to; ttl++ {
			if ttl > from {
				select {
				case <-ctx.Done():
					return false
				case <-time.After(stagger):
				}
			}
			wg.Add(1)
			go func(ttl int) {
				defer wg.Done()
				res, err := echo(ctx, dst, ttl, timeout)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					if errors.Is(err, ErrNeedsPrivilege) {
						fatal = err
					}
					return
				}
				h := hops[ttl]
				atDst := res.Status == StatusReply || (res.Status == StatusUnreachable && res.From == dst)
				if atDst && ttl < limit {
					// The destination is nearer than was known. What this TTL
					// recorded before came from rounds in which several probes
					// hit the destination at once; start its count afresh.
					h.st = stats{}
				}
				h.st.sent++
				h.status = res.Status
				if res.Status == StatusTimeout || res.Status == StatusError {
					return
				}
				h.st.add(float64(res.RTT.Microseconds()) / 1000)
				if res.From.IsValid() {
					h.addrs[res.From]++
					h.last = res.From
				}
				if atDst {
					if ttl < limit {
						limit = ttl
					}
					reached = true
				}
			}(ttl)
		}
		return true
	}
	locked := func(f func()) { mu.Lock(); defer mu.Unlock(); f() }

	for r := 1; rounds == 0 || r <= rounds; r++ {
		start := time.Now()
		if r == 1 {
			// Discovery: walk outward until the destination answers.
			answeredTo := 0
			for lo := 1; lo <= maxHops; lo += batch {
				hi := min(lo+batch-1, maxHops)
				if !probe(lo, hi) {
					break
				}
				var done bool
				locked(func() {
					for ttl := lo; ttl <= hi; ttl++ {
						if hops[ttl].st.recv > 0 {
							answeredTo = ttl
						}
					}
					done = reached || fatal != nil
					if !done && hi < maxHops && ctx.Err() == nil {
						emit(buildPath(kind, dst, r, true, false, hops, hi, describe, note))
					}
				})
				if done || ctx.Err() != nil {
					break
				}
			}
			locked(func() {
				if !reached {
					// The destination stayed silent: later rounds stop a few
					// hops past the last one that answered.
					limit = min(answeredTo+3, maxHops)
				}
			})
		} else {
			var to int
			locked(func() { to = limit }) // probes may lower limit; launch against a snapshot
			probe(1, to)
		}
		if fatal != nil {
			return fatal
		}
		if ctx.Err() != nil {
			emit(buildPath(kind, dst, r, false, reached, hops, limit, describe, note))
			return nil
		}
		// Hops beyond the destination were probed this round; drop their counts.
		for ttl := limit + 1; ttl <= maxHops; ttl++ {
			hops[ttl] = &hopState{addrs: map[netip.Addr]int{}, status: StatusTimeout}
		}
		done := rounds > 0 && r == rounds
		if !reached && r >= 2 && kind == "trace" {
			note = "Destination did not answer; it may filter ICMP echo."
		}
		emit(buildPath(kind, dst, r, !done, reached, hops, limit, describe, note))
		if done {
			return nil
		}
		if wait := interval - time.Since(start); wait > 0 {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(wait):
			}
		}
	}
	return nil
}

func buildPath(kind string, dst netip.Addr, rounds int, running, reached bool, hops []*hopState, limit int, describe Describe, note string) PathReport {
	rep := PathReport{Kind: kind, Target: dst.String(), Rounds: rounds, Running: running, Reached: reached, Note: note}
	// Trim trailing all-timeout hops when the destination never answered.
	last := limit
	if !reached {
		for last > 1 && hops[last].st.recv == 0 {
			last--
		}
		if last < limit {
			last++ // keep one "???" row to show where it stops answering
		}
	}
	for ttl := 1; ttl <= last; ttl++ {
		h := hops[ttl]
		row := Hop{TTL: ttl, Sent: h.st.sent, Recv: h.st.recv, Loss: round2(h.st.loss()), Status: h.status.String()}
		if h.st.recv > 0 {
			row.Last, row.Avg, row.Best, row.Worst, row.StDev = round2(h.st.last), round2(h.st.mean), round2(h.st.best), round2(h.st.worst), round2(h.st.stdev())
		}
		if h.last.IsValid() {
			row.Addr = h.last.String()
			if describe != nil {
				row.HopInfo = describe(h.last)
			}
			for a := range h.addrs {
				if a != h.last && len(row.Others) < 4 {
					row.Others = append(row.Others, a.String())
				}
			}
		}
		rep.Hops = append(rep.Hops, row)
	}
	markZones(rep.Hops)
	return rep
}

// ---------------------------------------------------------------- ping / TCP

// Sample is one ping or TCP-connect attempt.
type Sample struct {
	Seq    int     `json:"seq"`
	RTT    float64 `json:"rtt"` // ms; -1 when lost
	Status string  `json:"status"`
	TTL    int     `json:"ttl,omitempty"`
	From   string  `json:"from,omitempty"`
}

// SeriesReport is a snapshot of a running ping or TCP check.
type SeriesReport struct {
	Kind    string   `json:"kind"`
	Target  string   `json:"target"`
	Port    int      `json:"port,omitempty"`
	Running bool     `json:"running"`
	Sent    int      `json:"sent"`
	Recv    int      `json:"recv"`
	Loss    float64  `json:"loss"`
	Min     float64  `json:"min"`
	Avg     float64  `json:"avg"`
	Max     float64  `json:"max"`
	StDev   float64  `json:"stdev"`
	Jitter  float64  `json:"jitter"`
	Samples []Sample `json:"samples"`
	Note    string   `json:"note,omitempty"`
}

// RunSeries repeats a single probe every interval (count 0 = until ctx is done).
func RunSeries(ctx context.Context, kind string, dst netip.Addr, port, count int, interval time.Duration,
	probe func(ctx context.Context) Sample, emit func(SeriesReport)) {
	var st stats
	var samples []Sample
	rep := func(running bool) SeriesReport {
		r := SeriesReport{Kind: kind, Target: dst.String(), Port: port, Running: running,
			Sent: st.sent, Recv: st.recv, Loss: round2(st.loss()), Samples: samples}
		if st.recv > 0 {
			r.Min, r.Avg, r.Max, r.StDev, r.Jitter = round2(st.best), round2(st.mean), round2(st.worst), round2(st.stdev()), round2(st.jitter())
		}
		return r
	}
	for seq := 1; count == 0 || seq <= count; seq++ {
		start := time.Now()
		s := probe(ctx)
		if ctx.Err() != nil {
			break
		}
		s.Seq = seq
		st.sent++
		if s.RTT >= 0 {
			st.add(s.RTT)
		}
		samples = append(samples, s)
		if len(samples) > 120 {
			samples = samples[len(samples)-120:]
		}
		emit(rep(true))
		if count > 0 && seq == count {
			break
		}
		if wait := interval - time.Since(start); wait > 0 {
			select {
			case <-ctx.Done():
			case <-time.After(wait):
			}
		}
		if ctx.Err() != nil {
			break
		}
	}
	emit(rep(false))
}

// PingProbe sends one ICMP echo at the default TTL.
func PingProbe(echo EchoFunc, dst netip.Addr) func(ctx context.Context) Sample {
	return func(ctx context.Context) Sample {
		res, err := echo(ctx, dst, 128, 2*time.Second)
		s := Sample{RTT: -1, Status: res.Status.String()}
		if err != nil {
			s.Status = "error"
			return s
		}
		if res.From.IsValid() {
			s.From = res.From.String()
		}
		if res.Status == StatusReply {
			s.RTT = round2(float64(res.RTT.Microseconds()) / 1000)
			s.TTL = res.TTL
		}
		return s
	}
}

// TCPProbe times a TCP handshake to dst:port.
func TCPProbe(dst netip.Addr, port int) func(ctx context.Context) Sample {
	addr := net.JoinHostPort(dst.String(), strconv.Itoa(port))
	return func(ctx context.Context) Sample {
		d := net.Dialer{Timeout: 3 * time.Second}
		start := time.Now()
		c, err := d.DialContext(ctx, "tcp", addr)
		el := time.Since(start)
		if err == nil {
			c.Close()
			return Sample{RTT: round2(float64(el.Microseconds()) / 1000), Status: "open"}
		}
		var oe *net.OpError
		status := "timeout"
		if errors.As(err, &oe) && !oe.Timeout() {
			status = "refused"
			if el > 2900*time.Millisecond {
				status = "timeout"
			}
		}
		if ctx.Err() != nil {
			status = "stopped"
		}
		return Sample{RTT: -1, Status: status}
	}
}

// EchoFunc sends one ICMP echo request with the given TTL and waits for the
// matching reply or ICMP error.
type EchoFunc func(ctx context.Context, dst netip.Addr, ttl int, timeout time.Duration) (Result, error)

// Echo is the platform implementation.
var Echo EchoFunc = echoPlatform
