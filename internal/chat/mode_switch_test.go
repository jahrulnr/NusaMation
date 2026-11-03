package chat

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jahrulnr/nusamation/internal/agent"
	"github.com/jahrulnr/nusamation/internal/channels"
	"github.com/jahrulnr/nusamation/internal/events"
	"github.com/jahrulnr/nusamation/internal/security"
	"github.com/jahrulnr/nusamation/internal/store"
)

func TestMidTurnModeSwitchSkipsLaterApprovals(t *testing.T) {
	cases := []struct {
		name string
		env  store.SecretList
	}{
		{name: "config-option"},
		{name: "legacy-modes", env: store.SecretList{{Name: "FAKE_ACP_MODES", Value: "legacy"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { runMidTurnModeSwitch(t, tc.env) })
	}
}

func runMidTurnModeSwitch(t *testing.T, env store.SecretList) {
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "fake-acp")
	build := exec.Command("go", "build", "-o", bin, "./cmd/fake-acp-agent")
	build.Dir = repo
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake agent: %v\n%s", err, out)
	}

	dir := t.TempDir()
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
	defer agentMgr.StopAll()

	enabled := true
	ag, err := st.CreateAgent(context.Background(), store.AgentInput{
		Name: "Fake", Slug: "fake-mode", Command: bin, Cwd: workspace, Enabled: &enabled,
		ApprovalPolicy: "ask", Env: env,
	})
	if err != nil {
		t.Fatal(err)
	}
	conv, err := svc.CreateConversation(context.Background(), ag.ID, "Mode switch", workspace, "web", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.PrepareSession(context.Background(), conv.ID); err != nil {
		t.Fatal(err)
	}
	if got := modeTestCurrent(agentMgr.Session(conv.ID).Config(), "mode"); got != "ask" {
		t.Fatalf("initial mode = %q, want ask", got)
	}
	if _, _, err := svc.Submit(context.Background(), SubmitInput{
		ConversationID: conv.ID, Text: "mode-switch-test: run both tools.", Channel: "web",
	}); err != nil {
		t.Fatal(err)
	}

	first := modeTestWaitApproval(t, st, conv.ID)
	updated, err := svc.SetConfig(context.Background(), conv.ID, "mode", "bypass")
	if err != nil {
		t.Fatalf("switch mode while the approval is pending: %v", err)
	}
	if got := modeTestCurrent(updated.ConfigOptions, "mode"); got != "bypass" {
		t.Fatalf("stored mode = %q, want bypass", got)
	}
	if got := modeTestCurrent(agentMgr.Session(conv.ID).Config(), "mode"); got != "bypass" {
		t.Fatalf("live session mode = %q, want bypass", got)
	}

	option := ""
	for _, o := range first.Options {
		if o.Kind == "allow_once" {
			option = o.OptionID
		}
	}
	if option == "" {
		t.Fatal("allow_once option missing")
	}
	if _, err := svc.ResolveApproval(context.Background(), first.ID, option); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(30 * time.Second)
	for {
		turn, err := st.GetTurn(context.Background(), first.TurnID)
		if err == nil && (turn.Status == "completed" || turn.Status == "error" || turn.Status == "cancelled") {
			if turn.Status != "completed" {
				t.Fatalf("turn ended with status %s: %s", turn.Status, turn.Error)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the turn to finish")
		}
		time.Sleep(80 * time.Millisecond)
	}

	list, err := st.ListApprovalsForConversation(context.Background(), conv.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("expected only the first tool call to ask for approval, got %d approvals", len(list))
	}
}

func modeTestWaitApproval(t *testing.T, st *store.Store, conversationID string) store.Approval {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		list, err := st.ListApprovalsForConversation(context.Background(), conversationID, 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range list {
			if a.Status == "pending" {
				return a
			}
		}
		time.Sleep(80 * time.Millisecond)
	}
	t.Fatal("timed out waiting for a pending approval")
	return store.Approval{}
}

func modeTestCurrent(raw json.RawMessage, configID string) string {
	for _, o := range ParseConfigOptions(raw) {
		if o.ID == configID {
			value, _ := o.CurrentValue.(string)
			return value
		}
	}
	return ""
}
