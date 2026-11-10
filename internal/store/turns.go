package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/jahrulnr/nusamation/internal/ids"
)

func (s *Store) CreateTurn(ctx context.Context, conversationID, userMessageID, originChannel, originRef string) (Turn, error) {
	t := Turn{
		ID:             ids.New("turn"),
		ConversationID: conversationID,
		UserMessageID:  userMessageID,
		Status:         "queued",
		OriginChannel:  originChannel,
		OriginRef:      originRef,
		CreatedAt:      nowMS(),
	}
	if t.OriginChannel == "" {
		t.OriginChannel = "web"
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO turns
		(id,conversation_id,user_message_id,status,origin_channel,origin_ref,queue_position,created_at)
		VALUES (?,?,?,?,?,?,
		 (SELECT COUNT(*)+1 FROM turns WHERE conversation_id=? AND status IN ('queued','running','waiting_approval')),?)`,
		t.ID, t.ConversationID, t.UserMessageID, t.Status, t.OriginChannel, t.OriginRef, conversationID, t.CreatedAt)
	if err != nil {
		return t, err
	}
	return s.GetTurn(ctx, t.ID)
}

func (s *Store) GetTurn(ctx context.Context, id string) (Turn, error) {
	var t Turn
	err := s.db.QueryRowContext(ctx, `SELECT id,conversation_id,user_message_id,status,stop_reason,error,
		origin_channel,origin_ref,queue_position,started_at,ended_at,created_at FROM turns WHERE id=?`, id).
		Scan(&t.ID, &t.ConversationID, &t.UserMessageID, &t.Status, &t.StopReason, &t.Error,
			&t.OriginChannel, &t.OriginRef, &t.QueuePosition, &t.StartedAt, &t.EndedAt, &t.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

// StartTurn claims a queued turn for processing. It reports false when the
// turn left 'queued' first (e.g. cancelled before the worker reached it), so
// a raced cancel is never resurrected into 'running'.
func (s *Store) StartTurn(ctx context.Context, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE turns SET status='running',started_at=? WHERE id=? AND status='queued'`, nowMS(), id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *Store) SetTurnWaiting(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE turns SET status='waiting_approval' WHERE id=?`, id)
	return err
}

func (s *Store) SetTurnRunning(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE turns SET status='running' WHERE id=?`, id)
	return err
}

func (s *Store) FinishTurn(ctx context.Context, id, status, stopReason, errMsg string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE turns SET status=?,stop_reason=?,error=?,ended_at=? WHERE id=?`,
		status, stopReason, errMsg, nowMS(), id)
	return err
}

func (s *Store) ActiveTurn(ctx context.Context, conversationID string) (Turn, error) {
	var t Turn
	err := s.db.QueryRowContext(ctx, `SELECT id,conversation_id,user_message_id,status,stop_reason,error,
		origin_channel,origin_ref,queue_position,started_at,ended_at,created_at FROM turns
		WHERE conversation_id=? AND status IN ('running','waiting_approval') ORDER BY created_at ASC LIMIT 1`, conversationID).
		Scan(&t.ID, &t.ConversationID, &t.UserMessageID, &t.Status, &t.StopReason, &t.Error,
			&t.OriginChannel, &t.OriginRef, &t.QueuePosition, &t.StartedAt, &t.EndedAt, &t.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

func (s *Store) NextQueuedTurn(ctx context.Context, conversationID string) (Turn, error) {
	var t Turn
	err := s.db.QueryRowContext(ctx, `SELECT id,conversation_id,user_message_id,status,stop_reason,error,
		origin_channel,origin_ref,queue_position,started_at,ended_at,created_at FROM turns
		WHERE conversation_id=? AND status='queued' ORDER BY created_at ASC LIMIT 1`, conversationID).
		Scan(&t.ID, &t.ConversationID, &t.UserMessageID, &t.Status, &t.StopReason, &t.Error,
			&t.OriginChannel, &t.OriginRef, &t.QueuePosition, &t.StartedAt, &t.EndedAt, &t.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

func (s *Store) CancelQueuedTurn(ctx context.Context, conversationID string) (Turn, error) {
	t, err := s.NextQueuedTurn(ctx, conversationID)
	if err != nil {
		return t, err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE turns SET status='cancelled',stop_reason='cancelled',ended_at=? WHERE id=? AND status='queued'`, nowMS(), t.ID)
	if err != nil {
		return t, err
	}
	return s.GetTurn(ctx, t.ID)
}

