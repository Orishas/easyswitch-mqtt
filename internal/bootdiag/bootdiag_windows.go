package bootdiag

import (
	"os"
	"path/filepath"
	"unsafe"

	"github.com/kardianos/service"
	"golang.org/x/sys/windows"
)

const supported = true

func reportPath() string {
	dir := os.Getenv("ProgramData")
	if dir == "" {
		dir = `C:\ProgramData`
	}
	return filepath.Join(dir, "easyswitch", "boot-diag.txt")
}

var procWTSQuerySessionInformation = windows.NewLazySystemDLL("wtsapi32.dll").NewProc("WTSQuerySessionInformationW")

const (
	wtsUserName      = 5          // WTS_INFO_CLASS WTSUserName
	noConsoleSession = 0xFFFFFFFF // WTSGetActiveConsoleSessionId: no session attached
)

// sessionState asks for the user of the active console session; there
// is none at the login screen.
func sessionState() string {
	id := windows.WTSGetActiveConsoleSessionId()
	if id == noConsoleSession {
		return "unknown"
	}
	var buf *uint16
	var n uint32
	ok, _, _ := procWTSQuerySessionInformation.Call(0, uintptr(id), wtsUserName,
		uintptr(unsafe.Pointer(&buf)), uintptr(unsafe.Pointer(&n)))
	if ok == 0 || buf == nil {
		return "unknown"
	}
	name := windows.UTF16PtrToString(buf)
	windows.WTSFreeMemory(uintptr(unsafe.Pointer(buf)))
	if name == "" {
		return "before login"
	}
	return "logged in: " + name
}

func isAdmin() bool { return windows.GetCurrentProcessToken().IsElevated() }

// endService asks the service manager to stop the service once the runs
// are done; its Run returns after that.
func endService(s service.Service) { _ = s.Stop() }
