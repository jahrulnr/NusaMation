package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jahrulnr/nusamation/internal/acp"
	"github.com/jahrulnr/nusamation/internal/agent"
	"github.com/jahrulnr/nusamation/internal/store"
	"github.com/jahrulnr/nusamation/internal/textfmt"
)

type approvalDecision struct {
	OptionID  string
	Cancelled bool
}

var cancelledPermission = acp.PermissionResponse{Outcome: acp.PermissionOutcome{Outcome: "cancelled"}}

// Permission is called by the ACP client while the agent is waiting for a
// user decision. It blocks until the web UI (or a channel command) answers.
func (s *Service) Permission(ctx context.Context, sess *agent.Session, req acp.PermissionRequest) (acp.PermissionResponse, error) {
	a := s.activeFor(sess.ConversationID())
	if a == nil {
		return cancelledPermission, nil
	}
	if len(req.Options) == 0 || isClosed(a.cancelSignal) {
		return cancelledPermission, nil
	}

	var tool acp.ToolCall
	_ = json.Unmarshal(req.ToolCall, &tool)
	if tool.ToolCallID != "" {
		s.applyToolUpdate(a, acp.SessionUpdate{
			SessionUpdate: "tool_call", ToolCallID: tool.ToolCallID, Title: tool.Title, Name: tool.Name,
			Kind: tool.Kind, Status: tool.Status, Locations: tool.Locations, RawInput: tool.RawInput,
			RawOutput: tool.RawOutput, Content: tool.Content,
		})
	}

	title := tool.Title
	if title == "" {
		title = tool.Name
	}
	if title == "" {
		title = "Tool call"
	}
	kind := strings.ToLower(strings.TrimSpace(tool.Kind))
	if kind == "" {
		kind = "other"
	}
	opts := make([]store.PermissionOption, 0, len(req.Options))
	for _, o := range req.Options {
		opts = append(opts, store.PermissionOption{OptionID: o.OptionID, Name: o.Name, Kind: o.Kind})
	}

	allow, auto, autoOption := s.autoApprove(ctx, a, tool.Name, kind, opts)
	if allow {
		ap, _ := s.createApproval(ctx, a, title, kind, req.ToolCall, req.Options, "pending", "")
		if ap.ID != "" {
			ap, _ = s.store.ResolveApproval(ctx, ap.ID, autoOption, "approved")
			s.emit("approval.auto", a.conversationID, map[string]any{
				"approval": ap, "reason": auto, "conversationId": a.conversationID,
			})
		}
		return selected(autoOption), nil
	}

	ap, err := s.createApproval(ctx, a, title, kind, req.ToolCall, req.Options, "pending", "")
	if err != nil {
		return cancelledPermission, err
	}
	ch := make(chan approvalDecision, 1)
	s.mu.Lock()
	s.waiters[ap.ID] = ch
	s.mu.Unlock()

	_ = s.store.SetTurnWaiting(context.Background(), a.turn.ID)
	_ = s.store.SetConversationStatus(context.Background(), a.conversationID, "waiting_approval")
	a.mu.Lock()
	a.turn.Status = "waiting_approval"
	a.approvals[ap.ID] = true
	a.signal()
	a.mu.Unlock()
	s.emit("turn.status", a.conversationID, map[string]any{
		"turnId": a.turn.ID, "status": "waiting_approval", "approvalId": ap.ID,
	})
	s.emit("approval.created", a.conversationID, map[string]any{
		"approval": ap, "conversationId": a.conversationID, "agent": a.agent.Public(),
	})
	s.notifyApproval(a, ap)

	var decision approvalDecision
	select {
	case decision = <-ch:
	case <-a.ctx.Done():
		decision = approvalDecision{Cancelled: true}
	case <-a.cancelSignal:
		decision = approvalDecision{Cancelled: true}
	case <-ctx.Done():
		decision = approvalDecision{Cancelled: true}
	}
	s.mu.Lock()
	if s.waiters[ap.ID] == ch {
		delete(s.waiters, ap.ID)
	}
	s.mu.Unlock()
	a.mu.Lock()
	delete(a.approvals, ap.ID)
	a.signal()
	a.mu.Unlock()

	if decision.Cancelled || decision.OptionID == "" {
		updated, _ := s.store.ResolveApproval(context.Background(), ap.ID, "", "cancelled")
		s.emit("approval.resolved", a.conversationID, map[string]any{
			"approval": updated, "conversationId": a.conversationID,
		})
		return cancelledPermission, nil
	}
	option, ok := findOption(opts, decision.OptionID)
	if !ok {
		updated, _ := s.store.ResolveApproval(context.Background(), ap.ID, "", "cancelled")
		s.emit("approval.resolved", a.conversationID, map[string]any{
			"approval": updated, "conversationId": a.conversationID,
		})
		return cancelledPermission, nil
	}
	status := "approved"
	if strings.HasPrefix(strings.ToLower(option.Kind), "reject") {
		status = "denied"
	}
	updated, _ := s.store.ResolveApproval(context.Background(), ap.ID, option.OptionID, status)
	if status == "approved" && strings.HasPrefix(strings.ToLower(option.Kind), "allow_always") {
		s.rememberRule(a, tool.Name, kind)
	}
	_ = s.store.SetTurnRunning(context.Background(), a.turn.ID)
	_ = s.store.SetConversationStatus(context.Background(), a.conversationID, "active")
	a.mu.Lock()
	a.turn.Status = "running"
	a.mu.Unlock()
	s.emit("approval.resolved", a.conversationID, map[string]any{
		"approval": updated, "conversationId": a.conversationID,
	})
	s.emit("turn.status", a.conversationID, map[string]any{
		"turnId": a.turn.ID, "status": "running", "approvalId": ap.ID,
	})
	return selected(option.OptionID), nil
}

