package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jahrulnr/nusamation/internal/agent"
	"github.com/jahrulnr/nusamation/internal/channels"
	"github.com/jahrulnr/nusamation/internal/chat"
	"github.com/jahrulnr/nusamation/internal/events"
	"github.com/jahrulnr/nusamation/internal/mcp/grants"
	"github.com/jahrulnr/nusamation/internal/security"
	"github.com/jahrulnr/nusamation/internal/store"
	"github.com/jahrulnr/nusamation/internal/testutil"
)

type controlFixture struct {
	srv    *Server
	store  *store.Store
	grants *grants.Grants
	caller store.Conversation
	target store.Conversation
	token  string
	agent  store.Agent
}

func newControlFixture(t *testing.T) *controlFixture {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	sealer, err := security.LoadOrCreateSealer(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	st.SetSealer(sealer)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	enabled := true
	ag, err := st.CreateAgent(context.Background(), store.AgentInput{
		Name: "Control Agent", Slug: "control-agent", Command: "/bin/echo", Cwd: dir, Enabled: &enabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	caller, err := st.CreateConversation(context.Background(), ag.ID, "Supervisor", "", "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	target, err := st.CreateConversation(context.Background(), ag.ID, "Worker", "", "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	g := grants.NewHashed("")
	svc := chat.New(chat.Options{Store: st, Log: logger})
	return &controlFixture{
		srv: &Server{
			store: st, chat: svc, conversation: g, waits: map[string]int{}, log: logger,
		},
		store: st, grants: g, caller: caller, target: target,
		token: g.Token(caller.ID), agent: ag,
	}
}

func newLiveControlFixture(t *testing.T) *controlFixture {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := security.LoadOrCreateSealer(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	st.SetSealer(sealer)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	workspace := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	agentMgr := agent.NewManager(st, logger)
	chanMgr := channels.NewManager(events.Nop{}, logger)
	svc := chat.New(chat.Options{
		Store: st, Agents: agentMgr, Channels: chanMgr, Events: events.Nop{}, Log: logger,
		UploadDir: filepath.Join(dir, "uploads"), Workspace: workspace,
	})
	agentMgr.SetSink(svc)
	t.Cleanup(func() {
		agentMgr.StopAll()
		_ = st.Close()
	})
	enabled := true
	ag, err := st.CreateAgent(context.Background(), store.AgentInput{
		Name: "Fake Agent", Slug: "fake-control", Command: testutil.BuildFakeACP(t), Cwd: workspace,
		Enabled: &enabled, ApprovalPolicy: "auto_all",
	})
	if err != nil {
		t.Fatal(err)
	}
	caller, err := st.CreateConversation(context.Background(), ag.ID, "Supervisor", workspace, "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	target, err := st.CreateConversation(context.Background(), ag.ID, "Worker", workspace, "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	g := grants.NewHashed("")
	return &controlFixture{
		srv: &Server{
			store: st, chat: svc, conversation: g, waits: map[string]int{}, log: logger,
		},
		store: st, grants: g, caller: caller, target: target,
		token: g.Token(caller.ID), agent: ag,
	}
}

func (f *controlFixture) enable(t *testing.T, origin string) {
	t.Helper()
	ctx := context.Background()
	if err := f.store.SetSetting(ctx, "orchestration.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(map[string]any{
		"orchestration": map[string]any{"enabled": true, "scope": "all"},
	})
	if err := f.store.UpdateConversationMetadata(ctx, f.caller.ID, meta); err != nil {
		t.Fatal(err)
	}
	msg := store.Message{ConversationID: f.caller.ID, Role: "user", Kind: "text", Text: "go"}
	if err := f.store.AppendMessage(ctx, &msg); err != nil {
		t.Fatal(err)
	}
	turn, err := f.store.CreateTurn(ctx, f.caller.ID, msg.ID, origin, "")
	if err != nil {
		t.Fatal(err)
	}
	if started, err := f.store.StartTurn(ctx, turn.ID); err != nil || !started {
		t.Fatalf("start = %v err=%v", started, err)
	}
}

func (f *controlFixture) post(t *testing.T, body map[string]any) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/conversation-control", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+f.token)
	rr := httptest.NewRecorder()
	f.srv.handleConversationControl(rr, req)
	var out map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	return rr.Code, out
}

func TestConversationControlAuthAndSwitch(t *testing.T) {
	f := newControlFixture(t)

	req := httptest.NewRequest(http.MethodPost, "/api/conversation-control", strings.NewReader(`{"tool":"list"}`))
	rr := httptest.NewRecorder()
	f.srv.handleConversationControl(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("missing token status = %d", rr.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/conversation-control", strings.NewReader(`{"tool":"list"}`))
	req.Header.Set("Authorization", "Bearer nope")
	rr = httptest.NewRecorder()
	f.srv.handleConversationControl(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("bad token status = %d", rr.Code)
	}

	status, out := f.post(t, map[string]any{"tool": "list"})
	if status != http.StatusForbidden || out["code"] != "orchestration_disabled" {
		t.Fatalf("global off status=%d body=%v", status, out)
	}

	if err := f.store.SetSetting(context.Background(), "orchestration.enabled", "true"); err != nil {
		t.Fatal(err)
	}
	status, out = f.post(t, map[string]any{"tool": "list"})
	if status != http.StatusForbidden || out["code"] != "policy_denied" {
		t.Fatalf("per-chat off status=%d body=%v", status, out)
	}

	f.enable(t, "web")
	status, out = f.post(t, map[string]any{"tool": "list"})
	if status != http.StatusOK {
		t.Fatalf("enabled status=%d body=%v", status, out)
	}
}

func TestConversationControlOrchestratedCallerDenied(t *testing.T) {
	f := newControlFixture(t)
	f.enable(t, "agent")
	status, out := f.post(t, map[string]any{"tool": "list"})
	if status != http.StatusForbidden || out["code"] != "orchestrated_turn" {
		t.Fatalf("status=%d body=%v", status, out)
	}
}

func TestConversationControlListAndRead(t *testing.T) {
	f := newControlFixture(t)
	f.enable(t, "web")
	ctx := context.Background()

	if err := f.store.SetConversationStatus(ctx, f.target.ID, "active"); err != nil {
		t.Fatal(err)
	}
	text := store.Message{ConversationID: f.target.ID, Role: "assistant", Kind: "text", Text: "worker output"}
	if err := f.store.AppendMessage(ctx, &text); err != nil {
		t.Fatal(err)
	}
	thought := store.Message{ConversationID: f.target.ID, Role: "assistant", Kind: "thought", Text: "secret reasoning"}
	if err := f.store.AppendMessage(ctx, &thought); err != nil {
		t.Fatal(err)
	}
	tool := store.Message{ConversationID: f.target.ID, Role: "assistant", Kind: "tool", Text: "Run tests", ToolCallID: "tc_1"}
	if err := f.store.AppendMessage(ctx, &tool); err != nil {
		t.Fatal(err)
	}
	ap := store.Approval{ConversationID: f.target.ID, Title: "Run command", Kind: "execute"}
	if err := f.store.CreateApproval(ctx, &ap); err != nil {
		t.Fatal(err)
	}

	status, out := f.post(t, map[string]any{"tool": "list", "query": "Worker"})
	if status != http.StatusOK {
		t.Fatalf("list status=%d body=%v", status, out)
	}
	list, _ := out["conversations"].([]any)
	if len(list) != 1 {
		t.Fatalf("list = %#v", out["conversations"])
	}
	row := list[0].(map[string]any)
	if row["id"] != f.target.ID || row["isSelf"] != nil && row["isSelf"] == true {
		t.Fatalf("row = %#v", row)
	}

	status, out = f.post(t, map[string]any{"tool": "read", "conversationId": f.target.ID})
	if status != http.StatusOK {
		t.Fatalf("read status=%d body=%v", status, out)
	}
	msgs, _ := out["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages = %#v", out["messages"])
	}
	for _, raw := range msgs {
		if raw.(map[string]any)["kind"] == "thought" {
			t.Fatal("thought leaked into default read")
		}
	}
	approvals, _ := out["pendingApprovals"].([]any)
	if len(approvals) != 1 || approvals[0].(map[string]any)["id"] != ap.ID {
		t.Fatalf("approvals = %#v", approvals)
	}

	status, out = f.post(t, map[string]any{"tool": "read", "conversationId": f.target.ID, "includeThoughts": true})
	if status != http.StatusOK {
		t.Fatalf("read thoughts status=%d body=%v", status, out)
	}
	msgs, _ = out["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("thoughts messages = %#v", out["messages"])
	}
}

func TestConversationControlReadTruncates(t *testing.T) {
	f := newControlFixture(t)
	f.enable(t, "web")
	long := strings.Repeat("x", 7000)
	msg := store.Message{ConversationID: f.target.ID, Role: "assistant", Kind: "text", Text: long}
	if err := f.store.AppendMessage(context.Background(), &msg); err != nil {
		t.Fatal(err)
	}
	status, out := f.post(t, map[string]any{"tool": "read", "conversationId": f.target.ID})
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%v", status, out)
	}
	msgs, _ := out["messages"].([]any)
	first := msgs[0].(map[string]any)
	if first["truncated"] != true {
		t.Fatalf("truncated = %v", first["truncated"])
	}
	if len(first["text"].(string)) > controlMessageMax {
		t.Fatalf("text length = %d", len(first["text"].(string)))
	}
}

func TestConversationControlProjectScope(t *testing.T) {
	f := newControlFixture(t)
	f.enable(t, "web")
	ctx := context.Background()
	project, err := f.store.InsertProject(ctx, "Project", []string{t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	member, err := f.store.CreateProjectConversation(ctx, f.agent.ID, "Member", "", "web", "", "", project.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	outside, err := f.store.CreateConversation(ctx, f.agent.ID, "Outside", "", "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(map[string]any{
		"orchestration": map[string]any{"enabled": true, "scope": "project"},
	})
	if err := f.store.UpdateConversationMetadata(ctx, f.caller.ID, meta); err != nil {
		t.Fatal(err)
	}

	status, out := f.post(t, map[string]any{"tool": "read", "conversationId": member.ID})
	if status != http.StatusForbidden || out["code"] != "policy_denied" {
		t.Fatalf("empty project read status=%d body=%v", status, out)
	}
	status, out = f.post(t, map[string]any{"tool": "list"})
	if status != http.StatusOK {
		t.Fatalf("empty project list status=%d body=%v", status, out)
	}
	list, _ := out["conversations"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["id"] != f.caller.ID {
		t.Fatalf("empty project list = %#v", out["conversations"])
	}

	// Put a second caller in the same project; it may read members but not outsiders.
	projectCaller, err := f.store.CreateProjectConversation(ctx, f.agent.ID, "Supervisor P", "", "web", "", "", project.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	projectMeta, _ := json.Marshal(map[string]any{
		"orchestration": map[string]any{"enabled": true, "scope": "project"},
	})
	if err := f.store.UpdateConversationMetadata(ctx, projectCaller.ID, projectMeta); err != nil {
		t.Fatal(err)
	}
	trigger := store.Message{ConversationID: projectCaller.ID, Role: "user", Kind: "text", Text: "go"}
	if err := f.store.AppendMessage(ctx, &trigger); err != nil {
		t.Fatal(err)
	}
	callerTurn, err := f.store.CreateTurn(ctx, projectCaller.ID, trigger.ID, "web", "")
	if err != nil {
		t.Fatal(err)
	}
	if started, err := f.store.StartTurn(ctx, callerTurn.ID); err != nil || !started {
		t.Fatalf("start = %v err=%v", started, err)
	}
	f.caller = projectCaller
	f.token = f.grants.Token(projectCaller.ID)

	status, _ = f.post(t, map[string]any{"tool": "read", "conversationId": member.ID})
	if status != http.StatusOK {
		t.Fatalf("member read status=%d", status)
	}
	status, out = f.post(t, map[string]any{"tool": "read", "conversationId": outside.ID})
	if status != http.StatusForbidden {
		t.Fatalf("outside read status=%d body=%v", status, out)
	}
}

func TestConversationControlSendLifecycle(t *testing.T) {
	f := newLiveControlFixture(t)
	f.enable(t, "web")
	ctx := context.Background()
	status, out := f.post(t, map[string]any{
		"tool": "send", "conversationId": f.target.ID, "text": "do the work", "requestId": "req-1", "delivery": "steer",
	})
	if status != http.StatusOK {
		t.Fatalf("send status=%d body=%v", status, out)
	}
	if out["delivery"] != "queue" || !strings.Contains(out["note"].(string), "idle target") {
		t.Fatalf("idle steer mapping = %v", out)
	}
	msg, _ := out["message"].(map[string]any)
	turn, _ := out["turn"].(map[string]any)
	if msg["id"] == nil || turn["id"] == nil {
		t.Fatalf("send response = %v", out)
	}
	stored, err := f.store.GetMessage(ctx, msg["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	var meta struct {
		Orchestration *chat.Orchestrator `json:"orchestration"`
	}
	if err := json.Unmarshal(stored.Metadata, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Orchestration == nil || meta.Orchestration.ConversationID != f.caller.ID {
		t.Fatalf("message metadata = %s", stored.Metadata)
	}
	storedTurn, err := f.store.GetTurn(ctx, turn["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if storedTurn.OriginChannel != "agent" {
		t.Fatalf("turn origin = %q", storedTurn.OriginChannel)
	}

	status, dup := f.post(t, map[string]any{
		"tool": "send", "conversationId": f.target.ID, "text": "do the work", "requestId": "req-1",
	})
	if status != http.StatusOK || dup["duplicate"] != true {
		t.Fatalf("duplicate status=%d body=%v", status, dup)
	}
	dupMsg, _ := dup["message"].(map[string]any)
	if dupMsg["id"] != msg["id"] {
		t.Fatalf("duplicate id = %v want %v", dupMsg["id"], msg["id"])
	}

	status, waited := f.post(t, map[string]any{
		"tool": "wait", "conversationId": f.target.ID, "turnId": turn["id"], "timeoutSeconds": 3,
	})
	if status != http.StatusOK || waited["timedOut"] != false {
		t.Fatalf("wait status=%d body=%v", status, waited)
	}
}

func TestConversationControlSendRejects(t *testing.T) {
	f := newControlFixture(t)
	f.enable(t, "web")
	ctx := context.Background()
	if _, err := f.srv.chat.SetOrchestration(ctx, f.caller.ID, chat.OrchestrationPolicy{Enabled: true, ReadOnly: true, Scope: "all"}); err != nil {
		t.Fatal(err)
	}
	status, out := f.post(t, map[string]any{"tool": "send", "conversationId": f.target.ID, "text": "x"})
	if status != http.StatusForbidden || out["code"] != "policy_denied" {
		t.Fatalf("read-only status=%d body=%v", status, out)
	}
	if _, err := f.srv.chat.SetOrchestration(ctx, f.caller.ID, chat.OrchestrationPolicy{Enabled: true, Scope: "all"}); err != nil {
		t.Fatal(err)
	}
	status, out = f.post(t, map[string]any{"tool": "send", "conversationId": f.caller.ID, "text": "x"})
	if status != http.StatusForbidden || out["code"] != "target_self" {
		t.Fatalf("self status=%d body=%v", status, out)
	}
	archived := true
	if _, err := f.store.UpdateConversation(ctx, f.target.ID, store.ConversationUpdate{Archived: &archived}); err != nil {
		t.Fatal(err)
	}
	status, out = f.post(t, map[string]any{"tool": "send", "conversationId": f.target.ID, "text": "x"})
	if status != http.StatusConflict || out["code"] != "target_archived" {
		t.Fatalf("archived status=%d body=%v", status, out)
	}
	if _, err := f.store.UpdateConversation(ctx, f.target.ID, store.ConversationUpdate{Archived: boolPtr(false)}); err != nil {
		t.Fatal(err)
	}
	ap := store.Approval{ConversationID: f.target.ID, Title: "Approve", Kind: "execute"}
	if err := f.store.CreateApproval(ctx, &ap); err != nil {
		t.Fatal(err)
	}
	status, out = f.post(t, map[string]any{"tool": "send", "conversationId": f.target.ID, "text": "x"})
	if status != http.StatusConflict || out["code"] != "target_waiting_for_approval" {
		t.Fatalf("pending status=%d body=%v", status, out)
	}
	status, out = f.post(t, map[string]any{"tool": "send", "conversationId": f.target.ID, "text": "x", "delivery": "nope"})
	if status != http.StatusConflict || out["code"] != "target_waiting_for_approval" {
		t.Fatalf("pending-before-delivery status=%d body=%v", status, out)
	}
}

func TestConversationControlStopQueued(t *testing.T) {
	f := newControlFixture(t)
	f.enable(t, "web")
	ctx := context.Background()
	msg := store.Message{ConversationID: f.target.ID, Role: "user", Kind: "text", Text: "queued"}
	if err := f.store.AppendMessage(ctx, &msg); err != nil {
		t.Fatal(err)
	}
	turn, err := f.store.CreateTurn(ctx, f.target.ID, msg.ID, "web", "")
	if err != nil {
		t.Fatal(err)
	}
	status, out := f.post(t, map[string]any{"tool": "stop", "conversationId": f.target.ID, "turnId": turn.ID})
	if status != http.StatusOK || out["stopped"] != true {
		t.Fatalf("stop status=%d body=%v", status, out)
	}
	tv, _ := out["turn"].(map[string]any)
	if tv["status"] != "cancelled" {
		t.Fatalf("turn = %#v", out["turn"])
	}
	finished, err := f.store.CreateTurn(ctx, f.target.ID, msg.ID, "web", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.FinishTurn(ctx, finished.ID, "completed", "end_turn", ""); err != nil {
		t.Fatal(err)
	}
	status, out = f.post(t, map[string]any{"tool": "stop", "conversationId": f.target.ID, "turnId": finished.ID})
	if status != http.StatusOK || out["stopped"] != false || out["reason"] != "turn_finished" {
		t.Fatalf("finished stop status=%d body=%v", status, out)
	}
	status, out = f.post(t, map[string]any{"tool": "stop", "conversationId": f.caller.ID})
	if status != http.StatusForbidden || out["code"] != "target_self" {
		t.Fatalf("self stop status=%d body=%v", status, out)
	}
}

func TestConversationControlSpawn(t *testing.T) {
	f := newLiveControlFixture(t)
	f.enable(t, "web")
	ctx := context.Background()
	if _, err := f.srv.chat.SetOrchestration(ctx, f.caller.ID, chat.OrchestrationPolicy{Enabled: true, Scope: "all", Spawn: true}); err != nil {
		t.Fatal(err)
	}
	status, out := f.post(t, map[string]any{
		"tool": "spawn", "agent": "fake-control", "title": "Child", "prompt": "do the child work",
	})
	if status != http.StatusOK || out["promptQueued"] != true {
		t.Fatalf("spawn status=%d body=%v", status, out)
	}
	childView, _ := out["conversation"].(map[string]any)
	childID, _ := childView["id"].(string)
	if childID == "" {
		t.Fatalf("spawn conversation = %#v", out["conversation"])
	}
	child, err := f.store.GetConversation(ctx, childID)
	if err != nil {
		t.Fatal(err)
	}
	var meta struct {
		SpawnedBy string `json:"spawnedBy"`
	}
	if err := json.Unmarshal(child.Metadata, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.SpawnedBy != f.caller.ID {
		t.Fatalf("child metadata = %s", child.Metadata)
	}
	if child.OriginChannel != "agent" {
		t.Fatalf("child origin = %q", child.OriginChannel)
	}
	if n, err := f.store.CountSpawnedConversations(ctx, f.caller.ID); err != nil || n != 1 {
		t.Fatalf("spawn count = %d err=%v", n, err)
	}
	turn, _ := out["turn"].(map[string]any)
	if turn["id"] == nil {
		t.Fatalf("spawn turn = %#v", out["turn"])
	}
	storedTurn, err := f.store.GetTurn(ctx, turn["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if storedTurn.OriginChannel != "agent" {
		t.Fatalf("child turn origin = %q", storedTurn.OriginChannel)
	}

	if _, err := f.srv.chat.SetOrchestration(ctx, f.caller.ID, chat.OrchestrationPolicy{Enabled: true, Scope: "all"}); err != nil {
		t.Fatal(err)
	}
	status, out = f.post(t, map[string]any{"tool": "spawn", "agent": "fake-control"})
	if status != http.StatusForbidden || out["code"] != "spawn_not_allowed" {
		t.Fatalf("spawn policy status=%d body=%v", status, out)
	}
}

func boolPtr(v bool) *bool { return &v }

func TestConversationControlActivity(t *testing.T) {
	f := newControlFixture(t)
	f.enable(t, "web")
	if status, out := f.post(t, map[string]any{"tool": "list", "query": "Worker"}); status != http.StatusOK {
		t.Fatalf("list status=%d body=%v", status, out)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/conversation-control/activity?caller="+f.caller.ID, nil)
	rr := httptest.NewRecorder()
	f.srv.handleControlActivity(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("activity status = %d", rr.Code)
	}
	var body struct {
		Activity []controlAuditEntry `json:"activity"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Activity) != 1 || body.Activity[0].Tool != "list" || body.Activity[0].Caller != f.caller.ID {
		t.Fatalf("activity = %+v", body.Activity)
	}
}

func TestControlLimiter(t *testing.T) {
	var l controlLimiter
	for range 5 {
		if ok, _ := l.allow("caller", 20.0/60, 5); !ok {
			t.Fatal("limit refused inside burst")
		}
	}
	if ok, retry := l.allow("caller", 20.0/60, 5); ok || retry <= 0 {
		t.Fatalf("limit allowed over burst: ok=%v retry=%v", ok, retry)
	}
	if ok, _ := l.allow("other", 20.0/60, 5); !ok {
		t.Fatal("limiter leaked across keys")
	}
}

func TestConversationControlSpawnLimits(t *testing.T) {
	f := newControlFixture(t)
	f.enable(t, "web")
	ctx := context.Background()
	outside, err := f.store.InsertProject(ctx, "Outside", []string{t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.srv.chat.SetOrchestration(ctx, f.caller.ID, chat.OrchestrationPolicy{Enabled: true, Scope: "project", Spawn: true}); err != nil {
		t.Fatal(err)
	}
	status, out := f.post(t, map[string]any{"tool": "spawn", "agent": f.agent.Slug, "projectId": outside.ID})
	if status != http.StatusForbidden || out["code"] != "policy_denied" {
		t.Fatalf("spawn project status=%d body=%v", status, out)
	}
	if _, err := f.srv.chat.SetOrchestration(ctx, f.caller.ID, chat.OrchestrationPolicy{Enabled: true, Scope: "all", Spawn: true}); err != nil {
		t.Fatal(err)
	}
	for range 10 {
		child, err := f.store.CreateConversation(ctx, f.agent.ID, "child", "", "web", "", "")
		if err != nil {
			t.Fatal(err)
		}
		meta, _ := json.Marshal(map[string]any{"spawnedBy": f.caller.ID})
		if err := f.store.UpdateConversationMetadata(ctx, child.ID, meta); err != nil {
			t.Fatal(err)
		}
	}
	status, out = f.post(t, map[string]any{"tool": "spawn", "agent": f.agent.Slug})
	if status != http.StatusConflict || out["code"] != "spawn_limit" {
		t.Fatalf("spawn limit status=%d body=%v", status, out)
	}
}

func TestConversationControlReport(t *testing.T) {
	f := newControlFixture(t)
	f.enable(t, "web")
	status, out := f.post(t, map[string]any{"tool": "report", "text": "progress"})
	if status != http.StatusForbidden || out["code"] != "pings_not_allowed" {
		t.Fatalf("ping policy status=%d body=%v", status, out)
	}
	if _, err := f.srv.chat.SetOrchestration(context.Background(), f.caller.ID, chat.OrchestrationPolicy{
		Enabled: true, Scope: "all", ProgressPings: true,
	}); err != nil {
		t.Fatal(err)
	}
	status, out = f.post(t, map[string]any{"tool": "report", "text": "progress"})
	if status != http.StatusConflict || out["code"] != "no_active_turn" {
		t.Fatalf("ping no active status=%d body=%v", status, out)
	}
	status, out = f.post(t, map[string]any{"tool": "report", "text": "  "})
	if status != http.StatusBadRequest || out["code"] != "invalid_request" {
		t.Fatalf("ping empty status=%d body=%v", status, out)
	}
}

func TestConversationControlRevokedToken(t *testing.T) {
	f := newControlFixture(t)
	f.enable(t, "web")
	f.grants.Revoke(f.caller.ID)
	status, out := f.post(t, map[string]any{"tool": "list"})
	if status != http.StatusUnauthorized {
		t.Fatalf("revoked status=%d body=%v", status, out)
	}
}

func TestConversationControlWaitDeletedTarget(t *testing.T) {
	f := newControlFixture(t)
	f.enable(t, "web")
	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = f.store.DeleteConversation(context.Background(), f.target.ID)
	}()
	status, out := f.post(t, map[string]any{"tool": "wait", "conversationId": f.target.ID, "timeoutSeconds": 2})
	if status != http.StatusNotFound {
		t.Fatalf("deleted wait status=%d body=%v", status, out)
	}
}

func TestConversationControlRejectsOversizedBody(t *testing.T) {
	f := newControlFixture(t)
	req := httptest.NewRequest(http.MethodPost, "/api/conversation-control", strings.NewReader(strings.Repeat("x", (1<<20)+100)))
	req.Header.Set("Authorization", "Bearer "+f.token)
	rr := httptest.NewRecorder()
	f.srv.handleConversationControl(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("oversized status = %d", rr.Code)
	}
}

func TestConversationControlWatchingCount(t *testing.T) {
	f := newControlFixture(t)
	f.enable(t, "web")
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = f.post(t, map[string]any{"tool": "wait", "conversationId": f.target.ID, "timeoutSeconds": 1})
	}()
	time.Sleep(150 * time.Millisecond)
	status, out := f.post(t, map[string]any{"tool": "read", "conversationId": f.target.ID})
	if status != http.StatusOK {
		t.Fatalf("read status=%d body=%v", status, out)
	}
	conv, _ := out["conversation"].(map[string]any)
	if conv["watchers"] != float64(1) {
		t.Fatalf("watchers = %v", conv["watchers"])
	}
	<-done
	status, out = f.post(t, map[string]any{"tool": "read", "conversationId": f.target.ID})
	if status != http.StatusOK {
		t.Fatalf("read after wait status=%d body=%v", status, out)
	}
	conv, _ = out["conversation"].(map[string]any)
	if conv["watchers"] != nil && conv["watchers"] != float64(0) {
		t.Fatalf("watchers after wait = %v", conv["watchers"])
	}
}

func TestConversationControlProjects(t *testing.T) {
	f := newControlFixture(t)
	f.enable(t, "web")
	ctx := context.Background()
	projectA, err := f.store.InsertProject(ctx, "Project A", []string{t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	projectB, err := f.store.InsertProject(ctx, "Project B", []string{t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	memberA, err := f.store.CreateProjectConversation(ctx, f.agent.ID, "A worker", "", "web", "", "", projectA.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	memberB, err := f.store.CreateProjectConversation(ctx, f.agent.ID, "B worker", "", "web", "", "", projectB.ID, nil)
	if err != nil {
		t.Fatal(err)
	}

	status, out := f.post(t, map[string]any{"tool": "projects_list"})
	if status != http.StatusOK {
		t.Fatalf("projects status=%d body=%v", status, out)
	}
	projects, _ := out["projects"].([]any)
	if len(projects) != 2 {
		t.Fatalf("projects = %#v", out["projects"])
	}

	status, out = f.post(t, map[string]any{"tool": "list", "project": "Project A"})
	if status != http.StatusOK {
		t.Fatalf("project list status=%d body=%v", status, out)
	}
	convs, _ := out["conversations"].([]any)
	if len(convs) != 1 || convs[0].(map[string]any)["id"] != memberA.ID {
		t.Fatalf("project list = %#v", out["conversations"])
	}
	if convs[0].(map[string]any)["projectName"] != projectA.Name {
		t.Fatalf("projectName = %v", convs[0].(map[string]any)["projectName"])
	}

	status, out = f.post(t, map[string]any{"tool": "list", "project": "Project B"})
	if status != http.StatusOK {
		t.Fatalf("project B list status=%d body=%v", status, out)
	}
	convs, _ = out["conversations"].([]any)
	if len(convs) != 1 || convs[0].(map[string]any)["id"] != memberB.ID {
		t.Fatalf("project B list = %#v", out["conversations"])
	}

	status, out = f.post(t, map[string]any{"tool": "list", "project": "nope"})
	if status != http.StatusBadRequest || out["code"] != "invalid_project" {
		t.Fatalf("invalid project status=%d body=%v", status, out)
	}
	basename := filepath.Base(projectB.RootPaths[0])
	status, out = f.post(t, map[string]any{"tool": "list", "project": basename})
	if status != http.StatusOK {
		t.Fatalf("basename project status=%d body=%v", status, out)
	}
	convs, _ = out["conversations"].([]any)
	if len(convs) != 1 || convs[0].(map[string]any)["id"] != memberB.ID {
		t.Fatalf("basename project list = %#v", out["conversations"])
	}
	if _, err := f.store.InsertProject(ctx, "Dup", []string{t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.InsertProject(ctx, "Dup", []string{t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	status, out = f.post(t, map[string]any{"tool": "list", "project": "Dup"})
	if status != http.StatusBadRequest || out["code"] != "ambiguous_project" {
		t.Fatalf("ambiguous project status=%d body=%v", status, out)
	}

	if _, err := f.srv.chat.SetOrchestration(ctx, f.caller.ID, chat.OrchestrationPolicy{
		Enabled: true, Scope: "all", Spawn: true,
	}); err != nil {
		t.Fatal(err)
	}
	status, out = f.post(t, map[string]any{"tool": "spawn", "agent": f.agent.Slug, "project": "Project A"})
	if status != http.StatusOK {
		t.Fatalf("spawn project status=%d body=%v", status, out)
	}
	childView, _ := out["conversation"].(map[string]any)
	childID, _ := childView["id"].(string)
	child, err := f.store.GetConversation(ctx, childID)
	if err != nil {
		t.Fatal(err)
	}
	if child.ProjectID != projectA.ID {
		t.Fatalf("child project = %q want %q", child.ProjectID, projectA.ID)
	}

	if _, err := f.srv.chat.SetOrchestration(ctx, f.caller.ID, chat.OrchestrationPolicy{
		Enabled: true, Scope: "project", Spawn: true,
	}); err != nil {
		t.Fatal(err)
	}
	status, out = f.post(t, map[string]any{"tool": "list", "project": "Project A"})
	if status != http.StatusForbidden || out["code"] != "policy_denied" {
		t.Fatalf("scope project status=%d body=%v", status, out)
	}
}

func TestConversationControlLastTurnSummary(t *testing.T) {
	f := newControlFixture(t)
	f.enable(t, "web")
	ctx := context.Background()
	user := store.Message{ConversationID: f.target.ID, Role: "user", Kind: "text", Text: "last prompt"}
	if err := f.store.AppendMessage(ctx, &user); err != nil {
		t.Fatal(err)
	}
	turn, err := f.store.CreateTurn(ctx, f.target.ID, user.ID, "web", "")
	if err != nil {
		t.Fatal(err)
	}
	answer := store.Message{ConversationID: f.target.ID, TurnID: turn.ID, Role: "assistant", Kind: "text", Text: "last answer"}
	if err := f.store.AppendMessage(ctx, &answer); err != nil {
		t.Fatal(err)
	}
	if err := f.store.FinishTurn(ctx, turn.ID, "completed", "end_turn", ""); err != nil {
		t.Fatal(err)
	}
	status, out := f.post(t, map[string]any{"tool": "list", "query": "Worker"})
	if status != http.StatusOK {
		t.Fatalf("list status=%d body=%v", status, out)
	}
	list, _ := out["conversations"].([]any)
	if len(list) != 1 {
		t.Fatalf("list = %#v", out["conversations"])
	}
	last, _ := list[0].(map[string]any)["lastTurn"].(map[string]any)
	if last["status"] != "completed" || last["prompt"] != "last prompt" || last["answer"] != "last answer" {
		t.Fatalf("lastTurn = %#v", last)
	}
	status, out = f.post(t, map[string]any{"tool": "read", "conversationId": f.target.ID})
	if status != http.StatusOK {
		t.Fatalf("read status=%d body=%v", status, out)
	}
	conv, _ := out["conversation"].(map[string]any)
	last, _ = conv["lastTurn"].(map[string]any)
	if last["status"] != "completed" || last["answer"] != "last answer" {
		t.Fatalf("read lastTurn = %#v", last)
	}
}

func TestConversationControlExport(t *testing.T) {
	f := newControlFixture(t)
	f.srv.cfg.DataDir = t.TempDir()
	f.enable(t, "web")
	ctx := context.Background()
	text := store.Message{ConversationID: f.target.ID, Role: "assistant", Kind: "text", Text: "export me"}
	if err := f.store.AppendMessage(ctx, &text); err != nil {
		t.Fatal(err)
	}
	thought := store.Message{ConversationID: f.target.ID, Role: "assistant", Kind: "thought", Text: "hidden thought"}
	if err := f.store.AppendMessage(ctx, &thought); err != nil {
		t.Fatal(err)
	}
	tool := store.Message{ConversationID: f.target.ID, Role: "assistant", Kind: "tool", Text: "run command", Content: json.RawMessage(`{"command":"pio run"}`)}
	if err := f.store.AppendMessage(ctx, &tool); err != nil {
		t.Fatal(err)
	}
	status, out := f.post(t, map[string]any{"tool": "export", "conversationId": f.target.ID})
	if status != http.StatusOK {
		t.Fatalf("export status=%d body=%v", status, out)
	}
	path, _ := out["path"].(string)
	if path == "" || strings.Contains(path, "nusamation.db") {
		t.Fatalf("path = %q", path)
	}
	if !strings.HasPrefix(path, filepath.Join(f.srv.cfg.DataDir, "exports")+string(os.PathSeparator)) {
		t.Fatalf("path outside export dir: %q", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %d: %s", len(lines), raw)
	}
	if strings.Contains(string(raw), "hidden thought") {
		t.Fatal("thought leaked into default export")
	}
	if !strings.Contains(string(raw), "export me") || !strings.Contains(string(raw), "run command") {
		t.Fatalf("export content = %s", raw)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows does not expose Unix permission bits through os.FileMode.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode().Perm())
	}

	status, out = f.post(t, map[string]any{"tool": "export", "conversationId": f.target.ID, "includeThoughts": true, "includeToolOutput": true, "limit": 3})
	if status != http.StatusOK {
		t.Fatalf("full export status=%d body=%v", status, out)
	}
	raw, err = os.ReadFile(out["path"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "hidden thought") || !strings.Contains(string(raw), "pio run") {
		t.Fatalf("full export content = %s", raw)
	}

	status, out = f.post(t, map[string]any{"tool": "export", "conversationId": f.target.ID, "limit": 1})
	if status != http.StatusOK || out["truncated"] != true || out["messages"] != float64(1) {
		t.Fatalf("limited export status=%d body=%v", status, out)
	}

	status, out = f.post(t, map[string]any{"tool": "export", "project": "nope"})
	if status != http.StatusBadRequest || out["code"] != "invalid_project" {
		t.Fatalf("invalid project status=%d body=%v", status, out)
	}
}

func TestConversationControlSearch(t *testing.T) {
	f := newControlFixture(t)
	f.enable(t, "web")
	ctx := context.Background()
	text := store.Message{ConversationID: f.target.ID, Role: "assistant", Kind: "text", Text: "deploy the backend"}
	if err := f.store.AppendMessage(ctx, &text); err != nil {
		t.Fatal(err)
	}
	tool := store.Message{ConversationID: f.target.ID, Role: "assistant", Kind: "tool", Text: "deploy tool output"}
	if err := f.store.AppendMessage(ctx, &tool); err != nil {
		t.Fatal(err)
	}
	thought := store.Message{ConversationID: f.target.ID, Role: "assistant", Kind: "thought", Text: "deploy thought"}
	if err := f.store.AppendMessage(ctx, &thought); err != nil {
		t.Fatal(err)
	}
	other, err := f.store.CreateConversation(ctx, f.agent.ID, "Other search", "", "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	otherMsg := store.Message{ConversationID: other.ID, Role: "user", Kind: "text", Text: "deploy somewhere else"}
	if err := f.store.AppendMessage(ctx, &otherMsg); err != nil {
		t.Fatal(err)
	}

	status, out := f.post(t, map[string]any{"tool": "search", "query": "deploy!"})
	if status != http.StatusOK {
		t.Fatalf("search status=%d body=%v", status, out)
	}
	hits, _ := out["hits"].([]any)
	if len(hits) != 2 {
		t.Fatalf("hits = %#v", out["hits"])
	}
	first := hits[0].(map[string]any)
	if first["conversationId"] == "" || first["seq"] == nil || !strings.Contains(first["snippet"].(string), "deploy") {
		t.Fatalf("hit = %#v", first)
	}

	status, out = f.post(t, map[string]any{"tool": "search", "query": "deploy", "kinds": []string{"tool"}})
	if status != http.StatusOK {
		t.Fatalf("tool search status=%d body=%v", status, out)
	}
	hits, _ = out["hits"].([]any)
	if len(hits) != 1 || hits[0].(map[string]any)["messageId"] != tool.ID {
		t.Fatalf("tool hits = %#v", out["hits"])
	}

	status, out = f.post(t, map[string]any{"tool": "search", "query": "deploy", "conversationId": f.target.ID})
	if status != http.StatusOK {
		t.Fatalf("conversation search status=%d body=%v", status, out)
	}
	hits, _ = out["hits"].([]any)
	if len(hits) != 1 || hits[0].(map[string]any)["messageId"] != text.ID {
		t.Fatalf("conversation hits = %#v", out["hits"])
	}

	status, out = f.post(t, map[string]any{"tool": "search", "query": "deploy", "limit": 1})
	if status != http.StatusOK || out["hasMore"] != true || out["nextOffset"] != float64(1) {
		t.Fatalf("limited search status=%d body=%v", status, out)
	}
	status, out = f.post(t, map[string]any{"tool": "search", "query": ""})
	if status != http.StatusBadRequest || out["code"] != "invalid_request" {
		t.Fatalf("empty query status=%d body=%v", status, out)
	}
	status, out = f.post(t, map[string]any{"tool": "search", "query": "deploy", "kinds": []string{"nope"}})
	if status != http.StatusBadRequest || out["code"] != "invalid_kind" {
		t.Fatalf("bad kind status=%d body=%v", status, out)
	}
	status, out = f.post(t, map[string]any{"tool": "search", "query": "deploy", "conversationId": f.caller.ID})
	if status != http.StatusOK || len(out["hits"].([]any)) != 0 {
		t.Fatalf("self search status=%d body=%v", status, out)
	}
}

func TestConversationControlWaitNewMessage(t *testing.T) {
	f := newControlFixture(t)
	f.enable(t, "web")
	go func() {
		time.Sleep(150 * time.Millisecond)
		msg := store.Message{ConversationID: f.target.ID, Role: "assistant", Kind: "text", Text: "progress"}
		_ = f.store.AppendMessage(context.Background(), &msg)
	}()
	start := time.Now()
	status, out := f.post(t, map[string]any{"tool": "wait", "conversationId": f.target.ID, "timeoutSeconds": 2})
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%v", status, out)
	}
	if out["timedOut"] != false || out["changed"] != true {
		t.Fatalf("wait = %v", out)
	}
	if time.Since(start) > 1500*time.Millisecond {
		t.Fatalf("wait did not return early: %s", time.Since(start))
	}
	msgs, _ := out["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["text"] != "progress" {
		t.Fatalf("messages = %#v", out["messages"])
	}
}

func TestConversationControlWaitTimeoutAndTurn(t *testing.T) {
	f := newControlFixture(t)
	f.enable(t, "web")
	ctx := context.Background()
	msg := store.Message{ConversationID: f.target.ID, Role: "user", Kind: "text", Text: "work"}
	if err := f.store.AppendMessage(ctx, &msg); err != nil {
		t.Fatal(err)
	}
	turn, err := f.store.CreateTurn(ctx, f.target.ID, msg.ID, "web", "")
	if err != nil {
		t.Fatal(err)
	}
	if started, err := f.store.StartTurn(ctx, turn.ID); err != nil || !started {
		t.Fatalf("start = %v err=%v", started, err)
	}
	go func() {
		time.Sleep(150 * time.Millisecond)
		_ = f.store.FinishTurn(context.Background(), turn.ID, "completed", "end_turn", "")
	}()
	status, out := f.post(t, map[string]any{"tool": "wait", "conversationId": f.target.ID, "turnId": turn.ID, "timeoutSeconds": 2})
	if status != http.StatusOK || out["timedOut"] != false {
		t.Fatalf("turn wait status=%d body=%v", status, out)
	}
	tv, _ := out["turn"].(map[string]any)
	if tv["status"] != "completed" {
		t.Fatalf("turn = %#v", out["turn"])
	}

	status, out = f.post(t, map[string]any{"tool": "wait", "conversationId": f.target.ID, "timeoutSeconds": 1})
	if status != http.StatusOK || out["timedOut"] != true {
		t.Fatalf("timeout status=%d body=%v", status, out)
	}
}

func TestConversationControlWritesAndUnknown(t *testing.T) {
	f := newControlFixture(t)
	f.enable(t, "web")
	status, out := f.post(t, map[string]any{"tool": "send", "conversationId": f.target.ID})
	if status != http.StatusBadRequest || out["code"] != "invalid_request" {
		t.Fatalf("empty send status=%d body=%v", status, out)
	}
	status, out = f.post(t, map[string]any{"tool": "stop", "conversationId": f.target.ID})
	if status != http.StatusOK || out["stopped"] != false || out["reason"] != "idle" {
		t.Fatalf("idle stop status=%d body=%v", status, out)
	}
	status, out = f.post(t, map[string]any{"tool": "nope"})
	if status != http.StatusBadRequest || out["code"] != "unknown_tool" {
		t.Fatalf("unknown status=%d body=%v", status, out)
	}
}

func TestConversationControlWaitLimit(t *testing.T) {
	f := newControlFixture(t)
	for range controlWaitPerCaller {
		if !f.srv.acquireControlWait(f.caller.ID) {
			t.Fatal("wait slot refused early")
		}
	}
	if f.srv.acquireControlWait(f.caller.ID) {
		t.Fatal("wait slot over limit")
	}
	f.srv.releaseControlWait(f.caller.ID)
	if !f.srv.acquireControlWait(f.caller.ID) {
		t.Fatal("wait slot not released")
	}
}

func TestControlReadAdvancesAcrossHiddenThoughts(t *testing.T) {
	f := newControlFixture(t)
	f.enable(t, "web")
	for _, kind := range []string{"text", "thought", "thought", "text"} {
		msg := store.Message{ConversationID: f.target.ID, Role: "assistant", Kind: kind, Text: "page"}
		if err := f.store.AppendMessage(context.Background(), &msg); err != nil {
			t.Fatal(err)
		}
	}
	status, out := f.post(t, map[string]any{"tool": "read", "conversationId": f.target.ID, "afterSeq": 1, "limit": 2})
	if status != http.StatusOK || out["nextAfterSeq"] != float64(3) {
		t.Fatalf("hidden page: status=%d out=%v", status, out)
	}
	status, out = f.post(t, map[string]any{"tool": "read", "conversationId": f.target.ID, "afterSeq": out["nextAfterSeq"], "limit": 2})
	if status != http.StatusOK || len(out["messages"].([]any)) != 1 {
		t.Fatalf("following page: status=%d out=%v", status, out)
	}
}
