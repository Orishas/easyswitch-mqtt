package switcher

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Orishas/easyswitch-mqtt/internal/hid"
	"github.com/Orishas/easyswitch-mqtt/internal/hidpp"
	"github.com/Orishas/easyswitch-mqtt/internal/hidtest"
)

const (
	keysChangeHost   byte = 0x0a
	masterChangeHost byte = 0x0e
	nameIdx          byte = 0x03
)

// fast shortens the waits so a test with absent slots stays well under a
// second.
func fast(t *testing.T) {
	t.Helper()
	oldTimeout, oldPause := requestTimeout, roundPause
	requestTimeout, roundPause = 30*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { requestTimeout, roundPause = oldTimeout, oldPause })
}

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

func connect(t *testing.T, dev hid.Device) *hidpp.Conn {
	t.Helper()
	conn := hidpp.NewConn(dev)
	t.Cleanup(func() { conn.Close() })
	return conn
}

// setHosts counts the setCurrentHost requests sent to slot devIdx.
func setHosts(rx *hidtest.Receiver, devIdx, changeHostIdx byte) int {
	n := 0
	for _, f := range rx.Sent() {
		if f[1] == devIdx && f[2] == changeHostIdx && f[3]>>4 == 1 {
			n++
		}
	}
	return n
}

func current(rx *hidtest.Receiver, devIdx byte) int {
	var c int
	rx.Update(devIdx, func(s *hidtest.Slot) { c = s.Current })
	return c
}

