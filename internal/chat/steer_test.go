package chat

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jahrulnr/nusamation/internal/agent"
	"github.com/jahrulnr/nusamation/internal/channels"
	"github.com/jahrulnr/nusamation/internal/events"
	"github.com/jahrulnr/nusamation/internal/security"
	"github.com/jahrulnr/nusamation/internal/store"
)

func TestSteerInjectsIntoRunningTurn(t *testing.T) {
	t.Setenv("FAKE_ACP_MIDTURN", "1")
	e := newAfterToolsEnv(t)
	_, p1 := e.submit("steer-hold", "")
	waitFor(t, func() bool { return e.hasText("working") }, "hold prompt to start")

	msg, turn := e.submit("use usb", deliverySteer)
	if turn.ID != p1.ID {
		t.Fatalf("steer created turn %s, want the running turn %s", turn.ID, p1.ID)
	}
	if msg.Text != "use usb" {
		t.Fatalf("stored text %q", msg.Text)
	}
	if _, err := e.st.NextQueuedTurn(context.Background(), e.conv.ID); !store.IsNotFound(err) {
		t.Fatalf("steer left a queued turn: %v", err)
	}
	waitFor(t, func() bool { return e.hasText("saw-steer: use usb") }, "agent to see the steer")

	e.waitStatus(p1.ID, "completed")
	lines := e.trace()
	_, start := promptStart(t, lines, "steer-hold")
	id := strings.Fields(lines[start])[1]
	steered, answered := -1, -1
	for i, line := range lines {
		if steered < 0 && strings.HasPrefix(line, "steer ") {
			steered = i
		}
		if answered < 0 && line == "respond "+id {
			answered = i
		}
	}
	if steered < 0 || answered < 0 || !(start < steered && steered < answered) {
		t.Fatalf("steer was not injected before the running prompt returned: %v", lines)
	}
	if n := countTrace(lines, "cancel"); n != 0 {
		t.Fatalf("steer cancelled the turn %d times", n)
	}
}

func TestChannelSteerWithoutCapabilityWaitsForTools(t *testing.T) {
	e := newAfterToolsEnv(t)
	_, p1 := e.submit("steer-hold", "")
	waitFor(t, func() bool { return e.hasText("working") }, "hold prompt to start")
	msg, p2, err := e.svc.Submit(context.Background(), SubmitInput{
		ConversationID: e.conv.ID, Text: "use usb", Channel: "whatsapp", Delivery: deliverySteer,
	})
	if err != nil {
		t.Fatal(err)
	}
	if p2.ID == p1.ID || p2.Status != "queued" {
		t.Fatalf("channel steer fallback = %+v, running %s", p2, p1.ID)
	}
	var meta map[string]any
	if json.Unmarshal(msg.Metadata, &meta) != nil || meta["delivery"] != deliveryAfterTools {
		t.Fatalf("metadata = %s", msg.Metadata)
	}
	e.waitStatus(p1.ID, "cancelled")
	e.waitStatus(p2.ID, "completed")
	if n := countTrace(e.trace(), "cancel"); n == 0 {
		t.Fatal("channel steer did not cancel the running turn after its tools")
	}
}

func TestSteerWithoutCapabilityQueues(t *testing.T) {
	e := newAfterToolsEnv(t)
	_, p1 := e.submit("steer-hold", "")
	waitFor(t, func() bool { return e.hasText("working") }, "hold prompt to start")
	_, p2 := e.submit("use usb", deliverySteer)
	if p2.ID == p1.ID || p2.Status != "queued" {
		t.Fatalf("unsupported steer = %+v, running %s", p2, p1.ID)
	}
	e.waitStatus(p1.ID, "completed")
	e.waitStatus(p2.ID, "completed")
	if n := countTrace(e.trace(), "cancel"); n != 0 {
		t.Fatalf("fallback queue cancelled the turn %d times", n)
	}
}

func (e *afterToolsEnv) hasText(part string) bool {
	msgs, err := e.st.ListMessages(context.Background(), e.conv.ID, 0, 0, 500)
	if err != nil {
		return false
	}
	for _, m := range msgs {
		if strings.Contains(m.Text, part) {
			return true
		}
	}
	// The steer chunk can still be in the live buffer.
	for _, text := range e.svc.ActiveMessageTexts(e.conv.ID) {
		if strings.Contains(text, part) {
			return true
		}
	}
	return false
}

