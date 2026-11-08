package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jahrulnr/nusamation/internal/acp"
	"github.com/jahrulnr/nusamation/internal/agent"
	"github.com/jahrulnr/nusamation/internal/channels"
	"github.com/jahrulnr/nusamation/internal/store"
	"github.com/jahrulnr/nusamation/internal/textfmt"
)

func (s *Service) AgentState(agentID, status, lastError string) {
	s.emit("agent.status", "", map[string]any{
		"agentId": agentID, "status": status, "lastError": lastError,
	})
}

func (s *Service) SessionCreated(sess *agent.Session, configs json.RawMessage, recovered bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conv, err := s.store.GetConversation(ctx, sess.ConversationID())
	if err != nil || conv.AgentID != sess.AgentID() {
		return
	}
	if sess.ACPID() != "" {
		if err := s.store.SetConversationACPSession(ctx, conv.ID, sess.ACPID()); err != nil {
			s.log.Error("could not record ACP session", "conversation", conv.ID, "agent", sess.AgentID(), "session", sess.ACPID(), "error", err)
		}
	}
	if len(configs) > 0 {
		s.persistConfigs(ctx, conv, configs)
	}
	conv, _ = s.store.GetConversation(ctx, conv.ID)
	s.emit("agent.session", conv.ID, map[string]any{
		"conversationId": conv.ID, "acpSessionId": sess.ACPID(), "recovered": recovered,
		"agentId": sess.AgentID(), "configOptions": conv.ConfigOptions,
	})
	if recovered {
		s.emit("notice", conv.ID, map[string]any{
			"severity": "info", "title": "Session recovered",
			"description": "The agent session was recreated. A transcript recap will be attached to this turn.",
		})
	}
}

func (s *Service) SessionUpdate(sess *agent.Session, u acp.SessionUpdate, raw json.RawMessage) {
	a := s.activeFor(sess.ConversationID())
	if a == nil {
		if u.SessionUpdate == "session_info_update" && strings.TrimSpace(u.Title) != "" {
			s.applyAgentTitle(sess.ConversationID(), u.Title)
		}
		return
	}
	switch u.SessionUpdate {
	case "agent_message_chunk":
		var block acp.ContentBlock
		if json.Unmarshal(u.Content, &block) == nil && block.Type != "" && block.Type != "text" {
			s.collectOutbound(a, block)
			return
		}
		text := acp.DecodeTextContent(u.Content)
		if text == "" {
			return
		}
		s.rotateStream(a, "thought")
		s.appendAgentText(a, u.MessageID, text)
	case "agent_thought_chunk":
		text := acp.DecodeTextContent(u.Content)
		if text == "" {
			return
		}
		s.rotateStream(a, "assistant")
		s.appendAgentThought(a, text)
	case "user_message_chunk":
		// The web UI already has the user's message.
	case "tool_call", "tool_call_update":
		if u.SessionUpdate == "tool_call" {
			a.mu.Lock()
			_, exists := a.tools[u.ToolCallID]
			a.mu.Unlock()
			if !exists && u.ToolCallID != "" {
				s.rotateStream(a, "thought")
				s.rotateStream(a, "assistant")
			}
		}
		s.applyToolUpdate(a, u)
	case "plan":
		s.applyPlan(a, u.Entries)
	case "available_commands_update":
		s.applyCommands(a, u.AvailableCommands)
	case "current_mode_update":
		s.emit("config.updated", a.conversationID, map[string]any{
			"conversationId": a.conversationID, "modeId": u.CurrentModeID,
		})
	case "config_option_update":
		if len(u.ConfigOptions) > 0 {
			a.mu.Lock()
			conv := a.conv
			a.mu.Unlock()
			s.persistConfigs(context.Background(), conv, u.ConfigOptions)
		}
	case "usage_update":
		s.applyUsage(a, u)
	case "session_info_update":
		if strings.TrimSpace(u.Title) != "" {
			s.applyAgentTitle(a.conversationID, u.Title)
		}
	case "notice":
		sev := u.Severity
		if sev == "" {
			sev = "info"
		}
		if strings.TrimSpace(u.Description) != "" {
			s.emit("notice", a.conversationID, map[string]any{
				"severity": sev, "title": u.Title, "description": u.Description,
			})
		}
	case "compaction_update":
		s.emit("notice", a.conversationID, map[string]any{
			"severity": "info", "title": "Compacting context",
			"description": "The agent is compacting its context window.",
		})
	case "compaction_summary_chunk":
		// Internal summary stream; deliberately not shown as assistant output.
	default:
		s.log.Debug("unhandled session update", "type", u.SessionUpdate, "conversation", a.conversationID)
	}
	_ = raw
}

