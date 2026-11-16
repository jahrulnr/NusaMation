package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

var discardLog = slog.New(slog.NewTextHandler(io.Discard, nil))

func shrinkListenRetry(t *testing.T, d time.Duration) {
	t.Helper()
	old := listenRetryFor
	listenRetryFor = d
	t.Cleanup(func() { listenRetryFor = old })
}

func TestListenBindsAfterPortFrees(t *testing.T) {
	hold, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	shrinkListenRetry(t, 5*time.Second)
	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = hold.Close()
	}()
	ln, err := listenTCP(context.Background(), hold.Addr().String(), discardLog)
	if err != nil {
		t.Fatalf("listenTCP: %v", err)
	}
	_ = ln.Close()
}

func TestListenGivesUpWhenPortStaysTaken(t *testing.T) {
	hold, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hold.Close() })
	shrinkListenRetry(t, 300*time.Millisecond)
	start := time.Now()
	_, err = listenTCP(context.Background(), hold.Addr().String(), discardLog)
	if err == nil {
		t.Fatal("expected a bind error while the port is held")
	}
	if !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatalf("want wrapped EADDRINUSE, got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("gave up too late: %v", time.Since(start))
	}
}

func TestListenContextCancelStopsRetry(t *testing.T) {
	hold, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hold.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err = listenTCP(ctx, hold.Addr().String(), discardLog)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want context error, got %v", err)
	}
}

func TestListenRetryable(t *testing.T) {
	mk := func(errno syscall.Errno) error {
		return &net.OpError{Op: "listen", Net: "tcp", Err: os.NewSyscallError("bind", errno)}
	}
	for _, errno := range []syscall.Errno{syscall.EADDRINUSE, syscall.EADDRNOTAVAIL, syscall.EACCES, syscall.EPERM} {
		if !listenRetryable(mk(errno)) {
			t.Errorf("%v should be retryable", errno)
		}
	}
	// Raw Winsock codes a Windows bind surfaces; syscall.EADDR* constants are
	// invented values there and errors.Is never matches, so the numbers must be
	// recognized directly.
	for _, errno := range []syscall.Errno{errnoWSAAddrInUse, errnoWSAAddrNotAvail, errnoWSAAccess} {
		if !listenRetryable(mk(errno)) {
			t.Errorf("WSA errno %d should be retryable", int(errno))
		}
	}
	if listenRetryable(mk(syscall.EINVAL)) {
		t.Error("EINVAL must not be retried — it is a config error")
	}
}

func TestListenErrorHints(t *testing.T) {
	mk := func(errno syscall.Errno) error {
		return &net.OpError{Op: "listen", Net: "tcp", Err: os.NewSyscallError("bind", errno)}
	}
	got := listenError("127.0.0.1:8787", mk(syscall.EACCES)).Error()
	if runtime.GOOS == "windows" {
		if !strings.Contains(got, "excludedportrange") {
			t.Fatalf("windows EACCES/WSAEACCES hint missing: %s", got)
		}
	} else if strings.Contains(got, "excludedportrange") {
		t.Fatalf("windows-only hint leaked on %s: %s", runtime.GOOS, got)
	}
	if got := listenError("127.0.0.1:8787", mk(syscall.EADDRINUSE)).Error(); !strings.Contains(got, "netstat") {
		t.Fatalf("EADDRINUSE hint missing: %s", got)
	}
	if got := listenError("127.0.0.1:8787", mk(errnoWSAAddrInUse)).Error(); !strings.Contains(got, "netstat") {
		t.Fatalf("WSAEADDRINUSE hint missing: %s", got)
	}
}
