// Package hid lists Logitech HID interfaces and opens a receiver for raw
// report I/O. Devices are always opened non-exclusively.
//
// Enumerate and Receiver.Open are implemented once per OS; everything in
// this file is pure.
package hid

import (
	"context"
	"errors"
	"sort"
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
