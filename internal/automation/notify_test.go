package automation

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jahrulnr/nusamation/internal/agent"
	"github.com/jahrulnr/nusamation/internal/channels"
	"github.com/jahrulnr/nusamation/internal/chat"
	"github.com/jahrulnr/nusamation/internal/events"
	"github.com/jahrulnr/nusamation/internal/security"
	"github.com/jahrulnr/nusamation/internal/store"
	"github.com/jahrulnr/nusamation/internal/testutil"
)

// recordingChannel captures Begin/Update/Notify so the test can assert that
// the run's final answer reaches the notify target.
type recordingChannel struct {
	mu     sync.Mutex
	begins []string
	finals []string
}

func (c *recordingChannel) Name() string  { return "wa" }
func (c *recordingChannel) Label() string { return "WA" }
func (c *recordingChannel) Start(context.Context) error {
	return nil
}
func (c *recordingChannel) Stop() error { return nil }
func (c *recordingChannel) Status() channels.Status {
	return channels.Status{Name: "wa", State: "connected", UpdatedAt: time.Now().UnixMilli()}
}
func (c *recordingChannel) Begin(_ context.Context, _ channels.Target, text string) (channels.Delivery, error) {
	c.mu.Lock()
	c.begins = append(c.begins, text)
	d := &recordingDelivery{ch: c}
	c.mu.Unlock()
	return d, nil
}
func (c *recordingChannel) Notify(_ context.Context, _ channels.Target, text string) error {
	c.mu.Lock()
	c.finals = append(c.finals, text)
	c.mu.Unlock()
	return nil
}
func (c *recordingChannel) snapshots() (begins, finals []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.begins...), append([]string(nil), c.finals...)
}

type recordingDelivery struct{ ch *recordingChannel }

func (d *recordingDelivery) ID() string { return "rec" }
func (d *recordingDelivery) Update(_ context.Context, text string, final bool) error {
	if !final {
		return nil
	}
	d.ch.mu.Lock()
	d.ch.finals = append(d.ch.finals, text)
	d.ch.mu.Unlock()
	return nil
}

// The notify target is a delivery target, not a routing link: a scheduled run
// must never move a chat away from the conversation it already belongs to,
// and the run's final answer must reach the configured chat directly.
func TestNotifyTargetDeliversWithoutStealingLinks(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	bin := testutil.BuildFakeACP(t)
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sealer, err := security.LoadOrCreateSealer(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	st.SetSealer(sealer)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	agentMgr := agent.NewManager(st, logger)
	t.Cleanup(agentMgr.StopAll)

	rec := &recordingChannel{}
	chanMgr := channels.NewManager(events.Nop{}, logger)
	chanMgr.Register(rec)
	svc := newSchedulerService(st, agentMgr, chanMgr, logger, dir)

	enabled := true
	ag, err := st.CreateAgent(ctx, store.AgentInput{
		Name: "Fake", Slug: "notify-agent", Command: bin, Cwd: dir, Enabled: &enabled,
		ApprovalPolicy: "auto_all",
	})
	if err != nil {
		t.Fatal(err)
	}
	// A daily conversation already owns the notify chat — the run must leave
	// it alone.
	daily, err := st.CreateConversation(ctx, ag.ID, "Daily", dir, "whatsapp", `{"externalId":"62@s.whatsapp.net"}`, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.MoveChannelLink(ctx, store.ChannelLink{
		ConversationID: daily.ID, Channel: "whatsapp", ExternalID: "62@s.whatsapp.net",
		DisplayName: "My chat", IsGroup: false,
	}); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	job, err := st.SaveAutomation(ctx, store.Automation{
		Name: "Report", Prompt: "hello", AgentID: ag.ID, Schedule: "cron", Cron: "* * * * *",
		Timezone: "UTC", SessionMode: "isolated", Enabled: true,
		NotifyChannel: "wa", NotifyTarget: "62@s.whatsapp.net",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	scheduler := &Scheduler{Store: st, Chat: svc, Log: logger}
	scheduler.Tick(ctx, now.Add(time.Minute))
	waitFor(t, func() bool { return len(jobRuns(t, st, job.ID)) == 1 }, "run recorded")

	// The final answer reached the notify chat.
	waitFor(t, func() bool {
		_, got := rec.snapshots()
		return len(got) > 0
	}, "final answer delivered")
	_, finals := rec.snapshots()
	if len(finals) == 0 {
		t.Fatal("no final answer delivered")
	}

	// The daily conversation keeps its link untouched; the run conversation
	// has none.
	got, err := st.GetConversation(ctx, daily.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Links) != 1 || got.Links[0].ExternalID != "62@s.whatsapp.net" || got.Links[0].DisplayName != "My chat" {
		t.Fatalf("daily conversation links were stolen: %#v", got.Links)
	}
	runConvID := jobRuns(t, st, job.ID)[0].ConversationID
	runConv, err := st.GetConversation(ctx, runConvID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runConv.Links) != 0 {
		t.Fatalf("run conversation gained links: %#v", runConv.Links)
	}
}

func newSchedulerService(st *store.Store, mgr *agent.Manager, chanMgr *channels.Manager, logger *slog.Logger, dir string) *chat.Service {
	return chat.New(chat.Options{Store: st, Agents: mgr, Events: events.Nop{}, Log: logger, Channels: chanMgr, Workspace: dir})
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timeout waiting for " + what)
}

func jobRuns(t *testing.T, st *store.Store, jobID string) []store.AutomationRun {
	t.Helper()
	runs, err := st.ListAutomationRuns(context.Background(), jobID)
	if err != nil {
		t.Fatal(err)
	}
	return runs
}