func (s *Service) SessionConfigUpdated(sess *agent.Session, configs json.RawMessage) {
	if len(configs) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conv, err := s.store.GetConversation(ctx, sess.ConversationID())
	if err != nil || conv.AgentID != sess.AgentID() {
		return
	}
	s.persistConfigs(ctx, conv, configs)
	updated, err := s.store.GetConversation(ctx, conv.ID)
	if err == nil {
		s.emit("conversation.updated", conv.ID, updated)
		s.emit("config.updated", conv.ID, map[string]any{
			"conversationId": conv.ID, "configOptions": updated.ConfigOptions, "model": updated.Model,
		})
	}
}

func (s *Service) AgentLog(agentID, line string) {
	line = textfmt.StripANSI(line)
	if len(line) > 2000 {
		line = line[:2000]
	}
	s.log.Debug("agent log", "agent", agentID, "line", line)
}

func (s *Service) AgentExit(agentID string, err error) {
	msg := "agent process exited"
	if err != nil {
		msg = err.Error()
	}
	s.emit("agent.status", "", map[string]any{"agentId": agentID, "status": "error", "lastError": msg})
}

func (s *Service) SessionLost(sess *agent.Session, err error) {
	a := s.activeFor(sess.ConversationID())
	if a == nil {
		return
	}
	msg := "agent session lost"
	if err != nil {
		msg = err.Error()
	}
	s.emit("notice", a.conversationID, map[string]any{
		"severity": "error", "title": "Agent disconnected", "description": msg,
	})
}

func (s *Service) appendAgentText(a *activeTurn, acpMessageID, delta string) {
	if acpMessageID != "" {
		a.mu.Lock()
		rotate := a.assistantID != "" && a.assistantACPID != "" && a.assistantACPID != acpMessageID
		a.mu.Unlock()
		if rotate {
			s.rotateStream(a, "assistant")
		}
	}
	s.ensureStream(a, "assistant", acpMessageID)
	delta, first, offset := a.appendAssistant(delta)
	if delta == "" {
		return
	}
	a.mu.Lock()
	msgID := a.assistantID
	a.mu.Unlock()
	s.emit("message.delta", a.conversationID, map[string]any{
		"conversationId": a.conversationID, "messageId": msgID, "kind": "text", "delta": delta, "offsetBytes": offset,
	})
	_ = first
}

// Start a new message whenever the event type changes so the stored sequence
// reflects the order in which the agent spoke and called tools.
func (s *Service) rotateStream(a *activeTurn, kind string) {
	a.streamMu.Lock()
	defer a.streamMu.Unlock()
	a.mu.Lock()
	var id, text string
	if kind == "thought" {
		id, text = a.thoughtID, a.thought.String()
		a.thoughtID = ""
		a.thought.Reset()
		a.thoughtDirty = false
	} else {
		id, text = a.assistantID, a.assistant.String()
		if id != "" {
			a.assistantTextID = id
		}
		if strings.TrimSpace(text) != "" {
			a.assistantGap = true
		}
		a.assistantID, a.assistantACPID = "", ""
		a.assistant.Reset()
		a.assistantDirty = false
	}
	a.mu.Unlock()
	if id != "" {
		s.finalizeMessage(id, text, "complete")
		if kind == "assistant" {
			s.deliverSegment(a, text)
		}
	}
}

