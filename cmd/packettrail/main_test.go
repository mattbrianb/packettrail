package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Only a PacketTrail dashboard should be recognised as an existing instance.
func TestProbeAppIdentity(t *testing.T) {
	for _, app := range []string{"packettrail", "unrelated-app", "other"} {
		t.Run(app, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/status" {
					t.Errorf("unexpected probe path %s", r.URL.Path)
				}
				fmt.Fprintf(w, `{"app":%q}`, app)
			}))
			defer srv.Close()
			got := probe(srv.Listener.Addr().(*net.TCPAddr).Port)
			if app == "packettrail" && got != srv.URL+"/" {
				t.Fatalf("PacketTrail was not recognised: %q", got)
			}
			if app != "packettrail" && got != "" {
				t.Fatalf("opened another application's dashboard: %q", got)
			}
		})
	}
}
