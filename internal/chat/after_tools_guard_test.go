package chat

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jahrulnr/nusamation/internal/store"
)

func messageMetadata(t *testing.T, st *store.Store, id string) map[string]any {
	t.Helper()
	msg, err := st.GetMessage(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	if err := json.Unmarshal(msg.Metadata, &meta); err != nil {
		t.Fatal(err)
	}
	return meta
}

func TestGracefulStopWhileToolsPendingWaitsForRemoteResponse(t *testing.T) {
	e := newAfterToolsEnv(t)
	_, p1 := e.submit("after-tools-two-tools stop", "")
	waitFor(t, func() bool { return e.hasToolMessage(p1.ID) }, "tool call")
	_, p2 := e.submit("follow-up after stop", deliveryAfterTools)
	if err := e.svc.Cancel(context.Background(), e.conv.ID); err != nil {
		t.Fatal(err)
	}
	e.waitStatus(p1.ID, "cancelled")
	e.waitStatus(p2.ID, "completed")

	lines := e.trace()
	id1, _ := promptStart(t, lines, "after-tools-two-tools stop")
	_, start2 := promptStart(t, lines, "follow-up after stop")
	cancel := traceIndex(lines, "cancel")
	respond1 := traceIndex(lines, "respond "+id1)
	if countTrace(lines, "cancel") != 1 || cancel < 0 || !(cancel < respond1 && respond1 < start2) {
		t.Fatalf("stop bypassed remote barrier: cancel=%d respond1=%d start2=%d\n%v", cancel, respond1, start2, lines)
	}
}

func TestNoToolsCancelsAfterDispatchAndLateChunkStaysOnOldTurn(t *testing.T) {
	e := newAfterToolsEnv(t)
	_, p1 := e.submit("after-tools-no-tools one", "")
	e.waitStatus(p1.ID, "running")
	_, p2 := e.submit("follow-up no tools", deliveryAfterTools)
	e.waitStatus(p1.ID, "cancelled")
	e.waitStatus(p2.ID, "completed")

	lines := e.trace()
	id1, start1 := promptStart(t, lines, "after-tools-no-tools one")
	cancel := traceIndex(lines, "cancel")
	respond1 := traceIndex(lines, "respond "+id1)
	_, start2 := promptStart(t, lines, "follow-up no tools")
	if !(start1 < cancel && cancel < respond1 && respond1 < start2) {
		t.Fatalf("order: start1=%d cancel=%d respond1=%d start2=%d\n%v", start1, cancel, respond1, start2, lines)
	}
	msgs, err := e.st.ListMessages(context.Background(), e.conv.ID, 0, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	late := false
	for _, m := range msgs {
		if strings.Contains(m.Text, "late-chunk") {
			late = m.TurnID == p1.ID
		}
	}
	if !late {
		t.Fatal("late chunk not attributed to the cancelled turn")
	}
}

func TestPendingPermissionCancelledByGracefulStop(t *testing.T) {
	e := newAfterToolsEnvWithPolicy(t, "ask")
	_, p1 := e.submit("after-tools-permission one", "")
	var approval store.Approval
	waitFor(t, func() bool {
		list, err := e.st.ListApprovalsForConversation(context.Background(), e.conv.ID, 10)
		if err != nil || len(list) == 0 {
			return false
		}
		approval = list[0]
		return true
	}, "permission request")
	_, p2 := e.submit("follow-up permission", deliveryAfterTools)
	if err := e.svc.Cancel(context.Background(), e.conv.ID); err != nil {
		t.Fatal(err)
	}
	e.waitStatus(p1.ID, "cancelled")
	ap, err := e.st.GetApproval(context.Background(), approval.ID)
	if err != nil || ap.Status != "cancelled" {
		t.Fatalf("approval = %#v, %v", ap, err)
	}
	lines := e.trace()
	id1, _ := promptStart(t, lines, "after-tools-permission one")
	if respond := traceIndex(lines, "respond "+id1); respond < 0 || traceIndex(lines, "cancel") > respond {
		t.Fatalf("permission turn answered before cancel: %v", lines)
	}

	// The queued follow-up gets its own permission request; decline it so the
	// test does not wait on an unrelated approval.
	waitFor(t, func() bool {
		list, _ := e.st.ListApprovalsForConversation(context.Background(), e.conv.ID, 10)
		for _, a := range list {
			if a.TurnID == p2.ID && a.Status == "pending" {
				approval = a
				return true
			}
		}
		return false
	}, "follow-up permission")
	if _, err := e.svc.ResolveApproval(context.Background(), approval.ID, "reject-once"); err != nil {
		t.Fatal(err)
	}
	e.waitStatus(p2.ID, "completed")
}

func TestNaturalCompletionRacingCancelLeavesConversationIdle(t *testing.T) {
	e := newAfterToolsEnv(t)
	if err := e.st.SetConversationStatus(context.Background(), e.conv.ID, "idle"); err != nil {
		t.Fatal(err)
	}
	a := newActiveTurn(e.conv, store.Turn{ID: "turn-race"}, store.Agent{}, store.Message{}, context.Background(), func() {})
	a.afterTools = true
	a.endPrompt()
	if e.svc.requestGracefulCancel(a) {
		t.Fatal("cancel accepted after the prompt returned")
	}
	conv, err := e.st.GetConversation(context.Background(), e.conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if conv.Status != "idle" {
		t.Fatalf("conversation status = %q", conv.Status)
	}
}

func TestDeadlineDropsTargetSessionAndQueueRecovers(t *testing.T) {
	e := newAfterToolsEnv(t)
	setTimeout := func(d time.Duration) {
		e.svc.mu.Lock()
		e.svc.turnTimeout = d
		e.svc.mu.Unlock()
	}
	setTimeout(500 * time.Millisecond)
	_, p1 := e.submit("after-tools-ignore-cancel deadline", "")
	e.waitStatus(p1.ID, "error")
	setTimeout(defaultTurnTimeout)
	_, p2 := e.submit("follow-up deadline", "")
	e.waitStatus(p2.ID, "completed")

	lines := e.trace()
	if n := countTrace(lines, "session_new"); n != 2 {
		t.Fatalf("session_new = %d, want a fresh session after the deadline\n%v", n, lines)
	}
	if n := countTrace(lines, "cancel"); n != 0 {
		t.Fatalf("deadline path sent %d cancels", n)
	}
	conv, err := e.st.GetConversation(context.Background(), e.conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if conv.ACPSessionID == "" {
		t.Fatal("new session was not persisted for the next prompt")
	}
}

func TestOldFIFOHeadPreventsArmingNewAfterTools(t *testing.T) {
	e := newAfterToolsEnv(t)
	_, p1 := e.submit("after-tools-ignore-cancel head", "")
	waitFor(t, func() bool { return e.hasToolMessage(p1.ID) }, "tool call")
	_, p2 := e.submit("follow-up older queued", deliveryQueue)
	m3, p3 := e.submit("follow-up newer after tools", deliveryAfterTools)
	e.waitStatus(p1.ID, "completed")
	e.waitStatus(p2.ID, "completed")
	e.waitStatus(p3.ID, "completed")

	if _, ok := messageMetadata(t, e.st, m3.ID)["interruptTurnId"]; ok {
		t.Fatal("input behind an older queued head armed an interrupt")
	}
	if n := countTrace(e.trace(), "cancel"); n != 0 {
		t.Fatalf("cancel sent %d times", n)
	}
}

func TestFinishedTargetDoesNotAffectNextTurn(t *testing.T) {
	e := newAfterToolsEnv(t)
	_, p1 := e.submit("follow-up finished target", "")
	e.waitStatus(p1.ID, "completed")
	m2, p2 := e.submit("follow-up after finish", deliveryAfterTools)
	e.waitStatus(p2.ID, "completed")

	if _, ok := messageMetadata(t, e.st, m2.ID)["interruptTurnId"]; ok {
		t.Fatal("input armed against a finished turn")
	}
	if n := countTrace(e.trace(), "cancel"); n != 0 {
		t.Fatalf("cancel sent %d times", n)
	}
}

func TestConcurrentSubmitsDrainQueueWithNoOwnerLeft(t *testing.T) {
	e := newAfterToolsEnv(t)
	const n = 4
	turnIDs := make(chan string, n)
	for range n {
		go func() {
			_, turn, err := e.svc.Submit(context.Background(), SubmitInput{
				ConversationID: e.conv.ID, Text: "follow-up burst", Channel: "web",
			})
			if err != nil {
				t.Error(err)
			}
			turnIDs <- turn.ID
		}()
	}
	for range n {
		e.waitStatus(<-turnIDs, "completed")
	}
	waitFor(t, func() bool {
		e.svc.mu.Lock()
		defer e.svc.mu.Unlock()
		return len(e.svc.running) == 0 && len(e.svc.kick) == 0
	}, "queue owner to release")
}
