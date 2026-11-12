package hidpp_test

import (
	"context"
	"testing"
	"time"

	"github.com/Orishas/easyswitch-mqtt/internal/hidpp"
	"github.com/Orishas/easyswitch-mqtt/internal/hidtest"
)

const (
	keysChangeHostIdx byte = 0x0a
	nameIdx           byte = 0x03
	longName               = "Logitech MX Keys S for Business Plus" // 36 bytes: three fragments
)

// newBolt returns a fake receiver with "MX Keys S" at slot 1, "MX Master 4"
// at slot 2 and a device with a long name at slot 3.
func newBolt() (*hidtest.Receiver, *hidpp.Conn) {
	r := hidtest.NewReceiver(map[byte]*hidtest.Slot{
		1: {
			Name:     "MX Keys S",
			Features: map[uint16]byte{hidpp.FeatDeviceName: nameIdx, hidpp.FeatChangeHost: keysChangeHostIdx},
			Hosts:    3,
			Awake:    true,
		},
		2: {
			Name:     "MX Master 4",
			Features: map[uint16]byte{hidpp.FeatDeviceName: nameIdx, hidpp.FeatChangeHost: changeHostIdx},
			Hosts:    3,
			Awake:    true,
		},
		3: {
			Name:     longName,
			Features: map[uint16]byte{hidpp.FeatDeviceName: nameIdx},
			Awake:    true,
		},
	})
	return r, hidpp.NewConn(r)
}

func testCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestFeatureIndex(t *testing.T) {
	_, c := newBolt()
	defer c.Close()
	for _, tc := range []struct {
		devIdx byte
		id     uint16
		want   byte
	}{
		{1, hidpp.FeatChangeHost, keysChangeHostIdx},
		{2, hidpp.FeatChangeHost, changeHostIdx},
		{2, hidpp.FeatHostsInfo, 0}, // not present
	} {
		got, err := c.FeatureIndex(testCtx(t), tc.devIdx, tc.id)
		if err != nil {
			t.Fatalf("FeatureIndex(%d, %#04x): %v", tc.devIdx, tc.id, err)
		}
		if got != tc.want {
			t.Fatalf("FeatureIndex(%d, %#04x) = %#02x, want %#02x", tc.devIdx, tc.id, got, tc.want)
		}
	}
}

func TestDeviceName(t *testing.T) {
	for _, tc := range []struct {
		devIdx    byte
		want      string
		fragments int
	}{
		{2, "MX Master 4", 1},
		{3, longName, 3},
	} {
		t.Run(tc.want, func(t *testing.T) {
			r, c := newBolt()
			defer c.Close()
			got, err := c.DeviceName(testCtx(t), tc.devIdx, nameIdx)
			if err != nil {
				t.Fatalf("DeviceName: %v", err)
			}
			if got != tc.want {
				t.Fatalf("DeviceName = %q, want %q", got, tc.want)
			}
			var fragments int
			for _, f := range r.Sent() {
				if f[2] == nameIdx && f[3]>>4 == 1 {
					fragments++
				}
			}
			if fragments != tc.fragments {
				t.Fatalf("requested %d name fragments, want %d", fragments, tc.fragments)
			}
		})
	}
}

func TestHostInfo(t *testing.T) {
	_, c := newBolt()
	defer c.Close()
	got, err := c.HostInfo(testCtx(t), 1, keysChangeHostIdx)
	if err != nil {
		t.Fatalf("HostInfo: %v", err)
	}
	if want := (hidpp.HostInfo{Count: 3, Current: 0}); got != want {
		t.Fatalf("HostInfo = %+v, want %+v", got, want)
	}
}

func TestSetHost(t *testing.T) {
	r, c := newBolt()
	defer c.Close()
	if err := c.SetHost(2, changeHostIdx, 1); err != nil {
		t.Fatalf("SetHost: %v", err)
	}
	sent := r.Sent()
	if len(sent) != 1 {
		t.Fatalf("sent %d frames, want 1", len(sent))
	}
	f := sent[0]
	want := make([]byte, 20)
	copy(want, []byte{0x11, 0x02, 0x0e, f[3], 0x01})
	if f[3]>>4 != 1 || string(f) != string(want) {
		t.Fatalf("sent % x, want 11 02 0e 1? 01 00…", f)
	}
	var current int
	r.Update(2, func(s *hidtest.Slot) { current = s.Current })
	if current != 1 {
		t.Fatalf("slot 2 on host %d, want 1", current)
	}

	for _, host := range []int{-1, 3} {
		if err := c.SetHost(2, changeHostIdx, host); err == nil {
			t.Fatalf("SetHost(…, %d) = nil, want an error", host)
		}
	}
	if n := len(r.Sent()); n != 1 {
		t.Fatalf("sent %d frames after invalid hosts, want still 1", n)
	}
}

func TestPing(t *testing.T) {
	const delay = 30 * time.Millisecond
	r, c := newBolt()
	defer c.Close()
	r.Update(2, func(s *hidtest.Slot) { s.Delay = delay })
	got, err := c.Ping(testCtx(t), 2)
	if err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if got < delay || got > delay+200*time.Millisecond {
		t.Fatalf("Ping = %v, want about %v", got, delay)
	}
	sent := r.Sent()
	want := make([]byte, 20)
	copy(want, []byte{0x11, 0x02, 0x00, sent[0][3], 0x00, 0x00, 0x5A})
	if len(sent) != 1 || sent[0][3]>>4 != 1 || string(sent[0]) != string(want) {
		t.Fatalf("sent % x, want one root ping 11 02 00 1? 00 00 5a", sent)
	}
}
