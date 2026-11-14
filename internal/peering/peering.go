// Package peering looks up networks in PeeringDB (cached) and works out
// which network this machine sits in (its own ASN).
package peering

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"packettrail/internal/enrich"
	"packettrail/internal/store"
	"packettrail/internal/tools"
)

const (
	cacheTTL    = 7 * 24 * time.Hour
	missTTL     = 24 * time.Hour
	reqGap      = 4 * time.Second // between batches; the anonymous API is rate-limited
	batchSize   = 10
	httpTimeout = 20 * time.Second // PeeringDB can take several seconds; a slow link adds more
	errDelayMin = 15 * time.Second // wait after a failed request, doubling up to errDelayMax
	errDelayMax = 5 * time.Minute
	userAgent   = "packettrail/1.0 (+https://github.com/mattbrianb/packettrail)"
)

// apiBase is a variable so tests can point the client at a local server.
var apiBase = "https://www.peeringdb.com/api/"

// IX is one internet-exchange connection of a network.
type IX struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Speed  int    `json:"speed"` // Mbit/s
	RSPeer bool   `json:"rs"`
}

// Record is what we keep per ASN.
type Record struct {
	ASN     uint32 `json:"asn"`
	Found   bool   `json:"found"` // present in PeeringDB
	Name    string `json:"name"`
	Policy  string `json:"policy"`
	Type    string `json:"type"`
	Ratio   string `json:"ratio"`
	Scope   string `json:"scope"`
	Traffic string `json:"traffic"`
	IXCount int    `json:"ixCount"`
	IXes    []IX   `json:"ixes"`
	Fetched int64  `json:"fetched"`
}

// Client fetches PeeringDB records in the background, one at a time.
type Client struct {
	st       *store.Store
	http     *http.Client
	mu       sync.Mutex
	mem      map[uint32]*Record
	pending  map[uint32]bool
	queue    chan uint32
	backoff  time.Time
	errDelay time.Duration // current wait after a failure (0 after a success)
	lastErr  string
}

func NewClient(st *store.Store) *Client {
	c := &Client{st: st, http: &http.Client{Timeout: httpTimeout}, mem: map[uint32]*Record{},
		pending: map[uint32]bool{}, queue: make(chan uint32, 256)}
	go c.worker()
	return c
}

// Get returns the record for asn if known, queueing a (re)fetch when it is
// missing or stale. pending reports whether a fetch is queued.
func (c *Client) Get(asn uint32) (rec *Record, pending bool) {
	if asn == 0 {
		return nil, false
	}
	c.mu.Lock()
	rec = c.mem[asn]
	c.mu.Unlock()
	if rec == nil {
		if data, at, ok := c.st.PeeringDB(asn); ok {
			var r Record
			if json.Unmarshal([]byte(data), &r) == nil {
				r.Fetched = at
				rec = &r
				c.mu.Lock()
				c.mem[asn] = rec
				c.mu.Unlock()
			}
		}
	}
	ttl := cacheTTL
	if rec != nil && !rec.Found {
		ttl = missTTL
	}
	if rec == nil || time.Since(time.Unix(rec.Fetched, 0)) > ttl {
		pending = c.enqueue(asn)
	}
	return rec, pending
}

func (c *Client) enqueue(asn uint32) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending[asn] {
		return true
	}
	select {
	case c.queue <- asn:
		c.pending[asn] = true
		return true
	default:
		return false
	}
}

// LastError is the most recent fetch problem ("" when fine).
func (c *Client) LastError() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastErr
}

func (c *Client) worker() {
	for asn := range c.queue {
		// Batch: gather whatever else is queued in the next moment (up to 10).
		batch := []uint32{asn}
		deadline := time.After(400 * time.Millisecond)
	collect:
		for len(batch) < batchSize {
			select {
			case a := <-c.queue:
				batch = append(batch, a)
			case <-deadline:
				break collect
			}
		}
		c.mu.Lock()
		wait := time.Until(c.backoff)
		c.mu.Unlock()
		if wait > 0 {
			time.Sleep(wait)
		}
		recs, retry, err := c.fetchBatch(batch)
		c.mu.Lock()
		requeue := func() { // put the batch back; give up silently if the queue is full
			for _, a := range batch {
				select {
				case c.queue <- a:
				default:
					delete(c.pending, a)
				}
			}
		}
		before := c.lastErr
		switch {
		case retry > 0:
			c.backoff = time.Now().Add(retry)
			c.lastErr = fmt.Sprintf("PeeringDB asked us to slow down; trying again in %s", roughly(retry))
			requeue()
		case err != nil:
			// Wait longer after each failure instead of asking again every few
			// seconds, and say what went wrong.
			c.errDelay = min(max(2*c.errDelay, errDelayMin), errDelayMax)
			c.backoff = time.Now().Add(c.errDelay)
			c.lastErr = fmt.Sprintf("%s; trying again in %s", describeErr(err), roughly(c.errDelay))
			requeue()
		default:
			c.errDelay = 0
			c.lastErr = ""
			for _, r := range recs {
				c.mem[r.ASN] = r
				delete(c.pending, r.ASN)
			}
		}
		if c.lastErr != "" && (before == "" || err != nil) {
			log.Printf("peeringdb: %s", c.lastErr)
		}
		c.mu.Unlock()
		for _, r := range recs {
			b, _ := json.Marshal(r)
			c.st.SavePeeringDB(r.ASN, string(b), r.Fetched)
		}
		time.Sleep(reqGap)
	}
}

