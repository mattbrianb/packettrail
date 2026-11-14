// Command packettrail is a portable per-application network traffic monitor.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"packettrail/internal/capture"
	"packettrail/internal/core"
	"packettrail/internal/enrich"
	"packettrail/internal/monitor"
	"packettrail/internal/peering"
	"packettrail/internal/platform"
	"packettrail/internal/store"
	"packettrail/internal/sysmon"
	"packettrail/internal/tools"
	"packettrail/internal/web"
)

var version = "1.1.0"

// Publisher and copyright, shown by --version and in the app's About section.
// Keep in step with winres/winres.json (the Windows file details).
const (
	publisher = "Matthew Burnett"
	copyright = "© 2026 Matthew Burnett"
)

type config struct {
	port       int
	dataDir    string
	noElevate  bool
	limited    bool
	browser    bool
	headless   bool
	selftest   int
	retention  int
	loopback   bool
	relaunched bool
	demo       bool
	noGeoDL    bool
}

func main() {
	var c config
	flag.IntVar(&c.port, "port", 47810, "dashboard port on 127.0.0.1 (falls back to a free port if taken)")
	flag.StringVar(&c.dataDir, "data", "", "data folder for the history database and GeoIP files (default: 'data' next to the executable)")
	flag.BoolVar(&c.noElevate, "no-elevate", false, "Windows: do not ask for administrator rights at start")
	flag.BoolVar(&c.limited, "limited", false, "force connections-only mode (no byte counts)")
	flag.BoolVar(&c.browser, "browser", false, "Windows: open the dashboard in the default browser instead of a window")
	flag.BoolVar(&c.headless, "headless", false, "do not open any UI; just capture and serve the dashboard")
	flag.IntVar(&c.selftest, "selftest", 0, "capture for N seconds, print a diagnostic report and exit")
	flag.IntVar(&c.retention, "retention", 90, "days of history to keep")
	flag.BoolVar(&c.loopback, "loopback", false, "include loopback (127.0.0.1 / ::1) traffic")
	flag.BoolVar(&c.relaunched, "relaunched", false, "internal: set when restarted elevated")
	flag.BoolVar(&c.demo, "demo", false, "test aid: real connections with synthetic byte counts, stored in packettrail-demo.db")
	flag.BoolVar(&c.noGeoDL, "no-geoip-download", false, "do not download the DB-IP Lite databases automatically when they are missing")
	showVersion := flag.Bool("version", false, "print version and exit")
	install := flag.Bool("install", false, "Linux: add PacketTrail to your applications menu, then exit")
	uninstall := flag.Bool("uninstall", false, "Linux: remove PacketTrail from your applications menu, then exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("PacketTrail", version, runtime.GOOS+"/"+runtime.GOARCH)
		fmt.Println(copyright)
		return
	}
	if *install || *uninstall {
		os.Exit(launcher(*install))
	}

	dataDir, err := resolveDataDir(c.dataDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "data folder:", err)
		os.Exit(1)
	}
	c.dataDir = dataDir
	if c.selftest > 0 {
		os.Exit(selfTest(c))
	}

	logf, _ := os.OpenFile(filepath.Join(dataDir, "packettrail.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if logf != nil {
		trimLog(logf)
		log.SetOutput(io.MultiWriter(os.Stderr, logf))
	}
	log.Printf("packettrail %s starting (%s/%s), data folder %s", version, runtime.GOOS, runtime.GOARCH, dataDir)

	// Another instance already running? Just show it.
	if !c.relaunched {
		if url := probe(c.port); url != "" {
			log.Printf("already running at %s", url)
			if !c.headless {
				platform.OpenURL(url)
			}
			return
		}
	}

	if runtime.GOOS == "windows" && !platform.IsElevated() && !c.noElevate && !c.limited && !c.relaunched && !c.demo {
		if err := platform.RelaunchElevated(relaunchArgs()); err == nil {
			log.Printf("restarted with administrator rights")
			return
		} else {
			log.Printf("elevation not granted (%v): running in connections-only mode", err)
		}
	}
	if err := run(c); err != nil {
		log.Printf("fatal: %v", err)
		os.Exit(1)
	}
}

