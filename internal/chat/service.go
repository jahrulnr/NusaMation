package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jahrulnr/nusamation/internal/agent"
	"github.com/jahrulnr/nusamation/internal/channels"
	"github.com/jahrulnr/nusamation/internal/events"
	"github.com/jahrulnr/nusamation/internal/ids"
	"github.com/jahrulnr/nusamation/internal/store"
)

var (
	ErrBusy          = errors.New("the agent is still working in this conversation")
	ErrNoActiveTurn  = errors.New("there is no active turn")
	ErrApprovalGone  = errors.New("this approval is no longer waiting for an answer")
	ErrNoAgent       = errors.New("no enabled agent is configured")
	ErrInvalidPrompt = errors.New("a prompt or attachment is required")
)

type Service struct {
	store     *store.Store
	agents    *agent.Manager
	channels  *channels.Manager
	bus       events.Broadcaster
	log       *slog.Logger
	uploadDir string
	workspace string

	mu            sync.Mutex
	active        map[string]*activeTurn
	running       map[string]bool
	kick          map[string]bool
	waiters       map[string]chan approvalDecision
	elicitations  map[string]*elicitationWaiter
	terminalTurns map[string]*activeTurn

	cancelNoticeAfter time.Duration
	turnTimeout       time.Duration
}

type Options struct {
	Store     *store.Store
	Agents    *agent.Manager
	Channels  *channels.Manager
	Events    events.Broadcaster
	Log       *slog.Logger
	UploadDir string
	Workspace string
}

func New(opts Options) *Service {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Events == nil {
		opts.Events = events.Nop{}
	}
	return &Service{
		store:             opts.Store,
		agents:            opts.Agents,
		channels:          opts.Channels,
		bus:               opts.Events,
		log:               opts.Log.With("component", "chat"),
		uploadDir:         opts.UploadDir,
		workspace:         opts.Workspace,
		active:            map[string]*activeTurn{},
		running:           map[string]bool{},
		kick:              map[string]bool{},
		waiters:           map[string]chan approvalDecision{},
		elicitations:      map[string]*elicitationWaiter{},
		terminalTurns:     map[string]*activeTurn{},
		cancelNoticeAfter: defaultCancelNoticeAfter,
		turnTimeout:       defaultTurnTimeout,
	}
}

// SetChannels allows the channel manager to be wired after construction, which
// avoids a dependency cycle between channel adapters and the chat service.
func (s *Service) SetChannels(m *channels.Manager) {
	s.channels = m
}

func (s *Service) Store() *store.Store { return s.store }

func (s *Service) Broadcast(ev events.Event) {
	if s.bus != nil {
		s.bus.Broadcast(ev)
	}
}

func (s *Service) emit(typ, conversationID string, data any) {
	s.Broadcast(events.New(typ, conversationID, data))
}

func (s *Service) Start(ctx context.Context) error {
	if _, err := s.store.MarkInterruptedTurns(ctx); err != nil {
		return fmt.Errorf("mark interrupted turns: %w", err)
	}
	if _, err := s.store.CancelAllPendingApprovals(ctx, "server restarted"); err != nil {
		return fmt.Errorf("cancel stale approvals: %w", err)
	}
	if _, err := s.store.ResetConversationStatuses(ctx); err != nil {
		return fmt.Errorf("reset conversation statuses: %w", err)
	}
	go s.resumeQueues(ctx)
	return nil
}

func (s *Service) resumeQueues(ctx context.Context) {
	turns, err := s.store.ListQueuedTurns(ctx)
	if err != nil {
		s.log.Warn("failed to load queued turns", "error", err)
		return
	}
	seen := map[string]bool{}
	for _, t := range turns {
		if seen[t.ConversationID] {
			continue
		}
		seen[t.ConversationID] = true
		go s.runQueue(t.ConversationID)
	}
}

