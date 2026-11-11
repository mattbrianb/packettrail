// Package web serves the local dashboard and its JSON API on 127.0.0.1.
package web

import (
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"packettrail/internal/capture"
	"packettrail/internal/core"
	"packettrail/internal/enrich"
	"packettrail/internal/monitor"
	"packettrail/internal/peering"
	"packettrail/internal/store"
	"packettrail/internal/sysmon"
)

//go:embed static
var static embed.FS

// Options configures the server.
type Options struct {
	Version    string
	Copyright  string // shown in Settings → About
	HostName   string // replaces the computer's name in the UI (demo mode); empty = real name
	DataDir    string
	CanElevate bool
	Elevate    func() error       // relaunch elevated; nil if unsupported
	Flush      func()             // persist pending engine data
	OpenURL    func(string) error // open a link in the system browser
	ExportDir  string             // where CSV exports are saved
	Reveal     func(string) error // show a saved file in the file manager
	ListSep    string             // CSV delimiter matching the user's locale
	AppIcon    func(path string, size int) ([]byte, error)
	Monitors   *monitor.Manager
	Gateway    func() netip.Addr
	DNSServers func() []netip.Addr
	Peering    *peering.Detector
	PeeringDB  *peering.Client
	System     *sysmon.Sampler
}

// External links the dashboard may open in the system browser.
var linkAllow = []string{"https://db-ip.com", "https://github.com/jdecked/twemoji", "https://creativecommons.org/licenses/by/4.0/"}

type cached struct {
	rows []core.FlowRow
	at   time.Time
}

type Server struct {
	eng  *core.Engine
	st   *store.Store
	enr  *enrich.Enricher
	cap  capture.Capturer
	opts Options

	mu    sync.Mutex
	cache map[string]cached
	port  int

	iconMu sync.Mutex
	icons  map[string][]byte // by executable path; nil = no icon
	// process icons for the System tab, keyed "proc:<path>"
	procPaths map[string]string

	token     string        // guards the streaming diagnostics endpoint
	toolSlots chan struct{} // limits concurrent diagnostics
}

func New(eng *core.Engine, st *store.Store, enr *enrich.Enricher, c capture.Capturer, opts Options) *Server {
	return &Server{eng: eng, st: st, enr: enr, cap: c, opts: opts, cache: map[string]cached{}, icons: map[string][]byte{},
		token: newToken(), toolSlots: make(chan struct{}, maxToolRuns)}
}

// rows returns flow rows for a period, cached briefly so several views
// refreshing together share one query.
func (s *Server) rows(p Period) ([]core.FlowRow, error) {
	if p.Session {
		return s.eng.SessionRows(), nil
	}
	s.mu.Lock()
	c, ok := s.cache[p.Name]
	s.mu.Unlock()
	if ok && time.Since(c.at) < 3*time.Second {
		return c.rows, nil
	}
	if s.opts.Flush != nil && s.eng.HasPending() {
		s.opts.Flush()
	}
	rows, err := s.st.FlowRows(p.Since)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.cache[p.Name] = cached{rows, time.Now()}
	s.mu.Unlock()
	return rows, nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	enc.Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": err})
}

