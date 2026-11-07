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
	if started, err := st.StartTurn(ctx, queuedA.ID); err != nil || !started {
		t.Fatalf("StartTurn = %v, %v", started, err)
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

func TestCancelQueuedTurnByID(t *testing.T) {
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
	head, err := st.CreateTurn(ctx, a.ID, "msg-a1", "web", "")
	if err != nil {
		t.Fatal(err)
	}
	queued, err := st.CreateTurn(ctx, a.ID, "msg-a2", "web", "")
	if err != nil {
		t.Fatal(err)
	}
	if started, err := st.StartTurn(ctx, head.ID); err != nil || !started {
		t.Fatalf("StartTurn = %v, %v", started, err)
	}

	// Cancelling a specific queued turn leaves the running head alone.
	got, err := st.CancelQueuedTurnByID(ctx, a.ID, queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "cancelled" || got.StopReason != "cancelled" || got.EndedAt == 0 {
		t.Fatalf("cancelled turn = %#v", got)
	}
	if headNow, err := st.GetTurn(ctx, head.ID); err != nil || headNow.Status != "running" {
		t.Fatalf("running head changed = %#v, %v", headNow, err)
	}
	if rest, err := st.ListQueuedTurnsForConversation(ctx, a.ID); err != nil || len(rest) != 0 {
		t.Fatalf("queue after cancel = %#v, %v", rest, err)
	}

	// A finished turn and a foreign/missing turn are rejected distinctly.
	if _, err := st.CancelQueuedTurnByID(ctx, a.ID, head.ID); err != ErrNotQueued {
		t.Fatalf("cancel running turn err = %v, want ErrNotQueued", err)
	}
	if _, err := st.CancelQueuedTurnByID(ctx, a.ID, "turn-missing"); err != ErrNotFound {
		t.Fatalf("cancel missing turn err = %v, want ErrNotFound", err)
	}
	other, err := st.CreateTurn(ctx, b.ID, "msg-b1", "web", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CancelQueuedTurnByID(ctx, a.ID, other.ID); err != ErrNotFound {
		t.Fatalf("cancel foreign turn err = %v, want ErrNotFound", err)
	}
	if still, err := st.GetTurn(ctx, other.ID); err != nil || still.Status != "queued" {
		t.Fatalf("foreign turn changed = %#v, %v", still, err)
	}
}

func TestStartTurnDoesNotResurrectCancelledTurn(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	ag := testAgent(t, st)
	conv, err := st.CreateConversation(ctx, ag.ID, "A", ag.Cwd, "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := st.CreateTurn(ctx, conv.ID, "msg-1", "web", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CancelQueuedTurn(ctx, conv.ID); err != nil {
		t.Fatal(err)
	}
	started, err := st.StartTurn(ctx, turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if started {
		t.Fatal("cancelled turn was resurrected")
	}
	got, err := st.GetTurn(ctx, turn.ID)
	if err != nil || got.Status != "cancelled" {
		t.Fatalf("turn = %#v, %v", got, err)
	}
}
