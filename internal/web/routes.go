package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"packettrail/internal/tools"
)

// RouteTarget is one destination network for the route map.
type RouteTarget struct {
	ASN   uint32  `json:"asn"`
	Org   string  `json:"org"`
	CC    string  `json:"cc"`
	Share float64 `json:"share"`
	IP    string  `json:"ip"`
	Host  string  `json:"host"`
}

// routeTargets picks the busiest IP in each of the top n off-net networks.
func (s *Server) routeTargets(p Period, n int) ([]RouteTarget, error) {
	v, err := s.peeringView(p)
	if err != nil {
		return nil, err
	}
	m, _, err := s.ipAggs(p)
	if err != nil {
		return nil, err
	}
	best := map[uint32]netip.Addr{}
	bestTot := map[uint32]uint64{}
	for ip, a := range m {
		info := s.enr.Lookup(ip)
		if info.Scope != "" || info.ASN == 0 || !ip.Is4() {
			continue
		}
		if t := a.In + a.Out; t >= bestTot[info.ASN] {
			best[info.ASN], bestTot[info.ASN] = ip, t
		}
	}
	var out []RouteTarget
	for _, nw := range v.Networks {
		ip, ok := best[nw.ASN]
		if !ok {
			continue
		}
		host := s.eng.DNS.Get(ip)
		out = append(out, RouteTarget{ASN: nw.ASN, Org: nw.Org, CC: nw.CC, Share: nw.Share, IP: ip.String(), Host: host})
		if len(out) == n {
			break
		}
	}
	return out, nil
}

// handleRoutes traces the path to each top network (two probes per hop) and
// streams every finished route as a Server-Sent Event.
func (s *Server) handleRoutes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("token") != s.token {
		writeErr(w, 403, "bad token")
		return
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		writeErr(w, 403, "cross-site request")
		return
	}
	n, _ := strconv.Atoi(q.Get("n"))
	if n < 1 || n > 12 {
		n = 8
	}
	targets, err := s.routeTargets(parsePeriod(q.Get("period")), n)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	select {
	case s.toolSlots <- struct{}{}:
		defer func() { <-s.toolSlots }()
	default:
		writeErr(w, 429, "too many diagnostics running; stop one first")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	var wmu sync.Mutex
	send := func(event string, v any) {
		b, _ := json.Marshal(v)
		wmu.Lock()
		defer wmu.Unlock()
		if event != "" {
			fmt.Fprintf(w, "event: %s\n", event)
		}
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
	send("targets", targets)
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	describe := func(a netip.Addr) tools.HopInfo {
		info := s.enr.Lookup(a)
		host := info.PTR
		if host == "" {
			host = s.eng.DNS.Get(a)
		}
		return tools.HopInfo{Host: host, Scope: info.Scope, CC: info.CC, City: info.City, ASN: info.ASN, ASOrg: info.ASOrg}
	}
	sem := make(chan struct{}, 4) // trace four networks at a time
	var wg sync.WaitGroup
	for i, t := range targets {
		dst, err := netip.ParseAddr(t.IP)
		if err != nil {
			continue
		}
		wg.Add(1)
		go func(i int, t RouteTarget, dst netip.Addr) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			var last tools.PathReport
			if err := tools.RunPath(ctx, "trace", dst, 2, 0, tools.Echo, describe, func(rep tools.PathReport) { last = rep }); err != nil {
				send("failure", map[string]string{"error": err.Error()})
				return
			}
			if ctx.Err() == nil {
				send("route", map[string]any{"index": i, "target": t, "path": last})
			}
		}(i, t, dst)
	}
	wg.Wait()
	send("done", map[string]int{"count": len(targets)})
}
