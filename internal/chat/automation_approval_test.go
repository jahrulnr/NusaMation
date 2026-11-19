package chat

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/jahrulnr/nusamation/internal/channels"
	"github.com/jahrulnr/nusamation/internal/events"
	"github.com/jahrulnr/nusamation/internal/store"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// An automation turn's result chat must never become a remote control: its
// approvals are surfaced in the web inbox only, even when the conversation
// has linked chats that the orchestrator fallback would otherwise use.
func TestAutomationApprovalStaysOnWeb(t *testing.T) {
	logger := testLogger()
	manager := channels.NewManager(events.Nop{}, logger)
	manager.Register(&recChannel{})
	svc := &Service{channels: manager, log: logger}
	a := &activeTurn{
		turn: store.Turn{
			ID: "t1", OriginChannel: "whatsapp",
			OriginRef: `{"externalId":"62@s.whatsapp.net","automation":true}`,
		},
		conv: store.Conversation{Links: []store.ChannelLink{{Channel: "wa", ExternalID: "62@s.whatsapp.net"}}},
	}
	if _, _, ok := svc.approvalChannel(a); ok {
		t.Fatal("automation approval was routed to a channel")
	}
	// A normal turn with the same origin still resolves its channel.
	a.turn.OriginRef = `{"externalId":"62@s.whatsapp.net"}`
	if _, _, ok := svc.approvalChannel(a); !ok {
		t.Fatal("non-automation approval lost its channel")
	}
}