// roughly formats a wait for people: "45 s", "2 min".
func roughly(d time.Duration) string {
	if d >= 90*time.Second {
		return fmt.Sprintf("%d min", int(d.Round(time.Minute)/time.Minute))
	}
	return fmt.Sprintf("%d s", int(d.Round(time.Second)/time.Second))
}

// describeErr turns a request failure into a sentence a user can act on.
func describeErr(err error) string {
	var ne interface{ Timeout() bool }
	if errors.As(err, &ne) && ne.Timeout() {
		return fmt.Sprintf("PeeringDB did not answer within %s ; check your connection or network policy", roughly(httpTimeout))
	}
	return "PeeringDB lookup failed: " + err.Error()
}

// get fetches one API URL into out. A non-zero retry means PeeringDB
// rate-limited us.
func (c *Client) get(path string, out any) (retry time.Duration, err error) {
	req, _ := http.NewRequest(http.MethodGet, apiBase+path, nil)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		retry = time.Minute
		if s, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && s > 0 && s < 3600 {
			retry = time.Duration(s+1) * time.Second
		}
		return retry, nil
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return 0, json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(out)
}

// fetchBatch looks up several networks and their exchange connections. ASNs
// missing from the reply are recorded as "not in PeeringDB". A non-zero retry
// means PeeringDB rate-limited us.
//
// It makes two small requests (the networks, then their exchange ports)
// instead of one "depth=2" request: for ten large networks that one took
// PeeringDB about 8 s and 700 kB, which on a slow link ran into the client
// timeout every time. The two together take under 3 s and a quarter of the
// size.
func (c *Client) fetchBatch(asns []uint32) ([]*Record, time.Duration, error) {
	parts := make([]string, len(asns))
	for i, a := range asns {
		parts[i] = strconv.FormatUint(uint64(a), 10)
	}
	list := strings.Join(parts, ",")
	var nets struct {
		Data []struct {
			ASN     uint32   `json:"asn"`
			Name    string   `json:"name"`
			Policy  string   `json:"policy_general"`
			Type    string   `json:"info_type"`
			Types   []string `json:"info_types"`
			Ratio   string   `json:"info_ratio"`
			Scope   string   `json:"info_scope"`
			Traffic string   `json:"info_traffic"`
			IXCount int      `json:"ix_count"`
		} `json:"data"`
	}
	if retry, err := c.get("net?asn__in="+list+"&fields=asn,name,policy_general,info_type,info_types,info_ratio,info_scope,info_traffic,ix_count", &nets); retry > 0 || err != nil {
		return nil, retry, err
	}
	now := time.Now().Unix()
	found := map[uint32]*Record{}
	for _, d := range nets.Data {
		typ := d.Type
		if typ == "" {
			typ = strings.Join(d.Types, ", ")
		}
		found[d.ASN] = &Record{ASN: d.ASN, Found: true, Name: d.Name, Policy: d.Policy, Type: typ, Ratio: d.Ratio,
			Scope: d.Scope, Traffic: d.Traffic, IXCount: d.IXCount, Fetched: now, IXes: []IX{}}
	}
	if len(found) > 0 {
		var lans struct {
			Data []struct {
				ASN         uint32 `json:"asn"`
				IXID        int    `json:"ix_id"`
				Name        string `json:"name"`
				Speed       int    `json:"speed"`
				RSPeer      bool   `json:"is_rs_peer"`
				Operational bool   `json:"operational"`
			} `json:"data"`
		}
		if retry, err := c.get("netixlan?asn__in="+list+"&fields=asn,ix_id,name,speed,is_rs_peer,operational", &lans); retry > 0 || err != nil {
			return nil, retry, err
		}
		type key struct {
			asn uint32
			ix  int
		}
		byIX := map[key]int{} // index into the record's IXes
		for _, l := range lans.Data {
			rec := found[l.ASN]
			if rec == nil || !l.Operational {
				continue
			}
			k := key{l.ASN, l.IXID}
			if i, ok := byIX[k]; ok { // several ports at one exchange
				rec.IXes[i].Speed += l.Speed
				rec.IXes[i].RSPeer = rec.IXes[i].RSPeer || l.RSPeer
				continue
			}
			byIX[k] = len(rec.IXes)
			rec.IXes = append(rec.IXes, IX{ID: l.IXID, Name: l.Name, Speed: l.Speed, RSPeer: l.RSPeer})
		}
	}
	out := make([]*Record, 0, len(asns))
	for _, a := range asns {
		if r := found[a]; r != nil {
			out = append(out, r)
		} else {
			out = append(out, &Record{ASN: a, Fetched: now, IXes: []IX{}})
		}
	}
	return out, 0, nil
}

