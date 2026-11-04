package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jahrulnr/nusamation/internal/channels"
	"github.com/jahrulnr/nusamation/internal/store"
	"github.com/jahrulnr/nusamation/internal/textfmt"
)

func (s *Service) HandleInbound(ctx context.Context, in channels.Inbound) error {
	if in.Channel == "" || in.ExternalID == "" {
		return nil
	}
	if in.MessageID != "" {
		fresh, err := s.store.ClaimInbound(ctx, in.Channel, in.MessageID, in.ThreadID, "")
		if err != nil {
			return fmt.Errorf("dedupe inbound: %w", err)
		}
		if !fresh {
			return nil
		}
	}
	if in.PollVote != nil {
		return s.resolvePollVote(ctx, in)
	}
	text := strings.TrimSpace(in.Text)
	if text == "" && len(in.Attachments) == 0 {
		return nil
	}
	cmd, args := parseCommand(text)
	display := strings.TrimSpace(in.ChatName)
	if display == "" {
		display = strings.TrimSpace(in.SenderName)
	}
	if display == "" {
		display = in.ExternalID
	}

	conv, convErr := s.store.FindConversationByChannel(ctx, in.Channel, in.ExternalID, in.ThreadID)
	hasConv := convErr == nil
	if in.MessageID != "" && hasConv {
		_ = s.store.AttachInboundConversation(ctx, in.Channel, in.MessageID, in.ThreadID, conv.ID)
	}

	switch cmd {
	case "help", "start":
		_ = s.reply(ctx, in, helpText())
		return nil
	case "new":
		agentID := ""
		if hasConv {
			agentID = conv.AgentID
			_ = s.Cancel(ctx, conv.ID)
		}
		if len(args) > 0 {
			if ag, err := s.store.GetAgentBySlug(ctx, args[0]); err == nil {
				agentID = ag.ID
			} else {
				_ = s.reply(ctx, in, fmt.Sprintf("I couldn't find an agent named %q.", args[0]))
				return nil
			}
		}
		newConv, err := s.newLinkedConversation(ctx, in, agentID, display)
		if err != nil {
			_ = s.reply(ctx, in, "Could not start a new conversation: "+shortErr(err))
			return err
		}
		_ = s.reply(ctx, in, fmt.Sprintf("New conversation started with %s.\nOpen the web UI to see the full history.", newConv.AgentName))
		return nil
	case "agent":
		return s.commandAgent(ctx, in, conv, hasConv, args)
	case "model":
		return s.commandModel(ctx, in, conv, hasConv, args)
	case "status":
		return s.commandStatus(ctx, in, conv, hasConv)
	case "stop", "cancel":
		if !hasConv {
			_ = s.reply(ctx, in, "There is no active conversation in this chat.")
			return nil
		}
		if err := s.Cancel(ctx, conv.ID); err != nil {
			_ = s.reply(ctx, in, "Nothing is running right now.")
			return nil
		}
		_ = s.reply(ctx, in, "Cancelling the current turn…")
		return nil
	case "retry":
		if !hasConv {
			_ = s.reply(ctx, in, "There is no conversation to retry yet.")
			return nil
		}
		if _, err := s.Retry(ctx, conv.ID, in.Channel, linkRefFromInbound(in)); err != nil {
			_ = s.reply(ctx, in, "Could not retry: "+shortErr(err))
			return err
		}
		_ = s.reply(ctx, in, "Retrying the last message…")
		return nil
	case "approve", "deny":
		return s.commandApproval(ctx, in, conv, hasConv, cmd, args)
	case "":
		// Plain message.
	default:
		if strings.HasPrefix(text, "/") && hasConv {
			// Unknown slash commands are ACP commands (e.g. /compact, /init)
			// and are forwarded to the agent verbatim.
		}
	}

	if !hasConv {
		var err error
		conv, err = s.newLinkedConversation(ctx, in, "", display)
		if err != nil {
			_ = s.reply(ctx, in, "No agent is available yet. Open the NusaMation web UI to add one.")
			return err
		}
	} else if conv.Archived {
		_ = s.reply(ctx, in, "This conversation is archived. Use /new to start a fresh one.")
		return nil
	}

	body := text
	if in.IsGroup && !strings.HasPrefix(text, "/") && strings.TrimSpace(in.SenderName) != "" {
		body = in.SenderName + ": " + text
	}
	var attachments []store.AttachmentInput
	for i, att := range in.Attachments {
		if i >= 6 {
			break
		}
		mime := att.MIME
		if mime == "" {
			mime = http.DetectContentType(att.Data)
		}
		name := att.Name
		if name == "" {
			name = fmt.Sprintf("attachment-%d", i+1)
		}
		up, err := s.SaveInboundAttachment(ctx, conv.ID, name, mime, att.Data)
		if err != nil {
			s.log.Warn("failed to store inbound attachment", "error", err)
			continue
		}
		attachments = append(attachments, store.AttachmentInput{UploadID: up.ID, Name: up.Name, MIME: up.MIME, Size: up.Size})
	}
	msg, _, err := s.Submit(ctx, SubmitInput{
		ConversationID: conv.ID,
		Text:           body,
		Attachments:    attachments,
		Channel:        in.Channel,
		OriginRef:      linkRefFromInbound(in),
	})
	if err != nil {
		// Nothing was queued — release the dedupe claim so a channel retry
		// can still be processed instead of being dropped as a duplicate.
		if in.MessageID != "" {
			_ = s.store.ReleaseInbound(ctx, in.Channel, in.MessageID, in.ThreadID)
		}
		_ = s.reply(ctx, in, "I couldn't queue that message: "+shortErr(err))
		return err
	}
	if in.MessageID != "" {
		_ = s.store.AttachInboundConversation(ctx, in.Channel, in.MessageID, in.ThreadID, conv.ID)
	}
	_ = msg
	return nil
}