// launcher adds or removes the Linux applications-menu entry.
func launcher(add bool) int {
	if !add {
		if err := platform.UninstallLauncher(); err != nil {
			fmt.Fprintln(os.Stderr, "uninstall:", err)
			return 1
		}
		fmt.Println("Removed PacketTrail from your applications menu.")
		return 0
	}
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "install:", err)
		return 1
	}
	entry, err := platform.InstallLauncher(exe)
	if err != nil {
		fmt.Fprintln(os.Stderr, "install:", err)
		return 1
	}
	fmt.Println("Added PacketTrail to your applications menu:", entry)
	fmt.Println("It runs", exe, "- keep the file there, or run --install again after moving it.")
	return 0
}

func relaunchArgs() []string {
	args := []string{}
	for _, a := range os.Args[1:] {
		if a != "-relaunched" && a != "--relaunched" {
			args = append(args, a)
		}
	}
	return append(args, "--relaunched")
}

// resolveDataDir prefers a "data" folder next to the executable (portable
// use); if that is not writable it uses the per-user config folder.
func resolveDataDir(flagVal string) (string, error) {
	try := func(d string) bool {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return false
		}
		f, err := os.CreateTemp(d, ".write-test-*")
		if err != nil {
			return false
		}
		f.Close()
		os.Remove(f.Name())
		return true
	}
	if flagVal != "" {
		if !try(flagVal) {
			return "", fmt.Errorf("%s is not writable", flagVal)
		}
		return filepath.Abs(flagVal)
	}
	if exe, err := os.Executable(); err == nil && !insideAppBundle(exe) {
		if d := filepath.Join(filepath.Dir(exe), "data"); try(d) {
			return d, nil
		}
	}
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	d := filepath.Join(cfg, "packettrail")
	if !try(d) {
		return "", fmt.Errorf("no writable data folder (tried next to the executable and %s)", d)
	}
	return d, nil
}

// insideAppBundle reports whether exe runs from a macOS .app bundle. Data is
// never written into a bundle: that would break its signature, and a freshly
// downloaded app runs from a read-only, translocated copy anyway.
func insideAppBundle(exe string) bool {
	return strings.Contains(filepath.ToSlash(exe), ".app/Contents/MacOS/")
}

func trimLog(f *os.File) {
	if st, err := f.Stat(); err == nil && st.Size() > 5<<20 {
		f.Truncate(0)
		f.Seek(0, io.SeekStart)
	}
}

// probe returns the dashboard URL if a packettrail instance answers on port.
func probe(port int) string {
	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	cl := http.Client{Timeout: 700 * time.Millisecond}
	resp, err := cl.Get(url + "api/status")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if strings.Contains(string(b), `"app":"packettrail"`) {
		return url
	}
	return ""
}

type nullCapture struct{ err string }

func (n nullCapture) Start(capture.Sink) error { return nil }
func (n nullCapture) Stop()                    {}
func (n nullCapture) Status() capture.Status {
	return capture.Status{Backend: "none", Mode: "none", Error: n.err}
}

