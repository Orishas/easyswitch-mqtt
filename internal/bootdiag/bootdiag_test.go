package bootdiag

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Orishas/easyswitch-mqtt/internal/diag"
	"github.com/Orishas/easyswitch-mqtt/internal/hid"
	"github.com/Orishas/easyswitch-mqtt/internal/hidpp"
	"github.com/Orishas/easyswitch-mqtt/internal/hidtest"
)

// clock is a fake clock whose Sleep returns at once, advancing the time.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
	return nil
}

// boltOpener lists one Bolt receiver and opens it to a fresh fake with one
// awake device each time, as every run closes what it opened.
func boltOpener() diag.Opener {
	info := hid.Info{
		Path: "usb-0-col01", Group: "usb-0", Product: "USB Receiver",
		VendorID: hid.LogitechVID, ProductID: hid.BoltPID,
		UsagePage: 0xFF00, Usage: 0x0002, InLen: 20, OutLen: 20,
	}
	rx := hid.Receiver{Group: info.Group, Product: info.Product, ProductID: info.ProductID, Interfaces: []hid.Info{info}}
	open := func(hid.Receiver) (hid.Device, error) {
		return hidtest.NewReceiver(map[byte]*hidtest.Slot{
			1: {
				Name:     "MX Keys S",
				Features: map[uint16]byte{hidpp.FeatDeviceName: 0x03, hidpp.FeatChangeHost: 0x0a},
				Hosts:    3,
				Awake:    true,
			},
		}), nil
	}
	return func() ([]hid.Info, []hid.Receiver, func(hid.Receiver) (hid.Device, error), error) {
		return []hid.Info{info}, []hid.Receiver{rx}, open, nil
	}
}

func testDeps(c *clock, path string, open diag.Opener) deps {
	return deps{
		open:    open,
		now:     c.Now,
		sleep:   c.Sleep,
		path:    path,
		session: func() string { return "before login" },
		timeout: 10 * time.Millisecond, // slots 2 to 6 are empty and time out
		version: "test",
	}
}

