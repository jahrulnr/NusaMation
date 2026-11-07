package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jahrulnr/nusamation/internal/agent"
	"github.com/jahrulnr/nusamation/internal/browser"
	"github.com/jahrulnr/nusamation/internal/chat"
	"github.com/jahrulnr/nusamation/internal/store"
)

func (s *Server) handlePrepareConversation(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	if err := s.chat.PrepareSession(ctx, r.PathValue("id")); err != nil {
		if authErr, ok := errors.AsType[*agent.AuthRequiredError](err); ok {
			writeJSON(w, http.StatusForbidden, map[string]any{"code": "agent_auth_required", "error": err.Error(), "authMethods": authErr.Methods})
			return
		}
		writeError(w, http.StatusBadGateway, "prepare_failed", err.Error())
		return
	}
	conv, err := s.store.GetConversation(ctx, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	configs, _ := s.chat.ConversationConfig(ctx, conv.ID)
	writeJSON(w, http.StatusOK, map[string]any{"conversation": conv, "configs": configs})
}

func (s *Server) handleElicitationResponse(w http.ResponseWriter, r *http.Request) {
	var response chat.ElicitationResponse
	if err := readJSON(w, r, &response, 256<<10); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid response.")
		return
	}
	if err := s.chat.ResolveElicitation(r.PathValue("id"), r.PathValue("elicitationId"), response); err != nil {
		writeError(w, http.StatusConflict, "elicitation_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type messageView struct {
	store.Message
	Attachments []store.Upload `json:"attachments,omitempty"`
}

func (s *Server) hydrateMessages(r *http.Request, msgs []store.Message) []messageView {
	ids := make([]string, 0, len(msgs))
	for _, m := range msgs {
		ids = append(ids, m.ID)
	}
	byMessage := map[string][]store.Upload{}
	if ups, err := s.store.ListUploadsForMessages(r.Context(), ids); err == nil {
		for _, u := range ups {
			byMessage[u.MessageID] = append(byMessage[u.MessageID], u)
		}
	}
	out := make([]messageView, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, messageView{Message: m, Attachments: byMessage[m.ID]})
	}
	return out
}

func (s *Server) handleListConversations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	archived := q.Get("archived") == "1" || strings.EqualFold(q.Get("archived"), "true")
	limit := min(parseInt(q.Get("limit"), 100), 500)
	list, err := s.store.ListConversations(r.Context(), store.ConversationFilter{
		Query: q.Get("q"), AgentID: q.Get("agentId"), Channel: q.Get("channel"),
		Archived: archived, Limit: limit, Offset: parseInt(q.Get("offset"), 0),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "conversations", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversations": list})
}

func (s *Server) handleCreateConversation(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AgentID string `json:"agentId"`
		Title   string `json:"title"`
		Cwd     string `json:"cwd"`
	}
	if err := readJSON(w, r, &in, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid conversation payload.")
		return
	}
	conv, err := s.chat.CreateConversation(r.Context(), in.AgentID, in.Title, in.Cwd, "web", "")
	if err != nil {
		writeError(w, http.StatusBadRequest, "create_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"conversation": conv})
}

func (s *Server) handleGetConversation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	conv, err := s.store.GetConversation(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Conversation not found.")
		return
	}
	msgs, _ := s.store.ListMessages(r.Context(), id, 0, 0, 150)
	turns := make(map[string]store.Turn)
	for _, msg := range msgs {
		if msg.TurnID == "" {
			continue
		}
		if _, ok := turns[msg.TurnID]; ok {
			continue
		}
		if turn, err := s.store.GetTurn(r.Context(), msg.TurnID); err == nil {
			turns[msg.TurnID] = turn
		}
	}
	// Queued turns have no messages yet, so include them for per-message queue status.
	queued, err := s.store.ListQueuedTurnsForConversation(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "turns", err.Error())
		return
	}
	for _, turn := range queued {
		turns[turn.ID] = turn
	}
	approvals, _ := s.store.ListApprovalsForConversation(r.Context(), id, 100)
	views := s.hydrateMessages(r, msgs)
	liveTexts := s.chat.ActiveMessageTexts(id)
	for i := range views {
		if text, ok := liveTexts[views[i].ID]; ok {
			views[i].Text = text
		}
	}
	configs, _ := s.chat.ConversationConfig(r.Context(), id)
	activeTurnID, activeStatus, active := s.chat.ActiveStatus(id)
	if active {
		if turn, err := s.store.GetTurn(r.Context(), activeTurnID); err == nil {
			turns[turn.ID] = turn
		}
	}
	ag, _ := s.store.GetAgent(r.Context(), conv.AgentID)
	writeJSON(w, http.StatusOK, map[string]any{
		"conversation": conv,
		"messages":     views,
		"turns":        turns,
		"approvals":    approvals,
		"elicitations": s.chat.PendingElicitations(id),
		"configs":      configs,
		"active":       map[string]any{"running": active, "turnId": activeTurnID, "status": activeStatus},
		"agent":        ag.Public(),
	})
}

func (s *Server) handleUpdateConversation(w http.ResponseWriter, r *http.Request) {
	var in chat.ConversationPatch
	if err := readJSON(w, r, &in, 256<<10); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid conversation update.")
		return
	}
	conv, err := s.chat.UpdateConversation(r.Context(), r.PathValue("id"), in)
	if err != nil {
		writeError(w, http.StatusBadRequest, "update_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversation": conv})
}

