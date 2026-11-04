package whatsapp

import (
	"context"
	"github.com/jahrulnr/nusamation/internal/security"
	"github.com/jahrulnr/nusamation/internal/store"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
	"path/filepath"
	"testing"
)

func TestBuildOutbound(t *testing.T) {
	lat, lon := -6.2, 106.8
	cases := []struct {
		name  string
		req   OutboundRequest
		check func() bool
	}{
		{name: "reply", req: OutboundRequest{Type: "reply", Text: "hello", MessageID: "m1", SenderID: "123@s.whatsapp.net", QuotedText: "original"}},
		{name: "location", req: OutboundRequest{Type: "location", Latitude: &lat, Longitude: &lon, Name: "Jakarta"}},
		{name: "contact", req: OutboundRequest{Type: "contact", Name: "Alice", VCard: "BEGIN:VCARD\nVERSION:3.0\nFN:Alice\nEND:VCARD"}},
		{name: "event", req: OutboundRequest{Type: "event", Name: "Meeting", StartTime: 1800000000, EndTime: 1800003600, Latitude: &lat, Longitude: &lon}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg, err := buildOutbound(tc.req)
			if err != nil {
				t.Fatal(err)
			}
			switch tc.name {
			case "reply":
				c := msg.GetExtendedTextMessage().GetContextInfo()
				if c.GetStanzaID() != "m1" || c.GetParticipant() != tc.req.SenderID || c.GetQuotedMessage().GetConversation() != "original" {
					t.Fatal(c)
				}
			case "location":
				if msg.GetLocationMessage().GetDegreesLatitude() != lat {
					t.Fatal(msg)
				}
			case "contact":
				if msg.GetContactMessage().GetVcard() != tc.req.VCard {
					t.Fatal(msg)
				}
			case "event":
				if msg.GetEventMessage().GetStartTime() != tc.req.StartTime || msg.GetEventMessage().GetLocation().GetDegreesLongitude() != lon || len(msg.GetMessageContextInfo().GetMessageSecret()) != 32 {
					t.Fatal(msg)
				}
			}
			if _, err := proto.Marshal(msg); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestBuildOutboundInvalid(t *testing.T) {
	invalidLat, lon := 91.0, 1.0
	for _, req := range []OutboundRequest{{Type: "reply", Text: "x"}, {Type: "location", Latitude: &invalidLat, Longitude: &lon}, {Type: "contact", Name: "Alice"}, {Type: "event", Name: "meeting", StartTime: 2, EndTime: 1}, {Type: "unknown"}} {
		if _, err := buildOutbound(req); err == nil {
			t.Fatalf("accepted %#v", req)
		}
	}
}

func TestGeneralPollOptionsSurviveStoreReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polls.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := security.LoadOrCreateSealer(filepath.Join(filepath.Dir(path), "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	st.SetSealer(sealer)
	jid := types.NewJID("123", types.DefaultUserServer)
	if err := st.SetJSONSetting(context.Background(), generalPollKey(jid.String(), "ID"), []string{"Yes", "No"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st.SetSealer(sealer)
	defer st.Close()
	c := &Channel{store: st}
	options, ok := c.generalPollOptions(context.Background(), jid, "ID")
	if !ok || len(options) != 2 || options[1] != "No" {
		t.Fatalf("options=%v ok=%v", options, ok)
	}
	if _, ok := c.generalPollOptions(context.Background(), jid, "other"); ok {
		t.Fatal("unknown poll matched")
	}
}
