//go:build !windows && !darwin

package bootdiag

import "github.com/kardianos/service"

// supported: this platform has no boot diag yet.
const supported = false

func reportPath() string { return "" }

func sessionState() string { return "unknown" }

func isAdmin() bool { return false }

func endService(service.Service) {}
