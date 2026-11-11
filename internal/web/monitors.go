package web

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"packettrail/internal/store"
)

// Suggestion is a one-click monitor target.
type Suggestion struct {
	Name   string `json:"name"`
	Target string `json:"target"`
	Kind   string `json:"kind"` // gateway | dns | public
}

func (s *Server) monitorSuggestions() []Suggestion {
	have := s.opts.Monitors.Targets()
	var out []Suggestion
	add := func(name, target, kind string) {
		if !have[target] {
			have[target] = true
			out = append(out, Suggestion{name, target, kind})
		}
	}
	if s.opts.Gateway != nil {
		if gw := s.opts.Gateway(); gw.IsValid() {
			add("Default gateway", gw.String(), "gateway")
		}
	}
	if s.opts.DNSServers != nil {
		n := 0
		for _, d := range s.opts.DNSServers() {
			if n == 3 {
				break
			}
			add("DNS server "+d.String(), d.String(), "dns")
			n++
		}
	}
	add("Cloudflare 1.1.1.1", "1.1.1.1", "public")
	add("Google 8.8.8.8", "8.8.8.8", "public")
	return out
}

func (s *Server) registerMonitorRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/monitors", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"monitors": s.opts.Monitors.List(), "suggest": s.monitorSuggestions()})
	})
	mux.HandleFunc("POST /api/monitors", func(w http.ResponseWriter, r *http.Request) {
		var m store.Monitor
		body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
		if err := json.Unmarshal(body, &m); err != nil {
			writeErr(w, 400, "bad request")
			return
		}
		added, err := s.opts.Monitors.Add(m)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, added)
	})
	mux.HandleFunc("POST /api/monitors/delete", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		if err := s.opts.Monitors.Remove(id); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/monitors/enable", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		id, _ := strconv.ParseInt(q.Get("id"), 10, 64)
		if err := s.opts.Monitors.SetEnabled(id, q.Get("on") == "1"); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/monitors/enable-all", func(w http.ResponseWriter, r *http.Request) {
		n, err := s.opts.Monitors.SetAllEnabled(r.URL.Query().Get("on") == "1")
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]int{"changed": n})
	})
	mux.HandleFunc("GET /api/monitors/series", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		id, _ := strconv.ParseInt(q.Get("id"), 10, 64)
		if !s.opts.Monitors.Has(id) {
			writeErr(w, 404, "no such monitor")
			return
		}
		p := parsePeriod(q.Get("period"))
		if p.Session {
			// Raw samples from memory (about the last 10–20 minutes).
			pts := []store.MonitorPoint{}
			for _, sm := range s.opts.Monitors.Samples(id) {
				pt := store.MonitorPoint{T: sm.T / 1000, Sent: 1}
				if sm.RTT >= 0 {
					pt.Recv, pt.Min, pt.Avg, pt.Max = 1, sm.RTT, sm.RTT, sm.RTT
				}
				pts = append(pts, pt)
			}
			writeJSON(w, map[string]any{"step": 0, "points": pts})
			return
		}
		s.opts.Monitors.Flush()
		since := p.Since
		if since == 0 {
			since = 1
		}
		since -= since % p.Step
		pts, err := s.st.MonitorSeries(id, since, p.Step)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]any{"step": p.Step, "since": since, "now": time.Now().Unix(), "points": pts})
	})
}