// guard blocks DNS-rebinding and cross-site requests: only our own origin
// may talk to the API, and state-changing calls need a custom header (which
// browsers will not send cross-origin without a CORS preflight we never allow).
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if host != "127.0.0.1" && host != "localhost" && host != "::1" {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Header.Get("X-PacketTrail") != "1" {
			http.Error(w, "missing X-PacketTrail header", http.StatusForbidden)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(static, "static")
	mux.Handle("GET /", http.FileServerFS(sub))

	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/live", func(w http.ResponseWriter, r *http.Request) {
		in, out, rate, _ := s.eng.Totals()
		writeJSON(w, map[string]any{
			"t": time.Now().Unix(), "rateIn": rate.In, "rateOut": rate.Out, "totalIn": in, "totalOut": out,
			"series": s.eng.LiveSeries(r.URL.Query().Get("app"), 300),
		})
	})
	mux.HandleFunc("GET /api/apps", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.appsView(parsePeriod(r.URL.Query().Get("period")))
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, v)
	})
	mux.HandleFunc("GET /api/flows", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		v, err := s.flowsView(parsePeriod(q.Get("period")), q.Get("app"))
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, v)
	})
	mux.HandleFunc("GET /api/ips", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.ipsView(parsePeriod(r.URL.Query().Get("period")))
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, v)
	})
	mux.HandleFunc("GET /api/asns", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.asnsView(parsePeriod(r.URL.Query().Get("period")))
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, v)
	})
	mux.HandleFunc("GET /api/ip", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		v, err := s.ipDetail(parsePeriod(q.Get("period")), q.Get("ip"))
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, v)
	})
	mux.HandleFunc("GET /api/asn", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		v, err := s.asnDetail(parsePeriod(q.Get("period")), q.Get("key"))
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, v)
	})
	mux.HandleFunc("GET /api/tool", s.handleTool)
	mux.HandleFunc("GET /api/resolve", s.handleResolve)
	mux.HandleFunc("GET /api/routes", s.handleRoutes)
	s.registerMonitorRoutes(mux)
	s.registerPeeringRoutes(mux)
	s.registerSystemRoutes(mux)
	mux.HandleFunc("GET /api/icon", func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("app")
		path := s.appMeta()[key].Path
		if path == "" && strings.HasPrefix(key, "proc:") {
			s.iconMu.Lock()
			path = s.procPaths[key]
			s.iconMu.Unlock()
		}
		if path == "" || s.opts.AppIcon == nil {
			http.NotFound(w, r)
			return
		}
		s.iconMu.Lock()
		png, ok := s.icons[path]
		s.iconMu.Unlock()
		if !ok {
			png, _ = s.opts.AppIcon(path, 64)
			s.iconMu.Lock()
			s.icons[path] = png
			s.iconMu.Unlock()
		}
		if png == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "max-age=86400")
		w.Write(png)
	})
	mux.HandleFunc("GET /api/series", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		p := parsePeriod(q.Get("period"))
		app := q.Get("app")
		if p.Session {
			writeJSON(w, map[string]any{"step": 1, "points": s.eng.LiveSeries(app, 300)})
			return
		}
		if s.opts.Flush != nil && s.eng.HasPending() {
			s.opts.Flush()
		}
		since := p.Since
		if since == 0 {
			since = 1
		}
		since -= since % p.Step
		pts, err := s.st.Series(app, since, p.Step)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if pts == nil {
			pts = []core.Point{}
		}
		writeJSON(w, map[string]any{"step": p.Step, "since": since, "points": pts})
	})
	mux.HandleFunc("POST /api/geo/update", func(w http.ResponseWriter, r *http.Request) {
		if s.enr.Downloading.Load() {
			writeErr(w, 409, "download already running")
			return
		}
		go s.enr.Download()
		writeJSON(w, map[string]bool{"started": true})
	})
	mux.HandleFunc("POST /api/open", func(w http.ResponseWriter, r *http.Request) {
		u := r.URL.Query().Get("url")
		ok := false
		for _, p := range linkAllow {
			if strings.HasPrefix(u, p) && !strings.ContainsAny(u, " \"'<>`\\^|{}") {
				ok = true
			}
		}
		if !ok || s.opts.OpenURL == nil {
			writeErr(w, 400, "link not allowed")
			return
		}
		if err := s.opts.OpenURL(u); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/export", func(w http.ResponseWriter, r *http.Request) {
		name := filepath.Base(r.URL.Query().Get("name"))
		if !strings.HasSuffix(strings.ToLower(name), ".csv") || strings.ContainsAny(name, `\/:*?"<>|`) || name == ".csv" {
			writeErr(w, 400, "bad file name")
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 256<<20))
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		if err := os.MkdirAll(s.opts.ExportDir, 0o755); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		path := filepath.Join(s.opts.ExportDir, name)
		if err := os.WriteFile(path, body, 0o644); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]string{"path": path})
	})
	mux.HandleFunc("POST /api/reveal", func(w http.ResponseWriter, r *http.Request) {
		path := filepath.Clean(r.URL.Query().Get("path"))
		if filepath.Dir(path) != filepath.Clean(s.opts.ExportDir) || s.opts.Reveal == nil {
			writeErr(w, 400, "not an export")
			return
		}
		if err := s.opts.Reveal(path); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/elevate", func(w http.ResponseWriter, r *http.Request) {
		if s.opts.Elevate == nil {
			writeErr(w, 400, "elevation is not supported here")
			return
		}
		if err := s.opts.Elevate(); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	})
	return s.guard(mux)
}

// hostname is the computer name shown in the UI.
func (s *Server) hostname() string {
	if s.opts.HostName != "" {
		return s.opts.HostName
	}
	h, _ := os.Hostname()
	return h
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	in, out, rate, events := s.eng.Totals()
	host := s.hostname()
	writeJSON(w, map[string]any{
		"app":        "packettrail",
		"version":    s.opts.Version,
		"copyright":  s.opts.Copyright,
		"os":         runtime.GOOS,
		"arch":       runtime.GOARCH,
		"hostname":   host,
		"capture":    s.cap.Status(),
		"canElevate": s.opts.CanElevate && s.opts.Elevate != nil,
		"dataDir":    s.opts.DataDir,
		"dbPath":     s.st.Path,
		"dbSize":     s.st.Size(),
		"geo":        s.enr.Status(),
		"start":      s.eng.Start().Unix(),
		"totalIn":    in,
		"totalOut":   out,
		"rateIn":     rate.In,
		"rateOut":    rate.Out,
		"flowEvents": events,
		"dnsEvents":  s.eng.DNS.Events(),
		"loopback":   s.eng.Loopback,
		"listSep":    s.opts.ListSep,
		"token":      s.token,
		"exportDir":  s.opts.ExportDir,
	})
}
