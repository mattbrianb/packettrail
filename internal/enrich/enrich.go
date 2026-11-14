// Package enrich adds geolocation, ASN and reverse DNS to remote addresses.
// All lookups are local (MMDB files) or cached; private and special-purpose
// ranges are never looked up.
package enrich

import (
	"context"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oschwald/maxminddb-golang/v2"

	"packettrail/internal/store"
)

// Info is everything we know about a remote address.
type Info struct {
	Lat     *float64 `json:"lat,omitempty"`
	Lon     *float64 `json:"lon,omitempty"`
	Scope   string   `json:"scope,omitempty"` // private, loopback, cgnat, ... ("" = public)
	CC      string   `json:"cc,omitempty"`
	Country string   `json:"country,omitempty"`
	Region  string   `json:"region,omitempty"`
	City    string   `json:"city,omitempty"`
	ASN     uint32   `json:"asn,omitempty"`
	ASOrg   string   `json:"asOrg,omitempty"`
	Network string   `json:"network,omitempty"` // routed prefix containing the IP (from the ASN database)
	PTR     string   `json:"ptr,omitempty"`
}

type special struct {
	p     netip.Prefix
	scope string
}

var specials = func() []special {
	list := [][2]string{
		{"0.0.0.0/8", "reserved"}, {"10.0.0.0/8", "private"}, {"100.64.0.0/10", "cgnat"},
		{"127.0.0.0/8", "loopback"}, {"169.254.0.0/16", "link-local"}, {"172.16.0.0/12", "private"},
		{"192.0.0.0/24", "reserved"}, {"192.0.2.0/24", "documentation"}, {"192.168.0.0/16", "private"},
		{"198.18.0.0/15", "benchmark"}, {"198.51.100.0/24", "documentation"}, {"203.0.113.0/24", "documentation"},
		{"224.0.0.0/4", "multicast"}, {"240.0.0.0/4", "reserved"},
		{"::/128", "unspecified"}, {"::1/128", "loopback"}, {"fc00::/7", "private"}, {"fe80::/10", "link-local"},
		{"ff00::/8", "multicast"}, {"2001:db8::/32", "documentation"},
	}
	out := make([]special, len(list))
	for i, l := range list {
		out[i] = special{netip.MustParsePrefix(l[0]), l[1]}
	}
	return out
}()

// Scope classifies special-purpose addresses; "" means public.
func Scope(ip netip.Addr) string {
	ip = ip.Unmap()
	for _, s := range specials {
		if s.p.Contains(ip) {
			return s.scope
		}
	}
	return ""
}

type cityRec struct {
	Location struct {
		Lat *float64 `maxminddb:"latitude"`
		Lon *float64 `maxminddb:"longitude"`
	} `maxminddb:"location"`
	City struct {
		Names struct {
			En string `maxminddb:"en"`
		} `maxminddb:"names"`
	} `maxminddb:"city"`
	Country struct {
		ISO   string `maxminddb:"iso_code"`
		Names struct {
			En string `maxminddb:"en"`
		} `maxminddb:"names"`
	} `maxminddb:"country"`
	Subdivisions []struct {
		Names struct {
			En string `maxminddb:"en"`
		} `maxminddb:"names"`
	} `maxminddb:"subdivisions"`
}

type asnRec struct {
	ASN uint32 `maxminddb:"autonomous_system_number"`
	Org string `maxminddb:"autonomous_system_organization"`
}

type geoDBs struct {
	city, asn         *maxminddb.Reader
	cityFile, asnFile string
	cityDate, asnDate string
}

type ptrState struct {
	name string
	at   int64
}

// Enricher looks up and caches information about addresses.
type Enricher struct {
	dir   string
	dbs   atomic.Pointer[geoDBs]
	mu    sync.Mutex
	geo   map[netip.Addr]Info
	ptr   map[netip.Addr]ptrState
	dirty map[netip.Addr]store.PTR
	queue chan netip.Addr
	inq   map[netip.Addr]bool
	st    *store.Store
	stop  chan struct{}
	wg    sync.WaitGroup

	Downloading atomic.Bool
	dlMu        sync.Mutex
	dlErr       string
	dlMsg       string
}

// New opens the GeoIP databases found in dir and starts the PTR resolvers.
func New(dir string, st *store.Store) *Enricher {
	e := &Enricher{
		dir: dir, st: st,
		geo: map[netip.Addr]Info{}, ptr: map[netip.Addr]ptrState{}, dirty: map[netip.Addr]store.PTR{},
		queue: make(chan netip.Addr, 8192), inq: map[netip.Addr]bool{}, stop: make(chan struct{}),
	}
	e.Reload()
	if st != nil {
		for ip, p := range st.LoadPTR(time.Now().Add(-30 * 24 * time.Hour).Unix()) {
			e.ptr[ip] = ptrState{p.Name, p.At}
		}
	}
	for i := 0; i < 4; i++ {
		e.wg.Add(1)
		go e.ptrWorker()
	}
	e.wg.Add(1)
	go e.saver()
	return e
}

func (e *Enricher) Close() {
	close(e.stop)
	e.wg.Wait()
	e.savePTR()
	if d := e.dbs.Load(); d != nil {
		if d.city != nil {
			d.city.Close()
		}
		if d.asn != nil {
			d.asn.Close()
		}
	}
}

// Candidate file names, most preferred first. GeoLite2 files from MaxMind
// work too if the user supplies them.
var cityNames = []string{"dbip-city-lite.mmdb", "GeoLite2-City.mmdb", "dbip-country-lite.mmdb", "GeoLite2-Country.mmdb"}
var asnNames = []string{"dbip-asn-lite.mmdb", "GeoLite2-ASN.mmdb"}

