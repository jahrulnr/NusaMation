package whatsapp

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/jahrulnr/nusamation/internal/store"
)

func newGateChannel(t *testing.T) (*Channel, *store.Store) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	c := &Channel{store: st, log: slog.New(slog.NewTextHandler(io.Discard, nil)), settings: DefaultSettings()}
	return c, st
}

func msgEvent(sender, chat, id string, msg *waE2E.Message) *events.Message {
	jid := types.JID{User: sender, Server: types.DefaultUserServer}
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: jid, Sender: jid},
			ID:            types.MessageID(id), Timestamp: time.Now(), PushName: "Tester",
		},
		Message: msg,
	}
}

// gateInbound rejects unknown senders, records them for the blocked panel,
// and lets allowlisted senders through.
func TestGateInboundRecordsBlockedSenders(t *testing.T) {
	c, st := newGateChannel(t)
	c.settings = Settings{Enabled: true, AllowedNumbers: []string{"62"}, GroupPolicy: "mention"}

	blocked := msgEvent("999", "999", "m1", &waE2E.Message{Conversation: new("hi")})
	if c.gateInbound(context.Background(), blocked) {
		t.Fatal("unknown sender passed the gate")
	}
	list := c.blockedList()
	if len(list) != 1 || list[0].Sender != "999@s.whatsapp.net" || list[0].PushName != "Tester" {
		t.Fatalf("blocked list = %#v", list)
	}

	allowed := msgEvent("62", "62", "m2", &waE2E.Message{Conversation: new("hi")})
	if !c.gateInbound(context.Background(), allowed) {
		t.Fatal("allowlisted sender was rejected")
	}
	if len(c.blockedList()) != 1 {
		t.Fatal("allowlisted sender was recorded as blocked")
	}

	// A repeat tap dedupes.
	c.gateInbound(context.Background(), blocked)
	if got := c.blockedList(); len(got) != 1 {
		t.Fatalf("blocked list grew: %#v", got)
	}

	// Disabled settings close the gate.
	c.settings.Enabled = false
	if c.gateInbound(context.Background(), allowed) {
		t.Fatal("disabled channel passed the gate")
	}
	_ = st
}

// Stale history and broadcasts must not reach the agent.
func TestGateInboundRejectsStaleAndBroadcast(t *testing.T) {
	c, _ := newGateChannel(t)
	c.settings = DefaultSettings()

	stale := msgEvent("62", "62", "m1", &waE2E.Message{Conversation: new("hi")})
	stale.Info.Timestamp = time.Now().Add(-3 * time.Hour)
	if c.gateInbound(context.Background(), stale) {
		t.Fatal("stale message passed the gate")
	}

	status := msgEvent("62", "status", "m2", &waE2E.Message{Conversation: new("hi")})
	status.Info.Chat = types.JID{User: "status", Server: types.BroadcastServer}
	if c.gateInbound(context.Background(), status) {
		t.Fatal("status update passed the gate")
	}
}

// An incoming revoke tombstones the indexed original instead of storing an
// empty protocol-message row.
func TestIndexMessageRevokesOriginal(t *testing.T) {
	c, st := newGateChannel(t)
	ctx := context.Background()
	if err := c.indexMessage(ctx, msgEvent("62", "62", "m1", &waE2E.Message{Conversation: new("secret plans")}), "Tester", true); err != nil {
		t.Fatal(err)
	}
	page, err := st.SearchWhatsAppMessages(ctx, store.WhatsAppSearch{Query: "secret", Limit: 10})
	if err != nil || len(page.Messages) != 1 {
		t.Fatalf("before revoke: %#v, %v", page, err)
	}

	revoke := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: types.JID{User: "62", Server: types.DefaultUserServer}, Sender: types.JID{User: "62", Server: types.DefaultUserServer}},
			ID:            types.MessageID("rev1"), Timestamp: time.Now(),
		},
		Message: &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
			Type: waE2E.ProtocolMessage_REVOKE.Enum(),
			Key:  &waCommon.MessageKey{RemoteJID: new("62@s.whatsapp.net"), ID: new("m1"), FromMe: new(false)},
		}},
	}
	if err := c.indexMessage(ctx, revoke, "", true); err != nil {
		t.Fatal(err)
	}
	page, err = st.SearchWhatsAppMessages(ctx, store.WhatsAppSearch{Query: "secret", Limit: 10})
	if err != nil || len(page.Messages) != 0 {
		t.Fatalf("revoked message still searchable: %#v, %v", page, err)
	}
	// The revoke wrapper itself must not become an indexed row.
	page, err = st.SearchWhatsAppMessages(ctx, store.WhatsAppSearch{Query: "", Limit: 50})
	if err != nil || len(page.Messages) != 1 {
		t.Fatalf("revoke wrapper indexed: %#v, %v", page, err)
	}
	if got := page.Messages[0]; got.ID != "m1" || strings.TrimSpace(got.Text) != "" {
		t.Fatalf("remaining row = %#v", got)
	}
}

func TestSettingsRetentionNormalization(t *testing.T) {
	s := Settings{RetentionDays: -5}
	s.Normalize()
	if s.RetentionDays != 0 {
		t.Fatalf("negative retention = %d", s.RetentionDays)
	}
	s.RetentionDays = 99999
	s.Normalize()
	if s.RetentionDays != 3650 {
		t.Fatalf("huge retention = %d", s.RetentionDays)
	}
}
