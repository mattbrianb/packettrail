// Package capture defines the platform-neutral interface between the
// OS-specific traffic sources (ETW on Windows, sock_diag/AF_PACKET on Linux,
// nettop on macOS) and the rest of the application.
package capture

import (
	"net/netip"
	"strings"
	"sync"
	"time"
)

// Proto is the IP protocol number.
type Proto uint8

const (
	TCP Proto = 6
	UDP Proto = 17
)

func (p Proto) String() string {
	switch p {
	case TCP:
		return "TCP"
	case UDP:
		return "UDP"
	}
	return "?"
}

// Dir says who initiated a flow.
type Dir uint8

const (
	DirUnknown Dir = iota // let the engine guess from the ports
	DirOut                // this host initiated; the interesting port is the remote one
	DirIn                 // remote host initiated; the interesting port is the local one
)

// FlowEvent reports payload bytes for one socket 5-tuple. An event with both
// byte counts at zero just records that the connection exists (used by the
// connections-only fallback modes).
type FlowEvent struct {
	Time   time.Time     // when the bytes were seen (end of Span)
	Span   time.Duration // period the counts cover; 0 = a single instant
	PID    int
	Proc   *ProcInfo // optional: set when the backend already knows the process
	Proto  Proto
	Local  netip.AddrPort
	Remote netip.AddrPort
	Dir    Dir
	In     uint64 // bytes received by this host
	Out    uint64 // bytes sent by this host

	// Path quality (TCP only, optional).
	Retrans     uint64  // bytes retransmitted by this host
	RetransPkts uint64  // retransmitted segments
	RTT         float64 // smoothed round-trip time sample in ms
	HasRTT      bool
	LossIn      uint64 // gaps detected in received data (duplicate-ACK episodes)
	SegsIn      uint64 // segments received over the same interval
}

// DNSEvent maps a name an application looked up to the addresses it got back.
type DNSEvent struct {
	Time  time.Time
	PID   int // 0 when unknown (e.g. read from the system DNS cache)
	Name  string
	Addrs []netip.Addr
}

// ProcInfo identifies the application that owns a socket.
type ProcInfo struct {
	PID  int
	Name string // display name, e.g. "chrome.exe" or "svchost.exe (Dnscache)"
	Path string // full image path when known
	Key  string // stable identity used for aggregation and history
}

// AppKey builds the aggregation key from a display name. Apps are keyed by
// executable name (case-insensitive) so history lines up across runs and
// privilege levels, even when the full path cannot be read.
func AppKey(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

// Sink receives events from a capturer. Implementations must be safe for
// concurrent use.
type Sink interface {
	Flows(evs []FlowEvent)
	DNS(ev DNSEvent)
}

// Status describes what a capturer is able to see.
type Status struct {
	Backend    string   `json:"backend"`    // etw, conntable, sockdiag, nettop
	Mode       string   `json:"mode"`       // "full" (bytes per process) or "connections" (no byte counts)
	Privileged bool     `json:"privileged"` // running as admin/root
	Detail     string   `json:"detail"`
	Error      string   `json:"error,omitempty"`
	Events     uint64   `json:"events"`
	Lost       uint64   `json:"lost"`
	DNSEvents  uint64   `json:"dnsEvents"`
	Notes      []string `json:"notes,omitempty"`
}

// Capturer is implemented once per operating system.
type Capturer interface {
	Start(sink Sink) error
	Stop()
	Status() Status
}

// ProcResolver maps a PID to the owning application.
type ProcResolver interface {
	Lookup(pid int) ProcInfo
}

// Options tune the capture backends.
type Options struct {
	// Unprivileged forces the reduced mode even when running as admin/root.
	Unprivileged bool
}

// New returns the capturer and process resolver for the current OS.
func New(opts Options) (Capturer, ProcResolver) { return newPlatform(opts) }

// Batcher collects events from a hot path (e.g. an ETW callback) and hands
// them to the sink in batches, so the sink's lock is taken a few times per
// second instead of once per packet.
type Batcher struct {
	mu   sync.Mutex
	buf  []FlowEvent
	sink Sink
	stop chan struct{}
	done chan struct{}
}

func NewBatcher(sink Sink, every time.Duration) *Batcher {
	b := &Batcher{sink: sink, stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(b.done)
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				b.Flush()
			case <-b.stop:
				b.Flush()
				return
			}
		}
	}()
	return b
}

func (b *Batcher) Add(ev FlowEvent) {
	b.mu.Lock()
	b.buf = append(b.buf, ev)
	b.mu.Unlock()
}

func (b *Batcher) Flush() {
	b.mu.Lock()
	evs := b.buf
	b.buf = make([]FlowEvent, 0, len(evs))
	b.mu.Unlock()
	if len(evs) > 0 {
		b.sink.Flows(evs)
	}
}

func (b *Batcher) Close() {
	close(b.stop)
	<-b.done
}

// Unmap normalises IPv4-mapped IPv6 addresses to plain IPv4.
func Unmap(ap netip.AddrPort) netip.AddrPort {
	return netip.AddrPortFrom(ap.Addr().Unmap().WithZone(""), ap.Port())
}
