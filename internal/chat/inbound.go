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
		_ = s.reply(ctx, in, s.helpText(conv, hasConv))
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
	case "mode":
		return s.commandMode(ctx, in, conv, hasConv, args)
	case "set":
		return s.commandSet(ctx, in, conv, hasConv, args)
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
		if strings.HasPrefix(text, "/") {
			// A near-miss of one of our own commands gets a suggestion instead
			// of reaching the agent; anything else — advertised agent commands
			// and unvalidated input alike — is forwarded verbatim.
			if !agentCommandExists(conv, cmd) {
				if suggestion := suggestCommand(cmd); suggestion != "" {
					_ = s.reply(ctx, in, fmt.Sprintf("Unknown command /%s — did you mean /%s?", cmd, suggestion))
					return nil
				}
			}
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
		// A message while a turn is running has to reach the agent now.
		// Queueing hides a correction until the turn finishes.
		Delivery: deliverySteer,
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
		_ = s.reply(ctx, in, fmt.Sprintf("I couldn't find an agent named %q.", args[0]))
		return nil
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
	modelOpt, found := configByCategory(opts, "model")
	if len(args) == 0 {
		current := conv.Model
		if current == "" && found {
			current = configValueString(modelOpt.CurrentValue)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Current: %s\n", fallback(current, "agent default"))
		if found {
			choices := flattenChoices(modelOpt.Options)
			const maxList = 15
			for i, c := range choices {
				if i >= maxList {
					fmt.Fprintf(&b, "%d more, refine with /model <text>\n", len(choices)-maxList)
					break
				}
				b.WriteString(c.Value + "\n")
			}
		}
		if ex := modifierExample(opts); ex != "" {
			fmt.Fprintf(&b, "\nCombine with options: /model %s[%s]\n", fallback(current, "<value>"), ex)
		}
		b.WriteString("\nUse /model <value>[key=value] to change it.")
		_ = s.reply(ctx, in, b.String())
		return nil
	}
	raw := strings.Join(args, " ")
	if found {
		var cands []ConfigChoice
		for _, c := range flattenChoices(modelOpt.Options) {
			if strings.Contains(strings.ToLower(c.Value), strings.ToLower(raw)) {
				cands = append(cands, c)
			}
		}
		if len(cands) == 1 {
			res, err := s.SetSelector(ctx, conv.ID, cands[0].Value)
			return s.replySelector(ctx, in, res, err, "Model set to "+fallback(res.Model, cands[0].Value)+".")
		}
		if len(cands) > 1 {
			_ = s.reply(ctx, in, candidateList(raw, cands))
			return nil
		}
	}
	res, err := s.SetSelector(ctx, conv.ID, raw)
	return s.replySelector(ctx, in, res, err, "")
}

// replySelector turns a SetSelector outcome into the channel reply: ambiguity
// lists candidates, errors are reported, and partial results name exactly what
// landed and what did not.
func (s *Service) replySelector(ctx context.Context, in channels.Inbound, res SelectorResult, err error, okText string) error {
	if len(res.Candidates) > 0 {
		_ = s.reply(ctx, in, candidateList("", res.Candidates))
		return nil
	}
	if err != nil {
		_ = s.reply(ctx, in, "Could not change model: "+shortErr(err))
		return err
	}
	if okText == "" {
		okText = formatSelectorResult(res)
	}
	_ = s.reply(ctx, in, okText)
	return nil
}

func formatSelectorResult(res SelectorResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Model set to %s.", res.Model)
	for _, m := range res.Applied {
		fmt.Fprintf(&b, " %s=%s applied.", m.Key, m.Value)
	}
	for _, f := range res.Failed {
		fmt.Fprintf(&b, " %s=%s %s", f.Modifier.Key, f.Modifier.Value, f.Reason)
		if len(f.Offers) > 0 {
			fmt.Fprintf(&b, " — offers: %s", strings.Join(f.Offers, ", "))
		}
		b.WriteString(".")
	}
	return b.String()
}

func candidateList(raw string, cands []ConfigChoice) string {
	var b strings.Builder
	if raw != "" {
		fmt.Fprintf(&b, "More than one option matches %q:\n", raw)
	} else {
		b.WriteString("More than one option matches:\n")
	}
	for _, c := range cands {
		b.WriteString(c.Value + "\n")
	}
	b.WriteString("Refine with /model <text>.")
	return b.String()
}

// modifierExample builds a key=value hint from a real non-model option, so
// the /model help line teaches syntax that exists on this agent.
func modifierExample(opts []ConfigOption) string {
	for _, o := range opts {
		if strings.EqualFold(o.Category, "model") || o.ID == "" {
			continue
		}
		if choices := flattenChoices(o.Options); len(choices) > 0 {
			return o.ID + "=" + choices[len(choices)-1].Value
		}
		if strings.EqualFold(o.Type, "boolean") {
			return o.ID + "=true"
		}
	}
	return ""
}

func (s *Service) commandMode(ctx context.Context, in channels.Inbound, conv store.Conversation, hasConv bool, args []string) error {
	if !hasConv {
		_ = s.reply(ctx, in, "Start or join a conversation before changing the mode.")
		return nil
	}
	opts, _ := s.ConversationConfig(ctx, conv.ID)
	opt, ok := ResolveModifierKey(opts, "mode")
	if !ok {
		_ = s.reply(ctx, in, "This agent has no mode option.")
		return nil
	}
	if len(args) == 0 {
		_ = s.reply(ctx, in, optionListing(opt, "/mode <value>"))
		return nil
	}
	return s.setOptionValue(ctx, in, conv, opt, strings.Join(args, " "))
}

func (s *Service) commandSet(ctx context.Context, in channels.Inbound, conv store.Conversation, hasConv bool, args []string) error {
	if !hasConv {
		_ = s.reply(ctx, in, "Start or join a conversation before changing options.")
		return nil
	}
	opts, _ := s.ConversationConfig(ctx, conv.ID)
	if len(args) == 0 {
		var b strings.Builder
		b.WriteString("Options:\n")
		for _, o := range opts {
			fmt.Fprintf(&b, "%s — %s\n", o.ID, configValueString(o.CurrentValue))
		}
		b.WriteString("\nUse /set <key> <value>. Keys match an option's id, category or name.")
		_ = s.reply(ctx, in, b.String())
		return nil
	}
	opt, ok := ResolveModifierKey(opts, args[0])
	if !ok {
		_ = s.reply(ctx, in, fmt.Sprintf("I don't know an option called %q.", args[0]))
		return nil
	}
	if len(args) == 1 {
		_ = s.reply(ctx, in, optionListing(opt, "/set "+opt.ID+" <value>"))
		return nil
	}
	return s.setOptionValue(ctx, in, conv, opt, strings.Join(args[1:], " "))
}

func (s *Service) setOptionValue(ctx context.Context, in channels.Inbound, conv store.Conversation, opt ConfigOption, raw string) error {
	value, offers := ResolveModifierValue(opt, raw)
	if offers != nil {
		text := fmt.Sprintf("%q is not available for %s", raw, opt.ID)
		if len(offers) > 0 {
			text += " — offers: " + strings.Join(offers, ", ")
		}
		_ = s.reply(ctx, in, text)
		return nil
	}
	v := any(value)
	if strings.EqualFold(opt.Type, "boolean") {
		v = value == "true"
	}
	if _, err := s.SetConfig(ctx, conv.ID, opt.ID, v); err != nil {
		_ = s.reply(ctx, in, "Could not set "+opt.ID+": "+shortErr(err))
		return err
	}
	_ = s.reply(ctx, in, opt.ID+" set to "+value+".")
	return nil
}

func optionListing(opt ConfigOption, usage string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Current: %s\n", fallback(configValueString(opt.CurrentValue), "unset"))
	for _, c := range flattenChoices(opt.Options) {
		b.WriteString(c.Value + "\n")
	}
	fmt.Fprintf(&b, "\nUse %s to change it.", usage)
	return b.String()
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
	if !hasConv {
		_ = s.reply(ctx, in, "This chat is not linked to a conversation.")
		return nil
	}
	if len(args) == 0 {
		list, _ := s.store.ListApprovalsForConversation(ctx, conv.ID, 20)
		var pending []store.Approval
		for _, a := range list {
			if a.Status == "pending" {
				pending = append(pending, a)
			}
		}
		if len(pending) == 0 {
			_ = s.reply(ctx, in, "No pending approvals.")
			return nil
		}
		var b strings.Builder
		b.WriteString("Pending approvals:\n")
		for _, a := range pending {
			fmt.Fprintf(&b, "%s — %s\n", approvalPrefix(a.ID), fallback(a.Title, a.Kind))
		}
		fmt.Fprintf(&b, "\nUse /%s <id> — a unique prefix is enough.", cmd)
		_ = s.reply(ctx, in, b.String())
		return nil
	}
	ref := strings.TrimPrefix(args[0], "#")
	ap, err := s.store.GetApproval(ctx, ref)
	if err != nil {
		list, _ := s.store.ListApprovalsForConversation(ctx, conv.ID, 100)
		var matches []store.Approval
		for _, a := range list {
			if a.Status == "pending" && strings.HasPrefix(a.ID, ref) {
				matches = append(matches, a)
			}
		}
		switch len(matches) {
		case 1:
			ap = matches[0]
		case 0:
			_ = s.reply(ctx, in, "I couldn't find that approval.")
			return nil
		default:
			var b strings.Builder
			fmt.Fprintf(&b, "%q matches more than one pending approval:\n", ref)
			for _, a := range matches {
				fmt.Fprintf(&b, "%s — %s\n", approvalPrefix(a.ID), fallback(a.Title, a.Kind))
			}
			_ = s.reply(ctx, in, b.String())
			return nil
		}
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

// turnIsAutomation reports whether the turn was launched by a scheduler run.
// Such turns record no origin sender, so channel-based answering must stay
// closed — approvals and poll questions belong to the web inbox.
func turnIsAutomation(turn store.Turn) bool {
	if turn.OriginRef == "" {
		return false
	}
	var ref struct {
		Automation bool `json:"automation"`
	}
	_ = json.Unmarshal([]byte(turn.OriginRef), &ref)
	return ref.Automation
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

// approvalPrefix is the short id form shown in channel lists — long enough to
// be unique in practice, short enough to type on a phone.
func approvalPrefix(id string) string {
	if len(id) > 6 {
		return id[:6]
	}
	return id
}

var helpText = map[string]string{
	"new":     "/new [agent] — start a fresh conversation",
	"agent":   "/agent [slug] — list or switch agents",
	"model":   "/model [selector] — list or change the model",
	"mode":    "/mode [value] — list or change the session mode",
	"set":     "/set <key> <value> — change any session option",
	"status":  "/status — agent, model, turn and approval status",
	"stop":    "/stop — cancel the running turn",
	"retry":   "/retry — retry the last message",
	"approve": "/approve [id] — list or allow a pending tool call",
	"deny":    "/deny [id] — reject a pending tool call",
}

var helpOrder = []string{"new", "agent", "model", "mode", "set", "status", "stop", "retry", "approve", "deny"}

// helpText composes the channel help: NusaMation commands, then the commands
// the live agent advertised — but only while they belong to the current ACP
// session, the same guard the web composer uses.
func (s *Service) helpText(conv store.Conversation, hasConv bool) string {
	var b strings.Builder
	b.WriteString("NusaMation — your ACP agent, everywhere.\n\nCommands:\n")
	for _, name := range helpOrder {
		b.WriteString(helpText[name] + "\n")
	}
	if cmds := agentCommandList(conv, hasConv); len(cmds) > 0 {
		b.WriteString("\nAgent commands:\n")
		for _, c := range cmds {
			b.WriteString("/" + c.Name)
			if c.Description != "" {
				b.WriteString(" — " + c.Description)
			}
			if c.Hint != "" {
				b.WriteString(" " + c.Hint)
			}
			b.WriteString("\n")
		}
	}
	b.WriteString("\nPlain messages are sent to the agent. A message sent while the agent is working is steered into that turn, so a correction is visible immediately. In groups, mention the bot based on the channel's group policy. The web control center shows thoughts and tool calls; chat channels receive each assistant reply as it finishes.")
	return b.String()
}

type agentCommand struct {
	Name        string
	Description string
	Hint        string
}

// agentCommandList reads conv.Meta commands defensively — the blob is free-form
// json.RawMessage from the agent — and only when commandsSessionId matches the
// live ACP session, so a stale list never survives an agent switch or restart.
func agentCommandList(conv store.Conversation, hasConv bool) []agentCommand {
	if !hasConv || conv.ACPSessionID == "" {
		return nil
	}
	var meta struct {
		SessionID string          `json:"commandsSessionId"`
		Commands  json.RawMessage `json:"commands"`
	}
	if json.Unmarshal(conv.Metadata, &meta) != nil || meta.SessionID != conv.ACPSessionID {
		return nil
	}
	var raw []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Input       *struct {
			Hint string `json:"hint"`
		} `json:"input"`
	}
	if json.Unmarshal(meta.Commands, &raw) != nil {
		return nil
	}
	out := make([]agentCommand, 0, len(raw))
	for _, c := range raw {
		if c.Name == "" {
			continue
		}
		cmd := agentCommand{Name: c.Name, Description: c.Description}
		if c.Input != nil {
			cmd.Hint = c.Input.Hint
		}
		out = append(out, cmd)
	}
	return out
}

func agentCommandExists(conv store.Conversation, cmd string) bool {
	for _, c := range agentCommandList(conv, true) {
		if c.Name == cmd {
			return true
		}
	}
	return false
}

// suggestCommand returns the closest NusaMation command for a mistyped name —
// close enough to be a typo, never a guess at agent commands.
func suggestCommand(cmd string) string {
	best, bestDist := "", 3
	for _, name := range helpOrder {
		d := editDistance(cmd, name)
		if d < bestDist {
			best, bestDist = name, d
		}
	}
	// Only a real typo qualifies: /modl → /model. Longer words tolerate one
	// more edit; anything further away is probably an agent command.
	if bestDist <= 1 || bestDist <= 2 && len(cmd) >= 5 {
		return best
	}
	return ""
}

func editDistance(a, b string) int {
	ar, br := []rune(a), []rune(b)
	prev := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		cur := make([]int, len(br)+1)
		cur[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			cur[j] = min(cur[j-1]+1, min(prev[j]+1, prev[j-1]+cost))
		}
		prev = cur
	}
	return prev[len(br)]
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
