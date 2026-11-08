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
	"github.com/jahrulnr/nusamation/internal/security"
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

	mu               sync.Mutex
	metaMu           sync.Mutex
	rootResetCond    *sync.Cond
	pendingRootReset map[string]bool
	rootResetBusy    map[string]bool
	preparing        map[string]int
	active           map[string]*activeTurn
	running          map[string]bool
	kick             map[string]bool
	waiters          map[string]chan approvalDecision
	elicitations     map[string]*elicitationWaiter
	pollWaiters      map[string]*pollWaiter
	terminalTurns    map[string]*activeTurn
	// resetConversation drops the loaded ACP session. Tests replace it to hold a drop open.
	resetConversation func(context.Context, string)

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
	s := &Service{
		store:             opts.Store,
		agents:            opts.Agents,
		channels:          opts.Channels,
		bus:               opts.Events,
		log:               opts.Log.With("component", "chat"),
		uploadDir:         opts.UploadDir,
		workspace:         opts.Workspace,
		active:            map[string]*activeTurn{},
		pendingRootReset:  map[string]bool{},
		rootResetBusy:     map[string]bool{},
		preparing:         map[string]int{},
		running:           map[string]bool{},
		kick:              map[string]bool{},
		waiters:           map[string]chan approvalDecision{},
		elicitations:      map[string]*elicitationWaiter{},
		pollWaiters:       map[string]*pollWaiter{},
		terminalTurns:     map[string]*activeTurn{},
		cancelNoticeAfter: defaultCancelNoticeAfter,
		turnTimeout:       defaultTurnTimeout,
	}
	s.rootResetCond = sync.NewCond(&s.mu)
	s.resetConversation = func(ctx context.Context, conversationID string) {
		if s.agents != nil {
			s.agents.ResetConversation(ctx, conversationID)
		}
	}
	return s
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
	return nil
}

// ResumeQueues restarts queued turns. Call it only after browser_use is
// registered, because a session created here keeps the MCP list from that moment.
func (s *Service) ResumeQueues(ctx context.Context) {
	go s.resumeQueues(ctx)
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
	// Delivery is "" or "queue" (FIFO), "after_tools", or "steer".
	// steer injects the text into the running turn when the agent accepts
	// mid-turn input. A channel message that cannot be steered waits until
	// the running turn's tools finish, then cancels that turn and sends.
	Delivery string `json:"delivery,omitempty"`
	// Annotations carries resolved browser picks (pickId → descriptor +
	// marked-screenshot upload); the http layer resolves them so chat sees
	// plain data.
	Annotations     []store.AnnotationInput `json:"-"`
	AutomationRunID string                  `json:"-"`
}

type messagePayload struct {
	store.Message
	Attachments []store.Upload `json:"attachments,omitempty"`
}

