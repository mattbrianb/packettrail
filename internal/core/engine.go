// Package core aggregates capture events into live per-app / per-flow
// statistics and queues them for the history store.
package core

import (
	"net"
	"net/netip"
	"sort"
	"sync"
	"time"

	"packettrail/internal/capture"
)

const (
	ringN        = 300 // seconds of per-second history kept in memory
	miniN        = 4
	BucketSecs   = 300 // flow history bucket (5 minutes)
	SeriesSecs   = 60  // per-app time series bucket (1 minute)
	idleEvict    = 6 * time.Hour
	pidKeep      = 10 * time.Minute
	unknownApp   = "(unknown)"
	maxSessFlows = 200000
)

// FlowKey identifies one row of the per-app remote endpoint table.
type FlowKey struct {
	App   string
	Proto capture.Proto
	IP    netip.Addr
	Port  uint16 // remote port for outbound flows, local port for inbound
	Dir   capture.Dir
}

// FlowRow is the common shape for session and history data.
type FlowRow struct {
	FlowKey
	In, Out     uint64
	First, Last int64
	Host        string

	// Passive path quality: retransmitted bytes/segments and the sum and
	// count of RTT samples (so averages can be taken over any period).
	Retrans, RetransPkts uint64
	RTTSum               float64
	RTTN                 uint64
	LossIn, SegsIn       uint64 // download-side loss events and segments received
}

// addQuality folds an event's path-quality fields into a row.
func (r *FlowRow) addQuality(ev *capture.FlowEvent) {
	r.Retrans += ev.Retrans
	r.RetransPkts += ev.RetransPkts
	if ev.HasRTT {
		r.RTTSum += ev.RTT
		r.RTTN++
	}
	r.LossIn += ev.LossIn
	r.SegsIn += ev.SegsIn
}

// AppMeta describes an application.
type AppMeta struct {
	Key, Name, Path string
	PIDs            []int
	First, Last     int64
}

type ring struct {
	sec [ringN]int64
	in  [ringN]uint64
	out [ringN]uint64
}

func (r *ring) add(sec int64, in, out uint64) {
	i := sec % ringN
	if r.sec[i] != sec {
		if r.sec[i] > sec { // slot already holds newer data; event too old
			return
		}
		r.sec[i], r.in[i], r.out[i] = sec, 0, 0
	}
	r.in[i] += in
	r.out[i] += out
}

func (r *ring) at(sec int64) (uint64, uint64) {
	i := sec % ringN
	if r.sec[i] == sec {
		return r.in[i], r.out[i]
	}
	return 0, 0
}

type miniRing struct {
	sec [miniN]int64
	in  [miniN]uint64
	out [miniN]uint64
}

func (r *miniRing) add(sec int64, in, out uint64) {
	i := sec % miniN
	if r.sec[i] != sec {
		if r.sec[i] > sec {
			return
		}
		r.sec[i], r.in[i], r.out[i] = sec, 0, 0
	}
	r.in[i] += in
	r.out[i] += out
}

func (r *miniRing) at(sec int64) (uint64, uint64) {
	i := sec % miniN
	if r.sec[i] == sec {
		return r.in[i], r.out[i]
	}
	return 0, 0
}

type app struct {
	AppMeta
	pids    map[int]int64
	in, out uint64
	r       ring
}

type flow struct {
	FlowRow
	pid int
	r   miniRing
}

type pendKey struct {
	Bucket int64
	FlowKey
}

// PendFlow is a not-yet-persisted flow delta.
type PendFlow struct {
	Bucket int64
	FlowRow
}

type seriesKey struct {
	Minute int64
	App    string
}

// PendSeries is a not-yet-persisted per-app per-minute delta.
type PendSeries struct {
	Minute  int64
	App     string
	In, Out uint64
}

// Engine is the live aggregation state. It implements capture.Sink.
type Engine struct {
	mu         sync.Mutex
	procs      capture.ProcResolver
	start      time.Time
	apps       map[string]*app
	flows      map[FlowKey]*flow
	total      ring
	totIn      uint64
	totOut     uint64
	pendFlows  map[pendKey]*PendFlow
	pendSeries map[seriesKey]*PendSeries
	dirtyApps  map[string]bool
	DNS        *DNSMap
	local      *localAddrs
	Loopback   bool // include traffic to 127.0.0.1/::1
	events     uint64
}

func NewEngine(procs capture.ProcResolver) *Engine {
	e := &Engine{
		procs:      procs,
		start:      time.Now(),
		apps:       map[string]*app{},
		flows:      map[FlowKey]*flow{},
		pendFlows:  map[pendKey]*PendFlow{},
		pendSeries: map[seriesKey]*PendSeries{},
		dirtyApps:  map[string]bool{},
		DNS:        NewDNSMap(),
		local:      newLocalAddrs(),
	}
	return e
}

