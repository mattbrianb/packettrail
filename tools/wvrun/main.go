//go:build darwin || linux

// wvrun loads a page in the system WebKit (WKWebView on macOS, WebKitGTK on
// Linux) and prints what the page reports. It is how WebKit-only behaviour is
// tested from a script: Safari's engine differs from Chrome's in ways a
// Windows machine never shows (scrolling, transitions, canvas sizing).
//
// The page calls window.wvReport(text) for each line of output and
// window.wvDone() when it has finished.
//
//	wvrun -lib internal/platform/webviewlib/linux_amd64 -dir testpages -page scroll.html
//	wvrun -lib ... -url http://127.0.0.1:47810/ -init steps.js
package main

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/abemedia/go-webview"
)

func main() {
	lib := flag.String("lib", "", "folder holding libwebview.so / libwebview.dylib")
	dir := flag.String("dir", "", "folder to serve on a local port")
	page := flag.String("page", "index.html", "page inside -dir to open")
	url := flag.String("url", "", "open this URL instead of serving -dir")
	initJS := flag.String("init", "", "JavaScript file injected into every page before it loads")
	timeout := flag.Duration("timeout", 60*time.Second, "give up after this long")
	width := flag.Int("w", 1200, "window width")
	height := flag.Int("h", 800, "window height")
	flag.Parse()

	if *lib != "" {
		abs, err := filepath.Abs(*lib)
		if err != nil {
			fail(err)
		}
		os.Setenv("WEBVIEW_PATH", abs)
	}
	target := *url
	if target == "" {
		if *dir == "" {
			fail(fmt.Errorf("give -dir or -url"))
		}
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			fail(err)
		}
		go http.Serve(l, http.FileServer(http.Dir(*dir)))
		target = fmt.Sprintf("http://%s/%s", l.Addr(), *page)
	}

	w := webview.New(true)
	defer w.Destroy()
	w.SetTitle("wvrun")
	w.SetSize(*width, *height, webview.HintNone)
	code := 0
	w.Bind("wvReport", func(s string) { fmt.Println(s) })
	w.Bind("wvDone", func() { w.Terminate() })
	if *initJS != "" {
		js, err := os.ReadFile(*initJS)
		if err != nil {
			fail(err)
		}
		w.Init(string(js))
	}
	go func() {
		time.Sleep(*timeout)
		fmt.Println("wvrun: timed out")
		code = 3
		w.Terminate()
	}()
	w.Navigate(target)
	w.Run()
	os.Exit(code)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "wvrun:", err)
	os.Exit(2)
}
