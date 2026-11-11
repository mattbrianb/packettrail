//go:build !windows

package platform

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"net/netip"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// DefaultGateway returns the IPv4 default gateway (Linux: /proc/net/route,
// macOS: `route -n get default`).
func DefaultGateway() netip.Addr {
	if runtime.GOOS == "linux" {
		f, err := os.Open("/proc/net/route")
		if err != nil {
			return netip.Addr{}
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fs := strings.Fields(sc.Text())
			if len(fs) < 3 || fs[1] != "00000000" {
				continue
			}
			b, err := hex.DecodeString(fs[2])
			if err != nil || len(b) != 4 {
				continue
			}
			v := binary.LittleEndian.Uint32(b)
			if v == 0 {
				continue
			}
			return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}).Unmap()
		}
		return netip.Addr{}
	}
	out, err := exec.Command("route", "-n", "get", "default").Output()
	if err != nil {
		return netip.Addr{}
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "gateway:") {
			a, _ := netip.ParseAddr(strings.TrimSpace(strings.TrimPrefix(line, "gateway:")))
			return a
		}
	}
	return netip.Addr{}
}

// DNSServers reads nameserver lines from /etc/resolv.conf.
func DNSServers() []netip.Addr {
	b, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		return nil
	}
	var out []netip.Addr
	for _, line := range strings.Split(string(b), "\n") {
		fs := strings.Fields(line)
		if len(fs) >= 2 && fs[0] == "nameserver" {
			if a, err := netip.ParseAddr(fs[1]); err == nil {
				out = append(out, a)
			}
		}
	}
	return out
}
