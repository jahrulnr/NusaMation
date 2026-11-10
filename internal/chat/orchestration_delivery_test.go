package chat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jahrulnr/nusamation/internal/agent"
	"github.com/jahrulnr/nusamation/internal/channels"
	"github.com/jahrulnr/nusamation/internal/events"
	"github.com/jahrulnr/nusamation/internal/security"
	"github.com/jahrulnr/nusamation/internal/store"
	"github.com/jahrulnr/nusamation/internal/testutil"
)

type notifyChannel struct {
	fakeMediaChannel
	mu    sync.Mutex
	notes []string
	fail  error
}

func (c *notifyChannel) Name() string { return "notify" }

func (c *notifyChannel) Notify(_ context.Context, _ channels.Target, text string) error {
	if c.fail != nil {
		return c.fail
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notes = append(c.notes, text)
	return nil
}

func (c *notifyChannel) sent() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.notes...)
}

func newNotifyService(t *testing.T) (*Service, *notifyChannel, *store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	sealer, err := security.LoadOrCreateSealer(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	st.SetSealer(sealer)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	chanMgr := channels.NewManager(events.Nop{}, logger)
	ch := &notifyChannel{}
	chanMgr.Register(ch)
	svc := New(Options{Store: st, Channels: chanMgr, Log: logger})
	return svc, ch, st, dir
}

func notifyTurn(origin string) *activeTurn {
	return &activeTurn{
		conversationID: "cv_1",
		turn:           store.Turn{OriginChannel: origin},
		conv: store.Conversation{
			Routing: store.Routing{ReplyToOrigin: true, MirrorFinal: true},
			Links:   []store.ChannelLink{{Channel: "notify", ExternalID: "ext", DisplayName: "Test"}},
		},
	}
}

func TestNotifyActiveUsesOriginChannel(t *testing.T) {
	svc, ch, _, _ := newNotifyService(t)
	a := notifyTurn("notify")
	svc.active["cv_1"] = a
	if err := svc.NotifyActive(context.Background(), "cv_1", "still working"); err != nil {
		t.Fatal(err)
	}
	if notes := ch.sent(); len(notes) != 1 || notes[0] != "still working" {
		t.Fatalf("notes = %#v", notes)
	}
}

func TestNotifyActiveRejectsWebAndMissingTurn(t *testing.T) {
	svc, _, _, _ := newNotifyService(t)
	a := notifyTurn("web")
	svc.active["cv_1"] = a
	if err := svc.NotifyActive(context.Background(), "cv_1", "x"); !errors.Is(err, ErrNoChannel) {
		t.Fatalf("web err = %v", err)
	}
	if err := svc.NotifyActive(context.Background(), "missing", "x"); !errors.Is(err, ErrNoActiveTurn) {
		t.Fatalf("missing err = %v", err)
	}
	if err := svc.NotifyActive(context.Background(), "cv_1", "  "); !errors.Is(err, ErrInvalidPrompt) {
		t.Fatalf("empty err = %v", err)
	}
}

func TestNotifyActiveSurfacesChannelFailure(t *testing.T) {
	svc, ch, _, _ := newNotifyService(t)
	ch.fail = errors.New("send failed")
	a := notifyTurn("notify")
	svc.active["cv_1"] = a
	if err := svc.NotifyActive(context.Background(), "cv_1", "x"); err == nil || err.Error() != "send failed" {
		t.Fatalf("err = %v", err)
	}
}

func TestApprovalChannelFallsBackToConversationLink(t *testing.T) {
	svc, ch, _, _ := newNotifyService(t)
	a := notifyTurn("agent")
	got, target, ok := svc.approvalChannel(a)
	if !ok || got.Name() != ch.Name() || target.ExternalID != "ext" {
		t.Fatalf("channel=%v target=%+v ok=%v", got, target, ok)
	}
	a.turn.OriginChannel = "notify"
	got, _, ok = svc.approvalChannel(a)
	if !ok || got.Name() != ch.Name() {
		t.Fatalf("origin channel = %v ok=%v", got, ok)
	}
}

func TestMirrorFinalSkipsOrchestratedTurn(t *testing.T) {
	svc, ch, _, _ := newNotifyService(t)
	a := notifyTurn("agent")
	a.finalStatus = "completed"
	svc.mirrorFinal(a)
	if notes := ch.sent(); len(notes) != 0 {
		t.Fatalf("orchestrated turn mirrored: %#v", notes)
	}
}

func TestSubmitOrchestratedTurnKeepsOriginAndSkipsChannel(t *testing.T) {
	dir := t.TempDir()
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
	agentMgr := agent.NewManager(st, logger)
	chanMgr := channels.NewManager(events.Nop{}, logger)
	ch := &notifyChannel{}
	chanMgr.Register(ch)
	workspace := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	svc := New(Options{
		Store: st, Agents: agentMgr, Channels: chanMgr, Events: events.Nop{}, Log: logger,
		UploadDir: filepath.Join(dir, "uploads"), Workspace: workspace,
	})
	agentMgr.SetSink(svc)
	defer agentMgr.StopAll()
	enabled := true
	ag, err := st.CreateAgent(context.Background(), store.AgentInput{
		Name: "Fake", Slug: "fake-orch", Command: testutil.BuildFakeACP(t), Cwd: workspace,
		Enabled: &enabled, ApprovalPolicy: "auto_all",
	})
	if err != nil {
		t.Fatal(err)
	}
	conv, err := svc.CreateConversation(context.Background(), ag.ID, "Worker", workspace, "web", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddChannelLink(context.Background(), store.ChannelLink{
		ConversationID: conv.ID, Channel: "notify", ExternalID: "ext", DisplayName: "Test",
	}); err != nil {
		t.Fatal(err)
	}
	msg, turn, err := svc.Submit(context.Background(), SubmitInput{
		ConversationID: conv.ID, Text: "work", Channel: "agent", Delivery: "queue",
		Orchestrator: &Orchestrator{ConversationID: "cv_supervisor", Title: "Supervisor"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var meta struct {
		Orchestration *Orchestrator `json:"orchestration"`
	}
	if err := json.Unmarshal(msg.Metadata, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Orchestration == nil || meta.Orchestration.ConversationID != "cv_supervisor" {
		t.Fatalf("metadata = %s", msg.Metadata)
	}
	if turn.OriginChannel != "agent" {
		t.Fatalf("origin = %q", turn.OriginChannel)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		current, err := st.GetTurn(context.Background(), turn.ID)
		if err == nil && (current.Status == "completed" || current.Status == "error" || current.Status == "cancelled") {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if notes := ch.sent(); len(notes) != 0 {
		t.Fatalf("orchestrated final reached channel: %#v", notes)
	}
}

func TestSubmitRejectsOrchestratedSteer(t *testing.T) {
	svc := New(Options{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	_, _, err := svc.Submit(context.Background(), SubmitInput{
		ConversationID: "cv_1", Text: "x", Delivery: "steer",
		Orchestrator: &Orchestrator{ConversationID: "cv_parent"},
	})
	if !errors.Is(err, ErrOrchestratedSteer) {
		t.Fatalf("err = %v", err)
	}
}
