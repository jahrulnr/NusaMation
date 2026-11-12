//go:build !windows && !darwin

package notify

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// unixSender delivers via a command-line notifier: notify-send when present,
// otherwise gdbus calling org.freedesktop.Notifications.Notify directly —
// minimal desktops often ship gdbus/dbus but not libnotify-bin.
type unixSender struct {
	bin, kind string
	state     *unixState
	probeBin  string // gdbus/dbus-send used to check the notification daemon
	probeKind string
}

// unixState is a delivery circuit breaker shared by the sender: a recent
// failure (no notification daemon, dead bus) marks the backend unavailable
// for a cooldown so event-driven sends do not keep spawning doomed execs.
// It heals by itself — after the cooldown the next send retries.
type unixState struct {
	lastFailUnix atomic.Int64
	lastErr      atomic.Value // string
}

// unixFailCooldown is how long a failed delivery marks the sender down.
const unixFailCooldown = 60 * time.Second

func platformSender() Sender {
	var probeBin, probeKind string
	if bin, err := exec.LookPath("gdbus"); err == nil {
		probeBin, probeKind = bin, "gdbus"
	} else if bin, err := exec.LookPath("dbus-send"); err == nil {
		probeBin, probeKind = bin, "dbus-send"
	}
	state := &unixState{}
	if bin, err := exec.LookPath("notify-send"); err == nil {
		return unixSender{bin: bin, kind: "notify-send", state: state, probeBin: probeBin, probeKind: probeKind}
	}
	if bin, err := exec.LookPath("gdbus"); err == nil {
		return unixSender{bin: bin, kind: "gdbus", state: state, probeBin: probeBin, probeKind: probeKind}
	}
	return Null{Reason: "no notifier found (install libnotify-bin or glib gdbus)"}
}

func (s unixSender) Name() string { return s.kind }

// Available requires a DBus session bus — notify-send and gdbus --session
// both talk to org.freedesktop.Notifications over it — and no tripped
// delivery breaker. On headless hosts (VPS, SBC without a desktop login) the
// binaries may exist while every call would fail; a lingering systemd user
// manager can even provide the bus socket without any notification daemon,
// which is what the breaker covers. Both checks are dynamic so a desktop
// session appearing later flips availability on automatically.
func (s unixSender) Available() bool {
	if !sessionBusReachable() {
		return false
	}
	f := s.state.lastFailUnix.Load()
	return f == 0 || time.Since(time.Unix(0, f)) >= unixFailCooldown
}

// probeState is Available plus a daemon re-probe once the breaker cooldown
// has elapsed — the settings UI uses it so a missing or recovered daemon is
// reflected without waiting for the next send. A few-ms exec is fine there;
// the hot Broadcast path keeps the cheap Available().
func (s unixSender) probeState() bool {
	if s.probeBin == "" {
		return s.Available()
	}
	if !sessionBusReachable() {
		return false
	}
	f := s.state.lastFailUnix.Load()
	if f == 0 {
		return true
	}
	if time.Since(time.Unix(0, f)) < unixFailCooldown {
		return false
	}
	s.Probe(context.Background())
	return s.state.lastFailUnix.Load() == 0
}

// why explains the current unavailability for logs.
func (s unixSender) why() string {
	if !sessionBusReachable() {
		return "no desktop session bus (headless host)"
	}
	if e, _ := s.state.lastErr.Load().(string); e != "" {
		return e
	}
	return "notification daemon not responding"
}

