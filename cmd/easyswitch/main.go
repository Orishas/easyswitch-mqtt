// Command easyswitch switches Logitech Easy-Switch devices behind a Logi
// Bolt receiver between channels and measures how they behave.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Orishas/easyswitch-mqtt/internal/bootdiag"
	"github.com/Orishas/easyswitch-mqtt/internal/diag"
	"github.com/Orishas/easyswitch-mqtt/internal/hid"
	"github.com/Orishas/easyswitch-mqtt/internal/hidpp"
	"github.com/Orishas/easyswitch-mqtt/internal/switcher"
)

// version is set by the release build with -ldflags "-X main.version=...".
var version = "dev"

const usageText = `usage:
  easyswitch diag [--idle 10s,1m,5m,30m] [--watch 2m] [--quick] [--out FILE]
  easyswitch diag --boot-install | --boot-report | --boot-remove
  easyswitch switch N
  easyswitch version

diag     measures how the receiver and its devices behave and writes a report
         --idle   idle times before each ping, comma-separated; "" for none
         --watch  how long to record what the receiver sends; 0 for none
         --quick  no idle test, no watch
         --out    report file (default: easyswitch-diag-<date>-<time>.txt here)
         --boot-install  measures again at the next start of the computer, before
                         login, as a system service (needs administrator rights)
         --boot-report   prints what that service measured
         --boot-remove   removes the service and its report
switch   moves every device to channel N (1, 2 or 3)
version  prints the version
`

// Exit codes.
const (
	exitOK    = 0
	exitFail  = 1
	exitUsage = 2
)

// maxChannel is the highest Easy-Switch channel.
const maxChannel = 3

// switchPatience is how long switch looks for devices that sleep.
const switchPatience = switcher.DefaultPatience

// defaultWatch is how long diag records what the receiver sends; the idle
// durations default inside diag.
const defaultWatch = 2 * time.Minute

// errUsage: the command line is wrong; the usage has been printed.
var errUsage = errors.New("usage")

// errNoReceiver: there is no Bolt receiver to switch with.
var errNoReceiver = errors.New("no Bolt receiver")

// deps is what run takes from the system.
type deps struct {
	open          diag.Opener
	goos          string // for the wording of an access denial
	now           func() time.Time
	switchDevices func(ctx context.Context, conn *hidpp.Conn, channel int, patience time.Duration) ([]switcher.Result, error)
	boot          bootDiag
}

// bootDiag is the system service that runs diag before login, see
// package bootdiag.
type bootDiag struct {
	install, uninstall func() error
	run                func(context.Context) error
	report             func(io.Writer) error
}

// The boot diag's messages.
const (
	msgBootInstalled   = "Installed. Restart the computer, wait one minute at the login screen while moving the mouse and pressing Shift on the keyboard, then log in and run: easyswitch diag --boot-report\nDo not move or delete this program until then."
	msgBootRemoved     = "Removed."
	msgNothingToRemove = "Nothing to remove."
	msgAdminWindows    = `Run this in a PowerShell window opened with "Run as administrator".`
	msgAdminElsewhere  = "Run this with sudo."
)

func main() {
	bootdiag.Version = version
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, deps{
		open:          diag.SystemOpener(),
		goos:          runtime.GOOS,
		now:           time.Now,
		switchDevices: switcher.Switch,
		boot: bootDiag{
			install:   bootdiag.Install,
			uninstall: bootdiag.Uninstall,
			run:       bootdiag.Run,
			report:    bootdiag.Report,
		},
	}))
}

