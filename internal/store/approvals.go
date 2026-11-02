package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jahrulnr/nusamation/internal/ids"
)

func riskForKind(kind string) string {
	switch kind {
	case "delete", "execute", "switch_mode":
		return "high"
	case "edit", "move", "other":
		return "medium"
	case "read", "search", "fetch", "think":
		return "low"
	default:
		return "medium"
	}
}

func (s *Store) CreateApproval(ctx context.Context, a *Approval) error {
	if a.ID == "" {
		a.ID = ids.New("ap")
	}
	if a.CreatedAt == 0 {
		a.CreatedAt = nowMS()
	}
	if a.Status == "" {
		a.Status = "pending"
	}
	if len(a.Details) == 0 {
		a.Details = json.RawMessage("{}")
	}
	if a.Options == nil {
		a.Options = []PermissionOption{}
	}
	a.Risk = riskForKind(a.Kind)
	_, err := s.db.ExecContext(ctx, `INSERT INTO approvals
		(id,conversation_id,turn_id,tool_call_id,title,kind,details_json,options_json,status,
		 selected_option,created_at,expires_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.ConversationID, a.TurnID, a.ToolCallID, a.Title, a.Kind, string(a.Details),
		js(a.Options, "[]"), a.Status, a.SelectedOption, a.CreatedAt, a.ExpiresAt)
	return err
}

func scanApproval(row rowScanner, join bool) (Approval, error) {
	var a Approval
	var details, options string
	dest := []any{&a.ID, &a.ConversationID, &a.TurnID, &a.ToolCallID, &a.Title, &a.Kind,
		&details, &options, &a.Status, &a.SelectedOption, &a.CreatedAt, &a.ResolvedAt, &a.ExpiresAt}
	if join {
		dest = append(dest, &a.ConversationName, &a.AgentName)
	}
	if err := row.Scan(dest...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return a, ErrNotFound
		}
		return a, err
	}
	a.Details = raw(details, "{}")
	var opts []PermissionOption
	_ = json.Unmarshal([]byte(options), &opts)
	if opts == nil {
		opts = []PermissionOption{}
	}
	a.Options = opts
	a.Risk = riskForKind(a.Kind)
	return a, nil
}

func (s *Store) GetApproval(ctx context.Context, id string) (Approval, error) {
	row := s.db.QueryRowContext(ctx, `SELECT ap.id,ap.conversation_id,ap.turn_id,ap.tool_call_id,ap.title,ap.kind,
		ap.details_json,ap.options_json,ap.status,ap.selected_option,ap.created_at,ap.resolved_at,ap.expires_at,
		c.title,a.name
		FROM approvals ap JOIN conversations c ON c.id=ap.conversation_id JOIN agents a ON a.id=c.agent_id
		WHERE ap.id=?`, id)
	return scanApproval(row, true)
}

func (s *Store) ResolveApproval(ctx context.Context, id, optionID, status string) (Approval, error) {
	if status == "" {
		status = "approved"
	}
	_, err := s.db.ExecContext(ctx, `UPDATE approvals SET status=?,selected_option=?,resolved_at=?
		WHERE id=? AND status='pending'`, status, optionID, nowMS(), id)
	if err != nil {
		return Approval{}, err
	}
	return s.GetApproval(ctx, id)
}

func (s *Store) CancelPendingApprovalsForTurn(ctx context.Context, turnID string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE approvals SET status='cancelled',resolved_at=?
		WHERE turn_id=? AND status='pending'`, nowMS(), turnID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func (s *Store) CancelPendingApprovalsForConversation(ctx context.Context, conversationID string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE approvals SET status='cancelled',resolved_at=?
		WHERE conversation_id=? AND status='pending'`, nowMS(), conversationID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func (s *Store) PendingApprovalsForTurn(ctx context.Context, turnID string) ([]Approval, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ap.id,ap.conversation_id,ap.turn_id,ap.tool_call_id,ap.title,ap.kind,
		ap.details_json,ap.options_json,ap.status,ap.selected_option,ap.created_at,ap.resolved_at,ap.expires_at
		FROM approvals ap WHERE ap.turn_id=? AND ap.status='pending' ORDER BY ap.created_at ASC`, turnID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Approval
	for rows.Next() {
		a, err := scanApproval(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) CreateApprovalRule(ctx context.Context, r *ApprovalRule) (ApprovalRule, error) {
	if r.Scope == "" {
		r.Scope = "conversation"
	}
	if r.Action == "" {
		r.Action = "allow"
	}
	if r.ID == "" {
		r.ID = ids.New("rule")
	}
	r.CreatedAt = nowMS()
	_, err := s.db.ExecContext(ctx, `INSERT INTO approval_rules
		(id,scope,agent_id,conversation_id,tool_name,tool_kind,action,created_at)
		VALUES (?,?,?,?,?,?,?,?)`,
		r.ID, r.Scope, r.AgentID, r.ConversationID, r.ToolName, r.ToolKind, r.Action, r.CreatedAt)
	return *r, err
}

func (s *Store) MatchApprovalRule(ctx context.Context, agentID, conversationID, toolName, toolKind string) (ApprovalRule, bool, error) {
	var r ApprovalRule
	err := s.db.QueryRowContext(ctx, `SELECT id,scope,agent_id,conversation_id,tool_name,tool_kind,action,created_at
		FROM approval_rules
		WHERE action='allow'
		  AND ((scope='conversation' AND conversation_id=?) OR (scope='agent' AND agent_id=?))
		  AND (tool_name='' OR tool_name=?)
		  AND (tool_kind='' OR tool_kind=?)
		ORDER BY CASE WHEN scope='conversation' THEN 0 ELSE 1 END, created_at DESC LIMIT 1`,
		conversationID, agentID, toolName, toolKind).Scan(
		&r.ID, &r.Scope, &r.AgentID, &r.ConversationID, &r.ToolName, &r.ToolKind, &r.Action, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	if err != nil {
		return r, false, err
	}
	return r, true, nil
}

func (s *Store) ListApprovalRules(ctx context.Context, agentID, conversationID string) ([]ApprovalRule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,scope,agent_id,conversation_id,tool_name,tool_kind,action,created_at
		FROM approval_rules WHERE agent_id=? OR conversation_id=? ORDER BY created_at DESC`,
		agentID, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ApprovalRule
	for rows.Next() {
		var r ApprovalRule
		if err := rows.Scan(&r.ID, &r.Scope, &r.AgentID, &r.ConversationID, &r.ToolName, &r.ToolKind, &r.Action, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) DeleteApprovalRule(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM approval_rules WHERE id=?`, id)
	return err
}

func (s *Store) DeleteApprovalRulesForConversation(ctx context.Context, conversationID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM approval_rules WHERE conversation_id=? AND scope='conversation'`, conversationID)
	return err
}

func (s *Store) CancelAllPendingApprovals(ctx context.Context, reason string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE approvals SET status='cancelled',resolved_at=? WHERE status='pending'`, nowMS())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func (s *Store) ResetConversationStatuses(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE conversations SET status='idle'
		WHERE status IN ('active','starting','waiting_approval','cancelling')`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func (s *Store) ApprovalCounts(ctx context.Context) (pending, approved, denied int, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(CASE WHEN status='pending' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='approved' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status IN ('denied','rejected') THEN 1 ELSE 0 END),0)
		FROM approvals`).Scan(&pending, &approved, &denied)
	return
}

func toolKindRisk(kind string) string { return riskForKind(kind) }

func normalizeOptionKind(k string) string { return strings.ToLower(strings.TrimSpace(k)) }
