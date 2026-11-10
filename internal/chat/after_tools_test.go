package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jahrulnr/nusamation/internal/acp"
	"github.com/jahrulnr/nusamation/internal/agent"
	"github.com/jahrulnr/nusamation/internal/channels"
	"github.com/jahrulnr/nusamation/internal/events"
	"github.com/jahrulnr/nusamation/internal/security"
	"github.com/jahrulnr/nusamation/internal/store"
	"github.com/jahrulnr/nusamation/internal/testutil"
)

// afterToolsEnv records notices and wires a Service to the fake agent whose
// RPC trace is written to a file the test can read.
type afterToolsEnv struct {
	t         *testing.T
	svc       *Service
	st        *store.Store
	conv      store.Conversation
	agents    *agent.Manager
	tracePath string

	mu      sync.Mutex
	notices []string
}

func (e *afterToolsEnv) Broadcast(ev events.Event) {
	if ev.Type != "notice" {
		return
	}
	data, _ := ev.Data.(map[string]any)
	e.mu.Lock()
	e.notices = append(e.notices, fmt.Sprint(data["title"]))
	e.mu.Unlock()
}

func newAfterToolsEnv(t *testing.T) *afterToolsEnv {
	return newAfterToolsEnvWithPolicy(t, "auto_all")
}

func newAfterToolsEnvWithPolicy(t *testing.T, approvalPolicy string) *afterToolsEnv {
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
	workspace := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	e := &afterToolsEnv{t: t, st: st, agents: agentMgr, tracePath: filepath.Join(dir, "trace.log")}
	t.Setenv("FAKE_ACP_TRACE", e.tracePath)
	e.svc = New(Options{
		Store: st, Agents: agentMgr, Channels: channels.NewManager(events.Nop{}, logger), Events: e,
		Log: logger, UploadDir: filepath.Join(dir, "uploads"), Workspace: workspace,
	})
	agentMgr.SetSink(e.svc)
	t.Cleanup(agentMgr.StopAll)

	enabled := true
	ag, err := st.CreateAgent(context.Background(), store.AgentInput{
		Name: "Fake", Slug: "fake-test", Command: bin, Cwd: workspace, Enabled: &enabled,
		ApprovalPolicy: approvalPolicy,
	})
	if err != nil {
		t.Fatal(err)
	}
	conv, err := e.svc.CreateConversation(context.Background(), ag.ID, "Fake test", workspace, "web", "")
	if err != nil {
		t.Fatal(err)
	}
	e.conv = conv
	return e
}

