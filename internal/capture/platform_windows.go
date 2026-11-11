//go:build windows

package capture

import "golang.org/x/sys/windows"

// IsPrivileged reports whether the process runs elevated.
func IsPrivileged() bool { return windows.GetCurrentProcessToken().IsElevated() }

func newPlatform(opts Options) (Capturer, ProcResolver) {
	procs := newWinProcs()
	if opts.Unprivileged {
		return newConnCapture("started in limited mode (--no-elevate)"), procs
	}
	if !IsPrivileged() {
		return newConnCapture("not running as administrator"), procs
	}
	return &fallbackCapture{primary: newETWCapture(procs), procs: procs}, procs
}

// fallbackCapture tries ETW and drops to connection-table polling if the
// ETW session cannot be started (e.g. blocked by policy).
type fallbackCapture struct {
	primary Capturer
	procs   *winProcs
	active  Capturer
	reason  string
}

func (f *fallbackCapture) Start(sink Sink) error {
	if err := f.primary.Start(sink); err != nil {
		f.reason = "ETW unavailable: " + err.Error()
		f.active = newConnCapture(f.reason)
		return f.active.Start(sink)
	}
	f.active = f.primary
	return nil
}

func (f *fallbackCapture) Stop() {
	if f.active != nil {
		f.active.Stop()
	}
}

func (f *fallbackCapture) Status() Status {
	if f.active == nil {
		return Status{Backend: "etw", Mode: "full", Privileged: true}
	}
	s := f.active.Status()
	s.Privileged = true
	if f.reason != "" {
		s.Error = f.reason
	}
	return s
}
