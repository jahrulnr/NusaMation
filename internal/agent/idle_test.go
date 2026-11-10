package agent

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jahrulnr/nusamation/internal/store"
	"github.com/jahrulnr/nusamation/internal/testutil"
)

// An agent process idle past the timeout is stopped and its runtime dropped;
// the next EnsureSession spawns a fresh runtime and reloads the room's
// persisted ACP session id, so the cutoff is transparent.
func TestReapIdleStopsProcessAndResumes(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	bin := testutil.BuildFakeACP(t)
	st := previewTestStore(t, dir)
	defer st.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := NewManager(st, logger)
	defer mgr.StopAll()

	enabled := true
	ag, err := st.CreateAgent(ctx, store.AgentInput{
		Name: "Fake", Slug: "fake-idle", Command: bin, Cwd: dir, Enabled: &enabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	conv, err := st.CreateConversation(ctx, ag.ID, "room", dir, "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := mgr.EnsureSession(ctx, EnsureRequest{AgentID: ag.ID, ConversationID: conv.ID, Cwd: dir})
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	acpID := sess.ACPID()
	if acpID == "" {
		t.Fatal("session has no ACP id")
	}
	rt := mgr.runtimeFor(ag.ID)
	if rt == nil {
		t.Fatal("expected a runtime")
	}
	client := rt.warmClient()
	if client == nil {
		t.Fatal("expected a live client")
	}

	// Recent activity keeps the process alive.
	mgr.ReapIdle(5 * time.Minute)
	if mgr.runtimeFor(ag.ID) != rt || !client.Alive() {
		t.Fatal("a busy agent must not be reaped")
	}

	// An EnsureSession hit on the already-loaded session is itself activity:
	// the next prompt would race the reaper if it were not.
	rt.mu.Lock()
	rt.lastActivity = time.Now().Add(-time.Hour)
	rt.mu.Unlock()
	if _, err := mgr.EnsureSession(ctx, EnsureRequest{AgentID: ag.ID, ConversationID: conv.ID, Cwd: dir}); err != nil {
		t.Fatalf("re-ensure on loaded session: %v", err)
	}
	mgr.ReapIdle(5 * time.Minute)
	if mgr.runtimeFor(ag.ID) != rt || !client.Alive() {
		t.Fatal("a session ensured just now must not be reaped")
	}

	// Past the timeout the process is stopped and the runtime dropped.
	rt.mu.Lock()
	rt.lastActivity = time.Now().Add(-time.Hour)
	rt.mu.Unlock()
	mgr.ReapIdle(5 * time.Minute)
	if mgr.runtimeFor(ag.ID) != nil {
		t.Fatal("idle runtime was not removed")
	}
	if mgr.Session(conv.ID) != nil {
		t.Fatal("idle session mapping must be cleared")
	}
	deadline := time.Now().Add(10 * time.Second)
	for client.Alive() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if client.Alive() {
		t.Fatal("idle agent process is still alive")
	}
	if state, _, _, _ := rt.Info(); state != "stopped" {
		t.Fatalf("runtime state = %q, want stopped", state)
	}
	stored, err := st.GetAgent(ctx, ag.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "stopped" || stored.LastError != "idle timeout" {
		t.Fatalf("agent status = %q/%q, want stopped/idle timeout", stored.Status, stored.LastError)
	}

	// The next turn restarts the process and reloads the same ACP session.
	sess2, err := mgr.EnsureSession(ctx, EnsureRequest{
		AgentID: ag.ID, ConversationID: conv.ID, Cwd: dir, ACPSessionID: acpID,
	})
	if err != nil {
		t.Fatalf("re-ensure after idle stop: %v", err)
	}
	if sess2.ACPID() != acpID {
		t.Fatalf("session id changed: %q -> %q", acpID, sess2.ACPID())
	}
	if mgr.runtimeFor(ag.ID) == rt {
		t.Fatal("expected a fresh runtime after idle stop")
	}
}

// Auto-start agents pay for residency by design, and a runtime with unbounded
// work in flight must never be cut — even when the idle window is zero.
func TestReapIdleSkipsAutoStartAndBusy(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	bin := testutil.BuildFakeACP(t)
	st := previewTestStore(t, dir)
	defer st.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := NewManager(st, logger)
	defer mgr.StopAll()

	enabled, autoOn, autoOff := true, true, false
	auto, err := st.CreateAgent(ctx, store.AgentInput{
		Name: "Auto", Slug: "auto", Command: bin, Cwd: dir, Enabled: &enabled, AutoStart: &autoOn,
	})
	if err != nil {
		t.Fatal(err)
	}
	busy, err := st.CreateAgent(ctx, store.AgentInput{
		Name: "Busy", Slug: "busy", Command: bin, Cwd: dir, Enabled: &enabled, AutoStart: &autoOff,
	})
	if err != nil {
		t.Fatal(err)
	}
	convAuto, err := st.CreateConversation(ctx, auto.ID, "auto room", dir, "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	convBusy, err := st.CreateConversation(ctx, busy.ID, "busy room", dir, "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.EnsureSession(ctx, EnsureRequest{AgentID: auto.ID, ConversationID: convAuto.ID, Cwd: dir}); err != nil {
		t.Fatalf("ensure auto: %v", err)
	}
	if _, err := mgr.EnsureSession(ctx, EnsureRequest{AgentID: busy.ID, ConversationID: convBusy.ID, Cwd: dir}); err != nil {
		t.Fatalf("ensure busy: %v", err)
	}
	rtAuto := mgr.runtimeFor(auto.ID)
	rtBusy := mgr.runtimeFor(busy.ID)
	rtBusy.mu.Lock()
	rtBusy.inflight = 1 // a prompt is in flight
	rtBusy.mu.Unlock()

	mgr.ReapIdle(0)
	if mgr.runtimeFor(auto.ID) != rtAuto {
		t.Fatal("auto-start agent must stay resident")
	}
	if mgr.runtimeFor(busy.ID) != rtBusy {
		t.Fatal("a runtime with work in flight must not be reaped")
	}

	// A start already in progress is not a reap candidate either.
	rtBusy.mu.Lock()
	rtBusy.inflight = 0
	rtBusy.starting = true
	rtBusy.mu.Unlock()
	mgr.ReapIdle(0)
	if mgr.runtimeFor(busy.ID) != rtBusy {
		t.Fatal("a starting runtime must not be reaped")
	}
	rtBusy.mu.Lock()
	rtBusy.starting = false
	rtBusy.mu.Unlock()
	mgr.ReapIdle(0)
	if mgr.runtimeFor(busy.ID) != nil {
		t.Fatal("idle non-auto-start runtime should have been reaped")
	}
}

// A manual reload restarts every live process — resident auto-start agents
// included — and warms a fresh runtime so the agent does not go cold. Busy
// runtimes are reported skipped, stopped agents are left alone, and each
// room's persisted ACP session id still resumes on the next turn.
func TestReloadActiveRestartsAndSkipsBusy(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	bin := testutil.BuildFakeACP(t)
	st := previewTestStore(t, dir)
	defer st.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := NewManager(st, logger)
	defer mgr.StopAll()

	enabled, autoOn, autoOff := true, true, false
	mk := func(slug string, auto *bool) store.Agent {
		ag, err := st.CreateAgent(ctx, store.AgentInput{Name: slug, Slug: slug, Command: bin, Cwd: dir, Enabled: &enabled, AutoStart: auto})
		if err != nil {
			t.Fatal(err)
		}
		return ag
	}
	active := mk("active", &autoOff)
	auto := mk("auto", &autoOn)
	busy := mk("busy", &autoOff)
	stopped := mk("stopped", &autoOff)

	ensure := func(ag store.Agent, title string) *Session {
		conv, err := st.CreateConversation(ctx, ag.ID, title, dir, "web", "", "")
		if err != nil {
			t.Fatal(err)
		}
		sess, err := mgr.EnsureSession(ctx, EnsureRequest{AgentID: ag.ID, ConversationID: conv.ID, Cwd: dir})
		if err != nil {
			t.Fatalf("ensure %s: %v", title, err)
		}
		return sess
	}
	sessActive := ensure(active, "active room")
	ensure(auto, "auto room")
	ensure(busy, "busy room")

	rtActive := mgr.runtimeFor(active.ID)
	rtAuto := mgr.runtimeFor(auto.ID)
	rtBusy := mgr.runtimeFor(busy.ID)
	clientActive := rtActive.warmClient()
	clientAuto := rtAuto.warmClient()
	if clientActive == nil || clientAuto == nil {
		t.Fatal("expected live clients before reload")
	}
	rtBusy.mu.Lock()
	rtBusy.inflight = 1 // a prompt is in flight
	rtBusy.mu.Unlock()

	reloaded, skipped := mgr.ReloadActive()
	reloadedIDs := map[string]bool{}
	for _, a := range reloaded {
		reloadedIDs[a.ID] = true
	}
	if !reloadedIDs[active.ID] || !reloadedIDs[auto.ID] {
		t.Fatalf("expected active and auto-start agents reloaded, got %v", reloadedIDs)
	}
	if len(skipped) != 1 || skipped[0].ID != busy.ID {
		t.Fatalf("expected busy agent skipped, got %v", skipped)
	}
	if mgr.runtimeFor(busy.ID) != rtBusy {
		t.Fatal("busy runtime must not be replaced")
	}
	if mgr.runtimeFor(stopped.ID) != nil {
		t.Fatal("reload must not cold-start a stopped agent")
	}
	if mgr.runtimeFor(active.ID) == nil || mgr.runtimeFor(active.ID) == rtActive {
		t.Fatal("expected a fresh runtime for the reloaded agent")
	}
	if mgr.runtimeFor(auto.ID) == nil || mgr.runtimeFor(auto.ID) == rtAuto {
		t.Fatal("expected a fresh runtime for the reloaded auto-start agent")
	}

	deadline := time.Now().Add(10 * time.Second)
	for (clientActive.Alive() || clientAuto.Alive()) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if clientActive.Alive() || clientAuto.Alive() {
		t.Fatal("old agent processes are still alive after reload")
	}

	// Room session ids survive: the next turn resumes the same ACP session.
	acpID := sessActive.ACPID()
	sess2, err := mgr.EnsureSession(ctx, EnsureRequest{AgentID: active.ID, ConversationID: sessActive.ConversationID(), Cwd: dir, ACPSessionID: acpID})
	if err != nil {
		t.Fatalf("re-ensure after reload: %v", err)
	}
	if sess2.ACPID() != acpID {
		t.Fatalf("session id changed across reload: %q -> %q", acpID, sess2.ACPID())
	}
}