func (s *Server) handleDeleteConversation(w http.ResponseWriter, r *http.Request) {
	if err := s.chat.DeleteConversation(r.Context(), r.PathValue("id")); err != nil {
		if store.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "not_found", "Conversation not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "delete_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleDeleteAllConversations(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Confirm string `json:"confirm"`
	}
	if err := readJSON(w, r, &in, 64<<10); err != nil || in.Confirm != "DELETE" {
		writeError(w, http.StatusBadRequest, "confirm_required", `Send {"confirm":"DELETE"} to erase all conversations.`)
		return
	}
	// Drain in pages: each deleted conversation removes its uploads, so a fixed
	// 500-item window would orphan upload files for older conversations.
	for {
		list, err := s.store.ListConversations(r.Context(), store.ConversationFilter{Limit: 200, Archived: true})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "list_failed", err.Error())
			return
		}
		active, err := s.store.ListConversations(r.Context(), store.ConversationFilter{Limit: 200})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "list_failed", err.Error())
			return
		}
		list = append(list, active...)
		if len(list) == 0 {
			break
		}
		progressed := false
		for _, c := range list {
			// DeleteConversation removes the conversation's uploads only after
			// the delete succeeds, so a failed delete keeps its files.
			if err := s.chat.DeleteConversation(r.Context(), c.ID); err != nil {
				s.log.Warn("could not delete conversation during erase", "id", c.ID, "error", err)
				continue
			}
			progressed = true
		}
		if !progressed {
			break
		}
	}
	if err := s.store.DeleteAllConversations(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "delete_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	after := int64(parseInt(q.Get("after"), 0))
	before := int64(parseInt(q.Get("before"), 0))
	limit := min(parseInt(q.Get("limit"), 100), 500)
	msgs, err := s.store.ListMessages(r.Context(), r.PathValue("id"), after, before, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "messages", err.Error())
		return
	}
	turns := make(map[string]store.Turn)
	for _, msg := range msgs {
		if msg.TurnID == "" {
			continue
		}
		if _, ok := turns[msg.TurnID]; !ok {
			if turn, err := s.store.GetTurn(r.Context(), msg.TurnID); err == nil {
				turns[msg.TurnID] = turn
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"messages": s.hydrateMessages(r, msgs),
		"turns":    turns,
		"hasMore":  len(msgs) == limit,
	})
}

func (s *Server) handlePrompt(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text        string                  `json:"text"`
		Attachments []store.AttachmentInput `json:"attachments"`
		Annotations []struct {
			PickID  string `json:"pickId"`
			Comment string `json:"comment"`
		} `json:"annotations"`
		ReplyToChannel bool   `json:"replyToChannel"`
		ReplyChannel   string `json:"replyChannel"`
		Delivery       string `json:"delivery"`
	}
	if err := readJSON(w, r, &in, 1<<20); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid message payload.")
		return
	}
	convID := r.PathValue("id")
	var annotations []store.AnnotationInput
	for _, a := range in.Annotations {
		pick, ok := s.resolvePick(a.PickID)
		if !ok || (pick.ConversationID != "" && pick.ConversationID != convID) {
			writeError(w, http.StatusBadRequest, "annotation_invalid",
				"Unknown annotation pick or it belongs to another conversation.")
			return
		}
		annotations = append(annotations, store.AnnotationInput{
			PickID: pick.ID, Comment: a.Comment, Descriptor: pick.Descriptor, Upload: pick.Upload,
		})
	}
	msg, turn, err := s.chat.Submit(r.Context(), chat.SubmitInput{
		ConversationID: convID, Text: in.Text, Attachments: in.Attachments,
		Channel: "web", ReplyToChannel: in.ReplyToChannel, ReplyChannel: in.ReplyChannel,
		Delivery: in.Delivery, Annotations: annotations,
	})
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, chat.ErrInvalidPrompt) {
			status = http.StatusUnprocessableEntity
		}
		writeError(w, status, "prompt_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"message": s.hydrateMessagesOne(r, msg), "turn": turn})
}

// resolvePick resolves a browser annotation pick id; the pick store is the
// single source of truth for descriptors (client-submitted ones are ignored).
func (s *Server) resolvePick(pickID string) (*browser.Pick, bool) {
	if s.browser == nil || pickID == "" {
		return nil, false
	}
	return s.browser.Pick(pickID)
}

