package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"packettrail/internal/tools"
)

// Diagnostics (MTR, traceroute, ping, TCP connect) stream results to the page
// as Server-Sent Events. Because an EventSource request is a plain GET, it is
// guarded by a per-process token that only our own page can read (other
// sites cannot read /api/status across origins).

const (
	maxToolRuns = 4
	maxToolTime = 15 * time.Minute
)

func newToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Server) handleTool(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("token") != s.token {
		writeErr(w, 403, "bad token")
		return
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		writeErr(w, 403, "cross-site request")
		return
	}
	dst, err := netip.ParseAddr(q.Get("ip"))
	if err != nil {
		writeErr(w, 400, "bad IP address")
		return
	}
	dst = dst.Unmap().WithZone("")
	if dst.IsUnspecified() || dst.IsMulticast() || dst == netip.AddrFrom4([4]byte{255, 255, 255, 255}) {
		writeErr(w, 400, "not a unicast address")
		return
	}
	kind := q.Get("kind")
	port, _ := strconv.Atoi(q.Get("port"))
	switch kind {
	case "mtr", "trace", "ping":
	case "tcp":
		if port < 1 || port > 65535 {
			writeErr(w, 400, "bad port")
			return
		}
	default:
		writeErr(w, 400, "unknown tool")
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
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	flusher.Flush()

	ctx, cancel := context.WithTimeout(r.Context(), maxToolTime)
	defer cancel()
	send := func(event string, v any) {
		b, _ := json.Marshal(v)
		if event != "" {
			fmt.Fprintf(w, "event: %s\n", event)
		}
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
	describe := func(a netip.Addr) tools.HopInfo {
		info := s.enr.Lookup(a)
		host := info.PTR
		if host == "" {
			host = s.eng.DNS.Get(a)
		}
		return tools.HopInfo{Lat: info.Lat, Lon: info.Lon, Host: host, Scope: info.Scope, CC: info.CC, City: info.City, ASN: info.ASN, ASOrg: info.ASOrg}
	}
	switch kind {
	case "mtr", "trace":
		rounds, interval := 0, time.Second
		if kind == "trace" {
			rounds, interval = 3, 0
		}
		note := tools.PathNote()
		err = tools.RunPath(ctx, kind, dst, rounds, interval, tools.Echo, describe, func(rep tools.PathReport) {
			if rep.Note == "" {
				rep.Note = note
			}
			send("", rep)
		})
	case "ping":
		tools.RunSeries(ctx, kind, dst, 0, 0, time.Second, tools.PingProbe(tools.Echo, dst), func(rep tools.SeriesReport) { send("", rep) })
	case "tcp":
		tools.RunSeries(ctx, kind, dst, port, 0, time.Second, tools.TCPProbe(dst, port), func(rep tools.SeriesReport) { send("", rep) })
	}
	if err != nil {
		send("failure", map[string]string{"error": err.Error()})
		return
	}
	if ctx.Err() == context.DeadlineExceeded {
		send("done", map[string]string{"reason": "Stopped after 15 minutes."})
		return
	}
	send("done", map[string]string{})
}
