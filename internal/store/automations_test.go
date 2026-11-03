package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestAutomationClaimsMissedRunOnceAndExpires(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	enabled := true
	ag, err := st.CreateAgent(ctx, AgentInput{Name: "Fake", Slug: "fake-auto", Command: "/bin/true", Cwd: t.TempDir(), Enabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a, err := st.SaveAutomation(ctx, Automation{Name: "Daily", Prompt: "hello", AgentID: ag.ID, Schedule: "cron", Cron: "0 9 * * *", Timezone: "Asia/Jakarta", SessionMode: "same", Enabled: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	if a.NextRunAt != time.Date(2026, 1, 1, 2, 0, 0, 0, time.UTC).UnixMilli() {
		t.Fatalf("next = %v", time.UnixMilli(a.NextRunAt))
	}
	later := now.Add(72 * time.Hour)
	run, ok, err := st.ClaimAutomation(ctx, a, later)
	if err != nil || !ok {
		t.Fatalf("claim %v %v", ok, err)
	}
	if _, ok, err := st.ClaimAutomation(ctx, a, later); err != nil || ok {
		t.Fatalf("duplicate claim %v %v", ok, err)
	}
	next, err := st.GetAutomation(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if next.NextRunAt <= later.UnixMilli() {
		t.Fatal("missed schedules were not coalesced")
	}
	runs, err := st.ListAutomationRuns(ctx, a.ID)
	if err != nil || len(runs) != 1 || runs[0].ID != run.ID {
		t.Fatalf("history %#v %v", runs, err)
	}
	next.ExpiresAt = later.Add(-time.Second).UnixMilli()
	next.NextRunAt = later.Add(-time.Minute).UnixMilli()
	if _, ok, err := st.ClaimAutomation(ctx, next, later); err != nil || ok {
		t.Fatalf("expired run %v %v", ok, err)
	}
}

func TestAutomationClaimRejectsStaleJobSnapshot(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	enabled := true
	ag, err := st.CreateAgent(ctx, AgentInput{Name: "Fake", Slug: "stale-agent", Command: "/bin/true", Cwd: t.TempDir(), Enabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	job, err := st.SaveAutomation(ctx, Automation{Name: "job", Prompt: "old", AgentID: ag.ID, Schedule: "once", RunAt: now.Add(-time.Minute).UnixMilli(), Timezone: "UTC", SessionMode: "same", Enabled: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	job.Prompt = "new"
	if _, err = st.SaveAutomation(ctx, job, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	job.Prompt = "old"
	if _, err := st.SaveAutomation(ctx, job, now.Add(2*time.Second)); err == nil {
		t.Fatal("stale update succeeded")
	}
	if _, ok, err := st.ClaimAutomation(ctx, job, now.Add(2*time.Second)); err != nil || ok {
		t.Fatalf("stale claim = %v, %v", ok, err)
	}
}
func TestAutomationValidation(t *testing.T) {
	now := time.Now()
	a := Automation{Name: "x", Prompt: "x", AgentID: "a", Schedule: "cron", Cron: "bad cron", SessionMode: "isolated", Timezone: "UTC"}
	if ValidateAutomation(&a, now) == nil {
		t.Fatal("invalid cron accepted")
	}
	a.Cron = "*/5 * * * *"
	if err := ValidateAutomation(&a, now); err != nil {
		t.Fatal(err)
	}
	a.ExpiresAt = now.Add(-time.Hour).UnixMilli()
	if err := ValidateAutomation(&a, now); err != nil {
		t.Fatalf("expired schedule should remain editable: %v", err)
	}
}

func TestAutomationCronUsesSelectedTimezone(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a := Automation{Name: "x", Prompt: "x", AgentID: "a", Schedule: "cron", Cron: "0 9 * * *", SessionMode: "isolated", Timezone: "America/New_York"}
	if err := ValidateAutomation(&a, now); err != nil {
		t.Fatal(err)
	}
	if got, want := time.UnixMilli(a.NextRunAt), time.Date(2026, 1, 1, 14, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("next = %v, want %v", got, want)
	}
}

func TestAutomationHistoryKeepsTurnOutcome(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	enabled := true
	ag, err := st.CreateAgent(ctx, AgentInput{Name: "Fake", Slug: "history-agent", Command: "/bin/true", Cwd: t.TempDir(), Enabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	job, err := st.SaveAutomation(ctx, Automation{Name: "once", Prompt: "hello", AgentID: ag.ID, Schedule: "once", RunAt: now.Add(-time.Minute).UnixMilli(), Timezone: "UTC", SessionMode: "same", Enabled: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	run, ok, err := st.ClaimAutomation(ctx, job, now)
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	conv, err := st.CreateConversation(ctx, ag.ID, "test", ag.Cwd, "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := st.CreateAutomationTurn(ctx, run.ID, conv.ID, "", "web", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.FinishTurn(ctx, turn.ID, "error", "error", "agent failed"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, `DELETE FROM conversations WHERE id=?`, conv.ID); err != nil {
		t.Fatal(err)
	}
	runs, err := st.ListAutomationRuns(ctx, job.ID)
	if err != nil || len(runs) != 1 || runs[0].Status != "error" || runs[0].Error != "agent failed" {
		t.Fatalf("history %#v, %v", runs, err)
	}
}
