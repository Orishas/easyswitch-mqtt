// Package switcher moves the Easy-Switch devices behind one receiver to
// another channel.
package switcher

import (
	"context"
	"errors"
	"fmt"
	"sync"
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

// DefaultPatience is how long Switch looks for devices when patience is
// not positive.
const DefaultPatience = 8 * time.Second

// attempts is how often setCurrentHost is sent to a device that keeps
// answering.
const attempts = 3

// Request timeouts; variables so tests can shorten them.
var (
	// shortTimeout is for a slot that has shown no sign of life since
	// Switch started: an idle device does not answer at all, so the rounds
	// over silent slots must be quick.
	shortTimeout = 300 * time.Millisecond
	// longTimeout is for every request to a slot that is awake, the ping
	// after setCurrentHost included.
	longTimeout = 2 * time.Second
	// roundPause is the pause between two rounds over the slots.
	roundPause = 200 * time.Millisecond
)

// errNoDevice: no device produced a result.
var errNoDevice = errors.New("no device answered")

// errUnknownDevice is the HID++ 1.0 error the receiver sends for a slot
// with no paired device.
const errUnknownDevice byte = 0x08

// slot is what Switch knows about one receiver slot.
type slot struct {
	n      byte
	idx    byte // index of ChangeHost, 0 while unknown
	name   string
	done   bool    // definitive: switched, already there, error, no ChangeHost, or empty
	result *Result // set when done with a device
}

// awake records the slots that sent any frame since Switch started, late
// replies to timed-out requests included.
type awake struct {
	mu   sync.Mutex
	seen [lastSlot + 1]bool
}

func (a *awake) mark(n byte) {
	if n >= firstSlot && n <= lastSlot {
		a.mu.Lock()
		a.seen[n] = true
		a.mu.Unlock()
	}
}

func (a *awake) is(n byte) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.seen[n]
}

// switcher is one Switch call.
type switcher struct {
	ctx      context.Context
	conn     *hidpp.Conn
	channel  int
	deadline time.Time
	awake    *awake
}

// Switch moves every Easy-Switch device on conn to channel (1-based). It
// asks slots 1 to 6 again and again, so devices that wake up late are
// still found, until patience (DefaultPatience when not positive) has
// passed. It returns earlier only when every slot has a definitive result:
// switched, already there, error, no Easy-Switch, or empty. A slot that
// stays silent or is reported unreachable keeps it looking.
//
// A device counts as gone when a ping after setCurrentHost fails or times
// out; otherwise setCurrentHost is sent again, three times at most.
// Results are in slot order; a device that answered but could not be
// switched in time is "asleep". With no result at all the error is "no
// device answered". When ctx ends, Switch returns the results so far and
// ctx.Err().
func Switch(ctx context.Context, conn *hidpp.Conn, channel int, patience time.Duration) ([]Result, error) {
	if patience <= 0 {
		patience = DefaultPatience
	}
	frames, stop := conn.Subscribe()
	sw := &switcher{ctx: ctx, conn: conn, channel: channel, deadline: time.Now().Add(patience), awake: &awake{}}
	watching := make(chan struct{})
	go func() {
		defer close(watching)
		for f := range frames {
			if len(f.Data) > 1 {
				sw.awake.mark(f.Data[1])
			}
		}
	}()
	defer func() { stop(); <-watching }()

	var slots []*slot
	for n := firstSlot; n <= lastSlot; n++ {
		slots = append(slots, &slot{n: n, name: fmt.Sprintf("slot %d", n)})
	}
	for {
		for _, s := range slots {
			if s.done {
				continue
			}
			if err := sw.try(s); err != nil {
				return collect(slots, false), err
			}
		}
		if allDone(slots) || !time.Now().Before(sw.deadline) {
			break
		}
		if err := pause(ctx, min(roundPause, time.Until(sw.deadline))); err != nil {
			return collect(slots, false), err
		}
	}
	out := collect(slots, true)
	if len(out) == 0 {
		return nil, errNoDevice
	}
	return out, nil
}

