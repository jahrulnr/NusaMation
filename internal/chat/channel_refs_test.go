package chat

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jahrulnr/nusamation/internal/channels"
	"github.com/jahrulnr/nusamation/internal/events"
	"github.com/jahrulnr/nusamation/internal/store"
)

// menuChannel records per-chat command publishes. A second channel type
// without SessionCommands stands in for WhatsApp, which must be skipped.
type menuChannel struct {
	mu      sync.Mutex
	synced  []menuSync
	cleared []string
}

type menuSync struct {
	external string
	names    []string
}

func (m *menuChannel) Name() string                { return "telegram" }
func (m *menuChannel) Label() string               { return "Telegram" }
func (m *menuChannel) Start(context.Context) error { return nil }
func (m *menuChannel) Stop() error                 { return nil }
func (m *menuChannel) Status() channels.Status     { return channels.Status{Name: "telegram"} }
func (m *menuChannel) Begin(context.Context, channels.Target, string) (channels.Delivery, error) {
	return nil, nil
}
func (m *menuChannel) Notify(context.Context, channels.Target, string) error { return nil }

func (m *menuChannel) SyncSessionCommands(_ context.Context, externalID string, cmds []channels.SessionCommand) error {
	names := make([]string, len(cmds))
	for i, c := range cmds {
		names[i] = c.Name
	}
	m.mu.Lock()
	m.synced = append(m.synced, menuSync{external: externalID, names: names})
	m.mu.Unlock()
	return nil
}

func (m *menuChannel) ClearSessionCommands(_ context.Context, externalID string) error {
	m.mu.Lock()
	m.cleared = append(m.cleared, externalID)
	m.mu.Unlock()
	return nil
}

type plainChannel struct{ name string }

func (p plainChannel) Name() string                { return p.name }
func (p plainChannel) Label() string               { return p.name }
func (p plainChannel) Start(context.Context) error { return nil }
func (p plainChannel) Stop() error                 { return nil }
func (p plainChannel) Status() channels.Status     { return channels.Status{Name: p.name} }
func (p plainChannel) Begin(context.Context, channels.Target, string) (channels.Delivery, error) {
	return nil, nil
}
func (p plainChannel) Notify(context.Context, channels.Target, string) error { return nil }

