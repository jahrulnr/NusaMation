package main

import (
	"context"
	"testing"
	"time"

	"github.com/jahrulnr/nusamation/internal/store"
)

func TestAutomationCLIManagesJobs(t *testing.T) {
	dir := t.TempDir()
	st, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	_, err = st.CreateAgent(context.Background(), store.AgentInput{Name: "Fake", Slug: "cli-fake", Command: "/bin/true", Cwd: dir, Enabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	if err := automationCmd([]string{"add", "-name", "Test", "-prompt", "original", "-agent", "cli-fake", "-cron", "0 9 * * *", "-tz", "UTC", "-session", "same", "-data-dir", dir}); err != nil {
		t.Fatal(err)
	}
	st, err = openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := st.ListAutomations(context.Background())
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs %#v %v", jobs, err)
	}
	id := jobs[0].ID
	st.Close()
	if err := automationCmd([]string{"pause", id, "-data-dir", dir}); err != nil {
		t.Fatal(err)
	}
	if err := automationCmd([]string{"set", id, "-prompt", "updated", "-expires", time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339), "-data-dir", dir}); err != nil {
		t.Fatal(err)
	}
	st, err = openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	job, err := st.GetAutomation(context.Background(), id)
	st.Close()
	if err != nil || job.Enabled || job.Prompt != "updated" || job.ExpiresAt == 0 {
		t.Fatalf("job %#v %v", job, err)
	}
	if err := automationCmd([]string{"resume", id, "-data-dir", dir}); err != nil {
		t.Fatal(err)
	}
	if err := automationCmd([]string{"history", id, "-data-dir", dir}); err != nil {
		t.Fatal(err)
	}
	if err := automationCmd([]string{"remove", id, "-data-dir", dir}); err != nil {
		t.Fatal(err)
	}
}
