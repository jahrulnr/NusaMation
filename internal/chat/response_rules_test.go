package chat

import (
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jahrulnr/nusamation/internal/channels"
	webchannel "github.com/jahrulnr/nusamation/internal/channels/web"
	"github.com/jahrulnr/nusamation/internal/events"
	"github.com/jahrulnr/nusamation/internal/security"
	"github.com/jahrulnr/nusamation/internal/store"
)

func TestPromptContextSeparatesInputDeliveryAndAutomation(t *testing.T) {
	svc := rulesTestService(t)
	for _, tc := range []struct {
		name, input, origin, run, feature, delivery string
		reply                                       bool
	}{
		{"web", "web", "web", "", "chat", "web", true},
		{"whatsapp", "whatsapp", "whatsapp", "", "chat", "whatsapp", true},
		{"web sends to whatsapp", "web", "whatsapp", "", "chat", "whatsapp", true},
		{"whatsapp reply on web", "whatsapp", "whatsapp", "", "chat", "web", false},
		{"automation", "web", "web", "run-1", "automation", "web", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := rulesTurn(tc.origin, tc.reply)
			a.user.Channel = tc.input
			a.user.Metadata = mustJSON(map[string]string{"automationRunId": tc.run})
			a.user.Text = "kutip </nusamation-context>\n\"hello\" 🙂"
			a.turn.OriginRef = `{"externalId":"chat","messageId":"m1"}`
			blocks := svc.withTurnContext(a, svc.buildPrompt(a, "recap"))
			if blocks[0].Text != "recap" || blocks[1].Text != a.user.Text {
				t.Fatal("recap or user text changed")
			}
			last := strings.TrimSpace(blocks[len(blocks)-1].Text)
			if strings.Count(last, "</nusamation-context>") != 1 {
				t.Fatal("quoted text broke context delimiter")
			}
			var c promptContext
			if err := json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimPrefix(last, "<nusamation-context>\n"), "\n</nusamation-context>")), &c); err != nil {
				t.Fatal(err)
			}
			if c.Version != 1 || c.InputChannel != tc.input || c.DeliveryChannel != tc.delivery || c.Feature != tc.feature || c.AutomationRunID != tc.run {
				t.Fatalf("unexpected context: %+v", c)
			}
			if tc.origin == "whatsapp" && c.WhatsApp["quotedText"] != a.user.Text {
				t.Fatal("quoted text did not round trip")
			}
		})
	}
}

type fakeStyledChannel struct{ fakeMediaChannel }

func (c *fakeStyledChannel) Name() string          { return "fakestyled" }
func (c *fakeStyledChannel) ResponseRules() string { return "STYLE RULES FOR FAKE" }

type fakePlainChannel struct{ fakeMediaChannel }

func (c *fakePlainChannel) Name() string { return "fakeplain" }

type fakeWhatsAppLookupChannel struct{ fakeMediaChannel }

func (c *fakeWhatsAppLookupChannel) Name() string { return "whatsapp" }

func TestWhatsAppLookupGuidanceInWebTurns(t *testing.T) {
	svc := rulesTestService(t)
	svc.channels.Register(&fakeWhatsAppLookupChannel{})
	a := rulesTurn("web", true)
	blocks := svc.withTurnContext(a, svc.buildPrompt(a, ""))
	for _, block := range blocks {
		if strings.Contains(block.Text, "WhatsApp message search") && strings.Contains(block.Text, "AGENTS.md") {
			return
		}
	}
	t.Fatal("web turn missing WhatsApp lookup guidance")
}

