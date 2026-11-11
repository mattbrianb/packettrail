package web

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

// IPAppRow is one application/port that talked to an IP.
type IPAppRow struct {
	Key     string  `json:"key"`
	Name    string  `json:"name"`
	Proto   string  `json:"proto"`
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
	quality
}

// IPDetail is everything known about one remote IP for a period.
type IPDetail struct {
	IP      string     `json:"ip"`
	Hosts   []string   `json:"hosts"`
	RateIn  float64    `json:"rateIn"`
	RateOut float64    `json:"rateOut"`
	In      uint64     `json:"in"`
	Out     uint64     `json:"out"`
	Total   uint64     `json:"total"`
	First   int64      `json:"first"`
	Last    int64      `json:"last"`
	Apps    []IPAppRow `json:"apps"`
	geoFields
	quality
}

func (s *Server) ipDetail(p Period, ipStr string) (*IPDetail, error) {
	ip, err := netip.ParseAddr(ipStr)
	if err != nil {
		return nil, fmt.Errorf("bad IP address")
	}
	ip = ip.Unmap()
	rows, err := s.rows(p)
	if err != nil {
		return nil, err
	}
	meta := s.appMeta()
	rates := s.eng.FlowRates()
	d := &IPDetail{IP: ip.String(), Apps: []IPAppRow{}}
	hosts := map[string]bool{}
	var q qacc
	for _, r := range rows {
		if r.IP != ip {
			continue
		}
		rt := rates[r.FlowKey]
		name := meta[r.App].Name
		if name == "" {
			name = r.App
		}
		q.add(r)
		var rq qacc
		rq.add(r)
		d.Apps = append(d.Apps, IPAppRow{quality: rq.view(),
			Key: r.App, Name: name, Proto: r.Proto.String(), Port: r.Port, Dir: dirName(r.Dir),
			RateIn: rt.In, RateOut: rt.Out, In: r.In, Out: r.Out, Total: r.In + r.Out,
			First: r.First, Last: r.Last, Host: r.Host,
		})
		d.In += r.In
		d.Out += r.Out
		d.RateIn += rt.In
		d.RateOut += rt.Out
		if d.First == 0 || r.First < d.First {
			d.First = r.First
		}
		if r.Last > d.Last {
			d.Last = r.Last
		}
		if r.Host != "" {
			hosts[r.Host] = true
		}
	}
	d.Total = d.In + d.Out
	d.quality = q.view()
	sort.Slice(d.Apps, func(i, j int) bool { return d.Apps[i].Total > d.Apps[j].Total })
	d.Hosts = s.eng.DNS.Names(ip)
	for _, h := range d.Hosts {
		delete(hosts, h)
	}
	for h := range hosts {
		d.Hosts = append(d.Hosts, h)
	}
	d.geoFields = geoOf(s.enr.Lookup(ip))
	return d, nil
}

// ASNIP is one IP inside a network.
type ASNIP struct {
	IP      string  `json:"ip"`
	Host    string  `json:"host"`
	Apps    string  `json:"apps"`
	RateIn  float64 `json:"rateIn"`
	RateOut float64 `json:"rateOut"`
	In      uint64  `json:"in"`
	Out     uint64  `json:"out"`
	Total   uint64  `json:"total"`
	Last    int64   `json:"last"`
	geoFields
	quality
}

// ASNPrefix summarises traffic per routed prefix within a network.
type ASNPrefix struct {
	Network string  `json:"network"`
	CC      string  `json:"cc"`
	City    string  `json:"city"`
	IPs     int     `json:"ips"`
	RateIn  float64 `json:"rateIn"`
	RateOut float64 `json:"rateOut"`
	In      uint64  `json:"in"`
	Out     uint64  `json:"out"`
	Total   uint64  `json:"total"`
	quality
	q qacc
}

// ASNDetail breaks a network row down into prefixes and IPs.
type ASNDetail struct {
	Key      string      `json:"key"`
	ASN      uint32      `json:"asn"`
	Org      string      `json:"org"`
	Prefixes []ASNPrefix `json:"prefixes"`
	IPs      []ASNIP     `json:"ips"`
}

func (s *Server) asnDetail(p Period, key string) (*ASNDetail, error) {
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
	d := &ASNDetail{Key: key, Prefixes: []ASNPrefix{}, IPs: []ASNIP{}}
	pfx := map[string]*ASNPrefix{}
	for ip, a := range m {
		info := s.enr.Lookup(ip)
		k := fmt.Sprint(info.ASN)
		if info.Scope != "" {
			k = "scope:" + info.Scope
		} else if info.ASN == 0 {
			k = "unknown"
		}
		if k != key {
			continue
		}
		d.ASN, d.Org = info.ASN, info.ASOrg
		host := a.Host
		if host == "" {
			host = s.eng.DNS.Get(ip)
		}
		apps, _ := topKeys(a.apps, 3, name)
		d.IPs = append(d.IPs, ASNIP{
			IP: ip.String(), Host: host, Apps: apps, RateIn: a.RateIn, RateOut: a.RateOut,
			In: a.In, Out: a.Out, Total: a.In + a.Out, Last: a.Last, geoFields: geoOf(info), quality: a.q.view(),
		})
		nw := info.Network
		if nw == "" {
			nw = "(unknown prefix)"
			if info.Scope != "" {
				nw = "(" + info.Scope + ")"
			}
		}
		g := pfx[nw]
		if g == nil {
			g = &ASNPrefix{Network: nw, CC: info.CC, City: info.City}
			pfx[nw] = g
		}
		g.IPs++
		g.In += a.In
		g.Out += a.Out
		g.q.merge(a.q)
		g.RateIn += a.RateIn
		g.RateOut += a.RateOut
		if g.City != info.City {
			g.City = ""
		}
	}
	for _, g := range pfx {
		g.Total = g.In + g.Out
		g.quality = g.q.view()
		d.Prefixes = append(d.Prefixes, *g)
	}
	sort.Slice(d.Prefixes, func(i, j int) bool { return d.Prefixes[i].Total > d.Prefixes[j].Total })
	sort.Slice(d.IPs, func(i, j int) bool { return d.IPs[i].Total > d.IPs[j].Total })
	if strings.HasPrefix(key, "scope:") {
		d.Org = "(" + strings.TrimPrefix(key, "scope:") + " addresses)"
	}
	if len(d.IPs) > 200 {
		d.IPs = d.IPs[:200]
	}
	return d, nil
}
