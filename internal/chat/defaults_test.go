package chat

import (
	"context"
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/jahrulnr/nusamation/internal/agent"
	"github.com/jahrulnr/nusamation/internal/events"
	"github.com/jahrulnr/nusamation/internal/security"
	"github.com/jahrulnr/nusamation/internal/store"
)

func TestAgentDefaultsFollowDynamicOptions(t *testing.T) {
	ctx := context.Background()
	repo, _ := filepath.Abs("../..")
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-acp")
	build := exec.Command("go", "build", "-o", bin, "./cmd/fake-acp-agent")
	build.Dir = repo
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, out)
	}
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sealer, err := security.LoadOrCreateSealer(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	st.SetSealer(sealer)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := agent.NewManager(st, logger)
	defer mgr.StopAll()
	svc := New(Options{Store: st, Agents: mgr, Events: events.Nop{}, Log: logger, Workspace: dir})
	mgr.SetSink(svc)
	enabled := true
	first, err := st.CreateAgent(ctx, store.AgentInput{Name: "First", Slug: "first-agent", Command: "/bin/echo", Cwd: dir, Enabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	ag, err := st.CreateAgent(ctx, store.AgentInput{Name: "Fake", Slug: "fake-defaults", Command: bin, Cwd: dir, Enabled: &enabled, Env: store.SecretList{{Name: "FAKE_ACP_DYNAMIC_CONFIG", Value: "1"}}})
	if err != nil {
		t.Fatal(err)
	}
	selections := []store.ConfigSelection{{ID: "model", Value: "fake-smart"}, {ID: "effort", Value: "high"}}
	if _, err := mgr.PreviewDefaults(ctx, ag.ID, selections); err != nil {
		t.Fatalf("valid sequential preview: %v", err)
	}
	if _, err := mgr.PreviewDefaults(ctx, ag.ID, []store.ConfigSelection{selections[1]}); err == nil {
		t.Fatal("accepted effort unavailable for initial model")
	}
	if _, err := mgr.PreviewDefaults(ctx, ag.ID, []store.ConfigSelection{{ID: "model", Value: "not-a-model"}}); err == nil {
		t.Fatal("accepted unavailable model")
	}
	if err := st.SetJSONSetting(ctx, "agent.defaults", store.AgentDefaults{AgentID: ag.ID, Selections: selections}); err != nil {
		t.Fatal(err)
	}
	conv, err := svc.CreateConversation(ctx, "", "defaults", dir, "whatsapp", "test")
	if err != nil {
		t.Fatal(err)
	}
	if conv.AgentID != ag.ID {
		t.Fatalf("default agent = %s, want %s", conv.AgentID, ag.ID)
	}
	// Later edits to application defaults must not alter this conversation's snapshot.
	if err := st.SetJSONSetting(ctx, "agent.defaults", store.AgentDefaults{AgentID: ag.ID}); err != nil {
		t.Fatal(err)
	}
	if err := svc.PrepareSession(ctx, conv.ID); err != nil {
		t.Fatalf("prepare defaults: %v", err)
	}
	configs := mgr.Session(conv.ID).Config()
	if got := modeTestCurrent(configs, "model"); got != "fake-smart" {
		t.Fatalf("model = %q", got)
	}
	if got := modeTestCurrent(configs, "effort"); got != "high" {
		t.Fatalf("effort = %q", got)
	}
	if err := st.SetJSONSetting(ctx, "agent.defaults", store.AgentDefaults{AgentID: ag.ID, Selections: selections}); err != nil {
		t.Fatal(err)
	}
	other, err := svc.CreateConversation(ctx, first.ID, "switch", dir, "whatsapp", "other")
	if err != nil {
		t.Fatal(err)
	}
	switched, err := svc.SwitchAgent(ctx, other.ID, ag.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := svc.sessionRequest(switched, ag).DefaultConfig; len(got) != 2 || got[1].Value != "high" {
		t.Fatalf("switch defaults = %#v", got)
	}
}
