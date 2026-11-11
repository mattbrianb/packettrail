package platform

import _ "embed"

//go:embed webviewlib/darwin_arm64/libwebview.dylib
var webviewLib []byte

const webviewLibName = "libwebview.dylib"