type SubmitInput struct {
	ConversationID string                  `json:"conversationId"`
	Text           string                  `json:"text"`
	Attachments    []store.AttachmentInput `json:"attachments,omitempty"`
	Channel        string                  `json:"channel,omitempty"`
	OriginRef      string                  `json:"originRef,omitempty"`
	ReplyToChannel bool                    `json:"replyToChannel,omitempty"`
	ReplyChannel   string                  `json:"replyChannel,omitempty"`
	// Delivery is "" or "queue" (default FIFO) or "after_tools".
	Delivery        string `json:"delivery,omitempty"`
	AutomationRunID string `json:"-"`
}

type messagePayload struct {
	store.Message
	Attachments []store.Upload `json:"attachments,omitempty"`
}

func (s *Service) Submit(ctx context.Context, in SubmitInput) (store.Message, store.Turn, error) {
	switch in.Delivery {
	case "", deliveryQueue, deliveryAfterTools:
	default:
		return store.Message{}, store.Turn{}, ErrInvalidDelivery
	}
	text := strings.TrimSpace(in.Text)
	if text == "" && len(in.Attachments) == 0 {
		return store.Message{}, store.Turn{}, ErrInvalidPrompt
	}
	if len(text) > 200000 {
		text = text[:200000]
	}
	conv, err := s.store.GetConversation(ctx, in.ConversationID)
	if err != nil {
		return store.Message{}, store.Turn{}, err
	}
	if conv.Archived {
		return store.Message{}, store.Turn{}, fmt.Errorf("conversation is archived")
	}
	ag, err := s.store.GetAgent(ctx, conv.AgentID)
	if err != nil {
		return store.Message{}, store.Turn{}, err
	}
	if !ag.Enabled {
		return store.Message{}, store.Turn{}, fmt.Errorf("agent %q is disabled", ag.Name)
	}
	var target *activeTurn
	if in.Delivery == deliveryAfterTools {
		target = s.activeFor(conv.ID)
	}

	channel := strings.TrimSpace(in.Channel)
	if channel == "" {
		channel = "web"
	}
	originChannel, originRef := channel, in.OriginRef
	if in.ReplyToChannel && channel == "web" {
		link, ok := chooseLink(conv.Links, in.ReplyChannel)
		if !ok {
			return store.Message{}, store.Turn{}, fmt.Errorf("this conversation is not linked to an external channel")
		}
		originChannel = link.Channel
		originRef = linkRef(link)
	}

	uploads, err := s.loadUploads(ctx, conv.ID, in.Attachments)
	if err != nil {
		return store.Message{}, store.Turn{}, err
	}
	meta := map[string]any{}
	if len(uploads) > 0 {
		list := make([]map[string]any, 0, len(uploads))
		for _, u := range uploads {
			list = append(list, map[string]any{
				"uploadId": u.ID, "name": u.Name, "mime": u.MIME, "size": u.Size,
			})
		}
		meta["attachments"] = list
	}
	if in.Delivery == deliveryAfterTools {
		meta["delivery"] = deliveryAfterTools
	}
	metaJSON, _ := json.Marshal(meta)
	msg := store.Message{
		ConversationID: conv.ID,
		Role:           "user",
		Kind:           "text",
		Text:           text,
		Status:         "complete",
		Channel:        channel,
		ExternalID:     in.OriginRef,
		Metadata:       metaJSON,
	}
	if err := s.store.AppendMessage(ctx, &msg); err != nil {
		return store.Message{}, store.Turn{}, err
	}
	if len(uploads) > 0 {
		ids := make([]string, 0, len(uploads))
		for _, u := range uploads {
			ids = append(ids, u.ID)
		}
		_ = s.store.BindUploadsToMessage(ctx, ids, conv.ID, msg.ID)
	}

	if conv.MessageCount == 0 && isDefaultTitle(conv.Title) {
		title := autoTitle(text, uploads)
		if title != "" {
			if updated, err := s.store.UpdateConversation(ctx, conv.ID, store.ConversationUpdate{Title: &title}); err == nil {
				conv = updated
				s.emit("conversation.updated", conv.ID, conv)
			}
		}
	}

	var turn store.Turn
	if in.AutomationRunID != "" {
		turn, err = s.store.CreateAutomationTurn(ctx, in.AutomationRunID, conv.ID, msg.ID, originChannel, originRef)
	} else {
		turn, err = s.store.CreateTurn(ctx, conv.ID, msg.ID, originChannel, originRef)
	}
	if err != nil {
		return msg, turn, err
	}
	if target != nil {
		s.armAfterTools(ctx, conv.ID, &msg, turn, target)
	}
	s.emit("message.created", conv.ID, s.hydrateMessage(ctx, msg))
	s.emit("turn.updated", conv.ID, turn)
	s.log.Info("prompt queued", "conversation", conv.ID, "turn", turn.ID, "channel", originChannel)
	go s.runQueue(conv.ID)
	return msg, turn, nil
}

