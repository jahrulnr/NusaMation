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
}

func (s *Store) SaveWhatsAppMessage(ctx context.Context, m WhatsAppMessage) error {
	if m.Chat == "" || m.ID == "" {
		return fmt.Errorf("WhatsApp message requires chat and message ID")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO whatsapp_messages
		(chat_jid,message_id,sender_jid,sender_name,chat_name,timestamp_ms,from_me,is_group,text,raw_json,is_live)
		VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(chat_jid,message_id) DO UPDATE SET
		sender_jid=excluded.sender_jid,sender_name=excluded.sender_name,
		chat_name=CASE WHEN excluded.chat_name <> '' THEN excluded.chat_name ELSE whatsapp_messages.chat_name END,
		timestamp_ms=excluded.timestamp_ms,from_me=excluded.from_me,is_group=excluded.is_group,
		text=excluded.text,raw_json=excluded.raw_json,is_live=excluded.is_live
		WHERE excluded.is_live > whatsapp_messages.is_live OR
		(excluded.is_live = whatsapp_messages.is_live AND excluded.timestamp_ms >= whatsapp_messages.timestamp_ms)`, m.Chat, m.ID, m.Sender, m.SenderName, m.ChatName, m.Timestamp, boolInt(m.FromMe), boolInt(m.IsGroup), m.Text, string(rawBytes(m.Raw, "{}")), boolInt(m.Live))
	return err
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
	where := []string{"instr(lower(text), lower(?)) > 0"}
	args := []any{f.Query}
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
	rows, err := s.db.QueryContext(ctx, `SELECT rowid,chat_jid,message_id,sender_jid,sender_name,chat_name,timestamp_ms,from_me,is_group,text,raw_json FROM whatsapp_messages WHERE `+strings.Join(where, " AND ")+` ORDER BY timestamp_ms DESC,rowid DESC LIMIT ?`, args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	var lastRow int64
	for rows.Next() {
		var m WhatsAppMessage
		var row int64
		var fromMe, group int
		var raw string
		if err := rows.Scan(&row, &m.Chat, &m.ID, &m.Sender, &m.SenderName, &m.ChatName, &m.Timestamp, &fromMe, &group, &m.Text, &raw); err != nil {
			return page, err
		}
		if len(page.Messages) == f.Limit {
			last := page.Messages[len(page.Messages)-1]
			page.NextCursor = fmt.Sprintf("%d:%d", last.Timestamp, lastRow)
			break
		}
		m.FromMe = fromMe != 0
		m.IsGroup = group != 0
		if f.Raw {
			m.Raw = json.RawMessage(raw)
		}
		page.Messages = append(page.Messages, m)
		lastRow = row
	}
	return page, rows.Err()
}
