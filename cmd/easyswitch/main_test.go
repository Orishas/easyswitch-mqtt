package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Orishas/easyswitch-mqtt/internal/diag"
	"github.com/Orishas/easyswitch-mqtt/internal/hid"
	"github.com/Orishas/easyswitch-mqtt/internal/hidpp"
	"github.com/Orishas/easyswitch-mqtt/internal/hidtest"
	"github.com/Orishas/easyswitch-mqtt/internal/switcher"
)

const (
	msgNoReceiver = "No Logitech receiver found. Plug in the Logi Bolt receiver and try again."
	msgMacDenied  = "macOS refused access to the receiver. Allow this program under System Settings > Privacy & Security > Input Monitoring, then try again."
	msgDenied     = "Access to the receiver was refused."
	msgNoPlatform = "This platform is not supported yet."
)

var testNow = time.Date(2026, 11, 12, 14, 3, 5, 0, time.Local)

// listing returns an opener whose listing fails with err.
func listing(err error) diag.Opener {
	return func() ([]hid.Info, []hid.Receiver, func(hid.Receiver) (hid.Device, error), error) {
		return nil, nil, nil, err
	}
}

// receivers returns an opener listing one receiver per device, opened to
// that device or failing with openErr.
func receivers(pid uint16, openErr error, devs ...hid.Device) diag.Opener {
	return func() ([]hid.Info, []hid.Receiver, func(hid.Receiver) (hid.Device, error), error) {
		var infos []hid.Info
		var rxs []hid.Receiver
		byGroup := map[string]hid.Device{}
		for i, d := range devs {
			group := "usb-" + string(rune('a'+i))
			in := hid.Info{Path: group, Group: group, Product: "USB Receiver", VendorID: hid.LogitechVID, ProductID: pid, UsagePage: 0xFF00, Usage: 2}
			infos = append(infos, in)
			rxs = append(rxs, hid.Receiver{Group: group, Product: in.Product, ProductID: pid, Interfaces: []hid.Info{in}})
			byGroup[group] = d
		}
		open := func(r hid.Receiver) (hid.Device, error) {
			if openErr != nil {
				return nil, openErr
			}
			return byGroup[r.Group], nil
		}
		return infos, rxs, open, nil
	}
}

// noSwitch fails a test that reaches the switcher.
func noSwitch(t *testing.T) func(context.Context, *hidpp.Conn, int, time.Duration) ([]switcher.Result, error) {
	return func(context.Context, *hidpp.Conn, int, time.Duration) ([]switcher.Result, error) {
		t.Error("switcher called")
		return nil, nil
	}
}

func testDeps(t *testing.T, open diag.Opener, goos string) deps {
	return deps{open: open, goos: goos, now: func() time.Time { return testNow }, switchDevices: noSwitch(t)}
}

func runCmd(d deps, args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = run(args, &out, &errOut, d)
	return code, out.String(), errOut.String()
}

func TestNoReceiver(t *testing.T) {
	for _, open := range []diag.Opener{
		receivers(hid.BoltPID, nil),
		receivers(0xC52B, nil, hidtest.NewReceiver(nil)), // a Unifying receiver is no Bolt receiver
	} {
		code, stdout, stderr := runCmd(testDeps(t, open, "windows"), "switch", "2")
		if code != 1 || stderr != msgNoReceiver+"\n" || stdout != "" {
			t.Errorf("switch: exit %d, stdout %q, stderr %q; want 1, nothing, %q", code, stdout, stderr, msgNoReceiver)
		}
	}

	out := filepath.Join(t.TempDir(), "report.txt")
	code, stdout, stderr := runCmd(testDeps(t, receivers(hid.BoltPID, nil), "windows"), "diag", "--quick", "--out", out)
	if code != 1 || lastLine(stderr) != msgNoReceiver {
		t.Errorf("diag: exit %d, stderr %q; want 1 and last line %q", code, stderr, msgNoReceiver)
	}
	if stdout != out+"\n" {
		t.Errorf("diag: stdout %q, want the report path %q", stdout, out)
	}
	if b, err := os.ReadFile(out); err != nil || !strings.Contains(string(b), "== interfaces ==") {
		t.Errorf("report %q, %v; want the interfaces section", b, err)
	}
}

