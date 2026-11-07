package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jahrulnr/nusamation/internal/ids"
)

func (s *Store) AppendMessage(ctx context.Context, m *Message) error {
	if m.ID == "" {
		m.ID = ids.New("msg")
	}
	now := nowMS()
	if m.CreatedAt == 0 {
		m.CreatedAt = now
	}
	m.UpdatedAt = now
	if m.Status == "" {
		m.Status = "complete"
	}
	if m.Kind == "" {
		m.Kind = "text"
	}
	if len(m.Content) == 0 {
		m.Content = json.RawMessage("{}")
	}
	if len(m.Metadata) == 0 {
		m.Metadata = json.RawMessage("{}")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO messages
		(id,conversation_id,turn_id,seq,role,kind,text,content_json,status,channel,external_id,
		 acp_message_id,tool_call_id,metadata_json,created_at,updated_at)
		SELECT ?,?,?,COALESCE(MAX(seq),0)+1,?,?,?,?,?,?,?,?,?,?,?,?
		FROM messages WHERE conversation_id=?`,
		m.ID, m.ConversationID, m.TurnID, m.Role, m.Kind, m.Text, string(m.Content), m.Status,
		m.Channel, m.ExternalID, m.ACPMessageID, m.ToolCallID, string(m.Metadata), m.CreatedAt, m.UpdatedAt,
		m.ConversationID)
	if err != nil {
		return err
	}
	row := s.db.QueryRowContext(ctx, `SELECT seq FROM messages WHERE id=?`, m.ID)
	if err := row.Scan(&m.Seq); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE conversations SET updated_at=?, last_message_at=? WHERE id=?`,
		now, now, m.ConversationID)
	return err
}

func (s *Store) GetMessage(ctx context.Context, id string) (Message, error) {
	row := s.db.QueryRowContext(ctx, messageSelect+` WHERE m.id=?`, id)
	return scanMessage(row)
}

const messageSelect = `SELECT m.id,m.conversation_id,m.turn_id,m.seq,m.role,m.kind,m.text,m.content_json,
	m.status,m.channel,m.external_id,m.acp_message_id,m.tool_call_id,m.metadata_json,m.created_at,m.updated_at
	FROM messages m`

func scanMessage(row rowScanner) (Message, error) {
	var m Message
	var content, meta string
	err := row.Scan(&m.ID, &m.ConversationID, &m.TurnID, &m.Seq, &m.Role, &m.Kind, &m.Text,
		&content, &m.Status, &m.Channel, &m.ExternalID, &m.ACPMessageID, &m.ToolCallID,
		&meta, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return m, ErrNotFound
		}
		return m, err
	}
	m.Content = raw(content, "{}")
	m.Metadata = raw(meta, "{}")
	return m, nil
}

func (s *Store) ListMessages(ctx context.Context, conversationID string, afterSeq, beforeSeq int64, limit int) ([]Message, error) {
	if limit <= 0 || limit > 2000 {
		limit = 100
	}
	var rows *sql.Rows
	var err error
	switch {
	case afterSeq > 0:
		rows, err = s.db.QueryContext(ctx, messageSelect+` WHERE m.conversation_id=? AND m.seq>? ORDER BY m.seq ASC LIMIT ?`,
			conversationID, afterSeq, limit)
	case beforeSeq > 0:
		rows, err = s.db.QueryContext(ctx, messageSelect+` WHERE m.conversation_id=? AND m.seq<? ORDER BY m.seq DESC LIMIT ?`,
			conversationID, beforeSeq, limit)
	default:
		rows, err = s.db.QueryContext(ctx, messageSelect+` WHERE m.conversation_id=? ORDER BY m.seq DESC LIMIT ?`,
			conversationID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if afterSeq == 0 {
		// Descending (latest or pagination-before) gets reversed for the UI.
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	return out, nil
}

func (s *Store) UpdateMessageText(ctx context.Context, id, text, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE messages SET text=?,status=?,updated_at=? WHERE id=?`, text, status, nowMS(), id)
	return err
}

func (s *Store) UpdateMessage(ctx context.Context, id string, text *string, content json.RawMessage, status *string, meta json.RawMessage) error {
	cur, err := s.GetMessage(ctx, id)
	if err != nil {
		return err
	}
	if text != nil {
		cur.Text = *text
	}
	if len(content) > 0 {
		cur.Content = content
	}
	if status != nil {
		cur.Status = *status
	}
	if len(meta) > 0 {
		cur.Metadata = meta
	}
	_, err = s.db.ExecContext(ctx, `UPDATE messages SET text=?,content_json=?,status=?,metadata_json=?,updated_at=? WHERE id=?`,
		cur.Text, string(rawBytes(cur.Content, "{}")), cur.Status, string(rawBytes(cur.Metadata, "{}")), nowMS(), id)
	return err
}

func (s *Store) FinalizeMessage(ctx context.Context, id, text, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE messages SET text=?,status=?,updated_at=? WHERE id=?`, text, status, nowMS(), id)
	return err
}

func (s *Store) DeleteMessage(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM messages WHERE id=?`, id)
	return err
}

func (s *Store) FindToolMessage(ctx context.Context, conversationID, toolCallID string) (Message, error) {
	row := s.db.QueryRowContext(ctx, messageSelect+` WHERE m.conversation_id=? AND m.tool_call_id=? ORDER BY m.seq DESC LIMIT 1`,
		conversationID, toolCallID)
	return scanMessage(row)
}

func (s *Store) LastMessageByKind(ctx context.Context, conversationID, kind string) (Message, error) {
	row := s.db.QueryRowContext(ctx, messageSelect+` WHERE m.conversation_id=? AND m.kind=? ORDER BY m.seq DESC LIMIT 1`,
		conversationID, kind)
	return scanMessage(row)
}

func (s *Store) RecentTranscript(ctx context.Context, conversationID string, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 30
	}
	rows, err := s.db.QueryContext(ctx, messageSelect+` WHERE m.conversation_id=? AND m.kind IN ('text','tool') ORDER BY m.seq DESC LIMIT ?`,
		conversationID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

func BuildTranscriptRecap(msgs []Message, maxChars int) string {
	if maxChars <= 0 {
		maxChars = 12000
	}
	var b strings.Builder
	b.WriteString("## Context from earlier in this conversation\n\n")
	for _, m := range slices.Backward(msgs) {

		if m.Kind == "tool" {
			continue
		}
		text := strings.TrimSpace(m.Text)
		if text == "" {
			continue
		}
		role := m.Role
		if role == "assistant" {
			role = "Assistant"
		} else if role == "user" {
			role = "User"
		}
		entry := fmt.Sprintf("**%s:** %s\n\n", role, text)
		if b.Len()+len(entry) > maxChars {
			break
		}
		b.WriteString(entry)
	}
	b.WriteString("Continue seamlessly from this context. Do not mention this handoff unless the user asks.\n")
	return b.String()
}

func JoinMessageText(msgs []Message) string {
	var parts []string
	for _, m := range msgs {
		if strings.TrimSpace(m.Text) != "" {
			parts = append(parts, m.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}
