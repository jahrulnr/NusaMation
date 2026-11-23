package telegram

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jahrulnr/nusamation/internal/channels"
	"github.com/jahrulnr/nusamation/internal/security"
	appstore "github.com/jahrulnr/nusamation/internal/store"
)

func clientToken(ch *Channel) string {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	if ch.client == nil {
		return ""
	}
	return ch.client.token
}

func pollState(ch *Channel) (polling bool, connected bool) {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	return ch.polling, ch.client != nil
}

// A rejected sender must publish its blocked entry without deadlocking:
// publish -> status reads the blocked ring, and the ring lock is not
// reentrant. The old test harness used a nil manager, so it never published.
func TestRecordBlockedPublishesWithManager(t *testing.T) {
	st := testStore(t)
	mgr := channels.NewManager(nil, slog.Default())
	ch := New(st, mgr, nil)
	ch.settings = Settings{Enabled: true, GroupPolicy: "mention"}

	done := make(chan struct{})
	go func() {
		ch.gateInbound(textMsg(1, 555, 777, "hello"))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("recordBlocked did not return: publish/status deadlocked on blockedMu")
	}
	blocked := ch.blockedList()
	if len(blocked) != 1 || blocked[0].ID != "777" {
		t.Fatalf("blocked ring = %#v", blocked)
	}
	if ch.Status().LoggedIn {
		t.Fatal("status reported a login that never happened")
	}
}

func TestTokenReplacementRestartsPolling(t *testing.T) {
	st := testStore(t)
	f := newFakeBotAPI(t)
	ch, _ := newTestChannel(t, st, f)
	ctx := context.Background()

	if err := ch.UpdateSettings(ctx, json.RawMessage(`{"token":"token-A","autoStart":true}`)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "polling with token A", func() bool {
		polling, hasClient := pollState(ch)
		return polling && hasClient && clientToken(ch) == "token-A"
	})

	if err := ch.UpdateSettings(ctx, json.RawMessage(`{"token":"token-B"}`)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "polling with token B", func() bool {
		polling, hasClient := pollState(ch)
		return polling && hasClient && clientToken(ch) == "token-B"
	})
	waitFor(t, "a poll with token B", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return slices.Contains(f.pollTokens, "token-B")
	})

	f.mu.Lock()
	lastMe := ""
	if len(f.getMeTokens) > 0 {
		lastMe = f.getMeTokens[len(f.getMeTokens)-1]
	}
	f.mu.Unlock()
	if lastMe != "token-B" {
		t.Fatalf("last getMe used %q, want token-B", lastMe)
	}
	ch.Disconnect()
}

func TestGroupAllowlistRejectsPositiveIDs(t *testing.T) {
	st := testStore(t)
	f := newFakeBotAPI(t)
	ch, _ := newTestChannel(t, st, f)

	err := ch.UpdateSettings(context.Background(), json.RawMessage(`{"allowedGroupIds":["123456789"]}`))
	if err == nil || !strings.Contains(err.Error(), "negative") {
		t.Fatalf("positive group id error = %v, want a negative-id refusal", err)
	}

	var s Settings
	s.AllowedGroupIDs = []string{"123456789", "-1001234567890"}
	s.Normalize()
	if len(s.AllowedGroupIDs) != 1 || s.AllowedGroupIDs[0] != "-1001234567890" {
		t.Fatalf("normalized group list = %#v", s.AllowedGroupIDs)
	}

	// A legacy positive entry must never match a private chat, whose chat id
	// is the user id.
	s = Settings{AllowedGroupIDs: []string{"123456789"}}
	if s.allows("123456789", "123456789") {
		t.Fatal("group allowlist entry matched a private chat")
	}
	if s.allows("123456789", "-1001234567890") {
		t.Fatal("positive entry matched an unrelated group")
	}
	if !(Settings{AllowedGroupIDs: []string{"-1001234567890"}}).allows("123456789", "-1001234567890") {
		t.Fatal("negative group entry did not allow its group")
	}

	ch.settings = Settings{Enabled: true, GroupPolicy: "mention", AllowedGroupIDs: []string{"777"}}
	if ch.gateInbound(textMsg(2, 777, 777, "hi")) {
		t.Fatal("a positive group entry opened the matching private chat")
	}
}

func TestUTF16TruncationKeepsRunesWhole(t *testing.T) {
	emoji := strings.Repeat("😀", maxMessageUnits) // 2 units each
	got := truncateUTF16(emoji, maxMessageUnits)
	if units := utf16Units(got); units > maxMessageUnits {
		t.Fatalf("truncated text has %d UTF-16 units", units)
	}
	if got == "" || strings.ContainsRune(got, '\uFFFD') {
		t.Fatalf("truncation split a rune: %q", got)
	}
	if n := len([]rune(got)); n != maxMessageUnits/2 {
		t.Fatalf("kept %d runes, want %d", n, maxMessageUnits/2)
	}
	ascii := strings.Repeat("a", maxMessageUnits)
	if truncateUTF16(ascii, maxMessageUnits) != ascii {
		t.Fatal("text within the limit was cut")
	}
}

