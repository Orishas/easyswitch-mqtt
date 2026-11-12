// Package hid lists Logitech HID interfaces and opens a receiver for raw
// report I/O. Devices are always opened non-exclusively.
//
// Enumerate and Receiver.Open are implemented once per OS; everything in
// this file is pure.
package hid

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Logitech vendor ID and the Bolt receiver's product ID.
const (
	LogitechVID uint16 = 0x046D
	BoltPID     uint16 = 0xC548
)

var (
	// ErrUnsupported: this OS has no HID backend.
	ErrUnsupported = errors.New("hid: platform not supported")
	// ErrAccessDenied: the OS refused to open the device.
	ErrAccessDenied = errors.New("hid: access denied")

	errClosed = errors.New("hid: device closed")
)

// Info describes one HID interface (top-level collection).
type Info struct {
	Path, Group, Product string // Group: OS identity shared by all collections of one USB device
	VendorID, ProductID  uint16
	UsagePage, Usage     uint16
	InLen, OutLen        int // 0 when the OS does not tell
}

// Receiver is one USB device with its vendor-defined collections.
type Receiver struct {
	Group, Product string
	ProductID      uint16
	Interfaces     []Info
}

// Device is an open receiver.
type Device interface {
	Write(report []byte) error                // report[0] is the report ID
	Read(ctx context.Context) ([]byte, error) // one input report incl. report ID; ctx.Err() on cancel
	Close() error
}

// vendorUsagePage is the first vendor-defined usage page; HID++ lives there.
const vendorUsagePage uint16 = 0xFF00

// GroupReceivers keeps the vendor-defined collections and groups them by
// Group, in input order within a group. Receivers are sorted by Group.
func GroupReceivers(infos []Info) []Receiver {
	byGroup := map[string]int{}
	var out []Receiver
	for _, in := range infos {
		if in.UsagePage < vendorUsagePage {
			continue
		}
		i, ok := byGroup[in.Group]
		if !ok {
			i = len(out)
			byGroup[in.Group] = i
			out = append(out, Receiver{Group: in.Group, Product: in.Product, ProductID: in.ProductID})
		}
		out[i].Interfaces = append(out[i].Interfaces, in)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Group < out[b].Group })
	return out
}

// windowsCollection matches the "&colNN" part of a lower-cased Windows HID
// device path; it is the only part that differs between the collections of
// one USB interface.
var windowsCollection = regexp.MustCompile(`&col[0-9a-f]+`)

// windowsGroup turns a Windows HID device path into its Group: the path
// lower-cased with "&colNN" removed.
func windowsGroup(path string) string {
	return windowsCollection.ReplaceAllString(strings.ToLower(path), "")
}

// frameLen is the HID++ frame length, report ID included, of a known report
// ID. It mirrors hidpp.FrameLen, which this package cannot import.
func frameLen(reportID byte) int {
	switch reportID {
	case 0x10:
		return 7
	case 0x11:
		return 20
	}
	return 0
}

// routeReport picks the interface that takes output reports with this ID:
// the one whose OutLen is the report's frame length or, failing that, the
// only interface there is (its OS does not tell OutLen, or one object
// carries every report ID, as on macOS).
func routeReport(ifaces []Info, reportID byte) (int, error) {
	if n := frameLen(reportID); n > 0 {
		for i, in := range ifaces {
			if in.OutLen == n {
				return i, nil
			}
		}
	}
	if len(ifaces) == 1 {
		return 0, nil
	}
	return 0, fmt.Errorf("hid: no interface takes report %#02x", reportID)
}

// withReportID makes an input report start with its report ID. IOKit is
// inconsistent about whether its callback buffer includes the ID; HID++
// reports never start with a byte equal to their own ID unless it is the
// ID, so compare and prepend when it is missing (as mxswitch's macOS code
// does). reportID 0 means the device uses no report IDs.
func withReportID(reportID uint32, report []byte) []byte {
	if reportID == 0 || (len(report) > 0 && report[0] == byte(reportID)) {
		return report
	}
	return append([]byte{byte(reportID)}, report...)
}
