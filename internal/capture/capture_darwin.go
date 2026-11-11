//go:build darwin

package capture

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// macOS backend: polls the built-in `nettop` tool, which reports cumulative
// per-socket byte counters for every process without needing root. No cgo.
// DNS names are not captured on macOS (reverse DNS is used instead).

func IsPrivileged() bool { return os.Geteuid() == 0 }

func newPlatform(opts Options) (Capturer, ProcResolver) {
	procs := &darwinProcs{paths: map[int]string{}}
	return &nettopCapture{procs: procs, prev: map[string][3]uint64{}, stop: make(chan struct{}), done: make(chan struct{})}, procs
}

// Pause between two nettop runs. Counters are cumulative per socket, so a
// connection that opens and closes inside one pause is missed, and the last
// moment of every connection is not counted: shorter is more accurate.
const nettopInterval = time.Second

type nettopCapture struct {
	procs  *darwinProcs
	sink   Sink
	prev   map[string][3]uint64
	first  bool
	stop   chan struct{}
	done   chan struct{}
	once   sync.Once
	events atomic.Uint64
	mu     sync.Mutex
	err    string
}

func (c *nettopCapture) Start(sink Sink) error {
	c.sink = sink
	if _, err := exec.LookPath("nettop"); err != nil {
		return fmt.Errorf("nettop not found: %w", err)
	}
	c.first = true
	go func() {
		defer close(c.done)
		last := time.Now()
		for {
			c.poll(time.Since(last))
			last = time.Now()
			select {
			case <-c.stop:
				return
			case <-time.After(nettopInterval):
			}
		}
	}()
	return nil
}

func (c *nettopCapture) Stop() {
	c.once.Do(func() { close(c.stop); <-c.done })
}

func (c *nettopCapture) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Status{
		Backend: "nettop", Mode: "full", Privileged: IsPrivileged(),
		Detail: "nettop per-socket counters, read about once a second",
		Error:  c.err, Events: c.events.Load(),
		Notes: []string{
			"DNS names are not captured on macOS; reverse DNS (PTR) is shown instead.",
			"Very short connections (under about a second) can be missed or undercounted on macOS.",
		},
	}
}

func (c *nettopCapture) setErr(s string) {
	c.mu.Lock()
	changed := s != c.err
	c.err = s
	c.mu.Unlock()
	if changed && s != "" {
		log.Printf("capture: %s", s)
	}
}

func (c *nettopCapture) poll(span time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "nettop", "-L", "1", "-n", "-x", "-J", "bytes_in,bytes_out,re-tx,rtt_avg").Output()
	if err != nil {
		msg := "nettop: " + err.Error()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if line, _, _ := strings.Cut(strings.TrimSpace(string(ee.Stderr)), "\n"); line != "" {
				msg += ": " + line
			}
		}
		c.setErr(msg)
		return
	}
	rows := ParseNettop(out)
	if len(rows) == 0 && bytes.Count(out, []byte("\n")) > 5 {
		// nettop listed processes, yet no socket line was understood: its
		// output format is not the one this parser knows. Say so instead of
		// showing an empty table.
		head := string(out)
		if len(head) > 300 {
			head = head[:300]
		}
		c.setErr("nettop output was not recognised. First lines: " + strings.ReplaceAll(strings.TrimSpace(head), "\n", " | "))
	} else {
		c.setErr("")
	}
	now := time.Now()
	seen := make(map[string]bool, len(rows))
	var evs []FlowEvent
	for _, r := range rows {
		key := strconv.Itoa(r.PID) + "|" + r.Proto.String() + "|" + r.Local.String() + "|" + r.Remote.String()
		seen[key] = true
		prev, had := c.prev[key]
		c.prev[key] = [3]uint64{r.In, r.Out, r.Retrans}
		var din, dout, drtx uint64
		switch {
		case c.first:
			// baseline: counters accumulated before we started
		case !had:
			din, dout, drtx = r.In, r.Out, r.Retrans
		default:
			if r.In >= prev[0] {
				din = r.In - prev[0]
			}
			if r.Out >= prev[1] {
				dout = r.Out - prev[1]
			}
			if r.Retrans >= prev[2] {
				drtx = r.Retrans - prev[2]
			}
		}
		if din == 0 && dout == 0 && drtx == 0 && !c.first {
			continue
		}
		info := c.procs.info(r.PID, r.Name)
		ev := FlowEvent{Time: now, Span: span, PID: r.PID, Proc: &info, Proto: r.Proto,
			Local: r.Local, Remote: r.Remote, In: din, Out: dout, Retrans: drtx}
		if r.RTT > 0 && r.Proto == TCP {
			ev.RTT, ev.HasRTT = r.RTT, true
		}
		evs = append(evs, ev)
		if din|dout != 0 {
			c.events.Add(1)
		}
	}
	for k := range c.prev {
		if !seen[k] {
			delete(c.prev, k)
		}
	}
	c.first = false
	if len(evs) > 0 {
		c.sink.Flows(evs)
	}
}

type darwinProcs struct {
	mu    sync.Mutex
	paths map[int]string
	at    time.Time
}

func (p *darwinProcs) refresh(force bool) {
	if !force && time.Since(p.at) < 10*time.Second || time.Since(p.at) < time.Second {
		return
	}
	p.at = time.Now()
	out, err := exec.Command("ps", "-axo", "pid=,comm=").Output()
	if err != nil {
		return
	}
	m := map[int]string{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		sp := strings.IndexByte(line, ' ')
		if sp <= 0 {
			continue
		}
		if pid, err := strconv.Atoi(line[:sp]); err == nil {
			m[pid] = strings.TrimSpace(line[sp+1:])
		}
	}
	p.paths = m
}

func (p *darwinProcs) info(pid int, nettopName string) ProcInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	path := p.paths[pid]
	if path == "" {
		p.refresh(true)
		path = p.paths[pid]
	} else {
		p.refresh(false)
	}
	name := nettopName
	if path != "" && strings.HasPrefix(path, "/") {
		name = filepath.Base(path)
	}
	if name == "" {
		name = "(unknown)"
	}
	return ProcInfo{PID: pid, Name: name, Path: path, Key: AppKey(name)}
}

func (p *darwinProcs) Lookup(pid int) ProcInfo { return p.info(pid, "") }
