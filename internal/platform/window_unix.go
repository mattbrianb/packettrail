//go:build darwin || linux

package platform

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/abemedia/go-webview"
	"github.com/ebitengine/purego"
)

var (
	winMu sync.Mutex
	win   webview.WebView
)

// RunWindow shows the dashboard in a native window (WKWebView on macOS,
// WebKitGTK on Linux) and blocks until it is closed. It must be called from
// the main goroutine, which go-webview pins to the main OS thread. It returns
// false without showing anything when a native window is not possible, so the
// caller can fall back to a browser.
func RunWindow(url, dataDir, title string) (shown bool) {
	if os.Geteuid() == 0 {
		// A root GUI (and root WebKit) is a bad idea; under sudo the dashboard
		// opens in the invoking user's browser instead.
		return false
	}
	if runtime.GOOS == "linux" && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return false // no desktop session (SSH, console)
	}
	dir, err := installWebviewLib(dataDir)
	if err != nil {
		log.Printf("native window unavailable: %v", err)
		return false
	}
	// Load it once ourselves first: go-webview panics (and cannot retry) when
	// the library or its dependencies, such as WebKitGTK, are missing.
	if _, err := purego.Dlopen(filepath.Join(dir, webviewLibName), purego.RTLD_LAZY|purego.RTLD_GLOBAL); err != nil {
		log.Printf("native window unavailable: %v", err)
		if runtime.GOOS == "linux" {
			log.Printf("install WebKitGTK for the app window, e.g. sudo apt install libwebkit2gtk-4.1-0")
		}
		return false
	}
	os.Setenv("WEBVIEW_PATH", dir)
	defer func() {
		if r := recover(); r != nil {
			log.Printf("native window failed: %v", r)
			shown = false
		}
	}()
	w := webview.New(false)
	w.SetTitle(title)
	width, height := windowSize()
	w.SetSize(width, height, webview.HintNone)
	placeWindow(w.Window())
	winMu.Lock()
	win = w
	winMu.Unlock()
	w.Navigate(url)
	w.Run()
	winMu.Lock()
	win = nil
	winMu.Unlock()
	w.Destroy()
	return true
}

// CloseWindow closes the window opened by RunWindow, if any. Safe from any goroutine.
func CloseWindow() {
	winMu.Lock()
	defer winMu.Unlock()
	if win != nil {
		win.Terminate()
	}
}

// installWebviewLib writes the embedded libwebview into <data>/lib (a folder
// only this user can write) and returns that folder. An existing copy is
// reused only if it matches the embedded bytes exactly.
func installWebviewLib(dataDir string) (string, error) {
	if len(webviewLib) == 0 {
		return "", errors.New("no webview library for " + runtime.GOOS + "/" + runtime.GOARCH)
	}
	dir := filepath.Join(dataDir, "lib")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, webviewLibName)
	if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, webviewLib) {
		return dir, nil
	}
	tmp, err := os.CreateTemp(dir, ".lib-*")
	if err != nil {
		return "", err
	}
	_, werr := tmp.Write(webviewLib)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("writing %s: %v %v", path, werr, cerr)
	}
	os.Chmod(tmp.Name(), 0o755)
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return dir, nil
}

// OpenAppWindow opens url in a Chromium-based browser's app mode (its own
// window, no tabs or address bar) when one is installed, using a private
// profile so the window is its own browser process. The returned channel
// closes when that window is closed. It returns nil if no such browser was
// found, so the caller can use the default browser.
func OpenAppWindow(url, dataDir string) <-chan struct{} {
	if os.Geteuid() == 0 {
		return nil
	}
	var candidates []string
	if runtime.GOOS == "darwin" {
		for _, app := range []string{"Google Chrome", "Microsoft Edge", "Chromium", "Brave Browser"} {
			candidates = append(candidates, "/Applications/"+app+".app/Contents/MacOS/"+app)
		}
	} else {
		for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "microsoft-edge", "microsoft-edge-stable", "brave-browser"} {
			if p, err := exec.LookPath(name); err == nil {
				candidates = append(candidates, p)
			}
		}
	}
	for _, bin := range candidates {
		if _, err := os.Stat(bin); err != nil {
			continue
		}
		profile := filepath.Join(dataDir, "browser-window")
		cmd := exec.Command(bin, "--app="+url, "--user-data-dir="+profile, "--no-first-run", "--no-default-browser-check", "--window-size=1280,820")
		if err := cmd.Start(); err == nil {
			log.Printf("dashboard opened in %s app window", filepath.Base(strings.TrimSuffix(bin, "/")))
			done := make(chan struct{})
			go func() { cmd.Wait(); close(done) }()
			return done
		}
	}
	return nil
}
