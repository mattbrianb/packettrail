package web

import (
	"fmt"
	"math"
	"net/netip"
	"sort"
	"strings"
	"time"

	"packettrail/internal/capture"
	"packettrail/internal/core"
	"packettrail/internal/enrich"
)

// Period selects the time window of a view.
type Period struct {
	Name    string
	Since   int64 // unix seconds; 0 for "all"
	Session bool
	Step    int64 // chart bucket for history series
}

func parsePeriod(s string) Period {
	now := time.Now().Unix()
	switch s {
	case "1h":
		return Period{Name: s, Since: now - 3600, Step: 60}
	case "24h":
		return Period{Name: s, Since: now - 86400, Step: 300}
	case "7d":
		return Period{Name: s, Since: now - 7*86400, Step: 3600}
	case "30d":
		return Period{Name: s, Since: now - 30*86400, Step: 4 * 3600}
	case "all":
		return Period{Name: s, Since: 0, Step: 86400}
	}
	return Period{Name: "session", Session: true}
}

type AppView struct {
	Key     string    `json:"key"`
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	PIDs    string    `json:"pids"`
	RateIn  float64   `json:"rateIn"`
	RateOut float64   `json:"rateOut"`
	In      uint64    `json:"in"`
	Out     uint64    `json:"out"`
	Total   uint64    `json:"total"`
	IPs     int       `json:"ips"`
	Flows   int       `json:"flows"`
	First   int64     `json:"first"`
	Last    int64     `json:"last"`
	Spark   []float64 `json:"spark,omitempty"`
	quality
}

type geoFields struct {
	Scope   string `json:"scope"`
	CC      string `json:"cc"`
	Country string `json:"country"`
	Region  string `json:"region"`
	City    string `json:"city"`
	ASN     uint32 `json:"asn"`
	ASOrg   string `json:"asOrg"`
	Network string `json:"network"`
	PTR     string `json:"ptr"`
}

func geoOf(i enrich.Info) geoFields {
	return geoFields{i.Scope, i.CC, i.Country, i.Region, i.City, i.ASN, i.ASOrg, i.Network, i.PTR}
}

// quality is the passive path-quality summary shown next to traffic.
type quality struct {
	RTT        float64 `json:"rtt"`        // average smoothed TCP RTT in ms; -1 = no samples
	Retrans    uint64  `json:"retrans"`    // bytes retransmitted
	RetransPct float64 `json:"retransPct"` // retransmitted / sent bytes, in %
	LossInPct  float64 `json:"lossInPct"`  // download-side loss events per segment received, %; -1 = no data
}

// qacc accumulates path quality over flow rows.
type qacc struct {
	rtx, out, rttN uint64
	rttSum         float64
	lossIn, segsIn uint64
}

func (q *qacc) add(r core.FlowRow) {
	q.rtx += r.Retrans
	q.out += r.Out
	q.rttSum += r.RTTSum
	q.rttN += r.RTTN
	q.lossIn += r.LossIn
	q.segsIn += r.SegsIn
}

func (q *qacc) merge(o qacc) {
	q.rtx += o.rtx
	q.out += o.out
	q.rttSum += o.rttSum
	q.rttN += o.rttN
	q.lossIn += o.lossIn
	q.segsIn += o.segsIn
}

func (q qacc) view() quality {
	v := quality{RTT: -1, Retrans: q.rtx, LossInPct: -1}
	if q.segsIn > 0 {
		v.LossInPct = math.Round(10000*float64(q.lossIn)/float64(q.segsIn)) / 100
	}
	if q.rttN > 0 {
		v.RTT = math.Round(q.rttSum/float64(q.rttN)*10) / 10
	}
	if q.out > 0 {
		v.RetransPct = math.Round(10000*float64(q.rtx)/float64(q.out)) / 100
	}
	return v
}

type FlowView struct {
	Proto   string  `json:"proto"`
	IP      string  `json:"ip"`
	Port    uint16  `json:"port"`
	Dir     string  `json:"dir"`
	RateIn  float64 `json:"rateIn"`
	RateOut float64 `json:"rateOut"`
	In      uint64  `json:"in"`
	Out     uint64  `json:"out"`
	Total   uint64  `json:"total"`
	First   int64   `json:"first"`
	Last    int64   `json:"last"`
	Host    string  `json:"host"`
	geoFields
	quality
}

