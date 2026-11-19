package whatsapp

import (
	"context"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"

	"github.com/jahrulnr/nusamation/internal/store"
)

// A message this device sends into a group whose name is already known must be
// indexed with that name. Today sent messages are always stored with an empty
// chat name, so search results for them show no group.
func TestSentGroupMessageKeepsGroupName(t *testing.T) {
	c, st := newGateChannel(t)
	c.groupCache = map[string]groupEntry{}
	group := types.JID{User: "1203630", Server: types.GroupServer}
	c.groupCache[group.String()] = groupEntry{name: "Keluarga", at: time.Now()}
	ctx := context.Background()
	c.indexSent(ctx, group, whatsmeow.SendResponse{ID: "out1", Timestamp: time.Now()}, &waE2E.Message{Conversation: new("halo")}, "")
	page, err := st.SearchWhatsAppMessages(ctx, store.WhatsAppSearch{Query: "halo", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].ChatName != "Keluarga" {
		t.Fatalf("indexed sent message = %#v, want chat name Keluarga", page.Messages)
	}
}
