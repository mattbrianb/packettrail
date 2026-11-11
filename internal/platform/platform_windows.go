//go:build windows

// Package platform holds OS-specific glue: elevation, opening URLs and the
// native window that hosts the dashboard.
package platform

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	modKernel32               = windows.NewLazySystemDLL("kernel32.dll")
	modUser32                 = windows.NewLazySystemDLL("user32.dll")
	procGetConsoleWindow      = modKernel32.NewProc("GetConsoleWindow")
	procGetConsoleProcessList = modKernel32.NewProc("GetConsoleProcessList")
	procShowWindow            = modUser32.NewProc("ShowWindow")
	procGetSystemMetrics      = modUser32.NewProc("GetSystemMetrics")
)

func IsElevated() bool { return windows.GetCurrentProcessToken().IsElevated() }

// CanElevate reports whether RelaunchElevated is supported.
func CanElevate() bool { return true }

// RelaunchElevated starts this executable again via the UAC "runas" verb.
// It returns an error if the user declined the prompt.
func RelaunchElevated(args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = windows.EscapeArg(a)
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	params, _ := windows.UTF16PtrFromString(strings.Join(quoted, " "))
	dir, _ := windows.UTF16PtrFromString(filepath.Dir(exe))
	return windows.ShellExecute(0, verb, file, params, dir, windows.SW_SHOWNORMAL)
}

// HideOwnConsole hides the console window when this process is its only
// user (i.e. it was started by double-clicking rather than from a terminal).
func HideOwnConsole() {
	hwnd, _, _ := procGetConsoleWindow.Call()
	if hwnd == 0 {
		return
	}
	var pids [4]uint32
	n, _, _ := procGetConsoleProcessList.Call(uintptr(unsafe.Pointer(&pids[0])), 4)
	if n == 1 {
		procShowWindow.Call(hwnd, 0) // SW_HIDE
	}
}

// OpenURL opens url in the user's default browser. Going through
// explorer.exe makes the browser start un-elevated even when we run as admin.
func OpenURL(url string) error {
	return exec.Command("explorer.exe", url).Start()
}

// DownloadsDir returns the user's Downloads folder.
func DownloadsDir() string {
	if p, err := windows.KnownFolderPath(windows.FOLDERID_Downloads, 0); err == nil && p != "" {
		return p
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, "Downloads")
	}
	return ""
}

// Reveal shows a file selected in Explorer.
func Reveal(path string) error {
	return exec.Command("explorer.exe", "/select,"+path).Start()
}

var procGetLocaleInfoEx = modKernel32.NewProc("GetLocaleInfoEx")

// ListSeparator returns the user's regional list separator, which is what
// Excel expects as the CSV delimiter (";" in decimal-comma locales).
func ListSeparator() string {
	buf := make([]uint16, 8)
	const localeSList = 0x0C
	n, _, _ := procGetLocaleInfoEx.Call(0, localeSList, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n > 1 {
		if s := windows.UTF16ToString(buf[:n-1]); s != "" {
			return s
		}
	}
	return ","
}

var (
	wvMu sync.Mutex
	wv   webview2.WebView
)

// RunWindow shows the dashboard in a native WebView2 window and blocks until
// it is closed. It returns false if WebView2 is not available.
func RunWindow(url, dataDir, title string) bool {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	sw, _, _ := procGetSystemMetrics.Call(0) // SM_CXSCREEN
	sh, _, _ := procGetSystemMetrics.Call(1) // SM_CYSCREEN
	width, height := uint(sw)*4/5, uint(sh)*4/5
	if width < 1000 {
		width, height = 1000, 700
	}
	// Keep the embedded browser lean: the dashboard needs no GPU
	// acceleration, and one renderer process is plenty.
	if os.Getenv("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS") == "" {
		os.Setenv("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS",
			"--disable-gpu --disable-gpu-compositing --renderer-process-limit=1 --disable-background-networking --disable-component-update --disable-features=msSmartScreenProtection,Translate")
	}
	// Paint the window in the app's background colour before the page loads,
	// so a dark-mode launch never flashes white.
	dark := systemDark()
	if os.Getenv("WEBVIEW2_DEFAULT_BACKGROUND_COLOR") == "" {
		if dark {
			os.Setenv("WEBVIEW2_DEFAULT_BACKGROUND_COLOR", "FF0C0D10")
		} else {
			os.Setenv("WEBVIEW2_DEFAULT_BACKGROUND_COLOR", "FFF2F3F5")
		}
	}
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		AutoFocus: true,
		DataPath:  filepath.Join(dataDir, "webview2"),
		WindowOptions: webview2.WindowOptions{
			Title: title, Width: width, Height: height, Center: true, IconId: 1,
		},
	})
	if w == nil {
		return false
	}
	// The window is up, so the console (if we own it) is no longer needed.
	HideOwnConsole()
	hwnd := uintptr(w.Window())
	setDarkTitleBar(hwnd, dark)
	// The page tells us its resolved appearance so the title bar matches it.
	w.Bind("nmSetDark", func(d bool) { setDarkTitleBar(hwnd, d) })
	wvMu.Lock()
	wv = w
	wvMu.Unlock()
	w.Navigate(url)
	w.Run()
	wvMu.Lock()
	wv = nil
	wvMu.Unlock()
	return true
}

var (
	procDwmSetWindowAttribute = windows.NewLazySystemDLL("dwmapi.dll").NewProc("DwmSetWindowAttribute")
	procSetWindowPos          = modUser32.NewProc("SetWindowPos")
)

// systemDark reports whether Windows apps are set to dark mode.
func systemDark() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("AppsUseLightTheme")
	return err == nil && v == 0
}

// setDarkTitleBar switches the window frame between light and dark
// (DWMWA_USE_IMMERSIVE_DARK_MODE, Windows 10 20H1 and later).
func setDarkTitleBar(hwnd uintptr, dark bool) {
	if hwnd == 0 || procDwmSetWindowAttribute.Find() != nil {
		return
	}
	var v int32
	if dark {
		v = 1
	}
	procDwmSetWindowAttribute.Call(hwnd, 20, uintptr(unsafe.Pointer(&v)), 4)
	// SWP_NOSIZE|SWP_NOMOVE|SWP_NOZORDER|SWP_NOACTIVATE|SWP_FRAMECHANGED: repaint the frame now.
	procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0, 0x1|0x2|0x4|0x10|0x20)
}

// OpenAppWindow is not needed on Windows: WebView2 is the window, and the
// default browser is the fallback.
func OpenAppWindow(url, dataDir string) <-chan struct{} { return nil }

// CloseWindow closes the window opened by RunWindow, if any.
func CloseWindow() {
	wvMu.Lock()
	defer wvMu.Unlock()
	if wv != nil {
		wv.Destroy()
	}
}
