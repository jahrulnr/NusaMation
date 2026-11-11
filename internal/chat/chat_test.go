package chat

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

	"github.com/jahrulnr/nusamation/internal/agent"
	"github.com/jahrulnr/nusamation/internal/channels"
	"github.com/jahrulnr/nusamation/internal/events"
	"github.com/jahrulnr/nusamation/internal/security"
	"github.com/jahrulnr/nusamation/internal/store"
	"github.com/jahrulnr/nusamation/internal/testutil"
)

func TestOrchestratorApprovalFlow(t *testing.T) {
	bin := testutil.BuildFakeACP(t)

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
	if err := svc.PrepareSession(context.Background(), conv.ID); err != nil {
		t.Fatal(err)
	}
	if agentMgr.Session(conv.ID) == nil {
		t.Fatal("prepared session was not registered")
	}
	// Commands can arrive during session/new, before any prompt is active.
	commandDeadline := time.Now().Add(5 * time.Second)
	for {
		stored, err := st.GetConversation(context.Background(), conv.ID)
		if err != nil {
			t.Fatal(err)
		}
		var meta struct {
			Commands []struct {
				Name string `json:"name"`
			} `json:"commands"`
			SessionID string `json:"commandsSessionId"`
		}
		_ = json.Unmarshal(stored.Metadata, &meta)
		if len(meta.Commands) > 0 && meta.Commands[0].Name == "compact" && meta.SessionID == stored.ACPSessionID {
			break
		}
		if time.Now().After(commandDeadline) {
			t.Fatalf("session commands not saved: %s", stored.Metadata)
		}
		time.Sleep(10 * time.Millisecond)
	}
	updated, err := svc.SetConfig(context.Background(), conv.ID, "model", "fake-smart")
	if err != nil {
		t.Fatal(err)
	}
	if modelFromConfigs(updated.ConfigOptions) != "fake-smart" || modelFromConfigs(agentMgr.Session(conv.ID).Config()) != "fake-smart" {
		t.Fatal("model selection did not reach the live ACP session")
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

	_, terminalTurn, err := svc.Submit(context.Background(), SubmitInput{ConversationID: conv.ID, Text: "timeline-terminal-test", Channel: "web"})
	if err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(10 * time.Second)
	var sawLive, sawFinal bool
	for time.Now().Before(deadline) {
		messages, err := st.ListMessages(context.Background(), conv.ID, 0, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range messages {
			if m.TurnID != terminalTurn.ID || m.ToolCallID != "terminal-demo" {
				continue
			}
			var payload struct {
				Terminals map[string]agent.TerminalUpdate `json:"terminals"`
			}
			if err := json.Unmarshal(m.Content, &payload); err != nil {
				t.Fatal(err)
			}
			for _, term := range payload.Terminals {
				if term.Output == "first" && term.ExitStatus == nil {
					sawLive = true
				}
				if term.Output == "firstsecond" && term.ExitStatus != nil && term.ExitStatus.ExitCode != nil && *term.ExitStatus.ExitCode == 3 {
					sawFinal = true
				}
			}
		}
		if sawLive && sawFinal {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if !sawLive || !sawFinal {
		t.Fatalf("terminal updates: live=%v final=%v", sawLive, sawFinal)
	}
	for time.Now().Before(deadline) {
		turn, _ := st.GetTurn(context.Background(), terminalTurn.ID)
		if turn.Status == "completed" {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	_, backgroundTurn, err := svc.Submit(context.Background(), SubmitInput{ConversationID: conv.ID, Text: "timeline-background-test", Channel: "web"})
	if err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(10 * time.Second)
	sawCompletedBeforeOutput := false
	var backgroundFinal bool
	for time.Now().Before(deadline) {
		turn, _ := st.GetTurn(context.Background(), backgroundTurn.ID)
		messages, err := st.ListMessages(context.Background(), conv.ID, 0, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range messages {
			if m.TurnID != backgroundTurn.ID || m.ToolCallID != "terminal-demo" {
				continue
			}
			var payload struct {
				Terminals map[string]agent.TerminalUpdate `json:"terminals"`
			}
			if err := json.Unmarshal(m.Content, &payload); err != nil {
				t.Fatal(err)
			}
			for _, term := range payload.Terminals {
				if turn.Status == "completed" && term.ExitStatus == nil {
					sawCompletedBeforeOutput = true
				}
				if term.Output == "firstsecond" && term.ExitStatus != nil {
					backgroundFinal = true
				}
			}
		}
		if sawCompletedBeforeOutput && backgroundFinal {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if !sawCompletedBeforeOutput || !backgroundFinal {
		t.Fatalf("background terminal: turn finished first=%v final output=%v", sawCompletedBeforeOutput, backgroundFinal)
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
