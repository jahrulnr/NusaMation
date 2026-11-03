package chat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jahrulnr/nusamation/internal/acp"
	"github.com/jahrulnr/nusamation/internal/agent"
	"github.com/jahrulnr/nusamation/internal/channels"
	"github.com/jahrulnr/nusamation/internal/store"
	"github.com/jahrulnr/nusamation/internal/textfmt"
)

func (s *Service) processTurn(turn store.Turn) {
	s.mu.Lock()
	timeout := s.turnTimeout
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	conv, err := s.store.GetConversation(ctx, turn.ConversationID)
	if err != nil {
		s.failOrphanTurn(turn, err)
		return
	}
	ag, err := s.store.GetAgent(ctx, conv.AgentID)
	if err != nil {
		s.failOrphanTurn(turn, err)
		return
	}
	user, err := s.store.GetMessage(ctx, turn.UserMessageID)
	if err != nil {
		s.failOrphanTurn(turn, err)
		return
	}
	a := newActiveTurn(conv, turn, ag, user, ctx, cancel)
	s.setActive(a)
	go s.watchTurn(a)

	_ = s.store.StartTurn(ctx, turn.ID)
	turn.Status = "running"
	turn.StartedAt = time.Now().UnixMilli()
	a.mu.Lock()
	a.turn.Status = "running"
	a.turn.StartedAt = turn.StartedAt
	a.mu.Unlock()
	_ = s.store.SetConversationStatus(ctx, conv.ID, "active")
	s.emit("turn.started", conv.ID, map[string]any{"turn": turn, "conversationId": conv.ID})

	go s.flushLoop(a)
	s.startDelivery(a)

	status, stopReason, runErr := s.runPrompt(a)
	a.endPrompt()
	s.finishTurn(a, status, stopReason, runErr)
}

func (s *Service) failOrphanTurn(turn store.Turn, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	msg := store.Message{
		ConversationID: turn.ConversationID, TurnID: turn.ID, Role: "system", Kind: "error",
		Status: "error", Text: "Could not start this turn: " + err.Error(),
	}
	_ = s.store.AppendMessage(ctx, &msg)
	s.emit("message.created", turn.ConversationID, msg)
	_ = s.store.FinishTurn(ctx, turn.ID, "error", "", err.Error())
	_ = s.store.SetConversationStatus(ctx, turn.ConversationID, "error")
	updated, _ := s.store.GetTurn(ctx, turn.ID)
	s.emit("turn.completed", turn.ConversationID, map[string]any{
		"turn": updated, "status": "error", "error": err.Error(),
	})
	s.log.Error("failed to start turn", "conversation", turn.ConversationID, "turn", turn.ID, "error", err)
}

