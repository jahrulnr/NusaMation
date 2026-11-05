package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jahrulnr/nusamation/internal/ids"
)

func (s *Store) CreateConversation(ctx context.Context, agentID, title, cwd, originChannel, originRef, model string) (Conversation, error) {
	now := nowMS()
	id := ids.New("cv")
	if strings.TrimSpace(title) == "" {
		title = "New conversation"
	}
	if originChannel == "" {
		originChannel = "web"
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO conversations
		(id,title,agent_id,acp_session_id,cwd,status,origin_channel,origin_ref,model,config_options_json,
		 routing_json,pinned,archived,plan_json,usage_json,metadata_json,created_at,updated_at,last_message_at)
		VALUES (?,?,?,'',?,'idle',?,?,?,'[]',?,0,0,'[]','{}','{}',?,?,?)`,
		id, title, agentID, cwd, originChannel, originRef, model, js(DefaultRouting(), "{}"), now, now, now)
	if err != nil {
		return Conversation{}, err
	}
	return s.GetConversation(ctx, id)
}

func (s *Store) GetConversation(ctx context.Context, id string) (Conversation, error) {
	row := s.db.QueryRowContext(ctx, conversationSelect+` WHERE c.id=?`, id)
	c, err := s.scanConversation(row)
	if err != nil {
		return c, err
	}
	c.Links, _ = s.ListChannelLinks(ctx, id)
	return c, nil
}

const conversationSelect = `
SELECT c.id,c.title,c.agent_id,a.name,a.icon,a.color,c.acp_session_id,c.cwd,c.status,
       c.origin_channel,c.origin_ref,c.model,c.config_options_json,c.routing_json,c.pinned,
       c.archived,c.plan_json,c.usage_json,c.metadata_json,c.created_at,c.updated_at,c.last_message_at,
       COALESCE((SELECT CASE WHEN m.kind='tool' THEN COALESCE(json_extract(m.content_json,'$.title'),m.text) ELSE m.text END
                 FROM messages m WHERE m.conversation_id=c.id ORDER BY m.seq DESC LIMIT 1),'') AS last_text,
       COALESCE((SELECT m.role FROM messages m WHERE m.conversation_id=c.id ORDER BY m.seq DESC LIMIT 1),'') AS last_role,
       (SELECT COUNT(*) FROM messages m WHERE m.conversation_id=c.id) AS msg_count,
       (SELECT COUNT(*) FROM approvals ap WHERE ap.conversation_id=c.id AND ap.status='pending') AS pending_count
FROM conversations c JOIN agents a ON a.id=c.agent_id`

func (s *Store) scanConversation(row rowScanner) (Conversation, error) {
	var c Conversation
	var configs, routing, plan, usage, meta string
	var pinned, archived int
	err := row.Scan(&c.ID, &c.Title, &c.AgentID, &c.AgentName, &c.AgentIcon, &c.AgentColor,
		&c.ACPSessionID, &c.Cwd, &c.Status, &c.OriginChannel, &c.OriginRef, &c.Model,
		&configs, &routing, &pinned, &archived, &plan, &usage, &meta, &c.CreatedAt, &c.UpdatedAt,
		&c.LastMessageAt, &c.LastMessage, &c.LastMessageRole, &c.MessageCount, &c.PendingCount)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return c, ErrNotFound
		}
		return c, err
	}
	c.Pinned = pinned != 0
	c.Archived = archived != 0
	c.ConfigOptions = raw(configs, "[]")
	c.Plan = raw(plan, "[]")
	c.Metadata = raw(meta, "{}")
	c.Routing = DefaultRouting()
	_ = json.Unmarshal([]byte(routing), &c.Routing)
	_ = json.Unmarshal([]byte(usage), &c.Usage)
	return c, nil
}

type ConversationUpdate struct {
	Title         *string
	AgentID       *string
	Pinned        *bool
	Archived      *bool
	Routing       *Routing
	Model         *string
	ConfigOptions json.RawMessage
}

func (s *Store) UpdateConversation(ctx context.Context, id string, up ConversationUpdate) (Conversation, error) {
	cur, err := s.GetConversation(ctx, id)
	if err != nil {
		return cur, err
	}
	if up.Title != nil {
		cur.Title = strings.TrimSpace(*up.Title)
		if cur.Title == "" {
			cur.Title = "Untitled"
		}
	}
	if up.AgentID != nil && *up.AgentID != cur.AgentID {
		cur.AgentID = *up.AgentID
		// Agent changes always start a fresh ACP session; history is replayed by the app.
		cur.ACPSessionID = ""
		cur.ConfigOptions = json.RawMessage("[]")
		cur.Model = ""
	}
	if up.Pinned != nil {
		cur.Pinned = *up.Pinned
	}
	if up.Archived != nil {
		cur.Archived = *up.Archived
	}
	if up.Routing != nil {
		cur.Routing = *up.Routing
	}
	if up.Model != nil {
		cur.Model = strings.TrimSpace(*up.Model)
	}
	if len(up.ConfigOptions) > 0 {
		cur.ConfigOptions = up.ConfigOptions
	}
	_, err = s.db.ExecContext(ctx, `UPDATE conversations SET title=?,agent_id=?,acp_session_id=?,model=?,config_options_json=?,
		routing_json=?,pinned=?,archived=?,updated_at=? WHERE id=?`,
		cur.Title, cur.AgentID, cur.ACPSessionID, cur.Model, string(rawBytes(cur.ConfigOptions, "[]")),
		js(cur.Routing, "{}"), boolInt(cur.Pinned), boolInt(cur.Archived), nowMS(), id)
	if err != nil {
		return cur, err
	}
	return s.GetConversation(ctx, id)
}

func (s *Store) DeleteConversation(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM conversations WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) EnsureConversationForAgent(ctx context.Context, agentID, cwd string) (Conversation, error) {
	c, err := s.CreateConversation(ctx, agentID, "New conversation", cwd, "web", "", "")
	return c, err
}

func (s *Store) SetConversationACPSession(ctx context.Context, id, sessionID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if sessionID != "" {
		_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO conversation_acp_sessions (conversation_id,agent_id,agent_slug,session_id)
			SELECT c.id,c.agent_id,a.slug,? FROM conversations c JOIN agents a ON a.id=c.agent_id WHERE c.id=?`, sessionID, id)
		if err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE conversations SET acp_session_id=?,updated_at=? WHERE id=?`, sessionID, nowMS(), id); err != nil {
		return err
	}
	return tx.Commit()
}

type ConversationACPSession struct{ AgentID, AgentSlug, SessionID string }

func (s *Store) ListConversationACPSessions(ctx context.Context, id string) ([]ConversationACPSession, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT agent_id,agent_slug,session_id FROM conversation_acp_sessions WHERE conversation_id=?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sessions []ConversationACPSession
	for rows.Next() {
		var session ConversationACPSession
		if err := rows.Scan(&session.AgentID, &session.AgentSlug, &session.SessionID); err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

func (s *Store) SetConversationStatus(ctx context.Context, id, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE conversations SET status=?,updated_at=? WHERE id=?`, status, nowMS(), id)
	return err
}

