package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jahrulnr/nusamation/internal/channels"
	"github.com/jahrulnr/nusamation/internal/security"
	appstore "github.com/jahrulnr/nusamation/internal/store"
)

// fakeBotAPI is an httptest stand-in for api.telegram.org. It records every
// call and serves canned responses — no test may dial the real endpoint.
type fakeBotAPI struct {
	srv *httptest.Server

	mu         sync.Mutex
	me         tgUser
	updates    []tgUpdate
	updateSeq  int // consumed updates counter for scripted batches
	cmds       []botCommand
	scopes     []map[string]any
	cmdCalls   int
	deleted    []map[string]any
	answers    []inlineAnswer
	allowed    string
	sent       []map[string]any
	offsets    []int64
	failGetMe  bool
	failDelete bool
	getMeError map[string]any

	// Media surface: uploads are recorded, getFile is counted, and the file
	// endpoint serves fileBytes for every path.
	media        []mediaCall
	parseRejects int
	getFileCalls int
	getFileIDs   []string
	fileBytes    []byte
	downloads    int

	// Ownership, rate-limit and typing instrumentation.
	getMeTokens       []string
	pollTokens        []string
	send429           int
	poll429           int
	chatActions       int
	chatActionChats   []string
	chatActionThreads []string
}

// mediaCall is one recorded multipart send — enough to assert method choice,
// thread routing and caption formatting without a real Telegram server.
type mediaCall struct {
	Method    string
	ChatID    string
	ThreadID  string
	Caption   string
	ParseMode string
	FileName  string
	FileMIME  string
	Size      int
}

func isMediaMethod(method string) bool {
	switch method {
	case "sendPhoto", "sendDocument", "sendVideo", "sendVoice", "sendAudio", "sendAnimation":
		return true
	}
	return false
}

type inlineAnswer struct {
	ID      string
	Results string
	Body    map[string]any
}