func (s *Service) deliverSegment(a *activeTurn, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	a.deliveryMu.Lock()
	defer a.deliveryMu.Unlock()
	a.mu.Lock()
	d, ok := a.delivery.(channels.SegmentDelivery)
	a.mu.Unlock()
	if !ok {
		return
	}
	s.collectOutboundFromText(a)
	text = strings.TrimSpace(cleanDeliveredText(textfmt.ChatMarkdown(text), a.outboundRefs()))
	if text == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := d.SendSegment(ctx, textfmt.Truncate(text, 3500)); err != nil {
		s.log.Warn("failed to send channel reply", "conversation", a.conversationID, "error", err)
		s.notifyOrigin(a, textfmt.Truncate(text, 3500))
	}
	a.mu.Lock()
	a.deliveredSegment = true
	a.mu.Unlock()
}

func (s *Service) appendAgentThought(a *activeTurn, delta string) {
	s.ensureStream(a, "thought", "")
	var offset int
	delta, _, offset = a.appendThought(delta)
	if delta == "" {
		return
	}
	a.mu.Lock()
	msgID := a.thoughtID
	a.mu.Unlock()
	s.emit("message.delta", a.conversationID, map[string]any{
		"conversationId": a.conversationID, "messageId": msgID, "kind": "thought", "delta": delta, "offsetBytes": offset,
	})
}

// ensureStream lazily creates the database row for an assistant or thought
// stream. It is called from the ACP notification loop, so it keeps DB work to a
// minimum (one insert per stream, not per chunk).
func (s *Service) ensureStream(a *activeTurn, kind, acpMessageID string) {
	a.mu.Lock()
	var existing string
	if kind == "thought" {
		existing = a.thoughtID
	} else {
		existing = a.assistantID
	}
	if existing != "" {
		if kind == "assistant" && acpMessageID != "" && a.assistantACPID == "" {
			a.assistantACPID = acpMessageID
		}
		a.mu.Unlock()
		return
	}
	msg := store.Message{
		ConversationID: a.conversationID,
		TurnID:         a.turn.ID,
		Role:           "assistant",
		Kind:           kind,
		Status:         "streaming",
		Channel:        "web",
		ACPMessageID:   acpMessageID,
	}
	if kind == "thought" {
		msg.Kind = "thought"
	} else {
		msg.Kind = "text"
	}
	if kind == "thought" {
		msg.Text = ""
	}
	a.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err := s.store.AppendMessage(ctx, &msg)
	cancel()
	if err != nil {
		s.log.Warn("failed to create stream message", "kind", kind, "error", err)
		return
	}
	a.mu.Lock()
	if kind == "thought" {
		if a.thoughtID == "" {
			a.thoughtID = msg.ID
		}
	} else {
		if a.assistantID == "" {
			a.assistantID = msg.ID
			a.assistantTextID = msg.ID
			if acpMessageID != "" {
				a.assistantACPID = acpMessageID
			}
		}
	}
	a.mu.Unlock()
	s.emit("message.created", a.conversationID, s.hydrateMessage(context.Background(), msg))
}

