//go:build !windows

package main

import "packettrail/internal/capture"

func newDemoCapture() capture.Capturer {
	return nullCapture{err: "--demo is only implemented on Windows"}
}