func (e *Engine) Start() time.Time { return e.start }

type sinkAdapter struct{ e *Engine }

func (s sinkAdapter) Flows(evs []capture.FlowEvent) { s.e.Flows(evs) }
func (s sinkAdapter) DNS(ev capture.DNSEvent)       { s.e.DNS.Add(ev) }

// Sink returns the capture.Sink view of the engine.
func (e *Engine) Sink() capture.Sink { return sinkAdapter{e} }

// GuessDir decides flow direction from ports when the backend could not.
func GuessDir(localPort, remotePort uint16) capture.Dir {
	if localPort < 1024 && remotePort >= 1024 {
		return capture.DirIn
	}
	if localPort < remotePort && remotePort >= 32768 && localPort < 32768 {
		return capture.DirIn
	}
	return capture.DirOut
}

// Flows ingests a batch of capture events.
func (e *Engine) Flows(evs []capture.FlowEvent) {
	now := time.Now()
	e.local.refresh(now)
	// Resolve processes outside the engine lock: lookups may call into the OS.
	procs := make([]capture.ProcInfo, len(evs))
	var last int = -1
	for i := range evs {
		if evs[i].Proc != nil {
			procs[i] = *evs[i].Proc
			continue
		}
		if last >= 0 && evs[last].Proc == nil && evs[last].PID == evs[i].PID {
			procs[i] = procs[last]
		} else {
			procs[i] = e.procs.Lookup(evs[i].PID)
		}
		last = i
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range evs {
		e.addLocked(&evs[i], &procs[i], now)
	}
}

func (e *Engine) addLocked(ev *capture.FlowEvent, p *capture.ProcInfo, now time.Time) {
	local, remote := capture.Unmap(ev.Local), capture.Unmap(ev.Remote)
	// Some sources are inconsistent about which address is ours; fix it up.
	if e.local.has(remote.Addr()) && !e.local.has(local.Addr()) && !remote.Addr().IsLoopback() {
		local, remote = remote, local
	}
	rip := remote.Addr()
	if !rip.IsValid() || rip.IsUnspecified() {
		return
	}
	if !e.Loopback && rip.IsLoopback() {
		return
	}
	e.events++
	key := p.Key
	if key == "" {
		key = unknownApp
		p.Name = unknownApp
	}
	dir := ev.Dir
	if dir == capture.DirUnknown {
		dir = GuessDir(local.Port(), remote.Port())
	}
	port := remote.Port()
	if dir == capture.DirIn {
		port = local.Port()
	}
	ts := ev.Time
	if ts.IsZero() || ts.After(now) {
		ts = now
	}
	sec := ts.Unix()

	a := e.apps[key]
	if a == nil {
		a = &app{AppMeta: AppMeta{Key: key, Name: p.Name, Path: p.Path, First: sec}, pids: map[int]int64{}}
		e.apps[key] = a
		e.dirtyApps[key] = true
	}
	if (a.Path == "" && p.Path != "") || (a.Name != p.Name && p.Name != "") {
		if p.Path != "" {
			a.Path = p.Path
		}
		a.Name = p.Name
		e.dirtyApps[key] = true
	}
	if ev.PID > 0 {
		a.pids[ev.PID] = now.Unix()
	}
	a.in += ev.In
	a.out += ev.Out
	if sec > a.Last {
		a.Last = sec
	}

	fk := FlowKey{App: key, Proto: ev.Proto, IP: rip, Port: port, Dir: dir}
	f := e.flows[fk]
	if f == nil {
		if len(e.flows) >= maxSessFlows {
			e.evictLocked(now, time.Hour)
		}
		f = &flow{FlowRow: FlowRow{FlowKey: fk, First: sec}}
		e.flows[fk] = f
	}
	if f.Host == "" || (f.pid != ev.PID && ev.PID > 0) {
		if h := e.DNS.Lookup(ev.PID, rip); h != "" {
			f.Host = h
		}
	}
	f.pid = ev.PID
	f.In += ev.In
	f.Out += ev.Out
	f.addQuality(ev)
	if sec < f.First {
		f.First = sec
	}
	if sec > f.Last {
		f.Last = sec
	}

	if ev.In|ev.Out != 0 {
		e.totIn += ev.In
		e.totOut += ev.Out
		n := int64(ev.Span.Round(time.Second) / time.Second)
		if n <= 1 {
			e.total.add(sec, ev.In, ev.Out)
			a.r.add(sec, ev.In, ev.Out)
			f.r.add(sec, ev.In, ev.Out)
		} else {
			if n > 60 {
				n = 60
			}
			pi, po := ev.In/uint64(n), ev.Out/uint64(n)
			for k := int64(0); k < n; k++ {
				ii, oo := pi, po
				if k == n-1 {
					ii, oo = ev.In-pi*uint64(n-1), ev.Out-po*uint64(n-1)
				}
				s := sec - (n - 1) + k
				e.total.add(s, ii, oo)
				a.r.add(s, ii, oo)
				f.r.add(s, ii, oo)
			}
		}
	}

	pk := pendKey{Bucket: sec - sec%BucketSecs, FlowKey: fk}
	pf := e.pendFlows[pk]
	if pf == nil {
		pf = &PendFlow{Bucket: pk.Bucket, FlowRow: FlowRow{FlowKey: fk, First: sec, Last: sec}}
		e.pendFlows[pk] = pf
	}
	pf.In += ev.In
	pf.Out += ev.Out
	pf.addQuality(ev)
	if sec < pf.First {
		pf.First = sec
	}
	if sec > pf.Last {
		pf.Last = sec
	}
	if f.Host != "" {
		pf.Host = f.Host
	}
	if ev.In|ev.Out != 0 {
		sk := seriesKey{Minute: sec - sec%SeriesSecs, App: key}
		ps := e.pendSeries[sk]
		if ps == nil {
			ps = &PendSeries{Minute: sk.Minute, App: key}
			e.pendSeries[sk] = ps
		}
		ps.In += ev.In
		ps.Out += ev.Out
	}
}

func (e *Engine) evictLocked(now time.Time, idle time.Duration) {
	cut := now.Add(-idle).Unix()
	for k, f := range e.flows {
		if f.Last < cut {
			delete(e.flows, k)
		}
	}
}

// Maintain drops idle in-memory state; call it every minute or so.
func (e *Engine) Maintain() {
	now := time.Now()
	e.mu.Lock()
	e.evictLocked(now, idleEvict)
	pcut := now.Add(-pidKeep).Unix()
	for _, a := range e.apps {
		for pid, t := range a.pids {
			if t < pcut {
				delete(a.pids, pid)
			}
		}
	}
	e.mu.Unlock()
	e.DNS.Maintain(now)
}

// TakePending hands over everything not yet persisted.
func (e *Engine) TakePending() ([]PendFlow, []PendSeries, []AppMeta) {
	e.mu.Lock()
	defer e.mu.Unlock()
	flows := make([]PendFlow, 0, len(e.pendFlows))
	for _, p := range e.pendFlows {
		flows = append(flows, *p)
	}
	series := make([]PendSeries, 0, len(e.pendSeries))
	for _, p := range e.pendSeries {
		series = append(series, *p)
	}
	apps := make([]AppMeta, 0, len(e.dirtyApps))
	for k := range e.dirtyApps {
		if a := e.apps[k]; a != nil {
			apps = append(apps, a.AppMeta)
		}
	}
	e.pendFlows = map[pendKey]*PendFlow{}
	e.pendSeries = map[seriesKey]*PendSeries{}
	e.dirtyApps = map[string]bool{}
	return flows, series, apps
}

// HasPending reports whether a flush would write anything.
func (e *Engine) HasPending() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.pendFlows) > 0 || len(e.pendSeries) > 0
}

