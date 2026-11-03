package store

import (
	"context"
	"testing"
)

func TestListQueuedTurnsForConversationOnlyReturnsRequestedQueue(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	ag := testAgent(t, st)
	a, err := st.CreateConversation(ctx, ag.ID, "A", ag.Cwd, "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateConversation(ctx, ag.ID, "B", ag.Cwd, "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	queuedA, err := st.CreateTurn(ctx, a.ID, "msg-a1", "web", "")
	if err != nil {
		t.Fatal(err)
	}
	queuedA2, err := st.CreateTurn(ctx, a.ID, "msg-a2", "web", "")
	if err != nil {
		t.Fatal(err)
	}
	queuedB, err := st.CreateTurn(ctx, b.ID, "msg-b1", "web", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.StartTurn(ctx, queuedA.ID); err != nil {
		t.Fatal(err)
	}

	got, err := st.ListQueuedTurnsForConversation(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != queuedA2.ID {
		t.Fatalf("queued turns for A = %#v, want only %s", got, queuedA2.ID)
	}
	for _, turn := range got {
		if turn.ConversationID != a.ID {
			t.Fatalf("turn %s belongs to %s", turn.ID, turn.ConversationID)
		}
	}

	all, err := st.ListQueuedTurns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("global queue = %d turns, want 2 (A2 and B1)", len(all))
	}
	_ = queuedB
}
