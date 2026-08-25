package backend

// support.go is the shared services the backend implementations build on — the
// ssh-reachability wait, size parsing, command-output helpers. Not a backend
// itself and not orchestration; the common plumbing every backend would
// otherwise duplicate, kept here in the framework so internal/backends holds
// only the individual backends.

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/mutms/mpd-virt/go/internal/exec"
	"github.com/mutms/mpd-virt/go/internal/host"
)

// ShortErr collapses a failed command's output to a single line for a warning.
func ShortErr(r exec.Result) string {
	s := strings.TrimSpace(r.Stderr)
	if s == "" {
		s = strings.TrimSpace(r.Stdout)
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return "non-zero exit"
	}
	return s
}

// ShellQuote single-quotes a value for safe use inside `sh -c`.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// WaitReachable polls until the target answers key-auth ssh, or the timeout.
func WaitReachable(ctx context.Context, t host.Target, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if t.Reachable(ctx) {
			return true
		}
		time.Sleep(3 * time.Second)
	}
	return false
}

// --- size parsing -----------------------------------------------------------

// ParseSizeMiB turns "8g"/"8192m"/"8192" into MiB; 0 for empty/unparseable (the
// caller decides).
func ParseSizeMiB(s string) int {
	s = strings.ToLower(strings.TrimSpace(s))
	switch {
	case s == "":
		return 0
	case strings.HasSuffix(s, "g"):
		if n, err := strconv.Atoi(strings.TrimSuffix(s, "g")); err == nil {
			return n * 1024
		}
	case strings.HasSuffix(s, "m"):
		if n, err := strconv.Atoi(strings.TrimSuffix(s, "m")); err == nil {
			return n
		}
	default:
		if n, err := strconv.Atoi(s); err == nil {
			return n
		}
	}
	return 0
}

// ParseSizeGiB turns "80g"/"81920m"/"81920" into GiB; 0 for empty/unparseable.
func ParseSizeGiB(s string) int {
	s = strings.ToLower(strings.TrimSpace(s))
	switch {
	case s == "":
		return 0
	case strings.HasSuffix(s, "g"):
		if n, err := strconv.Atoi(strings.TrimSuffix(s, "g")); err == nil {
			return n
		}
	case strings.HasSuffix(s, "m"):
		if n, err := strconv.Atoi(strings.TrimSuffix(s, "m")); err == nil {
			return n / 1024
		}
	default:
		if n, err := strconv.Atoi(s); err == nil {
			return n / 1024
		}
	}
	return 0
}

// StripMask drops a "/24"-style suffix, returning the bare IP — shared by the
// backends that read CIDR-form addresses (container, parallels).
func StripMask(addr string) string {
	addr = strings.TrimSpace(addr)
	if i := strings.IndexByte(addr, '/'); i >= 0 {
		addr = addr[:i]
	}
	return addr
}
