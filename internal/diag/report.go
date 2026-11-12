package diag

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/Orishas/easyswitch-mqtt/internal/hidpp"
)

// report writes the plain-text report line by line, so that whatever was
// measured is already written when a run is interrupted. It keeps the
// first write error.
type report struct {
	w       io.Writer
	started bool
	err     error
}

func (r *report) line(format string, args ...any) {
	if r.err == nil {
		_, r.err = fmt.Fprintf(r.w, format+"\n", args...)
	}
}

// section starts a top-level section: "== name ==".
func (r *report) section(name string) {
	if r.started {
		r.line("")
	}
	r.started = true
	r.line("== %s ==", name)
}

// part starts a part of a receiver section: "-- name --".
func (r *report) part(name string) {
	r.line("")
	r.line("-- %s --", name)
}

// result returns err, joined with the write error if writing failed.
func (r *report) result(err error) error {
	if r.err == nil {
		return err
	}
	return errors.Join(err, fmt.Errorf("diag: writing the report: %w", r.err))
}

// say writes one instruction for the person; its errors do not matter.
func say(w io.Writer, format string, args ...any) {
	fmt.Fprintf(w, format+"\n", args...)
}

// ms formats a latency in milliseconds with one decimal.
func ms(d time.Duration) string {
	return fmt.Sprintf("%.1f ms", float64(d)/float64(time.Millisecond))
}

// offset formats a time since a start as +<seconds>.<milliseconds>.
func offset(d time.Duration) string {
	d = max(d, 0)
	return fmt.Sprintf("+%d.%03d", d/time.Second, d%time.Second/time.Millisecond)
}

// span formats a wait for a person: "10 s", "5 min", "4 min 30 s".
func span(d time.Duration) string {
	if d < time.Second || d%time.Second != 0 {
		return d.String()
	}
	m, s := d/time.Minute, d%time.Minute/time.Second
	switch {
	case m == 0:
		return fmt.Sprintf("%d s", s)
	case s == 0:
		return fmt.Sprintf("%d min", m)
	}
	return fmt.Sprintf("%d min %d s", m, s)
}

// hexFrame formats a frame as space-separated hex bytes.
func hexFrame(b []byte) string { return fmt.Sprintf("% x", b) }

// outcome describes a failed request: "timeout" when nothing answered,
// "error 0x09 (hid++1) after 1.2 ms" when the receiver or device answered
// with an error.
func outcome(err error) string {
	var de *hidpp.DeviceError
	switch {
	case errors.As(err, &de):
		v := "hid++2"
		if de.HIDPP1 {
			v = "hid++1"
		}
		return fmt.Sprintf("error 0x%02x (%s) after %s", de.Code, v, ms(de.Latency))
	case errors.Is(err, hidpp.ErrTimeout):
		return "timeout"
	}
	return "failed: " + err.Error()
}

// yesNo formats a bool for the person reading the report.
func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
