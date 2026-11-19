package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type WhatsAppMessage struct {
	Chat       string          `json:"chat"`
	ID         string          `json:"id"`
	Sender     string          `json:"sender"`
	SenderName string          `json:"senderName,omitempty"`
	ChatName   string          `json:"chatName,omitempty"`
	Timestamp  int64           `json:"timestampMs"`
	FromMe     bool            `json:"fromMe"`
	IsGroup    bool            `json:"isGroup"`
	Text       string          `json:"text"`
	Raw        json.RawMessage `json:"raw,omitempty"`
	Live       bool            `json:"-"`
	Receipt    string          `json:"receipt,omitempty"`
}

func (s *Store) SaveWhatsAppMessage(ctx context.Context, m WhatsAppMessage) error {
	if m.Chat == "" || m.ID == "" {
		return fmt.Errorf("WhatsApp message requires chat and message ID")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO whatsapp_messages
		(chat_jid,message_id,sender_jid,sender_name,chat_name,timestamp_ms,from_me,is_group,text,raw_json,is_live,receipt)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(chat_jid,message_id) DO UPDATE SET
		sender_jid=excluded.sender_jid,sender_name=excluded.sender_name,
		chat_name=CASE WHEN excluded.chat_name <> '' THEN excluded.chat_name ELSE whatsapp_messages.chat_name END,
		timestamp_ms=excluded.timestamp_ms,from_me=excluded.from_me,is_group=excluded.is_group,
		text=excluded.text,raw_json=excluded.raw_json,is_live=excluded.is_live
		WHERE excluded.is_live > whatsapp_messages.is_live OR
		(excluded.is_live = whatsapp_messages.is_live AND excluded.timestamp_ms >= whatsapp_messages.timestamp_ms)`, m.Chat, m.ID, m.Sender, m.SenderName, m.ChatName, m.Timestamp, boolInt(m.FromMe), boolInt(m.IsGroup), m.Text, string(rawBytes(m.Raw, "{}")), boolInt(m.Live), m.Receipt)
	return err
}

// RevokeWhatsAppMessage tombstones an indexed message that was deleted for
// everyone: the text and raw payload are cleared so a deleted message can no
// longer be searched or read out of the index. Unknown rows are ignored.
func (s *Store) RevokeWhatsAppMessage(ctx context.Context, chat, id string) error {
	if chat == "" || id == "" {
		return fmt.Errorf("WhatsApp revoke requires chat and message ID")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE whatsapp_messages
		SET text='', raw_json='', receipt='' WHERE chat_jid=? AND message_id=? AND (text<>'' OR raw_json<>'')`, chat, id)
	return err
}