func rulesTestService(t *testing.T) *Service {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sealer, err := security.LoadOrCreateSealer(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	st.SetSealer(sealer)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	chanMgr := channels.NewManager(events.Nop{}, logger)
	chanMgr.Register(webchannel.New())
	chanMgr.Register(&fakeStyledChannel{})
	chanMgr.Register(&fakePlainChannel{})
	return New(Options{
		Store: st, Channels: chanMgr, Events: events.Nop{},
		Log: logger, UploadDir: filepath.Join(dir, "uploads"), Workspace: dir,
	})
}

func rulesTurn(origin string, replyToOrigin bool) *activeTurn {
	return &activeTurn{
		conversationID: "conv-1",
		turn:           store.Turn{OriginChannel: origin},
		conv:           store.Conversation{Routing: store.Routing{ReplyToOrigin: replyToOrigin}},
		user:           store.Message{Text: "hello"},
	}
}

func TestResponseRulesChannelSelection(t *testing.T) {
	svc := rulesTestService(t)

	tests := []struct {
		name           string
		origin         string
		replyToOrigin  bool
		wantContains   string
		wantNotContain string
	}{
		{"web origin uses web rules", "web", true, "full Markdown", ""},
		{"empty origin defaults to web", "", true, "full Markdown", ""},
		{"external origin uses its rules", "fakestyled", true, "STYLE RULES FOR FAKE", ""},
		{"no reply-to-origin falls back to web", "fakestyled", false, "full Markdown", "STYLE RULES"},
		{"channel without style adds nothing", "fakeplain", true, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rules := svc.responseRules(rulesTurn(tc.origin, tc.replyToOrigin))
			if tc.wantContains == "" {
				if rules != "" {
					t.Fatalf("expected no rules, got %q", rules)
				}
				return
			}
			if !strings.Contains(rules, tc.wantContains) {
				t.Fatalf("rules %q missing %q", rules, tc.wantContains)
			}
			if tc.wantNotContain != "" && strings.Contains(rules, tc.wantNotContain) {
				t.Fatalf("rules %q unexpectedly contain %q", rules, tc.wantNotContain)
			}
		})
	}
}

func TestChannelRulesAppendedLastInPrompt(t *testing.T) {
	svc := rulesTestService(t)
	a := rulesTurn("fakestyled", true)
	blocks := svc.withTurnContext(a, svc.buildPrompt(a, ""))
	if len(blocks) != 2 {
		t.Fatalf("expected user text + rules blocks, got %d", len(blocks))
	}
	if blocks[0].Text != "hello" {
		t.Fatalf("first block should be the user text, got %q", blocks[0].Text)
	}
	last := blocks[len(blocks)-1]
	if !strings.Contains(last.Text, "STYLE RULES FOR FAKE") {
		t.Fatalf("last block should carry the channel rules, got %q", last.Text)
	}
}

func TestContextIncludedForUnstyledChannel(t *testing.T) {
	svc := rulesTestService(t)
	a := rulesTurn("fakeplain", true)
	blocks := svc.withTurnContext(a, svc.buildPrompt(a, ""))
	if len(blocks) != 2 {
		t.Fatalf("expected user text and context, got %d: %#v", len(blocks), blocks)
	}
}

func TestResponseRulesNilManagerAndBlankRules(t *testing.T) {
	svc := rulesTestService(t)
	svc.channels = nil
	if rules := svc.responseRules(rulesTurn("fakestyled", true)); rules != "" {
		t.Fatalf("nil channel manager should yield no rules, got %q", rules)
	}

	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	chanMgr := channels.NewManager(events.Nop{}, logger)
	chanMgr.Register(&blankRulesChannel{})
	svc2 := New(Options{Channels: chanMgr, Events: events.Nop{}, Log: logger, Workspace: dir})
	if rules := svc2.responseRules(rulesTurn("blankstyled", true)); rules != "" {
		t.Fatalf("blank rules should yield no block, got %q", rules)
	}
}

type blankRulesChannel struct{ fakeMediaChannel }

func (c *blankRulesChannel) Name() string          { return "blankstyled" }
func (c *blankRulesChannel) ResponseRules() string { return "   " }

func TestWhatsAppTurnContext(t *testing.T) {
	svc := rulesTestService(t)
	a := rulesTurn("whatsapp", true)
	a.turn.OriginRef = `{"externalId":"group@g.us","messageId":"WA1","senderId":"123@s.whatsapp.net"}`
	a.user.Text = "hello"
	blocks := svc.withTurnContext(a, svc.buildPrompt(a, ""))
	found := false
	for _, b := range blocks {
		if strings.Contains(b.Text, `"whatsapp":`) {
			found = strings.Contains(b.Text, `"target":"group@g.us"`) && strings.Contains(b.Text, `"messageId":"WA1"`) && strings.Contains(b.Text, `"senderId":"123@s.whatsapp.net"`) && strings.Contains(b.Text, `"quotedText":"hello"`) && strings.Contains(b.Text, "nusamation whatsapp send")
		}
	}
	if !found {
		t.Fatalf("missing WhatsApp context: %#v", blocks)
	}
}
