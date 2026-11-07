package chat

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jahrulnr/nusamation/internal/acp"
	"github.com/jahrulnr/nusamation/internal/agent"
	"github.com/jahrulnr/nusamation/internal/channels"
	"github.com/jahrulnr/nusamation/internal/events"
	"github.com/jahrulnr/nusamation/internal/security"
	"github.com/jahrulnr/nusamation/internal/store"
	"github.com/jahrulnr/nusamation/internal/testutil"
)

type fakeMediaChannel struct {
	mu    sync.Mutex
	media []channels.OutboundMedia
}

func (c *fakeMediaChannel) Name() string  { return "fakechan" }
func (c *fakeMediaChannel) Label() string { return "Fake" }
func (c *fakeMediaChannel) Start(context.Context) error {
	return nil
}
func (c *fakeMediaChannel) Stop() error { return nil }
func (c *fakeMediaChannel) Status() channels.Status {
	return channels.Status{Name: "fakechan", State: "connected", UpdatedAt: time.Now().UnixMilli()}
}
func (c *fakeMediaChannel) Begin(context.Context, channels.Target, string) (channels.Delivery, error) {
	return fakeDelivery{}, nil
}
func (c *fakeMediaChannel) Notify(context.Context, channels.Target, string) error { return nil }

func (c *fakeMediaChannel) SendMedia(_ context.Context, _ channels.Target, media channels.OutboundMedia) error {
	if len(media.Data) == 0 && media.Path != "" {
		data, err := os.ReadFile(media.Path)
		if err != nil {
			return err
		}
		media.Data = data
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.media = append(c.media, media)
	return nil
}

func (c *fakeMediaChannel) sent() []channels.OutboundMedia {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]channels.OutboundMedia(nil), c.media...)
}

type fakeDelivery struct{}

func (fakeDelivery) ID() string { return "fake" }
func (fakeDelivery) Update(context.Context, string, bool) error {
	return nil
}

