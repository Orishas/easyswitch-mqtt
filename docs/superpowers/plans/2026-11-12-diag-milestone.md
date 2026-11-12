# easyswitch diag milestone Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A single `easyswitch` binary for Windows and macOS that measures how Logi Bolt devices behave (`diag`) and can switch them by hand (`switch N`), so the service design can be fixed on numbers instead of guesses.

**Architecture:** A pure HID++ layer (`internal/hidpp`) talks to an abstract HID device (`internal/hid`), with one native backend per OS and a scripted fake receiver for tests. `internal/diag` runs probes against that layer and writes a plain-text report. `internal/bootdiag` installs a temporary system service to run the same probes before login. No MQTT in this plan: the service, Home Assistant side and installers are plan 2, written after the measurements are back.

**Tech Stack:** Go 1.24, `golang.org/x/sys/windows`, cgo + IOKit on macOS, `github.com/kardianos/service` for the temporary system service, GitHub Actions.

**Spec:** `docs/superpowers/specs/2026-11-12-easyswitch-mqtt-design.md` (German). This plan implements its section "Erster Meilenstein" plus the `switch N` command.

## Global Constraints

- Module path `github.com/Orishas/easyswitch-mqtt`. Binary name `easyswitch`. License MIT.
- Repo language is English: code, README, commit messages, report text.
- Targets: Windows amd64 and arm64, macOS universal (arm64 + amd64). Linux must compile and pass unit tests; its HID backend returns `hid.ErrUnsupported`.
- Devices are opened **non-exclusively**. Nothing in this plan may seize a device or swallow replies meant for Logi Options+.
- Only Logitech vendor ID `0x046D`. Bolt receiver product ID `0xC548`. Devices sit at receiver slots 1 to 6; all requests use long reports (`0x11`, 20 bytes).
- ChangeHost is feature `0x1814`: function 0 = getHostInfo (reply params: host count, current host 0-based), function 1 = setCurrentHost (param: host 0-based; **never replies**). Hosts info is feature `0x1815`; this plan only dumps its raw replies.
- Channels are 1-based everywhere a person sees them, 0-based only on the wire.
- No third-party runtime, no window, no tray. No config file in this plan.
- Test frames taken from `marcocosta97/mxswitch` (MIT) carry a notice in `NOTICE` and in the fixture file.
- Both native backends are untestable in CI beyond compiling. The reference for their OS calls is mxswitch's `python/mxswitch.py` (Windows, ctypes over setupapi/hid.dll) and `macos/mxswitch.c` (IOKit), cloned at `/home/user/ref/mxswitch`. Read them before writing a backend.

## Review Focus

1. **No receiver, or access denied** (macOS without Input Monitoring, receiver unplugged): every command prints one plain sentence saying which of the two it was and exits 1. No panic, no stack trace. → Task 6.
2. **Foreign traffic on the same receiver** (Options+ polls the devices; its replies carry another software ID, and unsolicited notifications arrive at any time): a request must never take such a frame for its own reply. → Task 2.
3. **Other Logitech hardware present** (a webcam, a Unifying receiver `0xC52B`, a second Bolt receiver, a wired G-series mouse): enumeration lists them, `diag` probes each receiver separately and labels it, nothing crashes on a device that never answers. → Task 5.
4. **Interrupted run** (Ctrl-C during a 30-minute idle test, receiver unplugged mid-run): the report written so far is flushed to the file and its path is printed. → Task 5.
5. **A device answers with an error instead of silence** (HID++ 1.0 error `0x8F` from the receiver for an unreachable slot, HID++ 2.0 error `0xFF`): the report shows the error code and the latency of the error, distinct from a timeout. → Task 2 and Task 5.

---

## File Structure

```
go.mod, LICENSE, NOTICE, README.md, .gitignore
cmd/easyswitch/main.go            command dispatch only: diag, switch, version
internal/hid/hid.go               Info, Receiver, Device, errors; pure grouping logic
internal/hid/hid_windows.go       setupapi + hid.dll + overlapped I/O
internal/hid/hid_darwin.go        IOKit via cgo
internal/hid/hid_other.go         ErrUnsupported (linux and the rest)
internal/hidpp/frame.go           Build, Classify
internal/hidpp/conn.go            Conn: request/reply matching, frame fan-out
internal/hidpp/features.go        FeatureIndex, Ping, DeviceName, HostInfo, SetHost
internal/hidpp/testdata/frames.json   fixtures from mxswitch
internal/hidtest/receiver.go      scripted fake Bolt receiver implementing hid.Device
internal/diag/diag.go             Run, Options, section order
internal/diag/probe.go            per-slot probe, idle test, watch
internal/diag/report.go           plain-text report writer
internal/switcher/switcher.go     Switch: patient discovery, send, confirm
internal/bootdiag/bootdiag.go     temporary system service, report file
.github/workflows/ci.yml, release.yml
```