func (s *Service) newLinkedConversation(ctx context.Context, in channels.Inbound, agentID, display string) (store.Conversation, error) {
	title := channelLabel(in.Channel) + " · " + display
	if len(title) > 180 {
		title = title[:180]
	}
	conv, err := s.CreateConversation(ctx, agentID, title, "", in.Channel, linkRefFromInbound(in))
	if err != nil {
		return conv, err
	}
	link, err := s.store.MoveChannelLink(ctx, store.ChannelLink{
		ConversationID: conv.ID, Channel: in.Channel, ExternalID: in.ExternalID,
		ThreadID: in.ThreadID, DisplayName: display, IsGroup: in.IsGroup,
	})
	if err != nil {
		return conv, err
	}
	conv, _ = s.store.GetConversation(ctx, conv.ID)
	s.emit("conversation.updated", conv.ID, conv)
	s.log.Info("linked inbound conversation", "conversation", conv.ID, "channel", in.Channel, "link", link.ID)
	return conv, nil
}

func (s *Service) reply(ctx context.Context, in channels.Inbound, text string) error {
	if s.channels == nil {
		return nil
	}
	ch, ok := s.channels.Get(in.Channel)
	if !ok {
		return fmt.Errorf("channel %q is not available", in.Channel)
	}
	target := channels.Target{
		Channel: in.Channel, ExternalID: in.ExternalID, ThreadID: in.ThreadID,
		DisplayName: in.ChatName, IsGroup: in.IsGroup,
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return ch.Notify(ctx, target, textfmt.ChatMarkdown(text))
}

func (s *Service) commandAgent(ctx context.Context, in channels.Inbound, conv store.Conversation, hasConv bool, args []string) error {
	if len(args) == 0 {
		agents, _ := s.store.ListAgents(ctx)
		var b strings.Builder
		b.WriteString("Agents:\n")
		for _, a := range agents {
			if !a.Enabled {
				continue
			}
			mark := ""
			if hasConv && a.ID == conv.AgentID {
				mark = "  ← current"
			}
			fmt.Fprintf(&b, "• %s — %s%s\n", a.Slug, a.Name, mark)
		}
		b.WriteString("\nUse /agent <slug> to switch. Switching starts a fresh agent session with a transcript recap.")
		_ = s.reply(ctx, in, b.String())
		return nil
	}
	if !hasConv {
		_ = s.reply(ctx, in, "Start a conversation first with /new "+args[0]+".")
		return nil
	}
	ag, err := s.store.GetAgentBySlug(ctx, args[0])
	if err != nil {
		list, _ := s.store.ListAgents(ctx)
		for _, candidate := range list {
			if strings.EqualFold(candidate.Name, args[0]) {
				ag, err = candidate, nil
				break
			}
		}
		if err != nil {
			_ = s.reply(ctx, in, fmt.Sprintf("I couldn't find an agent named %q.", args[0]))
			return nil
		}
	}
	if _, err := s.SwitchAgent(ctx, conv.ID, ag.ID); err != nil {
		_ = s.reply(ctx, in, "Could not switch agent: "+shortErr(err))
		return err
	}
	_ = s.reply(ctx, in, "Switched to "+ag.Name+". The next message will include a recap of this conversation.")
	return nil
}

func (s *Service) commandModel(ctx context.Context, in channels.Inbound, conv store.Conversation, hasConv bool, args []string) error {
	if !hasConv {
		_ = s.reply(ctx, in, "Start or join a conversation before choosing a model.")
		return nil
	}
	opts, _ := s.ConversationConfig(ctx, conv.ID)
	var modelOpt ConfigOption
	found := false
	for _, o := range opts {
		if strings.EqualFold(o.Category, "model") {
			modelOpt, found = o, true
			break
		}
	}
	if len(args) == 0 {
		current := conv.Model
		if current == "" && found {
			current = configValueString(modelOpt.CurrentValue)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Current model: %s\n", fallback(current, "agent default"))
		if found && len(modelOpt.Options) > 0 {
			b.WriteString("\nAvailable models:\n")
			for i, c := range modelOpt.Options {
				if i >= 18 {
					b.WriteString("…open the web UI to see all models.\n")
					break
				}
				name := c.Name
				if name == "" {
					name = c.Value
				}
				mark := ""
				if c.Value == current {
					mark = " ←"
				}
				fmt.Fprintf(&b, "• %s (%s)%s\n", name, c.Value, mark)
			}
		}
		b.WriteString("\nUse /model <value> to change it.")
		_ = s.reply(ctx, in, b.String())
		return nil
	}
	updated, err := s.SetModel(ctx, conv.ID, args[0])
	if err != nil {
		_ = s.reply(ctx, in, "Could not change model: "+shortErr(err))
		return err
	}
	_ = s.reply(ctx, in, "Model set to "+fallback(updated.Model, args[0])+".")
	return nil
}

func (s *Service) commandStatus(ctx context.Context, in channels.Inbound, conv store.Conversation, hasConv bool) error {
	if !hasConv {
		_ = s.reply(ctx, in, "This chat is not linked to a NusaMation conversation yet.")
		return nil
	}
	status := conv.Status
	if a := s.activeFor(conv.ID); a != nil {
		a.mu.Lock()
		turnStatus := a.turn.Status
		a.mu.Unlock()
		status = "turn " + turnStatus
	}
	text := fmt.Sprintf("Agent: %s\nModel: %s\nConversation: %s\nStatus: %s\nMessages: %d\nPending approvals: %d",
		conv.AgentName, fallback(conv.Model, "agent default"), conv.Title, status, conv.MessageCount, conv.PendingCount)
	_ = s.reply(ctx, in, text)
	return nil
}

func (s *Service) commandApproval(ctx context.Context, in channels.Inbound, conv store.Conversation, hasConv bool, cmd string, args []string) error {
	if len(args) == 0 {
		_ = s.reply(ctx, in, "Usage: /"+cmd+" <approval-id>")
		return nil
	}
	if !hasConv {
		_ = s.reply(ctx, in, "This chat is not linked to a conversation.")
		return nil
	}
	ap, err := s.store.GetApproval(ctx, strings.TrimPrefix(args[0], "#"))
	if err != nil {
		_ = s.reply(ctx, in, "I couldn't find that approval.")
		return nil
	}
	if ap.ConversationID != conv.ID {
		_ = s.reply(ctx, in, "That approval belongs to a different conversation.")
		return nil
	}
	// Only the user whose prompt created this turn may answer its approvals.
	// Older turns without a recorded sender keep the previous behavior.
	if turn, err := s.store.GetTurn(ctx, ap.TurnID); err == nil {
		if ref := turnOriginSender(turn); ref != "" && in.SenderID != "" && ref != in.SenderID {
			_ = s.reply(ctx, in, "Only the person who sent that request can approve it.")
			return nil
		}
	}
	optionID := ""
	if cmd == "deny" {
		for _, o := range ap.Options {
			k := strings.ToLower(o.Kind)
			if k == "reject_once" {
				optionID = o.OptionID
				break
			}
			if k == "reject_always" && optionID == "" {
				optionID = o.OptionID
			}
		}
	} else {
		for _, o := range ap.Options {
			k := strings.ToLower(o.Kind)
			if k == "allow_once" {
				optionID = o.OptionID
				break
			}
			if k == "allow_always" && optionID == "" {
				optionID = o.OptionID
			}
		}
	}
	updated, err := s.ResolveApproval(ctx, ap.ID, optionID)
	if err != nil {
		_ = s.reply(ctx, in, "Could not answer that approval: "+shortErr(err))
		return err
	}
	_ = s.reply(ctx, in, "Approval "+updated.ID+" is now "+updated.Status+".")
	return nil
}

// turnOriginSender extracts the recorded origin sender of a turn, used to
// gate who may answer its approvals (slash command or poll vote).
func turnOriginSender(turn store.Turn) string {
	if turn.OriginRef == "" {
		return ""
	}
	var ref struct {
		SenderID string `json:"senderId"`
	}
	_ = json.Unmarshal([]byte(turn.OriginRef), &ref)
	return ref.SenderID
}

func parseCommand(text string) (string, []string) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return "", nil
	}
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "", nil
	}
	cmd := strings.ToLower(strings.TrimPrefix(fields[0], "/"))
	if i := strings.IndexByte(cmd, '@'); i >= 0 {
		cmd = cmd[:i]
	}
	return cmd, fields[1:]
}