func (s *Service) Retry(ctx context.Context, conversationID, channel, originRef string) (store.Turn, error) {
	conv, err := s.store.GetConversation(ctx, conversationID)
	if err != nil {
		return store.Turn{}, err
	}
	if conv.Archived {
		return store.Turn{}, fmt.Errorf("conversation is archived")
	}
	if channel == "" {
		channel = "web"
	}
	if channel == "web" {
		if _, err := s.store.ActiveTurn(ctx, conversationID); err == nil {
			return store.Turn{}, ErrBusy
		}
	}
	last, err := s.store.LastUserMessage(ctx, conversationID)
	if err != nil {
		return store.Turn{}, fmt.Errorf("nothing to retry: %w", err)
	}
	turn, err := s.store.CreateTurn(ctx, conversationID, last.ID, channel, originRef)
	if err != nil {
		return store.Turn{}, err
	}
	s.emit("turn.updated", conversationID, turn)
	s.emit("notice", conversationID, map[string]any{
		"severity": "info", "title": "Retrying", "description": "Re-running the last request.",
	})
	go s.runQueue(conversationID)
	return turn, nil
}

func (s *Service) Cancel(ctx context.Context, conversationID string) error {
	a := s.activeFor(conversationID)
	if a == nil {
		turn, err := s.store.CancelQueuedTurn(ctx, conversationID)
		if err != nil {
			return ErrNoActiveTurn
		}
		s.emit("turn.completed", conversationID, map[string]any{
			"turn": turn, "status": "cancelled", "stopReason": "cancelled",
		})
		return nil
	}
	a.mu.Lock()
	if a.finished {
		a.mu.Unlock()
		return ErrNoActiveTurn
	}
	if a.afterTools {
		a.mu.Unlock()
		s.requestGracefulCancel(a)
		return nil
	}
	a.cancelRequested = true
	session := a.session
	turn := a.turn
	a.mu.Unlock()

	_ = s.store.SetConversationStatus(ctx, conversationID, "cancelling")
	s.emit("turn.status", conversationID, map[string]any{"turnId": turn.ID, "status": "cancelling"})
	s.cancelPendingApprovals(conversationID, "Turn cancelled by user.")
	if session != nil {
		cancelCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := s.agents.Cancel(cancelCtx, session); err != nil {
			s.log.Warn("failed to notify agent about cancel", "error", err)
		}
		cancel()
	}
	a.cancel()
	return nil
}

// runQueue drains a conversation's queue. Every call sets kick, and the owner
// only releases once no kick arrived after its last empty check, so a turn
// persisted during that check is never stranded.
func (s *Service) runQueue(conversationID string) {
	s.mu.Lock()
	s.kick[conversationID] = true
	if s.running[conversationID] {
		s.mu.Unlock()
		return
	}
	s.running[conversationID] = true
	s.mu.Unlock()
	for {
		s.mu.Lock()
		s.kick[conversationID] = false
		s.mu.Unlock()
		for {
			turn, err := s.store.NextQueuedTurn(context.Background(), conversationID)
			if err != nil {
				if !store.IsNotFound(err) {
					s.log.Warn("failed to read queued turn", "conversation", conversationID, "error", err)
				}
				break
			}
			expired, err := s.store.AutomationTurnExpired(context.Background(), turn.ID, time.Now())
			if err != nil {
				s.log.Warn("failed to check automation expiry", "turn", turn.ID, "error", err)
				break
			}
			if expired {
				turn.Status, turn.StopReason, turn.Error = "cancelled", "expired", "Automation expired before the queued run could start."
				turn.EndedAt = time.Now().UnixMilli()
				if err := s.store.FinishTurn(context.Background(), turn.ID, turn.Status, turn.StopReason, turn.Error); err != nil {
					s.log.Warn("failed to expire automation turn", "turn", turn.ID, "error", err)
					break
				}
				s.emit("turn.updated", conversationID, turn)
				continue
			}
			s.processTurn(turn)
		}
		s.mu.Lock()
		if !s.kick[conversationID] {
			delete(s.running, conversationID)
			delete(s.kick, conversationID)
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
	}
}

func (s *Service) activeFor(conversationID string) *activeTurn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active[conversationID]
}

