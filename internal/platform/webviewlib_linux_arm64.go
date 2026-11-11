package platform

import _ "embed"

//go:embed webviewlib/linux_arm64/libwebview.so
var webviewLib []byte

const webviewLibName = "libwebview.so"
