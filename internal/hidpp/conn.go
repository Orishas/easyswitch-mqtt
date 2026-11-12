package hidpp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Orishas/easyswitch-mqtt/internal/hid"
)

// Frame is one input report as it arrived.
type Frame struct {
	At   time.Time
	Data []byte // shared between subscribers; do not modify
}

// Reply is the device's answer to a request.
type Reply struct {
	Data    []byte
	Latency time.Duration
}

// ErrTimeout: the request's deadline passed without a reply.
var ErrTimeout = errors.New("hidpp: timeout")

var errClosed = errors.New("hidpp: connection closed")

// DeviceError is an error reply to a request.
type DeviceError struct {
	Code    byte
	HIDPP1  bool // HID++ 1.0 error from the receiver, else HID++ 2.0 from the device
	Latency time.Duration
}

func (e *DeviceError) Error() string {
	v := "hid++2"
	if e.HIDPP1 {
		v = "hid++1"
	}
	return fmt.Sprintf("hidpp: device error %#02x (%s)", e.Code, v)
}

// subscriberBuffer is how many frames a subscriber may lag behind before
// frames are dropped for it.
const subscriberBuffer = 256

// Software IDs used for requests; rotating them lets a late reply to an
// earlier request be told apart.
const swFirst, swLast byte = 0x8, 0xF

// pending is the request waiting for its reply.
type pending struct {
	devIdx, featIdx, fn, swID byte
	reply                     chan Frame // cap 1
}

// Conn matches HID++ requests to their replies over one device and fans
// every input frame out to subscribers. One reader goroutine owns Read.
type Conn struct {
	dev        hid.Device
	stopReader context.CancelFunc
	readerDone chan struct{} // closed when the reader ends

	inflight chan struct{} // cap 1: held by the request in flight
	writeMu  sync.Mutex

	mu      sync.Mutex
	nextSW  byte
	pending *pending
	subs    map[*chan Frame]struct{}
	err     error // set when the reader ends
}

// NewConn starts reading dev.
func NewConn(dev hid.Device) *Conn {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Conn{
		dev:        dev,
		stopReader: cancel,
		readerDone: make(chan struct{}),
		inflight:   make(chan struct{}, 1),
		nextSW:     swFirst,
		subs:       map[*chan Frame]struct{}{},
	}
	go c.read(ctx)
	return c
}

func (c *Conn) read(ctx context.Context) {
	defer close(c.readerDone)
	for {
		data, err := c.dev.Read(ctx)
		if err != nil {
			c.mu.Lock()
			if ctx.Err() != nil {
				err = errClosed
			}
			c.err = err
			for ch := range c.subs {
				close(*ch)
			}
			clear(c.subs)
			c.mu.Unlock()
			return
		}
		f := Frame{At: time.Now(), Data: data}
		c.mu.Lock()
		for ch := range c.subs {
			select {
			case *ch <- f:
			default: // subscriber full: drop
			}
		}
		if p := c.pending; p != nil {
			switch Classify(data, p.devIdx, p.featIdx, p.fn, p.swID) {
			case Match, Error:
				p.reply <- f
				c.pending = nil
			}
		}
		c.mu.Unlock()
	}
}

// Subscribe returns every input frame from now on. The channel is closed
// when cancel is called or the reader ends.
func (c *Conn) Subscribe() (frames <-chan Frame, cancel func()) {
	ch := make(chan Frame, subscriberBuffer)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		close(ch)
		return ch, func() {}
	}
	c.subs[&ch] = struct{}{}
	return ch, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if _, ok := c.subs[&ch]; ok {
			delete(c.subs, &ch)
			close(ch)
		}
	}
}

// Request sends one long HID++ request and waits for its reply or error
// reply until ctx is done. Requests are serialised.
func (c *Conn) Request(ctx context.Context, devIdx, featIdx, fn byte, params []byte) (Reply, error) {
	select {
	case c.inflight <- struct{}{}:
	case <-ctx.Done():
		return Reply{}, ctxErr(ctx)
	}
	defer func() { <-c.inflight }()

	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return Reply{}, err
	}
	swID := c.rotateSW()
	frame, err := Build(Long, devIdx, featIdx, fn, swID, params)
	if err != nil {
		c.mu.Unlock()
		return Reply{}, err
	}
	p := &pending{devIdx: devIdx, featIdx: featIdx, fn: fn, swID: swID, reply: make(chan Frame, 1)}
	c.pending = p
	c.mu.Unlock()
	defer c.clearPending(p)

	start := time.Now()
	if err := c.write(frame); err != nil {
		return Reply{}, err
	}
	select {
	case f := <-p.reply:
		latency := f.At.Sub(start)
		if Classify(f.Data, devIdx, featIdx, fn, swID) == Error {
			code, hidpp1 := ErrorCode(f.Data)
			return Reply{}, &DeviceError{Code: code, HIDPP1: hidpp1, Latency: latency}
		}
		return Reply{Data: f.Data, Latency: latency}, nil
	case <-ctx.Done():
		return Reply{}, ctxErr(ctx)
	case <-c.readerDone:
		return Reply{}, c.Err()
	}
}

// Send sends one long HID++ request that gets no reply.
func (c *Conn) Send(devIdx, featIdx, fn byte, params []byte) error {
	if err := c.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	swID := c.rotateSW()
	c.mu.Unlock()
	frame, err := Build(Long, devIdx, featIdx, fn, swID, params)
	if err != nil {
		return err
	}
	return c.write(frame)
}

// Err returns the read error that ended the reader, nil while healthy.
func (c *Conn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Close stops the reader and closes the device.
func (c *Conn) Close() error {
	c.stopReader()
	err := c.dev.Close()
	<-c.readerDone
	return err
}

func (c *Conn) write(frame []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.dev.Write(frame)
}

// rotateSW returns the next software ID. The caller holds c.mu.
func (c *Conn) rotateSW() byte {
	sw := c.nextSW
	c.nextSW++
	if c.nextSW > swLast {
		c.nextSW = swFirst
	}
	return sw
}

func (c *Conn) clearPending(p *pending) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending == p {
		c.pending = nil
	}
}

// ctxErr maps a passed deadline to ErrTimeout and keeps cancellation as is.
func ctxErr(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ErrTimeout
	}
	return ctx.Err()
}