// run executes the command line args and returns the exit code. Ctrl-C
// ends the command early.
func run(args []string, stdout, stderr io.Writer, d deps) int {
	if len(args) == 0 {
		return usage(stderr)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch cmd, rest := args[0], args[1:]; cmd {
	case "diag":
		return runDiag(ctx, rest, stdout, stderr, d)
	case "switch":
		return runSwitch(ctx, rest, stdout, stderr, d)
	case "version":
		if len(rest) != 0 {
			return usage(stderr)
		}
		fmt.Fprintf(stdout, "easyswitch %s\n", version)
		return exitOK
	}
	return usage(stderr)
}

func usage(stderr io.Writer) int {
	fmt.Fprint(stderr, usageText)
	return exitUsage
}

// diagConfig is diag's command line.
type diagConfig struct {
	opts diag.Options // Idle and Watch
	out  string       // report file, "" for the default name
}

// parseDiag reads diag's flags. Idle stays nil for the default durations;
// --quick and --idle "" make it empty, which skips the idle test.
func parseDiag(args []string, stderr io.Writer) (diagConfig, error) {
	var c diagConfig
	fs := flag.NewFlagSet("diag", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {}
	fs.Func("idle", "", func(v string) (err error) {
		c.opts.Idle, err = parseDurations(v)
		return err
	})
	fs.DurationVar(&c.opts.Watch, "watch", defaultWatch, "")
	quick := fs.Bool("quick", false, "")
	fs.StringVar(&c.out, "out", "", "")
	if err := fs.Parse(args); err != nil {
		return c, errUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "unexpected argument %q\n", fs.Arg(0))
		return c, errUsage
	}
	if *quick {
		c.opts.Idle, c.opts.Watch = []time.Duration{}, 0
	}
	return c, nil
}

// parseDurations reads a comma-separated list of positive durations; ""
// is the empty list.
func parseDurations(v string) ([]time.Duration, error) {
	out := []time.Duration{}
	if strings.TrimSpace(v) == "" {
		return out, nil
	}
	for _, f := range strings.Split(v, ",") {
		d, err := time.ParseDuration(strings.TrimSpace(f))
		if err != nil {
			return nil, err
		}
		if d <= 0 {
			return nil, fmt.Errorf("%s is not a positive duration", f)
		}
		out = append(out, d)
	}
	return out, nil
}

func runDiag(ctx context.Context, args []string, stdout, stderr io.Writer, d deps) int {
	if len(args) == 1 {
		if code, ok := runBoot(ctx, args[0], stdout, stderr, d); ok {
			return code
		}
	}
	c, err := parseDiag(args, stderr)
	if err != nil {
		return usage(stderr)
	}
	path := c.out
	if path == "" {
		path = "easyswitch-diag-" + d.now().Local().Format("20060102-150405") + ".txt"
	}
	f, err := os.Create(path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFail
	}
	o := c.opts
	o.Out, o.Progress, o.Now, o.Version = f, stderr, d.now, version
	code := exitOK
	if err := diag.Run(ctx, d.open, o); err != nil {
		code = exitFail
		if !errors.Is(err, context.Canceled) {
			fmt.Fprintln(stderr, explain(err, d.goos))
		}
	}
	if err := f.Close(); err != nil {
		fmt.Fprintln(stderr, err)
		code = exitFail
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	fmt.Fprintln(stdout, path)
	return code
}

// runBoot runs the boot diag flag arg, which must stand alone. ok is false
// when arg is no such flag.
func runBoot(ctx context.Context, arg string, stdout, stderr io.Writer, d deps) (code int, ok bool) {
	var err error
	switch strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-") {
	case "boot-install":
		if err = d.boot.install(); err == nil {
			fmt.Fprintln(stdout, msgBootInstalled)
		}
	case "boot-run":
		err = d.boot.run(ctx)
	case "boot-report":
		err = d.boot.report(stdout)
	case "boot-remove":
		err = d.boot.uninstall()
		switch {
		case err == nil:
			fmt.Fprintln(stdout, msgBootRemoved)
		case errors.Is(err, bootdiag.ErrNotInstalled):
			fmt.Fprintln(stdout, msgNothingToRemove)
			return exitOK, true
		}
	default:
		return 0, false
	}
	switch {
	case err == nil:
		return exitOK, true
	case errors.Is(err, bootdiag.ErrNotAdmin) && d.goos == "windows":
		fmt.Fprintln(stderr, msgAdminWindows)
	case errors.Is(err, bootdiag.ErrNotAdmin):
		fmt.Fprintln(stderr, msgAdminElsewhere)
	default:
		fmt.Fprintln(stderr, explain(err, d.goos))
	}
	return exitFail, true
}

func runSwitch(ctx context.Context, args []string, stdout, stderr io.Writer, d deps) int {
	if len(args) != 1 {
		return usage(stderr)
	}
	channel, err := strconv.Atoi(args[0])
	if err != nil || channel < 1 || channel > maxChannel {
		return usage(stderr)
	}
	_, receivers, open, err := d.open()
	if err != nil {
		fmt.Fprintln(stderr, explain(err, d.goos))
		return exitFail
	}
	var bolts []hid.Receiver
	for _, rx := range receivers {
		if rx.ProductID == hid.BoltPID {
			bolts = append(bolts, rx)
		}
	}
	if len(bolts) == 0 {
		fmt.Fprintln(stderr, explain(errNoReceiver, d.goos))
		return exitFail
	}

	var openErr error // the first failure to open, an access denial preferred
	opened, switched := 0, false
	for _, rx := range bolts {
		dev, err := open(rx)
		if err != nil {
			if openErr == nil || (errors.Is(err, hid.ErrAccessDenied) && !errors.Is(openErr, hid.ErrAccessDenied)) {
				openErr = err
			}
			continue
		}
		if opened == 0 {
			fmt.Fprintf(stderr, "Switching to channel %d. Looking for devices for up to %d s; move the mouse or press a key to wake a device that sleeps.\n",
				channel, int(switchPatience/time.Second))
		}
		opened++
		conn := hidpp.NewConn(dev)
		results, err := d.switchDevices(ctx, conn, channel, switchPatience)
		conn.Close()
		for _, r := range results {
			line := r.Name + ": " + r.Outcome
			if r.Outcome == "left" {
				line += fmt.Sprintf(" (was on channel %d)", r.From)
			}
			fmt.Fprintln(stdout, line)
			switched = switched || r.Outcome == "left" || r.Outcome == "already"
		}
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			fmt.Fprintln(stderr, "Interrupted.")
			return exitFail
		}
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", rx.Product, err)
		}
	}
	if opened == 0 {
		fmt.Fprintln(stderr, explain(openErr, d.goos))
		return exitFail
	}
	if !switched {
		return exitFail
	}
	return exitOK
}

// explain turns an error into the sentence the person reads.
func explain(err error, goos string) string {
	switch {
	case errors.Is(err, diag.ErrNoReceiver), errors.Is(err, errNoReceiver):
		return "No Logitech receiver found. Plug in the Logi Bolt receiver and try again."
	case errors.Is(err, hid.ErrAccessDenied) && goos == "darwin":
		return "macOS refused access to the receiver. Allow this program under System Settings > Privacy & Security > Input Monitoring, then try again."
	case errors.Is(err, hid.ErrAccessDenied):
		return "Access to the receiver was refused."
	case errors.Is(err, hid.ErrUnsupported):
		return "This platform is not supported yet."
	}
	return err.Error()
}