func TestSwitchBoth(t *testing.T) {
	fast(t)
	rx := hidtest.NewReceiver(boltSlots())
	got, err := Switch(context.Background(), connect(t, rx), 2, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	want := []Result{
		{Name: "MX Keys S", Slot: 1, From: 1, Outcome: "left"},
		{Name: "MX Master 4", Slot: 2, From: 1, Outcome: "left"},
	}
	assertResults(t, got, want)
	for _, slot := range []byte{1, 2} {
		if c := current(rx, slot); c != 1 {
			t.Errorf("slot %d on host %d, want 1", slot, c)
		}
	}
}

func TestSwitchAlready(t *testing.T) {
	fast(t)
	slots := boltSlots()
	delete(slots, 2)
	slots[1].Current = 1
	rx := hidtest.NewReceiver(slots)
	got, err := Switch(context.Background(), connect(t, rx), 2, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	assertResults(t, got, []Result{{Name: "MX Keys S", Slot: 1, From: 2, Outcome: "already"}})
	if n := setHosts(rx, 1, keysChangeHost); n != 0 {
		t.Errorf("%d setCurrentHost requests sent, want 0", n)
	}
}

func TestSwitchLateWaker(t *testing.T) {
	fast(t)
	slots := boltSlots()
	slots[2].Offline = true // asleep: the receiver answers for it
	rx := hidtest.NewReceiver(slots)
	timer := time.AfterFunc(150*time.Millisecond, func() {
		rx.Update(2, func(s *hidtest.Slot) { s.Offline = false })
	})
	defer timer.Stop()

	const patience = 2 * time.Second
	start := time.Now()
	got, err := Switch(context.Background(), connect(t, rx), 3, patience)
	if err != nil {
		t.Fatal(err)
	}
	assertResults(t, got, []Result{
		{Name: "MX Keys S", Slot: 1, From: 1, Outcome: "left"},
		{Name: "MX Master 4", Slot: 2, From: 1, Outcome: "left"},
	})
	if took := time.Since(start); took >= patience {
		t.Errorf("took %v: did not stop once every device was handled", took)
	}
}

func TestSwitchAsleep(t *testing.T) {
	fast(t)
	slots := boltSlots()
	delete(slots, 2)
	slots[1].Offline = true
	rx := hidtest.NewReceiver(slots)
	const patience = 300 * time.Millisecond
	start := time.Now()
	got, err := Switch(context.Background(), connect(t, rx), 2, patience)
	if took := time.Since(start); took < patience {
		t.Errorf("gave up after %v, want the whole patience of %v", took, patience)
	}
	if len(got) != 0 {
		t.Errorf("results %+v, want none", got)
	}
	if err == nil || err.Error() != "no device answered" {
		t.Errorf("err = %v, want no device answered", err)
	}
	if n := setHosts(rx, 1, keysChangeHost); n != 0 {
		t.Errorf("%d setCurrentHost requests sent, want 0", n)
	}

	t.Run("no device", func(t *testing.T) {
		rx := hidtest.NewReceiver(nil)
		got, err := Switch(context.Background(), connect(t, rx), 2, 200*time.Millisecond)
		if len(got) != 0 || err == nil || err.Error() != "no device answered" {
			t.Errorf("Switch = %+v, %v; want no results, no device answered", got, err)
		}
	})
}

// A device that answered once but not its host info before the patience
// ran out is reported asleep.
func TestSwitchAnsweredThenSlept(t *testing.T) {
	fast(t)
	slots := boltSlots()
	delete(slots, 2)
	rx := hidtest.NewReceiver(slots)
	dev := &hooked{Receiver: rx, onWrite: func(f []byte) bool {
		if f[1] == 1 && f[2] == keysChangeHost && f[3]>>4 == 0 { // getHostInfo: fall asleep first
			rx.Update(1, func(s *hidtest.Slot) { s.Offline = true })
		}
		return true
	}}
	got, err := Switch(context.Background(), connect(t, dev), 2, 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	assertResults(t, got, []Result{{Name: "MX Keys S", Slot: 1, Outcome: "asleep"}})
}

func TestSwitchChannelOutOfRange(t *testing.T) {
	fast(t)
	slots := boltSlots()
	delete(slots, 2)
	rx := hidtest.NewReceiver(slots)
	got, err := Switch(context.Background(), connect(t, rx), 4, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	assertResults(t, got, []Result{{Name: "MX Keys S", Slot: 1, From: 1, Outcome: "error: device has 3 channels"}})
	if n := setHosts(rx, 1, keysChangeHost); n != 0 {
		t.Errorf("%d setCurrentHost requests sent, want 0", n)
	}
}

// A device that keeps answering after setCurrentHost gets it three times,
// then counts as an error.
func TestSwitchDeviceStays(t *testing.T) {
	fast(t)
	slots := boltSlots()
	delete(slots, 2)
	rx := hidtest.NewReceiver(slots)
	sent := 0
	dev := &hooked{Receiver: rx, onWrite: func(f []byte) bool {
		if f[1] == 1 && f[2] == keysChangeHost && f[3]>>4 == 1 { // setCurrentHost: ignored
			sent++
			return false
		}
		return true
	}}
	got, err := Switch(context.Background(), connect(t, dev), 2, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	assertResults(t, got, []Result{{Name: "MX Keys S", Slot: 1, From: 1, Outcome: "error: device did not leave"}})
	if sent != 3 {
		t.Errorf("%d setCurrentHost requests sent, want 3", sent)
	}
}

// Slots the receiver reports as unknown are not waited for: with the real
// one-second request timeout, a round over four silent slots would take
// four seconds.
func TestSwitchEmptySlots(t *testing.T) {
	slots := boltSlots()
	rx := hidtest.NewReceiver(slots)
	dev := &hooked{Receiver: rx, onWrite: func(f []byte) bool {
		if f[1] >= 3 {
			rx.Inject([]byte{0x10, f[1], 0x8F, f[2], f[3], 0x08, 0x00}) // unknown device
			return false
		}
		return true
	}}
	start := time.Now()
	got, err := Switch(context.Background(), connect(t, dev), 2, 8*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Errorf("took %v, want no wait for empty slots", took)
	}
	if len(got) != 2 {
		t.Errorf("results:\n%s\nwant two", show(got))
	}
}

func TestSwitchNoName(t *testing.T) {
	fast(t)
	slots := boltSlots()
	delete(slots, 2)
	delete(slots[1].Features, hidpp.FeatDeviceName)
	rx := hidtest.NewReceiver(slots)
	got, err := Switch(context.Background(), connect(t, rx), 2, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	assertResults(t, got, []Result{{Name: "slot 1", Slot: 1, From: 1, Outcome: "left"}})
}

func TestSwitchCancel(t *testing.T) {
	fast(t)
	rx := hidtest.NewReceiver(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := Switch(ctx, connect(t, rx), 2, 5*time.Second)
	if err != context.DeadlineExceeded {
		t.Errorf("err = %v, want context.DeadlineExceeded", err)
	}
}

func assertResults(t *testing.T, got, want []Result) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("results:\n%s\nwant:\n%s", show(got), show(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("results:\n%s\nwant:\n%s", show(got), show(want))
		}
	}
}

func show(rs []Result) string {
	var b strings.Builder
	for _, r := range rs {
		fmt.Fprintf(&b, "  %+v\n", r)
	}
	return b.String()
}

// hooked passes writes to the fake receiver unless onWrite says no. Writes
// come from Switch's goroutine only.
type hooked struct {
	*hidtest.Receiver
	onWrite func(frame []byte) (forward bool)
}

func (h *hooked) Write(frame []byte) error {
	if !h.onWrite(frame) {
		return nil
	}
	return h.Receiver.Write(frame)
}