func (s *Service) Submit(ctx context.Context, in SubmitInput) (store.Message, store.Turn, error) {
	switch in.Delivery {
	case "", deliveryQueue, deliveryAfterTools, deliverySteer:
	default:
		return store.Message{}, store.Turn{}, ErrInvalidDelivery
	}
	text := strings.TrimSpace(in.Text)
	if text == "" && len(in.Attachments) == 0 && len(in.Annotations) == 0 {
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
	// Annotation screenshot uploads bind after client attachments, in
	// annotations[] order (browser-api-contract §3.9). Pins opted out of
	// screenshots keep the upload for history thumbnails but send no image
	// to the agent.
	for _, a := range in.Annotations {
		if a.OmitScreenshot {
			continue
		}
		uploads = append(uploads, a.Upload)
	}
	// Steer injects text into the running prompt — it cannot carry uploads
	// or annotations, so any message with them must take the
	// wait-tools→cancel→send path below instead of silently dropping them.
	if in.Delivery == deliverySteer && len(uploads) == 0 && len(in.Annotations) == 0 {
		if msg, turn, handled, err := s.trySteer(ctx, conv, text, channel, originChannel, originRef); err != nil || handled {
			return msg, turn, err
		}
	}
	// A steer the agent cannot take falls back to cancel-and-resend: the
	// queued input waits until the running turn's tools finish, cancels that
	// turn, then sends. This matches the channel path so a web steer on an
	// agent without steering support does not just sit behind a long turn.
	if in.Delivery == deliverySteer && s.activeFor(conv.ID) != nil {
		in.Delivery = deliveryAfterTools
	}
	var target *activeTurn
	if in.Delivery == deliveryAfterTools {
		target = s.activeFor(conv.ID)
	}
	meta := map[string]any{}
	if in.AutomationRunID != "" {
		meta["automationRunId"] = in.AutomationRunID
	}
	if len(uploads) > 0 {
		list := make([]map[string]any, 0, len(uploads))
		for _, u := range uploads {
			list = append(list, map[string]any{
				"uploadId": u.ID, "name": u.Name, "mime": u.MIME, "size": u.Size,
			})
		}
		meta["attachments"] = list
	}
	if len(in.Annotations) > 0 {
		list := make([]store.MessageAnnotation, 0, len(in.Annotations))
		for _, a := range in.Annotations {
			list = append(list, store.MessageAnnotation{
				PickID: a.PickID, Comment: a.Comment, Marker: a.Marker,
				UploadID: a.Upload.ID, OmitScreenshot: a.OmitScreenshot,
				Descriptor: a.Descriptor,
			})
		}
		meta["annotations"] = list
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

// CancelQueued cancels one queued turn without disturbing the running one.
// With dropMessage the user message that queued the turn is deleted as well —
// the web "edit queued message" flow uses this so the composer can resend the
// prompt as a fresh turn instead of rewriting history.
func (s *Service) CancelQueued(ctx context.Context, conversationID, turnID string, dropMessage bool) (store.Turn, error) {
	turn, err := s.store.CancelQueuedTurnByID(ctx, conversationID, turnID)
	if err != nil {
		return turn, err
	}
	// A cancelled queue head must not cancel the running turn it armed.
	if a := s.activeFor(conversationID); a != nil {
		a.disarmAfterToolsBy(turn.ID)
	}
	if dropMessage && turn.UserMessageID != "" {
		if derr := s.store.DeleteMessage(ctx, turn.UserMessageID); derr != nil {
			s.log.Warn("failed to drop queued message", "conversation", conversationID, "message", turn.UserMessageID, "error", derr)
		} else {
			s.emit("message.deleted", conversationID, map[string]any{"id": turn.UserMessageID})
		}
	}
	// A queued cancel is not a turn completion: emit turn.updated so the UI
	// refreshes the queue label without clearing the active running turn.
	s.emit("turn.updated", conversationID, turn)
	if s.activeFor(conversationID) == nil {
		_ = s.store.SetConversationStatus(ctx, conversationID, "idle")
		if conv, cerr := s.store.GetConversation(ctx, conversationID); cerr == nil {
			s.emit("conversation.updated", conversationID, conv)
		}
	}
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
	// The first cancel on an after_tools-armed turn asks the agent to stop
	// gracefully; a second explicit cancel escalates to the hard cancel below
	// so a turn whose agent ignores session/cancel is not stuck.
	if a.afterTools && !a.cancelRequested {
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

// setActive publishes the turn. It waits until an in-progress root drop for
// this conversation finishes, so the turn is not active while ResetConversation
// runs and the next session load happens after the drop. The wait, the busy
// flag, and the active map share s.mu.
func (s *Service) setActive(a *activeTurn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.rootResetBusy[a.conversationID] {
		s.rootResetCond.Wait()
	}
	s.active[a.conversationID] = a
}

// clearActive removes this turn. When a project-root reset was waiting, it
// marks that drop busy before releasing s.mu and reports that this caller must
// drop the session. The active check, the pending flag, and the busy flag
// share s.mu with markRootReset and setActive.
func (s *Service) clearActive(a *activeTurn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active[a.conversationID] != a {
		return false
	}
	delete(s.active, a.conversationID)
	pending := s.pendingRootReset[a.conversationID]
	delete(s.pendingRootReset, a.conversationID)
	if pending {
		s.rootResetBusy[a.conversationID] = true
	}
	return pending
}

func (s *Service) endRootResetBusy(conversationID string) {
	s.mu.Lock()
	delete(s.rootResetBusy, conversationID)
	s.rootResetCond.Broadcast()
	s.mu.Unlock()
}

// beginPrepare marks a session preparation so a root reset that lands while
// EnsureSession runs defers its drop like a running turn. The returned func
// ends the preparation; the last one out runs the deferred drop.
func (s *Service) beginPrepare(conversationID string) func() {
	s.mu.Lock()
	for s.rootResetBusy[conversationID] {
		s.rootResetCond.Wait()
	}
	if s.preparing == nil {
		s.preparing = map[string]int{}
	}
	s.preparing[conversationID]++
	s.mu.Unlock()
	return func() { s.endPrepare(conversationID) }
}

// endPrepare ends a preparation. The last one out runs a deferred drop unless
// a turn became active meanwhile — then finishActive owns it. The count, the
// pending flag, and the busy flag share s.mu.
func (s *Service) endPrepare(conversationID string) {
	s.mu.Lock()
	if s.preparing[conversationID] > 1 {
		s.preparing[conversationID]--
		s.mu.Unlock()
		return
	}
	delete(s.preparing, conversationID)
	pending := s.pendingRootReset[conversationID] && s.active[conversationID] == nil
	if pending {
		delete(s.pendingRootReset, conversationID)
		s.rootResetBusy[conversationID] = true
	}
	s.mu.Unlock()
	if !pending {
		return
	}
	defer s.endRootResetBusy(conversationID)
	s.resetConversation(context.Background(), conversationID)
}

// finishActive clears the turn, then drops a session whose roots changed while
// the turn was running. The drop happens after the lock is released. setActive
// waits until that drop clears the busy flag.
func (s *Service) finishActive(a *activeTurn) {
	if a == nil || !s.clearActive(a) {
		return
	}
	defer s.endRootResetBusy(a.conversationID)
	s.resetConversation(context.Background(), a.conversationID)
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

// ActiveMessageTexts returns the current in-memory stream, including chunks not
// yet flushed to storage. The snapshot pairs with offsetBytes on delta events.
func (s *Service) ActiveMessageTexts(conversationID string) map[string]string {
	result := make(map[string]string)
	a := s.activeFor(conversationID)
	if a == nil {
		return result
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.assistantID != "" {
		result[a.assistantID] = a.assistant.String()
	}
	if a.thoughtID != "" {
		result[a.thoughtID] = a.thought.String()
	}
	return result
}

func (s *Service) CreateConversation(ctx context.Context, agentID, title, cwd, channel, ref string) (store.Conversation, error) {
	return s.createConversation(ctx, agentID, title, cwd, channel, ref, "")
}

// CreateProjectConversation starts a chat in a project. cwd and
// additionalDirectories come from that project.
func (s *Service) CreateProjectConversation(ctx context.Context, agentID, title, channel, ref, projectID string) (store.Conversation, error) {
	return s.createConversation(ctx, agentID, title, "", channel, ref, projectID)
}

func (s *Service) createConversation(ctx context.Context, agentID, title, cwd, channel, ref, projectID string) (store.Conversation, error) {
	projectID = strings.TrimSpace(projectID)
	var roots []string
	if projectID != "" {
		project, err := s.store.GetProject(ctx, projectID)
		if err != nil {
			if store.IsNotFound(err) {
				return store.Conversation{}, ErrProjectNotFound
			}
			return store.Conversation{}, err
		}
		if len(project.RootPaths) == 0 {
			return store.Conversation{}, invalidProject("A project needs at least one folder.")
		}
		roots = project.RootPaths
		cwd = project.RootPaths[0]
	}
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
	var conv store.Conversation
	if projectID != "" {
		extra := []string{}
		if len(roots) > 1 {
			extra = append(extra, roots[1:]...)
		}
		conv, err = s.store.CreateProjectConversation(ctx, agentID, title, cwd, channel, ref, ag.DefaultModel, projectID, extra)
		if store.IsNotFound(err) {
			return store.Conversation{}, ErrProjectNotFound
		}
	} else {
		conv, err = s.store.CreateConversation(ctx, agentID, title, cwd, channel, ref, ag.DefaultModel)
	}
	if err != nil {
		return conv, err
	}
	if agentID == defaults.AgentID && len(defaults.Selections) > 0 {
		conv, err = s.patchMeta(ctx, conv.ID, map[string]any{"defaultConfig": defaults.Selections})
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
			_, _ = s.patchMeta(ctx, id, map[string]any{"titleSource": "user"})
		}
	}
	if patch.Archived != nil && *patch.Archived {
		if a := s.activeFor(id); a != nil {
			_ = s.Cancel(ctx, id)
			for range 50 {
				if s.activeFor(id) == nil {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
		}
		if s.activeFor(id) != nil {
			return conv, ErrBusy
		}
		s.agents.ResetConversation(ctx, id)
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
	// A project member's cwd stays on the project's primary folder for the life
	// of the membership, even when that folder is also the previous agent's cwd.
	if conv.ProjectID == "" && (conv.Cwd == "" || conv.Cwd == oldAgent.Cwd) {
		_ = s.store.SetConversationCwd(ctx, conversationID, newAgent.Cwd)
	}
	patch := map[string]any{
		"handoff": true, "desiredConfig": nil, "defaultConfig": nil, "titleSource": "agent",
	}
	var defaults store.AgentDefaults
	if s.store.GetJSONSetting(ctx, "agent.defaults", &defaults) == nil && defaults.AgentID == agentID && len(defaults.Selections) > 0 {
		patch["defaultConfig"] = defaults.Selections
	}
	_, _ = s.patchMeta(ctx, conversationID, patch)
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
	if _, err := s.store.GetConversation(ctx, id); err != nil {
		return err
	}
	if a := s.activeFor(id); a != nil {
		_ = s.Cancel(ctx, id)
		for range 100 {
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
	sessions, err := s.store.ListConversationACPSessions(ctx, id)
	if err != nil {
		return err
	}
	for _, session := range sessions {
		deleteCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := s.agents.DeleteStoredSession(deleteCtx, session)
		cancel()
		if err != nil {
			s.log.Warn("could not delete ACP session", "conversation", id, "agent", session.AgentID, "agent_slug", session.AgentSlug, "session", session.SessionID, "error", err)
		}
	}
	uploads, _ := s.store.ListUploadsForConversation(ctx, id)
	if err := s.store.DeleteConversation(ctx, id); err != nil {
		return err
	}
	for _, u := range uploads {
		path, err := security.SafePath(s.uploadDir, u.Path, false)
		if err != nil {
			s.log.Warn("skipping upload outside upload dir", "path", u.Path, "conversation", id)
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			s.log.Warn("could not remove upload file", "path", path, "error", err)
		}
	}
	_ = s.store.DeleteUploadsForConversation(ctx, id)
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

// patchMeta merges patch into the conversation metadata. metaMu serializes
// the read-modify-write so concurrent patches cannot lose each other's keys.
// A nil value deletes the key.
func (s *Service) patchMeta(ctx context.Context, conversationID string, patch map[string]any) (store.Conversation, error) {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
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
