package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"github.com/jahrulnr/nusamation/internal/security"
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

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `SELECT 1`); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, schemaV1); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `PRAGMA user_version = 1`); err != nil {
		return err
	}
	return tx.Commit()
}

const schemaV1 = `
CREATE TABLE IF NOT EXISTS settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
  id_hash TEXT PRIMARY KEY,
  csrf TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  ip TEXT NOT NULL DEFAULT '',
  user_agent TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at);

CREATE TABLE IF NOT EXISTS agents (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  slug TEXT NOT NULL UNIQUE,
  description TEXT NOT NULL DEFAULT '',
  command TEXT NOT NULL,
  args_json TEXT NOT NULL DEFAULT '[]',
  env_enc TEXT NOT NULL DEFAULT '',
  cwd TEXT NOT NULL,
  icon TEXT NOT NULL DEFAULT 'sparkles',
  color TEXT NOT NULL DEFAULT 'amber',
  enabled INTEGER NOT NULL DEFAULT 1,
  auto_start INTEGER NOT NULL DEFAULT 0,
  approval_policy TEXT NOT NULL DEFAULT 'ask',
  default_model TEXT NOT NULL DEFAULT '',
  models_json TEXT NOT NULL DEFAULT '[]',
  config_options_json TEXT NOT NULL DEFAULT '[]',
  capabilities_json TEXT NOT NULL DEFAULT '{}',
  auth_methods_json TEXT NOT NULL DEFAULT '[]',
  status TEXT NOT NULL DEFAULT 'unknown',
  last_error TEXT NOT NULL DEFAULT '',
  last_probe_at INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS mcp_servers (
  id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  transport TEXT NOT NULL DEFAULT 'stdio',
  command TEXT NOT NULL DEFAULT '',
  args_json TEXT NOT NULL DEFAULT '[]',
  env_enc TEXT NOT NULL DEFAULT '',
  url TEXT NOT NULL DEFAULT '',
  headers_enc TEXT NOT NULL DEFAULT '',
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_mcp_agent ON mcp_servers(agent_id);

CREATE TABLE IF NOT EXISTS conversations (
  id TEXT PRIMARY KEY,
  title TEXT NOT NULL DEFAULT 'New conversation',
  agent_id TEXT NOT NULL REFERENCES agents(id),
  acp_session_id TEXT NOT NULL DEFAULT '',
  cwd TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'idle',
  origin_channel TEXT NOT NULL DEFAULT 'web',
  origin_ref TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  config_options_json TEXT NOT NULL DEFAULT '[]',
  routing_json TEXT NOT NULL DEFAULT '{}',
  pinned INTEGER NOT NULL DEFAULT 0,
  archived INTEGER NOT NULL DEFAULT 0,
  plan_json TEXT NOT NULL DEFAULT '[]',
  usage_json TEXT NOT NULL DEFAULT '{}',
  metadata_json TEXT NOT NULL DEFAULT '{}',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  last_message_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_conversations_updated ON conversations(archived, pinned DESC, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_conversations_agent ON conversations(agent_id);

CREATE TABLE IF NOT EXISTS channel_links (
  id TEXT PRIMARY KEY,
  conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  channel TEXT NOT NULL,
  external_id TEXT NOT NULL,
  thread_id TEXT NOT NULL DEFAULT '',
  display_name TEXT NOT NULL DEFAULT '',
  is_group INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  UNIQUE(channel, external_id, thread_id)
);
CREATE INDEX IF NOT EXISTS idx_channel_links_conv ON channel_links(conversation_id);

CREATE TABLE IF NOT EXISTS messages (
  id TEXT PRIMARY KEY,
  conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  turn_id TEXT NOT NULL DEFAULT '',
  seq INTEGER NOT NULL,
  role TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT 'text',
  text TEXT NOT NULL DEFAULT '',
  content_json TEXT NOT NULL DEFAULT '{}',
  status TEXT NOT NULL DEFAULT 'complete',
  channel TEXT NOT NULL DEFAULT '',
  external_id TEXT NOT NULL DEFAULT '',
  acp_message_id TEXT NOT NULL DEFAULT '',
  tool_call_id TEXT NOT NULL DEFAULT '',
  metadata_json TEXT NOT NULL DEFAULT '{}',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  UNIQUE(conversation_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_messages_conv_seq ON messages(conversation_id, seq);
CREATE INDEX IF NOT EXISTS idx_messages_tool ON messages(conversation_id, tool_call_id);

CREATE TABLE IF NOT EXISTS turns (
  id TEXT PRIMARY KEY,
  conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  user_message_id TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'queued',
  stop_reason TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  origin_channel TEXT NOT NULL DEFAULT 'web',
  origin_ref TEXT NOT NULL DEFAULT '',
  queue_position INTEGER NOT NULL DEFAULT 0,
  started_at INTEGER NOT NULL DEFAULT 0,
  ended_at INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_turns_conv ON turns(conversation_id, created_at);
CREATE INDEX IF NOT EXISTS idx_turns_status ON turns(status);

CREATE TABLE IF NOT EXISTS approvals (
  id TEXT PRIMARY KEY,
  conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  turn_id TEXT NOT NULL DEFAULT '',
  tool_call_id TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL DEFAULT 'other',
  details_json TEXT NOT NULL DEFAULT '{}',
  options_json TEXT NOT NULL DEFAULT '[]',
  status TEXT NOT NULL DEFAULT 'pending',
  selected_option TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  resolved_at INTEGER NOT NULL DEFAULT 0,
  expires_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_approvals_pending ON approvals(status, created_at);
CREATE INDEX IF NOT EXISTS idx_approvals_conv ON approvals(conversation_id, created_at);

CREATE TABLE IF NOT EXISTS approval_rules (
  id TEXT PRIMARY KEY,
  scope TEXT NOT NULL DEFAULT 'conversation',
  agent_id TEXT NOT NULL DEFAULT '',
  conversation_id TEXT NOT NULL DEFAULT '',
  tool_name TEXT NOT NULL DEFAULT '',
  tool_kind TEXT NOT NULL DEFAULT '',
  action TEXT NOT NULL DEFAULT 'allow',
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_rules_lookup ON approval_rules(agent_id, conversation_id, tool_name, tool_kind);

CREATE TABLE IF NOT EXISTS inbound_messages (
  channel TEXT NOT NULL,
  external_id TEXT NOT NULL,
  thread_id TEXT NOT NULL DEFAULT '',
  conversation_id TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  PRIMARY KEY(channel, external_id, thread_id)
);

CREATE TABLE IF NOT EXISTS uploads (
  id TEXT PRIMARY KEY,
  conversation_id TEXT NOT NULL DEFAULT '',
  message_id TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL,
  mime TEXT NOT NULL,
  size INTEGER NOT NULL,
  path TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
`

func nowMS() int64 { return time.Now().UnixMilli() }

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

var ErrNotFound = errors.New("not found")

func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) || errors.Is(err, sql.ErrNoRows) }