func newFakeBotAPI(t *testing.T) *fakeBotAPI {
	t.Helper()
	f := &fakeBotAPI{me: tgUser{ID: 4242, IsBot: true, FirstName: "NusaBot", Username: "nusa_bot"}}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		reply := func(v any) {
			b, _ := json.Marshal(map[string]any{"ok": true, "result": v})
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(b)
		}
		fail := func(code int, desc string) {
			b, _ := json.Marshal(map[string]any{"ok": false, "error_code": code, "description": desc})
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(b)
		}
		rateLimit := func() {
			b, _ := json.Marshal(map[string]any{
				"ok": false, "error_code": 429, "description": "Too Many Requests: retry after 1",
				"parameters": map[string]any{"retry_after": 1},
			})
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(b)
		}
		// /file/bot<token>/<path> serves the canned download bytes.
		if strings.HasPrefix(r.URL.Path, "/file/") {
			f.mu.Lock()
			data := f.fileBytes
			f.downloads++
			f.mu.Unlock()
			_, _ = w.Write(data)
			return
		}
		// /bot<token>/<method> — the method is the last path element and the
		// token the segment after "bot".
		rest := strings.TrimPrefix(r.URL.Path, "/")
		token := ""
		if strings.HasPrefix(rest, "bot") {
			if i := strings.LastIndexByte(rest, '/'); i > len("bot") {
				token = rest[len("bot"):i]
			}
		}
		method := r.URL.Path[strings.LastIndexByte(r.URL.Path, '/')+1:]
		if isMediaMethod(method) {
			_ = r.ParseMultipartForm(64 << 20)
			call := mediaCall{
				Method: method, ChatID: r.FormValue("chat_id"),
				ThreadID: r.FormValue("message_thread_id"), Caption: r.FormValue("caption"),
				ParseMode: r.FormValue("parse_mode"),
			}
			for _, field := range []string{"photo", "document", "video", "voice", "audio", "animation"} {
				file, header, err := r.FormFile(field)
				if err != nil {
					continue
				}
				data, _ := io.ReadAll(file)
				_ = file.Close()
				call.FileName = header.Filename
				call.FileMIME = header.Header.Get("Content-Type")
				call.Size = len(data)
				break
			}
			f.mu.Lock()
			f.media = append(f.media, call)
			reject := f.parseRejects > 0 && call.ParseMode != ""
			if reject {
				f.parseRejects--
			}
			limited := f.send429 > 0
			if limited {
				f.send429--
			}
			n := len(f.media)
			f.mu.Unlock()
			if limited {
				rateLimit()
				return
			}
			if reject {
				fail(400, "Bad Request: can't parse entities: unsupported start tag")
				return
			}
			reply(tgMessage{MessageID: int64(n + 200)})
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		switch method {
		case "getMe":
			f.getMeTokens = append(f.getMeTokens, token)
			if f.failGetMe {
				fail(401, "Unauthorized")
				return
			}
			reply(f.me)
		case "getUpdates":
			f.pollTokens = append(f.pollTokens, token)
			if f.poll429 > 0 {
				f.poll429--
				rateLimit()
				return
			}
			if raw, ok := body["allowed_updates"]; ok {
				b, _ := json.Marshal(raw)
				f.allowed = string(b)
			}
			f.offsets = append(f.offsets, int64(body["offset"].(float64)))
			var batch []tgUpdate
			if f.updateSeq < len(f.updates) {
				batch = f.updates[f.updateSeq:]
				f.updateSeq = len(f.updates)
			}
			reply(batch)
		case "sendMessage":
			if f.send429 > 0 {
				f.send429--
				rateLimit()
				return
			}
			f.sent = append(f.sent, body)
			if f.parseRejects > 0 {
				if _, parsed := body["parse_mode"]; parsed {
					f.parseRejects--
					fail(400, "Bad Request: can't parse entities: unsupported start tag")
					return
				}
			}
			reply(tgMessage{MessageID: int64(len(f.sent) + 100)})
		case "sendChatAction":
			f.chatActions++
			f.chatActionChats = append(f.chatActionChats, fmt.Sprint(body["chat_id"]))
			if v, ok := body["message_thread_id"]; ok {
				f.chatActionThreads = append(f.chatActionThreads, fmt.Sprint(v))
			} else {
				f.chatActionThreads = append(f.chatActionThreads, "")
			}
			reply(true)
		case "getFile":
			f.getFileCalls++
			id := fmt.Sprint(body["file_id"])
			f.getFileIDs = append(f.getFileIDs, id)
			reply(tgFile{FileID: id, FilePath: "media/" + id, FileSize: int64(len(f.fileBytes))})
		case "setMyCommands":
			f.cmdCalls++
			var cmds []botCommand
			raw, _ := json.Marshal(body["commands"])
			_ = json.Unmarshal(raw, &cmds)
			f.cmds = cmds
			var scope map[string]any
			raw, _ = json.Marshal(body["scope"])
			_ = json.Unmarshal(raw, &scope)
			f.scopes = append(f.scopes, scope)
			reply(true)
		case "deleteMyCommands":
			var scope map[string]any
			raw, _ := json.Marshal(body["scope"])
			_ = json.Unmarshal(raw, &scope)
			f.deleted = append(f.deleted, scope)
			if f.failDelete {
				fail(400, "delete refused")
				return
			}
			reply(true)
		case "answerInlineQuery":
			raw, _ := json.Marshal(body["results"])
			copied := map[string]any{}
			for k, v := range body {
				copied[k] = v
			}
			f.answers = append(f.answers, inlineAnswer{
				ID: fmt.Sprint(body["inline_query_id"]), Results: string(raw), Body: copied,
			})
			reply(true)
		default:
			fail(404, "method not found")
		}
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeBotAPI) sentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func (f *fakeBotAPI) lastOffset() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.offsets) == 0 {
		return -1
	}
	return f.offsets[len(f.offsets)-1]
}