---

### Task 1: Repo skeleton and HID++ frames

**Files:**
- Create: `go.mod`, `LICENSE`, `NOTICE`, `.gitignore`, `README.md` (title, one paragraph, "work in progress")
- Create: `internal/hidpp/frame.go`, `internal/hidpp/frame_test.go`, `internal/hidpp/testdata/frames.json`

**Interfaces:**
- Produces:
  ```go
  const Short, Long byte = 0x10, 0x11
  func FrameLen(reportID byte) (int, bool)              // 7, 20
  func Build(reportID, devIdx, featIdx, fn, swID byte, params []byte) ([]byte, error)
  type Outcome int
  const ( Ignore Outcome = iota; TooShort; Error; Match )
  func Classify(reply []byte, devIdx, featIdx, fn, swID byte) Outcome
  func ErrorCode(reply []byte) (code byte, hidpp1 bool) // valid when Classify returned Error
  ```

- [ ] **Step 1:** `git init`, `go mod init github.com/Orishas/easyswitch-mqtt`, copy `/home/user/ref/mxswitch/tests/fixtures/frames.json` to `internal/hidpp/testdata/frames.json`, add `NOTICE` naming mxswitch, its author and MIT.
- [ ] **Step 2: Write the failing tests** in `frame_test.go`:
  - `TestBuildMatchesFixtures`: for every entry in `frames`, `Build(report_id, device_index, feature_index, function, software_id, params)` equals `hex`.
  - `TestClassifyMatchesFixtures`: for every entry in `replies`, `Classify(hex, device_index, feature_index, function (0 when the entry has none), software_id)` returns the entry's `outcome` (`match`, `error`, `ignore`, `too_short`).
  - `TestBuildRejects`: unknown report ID and params longer than the frame return an error.
  - `TestErrorCode`: `10 02 8f 00 1a 09 00` → `(0x09, true)`; `11 02 ff 00 1a 05 …` → `(0x05, false)`.
- [ ] **Step 3:** Run `go test ./internal/hidpp/` → FAIL (undefined).
- [ ] **Step 4:** Implement `frame.go`. Port the rules of `classify_reply` in `/home/user/ref/mxswitch/python/mxswitch.py:65-86` exactly, including "device index 0xFF also matches a reply from index 0".
- [ ] **Step 5:** Run `go test ./internal/hidpp/` → PASS.
- [ ] **Step 6:** Commit `Skeleton and HID++ frame encoding with mxswitch's fixtures`.

### Task 2: HID device abstraction, fake receiver, Conn

**Files:**
- Create: `internal/hid/hid.go`, `internal/hid/hid_other.go`, `internal/hid/hid_test.go`
- Create: `internal/hidtest/receiver.go`
- Create: `internal/hidpp/conn.go`, `internal/hidpp/conn_test.go`

**Interfaces:**
- Produces (`internal/hid`):
  ```go
  const LogitechVID uint16 = 0x046D
  const BoltPID uint16 = 0xC548
  var ErrUnsupported, ErrAccessDenied error
  type Info struct {
      Path, Group, Product  string   // Group: OS identity shared by all collections of one USB device
      VendorID, ProductID   uint16
      UsagePage, Usage      uint16
      InLen, OutLen         int      // 0 when the OS does not tell
  }
  type Receiver struct { Group, Product string; ProductID uint16; Interfaces []Info }
  type Device interface {
      Write(report []byte) error                       // report[0] is the report ID
      Read(ctx context.Context) ([]byte, error)        // one input report incl. report ID; ctx.Err() on cancel
      Close() error
  }
  func Enumerate() ([]Info, error)          // per OS; every Logitech HID interface
  func GroupReceivers(infos []Info) []Receiver   // pure: vendor collections (UsagePage >= 0xFF00) grouped by Group, sorted by Group
  func (r Receiver) Open() (Device, error)  // per OS; routes a write to the collection that takes that report ID, merges reads
  ```
