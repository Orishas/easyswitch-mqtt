package hidpp_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Orishas/easyswitch-mqtt/internal/hidpp"
	"github.com/Orishas/easyswitch-mqtt/internal/hidtest"
)

const changeHostIdx byte = 0x0e

func newFake(s *hidtest.Slot) (*hidtest.Receiver, *hidpp.Conn) {
	r := hidtest.NewReceiver(map[byte]*hidtest.Slot{2: s})
	return r, hidpp.NewConn(r)
}

func awakeSlot() *hidtest.Slot {
	return &hidtest.Slot{
		Name:     "MX Master 4",
		Features: map[uint16]byte{0x1814: changeHostIdx},
		Hosts:    3,
		Awake:    true,
	}
}

// waitSent waits until the fake has received n frames and returns the last.
func waitSent(t *testing.T, r *hidtest.Receiver, n int) []byte {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sent := r.Sent(); len(sent) >= n {
			return sent[n-1]
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("fake did not receive frame %d", n)
	return nil
}

func TestRequestReply(t *testing.T) {
	_, c := newFake(awakeSlot())
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	rep, err := c.Request(ctx, 2, 0x00, 0, []byte{0x18, 0x14})
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if len(rep.Data) != 20 || rep.Data[0] != hidpp.Long || rep.Data[1] != 2 || rep.Data[4] != changeHostIdx {
		t.Fatalf("reply % x, want long reply for slot 2 with index %#02x", rep.Data, changeHostIdx)
	}
	if rep.Latency <= 0 {
		t.Fatalf("Latency = %v, want > 0", rep.Latency)
	}
}

func TestRequestUsesLongReportAndRotatesSoftwareID(t *testing.T) {
	r, c := newFake(awakeSlot())
	defer c.Close()
	for i := 0; i < 9; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err := c.Request(ctx, 2, 0x00, 0, []byte{0x18, 0x14})
		cancel()
		if err != nil {
			t.Fatalf("Request %d: %v", i, err)
		}
	}
	sent := r.Sent()
	for i, f := range sent {
		if f[0] != hidpp.Long || len(f) != 20 {
			t.Fatalf("frame %d % x is not a long report", i, f)
		}
		sw := f[3] & 0x0F
		if sw < 0x8 {
			t.Fatalf("frame %d software ID %#x outside 0x8..0xF", i, sw)
		}
		if i > 0 && sw == sent[i-1][3]&0x0F {
			t.Fatalf("frame %d reuses the previous software ID %#x", i, sw)
		}
	}
}

