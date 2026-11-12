// Windows backend: setupapi.dll and hid.dll for enumeration, overlapped
// ReadFile/WriteFile for I/O. Pure Go, no cgo.
//
// The structures, the call sequence and the rule that a timed-out or
// stopped overlapped operation must be cancelled and waited for are ported
// from mxswitch's python/mxswitch.py (https://github.com/marcocosta97/mxswitch,
// MIT, Copyright (c) Marco Costa).

package hid

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modSetupapi = windows.NewLazySystemDLL("setupapi.dll")
	modHid      = windows.NewLazySystemDLL("hid.dll")

	procSetupDiGetClassDevsW             = modSetupapi.NewProc("SetupDiGetClassDevsW")
	procSetupDiEnumDeviceInterfaces      = modSetupapi.NewProc("SetupDiEnumDeviceInterfaces")
	procSetupDiGetDeviceInterfaceDetailW = modSetupapi.NewProc("SetupDiGetDeviceInterfaceDetailW")
	procSetupDiDestroyDeviceInfoList     = modSetupapi.NewProc("SetupDiDestroyDeviceInfoList")

	procHidDGetHidGuid        = modHid.NewProc("HidD_GetHidGuid")
	procHidDGetAttributes     = modHid.NewProc("HidD_GetAttributes")
	procHidDGetPreparsedData  = modHid.NewProc("HidD_GetPreparsedData")
	procHidDFreePreparsedData = modHid.NewProc("HidD_FreePreparsedData")
	procHidPGetCaps           = modHid.NewProc("HidP_GetCaps")
	procHidDGetProductString  = modHid.NewProc("HidD_GetProductString")
	enumerationProcs          = []*windows.LazyProc{
		procSetupDiGetClassDevsW, procSetupDiEnumDeviceInterfaces,
		procSetupDiGetDeviceInterfaceDetailW, procSetupDiDestroyDeviceInfoList,
		procHidDGetHidGuid, procHidDGetAttributes, procHidDGetPreparsedData,
		procHidDFreePreparsedData, procHidPGetCaps, procHidDGetProductString,
	}
)

const hidpStatusSuccess = 0x00110000 // NTSTATUS HIDP_STATUS_SUCCESS

// writeTimeoutMs bounds one WriteFile, as in mxswitch.
const writeTimeoutMs = 1000

// spDeviceInterfaceData is SP_DEVICE_INTERFACE_DATA.
type spDeviceInterfaceData struct {
	cbSize             uint32
	interfaceClassGUID windows.GUID
	flags              uint32
	reserved           uintptr
}

// hiddAttributes is HIDD_ATTRIBUTES.
type hiddAttributes struct {
	size                         uint32
	vendorID, productID, version uint16
}

// hidpCaps is HIDP_CAPS.
type hidpCaps struct {
	usage, usagePage                uint16
	inputLen, outputLen, featureLen uint16
	reserved                        [17]uint16
	numberLinkCollectionNodes       uint16
	numberInputButtonCaps           uint16
	numberInputValueCaps            uint16
	numberInputDataIndices          uint16
	numberOutputButtonCaps          uint16
	numberOutputValueCaps           uint16
	numberOutputDataIndices         uint16
	numberFeatureButtonCaps         uint16
	numberFeatureValueCaps          uint16
	numberFeatureDataIndices        uint16
}

// Enumerate lists the Logitech HID collections present.
func Enumerate() ([]Info, error) {
	for _, p := range enumerationProcs {
		if err := p.Find(); err != nil {
			return nil, fmt.Errorf("hid: %w", err)
		}
	}
	var guid windows.GUID
	procHidDGetHidGuid.Call(uintptr(unsafe.Pointer(&guid)))
	set, _, e := procSetupDiGetClassDevsW.Call(uintptr(unsafe.Pointer(&guid)), 0, 0,
		uintptr(windows.DIGCF_PRESENT|windows.DIGCF_DEVICEINTERFACE))
	if windows.Handle(set) == windows.InvalidHandle {
		return nil, fmt.Errorf("hid: SetupDiGetClassDevs: %w", e)
	}
	defer procSetupDiDestroyDeviceInfoList.Call(set)

	var out []Info
	for i := uint32(0); ; i++ {
		ifd := spDeviceInterfaceData{cbSize: uint32(unsafe.Sizeof(spDeviceInterfaceData{}))}
		r, _, e := procSetupDiEnumDeviceInterfaces.Call(set, 0, uintptr(unsafe.Pointer(&guid)),
			uintptr(i), uintptr(unsafe.Pointer(&ifd)))
		if r == 0 {
			if errors.Is(e, windows.ERROR_NO_MORE_ITEMS) {
				return out, nil
			}
			return nil, fmt.Errorf("hid: SetupDiEnumDeviceInterfaces: %w", e)
		}
		path, ok := interfacePath(set, &ifd)
		if !ok {
			continue
		}
		if info, ok := queryInterface(path); ok {
			out = append(out, info)
		}
	}
}

