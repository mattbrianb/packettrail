//go:build windows

package capture

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ETW plumbing written directly against advapi32 so the binary needs no cgo
// and no native helper DLLs.

var (
	modAdvapi32        = windows.NewLazySystemDLL("advapi32.dll")
	procStartTraceW    = modAdvapi32.NewProc("StartTraceW")
	procControlTraceW  = modAdvapi32.NewProc("ControlTraceW")
	procEnableTraceEx2 = modAdvapi32.NewProc("EnableTraceEx2")
	procOpenTraceW     = modAdvapi32.NewProc("OpenTraceW")
	procProcessTrace   = modAdvapi32.NewProc("ProcessTrace")
	procCloseTrace     = modAdvapi32.NewProc("CloseTrace")
)

const (
	etwSessionName = "PacketTrail-Capture"

	wnodeFlagTracedGUID       = 0x00020000
	eventTraceRealTimeMode    = 0x00000100
	eventTraceUseMsFlushTimer = 0x00000010
	eventTraceControlQuery    = 0
	eventTraceControlStop     = 1
	eventControlCodeEnable    = 1
	processTraceRealTime      = 0x00000100
	processTraceEventRecord   = 0x10000000
	invalidProcessTraceHandle = ^uint64(0)
	traceLevelVerbose         = 5
)

var (
	// Microsoft-Windows-Kernel-Network
	guidKernelNetwork = windows.GUID{Data1: 0x7dd42a49, Data2: 0x5329, Data3: 0x4832, Data4: [8]byte{0x8d, 0xfd, 0x43, 0xd9, 0x79, 0x15, 0x3a, 0x88}}
	// Microsoft-Windows-DNS-Client
	guidDNSClient = windows.GUID{Data1: 0x1c95126e, Data2: 0x7eea, Data3: 0x49a9, Data4: [8]byte{0xa3, 0xfe, 0xa3, 0x78, 0xb0, 0x3d, 0xdb, 0x4d}}
	// Arbitrary GUID identifying our session.
	guidSession = windows.GUID{Data1: 0x6e1c4a7b, Data2: 0x2f0d, Data3: 0x4d0a, Data4: [8]byte{0x9b, 0x51, 0x3c, 0x7e, 0x11, 0x42, 0x90, 0x5d}}
)

type wnodeHeader struct {
	BufferSize        uint32
	ProviderId        uint32
	HistoricalContext uint64
	TimeStamp         int64
	Guid              windows.GUID
	ClientContext     uint32
	Flags             uint32
}

type eventTraceProperties struct {
	Wnode               wnodeHeader
	BufferSize          uint32
	MinimumBuffers      uint32
	MaximumBuffers      uint32
	MaximumFileSize     uint32
	LogFileMode         uint32
	FlushTimer          uint32
	EnableFlags         uint32
	AgeLimit            int32
	NumberOfBuffers     uint32
	FreeBuffers         uint32
	EventsLost          uint32
	BuffersWritten      uint32
	LogBuffersLost      uint32
	RealTimeBuffersLost uint32
	LoggerThreadId      uintptr
	LogFileNameOffset   uint32
	LoggerNameOffset    uint32
}

type eventTraceHeader struct {
	Size           uint16
	FieldTypeFlags uint16
	Version        uint32
	ThreadId       uint32
	ProcessId      uint32
	TimeStamp      int64
	Guid           windows.GUID
	ProcessorTime  uint64
}

type eventTrace struct {
	Header           eventTraceHeader
	InstanceId       uint32
	ParentInstanceId uint32
	ParentGuid       windows.GUID
	MofData          uintptr
	MofLength        uint32
	ClientContext    uint32
}

type traceLogfileHeader struct {
	BufferSize         uint32
	Version            uint32
	ProviderVersion    uint32
	NumberOfProcessors uint32
	EndTime            int64
	TimerResolution    uint32
	MaximumFileSize    uint32
	LogFileMode        uint32
	BuffersWritten     uint32
	LogInstanceGuid    windows.GUID
	LoggerName         uintptr
	LogFileName        uintptr
	TimeZone           windows.Timezoneinformation
	BootTime           int64
	PerfFreq           int64
	StartTime          int64
	ReservedFlags      uint32
	BuffersLost        uint32
}