func testStore(t *testing.T) *appstore.Store {
	t.Helper()
	dir := t.TempDir()
	st, err := appstore.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	sealer, err := security.LoadOrCreateSealer(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	st.SetSealer(sealer)
	return st
}

// newTestChannel wires a channel to the fake API with a captured inbound
// handler, an allowed user id, and a saved (unvalidated) token.
func newTestChannel(t *testing.T, st *appstore.Store, f *fakeBotAPI) (*Channel, *[]channels.Inbound) {
	t.Helper()
	got := &[]channels.Inbound{}
	ch := New(st, nil, nil)
	ch.apiBase = f.srv.URL
	ch.SetHandler(handlerFunc(func(ctx context.Context, in channels.Inbound) error {
		*got = append(*got, in)
		return nil
	}))
	ch.settings = Settings{
		Enabled: true, AutoStart: false,
		AllowedUserIDs: []string{"111"}, AllowedGroupIDs: []string{"-900"},
		GroupPolicy: "mention",
	}
	return ch, got
}

type handlerFunc func(context.Context, channels.Inbound) error

func (h handlerFunc) HandleInbound(ctx context.Context, in channels.Inbound) error {
	return h(ctx, in)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func textMsg(id, chatID, senderID int64, text string) *tgMessage {
	return &tgMessage{
		MessageID: id,
		Chat:      tgChat{ID: chatID, Type: "private"},
		From:      &tgUser{ID: senderID, FirstName: "Ana", Username: "ana"},
		Date:      time.Now().Unix(),
		Text:      text,
	}
}

func saveTokenDirect(t *testing.T, ch *Channel, st *appstore.Store) {
	t.Helper()
	if err := ch.saveToken(context.Background(), "test-token"); err != nil {
		t.Fatal(err)
	}
	ch.mu.Lock()
	ch.tokenPresent = true
	ch.mu.Unlock()
}

func TestMenuCommandsPublishedOnceOnConnect(t *testing.T) {
	st := testStore(t)
	f := newFakeBotAPI(t)
	ch, _ := newTestChannel(t, st, f)
	saveTokenDirect(t, ch, st)
	if err := ch.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "setMyCommands", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.cmdCalls > 0 && strings.Contains(f.allowed, "inline_query")
	})
	ch.Disconnect()
	f.mu.Lock()
	defer f.mu.Unlock()
	want := []string{"start", "help", "new", "agent", "model", "mode", "set", "status", "stop", "retry", "approve", "deny"}
	if len(f.cmds) != len(want) {
		t.Fatalf("menu has %d commands, want %d", len(f.cmds), len(want))
	}
	for i, name := range want {
		if f.cmds[i].Command != name {
			t.Fatalf("command %d = %q, want %q", i, f.cmds[i].Command, name)
		}
		if f.cmds[i].Description == "" || len(f.cmds[i].Description) > 256 {
			t.Fatalf("description %d invalid length", i)
		}
	}
	if f.cmdCalls != 1 {
		t.Fatalf("setMyCommands called %d times, want once", f.cmdCalls)
	}
	if len(f.scopes) != 1 || f.scopes[0]["type"] != "default" {
		t.Fatalf("default menu scope = %#v", f.scopes)
	}
	if !strings.Contains(f.allowed, "inline_query") || !strings.Contains(f.allowed, "message") {
		t.Fatalf("allowed_updates = %s", f.allowed)
	}
}

func TestEmptyAllowlistRejects(t *testing.T) {
	st := testStore(t)
	f := newFakeBotAPI(t)
	got := &[]channels.Inbound{}
	ch := New(st, nil, nil)
	ch.apiBase = f.srv.URL
	ch.SetHandler(handlerFunc(func(ctx context.Context, in channels.Inbound) error {
		*got = append(*got, in)
		return nil
	}))
	// Default settings: enabled, empty allowlists — deny everyone.
	if ch.gateInbound(textMsg(1, 555, 777, "hello")) {
		t.Fatal("empty allowlist allowed a message")
	}
	if len(*got) != 0 {
		t.Fatal("rejected message reached the handler")
	}
}

