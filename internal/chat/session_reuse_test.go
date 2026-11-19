package chat

import (
	"context"
	"strings"
	"testing"
)

// The session-assignment audit: every conversation must own its own ACP
// session id. Rooms start blank, the agent mints the id, and NusaMation only
// ever resumes the id the agent minted for that room — there is no path that
// invents or borrows another room's id.
func TestRoomsGetDistinctAgentSessions(t *testing.T) {
	e := newAfterToolsEnv(t)
	ctx := context.Background()

	first, err := e.st.GetConversation(ctx, e.conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.ACPSessionID != "" {
		t.Fatalf("fresh conversation already has an ACP session %q", first.ACPSessionID)
	}
	_, turnA := e.submit("plain prompt", "queue")
	e.waitStatus(turnA.ID, "completed")

	// A second conversation of the same agent gets its own session id.
	second, err := e.svc.CreateConversation(ctx, first.AgentID, "Room B", first.Cwd, "web", "")
	if err != nil {
		t.Fatal(err)
	}
	msgB, _, err := e.svc.Submit(ctx, SubmitInput{ConversationID: second.ID, Text: "other room", Channel: "web", Delivery: "queue"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		updated, err := e.st.GetConversation(ctx, second.ID)
		return err == nil && updated.ACPSessionID != ""
	}, "room B to be assigned an ACP session")

	a, err := e.st.GetConversation(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.st.GetConversation(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if a.ACPSessionID == "" || b.ACPSessionID == "" {
		t.Fatalf("missing session ids: %q %q", a.ACPSessionID, b.ACPSessionID)
	}
	if a.ACPSessionID == b.ACPSessionID {
		t.Fatalf("two rooms share one ACP session id %q", a.ACPSessionID)
	}

	// The mapping rows stay per room: one row each, pointing at its own id.
	rowsA, err := e.st.ListConversationACPSessions(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	rowsB, err := e.st.ListConversationACPSessions(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rowsA) != 1 || len(rowsB) != 1 || rowsA[0].SessionID != a.ACPSessionID || rowsB[0].SessionID != b.ACPSessionID {
		t.Fatalf("mapping rows: A=%v B=%v", rowsA, rowsB)
	}
	_ = msgB
}

// An agent that hands two conversations the same session id — a backend
// attaching its latest session instead of starting fresh — is refused for the
// second room instead of silently sharing one agent context and rerouting
// notifications to the last registrant.
func TestAgentSessionReuseRefused(t *testing.T) {
	t.Setenv("FAKE_ACP_REUSE_SESSION", "1")
	e := newAfterToolsEnv(t)
	ctx := context.Background()

	// Room A takes the shared id first and keeps working throughout.
	_, turnA := e.submit("plain prompt", "queue")
	e.waitStatus(turnA.ID, "completed")
	owner, err := e.st.GetConversation(ctx, e.conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if owner.ACPSessionID == "" {
		t.Fatal("room A has no session")
	}

	// Room B gets the same id from the agent: the ensure inside its turn must
	// refuse, and the refusal text lands on the turn for the operator.
	second, err := e.svc.CreateConversation(ctx, owner.AgentID, "Room B", owner.Cwd, "web", "")
	if err != nil {
		t.Fatal(err)
	}
	_, turnB, err := e.svc.Submit(ctx, SubmitInput{ConversationID: second.ID, Text: "other room", Channel: "web", Delivery: "queue"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		got, err := e.st.GetTurn(ctx, turnB.ID)
		return err == nil && got.Status == "error" && strings.Contains(got.Error, "already owns")
	}, "room B's turn to fail with the session-reuse refusal")
	failed, err := e.st.GetTurn(ctx, turnB.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The refusal names the owning room and the shared id in its text.
	if !strings.Contains(failed.Error, "already owns") || !strings.Contains(failed.Error, owner.ID) {
		t.Fatalf("turn error = %q, want a session-reuse refusal naming room %s", failed.Error, owner.ID)
	}

	// The refusal names the owning room and leaves both rows honest: the
	// owner keeps its id, the second room never adopted one.
	afterA, err := e.st.GetConversation(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterA.ACPSessionID != owner.ACPSessionID {
		t.Fatalf("owner session changed: %q -> %q", owner.ACPSessionID, afterA.ACPSessionID)
	}
	afterB, err := e.st.GetConversation(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterB.ACPSessionID != "" {
		t.Fatalf("refused room adopted session %q", afterB.ACPSessionID)
	}

	// Room A still completes a follow-up turn — its routing was untouched.
	_, turnC := e.submit("still mine", "queue")
	e.waitStatus(turnC.ID, "completed")
}
