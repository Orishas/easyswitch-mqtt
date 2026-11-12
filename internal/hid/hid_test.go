package hid

import (
	"fmt"
	"reflect"
	"strings"
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

func TestWindowsGroup(t *testing.T) {
	const col1 = `\\?\HID#VID_046D&PID_C548&MI_02&Col01#8&1a2b3c&0&0000#{4d1e55b2-f16f-11cf-88cb-001111000030}`
	const col2 = `\\?\hid#vid_046d&pid_c548&mi_02&col02#8&1a2b3c&0&0000#{4d1e55b2-f16f-11cf-88cb-001111000030}`
	const want = `\\?\hid#vid_046d&pid_c548&mi_02#8&1a2b3c&0&0000#{4d1e55b2-f16f-11cf-88cb-001111000030}`
	if g1, g2 := windowsGroup(col1), windowsGroup(col2); g1 != want || g2 != want {
		t.Fatalf("windowsGroup:\n col01 → %s\n col02 → %s\nwant %s", g1, g2, want)
	}
	const single = `\\?\hid#vid_046d&pid_c548&mi_00#8&2c3d4e&0&0000#{4d1e55b2-f16f-11cf-88cb-001111000030}`
	if g := windowsGroup(single); g != single {
		t.Fatalf("windowsGroup(%s) = %s, want it unchanged", single, g)
	}
	if windowsGroup(single) == windowsGroup(col1) {
		t.Fatal("different devices share a Group")
	}
}

func TestRouteReport(t *testing.T) {
	both := []Info{{Path: "short", OutLen: 7}, {Path: "long", OutLen: 20}}
	for _, c := range []struct {
		ifaces []Info
		id     byte
		want   int
	}{
		{both, 0x10, 0},
		{both, 0x11, 1},
		{[]Info{{OutLen: 20}, {OutLen: 7}}, 0x10, 1},
		{[]Info{{OutLen: 0}}, 0x10, 0},
		{[]Info{{OutLen: 0}}, 0x11, 0},
		{[]Info{{OutLen: 0}}, 0x12, 0},
	} {
		got, err := routeReport(c.ifaces, c.id)
		if err != nil || got != c.want {
			t.Errorf("routeReport(%+v, %#02x) = (%d, %v), want %d", c.ifaces, c.id, got, err, c.want)
		}
	}
	for _, c := range []struct {
		ifaces []Info
		id     byte
	}{
		{both, 0x12},
		{[]Info{{OutLen: 7}, {OutLen: 7}}, 0x11},
		{nil, 0x11},
	} {
		_, err := routeReport(c.ifaces, c.id)
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("%#02x", c.id)) {
			t.Errorf("routeReport(%+v, %#02x) error = %v, want one naming the report ID", c.ifaces, c.id, err)
		}
	}
}

func TestWithReportID(t *testing.T) {
	for _, c := range []struct {
		id       uint32
		in, want []byte
	}{
		{0x11, []byte{0x11, 0x01, 0x02}, []byte{0x11, 0x01, 0x02}}, // ID already in the buffer
		{0x11, []byte{0x01, 0x02}, []byte{0x11, 0x01, 0x02}},       // ID left out
		{0x10, []byte{0xFF, 0x00}, []byte{0x10, 0xFF, 0x00}},
		{0, []byte{0x01, 0x02}, []byte{0x01, 0x02}}, // no report IDs on this device
	} {
		if got := withReportID(c.id, append([]byte(nil), c.in...)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("withReportID(%#x, % x) = % x, want % x", c.id, c.in, got, c.want)
		}
	}
}