func (s *Store) SetConversationConfig(ctx context.Context, id, model string, configs json.RawMessage) error {
	_, err := s.db.ExecContext(ctx, `UPDATE conversations SET model=?,config_options_json=?,updated_at=? WHERE id=?`,
		model, string(rawBytes(configs, "[]")), nowMS(), id)
	return err
}

func (s *Store) SetConversationModel(ctx context.Context, id, model string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE conversations SET model=?,updated_at=? WHERE id=?`, model, nowMS(), id)
	return err
}

func (s *Store) SetConversationCwd(ctx context.Context, id, cwd string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE conversations SET cwd=?,updated_at=? WHERE id=?`, cwd, nowMS(), id)
	return err
}

func (s *Store) SetConversationPlan(ctx context.Context, id string, plan json.RawMessage) error {
	_, err := s.db.ExecContext(ctx, `UPDATE conversations SET plan_json=?,updated_at=? WHERE id=?`,
		string(rawBytes(plan, "[]")), nowMS(), id)
	return err
}

func (s *Store) SetConversationUsage(ctx context.Context, id string, usage Usage) error {
	_, err := s.db.ExecContext(ctx, `UPDATE conversations SET usage_json=?,updated_at=? WHERE id=?`,
		js(usage, "{}"), nowMS(), id)
	return err
}

