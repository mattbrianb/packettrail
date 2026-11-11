package platform

import _ "embed"

//go:embed webviewlib/darwin_amd64/libwebview.dylib
var webviewLib []byte

const webviewLibName = "libwebview.dylib"
