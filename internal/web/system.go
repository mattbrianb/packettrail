package web

import (
	"net/http"
	"runtime"
	"strings"

	"packettrail/internal/sysmon"
)

// SystemView is the latest system snapshot plus chart history.
type SystemView struct {
	*sysmon.Snapshot
	Ready   bool           `json:"ready"`
	Host    string         `json:"host"`
	OS      string         `json:"os"`
	History []sysmon.Point `json:"history"`
}

func (s *Server) registerSystemRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/system", s.handleSystem)
}

func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	v := SystemView{Host: s.hostname(), OS: runtime.GOOS}
	sm := s.opts.System
	if sm == nil {
		writeErr(w, 503, "system monitor is not running")
		return
	}
	snap := sm.Latest()
	if snap == nil {
		writeJSON(w, v) // first sample not taken yet
		return
	}
	if r.URL.Query().Get("lite") == "1" { // just the hints, for the tab's alert dot
		writeJSON(w, map[string]any{"ready": true, "hints": snap.Hints})
		return
	}
	cp := *snap // the sampler's copy is shared; work on our own
	cp.Procs = append([]sysmon.Proc(nil), snap.Procs...)
	rates, apps := s.eng.PIDRates()
	meta := s.appMeta()
	s.iconMu.Lock()
	if s.procPaths == nil {
		s.procPaths = map[string]string{}
	}
	for i := range cp.Procs {
		p := &cp.Procs[i]
		if rt, ok := rates[p.PID]; ok {
			p.NetIn, p.NetOut = rt.In, rt.Out
		}
		if k := apps[p.PID]; k != "" && meta[k].Path != "" {
			p.App = k
		} else if p.Path != "" {
			// Not a network app: serve its icon by path under a private key.
			k := "proc:" + strings.ToLower(p.Path)
			s.procPaths[k] = p.Path
			p.App = k
		}
	}
	if len(s.procPaths) > 4000 {
		clear(s.procPaths)
	}
	s.iconMu.Unlock()
	v.Snapshot, v.Ready, v.History = &cp, true, sm.History()
	writeJSON(w, v)
}
