package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// Reopening a binary, including one extracted to a different directory, must
// resolve the same user data location and leave installed GeoIP files intact.
func TestResolveDataDirPersistent(t *testing.T) {
	root := t.TempDir()
	t.Setenv("APPDATA", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("HOME", root)
	cfg, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(cfg, "packettrail")
	got, err := resolveDataDir("")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("default data dir = %q, want %q", got, want)
	}
	db := filepath.Join(got, "dbip-city-lite.mmdb")
	if err := os.WriteFile(db, []byte("installed database"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = resolveDataDir("")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("second launch dir = %q, want %q", got, want)
	}
	data, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "installed database" {
		t.Fatal("installed database was replaced")
	}
	custom := filepath.Join(root, "portable")
	got, err = resolveDataDir(custom)
	if err != nil {
		t.Fatal(err)
	}
	if got != custom {
		t.Fatalf("explicit data dir = %q, want %q", got, custom)
	}
}