func linkRefFromInbound(in channels.Inbound) string {
	b, _ := json.Marshal(map[string]any{
		"externalId": in.ExternalID, "threadId": in.ThreadID,
		"displayName": in.ChatName, "isGroup": in.IsGroup, "senderId": in.SenderID,
		"messageId": in.MessageID,
	})
	return string(b)
}

func channelLabel(name string) string {
	switch name {
	case "whatsapp":
		return "WhatsApp"
	case "telegram":
		return "Telegram"
	case "discord":
		return "Discord"
	default:
		if name == "" {
			return "Chat"
		}
		return strings.ToUpper(name[:1]) + name[1:]
	}
}

func helpText() string {
	return `NusaMation — your ACP agent, everywhere.

Commands:
/new [agent] — start a fresh conversation
/agent [slug] — list or switch agents
/model [value] — list or change the model
/status — agent, model, turn and approval status
/stop — cancel the running turn
/retry — retry the last message
/approve <id> — allow a pending tool call
/deny <id> — reject a pending tool call

Plain messages are sent to the agent. In groups, mention the bot based on the channel's group policy. The web control center always shows the complete stream, including thoughts and tool calls; chat channels receive the final answer only.`
}

func shortErr(err error) string {
	if err == nil {
		return ""
	}
	return textfmt.Truncate(strings.TrimSpace(err.Error()), 400)
}

func fallback(s, alt string) string {
	if strings.TrimSpace(s) != "" {
		return s
	}
	return alt
}
