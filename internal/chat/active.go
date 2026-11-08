package chat

import (
	"context"
	"encoding/json"
	"maps"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jahrulnr/nusamation/internal/agent"
	"github.com/jahrulnr/nusamation/internal/channels"
	"github.com/jahrulnr/nusamation/internal/store"
)

type toolState struct {
	MessageID  string
	ToolCallID string
	Title      string
	Name       string
	Kind       string
	Status     string
	Content    json.RawMessage
	Locations  json.RawMessage
	RawInput   json.RawMessage
	RawOutput  json.RawMessage
	Meta       json.RawMessage
	Terminals  map[string]agent.TerminalUpdate
}

func (t *toolState) json() json.RawMessage {
	m := map[string]any{"toolCallId": t.ToolCallID}
	if t.Title != "" {
		m["title"] = t.Title
	}
	if t.Name != "" {
		m["name"] = t.Name
	}
	if t.Kind != "" {
		m["kind"] = t.Kind
	}
	if t.Status != "" {
		m["status"] = t.Status
	}
	if len(t.Content) > 0 {
		m["content"] = t.Content
	}
	if len(t.Locations) > 0 {
		m["locations"] = t.Locations
	}
	if len(t.RawInput) > 0 {
		m["rawInput"] = t.RawInput
	}
	if len(t.RawOutput) > 0 {
		m["rawOutput"] = t.RawOutput
	}
	if len(t.Meta) > 0 {
		m["_meta"] = t.Meta
	}
	if len(t.Terminals) > 0 {
		m["terminals"] = t.Terminals
	}
	b, _ := json.Marshal(m)
	return b
}

func (t *toolState) display() string {
	if t.Title != "" {
		return t.Title
	}
	if t.Name != "" {
		return t.Name
	}
	return "Tool call"
}

type activeTurn struct {
	mu       sync.Mutex
	streamMu sync.Mutex
	toolMu   sync.Mutex

	conversationID string
	turn           store.Turn
	conv           store.Conversation
	agent          store.Agent
	user           store.Message
	session        *agent.Session

	ctx       context.Context
	cancel    context.CancelFunc
	flushStop chan struct{}

	assistantID     string
	assistantACPID  string
	assistantTextID string
	assistant       strings.Builder
	assistantAll    strings.Builder
	assistantDirty  bool
	assistantDone   bool

	thoughtID    string
	thought      strings.Builder
	thoughtDirty bool
	thoughtDone  bool

	tools     map[string]*toolState
	terminals map[string]agent.TerminalUpdate
	toolCount int

	deliveryMu       sync.Mutex
	delivery         channels.Delivery
	deliverToChannel bool
	deliveredSegment bool
	assistantGap     bool

	outboundMu sync.Mutex
	outbound   []channels.OutboundMedia
	sentRefs   map[string]bool

	approvals       map[string]bool
	recapApplied    bool
	cancelRequested bool
	afterTools      bool
	afterToolsBy    string
	// steerAccepted holds _session/steering "injected" inputs (msgID →
	// steerEntry). An injected steer has no turn of its own, so a turn that
	// ends with no generated output after the steer was sent dropped it —
	// it is requeued as a normal turn instead of dangling in history.
	steerAccepted map[string]steerEntry
	steerNextOrd  int64
	// outputSeq increments on every generated assistant output — message
	// text, thought, or a newly started tool call. A steer claims orphan
	// only when this is unchanged since the snapshot taken just before its
	// RPC; any output that lands after the snapshot (even trajectory output
	// already in flight) counts as consumed, so a delivered input is never
	// re-sent. Tool-call updates to calls started before the snapshot do
	// not count.
	outputSeq     int64
	finished      bool
	finalStatus   string
	finalError    string
	noOutput      bool
	finalDelivery bool
	startedAt     time.Time
	lastFlush     time.Time

	// wake nudges the turn's coordinator; cancelSignal closes once a graceful
	// cancel is requested; finishedCh closes when the turn finishes.
	wake         chan struct{}
	cancelSignal chan struct{}
	finishedCh   chan struct{}

	// promptMu orders session/cancel against prompt completion. promptSent
	// closes once the session/prompt request is written; promptDone closes once
	// Prompt has returned or was never started.
	promptMu       sync.Mutex
	promptStarting bool
	promptSent     chan struct{}
	promptDone     chan struct{}
	promptSentOnce sync.Once
	promptDoneOnce sync.Once
}

