package hid

import (
	"reflect"
	"testing"
)

func TestGroupReceivers(t *testing.T) {
	infos := []Info{
		{Path: "z1", Group: "usb-z", Product: "USB Receiver", VendorID: LogitechVID, ProductID: BoltPID, UsagePage: 0xFF00, Usage: 0x02},
		{Path: "a1", Group: "usb-a", Product: "USB Receiver", VendorID: LogitechVID, ProductID: BoltPID, UsagePage: 0xFF00, Usage: 0x01},
		{Path: "a2", Group: "usb-a", Product: "USB Receiver", VendorID: LogitechVID, ProductID: BoltPID, UsagePage: 0x0001, Usage: 0x02},
		{Path: "a3", Group: "usb-a", Product: "USB Receiver", VendorID: LogitechVID, ProductID: BoltPID, UsagePage: 0xFF00, Usage: 0x02},
	}
	got := GroupReceivers(infos)
	want := []Receiver{
		{Group: "usb-a", Product: "USB Receiver", ProductID: BoltPID, Interfaces: []Info{infos[1], infos[3]}},
		{Group: "usb-z", Product: "USB Receiver", ProductID: BoltPID, Interfaces: []Info{infos[0]}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GroupReceivers:\n got %+v\nwant %+v", got, want)
	}
}

func TestGroupReceiversEmpty(t *testing.T) {
	if got := GroupReceivers([]Info{{Group: "g", UsagePage: 0x000C}}); len(got) != 0 {
		t.Fatalf("GroupReceivers = %+v, want none", got)
	}
}
