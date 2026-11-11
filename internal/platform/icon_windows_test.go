//go:build windows

package platform

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestAppIcon(t *testing.T) {
	exe := filepath.Join(os.Getenv("WINDIR"), "explorer.exe")
	b, err := AppIcon(exe, 64)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 64 {
		t.Fatalf("size %v", img.Bounds())
	}
	if out := os.Getenv("ICON_OUT"); out != "" {
		os.WriteFile(out, b, 0o644)
	}
	if _, err := AppIcon(`C:\definitely\missing.exe`, 64); err == nil {
		t.Fatal("expected error for missing file")
	}
}