func openFirst(dir string, names []string) (*maxminddb.Reader, string, string) {
	for _, n := range names {
		p := filepath.Join(dir, n)
		if _, err := os.Stat(p); err != nil {
			continue
		}
		r, err := maxminddb.Open(p)
		if err != nil {
			continue
		}
		date := r.Metadata.BuildTime().Format("2006-01-02")
		if v, err := os.ReadFile(strings.TrimSuffix(p, ".mmdb") + ".version"); err == nil {
			date = strings.TrimSpace(string(v))
		}
		return r, n, date
	}
	return nil, "", ""
}

// Reload (re)opens the MMDB files and clears the geo cache.
func (e *Enricher) Reload() {
	d := &geoDBs{}
	d.city, d.cityFile, d.cityDate = openFirst(e.dir, cityNames)
	d.asn, d.asnFile, d.asnDate = openFirst(e.dir, asnNames)
	old := e.dbs.Swap(d)
	e.mu.Lock()
	e.geo = map[netip.Addr]Info{}
	e.mu.Unlock()
	if old != nil {
		// Readers may still be in use by a concurrent lookup; give them a moment.
		go func() {
			time.Sleep(5 * time.Second)
			if old.city != nil {
				old.city.Close()
			}
			if old.asn != nil {
				old.asn.Close()
			}
		}()
	}
}

// DBStatus describes the loaded GeoIP databases.
type DBStatus struct {
	Dir         string `json:"dir"`
	City        string `json:"city"`
	CityDate    string `json:"cityDate"`
	ASN         string `json:"asn"`
	ASNDate     string `json:"asnDate"`
	Downloading bool   `json:"downloading"`
	Message     string `json:"message,omitempty"`
	Error       string `json:"error,omitempty"`
}

// Missing reports whether the city or ASN database is absent, as on a fresh
// install.
func (e *Enricher) Missing() bool {
	d := e.dbs.Load()
	return d == nil || d.city == nil || d.asn == nil
}

func (e *Enricher) Status() DBStatus {
	d := e.dbs.Load()
	e.dlMu.Lock()
	defer e.dlMu.Unlock()
	return DBStatus{Dir: e.dir, City: d.cityFile, CityDate: d.cityDate, ASN: d.asnFile, ASNDate: d.asnDate,
		Downloading: e.Downloading.Load(), Message: e.dlMsg, Error: e.dlErr}
}

// Lookup returns cached or freshly computed info. Reverse DNS is resolved in
// the background; the first call for a new address returns without PTR.
func (e *Enricher) Lookup(ip netip.Addr) Info {
	ip = ip.Unmap().WithZone("")
	e.mu.Lock()
	info, ok := e.geo[ip]
	p, hasPTR := e.ptr[ip]
	e.mu.Unlock()
	if !ok {
		info = e.lookupGeo(ip)
		e.mu.Lock()
		if len(e.geo) > 200000 {
			e.geo = map[netip.Addr]Info{}
		}
		e.geo[ip] = info
		e.mu.Unlock()
	}
	if info.Scope != "" {
		return info
	}
	if hasPTR {
		info.PTR = p.name
		age := time.Since(time.Unix(p.at, 0))
		if (p.name == "" && age > 24*time.Hour) || age > 7*24*time.Hour {
			e.enqueue(ip)
		}
	} else {
		e.enqueue(ip)
	}
	return info
}

func (e *Enricher) lookupGeo(ip netip.Addr) Info {
	info := Info{Scope: Scope(ip)}
	if info.Scope != "" {
		return info
	}
	d := e.dbs.Load()
	if d.city != nil {
		var r cityRec
		if d.city.Lookup(ip).Decode(&r) == nil {
			info.Lat, info.Lon = r.Location.Lat, r.Location.Lon
			info.CC, info.Country, info.City = r.Country.ISO, r.Country.Names.En, r.City.Names.En
			if len(r.Subdivisions) > 0 {
				info.Region = r.Subdivisions[0].Names.En
			}
		}
	}
	if d.asn != nil {
		var r asnRec
		res := d.asn.Lookup(ip)
		if res.Decode(&r) == nil {
			info.ASN, info.ASOrg = r.ASN, r.Org
			if res.Found() {
				if p := res.Prefix(); p.IsValid() {
					info.Network = p.Masked().String()
				}
			}
		}
	}
	return info
}

func (e *Enricher) enqueue(ip netip.Addr) {
	e.mu.Lock()
	if e.inq[ip] {
		e.mu.Unlock()
		return
	}
	e.inq[ip] = true
	e.mu.Unlock()
	select {
	case e.queue <- ip:
	default: // queue full; it will be retried on the next view refresh
		e.mu.Lock()
		delete(e.inq, ip)
		e.mu.Unlock()
	}
}

func (e *Enricher) ptrWorker() {
	defer e.wg.Done()
	for {
		select {
		case <-e.stop:
			return
		case ip := <-e.queue:
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			names, _ := net.DefaultResolver.LookupAddr(ctx, ip.String())
			cancel()
			name := ""
			if len(names) > 0 {
				name = strings.TrimSuffix(names[0], ".")
			}
			now := time.Now().Unix()
			e.mu.Lock()
			e.ptr[ip] = ptrState{name, now}
			e.dirty[ip] = store.PTR{Name: name, At: now}
			delete(e.inq, ip)
			e.mu.Unlock()
		}
	}
}

func (e *Enricher) saver() {
	defer e.wg.Done()
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-e.stop:
			return
		case <-t.C:
			e.savePTR()
		}
	}
}

func (e *Enricher) savePTR() {
	if e.st == nil {
		return
	}
	e.mu.Lock()
	d := e.dirty
	e.dirty = map[netip.Addr]store.PTR{}
	e.mu.Unlock()
	e.st.SavePTR(d)
}