// UpdateWhatsAppMessageText rewrites only the text of an indexed message
// (used when the sender edits their own outgoing message), keeping sender and
// metadata intact. The FTS trigger keeps the search index in sync.
func (s *Store) UpdateWhatsAppMessageText(ctx context.Context, chat, id, text string) error {
	if chat == "" || id == "" {
		return fmt.Errorf("WhatsApp text update requires chat and message ID")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE whatsapp_messages SET text=? WHERE chat_jid=? AND message_id=?`, text, chat, id)
	return err
}

// MarkWhatsAppReceipt records the furthest delivery state ("delivered" or
// "read") of outgoing messages in one chat.
func (s *Store) MarkWhatsAppReceipt(ctx context.Context, chat string, ids []string, receipt string) error {
	if chat == "" || receipt == "" || len(ids) == 0 {
		return fmt.Errorf("WhatsApp receipt requires a chat, message ids and a state")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE whatsapp_messages
			SET receipt = CASE WHEN receipt = 'read' THEN receipt WHEN ? = 'read' THEN 'read' ELSE 'delivered' END
			WHERE chat_jid=? AND message_id=? AND from_me=1`, receipt, chat, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PurgeWhatsAppMessages removes indexed messages older than the cutoff (Unix
// milliseconds). Returns the number of removed rows.
func (s *Store) PurgeWhatsAppMessages(ctx context.Context, beforeMs int64) (int64, error) {
	if beforeMs <= 0 {
		return 0, fmt.Errorf("purge cutoff must be a positive Unix timestamp")
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM whatsapp_messages WHERE timestamp_ms < ?`, beforeMs)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

type WhatsAppSearch struct {
	Query, Chat, Sender, Cursor string
	Limit                       int
	Raw                         bool
}
type WhatsAppSearchPage struct {
	Messages   []WhatsAppMessage `json:"messages"`
	NextCursor string            `json:"nextCursor,omitempty"`
}

func (s *Store) SearchWhatsAppMessages(ctx context.Context, f WhatsAppSearch) (WhatsAppSearchPage, error) {
	page := WhatsAppSearchPage{Messages: []WhatsAppMessage{}}
	if f.Limit < 1 || f.Limit > 500 {
		return page, fmt.Errorf("limit must be between 1 and 500")
	}
	// Prefer the FTS5 index (Unicode case-folding, indexed lookup). Match the
	// whole query as one phrase so user punctuation cannot break the syntax;
	// fall back to the full-scan substring match when the index is unusable.
	if query, ok := ftsPhrase(f.Query); ok {
		where := []string{"rowid IN (SELECT rowid FROM whatsapp_messages_fts WHERE whatsapp_messages_fts MATCH ?)"}
		args := []any{query}
		if page, err := s.searchWhatsAppWhere(ctx, f, where, args); err == nil {
			return page, nil
		}
	}
	where := []string{"instr(lower(text), lower(?)) > 0"}
	args := []any{f.Query}
	return s.searchWhatsAppWhere(ctx, f, where, args)
}

// ftsPhrase wraps the query in double quotes for a phrase MATCH, rejecting
// queries that are empty or contain their own quotes. Surrounding quotes are
// unwrapped so a quoted search still finds the phrase.
func ftsPhrase(query string) (string, bool) {
	query = strings.TrimSpace(query)
	if len(query) >= 2 && strings.HasPrefix(query, `"`) && strings.HasSuffix(query, `"`) {
		query = strings.TrimSpace(query[1 : len(query)-1])
	}
	if query == "" || strings.ContainsAny(query, "\"'") {
		return "", false
	}
	return `"` + query + `"`, true
}

func (s *Store) searchWhatsAppWhere(ctx context.Context, f WhatsAppSearch, where []string, args []any) (WhatsAppSearchPage, error) {
	page := WhatsAppSearchPage{Messages: []WhatsAppMessage{}}
	if f.Chat != "" {
		where = append(where, "chat_jid=?")
		args = append(args, f.Chat)
	}
	if f.Sender != "" {
		where = append(where, "sender_jid=?")
		args = append(args, f.Sender)
	}
	if f.Cursor != "" {
		parts := strings.Split(f.Cursor, ":")
		if len(parts) != 2 {
			return page, fmt.Errorf("invalid cursor")
		}
		ts, e1 := strconv.ParseInt(parts[0], 10, 64)
		row, e2 := strconv.ParseInt(parts[1], 10, 64)
		if e1 != nil || e2 != nil || row < 1 {
			return page, fmt.Errorf("invalid cursor")
		}
		where = append(where, "(timestamp_ms < ? OR (timestamp_ms = ? AND rowid < ?))")
		args = append(args, ts, ts, row)
	}
	args = append(args, f.Limit+1)
	rows, err := s.db.QueryContext(ctx, `SELECT rowid,chat_jid,message_id,sender_jid,sender_name,chat_name,timestamp_ms,from_me,is_group,text,raw_json,receipt FROM whatsapp_messages WHERE `+strings.Join(where, " AND ")+` ORDER BY timestamp_ms DESC,rowid DESC LIMIT ?`, args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	var lastRow int64
	for rows.Next() {
		var m WhatsAppMessage
		var row int64
		var fromMe, group int
		var raw, receipt string
		if err := rows.Scan(&row, &m.Chat, &m.ID, &m.Sender, &m.SenderName, &m.ChatName, &m.Timestamp, &fromMe, &group, &m.Text, &raw, &receipt); err != nil {
			return page, err
		}
		if len(page.Messages) == f.Limit {
			last := page.Messages[len(page.Messages)-1]
			page.NextCursor = fmt.Sprintf("%d:%d", last.Timestamp, lastRow)
			break
		}
		m.FromMe = fromMe != 0
		m.IsGroup = group != 0
		m.Receipt = receipt
		if f.Raw {
			m.Raw = json.RawMessage(raw)
		}
		page.Messages = append(page.Messages, m)
		lastRow = row
	}
	return page, rows.Err()
}