// Probe checks once whether org.freedesktop.Notifications has an owner on the
// session bus — a systemd user manager can provide the bus on a headless host
// without any daemon behind it, which would otherwise only surface as a
// failure on the first real notification. When the probe cannot run (no
// gdbus/dbus-send on PATH) it leaves the sender alone; the delivery breaker
// still covers that case after the first failed send.
func (s unixSender) Probe(ctx context.Context) {
	if s.probeBin == "" || !sessionBusReachable() {
		return
	}
	var args []string
	if s.probeKind == "dbus-send" {
		args = []string{"--session", "--dest=org.freedesktop.DBus", "--type=method_call", "--print-reply",
			"/org/freedesktop/DBus", "org.freedesktop.DBus.NameHasOwner", "string:org.freedesktop.Notifications"}
	} else {
		args = []string{"call", "--session", "--dest", "org.freedesktop.DBus",
			"--object-path", "/org/freedesktop/DBus",
			"--method", "org.freedesktop.DBus.NameHasOwner", "org.freedesktop.Notifications"}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, s.probeBin, args...)
	cmd.Env = sessionBusEnv()
	out, err := cmd.CombinedOutput()
	if err == nil && strings.Contains(string(out), "true") {
		s.state.lastFailUnix.Store(0)
		s.state.lastErr.Store("")
		return
	}
	msg := "no org.freedesktop.Notifications owner on session bus"
	if err != nil {
		msg = s.probeKind + " probe failed: " + strings.TrimSpace(string(out))
	}
	if len(msg) > 160 {
		msg = msg[:160]
	}
	s.state.lastFailUnix.Store(time.Now().UnixNano())
	s.state.lastErr.Store(msg)
}

func (s unixSender) Notify(ctx context.Context, n Notification) error {
	title := n.Title
	if strings.TrimSpace(title) == "" {
		title = "NusaMation"
	}
	icon := n.Icon
	if icon == "" {
		icon = "dialog-information"
	}
	if path, cleanup, err := temporaryIcon("mark.png"); err == nil {
		icon = path
		defer cleanup()
	}
	var args []string
	if s.kind == "gdbus" {
		args = s.gdbusArgs(title, n.Body, icon, n.Urgency)
	} else {
		args = s.notifySendArgs(title, n.Body, icon, n.Urgency)
	}
	cmd := exec.CommandContext(ctx, s.bin, args...)
	cmd.Env = sessionBusEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		werr := fmt.Errorf("%s: %w (%s)", s.kind, err, strings.TrimSpace(string(out)))
		msg := werr.Error()
		if len(msg) > 160 {
			msg = msg[:160]
		}
		s.state.lastFailUnix.Store(time.Now().UnixNano())
		s.state.lastErr.Store(msg)
		return werr
	}
	return nil
}

func (s unixSender) notifySendArgs(title, body, icon string, urg Urgency) []string {
	u := string(urg)
	if u == "" {
		u = "normal"
	}
	args := []string{"--app-name=NusaMation", "--urgency=" + u, "--icon=" + icon}
	if urg == UrgencyCritical {
		// Stay visible until dismissed on daemons that honor expire-time.
		args = append(args, "--expire-time=0")
	}
	return append(args, title, body)
}

// gdbusArgs builds the org.freedesktop.Notifications.Notify call:
// Notify(app_name, replaces_id, icon, summary, body, actions, hints, expire).
// The urgency hint is a byte: 0 low, 1 normal, 2 critical.
func (s unixSender) gdbusArgs(title, body, icon string, urg Urgency) []string {
	// An explicit timeout: -1 ("server default") can't be passed because
	// gdbus parses bare "-1" as an option flag.
	u, expire := "1", "5000"
	switch urg {
	case UrgencyLow:
		u = "0"
	case UrgencyCritical:
		u, expire = "2", "0" // stay visible until dismissed
	}
	return []string{
		"call", "--session",
		"--dest", "org.freedesktop.Notifications",
		"--object-path", "/org/freedesktop/Notifications",
		"--method", "org.freedesktop.Notifications.Notify",
		"NusaMation", "0", icon, title, body, "[]",
		"{'urgency': <byte " + u + ">}", expire,
	}
}

// sessionBusReachable reports whether a DBus session bus is advertised or its
// standard per-user socket exists.
func sessionBusReachable() bool {
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") != "" {
		return true
	}
	_, err := os.Stat("/run/user/" + strconv.Itoa(os.Getuid()) + "/bus")
	return err == nil
}

// Under a systemd user service the session-bus address is often missing from
// the service environment; fall back to the standard per-user socket when it
// exists so notifications still reach the desktop session.
func sessionBusEnv() []string {
	env := os.Environ()
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" {
		sock := "/run/user/" + strconv.Itoa(os.Getuid()) + "/bus"
		if _, err := os.Stat(sock); err == nil {
			env = append(env, "DBUS_SESSION_BUS_ADDRESS=unix:path="+sock)
		}
	}
	return env
}