func (s *Store) TouchConversation(ctx context.Context, id, title, lastMessage string, at int64) error {
	if at == 0 {
		at = nowMS()
	}
	if title != "" {
		_, err := s.db.ExecContext(ctx, `UPDATE conversations SET updated_at=?,last_message_at=?,title=? WHERE id=?`, at, at, title, id)
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE conversations SET updated_at=?,last_message_at=? WHERE id=?`, at, at, id)
	return err
}

func (s *Store) ListConversations(ctx context.Context, f ConversationFilter) ([]Conversation, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	where := []string{"c.archived=?"}
	args := []any{boolInt(f.Archived)}
	if f.AgentID != "" {
		where = append(where, "c.agent_id=?")
		args = append(args, f.AgentID)
	}
	if f.Channel != "" {
		where = append(where, "EXISTS(SELECT 1 FROM channel_links l WHERE l.conversation_id=c.id AND l.channel=?)")
		args = append(args, f.Channel)
	}
	if f.Pinned != nil {
		where = append(where, "c.pinned=?")
		args = append(args, boolInt(*f.Pinned))
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		like := "%" + q + "%"
		where = append(where, `(c.title LIKE ? OR EXISTS(SELECT 1 FROM messages m WHERE m.conversation_id=c.id AND m.text LIKE ?))`)
		args = append(args, like, like)
	}
	args = append(args, f.Limit, f.Offset)
	q := conversationSelect + " WHERE " + strings.Join(where, " AND ") +
		" ORDER BY c.pinned DESC, c.last_message_at DESC, c.updated_at DESC LIMIT ? OFFSET ?"
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Conversation
	for rows.Next() {
		c, err := s.scanConversation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) ListChannelLinks(ctx context.Context, conversationID string) ([]ChannelLink, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,conversation_id,channel,external_id,thread_id,display_name,is_group,created_at
		FROM channel_links WHERE conversation_id=? ORDER BY created_at ASC`, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChannelLink
	for rows.Next() {
		var l ChannelLink
		var isGroup int
		if err := rows.Scan(&l.ID, &l.ConversationID, &l.Channel, &l.ExternalID, &l.ThreadID, &l.DisplayName, &isGroup, &l.CreatedAt); err != nil {
			return nil, err
		}
		l.IsGroup = isGroup != 0
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) AddChannelLink(ctx context.Context, l ChannelLink) (ChannelLink, error) {
	if l.ID == "" {
		l.ID = ids.New("ch")
	}
	l.CreatedAt = nowMS()
	_, err := s.db.ExecContext(ctx, `INSERT INTO channel_links
		(id,conversation_id,channel,external_id,thread_id,display_name,is_group,created_at)
		VALUES (?,?,?,?,?,?,?,?)`,
		l.ID, l.ConversationID, l.Channel, l.ExternalID, l.ThreadID, l.DisplayName, boolInt(l.IsGroup), l.CreatedAt)
	if err != nil {
		return l, err
	}
	return l, nil
}

func (s *Store) FindConversationByChannel(ctx context.Context, channel, externalID, threadID string) (Conversation, error) {
	row := s.db.QueryRowContext(ctx, conversationSelect+`
		WHERE c.id=(SELECT conversation_id FROM channel_links WHERE channel=? AND external_id=? AND thread_id=? LIMIT 1)`,
		channel, externalID, threadID)
	c, err := s.scanConversation(row)
	if err != nil {
		return c, err
	}
	c.Links, _ = s.ListChannelLinks(ctx, c.ID)
	return c, nil
}

// MoveChannelLink atomically (re)assigns a chat to a conversation. The unique
// (channel, external_id, thread_id) constraint makes a delete-then-insert
// sequence lose the link entirely if the insert fails; an upsert cannot.
func (s *Store) MoveChannelLink(ctx context.Context, l ChannelLink) (ChannelLink, error) {
	l.CreatedAt = nowMS()
	_, err := s.db.ExecContext(ctx, `INSERT INTO channel_links
		(id,conversation_id,channel,external_id,thread_id,display_name,is_group,created_at)
		VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT(channel,external_id,thread_id) DO UPDATE SET
		conversation_id=excluded.conversation_id,
		display_name=excluded.display_name,
		is_group=excluded.is_group`,
		ids.New("ch"), l.ConversationID, l.Channel, l.ExternalID, l.ThreadID,
		l.DisplayName, boolInt(l.IsGroup), l.CreatedAt)
	if err != nil {
		return l, err
	}
	_ = s.db.QueryRowContext(ctx, `SELECT id FROM channel_links
		WHERE channel=? AND external_id=? AND thread_id=?`,
		l.Channel, l.ExternalID, l.ThreadID).Scan(&l.ID)
	return l, nil
}

func (s *Store) RemoveChannelLink(ctx context.Context, conversationID, channel, externalID, threadID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM channel_links
		WHERE conversation_id=? AND channel=? AND external_id=? AND thread_id=?`,
		conversationID, channel, externalID, threadID)
	return err
}

func (s *Store) AttachInboundConversation(ctx context.Context, channel, messageID, threadID, conversationID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE inbound_messages SET conversation_id=?
		WHERE channel=? AND external_id=? AND thread_id=?`, conversationID, channel, messageID, threadID)
	return err
}

func (s *Store) ClaimInbound(ctx context.Context, channel, externalID, threadID, conversationID string) (bool, error) {
	if externalID == "" {
		return true, nil
	}
	res, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO inbound_messages
		(channel,external_id,thread_id,conversation_id,created_at) VALUES (?,?,?,?,?)`,
		channel, externalID, threadID, conversationID, nowMS())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ReleaseInbound drops a dedupe claim so a failed message can be retried
// instead of being silently swallowed as a duplicate.
func (s *Store) ReleaseInbound(ctx context.Context, channel, externalID, threadID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM inbound_messages
		WHERE channel=? AND external_id=? AND thread_id=?`, channel, externalID, threadID)
	return err
}

