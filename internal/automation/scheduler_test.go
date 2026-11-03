package automation

import (
	"context"
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jahrulnr/nusamation/internal/agent"
	"github.com/jahrulnr/nusamation/internal/chat"
	"github.com/jahrulnr/nusamation/internal/events"
	"github.com/jahrulnr/nusamation/internal/security"
	"github.com/jahrulnr/nusamation/internal/store"
)

func TestSchedulerSessionOwnership(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	repo, _ := filepath.Abs("../..")
	bin := filepath.Join(dir, "fake-acp")
	build := exec.Command("go", "build", "-o", bin, "./cmd/fake-acp-agent")
	build.Dir = repo
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sealer, err := security.LoadOrCreateSealer(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	st.SetSealer(sealer)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := agent.NewManager(st, logger)
	defer mgr.StopAll()
	svc := chat.New(chat.Options{Store: st, Agents: mgr, Events: events.Nop{}, Log: logger, Workspace: dir})
	mgr.SetSink(svc)
	enabled := true
	ag, err := st.CreateAgent(ctx, store.AgentInput{Name: "Fake", Slug: "fake-scheduler", Command: bin, Cwd: dir, Enabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Minute)
	scheduler := Scheduler{Store: st, Chat: svc, Log: logger}
	for _, mode := range []string{"same", "isolated"} {
		job, err := st.SaveAutomation(ctx, store.Automation{Name: mode, Prompt: "hello", AgentID: ag.ID, Schedule: "cron", Cron: "* * * * *", Timezone: "UTC", SessionMode: mode, Enabled: true}, now)
		if err != nil {
			t.Fatal(err)
		}
		scheduler.Tick(ctx, now.Add(time.Minute))
		scheduler.Tick(ctx, now.Add(2*time.Minute))
		runs, err := st.ListAutomationRuns(ctx, job.ID)
		if err != nil || len(runs) != 2 {
			t.Fatalf("%s runs=%#v err=%v", mode, runs, err)
		}
		if runs[0].TurnID == "" || runs[1].TurnID == "" {
			t.Fatalf("missing turn %#v", runs)
		}
		if (runs[0].ConversationID == runs[1].ConversationID) != (mode == "same") {
			t.Fatalf("%s conversations %#v", mode, runs)
		}
		if err := st.SetAutomationEnabled(ctx, job.ID, false); err != nil {
			t.Fatal(err)
		}
	}
}
