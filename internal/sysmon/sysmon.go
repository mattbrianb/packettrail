// Package sysmon samples system resources (CPU, memory, disks, network
// adapters, the TCP/IP stack and processes) every couple of seconds, keeps a
// short history and points out things that commonly cause network trouble.
package sysmon

import (
	"fmt"
	"math"
	"sort"
	"sync"
	"time"
)

// CPU is utilisation in percent of the whole machine.
type CPU struct {
	Busy    float64   `json:"busy"`
	User    float64   `json:"user"`
	System  float64   `json:"system"`
	DPC     float64   `json:"dpc"`    // Windows: deferred procedure calls
	IRQ     float64   `json:"irq"`    // interrupts (Windows: hardware; Linux: irq+softirq)
	IOWait  float64   `json:"iowait"` // Linux
	Steal   float64   `json:"steal"`  // Linux VMs
	Cores   []float64 `json:"cores"`
	Load    []float64 `json:"load,omitempty"` // 1, 5, 15 min (Linux/macOS)
	Count   int       `json:"count"`
	HasDPC  bool      `json:"hasDpc"`
	HasWait bool      `json:"hasWait"`
}

// Mem is physical memory, swap and (Windows) commit.
type Mem struct {
	Total       uint64  `json:"total"`
	Used        uint64  `json:"used"`
	Avail       uint64  `json:"avail"`
	Pct         float64 `json:"pct"`
	SwapTotal   uint64  `json:"swapTotal"`
	SwapUsed    uint64  `json:"swapUsed"`
	CommitUsed  uint64  `json:"commitUsed"`
	CommitLimit uint64  `json:"commitLimit"`
}

// Disk is one mounted volume.
type Disk struct {
	Mount string  `json:"mount"`
	Label string  `json:"label"`
	FS    string  `json:"fs"`
	Total uint64  `json:"total"`
	Free  uint64  `json:"free"`
	Pct   float64 `json:"pct"` // used
}

// DiskIO is machine-wide disk throughput.
type DiskIO struct {
	Has      bool    `json:"has"`
	Split    bool    `json:"split"` // read/write reported separately
	Read     float64 `json:"read"`  // bytes/s (total when !Split)
	Write    float64 `json:"write"`
	Busy     float64 `json:"busy"`    // % of time busy, -1 = unknown
	Latency  float64 `json:"latency"` // ms per transfer, -1 = unknown
	QueueLen float64 `json:"queue"`   // -1 = unknown
}

// Iface is a network adapter.
type Iface struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Desc     string  `json:"desc"`
	Kind     string  `json:"kind"` // ethernet, wifi, cellular, vpn, virtual, other
	Physical bool    `json:"physical"`
	Up       bool    `json:"up"`
	Speed    uint64  `json:"speed"` // link speed, bits/s (receive side); 0 = unknown
	SpeedTx  uint64  `json:"speedTx"`
	MTU      int     `json:"mtu"`
	MAC      string  `json:"mac"`
	Rx       float64 `json:"rx"` // bytes/s
	Tx       float64 `json:"tx"`
	RxUtil   float64 `json:"rxUtil"` // % of link speed, -1 = unknown
	TxUtil   float64 `json:"txUtil"`
	RxBytes  uint64  `json:"rxBytes"` // since boot
	TxBytes  uint64  `json:"txBytes"`
	Errs     uint64  `json:"errs"` // in+out errors since boot
	Discards uint64  `json:"discards"`
	// Over roughly the last minute:
	ErrsRecent     uint64 `json:"errsRecent"`
	DiscardsRecent uint64 `json:"discardsRecent"`
}