func newActiveTurn(conv store.Conversation, turn store.Turn, ag store.Agent, user store.Message, ctx context.Context, cancel context.CancelFunc) *activeTurn {
	return &activeTurn{
		conversationID: conv.ID,
		turn:           turn,
		conv:           conv,
		agent:          ag,
		user:           user,
		ctx:            ctx,
		cancel:         cancel,
		flushStop:      make(chan struct{}),
		tools:          map[string]*toolState{},
		approvals:      map[string]bool{},
		startedAt:      time.Now(),
		steerAccepted:  map[string]steerEntry{},
		wake:           make(chan struct{}, 1),
		cancelSignal:   make(chan struct{}),
		finishedCh:     make(chan struct{}),
		promptSent:     make(chan struct{}),
		promptDone:     make(chan struct{}),
	}
}

// signal wakes the coordinator without blocking; a pending wake already covers
// this change. A nil channel (test-built turns) is a no-op.
func (a *activeTurn) signal() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

func closeOnce(once *sync.Once, ch chan struct{}) {
	if ch == nil {
		return
	}
	once.Do(func() { close(ch) })
}

func isClosed(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// armAfterTools marks the turn as the target of a queued after_tools input.
// It fails once the turn is finished or a cancel is already underway.
func (a *activeTurn) armAfterTools(queuedTurnID string) (turnID string, ok bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.finished || a.cancelRequested {
		return "", false
	}
	a.afterTools = true
	a.afterToolsBy = queuedTurnID
	a.signal()
	return a.turn.ID, true
}

// disarmAfterToolsBy clears the arm when the queued input that armed this
// turn is cancelled, so the running turn is no longer cancelled on its
// behalf.
func (a *activeTurn) disarmAfterToolsBy(queuedTurnID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.afterToolsBy == queuedTurnID {
		a.afterTools = false
		a.afterToolsBy = ""
	}
}

// afterToolsBoundaryReached reports whether an armed turn may be cancelled:
// the prompt was sent, no approval is pending, and every known tool is in a
// terminal state. Unknown or missing statuses are not terminal.
func (a *activeTurn) afterToolsBoundaryReached() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.afterTools || a.cancelRequested || a.finished || len(a.approvals) > 0 {
		return false
	}
	if !isClosed(a.promptSent) {
		return false
	}
	for _, t := range a.tools {
		if t.Status != "completed" && t.Status != "failed" {
			return false
		}
	}
	return true
}

func (a *activeTurn) isCancelRequested() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cancelRequested
}

// beginPrompt reports whether Prompt may be dispatched. It returns false once a
// cancel was requested so that no prompt is sent after cancellation.
func (a *activeTurn) beginPrompt() bool {
	a.promptMu.Lock()
	defer a.promptMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancelRequested {
		return false
	}
	a.promptStarting = true
	return true
}

func (a *activeTurn) endPrompt() {
	a.promptMu.Lock()
	defer a.promptMu.Unlock()
	closeOnce(&a.promptDoneOnce, a.promptDone)
}

func (a *activeTurn) markPromptSent() {
	closeOnce(&a.promptSentOnce, a.promptSent)
	a.signal()
}

func (a *activeTurn) appendAssistant(delta string) (text string, first bool, offset int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if delta == "" {
		return "", false, 0
	}
	offset = a.assistant.Len()
	first = a.assistant.Len() == 0
	if a.assistantGap && a.assistant.Len() == 0 {
		if a.assistantAll.Len() > 0 {
			a.assistantAll.WriteString("\n\n")
		}
		a.assistantGap = false
	}
	a.assistant.WriteString(delta)
	a.assistantAll.WriteString(delta)
	a.assistantDirty = true
	a.outputSeq++
	return delta, first, offset
}

// steerEntry is one accepted-but-turn-bound steer: the output snapshot taken
// just before the steer RPC plus the delivery origin the queued turn needs
// if the input turns out to be dropped.
type steerEntry struct {
	seq           int64
	ord           int64
	originChannel string
	originRef     string
}

// steerClaim is a dropped steer being requeued as a normal turn.
type steerClaim struct {
	msgID         string
	originChannel string
	originRef     string
}

