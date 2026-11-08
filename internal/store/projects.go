package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jahrulnr/nusamation/internal/ids"
)

const projectSelect = `SELECT id,name,root_paths,pinned,created_at,updated_at FROM projects`

const memberDirsSQL = `json_set(
	CASE WHEN json_valid(metadata_json) AND json_type(metadata_json)='object' THEN metadata_json ELSE '{}' END,
	'$.additionalDirectories', json(?))`

func scanProject(row rowScanner) (Project, error) {
	var p Project
	var roots string
	var pinned int
	err := row.Scan(&p.ID, &p.Name, &roots, &pinned, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Project{}, ErrNotFound
		}
		return Project{}, err
	}
	p.Pinned = pinned != 0
	if err := json.Unmarshal([]byte(roots), &p.RootPaths); err != nil {
		return Project{}, err
	}
	if p.RootPaths == nil {
		p.RootPaths = []string{}
	}
	return p, nil
}

func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx, projectSelect+` ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Project{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) GetProject(ctx context.Context, id string) (Project, error) {
	return scanProject(s.db.QueryRowContext(ctx, projectSelect+` WHERE id=?`, id))
}

func (s *Store) InsertProject(ctx context.Context, name string, roots []string) (Project, error) {
	raw, err := json.Marshal(roots)
	if err != nil {
		return Project{}, err
	}
	id := ids.New("prj")
	now := nowMS()
	_, err = s.db.ExecContext(ctx, `INSERT INTO projects
		(id,name,root_paths,pinned,created_at,updated_at)
		VALUES (?,?,?,0,?,?)`, id, name, string(raw), now, now)
	if err != nil {
		return Project{}, err
	}
	return s.GetProject(ctx, id)
}

// SaveProjectSources writes the name and root paths when updated_at still
// matches expectUpdatedAt. Pin is included only when the caller sets it.
// Member cwd and additionalDirectories change only when the root list itself
// changes. The returned ids are those members.
func (s *Store) SaveProjectSources(ctx context.Context, id, name string, roots []string, expectUpdatedAt int64, pinned *bool) (Project, bool, []string, error) {
	raw, err := json.Marshal(roots)
	if err != nil {
		return Project{}, false, nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Project{}, false, nil, err
	}
	defer tx.Rollback()
	cur, err := scanProject(tx.QueryRowContext(ctx, projectSelect+` WHERE id=?`, id))
	if err != nil {
		return Project{}, false, nil, err
	}
	if cur.UpdatedAt != expectUpdatedAt {
		return Project{}, false, nil, ErrConflict
	}
	now := nowMS()
	if now <= expectUpdatedAt {
		now = expectUpdatedAt + 1
	}
	sets := []string{"name=?", "root_paths=?", "updated_at=?"}
	args := []any{name, string(raw), now}
	if pinned != nil {
		sets = append(sets, "pinned=?")
		args = append(args, boolInt(*pinned))
	}
	args = append(args, id, expectUpdatedAt)
	res, err := tx.ExecContext(ctx, `UPDATE projects SET `+strings.Join(sets, ",")+` WHERE id=? AND updated_at=?`, args...)
	if err != nil {
		return Project{}, false, nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return Project{}, false, nil, ErrConflict
	}
	changed := !sameStrings(cur.RootPaths, roots)
	var memberIDs []string
	if changed {
		memberIDs, err = patchProjectMembers(ctx, tx, id, roots, now)
		if err != nil {
			return Project{}, false, nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Project{}, false, nil, err
	}
	p, err := s.GetProject(ctx, id)
	return p, changed, memberIDs, err
}

// PlaceProject updates pin only. It does not touch roots, member cwd, or
// updated_at.
func (s *Store) PlaceProject(ctx context.Context, id string, pinned *bool) (Project, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Project{}, err
	}
	defer tx.Rollback()
	if _, err := scanProject(tx.QueryRowContext(ctx, projectSelect+` WHERE id=?`, id)); err != nil {
		return Project{}, err
	}
	sets := []string{}
	args := []any{}
	if pinned != nil {
		sets = append(sets, "pinned=?")
		args = append(args, boolInt(*pinned))
	}
	if len(sets) > 0 {
		args = append(args, id)
		if _, err := tx.ExecContext(ctx, `UPDATE projects SET `+strings.Join(sets, ",")+` WHERE id=?`, args...); err != nil {
			return Project{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Project{}, err
	}
	return s.GetProject(ctx, id)
}

// DeleteProject removes the project row and clears member project_id. cwd and
// metadata stay. The snapshot is the project as it was stored.
func (s *Store) DeleteProject(ctx context.Context, id string) (Project, []string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Project{}, nil, err
	}
	defer tx.Rollback()
	p, err := scanProject(tx.QueryRowContext(ctx, projectSelect+` WHERE id=?`, id))
	if err != nil {
		return Project{}, nil, err
	}
	memberIDs, err := projectMemberIDs(ctx, tx, id)
	if err != nil {
		return Project{}, nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE conversations SET project_id=NULL, updated_at=? WHERE project_id=?`, nowMS(), id); err != nil {
		return Project{}, nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM projects WHERE id=?`, id); err != nil {
		return Project{}, nil, err
	}
	if err := tx.Commit(); err != nil {
		return Project{}, nil, err
	}
	return p, memberIDs, nil
}

// RestoreProject recreates id. A conversation is reattached only when its
// project_id is still empty. resetIDs are reattached chats whose cwd or
// additional directories actually changed.
func (s *Store) RestoreProject(ctx context.Context, id, name string, roots []string, pinned bool, conversationIDs []string) (Project, []string, []string, error) {
	if strings.TrimSpace(id) == "" || len(roots) == 0 {
		return Project{}, nil, nil, ErrConflict
	}
	raw, err := json.Marshal(roots)
	if err != nil {
		return Project{}, nil, nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Project{}, nil, nil, err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE id=?`, id).Scan(&exists); err != nil {
		return Project{}, nil, nil, err
	}
	if exists > 0 {
		return Project{}, nil, nil, ErrConflict
	}
	now := nowMS()
	if _, err := tx.ExecContext(ctx, `INSERT INTO projects
		(id,name,root_paths,pinned,created_at,updated_at)
		VALUES (?,?,?,?,?,?)`, id, name, string(raw), boolInt(pinned), now, now); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return Project{}, nil, nil, ErrConflict
		}
		return Project{}, nil, nil, err
	}
	extra, err := json.Marshal(rootExtras(roots))
	if err != nil {
		return Project{}, nil, nil, err
	}
	var reattached, reset []string
	for _, convID := range conversationIDs {
		convID = strings.TrimSpace(convID)
		if convID == "" {
			continue
		}
		var projectID sql.NullString
		var cwd, meta string
		err := tx.QueryRowContext(ctx, `SELECT project_id, cwd, metadata_json FROM conversations WHERE id=?`, convID).Scan(&projectID, &cwd, &meta)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return Project{}, nil, nil, err
		}
		if projectID.Valid && projectID.String != "" {
			continue
		}
		res, err := tx.ExecContext(ctx, `UPDATE conversations
			SET project_id=?, cwd=?, metadata_json=`+memberDirsSQL+`, updated_at=?
			WHERE id=? AND (project_id IS NULL OR project_id='')`,
			id, roots[0], string(extra), now, convID)
		if err != nil {
			return Project{}, nil, nil, err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			continue
		}
		reattached = append(reattached, convID)
		if !memberRootsMatch(cwd, meta, roots) {
			reset = append(reset, convID)
		}
	}
	if err := tx.Commit(); err != nil {
		return Project{}, nil, nil, err
	}
	p, err := s.GetProject(ctx, id)
	return p, reattached, reset, err
}

