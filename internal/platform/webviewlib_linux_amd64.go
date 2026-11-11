package platform

import _ "embed"

//go:embed webviewlib/linux_amd64/libwebview.so
var webviewLib []byte

const webviewLibName = "libwebview.so"
