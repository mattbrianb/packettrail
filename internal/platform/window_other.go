//go:build !windows && !darwin && !linux

package platform

// RunWindow has no native window on this platform; the dashboard opens in
// the default browser.
func RunWindow(url, dataDir, title string) bool { return false }

func CloseWindow() {}

func OpenAppWindow(url, dataDir string) <-chan struct{} { return nil }