// TCP is the stack-wide TCP/UDP picture, per second.
type TCP struct {
	Has         bool    `json:"has"`
	OutSegs     float64 `json:"outSegs"`
	InSegs      float64 `json:"inSegs"`
	Retrans     float64 `json:"retrans"`
	RetransPct  float64 `json:"retransPct"`  // this sample
	RetransPct1 float64 `json:"retransPct1"` // last minute; -1 = not enough traffic
	Resets      float64 `json:"resets"`      // resets sent
	Fails       float64 `json:"fails"`       // failed connection attempts
	Estab       int     `json:"estab"`
	InErrs      float64 `json:"inErrs"`  // TCP segments received in error
	UDPErrs     float64 `json:"udpErrs"` // UDP receive errors (mostly full buffers)
	UDPIn       float64 `json:"udpIn"`
	UDPOut      float64 `json:"udpOut"`
}

// Proc is one process.
type Proc struct {
	PID     int     `json:"pid"`
	Name    string  `json:"name"`
	Path    string  `json:"path,omitempty"`
	CPU     float64 `json:"cpu"` // % of the whole machine
	Mem     uint64  `json:"mem"` // private working set / resident
	IO      float64 `json:"io"`  // bytes/s of all I/O, -1 = unknown
	Threads int     `json:"threads"`
	Handles int     `json:"handles"` // -1 = unknown
	// Filled in by the web layer from the capture engine:
	App    string  `json:"app,omitempty"`
	NetIn  float64 `json:"netIn"`
	NetOut float64 `json:"netOut"`
}

// Hint is something worth looking at.
type Hint struct {
	Level string `json:"level"` // bad, warn, info
	Area  string `json:"area"`  // cpu, mem, disk, net, tcp
	Title string `json:"title"`
	Text  string `json:"text"`
}

// Snapshot is one sample.
type Snapshot struct {
	Time    int64    `json:"t"` // unix ms
	Uptime  int64    `json:"uptime"`
	CPU     CPU      `json:"cpu"`
	Mem     Mem      `json:"mem"`
	Disks   []Disk   `json:"disks"`
	DiskIO  DiskIO   `json:"diskIO"`
	Ifaces  []Iface  `json:"ifaces"`
	TCP     TCP      `json:"tcp"`
	Procs   []Proc   `json:"procs"`
	Hints   []Hint   `json:"hints"`
	Notes   []string `json:"notes"`
	Elapsed float64  `json:"elapsed"` // seconds this sample covers
}

// Point is one history sample for the charts.
type Point struct {
	T        int64   `json:"t"` // unix seconds
	CPU      float64 `json:"cpu"`
	MaxCore  float64 `json:"maxCore"`
	DPC      float64 `json:"dpc"` // dpc + irq
	Mem      float64 `json:"mem"`
	DiskBusy float64 `json:"diskBusy"`
	DiskR    float64 `json:"diskR"`
	DiskW    float64 `json:"diskW"`
	DiskLat  float64 `json:"diskLat"`
	NetRx    float64 `json:"netRx"`
	NetTx    float64 `json:"netTx"`
	OutSegs  float64 `json:"outSegs"`
	Retrans  float64 `json:"retrans"`
	UDPErrs  float64 `json:"udpErrs"`
}

// source is the platform part: it returns a snapshot with rates computed
// against its own previous reading.
type source interface {
	sample(now time.Time) *Snapshot
	close()
}

const (
	histKeep   = 15 * time.Minute
	errWindow  = 60 * time.Second
	hintWindow = 30 * time.Second
)

type ifCount struct {
	t          time.Time
	errs, disc uint64
}

// Sampler collects snapshots in the background.
type Sampler struct {
	interval time.Duration
	src      source
	mu       sync.Mutex
	last     *Snapshot
	hist     []Point
	ifHist   map[string][]ifCount
	stop     chan struct{}
	done     chan struct{}
}

// New creates a sampler; call Start to begin.
func New(interval time.Duration) *Sampler {
	if interval < time.Second {
		interval = 2 * time.Second
	}
	return &Sampler{interval: interval, ifHist: map[string][]ifCount{}}
}

