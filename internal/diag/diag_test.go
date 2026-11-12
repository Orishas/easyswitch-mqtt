package diag_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Orishas/easyswitch-mqtt/internal/diag"
	"github.com/Orishas/easyswitch-mqtt/internal/hid"
	"github.com/Orishas/easyswitch-mqtt/internal/hidpp"
	"github.com/Orishas/easyswitch-mqtt/internal/hidtest"
)

const (
	keysChangeHost   byte = 0x0a
	masterChangeHost byte = 0x0e
	nameIdx          byte = 0x03
	hostsInfoIdx     byte = 0x0c
	wakeWait              = 5 * time.Second // the wait after "move the mouse and press a key"
)

// newBolt returns a fake Bolt receiver serving boltSlots.
func newBolt() *hidtest.Receiver { return hidtest.NewReceiver(boltSlots()) }

// boltSlots are "MX Keys S" at slot 1 and "MX Master 4" at slot 2, both
// awake on channel 1 of 3.
func boltSlots() map[byte]*hidtest.Slot {
	return map[byte]*hidtest.Slot{
		1: {
			Name:     "MX Keys S",
			Features: map[uint16]byte{hidpp.FeatDeviceName: nameIdx, hidpp.FeatChangeHost: keysChangeHost},
			Hosts:    3,
			Awake:    true,
		},
		2: {
			Name:     "MX Master 4",
			Features: map[uint16]byte{hidpp.FeatDeviceName: nameIdx, hidpp.FeatChangeHost: masterChangeHost},
			Hosts:    3,
			Awake:    true,
		},
	}
}

// fakeRx is one receiver the fake opener offers.
type fakeRx struct {
	product string
	pid     uint16
	dev     hid.Device
	openErr error
}

// opener lists one vendor-defined interface per receiver and opens each
// to its fake device.
func opener(rxs ...fakeRx) diag.Opener {
	return func() ([]hid.Info, []hid.Receiver, func(hid.Receiver) (hid.Device, error), error) {
		var infos []hid.Info
		byGroup := map[string]fakeRx{}
		for i, rx := range rxs {
			group := fmt.Sprintf("usb-%d", i)
			byGroup[group] = rx
			infos = append(infos, hid.Info{
				Path: group + "-col01", Group: group, Product: rx.product,
				VendorID: hid.LogitechVID, ProductID: rx.pid,
				UsagePage: 0xFF00, Usage: 0x0002, InLen: 20, OutLen: 20,
			})
		}
		open := func(r hid.Receiver) (hid.Device, error) {
			rx := byGroup[r.Group]
			if rx.openErr != nil {
				return nil, rx.openErr
			}
			return rx.dev, nil
		}
		return infos, hid.GroupReceivers(infos), open, nil
	}
}

func bolt(dev hid.Device) fakeRx {
	return fakeRx{product: "USB Receiver", pid: hid.BoltPID, dev: dev}
}

// clock is a fake clock whose Sleep returns at once, advancing the time.
// hook, if set, runs at the start of every Sleep.
type clock struct {
	mu   sync.Mutex
	now  time.Time
	hook func(ctx context.Context, d time.Duration)
}

