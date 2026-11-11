//go:build windows

package capture

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type procEntry struct {
	info ProcInfo
	at   time.Time
}

// winProcs resolves PIDs to executables, and svchost PIDs to the services
// they host.
type winProcs struct {
	mu     sync.Mutex
	cache  map[int]procEntry
	snap   map[int]string
	snapAt time.Time
	svc    map[int][]string
	svcAt  time.Time
}

func newWinProcs() *winProcs {
	return &winProcs{cache: map[int]procEntry{}}
}

func (p *winProcs) Lookup(pid int) ProcInfo {
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.cache[pid]; ok && now.Sub(e.at) < 30*time.Second {
		return e.info
	}
	info := p.resolve(pid, now)
	if len(p.cache) > 20000 {
		clear(p.cache)
	}
	p.cache[pid] = procEntry{info, now}
	return info
}

func (p *winProcs) resolve(pid int, now time.Time) ProcInfo {
	if pid == 0 || pid == 4 {
		return ProcInfo{PID: pid, Name: "System", Key: "system"}
	}
	var path, name string
	if h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid)); err == nil {
		buf := make([]uint16, windows.MAX_LONG_PATH)
		n := uint32(len(buf))
		if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) == nil {
			path = windows.UTF16ToString(buf[:n])
			name = filepath.Base(path)
		}
		windows.CloseHandle(h)
	}
	if name == "" {
		if p.snap == nil || now.Sub(p.snapAt) > 2*time.Second || (p.snap[pid] == "" && now.Sub(p.snapAt) > 200*time.Millisecond) {
			p.snap = processSnapshot()
			p.snapAt = now
		}
		name = p.snap[pid]
	}
	if name == "" {
		return ProcInfo{PID: pid, Name: "(exited pid " + strconv.Itoa(pid) + ")", Key: "(unknown)"}
	}
	info := ProcInfo{PID: pid, Name: name, Path: path, Key: AppKey(name)}
	if strings.EqualFold(name, "svchost.exe") {
		if p.svc == nil || now.Sub(p.svcAt) > 30*time.Second || (p.svc[pid] == nil && now.Sub(p.svcAt) > 2*time.Second) {
			p.svc = serviceMap()
			p.svcAt = now
		}
		if s := p.svc[pid]; len(s) > 0 {
			list := strings.Join(s, ", ")
			info.Name = "svchost.exe (" + list + ")"
			info.Key = AppKey("svchost.exe:" + list)
		}
	}
	return info
}

func processSnapshot() map[int]string {
	m := map[int]string{}
	h, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return m
	}
	defer windows.CloseHandle(h)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(h, &e); err == nil; err = windows.Process32Next(h, &e) {
		m[int(e.ProcessID)] = windows.UTF16ToString(e.ExeFile[:])
	}
	return m
}

func serviceMap() map[int][]string {
	m := map[int][]string{}
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_ENUMERATE_SERVICE)
	if err != nil {
		return m
	}
	defer windows.CloseServiceHandle(scm)
	var needed, count, resume uint32
	buf := make([]byte, 256*1024)
	for {
		err = windows.EnumServicesStatusEx(scm, windows.SC_ENUM_PROCESS_INFO, windows.SERVICE_WIN32,
			windows.SERVICE_ACTIVE, &buf[0], uint32(len(buf)), &needed, &count, &resume, nil)
		if err == windows.ERROR_MORE_DATA && needed > uint32(len(buf)) {
			buf = make([]byte, needed)
			resume = 0
			continue
		}
		if err != nil && err != windows.ERROR_MORE_DATA {
			return m
		}
		entries := unsafe.Slice((*windows.ENUM_SERVICE_STATUS_PROCESS)(unsafe.Pointer(&buf[0])), count)
		for _, s := range entries {
			pid := int(s.ServiceStatusProcess.ProcessId)
			if pid != 0 {
				m[pid] = append(m[pid], windows.UTF16PtrToString(s.ServiceName))
			}
		}
		if err == nil {
			break
		}
	}
	for _, v := range m {
		sort.Strings(v)
	}
	return m
}
