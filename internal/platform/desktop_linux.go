package platform

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed assets/icon.png
var appIcon []byte

// desktopPaths returns where the launcher and icon live for this user.
func desktopPaths() (entry, icon string, err error) {
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", "", err
		}
		data = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(data, "applications", "packettrail.desktop"),
		filepath.Join(data, "icons", "hicolor", "256x256", "apps", "packettrail.png"), nil
}

// InstallLauncher adds PacketTrail to this user's applications menu (a
// freedesktop .desktop entry plus icon) pointing at exe.
func InstallLauncher(exe string) (string, error) {
	if os.Geteuid() == 0 {
		return "", fmt.Errorf("run --install as your normal user, not with sudo")
	}
	entry, icon, err := desktopPaths()
	if err != nil {
		return "", err
	}
	for _, d := range []string{filepath.Dir(entry), filepath.Dir(icon)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(icon, appIcon, 0o644); err != nil {
		return "", err
	}
	// Exec quoting per the Desktop Entry spec: escape \ " ` $ inside double
	// quotes (the backslash twice, as string-value escaping applies first)
	// and double any % so it is not read as a field code.
	quoted := `"` + strings.NewReplacer(`\`, `\\\\`, `"`, `\\"`, "`", "\\\\`", "$", `\\$`, "%", "%%").Replace(exe) + `"`
	body := strings.Join([]string{
		"[Desktop Entry]",
		"Type=Application",
		"Name=PacketTrail",
		"GenericName=Network traffic monitor",
		"Comment=See which app is using your network, and where it goes",
		"Exec=" + quoted,
		"Icon=" + icon,
		"Terminal=false",
		"Categories=Network;Monitor;System;",
		"Keywords=network;bandwidth;traffic;monitor;mtr;traceroute;",
		"StartupWMClass=packettrail",
		"",
	}, "\n")
	if err := os.WriteFile(entry, []byte(body), 0o644); err != nil {
		return "", err
	}
	return entry, nil
}

// UninstallLauncher removes what InstallLauncher added.
func UninstallLauncher() error {
	entry, icon, err := desktopPaths()
	if err != nil {
		return err
	}
	for _, p := range []string{entry, icon} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// LauncherInstalled reports whether the applications-menu entry exists.
func LauncherInstalled() bool {
	entry, _, err := desktopPaths()
	if err != nil {
		return false
	}
	_, err = os.Stat(entry)
	return err == nil
}
