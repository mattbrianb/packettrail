//go:build windows

package capture

import (
	"encoding/binary"
	"net/netip"
	"time"
	"unsafe"
)

// Passive per-connection path quality from the TCP extended statistics
// (ESTATS) Windows keeps for each connection once collection is switched on.
// Switching it on needs administrator rights, which full (ETW) mode has.
//
//   - Path: smoothed RTT.
//   - Rec:  duplicate-ACK episodes — each time this host saw a gap in the
//           data it was receiving (loss or reordering on the way TO us).
//   - Data: segments received, to turn episodes into a rate.

var (
	procSetPerTcpConnectionEStats  = modIphlpapi.NewProc("SetPerTcpConnectionEStats")
	procGetPerTcpConnectionEStats  = modIphlpapi.NewProc("GetPerTcpConnectionEStats")
	procSetPerTcp6ConnectionEStats = modIphlpapi.NewProc("SetPerTcp6ConnectionEStats")
	procGetPerTcp6ConnectionEStats = modIphlpapi.NewProc("GetPerTcp6ConnectionEStats")
)

const (
	estatsData = 1
	estatsPath = 3
	estatsRec  = 5

	pathRodSize = 160 // TCP_ESTATS_PATH_ROD_v0: 40 ULONGs
	recRodSize  = 72  // TCP_ESTATS_REC_ROD_v0 (x64)
	dataRodSize = 96  // TCP_ESTATS_DATA_ROD_v0 (x64)

	rodSmoothedRtt    = 27 * 4 // Path
	rodCountRtt       = 32 * 4 // Path
	rodDupAckEpisodes = 4 * 4  // Rec
	rodDataSegsIn     = 24     // Data (ULONG64)

	mibTCPStateEstab = 5
)

type mibTCPRow struct {
	State, LocalAddr, LocalPort, RemoteAddr, RemotePort uint32
}

type mibTCP6Row struct {
	State         uint32
	LocalAddr     [16]byte
	LocalScopeID  uint32
	LocalPort     uint32
	RemoteAddr    [16]byte
	RemoteScopeID uint32
	RemotePort    uint32
}

var _ = [1]struct{}{}[unsafe.Sizeof(mibTCP6Row{})-52]

// netPort returns the DWORD whose first two bytes in memory are the port in
// network byte order, as the MIB rows store it.
func netPort(p uint16) uint32 { return uint32(p>>8 | (p&0xff)<<8) }

type connCounters struct {
	dupEp  uint32
	segsIn uint64
	have   bool
}

type rttSampler struct {
	enabled map[[2]netip.AddrPort]*connCounters
	failed  bool
}

// estats enables collection (rod == nil) or reads one statistics block.
func estats(r ConnRow, typ uintptr, rod []byte) bool {
	rw := byte(1)
	var rodPtr, rodLen uintptr
	if rod != nil {
		rodPtr, rodLen = uintptr(unsafe.Pointer(&rod[0])), uintptr(len(rod))
	}
	if r.Local.Addr().Is4() {
		row := mibTCPRow{State: r.State, RemotePort: netPort(r.Remote.Port()), LocalPort: netPort(r.Local.Port())}
		l, rm := r.Local.Addr().As4(), r.Remote.Addr().As4()
		row.LocalAddr, row.RemoteAddr = binary.LittleEndian.Uint32(l[:]), binary.LittleEndian.Uint32(rm[:])
		if rod == nil {
			ret, _, _ := procSetPerTcpConnectionEStats.Call(uintptr(unsafe.Pointer(&row)), typ, uintptr(unsafe.Pointer(&rw)), 0, 1, 0)
			return ret == 0
		}
		ret, _, _ := procGetPerTcpConnectionEStats.Call(uintptr(unsafe.Pointer(&row)), typ, 0, 0, 0, 0, 0, 0, rodPtr, 0, rodLen)
		return ret == 0
	}
	row := mibTCP6Row{State: r.State, LocalAddr: r.Local.Addr().As16(), RemoteAddr: r.Remote.Addr().As16(),
		LocalPort: netPort(r.Local.Port()), RemotePort: netPort(r.Remote.Port())}
	if rod == nil {
		ret, _, _ := procSetPerTcp6ConnectionEStats.Call(uintptr(unsafe.Pointer(&row)), typ, uintptr(unsafe.Pointer(&rw)), 0, 1, 0)
		return ret == 0
	}
	ret, _, _ := procGetPerTcp6ConnectionEStats.Call(uintptr(unsafe.Pointer(&row)), typ, 0, 0, 0, 0, 0, 0, rodPtr, 0, rodLen)
	return ret == 0
}

// sample enables collection on new established connections and returns RTT
// and download-side loss samples for the others.
func (s *rttSampler) sample(ls *listenSet) []FlowEvent {
	if s.failed {
		return nil
	}
	rows, err := ReadConnTables(false)
	if err != nil {
		return nil
	}
	if s.enabled == nil {
		s.enabled = map[[2]netip.AddrPort]*connCounters{}
	}
	seen := map[[2]netip.AddrPort]bool{}
	now := time.Now()
	path, rec, data := make([]byte, pathRodSize), make([]byte, recRodSize), make([]byte, dataRodSize)
	var evs []FlowEvent
	tried, ok := 0, 0
	for _, r := range rows {
		if r.State != mibTCPStateEstab || r.PID == 0 || r.Remote.Addr().IsLoopback() {
			continue
		}
		k := [2]netip.AddrPort{r.Local, r.Remote}
		seen[k] = true
		cc := s.enabled[k]
		if cc == nil {
			tried++
			if estats(r, estatsPath, nil) {
				ok++
				estats(r, estatsRec, nil)
				estats(r, estatsData, nil)
				s.enabled[k] = &connCounters{}
			}
			continue // the first sample arrives on the next round
		}
		ev := FlowEvent{Time: now, PID: r.PID, Proto: TCP, Local: r.Local, Remote: r.Remote, Dir: DirOut}
		if ls != nil && ls.tcp[r.Local.Port()] {
			ev.Dir = DirIn
		}
		clear(path)
		if estats(r, estatsPath, path) && binary.LittleEndian.Uint32(path[rodCountRtt:]) > 0 {
			ev.RTT, ev.HasRTT = float64(binary.LittleEndian.Uint32(path[rodSmoothedRtt:])), true
		}
		clear(rec)
		clear(data)
		if estats(r, estatsRec, rec) && estats(r, estatsData, data) {
			dup := binary.LittleEndian.Uint32(rec[rodDupAckEpisodes:])
			segs := binary.LittleEndian.Uint64(data[rodDataSegsIn:])
			if cc.have && dup >= cc.dupEp && segs >= cc.segsIn {
				ev.LossIn, ev.SegsIn = uint64(dup-cc.dupEp), segs-cc.segsIn
			}
			cc.dupEp, cc.segsIn, cc.have = dup, segs, true
		}
		if ev.HasRTT || ev.SegsIn > 0 {
			evs = append(evs, ev)
		}
	}
	if tried > 20 && ok == 0 && len(s.enabled) == 0 {
		s.failed = true // not permitted on this system; stop trying
	}
	for k := range s.enabled {
		if !seen[k] {
			delete(s.enabled, k)
		}
	}
	return evs
}