// Start begins sampling in the background.
func (s *Sampler) Start() {
	if s.stop != nil {
		return
	}
	s.src = newSource()
	s.stop, s.done = make(chan struct{}), make(chan struct{})
	go func() {
		defer close(s.done)
		s.src.sample(time.Now()) // baseline for rates
		t := time.NewTicker(s.interval)
		defer t.Stop()
		for {
			select {
			case <-s.stop:
				return
			case now := <-t.C:
				s.add(s.src.sample(now), now)
			}
		}
	}()
}

// Stop ends sampling.
func (s *Sampler) Stop() {
	if s.stop == nil {
		return
	}
	close(s.stop)
	<-s.done
	s.src.close()
}

// Latest returns the most recent snapshot (nil before the first one).
func (s *Sampler) Latest() *Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

// History returns the chart points, oldest first.
func (s *Sampler) History() []Point {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Point(nil), s.hist...)
}

func (s *Sampler) add(snap *Snapshot, now time.Time) {
	if snap == nil {
		return
	}
	snap.Time = now.UnixMilli()
	s.mu.Lock()
	defer s.mu.Unlock()
	// Errors and discards over the last minute, per adapter.
	for i := range snap.Ifaces {
		f := &snap.Ifaces[i]
		h := append(s.ifHist[f.ID], ifCount{now, f.Errs, f.Discards})
		for len(h) > 1 && now.Sub(h[0].t) > errWindow {
			h = h[1:]
		}
		s.ifHist[f.ID] = h
		if f.Errs >= h[0].errs {
			f.ErrsRecent = f.Errs - h[0].errs
		}
		if f.Discards >= h[0].disc {
			f.DiscardsRecent = f.Discards - h[0].disc
		}
	}
	rx, tx := primaryTraffic(snap.Ifaces)
	p := Point{T: now.Unix(), CPU: snap.CPU.Busy, DPC: snap.CPU.DPC + snap.CPU.IRQ, Mem: snap.Mem.Pct,
		DiskBusy: snap.DiskIO.Busy, DiskR: snap.DiskIO.Read, DiskW: snap.DiskIO.Write, DiskLat: snap.DiskIO.Latency,
		NetRx: rx, NetTx: tx, OutSegs: snap.TCP.OutSegs * snap.Elapsed, Retrans: snap.TCP.Retrans * snap.Elapsed,
		UDPErrs: snap.TCP.UDPErrs * snap.Elapsed}
	for _, c := range snap.CPU.Cores {
		p.MaxCore = math.Max(p.MaxCore, c)
	}
	s.hist = append(s.hist, p)
	for len(s.hist) > 0 && now.Unix()-s.hist[0].T > int64(histKeep/time.Second) {
		s.hist = s.hist[1:]
	}
	snap.TCP.RetransPct1 = -1
	if out, re, _ := sums(s.hist, now, errWindow); out >= 500 {
		snap.TCP.RetransPct1 = 100 * re / out
	}
	snap.Hints = hints(snap, s.hist, now)
	s.last = snap
}

// primaryTraffic sums the physical adapters (or, if none are known, every
// non-virtual adapter) so VPN tunnels are not counted twice.
func primaryTraffic(ifs []Iface) (rx, tx float64) {
	phys := false
	for _, f := range ifs {
		if f.Physical && f.Up {
			phys = true
			break
		}
	}
	for _, f := range ifs {
		if (phys && f.Physical) || (!phys && f.Kind != "vpn" && f.Kind != "virtual") {
			rx += f.Rx
			tx += f.Tx
		}
	}
	return
}

func sums(h []Point, now time.Time, w time.Duration) (out, retrans, udpErrs float64) {
	cut := now.Add(-w).Unix()
	for i := len(h) - 1; i >= 0 && h[i].T > cut; i-- {
		out += h[i].OutSegs
		retrans += h[i].Retrans
		udpErrs += h[i].UDPErrs
	}
	return
}

