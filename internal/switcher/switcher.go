// Package switcher moves the Easy-Switch devices behind one receiver to
// another channel.
package switcher

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Orishas/easyswitch-mqtt/internal/hidpp"
)

// Result is what happened to one device.
type Result struct {
	Name    string
	Slot    byte
	From    int    // 1-based channel the device was on; 0 when unknown
	Outcome string // "left", "already", "asleep" or "error: …"
}

// Slots a Bolt receiver pairs devices at.
const firstSlot, lastSlot byte = 1, 6

// How often setCurrentHost is sent to a device that keeps answering, and
// how long the ping after it may take to fail.
const (
	attempts     = 3
	leaveTimeout = 2 * time.Second
)

// Waits while looking for devices; variables so tests can shorten them.
var (
	requestTimeout = time.Second            // one request while looking
	roundPause     = 200 * time.Millisecond // between two rounds over the slots
)

// errNoDevice: no device produced a result.
var errNoDevice = errors.New("no device answered")

// errUnknownDevice is the HID++ 1.0 error the receiver sends for a slot
// with no paired device.
const errUnknownDevice byte = 0x08

// slot is what Switch knows about one receiver slot.
type slot struct {
	n        byte
	answered bool // the receiver or the device answered a request for it
	idx      byte // index of ChangeHost, 0 while unknown
	name     string
	done     bool    // nothing more to do for this slot
	result   *Result // set when done with a device
}

// Switch moves every Easy-Switch device on conn to channel (1-based). It
// asks slots 1 to 6 again and again, so devices that wake up late are
// still found, until patience has passed or every slot that answered is
// done. A device counts as gone when a ping after setCurrentHost fails or
// times out; otherwise setCurrentHost is sent again, three times at most.
// Results are in slot order; a device that answered but could not be
// switched in time is "asleep". With no result at all the error is
// "no device answered". When ctx ends, Switch returns ctx.Err().
func Switch(ctx context.Context, conn *hidpp.Conn, channel int, patience time.Duration) ([]Result, error) {
	deadline := time.Now().Add(patience)
	var slots []*slot
	for n := firstSlot; n <= lastSlot; n++ {
		slots = append(slots, &slot{n: n, name: fmt.Sprintf("slot %d", n)})
	}
	for {
		for _, s := range slots {
			if s.done {
				continue
			}
			if err := try(ctx, conn, s, channel, deadline); err != nil {
				return nil, err
			}
		}
		if finished(slots) || !time.Now().Before(deadline) {
			break
		}
		if err := pause(ctx, min(roundPause, time.Until(deadline))); err != nil {
			return nil, err
		}
	}

	var out []Result
	for _, s := range slots {
		if s.result != nil {
			out = append(out, *s.result)
		} else if s.idx != 0 {
			out = append(out, Result{Name: s.name, Slot: s.n, Outcome: "asleep"})
		}
	}
	if len(out) == 0 {
		return nil, errNoDevice
	}
	return out, nil
}

// finished: something answered and every slot that answered is done.
func finished(slots []*slot) bool {
	seen := false
	for _, s := range slots {
		if s.answered {
			seen = true
			if !s.done {
				return false
			}
		}
	}
	return seen
}

// try takes slot s one step further and marks it done when there is
// nothing more to do: it is empty, its device has no Easy-Switch, or the
// device has its result. err is set only when Switch must stop.
func try(ctx context.Context, conn *hidpp.Conn, s *slot, channel int, deadline time.Time) error {
	if s.idx == 0 {
		var idx byte
		got, err := ask(ctx, s, deadline, func(ctx context.Context) (err error) {
			idx, err = conn.FeatureIndex(ctx, s.n, hidpp.FeatChangeHost)
			return err
		})
		if err != nil || !got {
			return err
		}
		if idx == 0 {
			s.done = true
			return nil
		}
		s.idx = idx
		name, err := readName(ctx, conn, s, deadline)
		if err != nil {
			return err
		}
		if name != "" {
			s.name = name
		}
	}

	var info hidpp.HostInfo
	got, err := ask(ctx, s, deadline, func(ctx context.Context) (err error) {
		info, err = conn.HostInfo(ctx, s.n, s.idx)
		return err
	})
	if err != nil || !got {
		return err
	}
	res := Result{Name: s.name, Slot: s.n, From: info.Current + 1}
	switch {
	case channel < 1 || channel > info.Count:
		res.Outcome = fmt.Sprintf("error: device has %d channels", info.Count)
	case info.Current == channel-1:
		res.Outcome = "already"
	default:
		res.Outcome, err = leave(ctx, conn, s, channel)
		if err != nil {
			return err
		}
	}
	s.result, s.done = &res, true
	return nil
}

// ask runs one request for slot s with the looking timeout, cut at
// deadline. got is true for a reply. A reply or a device error marks s as
// answered, except the receiver's "unknown device", which marks it done.
// Neither a device error nor a timeout is an error for Switch.
func ask(ctx context.Context, s *slot, deadline time.Time, request func(context.Context) error) (got bool, err error) {
	rctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	rctx, cancelDeadline := context.WithDeadline(rctx, deadline)
	defer cancelDeadline()
	err = request(rctx)
	var devErr *hidpp.DeviceError
	switch {
	case err == nil:
		s.answered = true
		return true, nil
	case errors.As(err, &devErr) && devErr.HIDPP1 && devErr.Code == errUnknownDevice:
		s.done = true
		return false, nil
	case errors.As(err, &devErr):
		s.answered = true
		return false, nil
	case ctx.Err() != nil:
		return false, ctx.Err()
	case errors.Is(err, hidpp.ErrTimeout):
		return false, nil
	}
	return false, err
}

// readName reads the device name, "" when the device has no name feature
// or does not tell.
func readName(ctx context.Context, conn *hidpp.Conn, s *slot, deadline time.Time) (string, error) {
	var name string
	_, err := ask(ctx, s, deadline, func(ctx context.Context) error {
		idx, err := conn.FeatureIndex(ctx, s.n, hidpp.FeatDeviceName)
		if err != nil || idx == 0 {
			return err
		}
		name, err = conn.DeviceName(ctx, s.n, idx)
		return err
	})
	return name, err
}

// leave sends setCurrentHost until a ping says the device is gone.
func leave(ctx context.Context, conn *hidpp.Conn, s *slot, channel int) (string, error) {
	for range attempts {
		if err := conn.SetHost(s.n, s.idx, channel-1); err != nil {
			return "error: " + err.Error(), nil
		}
		pctx, cancel := context.WithTimeout(ctx, leaveTimeout)
		_, err := conn.Ping(pctx, s.n)
		cancel()
		var devErr *hidpp.DeviceError
		switch {
		case ctx.Err() != nil:
			return "", ctx.Err()
		case errors.As(err, &devErr), errors.Is(err, hidpp.ErrTimeout):
			return "left", nil
		case err != nil:
			return "error: " + err.Error(), nil
		}
	}
	return "error: device did not leave", nil
}

// pause waits d or until ctx ends.
func pause(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