func run(c config) error {
	dbName := "packettrail.db"
	if c.demo {
		dbName = "packettrail-demo.db"
	}
	st, err := store.Open(filepath.Join(c.dataDir, dbName))
	if err != nil {
		return fmt.Errorf("open history database: %w", err)
	}
	defer st.Close()
	enr := enrich.New(c.dataDir, st)
	defer enr.Close()
	// First start: fetch the free DB-IP Lite databases so locations and
	// networks show up without a trip to Settings.
	if enr.Missing() && !c.noGeoDL {
		go func() {
			time.Sleep(2 * time.Second) // let the window open first
			log.Printf("GeoIP databases missing; downloading DB-IP Lite")
			if err := enr.Download(); err != nil {
				log.Printf("GeoIP download failed: %v (retry from Settings)", err)
			}
		}()
	}

	capt, procs := capture.New(capture.Options{Unprivileged: c.limited})
	if c.demo {
		capt = newDemoCapture()
	}
	eng := core.NewEngine(procs)
	eng.Loopback = c.loopback
	eng.DNS.Load(st.LoadDNS(time.Now().Add(-14 * 24 * time.Hour).Unix()))
	var active capture.Capturer = capt
	if err := capt.Start(eng.Sink()); err != nil {
		log.Printf("capture failed to start: %v", err)
		active = nullCapture{err: err.Error()}
	} else {
		s := capt.Status()
		log.Printf("capture: %s (%s mode) %s", s.Backend, s.Mode, s.Detail)
	}

	mons := monitor.New(st, tools.Echo)
	if err := mons.Start(); err != nil {
		log.Printf("monitors: %v", err)
	}
	defer mons.Stop()
	sys := sysmon.New(2 * time.Second)
	sys.Start()
	defer sys.Stop()

	var flushMu sync.Mutex
	flush := func() {
		flushMu.Lock()
		defer flushMu.Unlock()
		mons.Flush()
		f, s, a := eng.TakePending()
		d := eng.DNS.TakeDirty()
		if err := st.Write(f, s, a, d); err != nil {
			log.Printf("history write failed: %v", err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var bg sync.WaitGroup
	bg.Add(1)
	go func() {
		defer bg.Done()
		ft := time.NewTicker(10 * time.Second)
		mt := time.NewTicker(time.Minute)
		defer ft.Stop()
		defer mt.Stop()
		lastMaint := time.Time{}
		for {
			select {
			case <-ctx.Done():
				return
			case <-ft.C:
				flush()
			case <-mt.C:
				eng.Maintain()
				if time.Since(lastMaint) > time.Hour {
					lastMaint = time.Now()
					flushMu.Lock()
					if err := st.Maintain(c.retention); err != nil {
						log.Printf("history maintenance: %v", err)
					}
					flushMu.Unlock()
				}
			}
		}
	}()

	shutdown := make(chan struct{})
	var shutOnce sync.Once
	stop := func() { shutOnce.Do(func() { close(shutdown); platform.CloseWindow() }) }

	opts := web.Options{
		System:     sys,
		Version:    version,
		Copyright:  copyright,
		HostName:   map[bool]string{true: "DEMO-PC"}[c.demo],
		DataDir:    c.dataDir,
		CanElevate: platform.CanElevate() && !platform.IsElevated(),
		Flush:      flush,
		OpenURL:    platform.OpenURL,
		Reveal:     platform.Reveal,
		ListSep:    platform.ListSeparator(),
		ExportDir:  platform.DownloadsDir(),
		AppIcon:    platform.AppIcon,
		Monitors:   mons,
		Gateway:    platform.DefaultGateway,
		DNSServers: platform.DNSServers,
		Peering:    peering.NewDetector(st, enr, tools.Echo),
		PeeringDB:  peering.NewClient(st),
	}
	if opts.ExportDir == "" {
		opts.ExportDir = filepath.Join(c.dataDir, "exports")
	}
	if opts.CanElevate {
		opts.Elevate = func() error {
			if err := platform.RelaunchElevated(relaunchArgs()); err != nil {
				return err
			}
			go func() { time.Sleep(300 * time.Millisecond); stop() }()
			return nil
		}
	}
	srv := web.New(eng, st, enr, active, opts)

	l, err := listenWait(c.port, c.relaunched)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/", l.Addr().(*net.TCPAddr).Port)
	hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go hs.Serve(l)
	log.Printf("dashboard: %s", url)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case <-sig:
			stop()
		case <-shutdown:
		}
	}()

	switch {
	case c.headless:
		fmt.Println("Dashboard:", url, "(Ctrl+C to stop)")
		<-shutdown
	case !c.browser && platform.RunWindow(url, c.dataDir, "PacketTrail"):
		stop() // window closed
	default:
		fmt.Println("Dashboard:", url, "(Ctrl+C to stop)")
		var closed <-chan struct{} // set when the dashboard has its own app window
		if !c.browser {
			closed = platform.OpenAppWindow(url, c.dataDir)
		}
		if closed == nil {
			if err := platform.OpenURL(url); err != nil {
				fmt.Println("Open this address in your browser:", url)
			}
		}
		if runtime.GOOS == "linux" && !platform.LauncherInstalled() {
			fmt.Println("Tip: run PacketTrail with --install to add it to your applications menu.")
		}
		select {
		case <-shutdown:
		case <-closed: // closing the app window quits PacketTrail, as with the native window
			stop()
		}
	}

	log.Printf("shutting down")
	sctx, scancel := context.WithTimeout(context.Background(), 2*time.Second)
	hs.Shutdown(sctx)
	scancel()
	capt.Stop()
	cancel()
	bg.Wait()
	flush()
	return nil
}

// listenWait binds the dashboard port. After an elevated restart the old
// instance may still hold the port for a moment, so wait for it.
func listenWait(port int, wait bool) (net.Listener, error) {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	deadline := time.Now().Add(10 * time.Second)
	for {
		l, err := net.Listen("tcp", addr)
		if err == nil {
			return l, nil
		}
		if !wait || time.Now().After(deadline) {
			log.Printf("port %d unavailable (%v); using a random port", port, err)
			return net.Listen("tcp", "127.0.0.1:0")
		}
		time.Sleep(250 * time.Millisecond)
	}
}
