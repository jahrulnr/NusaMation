package chat

import (
	"context"
	"time"

	"github.com/jahrulnr/nusamation/internal/acp"
	"github.com/jahrulnr/nusamation/internal/agent"
	"github.com/jahrulnr/nusamation/internal/store"
)

// trySteer injects text into the running turn when the agent can accept it.
// handled is false when the caller should queue a normal turn instead, and
// in that case nothing has been written yet. A steer that the agent rejects
// after the message is stored is queued from here and reported as handled.
func (s *Service) trySteer(ctx context.Context, conv store.Conversation, text, channel, originChannel, originRef string, references []store.PromptReference) (store.Message, store.Turn, bool, error) {
	a := s.activeFor(conv.ID)
	if a == nil || !a.injectable() {
		return store.Message{}, store.Turn{}, false, nil
	}
	midTurn, steering := s.agents.InjectionMode(a.agent.ID)
	if !midTurn && !steering {
		return store.Message{}, store.Turn{}, false, nil
	}
	a.mu.Lock()
	session := a.session
	turn := a.turn
	a.mu.Unlock()
	if session == nil {
		return store.Message{}, store.Turn{}, false, nil
	}

	msg := store.Message{
		ConversationID: conv.ID,
		TurnID:         turn.ID,
		Role:           "user",
		Kind:           "text",
		Text:           text,
		Status:         "complete",
		Channel:        channel,
		ExternalID:     originRef,
		Metadata:       mustJSON(map[string]any{"delivery": deliverySteer, "references": references}),
	}
	if err := s.store.AppendMessage(ctx, &msg); err != nil {
		return store.Message{}, store.Turn{}, false, err
	}
	s.emit("message.created", conv.ID, s.hydrateMessage(ctx, msg))

	promptText, referenceBlocks, _ := promptReferences(text, references)
	blocks := append([]acp.ContentBlock{{Type: "text", Text: promptText}}, referenceBlocks...)
	inputContext := s.inputContext(msg, originChannel, originRef, s.deliveryChannel(a))
	inputContext.steering = true
	if rules := s.responseRules(a); rules != "" {
		inputContext.rules = rules
	}
	blocks = appendPromptContext(blocks, inputContext)
	if steering {
		// Snapshot the assistant-output sequence before the RPC — the
		// steering response can resolve while the agent's own pre-response
		// updates are still draining, so an accept-time snapshot cannot tell
		// "answered the steer" apart from "was already talking".
		seq := a.outputMarker()
		steerCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		res, err := s.agents.Steer(steerCtx, session, blocks)
		cancel()
		if err == nil && res.Accepted() {
			s.log.Info("prompt steered", "conversation", conv.ID, "turn", turn.ID, "outcome", res.Outcome)
			if res.Outcome == "injected" && a.addSteerAccepted(msg.ID, steerEntry{seq: seq, originChannel: originChannel, originRef: originRef}) {
				s.requeueSteerMessage(a.conversationID, msg.ID, originChannel, originRef)
			}
			return msg, turn, true, nil
		}
		s.log.Warn("steering was not accepted; queueing the message", "conversation", conv.ID, "error", err, "outcome", res.Outcome)
		// A rejected steer becomes cancel-and-resend on every channel: wait
		// for the running turn's tools, cancel it, then send this input.
		msg.Metadata = mustJSON(map[string]any{"delivery": deliveryAfterTools, "references": references})
		if uerr := s.store.UpdateMessage(ctx, msg.ID, nil, nil, nil, msg.Metadata); uerr != nil {
			s.log.Warn("failed to record after_tools fallback", "conversation", conv.ID, "error", uerr)
		}
		queued, qerr := s.queueStoredMessage(ctx, conv.ID, msg.ID, originChannel, originRef)
		if qerr == nil {
			if live := s.activeFor(conv.ID); live != nil {
				s.armAfterTools(ctx, conv.ID, &msg, queued, live)
			}
		}
		return msg, queued, true, qerr
	}

	go s.injectMidTurn(a, session, msg.ID, blocks, originChannel, originRef)
	s.log.Info("prompt steered", "conversation", conv.ID, "turn", turn.ID, "mode", "mid-turn")
	return msg, turn, true, nil
}

// injectMidTurn sends a second session/prompt into the running turn. Devin
// answers that call when the turn ends, with the same stop reason as the
// original prompt, so this result must not finish the turn. A failed inject
// requeues the input — like an accepted steer, it has no turn of its own and
// would otherwise dangle in history unsent.
func (s *Service) injectMidTurn(a *activeTurn, session *agent.Session, msgID string, blocks []acp.ContentBlock, originChannel, originRef string) {
	_, err := s.agents.Prompt(a.ctx, session, blocks)
	if err != nil && a.ctx.Err() == nil {
		s.log.Warn("mid-turn prompt failed; queueing the input", "conversation", a.conversationID, "error", err)
		s.requeueSteerMessage(a.conversationID, msgID, originChannel, originRef)
	}
}

// requeueOrphanedSteers resends accepted steers the turn ended up dropping:
// inputs the agent acknowledged but never produced output for. Each claimed
// message is queued once via takeOrphanedSteers' delete.
func (s *Service) requeueOrphanedSteers(a *activeTurn) {
	for _, c := range a.takeOrphanedSteers() {
		s.requeueSteerMessage(a.conversationID, c.msgID, c.originChannel, c.originRef)
	}
}

func (s *Service) requeueSteerMessage(conversationID, msgID, originChannel, originRef string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	msg, err := s.store.GetMessage(ctx, msgID)
	if err != nil {
		s.log.Warn("failed to reload steered message", "conversation", conversationID, "message", msgID, "error", err)
		return
	}
	if _, err := s.queueStoredMessage(ctx, conversationID, msg.ID, originChannel, originRef); err != nil {
		s.log.Warn("failed to requeue steered message", "conversation", conversationID, "message", msgID, "error", err)
	}
}

func (s *Service) queueStoredMessage(ctx context.Context, conversationID, messageID, originChannel, originRef string) (store.Turn, error) {
	turn, err := s.store.CreateTurn(ctx, conversationID, messageID, originChannel, originRef)
	if err != nil {
		return turn, err
	}
	s.emit("turn.updated", conversationID, turn)
	s.log.Info("prompt queued", "conversation", conversationID, "turn", turn.ID, "channel", originChannel)
	go s.runQueue(conversationID)
	return turn, nil
}

// injectable reports whether a prompt is on the wire and can receive a steer.
// promptSent must have completed or the injected prompt could overtake the
// original one on the wire.
func (a *activeTurn) injectable() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.finished || a.session == nil || a.cancelRequested || !a.promptStarting {
		return false
	}
	if !isClosed(a.promptSent) {
		return false
	}
	return !isClosed(a.promptDone)
}