func (s *Service) autoApprove(ctx context.Context, a *activeTurn, toolName, kind string, opts []store.PermissionOption) (allow bool, reason, optionID string) {
	option, ok := autoOption(opts)
	if !ok {
		return false, "", ""
	}
	policy := strings.ToLower(strings.TrimSpace(a.agent.ApprovalPolicy))
	if policy == "" {
		policy = "ask"
	}
	switch policy {
	case "auto_all":
		return true, "approval policy: auto-all", option
	case "auto_safe":
		if isSafeToolKind(kind) {
			return true, "approval policy: auto-safe", option
		}
	}
	rule, found, err := s.store.MatchApprovalRule(ctx, a.agent.ID, a.conversationID, toolName, kind)
	if err == nil && found {
		return true, "remembered rule: " + rule.ToolName + rule.ToolKind, option
	}
	return false, "", ""
}

func (s *Service) createApproval(ctx context.Context, a *activeTurn, title, kind string, toolCall json.RawMessage, options []acp.PermissionOption, status, selectedID string) (store.Approval, error) {
	opts := make([]store.PermissionOption, 0, len(options))
	for _, o := range options {
		opts = append(opts, store.PermissionOption{OptionID: o.OptionID, Name: o.Name, Kind: o.Kind})
	}
	details, _ := json.Marshal(map[string]any{
		"toolCall": toolCall, "toolName": toolNameFromRaw(toolCall), "toolKind": kind,
		"conversationId": a.conversationID, "agentId": a.agent.ID,
	})
	ap := store.Approval{
		ConversationID: a.conversationID, TurnID: a.turn.ID, ToolCallID: toolCallIDFromRaw(toolCall),
		Title: title, Kind: kind, Details: details, Options: opts, Status: status,
		SelectedOption: selectedID,
	}
	if err := s.store.CreateApproval(ctx, &ap); err != nil {
		return ap, err
	}
	return s.store.GetApproval(ctx, ap.ID)
}

func (s *Service) notifyApproval(a *activeTurn, ap store.Approval) {
	a.mu.Lock()
	routing := a.conv.Routing
	a.mu.Unlock()
	if !routing.NotifyApprovals {
		return
	}
	ch, target, ok := s.originChannel(a)
	if !ok {
		return
	}
	text := fmt.Sprintf("Approval needed: %s\nRisk: %s\nID: %s\nReply /approve %s or /deny %s",
		textfmt.OneLine(ap.Title, 160), ap.Risk, ap.ID, ap.ID, ap.ID)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := ch.Notify(ctx, target, text); err != nil {
			s.log.Warn("failed to notify approval", "channel", ch.Name(), "error", err)
		}
	}()
}

