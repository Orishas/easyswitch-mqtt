package bootdiag

import (
	"os"
	"os/user"
	"strconv"
	"syscall"

	"github.com/kardianos/service"
)

const supported = true

func reportPath() string { return "/Library/Application Support/easyswitch/boot-diag.txt" }

// sessionState reads the owner of /dev/console: root at the login window,
// the user who is logged in otherwise.
func sessionState() string {
	fi, err := os.Stat("/dev/console")
	if err != nil {
		return "unknown"
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return "unknown"
	}
	if st.Uid == 0 {
		return "before login"
	}
	uid := strconv.FormatUint(uint64(st.Uid), 10)
	if u, err := user.LookupId(uid); err == nil {
		return "logged in: " + u.Username
	}
	return "logged in: uid " + uid
}

func isAdmin() bool { return os.Geteuid() == 0 }

// endService: launchd needs nothing; Run returns through waitFor.
func endService(service.Service) {}
