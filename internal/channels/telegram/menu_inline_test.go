package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jahrulnr/nusamation/internal/channels"
	appstore "github.com/jahrulnr/nusamation/internal/store"
)

func attachClient(ch *Channel, f *fakeBotAPI) {
	ch.mu.Lock()
	ch.client = newAPIClient("test-token", f.srv.URL)
	ch.me = &tgUser{ID: 4242, IsBot: true, Username: "nusa_bot"}
	ch.mu.Unlock()
}

func TestSessionMenuGrammar(t *testing.T) {
	if sessionMenu(nil) != nil {
		t.Fatal("no session commands should publish a delete, not the stable list again")
	}
	long := strings.Repeat("å", 300)
	out := sessionMenu([]channels.SessionCommand{
		{Name: "Start"},
		{Name: "do-thing"},
		{Name: "start"},
		{Name: "compact"},
		{Name: "long", Description: long},
	})
	if len(out) != len(menuCommands)+2 {
		t.Fatalf("menu len %d, want stable+2", len(out))
	}
	if out[0].Command != "start" || out[len(menuCommands)].Command != "compact" {
		t.Fatalf("order = %#v", out)
	}
	if out[len(menuCommands)].Description != "compact" {
		t.Fatalf("empty description = %q", out[len(menuCommands)].Description)
	}
	got := out[len(menuCommands)+1].Description
	if n := utf8.RuneCountInString(got); n > 256 || n >= utf8.RuneCountInString(long) || !strings.HasSuffix(got, "…") {
		t.Fatalf("description has %d runes", n)
	}

	var many []channels.SessionCommand
	for i := 0; i < 90; i++ {
		many = append(many, channels.SessionCommand{Name: fmt.Sprintf("c%02d", i)})
	}
	capped := sessionMenu(many)
	if len(capped) != 100 {
		t.Fatalf("capped len %d, want 100", len(capped))
	}
	if capped[99].Command != "c87" {
		t.Fatalf("last command = %q, want c87", capped[99].Command)
	}
}