// CancelQueuedTurnByID cancels one specific queued turn, unlike
// CancelQueuedTurn which always targets the queue head. ErrNotFound means the
// turn does not exist in this conversation; ErrNotQueued means it exists but
// is no longer waiting (already running or finished).
func (s *Store) CancelQueuedTurnByID(ctx context.Context, conversationID, turnID string) (Turn, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE turns SET status='cancelled',stop_reason='cancelled',ended_at=? WHERE id=? AND conversation_id=? AND status='queued'`, nowMS(), turnID, conversationID)
	if err != nil {
		return Turn{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		t, gerr := s.GetTurn(ctx, turnID)
		if gerr != nil && !IsNotFound(gerr) {
			return Turn{}, gerr
		}
		if gerr != nil || t.ConversationID != conversationID {
			return Turn{}, ErrNotFound
		}
		return t, ErrNotQueued
	}
	return s.GetTurn(ctx, turnID)
}

func (s *Store) QueuedCount(ctx context.Context, conversationID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM turns WHERE conversation_id=? AND status='queued'`, conversationID).Scan(&n)
	return n, err
}

func (s *Store) MarkInterruptedTurns(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE turns SET status='interrupted',error=?,ended_at=?
		WHERE status IN ('running','waiting_approval')`, "Server restarted while this turn was running.", nowMS())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func (s *Store) ListQueuedTurns(ctx context.Context) ([]Turn, error) {
	return s.queuedTurns(ctx, `SELECT id,conversation_id,user_message_id,status,stop_reason,error,
		origin_channel,origin_ref,queue_position,started_at,ended_at,created_at FROM turns
		WHERE status='queued' ORDER BY created_at ASC`)
}

func (s *Store) ListQueuedTurnsForConversation(ctx context.Context, conversationID string) ([]Turn, error) {
	return s.queuedTurns(ctx, `SELECT id,conversation_id,user_message_id,status,stop_reason,error,
		origin_channel,origin_ref,queue_position,started_at,ended_at,created_at FROM turns
		WHERE conversation_id=? AND status='queued' ORDER BY created_at ASC`, conversationID)
}

func (s *Store) queuedTurns(ctx context.Context, query string, args ...any) ([]Turn, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Turn
	for rows.Next() {
		var t Turn
		if err := rows.Scan(&t.ID, &t.ConversationID, &t.UserMessageID, &t.Status, &t.StopReason, &t.Error,
			&t.OriginChannel, &t.OriginRef, &t.QueuePosition, &t.StartedAt, &t.EndedAt, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) LastUserMessage(ctx context.Context, conversationID string) (Message, error) {
	row := s.db.QueryRowContext(ctx, messageSelect+` WHERE m.conversation_id=? AND m.role='user' ORDER BY m.seq DESC LIMIT 1`, conversationID)
	return scanMessage(row)
}

// ListLastTurnSummaries returns the newest turn per conversation, including
// the user prompt and latest assistant text. One query for the whole list.
func (s *Store) ListLastTurnSummaries(ctx context.Context, conversationIDs []string) (map[string]TurnSummary, error) {
	out := map[string]TurnSummary{}
	if len(conversationIDs) == 0 {
		return out, nil
	}
	placeholders := make([]string, len(conversationIDs))
	args := make([]any, len(conversationIDs))
	for i, id := range conversationIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	query := `WITH ranked AS (
		SELECT t.id,t.conversation_id,t.user_message_id,t.status,t.stop_reason,t.error,t.created_at,t.started_at,t.ended_at,t.rowid AS turn_rowid,
		       ROW_NUMBER() OVER (PARTITION BY t.conversation_id ORDER BY t.created_at DESC, t.rowid DESC) AS rn
		FROM turns t WHERE t.conversation_id IN (` + strings.Join(placeholders, ",") + `)
	)
	SELECT r.conversation_id,r.id,r.status,r.stop_reason,r.error,r.created_at,r.started_at,r.ended_at,
	       COALESCE((SELECT m.text FROM messages m WHERE m.id=r.user_message_id LIMIT 1),''),
	       COALESCE((SELECT m.text FROM messages m WHERE m.turn_id=r.id AND m.role='assistant' AND m.kind='text'
	                 ORDER BY m.seq DESC LIMIT 1),'')
	FROM ranked r WHERE r.rn=1`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var summary TurnSummary
		var conversationID string
		if err := rows.Scan(&conversationID, &summary.ID, &summary.Status, &summary.StopReason, &summary.Error,
			&summary.CreatedAt, &summary.StartedAt, &summary.EndedAt, &summary.Prompt, &summary.Answer); err != nil {
			return nil, err
		}
		out[conversationID] = summary
	}
	return out, rows.Err()
}

func (s *Store) LastCompletedTurn(ctx context.Context, conversationID string) (Turn, error) {
	var t Turn
	err := s.db.QueryRowContext(ctx, `SELECT id,conversation_id,user_message_id,status,stop_reason,error,
		origin_channel,origin_ref,queue_position,started_at,ended_at,created_at FROM turns
		WHERE conversation_id=? AND status IN ('completed','error','cancelled') ORDER BY created_at DESC LIMIT 1`, conversationID).
		Scan(&t.ID, &t.ConversationID, &t.UserMessageID, &t.Status, &t.StopReason, &t.Error,
			&t.OriginChannel, &t.OriginRef, &t.QueuePosition, &t.StartedAt, &t.EndedAt, &t.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}