- Produces (`internal/hidtest`):
  ```go
  type Slot struct {
      Name     string
      Features map[uint16]byte     // feature ID → index
      Hosts, Current int           // Current 0-based
      Awake    bool                // false: requests are dropped (timeout)
      Offline  bool                // true: receiver answers with HID++ 1.0 error 0x8F code 0x09
      Delay    time.Duration       // added before each reply
  }
  type Receiver struct{ /* implements hid.Device */ }
  func NewReceiver(slots map[byte]*Slot) *Receiver
  func (r *Receiver) Inject(frame []byte)         // unsolicited frame to the reader
  func (r *Receiver) Sent() [][]byte              // every frame written, in order
  func (r *Receiver) Unplug()                     // Read and Write return an error from now on
  ```
  The fake answers root getFeature, root ping, device name (`0x0005`), getHostInfo; on setCurrentHost it sets `Current`, marks the slot `Offline` and sends nothing.
- Produces (`internal/hidpp`):
  ```go
  type Frame struct { At time.Time; Data []byte }
  type Reply struct { Data []byte; Latency time.Duration }
  var ErrTimeout error
  type DeviceError struct { Code byte; HIDPP1 bool; Latency time.Duration }  // implements error
  func NewConn(dev hid.Device) *Conn
  func (c *Conn) Subscribe() (frames <-chan Frame, cancel func())   // every input frame, buffered 256, drops when full
  func (c *Conn) Request(ctx context.Context, devIdx, featIdx, fn byte, params []byte) (Reply, error)
  func (c *Conn) Send(devIdx, featIdx, fn byte, params []byte) error   // no reply expected
  func (c *Conn) Err() error      // read error that ended the reader, nil while healthy
  func (c *Conn) Close() error
  ```
  `Request` always uses a long report, rotates the software ID through `0x8..0xF`, and serialises requests (one in flight). Its deadline is the context's.

- [ ] **Step 1: Write the failing tests.**
  - `hid_test.go` `TestGroupReceivers`: two infos with the same `Group` and usage pages `0xFF00` become one `Receiver` with two interfaces; an info with usage page `0x0001` is left out; a second group yields a second receiver; output is sorted by `Group`.
  - `conn_test.go`:
    - `TestRequestReply`: fake with slot 2 awake → `Request` for root getFeature `0x1814` returns the index, `Latency > 0`.
    - `TestRequestTimeout`: slot asleep, context of 50 ms → `ErrTimeout`.
    - `TestRequestDeviceError`: slot `Offline` → `*DeviceError{Code: 0x09, HIDPP1: true}`.
    - `TestForeignRepliesIgnored` (Review Focus 2): before the real reply, inject a reply for the same device and feature with software ID `0x01`, a reply for another device index, and a `0x41` notification; `Request` still returns the real reply, and a subscriber received all four frames.
    - `TestLateReplyOfPreviousRequestIgnored`: first request times out, its reply arrives during the second request, which gets its own reply.
    - `TestUnplug`: `Unplug()` during a request → the request returns an error that is not `ErrTimeout`, `Err()` is non-nil.
- [ ] **Step 2:** Run `go test ./internal/...` → FAIL.
- [ ] **Step 3:** Implement `hid.go`, `hid_other.go` (build tag `!windows && !darwin`; `Enumerate` and `Open` return `ErrUnsupported`), `hidtest/receiver.go`, `conn.go` (one reader goroutine; fan out to subscribers and to the pending request).
- [ ] **Step 4:** Run `go test -race ./internal/...` → PASS.
- [ ] **Step 5:** Commit `HID abstraction, scripted fake receiver, request matching`.

### Task 3: Features

**Files:**
- Create: `internal/hidpp/features.go`, `internal/hidpp/features_test.go`

**Interfaces:**
- Consumes: `Conn.Request`, `Conn.Send`, `hidtest.Receiver`.
- Produces:
  ```go
  const ( FeatRoot uint16 = 0x0000; FeatDeviceName uint16 = 0x0005; FeatChangeHost uint16 = 0x1814; FeatHostsInfo uint16 = 0x1815 )
  type HostInfo struct { Count, Current int }   // Current 0-based
  func (c *Conn) FeatureIndex(ctx context.Context, devIdx byte, id uint16) (byte, error)  // 0, nil when the device does not have it
  func (c *Conn) Ping(ctx context.Context, devIdx byte) (time.Duration, error)            // root function 1, params {0,0,0x5A}
  func (c *Conn) DeviceName(ctx context.Context, devIdx, idx byte) (string, error)
  func (c *Conn) HostInfo(ctx context.Context, devIdx, idx byte) (HostInfo, error)
  func (c *Conn) SetHost(devIdx, idx byte, host int) error                                // host 0-based, 0..2
  ```