func (s *Store) ConversationStats(ctx context.Context) (total, active, pending int, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM conversations WHERE archived=0),
		(SELECT COUNT(*) FROM conversations WHERE archived=0 AND status IN ('active','starting','waiting_approval')),
		(SELECT COUNT(*) FROM approvals WHERE status='pending')`).Scan(&total, &active, &pending)
	return
}

func (s *Store) ListPendingApprovals(ctx context.Context, limit int) ([]Approval, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT ap.id,ap.conversation_id,ap.turn_id,ap.tool_call_id,ap.title,ap.kind,
		ap.details_json,ap.options_json,ap.status,ap.selected_option,ap.created_at,ap.resolved_at,ap.expires_at,
		c.title,a.name
		FROM approvals ap JOIN conversations c ON c.id=ap.conversation_id JOIN agents a ON a.id=c.agent_id
		WHERE ap.status='pending' ORDER BY ap.created_at ASC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Approval
	for rows.Next() {
		a, err := scanApproval(rows, true)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) ListApprovalsForConversation(ctx context.Context, conversationID string, limit int) ([]Approval, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT ap.id,ap.conversation_id,ap.turn_id,ap.tool_call_id,ap.title,ap.kind,
		ap.details_json,ap.options_json,ap.status,ap.selected_option,ap.created_at,ap.resolved_at,ap.expires_at
		FROM approvals ap WHERE ap.conversation_id=? ORDER BY ap.created_at DESC LIMIT ?`, conversationID, limit)
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

func (s *Store) UpdateConversationMetadata(ctx context.Context, id string, meta json.RawMessage) error {
	_, err := s.db.ExecContext(ctx, `UPDATE conversations SET metadata_json=?,updated_at=? WHERE id=?`,
		string(rawBytes(meta, "{}")), nowMS(), id)
	return err
}

func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return v, err
}

func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES (?,?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`, key, value, nowMS())
	return err
}

func (s *Store) GetJSONSetting(ctx context.Context, key string, dst any) error {
	v, err := s.GetSetting(ctx, key)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(v), dst)
}

func (s *Store) SetJSONSetting(ctx context.Context, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.SetSetting(ctx, key, string(b))
}

func (s *Store) ListSettings(ctx context.Context) ([]Setting, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key,value,updated_at FROM settings ORDER BY key ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Setting
	for rows.Next() {
		var st Setting
		if err := rows.Scan(&st.Key, &st.Value, &st.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func (s *Store) DeleteSetting(ctx context.Context, key string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM settings WHERE key=?`, key)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteAllConversations(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM conversations`)
	return err
}

func (s *Store) ExportConversation(ctx context.Context, id string) (Conversation, []Message, error) {
	c, err := s.GetConversation(ctx, id)
	if err != nil {
		return c, nil, err
	}
	msgs, err := s.ListMessages(ctx, id, 0, 0, 10000)
	return c, msgs, err
}

func (s *Store) AgentConversationCount(ctx context.Context, agentID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM conversations WHERE agent_id=?`, agentID).Scan(&n)
	return n, err
}

func (s *Store) conversationExists(ctx context.Context, id string) bool {
	var one int
	return s.db.QueryRowContext(ctx, `SELECT 1 FROM conversations WHERE id=?`, id).Scan(&one) == nil
}

var _ = fmt.Sprintf
