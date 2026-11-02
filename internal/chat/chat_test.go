package chat

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jahrulnr/nusamation/internal/agent"
	"github.com/jahrulnr/nusamation/internal/channels"
	"github.com/jahrulnr/nusamation/internal/events"
	"github.com/jahrulnr/nusamation/internal/security"
	"github.com/jahrulnr/nusamation/internal/store"
)

func TestOrchestratorApprovalFlow(t *testing.T) {
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
		Name: "Fake", Slug: "fake-test", Command: bin, Cwd: workspace, Enabled: &enabled,
		ApprovalPolicy: "ask",
	})
	if err != nil {
		t.Fatal(err)
	}
	conv, err := svc.CreateConversation(context.Background(), ag.ID, "Fake test", workspace, "web", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Submit(context.Background(), SubmitInput{
		ConversationID: conv.ID, Text: "Run the demo and inspect the workspace.", Channel: "web",
	}); err != nil {
		t.Fatal(err)
	}

	var approval store.Approval
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		list, err := st.ListApprovalsForConversation(context.Background(), conv.ID, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(list) > 0 {
			approval = list[0]
			break
		}
		time.Sleep(80 * time.Millisecond)
	}
	if approval.ID == "" {
		t.Fatal("timed out waiting for an approval")
	}
	if approval.Title != "Read workspace files" || approval.Kind != "read" {
		t.Fatalf("unexpected approval: %#v", approval)
	}
	option := ""
	for _, o := range approval.Options {
		if o.Kind == "allow_once" {
			option = o.OptionID
			break
		}
	}
	if option == "" {
		t.Fatal("allow_once option missing")
	}
	if _, err := svc.ResolveApproval(context.Background(), approval.ID, option); err != nil {
		t.Fatal(err)
	}

	var final store.Turn
	for time.Now().Before(deadline) {
		turn, err := st.GetTurn(context.Background(), approval.TurnID)
		if err == nil && (turn.Status == "completed" || turn.Status == "error" || turn.Status == "cancelled") {
			final = turn
			break
		}
		time.Sleep(80 * time.Millisecond)
	}
	if final.Status != "completed" {
		t.Fatalf("turn did not complete: %#v", final)
	}
	if final.StopReason != "end_turn" {
		t.Fatalf("stop reason = %q", final.StopReason)
	}
	msgs, err := st.ListMessages(context.Background(), conv.ID, 0, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var thought, tool, text bool
	for _, m := range msgs {
		switch m.Kind {
		case "thought":
			thought = thought || strings.Contains(m.Text, "fake agent")
		case "tool":
			tool = tool || m.ToolCallID == "fake-tool-1"
			if m.Status != "complete" {
				t.Fatalf("tool message status = %q", m.Status)
			}
		case "text":
			if m.Role == "assistant" {
				text = text || strings.Contains(m.Text, "inspected the workspace safely")
			}
		}
	}
	if !thought || !tool || !text {
		t.Fatalf("missing streamed messages thought=%v tool=%v text=%v: %#v", thought, tool, text, msgs)
	}
	ap, err := st.GetApproval(context.Background(), approval.ID)
	if err != nil || ap.Status != "approved" {
		t.Fatalf("approval = %#v, %v", ap, err)
	}
	convNow, err := st.GetConversation(context.Background(), conv.ID)
	if err != nil || convNow.Status != "idle" {
		t.Fatalf("conversation status = %#v, %v", convNow, err)
	}
}

func TestParseCommandAndHelpers(t *testing.T) {
	cases := []struct {
		in   string
		cmd  string
		args []string
	}{
		{"/help", "help", nil},
		{"/approve ap_123", "approve", []string{"ap_123"}},
		{"/model@nusa fake-fast", "model", []string{"fake-fast"}},
		{"hello", "", nil},
		{" /new fake ", "new", []string{"fake"}},
	}
	for _, c := range cases {
		cmd, args := parseCommand(c.in)
		if cmd != c.cmd || strings.Join(args, " ") != strings.Join(c.args, " ") {
			t.Fatalf("parseCommand(%q) = %q %#v", c.in, cmd, args)
		}
	}
	if got := autoTitle("first line\nsecond", nil); got != "first line" {
		t.Fatalf("autoTitle = %q", got)
	}
	opts := ParseConfigOptions([]byte(`[{"id":"model","category":"model","type":"select","currentValue":"a","options":[{"value":"a"},{"value":"b"}]}]`))
	if len(opts) != 1 || modelFromConfigs([]byte(`[{"id":"model","category":"model","currentValue":"b"}]`)) != "b" {
		t.Fatalf("config parsing failed: %#v", opts)
	}
	if err := configValueAllowed(opts[0], "b"); err != nil {
		t.Fatal(err)
	}
	if err := configValueAllowed(opts[0], "nope"); err == nil {
		t.Fatal("invalid config value accepted")
	}
}
