//go:build (darwin || linux) && !amd64 && !arm64

package platform

// No prebuilt webview library for this architecture: RunWindow reports that
// and the dashboard opens in a browser.
var webviewLib []byte

const webviewLibName = "libwebview"