func (s *Service) applyToolUpdate(a *activeTurn, u acp.SessionUpdate) {
	if u.ToolCallID == "" {
		return
	}
	a.toolMu.Lock()
	defer a.toolMu.Unlock()
	a.mu.Lock()
	t := a.tools[u.ToolCallID]
	if t == nil {
		t = &toolState{Status: "pending", ToolCallID: u.ToolCallID}
		a.tools[u.ToolCallID] = t
		a.toolCount++
	}
	if u.Title != "" {
		t.Title = u.Title
	}
	if u.Name != "" {
		t.Name = u.Name
	}
	if u.Kind != "" {
		t.Kind = u.Kind
	}
	if u.Status != "" {
		t.Status = u.Status
	}
	if len(u.Content) > 0 {
		t.Content = u.Content
		var blocks []struct {
			Type       string `json:"type"`
			TerminalID string `json:"terminalId"`
		}
		if json.Unmarshal(u.Content, &blocks) == nil {
			for _, block := range blocks {
				if block.Type != "terminal" || block.TerminalID == "" {
					continue
				}
				if t.Terminals == nil {
					t.Terminals = make(map[string]agent.TerminalUpdate)
				}
				if state, ok := a.terminals[block.TerminalID]; ok {
					t.Terminals[block.TerminalID] = state
				} else if _, ok := t.Terminals[block.TerminalID]; !ok {
					t.Terminals[block.TerminalID] = agent.TerminalUpdate{ID: block.TerminalID}
				}
			}
		}
	}
	if len(u.Locations) > 0 {
		t.Locations = u.Locations
	}
	if len(u.RawInput) > 0 && string(u.RawInput) != "null" {
		t.RawInput = u.RawInput
	}
	if len(u.RawOutput) > 0 && string(u.RawOutput) != "null" {
		t.RawOutput = u.RawOutput
	}
	if len(u.Meta) > 0 {
		t.Meta = u.Meta
	}
	msgID := t.MessageID
	content := t.json()
	title := t.display()
	status := toolMessageStatus(t.Status)
	a.signal()
	a.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if msgID == "" {
		msg := store.Message{
			ConversationID: a.conversationID, TurnID: a.turn.ID, Role: "assistant", Kind: "tool",
			Text: title, Content: content, Status: status, ToolCallID: u.ToolCallID, Channel: "web",
		}
		if err := s.store.AppendMessage(ctx, &msg); err != nil {
			s.log.Warn("failed to store tool call", "error", err)
			return
		}
		a.mu.Lock()
		if t.MessageID == "" {
			t.MessageID = msg.ID
		}
		msg.ID = t.MessageID
		a.mu.Unlock()
		s.emit("tool.created", a.conversationID, msg)
		return
	}
	_ = s.store.UpdateMessage(ctx, msgID, &title, content, &status, nil)
	if msg, err := s.store.GetMessage(ctx, msgID); err == nil {
		s.emit("tool.updated", a.conversationID, msg)
	}
}

// A terminal may emit before its tool-call reference or finish after its turn.
// Keep its full client-side stream separate from ACP's bounded output response.
func (s *Service) TerminalUpdate(sess *agent.Session, update agent.TerminalUpdate) {
	s.mu.Lock()
	a := s.terminalTurns[update.ID]
	if a == nil {
		a = s.active[sess.ConversationID()]
		if a != nil {
			s.terminalTurns[update.ID] = a
		}
	}
	if update.ExitStatus != nil {
		delete(s.terminalTurns, update.ID)
	}
	s.mu.Unlock()
	if a == nil {
		return
	}
	s.applyTerminalUpdate(a, update)
}

func (s *Service) applyTerminalUpdate(a *activeTurn, update agent.TerminalUpdate) {
	a.toolMu.Lock()
	defer a.toolMu.Unlock()
	a.mu.Lock()
	if a.terminals == nil {
		a.terminals = make(map[string]agent.TerminalUpdate)
	}
	if previous, ok := a.terminals[update.ID]; ok && previous.Version > update.Version {
		a.mu.Unlock()
		return
	}
	a.terminals[update.ID] = update
	var changed []*toolState
	for _, t := range a.tools {
		if _, ok := t.Terminals[update.ID]; ok {
			t.Terminals[update.ID] = update
			if t.MessageID != "" {
				changed = append(changed, t)
			}
		}
	}
	a.mu.Unlock()
	for _, t := range changed {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		content := t.json()
		err := s.store.UpdateMessage(ctx, t.MessageID, nil, content, nil, nil)
		if err == nil {
			if msg, err := s.store.GetMessage(ctx, t.MessageID); err == nil {
				s.emit("tool.updated", a.conversationID, msg)
			}
		}
		cancel()
	}
}

func (s *Service) applyPlan(a *activeTurn, entries json.RawMessage) {
	if len(entries) == 0 {
		return
	}
	plan := capJSON(entries, 256<<10)
	_ = s.store.SetConversationPlan(context.Background(), a.conversationID, plan)
	a.mu.Lock()
	a.conv.Plan = plan
	a.mu.Unlock()
	s.emit("plan.updated", a.conversationID, map[string]any{
		"conversationId": a.conversationID, "plan": plan,
	})
}

