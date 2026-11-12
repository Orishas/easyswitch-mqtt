// Package bootdiag runs diag's quick probes as a temporary system service,
// so they also run at the login screen, before anyone logs in. It answers
// whether a Windows service in session 0, or a macOS LaunchDaemon running
// as root, may open the Bolt receiver. Each run is appended to a report
// file that the person prints after logging in.
package bootdiag

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/kardianos/service"

	"github.com/Orishas/easyswitch-mqtt/internal/diag"
	"github.com/Orishas/easyswitch-mqtt/internal/hid"
)

// Name is the service's name.
const Name = "easyswitch-bootdiag"

const (
	interval   = 15 * time.Second // between the starts of two runs
	duration   = 5 * time.Minute  // runs start this long after the service starts
	timeout    = 3 * time.Second  // per request
	timeFormat = "2006-01-02 15:04:05 -07:00"
)

// Version is the easyswitch version written into each run.
var Version = "unknown"

var (
	// ErrNoReport: the service has not written its file yet.
	ErrNoReport = errors.New("No boot report yet. Did the computer restart after --boot-install?")
	// ErrNotAdmin: installing or removing the service needs administrator
	// (Windows) or root (macOS) rights.
	ErrNotAdmin = errors.New("bootdiag: administrator rights needed")
	// ErrNotInstalled: there is neither a service nor a report to remove.
	ErrNotInstalled = errors.New("bootdiag: nothing installed")
)

// ReportPath is the report file: %ProgramData%\easyswitch\boot-diag.txt
// on Windows, /Library/Application Support/easyswitch/boot-diag.txt on
// macOS, "" elsewhere.
func ReportPath() string { return reportPath() }

// SessionState says whether someone is logged in at the console: "before
// login", "logged in: <user>" or "unknown".
func SessionState() string { return sessionState() }

