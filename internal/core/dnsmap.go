package core

import (
	"net/netip"
	"strings"
	"sync"
	"time"

	"packettrail/internal/capture"
)

type dnsEntry struct {
	Name string
	At   int64
}

type pidIP struct {
	PID int
	IP  netip.Addr
}

// DNSRow is a persisted IP -> looked-up name mapping.
type DNSRow struct {
	IP   netip.Addr
	Name string
	At   int64
}

// DNSMap remembers which hostname resolved to which IP, globally and per
// process, so traffic to CDN addresses can be labelled with the name the
// application actually asked for.
type DNSMap struct {
	mu     sync.Mutex
	byIP   map[netip.Addr]dnsEntry
	names  map[netip.Addr][]dnsEntry // recent distinct names per IP, newest first
	byPID  map[pidIP]dnsEntry
	dirty  map[netip.Addr]dnsEntry
	events uint64
}

func NewDNSMap() *DNSMap {
	return &DNSMap{byIP: map[netip.Addr]dnsEntry{}, names: map[netip.Addr][]dnsEntry{}, byPID: map[pidIP]dnsEntry{}, dirty: map[netip.Addr]dnsEntry{}}
}

func (d *DNSMap) Add(ev capture.DNSEvent) {
	name := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(ev.Name)), ".")
	if name == "" || len(ev.Addrs) == 0 {
		return
	}
	at := ev.Time.Unix()
	if ev.Time.IsZero() {
		at = time.Now().Unix()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.events++
	for _, ip := range ev.Addrs {
		ip = ip.Unmap().WithZone("")
		if !ip.IsValid() || ip.IsUnspecified() {
			continue
		}
		e := dnsEntry{name, at}
		old, had := d.byIP[ip]
		// Cache polls (PID 0) only fill gaps; live lookups always win.
		if !had || ev.PID != 0 || old.Name == name {
			if !had || old.Name != name || at-old.At > 3600 {
				d.dirty[ip] = e
			}
			d.byIP[ip] = e
		}
		if ev.PID != 0 {
			d.byPID[pidIP{ev.PID, ip}] = e
		}
		d.remember(ip, e)
	}
}

// remember keeps up to 8 distinct names per IP, most recent first.
func (d *DNSMap) remember(ip netip.Addr, e dnsEntry) {
	list := d.names[ip]
	for i, x := range list {
		if x.Name == e.Name {
			if i == 0 && e.At-x.At < 60 {
				return
			}
			list = append(list[:i], list[i+1:]...)
			break
		}
	}
	list = append([]dnsEntry{e}, list...)
	if len(list) > 8 {
		list = list[:8]
	}
	d.names[ip] = list
}

// Names returns the distinct names recently seen resolving to ip, newest first.
func (d *DNSMap) Names(ip netip.Addr) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, 0, len(d.names[ip]))
	for _, e := range d.names[ip] {
		out = append(out, e.Name)
	}
	return out
}

// Load seeds the map from persisted rows.
func (d *DNSMap) Load(rows []DNSRow) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range rows {
		if _, ok := d.byIP[r.IP]; !ok {
			d.byIP[r.IP] = dnsEntry{r.Name, r.At}
			d.remember(r.IP, dnsEntry{r.Name, r.At})
		}
	}
}

// Lookup returns the best hostname for traffic from pid to ip.
func (d *DNSMap) Lookup(pid int, ip netip.Addr) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if pid != 0 {
		if e, ok := d.byPID[pidIP{pid, ip}]; ok {
			return e.Name
		}
	}
	return d.byIP[ip].Name
}

// Get returns the global hostname for ip.
func (d *DNSMap) Get(ip netip.Addr) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.byIP[ip].Name
}

func (d *DNSMap) TakeDirty() []DNSRow {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows := make([]DNSRow, 0, len(d.dirty))
	for ip, e := range d.dirty {
		rows = append(rows, DNSRow{ip, e.Name, e.At})
	}
	d.dirty = map[netip.Addr]dnsEntry{}
	return rows
}

func (d *DNSMap) Events() uint64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.events
}

func (d *DNSMap) Maintain(now time.Time) {
	pcut := now.Add(-time.Hour).Unix()
	gcut := now.Add(-14 * 24 * time.Hour).Unix()
	d.mu.Lock()
	defer d.mu.Unlock()
	for k, e := range d.byPID {
		if e.At < pcut {
			delete(d.byPID, k)
		}
	}
	for k, e := range d.byIP {
		if e.At < gcut {
			delete(d.byIP, k)
			delete(d.names, k)
		}
	}
}