func (s *Service) ResolveApproval(ctx context.Context, approvalID, optionID string) (store.Approval, error) {
	ap, err := s.store.GetApproval(ctx, approvalID)
	if err != nil {
		return ap, err
	}
	if ap.Status != "pending" {
		return ap, ErrApprovalGone
	}
	status := "approved"
	if optionID == "" || optionID == "cancelled" || optionID == "cancel" {
		status = "cancelled"
		optionID = ""
	} else {
		opt, ok := findOption(ap.Options, optionID)
		if !ok {
			return ap, fmt.Errorf("option %q is not part of this approval", optionID)
		}
		if strings.HasPrefix(strings.ToLower(opt.Kind), "reject") {
			status = "denied"
		}
	}
	updated, err := s.store.ResolveApproval(ctx, approvalID, optionID, status)
	if err != nil {
		return updated, err
	}
	s.mu.Lock()
	ch := s.waiters[approvalID]
	delete(s.waiters, approvalID)
	s.mu.Unlock()
	if ch != nil {
		select {
		case ch <- approvalDecision{OptionID: optionID, Cancelled: status == "cancelled"}:
		default:
		}
	}
	_ = s.store.SetConversationStatus(ctx, updated.ConversationID, "active")
	s.emit("approval.resolved", updated.ConversationID, map[string]any{
		"approval": updated, "conversationId": updated.ConversationID,
	})
	s.emit("turn.status", updated.ConversationID, map[string]any{
		"turnId": updated.TurnID, "status": "running", "approvalId": updated.ID,
	})
	if ch == nil {
		s.log.Warn("approval resolved without an active waiter", "approval", approvalID)
	}
	return updated, nil
}

func (s *Service) cancelPendingApprovals(conversationID, reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	list, err := s.store.ListApprovalsForConversation(ctx, conversationID, 100)
	if err != nil {
		return
	}
	for _, ap := range list {
		if ap.Status != "pending" {
			continue
		}
		updated, _ := s.store.ResolveApproval(ctx, ap.ID, "", "cancelled")
		s.mu.Lock()
		ch := s.waiters[ap.ID]
		delete(s.waiters, ap.ID)
		s.mu.Unlock()
		if ch != nil {
			select {
			case ch <- approvalDecision{Cancelled: true}:
			default:
			}
		}
		s.emit("approval.resolved", conversationID, map[string]any{
			"approval": updated, "conversationId": conversationID, "reason": reason,
		})
	}
}

func (s *Service) cancelPendingApprovalsForTurn(turnID, reason string) {
	if turnID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	list, err := s.store.PendingApprovalsForTurn(ctx, turnID)
	if err != nil {
		return
	}
	for _, ap := range list {
		updated, _ := s.store.ResolveApproval(ctx, ap.ID, "", "cancelled")
		s.mu.Lock()
		ch := s.waiters[ap.ID]
		delete(s.waiters, ap.ID)
		s.mu.Unlock()
		if ch != nil {
			select {
			case ch <- approvalDecision{Cancelled: true}:
			default:
			}
		}
		s.emit("approval.resolved", ap.ConversationID, map[string]any{
			"approval": updated, "conversationId": ap.ConversationID, "reason": reason,
		})
	}
}

func (s *Service) rememberRule(a *activeTurn, toolName, kind string) {
	if strings.TrimSpace(toolName) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rules, err := s.store.ListApprovalRules(ctx, a.agent.ID, a.conversationID)
	if err == nil {
		for _, r := range rules {
			if r.ToolName == toolName && r.ToolKind == kind && r.Action == "allow" {
				return
			}
		}
	}
	rule := store.ApprovalRule{
		Scope: "conversation", AgentID: a.agent.ID, ConversationID: a.conversationID,
		ToolName: toolName, ToolKind: kind, Action: "allow",
	}
	if _, err := s.store.CreateApprovalRule(ctx, &rule); err == nil {
		s.emit("approval.rule", a.conversationID, map[string]any{"rule": rule})
	}
}

func selected(optionID string) acp.PermissionResponse {
	return acp.PermissionResponse{Outcome: acp.PermissionOutcome{Outcome: "selected", OptionID: optionID}}
}

func autoOption(opts []store.PermissionOption) (string, bool) {
	for _, kind := range []string{"allow_once", "allow_always"} {
		for _, o := range opts {
			if strings.EqualFold(o.Kind, kind) {
				return o.OptionID, true
			}
		}
	}
	return "", false
}

func findOption(opts []store.PermissionOption, id string) (store.PermissionOption, bool) {
	for _, o := range opts {
		if o.OptionID == id {
			return o, true
		}
	}
	return store.PermissionOption{}, false
}

func isSafeToolKind(kind string) bool {
	switch strings.ToLower(kind) {
	case "read", "search", "fetch", "think":
		return true
	}
	return false
}

func toolNameFromRaw(raw json.RawMessage) string {
	var t acp.ToolCall
	_ = json.Unmarshal(raw, &t)
	if t.Name != "" {
		return t.Name
	}
	if t.Title != "" {
		return t.Title
	}
	return t.ToolCallID
}

func toolCallIDFromRaw(raw json.RawMessage) string {
	var t acp.ToolCall
	_ = json.Unmarshal(raw, &t)
	return t.ToolCallID
}

// ErrNoWaiter is kept for API callers that want to distinguish an orphaned
// approval from a successful resolution.
var ErrNoWaiter = errors.New("approval is not waiting on an active turn")