func (s *Service) runPrompt(a *activeTurn) (status, stopReason string, runErr error) {
	defer func() {
		if r := recover(); r != nil {
			runErr = fmt.Errorf("internal error while running the agent: %v", r)
			status = "error"
		}
	}()

	req := s.sessionRequest(a.conv, a.agent)
	meta, _ := s.metaMap(a.conv)

	session, err := s.agents.EnsureSession(a.ctx, req)
	if err != nil {
		if a.isCancelRequested() {
			return "cancelled", "cancelled", nil
		}
		if authErr := new(agent.AuthRequiredError); errors.As(err, &authErr) {
			_ = s.store.SetConversationStatus(context.Background(), a.conversationID, "needs_auth")
			return "error", "", fmt.Errorf("this agent needs authentication: %w", err)
		}
		return "error", "", err
	}
	a.mu.Lock()
	a.session = session
	a.mu.Unlock()

	recap := ""
	needsRecap := session.Recovered()
	if v, _ := meta["handoff"].(bool); v {
		needsRecap = true
	}
	if needsRecap {
		recap = s.buildRecap(a)
		a.recapApplied = recap != ""
		session.ClearRecovered()
		if _, err := s.patchMeta(context.Background(), a.conversationID, map[string]any{"handoff": false}); err == nil {
			a.mu.Lock()
			a.conv, _ = s.store.GetConversation(context.Background(), a.conversationID)
			a.mu.Unlock()
		}
	}

	blocks := s.buildPrompt(a, recap)
	if len(blocks) == 0 {
		return "error", "", errors.New("nothing to send to the agent")
	}
	if !a.beginPrompt() {
		return "cancelled", "cancelled", nil
	}
	result, err := s.agents.Prompt(acp.WithRequestSent(a.ctx, a.markPromptSent), session, blocks)
	if err != nil {
		if a.ctx.Err() == context.DeadlineExceeded && isClosed(a.promptSent) {
			s.dropTimedOutSession(a)
		}
		if a.isCancelRequested() {
			return "cancelled", "cancelled", nil
		}
		if authErr := new(agent.AuthRequiredError); errors.As(err, &authErr) {
			return "error", "", fmt.Errorf("agent authentication expired: %w", err)
		}
		return "error", "", err
	}
	if len(result.Usage) > 0 {
		s.recordPromptUsage(a, result.Usage)
	}
	stop := result.StopReason
	if a.isCancelRequested() || stop == "cancelled" {
		return "cancelled", "cancelled", nil
	}
	if stop == "" {
		stop = "end_turn"
	}
	return "completed", stop, nil
}

// PrepareSession discovers the current agent's options before the first prompt.
func (s *Service) PrepareSession(ctx context.Context, conversationID string) error {
	conv, err := s.store.GetConversation(ctx, conversationID)
	if err != nil {
		return err
	}
	ag, err := s.store.GetAgent(ctx, conv.AgentID)
	if err != nil {
		return err
	}
	_, err = s.agents.EnsureSession(ctx, s.sessionRequest(conv, ag))
	return err
}

func (s *Service) sessionRequest(conv store.Conversation, ag store.Agent) agent.EnsureRequest {
	cwd := conv.Cwd
	if cwd == "" {
		cwd = ag.Cwd
	}
	if cwd == "" {
		cwd = s.workspace
	}
	if st, err := os.Stat(cwd); err != nil || !st.IsDir() {
		if s.workspace != "" {
			cwd = s.workspace
		}
	}
	meta, _ := s.metaMap(conv)
	var defaultConfig []store.ConfigSelection
	if raw, ok := meta["defaultConfig"]; ok {
		b, _ := json.Marshal(raw)
		_ = json.Unmarshal(b, &defaultConfig)
	}
	desired := map[string]any{}
	if raw, ok := meta["desiredConfig"].(map[string]any); ok {
		for k, v := range raw {
			desired[k] = v
		}
	}
	model := conv.Model
	if model == "" {
		model = ag.DefaultModel
	}
	cached := conv.ConfigOptions
	if len(ParseConfigOptions(cached)) == 0 {
		cached = ag.ConfigOptions
	}
	req := agent.EnsureRequest{
		AgentID:        ag.ID,
		ConversationID: conv.ID,
		Cwd:            cwd,
		ACPSessionID:   conv.ACPSessionID,
		DesiredModel:   model,
		DesiredConfig:  desired,
		DefaultConfig:  defaultConfig,
		CachedConfig:   cached,
	}
	if dirs, ok := meta["additionalDirectories"].([]any); ok {
		for _, d := range dirs {
			if s, ok := d.(string); ok && s != "" {
				req.AdditionalDirs = append(req.AdditionalDirs, s)
			}
		}
	}

	return req
}

func (s *Service) buildRecap(a *activeTurn) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	msgs, err := s.store.RecentTranscript(ctx, a.conversationID, 40)
	if err != nil {
		return ""
	}
	filtered := msgs[:0]
	for _, m := range msgs {
		if m.ID == a.user.ID {
			continue
		}
		if m.Kind == "tool" {
			continue
		}
		if strings.TrimSpace(m.Text) == "" {
			continue
		}
		filtered = append(filtered, m)
	}
	if len(filtered) == 0 {
		return ""
	}
	return store.BuildTranscriptRecap(filtered, 14000)
}

