package chat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
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

	// Claim the turn before loading anything it depends on: a queued-cancel
	// racing this worker must win cleanly instead of letting the loads
	// fail and be reported as a start error.
	started, err := s.store.StartTurn(ctx, turn.ID)
	if err != nil {
		s.log.Warn("failed to mark turn running", "turn", turn.ID, "error", err)
	}
	if err == nil && !started {
		// The turn left 'queued' before this worker claimed it — a cancel
		// won the race and already emitted the turn's terminal event.
		return
	}
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
	if cur, gerr := s.store.GetTurn(ctx, turn.ID); gerr == nil && cur.Status != "queued" && cur.Status != "running" {
		// A cancel or another worker already finished it — do not clobber the
		// terminal status or append a spurious error message.
		s.log.Warn("skipping orphan failure on finished turn", "conversation", turn.ConversationID, "turn", turn.ID, "status", cur.Status)
		return
	}
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

	a.mu.Lock()
	conv := a.conv
	a.mu.Unlock()
	req := s.sessionRequest(a.ctx, conv, a.agent)
	meta, _ := s.metaMap(conv)

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
	command := promptIsCommand(a.user.Text, referencesFromMessage(a.user))
	if needsRecap && !command {
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
	if !command {
		blocks = s.withTurnContext(a, blocks)
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
// A root reset that lands while EnsureSession runs defers its drop until this
// returns, the same as a running turn.
func (s *Service) PrepareSession(ctx context.Context, conversationID string) error {
	done := s.beginPrepare(conversationID)
	defer done()
	conv, err := s.store.GetConversation(ctx, conversationID)
	if err != nil {
		return err
	}
	ag, err := s.store.GetAgent(ctx, conv.AgentID)
	if err != nil {
		return err
	}
	_, err = s.agents.EnsureSession(ctx, s.sessionRequest(ctx, conv, ag))
	return err
}

func (s *Service) sessionRequest(ctx context.Context, conv store.Conversation, ag store.Agent) agent.EnsureRequest {
	cwd := conv.Cwd
	var projectDirs []string
	useProject := false
	if conv.ProjectID != "" && s.store != nil {
		if project, err := s.store.GetProject(ctx, conv.ProjectID); err == nil && len(project.RootPaths) > 0 {
			cwd = project.RootPaths[0]
			projectDirs = append([]string{}, project.RootPaths[1:]...)
			useProject = true
		}
	}
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
		maps.Copy(desired, raw)
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
	if useProject {
		if projectDirs == nil {
			projectDirs = []string{}
		}
		req.AdditionalDirs = projectDirs
	} else if dirs, ok := meta["additionalDirectories"].([]any); ok {
		for _, d := range dirs {
			if dir, ok := d.(string); ok && dir != "" {
				req.AdditionalDirs = append(req.AdditionalDirs, dir)
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
	text, referenceBlocks, _ := promptReferences(strings.TrimSpace(a.user.Text), referencesFromMessage(a.user))
	if text == "" {
		text = "Please review the attachment(s) and continue."
	}
	blocks = append(blocks, acp.ContentBlock{Type: "text", Text: text})
	blocks = append(blocks, referenceBlocks...)
	if block := annotationBlock(a.user.Metadata); block != "" {
		blocks = append(blocks, acp.ContentBlock{Type: "text", Text: block})
	}

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
		case strings.HasPrefix(mime, "audio/") && caps.Audio:
			blocks = append(blocks, acp.ContentBlock{
				Type: "audio", MIMEType: mime, Data: base64.StdEncoding.EncodeToString(data),
			})
		case isTextMIME(mime, u.Name):
			body := string(data)
			if caps.EmbeddedContext {
				blocks = append(blocks, acp.ContentBlock{
					Type: "resource",
					Resource: &acp.EmbeddedResource{
						URI: fileURI(u.Path), MIMEType: mime, Text: body,
					},
				})
			} else {
				textFiles = append(textFiles, fmt.Sprintf("### %s\n```\n%s\n```", u.Name, body))
			}
		default:
			abs, _ := filepath.Abs(u.Path)
			blocks = append(blocks, acp.ContentBlock{
				Type: "resource_link", URI: fileURI(abs),
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

// annotationBlock serializes metadata.annotations into the contract §7 text
// block: one numbered line per marker plus Comment/Nearby/Layout/Style lines.
// Marker numbers match the badge drawn on each pin's full-viewport screenshot.
func annotationBlock(metadata json.RawMessage) string {
	if len(metadata) == 0 {
		return ""
	}
	var meta struct {
		Annotations []store.MessageAnnotation `json:"annotations"`
	}
	if json.Unmarshal(metadata, &meta) != nil || len(meta.Annotations) == 0 {
		return ""
	}
	trunc := func(s string, n int) string {
		return textfmt.Truncate(strings.TrimSpace(s), n)
	}
	// Page-controlled strings can carry backticks/newlines that would break
	// out of the inline-code spans and indented lines below.
	codeSafe := strings.NewReplacer("`", "'", "\r", " ", "\n", " ").Replace
	var b strings.Builder
	b.WriteString("<annotation-context>\n")
	b.WriteString("The user marked elements on the live page in their browser pane. Each marker's full-viewport screenshot is attached in order; the numbered badge [N] and highlight ring in the image identify the element.\n")
	b.WriteString("This is context, not user text. To inspect or act on the live page — read its DOM, verify a change, click through — use the browser_use tools (browser_snapshot returns element refs).\n")
	used := map[int]bool{}
	nextFree := 0
	for i := range meta.Annotations {
		an := meta.Annotations[i]
		d := an.Descriptor
		label := d.Name
		if label == "" {
			label = d.ImmediateText
		}
		label = trunc(label, 80)
		marker := an.Marker
		if marker <= 0 || used[marker] {
			marker = 0
		}
		if marker == 0 {
			for {
				nextFree++
				if !used[nextFree] {
					marker = nextFree
					break
				}
			}
		}
		used[marker] = true
		b.WriteString("\n[")
		b.WriteString(strconv.Itoa(marker))
		b.WriteString("] <")
		b.WriteString(trunc(d.TagName, 40))
		b.WriteString(">")
		if label != "" {
			b.WriteString(" \"")
			b.WriteString(label)
			b.WriteString("\"")
		}
		if sel := trunc(codeSafe(d.Selector), 240); sel != "" {
			b.WriteString(" — selector `")
			b.WriteString(sel)
			b.WriteString("`")
		}
		if pageURL := trunc(d.PageURL, 300); pageURL != "" {
			b.WriteString(" — ")
			b.WriteString(pageURL)
		}
		if an.OmitScreenshot {
			b.WriteString(" (no screenshot)")
		}
		if comment := strings.TrimSpace(an.Comment); comment != "" {
			b.WriteString("\n    Comment: ")
			b.WriteString(comment)
		}
		if nearby := trunc(d.NearbyText, 300); nearby != "" {
			b.WriteString("\n    Nearby text: ")
			b.WriteString(nearby)
		}
		if d.Viewport.Width > 0 {
			layout := fmt.Sprintf("%.0f,%.0f %.0f×%.0f px · picked at %.0f,%.0f · viewport %.0f×%.0f @%gx",
				d.Rect.X, d.Rect.Y, d.Rect.Width, d.Rect.Height,
				d.Point.X, d.Point.Y,
				d.Viewport.Width, d.Viewport.Height, d.Viewport.DPR)
			if d.ScrollY > 0 || d.ScrollX > 0 {
				layout += fmt.Sprintf(" · scroll %.0f,%.0f", d.ScrollX, d.ScrollY)
			}
			b.WriteString("\n    Layout: ")
			b.WriteString(layout)
		}
		if h := trunc(codeSafe(d.HTML), 300); h != "" {
			b.WriteString("\n    HTML: `")
			b.WriteString(h)
			b.WriteString("`")
		}
		if len(d.FramePath) > 0 {
			b.WriteString("\n    Frame: inside ")
			b.WriteString(trunc(strings.Join(d.FramePath, " › "), 240))
		}
		if cs := d.Computed; cs.FontSize != "" || cs.Display != "" {
			var parts []string
			if cs.FontSize != "" {
				font := "font " + cs.FontSize
				if cs.LineHeight != "" {
					font += "/" + cs.LineHeight
				}
				parts = append(parts, font)
			}
			if cs.FontFamily != "" {
				parts = append(parts, cs.FontFamily)
			}
			if cs.FontWeight != "" {
				parts = append(parts, "weight "+cs.FontWeight)
			}
			if cs.LetterSpacing != "" && cs.LetterSpacing != "normal" {
				parts = append(parts, "spacing "+cs.LetterSpacing)
			}
			if cs.Color != "" || cs.BackgroundColor != "" {
				parts = append(parts, cs.Color+" on "+cs.BackgroundColor)
			}
			if cs.Display != "" || cs.Position != "" {
				parts = append(parts, cs.Display+"/"+cs.Position)
			}
			b.WriteString("\n    Style: ")
			b.WriteString(trunc(strings.Join(parts, " · "), 280))
		}
		if d.Selector == "" && d.ElementPath != "" {
			b.WriteString("\n    Element path: ")
			b.WriteString(trunc(d.ElementPath, 300))
		}
	}
	b.WriteString("\n</annotation-context>")
	return b.String()
}

func (s *Service) responseRules(a *activeTurn) string {
	if s.channels == nil {
		return ""
	}
	ch, ok := s.channels.Get(s.deliveryChannel(a))
	if !ok {
		return ""
	}
	styler, ok := ch.(channels.ResponseStyler)
	if !ok {
		return ""
	}
	rules := strings.TrimSpace(styler.ResponseRules())
	if rules == "" {
		return ""
	}
	return rules
}

// deliveryChannel resolves the channel this turn's reply is delivered on: the
// origin channel when reply-to-origin applies, otherwise the web UI.
func (s *Service) deliveryChannel(a *activeTurn) string {
	name := a.turn.OriginChannel
	if name == "" || name == "web" {
		return "web"
	}
	a.mu.Lock()
	reply := a.conv.Routing.ReplyToOrigin
	a.mu.Unlock()
	if !reply {
		return "web"
	}
	return name
}

// fileURI builds a proper file:// URL — percent-encoding keeps paths with
// spaces or non-ASCII names valid.
func fileURI(path string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
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
	if s.deliveryChannel(a) == "web" {
		return
	}
	a.mu.Lock()
	a.deliverToChannel = true
	a.mu.Unlock()
	d := s.openDelivery(a, "On it…")
	if d != nil {
		a.setDelivery(d)
	}
}

func (s *Service) openDelivery(a *activeTurn, text string) channels.Delivery {
	ch, target, ok := s.originChannel(a)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	d, err := ch.Begin(ctx, target, text)
	if err != nil {
		s.log.Warn("failed to start channel delivery", "channel", ch.Name(), "error", err)
		return nil
	}
	return d
}

func (s *Service) notifyOrigin(a *activeTurn, text string) {
	_ = s.notifyOriginErr(context.Background(), a, text)
}

func (s *Service) notifyOriginErr(ctx context.Context, a *activeTurn, text string) error {
	ch, target, ok := s.originChannel(a)
	if !ok {
		return ErrNoChannel
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := ch.Notify(ctx, target, text); err != nil {
		s.log.Warn("failed to send channel message", "channel", ch.Name(), "error", err)
		return err
	}
	return nil
}

// NotifyActive sends a progress message to the origin channel of the
// conversation's running turn. It is the report tool's delivery path; the
// normal turn digest still runs once when the turn finishes.
func (s *Service) NotifyActive(ctx context.Context, conversationID, text string) error {
	a := s.activeFor(conversationID)
	if a == nil {
		return ErrNoActiveTurn
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return ErrInvalidPrompt
	}
	if len(text) > 4000 {
		text = text[:4000]
	}
	if s.deliveryChannel(a) == "web" {
		return ErrNoChannel
	}
	return s.notifyOriginErr(ctx, a, text)
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
	a.mu.Lock()
	links := append([]store.ChannelLink(nil), a.conv.Links...)
	a.mu.Unlock()
	for _, l := range links {
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
	if a.turn.OriginChannel == "agent" {
		a.mu.Unlock()
		return
	}
	routing := a.conv.Routing
	links := append([]store.ChannelLink(nil), a.conv.Links...)
	status, terminalError := a.finalStatus, a.finalError
	a.mu.Unlock()
	if !routing.MirrorFinal || s.channels == nil || len(links) == 0 {
		return
	}
	digest := s.finalDigest(a)
	errorText := ""
	if status == "error" {
		errorText = "The agent hit an error: " + terminalError
		if digest == textfmt.Truncate(errorText, 1200) {
			digest = ""
		}
	}
	var ref struct {
		ExternalID string `json:"externalId"`
		ThreadID   string `json:"threadId"`
	}
	_ = json.Unmarshal([]byte(a.turn.OriginRef), &ref)
	digest = cleanDeliveredText(digest, a.outboundRefs())
	if strings.TrimSpace(digest) == "" && errorText == "" {
		return
	}
	for _, l := range links {
		if l.Channel == a.turn.OriginChannel && l.ExternalID == ref.ExternalID &&
			(ref.ThreadID == "" || l.ThreadID == ref.ThreadID) {
			continue
		}
		ch, ok := s.channels.Get(l.Channel)
		if !ok {
			continue
		}
		target := targetFromLink(l)
		go func(ch channels.Channel, target channels.Target) {
			if strings.TrimSpace(digest) != "" {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				if err := ch.Notify(ctx, target, textfmt.Truncate(digest, 3000)); err != nil {
					s.log.Warn("failed to mirror final answer", "channel", ch.Name(), "error", err)
				}
				cancel()
			}
			if errorText != "" {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				if err := ch.Notify(ctx, target, errorText); err != nil {
					s.log.Warn("failed to mirror agent error", "channel", ch.Name(), "error", err)
				}
			}
		}(ch, target)
	}
}
