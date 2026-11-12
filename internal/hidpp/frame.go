// Package hidpp encodes HID++ requests and classifies the replies to them.
// It is pure and touches no hardware.
package hidpp

import "fmt"

// HID++ report IDs.
const Short, Long byte = 0x10, 0x11

// Feature-byte values that mark an error reply.
const (
	errHIDPP1 byte = 0x8F // HID++ 1.0 error report
	errHIDPP2 byte = 0xFF // HID++ 2.0 error report
)

// FrameLen returns the full frame length, report ID and padding included,
// for a known report ID.
func FrameLen(reportID byte) (int, bool) {
	switch reportID {
	case Short:
		return 7, true
	case Long:
		return 20, true
	}
	return 0, false
}

// funcSW packs a function number and software ID into one header byte.
func funcSW(fn, swID byte) byte { return fn<<4 | swID }

// Build encodes one HID++ request and zero-pads it to the report's frame length.
func Build(reportID, devIdx, featIdx, fn, swID byte, params []byte) ([]byte, error) {
	n, ok := FrameLen(reportID)
	if !ok {
		return nil, fmt.Errorf("hidpp: unknown report ID %#02x", reportID)
	}
	if 4+len(params) > n {
		return nil, fmt.Errorf("hidpp: %d params do not fit report %#02x", len(params), reportID)
	}
	frame := make([]byte, n)
	frame[0], frame[1], frame[2], frame[3] = reportID, devIdx, featIdx, funcSW(fn, swID)
	copy(frame[4:], params)
	return frame, nil
}

// Outcome is how a reply relates to a request.
type Outcome int

const (
	Ignore   Outcome = iota // not a reply to this request
	TooShort                // too short to judge
	Error                   // error reply to this request
	Match                   // reply to this request
)

// Classify matches a reply against a request's device index and full header,
// error replies included. A request to device index 0xFF also matches a
// reply from index 0.
func Classify(reply []byte, devIdx, featIdx, fn, swID byte) Outcome {
	if len(reply) < 5 {
		return TooShort
	}
	n, ok := FrameLen(reply[0])
	if !ok {
		return Ignore
	}
	if len(reply) < n {
		return TooShort
	}
	if reply[1] != devIdx && !(devIdx == 0xFF && reply[1] == 0) {
		return Ignore
	}
	if reply[2] == errHIDPP2 || reply[2] == errHIDPP1 {
		if len(reply) < 6 {
			return TooShort
		}
		if reply[3] == featIdx && reply[4] == funcSW(fn, swID) {
			return Error
		}
		return Ignore
	}
	if reply[2] != featIdx {
		return Ignore // unsolicited notification
	}
	if reply[3] != funcSW(fn, swID) {
		return Ignore
	}
	return Match
}

// ErrorCode returns the error code of an error reply and whether it is a
// HID++ 1.0 error. It is valid when Classify returned Error.
func ErrorCode(reply []byte) (code byte, hidpp1 bool) {
	if len(reply) < 6 {
		return 0, false
	}
	return reply[5], reply[2] == errHIDPP1
}
