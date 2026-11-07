//go:build !windows && !darwin

package notify

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestSessionBusReachableViaEnv(t *testing.T) {
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/nonexistent/bus")
	if !sessionBusReachable() {
		t.Fatal("advertised bus address should be reachable even without a socket file")
	}
	s := unixSender{bin: "/bin/true", kind: "notify-send", state: &unixState{}}
	if !s.Available() {
		t.Fatal("sender unavailable despite advertised bus address")
	}
}

func TestSessionBusEnvAddsFallbackSocket(t *testing.T) {
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	sock := "/run/user/" + strconv.Itoa(os.Getuid()) + "/bus"
	found := false
	for _, kv := range sessionBusEnv() {
		if kv == "DBUS_SESSION_BUS_ADDRESS=unix:path="+sock {
			found = true
		}
	}
	// Only asserts the injection when the socket really exists on this host —
	// headless CI has no /run/user/$UID/bus and nothing should be added.
	if _, err := os.Stat(sock); err == nil && !found {
		t.Fatal("bus socket exists but DBUS_SESSION_BUS_ADDRESS was not injected")
	}
}

// A failed delivery must trip the breaker so hosts with a session bus but no
// notification daemon (e.g. a lingering systemd user manager on a VPS) stop
// spawning doomed execs — and the settings UI hides the section.
func TestUnixBreakerTripsOnFailure(t *testing.T) {
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/nonexistent/bus")
	s := unixSender{bin: "/bin/false", kind: "notify-send", state: &unixState{}}
	if !s.Available() {
		t.Fatal("fresh sender should start available")
	}
	if err := s.Notify(context.Background(), Notification{Title: "t"}); err == nil {
		t.Fatal("/bin/false notify should fail")
	}
	if s.Available() {
		t.Fatal("breaker did not trip after failed delivery")
	}
	if s.why() == "" {
		t.Fatal("why() should describe the failure while tripped")
	}
	// Cooldown expiry is optimistic again — the next real send decides.
	s.state.lastFailUnix.Store(time.Now().Add(-unixFailCooldown - time.Second).UnixNano())
	if !s.Available() {
		t.Fatal("breaker should lift after cooldown")
	}
}

// probeState re-probes the daemon for the settings UI: a failing probe keeps
// the section hidden, a successful probe heals the breaker.
func TestUnixProbeState(t *testing.T) {
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/nonexistent/bus")
	broken := unixSender{bin: "/bin/true", kind: "notify-send", state: &unixState{},
		probeBin: "/bin/false", probeKind: "dbus-send"}
	broken.state.lastFailUnix.Store(time.Now().Add(-unixFailCooldown - time.Second).UnixNano())
	if broken.probeState() {
		t.Fatal("probeState should stay down when the daemon probe fails")
	}
	if e := broken.why(); e == "" {
		t.Fatal("why() should carry the probe error")
	}
}

func TestUnixProbeClearsBreakerOnDaemonReply(t *testing.T) {
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/nonexistent/bus")
	// Fake probe binary answering like NameHasOwner → "(true,)".
	fake := filepath.Join(t.TempDir(), "probe")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho '(true,)'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := unixSender{bin: "/bin/true", kind: "notify-send", state: &unixState{},
		probeBin: fake, probeKind: "gdbus"}
	s.state.lastFailUnix.Store(time.Now().UnixNano())
	s.state.lastErr.Store("earlier failure")
	s.Probe(context.Background())
	if s.state.lastFailUnix.Load() != 0 {
		t.Fatal("probe seeing a daemon reply should clear the breaker")
	}
}
