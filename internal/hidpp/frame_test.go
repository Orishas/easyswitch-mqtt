package hidpp

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

type fixtures struct {
	SoftwareID byte `json:"software_id"`
	Frames     []struct {
		Name         string `json:"name"`
		ReportID     byte   `json:"report_id"`
		DeviceIndex  byte   `json:"device_index"`
		FeatureIndex byte   `json:"feature_index"`
		Function     byte   `json:"function"`
		Params       []int  `json:"params"`
		Hex          string `json:"hex"`
	} `json:"frames"`
	Replies []struct {
		Name         string `json:"name"`
		Hex          string `json:"hex"`
		DeviceIndex  byte   `json:"device_index"`
		FeatureIndex byte   `json:"feature_index"`
		Function     byte   `json:"function"` // 0 when absent
		Outcome      string `json:"outcome"`
	} `json:"replies"`
}

func loadFixtures(t *testing.T) fixtures {
	t.Helper()
	data, err := os.ReadFile("testdata/frames.json")
	if err != nil {
		t.Fatal(err)
	}
	var f fixtures
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Frames) == 0 || len(f.Replies) == 0 {
		t.Fatal("fixture file has no frames or no replies")
	}
	return f
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex %q: %v", s, err)
	}
	return b
}

func TestBuildMatchesFixtures(t *testing.T) {
	f := loadFixtures(t)
	for _, c := range f.Frames {
		t.Run(c.Name, func(t *testing.T) {
			params := make([]byte, len(c.Params))
			for i, p := range c.Params {
				params[i] = byte(p)
			}
			got, err := Build(c.ReportID, c.DeviceIndex, c.FeatureIndex, c.Function, f.SoftwareID, params)
			if err != nil {
				t.Fatal(err)
			}
			if want := mustHex(t, c.Hex); !bytes.Equal(got, want) {
				t.Errorf("Build = %x, want %x", got, want)
			}
		})
	}
}

func TestClassifyMatchesFixtures(t *testing.T) {
	names := map[string]Outcome{"ignore": Ignore, "too_short": TooShort, "error": Error, "match": Match}
	f := loadFixtures(t)
	for _, c := range f.Replies {
		t.Run(c.Name, func(t *testing.T) {
			want, ok := names[c.Outcome]
			if !ok {
				t.Fatalf("unknown outcome %q in fixture", c.Outcome)
			}
			got := Classify(mustHex(t, c.Hex), c.DeviceIndex, c.FeatureIndex, c.Function, f.SoftwareID)
			if got != want {
				t.Errorf("Classify = %d, want %d (%s)", got, want, c.Outcome)
			}
		})
	}
}

func TestFrameLen(t *testing.T) {
	cases := []struct {
		id   byte
		n    int
		okay bool
	}{{Short, 7, true}, {Long, 20, true}, {0x12, 0, false}}
	for _, c := range cases {
		n, ok := FrameLen(c.id)
		if n != c.n || ok != c.okay {
			t.Errorf("FrameLen(%#x) = (%d, %v), want (%d, %v)", c.id, n, ok, c.n, c.okay)
		}
	}
}

func TestBuildRejects(t *testing.T) {
	if _, err := Build(0x12, 0xFF, 0, 0, 0x0A, nil); err == nil {
		t.Error("unknown report ID: want error")
	}
	if _, err := Build(Short, 0xFF, 0, 0, 0x0A, make([]byte, 4)); err == nil {
		t.Error("short report with 4 params (8 bytes): want error")
	}
	if _, err := Build(Long, 0xFF, 0, 0, 0x0A, make([]byte, 17)); err == nil {
		t.Error("long report with 17 params (21 bytes): want error")
	}
	if _, err := Build(Long, 0xFF, 0, 0, 0x0A, make([]byte, 16)); err != nil {
		t.Errorf("long report with 16 params fills the frame exactly: %v", err)
	}
}

func TestErrorCode(t *testing.T) {
	cases := []struct {
		reply  string
		code   byte
		hidpp1 bool
	}{
		{"1002" + "8f001a0900", 0x09, true},
		{"1102" + "ff001a05" + "0000000000000000000000000000", 0x05, false},
	}
	for _, c := range cases {
		code, hidpp1 := ErrorCode(mustHex(t, c.reply))
		if code != c.code || hidpp1 != c.hidpp1 {
			t.Errorf("ErrorCode(%s) = (%#x, %v), want (%#x, %v)", c.reply, code, hidpp1, c.code, c.hidpp1)
		}
	}
}