type recChannel struct {
	mu     sync.Mutex
	events []recEvent
}

type recEvent struct {
	begin bool
	final bool
	text  string
}

func (c *recChannel) Name() string                { return "wa" }
func (c *recChannel) Label() string               { return "WA" }
func (c *recChannel) Start(context.Context) error { return nil }
func (c *recChannel) Stop() error                 { return nil }
func (c *recChannel) Status() channels.Status {
	return channels.Status{Name: "wa", State: "connected", UpdatedAt: time.Now().UnixMilli()}
}
func (c *recChannel) Begin(_ context.Context, _ channels.Target, text string) (channels.Delivery, error) {
	c.mu.Lock()
	c.events = append(c.events, recEvent{begin: true, text: text})
	c.mu.Unlock()
	return &recDelivery{ch: c}, nil
}
func (c *recChannel) Notify(_ context.Context, _ channels.Target, text string) error {
	c.mu.Lock()
	c.events = append(c.events, recEvent{final: true, text: text})
	c.mu.Unlock()
	return nil
}
func (c *recChannel) texts() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, ev := range c.events {
		if ev.begin && ev.text != "On it…" {
			out = append(out, ev.text)
		}
		if ev.final {
			out = append(out, ev.text)
		}
	}
	return out
}

type recDelivery struct{ ch *recChannel }

func (d *recDelivery) ID() string { return "rec" }
func (d *recDelivery) Update(_ context.Context, text string, final bool) error {
	d.ch.mu.Lock()
	d.ch.events = append(d.ch.events, recEvent{final: final, text: text})
	d.ch.mu.Unlock()
	return nil
}

func TestChannelDeliversEveryReason(t *testing.T) {
	bin := buildFakeAgent(t)
	dir := t.TempDir()
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
	chanMgr := channels.NewManager(events.Nop{}, logger)
	rec := &recChannel{}
	chanMgr.Register(rec)
	workspace := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	svc := New(Options{
		Store: st, Agents: agentMgr, Channels: chanMgr, Events: events.Nop{},
		Log: logger, UploadDir: filepath.Join(dir, "uploads"), Workspace: workspace,
	})
	agentMgr.SetSink(svc)
	t.Cleanup(agentMgr.StopAll)
	enabled := true
	ag, err := st.CreateAgent(context.Background(), store.AgentInput{
		Name: "Fake", Slug: "fake-reasons", Command: bin, Cwd: workspace, Enabled: &enabled,
		ApprovalPolicy: "auto_all",
	})
	if err != nil {
		t.Fatal(err)
	}
	conv, err := svc.CreateConversation(context.Background(), ag.ID, "Reasons", workspace, "wa", "")
	if err != nil {
		t.Fatal(err)
	}
	_, turn, err := svc.Submit(context.Background(), SubmitInput{
		ConversationID: conv.ID, Text: "segment-reasons-test", Channel: "wa",
		OriginRef: `{"externalId":"chat-1"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		got, err := st.GetTurn(context.Background(), turn.ID)
		if err != nil || got.Status != "completed" {
			return false
		}
		texts := rec.texts()
		return strings.Contains(strings.Join(texts, "\n"), "reason one") &&
			strings.Contains(strings.Join(texts, "\n"), "reason two") &&
			strings.Contains(strings.Join(texts, "\n"), "reason three")
	}, "three reasons on the channel")
	joined := strings.Join(rec.texts(), "\n")
	one := strings.Index(joined, "reason one")
	two := strings.Index(joined, "reason two")
	three := strings.Index(joined, "reason three")
	if one < 0 || two < 0 || three < 0 || !(one < two && two < three) {
		t.Fatalf("channel texts = %q", joined)
	}
	// Each completed reply is its own send, not one growing edit of the first.
	committed := 0
	rec.mu.Lock()
	for _, ev := range rec.events {
		if ev.final || (ev.begin && ev.text != "On it…") {
			committed++
		}
	}
	rec.mu.Unlock()
	if committed < 3 {
		t.Fatalf("committed channel messages = %d, events %#v", committed, rec.events)
	}
}