type eventTraceLogfile struct {
	LogFileName         *uint16
	LoggerName          *uint16
	CurrentTime         int64
	BuffersRead         uint32
	ProcessTraceMode    uint32
	CurrentEvent        eventTrace
	LogfileHeader       traceLogfileHeader
	BufferCallback      uintptr
	BufferSize          uint32
	Filled              uint32
	EventsLost          uint32
	EventRecordCallback uintptr
	IsKernelTrace       uint32
	Context             uintptr
}

type eventDescriptor struct {
	Id      uint16
	Version uint8
	Channel uint8
	Level   uint8
	Opcode  uint8
	Task    uint16
	Keyword uint64
}

type eventHeader struct {
	Size            uint16
	HeaderType      uint16
	Flags           uint16
	EventProperty   uint16
	ThreadId        uint32
	ProcessId       uint32
	TimeStamp       int64
	ProviderId      windows.GUID
	EventDescriptor eventDescriptor
	ProcessorTime   uint64
	ActivityId      windows.GUID
}

type eventRecord struct {
	EventHeader       eventHeader
	ProcessorNumber   uint8
	Alignment         uint8
	LoggerId          uint16
	ExtendedDataCount uint16
	UserDataLength    uint16
	ExtendedData      unsafe.Pointer
	UserData          unsafe.Pointer
	UserContext       unsafe.Pointer
}

// Compile-time layout checks against the Windows SDK sizes (x64).
var _ = [1]struct{}{}[unsafe.Sizeof(eventTraceProperties{})-120]
var _ = [1]struct{}{}[unsafe.Sizeof(eventTraceLogfile{})-448]
var _ = [1]struct{}{}[unsafe.Sizeof(eventRecord{})-112]

type sessionProps struct {
	props eventTraceProperties
	name  [256]uint16
}

func newSessionProps() *sessionProps {
	p := &sessionProps{}
	p.props.Wnode.BufferSize = uint32(unsafe.Sizeof(*p))
	p.props.Wnode.Guid = guidSession
	p.props.Wnode.ClientContext = 2 // system time stamps
	p.props.Wnode.Flags = wnodeFlagTracedGUID
	p.props.BufferSize = 64 // KB
	p.props.MinimumBuffers = 8
	p.props.MaximumBuffers = 64
	p.props.LogFileMode = eventTraceRealTimeMode | eventTraceUseMsFlushTimer
	p.props.FlushTimer = 250 // ms
	p.props.LoggerNameOffset = uint32(unsafe.Offsetof(p.name))
	return p
}

func controlTrace(handle uint64, name string, code uint32) (*sessionProps, error) {
	p := newSessionProps()
	n, _ := windows.UTF16PtrFromString(name)
	var np uintptr
	if handle == 0 {
		np = uintptr(unsafe.Pointer(n))
	}
	r, _, _ := procControlTraceW.Call(uintptr(handle), np, uintptr(unsafe.Pointer(&p.props)), uintptr(code))
	if r != 0 {
		return p, syscall.Errno(r)
	}
	return p, nil
}

type etwProvider struct {
	guid     windows.GUID
	keywords uint64
	required bool
	handle   func(c *etwCapture, rec *eventRecord, data []byte)
}

// etwCapture is the full-featured Windows backend.
type etwCapture struct {
	name      string
	providers []etwProvider
	procs     *winProcs
	sink      Sink
	batch     *Batcher
	session   uint64
	trace     uint64
	cb        uintptr

	listen atomic.Pointer[listenSet]
	dirs   map[tcpTuple]Dir // only touched on the ProcessTrace thread
	rtt    rttSampler       // only touched on the poll goroutine

	events, dnsEvents, unknown atomic.Uint64
	lost                       atomic.Uint64
	mu                         sync.Mutex
	err                        string
	stopOnce                   sync.Once
	done                       chan struct{}
	stopPoll                   chan struct{}
}

type tcpTuple struct {
	local, remote netip.AddrPort
}

var activeETW atomic.Pointer[etwCapture]

func newETWCapture(procs *winProcs) *etwCapture {
	return &etwCapture{
		name:  etwSessionName,
		procs: procs,
		providers: []etwProvider{
			{guid: guidKernelNetwork, keywords: 0x30 /* IPv4|IPv6 */, required: true, handle: (*etwCapture).onKernelNetwork},
			{guid: guidDNSClient, handle: (*etwCapture).onDNS},
		},
		dirs: map[tcpTuple]Dir{}, done: make(chan struct{}), stopPoll: make(chan struct{}),
	}
}