func (e *afterToolsEnv) submit(text, delivery string) (store.Message, store.Turn) {
	e.t.Helper()
	msg, turn, err := e.svc.Submit(context.Background(), SubmitInput{
		ConversationID: e.conv.ID, Text: text, Channel: "web", Delivery: delivery,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return msg, turn
}

func (e *afterToolsEnv) waitStatus(turnID, status string) {
	e.t.Helper()
	waitFor(e.t, func() bool {
		turn, err := e.st.GetTurn(context.Background(), turnID)
		return err == nil && turn.Status == status
	}, "turn "+turnID+" to reach "+status)
}

func (e *afterToolsEnv) hasToolMessage(turnID string) bool {
	msgs, err := e.st.ListMessages(context.Background(), e.conv.ID, 0, 0, 500)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, m := range msgs {
		if m.Kind == "tool" && m.TurnID == turnID {
			return true
		}
	}
	return false
}

func (e *afterToolsEnv) noticeCount(title string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, got := range e.notices {
		if got == title {
			n++
		}
	}
	return n
}

func (e *afterToolsEnv) trace() []string {
	e.t.Helper()
	b, err := os.ReadFile(e.tracePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		e.t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func traceIndex(lines []string, want string) int {
	for i, l := range lines {
		if l == want {
			return i
		}
	}
	return -1
}

func countTrace(lines []string, want string) int {
	n := 0
	for _, l := range lines {
		if l == want {
			n++
		}
	}
	return n
}

// promptStart returns the request id and trace index of the prompt containing marker.
func promptStart(t *testing.T, lines []string, marker string) (string, int) {
	t.Helper()
	for i, l := range lines {
		if strings.HasPrefix(l, "prompt_start ") && strings.Contains(l, marker) {
			return strings.Fields(l)[1], i
		}
	}
	t.Fatalf("no prompt_start for %q in %v", marker, lines)
	return "", -1
}

func TestAfterToolsCancelsOnlyAfterToolsAndRunsInputsFIFO(t *testing.T) {
	e := newAfterToolsEnv(t)
	_, p1 := e.submit("after-tools-two-tools one", "")
	waitFor(t, func() bool { return e.hasToolMessage(p1.ID) }, "tool call")
	m2, p2 := e.submit("follow-up two", deliveryAfterTools)
	m3, p3 := e.submit("follow-up three", deliveryAfterTools)
	e.waitStatus(p1.ID, "cancelled")
	e.waitStatus(p2.ID, "completed")
	e.waitStatus(p3.ID, "completed")

	lines := e.trace()
	if n := countTrace(lines, "cancel"); n != 1 {
		t.Fatalf("cancel sent %d times: %v", n, lines)
	}
	id1, _ := promptStart(t, lines, "after-tools-two-tools one")
	_, start2 := promptStart(t, lines, "follow-up two")
	toolB := traceIndex(lines, "tool_done tool-b")
	cancel := traceIndex(lines, "cancel")
	respond1 := traceIndex(lines, "respond "+id1)
	if !(toolB < cancel && cancel < respond1 && respond1 < start2) {
		t.Fatalf("order violated: tool-b=%d cancel=%d respond1=%d start2=%d\n%v", toolB, cancel, respond1, start2, lines)
	}

	assertTarget := func(msg store.Message, want string) {
		t.Helper()
		var meta map[string]any
		if err := json.Unmarshal(msg.Metadata, &meta); err != nil {
			t.Fatal(err)
		}
		if meta["delivery"] != deliveryAfterTools {
			t.Fatalf("delivery metadata = %v", meta)
		}
		if meta["interruptTurnId"] != want {
			t.Fatalf("interruptTurnId = %v, want %q", meta["interruptTurnId"], want)
		}
	}
	stored2, err := e.st.GetMessage(context.Background(), m2.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertTarget(stored2, p1.ID)
	stored3, err := e.st.GetMessage(context.Background(), m3.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored3.Metadata), "interruptTurnId") {
		t.Fatalf("non-head input armed an interrupt: %s", stored3.Metadata)
	}
}

func TestAfterToolsIgnoredCancelKeepsQueueAndNotifiesOnce(t *testing.T) {
	e := newAfterToolsEnv(t)
	e.svc.cancelNoticeAfter = 50 * time.Millisecond
	_, p1 := e.submit("after-tools-ignore-cancel one", "")
	waitFor(t, func() bool { return e.hasToolMessage(p1.ID) }, "tool call")
	_, p2 := e.submit("follow-up ignored", deliveryAfterTools)
	e.waitStatus(p1.ID, "cancelled")
	e.waitStatus(p2.ID, "completed")

	lines := e.trace()
	id1, _ := promptStart(t, lines, "after-tools-ignore-cancel one")
	_, start2 := promptStart(t, lines, "follow-up ignored")
	if respond1 := traceIndex(lines, "respond "+id1); respond1 < 0 || respond1 > start2 {
		t.Fatalf("second prompt started before first answered: %v", lines)
	}
	if n := e.noticeCount("Cancelling"); n != 1 {
		t.Fatalf("cancel notices = %d", n)
	}
}

// A second /stop on an after_tools turn escalates past the graceful
// session/cancel the agent is ignoring and drops the turn context instead.
func TestSecondCancelOnAfterToolsTurnEscalates(t *testing.T) {
	e := newAfterToolsEnv(t)
	_, p1 := e.submit("after-tools-ignore-cancel one", "")
	waitFor(t, func() bool { return e.hasToolMessage(p1.ID) }, "tool call")
	_, p2 := e.submit("follow-up ignored", deliveryAfterTools)
	if err := e.svc.Cancel(context.Background(), e.conv.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Cancel(context.Background(), e.conv.ID); err != nil {
		t.Fatal(err)
	}
	e.waitStatus(p1.ID, "cancelled")
	e.waitStatus(p2.ID, "completed")
	waitFor(t, func() bool { return countTrace(e.trace(), "cancel") >= 2 }, "escalated session/cancel on the wire")
}

func TestDefaultQueueDoesNotCancelRunningTurn(t *testing.T) {
	e := newAfterToolsEnv(t)
	_, p1 := e.submit("after-tools-ignore-cancel plain", "")
	waitFor(t, func() bool { return e.hasToolMessage(p1.ID) }, "tool call")
	_, p2 := e.submit("follow-up plain", deliveryQueue)
	e.waitStatus(p1.ID, "completed")
	e.waitStatus(p2.ID, "completed")
	if n := countTrace(e.trace(), "cancel"); n != 0 {
		t.Fatalf("default queue sent %d cancels", n)
	}
}

func TestUnknownDeliveryRejectedBeforeWrites(t *testing.T) {
	e := newAfterToolsEnv(t)
	count := func() int {
		msgs, err := e.st.ListMessages(context.Background(), e.conv.ID, 0, 0, 500)
		if err != nil {
			t.Fatal(err)
		}
		return len(msgs)
	}
	before := count()
	_, _, err := e.svc.Submit(context.Background(), SubmitInput{
		ConversationID: e.conv.ID, Text: "hello", Channel: "web", Delivery: "sideways",
	})
	if err != ErrInvalidDelivery {
		t.Fatalf("err = %v", err)
	}
	if after := count(); after != before {
		t.Fatalf("messages %d -> %d", before, after)
	}
}

func TestAfterToolsBoundary(t *testing.T) {
	newTurn := func() *activeTurn {
		return newActiveTurn(store.Conversation{ID: "c"}, store.Turn{ID: "t"}, store.Agent{}, store.Message{},
			context.Background(), func() {})
	}
	withTools := func(statuses ...string) *activeTurn {
		a := newTurn()
		a.afterTools = true
		a.markPromptSent()
		for i, s := range statuses {
			a.tools[fmt.Sprint(i)] = &toolState{Status: s}
		}
		return a
	}
	cases := []struct {
		name string
		a    *activeTurn
		want bool
	}{
		{"not armed", func() *activeTurn { a := withTools("completed"); a.afterTools = false; return a }(), false},
		{"prompt not sent", func() *activeTurn { a := newTurn(); a.afterTools = true; return a }(), false},
		{"no tools", withTools(), true},
		{"two completed", withTools("completed", "completed"), true},
		{"failed is terminal", withTools("completed", "failed"), true},
		{"one pending", withTools("completed", "pending"), false},
		{"in progress", withTools("in_progress"), false},
		{"missing status", withTools(""), false},
		{"unknown status", withTools("weird"), false},
		{"live terminal", func() *activeTurn {
			a := withTools("completed")
			a.terminals = map[string]agent.TerminalUpdate{"term": {ID: "term"}}
			return a
		}(), false},
		{"exited terminal", func() *activeTurn {
			a := withTools("completed")
			a.terminals = map[string]agent.TerminalUpdate{"term": {ID: "term", ExitStatus: &acp.TerminalExitStatus{}}}
			return a
		}(), true},
		{"approval pending", func() *activeTurn { a := withTools("completed"); a.approvals["ap"] = true; return a }(), false},
	}
	for _, c := range cases {
		if got := c.a.afterToolsBoundaryReached(); got != c.want {
			t.Fatalf("%s: boundary = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestAfterToolsCancelRechecksBoundary(t *testing.T) {
	a := newActiveTurn(store.Conversation{ID: "c"}, store.Turn{ID: "t"}, store.Agent{}, store.Message{}, context.Background(), func() {})
	a.afterTools = true
	a.markPromptSent()
	if !a.afterToolsBoundaryReached() {
		t.Fatal("expected an initially clear boundary")
	}
	a.tools["new"] = &toolState{Status: "in_progress"}
	if (&Service{}).requestAfterToolsCancel(a) {
		t.Fatal("cancel started after a tool became active")
	}
	if a.isCancelRequested() {
		t.Fatal("active tool was cancelled")
	}
}

func TestArmAfterToolsIgnoresFinishedTurn(t *testing.T) {
	a := newActiveTurn(store.Conversation{ID: "c"}, store.Turn{ID: "t"}, store.Agent{}, store.Message{},
		context.Background(), func() {})
	a.finished = true
	if _, ok := a.armAfterTools("queued-1"); ok || a.afterTools {
		t.Fatal("finished turn was armed")
	}
}

func TestDisarmAfterToolsByMatchesQueuedTurn(t *testing.T) {
	a := newActiveTurn(store.Conversation{ID: "c"}, store.Turn{ID: "t"}, store.Agent{}, store.Message{},
		context.Background(), func() {})
	if _, ok := a.armAfterTools("queued-1"); !ok || !a.afterTools {
		t.Fatal("turn was not armed")
	}
	// A different queued turn's cancel must not clear the arm.
	a.disarmAfterToolsBy("queued-2")
	if !a.afterTools {
		t.Fatal("arm cleared by a different turn id")
	}
	a.disarmAfterToolsBy("queued-1")
	if a.afterTools || a.afterToolsBy != "" {
		t.Fatal("arm still set after its queued turn was cancelled")
	}
}