type IPView struct {
	IP      string   `json:"ip"`
	RateIn  float64  `json:"rateIn"`
	RateOut float64  `json:"rateOut"`
	In      uint64   `json:"in"`
	Out     uint64   `json:"out"`
	Total   uint64   `json:"total"`
	First   int64    `json:"first"`
	Last    int64    `json:"last"`
	Apps    string   `json:"apps"`
	AppKeys []string `json:"appKeys"`
	Ports   string   `json:"ports"`
	Host    string   `json:"host"`
	geoFields
	quality
}

type ASNView struct {
	Key       string  `json:"key"`
	ASN       uint32  `json:"asn"`
	Org       string  `json:"org"`
	Countries string  `json:"countries"`
	IPs       int     `json:"ips"`
	Apps      string  `json:"apps"`
	RateIn    float64 `json:"rateIn"`
	RateOut   float64 `json:"rateOut"`
	In        uint64  `json:"in"`
	Out       uint64  `json:"out"`
	Total     uint64  `json:"total"`
	First     int64   `json:"first"`
	Last      int64   `json:"last"`
	quality
}

func dirName(d capture.Dir) string {
	if d == capture.DirIn {
		return "in"
	}
	return "out"
}

func (s *Server) appMeta() map[string]core.AppMeta {
	m := s.st.Apps()
	for k, v := range s.eng.Apps() {
		old := m[k]
		if v.Path == "" {
			v.Path = old.Path
		}
		m[k] = v
	}
	return m
}

func joinInts(v []int, max int) string {
	var b strings.Builder
	for i, x := range v {
		if i == max {
			fmt.Fprintf(&b, " +%d", len(v)-max)
			break
		}
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprint(&b, x)
	}
	return b.String()
}

func (s *Server) appsView(p Period) ([]AppView, error) {
	rows, err := s.rows(p)
	if err != nil {
		return nil, err
	}
	meta := s.appMeta()
	rates, sparks := s.eng.AppRates(60)
	type agg struct {
		AppView
		ips map[netip.Addr]struct{}
		q   qacc
	}
	m := map[string]*agg{}
	for _, r := range rows {
		a := m[r.App]
		if a == nil {
			a = &agg{AppView: AppView{Key: r.App, First: r.First}, ips: map[netip.Addr]struct{}{}}
			m[r.App] = a
		}
		a.In += r.In
		a.Out += r.Out
		a.Flows++
		a.ips[r.IP] = struct{}{}
		a.q.add(r)
		if r.First < a.First {
			a.First = r.First
		}
		if r.Last > a.Last {
			a.Last = r.Last
		}
	}
	out := make([]AppView, 0, len(m))
	for k, a := range m {
		v := a.AppView
		md := meta[k]
		v.Name, v.Path, v.PIDs = md.Name, md.Path, joinInts(md.PIDs, 6)
		if v.Name == "" {
			v.Name = k
		}
		v.Total, v.IPs = v.In+v.Out, len(a.ips)
		r := rates[k]
		v.RateIn, v.RateOut, v.Spark = r.In, r.Out, sparks[k]
		v.quality = a.q.view()
		out = append(out, v)
	}
	return out, nil
}

func (s *Server) flowsView(p Period, app string) ([]FlowView, error) {
	rows, err := s.rows(p)
	if err != nil {
		return nil, err
	}
	rates := s.eng.FlowRates()
	out := []FlowView{}
	for _, r := range rows {
		if r.App != app {
			continue
		}
		info := s.enr.Lookup(r.IP)
		host := r.Host
		if host == "" {
			host = s.eng.DNS.Get(r.IP)
		}
		rt := rates[r.FlowKey]
		var q qacc
		q.add(r)
		out = append(out, FlowView{quality: q.view(),
			Proto: r.Proto.String(), IP: r.IP.String(), Port: r.Port, Dir: dirName(r.Dir),
			RateIn: rt.In, RateOut: rt.Out, In: r.In, Out: r.Out, Total: r.In + r.Out,
			First: r.First, Last: r.Last, Host: host, geoFields: geoOf(info),
		})
	}
	return out, nil
}

type ipAgg struct {
	IPView
	apps  map[string]uint64
	ports map[string]uint64
	q     qacc
}