// window returns the points of the last d, and whether they cover most of it.
func window(h []Point, now time.Time, d time.Duration) ([]Point, bool) {
	cut := now.Add(-d).Unix()
	i := len(h)
	for i > 0 && h[i-1].T > cut {
		i--
	}
	w := h[i:]
	return w, len(w) >= 3 && now.Unix()-w[0].T >= int64(d/time.Second)*2/3
}

func minOf(w []Point, f func(Point) float64) float64 {
	m := math.Inf(1)
	for _, p := range w {
		m = math.Min(m, f(p))
	}
	return m
}

func meanOf(w []Point, f func(Point) float64) float64 {
	if len(w) == 0 {
		return 0
	}
	t := 0.0
	for _, p := range w {
		t += f(p)
	}
	return t / float64(len(w))
}

// FmtBytes formats a byte count for hint text.
func FmtBytes(n uint64) string {
	f := float64(n)
	for _, u := range []string{"B", "kB", "MB", "GB", "TB"} {
		if f < 1000 || u == "TB" {
			if f < 10 && u != "B" {
				return fmt.Sprintf("%.1f %s", f, u)
			}
			return fmt.Sprintf("%.0f %s", f, u)
		}
		f /= 1000
	}
	return ""
}

func fmtSpeed(bps uint64) string {
	switch {
	case bps >= 1e9:
		g := float64(bps) / 1e9
		if g == math.Trunc(g) {
			return fmt.Sprintf("%.0f Gbit/s", g)
		}
		return fmt.Sprintf("%.1f Gbit/s", g)
	case bps >= 1e6:
		return fmt.Sprintf("%.0f Mbit/s", float64(bps)/1e6)
	default:
		return fmt.Sprintf("%.0f kbit/s", float64(bps)/1e3)
	}
}

func topProc(ps []Proc, by func(Proc) float64) (Proc, bool) {
	var best Proc
	ok := false
	for _, p := range ps {
		if !ok || by(p) > by(best) {
			best, ok = p, true
		}
	}
	return best, ok
}

