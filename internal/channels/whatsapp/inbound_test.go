package whatsapp

import (
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

func TestPollText(t *testing.T) {
	p := &waE2E.PollCreationMessage{
		Name: new("ini bisa di jawab?"),
		Options: []*waE2E.PollCreationMessage_Option{
			{OptionName: new("Ya")},
			{OptionName: new("Tidak")},
		},
		SelectableOptionsCount: proto.Uint32(1),
	}
	got := pollText(p)
	want := "[Poll] ini bisa di jawab?\n- Ya\n- Tidak"
	if got != want {
		t.Fatalf("single-choice poll text = %q, want %q", got, want)
	}
	p.SelectableOptionsCount = proto.Uint32(0)
	if got := pollText(p); got != want+"\n(multiple answers allowed)" {
		t.Fatalf("unrestricted poll text = %q", got)
	}
	p.SelectableOptionsCount = proto.Uint32(2)
	if got := pollText(p); got != want+"\n(choose up to 2)" {
		t.Fatalf("multi-select poll text = %q", got)
	}
}

func TestPollCreationVariants(t *testing.T) {
	p := &waE2E.PollCreationMessage{Name: new("q")}
	if pollCreation(nil) != nil {
		t.Fatal("nil message should have no poll")
	}
	for i, msg := range []*waE2E.Message{
		{PollCreationMessage: p},
		{PollCreationMessageV2: p},
		{PollCreationMessageV3: p},
		{PollCreationMessageV5: p},
		{PollCreationMessageV6: p},
		{PollCreationMessageV4: &waE2E.FutureProofMessage{Message: &waE2E.Message{PollCreationMessage: p}}},
		{PollCreationOptionImageMessage: &waE2E.FutureProofMessage{Message: &waE2E.Message{PollCreationMessageV3: p}}},
	} {
		if got := pollCreation(msg); got != p {
			t.Fatalf("variant %d not extracted", i)
		}
	}
	if got := pollCreation(&waE2E.Message{Conversation: new("hi")}); got != nil {
		t.Fatalf("plain text resolved as poll: %v", got)
	}
}

func TestVoteOptionNames(t *testing.T) {
	options := []string{"Ya", "Tidak"}
	vote := &waE2E.PollVoteMessage{SelectedOptions: whatsmeow.HashPollOptions([]string{"Tidak"})}
	if got := voteOptionNames(vote, options); len(got) != 1 || got[0] != "Tidak" {
		t.Fatalf("picked = %#v", got)
	}
	vote.SelectedOptions = whatsmeow.HashPollOptions([]string{"Ya", "Tidak"})
	if got := voteOptionNames(vote, options); len(got) != 2 {
		t.Fatalf("multi picked = %#v", got)
	}
	vote.SelectedOptions = whatsmeow.HashPollOptions([]string{"ghost"})
	if got := voteOptionNames(vote, options); len(got) != 0 {
		t.Fatalf("unknown option mapped: %#v", got)
	}
	if got := voteOptionNames(vote, nil); got != nil {
		t.Fatalf("missing poll cache should map nothing, got %#v", got)
	}
}

func TestPollKeysMatchEmbeddedMessageKey(t *testing.T) {
	chat := types.JID{User: "62812", Server: "s.whatsapp.net"}
	stored := pollKeys(chat.String(), "POLLID1")
	// The vote's embedded MessageKey carries the same chat JID + message ID —
	// the lookup must land on a key rememberPoll wrote.
	found := false
	for _, k := range pollKeys((&waCommon.MessageKey{
		RemoteJID: new(chat.String()), ID: new("POLLID1"),
	}).GetRemoteJID(), "POLLID1") {
		for _, s := range stored {
			if s == k {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("embedded MessageKey %v does not resolve under creation-side keys %v", pollKeys(chat.String(), "POLLID1"), stored)
	}
	// Device-suffixed JIDs resolve to the same poll as their non-AD form.
	dev := types.JID{User: "62812", Device: 4, Server: "s.whatsapp.net"}
	if keys := pollKeys(dev.String(), "POLLID1"); len(keys) != 2 || keys[1] != pollKey(chat.String(), "POLLID1") {
		t.Fatalf("device JID keys = %v", keys)
	}
}

func TestPollAddOptionExtendsCache(t *testing.T) {
	c := &Channel{polls: map[string][]string{}}
	info := types.MessageInfo{
		Chat: types.JID{User: "62812", Server: "s.whatsapp.net"},
		ID:   "POLLID2",
	}
	c.rememberPoll(info, &waE2E.PollCreationMessage{
		Name: new("q"),
		Options: []*waE2E.PollCreationMessage_Option{
			{OptionName: new("Ya")},
		},
	})
	mk := &waCommon.MessageKey{RemoteJID: new(info.Chat.String()), ID: new("POLLID2")}
	c.rememberPollOption(mk, "Mungkin")
	vote := &waE2E.PollVoteMessage{SelectedOptions: whatsmeow.HashPollOptions([]string{"Mungkin"})}
	if got := voteOptionNames(vote, c.polls[pollKey(info.Chat.String(), "POLLID2")]); len(got) != 1 || got[0] != "Mungkin" {
		t.Fatalf("added option did not resolve: %#v", got)
	}
	if got := c.pollOptionText(&waE2E.PollAddOptionMessage{
		PollCreationMessageKey: mk,
		AddOption:              &waE2E.PollCreationMessage_Option{OptionName: new("Lainnya")},
	}); got != "[Poll option added: Lainnya]" {
		t.Fatalf("pollOptionText = %q", got)
	}
}

func TestIsPollTraffic(t *testing.T) {
	p := &waE2E.PollCreationMessage{Name: new("q")}
	for i, msg := range []*waE2E.Message{
		{PollCreationMessageV3: p},
		{PollUpdateMessage: &waE2E.PollUpdateMessage{}},
		{PollAddOptionMessage: &waE2E.PollAddOptionMessage{}},
	} {
		if !isPollTraffic(msg) {
			t.Fatalf("poll traffic %d not detected", i)
		}
	}
	if isPollTraffic(&waE2E.Message{Conversation: new("hi")}) {
		t.Fatal("plain text detected as poll traffic")
	}
}

func TestWaTextRichTypes(t *testing.T) {
	cases := []struct {
		name string
		msg  *waE2E.Message
		want string
	}{
		{"live location", &waE2E.Message{LiveLocationMessage: &waE2E.LiveLocationMessage{
			DegreesLatitude: new(-6.2), DegreesLongitude: new(106.8),
		}}, "[Live location: -6.200000, 106.800000]"},
		{"contacts array", &waE2E.Message{ContactsArrayMessage: &waE2E.ContactsArrayMessage{
			Contacts: []*waE2E.ContactMessage{
				{DisplayName: new("A")}, {DisplayName: new("B")},
			},
		}}, "[Contact cards: A, B]"},
		{"event", &waE2E.Message{EventMessage: &waE2E.EventMessage{
			Name: new("Rapat"), StartTime: proto.Int64(1730000000),
		}}, "[Event: Rapat — " + time.Unix(1730000000, 0).Format("2006-01-02 15:04") + "]"},
		{"group invite", &waE2E.Message{GroupInviteMessage: &waE2E.GroupInviteMessage{
			GroupName: new("Tim Inti"), InviteCode: new("xyz"),
		}}, "[Group invite: Tim Inti]"},
		{"flow response", &waE2E.Message{InteractiveResponseMessage: &waE2E.InteractiveResponseMessage{
			InteractiveResponseMessage: &waE2E.InteractiveResponseMessage_NativeFlowResponseMessage_{
				NativeFlowResponseMessage: &waE2E.InteractiveResponseMessage_NativeFlowResponseMessage{
					ParamsJSON: new(`{"a":"b"}`),
				},
			},
		}}, `[Form response: {"a":"b"}]`},
		{"order", &waE2E.Message{OrderMessage: &waE2E.OrderMessage{
			OrderTitle: new("Kopi"), ItemCount: proto.Int32(3),
		}}, "[Order: Kopi — 3 item(s)]"},
		{"product", &waE2E.Message{ProductMessage: &waE2E.ProductMessage{
			Body: new("Sepatu lari"),
		}}, "[Product: Sepatu lari]"},
	}
	for _, tc := range cases {
		if got := waText(tc.msg); got != tc.want {
			t.Fatalf("%s: waText = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestQuoteContext(t *testing.T) {
	msg := &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
		Text: new("Ya"),
		ContextInfo: &waE2E.ContextInfo{
			QuotedMessage: &waE2E.Message{Conversation: new("ini bisa di jawab?")},
		},
	}}
	if got := quoteContext(msg); got != "[In reply to: ini bisa di jawab?]" {
		t.Fatalf("text quote = %q", got)
	}
	img := &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
		Text: new("bagus"),
		ContextInfo: &waE2E.ContextInfo{
			QuotedMessage: &waE2E.Message{ImageMessage: &waE2E.ImageMessage{}},
		},
	}}
	if got := quoteContext(img); got != "[In reply to: an image]" {
		t.Fatalf("image quote = %q", got)
	}
	// A sticker carrying a quote (sticker replies now surface their context).
	sticker := &waE2E.Message{StickerMessage: &waE2E.StickerMessage{
		ContextInfo: &waE2E.ContextInfo{
			QuotedMessage: &waE2E.Message{Conversation: new("besok jadi?")},
		},
	}}
	if got := quoteContext(sticker); got != "[In reply to: besok jadi?]" {
		t.Fatalf("sticker quote = %q", got)
	}
	// A quote wrapped in view-once unwraps before labeling.
	vo := &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
		Text: new("ok"),
		ContextInfo: &waE2E.ContextInfo{
			QuotedMessage: &waE2E.Message{ViewOnceMessage: &waE2E.FutureProofMessage{
				Message: &waE2E.Message{ImageMessage: &waE2E.ImageMessage{}},
			}},
		},
	}}
	if got := quoteContext(vo); got != "[In reply to: an image]" {
		t.Fatalf("view-once quote = %q", got)
	}
	// A poll without caption-like text falls back to its kind label.
	pq := &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
		Text: new("Ya"),
		ContextInfo: &waE2E.ContextInfo{
			QuotedMessage: &waE2E.Message{PollCreationMessage: &waE2E.PollCreationMessage{}},
		},
	}}
	if got := quoteContext(pq); got != "[In reply to: a poll]" {
		t.Fatalf("poll quote = %q", got)
	}
	if got := quoteContext(&waE2E.Message{Conversation: new("hi")}); got != "" {
		t.Fatalf("plain message quote = %q", got)
	}
}

func TestQuoteMediaWithoutCaption(t *testing.T) {
	for _, tc := range []struct {
		name   string
		quoted *waE2E.Message
		want   string
	}{
		{"image", &waE2E.Message{ImageMessage: &waE2E.ImageMessage{}}, "an image"},
		{"video", &waE2E.Message{VideoMessage: &waE2E.VideoMessage{}}, "a video"},
		{"document", &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{}}, "a document"},
		{"audio", &waE2E.Message{AudioMessage: &waE2E.AudioMessage{}}, "a voice message"},
		{"sticker", &waE2E.Message{StickerMessage: &waE2E.StickerMessage{}}, "a sticker"},
		{"video note", &waE2E.Message{PtvMessage: &waE2E.VideoMessage{}}, "a video note"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &waE2E.ContextInfo{QuotedMessage: tc.quoted}
			// A reply may itself contain only an attachment.
			reply := &waE2E.Message{ImageMessage: &waE2E.ImageMessage{ContextInfo: ctx}}
			if got, want := quoteContext(reply), "[In reply to: "+tc.want+"]"; got != want {
				t.Fatalf("quote = %q, want %q", got, want)
			}
		})
	}
}
