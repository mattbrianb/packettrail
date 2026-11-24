// Package updater checks the publisher's GitHub releases and verifies downloads.
package updater

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const releaseAPI = "https://api.github.com/repos/mattbrianb/packettrail/releases/latest"
const releaseBase = "https://github.com/mattbrianb/packettrail/releases/download"
const maxAssetSize = 1 << 30

var tagPattern = regexp.MustCompile(`^v([0-9]+)\.([0-9]+)\.([0-9]+)$`)
var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// State is safe to show in the local dashboard. Release notes are untrusted text.
type State struct {
	Version     string `json:"version,omitempty"`
	Available   bool   `json:"available"`
	Notes       string `json:"notes,omitempty"`
	Published   string `json:"published,omitempty"`
	Checking    bool   `json:"checking"`
	Downloading bool   `json:"downloading"`
	Downloaded  int64  `json:"downloaded"`
	Size        int64  `json:"size"`
	AssetReady  bool   `json:"assetReady"`
	Path        string `json:"path,omitempty"`
	Error       string `json:"error,omitempty"`
	CheckedAt   int64  `json:"checkedAt,omitempty"`
}

type asset struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}
type release struct {
	Tag        string  `json:"tag_name"`
	Body       string  `json:"body"`
	Published  string  `json:"published_at"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []asset `json:"assets"`
}

// Manager never installs silently. A user explicitly starts a verified download.
type Manager struct {
	mu                   sync.Mutex
	state                State
	current              string
	target               string
	dir                  string
	selected             asset
	checked              time.Time
	client               *http.Client
	apiURL, downloadBase string
}

func New(current, dir string) *Manager {
	return &Manager{current: current, target: assetName(runtime.GOOS, runtime.GOARCH), dir: dir,
		client: &http.Client{Timeout: 20 * time.Minute}, apiURL: releaseAPI, downloadBase: releaseBase}
}

func assetName(goos, arch string) string {
	switch goos + "/" + arch {
	case "windows/amd64":
		return "packettrail-windows-x64-setup.exe"
	case "darwin/arm64":
		return "packettrail-macos-arm64.zip"
	case "darwin/amd64":
		return "packettrail-macos-x64.zip"
	case "linux/amd64":
		return "packettrail-linux-x64.tar.gz"
	case "linux/arm64":
		return "packettrail-linux-arm64.tar.gz"
	}
	return ""
}

func compareVersion(a, b string) (int, error) {
	x, y := tagPattern.FindStringSubmatch(a), tagPattern.FindStringSubmatch(b)
	if x == nil || y == nil {
		return 0, errors.New("invalid version")
	}
	for i := 1; i <= 3; i++ {
		ax, e1 := strconv.ParseUint(x[i], 10, 32)
		by, e2 := strconv.ParseUint(y[i], 10, 32)
		if e1 != nil || e2 != nil {
			return 0, errors.New("invalid version")
		}
		if ax < by {
			return -1, nil
		}
		if ax > by {
			return 1, nil
		}
	}
	return 0, nil
}

func (m *Manager) Status() State { m.mu.Lock(); defer m.mu.Unlock(); return m.state }

// Run checks on launch and every twelve hours while the app is running.
func (m *Manager) Run(ctx context.Context) {
	m.Check(ctx, false)
	ticker := time.NewTicker(12 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.Check(ctx, false)
		}
	}
}

func (m *Manager) Check(ctx context.Context, force bool) error {
	m.mu.Lock()
	if m.state.Checking || m.state.Downloading || (!force && time.Since(m.checked) < 12*time.Hour) {
		m.mu.Unlock()
		return nil
	}
	m.state.Checking = true
	m.state.Error = ""
	m.mu.Unlock()

	state, selected, err := m.fetch(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state.Checking = false
	m.checked = time.Now()
	m.state.CheckedAt = m.checked.Unix()
	if err != nil {
		m.state.Error = err.Error()
		return err
	}
	m.state = state
	m.state.CheckedAt = m.checked.Unix()
	m.selected = selected
	return nil
}

func (m *Manager) fetch(ctx context.Context) (State, asset, error) {
	var st State
	var selected asset
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.apiURL, nil)
	if err != nil {
		return st, selected, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "PacketTrail/"+m.current)
	resp, err := m.client.Do(req)
	if err != nil {
		return st, selected, fmt.Errorf("could not check GitHub releases: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return st, selected, fmt.Errorf("GitHub releases returned HTTP %d", resp.StatusCode)
	}
	var rel release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return st, selected, fmt.Errorf("invalid release information: %w", err)
	}
	if rel.Draft || rel.Prerelease {
		return st, selected, errors.New("latest release is not stable")
	}
	if !tagPattern.MatchString(rel.Tag) {
		return st, selected, errors.New("latest release has an invalid version")
	}
	cmp, err := compareVersion("v"+strings.TrimPrefix(m.current, "v"), rel.Tag)
	if err != nil {
		return st, selected, err
	}
	st.Version = strings.TrimPrefix(rel.Tag, "v")
	st.Available = cmp < 0
	if !st.Available {
		return st, selected, nil
	}
	st.Notes = strings.TrimSpace(rel.Body)
	if len(st.Notes) > 8000 {
		st.Notes = st.Notes[:8000] + "…"
	}
	st.Notes = m.history(ctx, rel.Tag, st.Notes)
	st.Published = rel.Published
	for _, a := range rel.Assets {
		if a.Name == m.target && a.Size > 0 && a.Size <= maxAssetSize && digestPattern.MatchString(a.Digest) {
			selected = a
			st.AssetReady = true
			st.Size = a.Size
			break
		}
	}
	return st, selected, nil
}

// history keeps the latest release usable if GitHub cannot serve older notes.
func (m *Manager) history(ctx context.Context, latest, body string) string {
	current := "v" + strings.TrimPrefix(m.current, "v")
	notes := map[string]string{latest: body}
	for page := 1; ; page++ {
		url := fmt.Sprintf("%s?per_page=30&page=%d", strings.TrimSuffix(m.apiURL, "/latest"), page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			break
		}
		resp, err := m.client.Do(req)
		if err != nil {
			break
		}
		var releases []release
		err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&releases)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || err != nil {
			break
		}
		for _, r := range releases {
			newer, e1 := compareVersion(r.Tag, current)
			upper, e2 := compareVersion(r.Tag, latest)
			if !r.Draft && !r.Prerelease && e1 == nil && e2 == nil && newer > 0 && upper < 0 {
				notes[r.Tag] = strings.TrimSpace(r.Body)
			}
		}
		if len(releases) < 30 {
			break
		}
	}
	tags := make([]string, 0, len(notes))
	for tag := range notes {
		tags = append(tags, tag)
	}
	sort.Slice(tags, func(i, j int) bool { c, _ := compareVersion(tags[i], tags[j]); return c > 0 })
	var sections []string
	for _, tag := range tags {
		note := notes[tag]
		if note == "" {
			note = "No release notes provided."
		}
		sections = append(sections, tag+"\n"+note)
	}
	return strings.Join(sections, "\n\n")
}

func (m *Manager) StartDownload(ctx context.Context) error {
	m.mu.Lock()
	if m.state.Downloading {
		m.mu.Unlock()
		return errors.New("download already in progress")
	}
	if !m.state.Available || !m.state.AssetReady {
		m.mu.Unlock()
		return errors.New("an update for this platform is not available yet")
	}
	a, version := m.selected, m.state.Version
	m.state.Downloading, m.state.Downloaded, m.state.Path, m.state.Error = true, 0, "", ""
	m.mu.Unlock()
	go m.download(ctx, a, version)
	return nil
}

func (m *Manager) download(ctx context.Context, a asset, version string) {
	path, err := m.fetchAsset(ctx, a, version)
	m.mu.Lock()
	m.state.Downloading = false
	if err != nil {
		m.state.Error = err.Error()
	} else {
		m.state.Path = path
		m.state.Downloaded = a.Size
	}
	m.mu.Unlock()
}

func (m *Manager) fetchAsset(ctx context.Context, a asset, version string) (string, error) {
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return "", err
	}
	dest := filepath.Join(m.dir, "PacketTrail-v"+version+"-"+a.Name)
	if sameDigest(dest, a.Digest) {
		return dest, nil
	}
	if _, err := os.Stat(dest); err == nil {
		return "", fmt.Errorf("a different file already exists at %s", dest)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	url := m.downloadBase + "/v" + version + "/" + a.Name
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "PacketTrail/"+m.current)
	resp, err := m.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}
	f, err := os.CreateTemp(m.dir, ".packettrail-update-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	h := sha256.New()
	progress := &progressWriter{m: m, w: io.MultiWriter(f, h)}
	n, err := io.Copy(progress, io.LimitReader(resp.Body, a.Size+1))
	if err != nil {
		return "", err
	}
	if n != a.Size {
		return "", fmt.Errorf("download size mismatch: got %d bytes, expected %d", n, a.Size)
	}
	if "sha256:"+fmt.Sprintf("%x", h.Sum(nil)) != a.Digest {
		return "", errors.New("download checksum did not match the GitHub release")
	}
	if err := f.Chmod(0o644); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(f.Name(), dest); err != nil {
		return "", err
	}
	return dest, nil
}

type progressWriter struct {
	m *Manager
	w io.Writer
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.m.mu.Lock()
	p.m.state.Downloaded += int64(n)
	p.m.mu.Unlock()
	return n, err
}

func sameDigest(path, want string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false
	}
	return "sha256:"+fmt.Sprintf("%x", h.Sum(nil)) == want
}

// ReadyPath is only set after the release asset passes its SHA-256 check.
func (m *Manager) ReadyPath() (string, error) {
	m.mu.Lock()
	path, digest := m.state.Path, m.selected.Digest
	m.mu.Unlock()
	if path == "" || digest == "" {
		return "", errors.New("no verified update is ready")
	}
	if !sameDigest(path, digest) {
		return "", errors.New("downloaded update has changed; download it again")
	}
	return path, nil
}
