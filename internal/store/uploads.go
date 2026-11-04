package store

import (
	"context"
)

func (s *Store) ListUploadsForConversation(ctx context.Context, conversationID string) ([]Upload, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,conversation_id,message_id,name,mime,size,path,created_at
		FROM uploads WHERE conversation_id=? ORDER BY created_at ASC`, conversationID)
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

func (s *Store) DeleteUploadsForConversation(ctx context.Context, conversationID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM uploads WHERE conversation_id=?`, conversationID)
	return err
}