func (s *Service) applyUsage(a *activeTurn, u acp.SessionUpdate) {
	a.mu.Lock()
	usage := a.conv.Usage
	if u.Used > 0 {
		usage.Used = u.Used
	}
	if u.Size > 0 {
		usage.Size = u.Size
	}
	if u.Cost != nil {
		usage.CostAmount = u.Cost.Amount
		usage.CostCurrency = u.Cost.Currency
	}
	if len(u.Meta) > 0 {
		var meta struct {
			TotalTokens  int64 `json:"totalTokens"`
			InputTokens  int64 `json:"inputTokens"`
			OutputTokens int64 `json:"outputTokens"`
		}
		if json.Unmarshal(u.Meta, &meta) == nil {
			if meta.TotalTokens > 0 {
				usage.TotalTokens = meta.TotalTokens
			}
			if meta.InputTokens > 0 {
				usage.InputTokens = meta.InputTokens
			}
			if meta.OutputTokens > 0 {
				usage.OutputTokens = meta.OutputTokens
			}
		}
	}
	a.conv.Usage = usage
	a.mu.Unlock()
	_ = s.store.SetConversationUsage(context.Background(), a.conversationID, usage)
	s.emit("usage.updated", a.conversationID, map[string]any{
		"conversationId": a.conversationID, "usage": usage,
	})
}

func (s *Service) applyCommands(a *activeTurn, commands json.RawMessage) {
	if len(commands) == 0 {
		return
	}
	commands = capJSON(commands, 64<<10)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conv, err := s.patchMeta(ctx, a.conversationID, map[string]any{"commands": commands})
	if err == nil {
		a.mu.Lock()
		a.conv = conv
		a.mu.Unlock()
	}
}

