package enrich

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/oschwald/maxminddb-golang/v2"
)

// DB-IP Lite databases: free, CC BY 4.0 (commercial use allowed with
// attribution: "IP Geolocation by DB-IP", https://db-ip.com). Published
// monthly at a predictable URL.
const dbipURL = "https://download.db-ip.com/free/dbip-%s-lite-%s.mmdb.gz"

// Download fetches the latest DB-IP Lite City and ASN databases into the
// data directory, then reloads them. It is safe to call from a goroutine.
func (e *Enricher) Download() error {
	return e.download(context.Background(), false)
}

// RefreshAfterUpgrade keeps current databases and user-supplied alternatives.
func (e *Enricher) RefreshAfterUpgrade(ctx context.Context) error {
	return e.download(ctx, true)
}

func (e *Enricher) download(ctx context.Context, staleOnly bool) error {
	if !e.Downloading.CompareAndSwap(false, true) {
		return fmt.Errorf("download already running")
	}
	defer e.Downloading.Store(false)
	type got struct{ kind, month, tmp string }
	var files []got
	for _, kind := range []string{"asn", "city"} {
		var lastErr error
		now := time.Now().UTC()
		ok := false
		// A new month's file appears a few days into the month; fall back.
		status := e.Status()
		file, date := status.ASN, status.ASNDate
		if kind == "city" {
			file, date = status.City, status.CityDate
		}
		if staleOnly && file != "" && file != "dbip-"+kind+"-lite.mmdb" {
			continue
		}
		monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		for _, m := range []time.Time{monthStart, monthStart.AddDate(0, -1, 0)} {
			month := m.Format("2006-01")
			if staleOnly && file != "" && date >= month {
				ok = true
				break
			}
			e.setDL(fmt.Sprintf("Downloading DB-IP %s lite %s…", kind, month), "")
			tmp, err := downloadOneContext(ctx, e.dir, kind, month)
			if err != nil {
				lastErr = err
				continue
			}
			files = append(files, got{kind, month, tmp})
			ok = true
			break
		}
		if !ok {
			for _, f := range files {
				os.Remove(f.tmp)
			}
			e.setDL("", fmt.Sprintf("%s database: %v", kind, lastErr))
			return lastErr
		}
	}
	if len(files) == 0 {
		e.setDL("GeoIP databases are current.", "")
		return nil
	}
	// Release the memory-mapped readers before replacing their files
	// (Windows will not replace a mapped file).
	old := e.dbs.Swap(&geoDBs{})
	time.Sleep(2 * time.Second)
	if old != nil {
		if old.city != nil {
			old.city.Close()
		}
		if old.asn != nil {
			old.asn.Close()
		}
	}
	var firstErr error
	for _, f := range files {
		final := filepath.Join(e.dir, "dbip-"+f.kind+"-lite.mmdb")
		if err := os.Rename(f.tmp, final); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			os.Remove(f.tmp)
			continue
		}
		if err := os.WriteFile(filepath.Join(e.dir, "dbip-"+f.kind+"-lite.version"), []byte(f.month+"\n"), 0o644); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	e.Reload()
	if firstErr != nil {
		e.setDL("", firstErr.Error())
		return firstErr
	}
	e.setDL("GeoIP databases updated.", "")
	return nil
}

func (e *Enricher) setDL(msg, err string) {
	e.dlMu.Lock()
	e.dlMsg, e.dlErr = msg, err
	e.dlMu.Unlock()
}

// downloadOne downloads and validates one database into a temporary file.
func downloadOneContext(parent context.Context, dir, kind, month string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Minute)
	defer cancel()
	url := fmt.Sprintf(dbipURL, kind, month)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("User-Agent", "packettrail/1.0 (+https://github.com/mattbrianb/packettrail)")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return "", err
	}
	tmp := filepath.Join(dir, "dbip-"+kind+"-lite.mmdb.download")
	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	_, err = io.Copy(f, gz)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return "", err
	}
	r, err := maxminddb.Open(tmp)
	if err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("downloaded file is not a valid MMDB: %w", err)
	}
	r.Close()
	return tmp, nil
}
