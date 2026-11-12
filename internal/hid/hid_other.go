//go:build !windows && !darwin

package hid

// Enumerate is not available on this OS.
func Enumerate() ([]Info, error) { return nil, ErrUnsupported }

// Open is not available on this OS.
func (r Receiver) Open() (Device, error) { return nil, ErrUnsupported }
