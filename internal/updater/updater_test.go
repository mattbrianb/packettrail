package updater

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestVersionComparison(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"v1.3.1", "v1.4.0", -1}, {"v1.10.0", "v1.9.9", 1}, {"v1.4.0", "v1.4.0", 0},
	} {
		got, err := compareVersion(tc.a, tc.b)
		if err != nil || got != tc.want {
			t.Fatalf("compareVersion(%q,%q) = %d, %v", tc.a, tc.b, got, err)
		}
	}
	if _, err := compareVersion("v1.4.0-beta", "v1.4.0"); err == nil {
		t.Fatal("accepted a prerelease version")
	}
}

func TestVerifiedDownloadAndTamper(t *testing.T) {
	payload := []byte("verified installer fixture")
	sum := sha256.Sum256(payload)
	assetName := "packettrail-windows-x64-setup.exe"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			json.NewEncoder(w).Encode(release{Tag: "v1.4.0", Body: "Changes <must remain text>", Assets: []asset{{Name: assetName, Digest: fmt.Sprintf("sha256:%x", sum), Size: int64(len(payload))}}})
		case "/v1.4.0/" + assetName:
			w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	m := New("1.3.1", t.TempDir())
	m.target = assetName
	m.apiURL = srv.URL + "/latest"
	m.downloadBase = srv.URL
	if err := m.Check(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	st := m.Status()
	if !st.Available || !st.AssetReady || st.Notes != "Changes <must remain text>" {
		t.Fatalf("unexpected release: %+v", st)
	}
	if err := m.StartDownload(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for m.Status().Downloading && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	path, err := m.ReadyPath()
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("download = %q, %v", got, err)
	}
	if err := os.WriteFile(path, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ReadyPath(); err == nil {
		t.Fatal("offered a modified download")
	}
}

func TestChecksumMismatchIsNotOffered(t *testing.T) {
	name := "packettrail-windows-x64-setup.exe"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest" {
			json.NewEncoder(w).Encode(release{Tag: "v1.4.0", Assets: []asset{{Name: name, Digest: "sha256:" + strings.Repeat("0", 64), Size: 3}}})
			return
		}
		w.Write([]byte("bad"))
	}))
	defer srv.Close()
	m := New("1.3.1", t.TempDir())
	m.target = name
	m.apiURL = srv.URL + "/latest"
	m.downloadBase = srv.URL
	if err := m.Check(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err := m.StartDownload(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for m.Status().Downloading && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := m.ReadyPath(); err == nil {
		t.Fatal("checksum mismatch was offered")
	}
	if !strings.Contains(m.Status().Error, "checksum") {
		t.Fatalf("missing checksum error: %+v", m.Status())
	}
}
