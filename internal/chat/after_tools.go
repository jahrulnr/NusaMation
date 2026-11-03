package chat

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jahrulnr/nusamation/internal/store"
)

const (
	deliveryQueue      = "queue"
	deliveryAfterTools = "after_tools"

	defaultCancelNoticeAfter = 15 * time.Second
	defaultTurnTimeout       = 6 * time.Hour
)

var ErrInvalidDelivery = errors.New("delivery must be queue or after_tools")

// watchTurn re-checks the after_tools boundary whenever the turn changes.
func (s *Service) watchTurn(a *activeTurn) {
	for {
		select {
		case <-a.wake:
			if a.afterToolsBoundaryReached() {
				s.requestGracefulCancel(a)
			}
		case <-a.finishedCh:
			return
		}
	}
}

// armAfterTools arms target when the just-queued input is the queue head and
// records the interrupted turn in the input's metadata. The input is already
// queued, so a failed metadata write is logged instead of failing the submit.
func (s *Service) armAfterTools(ctx context.Context, conversationID string, msg *store.Message, turn store.Turn, target *activeTurn) {
	head, err := s.store.NextQueuedTurn(ctx, conversationID)
	if err != nil {
		if !store.IsNotFound(err) {
			s.log.Warn("failed to read queue head", "conversation", conversationID, "error", err)
		}
		return
	}
	if head.ID != turn.ID {
		return
	}
	targetID, ok := target.armAfterTools()
	if !ok {
		return
	}
	meta := map[string]any{}
	_ = json.Unmarshal(msg.Metadata, &meta)
	meta["interruptTurnId"] = targetID
	metaJSON := mustJSON(meta)
	if err := s.store.UpdateMessage(ctx, msg.ID, nil, nil, nil, metaJSON); err != nil {
		s.log.Warn("failed to record after_tools target", "conversation", conversationID, "message", msg.ID, "error", err)
		return
	}
	msg.Metadata = metaJSON
}

// requestGracefulCancel asks the agent to stop the running prompt. The turn
// stays open until the agent answers that prompt. It reports whether this call
// started the cancel.
func (s *Service) requestGracefulCancel(a *activeTurn) bool {
	// promptMu is held through the cancelling status write so endPrompt cannot
	// complete the turn before that status is emitted.
	a.promptMu.Lock()
	defer a.promptMu.Unlock()
	a.mu.Lock()
	if a.finished || a.cancelRequested || isClosed(a.promptDone) {
		a.mu.Unlock()
		return false
	}
	a.cancelRequested = true
	close(a.cancelSignal)
	preDispatch := !a.promptStarting
	turnID := a.turn.ID
	a.mu.Unlock()

	if preDispatch {
		a.cancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = s.store.SetConversationStatus(ctx, a.conversationID, "cancelling")
	cancel()
	s.emit("turn.status", a.conversationID, map[string]any{"turnId": turnID, "status": "cancelling"})
	go s.sendRemoteCancel(a, turnID)
	go s.noticeIfSlow(a)
	return true
}

// sendRemoteCancel sends session/cancel only while the prompt is on the wire
// and has not returned, then rejects the turn's pending approvals. Approvals
// are rejected after the cancel is written so the agent sees the cancel first.
// It runs outside the ACP read loop.
func (s *Service) sendRemoteCancel(a *activeTurn, turnID string) {
	select {
	case <-a.promptSent:
	case <-a.promptDone:
	}
	a.promptMu.Lock()
	if !isClosed(a.promptDone) {
		a.mu.Lock()
		session := a.session
		a.mu.Unlock()
		if session != nil {
			if err := s.agents.Cancel(context.Background(), session); err != nil {
				s.log.Warn("failed to notify agent about cancel", "conversation", a.conversationID, "error", err)
			}
		}
	}
	a.promptMu.Unlock()
	s.cancelPendingApprovalsForTurn(turnID, "Turn cancelled by user.")
}

// noticeIfSlow emits one notice when the agent has not answered the cancelled
// prompt after cancelNoticeAfter. The turn keeps waiting regardless.
func (s *Service) noticeIfSlow(a *activeTurn) {
	t := time.NewTimer(s.cancelNoticeAfter)
	defer t.Stop()
	select {
	case <-a.promptDone:
		return
	case <-t.C:
	}
	s.emit("notice", a.conversationID, map[string]any{
		"severity": "info", "title": "Cancelling",
		"description": "Waiting for the agent to confirm the cancel. Queued messages run after it does.",
	})
}

// dropTimedOutSession discards the agent session after the turn deadline cut a
// prompt that may still be running remotely, so no later prompt reuses it. The
// next prompt starts a fresh session and recaps the transcript.
func (s *Service) dropTimedOutSession(a *activeTurn) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s.agents.ResetConversation(ctx, a.conversationID)
	if err := s.store.SetConversationACPSession(ctx, a.conversationID, ""); err != nil {
		s.log.Warn("failed to clear agent session", "conversation", a.conversationID, "error", err)
	}
	if _, err := s.patchMeta(ctx, a.conversationID, map[string]any{"handoff": true}); err != nil {
		s.log.Warn("failed to flag session recap", "conversation", a.conversationID, "error", err)
	}
}