// interfacePath reads the device path of one enumerated interface.
func interfacePath(set uintptr, ifd *spDeviceInterfaceData) (string, bool) {
	var size uint32
	// Sizing call: fails with ERROR_INSUFFICIENT_BUFFER and sets size.
	procSetupDiGetDeviceInterfaceDetailW.Call(set, uintptr(unsafe.Pointer(ifd)), 0, 0,
		uintptr(unsafe.Pointer(&size)), 0)
	if size <= 4 {
		return "", false
	}
	// SP_DEVICE_INTERFACE_DETAIL_DATA_W: a DWORD cbSize, then the path.
	// DWORD-aligned buffer; cbSize is the documented fixed value, not the
	// buffer size: 8 on 64-bit Windows, 6 on 32-bit.
	buf := make([]uint32, (size+3)/4)
	if unsafe.Sizeof(uintptr(0)) == 8 {
		buf[0] = 8
	} else {
		buf[0] = 6
	}
	r, _, _ := procSetupDiGetDeviceInterfaceDetailW.Call(set, uintptr(unsafe.Pointer(ifd)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(size), 0, 0)
	if r == 0 {
		return "", false
	}
	return windows.UTF16PtrToString((*uint16)(unsafe.Pointer(&buf[1]))), true
}

// queryInterface opens path without access rights, reads its attributes
// and caps, and keeps it if it is Logitech's.
func queryInterface(path string) (Info, bool) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return Info{}, false
	}
	h, err := windows.CreateFile(p, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return Info{}, false
	}
	defer windows.CloseHandle(h)

	attrs := hiddAttributes{size: uint32(unsafe.Sizeof(hiddAttributes{}))}
	if r, _, _ := procHidDGetAttributes.Call(uintptr(h), uintptr(unsafe.Pointer(&attrs))); r == 0 {
		return Info{}, false
	}
	if attrs.vendorID != LogitechVID {
		return Info{}, false
	}

	var pp uintptr
	if r, _, _ := procHidDGetPreparsedData.Call(uintptr(h), uintptr(unsafe.Pointer(&pp))); r == 0 {
		return Info{}, false
	}
	var caps hidpCaps
	st, _, _ := procHidPGetCaps.Call(pp, uintptr(unsafe.Pointer(&caps)))
	procHidDFreePreparsedData.Call(pp)
	if uint32(st) != hidpStatusSuccess {
		return Info{}, false
	}

	var name [128]uint16
	product := ""
	if r, _, _ := procHidDGetProductString.Call(uintptr(h), uintptr(unsafe.Pointer(&name[0])),
		unsafe.Sizeof(name)); r != 0 {
		product = windows.UTF16ToString(name[:])
	}
	return Info{
		Path:      path,
		Group:     windowsGroup(path),
		Product:   product,
		VendorID:  attrs.vendorID,
		ProductID: attrs.productID,
		UsagePage: caps.usagePage,
		Usage:     caps.usage,
		InLen:     int(caps.inputLen),
		OutLen:    int(caps.outputLen),
	}, true
}

var (
	errStopped      = errors.New("hid: stopped")
	errWriteTimeout = errors.New("hid: write timed out")
)

// winIface is one open collection. The OVERLAPPED structures live here, on
// the heap, because the OS writes to them after the call that started the
// operation has returned.
type winIface struct {
	info            Info
	h               windows.Handle
	readOv, writeOv windows.Overlapped // HEvent: manual-reset events
}

// winDevice is an open receiver. One reader goroutine per collection feeds
// reports; Close signals stop, which makes every pending operation cancel
// and wait for itself.
type winDevice struct {
	ifaces  []*winIface
	infos   []Info
	stop    windows.Handle // manual-reset event, set by Close
	done    chan struct{}  // closed by Close
	reports chan []byte
	readers sync.WaitGroup

	failOnce sync.Once
	failed   chan struct{} // closed when a read failed for good
	err      error         // that failure; written before failed is closed

	writeMu sync.Mutex
	closed  bool // under writeMu

	closeOnce sync.Once
}

// Open opens every collection of the receiver for overlapped read and write.
func (r Receiver) Open() (Device, error) {
	if len(r.Interfaces) == 0 {
		return nil, errors.New("hid: receiver has no interfaces")
	}
	stop, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return nil, fmt.Errorf("hid: CreateEvent: %w", err)
	}
	d := &winDevice{
		stop:    stop,
		done:    make(chan struct{}),
		reports: make(chan []byte, 64),
		failed:  make(chan struct{}),
	}
	for _, in := range r.Interfaces {
		c, err := openInterface(in)
		if err != nil {
			d.closeHandles()
			return nil, err
		}
		d.ifaces = append(d.ifaces, c)
		d.infos = append(d.infos, in)
	}
	for _, c := range d.ifaces {
		if c.info.InLen > 0 {
			d.readers.Add(1)
			go d.readLoop(c)
		}
	}
	return d, nil
}

