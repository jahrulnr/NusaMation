package chat

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/jahrulnr/nusamation/internal/events"
	"github.com/jahrulnr/nusamation/internal/store"
)

func TestQueuedAutomationDoesNotStartAfterExpiry(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	enabled := true
	ag, err := st.CreateAgent(ctx, store.AgentInput{Name: "Fake", Slug: "expiry-agent", Command: "/bin/true", Cwd: dir, Enabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	job, err := st.SaveAutomation(ctx, store.Automation{Name: "job", Prompt: "hello", AgentID: ag.ID, Schedule: "once", RunAt: now.Add(-time.Second).UnixMilli(), ExpiresAt: now.Add(time.Minute).UnixMilli(), Timezone: "UTC", SessionMode: "same", Enabled: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	run, ok, err := st.ClaimAutomation(ctx, job, now)
	if err != nil || !ok {
		t.Fatalf("claim %v %v", ok, err)
	}
	conv, err := st.CreateConversation(ctx, ag.ID, "job", dir, "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := st.CreateAutomationTurn(ctx, run.ID, conv.ID, "", "web", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, `UPDATE automations SET expires_at=? WHERE id=?`, now.Add(-time.Second).UnixMilli(), job.ID); err != nil {
		t.Fatal(err)
	}
	svc := New(Options{Store: st, Events: events.Nop{}, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Workspace: dir})
	svc.runQueue(conv.ID)
	got, err := st.GetTurn(ctx, turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "cancelled" || got.StopReason != "expired" {
		t.Fatalf("turn = %#v", got)
	}
	runs, err := st.ListAutomationRuns(ctx, job.ID)
	if err != nil || len(runs) != 1 || runs[0].Status != "cancelled" {
		t.Fatalf("runs = %#v, %v", runs, err)
	}
}