func TestSessionCommandsSyncAndClear(t *testing.T) {
	st := testStore(t)
	f := newFakeBotAPI(t)
	ch, _ := newTestChannel(t, st, f)
	attachClient(ch, f)
	ctx := context.Background()
	cmds := []channels.SessionCommand{{Name: "compact", Description: "Compact the context"}}

	if err := ch.SyncSessionCommands(ctx, "not-a-chat", cmds); err != nil {
		t.Fatal(err)
	}
	if err := ch.SyncSessionCommands(ctx, "555", cmds); err != nil {
		t.Fatal(err)
	}
	if err := ch.SyncSessionCommands(ctx, "555", cmds); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	if f.cmdCalls != 1 {
		t.Fatalf("duplicate sync called setMyCommands %d times", f.cmdCalls)
	}
	if f.scopes[0]["type"] != "chat" || f.scopes[0]["chat_id"].(float64) != 555 {
		t.Fatalf("scope = %#v", f.scopes[0])
	}
	if len(f.cmds) != len(menuCommands)+1 || f.cmds[0].Command != "start" || f.cmds[len(f.cmds)-1].Command != "compact" {
		t.Fatalf("published %#v", f.cmds)
	}
	f.mu.Unlock()

	if err := ch.SyncSessionCommands(ctx, "555", append(cmds, channels.SessionCommand{Name: "ship", Description: "Ship it"})); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	if f.cmdCalls != 2 || f.cmds[len(f.cmds)-1].Command != "ship" {
		t.Fatalf("changed menu was not published: calls=%d last=%q", f.cmdCalls, f.cmds[len(f.cmds)-1].Command)
	}
	f.mu.Unlock()

	if err := ch.SyncSessionCommands(ctx, "555", nil); err != nil {
		t.Fatal(err)
	}
	if err := ch.SyncSessionCommands(ctx, "555", nil); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	if len(f.deleted) != 1 || f.deleted[0]["type"] != "chat" {
		t.Fatalf("empty session list deletes = %#v", f.deleted)
	}
	f.mu.Unlock()

	if err := ch.SyncSessionCommands(ctx, "555", cmds); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.failDelete = true
	failedAt := len(f.deleted)
	f.mu.Unlock()
	if err := ch.ClearSessionCommands(ctx, "555"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	if len(f.deleted) != failedAt+1 {
		t.Fatalf("failed clear was not attempted: %d", len(f.deleted))
	}
	f.failDelete = false
	f.mu.Unlock()
	if err := ch.ClearSessionCommands(ctx, "555"); err != nil {
		t.Fatal(err)
	}
	if err := ch.ClearSessionCommands(ctx, "555"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	if len(f.deleted) != failedAt+2 {
		t.Fatalf("clear retries = %d, want one success after the failure", len(f.deleted)-failedAt)
	}
	f.mu.Unlock()

	ch.mu.Lock()
	ch.client = nil
	ch.mu.Unlock()
	f.mu.Lock()
	calls := f.cmdCalls
	f.mu.Unlock()
	if err := ch.SyncSessionCommands(ctx, "555", cmds); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	if f.cmdCalls != calls {
		t.Fatal("disconnected sync hit the API")
	}
	f.mu.Unlock()
}

func TestInlineQueryAndPick(t *testing.T) {
	st := testStore(t)
	f := newFakeBotAPI(t)
	ch, got := newTestChannel(t, st, f)
	attachClient(ch, f)
	ctx := context.Background()
	enabled := true
	ag, err := st.CreateAgent(ctx, appstore.AgentInput{
		Name: "Agent", Slug: "ag", Command: "echo", Cwd: t.TempDir(), Enabled: &enabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	hunter, err := st.CreateConversation(ctx, ag.ID, "Hunter room", "", "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if hunter.Label != "cat" {
		t.Fatalf("first label = %q, want cat", hunter.Label)
	}
	if _, err := st.UpdateConversation(ctx, hunter.ID, appstore.ConversationUpdate{Title: new("Hunter room")}); err != nil {
		t.Fatal(err)
	}
	fixer, err := st.CreateConversation(ctx, ag.ID, "Fixer", "", "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if fixer.Label != "dog" {
		t.Fatalf("second label = %q, want dog", fixer.Label)
	}
	archived, err := st.CreateConversation(ctx, ag.ID, "Old", "", "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateConversation(ctx, archived.ID, appstore.ConversationUpdate{Archived: new(true)}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertProject(ctx, "Desk Tools", []string{"/tmp/desk"}); err != nil {
		t.Fatal(err)
	}

	ch.handleInlineQuery(ctx, &tgInlineQuery{ID: "q-deny", From: &tgUser{ID: 999}, Query: "cat"})
	f.mu.Lock()
	if len(f.answers) != 1 || f.answers[0].Results != "[]" {
		t.Fatalf("unallowed answer = %#v", f.answers)
	}
	if f.answers[0].Body["is_personal"] != true || f.answers[0].Body["cache_time"].(float64) != 0 {
		t.Fatalf("answer flags = %#v", f.answers[0].Body)
	}
	f.mu.Unlock()

	ch.handleInlineQuery(ctx, &tgInlineQuery{ID: "q-cat", From: &tgUser{ID: 111}, Query: "ca  please"})
	f.mu.Lock()
	catText := messageTexts(t, f.answers[1].Results)
	f.mu.Unlock()
	if len(catText) != 1 || catText[0] != "@cat  please" {
		t.Fatalf("cat insert = %#v", catText)
	}

	ch.handleInlineQuery(ctx, &tgInlineQuery{ID: "q-title", From: &tgUser{ID: 111}, Query: "hunt"})
	f.mu.Lock()
	huntText := messageTexts(t, f.answers[2].Results)
	f.mu.Unlock()
	if len(huntText) != 1 || huntText[0] != "@cat" {
		t.Fatalf("title match = %#v", huntText)
	}

	ch.handleInlineQuery(ctx, &tgInlineQuery{ID: "q-proj", From: &tgUser{ID: 111}, Query: "tools"})
	f.mu.Lock()
	projText := messageTexts(t, f.answers[3].Results)
	f.mu.Unlock()
	if len(projText) != 1 || projText[0] != "@desk-tools" {
		t.Fatalf("project insert = %#v", projText)
	}

	ch.handleInlineQuery(ctx, &tgInlineQuery{ID: "q-all", From: &tgUser{ID: 111}, Query: ""})
	f.mu.Lock()
	allText := messageTexts(t, f.answers[4].Results)
	f.mu.Unlock()
	joined := strings.Join(allText, " ")
	if !strings.Contains(joined, "@cat") || !strings.Contains(joined, "@dog") || !strings.Contains(joined, "@desk-tools") {
		t.Fatalf("empty query = %#v", allText)
	}
	if strings.Contains(joined, "@"+archived.Label) {
		t.Fatalf("archived room was offered: %#v", allText)
	}

	text := "see @CAT and @desk-tools and @nusa_bot and @nope"
	m := textMsg(3, 111, 111, text)
	m.ViaBot = &tgUser{ID: 4242, Username: "Nusa_Bot"}
	in := ch.toInbound(m)
	if len(in.References) != 2 {
		t.Fatalf("references = %#v", in.References)
	}
	if in.References[0].Kind != "conversation" || in.References[0].ID != hunter.ID || in.References[0].Label != "@CAT" {
		t.Fatalf("room ref = %#v", in.References[0])
	}
	if in.References[1].Kind != "project" || in.References[1].Label != "@desk-tools" {
		t.Fatalf("project ref = %#v", in.References[1])
	}
	if text[in.References[0].Start:in.References[0].End] != "@CAT" || text[in.References[1].Start:in.References[1].End] != "@desk-tools" {
		t.Fatal("reference offsets do not cover their labels")
	}
	plain := ch.toInbound(textMsg(4, 111, 111, text))
	if len(plain.References) != 0 {
		t.Fatalf("ordinary mention became a reference: %#v", plain.References)
	}
	other := textMsg(5, 111, 111, text)
	other.ViaBot = &tgUser{ID: 7, Username: "other_bot"}
	if len(ch.toInbound(other).References) != 0 {
		t.Fatal("another bot's inline message was resolved")
	}
	if len(*got) != 0 {
		t.Fatal("inline handling delivered an inbound message")
	}
}

func TestInlineRoomCap(t *testing.T) {
	st := testStore(t)
	f := newFakeBotAPI(t)
	ch, _ := newTestChannel(t, st, f)
	attachClient(ch, f)
	ctx := context.Background()
	enabled := true
	ag, err := st.CreateAgent(ctx, appstore.AgentInput{
		Name: "Agent", Slug: "ag", Command: "echo", Cwd: t.TempDir(), Enabled: &enabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]bool{}
	for i := 0; i < 16; i++ {
		conv, err := st.CreateConversation(ctx, ag.ID, "Room", "", "web", "", "")
		if err != nil {
			t.Fatal(err)
		}
		labels[conv.Label] = true
	}
	archived, err := st.CreateConversation(ctx, ag.ID, "Old", "", "web", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateConversation(ctx, archived.ID, appstore.ConversationUpdate{Archived: new(true)}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertProject(ctx, "Desk Tools", []string{"/tmp/desk"}); err != nil {
		t.Fatal(err)
	}
	ch.handleInlineQuery(ctx, &tgInlineQuery{ID: "q-cap", From: &tgUser{ID: 111}, Query: ""})
	f.mu.Lock()
	defer f.mu.Unlock()
	texts := messageTexts(t, f.answers[0].Results)
	if len(texts) != inlineRoomCap+1 {
		t.Fatalf("results %d, want %d rooms plus the project", len(texts), inlineRoomCap)
	}
	if texts[len(texts)-1] != "@desk-tools" {
		t.Fatalf("project was not last: %#v", texts)
	}
	shown := map[string]bool{}
	for _, text := range texts[:len(texts)-1] {
		shown[strings.TrimPrefix(text, "@")] = true
	}
	missing := 0
	for label := range labels {
		if !shown[label] {
			missing++
		}
	}
	if missing != 1 || shown[archived.Label] {
		t.Fatalf("shown %d of %d active, archived included=%v", len(shown), len(labels), shown[archived.Label])
	}
}

func TestInlineQueryFromPollIsNotAMessage(t *testing.T) {
	st := testStore(t)
	f := newFakeBotAPI(t)
	ch, got := newTestChannel(t, st, f)
	saveTokenDirect(t, ch, st)
	f.updates = []tgUpdate{{
		UpdateID:    1,
		InlineQuery: &tgInlineQuery{ID: "iq", From: &tgUser{ID: 111}, Query: "cat"},
	}}
	if err := ch.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "answerInlineQuery", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.answers) > 0
	})
	ch.Disconnect()
	if len(*got) != 0 {
		t.Fatal("inline query was delivered as a chat message")
	}
}

func messageTexts(t *testing.T, raw string) []string {
	t.Helper()
	var results []struct {
		Input struct {
			Text string `json:"message_text"`
		} `json:"input_message_content"`
	}
	if err := json.Unmarshal([]byte(raw), &results); err != nil {
		t.Fatalf("results %s: %v", raw, err)
	}
	out := make([]string, len(results))
	for i, r := range results {
		out[i] = r.Input.Text
	}
	return out
}