func openInterface(in Info) (*winIface, error) {
	p, err := windows.UTF16PtrFromString(in.Path)
	if err != nil {
		return nil, fmt.Errorf("hid: open %s: %w", in.Path, err)
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OVERLAPPED, 0)
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return nil, fmt.Errorf("%w: %s", ErrAccessDenied, in.Path)
	}
	if err != nil {
		return nil, fmt.Errorf("hid: open %s: %w", in.Path, err)
	}
	c := &winIface{info: in, h: h}
	if c.readOv.HEvent, err = windows.CreateEvent(nil, 1, 0, nil); err == nil {
		c.writeOv.HEvent, err = windows.CreateEvent(nil, 1, 0, nil)
	}
	if err != nil {
		c.closeHandles()
		return nil, fmt.Errorf("hid: CreateEvent: %w", err)
	}
	return c, nil
}

func (c *winIface) closeHandles() {
	for _, h := range []windows.Handle{c.h, c.readOv.HEvent, c.writeOv.HEvent} {
		if h != 0 && h != windows.InvalidHandle {
			windows.CloseHandle(h)
		}
	}
}

// wait waits for the overlapped operation on ov. If stop is signalled or
// the timeout passes first, it cancels the operation and waits for the
// cancellation to finish, so the OS is done with ov and its buffer before
// wait returns.
func (c *winIface) wait(ov *windows.Overlapped, stop windows.Handle, timeoutMs uint32) (int, error) {
	var n uint32
	ev, err := windows.WaitForMultipleObjects([]windows.Handle{ov.HEvent, stop}, false, timeoutMs)
	if err == nil && ev == windows.WAIT_OBJECT_0 {
		if err := windows.GetOverlappedResult(c.h, ov, &n, false); err != nil {
			return 0, err
		}
		return int(n), nil
	}
	windows.CancelIoEx(c.h, ov)
	finished := windows.GetOverlappedResult(c.h, ov, &n, true) == nil
	switch {
	case err != nil:
		return 0, err
	case ev == windows.WAIT_OBJECT_0+1:
		return 0, errStopped
	case finished: // completed just before the cancel
		return int(n), nil
	}
	return 0, errWriteTimeout
}

func (d *winDevice) readLoop(c *winIface) {
	defer d.readers.Done()
	for {
		buf := make([]byte, c.info.InLen)
		var n uint32
		err := windows.ReadFile(c.h, buf, &n, &c.readOv)
		if err == nil || err == windows.ERROR_IO_PENDING {
			var got int
			got, err = c.wait(&c.readOv, d.stop, windows.INFINITE)
			n = uint32(got)
		}
		runtime.KeepAlive(buf) // the OS wrote to it until wait returned
		if err == errStopped {
			return
		}
		if err != nil {
			d.fail(fmt.Errorf("hid: read %s: %w", c.info.Path, err))
			return
		}
		select {
		case d.reports <- buf[:n]:
		case <-d.done:
			return
		}
	}
}

func (d *winDevice) fail(err error) {
	d.failOnce.Do(func() {
		d.err = err
		close(d.failed)
	})
}

// Read returns the next input report from any collection.
func (d *winDevice) Read(ctx context.Context) ([]byte, error) {
	select {
	case <-d.done:
		return nil, errClosed
	case <-d.failed:
		return nil, d.err
	default:
	}
	select {
	case r := <-d.reports:
		return r, nil
	case <-d.failed:
		return nil, d.err
	case <-d.done:
		return nil, errClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Write sends report to the collection whose output length matches its
// report ID, zero-padded to that length.
func (d *winDevice) Write(report []byte) error {
	if len(report) == 0 {
		return errors.New("hid: empty report")
	}
	i, err := routeReport(d.infos, report[0])
	if err != nil {
		return err
	}
	c := d.ifaces[i]
	if len(report) > c.info.OutLen {
		return fmt.Errorf("hid: report %#02x is %d bytes, %s takes %d", report[0], len(report), c.info.Path, c.info.OutLen)
	}
	buf := make([]byte, c.info.OutLen)
	copy(buf, report)

	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	if d.closed {
		return errClosed
	}
	var n uint32
	err = windows.WriteFile(c.h, buf, &n, &c.writeOv)
	if err == nil || err == windows.ERROR_IO_PENDING {
		var got int
		got, err = c.wait(&c.writeOv, d.stop, writeTimeoutMs)
		n = uint32(got)
	}
	runtime.KeepAlive(buf) // the OS read from it until wait returned
	switch {
	case err == errStopped:
		return errClosed
	case err != nil:
		return fmt.Errorf("hid: write %s: %w", c.info.Path, err)
	case int(n) != len(buf):
		return fmt.Errorf("hid: write %s: %d of %d bytes", c.info.Path, n, len(buf))
	}
	return nil
}

// Close stops the readers, waits for them and for a pending write, then
// closes every handle.
func (d *winDevice) Close() error {
	d.closeOnce.Do(func() {
		close(d.done)
		windows.SetEvent(d.stop)
		d.readers.Wait()
		d.writeMu.Lock()
		d.closed = true
		d.writeMu.Unlock()
		d.closeHandles()
	})
	return nil
}

func (d *winDevice) closeHandles() {
	for _, c := range d.ifaces {
		c.closeHandles()
	}
	windows.CloseHandle(d.stop)
}
