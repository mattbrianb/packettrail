//go:build windows

package sysmon

import (
	"strings"
	"testing"
	"time"
)

// TestLiveSample reads this machine twice and sanity-checks every section,
// which also validates the hand-computed struct offsets.
func TestLiveSample(t *testing.T) {
	src := newSource().(*winSource)
	defer src.close()
	src.sample(time.Now())
	time.Sleep(1500 * time.Millisecond)
	s := src.sample(time.Now())

	if s.CPU.Count < 1 || len(s.CPU.Cores) != s.CPU.Count || s.CPU.Busy < 0 || s.CPU.Busy > 100 {
		t.Fatalf("cpu: %+v", s.CPU)
	}
	if s.Mem.Total < 1<<30 || s.Mem.Avail > s.Mem.Total || s.Mem.CommitLimit < s.Mem.CommitUsed {
		t.Fatalf("mem: %+v", s.Mem)
	}
	if len(s.Disks) == 0 || !strings.HasSuffix(s.Disks[0].Mount, ":") || s.Disks[0].Total == 0 {
		t.Fatalf("disks: %+v", s.Disks)
	}
	if len(s.Ifaces) == 0 {
		t.Fatal("no adapters")
	}
	up := false
	for _, f := range s.Ifaces {
		if f.Name == "" || strings.ContainsRune(f.Name, 0) {
			t.Fatalf("bad adapter name %q", f.Name)
		}
		if f.Up && f.Physical {
			up = true
		}
		t.Logf("if %-28s %-9s phys=%v up=%v speed=%d mtu=%d mac=%s rx=%.0f tx=%.0f errs=%d disc=%d (%s)",
			f.Name, f.Kind, f.Physical, f.Up, f.Speed, f.MTU, f.MAC, f.Rx, f.Tx, f.Errs, f.Discards, f.Desc)
	}
	if !up {
		t.Error("no physical adapter is up")
	}
	if !s.TCP.Has || s.TCP.Estab == 0 {
		t.Errorf("tcp: %+v", s.TCP)
	}
	if len(s.Procs) < 20 {
		t.Fatalf("only %d processes", len(s.Procs))
	}
	var total float64
	names := map[string]bool{}
	for _, p := range s.Procs {
		total += p.CPU
		names[strings.ToLower(p.Name)] = true
		if p.Threads < 0 || p.PID == 0 || p.Name == "" {
			t.Fatalf("bad process %+v", p)
		}
	}
	for _, want := range []string{"system", "explorer.exe", "svchost.exe"} {
		if !names[want] {
			t.Errorf("process %s not found", want)
		}
	}
	if total > 101 {
		t.Errorf("process CPU sums to %.1f%%", total)
	}
	t.Logf("cpu %.1f%% (user %.1f sys %.1f dpc %.2f irq %.2f) cores=%d", s.CPU.Busy, s.CPU.User, s.CPU.System, s.CPU.DPC, s.CPU.IRQ, s.CPU.Count)
	t.Logf("mem %.1f%% of %s, commit %s/%s", s.Mem.Pct, FmtBytes(s.Mem.Total), FmtBytes(s.Mem.CommitUsed), FmtBytes(s.Mem.CommitLimit))
	t.Logf("disks %+v io %+v", s.Disks, s.DiskIO)
	t.Logf("tcp %+v", s.TCP)
	t.Logf("%d processes, CPU sum %.1f%%, uptime %ds", len(s.Procs), total, s.Uptime)
}

func TestSamplerHints(t *testing.T) {
	s := New(time.Second)
	now := time.Unix(1_700_000_000, 0)
	for i := 0; i < 20; i++ {
		at := now.Add(time.Duration(i) * 2 * time.Second)
		s.add(&Snapshot{Elapsed: 2,
			CPU:    CPU{Busy: 96, Count: 8, Cores: []float64{100, 90}, HasDPC: true, DPC: 9, IRQ: 3},
			Mem:    Mem{Total: 16 << 30, Avail: 1 << 30, Pct: 94},
			Disks:  []Disk{{Mount: "C:", Total: 500 << 30, Free: 1 << 30}},
			DiskIO: DiskIO{Busy: 10, Latency: 2},
			Ifaces: []Iface{{ID: "1", Name: "Ethernet", Kind: "ethernet", Physical: true, Up: true, Speed: 100e6, Errs: uint64(i * 3), RxUtil: 90, Rx: 11e6}},
			TCP:    TCP{Has: true, OutSegs: 500, Retrans: 25},
			Procs:  []Proc{{Name: "busy.exe", CPU: 60}},
		}, at)
	}
	h := s.Latest().Hints
	got := map[string]bool{}
	for _, x := range h {
		got[x.Title] = true
		t.Logf("%s %-34s %s", x.Level, x.Title, x.Text)
	}
	for _, want := range []string{"CPU is maxed out", "High interrupt / DPC time", "Memory is nearly full", "Disk C: is almost full",
		"Ethernet has errors", "Ethernet link is saturated", "Ethernet negotiated 100 Mbit/s", "TCP is retransmitting"} {
		if !got[want] {
			t.Errorf("missing hint %q", want)
		}
	}
	if h[0].Level != "bad" {
		t.Errorf("hints not ordered by severity: %+v", h[0])
	}
	if p := s.Latest().TCP.RetransPct1; p < 4.9 || p > 5.1 {
		t.Errorf("retrans over last minute = %.2f, want 5", p)
	}
	if len(s.History()) != 20 {
		t.Errorf("history %d", len(s.History()))
	}
}