func newClock() *clock {
	return &clock{now: time.Date(2026, 11, 12, 14, 3, 5, 0, time.UTC)}
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// advance moves the fake time on, for a hook that needs time to pass
// before it acts.
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func (c *clock) Sleep(ctx context.Context, d time.Duration) error {
	if c.hook != nil {
		c.hook(ctx, d)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
	return nil
}

// options returns fast options writing to out and progress.
func options(c *clock, out, progress *bytes.Buffer, idle ...time.Duration) diag.Options {
	if idle == nil {
		idle = []time.Duration{}
	}
	return diag.Options{
		Idle:     idle,
		Timeout:  50 * time.Millisecond,
		Out:      out,
		Progress: progress,
		Now:      c.Now,
		Sleep:    c.Sleep,
		Version:  "1.2.3",
	}
}

// block returns the lines under header up to the next header ("== " or "-- ").
func block(t *testing.T, text, header string) []string {
	t.Helper()
	i := strings.Index(text, header+"\n")
	if i < 0 {
		t.Fatalf("no %q in report:\n%s", header, text)
	}
	var lines []string
	for _, l := range strings.Split(text[i+len(header)+1:], "\n") {
		if strings.HasPrefix(l, "== ") || strings.HasPrefix(l, "-- ") {
			break
		}
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// receivers splits the report into its receiver sections.
func receivers(text string) []string {
	parts := strings.Split(text, "\n== receiver ")[1:]
	for i, p := range parts {
		if j := strings.Index(p, "\n== unsolicited =="); j >= 0 {
			parts[i] = p[:j]
		}
		parts[i] = "== receiver " + parts[i]
	}
	return parts
}

// slotLines returns the lines starting with "slot ".
func slotLines(lines []string) []string {
	var out []string
	for _, l := range lines {
		if strings.HasPrefix(l, "slot ") {
			out = append(out, l)
		}
	}
	return out
}

func mustContain(t *testing.T, s, want string) {
	t.Helper()
	if !strings.Contains(s, want) {
		t.Errorf("missing %q in:\n%s", want, s)
	}
}

func TestReportSlots(t *testing.T) {
	slots := boltSlots()
	slots[3] = &hidtest.Slot{Name: "Lost Mouse", Offline: true}
	fake := hidtest.NewReceiver(slots)
	var out, progress bytes.Buffer
	c := newClock()
	if err := diag.Run(context.Background(), opener(bolt(fake)), options(c, &out, &progress)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := slotLines(block(t, out.String(), "-- slots --"))
	if len(got) != 6 {
		t.Fatalf("slots has %d slot lines, want 6:\n%s", len(got), strings.Join(got, "\n"))
	}
	mustContain(t, out.String(), `"timeout" means no answer within 50ms;`)
	for i, want := range [][]string{
		{"slot 1:", "Easy-Switch (0x1814) at feature index 0x0a", `name "MX Keys S"`, "3 channels", "current channel 1"},
		{"slot 2:", "Easy-Switch (0x1814) at feature index 0x0e", `name "MX Master 4"`, "3 channels", "current channel 1"},
		{"slot 3:", "error 0x09 (hid++1) after "},
		{"slot 4: timeout"},
		{"slot 5: timeout"},
		{"slot 6: timeout"},
	} {
		for _, w := range want {
			mustContain(t, got[i], w)
		}
		if i < 2 && !strings.Contains(got[i], " ms") {
			t.Errorf("slot %d line has no latency: %s", i+1, got[i])
		}
	}
}

func TestIdleDistinguishesTimeoutAndError(t *testing.T) {
	fake := newBolt()
	var out, progress bytes.Buffer
	c := newClock()
	c.hook = func(_ context.Context, d time.Duration) {
		if d == 10*time.Second { // the idle wait: the devices change while nobody touches them
			fake.Update(1, func(s *hidtest.Slot) { s.Awake = false })
			fake.Update(2, func(s *hidtest.Slot) { s.Offline = true })
		}
		if d == wakeWait && progressSays(&progress, "move the mouse and press a key") > 1 {
			fake.Update(1, func(s *hidtest.Slot) { s.Awake = true })
			fake.Update(2, func(s *hidtest.Slot) { s.Offline = false })
		}
	}
	if err := diag.Run(context.Background(), opener(bolt(fake)), options(c, &out, &progress, 10*time.Second)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	idle := strings.Join(block(t, out.String(), "-- idle --"), "\n")
	mustContain(t, idle, "after 10 s idle, slot 1 (MX Keys S): timeout")
	mustContain(t, idle, "after 10 s idle, slot 2 (MX Master 4): error 0x09 (hid++1) after ")
	for _, l := range strings.Split(idle, "\n") {
		if strings.HasPrefix(l, "after-wake") && !strings.HasSuffix(l, " ms") {
			t.Errorf("after-wake line has no latency: %s", l)
		}
	}
	mustContain(t, idle, "after-wake, slot 1 (MX Keys S): ")
	mustContain(t, idle, "after-wake, slot 2 (MX Master 4): ")
	mustContain(t, progress.String(), "Do not touch the mouse or the keyboard for 10 s.")
}

// progressSays counts how often the progress output contains s, case ignored.
func progressSays(progress *bytes.Buffer, s string) int {
	return strings.Count(strings.ToLower(progress.String()), strings.ToLower(s))
}

func TestTwoReceiversAndForeignDevice(t *testing.T) {
	unifying := hidtest.NewReceiver(map[byte]*hidtest.Slot{}) // nothing ever answers
	var out, progress bytes.Buffer
	c := newClock()
	o := options(c, &out, &progress, 10*time.Second)
	o.Watch = time.Minute
	err := diag.Run(context.Background(), opener(bolt(newBolt()), fakeRx{product: "USB Receiver", pid: 0xC52B, dev: unifying}), o)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	rs := receivers(out.String())
	if len(rs) != 2 {
		t.Fatalf("%d receiver sections, want 2:\n%s", len(rs), out.String())
	}
	mustContain(t, rs[0], "== receiver USB Receiver pid=0xC548 ==")
	mustContain(t, rs[1], "== receiver USB Receiver pid=0xC52B ==")
	got := slotLines(block(t, rs[1], "-- slots --"))
	if len(got) != 6 {
		t.Fatalf("second receiver has %d slot lines, want 6", len(got))
	}
	for i, l := range got {
		if want := fmt.Sprintf("slot %d: timeout", i+1); l != want {
			t.Errorf("line %d = %q, want %q", i, l, want)
		}
	}
}

func TestCancelFlushes(t *testing.T) {
	fake := newBolt()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out, progress bytes.Buffer
	c := newClock()
	c.hook = func(_ context.Context, d time.Duration) {
		if d == 10*time.Second {
			cancel()
		}
	}
	err := diag.Run(ctx, opener(bolt(fake)), options(c, &out, &progress, 10*time.Second, time.Minute))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
	text := out.String()
	if got := slotLines(block(t, text, "-- slots --")); len(got) != 6 {
		t.Errorf("slots has %d slot lines, want 6", len(got))
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if last := lines[len(lines)-1]; last != "== interrupted ==" {
		t.Errorf("last line = %q, want == interrupted ==\n%s", last, text)
	}
	mustContain(t, text, "== unsolicited ==")
}

func TestUnplugMidRun(t *testing.T) {
	first, second := newBolt(), newBolt()
	var out, progress bytes.Buffer
	c := newClock()
	var once sync.Once
	c.hook = func(_ context.Context, d time.Duration) {
		if d == 10*time.Second {
			once.Do(first.Unplug)
		}
	}
	o := options(c, &out, &progress, 10*time.Second)
	if err := diag.Run(context.Background(), opener(bolt(first), bolt(second)), o); err != nil {
		t.Fatalf("Run: %v", err)
	}
	rs := receivers(out.String())
	if len(rs) != 2 {
		t.Fatalf("%d receiver sections, want 2:\n%s", len(rs), out.String())
	}
	lines := strings.Split(strings.TrimRight(rs[0], "\n"), "\n")
	if last := lines[len(lines)-1]; last != "receiver lost: hidtest: receiver unplugged" {
		t.Errorf("first receiver ends with %q\n%s", last, rs[0])
	}
	got := slotLines(block(t, rs[1], "-- slots --"))
	if len(got) != 6 || !strings.Contains(got[0], `name "MX Keys S"`) {
		t.Errorf("second receiver not probed:\n%s", rs[1])
	}
	mustContain(t, rs[1], "after-wake, slot 2 (MX Master 4): ")
}

func TestUnsolicitedLogged(t *testing.T) {
	fake := newBolt()
	connect := []byte{0x10, 0x02, 0x41, 0x04, 0x01, 0x82, 0x40}
	foreign := make([]byte, 20) // another program's reply to its own root getFeature
	copy(foreign, []byte{0x11, 0x01, 0x00, 0x01, 0x0a})
	fake.Inject(connect)
	fake.Inject(foreign)
	var out, progress bytes.Buffer
	c := newClock()
	if err := diag.Run(context.Background(), opener(bolt(fake)), options(c, &out, &progress)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := block(t, out.String(), "== unsolicited ==")
	var frames []string
	for _, l := range got {
		if strings.HasPrefix(l, "+") {
			frames = append(frames, l)
		}
	}
	// the offsets depend on when the reader took the frames: compare without them
	for i, f := range frames {
		frames[i] = f[strings.Index(f, " ")+1:]
	}
	want := []string{
		"[receiver 1] 10 02 41 04 01 82 40",
		"[receiver 1] " + hexs(foreign),
	}
	if strings.Join(frames, "\n") != strings.Join(want, "\n") {
		t.Errorf("unsolicited frames:\n%s\nwant:\n%s", strings.Join(frames, "\n"), strings.Join(want, "\n"))
	}
}

func hexs(b []byte) string { return fmt.Sprintf("% x", b) }

func TestHostsInfo(t *testing.T) {
	fake := newBolt()
	fake.Update(1, func(s *hidtest.Slot) { s.Features[hidpp.FeatHostsInfo] = hostsInfoIdx })
	var out, progress bytes.Buffer
	c := newClock()
	if err := diag.Run(context.Background(), opener(bolt(fake)), options(c, &out, &progress)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := block(t, out.String(), "-- hosts-info --")
	text := strings.Join(got, "\n")
	mustContain(t, text, "slot 1 (MX Keys S): feature 0x1815 at index 0x0c")
	mustContain(t, text, "slot 2 (MX Master 4): no feature 0x1815")
	params := hexs(hidtest.HostsInfoParams)
	for _, label := range []string{"function 0", "function 1, params 00", "function 1, params 01", "function 1, params 02"} {
		found := false
		for _, l := range got {
			if strings.HasPrefix(strings.TrimSpace(l), label+":") {
				found = true
				// 11 01 0c <fn|sw> then the parameters
				if !strings.HasPrefix(strings.TrimSpace(strings.SplitN(l, ": ", 2)[1]), "11 01 0c ") || !strings.Contains(l, params) {
					t.Errorf("%s: %s", label, l)
				}
			}
		}
		if !found {
			t.Errorf("no %q line in:\n%s", label, text)
		}
	}
}

// syncDevice counts the Reads its reader started, so a test knows when
// an injected frame has been taken and passed on.
type syncDevice struct {
	*hidtest.Receiver
	reads atomic.Int32
}

func (d *syncDevice) Read(ctx context.Context) ([]byte, error) {
	d.reads.Add(1)
	return d.Receiver.Read(ctx)
}

// injectAndWait injects frame and returns once the reader has passed it on.
func injectAndWait(t *testing.T, d *syncDevice, frame []byte) {
	before := d.reads.Load()
	d.Inject(frame)
	deadline := time.Now().Add(2 * time.Second)
	for d.reads.Load() == before {
		if time.Now().After(deadline) {
			t.Error("injected frame was never read")
			return
		}
		time.Sleep(time.Millisecond)
	}
}

func TestWatch(t *testing.T) {
	dev := &syncDevice{Receiver: newBolt()}
	sleep := []byte{0x10, 0x01, 0x41, 0x04, 0x41, 0x83, 0x40}
	var out, progress bytes.Buffer
	c := newClock()
	watching := false
	c.hook = func(_ context.Context, d time.Duration) {
		if d == 20*time.Second {
			watching = true
			return
		}
		if watching && d == time.Minute { // first minute after the Easy-Switch request
			injectAndWait(t, dev, sleep)
			watching = false
		}
	}
	o := options(c, &out, &progress)
	o.Watch = 2 * time.Minute
	if err := diag.Run(context.Background(), opener(bolt(dev)), o); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := block(t, out.String(), "-- watch --")
	want := []string{
		"+0.000 [asked: press a key]",
		"+20.000 [asked: switch to another channel and back]",
		"+20.000 10 01 41 04 41 83 40",
	}
	var frames []string
	for _, l := range got {
		if strings.HasPrefix(l, "+") {
			frames = append(frames, l)
		}
	}
	if strings.Join(frames, "\n") != strings.Join(want, "\n") {
		t.Errorf("watch:\n%s\nwant:\n%s", strings.Join(frames, "\n"), strings.Join(want, "\n"))
	}
	p := progress.String()
	mustContain(t, p, "Press a key on the keyboard now.")
	mustContain(t, p, "Easy-Switch")
	// the frame was seen during the run, so it is also unsolicited
	mustContain(t, strings.Join(block(t, out.String(), "== unsolicited =="), "\n"), "10 01 41 04 41 83 40")
}

func TestLongWaitCountsDown(t *testing.T) {
	var out, progress bytes.Buffer
	c := newClock()
	var longest time.Duration
	c.hook = func(_ context.Context, d time.Duration) { longest = max(longest, d) }
	if err := diag.Run(context.Background(), opener(bolt(newBolt())), options(c, &out, &progress, 5*time.Minute)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if longest > time.Minute {
		t.Errorf("one Sleep lasted %v; the person must see a sign of life every minute", longest)
	}
	for _, left := range []string{"4 min left", "3 min left", "2 min left", "1 min left"} {
		mustContain(t, progress.String(), left)
	}
}

func TestSystemAndInterfaces(t *testing.T) {
	var out, progress bytes.Buffer
	c := newClock()
	o := options(c, &out, &progress)
	o.Service = true
	if err := diag.Run(context.Background(), opener(bolt(newBolt())), o); err != nil {
		t.Fatalf("Run: %v", err)
	}
	text := out.String()
	if !strings.HasPrefix(text, "== system ==\n") {
		t.Errorf("report does not start with the system section:\n%s", text)
	}
	sys := strings.Join(block(t, text, "== system =="), "\n")
	for _, want := range []string{
		"easyswitch version: 1.2.3",
		"operating system: " + runtime.GOOS,
		"architecture: " + runtime.GOARCH,
		"running as a service: yes",
		"time: 2026-11-12 14:03:05 +00:00",
	} {
		mustContain(t, sys, want)
	}
	ifs := strings.Join(block(t, text, "== interfaces =="), "\n")
	mustContain(t, ifs, `usage FF00:0002 pid=0xC548 in=20 out=20 product="USB Receiver"`)
	order := []string{"== system ==", "== interfaces ==", "== receiver ", "-- slots --", "-- hosts-info --", "-- idle --", "-- watch --", "== unsolicited =="}
	last := -1
	for _, h := range order {
		i := strings.Index(text, h)
		if i <= last {
			t.Errorf("%q out of order", h)
		}
		last = i
	}
}

func TestNoReceiver(t *testing.T) {
	var out, progress bytes.Buffer
	c := newClock()
	err := diag.Run(context.Background(), opener(), options(c, &out, &progress))
	if !errors.Is(err, diag.ErrNoReceiver) {
		t.Fatalf("Run = %v, want ErrNoReceiver", err)
	}
	text := out.String()
	mustContain(t, text, "== system ==")
	mustContain(t, text, "== interfaces ==")
	if strings.Contains(text, "== receiver") {
		t.Errorf("receiver section without a receiver:\n%s", text)
	}
}

func TestEnumerateAccessDenied(t *testing.T) {
	var out, progress bytes.Buffer
	c := newClock()
	denied := func() ([]hid.Info, []hid.Receiver, func(hid.Receiver) (hid.Device, error), error) {
		return nil, nil, nil, hid.ErrAccessDenied
	}
	err := diag.Run(context.Background(), denied, options(c, &out, &progress))
	if !errors.Is(err, hid.ErrAccessDenied) {
		t.Fatalf("Run = %v, want hid.ErrAccessDenied", err)
	}
	mustContain(t, out.String(), "listing failed: hid: access denied")
}

func TestOpenFailedContinues(t *testing.T) {
	var out, progress bytes.Buffer
	c := newClock()
	broken := fakeRx{product: "USB Receiver", pid: hid.BoltPID, openErr: errors.New("busy")}
	if err := diag.Run(context.Background(), opener(broken, bolt(newBolt())), options(c, &out, &progress)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	rs := receivers(out.String())
	if len(rs) != 2 {
		t.Fatalf("%d receiver sections, want 2", len(rs))
	}
	mustContain(t, rs[0], "open failed: busy")
	mustContain(t, rs[1], `name "MX Keys S"`)
}

func TestOpenDeniedEverywhere(t *testing.T) {
	var out, progress bytes.Buffer
	c := newClock()
	denied := fakeRx{product: "USB Receiver", pid: hid.BoltPID, openErr: fmt.Errorf("%w (IOReturn 0xe00002e2)", hid.ErrAccessDenied)}
	err := diag.Run(context.Background(), opener(denied), options(c, &out, &progress))
	if !errors.Is(err, hid.ErrAccessDenied) {
		t.Fatalf("Run = %v, want hid.ErrAccessDenied", err)
	}
	mustContain(t, out.String(), "open failed: hid: access denied")
}

func TestSystemOpener(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		t.Skip("has a native HID backend")
	}
	var out, progress bytes.Buffer
	c := newClock()
	err := diag.Run(context.Background(), diag.SystemOpener(), options(c, &out, &progress))
	if !errors.Is(err, hid.ErrUnsupported) {
		t.Fatalf("Run = %v, want hid.ErrUnsupported", err)
	}
}

// pings returns the root ping frames written to devIdx, in order.
func pings(r *hidtest.Receiver, devIdx byte) [][]byte {
	var out [][]byte
	for _, f := range r.Sent() {
		if f[1] == devIdx && f[2] == 0x00 && f[3]>>4 == 1 {
			out = append(out, f)
		}
	}
	return out
}

// pingReply is the device's reply to ping request req.
func pingReply(req []byte) []byte {
	f := make([]byte, 20)
	copy(f, []byte{0x11, req[1], 0x00, req[3], 0x04, 0x05, req[6]})
	return f
}

// unreachable is the receiver's HID++ 1.0 error reply to request req.
func unreachable(req []byte) []byte {
	return []byte{0x10, req[1], 0x8F, req[2], req[3], 0x09, 0x00}
}

func TestLateReply(t *testing.T) {
	dev := &syncDevice{Receiver: newBolt()}
	var out, progress bytes.Buffer
	c := newClock()
	o := options(c, &out, &progress, 10*time.Second)
	var injected [][]byte
	c.hook = func(_ context.Context, d time.Duration) {
		switch d {
		case 10 * time.Second: // both fall asleep while nobody touches them
			dev.Update(1, func(s *hidtest.Slot) { s.Awake = false })
			dev.Update(2, func(s *hidtest.Slot) { s.Awake = false })
		case 2 * o.Timeout: // listening for late answers: both answer the idle ping now
			c.advance(1200 * time.Millisecond)
			injected = [][]byte{pingReply(pings(dev.Receiver, 1)[0]), unreachable(pings(dev.Receiver, 2)[0])}
			for _, f := range injected {
				injectAndWait(t, dev, f)
			}
		}
	}
	if err := diag.Run(context.Background(), opener(bolt(dev)), o); err != nil {
		t.Fatalf("Run: %v", err)
	}
	text := out.String()
	idle := strings.Join(block(t, text, "-- idle --"), "\n")
	mustContain(t, idle, "after 10 s idle, slot 1 (MX Keys S): timeout")
	mustContain(t, idle, "after 10 s idle, slot 2 (MX Master 4): timeout")
	// the idle ping was written 5 s before the after-wake pings, 6.2 s before the answers
	got := block(t, text, "-- late replies --")
	want := []string{
		"slot 1 (MX Keys S) idle ping after 10 s: reply after 6200.0 ms",
		"slot 2 (MX Master 4) idle ping after 10 s: error 0x09 (hid++1) after 6200.0 ms",
	}
	if strings.Join(got[1:], "\n") != strings.Join(want, "\n") {
		t.Errorf("late replies:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	unsolicited := strings.Join(block(t, text, "== unsolicited =="), "\n")
	if len(injected) != 2 {
		t.Fatal("never listened for late answers")
	}
	for _, f := range injected {
		if strings.Contains(unsolicited, hexs(f)) {
			t.Errorf("late reply %s listed as unsolicited", hexs(f))
		}
	}
	mustContain(t, progress.String(), "Listening 100ms for late answers.")
}

func TestUnansweredRequestDoesNotClaimLaterFrame(t *testing.T) {
	dev := &syncDevice{Receiver: newBolt()}
	var out, progress bytes.Buffer
	c := newClock()
	o := options(c, &out, &progress, 10*time.Second)
	o.Watch = time.Minute
	var foreign []byte
	c.hook = func(_ context.Context, d time.Duration) {
		switch d {
		case 10 * time.Second:
			dev.Update(1, func(s *hidtest.Slot) { s.Awake = false })
		case 20 * time.Second: // in the watch, long after listening for late answers
			// the same header as diag's unanswered after-wake ping, e.g. another program's ping
			foreign = pingReply(pings(dev.Receiver, 1)[1])
			injectAndWait(t, dev, foreign)
		}
	}
	if err := diag.Run(context.Background(), opener(bolt(dev)), o); err != nil {
		t.Fatalf("Run: %v", err)
	}
	text := out.String()
	if foreign == nil {
		t.Fatal("watch never ran")
	}
	mustContain(t, strings.Join(block(t, text, "== unsolicited =="), "\n"), hexs(foreign))
	mustContain(t, strings.Join(block(t, text, "-- watch --"), "\n"), hexs(foreign))
	if got := block(t, text, "-- late replies --"); got[len(got)-1] != "none" {
		t.Errorf("late replies:\n%s", strings.Join(got, "\n"))
	}
}
