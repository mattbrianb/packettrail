package peering

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestFetchBatchLive(t *testing.T) {
	if os.Getenv("PACKETTRAIL_LIVE") == "" {
		t.Skip("set PACKETTRAIL_LIVE=1 to query PeeringDB")
	}
	c := &Client{http: &http.Client{Timeout: 30 * time.Second}}
	recs, retry, err := c.fetchBatch([]uint32{15169, 13335, 4200000123}) // Google, Cloudflare, a private ASN
	if err != nil || retry > 0 {
		t.Fatalf("err %v retry %v", err, retry)
	}
	for _, r := range recs {
		t.Logf("AS%d found=%v %q policy=%s ixes=%d", r.ASN, r.Found, r.Name, r.Policy, len(r.IXes))
	}
	if len(recs) != 3 || !recs[0].Found || !recs[1].Found || recs[2].Found || len(recs[0].IXes) == 0 {
		t.Fatal("unexpected batch result")
	}
}

// fakeAPI stands in for PeeringDB: two networks, one with two ports at the
// same exchange and one port that is not operational.
func fakeAPI(t *testing.T, calls *[]string) *httptest.Server {
	t.Helper()
	old := apiBase
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls = append(*calls, r.URL.Path)
		if r.URL.Query().Get("depth") != "" {
			t.Errorf("depth must not be requested (it is slow and large): %s", r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/net":
			io.WriteString(w, `{"data":[
				{"asn":64500,"name":"Example Content","policy_general":"Open","info_type":"Content","info_ratio":"Mostly Outbound","info_scope":"Global","info_traffic":"1-5Gbps","ix_count":2},
				{"asn":64501,"name":"Example Access","policy_general":"Selective","info_type":"","info_types":["Cable/DSL/ISP","NSP"],"ix_count":1}]}`)
		case "/api/netixlan":
			io.WriteString(w, `{"data":[
				{"asn":64500,"ix_id":10,"name":"IX One","speed":10000,"is_rs_peer":false,"operational":true},
				{"asn":64500,"ix_id":10,"name":"IX One","speed":10000,"is_rs_peer":true,"operational":true},
				{"asn":64500,"ix_id":11,"name":"IX Two","speed":1000,"is_rs_peer":false,"operational":false},
				{"asn":64501,"ix_id":10,"name":"IX One","speed":1000,"is_rs_peer":true,"operational":true}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	apiBase = srv.URL + "/api/"
	t.Cleanup(func() { srv.Close(); apiBase = old })
	return srv
}

func TestFetchBatch(t *testing.T) {
	var calls []string
	fakeAPI(t, &calls)
	c := &Client{http: &http.Client{Timeout: 5 * time.Second}}
	recs, retry, err := c.fetchBatch([]uint32{64500, 64501, 64502})
	if err != nil || retry > 0 {
		t.Fatalf("err %v retry %v", err, retry)
	}
	if len(calls) != 2 || calls[0] != "/api/net" || calls[1] != "/api/netixlan" {
		t.Errorf("want one net and one netixlan request, got %v", calls)
	}
	if len(recs) != 3 {
		t.Fatalf("want 3 records, got %d", len(recs))
	}
	a, b, missing := recs[0], recs[1], recs[2]
	if !a.Found || a.Name != "Example Content" || a.Policy != "Open" || a.Type != "Content" || len(a.IXes) != 1 ||
		a.IXes[0].ID != 10 || a.IXes[0].Speed != 20000 || !a.IXes[0].RSPeer {
		t.Errorf("AS64500: %+v", a)
	}
	if !b.Found || b.Type != "Cable/DSL/ISP, NSP" || len(b.IXes) != 1 || b.IXes[0].Name != "IX One" {
		t.Errorf("AS64501: %+v", b)
	}
	if missing.ASN != 64502 || missing.Found || missing.IXes == nil {
		t.Errorf("AS64502 should be recorded as not in PeeringDB: %+v", missing)
	}
}

func TestFetchBatchRateLimitAndErrors(t *testing.T) {
	old := apiBase
	status := http.StatusTooManyRequests
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "30")
		}
		w.WriteHeader(status)
	}))
	apiBase = srv.URL + "/api/"
	defer func() { srv.Close(); apiBase = old }()
	c := &Client{http: &http.Client{Timeout: 5 * time.Second}}
	if _, retry, err := c.fetchBatch([]uint32{64500}); err != nil || retry != 31*time.Second {
		t.Errorf("429 with Retry-After 30: retry %v err %v, want 31s and no error", retry, err)
	}
	status = http.StatusForbidden
	_, retry, err := c.fetchBatch([]uint32{64500})
	if err == nil || retry != 0 {
		t.Fatalf("403: want an error, got retry %v err %v", retry, err)
	}
	if got := describeErr(err); got != "PeeringDB lookup failed: HTTP 403" {
		t.Errorf("describeErr = %q", got)
	}
	if got := roughly(45 * time.Second); got != "45 s" {
		t.Errorf("roughly(45s) = %q", got)
	}
	if got := roughly(5 * time.Minute); got != "5 min" {
		t.Errorf("roughly(5m) = %q", got)
	}
}

func TestParseASNs(t *testing.T) {
	// 64496–64511 are reserved for documentation (RFC 5398)
	got := parseASNs("AS64500, 64501;as64510 x 0")
	if len(got) != 3 || got[0] != 64500 || got[1] != 64501 || got[2] != 64510 {
		t.Fatalf("got %v", got)
	}
}