func TestRequestTimeout(t *testing.T) {
	s := awakeSlot()
	s.Awake = false
	_, c := newFake(s)
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := c.Request(ctx, 2, 0x00, 0, []byte{0x18, 0x14})
	if !errors.Is(err, hidpp.ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
}

func TestRequestCanceled(t *testing.T) {
	s := awakeSlot()
	s.Awake = false
	_, c := newFake(s)
	defer c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	_, err := c.Request(ctx, 2, 0x00, 0, []byte{0x18, 0x14})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestRequestDeviceError(t *testing.T) {
	s := awakeSlot()
	s.Offline = true
	_, c := newFake(s)
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := c.Request(ctx, 2, 0x00, 0, []byte{0x18, 0x14})
	var de *hidpp.DeviceError
	if !errors.As(err, &de) {
		t.Fatalf("err = %v, want *DeviceError", err)
	}
	if de.Code != 0x09 || !de.HIDPP1 {
		t.Fatalf("DeviceError = %+v, want code 0x09, HID++ 1.0", de)
	}
	if de.Latency <= 0 {
		t.Fatalf("Latency = %v, want > 0", de.Latency)
	}
	if errors.Is(err, hidpp.ErrTimeout) {
		t.Fatal("a device error must not count as a timeout")
	}
}

func TestForeignRepliesIgnored(t *testing.T) {
	s := awakeSlot()
	s.Delay = 100 * time.Millisecond
	r, c := newFake(s)
	defer c.Close()
	frames, unsubscribe := c.Subscribe()
	defer unsubscribe()

	type result struct {
		rep hidpp.Reply
		err error
	}
	done := make(chan result, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		rep, err := c.Request(ctx, 2, 0x00, 0, []byte{0x18, 0x14})
		done <- result{rep, err}
	}()
	req := waitSent(t, r, 1)

	foreignSW := make([]byte, 20)
	copy(foreignSW, []byte{0x11, 0x02, 0x00, 0x01, 0x07}) // same device and feature, software ID 0x01
	otherDev := make([]byte, 20)
	copy(otherDev, []byte{0x11, 0x03, 0x00, req[3], 0x07}) // same header, device index 3
	notification := make([]byte, 20)
	copy(notification, []byte{0x11, 0x02, 0x41, 0x04, 0x01}) // device connection notification
	r.Inject(foreignSW)
	r.Inject(otherDev)
	r.Inject(notification)

	res := <-done
	if res.err != nil {
		t.Fatalf("Request: %v", res.err)
	}
	if res.rep.Data[4] != changeHostIdx || res.rep.Data[3] != req[3] {
		t.Fatalf("reply % x, want the fake's reply to % x", res.rep.Data, req[:4])
	}

	want := [][]byte{foreignSW, otherDev, notification, res.rep.Data}
	for i, w := range want {
		select {
		case f := <-frames:
			if string(f.Data) != string(w) {
				t.Fatalf("subscriber frame %d = % x, want % x", i, f.Data, w)
			}
			if f.At.IsZero() {
				t.Fatalf("subscriber frame %d has no timestamp", i)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber missed frame %d", i)
		}
	}
}

func TestLateReplyOfPreviousRequestIgnored(t *testing.T) {
	s := awakeSlot()
	s.Delay = 200 * time.Millisecond
	r, c := newFake(s)
	defer c.Close()

	ctx1, cancel1 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel1()
	if _, err := c.Request(ctx1, 2, 0x00, 0, []byte{0x18, 0x14}); !errors.Is(err, hidpp.ErrTimeout) {
		t.Fatalf("first request: err = %v, want ErrTimeout", err)
	}

	frames, unsubscribe := c.Subscribe()
	defer unsubscribe()
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	rep, err := c.Request(ctx2, 2, 0x00, 0, []byte{0x18, 0x14})
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	sent := r.Sent()
	if len(sent) != 2 {
		t.Fatalf("sent %d frames, want 2", len(sent))
	}
	if rep.Data[3] != sent[1][3] {
		t.Fatalf("second request got header %#02x, want its own %#02x", rep.Data[3], sent[1][3])
	}
	// The late reply to the first request arrived while the second was pending.
	select {
	case f := <-frames:
		if f.Data[3] != sent[0][3] {
			t.Fatalf("first frame seen during second request = % x, want the late reply to the first", f.Data)
		}
	default:
		t.Fatal("late reply to the first request did not arrive during the second")
	}
}

func TestSlotWakesWhileRequesting(t *testing.T) {
	s := awakeSlot()
	s.Awake = false
	r, c := newFake(s)
	defer c.Close()
	time.AfterFunc(50*time.Millisecond, func() { r.Update(2, func(s *hidtest.Slot) { s.Awake = true }) })
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		_, err := c.Request(ctx, 2, 0x00, 0, []byte{0x18, 0x14})
		cancel()
		if err == nil {
			return
		}
		if !errors.Is(err, hidpp.ErrTimeout) {
			t.Fatalf("err = %v, want ErrTimeout while asleep", err)
		}
	}
	t.Fatal("device never answered after waking")
}

func TestSendExpectsNoReply(t *testing.T) {
	s := awakeSlot()
	r, c := newFake(s)
	defer c.Close()
	if err := c.Send(2, changeHostIdx, 1, []byte{0x01}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	sent := r.Sent()
	if len(sent) != 1 || sent[0][0] != hidpp.Long || sent[0][2] != changeHostIdx || sent[0][3]>>4 != 1 || sent[0][4] != 0x01 {
		t.Fatalf("sent % x, want one long setCurrentHost frame", sent)
	}
	if s.Current != 1 || !s.Offline {
		t.Fatalf("slot after setCurrentHost: Current %d, Offline %v; want 1, true", s.Current, s.Offline)
	}
}

func TestUnplug(t *testing.T) {
	s := awakeSlot()
	s.Awake = false
	r, c := newFake(s)
	defer c.Close()
	time.AfterFunc(30*time.Millisecond, r.Unplug)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := c.Request(ctx, 2, 0x00, 0, []byte{0x18, 0x14})
	if err == nil || errors.Is(err, hidpp.ErrTimeout) || ctx.Err() != nil {
		t.Fatalf("err = %v, want a read error before the deadline", err)
	}
	if c.Err() == nil {
		t.Fatal("Err() = nil after unplug")
	}
	if _, err := c.Request(ctx, 2, 0x00, 0, []byte{0x18, 0x14}); err == nil || errors.Is(err, hidpp.ErrTimeout) {
		t.Fatalf("request after unplug: err = %v, want the read error", err)
	}
}

func TestSubscriberClosedWhenReaderEnds(t *testing.T) {
	r, c := newFake(awakeSlot())
	defer c.Close()
	frames, cancel := c.Subscribe()
	defer cancel()
	r.Unplug()
	select {
	case _, ok := <-frames:
		if ok {
			t.Fatal("unexpected frame")
		}
	case <-time.After(time.Second):
		t.Fatal("subscription not closed after the reader ended")
	}
}
