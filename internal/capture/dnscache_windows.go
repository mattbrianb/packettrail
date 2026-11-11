//go:build windows

package capture

import (
	"net/netip"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Reads the Windows DNS client cache (no admin needed) so traffic can be
// labelled with names that were resolved before or outside our ETW session.

var (
	modDnsapi                = windows.NewLazySystemDLL("dnsapi.dll")
	procDnsGetCacheDataTable = modDnsapi.NewProc("DnsGetCacheDataTable")
	procDnsQueryW            = modDnsapi.NewProc("DnsQuery_W")
	procDnsRecordListFree    = modDnsapi.NewProc("DnsRecordListFree")
	procDnsFree              = modDnsapi.NewProc("DnsFree")
)

const (
	dnsTypeA          = 1
	dnsTypeCNAME      = 5
	dnsTypeAAAA       = 28
	dnsQueryNoWire    = 0x10
	dnsFreeFlat       = 0
	dnsFreeRecordList = 1
)

type dnsCacheEntry struct {
	next       *dnsCacheEntry
	name       *uint16
	typ        uint16
	dataLength uint16
	flags      uint32
}

type dnsRecord struct {
	next       *dnsRecord
	name       *uint16
	typ        uint16
	dataLength uint16
	flags      uint32
	ttl        uint32
	reserved   uint32
	data       [16]byte
}

// ReadDNSCache returns name -> addresses for every A/AAAA entry currently in
// the system DNS cache.
func ReadDNSCache() (map[string][]netip.Addr, error) {
	if err := procDnsGetCacheDataTable.Find(); err != nil {
		return nil, err
	}
	var head *dnsCacheEntry
	r, _, e := procDnsGetCacheDataTable.Call(uintptr(unsafe.Pointer(&head)))
	if r == 0 {
		return nil, e
	}
	names := map[string]bool{}
	for p := head; p != nil; {
		next := p.next
		if p.name != nil && (p.typ == dnsTypeA || p.typ == dnsTypeAAAA) {
			names[windows.UTF16PtrToString(p.name)] = true
		}
		if p.name != nil {
			procDnsFree.Call(uintptr(unsafe.Pointer(p.name)), dnsFreeFlat)
		}
		procDnsFree.Call(uintptr(unsafe.Pointer(p)), dnsFreeFlat)
		p = next
	}
	out := make(map[string][]netip.Addr, len(names))
	for name := range names {
		var addrs []netip.Addr
		for _, t := range []uint16{dnsTypeA, dnsTypeAAAA} {
			addrs = append(addrs, cacheQuery(name, t)...)
		}
		if len(addrs) > 0 {
			out[name] = addrs
		}
	}
	return out, nil
}

func cacheQuery(name string, typ uint16) []netip.Addr {
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil
	}
	var rec *dnsRecord
	r, _, _ := procDnsQueryW.Call(uintptr(unsafe.Pointer(n)), uintptr(typ), dnsQueryNoWire, 0, uintptr(unsafe.Pointer(&rec)), 0)
	if r != 0 || rec == nil {
		return nil
	}
	defer procDnsRecordListFree.Call(uintptr(unsafe.Pointer(rec)), dnsFreeRecordList)
	var out []netip.Addr
	for p := rec; p != nil; p = p.next {
		switch p.typ {
		case dnsTypeA:
			out = append(out, netip.AddrFrom4([4]byte(p.data[:4])))
		case dnsTypeAAAA:
			out = append(out, netip.AddrFrom16(p.data).Unmap())
		}
	}
	return out
}

func pollDNSCache(sink Sink) {
	m, err := ReadDNSCache()
	if err != nil {
		return
	}
	now := time.Now()
	for name, addrs := range m {
		sink.DNS(DNSEvent{Time: now, Name: name, Addrs: addrs})
	}
}
