package platform

import (
	"unsafe"

	"github.com/ebitengine/purego/objc"
)

type nsRect struct{ X, Y, W, H float64 }

// windowSize picks a window size for this Mac's main screen: four fifths of
// the usable area (as on Windows), never larger than the screen. Small
// laptops (1280x800) would otherwise get a window that hangs off the edge.
func windowSize() (w, h int) {
	w, h = 1180, 740 // fits every Mac screen if the screen can't be asked
	defer func() { recover() }()
	screen := objc.ID(objc.GetClass("NSScreen")).Send(objc.RegisterName("mainScreen"))
	if screen == 0 {
		return
	}
	r := objc.Send[nsRect](screen, objc.RegisterName("visibleFrame"))
	if r.W < 400 || r.H < 300 {
		return
	}
	w, h = int(r.W*4/5), int(r.H*4/5)
	if w < 1000 {
		w = min(1000, int(r.W)-20)
	}
	if h < 680 {
		h = min(680, int(r.H)-40)
	}
	return w, h
}

// placeWindow centres the NSWindow on its screen.
func placeWindow(win unsafe.Pointer) {
	if win == nil {
		return
	}
	defer func() { recover() }()
	objc.ID(win).Send(objc.RegisterName("center"))
}
