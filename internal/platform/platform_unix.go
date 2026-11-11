//go:build !windows

package platform

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
)

func IsElevated() bool { return os.Geteuid() == 0 }

func CanElevate() bool { return false }

func RelaunchElevated(args []string) error {
	return errors.New("restart the program with sudo for full capture")
}

func HideOwnConsole() {}

// OpenURL opens url in the desktop user's browser. When running under sudo
// it switches back to the invoking user so the browser does not run as root.
func OpenURL(url string) error {
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	if os.Geteuid() == 0 {
		if u := os.Getenv("SUDO_USER"); u != "" && u != "root" {
			args := []string{"-u", u}
			if runtime.GOOS != "darwin" {
				args = append(args, "env")
				for _, k := range []string{"DISPLAY", "WAYLAND_DISPLAY", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS"} {
					if v := os.Getenv(k); v != "" {
						args = append(args, k+"="+v)
					}
				}
			}
			args = append(args, opener, url)
			return exec.Command("sudo", args...).Start()
		}
	}
	return exec.Command(opener, url).Start()
}

// DownloadsDir returns ~/Downloads (of the sudo-invoking user if any), or "".
func DownloadsDir() string {
	home, _ := os.UserHomeDir()
	if u := os.Getenv("SUDO_USER"); os.Geteuid() == 0 && u != "" {
		if runtime.GOOS == "darwin" {
			home = "/Users/" + u
		} else {
			home = "/home/" + u
		}
	}
	if home == "" {
		return ""
	}
	d := home + "/Downloads"
	if st, err := os.Stat(d); err == nil && st.IsDir() {
		return d
	}
	return ""
}

// Reveal shows the file in the system file manager.
func Reveal(path string) error {
	if runtime.GOOS == "darwin" {
		return exec.Command("open", "-R", path).Start()
	}
	dir := path
	if i := lastSlash(path); i > 0 {
		dir = path[:i]
	}
	return OpenURL(dir)
}

func lastSlash(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			return i
		}
	}
	return -1
}

func ListSeparator() string { return "," }
