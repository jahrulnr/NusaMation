package chat

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jahrulnr/nusamation/internal/agent"
	"github.com/jahrulnr/nusamation/internal/events"
	"github.com/jahrulnr/nusamation/internal/security"
	"github.com/jahrulnr/nusamation/internal/store"
	"github.com/jahrulnr/nusamation/internal/testutil"
)

func TestAgentDefaultsFollowDynamicOptions(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	bin := testutil.BuildFakeACP(t)
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
	if got := svc.sessionRequest(ctx, switched, ag).DefaultConfig; len(got) != 2 || got[1].Value != "high" {
		t.Fatalf("switch defaults = %#v", got)
	}
}

func TestAgentDefaultsSurviveLaterChoiceReset(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	bin := testutil.BuildFakeACP(t)
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
	tracePath := filepath.Join(dir, "trace.log")
	ag, err := st.CreateAgent(ctx, store.AgentInput{Name: "Fake", Slug: "fake-reset", Command: bin, Cwd: dir, Enabled: &enabled, Env: store.SecretList{{Name: "FAKE_ACP_MODEL_RESETS_EFFORT", Value: "1"}, {Name: "FAKE_ACP_TRACE", Value: tracePath}}})
	if err != nil {
		t.Fatal(err)
	}
	// effort picked before model: setting the model resets effort, so the
	// replay must re-apply effort instead of failing the order check.
	selections := []store.ConfigSelection{{ID: "effort", Value: "high"}, {ID: "model", Value: "fake-smart"}}
	configs, err := mgr.PreviewDefaults(ctx, ag.ID, selections)
	if err != nil {
		t.Fatalf("preview with reset order: %v", err)
	}
	if got := modeTestCurrent(configs, "effort"); got != "high" {
		t.Fatalf("preview effort = %q", got)
	}
	if got := modeTestCurrent(configs, "model"); got != "fake-smart" {
		t.Fatalf("preview model = %q", got)
	}
	if err := st.SetJSONSetting(ctx, "agent.defaults", store.AgentDefaults{AgentID: ag.ID, Selections: selections}); err != nil {
		t.Fatal(err)
	}
	conv, err := svc.CreateConversation(ctx, "", "reset", dir, "whatsapp", "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.PrepareSession(ctx, conv.ID); err != nil {
		t.Fatalf("prepare defaults: %v", err)
	}
	configs = mgr.Session(conv.ID).Config()
	if got := modeTestCurrent(configs, "effort"); got != "high" {
		t.Fatalf("session effort = %q", got)
	}
	if got := modeTestCurrent(configs, "model"); got != "fake-smart" {
		t.Fatalf("session model = %q", got)
	}
	// The settle pass must visibly re-apply effort after the model set.
	rawTrace, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatal(err)
	}
	modelIdx := strings.LastIndex(string(rawTrace), "config model fake-smart")
	effortIdx := strings.LastIndex(string(rawTrace), "config effort high")
	if modelIdx < 0 || effortIdx < modelIdx {
		t.Fatalf("effort was not re-applied after model:\n%s", rawTrace)
	}
}