func (s *Service) applyAgentTitle(conversationID, title string) {
	title = strings.TrimSpace(title)
	if title == "" {
		return
	}
	if len(title) > 200 {
		title = title[:200]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conv, err := s.store.GetConversation(ctx, conversationID)
	if err != nil {
		return
	}
	meta, _ := s.metaMap(conv)
	source, _ := meta["titleSource"].(string)
	if source == "user" {
		return
	}
	if !isDefaultTitle(conv.Title) && source != "agent" {
		return
	}
	updated, err := s.store.UpdateConversation(ctx, conversationID, store.ConversationUpdate{Title: &title})
	if err != nil {
		return
	}
	_, _ = s.patchMeta(ctx, conversationID, map[string]any{"titleSource": "agent"})
	s.emit("conversation.updated", conversationID, updated)
}

func (s *Service) persistConfigs(ctx context.Context, conv store.Conversation, configs json.RawMessage) {
	if len(configs) == 0 {
		return
	}
	configs = capJSON(configs, 512<<10)
	model := conv.Model
	if m := modelFromConfigs(configs); m != "" {
		model = m
	}
	_ = s.store.SetConversationConfig(ctx, conv.ID, model, configs)
}

func (s *Service) finalizeMessage(id, text, status string) {
	if id == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.store.FinalizeMessage(ctx, id, text, status)
	if m, err := s.store.GetMessage(ctx, id); err == nil {
		s.emit("message.updated", m.ConversationID, m)
	}
}

func (s *Service) flushStreams(a *activeTurn, final bool, status string) {
	a.streamMu.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	aid, atext, adirty := a.assistantSnapshot()
	if aid != "" && (adirty || final) {
		st := "streaming"
		if final {
			st = messageStatus(status)
		}
		_ = s.store.UpdateMessageText(ctx, aid, atext, st)
		if m, err := s.store.GetMessage(ctx, aid); err == nil {
			if final {
				s.emit("message.updated", a.conversationID, m)
			}
		}
	}
	tid, ttext, tdirty := a.thoughtSnapshot()
	if tid != "" && (tdirty || final) {
		st := "streaming"
		if final {
			st = "complete"
			if status == "cancelled" {
				st = "cancelled"
			}
		}
		_ = s.store.UpdateMessageText(ctx, tid, ttext, st)
		if final {
			if m, err := s.store.GetMessage(ctx, tid); err == nil {
				s.emit("message.updated", a.conversationID, m)
			}
		}
	}
	a.mu.Lock()
	thoughtID := a.thoughtID
	a.mu.Unlock()
	if final && thoughtID == "" {
		// Nothing to persist.
	}
	a.streamMu.Unlock()
	s.updateDelivery(a, final)
}

func (s *Service) updateDelivery(a *activeTurn, final bool) {
	if !final {
		return
	}
	a.deliveryMu.Lock()
	defer a.deliveryMu.Unlock()

	a.mu.Lock()
	d := a.delivery
	deliver := a.deliverToChannel
	text := strings.TrimSpace(a.assistantAll.String())
	status, terminalError := a.finalStatus, a.finalError
	_, segmented := d.(channels.SegmentDelivery)
	deliveredSegment := a.deliveredSegment
	if segmented {
		text = strings.TrimSpace(a.assistant.String())
	}
	a.delivery = nil
	a.mu.Unlock()
	if !deliver && d == nil {
		return
	}
	if text == "" && status != "error" && !(segmented && deliveredSegment) {
		text = s.finalDigest(a)
	} else if text != "" {
		s.collectOutboundFromText(a)
		text = strings.TrimSpace(cleanDeliveredText(textfmt.ChatMarkdown(text), a.outboundRefs()))
	}
	if strings.TrimSpace(text) == "" && status != "error" && !(segmented && deliveredSegment) {
		text = "Done."
	}
	text = textfmt.Truncate(text, 3500)
	if d == nil {
		if text != "" {
			s.notifyOrigin(a, text)
		}
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := d.Update(ctx, text, true); err != nil {
			s.log.Warn("failed to send channel reply", "conversation", a.conversationID, "error", err)
			if text != "" {
				s.notifyOrigin(a, text)
			}
		}
	}
	if status == "error" {
		s.notifyOrigin(a, "The agent hit an error: "+terminalError)
	}
}

func (s *Service) finalDigest(a *activeTurn) string {
	a.mu.Lock()
	terminalStatus, terminalError := a.finalStatus, a.finalError
	text := a.assistantAll.String()
	a.mu.Unlock()
	// assistantAll is the authoritative cumulative reply; prefer it over a
	// windowed DB query so long turns never lose the assistant's text.
	if strings.TrimSpace(text) != "" {
		return textfmt.Truncate(textfmt.ChatMarkdown(text), 3500)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	msgs, err := s.store.ListMessages(ctx, a.conversationID, 0, 0, 100)
	if err != nil {
		return "Done."
	}
	var assistant []string
	var tools []string
	for _, m := range msgs {
		if m.TurnID != a.turn.ID {
			continue
		}
		switch m.Kind {
		case "text":
			if m.Role == "assistant" && strings.TrimSpace(m.Text) != "" {
				assistant = append(assistant, m.Text)
			}
		case "tool":
			if strings.TrimSpace(m.Text) != "" && m.Status != "failed" {
				tools = append(tools, "- "+textfmt.OneLine(m.Text, 120))
			}
		}
	}
	if len(assistant) > 0 {
		return textfmt.Truncate(textfmt.ChatMarkdown(strings.Join(assistant, "\n\n")), 3500)
	}
	if terminalStatus == "error" {
		return textfmt.Truncate("The agent hit an error: "+terminalError, 1200)
	}
	if terminalStatus == "cancelled" {
		return "Cancelled."
	}
	if len(tools) > 0 {
		return "Done. I used:\n" + textfmt.Truncate(strings.Join(tools, "\n"), 1200)
	}
	return "Done."
}

func capJSON(raw json.RawMessage, max int) json.RawMessage {
	if len(raw) <= max {
		return raw
	}
	b, _ := json.Marshal(map[string]any{"_truncated": true, "bytes": len(raw)})
	return b
}

func toolMessageStatus(status string) string {
	switch status {
	case "failed":
		return "error"
	case "completed":
		return "complete"
	default:
		return "streaming"
	}
}

func messageStatus(turnStatus string) string {
	switch turnStatus {
	case "error":
		return "error"
	case "cancelled":
		return "cancelled"
	default:
		return "complete"
	}
}

func (s *Service) flushLoop(a *activeTurn) {
	t := time.NewTicker(800 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-a.flushStop:
			return
		case <-t.C:
			a.mu.Lock()
			finished := a.finished
			a.mu.Unlock()
			if finished {
				return
			}
			s.flushStreams(a, false, "streaming")
		}
	}
}

func (s *Service) finishTurn(a *activeTurn, status, stopReason string, runErr error) {
	a.mu.Lock()
	if a.finished {
		a.mu.Unlock()
		return
	}
	a.finished = true
	cancelRequested := a.cancelRequested
	close(a.flushStop)
	close(a.finishedCh)
	a.mu.Unlock()

	if cancelRequested && status == "error" {
		status = "cancelled"
	}
	errMsg := ""
	if runErr != nil && status == "error" {
		errMsg = fmt.Errorf("%v", runErr).Error()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	_ = s.store.FinishTurn(ctx, a.turn.ID, status, stopReason, errMsg)
	_ = s.store.SetConversationStatus(ctx, a.conversationID, "idle")
	s.cancelPendingApprovalsForTurn(a.turn.ID, "Turn finished.")
	s.prepareOutbound(a)

	a.mu.Lock()
	hasOutput := a.assistantAll.Len() > 0 || a.toolCount > 0 || a.hasOutbound()
	a.finalStatus = status
	a.finalError = errMsg
	a.mu.Unlock()
	if status == "error" && errMsg != "" {
		notice := store.Message{
			ConversationID: a.conversationID, TurnID: a.turn.ID, Role: "system", Kind: "error",
			Status: "error", Text: "The agent hit an error: " + errMsg,
		}
		if err := s.store.AppendMessage(ctx, &notice); err == nil {
			s.emit("message.created", a.conversationID, notice)
		}
	} else if status == "completed" && !hasOutput {
		notice := store.Message{
			ConversationID: a.conversationID, TurnID: a.turn.ID, Role: "system", Kind: "notice",
			Status: "complete", Text: "The agent finished without a text response.",
		}
		if err := s.store.AppendMessage(ctx, &notice); err == nil {
			s.emit("message.created", a.conversationID, notice)
		}
	}

	s.flushStreams(a, true, status)
	go s.deliverOutbound(a)
	s.mirrorFinal(a)
	a.cancel()

	turn, _ := s.store.GetTurn(ctx, a.turn.ID)
	conv, _ := s.store.GetConversation(ctx, a.conversationID)
	var authMethods []acp.AuthMethod
	if authErr, ok := errors.AsType[*agent.AuthRequiredError](runErr); ok {
		authMethods = authErr.Methods
	}
	s.emit("turn.completed", a.conversationID, map[string]any{
		"turn": turn, "status": status, "stopReason": stopReason, "error": errMsg, "authMethods": authMethods,
	})
	if conv.ID != "" {
		s.emit("conversation.updated", a.conversationID, conv)
	}
	// Drop terminal mappings that already saw an exit status — including ones
	// a post-exit update re-registered. Live terminals keep their mapping so
	// output finishing after the turn still lands on this turn's messages.
	a.mu.Lock()
	for id, t := range a.terminals {
		if t.ExitStatus == nil {
			continue
		}
		s.mu.Lock()
		if s.terminalTurns[id] == a {
			delete(s.terminalTurns, id)
		}
		s.mu.Unlock()
	}
	a.mu.Unlock()
	s.finishActive(a)
	s.log.Info("turn finished", "conversation", a.conversationID, "turn", a.turn.ID, "status", status, "stop", stopReason)
}