// Poll votes on an automation turn's questions are denied for everyone; a
// legacy turn without a recorded sender keeps the permissive fallback.
func TestAutomationPollVoteDenied(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	enabled := true
	ag, err := st.CreateAgent(ctx, store.AgentInput{
		Name: "Fake", Slug: "auto-approval", Command: "true", Cwd: t.TempDir(), Enabled: &enabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	conv, err := st.CreateConversation(ctx, ag.ID, "t", t.TempDir(), "whatsapp", "", "")
	if err != nil {
		t.Fatal(err)
	}
	userMsg := &store.Message{ConversationID: conv.ID, Role: "user", Text: "go"}
	if err := st.AppendMessage(ctx, userMsg); err != nil {
		t.Fatal(err)
	}
	autoTurn, err := st.CreateTurn(ctx, conv.ID, userMsg.ID, "whatsapp", `{"externalId":"62@s.whatsapp.net","automation":true}`)
	if err != nil {
		t.Fatal(err)
	}
	legacyTurn, err := st.CreateTurn(ctx, conv.ID, userMsg.ID, "whatsapp", `{"externalId":"62@s.whatsapp.net"}`)
	if err != nil {
		t.Fatal(err)
	}
	ownerTurn, err := st.CreateTurn(ctx, conv.ID, userMsg.ID, "whatsapp", `{"senderId":"owner"}`)
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{store: st}
	if svc.pollVoterAllowed(ctx, autoTurn.ID, "62@s.whatsapp.net") {
		t.Fatal("automation turn accepted a channel vote")
	}
	if !svc.pollVoterAllowed(ctx, legacyTurn.ID, "62@s.whatsapp.net") {
		t.Fatal("legacy turn without sender lost the permissive fallback")
	}
	if svc.pollVoterAllowed(ctx, ownerTurn.ID, "intruder") {
		t.Fatal("non-owner vote accepted on an owned turn")
	}
	if !svc.pollVoterAllowed(ctx, ownerTurn.ID, "owner") {
		t.Fatal("owner vote rejected")
	}
}

// A slash /approve on the result chat must not resolve an automation turn.
// The turn records no sender, and the poll guard already denies every vote;
// the command path still treats that missing sender as a legacy turn and
// approves.
func TestAutomationCommandCannotResolve(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	enabled := true
	ag, err := st.CreateAgent(ctx, store.AgentInput{
		Name: "Fake", Slug: "auto-command", Command: "true", Cwd: t.TempDir(), Enabled: &enabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	conv, err := st.CreateConversation(ctx, ag.ID, "t", t.TempDir(), "whatsapp", "", "")
	if err != nil {
		t.Fatal(err)
	}
	userMsg := &store.Message{ConversationID: conv.ID, Role: "user", Text: "go"}
	if err := st.AppendMessage(ctx, userMsg); err != nil {
		t.Fatal(err)
	}
	humanTurn, err := st.CreateTurn(ctx, conv.ID, userMsg.ID, "whatsapp", `{"senderId":"owner"}`)
	if err != nil {
		t.Fatal(err)
	}
	autoTurn, err := st.CreateTurn(ctx, conv.ID, userMsg.ID, "whatsapp", `{"externalId":"62@s.whatsapp.net","automation":true}`)
	if err != nil {
		t.Fatal(err)
	}
	human := newPendingApproval(t, st, conv.ID, humanTurn.ID)
	auto := newPendingApproval(t, st, conv.ID, autoTurn.ID)
	svc := &Service{store: st, log: testLogger()}
	owner := channels.Inbound{Channel: "whatsapp", ExternalID: "62@s.whatsapp.net", SenderID: "owner"}
	if err := svc.commandApproval(ctx, owner, conv, true, "approve", []string{human.ID}); err != nil {
		t.Fatal(err)
	}
	if got, err := st.GetApproval(ctx, human.ID); err != nil || got.Status != "approved" {
		t.Fatalf("owner approval = %#v, %v", got, err)
	}
	anyone := channels.Inbound{Channel: "whatsapp", ExternalID: "62@s.whatsapp.net", SenderID: "anyone"}
	if err := svc.commandApproval(ctx, anyone, conv, true, "approve", []string{auto.ID}); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetApproval(ctx, auto.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "pending" {
		t.Fatalf("automation approval status = %s, want pending", got.Status)
	}
}

func newPendingApproval(t *testing.T, st *store.Store, conversationID, turnID string) store.Approval {
	t.Helper()
	ap := &store.Approval{
		ConversationID: conversationID, TurnID: turnID, Title: "run a command", Kind: "execute", Status: "pending",
		Options: []store.PermissionOption{{OptionID: "allow", Name: "Allow once", Kind: "allow_once"}},
	}
	if err := st.CreateApproval(context.Background(), ap); err != nil {
		t.Fatal(err)
	}
	return *ap
}

// Agent questions (elicitations) from an automation run stay on the web too —
// the result chat receives the run's answer, not a poll it cannot answer.
func TestAutomationElicitationStaysOnWeb(t *testing.T) {
	logger := testLogger()
	manager := channels.NewManager(events.Nop{}, logger)
	rec := &notifyRecordingChannel{name: "whatsapp"}
	manager.Register(rec)
	svc := testPollService()
	svc.channels = manager
	a := &activeTurn{
		turn: store.Turn{
			ID: "t1", OriginChannel: "whatsapp",
			OriginRef: `{"externalId":"62@s.whatsapp.net","automation":true}`,
		},
		conv: store.Conversation{Routing: store.Routing{NotifyApprovals: true}},
	}
	svc.notifyElicitation(a, Elicitation{ID: "el1", ConversationID: "c1", Request: json.RawMessage(
		`{"mode":"form","message":"pick one","requestedSchema":{"properties":{"s":{"type":"string","enum":["a","b"]}}}}`)})
	time.Sleep(50 * time.Millisecond)
	if notes := rec.replied(); len(notes) != 0 {
		t.Fatalf("automation elicitation leaked to the result chat: %#v", notes)
	}
	if rec.question != "" || len(rec.options) != 0 {
		t.Fatalf("automation elicitation poll sent: %q %#v", rec.question, rec.options)
	}

	// A normal turn with the same origin still gets its poll.
	a.turn.OriginRef = `{"externalId":"62@s.whatsapp.net"}`
	// Production registers the elicitation before notifying; without it the
	// dead-waiter cleanup would drop the registration.
	svc.elicitations["el2"] = &elicitationWaiter{
		request: Elicitation{ID: "el2", ConversationID: "c1"},
		result:  make(chan ElicitationResponse, 1),
	}
	svc.notifyElicitation(a, Elicitation{ID: "el2", ConversationID: "c1", Request: json.RawMessage(
		`{"mode":"form","message":"pick one","requestedSchema":{"properties":{"s":{"type":"string","enum":["a","b"]}}}}`)})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		svc.mu.Lock()
		w := svc.pollWaiters[pollWaiterKey("whatsapp", "POLLMSG1")]
		svc.mu.Unlock()
		if w != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	svc.mu.Lock()
	w := svc.pollWaiters[pollWaiterKey("whatsapp", "POLLMSG1")]
	svc.mu.Unlock()
	if w == nil || w.elicitationID != "el2" {
		t.Fatal("non-automation elicitation lost its poll")
	}
}
