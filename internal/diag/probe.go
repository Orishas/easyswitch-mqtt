package diag

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Orishas/easyswitch-mqtt/internal/hid"
	"github.com/Orishas/easyswitch-mqtt/internal/hidpp"
)

// Slots a Bolt receiver can pair devices to.
const firstSlot, lastSlot byte = 1, 6

// Waits the person is asked for.
const (
	wakeWait     = 5 * time.Second  // after "move the mouse and press a key"
	keyWait      = 20 * time.Second // after "press a key", before "switch channel"
	countdownGap = time.Minute      // longest wait without a sign of life
)

// frame is one input report as diag saw it.
type frame struct {
	at   time.Time
	data []byte
	own  bool // reply to one of diag's requests
}

// tap sits between the Conn and the receiver. It stamps every input
// report with the run's clock and tells the replies to diag's own requests
// (same device index, feature index, function and software ID as a
// request diag wrote, error replies of both HID++ versions included) from
// everything else. Each request claims at most one reply. A reply that
// comes after its request timed out is kept as a late reply. A request
// stops waiting when its software ID is used again or when expire is
// called, so it cannot claim a foreign frame much later.
type tap struct {
	dev     hid.Device
	now     func() time.Time
	timeout time.Duration           // a reply later than this is late
	lost    context.CancelCauseFunc // called with the read error that ended the reader

	mu      sync.Mutex
	label   string     // what the requests written now ask, for late replies
	last    *request   // the request written last
	waiting []*request // requests written that have had no reply yet
	frames  []frame
	late    []lateReply
}

// request is one request frame diag wrote.
type request struct {
	frame    []byte
	label    string
	at       time.Time
	timedOut bool       // its Request call returned ErrTimeout
	reply    *lateReply // set when a reply was claimed
	reported bool       // reply is in tap.late
}

// lateReply is a reply that came after its request had timed out.
type lateReply struct {
	label   string
	latency time.Duration // from write to reply
	isError bool
	code    byte
	hidpp1  bool
}

// ask labels the requests written from now on.
func (t *tap) ask(label string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.label = label
}

func (t *tap) Write(report []byte) error {
	req := &request{frame: append([]byte(nil), report...)}
	t.mu.Lock()
	req.label, req.at = t.label, t.now()
	if len(report) >= 4 { // its software ID comes round again: the old request can no longer be told apart
		t.waiting = slices.DeleteFunc(t.waiting, func(r *request) bool { return r.frame[3]&0x0F == report[3]&0x0F })
	}
	t.waiting = append(t.waiting, req)
	t.last = req
	t.mu.Unlock()
	if err := t.dev.Write(report); err != nil {
		t.mu.Lock()
		t.waiting = slices.DeleteFunc(t.waiting, func(r *request) bool { return r == req })
		t.mu.Unlock()
		return err
	}
	return nil
}

func (t *tap) Read(ctx context.Context) ([]byte, error) {
	data, err := t.dev.Read(ctx)
	if err != nil {
		if ctx.Err() == nil {
			t.lost(err)
		}
		return nil, err
	}
	f := frame{at: t.now(), data: append([]byte(nil), data...)}
	t.mu.Lock()
	f.own = t.claim(f)
	t.frames = append(t.frames, f)
	t.mu.Unlock()
	return data, nil
}

func (t *tap) Close() error { return t.dev.Close() }

// claim removes the first waiting request f replies to and reports
// whether there was one. The caller holds t.mu.
func (t *tap) claim(f frame) bool {
	for i, req := range t.waiting {
		r := req.frame
		if len(r) < 4 {
			continue
		}
		outcome := hidpp.Classify(f.data, r[1], r[2], r[3]>>4, r[3]&0x0F)
		if outcome != hidpp.Match && outcome != hidpp.Error {
			continue
		}
		t.waiting = slices.Delete(t.waiting, i, i+1)
		req.reply = &lateReply{label: req.label, latency: f.at.Sub(req.at), isError: outcome == hidpp.Error}
		if req.reply.isError {
			req.reply.code, req.reply.hidpp1 = hidpp.ErrorCode(f.data)
		}
		if req.timedOut || req.reply.latency > t.timeout {
			t.report(req)
		}
		return true
	}
	return false
}

// report adds req's reply to the late replies once. The caller holds t.mu.
func (t *tap) report(req *request) {
	if !req.reported {
		req.reported = true
		t.late = append(t.late, *req.reply)
	}
}

// timedOut marks the request written last as timed out: its Request call
// returned ErrTimeout. If its reply was claimed in the meantime, that
// reply is late.
func (t *tap) timedOut() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if req := t.last; req != nil {
		req.timedOut = true
		if req.reply != nil {
			t.report(req)
		}
	}
}

