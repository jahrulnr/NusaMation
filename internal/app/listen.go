package app

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"runtime"
	"syscall"
	"time"
)

// Startup bind failures on Windows are often transient: Hyper-V/WSL/WinNAT
// re-roll reserved port ranges at boot (bind then fails with EACCES), or the
// previous process is still releasing the port (EADDRINUSE). The logon task
// only restarts a crashed process a few times, so retrying here for a few
// minutes is what keeps a boot-time race from becoming a dead service — and
// it self-heals instead of needing a reinstall.
var (
	listenRetryFor = 5 * time.Minute
	listenRetryMin = 500 * time.Millisecond
	listenRetryMax = 5 * time.Second
)

func listenTCP(ctx context.Context, addr string, log *slog.Logger) (net.Listener, error) {
	deadline := time.Now().Add(listenRetryFor)
	delay := listenRetryMin
	retried := false
	for {
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			if retried {
				log.Info("bound after retrying", "addr", addr)
			}
			return ln, nil
		}
		if !listenRetryable(err) || time.Now().Add(delay).After(deadline) {
			return nil, listenError(addr, err)
		}
		retried = true
		log.Warn("listen failed, retrying", "addr", addr, "error", err, "retry_in", delay)
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-t.C:
		}
		if delay < listenRetryMax {
			delay *= 2
			if delay > listenRetryMax {
				delay = listenRetryMax
			}
		}
	}
}

// Raw Winsock bind(2) failures. On Windows, syscall.EADDR* constants are
// invented APPLICATION_ERROR values that never equal a live socket error, and
// Errno.Is only translates a handful of generic errors — so these codes must
// be compared directly. They cannot collide with real POSIX errnos, so the
// check is safe on every platform.
const (
	errnoWSAAddrInUse    = syscall.Errno(10048) // WSAEADDRINUSE
	errnoWSAAddrNotAvail = syscall.Errno(10049) // WSAEADDRNOTAVAIL
	errnoWSAAccess       = syscall.Errno(10013) // WSAEACCES
)

// bindErrno unwraps a net.Listen failure to its syscall.Errno, if any.
func bindErrno(err error) (syscall.Errno, bool) {
	if errno, ok := errors.AsType[syscall.Errno](err); ok {
		return errno, true
	}
	return 0, false
}

func isInUseErrno(errno syscall.Errno) bool {
	return errno == syscall.EADDRINUSE || errno == errnoWSAAddrInUse
}

func isPermissionErrno(errno syscall.Errno) bool {
	return errno == syscall.EACCES || errno == syscall.EPERM || errno == errnoWSAAccess
}

// listenRetryable reports whether a bind failure is worth waiting out:
// address-in-use (previous instance releasing), address-not-available (interface
// not up yet at boot), or permission-denied — Windows' answer when the port sits
// inside a dynamically reserved range is WSAEACCES. Genuine config problems
// (bad address syntax) fail fast instead.
func listenRetryable(err error) bool {
	errno, ok := bindErrno(err)
	if !ok {
		return errors.Is(err, os.ErrPermission)
	}
	return isInUseErrno(errno) ||
		errno == syscall.EADDRNOTAVAIL || errno == errnoWSAAddrNotAvail ||
		isPermissionErrno(errno)
}

// bindError is a listen failure carrying a platform-neutral verdict. Windows
// and Unix report "address already in use" through different errno values
// (WSAEADDRINUSE 10048 vs EADDRINUSE 98), and the Windows syscall.EADDR*
// constants never equal a live socket error, so errors.Is(err,
// syscall.EADDRINUSE) is false there. Is makes that check work everywhere
// while Unwrap still exposes the original error.
type bindError struct {
	addr  string
	err   error
	hint  string
	inUse bool
}

func (e *bindError) Error() string {
	return "listen on " + e.addr + ": " + e.err.Error() + e.hint
}

func (e *bindError) Unwrap() error { return e.err }

func (e *bindError) Is(target error) bool {
	return e.inUse && target == syscall.EADDRINUSE
}

func listenError(addr string, err error) error {
	hint := ""
	errno, _ := bindErrno(err)
	inUse := isInUseErrno(errno) || errors.Is(err, syscall.EADDRINUSE)
	switch {
	case (errors.Is(err, os.ErrPermission) || isPermissionErrno(errno)) && runtime.GOOS == "windows":
		hint = " — the port may be inside a reserved range (check: netsh int ipv4 show excludedportrange protocol=tcp)"
	case inUse:
		hint = " — another process holds it (check: netstat -ano)"
	}
	return &bindError{addr: addr, err: err, hint: hint, inUse: inUse}
}