func TestAccessDenied(t *testing.T) {
	for _, tc := range []struct {
		goos, want string
		open       diag.Opener
	}{
		{"darwin", msgMacDenied, listing(hid.ErrAccessDenied)},
		{"darwin", msgMacDenied, receivers(hid.BoltPID, hid.ErrAccessDenied, hidtest.NewReceiver(nil))},
		{"windows", msgDenied, listing(hid.ErrAccessDenied)},
		{"windows", msgDenied, receivers(hid.BoltPID, hid.ErrAccessDenied, hidtest.NewReceiver(nil))},
	} {
		code, stdout, stderr := runCmd(testDeps(t, tc.open, tc.goos), "switch", "1")
		if code != 1 || stderr != tc.want+"\n" || stdout != "" {
			t.Errorf("%s switch: exit %d, stdout %q, stderr %q; want 1, nothing, %q", tc.goos, code, stdout, stderr, tc.want)
		}
		out := filepath.Join(t.TempDir(), "report.txt")
		code, _, stderr = runCmd(testDeps(t, tc.open, tc.goos), "diag", "--quick", "--out", out)
		if code != 1 || lastLine(stderr) != tc.want {
			t.Errorf("%s diag: exit %d, stderr %q; want 1 and last line %q", tc.goos, code, stderr, tc.want)
		}
	}
}

func TestUnsupported(t *testing.T) {
	code, _, stderr := runCmd(testDeps(t, listing(hid.ErrUnsupported), "linux"), "switch", "1")
	if code != 1 || stderr != msgNoPlatform+"\n" {
		t.Errorf("switch: exit %d, stderr %q; want 1, %q", code, stderr, msgNoPlatform)
	}
	out := filepath.Join(t.TempDir(), "report.txt")
	code, _, stderr = runCmd(testDeps(t, listing(hid.ErrUnsupported), "linux"), "diag", "--quick", "--out", out)
	if code != 1 || lastLine(stderr) != msgNoPlatform {
		t.Errorf("diag: exit %d, stderr %q; want 1 and last line %q", code, stderr, msgNoPlatform)
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"frobnicate"},
		{"switch"},
		{"switch", "0"},
		{"switch", "x"},
		{"switch", "4"},
		{"switch", "1", "2"},
		{"diag", "--idle", "soon"},
		{"diag", "--idle", "10s,-1m"},
		{"diag", "--watch", "x"},
		{"diag", "extra"},
		{"version", "extra"},
	} {
		code, stdout, stderr := runCmd(testDeps(t, listing(errors.New("listed")), "windows"), args...)
		if code != 2 || !strings.Contains(stderr, "easyswitch switch N") || stdout != "" {
			t.Errorf("%q: exit %d, stdout %q, stderr %q; want 2 and the usage on stderr", args, code, stdout, stderr)
		}
	}
}

func TestVersion(t *testing.T) {
	code, stdout, _ := runCmd(testDeps(t, nil, "windows"), "version")
	if code != 0 || stdout != "easyswitch dev\n" {
		t.Errorf("exit %d, stdout %q; want 0, easyswitch dev", code, stdout)
	}
}

// The options diag gets: nil Idle means the default durations, an empty
// one no idle test.
func TestDiagOptions(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		idle  []time.Duration
		watch time.Duration
		out   string
	}{
		{nil, nil, 2 * time.Minute, ""},
		{[]string{"--quick"}, []time.Duration{}, 0, ""},
		{[]string{"--idle", ""}, []time.Duration{}, 2 * time.Minute, ""},
		{[]string{"--idle", "10s,1m"}, []time.Duration{10 * time.Second, time.Minute}, 2 * time.Minute, ""},
		{[]string{"--watch", "30s", "--out", "r.txt"}, nil, 30 * time.Second, "r.txt"},
		{[]string{"--quick", "--watch", "2m", "--idle", "1m"}, []time.Duration{}, 0, ""},
	} {
		got, err := parseDiag(tc.args, &bytes.Buffer{})
		if err != nil {
			t.Errorf("%q: %v", tc.args, err)
			continue
		}
		if !reflect.DeepEqual(got.opts.Idle, tc.idle) { // tells nil from empty
			t.Errorf("%q: Idle = %#v, want %#v", tc.args, got.opts.Idle, tc.idle)
		}
		if got.opts.Watch != tc.watch || got.out != tc.out {
			t.Errorf("%q: Watch %v, out %q; want %v, %q", tc.args, got.opts.Watch, got.out, tc.watch, tc.out)
		}
	}
}

