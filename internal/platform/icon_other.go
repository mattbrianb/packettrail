//go:build !windows

package platform

import "errors"

// AppIcon is only implemented on Windows; the dashboard falls back to a
// lettered tile elsewhere.
func AppIcon(path string, size int) ([]byte, error) { return nil, errors.New("not supported") }
