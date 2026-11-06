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

func TestCrossChannelSteerKeepsRunningDeliveryContext(t *testing.T) {
	t.Setenv("FAKE_ACP_MIDTURN", "1")
	e := newAfterToolsEnv(t)
	_, running := e.submit("steer-hold", "")
	waitFor(t, func() bool { return e.hasText("working") }, "hold prompt to start")
	msg, turn, err := e.svc.Submit(context.Background(), SubmitInput{
		ConversationID: e.conv.ID, Text: "use usb", Channel: "whatsapp", Delivery: deliverySteer,
		OriginRef: `{"externalId":"chat-1","messageId":"wa-1","senderId":"sender-1"}`,
	})
	if err != nil || turn.ID != running.ID || msg.Text != "use usb" {
		t.Fatalf("steer result: %+v %+v %v", msg, turn, err)
	}
	e.waitStatus(running.ID, "completed")
	for _, line := range e.trace() {
		if strings.HasPrefix(line, "steer use usb") {
			for _, want := range []string{`"inputChannel":"whatsapp"`, `"deliveryChannel":"web"`, `"messageId":"wa-1"`, "steers the running turn"} {
				if !strings.Contains(line, want) {
					t.Fatalf("steer missing %s: %s", want, line)
				}
			}
			return
		}
	}
	t.Fatal("no steering request recorded")
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

// A steer must not be injected before the original prompt reached the wire —
// otherwise it can overtake it.
func TestInjectableRequiresPromptSent(t *testing.T) {
	a := newActiveTurn(store.Conversation{ID: "c"}, store.Turn{ID: "t"}, store.Agent{}, store.Message{},
		context.Background(), func() {})
	a.session = &agent.Session{}
	a.promptStarting = true
	if a.injectable() {
		t.Fatal("injectable before the prompt was sent")
	}
	a.markPromptSent()
	if !a.injectable() {
		t.Fatal("not injectable after the prompt was sent")
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
func (c *recChannel) finals() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, ev := range c.events {
		if ev.final {
			out = append(out, ev.text)
		}
	}
	return out
}

type recDelivery struct{ ch *recChannel }

func (d *recDelivery) ID() string { return "rec" }
func (d *recDelivery) SendSegment(_ context.Context, text string) error {
	d.ch.mu.Lock()
	d.ch.events = append(d.ch.events, recEvent{final: true, text: text})
	d.ch.mu.Unlock()
	return nil
}
func (d *recDelivery) Update(_ context.Context, text string, final bool) error {
	d.ch.mu.Lock()
	d.ch.events = append(d.ch.events, recEvent{final: final, text: text})
	d.ch.mu.Unlock()
	return nil
}

func TestChannelReportsErrorsAfterPartialOutput(t *testing.T) {
	for _, tc := range []struct {
		name, current string
		delivered     bool
	}{
		{name: "no output"},
		{name: "partial output", current: "I am working"},
		{name: "earlier segment", delivered: true},
		{name: "earlier and current", current: "More progress", delivered: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recChannel{}
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			manager := channels.NewManager(events.Nop{}, logger)
			manager.Register(rec)
			svc := &Service{channels: manager, log: logger}
			a := &activeTurn{turn: store.Turn{OriginChannel: "wa", OriginRef: `{"externalId":"chat-1"}`}, delivery: &recDelivery{ch: rec}, deliverToChannel: true, deliveredSegment: tc.delivered, finalStatus: "error", finalError: "agent process exited: exit status 143"}
			a.assistant.WriteString(tc.current)
			a.assistantAll.WriteString(tc.current)
			svc.updateDelivery(a, true)
			got := rec.finals()
			if len(got) != 2 || got[0] != tc.current || got[1] != "The agent hit an error: agent process exited: exit status 143" {
				t.Fatalf("channel events = %#v", got)
			}
		})
	}
}

func TestMirroredErrorFollowsPartialOutput(t *testing.T) {
	rec := &recChannel{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := channels.NewManager(events.Nop{}, logger)
	manager.Register(rec)
	svc := &Service{channels: manager, log: logger}
	a := &activeTurn{
		turn:        store.Turn{OriginChannel: "web"},
		conv:        store.Conversation{Routing: store.Routing{MirrorFinal: true}, Links: []store.ChannelLink{{Channel: "wa", ExternalID: "chat-1"}}},
		finalStatus: "error", finalError: "process exited",
	}
	a.assistantAll.WriteString("I am working")
	svc.mirrorFinal(a)
	waitFor(t, func() bool { return len(rec.finals()) == 2 }, "mirrored error")
	got := rec.finals()
	if got[0] != "I am working" || got[1] != "The agent hit an error: process exited" {
		t.Fatalf("mirrored messages = %#v", got)
	}
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
	waitFor(t, func() bool { return len(rec.finals()) >= 1 }, "first message while agent is working")
	running, err := st.GetTurn(context.Background(), turn.ID)
	if err != nil || running.Status != "running" {
		t.Fatalf("first message arrived after work ended: turn=%#v err=%v", running, err)
	}
	waitFor(t, func() bool {
		got, err := st.GetTurn(context.Background(), turn.ID)
		return err == nil && got.Status == "completed" && len(rec.finals()) == 3
	}, "three separate channel messages")
	finals := rec.finals()
	if len(finals) != 3 {
		t.Fatalf("final messages = %#v", rec.events)
	}
	for i, part := range []string{"reason one", "reason two", "reason three"} {
		if finals[i] != part {
			t.Fatalf("message %d = %q, want %q", i, finals[i], part)
		}
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, ev := range rec.events {
		if !ev.final && strings.TrimSpace(ev.text) != "" && ev.text != "On it…" {
			t.Fatalf("text left the channel before the turn finished: %#v", rec.events)
		}
	}
}
