package chat

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jahrulnr/nusamation/internal/agent"
	"github.com/jahrulnr/nusamation/internal/channels"
	"github.com/jahrulnr/nusamation/internal/events"
	"github.com/jahrulnr/nusamation/internal/security"
	"github.com/jahrulnr/nusamation/internal/store"
	"github.com/jahrulnr/nusamation/internal/testutil"
)

type selectorFixture struct {
	svc       *Service
	st        *store.Store
	mgr       *agent.Manager
	conv      store.Conversation
	ch        *notifyChannel
	in        channels.Inbound
	tracePath string
}

// newSelectorFixture builds a chat service backed by the fake ACP agent and
// prepares its session, so conv.ConfigOptions holds the live catalog.
func newSelectorFixture(t *testing.T, env store.SecretList) *selectorFixture {
	t.Helper()
	bin := testutil.BuildFakeACP(t)
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sealer, err := security.LoadOrCreateSealer(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	st.SetSealer(sealer)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	agentMgr := agent.NewManager(st, logger)
	chanMgr := channels.NewManager(events.Nop{}, logger)
	workspace := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	svc := New(Options{
		Store: st, Agents: agentMgr, Channels: chanMgr, Events: events.Nop{},
		Log: logger, UploadDir: filepath.Join(dir, "uploads"), Workspace: workspace,
	})
	agentMgr.SetSink(svc)
	t.Cleanup(agentMgr.StopAll)

	tracePath := filepath.Join(dir, "trace.log")
	env = append(env, store.EnvVar{Name: "FAKE_ACP_TRACE", Value: tracePath})
	enabled := true
	ag, err := st.CreateAgent(context.Background(), store.AgentInput{
		Name: "Fake", Slug: "fake-sel", Command: bin, Cwd: workspace, Enabled: &enabled,
		ApprovalPolicy: "ask", Env: env,
	})
	if err != nil {
		t.Fatal(err)
	}
	conv, err := svc.CreateConversation(context.Background(), ag.ID, "Selector", workspace, "web", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.PrepareSession(context.Background(), conv.ID); err != nil {
		t.Fatal(err)
	}
	ch := &notifyChannel{}
	chanMgr.Register(ch)
	return &selectorFixture{
		svc: svc, st: st, mgr: agentMgr, conv: conv, ch: ch, tracePath: tracePath,
		in: channels.Inbound{Channel: "notify", ExternalID: "user-1", SenderID: "u1", Text: "/x"},
	}
}

func selectorTrace(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(string(raw), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func configCurrent(opts []ConfigOption, id string) string {
	for _, o := range opts {
		if o.ID == id {
			return configValueString(o.CurrentValue)
		}
	}
	return ""
}

func (f *selectorFixture) storedCurrent(t *testing.T, id string) string {
	t.Helper()
	opts, err := f.svc.ConversationConfig(context.Background(), f.conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	return configCurrent(opts, id)
}

func TestSetSelectorDynamicConfig(t *testing.T) {
	fx := newSelectorFixture(t, store.SecretList{{Name: "FAKE_ACP_DYNAMIC_CONFIG", Value: "1"}})
	// effort=high does not exist until the model is fake-smart; validating the
	// modifier before switching the model would reject it.
	res, err := fx.svc.SetSelector(context.Background(), fx.conv.ID, "fake-smart[effort=high]")
	if err != nil {
		t.Fatalf("SetSelector: %v", err)
	}
	if res.Model != "fake-smart" || len(res.Failed) != 0 {
		t.Fatalf("result = %#v", res)
	}
	if len(res.Applied) != 1 || res.Applied[0].Key != "effort" || res.Applied[0].Value != "high" {
		t.Fatalf("applied = %#v", res.Applied)
	}
	if got := fx.storedCurrent(t, "effort"); got != "high" {
		t.Fatalf("stored effort = %q", got)
	}
	if got := fx.storedCurrent(t, "model"); got != "fake-smart" {
		t.Fatalf("stored model = %q", got)
	}
	live := fx.mgr.Session(fx.conv.ID)
	if live == nil || configCurrent(ParseConfigOptions(live.Config()), "effort") != "high" {
		t.Fatal("live session did not take effort=high")
	}
}

func TestSetSelectorAppliesModifierAfterModelReset(t *testing.T) {
	fx := newSelectorFixture(t, store.SecretList{{Name: "FAKE_ACP_MODEL_RESETS_EFFORT", Value: "1"}})
	res, err := fx.svc.SetSelector(context.Background(), fx.conv.ID, "fake-smart[effort=high]")
	if err != nil {
		t.Fatalf("SetSelector: %v", err)
	}
	if len(res.Applied) != 1 || len(res.Failed) != 0 {
		t.Fatalf("result = %#v", res)
	}
	// The model switch resets effort to low inside the fake agent; the modifier
	// must land afterwards, so the final value is high, not the reset low.
	if got := fx.storedCurrent(t, "effort"); got != "high" {
		t.Fatalf("final effort = %q, want high (modifier must run after the model switch)", got)
	}
	lines := selectorTrace(t, fx.tracePath)
	mi, ei := traceIndex(lines, "config model fake-smart"), traceIndex(lines, "config effort high")
	if mi < 0 || ei < 0 || mi > ei {
		t.Fatalf("model must be set before the modifier:\n%s", strings.Join(lines, "\n"))
	}
}

func TestSetSelectorPartialFailure(t *testing.T) {
	fx := newSelectorFixture(t, store.SecretList{{Name: "FAKE_ACP_MODEL_RESETS_EFFORT", Value: "1"}})
	res, err := fx.svc.SetSelector(context.Background(), fx.conv.ID, "fake-smart[effort=bogus,nope=1]")
	if err != nil {
		t.Fatalf("SetSelector: %v", err)
	}
	if res.Model != "fake-smart" {
		t.Fatalf("model must still switch — no rollback: %#v", res)
	}
	if len(res.Failed) != 2 {
		t.Fatalf("failed = %#v", res.Failed)
	}
	if len(res.Failed[0].Offers) != 2 || res.Failed[0].Offers[0] != "low" {
		t.Fatalf("failed value must list offers: %#v", res.Failed[0])
	}
	conv, err := fx.st.GetConversation(context.Background(), fx.conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if conv.Model != "fake-smart" {
		t.Fatalf("conv.Model = %q — must hold the plain applied value", conv.Model)
	}
}

func TestSetSelectorAmbiguous(t *testing.T) {
	fx := newSelectorFixture(t, nil)
	res, err := fx.svc.SetSelector(context.Background(), fx.conv.ID, "fake")
	var amb *AmbiguousError
	if !errors.As(err, &amb) {
		t.Fatalf("expected AmbiguousError, got %v", err)
	}
	if len(res.Candidates) != 2 {
		t.Fatalf("candidates = %#v", res.Candidates)
	}
	if res.Model != "" {
		t.Fatalf("nothing may be applied on ambiguity: %#v", res)
	}
}

func TestSetSelectorNoModelOption(t *testing.T) {
	fx := newSelectorFixture(t, nil)
	if err := fx.st.SetConversationConfig(context.Background(), fx.conv.ID, "", mustJSON([]map[string]any{
		{"id": "effort", "name": "Effort", "category": "thought_level", "type": "select", "currentValue": "low",
			"options": []map[string]any{{"value": "low"}, {"value": "high"}}},
	})); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.svc.SetSelector(context.Background(), fx.conv.ID, "fake-smart[effort=high]"); err == nil ||
		!strings.Contains(err.Error(), "no model option") {
		t.Fatalf("expected a clear no-model-option error, got %v", err)
	}
}

func TestCommandModeUsesLegacySetMode(t *testing.T) {
	fx := newSelectorFixture(t, store.SecretList{{Name: "FAKE_ACP_MODES", Value: "legacy"}})
	if err := fx.svc.commandMode(context.Background(), fx.in, fx.conv, true, []string{"bypass"}); err != nil {
		t.Fatalf("commandMode: %v", err)
	}
	lines := selectorTrace(t, fx.tracePath)
	if traceIndex(lines, "set_mode bypass") < 0 {
		t.Fatalf("expected session/set_mode on the wire:\n%s", strings.Join(lines, "\n"))
	}
	if traceIndex(lines, "config mode bypass") >= 0 {
		t.Fatalf("legacy mode must not go through session/set_config_option:\n%s", strings.Join(lines, "\n"))
	}
	if got := fx.storedCurrent(t, "mode"); got != "bypass" {
		t.Fatalf("stored mode = %q", got)
	}
	sent := fx.ch.sent()
	if len(sent) == 0 || !strings.Contains(sent[len(sent)-1], "bypass") {
		t.Fatalf("reply should confirm the new mode: %#v", sent)
	}
}
