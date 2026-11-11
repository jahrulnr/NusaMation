package chat

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/jahrulnr/nusamation/internal/acp"
	"github.com/jahrulnr/nusamation/internal/store"
)

// promptContext describes the current input; it is user-role content.
type promptContext struct {
	input, delivery, rules, harness, automation string
	whatsapp                                    map[string]string
	orchestrated, steering                      bool
}

func (s *Service) withTurnContext(a *activeTurn, blocks []acp.ContentBlock) []acp.ContentBlock {
	c := s.inputContext(a.user, a.turn.OriginChannel, a.turn.OriginRef, s.deliveryChannel(a))
	c.rules = s.responseRules(a)
	a.mu.Lock()
	userMeta := append(json.RawMessage(nil), a.user.Metadata...)
	a.mu.Unlock()
	var metadata struct {
		Orchestration json.RawMessage `json:"orchestration"`
	}
	if json.Unmarshal(userMeta, &metadata) == nil && len(metadata.Orchestration) > 0 && string(metadata.Orchestration) != "null" {
		c.orchestrated = true
	}
	return appendPromptContext(blocks, c)
}

func (s *Service) inputContext(user store.Message, origin, ref, delivery string) promptContext {
	input := user.Channel
	if input == "" {
		input = origin
	}
	if input == "" {
		input = "web"
	}
	c := promptContext{input: input, delivery: delivery}
	if s.workspace != "" {
		c.harness = filepath.Join(s.workspace, "AGENTS.md")
	}
	var meta struct {
		AutomationRunID string `json:"automationRunId"`
	}
	_ = json.Unmarshal(user.Metadata, &meta)
	c.automation = meta.AutomationRunID
	if origin == "whatsapp" {
		var source struct {
			ExternalID string `json:"externalId"`
			MessageID  string `json:"messageId"`
			SenderID   string `json:"senderId"`
		}
		if json.Unmarshal([]byte(ref), &source) == nil && source.ExternalID != "" {
			c.whatsapp = map[string]string{"target": source.ExternalID, "messageId": source.MessageID, "senderId": source.SenderID, "quotedText": user.Text}
		}
	}
	return c
}

// quotedContextValue keeps line breaks and closing tags inside a single value.
func quotedContextValue(value string) string {
	b, _ := json.Marshal(value)
	return string(b)
}

func appendPromptContext(blocks []acp.ContentBlock, c promptContext) []acp.ContentBlock {
	var b strings.Builder
	b.WriteString("\n\n<nusamation-context>\nSend-by: ")
	b.WriteString(c.input)
	b.WriteString("\nResponse-output: ")
	b.WriteString(c.delivery)
	if c.rules != "" {
		b.WriteString(" — ")
		b.WriteString(c.rules)
	}
	if c.harness != "" {
		b.WriteString("\nHarness rule: ")
		b.WriteString(c.harness)
	}
	if c.automation != "" {
		b.WriteString("\nAutomation: scheduled run ")
		b.WriteString(quotedContextValue(c.automation))
		b.WriteString("; execute the task and record the final response in this conversation.")
	}
	if c.orchestrated {
		b.WriteString("\nInput: sent by another NusaMation conversation, not by the human user; conversation-control tools are unavailable during this turn.")
	}
	if c.steering {
		b.WriteString("\nInput: steers the running turn; the response destination remains unchanged.")
	}
	if len(c.whatsapp) != 0 {
		for _, key := range []string{"target", "messageId", "senderId", "quotedText"} {
			b.WriteString("\nWhatsApp ")
			b.WriteString(key)
			b.WriteString(": ")
			b.WriteString(quotedContextValue(c.whatsapp[key]))
		}
		b.WriteString("\nWhatsApp actions: use `nusamation whatsapp send` for polls, reactions, quoted replies, stickers, locations, contacts and events.")
	}
	b.WriteString("\n</nusamation-context>")
	return append(blocks, acp.ContentBlock{Type: "text", Text: b.String()})
}