func (s *Service) setActive(a *activeTurn) {
	s.mu.Lock()
	s.active[a.conversationID] = a
	s.mu.Unlock()
}

func (s *Service) clearActive(a *activeTurn) {
	s.mu.Lock()
	if s.active[a.conversationID] == a {
		delete(s.active, a.conversationID)
	}
	s.mu.Unlock()
}

func (s *Service) ActiveStatus(conversationID string) (turnID, status string, ok bool) {
	a := s.activeFor(conversationID)
	if a == nil {
		return "", "", false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.turn.ID, a.turn.Status, true
}

func (s *Service) CreateConversation(ctx context.Context, agentID, title, cwd, channel, ref string) (store.Conversation, error) {
	var defaults store.AgentDefaults
	_ = s.store.GetJSONSetting(ctx, "agent.defaults", &defaults)
	if agentID == "" {
		ag, err := s.store.GetAgent(ctx, defaults.AgentID)
		if err != nil || !ag.Enabled {
			ag, err = s.store.FirstEnabledAgent(ctx)
		}
		if err != nil {
			return store.Conversation{}, ErrNoAgent
		}
		agentID = ag.ID
	}
	ag, err := s.store.GetAgent(ctx, agentID)
	if err != nil {
		return store.Conversation{}, err
	}
	if !ag.Enabled {
		return store.Conversation{}, fmt.Errorf("agent %q is disabled", ag.Name)
	}
	if cwd == "" {
		cwd = ag.Cwd
	}
	if cwd == "" {
		cwd = s.workspace
	}
	if channel == "" {
		channel = "web"
	}
	conv, err := s.store.CreateConversation(ctx, agentID, title, cwd, channel, ref, ag.DefaultModel)
	if err != nil {
		return conv, err
	}
	if agentID == defaults.AgentID && len(defaults.Selections) > 0 {
		meta, _ := s.metaMap(conv)
		meta["defaultConfig"] = defaults.Selections
		if err := s.store.UpdateConversationMetadata(ctx, conv.ID, mustJSON(meta)); err != nil {
			return conv, err
		}
		conv, err = s.store.GetConversation(ctx, conv.ID)
		if err != nil {
			return conv, err
		}
	}
	s.emit("conversation.created", conv.ID, conv)
	return conv, nil
}

type ConversationPatch struct {
	Title    *string        `json:"title,omitempty"`
	Pinned   *bool          `json:"pinned,omitempty"`
	Archived *bool          `json:"archived,omitempty"`
	Routing  *store.Routing `json:"routing,omitempty"`
	AgentID  *string        `json:"agentId,omitempty"`
}

func (s *Service) UpdateConversation(ctx context.Context, id string, patch ConversationPatch) (store.Conversation, error) {
	conv, err := s.store.GetConversation(ctx, id)
	if err != nil {
		return conv, err
	}
	if patch.AgentID != nil && *patch.AgentID != "" && *patch.AgentID != conv.AgentID {
		return s.SwitchAgent(ctx, id, *patch.AgentID)
	}
	if patch.Title != nil {
		title := strings.TrimSpace(*patch.Title)
		if len(title) > 200 {
			title = title[:200]
		}
		if title != "" {
			meta, _ := s.metaMap(conv)
			meta["titleSource"] = "user"
			_ = s.store.UpdateConversationMetadata(ctx, id, mustJSON(meta))
		}
	}
	if patch.Archived != nil && *patch.Archived {
		if a := s.activeFor(id); a != nil {
			_ = s.Cancel(ctx, id)
			for i := 0; i < 50; i++ {
				if s.activeFor(id) == nil {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
		}
	}
	conv, err = s.store.UpdateConversation(ctx, id, store.ConversationUpdate{
		Title: patch.Title, Pinned: patch.Pinned, Archived: patch.Archived, Routing: patch.Routing,
	})
	if err != nil {
		return conv, err
	}
	s.emit("conversation.updated", id, conv)
	return conv, nil
}

func (s *Service) SwitchAgent(ctx context.Context, conversationID, agentID string) (store.Conversation, error) {
	conv, err := s.store.GetConversation(ctx, conversationID)
	if err != nil {
		return conv, err
	}
	if conv.AgentID == agentID {
		return conv, nil
	}
	if a := s.activeFor(conversationID); a != nil {
		return conv, ErrBusy
	}
	newAgent, err := s.store.GetAgent(ctx, agentID)
	if err != nil {
		return conv, err
	}
	if !newAgent.Enabled {
		return conv, fmt.Errorf("agent %q is disabled", newAgent.Name)
	}
	oldAgent, _ := s.store.GetAgent(ctx, conv.AgentID)
	_, err = s.store.UpdateConversation(ctx, conversationID, store.ConversationUpdate{AgentID: &agentID})
	if err != nil {
		return conv, err
	}
	if newAgent.DefaultModel != "" {
		_ = s.store.SetConversationModel(ctx, conversationID, newAgent.DefaultModel)
	}
	if conv.Cwd == "" || conv.Cwd == oldAgent.Cwd {
		_ = s.store.SetConversationCwd(ctx, conversationID, newAgent.Cwd)
	}
	meta, _ := s.metaMap(conv)
	meta["handoff"] = true
	delete(meta, "desiredConfig")
	delete(meta, "defaultConfig")
	var defaults store.AgentDefaults
	if s.store.GetJSONSetting(ctx, "agent.defaults", &defaults) == nil && defaults.AgentID == agentID && len(defaults.Selections) > 0 {
		meta["defaultConfig"] = defaults.Selections
	}
	meta["titleSource"] = "agent"
	_ = s.store.UpdateConversationMetadata(ctx, conversationID, mustJSON(meta))
	s.agents.ResetConversation(ctx, conversationID)

	sys := store.Message{
		ConversationID: conversationID, Role: "system", Kind: "system", Status: "complete",
		Text: fmt.Sprintf("Switched to %s. The new agent will receive a transcript recap on your next message.", newAgent.Name),
	}
	_ = s.store.AppendMessage(ctx, &sys)
	s.emit("message.created", conversationID, sys)
	updated, err := s.store.GetConversation(ctx, conversationID)
	if err == nil {
		s.emit("conversation.updated", conversationID, updated)
	}
	return updated, err
}

func (s *Service) DeleteConversation(ctx context.Context, id string) error {
	if a := s.activeFor(id); a != nil {
		_ = s.Cancel(ctx, id)
		for i := 0; i < 100; i++ {
			if s.activeFor(id) == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if s.activeFor(id) != nil {
			return ErrBusy
		}
	}
	s.agents.ResetConversation(ctx, id)
	if err := s.store.DeleteConversation(ctx, id); err != nil {
		return err
	}
	s.emit("conversation.deleted", id, map[string]any{"id": id})
	return nil
}

func (s *Service) ResolveQueued(ctx context.Context, conversationID string) {
	go s.runQueue(conversationID)
}

func (s *Service) loadUploads(ctx context.Context, conversationID string, inputs []store.AttachmentInput) ([]store.Upload, error) {
	out := make([]store.Upload, 0, len(inputs))
	for _, in := range inputs {
		if strings.TrimSpace(in.UploadID) == "" {
			continue
		}
		u, err := s.store.GetUpload(ctx, in.UploadID)
		if err != nil {
			return nil, fmt.Errorf("attachment %s: %w", in.UploadID, err)
		}
		if u.ConversationID != "" && u.ConversationID != conversationID {
			return nil, fmt.Errorf("attachment %s belongs to another conversation", in.UploadID)
		}
		if u.Size > 25<<20 {
			return nil, fmt.Errorf("attachment %s is too large", u.Name)
		}
		out = append(out, u)
	}
	return out, nil
}

func (s *Service) hydrateMessage(ctx context.Context, m store.Message) messagePayload {
	p := messagePayload{Message: m}
	if ups, err := s.store.ListUploadsForMessage(ctx, m.ID); err == nil {
		p.Attachments = ups
	}
	return p
}

func (s *Service) SaveInboundAttachment(ctx context.Context, conversationID string, name, mime string, data []byte) (store.Upload, error) {
	if len(data) == 0 {
		return store.Upload{}, errors.New("empty attachment")
	}
	if len(data) > 25<<20 {
		return store.Upload{}, errors.New("attachment is too large")
	}
	if name == "" {
		name = "attachment"
	}
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	id := ids.New("up")
	dir := filepath.Join(s.uploadDir, time.Now().Format("2006/01"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return store.Upload{}, err
	}
	path := filepath.Join(dir, id+"-"+safeFilename(name))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return store.Upload{}, err
	}
	u := store.Upload{
		ID: id, ConversationID: conversationID, Name: name, MIME: mime, Size: int64(len(data)), Path: path,
	}
	if err := s.store.CreateUpload(ctx, &u); err != nil {
		return u, err
	}
	return u, nil
}

func safeFilename(name string) string {
	name = filepath.Base(name)
	name = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '.', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, name)
	if len(name) > 80 {
		name = name[:80]
	}
	if name == "" || name == "." {
		name = "file"
	}
	return name
}

func (s *Service) metaMap(conv store.Conversation) (map[string]any, error) {
	meta := map[string]any{}
	if len(conv.Metadata) > 0 {
		_ = json.Unmarshal(conv.Metadata, &meta)
	}
	return meta, nil
}

func (s *Service) patchMeta(ctx context.Context, conversationID string, patch map[string]any) (store.Conversation, error) {
	conv, err := s.store.GetConversation(ctx, conversationID)
	if err != nil {
		return conv, err
	}
	meta, _ := s.metaMap(conv)
	for k, v := range patch {
		if v == nil {
			delete(meta, k)
			continue
		}
		meta[k] = v
	}
	if err := s.store.UpdateConversationMetadata(ctx, conversationID, mustJSON(meta)); err != nil {
		return conv, err
	}
	return s.store.GetConversation(ctx, conversationID)
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func chooseLink(links []store.ChannelLink, name string) (store.ChannelLink, bool) {
	for _, l := range links {
		if name == "" || l.Channel == name {
			return l, true
		}
	}
	return store.ChannelLink{}, false
}

func linkRef(l store.ChannelLink) string {
	b, _ := json.Marshal(map[string]any{
		"externalId": l.ExternalID, "threadId": l.ThreadID,
		"displayName": l.DisplayName, "isGroup": l.IsGroup,
	})
	return string(b)
}

func targetFromLink(l store.ChannelLink) channels.Target {
	return channels.Target{
		Channel: l.Channel, ExternalID: l.ExternalID, ThreadID: l.ThreadID,
		DisplayName: l.DisplayName, IsGroup: l.IsGroup,
	}
}

func isDefaultTitle(s string) bool {
	s = strings.TrimSpace(strings.ToLower(s))
	return s == "" || s == "new conversation" || s == "untitled"
}

func autoTitle(text string, uploads []store.Upload) string {
	text = strings.TrimSpace(text)
	if text == "" && len(uploads) > 0 {
		return "Attachment: " + uploads[0].Name
	}
	line := text
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimSpace(line)
	if len(line) > 72 {
		line = line[:72]
	}
	if line == "" {
		return ""
	}
	return line
}