// collect returns the results in slot order, with the devices that
// answered but have none as "asleep" when withAsleep is set.
func collect(slots []*slot, withAsleep bool) []Result {
	var out []Result
	for _, s := range slots {
		if s.result != nil {
			out = append(out, *s.result)
		} else if withAsleep && s.idx != 0 {
			out = append(out, Result{Name: s.name, Slot: s.n, Outcome: "asleep"})
		}
	}
	return out
}

func allDone(slots []*slot) bool {
	for _, s := range slots {
		if !s.done {
			return false
		}
	}
	return true
}

// try takes slot s one step further and marks it done when there is
// nothing more to do. err is set only when Switch must stop.
func (sw *switcher) try(s *slot) error {
	if s.idx == 0 {
		var idx byte
		got, err := sw.ask(s, func(ctx context.Context) (err error) {
			idx, err = sw.conn.FeatureIndex(ctx, s.n, hidpp.FeatChangeHost)
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
		name, err := sw.readName(s)
		if err != nil {
			return err
		}
		if name != "" {
			s.name = name
		}
	}

	var info hidpp.HostInfo
	got, err := sw.ask(s, func(ctx context.Context) (err error) {
		info, err = sw.conn.HostInfo(ctx, s.n, s.idx)
		return err
	})
	if err != nil || !got {
		return err
	}
	res := Result{Name: s.name, Slot: s.n, From: info.Current + 1}
	switch {
	case sw.channel < 1 || sw.channel > info.Count:
		res.Outcome = fmt.Sprintf("error: device has %d channels", info.Count)
	case info.Current == sw.channel-1:
		res.Outcome = "already"
	default:
		res.Outcome, err = sw.leave(s)
		if err != nil {
			return err
		}
	}
	s.result, s.done = &res, true
	return nil
}

// ask runs one request for slot s. A slot that has shown no sign of life
// gets the short timeout, cut at the deadline; an awake slot gets the
// long one. got is true for a reply. A reply or a device error marks s as
// awake; the receiver's "unknown device" marks it done. Neither a device
// error nor a timeout is an error for Switch.
func (sw *switcher) ask(s *slot, request func(context.Context) error) (got bool, err error) {
	var rctx context.Context
	var cancel context.CancelFunc
	if sw.awake.is(s.n) {
		rctx, cancel = context.WithTimeout(sw.ctx, longTimeout)
	} else {
		rctx, cancel = context.WithDeadline(sw.ctx, minTime(time.Now().Add(shortTimeout), sw.deadline))
	}
	defer cancel()
	err = request(rctx)
	var devErr *hidpp.DeviceError
	switch {
	case err == nil:
		sw.awake.mark(s.n)
		return true, nil
	case errors.As(err, &devErr) && devErr.HIDPP1 && devErr.Code == errUnknownDevice:
		s.done = true
		return false, nil
	case errors.As(err, &devErr):
		sw.awake.mark(s.n)
		return false, nil
	case sw.ctx.Err() != nil:
		return false, sw.ctx.Err()
	case errors.Is(err, hidpp.ErrTimeout):
		return false, nil
	}
	return false, err
}

// readName reads the device name, "" when the device has no name feature
// or does not tell.
func (sw *switcher) readName(s *slot) (string, error) {
	var name string
	_, err := sw.ask(s, func(ctx context.Context) error {
		idx, err := sw.conn.FeatureIndex(ctx, s.n, hidpp.FeatDeviceName)
		if err != nil || idx == 0 {
			return err
		}
		name, err = sw.conn.DeviceName(ctx, s.n, idx)
		return err
	})
	return name, err
}

// leave sends setCurrentHost until a ping says the device is gone.
func (sw *switcher) leave(s *slot) (string, error) {
	for range attempts {
		if err := sw.conn.SetHost(s.n, s.idx, sw.channel-1); err != nil {
			return "error: " + err.Error(), nil
		}
		pctx, cancel := context.WithTimeout(sw.ctx, longTimeout)
		_, err := sw.conn.Ping(pctx, s.n)
		cancel()
		var devErr *hidpp.DeviceError
		switch {
		case sw.ctx.Err() != nil:
			return "", sw.ctx.Err()
		case errors.As(err, &devErr), errors.Is(err, hidpp.ErrTimeout):
			return "left", nil
		case err != nil:
			return "error: " + err.Error(), nil
		}
	}
	return "error: device did not leave", nil
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
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
