//go:build windows

package platform

import (
	"net/netip"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modIphlpapi          = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetBestRoute     = modIphlpapi.NewProc("GetBestRoute")
	procGetNetworkParams = modIphlpapi.NewProc("GetNetworkParams")
)

// mibIPForwardRow is MIB_IPFORWARDROW (14 DWORDs).
type mibIPForwardRow struct {
	Dest, Mask, Policy, NextHop, IfIndex, Type, Proto, Age, NextHopAS, Metric1, Metric2, Metric3, Metric4, Metric5 uint32
}

// DefaultGateway returns the next hop used to reach the internet.
func DefaultGateway() netip.Addr {
	var row mibIPForwardRow
	dst := uint32(1) | 1<<8 | 1<<16 | 1<<24 // 1.1.1.1 as an in_addr DWORD
	if r, _, _ := procGetBestRoute.Call(uintptr(dst), 0, uintptr(unsafe.Pointer(&row))); r != 0 || row.NextHop == 0 {
		return netip.Addr{}
	}
	nh := row.NextHop
	return netip.AddrFrom4([4]byte{byte(nh), byte(nh >> 8), byte(nh >> 16), byte(nh >> 24)})
}

type ipAddrString struct {
	Next      *ipAddrString
	IPAddress [16]byte
	IPMask    [16]byte
	Context   uint32
}

// DNSServers returns the system's configured IPv4 DNS servers.
func DNSServers() []netip.Addr {
	var size uint32
	procGetNetworkParams.Call(0, uintptr(unsafe.Pointer(&size)))
	if size == 0 || size > 1<<20 {
		return nil
	}
	buf := make([]byte, size)
	if r, _, _ := procGetNetworkParams.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size))); r != 0 {
		return nil
	}
	// FIXED_INFO: HostName[132], DomainName[132], CurrentDnsServer*, DnsServerList (IP_ADDR_STRING) at 272 on x64.
	const listOff = 264 + unsafe.Sizeof(uintptr(0))
	var out []netip.Addr
	seen := map[netip.Addr]bool{}
	for p := (*ipAddrString)(unsafe.Pointer(&buf[listOff])); p != nil; p = p.Next {
		s := strings.TrimRight(string(p.IPAddress[:]), "\x00")
		if a, err := netip.ParseAddr(s); err == nil && !a.IsUnspecified() && !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
		if len(out) > 8 {
			break
		}
	}
	return out
}