- [ ] **Step 1: Write the failing tests** against a fake with slot 1 "MX Keys S" (`0x1814`→`0x0a`, 3 hosts, current 0) and slot 2 "MX Master 4" (`0x1814`→`0x0e`):
  - `TestFeatureIndex`: `0x0a`, `0x0e`; an unknown feature → `0, nil`.
  - `TestDeviceName`: a 11-character name arrives complete across fragments.
  - `TestHostInfo`: `{3, 0}`.
  - `TestSetHost`: the frame in `Sent()` is `11 02 0e 1? 01 00…` (function 1 in the high nibble); the fake's slot 2 is on host 1 afterwards; `SetHost(…, 3)` returns an error and sends nothing.
  - `TestPing`: returns the fake's `Delay` within tolerance.
- [ ] **Step 2:** Run → FAIL. **Step 3:** Implement (name fragments as in `/home/user/ref/mxswitch/python/mxswitch.py:467-479`). **Step 4:** Run → PASS.
- [ ] **Step 5:** Commit `Features: lookup, ping, name, host info, set host`.

### Task 4: Native backends

**Files:**
- Create: `internal/hid/hid_windows.go`, `internal/hid/hid_darwin.go`
- Create: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes/implements: `Enumerate`, `Receiver.Open`, `Device` from Task 2.

- [ ] **Step 1: Windows.** `Enumerate` via `SetupDiGetClassDevs`/`SetupDiEnumDeviceInterfaces` on the HID GUID, `HidD_GetAttributes`, `HidP_GetCaps`, `HidD_GetProductString`; keep vendor `0x046D`. `Group` is the device path lower-cased with `&colNN` removed. `Open` opens every interface with `GENERIC_READ|GENERIC_WRITE`, share read+write, `FILE_FLAG_OVERLAPPED`; a write goes to the interface whose `OutLen` equals `FrameLen(report[0])` and is padded to `OutLen`; one reader goroutine per interface feeds a merged channel. `ERROR_ACCESS_DENIED` maps to `ErrAccessDenied`. Measured on the target machine: the Bolt receiver shows `ff00:0001` (in/out 7) and `ff00:0002` (in/out 20).
- [ ] **Step 2: macOS.** cgo, `#cgo LDFLAGS: -framework IOKit -framework CoreFoundation`. `IOHIDManager` matching vendor `0x046D`, opened with `kIOHIDOptionsTypeNone`. `Group` is the location ID plus product ID. `Write` uses `IOHIDDeviceSetReport(kIOHIDReportTypeOutput, report[0], …)`. Input reports arrive through `IOHIDDeviceRegisterInputReportCallback` on a dedicated OS thread running a CFRunLoop (`runtime.LockOSThread`); check against `macos/mxswitch.c` whether the callback buffer includes the report ID and normalise so `Read` always returns it. `kIOReturnNotPermitted` and `kIOReturnNotPrivileged` map to `ErrAccessDenied`.
- [ ] **Step 3:** `ci.yml`: on push and pull request, matrix `ubuntu-latest`, `windows-latest`, `macos-latest`; steps `go vet ./...`, `go test -race ./...`, `go build ./...`. Add a job that cross-builds `GOOS=windows GOARCH=arm64`.
- [ ] **Step 4:** Locally: `GOOS=windows go vet ./... && GOOS=windows go build ./...` → no output. (macOS cannot be checked locally; CI does.)
- [ ] **Step 5:** Commit `Native HID backends for Windows and macOS, CI`.

### Task 5: diag

**Files:**
- Create: `internal/diag/diag.go`, `internal/diag/probe.go`, `internal/diag/report.go`, `internal/diag/diag_test.go`

**Interfaces:**
- Consumes: `hid.Receiver`, `hidpp.Conn` and features.
- Produces:
  ```go
  type Options struct {
      Idle     []time.Duration   // default 10s, 1m, 5m, 30m; empty skips the idle test
      Watch    time.Duration     // default 0 = skip
      Timeout  time.Duration     // per request, default 5s
      Out      io.Writer         // report
      Progress io.Writer         // what the person should do now
      Now      func() time.Time  // tests
      Sleep    func(context.Context, time.Duration) error  // tests
  }
  type Opener func() ([]hid.Receiver, func(hid.Receiver) (hid.Device, error), error)
  func Run(ctx context.Context, open Opener, o Options) error
  ```