// unanswered reports whether a timed-out request still waits for a reply.
func (t *tap) unanswered() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.ContainsFunc(t.waiting, func(r *request) bool { return r.timedOut })
}

// expire stops every request waiting.
func (t *tap) expire() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.waiting = nil
}

// lateReplies returns the late replies so far.
func (t *tap) lateReplies() []lateReply {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.late)
}

// seen returns the frames seen so far.
func (t *tap) seen() []frame {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.frames)
}

// errLost ends a receiver's probe: the receiver stopped working.
type errLost struct{ err error }

func (e errLost) Error() string { return "receiver lost: " + e.err.Error() }

// slot is a device that answered the first question.
type slot struct {
	idx        byte
	name       string // "" when unknown
	changeHost byte   // feature index of 0x1814, 0 when absent
}

func (s *slot) label() string {
	if s.name == "" {
		return fmt.Sprintf("slot %d", s.idx)
	}
	return fmt.Sprintf("slot %d (%s)", s.idx, s.name)
}

// probe measures one open receiver.
type probe struct {
	o     Options
	run   context.Context // the whole run; its end means interrupted
	ctx   context.Context // also ends when the receiver is lost
	conn  *hidpp.Conn
	tap   *tap
	out   *report
	slots []*slot // devices that answered, in slot order
}

// probeReceiver measures one receiver and returns what its tap saw. A
// lost receiver is written to the report and is not an error; the error
// is the run's end (interrupted) or a failed Sleep.
func probeReceiver(ctx context.Context, o Options, out *report, n, total int, dev hid.Device) ([]frame, error) {
	rctx, lost := context.WithCancelCause(ctx)
	defer lost(nil)
	t := &tap{dev: dev, now: o.Now, timeout: o.Timeout, lost: lost}
	conn := hidpp.NewConn(t)
	p := &probe{o: o, run: ctx, ctx: rctx, conn: conn, tap: t, out: out}
	err := p.all(n, total)
	conn.Close()
	p.writeLate()
	var l errLost
	if errors.As(err, &l) {
		out.line("receiver lost: %v", l.err)
		err = nil
	}
	return t.seen(), err
}