// ---------------------------------------------------------------- own network

// Own describes this machine's network.
type Own struct {
	ASNs   []uint32 `json:"asns"`
	Source string   `json:"source"` // manual | detected | unknown
	Detail string   `json:"detail"`
}

// Detector finds the access network's ASN from the first public hop on the
// path to the internet, and lets the user override it.
type Detector struct {
	st   *store.Store
	enr  *enrich.Enricher
	echo tools.EchoFunc
	mu   sync.Mutex
	busy bool
}

func NewDetector(st *store.Store, enr *enrich.Enricher, echo tools.EchoFunc) *Detector {
	return &Detector{st: st, enr: enr, echo: echo}
}

func parseASNs(s string) []uint32 {
	var out []uint32
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		f = strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(f)), "AS")
		if n, err := strconv.ParseUint(f, 10, 32); err == nil && n > 0 {
			out = append(out, uint32(n))
		}
	}
	return out
}

// Current returns the manual setting if any, else the last detection.
func (d *Detector) Current() Own {
	if m := parseASNs(d.st.GetMeta("own_asns")); len(m) > 0 {
		return Own{ASNs: m, Source: "manual", Detail: "Set by you"}
	}
	if a := parseASNs(d.st.GetMeta("detected_asn")); len(a) > 0 {
		return Own{ASNs: a, Source: "detected", Detail: d.st.GetMeta("detected_detail")}
	}
	return Own{Source: "unknown", Detail: "Not detected yet"}
}

// SetManual stores a manual list (empty = go back to auto-detection).
func (d *Detector) SetManual(s string) Own {
	asns := parseASNs(s)
	parts := make([]string, len(asns))
	for i, a := range asns {
		parts[i] = strconv.FormatUint(uint64(a), 10)
	}
	d.st.SetMeta("own_asns", strings.Join(parts, ","))
	return d.Current()
}

// DetectIfStale runs detection in the background when it is older than 6 h.
func (d *Detector) DetectIfStale() {
	at, _ := strconv.ParseInt(d.st.GetMeta("detected_at"), 10, 64)
	if time.Since(time.Unix(at, 0)) > 6*time.Hour {
		go d.Detect()
	}
}

// Detect probes TTL 1..10 towards 1.1.1.1 and takes the ASN of the first
// public hop (the access provider's router).
func (d *Detector) Detect() {
	d.mu.Lock()
	if d.busy {
		d.mu.Unlock()
		return
	}
	d.busy = true
	d.mu.Unlock()
	defer func() { d.mu.Lock(); d.busy = false; d.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dst := netip.MustParseAddr("1.1.1.1")
	for ttl := 1; ttl <= 10; ttl++ {
		var res tools.Result
		var err error
		for try := 0; try < 2; try++ {
			if res, err = d.echo(ctx, dst, ttl, 2*time.Second); err == nil && res.From.IsValid() {
				break
			}
		}
		if err != nil || !res.From.IsValid() {
			continue
		}
		info := d.enr.Lookup(res.From)
		if info.Scope != "" || info.ASN == 0 {
			continue
		}
		d.st.SetMeta("detected_asn", strconv.FormatUint(uint64(info.ASN), 10))
		d.st.SetMeta("detected_detail", fmt.Sprintf("First public hop %s (hop %d) is in AS%d %s", res.From, ttl, info.ASN, info.ASOrg))
		d.st.SetMeta("detected_at", strconv.FormatInt(time.Now().Unix(), 10))
		return
	}
	d.st.SetMeta("detected_at", strconv.FormatInt(time.Now().Unix(), 10))
}

// Busy reports whether detection is running.
func (d *Detector) Busy() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.busy
}
