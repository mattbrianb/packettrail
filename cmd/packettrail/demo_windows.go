//go:build windows

package main

import (
	"hash/fnv"
	"math/rand/v2"
	"net/netip"
	"sync"
	"time"

	"packettrail/internal/capture"
)

// demoCapture is a test aid (--demo): it reports the machine's real TCP
// connections with synthetic byte counts, so the dashboard, history and
// exports can be exercised without administrator rights. Status says DEMO.
type demoCapture struct {
	stop chan struct{}
	once sync.Once
	n    uint64
}

func newDemoCapture() capture.Capturer { return &demoCapture{stop: make(chan struct{})} }

func (d *demoCapture) Start(sink capture.Sink) error {
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-d.stop:
				return
			case now := <-t.C:
				d.tick(sink, now)
			}
		}
	}()
	return nil
}

func (d *demoCapture) tick(sink capture.Sink, now time.Time) {
	rows, err := capture.ReadConnTables(false)
	if err != nil {
		return
	}
	var evs []capture.FlowEvent
	for _, r := range rows {
		if r.State != 5 || r.PID == 0 || r.Remote.Addr().IsLoopback() {
			continue
		}
		h := fnv.New32a()
		h.Write(r.Remote.Addr().AsSlice())
		w := float64(h.Sum32()%100) / 100
		in := uint64(w * w * w * 400000 * (0.5 + rand.Float64()))
		out := uint64(float64(in)*0.08) + uint64(rand.IntN(400))
		// Synthetic path quality, stable per destination: 3-180 ms RTT and a
		// small retransmit rate that is worse on a few "bad" paths.
		rtt := 3 + w*w*177 + rand.Float64()*4
		rtx := uint64(0)
		if h.Sum32()%7 == 0 {
			rtx = out / 25
		} else if rand.IntN(4) == 0 {
			rtx = out / 400
		}
		evs = append(evs, capture.FlowEvent{Time: now, Span: time.Second, PID: r.PID, Proto: capture.TCP,
			Local: r.Local, Remote: r.Remote, Dir: capture.DirOut, In: in, Out: out,
			Retrans: rtx, RetransPkts: rtx / 1400, RTT: rtt, HasRTT: true,
			SegsIn: in / 1400, LossIn: lossIn(in/1400, h.Sum32()%5 == 0)})
		// a QUIC-like UDP flow next to some TCP ones
		if w > 0.8 {
			evs = append(evs, capture.FlowEvent{Time: now, Span: time.Second, PID: r.PID, Proto: capture.UDP,
				Local: netip.AddrPortFrom(r.Local.Addr(), 50000+r.Local.Port()%1000), Remote: netip.AddrPortFrom(r.Remote.Addr(), 443),
				In: in / 3, Out: out / 3})
		}
	}
	d.n += uint64(len(evs))
	sink.Flows(evs)
}

func (d *demoCapture) Stop() { d.once.Do(func() { close(d.stop) }) }

func (d *demoCapture) Status() capture.Status {
	return capture.Status{Backend: "demo", Mode: "full", Privileged: true, Events: d.n,
		Detail: "DEMO DATA: real connections with synthetic byte counts (--demo)"}
}

// lossIn simulates download-side loss events: ~0.05% normally, ~1.5% on bad paths.
func lossIn(segs uint64, bad bool) uint64 {
	rate := 0.0005
	if bad {
		rate = 0.015
	}
	n := float64(segs) * rate
	if rand.Float64() < n-float64(uint64(n)) {
		return uint64(n) + 1
	}
	return uint64(n)
}