func (s *Server) ipAggs(p Period) (map[netip.Addr]*ipAgg, map[string]core.AppMeta, error) {
	rows, err := s.rows(p)
	if err != nil {
		return nil, nil, err
	}
	meta := s.appMeta()
	rates := s.eng.FlowRates()
	m := map[netip.Addr]*ipAgg{}
	for _, r := range rows {
		a := m[r.IP]
		if a == nil {
			a = &ipAgg{IPView: IPView{IP: r.IP.String(), First: r.First}, apps: map[string]uint64{}, ports: map[string]uint64{}}
			m[r.IP] = a
		}
		a.In += r.In
		a.Out += r.Out
		a.q.add(r)
		if r.First < a.First {
			a.First = r.First
		}
		if r.Last > a.Last {
			a.Last = r.Last
		}
		if a.Host == "" && r.Host != "" {
			a.Host = r.Host
		}
		a.apps[r.App] += r.In + r.Out + 1
		port := fmt.Sprintf("%s/%d", r.Proto, r.Port)
		if r.Dir == capture.DirIn {
			port = fmt.Sprintf("%s/%d in", r.Proto, r.Port)
		}
		a.ports[port] += r.In + r.Out + 1
		if rt, ok := rates[r.FlowKey]; ok {
			a.RateIn += rt.In
			a.RateOut += rt.Out
		}
	}
	return m, meta, nil
}

func topKeys(m map[string]uint64, max int, label func(string) string) (string, []string) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return m[keys[i]] > m[keys[j]] })
	var b strings.Builder
	for i, k := range keys {
		if i == max {
			fmt.Fprintf(&b, " +%d", len(keys)-max)
			break
		}
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(label(k))
	}
	return b.String(), keys
}

func (s *Server) ipsView(p Period) ([]IPView, error) {
	m, meta, err := s.ipAggs(p)
	if err != nil {
		return nil, err
	}
	name := func(k string) string {
		if n := meta[k].Name; n != "" {
			return n
		}
		return k
	}
	same := func(k string) string { return k }
	out := make([]IPView, 0, len(m))
	for ip, a := range m {
		v := a.IPView
		v.Total = v.In + v.Out
		v.Apps, v.AppKeys = topKeys(a.apps, 4, name)
		v.Ports, _ = topKeys(a.ports, 5, same)
		if v.Host == "" {
			v.Host = s.eng.DNS.Get(ip)
		}
		v.geoFields = geoOf(s.enr.Lookup(ip))
		v.quality = a.q.view()
		out = append(out, v)
	}
	return out, nil
}

func (s *Server) asnsView(p Period) ([]ASNView, error) {
	m, meta, err := s.ipAggs(p)
	if err != nil {
		return nil, err
	}
	type agg struct {
		ASNView
		cc   map[string]uint64
		apps map[string]uint64
		q    qacc
	}
	by := map[string]*agg{}
	for ip, a := range m {
		info := s.enr.Lookup(ip)
		key := fmt.Sprint(info.ASN)
		org := info.ASOrg
		if info.Scope != "" {
			key, org = "scope:"+info.Scope, "("+info.Scope+" addresses)"
		} else if info.ASN == 0 {
			key, org = "unknown", "(no ASN data)"
		}
		g := by[key]
		if g == nil {
			g = &agg{ASNView: ASNView{Key: key, ASN: info.ASN, Org: org, First: a.First}, cc: map[string]uint64{}, apps: map[string]uint64{}}
			by[key] = g
		}
		g.IPs++
		g.In += a.In
		g.Out += a.Out
		g.q.merge(a.q)
		g.RateIn += a.RateIn
		g.RateOut += a.RateOut
		if a.First < g.First {
			g.First = a.First
		}
		if a.Last > g.Last {
			g.Last = a.Last
		}
		if info.CC != "" {
			g.cc[info.CC] += a.In + a.Out + 1
		}
		for k, v := range a.apps {
			g.apps[k] += v
		}
	}
	name := func(k string) string {
		if n := meta[k].Name; n != "" {
			return n
		}
		return k
	}
	same := func(k string) string { return k }
	out := make([]ASNView, 0, len(by))
	for _, g := range by {
		v := g.ASNView
		v.Total = v.In + v.Out
		v.Countries, _ = topKeys(g.cc, 4, same)
		v.Apps, _ = topKeys(g.apps, 4, name)
		v.quality = g.q.view()
		out = append(out, v)
	}
	return out, nil
}