func TestCaptionTruncationCountsUTF16Units(t *testing.T) {
	got := truncateHTML(sanitizeHTML(strings.Repeat("😀", 600)), maxCaptionUnits)
	if units := utf16Units(got); units > maxCaptionUnits {
		t.Fatalf("caption has %d UTF-16 units", units)
	}
}

func TestReconnectAfterDisconnectKeepsTheNewFlow(t *testing.T) {
	st := testStore(t)
	f := newFakeBotAPI(t)
	ch, _ := newTestChannel(t, st, f)
	ctx := context.Background()
	if err := ch.UpdateSettings(ctx, json.RawMessage(`{"token":"token-A","autoStart":true}`)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "initial connection", func() bool {
		polling, hasClient := pollState(ch)
		return polling && hasClient
	})

	for i := range 3 {
		if err := ch.Disconnect(); err != nil {
			t.Fatal(err)
		}
		if err := ch.Connect(ctx); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "reconnected flow", func() bool {
			polling, hasClient := pollState(ch)
			return polling && hasClient && clientToken(ch) == "token-A"
		})
		// Give the previous flow's cleanup a chance to run; it must not clear
		// the flow that replaced it.
		time.Sleep(30 * time.Millisecond)
		polling, hasClient := pollState(ch)
		if !polling || !hasClient {
			t.Fatalf("iteration %d: old flow cleanup cleared the new flow", i)
		}
		if state := ch.Status(); !state.Connected {
			t.Fatalf("iteration %d: status = %s", i, state.State)
		}
	}
	ch.Disconnect()
}

func TestTypingStartsOnBeginAndStopsOnFinal(t *testing.T) {
	previous := telegramTypingEvery
	telegramTypingEvery = 20 * time.Millisecond
	defer func() { telegramTypingEvery = previous }()

	st := testStore(t)
	f := newFakeBotAPI(t)
	ch, _ := newTestChannel(t, st, f)
	attachClient(ch, f)

	d, err := ch.Begin(context.Background(), channels.Target{
		Channel: "telegram", ExternalID: "111", ThreadID: "42",
	}, "On it…")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a typing action", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.chatActions > 0
	})
	time.Sleep(3 * telegramTypingEvery) // let the loop refresh
	if err := d.Update(context.Background(), "done", true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "final message", func() bool { return f.sentCount() == 1 })
	// Let a refresh that was already in flight land before taking the
	// baseline; after that the indicator must stay silent.
	time.Sleep(50 * time.Millisecond)
	f.mu.Lock()
	settled := f.chatActions
	f.mu.Unlock()
	time.Sleep(4 * telegramTypingEvery)

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.chatActionChats[0] != "111" || f.chatActionThreads[0] != "42" {
		t.Fatalf("typing routing = chat %q thread %q", f.chatActionChats[0], f.chatActionThreads[0])
	}
	if f.chatActions != settled {
		t.Fatalf("typing continued after the final update: %d actions, settled at %d", f.chatActions, settled)
	}
}

func TestSendRetriesOnceOnRateLimit(t *testing.T) {
	st := testStore(t)
	f := newFakeBotAPI(t)
	ch, _ := newTestChannel(t, st, f)
	attachClient(ch, f)
	f.mu.Lock()
	f.send429 = 1
	f.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if _, err := ch.sendText(ctx, 111, 0, "hello"); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 900*time.Millisecond {
		t.Fatal("the 429 retry did not wait for retry_after")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) != 1 {
		t.Fatalf("sent %d messages, want one after the retry", len(f.sent))
	}
}

func TestPollLoopSurvivesRateLimit(t *testing.T) {
	st := testStore(t)
	f := newFakeBotAPI(t)
	ch, _ := newTestChannel(t, st, f)
	sink := &inboundSink{}
	ch.SetHandler(sink)
	f.poll429 = 1
	f.updates = []tgUpdate{{UpdateID: 10, Message: textMsg(1, 111, 111, "hi")}}
	if err := ch.UpdateSettings(context.Background(), json.RawMessage(`{"token":"token-A","autoStart":true}`)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "message after 429", func() bool { return len(sink.snapshot()) == 1 })
	polling, _ := pollState(ch)
	if !polling {
		t.Fatal("poll loop stopped after a 429")
	}
	ch.Disconnect()
}