func (s *Server) hydrateMessagesOne(r *http.Request, m store.Message) messageView {
	views := s.hydrateMessages(r, []store.Message{m})
	if len(views) == 0 {
		return messageView{Message: m}
	}
	return views[0]
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	if err := s.chat.Cancel(r.Context(), r.PathValue("id")); err != nil {
		status := http.StatusConflict
		if errors.Is(err, chat.ErrNoActiveTurn) {
			status = http.StatusNotFound
		}
		writeError(w, status, "cancel_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleCancelQueuedTurn cancels a single queued turn without touching the
// running one. With {"dropMessage": true} the queued user message is deleted
// too, which the edit flow uses before resending the corrected prompt.
func (s *Server) handleCancelQueuedTurn(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DropMessage bool `json:"dropMessage"`
	}
	if r.ContentLength != 0 {
		if err := readJSON(w, r, &in, 0); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
	}
	turn, err := s.chat.CancelQueued(r.Context(), r.PathValue("id"), r.PathValue("turnId"), in.DropMessage)
	if err != nil {
		status := http.StatusBadRequest
		switch {
		case errors.Is(err, store.ErrNotFound):
			status = http.StatusNotFound
		case errors.Is(err, store.ErrNotQueued):
			status = http.StatusConflict
		}
		writeError(w, status, "cancel_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"turn": turn})
}

func (s *Server) handleRetry(w http.ResponseWriter, r *http.Request) {
	turn, err := s.chat.Retry(r.Context(), r.PathValue("id"), "web", "")
	if err != nil {
		writeError(w, http.StatusBadRequest, "retry_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"turn": turn})
}

func (s *Server) handleSetConfig(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ConfigID string          `json:"configId"`
		Value    json.RawMessage `json:"value"`
		Model    string          `json:"model"`
	}
	if err := readJSON(w, r, &in, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid config payload.")
		return
	}
	var conv store.Conversation
	var err error
	if in.Model != "" {
		conv, err = s.chat.SetModel(r.Context(), r.PathValue("id"), in.Model)
	} else {
		var value any
		if len(in.Value) == 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "A config value is required.")
			return
		}
		if err := json.Unmarshal(in.Value, &value); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "Invalid config value.")
			return
		}
		conv, err = s.chat.SetConfig(r.Context(), r.PathValue("id"), in.ConfigID, value)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "config_failed", err.Error())
		return
	}
	configs, _ := s.chat.ConversationConfig(r.Context(), conv.ID)
	writeJSON(w, http.StatusOK, map[string]any{"conversation": conv, "configs": configs})
}

func (s *Server) handleSwitchAgent(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AgentID string `json:"agentId"`
	}
	if err := readJSON(w, r, &in, 64<<10); err != nil || in.AgentID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Choose an agent.")
		return
	}
	conv, err := s.chat.SwitchAgent(r.Context(), r.PathValue("id"), in.AgentID)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, chat.ErrBusy) {
			status = http.StatusConflict
		}
		writeError(w, status, "switch_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversation": conv})
}

func (s *Server) handleRouting(w http.ResponseWriter, r *http.Request) {
	var routing store.Routing
	if err := readJSON(w, r, &routing, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid routing payload.")
		return
	}
	conv, err := s.chat.UpdateConversation(r.Context(), r.PathValue("id"), chat.ConversationPatch{Routing: &routing})
	if err != nil {
		writeError(w, http.StatusBadRequest, "routing_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversation": conv})
}

func (s *Server) handleResolveApproval(w http.ResponseWriter, r *http.Request) {
	convID, approvalID := r.PathValue("id"), r.PathValue("approvalId")
	ap, err := s.store.GetApproval(r.Context(), approvalID)
	if err != nil || ap.ConversationID != convID {
		writeError(w, http.StatusNotFound, "not_found", "Approval not found in this conversation.")
		return
	}
	s.resolveApproval(w, r, approvalID)
}

func (s *Server) handleResolveApprovalByID(w http.ResponseWriter, r *http.Request) {
	s.resolveApproval(w, r, r.PathValue("id"))
}

func (s *Server) resolveApproval(w http.ResponseWriter, r *http.Request, approvalID string) {
	var in struct {
		OptionID string `json:"optionId"`
	}
	if err := readJSON(w, r, &in, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Choose an option.")
		return
	}
	ap, err := s.chat.ResolveApproval(r.Context(), approvalID, in.OptionID)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, chat.ErrApprovalGone) {
			status = http.StatusConflict
		}
		writeError(w, status, "approval_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"approval": ap})
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	conv, msgs, err := s.store.ExportConversation(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "Conversation not found.")
		return
	}
	approvals, _ := s.store.ListApprovalsForConversation(r.Context(), conv.ID, 500)
	payload := map[string]any{
		"exportedAt":   nowMillis(),
		"conversation": conv,
		"messages":     s.hydrateMessages(r, msgs),
		"approvals":    approvals,
	}
	filename := fmt.Sprintf("nusamation-%s.json", conv.ID)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	writeJSON(w, http.StatusOK, payload)
}