func (c *etwCapture) Start(sink Sink) error {
	c.sink = sink
	// A previous run that crashed may have left the session running.
	controlTrace(0, c.name, eventTraceControlStop)

	p := newSessionProps()
	name, _ := windows.UTF16PtrFromString(c.name)
	var h uint64
	r, _, _ := procStartTraceW.Call(uintptr(unsafe.Pointer(&h)), uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&p.props)))
	if r != 0 {
		return fmt.Errorf("StartTrace: %w", syscall.Errno(r))
	}
	c.session = h

	for i := range c.providers {
		pr := &c.providers[i]
		r, _, _ := procEnableTraceEx2.Call(uintptr(h), uintptr(unsafe.Pointer(&pr.guid)), eventControlCodeEnable,
			traceLevelVerbose, uintptr(pr.keywords), 0, 0, 0)
		if r != 0 {
			err := syscall.Errno(r)
			if pr.required {
				c.stopSession()
				return fmt.Errorf("enable provider %v: %w", pr.guid, err)
			}
			c.setErr(fmt.Sprintf("optional provider %v not enabled: %v", pr.guid, err))
		}
	}

	c.batch = NewBatcher(sink, 250*time.Millisecond)
	activeETW.Store(c)
	c.cb = etwCallbackPtr()

	lf := eventTraceLogfile{}
	lf.LoggerName = name
	lf.ProcessTraceMode = processTraceRealTime | processTraceEventRecord
	lf.EventRecordCallback = c.cb
	th, _, _ := procOpenTraceW.Call(uintptr(unsafe.Pointer(&lf)))
	if uint64(th) == invalidProcessTraceHandle {
		err := windows.GetLastError()
		c.stopSession()
		c.batch.Close()
		return fmt.Errorf("OpenTrace: %v", err)
	}
	c.trace = uint64(th)

	c.refreshListen()
	go c.pollLoop()
	go func() {
		runtime.LockOSThread()
		defer close(c.done)
		handles := [1]uint64{c.trace}
		r, _, _ := procProcessTrace.Call(uintptr(unsafe.Pointer(&handles[0])), 1, 0, 0)
		if r != 0 && r != uintptr(windows.ERROR_CANCELLED) {
			c.setErr(fmt.Sprintf("ProcessTrace ended: %v", syscall.Errno(r)))
		}
	}()
	return nil
}

func (c *etwCapture) pollLoop() {
	dnsTick := time.NewTicker(20 * time.Second)
	lstTick := time.NewTicker(5 * time.Second)
	qTick := time.NewTicker(5 * time.Second)
	rttTick := time.NewTicker(5 * time.Second)
	defer dnsTick.Stop()
	defer lstTick.Stop()
	defer qTick.Stop()
	defer rttTick.Stop()
	pollDNSCache(c.sink)
	for {
		select {
		case <-c.stopPoll:
			return
		case <-dnsTick.C:
			pollDNSCache(c.sink)
		case <-lstTick.C:
			c.refreshListen()
		case <-rttTick.C:
			if evs := c.rtt.sample(c.listen.Load()); len(evs) > 0 {
				c.sink.Flows(evs)
			}
		case <-qTick.C:
			if p, err := controlTrace(c.session, "", eventTraceControlQuery); err == nil {
				c.lost.Store(uint64(p.props.EventsLost) + uint64(p.props.RealTimeBuffersLost))
			}
		}
	}
}

func (c *etwCapture) refreshListen() {
	if ls, err := readListenSet(); err == nil {
		c.listen.Store(ls)
	}
}

func (c *etwCapture) stopSession() {
	if c.session != 0 {
		controlTrace(c.session, "", eventTraceControlStop)
		c.session = 0
	}
}

func (c *etwCapture) Stop() {
	c.stopOnce.Do(func() {
		close(c.stopPoll)
		c.stopSession()
		if c.trace != 0 {
			procCloseTrace.Call(uintptr(c.trace))
		}
		select {
		case <-c.done:
		case <-time.After(3 * time.Second):
		}
		if c.batch != nil {
			c.batch.Close()
		}
		activeETW.CompareAndSwap(c, nil)
	})
}

