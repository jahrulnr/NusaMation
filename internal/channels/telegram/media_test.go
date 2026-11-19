package telegram

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jahrulnr/nusamation/internal/channels"
)

// inboundSink is a handler whose capture is safe to read while the album
// timer goroutine delivers.
type inboundSink struct {
	mu  sync.Mutex
	got []channels.Inbound
}

func (s *inboundSink) HandleInbound(_ context.Context, in channels.Inbound) error {
	s.mu.Lock()
	s.got = append(s.got, in)
	s.mu.Unlock()
	return nil
}

func (s *inboundSink) snapshot() []channels.Inbound {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]channels.Inbound, len(s.got))
	copy(out, s.got)
	return out
}

func TestMediaMethodMapping(t *testing.T) {
	cases := []struct {
		mime   string
		size   int
		method string
		field  string
	}{
		{"image/gif", 1024, "sendAnimation", "animation"},
		{"image/png", photoLimitBytes, "sendPhoto", "photo"},
		{"image/png", photoLimitBytes + 1, "sendDocument", "document"},
		{"video/mp4", 1024, "sendVideo", "video"},
		{"audio/ogg", 1024, "sendVoice", "voice"},
		{"audio/mpeg", 1024, "sendAudio", "audio"},
		{"application/pdf", 1024, "sendDocument", "document"},
	}
	for _, tc := range cases {
		method, field := mediaMethod(tc.mime, tc.size)
		if method != tc.method || field != tc.field {
			t.Fatalf("mediaMethod(%q, %d) = %s/%s, want %s/%s",
				tc.mime, tc.size, method, field, tc.method, tc.field)
		}
	}
}

func TestSendTextRetriesWithoutParseMode(t *testing.T) {
	st := testStore(t)
	f := newFakeBotAPI(t)
	ch, _ := newTestChannel(t, st, f)
	attachClient(ch, f)
	f.mu.Lock()
	f.parseRejects = 1
	f.mu.Unlock()

	if _, err := ch.sendText(context.Background(), 111, 0, "<b>hello</b>"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) != 2 {
		t.Fatalf("sent %d messages, want a parsed attempt plus a plain retry", len(f.sent))
	}
	if f.sent[0]["parse_mode"] != "HTML" {
		t.Fatalf("first attempt was not HTML: %#v", f.sent[0])
	}
	if f.sent[0]["text"] != "<b>hello</b>" {
		t.Fatalf("first attempt text = %q", f.sent[0]["text"])
	}
	if _, ok := f.sent[1]["parse_mode"]; ok {
		t.Fatal("retry kept parse_mode")
	}
	if f.sent[1]["text"] != "<b>hello</b>" {
		t.Fatalf("retry did not send the original text: %q", f.sent[1]["text"])
	}
}

func TestSendMediaPicksPhotoOrDocumentAtLimit(t *testing.T) {
	for _, tc := range []struct {
		name string
		size int
		want string
	}{
		{"at limit", photoLimitBytes, "sendPhoto"},
		{"over limit", photoLimitBytes + 1, "sendDocument"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := testStore(t)
			f := newFakeBotAPI(t)
			ch, _ := newTestChannel(t, st, f)
			attachClient(ch, f)

			data := make([]byte, tc.size)
			copy(data, "\x89PNG\r\n\x1a\n")
			err := ch.SendMedia(context.Background(),
				channels.Target{Channel: "telegram", ExternalID: "111", ThreadID: "42"},
				channels.OutboundMedia{Name: "shot.png", MIME: "image/png", Caption: "<b>pic</b>", Data: data})
			if err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.media) != 1 {
				t.Fatalf("sent %d media calls, want one", len(f.media))
			}
			call := f.media[0]
			if call.Method != tc.want {
				t.Fatalf("method = %s, want %s", call.Method, tc.want)
			}
			if call.ChatID != "111" || call.ThreadID != "42" {
				t.Fatalf("routing lost: chat=%q thread=%q", call.ChatID, call.ThreadID)
			}
			if call.Caption != "<b>pic</b>" || call.ParseMode != "HTML" {
				t.Fatalf("caption = %q parse_mode = %q", call.Caption, call.ParseMode)
			}
			if call.FileName != "shot.png" || call.FileMIME != "image/png" || call.Size != tc.size {
				t.Fatalf("file part = %+v", call)
			}
		})
	}
}

