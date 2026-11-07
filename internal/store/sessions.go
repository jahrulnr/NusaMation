package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/jahrulnr/nusamation/internal/ids"
)

func (s *Store) CreateSession(ctx context.Context, idHash, csrf, ip, ua string, ttlMS int64) error {
	now := nowMS()
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions
		(id_hash,csrf,created_at,last_seen_at,expires_at,ip,user_agent) VALUES (?,?,?,?,?,?,?)`,
		idHash, csrf, now, now, now+ttlMS, ip, ua)
	return err
}

func (s *Store) GetSession(ctx context.Context, idHash string) (Session, error) {
	var ss Session
	err := s.db.QueryRowContext(ctx, `SELECT id_hash,csrf,created_at,last_seen_at,expires_at,ip,user_agent
		FROM sessions WHERE id_hash=?`, idHash).Scan(&ss.IDHash, &ss.CSRF, &ss.CreatedAt, &ss.LastSeenAt, &ss.ExpiresAt, &ss.IP, &ss.UserAgent)
	if errors.Is(err, sql.ErrNoRows) {
		return ss, ErrNotFound
	}
	return ss, err
}

func (s *Store) TouchSession(ctx context.Context, idHash string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET last_seen_at=? WHERE id_hash=?`, nowMS(), idHash)
	return err
}

func (s *Store) DeleteSession(ctx context.Context, idHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash=?`, idHash)
	return err
}

func (s *Store) DeleteExpiredSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at<?`, nowMS())
	return err
}

func (s *Store) DeleteAllSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions`)
	return err
}

func (s *Store) CreateUpload(ctx context.Context, u *Upload) error {
	if u.ID == "" {
		u.ID = ids.New("up")
	}
	if u.CreatedAt == 0 {
		u.CreatedAt = nowMS()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO uploads
		(id,conversation_id,message_id,name,mime,size,path,created_at) VALUES (?,?,?,?,?,?,?,?)`,
		u.ID, u.ConversationID, u.MessageID, u.Name, u.MIME, u.Size, u.Path, u.CreatedAt)
	return err
}

func (s *Store) GetUpload(ctx context.Context, id string) (Upload, error) {
	var u Upload
	err := s.db.QueryRowContext(ctx, `SELECT id,conversation_id,message_id,name,mime,size,path,created_at
		FROM uploads WHERE id=?`, id).Scan(&u.ID, &u.ConversationID, &u.MessageID, &u.Name, &u.MIME, &u.Size, &u.Path, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

func (s *Store) BindUploadsToMessage(ctx context.Context, ids []string, conversationID, messageID string) error {
	for _, id := range ids {
		if _, err := s.db.ExecContext(ctx, `UPDATE uploads SET conversation_id=?,message_id=? WHERE id=?`, conversationID, messageID, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ListUploadsForMessage(ctx context.Context, messageID string) ([]Upload, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,conversation_id,message_id,name,mime,size,path,created_at
		FROM uploads WHERE message_id=? ORDER BY created_at ASC`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Upload
	for rows.Next() {
		var u Upload
		if err := rows.Scan(&u.ID, &u.ConversationID, &u.MessageID, &u.Name, &u.MIME, &u.Size, &u.Path, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// uploadBatchSize stays below SQLite's variable limit (~999) for IN clauses.
const uploadBatchSize = 500

// ListUploadsForMessages hydrates a page of messages' attachments in a single
// round trip instead of one query per message.
func (s *Store) ListUploadsForMessages(ctx context.Context, messageIDs []string) ([]Upload, error) {
	var out []Upload
	for start := 0; start < len(messageIDs); start += uploadBatchSize {
		end := min(start+uploadBatchSize, len(messageIDs))
		chunk := messageIDs[start:end]
		args := make([]any, 0, len(chunk))
		for _, id := range chunk {
			args = append(args, id)
		}
		rows, err := s.db.QueryContext(ctx, `SELECT id,conversation_id,message_id,name,mime,size,path,created_at
			FROM uploads WHERE message_id IN (`+strings.Repeat("?,", len(chunk)-1)+`?) ORDER BY created_at ASC`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var u Upload
			if err := rows.Scan(&u.ID, &u.ConversationID, &u.MessageID, &u.Name, &u.MIME, &u.Size, &u.Path, &u.CreatedAt); err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, u)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}
