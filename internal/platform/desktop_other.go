//go:build !linux

package platform

import "errors"

// InstallLauncher is Linux-only: Windows runs the exe directly and macOS uses
// the PacketTrail.app bundle.
func InstallLauncher(exe string) (string, error) {
	return "", errors.New("--install is only needed on Linux")
}

func UninstallLauncher() error { return errors.New("--uninstall is only needed on Linux") }

func LauncherInstalled() bool { return true }