func TestSendMediaCaptionFallbackKeepsTheFile(t *testing.T) {
	st := testStore(t)
	f := newFakeBotAPI(t)
	ch, _ := newTestChannel(t, st, f)
	attachClient(ch, f)
	f.mu.Lock()
	f.parseRejects = 1
	f.mu.Unlock()

	data := []byte("\x89PNG\r\n\x1a\nsmall")
	err := ch.SendMedia(context.Background(),
		channels.Target{Channel: "telegram", ExternalID: "111"},
		channels.OutboundMedia{Name: "pic.png", MIME: "image/png", Caption: "<b>pic</b>", Data: data})
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.media) != 2 {
		t.Fatalf("media calls = %d, want parsed attempt plus plain retry", len(f.media))
	}
	if f.media[0].ParseMode != "HTML" || f.media[0].Caption != "<b>pic</b>" {
		t.Fatalf("first media call = %+v", f.media[0])
	}
	if f.media[1].ParseMode != "" || f.media[1].Caption != "<b>pic</b>" {
		t.Fatalf("retry caption was not sent unparsed: %+v", f.media[1])
	}
	if f.media[1].Size != len(data) {
		t.Fatalf("retry lost the file: size %d", f.media[1].Size)
	}
}

func TestInboundPhotoUsesLargestSize(t *testing.T) {
	st := testStore(t)
	f := newFakeBotAPI(t)
	f.fileBytes = []byte("JPEGDATA")
	ch, got := newTestChannel(t, st, f)
	attachClient(ch, f)

	m := textMsg(31, 111, 111, "")
	m.Text = ""
	m.Caption = "look at this"
	m.Photo = []tgPhotoSize{
		{FileID: "small", FileSize: 3},
		{FileID: "large", FileSize: int64(len(f.fileBytes))},
	}
	ch.handleMessage(context.Background(), m)
	waitFor(t, "photo inbound", func() bool { return len(*got) == 1 })

	in := (*got)[0]
	if len(in.Attachments) != 1 {
		t.Fatalf("attachments = %d, want one", len(in.Attachments))
	}
	att := in.Attachments[0]
	if string(att.Data) != "JPEGDATA" || att.MIME != "image/jpeg" || att.Name != "photo-31.jpg" {
		t.Fatalf("attachment = %+v", att)
	}
	if in.Text != "look at this" {
		t.Fatalf("caption text = %q", in.Text)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.getFileIDs) != 1 || f.getFileIDs[0] != "large" {
		t.Fatalf("getFile ids = %#v, want the largest rendition", f.getFileIDs)
	}
}

func TestInboundDocumentKeepsTelegramFileName(t *testing.T) {
	st := testStore(t)
	f := newFakeBotAPI(t)
	f.fileBytes = []byte("%PDF-1.4")
	ch, got := newTestChannel(t, st, f)
	attachClient(ch, f)

	m := textMsg(32, 111, 111, "")
	m.Document = &tgMedia{FileID: "doc-1", FileName: "report.pdf", MIME: "application/pdf"}
	ch.handleMessage(context.Background(), m)
	waitFor(t, "document inbound", func() bool { return len(*got) == 1 })

	att := (*got)[0].Attachments[0]
	if att.Name != "report.pdf" || att.MIME != "application/pdf" {
		t.Fatalf("attachment = %+v", att)
	}
}