func rootExtras(roots []string) []string {
	if len(roots) < 2 {
		return []string{}
	}
	return roots[1:]
}

func patchProjectMembers(ctx context.Context, tx *sql.Tx, projectID string, roots []string, now int64) ([]string, error) {
	memberIDs, err := projectMemberIDs(ctx, tx, projectID)
	if err != nil {
		return nil, err
	}
	extra, err := json.Marshal(rootExtras(roots))
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE conversations
		SET cwd=?, metadata_json=`+memberDirsSQL+`, updated_at=?
		WHERE project_id=?`, roots[0], string(extra), now, projectID); err != nil {
		return nil, err
	}
	return memberIDs, nil
}

func projectMemberIDs(ctx context.Context, tx *sql.Tx, projectID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM conversations WHERE project_id=? ORDER BY created_at ASC, id ASC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	memberIDs := []string{}
	for rows.Next() {
		var memberID string
		if err := rows.Scan(&memberID); err != nil {
			return nil, err
		}
		memberIDs = append(memberIDs, memberID)
	}
	return memberIDs, rows.Err()
}

func memberRootsMatch(cwd, meta string, roots []string) bool {
	if len(roots) == 0 || cwd != roots[0] {
		return false
	}
	return sameStrings(dirsFromMetadata(meta), rootExtras(roots))
}

func dirsFromMetadata(meta string) []string {
	var decoded map[string]any
	if json.Unmarshal([]byte(meta), &decoded) != nil {
		return nil
	}
	raw, ok := decoded["additionalDirectories"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		s, ok := item.(string)
		if !ok {
			continue
		}
		out = append(out, s)
	}
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
