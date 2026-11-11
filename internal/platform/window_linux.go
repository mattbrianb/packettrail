package platform

import "unsafe"

// windowSize is the starting size of the GTK window; the window manager
// shrinks it to fit smaller screens.
func windowSize() (w, h int) { return 1280, 820 }

func placeWindow(win unsafe.Pointer) {}