func TestRunAppends(t *testing.T) {
	start := time.Date(2026, 11, 12, 7, 0, 0, 0, time.UTC)
	c := &clock{now: start}
	path := filepath.Join(t.TempDir(), "easyswitch", "boot-diag.txt")
	if err := run(context.Background(), testDeps(c, path, boltOpener())); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := c.Now().Sub(start); got < 5*time.Minute-interval || got > 5*time.Minute {
		t.Errorf("run took %v of fake time, want about 5 minutes", got)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	runs := strings.Split(string(b), "== boot-run ")[1:]
	if len(runs) != 20 {
		t.Fatalf("%d boot-run headers, want 20:\n%s", len(runs), b)
	}
	for i, r := range runs {
		header, _, _ := strings.Cut(r, "\n")
		at := start.Add(time.Duration(i) * interval).Format(timeFormat)
		if want := strconv.Itoa(i+1) + " at " + at + ", before login =="; header != want {
			t.Errorf("header %d = %q, want %q", i+1, "== boot-run "+header, "== boot-run "+want)
		}
		if !strings.Contains(r, "\n-- slots --\n") || !strings.Contains(r, "MX Keys S") {
			t.Errorf("run %d has no slots section with the device:\n%s", i+1, r)
		}
		if !strings.Contains(r, "running as a service: yes") {
			t.Errorf("run %d does not say it runs as a service", i+1)
		}
		if !strings.Contains(r, "-- idle --\nskipped\n") || !strings.Contains(r, "-- watch --\nskipped\n") {
			t.Errorf("run %d is not quick:\n%s", i+1, r)
		}
	}
}

func TestRunSlowRuns(t *testing.T) {
	start := time.Date(2026, 11, 12, 7, 0, 0, 0, time.UTC)
	c := &clock{now: start}
	path := filepath.Join(t.TempDir(), "boot-diag.txt")
	slow := func() ([]hid.Info, []hid.Receiver, func(hid.Receiver) (hid.Device, error), error) {
		c.Sleep(context.Background(), 40*time.Second) // listing takes 40 s, the wake wait 5 s more
		return boltOpener()()
	}
	if err := run(context.Background(), testDeps(c, path, slow)); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	var got []string
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "== boot-run ") {
			got = append(got, line)
		}
	}
	var want []string
	for i := 0; i < 7; i++ { // a run every 45 s, started within the 5 minutes
		at := start.Add(time.Duration(i) * 45 * time.Second).Format(timeFormat)
		want = append(want, "== boot-run "+strconv.Itoa(i+1)+" at "+at+", before login ==")
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("headers:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestRunWritesFailure(t *testing.T) {
	c := &clock{now: time.Date(2026, 11, 12, 7, 0, 0, 0, time.UTC)}
	path := filepath.Join(t.TempDir(), "boot-diag.txt")
	denied := func() ([]hid.Info, []hid.Receiver, func(hid.Receiver) (hid.Device, error), error) {
		return nil, nil, nil, hid.ErrAccessDenied
	}
	if err := run(context.Background(), testDeps(c, path, denied)); err != nil {
		t.Fatalf("run: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(b), "== boot-run "); n != 20 {
		t.Errorf("%d runs, want 20 even though every run fails", n)
	}
	if n := strings.Count(string(b), "\nrun failed: diag: list HID interfaces: "+hid.ErrAccessDenied.Error()+"\n"); n != 20 {
		t.Errorf("%d run failed lines with the error, want 20:\n%s", n, b)
	}
}

func TestRunAppendsToEarlierBoot(t *testing.T) {
	c := &clock{now: time.Date(2026, 11, 12, 7, 0, 0, 0, time.UTC)}
	path := filepath.Join(t.TempDir(), "boot-diag.txt")
	if err := os.WriteFile(path, []byte("earlier boot\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), testDeps(c, path, boltOpener())); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(b), "earlier boot\n") || strings.Count(string(b), "== boot-run 1 at") != 1 {
		t.Errorf("the earlier boot was not kept:\n%.300s", b)
	}
}

func TestRunStops(t *testing.T) {
	c := &clock{now: time.Date(2026, 11, 12, 7, 0, 0, 0, time.UTC)}
	path := filepath.Join(t.TempDir(), "boot-diag.txt")
	ctx, cancel := context.WithCancel(context.Background())
	d := testDeps(c, path, boltOpener())
	runs := 0
	d.session = func() string {
		runs++
		if runs == 3 {
			cancel()
		}
		return "logged in: alice"
	}
	if err := run(ctx, d); err != nil {
		t.Fatalf("run: %v, want nil when stopped", err)
	}
	b, _ := os.ReadFile(path)
	if n := strings.Count(string(b), "== boot-run "); n != 3 {
		t.Errorf("%d runs, want 3", n)
	}
	if !strings.Contains(string(b), "== boot-run 3 at 2026-11-12 07:00:30 +00:00, logged in: alice ==") {
		t.Errorf("no third header:\n%s", b)
	}
}

func TestReportMissingFile(t *testing.T) {
	var out bytes.Buffer
	err := report(&out, filepath.Join(t.TempDir(), "boot-diag.txt"))
	if err == nil || err.Error() != "No boot report yet. Did the computer restart after --boot-install?" {
		t.Errorf("report: %v", err)
	}
	if !errors.Is(err, ErrNoReport) {
		t.Errorf("report: %v, want ErrNoReport", err)
	}
	if out.Len() != 0 {
		t.Errorf("printed %q", out.String())
	}
}

func TestReportPrints(t *testing.T) {
	path := filepath.Join(t.TempDir(), "boot-diag.txt")
	want := "== boot-run 1 at x, before login ==\nhello\n"
	if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := report(&out, path); err != nil || out.String() != want {
		t.Errorf("report: %q, %v; want %q", out.String(), err, want)
	}
}
