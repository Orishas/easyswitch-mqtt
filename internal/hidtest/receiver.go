// Package hidtest provides a scripted fake Bolt receiver for tests.
package hidtest

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Orishas/easyswitch-mqtt/internal/hid"
)

// Feature IDs the fake knows.
const (
	featDeviceName uint16 = 0x0005
	featChangeHost uint16 = 0x1814
	featHostsInfo  uint16 = 0x1815
)

// HID++ 2.0 error codes the fake answers with.
const (
	errInvalidFeatureIndex byte = 0x06
	errInvalidFunction     byte = 0x07
)

// errUnreachable is the HID++ 1.0 error the receiver sends for a device it
// cannot reach.
const errUnreachable byte = 0x09

// nameFragment is how many name bytes one getDeviceName reply carries: the
// whole parameter area of a long report.
const nameFragment = 16

// HostsInfoParams are the reply parameters of every 0x1815 function.
var HostsInfoParams = []byte{0x01, 0x03, 0x00, 0x02, 0x00, 0x01}

var (
	errUnplugged = errors.New("hidtest: receiver unplugged")
	errClosed    = errors.New("hidtest: receiver closed")
)

// Slot is one paired device. Its fields may be set directly before the
// fake is used; while requests may be in flight, change them through
// Receiver.Update.
type Slot struct {
	Name           string
	Features       map[uint16]byte // feature ID → index
	Hosts, Current int             // Current 0-based
	Awake          bool            // false: requests are dropped (timeout)
	Offline        bool            // true: receiver answers with HID++ 1.0 error 0x8F code 0x09
	Delay          time.Duration   // added before each reply
}

// Receiver is a fake receiver with devices at the given slots. It
// implements hid.Device. Requests to a slot without a device are dropped.
type Receiver struct {
	mu      sync.Mutex
	slots   map[byte]*Slot
	sent    [][]byte
	queue   [][]byte      // input reports waiting for Read
	notify  chan struct{} // signalled when queue grows
	gone    chan struct{} // closed by Unplug or Close
	goneErr error
}

var _ hid.Device = (*Receiver)(nil)

// NewReceiver returns a fake receiver serving slots, keyed by device index.
func NewReceiver(slots map[byte]*Slot) *Receiver {
	return &Receiver{
		slots:  slots,
		notify: make(chan struct{}, 1),
		gone:   make(chan struct{}),
	}
}

// Update runs f on the slot at devIdx under the fake's lock, so tests can
// change a slot while requests are in flight. It does nothing when the slot
// has no device.
func (r *Receiver) Update(devIdx byte, f func(*Slot)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s := r.slots[devIdx]; s != nil {
		f(s)
	}
}

// Inject queues an unsolicited frame for the reader.
func (r *Receiver) Inject(frame []byte) {
	r.deliver(append([]byte(nil), frame...))
}

// Sent returns every frame written, in order.
func (r *Receiver) Sent() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([][]byte, len(r.sent))
	for i, f := range r.sent {
		out[i] = append([]byte(nil), f...)
	}
	return out
}

// Unplug makes Read and Write fail from now on.
func (r *Receiver) Unplug() { r.end(errUnplugged) }

// Close makes Read and Write fail from now on.
func (r *Receiver) Close() error {
	r.end(errClosed)
	return nil
}

func (r *Receiver) end(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.goneErr == nil {
		r.goneErr = err
		close(r.gone)
	}
}

// Write takes one output report and schedules the reply, if any.
func (r *Receiver) Write(report []byte) error {
	r.mu.Lock()
	if r.goneErr != nil {
		err := r.goneErr
		r.mu.Unlock()
		return err
	}
	req := append([]byte(nil), report...)
	r.sent = append(r.sent, req)
	reply, delay := r.answer(req)
	r.mu.Unlock()

	switch {
	case reply == nil:
	case delay > 0:
		time.AfterFunc(delay, func() { r.deliver(reply) })
	default:
		r.deliver(reply)
	}
	return nil
}

// Read returns the next queued input report.
func (r *Receiver) Read(ctx context.Context) ([]byte, error) {
	for {
		r.mu.Lock()
		if r.goneErr != nil {
			err := r.goneErr
			r.mu.Unlock()
			return nil, err
		}
		if len(r.queue) > 0 {
			f := r.queue[0]
			r.queue = r.queue[1:]
			r.mu.Unlock()
			return f, nil
		}
		r.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-r.notify:
		case <-r.gone:
		}
	}
}

func (r *Receiver) deliver(frame []byte) {
	r.mu.Lock()
	if r.goneErr != nil {
		r.mu.Unlock()
		return
	}
	r.queue = append(r.queue, frame)
	r.mu.Unlock()
	select {
	case r.notify <- struct{}{}:
	default:
	}
}

// answer applies a request to the slot it names and returns the reply,
// nil for none. The caller holds r.mu.
func (r *Receiver) answer(req []byte) ([]byte, time.Duration) {
	if len(req) < 5 {
		return nil, 0
	}
	devIdx, featIdx, fnSW := req[1], req[2], req[3]
	s := r.slots[devIdx]
	if s == nil {
		return nil, 0
	}
	if s.Offline {
		return []byte{0x10, devIdx, 0x8F, featIdx, fnSW, errUnreachable, 0x00}, s.Delay
	}
	if !s.Awake {
		return nil, 0
	}
	params := make([]byte, 16)
	copy(params, req[4:])
	reply := func(p ...byte) ([]byte, time.Duration) {
		f := make([]byte, 20)
		f[0], f[1], f[2], f[3] = 0x11, devIdx, featIdx, fnSW
		copy(f[4:], p)
		return f, s.Delay
	}
	fail := func(code byte) ([]byte, time.Duration) {
		f := make([]byte, 20)
		f[0], f[1], f[2], f[3], f[4], f[5] = 0x11, devIdx, 0xFF, featIdx, fnSW, code
		return f, s.Delay
	}

	fn := fnSW >> 4
	if featIdx == 0 { // root
		switch fn {
		case 0: // getFeature
			var idx byte
			if id := uint16(params[0])<<8 | uint16(params[1]); id != 0 {
				idx = s.Features[id]
			}
			return reply(idx, 0, 0)
		case 1: // ping
			return reply(4, 5, params[2])
		}
		return fail(errInvalidFunction)
	}

	id, ok := featureAt(s, featIdx)
	if !ok {
		return fail(errInvalidFeatureIndex)
	}
	switch {
	case id == featDeviceName && fn == 0:
		return reply(byte(len(s.Name)))
	case id == featDeviceName && fn == 1:
		off := min(int(params[0]), len(s.Name))
		end := min(off+nameFragment, len(s.Name))
		return reply([]byte(s.Name[off:end])...)
	case id == featChangeHost && fn == 0:
		return reply(byte(s.Hosts), byte(s.Current))
	case id == featChangeHost && fn == 1: // setCurrentHost never replies
		if host := int(params[0]); host < s.Hosts {
			s.Current = host
			s.Offline = true
		}
		return nil, 0
	case id == featHostsInfo:
		return reply(HostsInfoParams...)
	}
	return fail(errInvalidFunction)
}

func featureAt(s *Slot, idx byte) (uint16, bool) {
	for id, i := range s.Features {
		if i == idx {
			return id, true
		}
	}
	return 0, false
}