func (c *etwCapture) setErr(s string) {
	c.mu.Lock()
	c.err = s
	c.mu.Unlock()
}

func (c *etwCapture) Status() Status {
	c.mu.Lock()
	errS := c.err
	c.mu.Unlock()
	return Status{
		Backend: "etw", Mode: "full", Privileged: true,
		Detail:    "ETW Microsoft-Windows-Kernel-Network + DNS-Client (real-time session " + c.name + ")",
		Error:     errS,
		Events:    c.events.Load(),
		Lost:      c.lost.Load(),
		DNSEvents: c.dnsEvents.Load(),
	}
}

func etwCallback(rec *eventRecord) uintptr {
	c := activeETW.Load()
	if c == nil || rec == nil {
		return 0
	}
	if rec.UserDataLength == 0 || rec.UserData == nil {
		return 0
	}
	data := unsafe.Slice((*byte)(rec.UserData), int(rec.UserDataLength))
	for i := range c.providers {
		if c.providers[i].guid == rec.EventHeader.ProviderId {
			c.providers[i].handle(c, rec, data)
			break
		}
	}
	return 0
}

var (
	cbOnce sync.Once
	cbPtr  uintptr
)

// Callbacks created with NewCallback are never freed, so make exactly one.
func etwCallbackPtr() uintptr {
	cbOnce.Do(func() { cbPtr = windows.NewCallback(etwCallback) })
	return cbPtr
}

func filetimeToTime(ft int64) time.Time {
	if ft <= 0 {
		return time.Now()
	}
	return time.Unix(0, (ft-116444736000000000)*100)
}

// KNEvent is a decoded Microsoft-Windows-Kernel-Network event.
type KNEvent struct {
	Proto        Proto
	Kind         int // 1 send, 2 recv, 3 connect, 4 accept, 5 disconnect, 6 retransmit
	PID          uint32
	Size         uint32
	Daddr, Saddr netip.Addr
	Dport, Sport uint16
}

// ParseKernelNetwork decodes the payload of the Kernel-Network events we use.
// Layout (manifest templates): PID u32, size u32, daddr, saddr, dport, sport
// (ports in network byte order), followed by fields we ignore.
func ParseKernelNetwork(id uint16, d []byte) (KNEvent, bool) {
	var ev KNEvent
	v6 := false
	switch id {
	case 10:
		ev.Proto, ev.Kind = TCP, 1
	case 11:
		ev.Proto, ev.Kind = TCP, 2
	case 12:
		ev.Proto, ev.Kind = TCP, 3
	case 15:
		ev.Proto, ev.Kind = TCP, 4
	case 13:
		ev.Proto, ev.Kind = TCP, 5
	case 14:
		ev.Proto, ev.Kind = TCP, 6
	case 26:
		ev.Proto, ev.Kind, v6 = TCP, 1, true
	case 27:
		ev.Proto, ev.Kind, v6 = TCP, 2, true
	case 28:
		ev.Proto, ev.Kind, v6 = TCP, 3, true
	case 31:
		ev.Proto, ev.Kind, v6 = TCP, 4, true
	case 29:
		ev.Proto, ev.Kind, v6 = TCP, 5, true
	case 30:
		ev.Proto, ev.Kind, v6 = TCP, 6, true
	case 42:
		ev.Proto, ev.Kind = UDP, 1
	case 43:
		ev.Proto, ev.Kind = UDP, 2
	case 58:
		ev.Proto, ev.Kind, v6 = UDP, 1, true
	case 59:
		ev.Proto, ev.Kind, v6 = UDP, 2, true
	default:
		return ev, false
	}
	le := binary.LittleEndian
	be := binary.BigEndian
	if !v6 {
		if len(d) < 20 {
			return ev, false
		}
		ev.PID, ev.Size = le.Uint32(d[0:]), le.Uint32(d[4:])
		ev.Daddr = netip.AddrFrom4([4]byte(d[8:12]))
		ev.Saddr = netip.AddrFrom4([4]byte(d[12:16]))
		ev.Dport, ev.Sport = be.Uint16(d[16:]), be.Uint16(d[18:])
	} else {
		if len(d) < 44 {
			return ev, false
		}
		ev.PID, ev.Size = le.Uint32(d[0:]), le.Uint32(d[4:])
		ev.Daddr = netip.AddrFrom16([16]byte(d[8:24])).Unmap()
		ev.Saddr = netip.AddrFrom16([16]byte(d[24:40])).Unmap()
		ev.Dport, ev.Sport = be.Uint16(d[40:]), be.Uint16(d[42:])
	}
	return ev, true
}