func waitForOutbound(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(60 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestOutboundMediaDelivery(t *testing.T) {
	bin := testutil.BuildFakeACP(t)

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
	fake := &fakeMediaChannel{}
	chanMgr.Register(fake)
	workspace := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(workspace, "report.txt")
	if err := os.WriteFile(filePath, []byte("quarterly numbers\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := New(Options{
		Store: st, Agents: agentMgr, Channels: chanMgr, Events: events.Nop{},
		Log: logger, UploadDir: filepath.Join(dir, "uploads"), Workspace: workspace,
	})
	agentMgr.SetSink(svc)
	defer agentMgr.StopAll()

	ctx := context.Background()
	enabled := true
	ag, err := st.CreateAgent(ctx, store.AgentInput{
		Name: "Fake", Slug: "fake-media", Command: bin, Cwd: workspace, Enabled: &enabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	conv, err := svc.CreateConversation(ctx, ag.ID, "Media test", workspace, "fakechan", "")
	if err != nil {
		t.Fatal(err)
	}
	if !conv.Routing.ReplyToOrigin {
		t.Fatal("ReplyToOrigin should default to true")
	}
	_, turn, err := svc.Submit(ctx, SubmitInput{
		ConversationID: conv.ID,
		Text:           "send-media-test file://" + filepath.ToSlash(filePath),
		Channel:        "fakechan",
		OriginRef:      `{"externalId":"chat-1"}`,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Media delivery is asynchronous to the turn finishing; poll the channel.
	waitForOutbound(t, "2 outbound media deliveries", func() bool { return len(fake.sent()) == 2 })
	got := fake.sent()
	byName := map[string]channels.OutboundMedia{}
	for _, m := range got {
		byName[m.Name] = m
	}
	img, ok := byName["pixel.png"]
	if !ok || img.MIME != "image/png" || len(img.Data) == 0 {
		t.Fatalf("image content block not delivered: %#v", got)
	}
	file, ok := byName["result"]
	if !ok || string(file.Data) != "quarterly numbers\n" {
		t.Fatalf("file:// link not delivered as media: %#v", got)
	}

	var ups []store.Upload
	waitForOutbound(t, "uploads bound to the assistant message", func() bool {
		msgs, err := st.ListMessages(ctx, conv.ID, 0, 0, 100)
		if err != nil {
			return false
		}
		for _, m := range slices.Backward(msgs) {

			if m.TurnID == turn.ID && m.Role == "assistant" && m.Kind == "text" {
				ups, _ = st.ListUploadsForMessage(ctx, m.ID)
				return len(ups) == 2
			}
		}
		return false
	})
}

func TestCleanDeliveredText(t *testing.T) {
	sent := map[string]bool{"/tmp/a/chart.png": true, "file:///tmp/a/b.png": true}
	cases := []struct{ in, want string }{
		{"see [chart.png](/tmp/a/chart.png) here", "see chart.png here"},
		{"look ![chart](/tmp/a/chart.png)", "look chart"},
		{"path is file:///tmp/a/b.png done", "path is  done"},
		{"[f](file:///tmp/x.pdf)", "[f](file:///tmp/x.pdf)"},       // not sent → kept
		{"See [settings](/settings)", "See [settings](/settings)"}, // web link kept
		{"normal text", "normal text"},
		{"[docs](https://example.com/x)", "[docs](https://example.com/x)"},
	}
	for _, c := range cases {
		if got := cleanDeliveredText(c.in, sent); got != c.want {
			t.Errorf("cleanDeliveredText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func testOutboundService(t *testing.T) (*Service, string, string) {
	t.Helper()
	dir := t.TempDir()
	ws := filepath.Join(dir, "workspace")
	up := filepath.Join(dir, "uploads")
	if err := os.MkdirAll(ws, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(up, 0o700); err != nil {
		t.Fatal(err)
	}
	svc := New(Options{
		Workspace: ws, UploadDir: up,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return svc, ws, up
}

func TestOutboundFromBlock(t *testing.T) {
	svc, ws, _ := testOutboundService(t)
	inside := filepath.Join(ws, "rep.pdf")
	if err := os.WriteFile(inside, []byte("pdf"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &activeTurn{turn: store.Turn{ID: "turn_x"}}
	png := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	m, ok := svc.outboundFromBlock(a, acpBlock(t, `{"type":"image","mimeType":"image/png","data":"`+png+`","name":"p.png"}`))
	if !ok || m.Name != "p.png" || m.MIME != "image/png" || len(m.Data) == 0 {
		t.Fatalf("image block: %#v ok=%v", m, ok)
	}
	m, ok = svc.outboundFromBlock(a, acpBlock(t, `{"type":"resource_link","uri":"file://`+filepath.ToSlash(inside)+`","name":"rep.pdf"}`))
	if !ok || m.Path != inside || m.Name != "rep.pdf" {
		t.Fatalf("resource_link block: %#v ok=%v", m, ok)
	}
	if _, ok = svc.outboundFromBlock(a, acpBlock(t, `{"type":"resource_link","uri":"file:///etc/passwd"}`)); ok {
		t.Fatal("file outside allowed roots must be refused")
	}
	if _, ok = svc.outboundFromBlock(a, acpBlock(t, `{"type":"resource_link","uri":"https://example.com/x.png"}`)); ok {
		t.Fatal("http resource_link must not become outbound media")
	}
	if _, ok = svc.outboundFromBlock(a, acpBlock(t, `{"type":"text","text":"hi"}`)); ok {
		t.Fatal("text block must not become outbound media")
	}
}

func TestResolveOutboundPath(t *testing.T) {
	svc, ws, _ := testOutboundService(t)
	a := &activeTurn{conv: store.Conversation{Cwd: ws}}
	roots := svc.allowedOutboundRoots(a)
	resolve := func(ref string) (string, bool) {
		return svc.resolveOutboundPath(ref, roots)
	}

	real := filepath.Join(ws, "ok.txt")
	if err := os.WriteFile(real, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if p, ok := resolve("file://" + filepath.ToSlash(real)); !ok || p != real {
		t.Fatalf("allowed file refused: %q ok=%v", p, ok)
	}
	if p, ok := resolve(real); !ok || p != real {
		t.Fatalf("bare absolute path refused: %q ok=%v", p, ok)
	}
	spaced := filepath.Join(ws, "a b.pdf")
	if err := os.WriteFile(spaced, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if p, ok := resolve("file://" + filepath.ToSlash(ws) + "/a%20b.pdf"); !ok || p != spaced {
		t.Fatalf("percent-encoded path failed: %q ok=%v", p, ok)
	}
	cases := []string{
		"file:///etc/passwd",
		"file:///root/.nusamation/secret.key",
		"file:///dev/zero",
		"file://../../etc/passwd",
		"file://host/etc/passwd",
		"file:///../x",
		"relative/path.png",
		"file://",
		"https://example.com/x.png",
	}
	for _, ref := range cases {
		if p, ok := resolve(ref); ok {
			t.Errorf("ref %q resolved to %q, want refusal", ref, p)
		}
	}
	if _, ok := resolve("file://" + filepath.ToSlash(ws) + "/missing.png"); ok {
		t.Error("nonexistent file must be refused")
	}
	link := filepath.Join(ws, "escape.txt")
	if err := os.Symlink("/etc/passwd", link); err == nil {
		if p, ok := resolve("file://" + filepath.ToSlash(link)); ok {
			t.Errorf("symlink escape resolved to %q, want refusal", p)
		}
	}
	// A real file outside every allowed root — the exfiltration case.
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if p, ok := resolve("file://" + filepath.ToSlash(outside)); ok {
		t.Errorf("file outside roots resolved to %q, want refusal", p)
	}
}

func acpBlock(t *testing.T, raw string) acp.ContentBlock {
	t.Helper()
	var b acp.ContentBlock
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		t.Fatal(err)
	}
	return b
}