func (s *Service) buildPrompt(a *activeTurn, recap string) []acp.ContentBlock {
	var blocks []acp.ContentBlock
	if strings.TrimSpace(recap) != "" {
		blocks = append(blocks, acp.ContentBlock{Type: "text", Text: recap})
	}
	text := strings.TrimSpace(a.user.Text)
	if text == "" {
		text = "Please review the attachment(s) and continue."
	}
	blocks = append(blocks, acp.ContentBlock{Type: "text", Text: text})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	uploads, err := s.store.ListUploadsForMessage(ctx, a.user.ID)
	if err != nil || len(uploads) == 0 {
		return blocks
	}
	caps := s.agents.PromptCapabilities(a.agent.ID)
	var textFiles, skipped []string
	total := int64(0)
	for _, u := range uploads {
		if total > 40<<20 {
			skipped = append(skipped, u.Name+" (total attachment budget exceeded)")
			continue
		}
		data, err := os.ReadFile(u.Path)
		if err != nil {
			skipped = append(skipped, u.Name+" (unreadable)")
			continue
		}
		total += int64(len(data))
		mime := u.MIME
		if mime == "" {
			mime = "application/octet-stream"
		}
		switch {
		case strings.HasPrefix(mime, "image/"):
			if !caps.Image {
				skipped = append(skipped, u.Name+" (the active model does not accept images)")
				continue
			}
			blocks = append(blocks, acp.ContentBlock{
				Type: "image", MIMEType: mime, Data: base64.StdEncoding.EncodeToString(data),
			})
		case isTextMIME(mime, u.Name):
			body := string(data)
			if len(body) > 180000 {
				body = body[:180000] + "\n\n[truncated]"
			}
			if caps.EmbeddedContext {
				blocks = append(blocks, acp.ContentBlock{
					Type: "resource",
					Resource: &acp.EmbeddedResource{
						URI: "file://" + filepath.ToSlash(u.Path), MIMEType: mime, Text: body,
					},
				})
			} else {
				textFiles = append(textFiles, fmt.Sprintf("### %s\n```\n%s\n```", u.Name, body))
			}
		default:
			abs, _ := filepath.Abs(u.Path)
			blocks = append(blocks, acp.ContentBlock{
				Type: "resource_link", URI: "file://" + filepath.ToSlash(abs),
				Name: u.Name, MIMEType: mime, Size: u.Size,
			})
		}
	}
	if len(textFiles) > 0 {
		blocks = append(blocks, acp.ContentBlock{
			Type: "text", Text: "Attached text files:\n\n" + strings.Join(textFiles, "\n\n"),
		})
	}
	if len(skipped) > 0 {
		blocks = append(blocks, acp.ContentBlock{
			Type: "text", Text: "Attachments not sent to the model: " + strings.Join(skipped, "; "),
		})
	}
	return blocks
}

func isTextMIME(mime, name string) bool {
	if strings.HasPrefix(mime, "text/") {
		return true
	}
	switch mime {
	case "application/json", "application/xml", "application/yaml", "application/x-yaml",
		"application/javascript", "application/typescript", "application/toml",
		"application/x-sh", "application/sql":
		return true
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".md", ".txt", ".json", ".yaml", ".yml", ".toml", ".go", ".js", ".ts", ".tsx", ".jsx",
		".py", ".rs", ".java", ".kt", ".c", ".h", ".cpp", ".hpp", ".cs", ".rb", ".php", ".sh",
		".css", ".html", ".xml", ".sql", ".env", ".ini", ".conf", ".log":
		return true
	}
	return false
}