func (c *etwCapture) onKernelNetwork(rec *eventRecord, data []byte) {
	kn, ok := ParseKernelNetwork(rec.EventHeader.EventDescriptor.Id, data)
	if !ok {
		c.unknown.Add(1)
		return
	}
	c.events.Add(1)
	local := netip.AddrPortFrom(kn.Saddr, kn.Sport)
	remote := netip.AddrPortFrom(kn.Daddr, kn.Dport)
	tt := tcpTuple{local, remote}
	switch kn.Kind {
	case 3: // connect
		c.trackDir(tt, DirOut)
	case 4: // accept
		c.trackDir(tt, DirIn)
	case 5:
		delete(c.dirs, tt)
		return
	}
	ev := FlowEvent{
		Time:   filetimeToTime(rec.EventHeader.TimeStamp),
		PID:    int(kn.PID),
		Proto:  kn.Proto,
		Local:  local,
		Remote: remote,
	}
	switch kn.Kind {
	case 1:
		ev.Out = uint64(kn.Size)
	case 2:
		ev.In = uint64(kn.Size)
	case 6:
		ev.Retrans, ev.RetransPkts = uint64(kn.Size), 1
	}
	if kn.Proto == TCP {
		if d, ok := c.dirs[tt]; ok {
			ev.Dir = d
		} else if ls := c.listen.Load(); ls != nil {
			if ls.tcp[kn.Sport] {
				ev.Dir = DirIn
			} else {
				ev.Dir = DirOut
			}
		}
	}
	c.batch.Add(ev)
}

func (c *etwCapture) trackDir(t tcpTuple, d Dir) {
	if len(c.dirs) > 200000 {
		clear(c.dirs)
	}
	c.dirs[t] = d
}

// ParseDNS3008 decodes "DNS query is completed" events: QueryName (UTF-16z),
// QueryType u32, QueryOptions u64, QueryStatus u32, QueryResults (UTF-16z).
func ParseDNS3008(d []byte) (name string, status uint32, addrs []netip.Addr, ok bool) {
	name, n := utf16z(d)
	if n == 0 || len(d)-n < 16 {
		return "", 0, nil, false
	}
	d = d[n:]
	status = binary.LittleEndian.Uint32(d[12:])
	results, _ := utf16z(d[16:])
	return name, status, ParseDNSResults(results), true
}

// ParseDNSResults parses strings like
// "type:  5 edge.example.net;::ffff:93.184.216.34;2606:2800::1;".
func ParseDNSResults(s string) []netip.Addr {
	var out []netip.Addr
	for _, tok := range strings.Split(s, ";") {
		tok = strings.TrimSpace(tok)
		if tok == "" || strings.HasPrefix(tok, "type:") {
			continue
		}
		if ip, err := netip.ParseAddr(tok); err == nil {
			out = append(out, ip.Unmap())
		}
	}
	return out
}

func utf16z(d []byte) (string, int) {
	for i := 0; i+1 < len(d); i += 2 {
		if d[i] == 0 && d[i+1] == 0 {
			u := make([]uint16, i/2)
			for j := range u {
				u[j] = binary.LittleEndian.Uint16(d[2*j:])
			}
			return string(utf16.Decode(u)), i + 2
		}
	}
	return "", 0
}

func (c *etwCapture) onDNS(rec *eventRecord, data []byte) {
	if rec.EventHeader.EventDescriptor.Id != 3008 {
		return
	}
	name, status, addrs, ok := ParseDNS3008(data)
	if !ok || status != 0 || len(addrs) == 0 {
		return
	}
	c.dnsEvents.Add(1)
	c.sink.DNS(DNSEvent{Time: filetimeToTime(rec.EventHeader.TimeStamp), PID: int(rec.EventHeader.ProcessId), Name: name, Addrs: addrs})
}