func TestMentionRequiresWholeUsername(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"hey @nusa_bot look", true},
		{"@nusa_bot", true},
		{"@Nusa_Bot!", true},
		{"(@nusa_bot)", true},
		{"x@nusa_bot", false},
		{"@nusa_bot_extra", false},
		{"@nusa_bot2", false},
		{"no mention", false},
	}
	for _, tc := range cases {
		if got := textMentions(tc.text, nil, "@nusa_bot"); got != tc.want {
			t.Errorf("textMentions(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}

func TestOldStrangerMessageDoesNotEnterBlockedRing(t *testing.T) {
	st := testStore(t)
	f := newFakeBotAPI(t)
	ch, _ := newTestChannel(t, st, f)
	ch.settings = Settings{Enabled: true, GroupPolicy: "mention"}

	m := textMsg(1, 555, 777, "old")
	m.Date = time.Now().Add(-3 * time.Hour).Unix()
	if ch.gateInbound(m) {
		t.Fatal("stale message passed the gate")
	}
	if got := ch.blockedList(); len(got) != 0 {
		t.Fatalf("stale stranger entered the blocked ring: %#v", got)
	}

	m.Date = time.Now().Unix()
	if ch.gateInbound(m) {
		t.Fatal("fresh stranger passed an empty allowlist")
	}
	if got := ch.blockedList(); len(got) != 1 {
		t.Fatalf("fresh stranger was not recorded: %#v", got)
	}
}

func TestUploadNameStripsHeaderInjections(t *testing.T) {
	st := testStore(t)
	f := newFakeBotAPI(t)
	ch, _ := newTestChannel(t, st, f)
	attachClient(ch, f)

	err := ch.SendMedia(context.Background(), channels.Target{Channel: "telegram", ExternalID: "111"},
		channels.OutboundMedia{Name: "evil\r\nX-Injected: 1.png", MIME: "application/pdf", Data: []byte("%PDF")})
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.media) != 1 {
		t.Fatalf("media calls = %d", len(f.media))
	}
	name := f.media[0].FileName
	if name != "evilX-Injected: 1.png" {
		t.Fatalf("filename = %q, want control characters removed", name)
	}
	if strings.ContainsAny(name, "\r\n") {
		t.Fatalf("filename kept a header separator: %q", name)
	}
}

func TestMenuFingerprintsResetOnLogoutAndTokenChange(t *testing.T) {
	st := testStore(t)
	f := newFakeBotAPI(t)
	ch, _ := newTestChannel(t, st, f)
	ctx := context.Background()

	ch.lastCmds["555"] = "fp"
	if err := ch.UpdateSettings(ctx, json.RawMessage(`{"token":"token-B"}`)); err != nil {
		t.Fatal(err)
	}
	if len(ch.lastCmds) != 0 {
		t.Fatalf("token change kept menu fingerprints: %#v", ch.lastCmds)
	}

	ch.lastCmds["555"] = "fp"
	if err := ch.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	if len(ch.lastCmds) != 0 {
		t.Fatalf("logout kept menu fingerprints: %#v", ch.lastCmds)
	}
}

func TestSessionMenuSkipsShortDescriptions(t *testing.T) {
	out := sessionMenu([]channels.SessionCommand{
		{Name: "go"},                      // default description is 2 runes
		{Name: "up", Description: "Up"},   // still 2 runes
		{Name: "ok2", Description: "Run"}, // 3 runes, kept
	})
	if len(out) != len(menuCommands)+1 {
		t.Fatalf("menu len %d, want stable+1", len(out))
	}
	if out[len(menuCommands)].Command != "ok2" {
		t.Fatalf("kept %q, want ok2", out[len(menuCommands)].Command)
	}
	if sessionMenu([]channels.SessionCommand{{Name: "go"}}) != nil {
		t.Fatal("a menu made only of too-short descriptions must delete, not publish")
	}
}

func TestFailedSettingsWriteLeavesTheOldToken(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	st, err := appstore.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sealer, err := security.LoadOrCreateSealer(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	st.SetSealer(sealer)

	f := newFakeBotAPI(t)
	ch := New(st, nil, nil)
	ch.apiBase = f.srv.URL
	ctx := context.Background()
	if err := ch.UpdateSettings(ctx, json.RawMessage(`{"token":"token-A","autoStart":false}`)); err != nil {
		t.Fatal(err)
	}

	// Block only the non-secret settings row, so the token write would
	// succeed if it ran first — which is exactly the old partial commit.
	if _, err := st.DB().ExecContext(ctx, `CREATE TRIGGER fail_tg_settings BEFORE INSERT ON settings
		WHEN NEW.key = 'channel.telegram.settings'
		BEGIN SELECT RAISE(ABORT, 'injected settings failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := ch.UpdateSettings(ctx, json.RawMessage(`{"token":"token-B"}`)); err == nil {
		t.Fatal("settings write failure was not reported")
	}
	stored, err := st.GetSetting(ctx, tokenKey)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := st.OpenString(stored)
	if err != nil {
		t.Fatal(err)
	}
	if tok != "token-A" {
		t.Fatalf("token = %q after a failed settings write, want token-A", tok)
	}
}
