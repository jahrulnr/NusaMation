package whatsapp

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	wastore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/jahrulnr/nusamation/internal/store"
)

func TestPassiveCaptureAndHistoryAreSearchable(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	self := types.NewJID("100", types.DefaultUserServer)
	c := &Channel{store: st, log: slog.New(slog.NewTextHandler(io.Discard, nil)), lifeCtx: context.Background(), sem: make(chan struct{}, 4), settings: Settings{Enabled: true, GroupPolicy: "disabled", AllowedNumbers: []string{"100"}}, client: whatsmeow.NewClient(&wastore.Device{ID: &self}, nil)}
	chat := types.NewJID("200", types.DefaultUserServer)
	live := &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: chat, Sender: chat}, ID: "live", Timestamp: time.Now()}, Message: &waE2E.Message{Conversation: new("upgrade php live")}}
	c.handleEvent(live)
	group := types.NewJID("300", types.GroupServer)
	groupMessage := &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: group, Sender: chat, IsGroup: true}, ID: "group", Timestamp: time.Now()}, Message: &waE2E.Message{Conversation: new("upgrade php group")}}
	c.handleEvent(groupMessage)
	page, err := st.SearchWhatsAppMessages(context.Background(), store.WhatsAppSearch{Query: "upgrade php", Limit: 50})
	if err != nil || len(page.Messages) != 2 {
		t.Fatalf("passive message: %#v %v", page, err)
	}
	history := &events.HistorySync{Data: &waHistorySync.HistorySync{Conversations: []*waHistorySync.Conversation{{ID: new(chat.String()), Messages: []*waHistorySync.HistorySyncMsg{{Message: &waWeb.WebMessageInfo{Key: &waCommon.MessageKey{ID: new("old")}, Message: &waE2E.Message{Conversation: new("upgrade php history")}, MessageTimestamp: proto.Uint64(100)}}}}}}}
	c.indexHistory(history)
	c.indexHistory(history)
	page, err = st.SearchWhatsAppMessages(context.Background(), store.WhatsAppSearch{Query: "upgrade php", Limit: 50})
	if err != nil || len(page.Messages) != 3 {
		t.Fatalf("history: %#v %v", page, err)
	}
	deadline := time.Now().Add(time.Second)
	for len(c.sem) > 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(c.sem) > 0 {
		t.Fatal("live handlers did not finish")
	}
}
