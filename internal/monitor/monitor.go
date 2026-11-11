// Package monitor runs pinned latency monitors (smokeping-style): each target
// is probed in the background and results are kept per minute in the store.
package monitor

import (
	"context"
	"errors"
	"math"
	"net"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"packettrail/internal/store"
	"packettrail/internal/tools"
)

const (
	maxMonitors = 20
	keepSamples = 600 // raw samples kept in memory per monitor
)

// Sample is one probe result.
type Sample struct {
	T   int64   `json:"t"`   // unix ms
	RTT float64 `json:"rtt"` // ms; -1 = lost
}

// View is a monitor with its live statistics (last 5 minutes).
type View struct {
	store.Monitor
	Addr    string    `json:"addr"`
	Error   string    `json:"error,omitempty"`
	Last    float64   `json:"last"`
	Sent    int       `json:"sent"`
	Recv    int       `json:"recv"`
	Loss    float64   `json:"loss"`
	Min     float64   `json:"min"`
	Avg     float64   `json:"avg"`
	Max     float64   `json:"max"`
	Jitter  float64   `json:"jitter"`
	Spark   []float64 `json:"spark"`
	Healthy string    `json:"health"` // ok | warn | bad | idle
}

type minuteAgg struct {
	minute            int64
	sent, recv        int
	min, max, sum, sq float64
}

type runner struct {
	mon     store.Monitor
	addr    netip.Addr
	resolve time.Time
	err     string
	samples []Sample
	cur     minuteAgg
	cancel  context.CancelFunc
}

// Manager owns all monitors.
type Manager struct {
	mu   sync.Mutex
	st   *store.Store
	echo tools.EchoFunc
	runs map[int64]*runner
	done []store.MonitorMinute
	wg   sync.WaitGroup
}

func New(st *store.Store, echo tools.EchoFunc) *Manager {
	return &Manager{st: st, echo: echo, runs: map[int64]*runner{}}
}

// Start loads saved monitors and starts the enabled ones.
func (m *Manager) Start() error {
	mons, err := m.st.Monitors()
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, mon := range mons {
		r := &runner{mon: mon}
		m.runs[mon.ID] = r
		if mon.Enabled {
			m.startLocked(r)
		}
	}
	return nil
}

// Stop halts all probing and persists the current minutes.
func (m *Manager) Stop() {
	m.mu.Lock()
	for _, r := range m.runs {
		if r.cancel != nil {
			r.cancel()
			r.cancel = nil
		}
	}
	m.mu.Unlock()
	m.wg.Wait()
	m.mu.Lock()
	for _, r := range m.runs {
		m.closeMinuteLocked(r)
	}
	m.mu.Unlock()
	m.Flush()
}

// Flush writes completed minutes to the store.
func (m *Manager) Flush() {
	m.mu.Lock()
	rows := m.done
	m.done = nil
	m.mu.Unlock()
	if len(rows) > 0 {
		m.st.WriteMonitorMinutes(rows)
	}
}

func (m *Manager) startLocked(r *runner) {
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	m.wg.Add(1)
	go m.loop(ctx, r)
}

var hostRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,62}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,62}[A-Za-z0-9])?)*\.?$`)

// Validate checks a monitor definition and fills defaults.
func Validate(mon *store.Monitor) error {
	mon.Target = strings.TrimSpace(mon.Target)
	mon.Name = strings.TrimSpace(mon.Name)
	if mon.Target == "" || len(mon.Target) > 253 {
		return errors.New("enter an IP address or hostname")
	}
	if a, err := netip.ParseAddr(mon.Target); err == nil {
		a = a.Unmap()
		if a.IsUnspecified() || a.IsMulticast() {
			return errors.New("not a unicast address")
		}
		mon.Target = a.String()
	} else if !hostRe.MatchString(mon.Target) {
		return errors.New("not a valid IP address or hostname")
	}
	switch mon.Method {
	case "", "icmp":
		mon.Method, mon.Port = "icmp", 0
	case "tcp":
		if mon.Port < 1 || mon.Port > 65535 {
			return errors.New("TCP monitors need a port between 1 and 65535")
		}
	default:
		return errors.New("method must be icmp or tcp")
	}
	if mon.Interval == 0 {
		mon.Interval = 2
	}
	if mon.Interval < 1 || mon.Interval > 60 {
		return errors.New("interval must be 1–60 seconds")
	}
	if mon.Name == "" {
		mon.Name = mon.Target
	}
	if len(mon.Name) > 60 {
		mon.Name = mon.Name[:60]
	}
	return nil
}

// Add stores and starts a new monitor.
func (m *Manager) Add(mon store.Monitor) (store.Monitor, error) {
	if err := Validate(&mon); err != nil {
		return mon, err
	}
	m.mu.Lock()
	n := len(m.runs)
	for _, r := range m.runs {
		if r.mon.Target == mon.Target && r.mon.Method == mon.Method && r.mon.Port == mon.Port {
			m.mu.Unlock()
			return r.mon, errors.New("already monitoring " + mon.Target)
		}
	}
	m.mu.Unlock()
	if n >= maxMonitors {
		return mon, errors.New("at most 20 monitors")
	}
	mon.Enabled = true
	mon.Created = time.Now().Unix()
	id, err := m.st.AddMonitor(mon)
	if err != nil {
		return mon, err
	}
	mon.ID = id
	m.mu.Lock()
	r := &runner{mon: mon}
	m.runs[id] = r
	m.startLocked(r)
	m.mu.Unlock()
	return mon, nil
}

// Remove stops a monitor and deletes it with its history.
func (m *Manager) Remove(id int64) error {
	m.mu.Lock()
	r := m.runs[id]
	if r == nil {
		m.mu.Unlock()
		return errors.New("no such monitor")
	}
	if r.cancel != nil {
		r.cancel()
	}
	delete(m.runs, id)
	kept := m.done[:0]
	for _, d := range m.done {
		if d.MonitorID != id {
			kept = append(kept, d)
		}
	}
	m.done = kept
	m.mu.Unlock()
	return m.st.DeleteMonitor(id)
}

// SetEnabled pauses or resumes a monitor.
func (m *Manager) SetEnabled(id int64, on bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.runs[id]
	if r == nil {
		return errors.New("no such monitor")
	}
	if on && r.cancel == nil {
		m.startLocked(r)
	} else if !on && r.cancel != nil {
		r.cancel()
		r.cancel = nil
		m.closeMinuteLocked(r)
	}
	r.mon.Enabled = on
	return m.st.SetMonitorEnabled(id, on)
}

// SetAllEnabled pauses or resumes every monitor and reports how many changed.
func (m *Manager) SetAllEnabled(on bool) (int, error) {
	m.mu.Lock()
	var ids []int64
	for id, r := range m.runs {
		if r.mon.Enabled != on {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()
	n := 0
	for _, id := range ids {
		if err := m.SetEnabled(id, on); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func (m *Manager) loop(ctx context.Context, r *runner) {
	defer m.wg.Done()
	m.mu.Lock()
	mon := r.mon
	m.mu.Unlock()
	interval := time.Duration(mon.Interval) * time.Second
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		addr, err := m.target(ctx, r)
		s := Sample{T: time.Now().UnixMilli(), RTT: -1}
		if err == nil {
			s.RTT = m.probe(ctx, mon, addr)
		}
		if ctx.Err() != nil {
			return
		}
		m.record(r, s)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// target returns the address to probe, re-resolving hostnames every 5 minutes.
func (m *Manager) target(ctx context.Context, r *runner) (netip.Addr, error) {
	m.mu.Lock()
	mon, addr, at := r.mon, r.addr, r.resolve
	m.mu.Unlock()
	if a, err := netip.ParseAddr(mon.Target); err == nil {
		return a.Unmap(), nil
	}
	if addr.IsValid() && time.Since(at) < 5*time.Minute {
		return addr, nil
	}
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupNetIP(rctx, "ip", mon.Target)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil || len(ips) == 0 {
		r.err = "cannot resolve " + mon.Target
		if addr.IsValid() {
			return addr, nil // keep probing the last known address
		}
		return netip.Addr{}, errors.New(r.err)
	}
	sort.SliceStable(ips, func(i, j int) bool { return ips[i].Unmap().Is4() && !ips[j].Unmap().Is4() })
	r.addr, r.resolve, r.err = ips[0].Unmap(), time.Now(), ""
	return r.addr, nil
}

func (m *Manager) probe(ctx context.Context, mon store.Monitor, addr netip.Addr) float64 {
	if mon.Method == "tcp" {
		s := tools.TCPProbe(addr, mon.Port)(ctx)
		return s.RTT
	}
	res, err := m.echo(ctx, addr, 128, 2*time.Second)
	if err != nil || res.Status != tools.StatusReply {
		return -1
	}
	return math.Round(float64(res.RTT.Microseconds())/10) / 100
}

func (m *Manager) record(r *runner, s Sample) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r.samples = append(r.samples, s)
	if len(r.samples) > keepSamples {
		r.samples = r.samples[len(r.samples)-keepSamples:]
	}
	minute := s.T / 1000
	minute -= minute % 60
	if r.cur.minute != minute {
		m.closeMinuteLocked(r)
		r.cur = minuteAgg{minute: minute}
	}
	r.cur.sent++
	if s.RTT >= 0 {
		if r.cur.recv == 0 || s.RTT < r.cur.min {
			r.cur.min = s.RTT
		}
		if s.RTT > r.cur.max {
			r.cur.max = s.RTT
		}
		r.cur.recv++
		r.cur.sum += s.RTT
		r.cur.sq += s.RTT * s.RTT
	}
}

func (m *Manager) closeMinuteLocked(r *runner) {
	if r.cur.sent == 0 {
		return
	}
	c := r.cur
	m.done = append(m.done, store.MonitorMinute{MonitorID: r.mon.ID, Minute: c.minute, Sent: c.sent, Recv: c.recv,
		Min: c.min, Max: c.max, Sum: c.sum, Sq: c.sq})
	r.cur = minuteAgg{}
}

// List returns all monitors with statistics over the last 5 minutes.
func (m *Manager) List() []View {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]View, 0, len(m.runs))
	cut := time.Now().Add(-5 * time.Minute).UnixMilli()
	for _, r := range m.runs {
		v := View{Monitor: r.mon, Error: r.err, Last: -1, Healthy: "idle"}
		if r.addr.IsValid() {
			v.Addr = r.addr.String()
		} else if a, err := netip.ParseAddr(r.mon.Target); err == nil {
			v.Addr = a.String()
		}
		var sum, jit, prev float64
		havePrev := false
		for _, s := range r.samples {
			if s.T < cut {
				continue
			}
			v.Sent++
			if s.RTT < 0 {
				continue
			}
			if v.Recv == 0 || s.RTT < v.Min {
				v.Min = s.RTT
			}
			if s.RTT > v.Max {
				v.Max = s.RTT
			}
			v.Recv++
			sum += s.RTT
			if havePrev {
				jit += math.Abs(s.RTT - prev)
			}
			prev, havePrev = s.RTT, true
		}
		if v.Recv > 0 {
			v.Avg = math.Round(sum/float64(v.Recv)*100) / 100
		}
		if v.Recv > 1 {
			v.Jitter = math.Round(jit/float64(v.Recv-1)*100) / 100
		}
		if v.Sent > 0 {
			v.Loss = math.Round(1000*float64(v.Sent-v.Recv)/float64(v.Sent)) / 10
		}
		n := len(r.samples)
		if n > 0 {
			v.Last = r.samples[n-1].RTT
		}
		from := n - 60
		if from < 0 {
			from = 0
		}
		v.Spark = make([]float64, 0, n-from)
		for _, s := range r.samples[from:] {
			v.Spark = append(v.Spark, s.RTT)
		}
		if r.mon.Enabled && v.Sent > 0 {
			switch {
			case v.Loss >= 20 || (n >= 3 && r.samples[n-1].RTT < 0 && r.samples[n-2].RTT < 0 && r.samples[n-3].RTT < 0):
				v.Healthy = "bad"
			case v.Loss > 0 || (v.Avg > 0 && v.Max > 4*v.Avg && v.Max-v.Avg > 50):
				v.Healthy = "warn"
			default:
				v.Healthy = "ok"
			}
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Samples returns the raw in-memory samples for one monitor.
func (m *Manager) Samples(id int64) []Sample {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.runs[id]
	if r == nil {
		return nil
	}
	return append([]Sample(nil), r.samples...)
}

// Has reports whether a monitor exists.
func (m *Manager) Has(id int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.runs[id] != nil
}

// Targets returns the set of monitored targets (for suggestions).
func (m *Manager) Targets() map[string]bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := map[string]bool{}
	for _, r := range m.runs {
		t[r.mon.Target] = true
		t[r.mon.Target+":"+strconv.Itoa(r.mon.Port)] = true
	}
	return t
}
