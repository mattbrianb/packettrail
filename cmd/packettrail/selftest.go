package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"packettrail/internal/capture"
	"packettrail/internal/core"
	"packettrail/internal/enrich"
	"packettrail/internal/platform"
)

func human(b uint64) string {
	f := float64(b)
	for _, u := range []string{"B", "KB", "MB", "GB", "TB"} {
		if f < 1024 || u == "TB" {
			if u == "B" {
				return fmt.Sprintf("%d B", b)
			}
			return fmt.Sprintf("%.1f %s", f, u)
		}
		f /= 1024
	}
	return ""
}

// selfTest captures for a while without touching the history database and
// prints a diagnostic report (also saved in the data folder).
func selfTest(c config) int {
	path := filepath.Join(c.dataDir, "selftest-"+time.Now().Format("20060102-150405")+".txt")
	f, _ := os.Create(path)
	var w io.Writer = os.Stdout
	if f != nil {
		defer f.Close()
		w = io.MultiWriter(os.Stdout, f)
	}
	p := func(format string, a ...any) { fmt.Fprintf(w, format+"\n", a...) }

	p("packettrail %s self-test (%s/%s), %s", version, runtime.GOOS, runtime.GOARCH, time.Now().Format(time.RFC3339))
	p("elevated/root: %v", platform.IsElevated())
	capt, procs := capture.New(capture.Options{Unprivileged: c.limited})
	eng := core.NewEngine(procs)
	eng.Loopback = c.loopback
	enr := enrich.New(c.dataDir, nil)
	defer enr.Close()
	gs := enr.Status()
	p("GeoIP: city=%q (%s) asn=%q (%s) in %s", gs.City, gs.CityDate, gs.ASN, gs.ASNDate, gs.Dir)

	if err := capt.Start(eng.Sink()); err != nil {
		p("CAPTURE FAILED TO START: %v", err)
		return 2
	}
	st := capt.Status()
	p("capture backend: %s, mode: %s", st.Backend, st.Mode)
	p("  %s", st.Detail)
	p("capturing for %d s — generate some traffic (open a website, start a download)…", c.selftest)
	for i := 0; i < c.selftest; i++ {
		time.Sleep(time.Second)
		in, out, rate, _ := eng.Totals()
		fmt.Printf("\r  %2ds  down %s (%s/s)  up %s (%s/s)      ", i+1, human(in), human(uint64(rate.In)), human(out), human(uint64(rate.Out)))
	}
	fmt.Println()
	capt.Stop()
	st = capt.Status()
	in, out, _, events := eng.Totals()
	p("")
	p("events from capture: %d, lost: %d, DNS answers seen: %d (cache + live)", st.Events, st.Lost, eng.DNS.Events())
	if st.Error != "" {
		p("capture warnings: %s", st.Error)
	}
	for _, n := range st.Notes {
		p("note: %s", n)
	}
	p("flow events kept: %d, bytes down %s, up %s", events, human(in), human(out))

	rows := eng.SessionRows()
	apps := eng.Apps()
	type tot struct {
		key     string
		in, out uint64
		n       int
	}
	byApp := map[string]*tot{}
	byIP := map[string]*tot{}
	hosts := 0
	for _, r := range rows {
		a := byApp[r.App]
		if a == nil {
			a = &tot{key: r.App}
			byApp[r.App] = a
		}
		a.in += r.In
		a.out += r.Out
		a.n++
		ip := r.IP.String()
		b := byIP[ip]
		if b == nil {
			b = &tot{key: ip}
			byIP[ip] = b
		}
		b.in += r.In
		b.out += r.Out
		if r.Host != "" || eng.DNS.Get(r.IP) != "" {
			hosts++
		}
	}
	sortT := func(m map[string]*tot) []*tot {
		l := make([]*tot, 0, len(m))
		for _, v := range m {
			l = append(l, v)
		}
		sort.Slice(l, func(i, j int) bool {
			if l[i].in+l[i].out != l[j].in+l[j].out {
				return l[i].in+l[i].out > l[j].in+l[j].out
			}
			return l[i].n > l[j].n
		})
		return l
	}
	p("")
	p("apps with network activity: %d, endpoints: %d, endpoints with a DNS name: %d", len(byApp), len(rows), hosts)
	p("%-40s %10s %10s %6s  PIDs", "app", "down", "up", "flows")
	for i, a := range sortT(byApp) {
		if i == 15 {
			break
		}
		m := apps[a.key]
		pids := make([]string, 0, len(m.PIDs))
		for _, pid := range m.PIDs {
			pids = append(pids, fmt.Sprint(pid))
		}
		p("%-40.40s %10s %10s %6d  %s", m.Name, human(a.in), human(a.out), a.n, strings.Join(pids, ","))
	}
	p("")
	p("%-40s %10s %10s  %s", "remote IP", "down", "up", "geo / ASN / name")
	time.Sleep(10 * time.Millisecond)
	for i, b := range sortT(byIP) {
		if i == 15 {
			break
		}
		ip := rows[0].IP
		for _, r := range rows {
			if r.IP.String() == b.key {
				ip = r.IP
				break
			}
		}
		info := enr.Lookup(ip)
		geo := info.Scope
		if geo == "" {
			geo = strings.Trim(info.CC+" "+info.City, " ")
			if info.ASN != 0 {
				geo += fmt.Sprintf("  AS%d %s", info.ASN, info.ASOrg)
			}
		}
		p("%-40s %10s %10s  %s  %s", b.key, human(b.in), human(b.out), geo, eng.DNS.Get(ip))
	}
	p("")
	result := 0
	switch {
	case st.Mode != "full":
		p("RESULT: connections-only mode (no byte counts). Run as administrator/root for full capture.")
		result = 1
	case in+out == 0:
		p("RESULT: capture started but no traffic bytes were counted. Please report this.")
		result = 1
	default:
		p("RESULT: OK — per-app byte counting works.")
	}
	if f != nil {
		p("report saved to %s", path)
	}
	return result
}