func TestWildcardMixedWithIDsRejected(t *testing.T) {
	st := testStore(t)
	f := newFakeBotAPI(t)
	ch, _ := newTestChannel(t, st, f)
	before := ch.settingsSnapshot()
	err := ch.UpdateSettings(context.Background(), json.RawMessage(`{"allowedUserIds":["*","111"]}`))
	if err == nil {
		t.Fatal("mixed wildcard+id allowlist was accepted")
	}
	after := ch.settingsSnapshot()
	if strings.Join(after.AllowedUserIDs, ",") != strings.Join(before.AllowedUserIDs, ",") {
		t.Fatalf("rejected patch mutated the live allowlist: %#v", after.AllowedUserIDs)
	}
	// A lone wildcard is valid and allows every sender (group policy aside).
	if err := ch.UpdateSettings(context.Background(), json.RawMessage(`{"allowedUserIds":["*"],"allowedGroupIds":[]}`)); err != nil {
		t.Fatal(err)
	}
	if !ch.settingsSnapshot().allows("999", "555") {
		t.Fatal("lone wildcard did not allow a sender")
	}
}

func TestOffsetAdvancesAndPersists(t *testing.T) {
	st := testStore(t)
	f := newFakeBotAPI(t)
	ch, _ := newTestChannel(t, st, f)
	saveTokenDirect(t, ch, st)
	f.updates = []tgUpdate{
		{UpdateID: 5, Message: textMsg(1, 111, 111, "hi")},
		{UpdateID: 6, Message: textMsg(2, 111, 111, "again")},
	}
	if err := ch.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "second poll with offset 7", func() bool { return f.lastOffset() >= 7 })
	if got := ch.loadOffset(context.Background()); got != 7 {
		t.Fatalf("persisted offset = %d, want 7", got)
	}
	ch.Disconnect()
}

func TestGroupMentionPolicy(t *testing.T) {
	st := testStore(t)
	f := newFakeBotAPI(t)
	ch, _ := newTestChannel(t, st, f)
	ch.mu.Lock()
	ch.me = &tgUser{ID: 4242, Username: "nusa_bot"}
	ch.mu.Unlock()
	groupMsg := func(text string, entities []tgEntity, reply *tgMessage) *tgMessage {
		return &tgMessage{
			MessageID: 9, Chat: tgChat{ID: -900, Type: "supergroup", Title: "Team"},
			From: &tgUser{ID: 111, FirstName: "Ana"}, Date: time.Now().Unix(),
			Text: text, Entities: entities, ReplyToMessage: reply,
		}
	}
	// Plain group chatter is dropped under the mention policy.
	if ch.gateInbound(groupMsg("just talking", nil, nil)) {
		t.Fatal("group message without mention passed the mention policy")
	}
	// A "/command" is let through — the chat parser decides what it means.
	if !ch.gateInbound(groupMsg("/approve@somebot ap_1", nil, nil)) {
		t.Fatal("command in group was dropped by the mention policy")
	}
	// An @bot text mention is let through.
	if !ch.gateInbound(groupMsg("hey @nusa_bot look", nil, nil)) {
		t.Fatal("@bot mention was dropped by the mention policy")
	}
	// A mention entity pointing at the bot counts too.
	ent := []tgEntity{{Type: "mention", Offset: 4, Length: 9}}
	if !ch.gateInbound(groupMsg("hey @nusa_bot", ent, nil)) {
		t.Fatal("mention entity was dropped by the mention policy")
	}
	// A reply to a bot message counts.
	botMsg := &tgMessage{MessageID: 8, From: &tgUser{ID: 4242, IsBot: true}}
	if !ch.gateInbound(groupMsg("following up", nil, botMsg)) {
		t.Fatal("reply to bot message was dropped by the mention policy")
	}
	// A message the user sent through this bot's inline mode names a room,
	// not the bot, and still has to pass the mention policy.
	picked := groupMsg("@cat look", nil, nil)
	picked.ViaBot = &tgUser{ID: 4242, Username: "nusa_bot"}
	if !ch.gateInbound(picked) {
		t.Fatal("inline pick was dropped by the mention policy")
	}
	other := groupMsg("@cat look", nil, nil)
	other.ViaBot = &tgUser{ID: 7, Username: "other_bot"}
	if ch.gateInbound(other) {
		t.Fatal("another bot's inline message passed the mention policy")
	}
	// disabled rejects groups outright; all accepts them.
	ch.settings.GroupPolicy = "disabled"
	if ch.gateInbound(groupMsg("@nusa_bot hi", nil, nil)) {
		t.Fatal("group message passed the disabled policy")
	}
	ch.settings.GroupPolicy = "all"
	if !ch.gateInbound(groupMsg("just talking", nil, nil)) {
		t.Fatal("group message was dropped under the all policy")
	}
}