// hints looks for the usual suspects, preferring sustained conditions over
// momentary spikes.
func hints(s *Snapshot, h []Point, now time.Time) []Hint {
	var out []Hint
	add := func(level, area, title, text string, a ...any) {
		out = append(out, Hint{Level: level, Area: area, Title: title, Text: fmt.Sprintf(text, a...)})
	}
	w, full := window(h, now, hintWindow)
	busiest := ""
	if p, ok := topProc(s.Procs, func(p Proc) float64 { return p.CPU }); ok && p.CPU >= 5 {
		busiest = fmt.Sprintf(" Busiest: %s (%.0f%%).", p.Name, p.CPU)
	}
	if full {
		if c := minOf(w, func(p Point) float64 { return p.CPU }); c >= 90 {
			add("bad", "cpu", "CPU is maxed out", "CPU has stayed above 90%% for the last 30 s (now %.0f%%). Packet processing and timers get delayed, which shows up as jitter.%s", s.CPU.Busy, busiest)
		} else if c >= 75 {
			add("warn", "cpu", "CPU is very busy", "CPU has stayed above 75%% for the last 30 s (now %.0f%%).%s", s.CPU.Busy, busiest)
		} else if mc := minOf(w, func(p Point) float64 { return p.MaxCore }); mc >= 95 && s.CPU.Count > 1 {
			add("warn", "cpu", "One CPU core is pinned", "A single core has been at ~100%% for 30 s while the total is only %.0f%%. Single-threaded work (an app, or a driver handling all network interrupts on one core) can add latency even when total CPU looks fine.%s", s.CPU.Busy, busiest)
		}
		if s.CPU.HasDPC {
			if d := meanOf(w, func(p Point) float64 { return p.DPC }); d >= 8 {
				lvl := "warn"
				if d >= 15 {
					lvl = "bad"
				}
				add(lvl, "cpu", "High interrupt / DPC time", "%.0f%% of CPU time over the last 30 s went to interrupts and deferred procedure calls. That is almost always a driver (network, Wi-Fi, storage or audio) and causes latency spikes and dropped packets. Update or roll back the driver.", d)
			}
		}
		if s.CPU.HasWait {
			if d := meanOf(w, func(p Point) float64 { return p.DPC }); d >= 10 {
				add("warn", "cpu", "High interrupt time", "%.0f%% of CPU time is spent in hardware and soft interrupts, often network receive processing. Check that receive-side scaling spreads it over several cores.", d)
			}
		}
		if s.CPU.HasWait && s.CPU.IOWait >= 20 {
			add("warn", "disk", "CPU waiting on disk", "%.0f%% of CPU time is spent waiting for disk I/O.", s.CPU.IOWait)
		}
		if s.CPU.Steal >= 10 {
			add("warn", "cpu", "CPU steal time", "%.0f%% of CPU time is being taken by the hypervisor; this VM is short of real CPU.", s.CPU.Steal)
		}
		if b := meanOf(w, func(p Point) float64 { return p.DiskBusy }); s.DiskIO.Busy >= 0 && b >= 90 {
			add("warn", "disk", "Disk is saturated", "The disk has been busy %.0f%% of the time for 30 s. Anything that touches the disk (including apps that log or cache network data) will stall.", b)
		}
		if l := meanOf(w, func(p Point) float64 { return p.DiskLat }); s.DiskIO.Latency >= 0 && l >= 50 {
			add("warn", "disk", "Slow disk responses", "Disk requests are taking %.0f ms on average over the last 30 s (healthy SSDs answer in under 5 ms).", l)
		}
	}
	if len(s.CPU.Load) > 0 && s.CPU.Count > 0 && s.CPU.Load[0] > 2*float64(s.CPU.Count) {
		add("warn", "cpu", "High load average", "Load average is %.1f on %d cores: more work is queued than the CPU can run.", s.CPU.Load[0], s.CPU.Count)
	}
	m := s.Mem
	if m.Pct >= 92 {
		add("bad", "mem", "Memory is nearly full", "%.0f%% of memory is in use (%s free). The system is paging to disk, which makes everything slow and bursty.", m.Pct, FmtBytes(m.Avail))
	} else if m.Pct >= 85 {
		add("warn", "mem", "Memory is running low", "%.0f%% of memory is in use (%s free).", m.Pct, FmtBytes(m.Avail))
	}
	if m.CommitLimit > 0 && float64(m.CommitUsed) >= 0.9*float64(m.CommitLimit) {
		add("warn", "mem", "Commit limit almost reached", "%s of %s committed. Apps may fail to allocate memory; a bigger page file or fewer apps will help.", FmtBytes(m.CommitUsed), FmtBytes(m.CommitLimit))
	}
	if m.SwapTotal > 0 && float64(m.SwapUsed) >= 0.5*float64(m.SwapTotal) && m.SwapUsed > 1<<30 {
		add("warn", "mem", "Heavy swap use", "%s of %s swap is in use.", FmtBytes(m.SwapUsed), FmtBytes(m.SwapTotal))
	}
	for _, d := range s.Disks {
		if d.Total < 1<<30 {
			continue
		}
		free := float64(d.Free) / float64(d.Total) * 100
		// A percentage alone over-warns on big disks: 10% of 4 TB is plenty.
		switch {
		case d.Free < 2<<30 || (free < 3 && d.Free < 10<<30):
			add("bad", "disk", "Disk "+d.Mount+" is almost full", "Only %s free (%.1f%%). Updates, caches and logs start failing when a disk fills.", FmtBytes(d.Free), free)
		case free < 10 && d.Free < 20<<30:
			add("warn", "disk", "Disk "+d.Mount+" is getting full", "%s free (%.0f%%).", FmtBytes(d.Free), free)
		}
	}
	for _, f := range s.Ifaces {
		if !f.Up {
			continue
		}
		name := f.Name
		if f.ErrsRecent > 0 {
			lvl := "warn"
			if f.ErrsRecent >= 50 {
				lvl = "bad"
			}
			add(lvl, "net", name+" has errors", "%d packet errors on this adapter in the last minute (%d since boot). On Ethernet that points at the cable, the switch port or a duplex mismatch; on Wi-Fi, at interference or signal.", f.ErrsRecent, f.Errs)
		}
		if f.DiscardsRecent >= 200 {
			add("info", "net", name+" is discarding packets", "%d packets were discarded in the last minute. Some discards are normal (unwanted multicast); a steady stream during heavy traffic means receive buffers are overflowing.", f.DiscardsRecent)
		}
		if f.Speed > 0 {
			util := math.Max(f.RxUtil, f.TxUtil)
			if util >= 85 {
				dir := "download"
				if f.TxUtil > f.RxUtil {
					dir = "upload"
				}
				add("warn", "net", name+" link is saturated", "The %s side is at %.0f%% of the %s link. The local link, not the internet path, is the bottleneck right now.", dir, util, fmtSpeed(f.Speed))
			}
			if f.Kind == "ethernet" && f.Physical && f.Speed <= 100e6 {
				add("warn", "net", name+" negotiated "+fmtSpeed(f.Speed), "This Ethernet link came up at %s. Gigabit needs all four cable pairs, so this usually means a damaged cable or a port forced to a lower speed.", fmtSpeed(f.Speed))
			}
			if f.Kind == "wifi" && f.Speed < 50e6 {
				add("info", "net", name+" link rate is low", "Wi-Fi is connected at only %s. Weak signal, interference or an old band or standard; expect slow and jittery transfers.", fmtSpeed(f.Speed))
			}
		}
	}
	// At low volume a few SYN retries to unreachable hosts look like heavy
	// loss, so only judge the rate once there is real traffic.
	out1, re1, u := sums(h, now, errWindow)
	if s.TCP.Has && s.TCP.RetransPct1 >= 2 && out1 >= 3000 && re1 >= 30 {
		lvl := "warn"
		if s.TCP.RetransPct1 >= 5 {
			lvl = "bad"
		}
		add(lvl, "tcp", "TCP is retransmitting", "%.1f%% of the TCP segments this PC sent in the last minute were retransmissions, so packets are being lost somewhere. Check the network adapters below for errors first, then the per-network quality on the Networks tab to see which paths are affected.", s.TCP.RetransPct1)
	}
	if u >= 50 {
		add("info", "tcp", "UDP packets dropped on receive", "%.0f UDP datagrams were dropped in the last minute because an app's socket buffer was full. Voice, video and games show this as choppy audio or rubber-banding.", u)
	}
	order := map[string]int{"bad": 0, "warn": 1, "info": 2}
	sort.SliceStable(out, func(i, j int) bool { return order[out[i].Level] < order[out[j].Level] })
	return out
}

// rate turns two counter readings into a per-second rate, treating a
// counter that went backwards (wrapped or reset) as no change.
func rate(cur, prev uint64, secs float64) float64 {
	if cur < prev || secs <= 0 {
		return 0
	}
	return float64(cur-prev) / secs
}

func rate32(cur, prev uint32, secs float64) float64 {
	if secs <= 0 {
		return 0
	}
	return float64(cur-prev) / secs // uint32 subtraction handles one wrap
}

func pct(part, whole float64) float64 {
	if whole <= 0 {
		return 0
	}
	return math.Max(0, math.Min(100, 100*part/whole))
}

func utilOf(bytesPerSec float64, bitsPerSec uint64) float64 {
	if bitsPerSec == 0 {
		return -1
	}
	return math.Min(100, 100*bytesPerSec*8/float64(bitsPerSec))
}

// procPrev remembers a process's counters between samples.
type procPrev struct {
	start uint64 // creation time or start ticks, to spot PID reuse
	cpu   uint64
	io    uint64
	seen  bool
}