- Report sections, in this order, each starting with a line `== <name> ==`:
  1. `system`: easyswitch version, OS, arch, whether running as a service, time.
  2. `interfaces`: one line per `hid.Info` (usage page:usage, pid, in, out, product).
  3. Per receiver, `receiver <product> pid=<pid>`:
     - `slots`: for slot 1..6: latency or `timeout` or `error 0x.. (hid++1|2)` of root getFeature `0x1814`; if found: name, feature index, host count, current channel (1-based).
     - `hosts-info`: if `0x1815` exists: raw hex of function 0, and of function 1 with params `{0}`, `{1}`, `{2}`.
     - `idle`: for each duration: after asking the person not to touch the devices, wait, then `Ping` each found slot once; one line per slot with latency, `timeout` or error. Then ask the person to move the mouse and press a key, wait 5 s, ping again (line prefix `after-wake`).
     - `watch`: every frame for `Watch`, as `+<seconds>.<ms> <hex>`, while `Progress` asks: press a key, wait 20 s, press Easy-Switch to another channel, come back.
  4. `unsolicited`: every frame seen during the whole run that was not the reply to a diag request, with its offset.

- [ ] **Step 1: Write the failing tests** with fakes, a fake clock and an immediate `Sleep`:
  - `TestReportSlots`: slots 1 and 2 as in Task 3, slot 3 `Offline` → the `slots` section names both devices with channel 1 and shows `error 0x09 (hid++1)` for slot 3 and `timeout` for 4 to 6.
  - `TestIdleDistinguishesTimeoutAndError` (Review Focus 5): slot 1 asleep, slot 2 offline during the idle ping → `timeout` and `error 0x09`.
  - `TestTwoReceiversAndForeignDevice` (Review Focus 3): opener returns a Bolt receiver, a receiver with pid `0xC52B` whose device never answers, → two `receiver` sections, the second with six `timeout` lines, no error returned.
  - `TestCancelFlushes` (Review Focus 4): cancel the context during the first idle wait → `Run` returns `context.Canceled`, and `Out` contains the complete `slots` section and a final line `== interrupted ==`.
  - `TestUnplugMidRun` (Review Focus 4): `Unplug()` during idle → section ends with `receiver lost: <error>`, the next receiver is still probed.
  - `TestUnsolicitedLogged`: an injected `0x41` frame appears under `unsolicited`.
- [ ] **Step 2:** Run → FAIL. **Step 3:** Implement. **Step 4:** Run `go test -race ./internal/diag/` → PASS.
- [ ] **Step 5:** Commit `diag: slots, hosts info, idle latency, watch, unsolicited frames`.

### Task 6: switch and the command line

**Files:**
- Create: `internal/switcher/switcher.go`, `internal/switcher/switcher_test.go`
- Create: `cmd/easyswitch/main.go`, `cmd/easyswitch/main_test.go`

**Interfaces:**
- Consumes: everything above.
- Produces:
  ```go
  // switcher
  type Result struct { Name string; Slot byte; From int; Outcome string }  // Outcome: "left", "already", "asleep", "error: …"; From 1-based
  func Switch(ctx context.Context, conn *hidpp.Conn, channel int, patience time.Duration) ([]Result, error)
  ```
  `Switch` asks slots 1..6 for `0x1814` repeatedly until `patience` (default 8 s) has passed or every slot that answered once is handled; sends `SetHost`; a device counts as `left` when a following `Ping` returns a `DeviceError` or times out within 2 s, otherwise `SetHost` is sent again, three times at most.
- Command line (`main.go`, standard `flag`, no framework):
  ```
  easyswitch diag [--idle 10s,1m,5m,30m] [--watch 2m] [--quick] [--out FILE]
  easyswitch switch N
  easyswitch version
  ```
  `--quick` = no idle test, no watch. Without `--out` the report goes to `easyswitch-diag-<yyyymmdd-hhmmss>.txt` in the current directory and its path is printed last. Exit codes: 0 ok, 1 no receiver or access denied or nothing switched, 2 usage.
  Messages (exact):
  - no receiver: `No Logitech receiver found. Plug in the Logi Bolt receiver and try again.`
  - access denied on macOS: `macOS refused access to the receiver. Allow this program under System Settings > Privacy & Security > Input Monitoring, then try again.`
  - access denied elsewhere: `Access to the receiver was refused.`