// SessionRows returns all in-memory flows (activity since start, minus flows
// idle for longer than the eviction window).
func (e *Engine) SessionRows() []FlowRow {
	e.mu.Lock()
	defer e.mu.Unlock()
	rows := make([]FlowRow, 0, len(e.flows))
	for _, f := range e.flows {
		rows = append(rows, f.FlowRow)
	}
	return rows
}

// Apps returns live metadata for all apps seen this session.
func (e *Engine) Apps() map[string]AppMeta {
	e.mu.Lock()
	defer e.mu.Unlock()
	m := make(map[string]AppMeta, len(e.apps))
	for k, a := range e.apps {
		meta := a.AppMeta
		meta.PIDs = make([]int, 0, len(a.pids))
		for pid := range a.pids {
			meta.PIDs = append(meta.PIDs, pid)
		}
		sort.Ints(meta.PIDs)
		m[k] = meta
	}
	return m
}

// Rate is bytes per second, received and sent.
type Rate struct{ In, Out float64 }

// rateSecs: the most recent complete seconds used for "current" rates. ETW
// delivers events with up to ~0.5 s delay, so the current second is skipped.
func rateWindow(now time.Time) (int64, int64) {
	s := now.Unix()
	return s - 2, s - 1
}

// AppRates returns the current rate per app plus a short sparkline (total
// bytes/s for the last n seconds, oldest first).
func (e *Engine) AppRates(n int) (map[string]Rate, map[string][]float64) {
	now := time.Now()
	a0, a1 := rateWindow(now)
	e.mu.Lock()
	defer e.mu.Unlock()
	rates := make(map[string]Rate, len(e.apps))
	sparks := make(map[string][]float64, len(e.apps))
	for k, a := range e.apps {
		i0, o0 := a.r.at(a0)
		i1, o1 := a.r.at(a1)
		rates[k] = Rate{float64(i0+i1) / 2, float64(o0+o1) / 2}
		if n > 0 {
			sp := make([]float64, n)
			any := false
			for j := 0; j < n; j++ {
				in, out := a.r.at(a1 - int64(n-1-j))
				sp[j] = float64(in + out)
				if sp[j] > 0 {
					any = true
				}
			}
			if any {
				sparks[k] = sp
			}
		}
	}
	return rates, sparks
}

