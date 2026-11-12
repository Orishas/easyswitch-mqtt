// Package diag measures how a Logi Bolt receiver and its Easy-Switch
// devices behave: how fast idle devices answer, what the receiver sends on
// its own, and what the devices report about their channels. The result is
// a plain-text report meant to be pasted back to the developer.
package diag

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"time"

	"github.com/Orishas/easyswitch-mqtt/internal/hid"
)

// ErrNoReceiver: the system lists no receiver to probe.
var ErrNoReceiver = errors.New("diag: no receiver found")

// Options configure a run. Zero values take the defaults.
type Options struct {
	Idle     []time.Duration                            // nil: 10s, 1m, 5m, 30m; empty but not nil: skip the idle test
	Watch    time.Duration                              // 0: skip the watch
	Timeout  time.Duration                              // per request; 0: 5s
	Out      io.Writer                                  // the report
	Progress io.Writer                                  // what the person should do now
	Now      func() time.Time                           // every timestamp, also called by the reader goroutine; nil: time.Now
	Sleep    func(context.Context, time.Duration) error // every wait; nil: a timer
	Version  string                                     // easyswitch version for the report
	Service  bool                                       // running as a system service
}

// Opener lists the HID interfaces, the receivers among them and a function
// that opens one receiver.
type Opener func() (infos []hid.Info, receivers []hid.Receiver, open func(hid.Receiver) (hid.Device, error), err error)

// SystemOpener lists and opens the receivers of this computer.
func SystemOpener() Opener {
	return func() ([]hid.Info, []hid.Receiver, func(hid.Receiver) (hid.Device, error), error) {
		infos, err := hid.Enumerate()
		if err != nil {
			return nil, nil, nil, err
		}
		return infos, hid.GroupReceivers(infos), hid.Receiver.Open, nil
	}
}

func (o Options) withDefaults() Options {
	if o.Idle == nil {
		o.Idle = []time.Duration{10 * time.Second, time.Minute, 5 * time.Minute, 30 * time.Minute}
	}
	if o.Timeout <= 0 {
		o.Timeout = 5 * time.Second
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	if o.Progress == nil {
		o.Progress = io.Discard
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Sleep == nil {
		o.Sleep = sleep
	}
	if o.Version == "" {
		o.Version = "unknown"
	}
	return o
}

// sleep waits d or until ctx ends.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Run measures every receiver open lists and writes the report to o.Out
// as it goes. When ctx ends, the report so far is completed with the
// frames seen and a last line "== interrupted ==", and Run returns
// ctx.Err(). It returns an error wrapping ErrNoReceiver when there is no
// receiver, and the listing error (hid.ErrAccessDenied, hid.ErrUnsupported)
// when listing fails.
func Run(ctx context.Context, open Opener, o Options) error {
	o = o.withDefaults()
	say(o.Progress, "Measuring. Keep this window open and follow the instructions below.")
	out := &report{w: o.Out}
	start := o.Now()
	writeSystem(out, o, start)

	infos, receivers, openDev, err := open()
	writeInterfaces(out, infos, receivers, err)
	if err != nil {
		return out.result(fmt.Errorf("diag: list HID interfaces: %w", err))
	}
	if len(receivers) == 0 {
		say(o.Progress, "No receiver found.")
		return out.result(ErrNoReceiver)
	}

	var logs [][]frame // per receiver, nil when it did not open
	var openErr error  // the first failure to open, an access denial preferred
	opened := 0
	for i, rx := range receivers {
		out.section(fmt.Sprintf("receiver %s pid=0x%04X", rx.Product, rx.ProductID))
		out.line("This is receiver %d of %d.", i+1, len(receivers))
		dev, err := openDev(rx)
		if err != nil {
			out.line("open failed: %v", err)
			if openErr == nil || (errors.Is(err, hid.ErrAccessDenied) && !errors.Is(openErr, hid.ErrAccessDenied)) {
				openErr = err
			}
			logs = append(logs, nil)
			continue
		}
		opened++
		frames, err := probeReceiver(ctx, o, out, i+1, len(receivers), dev)
		logs = append(logs, frames)
		if err != nil {
			writeUnsolicited(out, start, logs)
			if ctx.Err() != nil {
				out.section("interrupted")
				say(o.Progress, "Interrupted.")
			}
			return out.result(err)
		}
	}
	writeUnsolicited(out, start, logs)
	say(o.Progress, "Finished.")
	if opened == 0 {
		return out.result(fmt.Errorf("diag: open receiver: %w", openErr))
	}
	return out.result(nil)
}

func writeSystem(out *report, o Options, now time.Time) {
	out.section("system")
	out.line("easyswitch version: %s", o.Version)
	out.line("operating system: %s", runtime.GOOS)
	out.line("architecture: %s", runtime.GOARCH)
	out.line("running as a service: %s", yesNo(o.Service))
	out.line("time: %s", now.Format("2006-01-02 15:04:05 -07:00"))
}

func writeInterfaces(out *report, infos []hid.Info, receivers []hid.Receiver, err error) {
	out.section("interfaces")
	out.line("Every Logitech HID interface the system lists. usage is usage page:usage in hex;")
	out.line("in and out are report lengths in bytes (0: the system does not say).")
	if err != nil {
		out.line("listing failed: %v", err)
		return
	}
	if len(infos) == 0 {
		out.line("none")
	}
	for _, in := range infos {
		out.line("usage %04X:%04X pid=0x%04X in=%d out=%d product=%q", in.UsagePage, in.Usage, in.ProductID, in.InLen, in.OutLen, in.Product)
	}
	out.line("receivers to probe (devices with a vendor-defined interface): %d", len(receivers))
}

func writeUnsolicited(out *report, start time.Time, logs [][]frame) {
	out.section("unsolicited")
	out.line("Every frame the receivers sent that was not an answer to this program's requests, with")
	out.line("seconds since the start of the run. Receivers are numbered as above. Frames are bytes in hex.")
	n := 0
	for i, frames := range logs {
		for _, f := range frames {
			if !f.own {
				out.line("%s [receiver %d] %s", offset(f.at.Sub(start)), i+1, hexFrame(f.data))
				n++
			}
		}
	}
	if n == 0 {
		out.line("none")
	}
}
