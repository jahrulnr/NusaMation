package whatsapp

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"

	"github.com/jahrulnr/nusamation/internal/security"
	"github.com/jahrulnr/nusamation/internal/store"
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

func TestMediaFromRequest(t *testing.T) {
	media, err := mediaFromRequest(OutboundRequest{Type: "image", Path: "/tmp/shot.png", Caption: " hi ", Name: "shot.png"})
	if err != nil || media.Path != "/tmp/shot.png" || media.Caption != "hi" || media.Name != "shot.png" || len(media.Data) != 0 {
		t.Fatalf("path request: %#v err=%v", media, err)
	}
	media, err = mediaFromRequest(OutboundRequest{Type: "document", Data: "aGVsbG8=", MIME: "application/pdf"})
	if err != nil || string(media.Data) != "hello" || media.MIME != "application/pdf" || media.Path != "" {
		t.Fatalf("data request: %#v err=%v", media, err)
	}
	for _, req := range []OutboundRequest{
		{Type: "image"},                         // neither path nor data
		{Type: "image", Data: "not base64!"},    // invalid payload
		{Type: "file", Path: " ", Data: "\t\n"}, // blank both
	} {
		if _, err := mediaFromRequest(req); err == nil {
			t.Errorf("accepted %#v", req)
		}
	}
	for _, typ := range []string{"image", "video", "audio", "document", "file"} {
		if !isMediaType(typ) {
			t.Errorf("%q should be a media type", typ)
		}
	}
	for _, typ := range []string{"reply", "sticker", "poll", "reaction", "text", ""} {
		if isMediaType(typ) {
			t.Errorf("%q should not be a media type", typ)
		}
	}
}

// fakeMediaClient stands in for *whatsmeow.Client on the media path, so the
// upload/send contract is testable without a WhatsApp connection.
type fakeMediaClient struct {
	uploadData []byte
	uploadType whatsmeow.MediaType
	to         types.JID
	sent       *waE2E.Message
	id         types.MessageID
}

func (f *fakeMediaClient) Upload(_ context.Context, data []byte, mediaType whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
	f.uploadData, f.uploadType = data, mediaType
	return whatsmeow.UploadResponse{URL: "https://mms.example/x", DirectPath: "/x", FileLength: uint64(len(data))}, nil
}

func (f *fakeMediaClient) SendMessage(_ context.Context, to types.JID, msg *waE2E.Message, _ ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error) {
	f.to, f.sent = to, msg
	return whatsmeow.SendResponse{ID: f.id}, nil
}

func TestMediaMessageShapes(t *testing.T) {
	up := whatsmeow.UploadResponse{URL: "https://mms.example/x", DirectPath: "/x", FileLength: 12}
	img := mediaMessage(up, whatsmeow.MediaImage, "image/png", "Screenshot", "shot.png").GetImageMessage()
	if img.GetMimetype() != "image/png" || img.GetCaption() != "Screenshot" || img.GetFileLength() != 12 {
		t.Fatalf("image message = %v", img)
	}
	doc := mediaMessage(up, whatsmeow.MediaDocument, "application/pdf", "", "report.pdf").GetDocumentMessage()
	if doc.GetFileName() != "report.pdf" || doc.GetMimetype() != "application/pdf" || doc.GetCaption() != "" {
		t.Fatalf("document message = %v", doc)
	}
	if got := mediaMessage(up, whatsmeow.MediaDocument, "application/octet-stream", "", "").GetDocumentMessage().GetFileName(); got != "file" {
		t.Fatalf("unnamed document filename = %q", got)
	}
	audio := mediaMessage(up, whatsmeow.MediaAudio, "audio/ogg", "", "voice.ogg").GetAudioMessage()
	if !audio.GetPTT() {
		t.Fatal("ogg audio should render as a voice note")
	}
	if mediaMessage(up, whatsmeow.MediaAudio, "audio/mpeg", "", "song.mp3").GetAudioMessage().GetPTT() {
		t.Fatal("mp3 audio must not be push-to-talk")
	}
}

// A PNG from disk with a spaced filename must reach SendMessage as a real
// ImageMessage. This is the regression for "agent writes an image path, no
// file is delivered, and the CLI falls back to a sticker".
func TestDeliverMediaPhotoWithoutWhatsApp(t *testing.T) {
	dir := t.TempDir()
	shot := filepath.Join(dir, "shot 1.png")
	// A real PNG header is enough for http.DetectContentType.
	data := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	if err := os.WriteFile(shot, data, 0o600); err != nil {
		t.Fatal(err)
	}
	c := &Channel{settings: Settings{Enabled: true}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	fake := &fakeMediaClient{id: "WA_ID"}
	mime := http.DetectContentType(data)
	if mime != "image/png" {
		t.Fatalf("detected mime = %q", mime)
	}
	jid := types.NewJID("123", types.DefaultUserServer)
	fileData, err := os.ReadFile(shot)
	if err != nil {
		t.Fatal(err)
	}
	id, err := c.deliverMedia(context.Background(), jid, fake, fileData, mediaTypeFor(mime), mime, "Screenshot", filepath.Base(shot))
	if err != nil {
		t.Fatal(err)
	}
	if id != "WA_ID" || fake.to != jid || fake.uploadType != whatsmeow.MediaImage {
		t.Fatalf("id=%q to=%v upload=%v", id, fake.to, fake.uploadType)
	}
	img := fake.sent.GetImageMessage()
	if img == nil || img.GetCaption() != "Screenshot" || string(fake.uploadData) != string(data) {
		t.Fatalf("sent %v upload=%q", img, fake.uploadData)
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