// Without --out the report goes to a file named after the local time in
// the current directory; its path is printed last.
func TestDiagDefaultReportFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	code, stdout, _ := runCmd(testDeps(t, receivers(hid.BoltPID, nil), "windows"), "diag", "--quick")
	if code != 1 {
		t.Errorf("exit %d, want 1 (no receiver)", code)
	}
	want, err := filepath.Abs("easyswitch-diag-20261112-140305.txt")
	if err != nil {
		t.Fatal(err)
	}
	if stdout != want+"\n" {
		t.Errorf("stdout %q, want %q", stdout, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Error(err)
	}
}

// Ctrl-C during diag ends the run with the report flushed and its path
// printed last.
func TestDiagInterrupt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a process cannot send itself an interrupt on Windows")
	}
	out := filepath.Join(t.TempDir(), "report.txt")
	d := testDeps(t, receivers(hid.BoltPID, nil, hidtest.NewReceiver(nil)), "windows")
	timer := time.AfterFunc(200*time.Millisecond, func() {
		p, err := os.FindProcess(os.Getpid())
		if err == nil {
			err = p.Signal(os.Interrupt)
		}
		if err != nil {
			t.Error(err)
		}
	})
	defer timer.Stop()
	code, stdout, _ := runCmd(d, "diag", "--quick", "--out", out) // first waits 5 s for the devices to wake
	if code != 1 || stdout != out+"\n" {
		t.Errorf("exit %d, stdout %q; want 1 and the report path", code, stdout)
	}
	b, err := os.ReadFile(out)
	if err != nil || !strings.HasSuffix(string(b), "== interrupted ==\n") {
		t.Errorf("report %q, %v; want it to end with == interrupted ==", b, err)
	}
}

func TestSwitchResults(t *testing.T) {
	const looking = "Switching to channel 3. Looking for devices for up to 8 s; move the mouse or press a key to wake a device that sleeps.\n"
	for _, tc := range []struct {
		name    string
		results []switcher.Result
		err     error
		code    int
		stdout  string
		stderr  string
	}{
		{
			name: "left and already",
			results: []switcher.Result{
				{Name: "MX Keys S", Slot: 1, From: 1, Outcome: "left"},
				{Name: "MX Master 4", Slot: 2, From: 2, Outcome: "already"},
			},
			code:   0,
			stderr: looking,
			stdout: "MX Keys S: left (was on channel 1)\nMX Master 4: already\n",
		},
		{
			name: "nothing switched",
			results: []switcher.Result{
				{Name: "MX Keys S", Slot: 1, Outcome: "asleep"},
				{Name: "MX Master 4", Slot: 2, From: 1, Outcome: "error: device has 3 channels"},
			},
			code:   1,
			stderr: looking,
			stdout: "MX Keys S: asleep\nMX Master 4: error: device has 3 channels\n",
		},
		{
			name:    "interrupted",
			results: []switcher.Result{{Name: "MX Keys S", Slot: 1, From: 1, Outcome: "left"}},
			err:     context.Canceled,
			code:    1,
			stdout:  "MX Keys S: left (was on channel 1)\n",
			stderr:  looking + "Interrupted.\n",
		},
		{
			name:   "no device answered",
			err:    errors.New("no device answered"),
			code:   1,
			stderr: looking + "USB Receiver: no device answered\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := testDeps(t, receivers(hid.BoltPID, nil, hidtest.NewReceiver(nil)), "windows")
			var gotChannel int
			var gotPatience time.Duration
			d.switchDevices = func(_ context.Context, conn *hidpp.Conn, channel int, patience time.Duration) ([]switcher.Result, error) {
				gotChannel, gotPatience = channel, patience
				return tc.results, tc.err
			}
			code, stdout, stderr := runCmd(d, "switch", "3")
			if code != tc.code || stdout != tc.stdout || stderr != tc.stderr {
				t.Errorf("exit %d, stdout %q, stderr %q; want %d, %q, %q", code, stdout, stderr, tc.code, tc.stdout, tc.stderr)
			}
			if gotChannel != 3 || gotPatience != 8*time.Second {
				t.Errorf("Switch(channel %d, patience %v), want 3, 8s", gotChannel, gotPatience)
			}
		})
	}
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return lines[len(lines)-1]
}