- [ ] **Step 1: Write the failing tests.**
  - `TestSwitchBoth`: both slots awake on channel 1, `Switch(…, 2, …)` → two results `left`, fake slots on host 1.
  - `TestSwitchAlready`: device on channel 2 → `already`, nothing sent.
  - `TestSwitchLateWaker`: slot 2 asleep, wakes after 3 s of fake time → `left` for both.
  - `TestSwitchAsleep`: slot 1 answered `Offline` for the whole patience → no result for it; with no device at all the error is `no device answered`.
  - `TestSwitchChannelOutOfRange`: channel 4 on a 3-host device → `error: device has 3 channels`, nothing sent.
  - `main_test.go` (Review Focus 1): run the dispatcher with an injected opener returning no receivers → stderr is the exact "No Logitech receiver found…" line, exit 1; opener returning `hid.ErrAccessDenied` with GOOS `darwin` → the Input Monitoring sentence, exit 1; `switch 0` and `switch x` → exit 2.
- [ ] **Step 2:** Run → FAIL. **Step 3:** Implement. **Step 4:** Run `go test -race ./...` → PASS; `go build -o /tmp/easyswitch ./cmd/easyswitch && /tmp/easyswitch version` prints a version; `/tmp/easyswitch diag --quick` on Linux prints `This platform is not supported yet.` and exits 1.
- [ ] **Step 5:** Commit `switch command and command line`.

### Task 7: boot diag

**Files:**
- Create: `internal/bootdiag/bootdiag.go`, `internal/bootdiag/bootdiag_test.go`
- Modify: `cmd/easyswitch/main.go`

**Interfaces:**
- Consumes: `diag.Run`, `github.com/kardianos/service`.
- Produces:
  ```go
  func Install() error          // registers service "easyswitch-bootdiag" running `easyswitch diag --boot-run`, start automatic
  func Run(ctx context.Context) error   // as the service: every 15 s for 5 min after start, append a quick diag
  func Report(w io.Writer) error        // print the file
  func Uninstall() error                // remove service and file
  func ReportPath() string  // %ProgramData%\easyswitch\boot-diag.txt, /Library/Application Support/easyswitch/boot-diag.txt
  func SessionState() string            // "before login" | "logged in: <user>" | "unknown"
  ```
  Each appended run starts with `== boot-run <n> at <time>, <SessionState()> ==`. `SessionState`: macOS = owner of `/dev/console` (`root` means before login); Windows = active console session ID and its user name through `WTSQuerySessionInformation` (empty user name means before login).
- Command line additions: `easyswitch diag --boot-install`, `--boot-run` (hidden), `--boot-report`, `--boot-remove`. `--boot-install` prints: `Installed. Restart the computer, wait one minute at the login screen while moving the mouse and pressing Shift on the keyboard, then log in and run: easyswitch diag --boot-report`.

- [ ] **Step 1: Write the failing tests:** `TestRunAppends` (fake clock, fake opener: after the fake 5 minutes the file holds 20 `== boot-run` headers, each followed by a `slots` section); `TestReportMissingFile` → error text `No boot report yet. Did the computer restart after --boot-install?`.
- [ ] **Step 2:** Run → FAIL. **Step 3:** Implement. **Step 4:** Run → PASS; `GOOS=windows go build ./...` clean.
- [ ] **Step 5:** Commit `Boot diag: the same probes as a system service before login`.

### Task 8: Release and the measuring guide

**Files:**
- Create: `.github/workflows/release.yml`
- Modify: `README.md`

- [ ] **Step 1:** `release.yml`: on tag `v*`: build `easyswitch-windows-amd64.exe`, `easyswitch-windows-arm64.exe` (ubuntu, `CGO_ENABLED=0`), `easyswitch-macos` (macos runner: build arm64 and amd64 with cgo, `lipo -create`, `codesign -s -`), attach to the release with `-ldflags "-X main.version=<tag>"`.
- [ ] **Step 2:** README section "Measuring your devices": the download line for each OS (`Invoke-WebRequest …/releases/latest/download/easyswitch-windows-amd64.exe -OutFile "$env:USERPROFILE\easyswitch.exe"`; `curl -L -o easyswitch …/easyswitch-macos && chmod +x easyswitch`), then `diag --quick`, `diag` (says it takes about 40 minutes and that the devices must stay untouched when asked), `diag --watch 2m --idle ""`, the boot diag sequence, `switch N`, and what to send back. State plainly that the programs are unsigned and what Windows and macOS will say about that.
- [ ] **Step 3:** Push, wait for CI green on all three systems, tag `v0.1.0`, check the three files are on the release.
- [ ] **Step 4:** Commit `Release workflow and measuring guide` (before the tag).