// newService returns the service as Install registers it, run by p,
// waiting on runWait (see waitFor). p and runWait may be nil for anything
// but Run.
func newService(p service.Interface, runWait func()) (service.Service, error) {
	if !supported {
		return nil, hid.ErrUnsupported
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if exe, err = filepath.Abs(exe); err != nil {
		return nil, err
	}
	opt := service.KeyValue{
		"RunAtLoad":    true,                       // macOS: start at boot
		"KeepAlive":    false,                      // macOS: do not start it again after it ends
		"LogDirectory": filepath.Dir(ReportPath()), // macOS: the default log directory may not exist
		"StartType":    "automatic",                // Windows
		"OnFailure":    "noaction",                 // Windows: do not start it again after it ends
	}
	if runWait != nil {
		opt["RunWait"] = runWait
	}
	return service.New(p, &service.Config{
		Name:        Name,
		DisplayName: "easyswitch boot diag",
		Description: "Checks for five minutes after start whether the Logi Bolt receiver can be reached before login. Remove it with: easyswitch diag --boot-remove",
		Executable:  exe,
		Arguments:   []string{"diag", "--boot-run"},
		Option:      opt,
	})
}

// Install registers the service to run `easyswitch diag --boot-run` with
// this executable at the next start of the computer. It does not start
// it. An installed service is replaced.
func Install() error {
	s, err := newService(nil, nil)
	if err != nil {
		return err
	}
	if !isAdmin() {
		return ErrNotAdmin
	}
	if _, err := s.Status(); !errors.Is(err, service.ErrNotInstalled) {
		if err := remove(s); err != nil {
			return fmt.Errorf("bootdiag: remove the earlier service: %w", err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(ReportPath()), 0o755); err != nil {
		return err
	}
	if err := s.Install(); err != nil {
		return fmt.Errorf("bootdiag: install the service: %w", err)
	}
	return nil
}

// Uninstall removes the service and the report file. It returns
// ErrNotInstalled when there is neither.
func Uninstall() error {
	s, err := newService(nil, nil)
	if err != nil {
		return err
	}
	_, statusErr := s.Status()
	installed := !errors.Is(statusErr, service.ErrNotInstalled)
	_, statErr := os.Stat(ReportPath())
	if !installed && errors.Is(statErr, os.ErrNotExist) {
		return ErrNotInstalled
	}
	if !isAdmin() {
		return ErrNotAdmin
	}
	if installed {
		if err := remove(s); err != nil {
			return fmt.Errorf("bootdiag: remove the service: %w", err)
		}
	}
	if err := os.Remove(ReportPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_ = os.Remove(filepath.Dir(ReportPath())) // only when empty
	return nil
}

// remove stops the service if it runs and unregisters it.
func remove(s service.Service) error {
	if st, err := s.Status(); err == nil && st == service.StatusRunning {
		_ = s.Stop()
	}
	return s.Uninstall()
}

// Report prints the report file to w.
func Report(w io.Writer) error {
	if !supported {
		return hid.ErrUnsupported
	}
	return report(w, ReportPath())
}

func report(w io.Writer, path string) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return ErrNoReport
	}
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

// Run is the service: it appends a quick diag to the report file every
// 15 s for 5 minutes after its start, then ends. Started from a terminal
// instead of the service manager, it does the same and ends early when
// ctx ends.
func Run(ctx context.Context) error {
	if !supported {
		return hid.ErrUnsupported
	}
	d := deps{
		open:    diag.SystemOpener(),
		now:     time.Now,
		path:    ReportPath(),
		session: SessionState,
		timeout: timeout,
		version: Version,
	}
	if service.Interactive() {
		return run(ctx, d)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	p := &program{ctx: ctx, cancel: cancel, d: d, done: make(chan struct{})}
	s, err := newService(p, waitFor(p.done))
	if err != nil {
		return err
	}
	p.s = s
	if err := s.Run(); err != nil {
		return err
	}
	return p.err
}

// waitFor returns the function the service manager's Run waits on before
// it stops the program on macOS: it returns once the runs are done or a
// stop signal arrives. (Windows does not use it; there endService stops
// the service.)
func waitFor(done <-chan struct{}) func() {
	return func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, os.Interrupt)
		defer signal.Stop(sig)
		select {
		case <-done:
		case <-sig:
		}
	}
}

// program is what the service manager starts and stops.
type program struct {
	ctx    context.Context
	cancel context.CancelFunc
	d      deps
	s      service.Service
	done   chan struct{} // closed when the runs are over
	err    error         // the runs' result, valid after done
}

func (p *program) Start(service.Service) error {
	go func() {
		p.err = run(p.ctx, p.d)
		close(p.done)
		endService(p.s)
	}()
	return nil
}

func (p *program) Stop(service.Service) error {
	p.cancel()
	<-p.done
	return nil
}

// deps is what run takes from the system.
type deps struct {
	open    diag.Opener
	now     func() time.Time
	sleep   func(context.Context, time.Duration) error // nil: a timer
	path    string                                     // the report file
	session func() string                              // SessionState
	timeout time.Duration                              // per request
	version string
}

// run appends a quick diag to d.path every 15 s, starting runs for 5
// minutes from its own start. A run that takes longer than 15 s is
// followed at once by the next. A failed diag is part of the measurement:
// it is written as "run failed: <error>" and the runs go on. run returns
// nil when ctx ends, and an error only when the report file cannot be
// written.
func run(ctx context.Context, d deps) error {
	sleep := d.sleep
	if sleep == nil {
		sleep = sleepTimer
	}
	start := d.now()
	end := start.Add(duration)
	for n, next := 1, start; next.Before(end); n++ {
		if wait := next.Sub(d.now()); wait > 0 {
			if err := sleep(ctx, wait); err != nil {
				return nil
			}
		}
		if ctx.Err() != nil {
			return nil
		}
		if err := appendRun(ctx, d, n, sleep); err != nil {
			return err
		}
		next = next.Add(interval)
		if now := d.now(); next.Before(now) {
			next = now
		}
	}
	return nil
}

// appendRun opens the report, appends run n and closes the file again, so
// a hard power-off loses nothing written before it.
func appendRun(ctx context.Context, d deps, n int, sleep func(context.Context, time.Duration) error) error {
	if err := os.MkdirAll(filepath.Dir(d.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(d.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	fmt.Fprintf(f, "== boot-run %d at %s, %s ==\n", n, d.now().Format(timeFormat), d.session())
	err = diag.Run(ctx, d.open, diag.Options{
		Idle:    []time.Duration{},
		Watch:   0,
		Timeout: d.timeout,
		Out:     f,
		Now:     d.now,
		Sleep:   sleep,
		Version: d.version,
		Service: true,
	})
	if err != nil {
		fmt.Fprintf(f, "run failed: %v\n", err)
	}
	_, werr := fmt.Fprintln(f)
	if err := f.Sync(); werr == nil {
		werr = err
	}
	if err := f.Close(); werr == nil {
		werr = err
	}
	return werr
}

// sleepTimer waits d or until ctx ends.
func sleepTimer(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
