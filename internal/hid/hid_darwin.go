// macOS backend: IOKit's IOHIDManager and IOHIDDevice through cgo. The C
// half lives in hid_darwin.c, because a file with //export may only declare
// C functions in its preamble.
//
// Each open receiver gets one goroutine locked to its own OS thread. That
// thread creates the IOHIDManager, opens the devices non-exclusively,
// schedules them on its CFRunLoop and runs the loop until Close; input
// reports and removal arrive there through the exported callbacks below.
// The C side knows the Go device only as a runtime/cgo.Handle.
//
// The IOKit usage follows mxswitch's macos/mxswitch.c
// (https://github.com/marcocosta97/mxswitch, MIT, Copyright (c) Marco Costa).

package hid

/*
#cgo LDFLAGS: -framework IOKit -framework CoreFoundation
#include <stdint.h>
#include <stdlib.h>

typedef struct esw_info {
	uint64_t id; // IORegistry entry ID of the IOHIDDevice's service
	int32_t vendor, product, usage_page, usage, max_in, max_out, location;
	char name[256];
} esw_info;

typedef struct esw_session esw_session;

int32_t esw_enumerate(esw_info **out, int *count);
int32_t esw_open(const uint64_t *ids, int n, uintptr_t ctx, esw_session **out);
void esw_run(esw_session *s);
void esw_stop(esw_session *s);
int32_t esw_set_report(esw_session *s, int index, uint8_t report_id, const uint8_t *data, long len);
int esw_access_denied(int32_t ret);
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"runtime/cgo"
	"strconv"
	"strings"
	"sync"
	"unsafe"
)

// pathPrefix starts every Info.Path on macOS; the rest is the IORegistry
// entry ID, as in hidapi.
const pathPrefix = "DevSrvsID:"

var errRemoved = errors.New("hid: device removed")

// Enumerate lists the Logitech HID devices present.
func Enumerate() ([]Info, error) {
	var list *C.esw_info
	var n C.int
	if ret := C.esw_enumerate(&list, &n); ret != 0 {
		return nil, fmt.Errorf("hid: enumerate: IOReturn %#08x", uint32(ret))
	}
	defer C.free(unsafe.Pointer(list))
	entries := unsafe.Slice(list, int(n))
	out := make([]Info, 0, len(entries))
	for i := range entries {
		e := &entries[i]
		if uint16(e.vendor) != LogitechVID || e.id == 0 {
			continue
		}
		out = append(out, Info{
			Path:      pathPrefix + strconv.FormatUint(uint64(e.id), 10),
			Group:     fmt.Sprintf("%08x:%04x", uint32(e.location), uint16(e.product)),
			Product:   C.GoString(&e.name[0]),
			VendorID:  uint16(e.vendor),
			ProductID: uint16(e.product),
			UsagePage: uint16(e.usage_page),
			Usage:     uint16(e.usage),
			InLen:     int(e.max_in),
			OutLen:    int(e.max_out),
		})
	}
	return out, nil
}

// darwinDevice is an open receiver.
type darwinDevice struct {
	infos   []Info // routing input: OutLen 0, see withUnknownOutLen
	reports chan []byte
	done    chan struct{} // closed by Close
	exited  chan struct{} // closed when the run-loop thread has closed everything

	removeOnce sync.Once
	removed    chan struct{} // closed when IOKit reports the device gone

	mu      sync.RWMutex // Write holds it shared, Close exclusively
	closed  bool
	session *C.esw_session // set before Open returns; freed by the run-loop thread

	closeOnce sync.Once
}

// Open opens every interface of the receiver, non-exclusively.
func (r Receiver) Open() (Device, error) {
	if len(r.Interfaces) == 0 {
		return nil, errors.New("hid: receiver has no interfaces")
	}
	ids := make([]C.uint64_t, len(r.Interfaces))
	for i, in := range r.Interfaces {
		s, ok := strings.CutPrefix(in.Path, pathPrefix)
		id, err := strconv.ParseUint(s, 10, 64)
		if !ok || err != nil {
			return nil, fmt.Errorf("hid: bad path %q", in.Path)
		}
		ids[i] = C.uint64_t(id)
	}
	d := &darwinDevice{
		infos:   withUnknownOutLen(r.Interfaces),
		reports: make(chan []byte, 64),
		done:    make(chan struct{}),
		exited:  make(chan struct{}),
		removed: make(chan struct{}),
	}
	ready := make(chan error, 1)
	go d.run(ids, ready)
	if err := <-ready; err != nil {
		<-d.exited
		return nil, err
	}
	return d, nil
}

// run owns the IOHIDManager, the devices and the run loop. It reports the
// outcome of opening on ready, then runs the loop until esw_stop.
func (d *darwinDevice) run(ids []C.uint64_t, ready chan<- error) {
	// Never unlocked: when this goroutine ends, the runtime ends the thread
	// with it instead of handing a thread with run-loop state to others.
	runtime.LockOSThread()
	defer close(d.exited)
	h := cgo.NewHandle(d)
	defer h.Delete()

	var s *C.esw_session
	if ret := C.esw_open(&ids[0], C.int(len(ids)), C.uintptr_t(h), &s); ret != 0 {
		ready <- openError(ret)
		return
	}
	d.session = s
	ready <- nil
	C.esw_run(s) // frees s before returning
}

func openError(ret C.int32_t) error {
	if C.esw_access_denied(ret) != 0 {
		return fmt.Errorf("%w (IOReturn %#08x)", ErrAccessDenied, uint32(ret))
	}
	return fmt.Errorf("hid: open: IOReturn %#08x", uint32(ret))
}

//export eswInputReport
func eswInputReport(ctx C.uintptr_t, reportID C.uint32_t, report *C.uint8_t, n C.long) {
	d := cgo.Handle(ctx).Value().(*darwinDevice)
	r := withReportID(uint32(reportID), C.GoBytes(unsafe.Pointer(report), C.int(n)))
	select {
	case d.reports <- r:
	case <-d.done:
	}
}

//export eswRemoved
func eswRemoved(ctx C.uintptr_t) {
	d := cgo.Handle(ctx).Value().(*darwinDevice)
	d.removeOnce.Do(func() { close(d.removed) })
}

// Read returns the next input report.
func (d *darwinDevice) Read(ctx context.Context) ([]byte, error) {
	select {
	case <-d.done:
		return nil, errClosed
	case <-d.removed:
		return nil, errRemoved
	default:
	}
	select {
	case r := <-d.reports:
		return r, nil
	case <-d.removed:
		return nil, errRemoved
	case <-d.done:
		return nil, errClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Write sends report as an output report; report[0] is its ID.
func (d *darwinDevice) Write(report []byte) error {
	if len(report) == 0 {
		return errors.New("hid: empty report")
	}
	i, err := routeReport(d.infos, report[0])
	if err != nil {
		return err
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed {
		return errClosed
	}
	ret := C.esw_set_report(d.session, C.int(i), C.uint8_t(report[0]),
		(*C.uint8_t)(unsafe.Pointer(&report[0])), C.long(len(report)))
	if ret != 0 {
		return fmt.Errorf("hid: write report %#02x: IOReturn %#08x", report[0], uint32(ret))
	}
	return nil
}

// Close waits for a pending Write, stops the run loop and waits until its
// thread has closed the devices and released the manager.
//
// d.session is alive when esw_stop reads it: Open returned d, so esw_open
// succeeded; the run loop ends only through the stop source, which only
// esw_stop signals (esw_run loops until then and cannot return on its own);
// and closeOnce makes this the single esw_stop call, so a second Close never
// touches the freed session.
func (d *darwinDevice) Close() error {
	d.closeOnce.Do(func() {
		d.mu.Lock()
		d.closed = true
		d.mu.Unlock()
		close(d.done) // unblocks a callback waiting to hand over a report
		C.esw_stop(d.session)
		<-d.exited
	})
	return nil
}