func (s *Service) recordPromptUsage(a *activeTurn, raw json.RawMessage) {
	var meta struct {
		TotalTokens  int64 `json:"totalTokens"`
		InputTokens  int64 `json:"inputTokens"`
		OutputTokens int64 `json:"outputTokens"`
	}
	if json.Unmarshal(raw, &meta) != nil {
		return
	}
	a.mu.Lock()
	usage := a.conv.Usage
	if meta.TotalTokens > 0 {
		usage.TotalTokens = meta.TotalTokens
	}
	if meta.InputTokens > 0 {
		usage.InputTokens = meta.InputTokens
	}
	if meta.OutputTokens > 0 {
		usage.OutputTokens = meta.OutputTokens
	}
	a.conv.Usage = usage
	a.mu.Unlock()
	_ = s.store.SetConversationUsage(context.Background(), a.conversationID, usage)
	s.emit("usage.updated", a.conversationID, map[string]any{
		"conversationId": a.conversationID, "usage": usage,
	})
}

func (s *Service) startDelivery(a *activeTurn) {
	if a.turn.OriginChannel == "" || a.turn.OriginChannel == "web" {
		return
	}
	a.mu.Lock()
	routing := a.conv.Routing
	a.mu.Unlock()
	if !routing.ReplyToOrigin {
		return
	}
	ch, target, ok := s.originChannel(a)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	d, err := ch.Begin(ctx, target, "On it…")
	if err != nil {
		s.log.Warn("failed to start channel delivery", "channel", ch.Name(), "error", err)
		return
	}
	a.setDelivery(d)
}

func (s *Service) originChannel(a *activeTurn) (channels.Channel, channels.Target, bool) {
	if s.channels == nil {
		return nil, channels.Target{}, false
	}
	ch, ok := s.channels.Get(a.turn.OriginChannel)
	if !ok {
		return nil, channels.Target{}, false
	}
	var ref struct {
		ExternalID  string `json:"externalId"`
		ThreadID    string `json:"threadId"`
		DisplayName string `json:"displayName"`
		IsGroup     bool   `json:"isGroup"`
	}
	_ = json.Unmarshal([]byte(a.turn.OriginRef), &ref)
	for _, l := range a.conv.Links {
		if l.Channel != a.turn.OriginChannel {
			continue
		}
		if ref.ExternalID == "" || (l.ExternalID == ref.ExternalID && (ref.ThreadID == "" || l.ThreadID == ref.ThreadID)) {
			return ch, targetFromLink(l), true
		}
	}
	if ref.ExternalID != "" {
		return ch, channels.Target{
			Channel: a.turn.OriginChannel, ExternalID: ref.ExternalID, ThreadID: ref.ThreadID,
			DisplayName: ref.DisplayName, IsGroup: ref.IsGroup,
		}, true
	}
	if a.turn.OriginRef != "" {
		return ch, channels.Target{Channel: a.turn.OriginChannel, ExternalID: a.turn.OriginRef}, true
	}
	return nil, channels.Target{}, false
}

func (s *Service) mirrorFinal(a *activeTurn) {
	a.mu.Lock()
	routing := a.conv.Routing
	links := append([]store.ChannelLink(nil), a.conv.Links...)
	a.mu.Unlock()
	if !routing.MirrorFinal || s.channels == nil || len(links) == 0 {
		return
	}
	digest := s.finalDigest(a)
	if strings.TrimSpace(digest) == "" {
		return
	}
	for _, l := range links {
		if l.Channel == a.turn.OriginChannel {
			continue
		}
		ch, ok := s.channels.Get(l.Channel)
		if !ok {
			continue
		}
		target := targetFromLink(l)
		go func(ch channels.Channel, target channels.Target) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if err := ch.Notify(ctx, target, textfmt.Truncate(digest, 3000)); err != nil {
				s.log.Warn("failed to mirror final answer", "channel", ch.Name(), "error", err)
			}
		}(ch, target)
	}
}
