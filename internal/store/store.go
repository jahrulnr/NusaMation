package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/jahrulnr/nusamation/internal/security"
	"github.com/jahrulnr/nusamation/migrations"
)

type Store struct {
	db     *sql.DB
	sealer *security.Sealer
}

func (s *Store) SetSealer(sealer *security.Sealer) { s.sealer = sealer }

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	dsn := "file:" + path + "?_pragma=busy_timeout(8000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) DB() *sql.DB { return s.db }

// Schema history lives in versioned .sql files under migrations/. user_version
// records the last applied file, so an upgrade only runs files it has not seen.
// Files are append-only: a released file is never edited, new schema changes go
// in a new NNNN_description.sql file.
func (s *Store) migrate(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	list, err := migrations.List()
	if err != nil {
		return err
	}
	for _, m := range list {
		if m.Version <= version {
			continue
		}
		// ADD COLUMN is not idempotent. Upgrade tests rewind user_version and
		// apply the same file again, so a column that already exists is skipped.
		// Check before the write transaction: this store has one connection.
		script, err := s.omitExistingAddColumns(ctx, m.SQL)
		if err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if strings.TrimSpace(script) != "" {
			if _, err := tx.ExecContext(ctx, script); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("apply %s: %w", m.Name, err)
			}
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, m.Version)); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func nowMS() int64 { return time.Now().UnixMilli() }

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

var ErrNotFound = errors.New("not found")

// ErrConflict reports a stale project revision or an id that already exists.
var ErrConflict = errors.New("conflict")

// ErrNotQueued reports a turn that exists but is not waiting in the queue —
// e.g. it already started or finished, so it cannot be cancelled as queued.
var ErrNotQueued = errors.New("turn is not queued")

func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) || errors.Is(err, sql.ErrNoRows) }

var addColumnStmt = regexp.MustCompile(`(?is)ALTER\s+TABLE\s+([A-Za-z_][A-Za-z0-9_]*)\s+ADD\s+COLUMN\s+([A-Za-z_][A-Za-z0-9_]*)\b[^;]*;`)

// omitExistingAddColumns removes ALTER TABLE ... ADD COLUMN statements whose
// column is already present. The rest of the script, including comments, is
// left for SQLite to parse.
func (s *Store) omitExistingAddColumns(ctx context.Context, script string) (string, error) {
	if !strings.Contains(strings.ToUpper(script), "ADD COLUMN") {
		return script, nil
	}
	var dropErr error
	out := addColumnStmt.ReplaceAllStringFunc(script, func(stmt string) string {
		if dropErr != nil {
			return stmt
		}
		match := addColumnStmt.FindStringSubmatch(stmt)
		if len(match) < 3 {
			return stmt
		}
		exists, err := s.columnExists(ctx, match[1], match[2])
		if err != nil {
			dropErr = err
			return stmt
		}
		if exists {
			return ""
		}
		return stmt
	})
	if dropErr != nil {
		return "", dropErr
	}
	return out, nil
}

func (s *Store) columnExists(ctx context.Context, table, column string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, table, column).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