func (p *probe) all(n, total int) error {
	say(p.o.Progress, "Receiver %d of %d: move the mouse and press a key on the keyboard now, so the devices are awake.", n, total)
	if err := p.wait(wakeWait, ""); err != nil {
		return err
	}
	say(p.o.Progress, "Checking slots %d to %d. Please wait.", firstSlot, lastSlot)
	for _, step := range []func() error{p.probeSlots, p.hostsInfo, p.idle, p.listen, p.watch} {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

// fatal returns what ends the probe after a failed request: the run's
// end, or a lost receiver. A timeout or error reply is only that
// request's outcome: nil.
func (p *probe) fatal(err error) error {
	if err == nil {
		return nil
	}
	if e := p.run.Err(); e != nil {
		return e
	}
	var de *hidpp.DeviceError
	if errors.As(err, &de) || errors.Is(err, hidpp.ErrTimeout) {
		return nil
	}
	if p.ctx.Err() != nil {
		err = context.Cause(p.ctx)
	}
	return errLost{err}
}

// call runs f with one request timeout. label says what f asks, for the
// late replies part. A request that times out is marked as such, so its
// reply, should it still come, is reported as late.
func (p *probe) call(label string, f func(context.Context) error) error {
	p.tap.ask(label)
	ctx, cancel := context.WithTimeout(p.ctx, p.o.Timeout)
	defer cancel()
	err := f(ctx)
	if errors.Is(err, hidpp.ErrTimeout) {
		p.tap.timedOut()
	}
	return err
}

func (p *probe) request(label string, devIdx, featIdx, fn byte, params ...byte) (rep hidpp.Reply, err error) {
	err = p.call(label, func(ctx context.Context) error {
		rep, err = p.conn.Request(ctx, devIdx, featIdx, fn, params)
		return err
	})
	return rep, err
}

func (p *probe) featureIndex(label string, devIdx byte, id uint16) (idx byte, err error) {
	err = p.call(label, func(ctx context.Context) error {
		idx, err = p.conn.FeatureIndex(ctx, devIdx, id)
		return err
	})
	return idx, err
}

// wait sleeps d, telling the person how long is left at least once a
// minute. reminder, if set, is repeated with each countdown line.
func (p *probe) wait(d time.Duration, reminder string) error {
	for left := d; left > 0; {
		step := min(left, countdownGap)
		if err := p.o.Sleep(p.ctx, step); err != nil {
			if e := p.run.Err(); e != nil {
				return e
			}
			if p.ctx.Err() != nil {
				return errLost{context.Cause(p.ctx)}
			}
			return err
		}
		left -= step
		if left > 0 {
			if reminder != "" {
				say(p.o.Progress, "  %s left. %s", span(left), reminder)
			} else {
				say(p.o.Progress, "  %s left.", span(left))
			}
		}
	}
	return nil
}

func (p *probe) probeSlots() error {
	p.out.part("slots")
	p.out.line("Each pairing slot is asked whether its device has the Easy-Switch feature (0x1814).")
	p.out.line("The time is how long the answer took. \"timeout\" means no answer within %s;", span(p.o.Timeout))
	p.out.line("\"error\" means the receiver (hid++1) or the device (hid++2) answered with that error code.")
	id := hidpp.FeatChangeHost
	for idx := firstSlot; idx <= lastSlot; idx++ {
		// root getFeature, sent directly for its latency
		rep, err := p.request(fmt.Sprintf("slot %d Easy-Switch lookup", idx), idx, 0x00, 0, byte(id>>8), byte(id), 0x00)
		if f := p.fatal(err); f != nil {
			return f
		}
		if err != nil {
			p.out.line("slot %d: %s", idx, outcome(err))
			continue
		}
		s := &slot{idx: idx, changeHost: rep.Data[4]}
		p.slots = append(p.slots, s)
		if s.changeHost == 0 {
			p.out.line("slot %d: answered in %s; no Easy-Switch feature (0x1814)", idx, ms(rep.Latency))
			continue
		}
		parts := []string{
			"answered in " + ms(rep.Latency),
			fmt.Sprintf("Easy-Switch (0x1814) at feature index 0x%02x", s.changeHost),
		}
		name, err := p.name(idx)
		if f := p.fatal(err); f != nil {
			return f
		}
		switch {
		case err != nil:
			parts = append(parts, "name: "+outcome(err))
		case name == "":
			parts = append(parts, "name unknown (no feature 0x0005)")
		default:
			s.name = name
			parts = append(parts, fmt.Sprintf("name %q", name))
		}
		var hi hidpp.HostInfo
		err = p.call(fmt.Sprintf("slot %d channels", idx), func(ctx context.Context) (err error) {
			hi, err = p.conn.HostInfo(ctx, idx, s.changeHost)
			return err
		})
		if f := p.fatal(err); f != nil {
			return f
		}
		if err != nil {
			parts = append(parts, "channels: "+outcome(err))
		} else {
			parts = append(parts, fmt.Sprintf("%d channels", hi.Count), fmt.Sprintf("current channel %d", hi.Current+1))
		}
		p.out.line("slot %d: %s", idx, strings.Join(parts, "; "))
	}
	return nil
}

// name reads a device's name, "" when it has no DeviceName feature.
func (p *probe) name(idx byte) (string, error) {
	nameIdx, err := p.featureIndex(fmt.Sprintf("slot %d name lookup", idx), idx, hidpp.FeatDeviceName)
	if err != nil || nameIdx == 0 {
		return "", err
	}
	var name string
	err = p.call(fmt.Sprintf("slot %d name", idx), func(ctx context.Context) (err error) {
		name, err = p.conn.DeviceName(ctx, idx, nameIdx)
		return err
	})
	return name, err
}

func (p *probe) hostsInfo() error {
	p.out.part("hosts-info")
	p.out.line("Feature 0x1815 may tell which channels are paired. Raw replies, bytes in hex.")
	if len(p.slots) == 0 {
		p.out.line("skipped: no device answered")
		return nil
	}
	for _, s := range p.slots {
		idx, err := p.featureIndex(s.label()+" 0x1815 lookup", s.idx, hidpp.FeatHostsInfo)
		if f := p.fatal(err); f != nil {
			return f
		}
		switch {
		case err != nil:
			p.out.line("%s: looking for feature 0x1815: %s", s.label(), outcome(err))
			continue
		case idx == 0:
			p.out.line("%s: no feature 0x1815", s.label())
			continue
		}
		p.out.line("%s: feature 0x1815 at index 0x%02x", s.label(), idx)
		dump := func(label string, fn byte, params ...byte) error {
			rep, err := p.request(s.label()+" 0x1815 "+label, s.idx, idx, fn, params...)
			if f := p.fatal(err); f != nil {
				return f
			}
			if err != nil {
				p.out.line("  %s: %s", label, outcome(err))
			} else {
				p.out.line("  %s: %s", label, hexFrame(rep.Data))
			}
			return nil
		}
		if err := dump("function 0", 0); err != nil {
			return err
		}
		for host := byte(0); host < 3; host++ {
			if err := dump(fmt.Sprintf("function 1, params %02x", host), 1, host); err != nil {
				return err
			}
		}
	}
	return nil
}

// easySwitch returns the answering devices that have feature 0x1814.
func (p *probe) easySwitch() []*slot {
	var out []*slot
	for _, s := range p.slots {
		if s.changeHost != 0 {
			out = append(out, s)
		}
	}
	return out
}

func (p *probe) idle() error {
	p.out.part("idle")
	devices := p.easySwitch()
	switch {
	case len(p.o.Idle) == 0:
		p.out.line("skipped")
		return nil
	case len(devices) == 0:
		p.out.line("skipped: no Easy-Switch device on this receiver")
		return nil
	}
	p.out.line("Nobody touched the mouse or the keyboard; then each device was pinged once. The waits")
	p.out.line("follow one another, so each is counted from the previous ping. \"after-wake\" is the ping")
	p.out.line("%s after you were asked to move the mouse and press a key. A timeout that was", span(wakeWait))
	p.out.line("answered later is listed under late replies at the end of this receiver.")
	const reminder = "Do not touch the mouse or the keyboard."
	for _, d := range p.o.Idle {
		say(p.o.Progress, "Do not touch the mouse or the keyboard for %s.", span(d))
		if err := p.wait(d, reminder); err != nil {
			return err
		}
		if err := p.pingAll("after "+span(d)+" idle", "idle ping after "+span(d), devices); err != nil {
			return err
		}
	}
	say(p.o.Progress, "Now move the mouse and press a key on the keyboard (Shift is fine).")
	if err := p.wait(wakeWait, ""); err != nil {
		return err
	}
	return p.pingAll("after-wake", "after-wake ping", devices)
}

// pingAll pings each device once; prefix starts its report line, asked
// names the ping in the late replies part.
func (p *probe) pingAll(prefix, asked string, devices []*slot) error {
	for _, s := range devices {
		var latency time.Duration
		err := p.call(s.label()+" "+asked, func(ctx context.Context) (err error) {
			latency, err = p.conn.Ping(ctx, s.idx)
			return err
		})
		if f := p.fatal(err); f != nil {
			return f
		}
		if err != nil {
			p.out.line("%s, %s: %s", prefix, s.label(), outcome(err))
		} else {
			p.out.line("%s, %s: %s", prefix, s.label(), ms(latency))
		}
	}
	return nil
}

// listen keeps the receiver open for two request timeouts when a request
// that timed out still has no reply, so a late reply can still arrive.
// Then no request waits any longer.
func (p *probe) listen() error {
	defer p.tap.expire()
	if !p.tap.unanswered() {
		return nil
	}
	d := 2 * p.o.Timeout
	say(p.o.Progress, "Listening %s for late answers. Please wait.", span(d))
	return p.wait(d, "")
}

// writeLate writes the replies that came after their request timed out.
func (p *probe) writeLate() {
	p.out.part("late replies")
	p.out.line("Answers that came after their request had timed out, with the time from request to answer.")
	late := p.tap.lateReplies()
	if len(late) == 0 {
		p.out.line("none")
	}
	for _, l := range late {
		what := "reply"
		if l.isError {
			what = errorName(l.code, l.hidpp1)
		}
		p.out.line("%s: %s after %s", l.label, what, ms(l.latency))
	}
}

func (p *probe) watch() error {
	p.out.part("watch")
	switch {
	case p.o.Watch <= 0:
		p.out.line("skipped")
		return nil
	case len(p.easySwitch()) == 0:
		p.out.line("skipped: no Easy-Switch device on this receiver")
		return nil
	}
	p.out.line("Every frame the receiver sent in %s, with seconds since the start of the watch.", span(p.o.Watch))
	p.out.line("Lines in brackets mark when you were asked to do something. Frames are bytes in hex.")
	type mark struct {
		at   time.Time
		text string
	}
	start, first := p.o.Now(), len(p.tap.seen())
	marks := []mark{{start, "[asked: press a key]"}}
	say(p.o.Progress, "For the next %s the program records what the receiver sends.", span(p.o.Watch))
	say(p.o.Progress, "Press a key on the keyboard now.")
	const reminder = "Wait, then come back to this channel if you have not yet."
	err := p.wait(min(keyWait, p.o.Watch), "")
	if left := p.o.Watch - keyWait; err == nil && left > 0 {
		marks = append(marks, mark{p.o.Now(), "[asked: switch to another channel and back]"})
		say(p.o.Progress, "Now press the Easy-Switch button on the keyboard to go to another channel,")
		say(p.o.Progress, "wait a few seconds, then press the button for this computer's channel to come back.")
		err = p.wait(left, reminder)
	}
	frames := p.tap.seen()[first:]
	// marks first at equal times: the instruction came before what it caused
	for len(marks) > 0 || len(frames) > 0 {
		if len(marks) > 0 && (len(frames) == 0 || !frames[0].at.Before(marks[0].at)) {
			p.out.line("%s %s", offset(marks[0].at.Sub(start)), marks[0].text)
			marks = marks[1:]
			continue
		}
		p.out.line("%s %s", offset(frames[0].at.Sub(start)), hexFrame(frames[0].data))
		frames = frames[1:]
	}
	return err
}
