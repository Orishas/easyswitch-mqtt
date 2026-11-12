package hidpp

import (
	"context"
	"fmt"
	"time"
)

// HID++ 2.0 feature IDs.
const (
	FeatRoot       uint16 = 0x0000
	FeatDeviceName uint16 = 0x0005
	FeatChangeHost uint16 = 0x1814
	FeatHostsInfo  uint16 = 0x1815
)

// maxHosts is how many hosts an Easy-Switch device has at most.
const maxHosts = 3

// pingData is the byte a ping echoes back.
const pingData byte = 0x5A

// HostInfo is a ChangeHost device's host count and current host.
type HostInfo struct {
	Count, Current int // Current 0-based
}

// FeatureIndex returns the index of feature id on the device, 0 when the
// device does not have it.
func (c *Conn) FeatureIndex(ctx context.Context, devIdx byte, id uint16) (byte, error) {
	rep, err := c.Request(ctx, devIdx, 0x00, 0, []byte{byte(id >> 8), byte(id), 0x00})
	if err != nil {
		return 0, err
	}
	return rep.Data[4], nil
}

// Ping sends a root ping and returns its round-trip time.
func (c *Conn) Ping(ctx context.Context, devIdx byte) (time.Duration, error) {
	rep, err := c.Request(ctx, devIdx, 0x00, 1, []byte{0x00, 0x00, pingData})
	if err != nil {
		return 0, err
	}
	return rep.Latency, nil
}

// DeviceName reads the device's name through the DeviceName feature at idx,
// one fragment at a time.
func (c *Conn) DeviceName(ctx context.Context, devIdx, idx byte) (string, error) {
	rep, err := c.Request(ctx, devIdx, idx, 0, nil)
	if err != nil {
		return "", err
	}
	length := int(rep.Data[4])
	name := make([]byte, 0, length)
	for len(name) < length {
		rep, err := c.Request(ctx, devIdx, idx, 1, []byte{byte(len(name))})
		if err != nil {
			return "", err
		}
		fragment := rep.Data[4:]
		name = append(name, fragment[:min(len(fragment), length-len(name))]...)
	}
	return string(name), nil
}

// HostInfo reads the host count and current host through the ChangeHost
// feature at idx.
func (c *Conn) HostInfo(ctx context.Context, devIdx, idx byte) (HostInfo, error) {
	rep, err := c.Request(ctx, devIdx, idx, 0, nil)
	if err != nil {
		return HostInfo{}, err
	}
	return HostInfo{Count: int(rep.Data[4]), Current: int(rep.Data[5])}, nil
}

// SetHost switches the device to host (0-based) through the ChangeHost
// feature at idx. The device never replies.
func (c *Conn) SetHost(devIdx, idx byte, host int) error {
	if host < 0 || host >= maxHosts {
		return fmt.Errorf("hidpp: host %d outside 0..%d", host, maxHosts-1)
	}
	return c.Send(devIdx, idx, 1, []byte{byte(host)})
}