func TestMessageOlderThanTwoHoursDropped(t *testing.T) {
	st := testStore(t)
	f := newFakeBotAPI(t)
	ch, _ := newTestChannel(t, st, f)
	m := textMsg(1, 111, 111, "old")
	m.Date = time.Now().Add(-3 * time.Hour).Unix()
	if ch.gateInbound(m) {
		t.Fatal("message older than two hours was accepted")
	}
	m.Date = time.Now().Add(-30 * time.Minute).Unix()
	if !ch.gateInbound(m) {
		t.Fatal("fresh message was rejected")
	}
}

func TestInboundMapping(t *testing.T) {
	st := testStore(t)
	f := newFakeBotAPI(t)
	ch, _ := newTestChannel(t, st, f)
	ch.mu.Lock()
	ch.me = &tgUser{ID: 4242, Username: "nusa_bot"}
	ch.mu.Unlock()
	m := &tgMessage{
		MessageID: 7, MessageThreadID: 99,
		Chat: tgChat{ID: -900, Type: "supergroup", Title: "Forum"},
		From: &tgUser{ID: 111, FirstName: "Ana", LastName: "Lo", Username: "ana"},
		Date: time.Now().Unix(), Text: "@nusa_bot hi",
	}
	in := ch.toInbound(m)
	if in.Channel != "telegram" || in.ExternalID != "-900" || in.ThreadID != "99" {
		t.Fatalf("bad inbound identity: %+v", in)
	}
	if in.SenderID != "111" || in.SenderName != "Ana Lo (@ana)" || in.ChatName != "Forum" {
		t.Fatalf("bad inbound names: %+v", in)
	}
	if in.MessageID != "-900:7" {
		t.Fatalf("MessageID = %q, want chat:message composite", in.MessageID)
	}
	if !in.IsGroup || !in.MentionsMe {
		t.Fatalf("bad inbound flags: %+v", in)
	}
}