func TestChannelCommandMenuSync(t *testing.T) {
	svc, st, _, ag := newProjectEnv(t, "")
	menu := &menuChannel{}
	mgr := channels.NewManager(events.Nop{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	mgr.Register(menu)
	mgr.Register(plainChannel{name: "whatsapp"})
	svc.channels = mgr
	ctx := context.Background()
	conv, err := st.CreateConversation(ctx, ag.ID, "Room", "", "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, thread := range []string{"1", "9"} {
		if _, err := st.MoveChannelLink(ctx, store.ChannelLink{
			ConversationID: conv.ID, Channel: "telegram", ExternalID: "555", ThreadID: thread,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.MoveChannelLink(ctx, store.ChannelLink{
		ConversationID: conv.ID, Channel: "whatsapp", ExternalID: "555",
	}); err != nil {
		t.Fatal(err)
	}

	svc.syncChannelCommands(conv.ID, []json.RawMessage{
		json.RawMessage(`{"name":"compact","description":"Compact"}`),
		json.RawMessage(`{"description":"nameless"}`),
		json.RawMessage(`not-json`),
	})
	waitUntil(t, func() bool {
		menu.mu.Lock()
		defer menu.mu.Unlock()
		return len(menu.synced) > 0
	})
	menu.mu.Lock()
	if len(menu.synced) != 1 || menu.synced[0].external != "555" || len(menu.synced[0].names) != 1 || menu.synced[0].names[0] != "compact" {
		t.Fatalf("sync = %#v", menu.synced)
	}
	menu.mu.Unlock()

	svc.syncChannelCommands(conv.ID, nil)
	waitUntil(t, func() bool {
		menu.mu.Lock()
		defer menu.mu.Unlock()
		return len(menu.cleared) > 0
	})
	menu.mu.Lock()
	if len(menu.cleared) != 1 || menu.cleared[0] != "555" {
		t.Fatalf("clear = %#v", menu.cleared)
	}
	menu.mu.Unlock()
}

func TestNewCommandClearsSessionMenu(t *testing.T) {
	svc, st, _, ag := newProjectEnv(t, "")
	menu := &menuChannel{}
	mgr := channels.NewManager(events.Nop{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	mgr.Register(menu)
	svc.channels = mgr
	ctx := context.Background()
	conv, err := st.CreateConversation(ctx, ag.ID, "Room", "", "telegram", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.MoveChannelLink(ctx, store.ChannelLink{
		ConversationID: conv.ID, Channel: "telegram", ExternalID: "77", DisplayName: "Ana",
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.HandleInbound(ctx, channels.Inbound{
		Channel: "telegram", ExternalID: "77", ChatName: "Ana", MessageID: "77:1", Text: "/new",
	}); err != nil {
		t.Fatal(err)
	}
	menu.mu.Lock()
	defer menu.mu.Unlock()
	if len(menu.cleared) != 1 || menu.cleared[0] != "77" {
		t.Fatalf("clear = %#v", menu.cleared)
	}
}

func TestInboundGroupPrefixShiftsReferences(t *testing.T) {
	svc, st, _, ag := newProjectEnv(t, "")
	ctx := context.Background()
	conv, err := st.CreateConversation(ctx, ag.ID, "Team", "", "telegram", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.MoveChannelLink(ctx, store.ChannelLink{
		ConversationID: conv.ID, Channel: "telegram", ExternalID: "42", DisplayName: "Team", IsGroup: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.HandleInbound(ctx, channels.Inbound{
		Channel: "telegram", ExternalID: "42", SenderName: "李", ChatName: "Team",
		MessageID: "42:1", Text: "@cat hi", IsGroup: true,
		References: []channels.Reference{
			{Kind: "conversation", ID: "cv_other", Label: "@cat", Start: 0, End: 4},
			{Kind: "project", ID: "prj_x", Label: "@nope", Start: 0, End: 5},
		},
	}); err != nil {
		t.Fatal(err)
	}
	refs := userReferences(t, st, conv.ID)
	if len(refs) != 1 || refs[0].Label != "@cat" || refs[0].ID != "cv_other" {
		t.Fatalf("references = %#v", refs)
	}
	msgs, err := st.ListMessagesAscending(ctx, conv.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	var text string
	for _, m := range msgs {
		if m.Role == "user" {
			text = m.Text
			break
		}
	}
	prefix := len("李: ")
	if text != "李: @cat hi" || refs[0].Start != prefix || refs[0].End != prefix+4 || text[refs[0].Start:refs[0].End] != "@cat" {
		t.Fatalf("text %q refs %#v", text, refs)
	}

	dm, err := st.CreateConversation(ctx, ag.ID, "DM", "", "telegram", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.MoveChannelLink(ctx, store.ChannelLink{
		ConversationID: dm.ID, Channel: "telegram", ExternalID: "43", DisplayName: "Ana",
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.HandleInbound(ctx, channels.Inbound{
		Channel: "telegram", ExternalID: "43", SenderName: "李", ChatName: "Ana",
		MessageID: "43:1", Text: "see @cat", IsGroup: false,
		References: []channels.Reference{
			{Kind: "conversation", ID: "cv_other", Label: "@cat", Start: 4, End: 8},
		},
	}); err != nil {
		t.Fatal(err)
	}
	dmRefs := userReferences(t, st, dm.ID)
	if len(dmRefs) != 1 || dmRefs[0].Start != 4 || dmRefs[0].End != 8 {
		t.Fatalf("dm references = %#v", dmRefs)
	}
}

func userReferences(t *testing.T, st *store.Store, conversationID string) []store.PromptReference {
	t.Helper()
	msgs, err := st.ListMessagesAscending(context.Background(), conversationID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if m.Role != "user" {
			continue
		}
		var meta struct {
			References []store.PromptReference `json:"references"`
		}
		if err := json.Unmarshal(m.Metadata, &meta); err != nil {
			t.Fatal(err)
		}
		return meta.References
	}
	t.Fatal("no user message")
	return nil
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out")
}
