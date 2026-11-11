package monitor

import (
	"context"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"packettrail/internal/store"
	"packettrail/internal/tools"
)

func TestSetAllEnabled(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	echo := func(ctx context.Context, dst netip.Addr, ttl int, timeout time.Duration) (tools.Result, error) {
		return tools.Result{}, context.DeadlineExceeded // nothing leaves the machine
	}
	m := New(st, echo)
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	defer m.Stop()
	var ids []int64
	for _, target := range []string{"192.0.2.1", "192.0.2.2", "192.0.2.3"} {
		mon, err := m.Add(store.Monitor{Target: target, Interval: 60})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, mon.ID)
	}
	if err := m.SetEnabled(ids[0], false); err != nil { // one already paused
		t.Fatal(err)
	}

	running := func() (n int) {
		for _, v := range m.List() {
			if v.Enabled {
				n++
			}
		}
		return
	}
	if n, err := m.SetAllEnabled(false); err != nil || n != 2 {
		t.Fatalf("pause all changed %d (err %v), want 2", n, err)
	}
	if running() != 0 {
		t.Fatalf("%d still running after pause all", running())
	}
	m.mu.Lock()
	for id, r := range m.runs {
		if r.cancel != nil {
			t.Errorf("monitor %d still has a probe loop", id)
		}
	}
	m.mu.Unlock()
	saved, _ := st.Monitors()
	for _, mon := range saved {
		if mon.Enabled {
			t.Errorf("monitor %d still enabled in the database", mon.ID)
		}
	}
	if n, _ := m.SetAllEnabled(false); n != 0 {
		t.Fatalf("second pause all changed %d, want 0", n)
	}
	if n, err := m.SetAllEnabled(true); err != nil || n != 3 {
		t.Fatalf("resume all changed %d (err %v), want 3", n, err)
	}
	if running() != 3 {
		t.Fatalf("%d running after resume all, want 3", running())
	}
}