// FlowRates returns the current rate for each active flow.
func (e *Engine) FlowRates() map[FlowKey]Rate {
	now := time.Now()
	a0, a1 := rateWindow(now)
	cut := now.Unix() - 5
	e.mu.Lock()
	defer e.mu.Unlock()
	m := map[FlowKey]Rate{}
	for k, f := range e.flows {
		if f.Last < cut {
			continue
		}
		i0, o0 := f.r.at(a0)
		i1, o1 := f.r.at(a1)
		if i0+i1+o0+o1 > 0 {
			m[k] = Rate{float64(i0+i1) / 2, float64(o0+o1) / 2}
		}
	}
	return m
}

// PIDRates returns the current network rate per process ID, and the app
// each process belongs to.
func (e *Engine) PIDRates() (map[int]Rate, map[int]string) {
	now := time.Now()
	a0, a1 := rateWindow(now)
	e.mu.Lock()
	defer e.mu.Unlock()
	rates := map[int]Rate{}
	for _, f := range e.flows {
		i0, o0 := f.r.at(a0)
		i1, o1 := f.r.at(a1)
		if i0+i1+o0+o1 > 0 {
			r := rates[f.pid]
			r.In += float64(i0+i1) / 2
			r.Out += float64(o0+o1) / 2
			rates[f.pid] = r
		}
	}
	apps := map[int]string{}
	for k, a := range e.apps {
		for pid := range a.pids {
			apps[pid] = k
		}
	}
	return rates, apps
}

// Point is one sample of a time series.
type Point struct {
	T   int64   `json:"t"`
	In  float64 `json:"in"`
	Out float64 `json:"out"`
}

// LiveSeries returns per-second bytes for the last n seconds for one app
// (or the whole machine when key is empty).
func (e *Engine) LiveSeries(key string, n int) []Point {
	if n > ringN-2 {
		n = ringN - 2
	}
	_, last := rateWindow(time.Now())
	e.mu.Lock()
	defer e.mu.Unlock()
	r := &e.total
	if key != "" {
		a := e.apps[key]
		if a == nil {
			return nil
		}
		r = &a.r
	}
	pts := make([]Point, n)
	for j := 0; j < n; j++ {
		s := last - int64(n-1-j)
		in, out := r.at(s)
		pts[j] = Point{s, float64(in), float64(out)}
	}
	return pts
}

// Totals returns bytes since start and the current machine-wide rate.
func (e *Engine) Totals() (in, out uint64, rate Rate, events uint64) {
	a0, a1 := rateWindow(time.Now())
	e.mu.Lock()
	defer e.mu.Unlock()
	i0, o0 := e.total.at(a0)
	i1, o1 := e.total.at(a1)
	return e.totIn, e.totOut, Rate{float64(i0+i1) / 2, float64(o0+o1) / 2}, e.events
}

// localAddrs tracks this machine's interface addresses.
type localAddrs struct {
	mu   sync.RWMutex
	set  map[netip.Addr]bool
	when time.Time
}

func newLocalAddrs() *localAddrs {
	l := &localAddrs{}
	l.refresh(time.Now())
	return l
}

func (l *localAddrs) refresh(now time.Time) {
	l.mu.RLock()
	fresh := now.Sub(l.when) < 30*time.Second
	l.mu.RUnlock()
	if fresh {
		return
	}
	set := map[netip.Addr]bool{}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				if ip, ok := netip.AddrFromSlice(ipn.IP); ok {
					set[ip.Unmap()] = true
				}
			}
		}
	}
	l.mu.Lock()
	l.set, l.when = set, now
	l.mu.Unlock()
}

func (l *localAddrs) has(ip netip.Addr) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.set[ip.WithZone("")]
}