func TestSendPreservesThreadAndFinalOnly(t *testing.T) {
	st := testStore(t)
	f := newFakeBotAPI(t)
	ch, _ := newTestChannel(t, st, f)
	ch.mu.Lock()
	ch.client = newAPIClient("test-token", f.srv.URL)
	ch.polling = true
	ch.mu.Unlock()
	d, err := ch.Begin(context.Background(), channels.Target{Channel: "telegram", ExternalID: "-900", ThreadID: "42"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Update(context.Background(), "partial", false); err != nil {
		t.Fatal(err)
	}
	if f.sentCount() != 0 {
		t.Fatal("intermediate update produced a Telegram message")
	}
	if err := d.Update(context.Background(), "final answer", true); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) != 1 {
		t.Fatalf("sent %d messages for one final update", len(f.sent))
	}
	if f.sent[0]["chat_id"].(float64) != -900 || f.sent[0]["message_thread_id"].(float64) != 42 {
		t.Fatalf("thread not preserved: %#v", f.sent[0])
	}
	if d.ID() != "-900:101" {
		t.Fatalf("delivery id = %q", d.ID())
	}
}

func TestTokenNeverLeavesSettings(t *testing.T) {
	st := testStore(t)
	f := newFakeBotAPI(t)
	ch := New(st, nil, nil)
	ch.apiBase = f.srv.URL
	secret := "123456:ABCDEF-secret-token"
	err := ch.UpdateSettings(context.Background(), json.RawMessage(`{"token":"`+secret+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := ch.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), secret) {
		t.Fatalf("Settings() leaked the token: %s", raw)
	}
	if !strings.Contains(string(raw), `"tokenSet":true`) {
		t.Fatalf("tokenSet missing from Settings(): %s", raw)
	}
	// Empty token in a patch keeps the saved one.
	if err := ch.UpdateSettings(context.Background(), json.RawMessage(`{"token":""}`)); err != nil {
		t.Fatal(err)
	}
	if tok, _ := ch.loadToken(context.Background()); tok != secret {
		t.Fatal("empty token patch wiped the saved token")
	}
	// The stored row is sealed, not plaintext.
	stored, err := st.GetSetting(context.Background(), tokenKey)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, secret) || !strings.HasPrefix(stored, "sealed:") {
		t.Fatalf("token stored unsealed: %q", stored)
	}
}

func TestNewTokenValidatedBeforeSave(t *testing.T) {
	st := testStore(t)
	f := newFakeBotAPI(t)
	ch := New(st, nil, nil)
	ch.apiBase = f.srv.URL
	f.failGetMe = true
	err := ch.UpdateSettings(context.Background(), json.RawMessage(`{"token":"bad-token"}`))
	if err == nil {
		t.Fatal("an invalid token was saved without getMe validation")
	}
	if tok, _ := ch.loadToken(context.Background()); tok != "" {
		t.Fatal("rejected token was persisted")
	}
}

func TestCorruptSettingsFailClosed(t *testing.T) {
	st := testStore(t)
	if err := st.SetSetting(context.Background(), SettingsKey, "{broken"); err != nil {
		t.Fatal(err)
	}
	f := newFakeBotAPI(t)
	ch := New(st, nil, nil)
	ch.apiBase = f.srv.URL
	if ch.settingsSnapshot().Enabled {
		t.Fatal("corrupt settings did not fail closed")
	}
	if ch.gateInbound(textMsg(1, 111, 111, "hi")) {
		t.Fatal("corrupt settings allowed a message")
	}
}

func TestNonMessageChatTypesSkipped(t *testing.T) {
	st := testStore(t)
	f := newFakeBotAPI(t)
	ch, _ := newTestChannel(t, st, f)
	ch.settings.AllowedUserIDs = []string{"*"}
	channelMsg := &tgMessage{
		MessageID: 1, Chat: tgChat{ID: -100, Type: "channel", Title: "Announce"},
		From: &tgUser{ID: 111}, Date: time.Now().Unix(), Text: "hello",
	}
	if ch.gateInbound(channelMsg) {
		t.Fatal("channel chat message was accepted")
	}
}

func TestBlockedRingRecordsRejection(t *testing.T) {
	st := testStore(t)
	f := newFakeBotAPI(t)
	ch, _ := newTestChannel(t, st, f)
	ch.settings.AllowedUserIDs = []string{"222"}
	ch.gateInbound(textMsg(1, 555, 777, "hello"))
	list := ch.blockedList()
	if len(list) != 1 || list[0].ID != "777" || list[0].Username != "ana" || list[0].Chat != "555" {
		t.Fatalf("blocked entry missing fields: %+v", list)
	}
}
