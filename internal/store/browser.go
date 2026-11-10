package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jahrulnr/nusamation/internal/ids"
)

// BrowserHistoryEntry is one visited URL in a browser profile.
type BrowserHistoryEntry struct {
	URL       string `json:"url"`
	Title     string `json:"title"`
	Visits    int    `json:"visits"`
	VisitedAt int64  `json:"visitedAt"`
}

// RecordBrowserVisit upserts a history row for (profile,url): the visit count
// grows and the timestamp/title refresh. about:/data: pages are not kept.
func (s *Store) RecordBrowserVisit(ctx context.Context, profile, rawURL, title string) error {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		profile = "default"
	}
	if strings.HasPrefix(rawURL, "about:") || strings.HasPrefix(rawURL, "data:") || rawURL == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO browser_history(profile,url,title,visits,visited_at)
		VALUES(?,?,?,1,?)
		ON CONFLICT(profile,url) DO UPDATE SET
		  title=CASE WHEN excluded.title='' THEN browser_history.title ELSE excluded.title END,
		  visits=browser_history.visits+1,
		  visited_at=excluded.visited_at`,
		profile, rawURL, strings.TrimSpace(title), nowMS())
	return err
}

// SearchBrowserHistory ranks recent entries: prefix matches first, then
// substring matches, newest and most-visited within each bucket.
func (s *Store) SearchBrowserHistory(ctx context.Context, profile, q string, limit int) ([]BrowserHistoryEntry, error) {
	if limit <= 0 {
		limit = 8
	}
	q = strings.TrimSpace(strings.ToLower(q))
	var rows *sql.Rows
	var err error
	if q == "" {
		rows, err = s.db.QueryContext(ctx, `SELECT url,title,visits,visited_at FROM browser_history
			WHERE profile=? ORDER BY visited_at DESC LIMIT ?`, profile, limit)
	} else {
		like := "%" + q + "%"
		pre := q + "%"
		rows, err = s.db.QueryContext(ctx, `SELECT url,title,visits,visited_at FROM browser_history
			WHERE profile=? AND (lower(url) LIKE ? OR lower(title) LIKE ?)
			ORDER BY (lower(url) LIKE ? OR lower(title) LIKE ?) DESC, visits DESC, visited_at DESC LIMIT ?`,
			profile, like, like, pre, pre, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BrowserHistoryEntry
	for rows.Next() {
		var e BrowserHistoryEntry
		if err := rows.Scan(&e.URL, &e.Title, &e.Visits, &e.VisitedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// BrowserCredential is a saved login; Password stays empty unless explicitly
// requested (list views mask it).
type BrowserCredential struct {
	ID        string `json:"id"`
	Profile   string `json:"profile"`
	Origin    string `json:"origin"`
	Username  string `json:"username"`
	Password  string `json:"password,omitempty"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

// SaveBrowserCredential stores or refreshes a login; the password is sealed.
func (s *Store) SaveBrowserCredential(ctx context.Context, profile, origin, username, password string) error {
	if s.sealer == nil {
		return errors.New("credential sealing is unavailable")
	}
	enc, err := s.sealer.Seal([]byte(password))
	if err != nil {
		return err
	}
	now := nowMS()
	_, err = s.db.ExecContext(ctx, `INSERT INTO browser_credentials(id,profile,origin,username,password_enc,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(profile,origin,username) DO UPDATE SET password_enc=excluded.password_enc, updated_at=excluded.updated_at`,
		ids.New("bc"), profile, origin, username, enc, now, now)
	return err
}

// BrowserCredentials returns the logins saved for an origin, passwords
// decrypted. Only the pane's autofill path should call this.
func (s *Store) BrowserCredentials(ctx context.Context, profile, origin string) ([]BrowserCredential, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,profile,origin,username,password_enc,created_at,updated_at
		FROM browser_credentials WHERE profile=? AND origin=? ORDER BY updated_at DESC`, profile, origin)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BrowserCredential
	for rows.Next() {
		var c BrowserCredential
		var enc string
		if err := rows.Scan(&c.ID, &c.Profile, &c.Origin, &c.Username, &enc, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		if s.sealer != nil {
			if b, err := s.sealer.Open(enc); err == nil {
				c.Password = string(b)
			}
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListBrowserCredentials returns saved logins without passwords.
func (s *Store) ListBrowserCredentials(ctx context.Context, profile string) ([]BrowserCredential, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,profile,origin,username,created_at,updated_at
		FROM browser_credentials WHERE profile=? ORDER BY updated_at DESC`, profile)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BrowserCredential{}
	for rows.Next() {
		var c BrowserCredential
		if err := rows.Scan(&c.ID, &c.Profile, &c.Origin, &c.Username, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteBrowserCredential removes one saved login by id.
func (s *Store) DeleteBrowserCredential(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM browser_credentials WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("credential %s not found", id)
	}
	return nil
}
