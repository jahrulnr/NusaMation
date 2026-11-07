package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jahrulnr/nusamation/internal/security"
	"github.com/jahrulnr/nusamation/internal/store"
	"github.com/jahrulnr/nusamation/internal/testutil"
)

func previewTestStore(t *testing.T, dir string) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := security.LoadOrCreateSealer(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	st.SetSealer(sealer)
	return st
}

func traceLines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for line := range strings.SplitSeq(string(raw), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func countTrace(lines []string, prefix string) int {
	n := 0
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

func configCurrentString(raw json.RawMessage, id string) string {
	v := findConfigCurrent(raw, id)
	s, _ := v.(string)
	return s
}

// Extending a selection list reuses the warm preview session and only applies
// the appended choice; a diverging list recreates the session on the same
// process instead of respawning the agent.
func TestPreviewWarmRuntimeReusesSession(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	bin := testutil.BuildFakeACP(t)
	st := previewTestStore(t, dir)
	defer st.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := NewManager(st, logger)
	defer mgr.StopAll()
	tracePath := filepath.Join(dir, "trace.log")
	enabled, autoStart := true, true
	ag, err := st.CreateAgent(ctx, store.AgentInput{
		Name: "Fake", Slug: "fake-warm", Command: bin, Cwd: dir, Enabled: &enabled, AutoStart: &autoStart,
		Env: store.SecretList{{Name: "FAKE_ACP_DYNAMIC_CONFIG", Value: "1"}, {Name: "FAKE_ACP_TRACE", Value: tracePath}},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := mgr.ensureRuntime(ag)
	r.warm(ctx)
	if mgr.runtimeFor(ag.ID).warmClient() == nil {
		t.Fatal("expected a live client after warm")
	}

	if _, err := mgr.PreviewDefaults(ctx, ag.ID, []store.ConfigSelection{{ID: "model", Value: "fake-smart"}}); err != nil {
		t.Fatalf("first preview: %v", err)
	}
	configs, err := mgr.PreviewDefaults(ctx, ag.ID, []store.ConfigSelection{
		{ID: "model", Value: "fake-smart"}, {ID: "effort", Value: "high"},
	})
	if err != nil {
		t.Fatalf("extended preview: %v", err)
	}
	if got := configCurrentString(configs, "effort"); got != "high" {
		t.Fatalf("effort = %q", got)
	}
	lines := traceLines(t, tracePath)
	if n := countTrace(lines, "session_new"); n != 1 {
		t.Fatalf("extension must not recreate the session, session_new = %d\n%s", n, lines)
	}
	if countTrace(lines, "config effort high") != 1 {
		t.Fatalf("delta choice was not applied exactly once:\n%s", lines)
	}

	// Reordering diverges from the applied list — a clean session is rebuilt on
	// the same process (still no second spawn: the trace would still grow, but
	// only via session_new, and the process is shared so no duplicate init).
	if _, err := mgr.PreviewDefaults(ctx, ag.ID, []store.ConfigSelection{{ID: "effort", Value: "low"}}); err != nil {
		t.Fatalf("diverging preview: %v", err)
	}
	lines = traceLines(t, tracePath)
	if n := countTrace(lines, "session_new"); n != 2 {
		t.Fatalf("divergence must recreate the session, session_new = %d\n%s", n, lines)
	}
}

// A replay error drops the preview session; the next preview rebuilds a clean
// one on the same process.
func TestPreviewDropsSessionAfterReplayError(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	bin := testutil.BuildFakeACP(t)
	st := previewTestStore(t, dir)
	defer st.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := NewManager(st, logger)
	defer mgr.StopAll()
	tracePath := filepath.Join(dir, "trace.log")
	enabled, autoStart := true, true
	ag, err := st.CreateAgent(ctx, store.AgentInput{
		Name: "Fake", Slug: "fake-err", Command: bin, Cwd: dir, Enabled: &enabled, AutoStart: &autoStart,
		Env: store.SecretList{{Name: "FAKE_ACP_TRACE", Value: tracePath}},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := mgr.ensureRuntime(ag)
	r.warm(ctx)

	if _, err := mgr.PreviewDefaults(ctx, ag.ID, []store.ConfigSelection{{ID: "model", Value: "fake-smart"}}); err != nil {
		t.Fatalf("seed preview: %v", err)
	}
	if _, err := mgr.PreviewDefaults(ctx, ag.ID, []store.ConfigSelection{{ID: "model", Value: "not-a-model"}}); err == nil {
		t.Fatal("expected an invalid selection to fail")
	}
	if _, err := mgr.PreviewDefaults(ctx, ag.ID, []store.ConfigSelection{{ID: "model", Value: "fake-fast"}}); err != nil {
		t.Fatalf("preview after dropped session: %v", err)
	}
	lines := traceLines(t, tracePath)
	// warm session + failed replay's recreate + post-error rebuild
	if n := countTrace(lines, "session_new"); n != 3 {
		t.Fatalf("session_new = %d, want 3\n%s", n, lines)
	}
}

// Probe on a warm agent must reuse the runtime client — proven by breaking the
// configured command after warm-up: a throwaway spawn would fail to launch.
func TestProbeReusesWarmClient(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	bin := testutil.BuildFakeACP(t)
	st := previewTestStore(t, dir)
	defer st.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := NewManager(st, logger)
	defer mgr.StopAll()
	enabled, autoStart := true, true
	ag, err := st.CreateAgent(ctx, store.AgentInput{
		Name: "Fake", Slug: "fake-probe", Command: bin, Cwd: dir, Enabled: &enabled, AutoStart: &autoStart,
	})
	if err != nil {
		t.Fatal(err)
	}
	mgr.ensureRuntime(ag).warm(ctx)
	bogus := "/nonexistent/fake-acp"
	if _, err := st.UpdateAgent(ctx, ag.ID, store.AgentInput{
		Name: ag.Name, Slug: ag.Slug, Command: bogus, Cwd: dir, Enabled: &enabled, AutoStart: &autoStart,
	}); err != nil {
		t.Fatal(err)
	}
	res, err := mgr.Probe(ctx, ag.ID)
	if err != nil {
		t.Fatalf("probe on warm client: %v", err)
	}
	if res.AgentInfo == nil {
		t.Fatal("probe returned no agent info")
	}
	if r := mgr.runtimeFor(ag.ID); r == nil || r.warmClient() == nil {
		t.Fatal("probe must not tear down the warm runtime")
	}
}

// With a seeded catalog and no warm runtime, previews merge locally and never
// spawn the agent process.
func TestPreviewCachedCatalogSkipsSpawn(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	bin := testutil.BuildFakeACP(t)
	st := previewTestStore(t, dir)
	defer st.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := NewManager(st, logger)
	defer mgr.StopAll()
	tracePath := filepath.Join(dir, "trace.log")
	enabled := true
	ag, err := st.CreateAgent(ctx, store.AgentInput{
		Name: "Fake", Slug: "fake-cached", Command: bin, Cwd: dir, Enabled: &enabled,
		Env: store.SecretList{{Name: "FAKE_ACP_TRACE", Value: tracePath}},
	})
	if err != nil {
		t.Fatal(err)
	}
	catalog := json.RawMessage(`[
		{"id":"mode","name":"Session Mode","category":"mode","type":"select","currentValue":"ask","options":[{"value":"ask"},{"value":"bypass"}]},
		{"id":"model","name":"Model","category":"model","type":"select","currentValue":"fake-fast","options":[{"value":"fake-fast"},{"value":"fake-smart"}]},
		{"id":"effort","name":"Effort","category":"thought_level","type":"select","currentValue":"low","options":[{"value":"low"},{"value":"high"}]}
	]`)
	if err := st.UpdateAgentConfigCache(ctx, ag.ID, catalog); err != nil {
		t.Fatal(err)
	}
	configs, err := mgr.PreviewDefaults(ctx, ag.ID, []store.ConfigSelection{{ID: "model", Value: "fake-smart"}})
	if err != nil {
		t.Fatalf("cached preview: %v", err)
	}
	if got := configCurrentString(configs, "model"); got != "fake-smart" {
		t.Fatalf("model = %q", got)
	}
	if lines := traceLines(t, tracePath); len(lines) != 0 {
		t.Fatalf("cached preview must not spawn the agent:\n%s", lines)
	}
}

// The first preview of an uncached cold agent spawns the probe but seeds the
// catalog, so the next preview merges locally.
func TestPreviewSeedsCatalogAfterLiveProbe(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	bin := testutil.BuildFakeACP(t)
	st := previewTestStore(t, dir)
	defer st.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := NewManager(st, logger)
	defer mgr.StopAll()
	tracePath := filepath.Join(dir, "trace.log")
	enabled := true
	ag, err := st.CreateAgent(ctx, store.AgentInput{
		Name: "Fake", Slug: "fake-seed", Command: bin, Cwd: dir, Enabled: &enabled,
		Env: store.SecretList{{Name: "FAKE_ACP_TRACE", Value: tracePath}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.PreviewDefaults(ctx, ag.ID, nil); err != nil {
		t.Fatalf("live preview: %v", err)
	}
	before := traceLines(t, tracePath)
	if countTrace(before, "session_new") != 1 {
		t.Fatalf("expected a probe session:\n%s", before)
	}
	fresh, err := st.GetAgent(ctx, ag.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh.ConfigOptions) == 0 || string(fresh.ConfigOptions) == "[]" {
		t.Fatal("probe preview did not seed the agent catalog cache")
	}
	if _, err := mgr.PreviewDefaults(ctx, ag.ID, []store.ConfigSelection{{ID: "model", Value: "fake-smart"}}); err != nil {
		t.Fatalf("cached preview: %v", err)
	}
	after := traceLines(t, tracePath)
	if len(after) != len(before) {
		t.Fatalf("second preview spawned the agent again:\nbefore: %s\nafter: %s", before, after)
	}
}

func TestWarmAutoStartOnlyWarmsOptedIn(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	bin := testutil.BuildFakeACP(t)
	st := previewTestStore(t, dir)
	defer st.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := NewManager(st, logger)
	defer mgr.StopAll()
	enabled, autoStart := true, true
	warm, err := st.CreateAgent(ctx, store.AgentInput{Name: "Warm", Slug: "warm", Command: bin, Cwd: dir, Enabled: &enabled, AutoStart: &autoStart})
	if err != nil {
		t.Fatal(err)
	}
	off := false
	cold, err := st.CreateAgent(ctx, store.AgentInput{Name: "Cold", Slug: "cold", Command: bin, Cwd: dir, Enabled: &enabled, AutoStart: &off})
	if err != nil {
		t.Fatal(err)
	}
	mgr.WarmAgent(cold.ID)
	if r := mgr.runtimeFor(cold.ID); r != nil {
		t.Fatal("WarmAgent must ignore agents without auto-start")
	}
	mgr.WarmAutoStart()
	deadline := time.Now().Add(15 * time.Second)
	for {
		r := mgr.runtimeFor(warm.ID)
		if r != nil && r.warmClient() != nil {
			r.previewMu.Lock()
			ready := r.preview != nil
			r.previewMu.Unlock()
			if ready {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("auto-start agent was not warmed in time")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if r := mgr.runtimeFor(cold.ID); r != nil && r.warmClient() != nil {
		t.Fatal("non-auto-start agent must stay cold")
	}
	mgr.InvalidateAgent(warm.ID)
	if r := mgr.runtimeFor(warm.ID); r != nil && r.warmClient() != nil {
		t.Fatal("invalidate must drop the warm client")
	}
}