// outputMarker returns the current assistant-output sequence. A steer
// snapshots it before the RPC; takeOrphanedSteers claims entries whose
// snapshot is still current.
func (a *activeTurn) outputMarker() int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.outputSeq
}

// addSteerAccepted records an injected steer. It reports dropped=true when
// the turn already finished completed — finishTurn's orphan pass ran before
// this accept landed, so the caller must requeue immediately. A steer
// accepted on a cancelled or errored turn is not tracked: the user ended
// that turn deliberately.
func (a *activeTurn) addSteerAccepted(msgID string, e steerEntry) (dropped bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.finished {
		return a.finalStatus == "completed"
	}
	e.ord = a.steerNextOrd
	a.steerNextOrd++
	a.steerAccepted[msgID] = e
	return false
}

// takeOrphanedSteers returns the accepted steers no generated output
// followed, in submission order, and clears them, so a second caller
// cannot requeue the same message.
func (a *activeTurn) takeOrphanedSteers() []steerClaim {
	a.mu.Lock()
	defer a.mu.Unlock()
	type ordered struct {
		ord   int64
		claim steerClaim
	}
	var lost []ordered
	for id, e := range a.steerAccepted {
		if e.seq != a.outputSeq {
			continue
		}
		lost = append(lost, ordered{e.ord, steerClaim{msgID: id, originChannel: e.originChannel, originRef: e.originRef}})
		delete(a.steerAccepted, id)
	}
	sort.Slice(lost, func(i, j int) bool { return lost[i].ord < lost[j].ord })
	claims := make([]steerClaim, len(lost))
	for i, l := range lost {
		claims[i] = l.claim
	}
	return claims
}

func (a *activeTurn) appendThought(delta string) (text string, first bool, offset int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if delta == "" {
		return "", false, 0
	}
	offset = a.thought.Len()
	first = a.thought.Len() == 0
	a.thought.WriteString(delta)
	a.thoughtDirty = true
	a.outputSeq++
	return delta, first, offset
}

func (a *activeTurn) assistantSnapshot() (id, text string, dirty bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	dirty = a.assistantDirty
	a.assistantDirty = false
	return a.assistantID, a.assistant.String(), dirty
}

func (a *activeTurn) thoughtSnapshot() (id, text string, dirty bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	dirty = a.thoughtDirty
	a.thoughtDirty = false
	return a.thoughtID, a.thought.String(), dirty
}

func (a *activeTurn) setDelivery(d channels.Delivery) {
	a.mu.Lock()
	a.delivery = d
	a.mu.Unlock()
}

const maxOutboundMediaPerTurn = 4

func (a *activeTurn) addOutbound(m channels.OutboundMedia) bool {
	a.outboundMu.Lock()
	defer a.outboundMu.Unlock()
	if len(a.outbound) >= maxOutboundMediaPerTurn {
		return false
	}
	a.outbound = append(a.outbound, m)
	return true
}

func (a *activeTurn) hasOutbound() bool {
	a.outboundMu.Lock()
	defer a.outboundMu.Unlock()
	return len(a.outbound) > 0
}

func (a *activeTurn) outboundSnapshot() []channels.OutboundMedia {
	a.outboundMu.Lock()
	defer a.outboundMu.Unlock()
	return append([]channels.OutboundMedia(nil), a.outbound...)
}

func (a *activeTurn) markOutboundRef(ref string) {
	a.outboundMu.Lock()
	defer a.outboundMu.Unlock()
	if a.sentRefs == nil {
		a.sentRefs = map[string]bool{}
	}
	a.sentRefs[ref] = true
}

func (a *activeTurn) outboundRefs() map[string]bool {
	a.outboundMu.Lock()
	defer a.outboundMu.Unlock()
	out := make(map[string]bool, len(a.sentRefs))
	maps.Copy(out, a.sentRefs)
	return out
}

func (a *activeTurn) getTool(id string) *toolState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.tools[id]
}

func (a *activeTurn) ensureTool(id string) *toolState {
	a.mu.Lock()
	defer a.mu.Unlock()
	if t, ok := a.tools[id]; ok {
		return t
	}
	t := &toolState{Status: "pending"}
	a.tools[id] = t
	a.toolCount++
	return t
}