func TestInboundFileOver20MBSkipsGetFile(t *testing.T) {
	st := testStore(t)
	f := newFakeBotAPI(t)
	ch, got := newTestChannel(t, st, f)
	attachClient(ch, f)

	m := textMsg(33, 111, 111, "")
	m.Text = ""
	m.Document = &tgMedia{FileID: "huge", FileName: "huge.zip", FileSize: downloadLimitBytes + 1}
	ch.handleMessage(context.Background(), m)
	waitFor(t, "oversize inbound", func() bool { return len(*got) == 1 })

	in := (*got)[0]
	if len(in.Attachments) != 0 {
		t.Fatalf("oversized file produced %d attachments", len(in.Attachments))
	}
	if !strings.Contains(in.Text, "[File is larger than 20 MB and was not downloaded]") {
		t.Fatalf("text = %q, want the 20 MB marker", in.Text)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getFileCalls != 0 {
		t.Fatalf("getFile was called %d times for an oversized file", f.getFileCalls)
	}
}

func TestAlbumMergesIntoOneInbound(t *testing.T) {
	st := testStore(t)
	f := newFakeBotAPI(t)
	f.fileBytes = []byte("PIC")
	ch, _ := newTestChannel(t, st, f)
	sink := &inboundSink{}
	ch.SetHandler(sink)
	attachClient(ch, f)

	first := textMsg(41, 111, 111, "")
	first.Text = ""
	first.MediaGroupID = "group-1"
	first.Photo = []tgPhotoSize{{FileID: "a", FileSize: int64(len(f.fileBytes))}}
	second := textMsg(42, 111, 111, "")
	second.Text = ""
	second.Caption = "two"
	second.MediaGroupID = "group-1"
	second.Photo = []tgPhotoSize{{FileID: "b", FileSize: int64(len(f.fileBytes))}}

	ch.handleMessage(context.Background(), first)
	ch.handleMessage(context.Background(), second)
	waitFor(t, "album inbound", func() bool { return len(sink.snapshot()) == 1 })
	time.Sleep(2 * albumDebounce) // a late flush would add a second inbound

	if got := sink.snapshot(); len(got) != 1 {
		t.Fatalf("album produced %d inbounds, want one", len(got))
	}
	in := sink.snapshot()[0]
	if len(in.Attachments) != 2 {
		t.Fatalf("attachments = %d, want two", len(in.Attachments))
	}
	if in.Attachments[0].Name != "photo-41.jpg" || in.Attachments[1].Name != "photo-42.jpg" {
		t.Fatalf("album order lost: %#v", in.Attachments)
	}
	if in.MessageID != "111:41" {
		t.Fatalf("MessageID = %q, want the first item id", in.MessageID)
	}
	if !strings.Contains(in.Text, "two") {
		t.Fatalf("album captions were dropped: %q", in.Text)
	}
}

func TestDisconnectFlushesBufferedAlbum(t *testing.T) {
	st := testStore(t)
	f := newFakeBotAPI(t)
	f.fileBytes = []byte("PIC")
	ch, got := newTestChannel(t, st, f)
	attachClient(ch, f)

	m := textMsg(51, 111, 111, "")
	m.Text = ""
	m.MediaGroupID = "group-2"
	m.Photo = []tgPhotoSize{{FileID: "c", FileSize: int64(len(f.fileBytes))}}
	ch.handleMessage(context.Background(), m)
	if len(*got) != 0 {
		t.Fatal("album was dispatched before its debounce")
	}
	if err := ch.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 {
		t.Fatalf("disconnect delivered %d inbounds, want one", len(*got))
	}
}

func TestDownloadErrorsNeverLeakTheToken(t *testing.T) {
	srv := httptest.NewServer(nil)
	base := srv.URL
	srv.Close()

	client := newAPIClient("123456:secret-token", base)
	_, err := client.downloadFile(context.Background(), "media/file.bin", 10)
	if err == nil {
		t.Fatal("expected the download to fail")
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("download error leaked the token: %v", err)
	}
	if !strings.Contains(err.Error(), "<token>") {
		t.Fatalf("download error was not redacted: %v", err)
	}
}
