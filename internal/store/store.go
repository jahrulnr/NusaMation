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

// migrations is the versioned schema history. user_version records the last
// applied entry, so an upgrade only runs the statements it has not seen.
type migration struct {
	version int
	stmts   []string
}

var migrations = []migration{
	{version: 1, stmts: []string{schemaV1}},
	{version: 2, stmts: []string{
		`CREATE INDEX IF NOT EXISTS idx_uploads_message_id ON uploads(message_id)`,
		`CREATE INDEX IF NOT EXISTS idx_uploads_conversation_id ON uploads(conversation_id)`,
	}},
	{version: 3, stmts: []string{
		`CREATE TABLE mcp_servers_global (
		 id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, transport TEXT NOT NULL DEFAULT 'stdio',
		 command TEXT NOT NULL DEFAULT '', args_json TEXT NOT NULL DEFAULT '[]', env_enc TEXT NOT NULL DEFAULT '',
		 url TEXT NOT NULL DEFAULT '', headers_enc TEXT NOT NULL DEFAULT '', enabled INTEGER NOT NULL DEFAULT 1,
		 created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
		`INSERT INTO mcp_servers_global (id,name,transport,command,args_json,env_enc,url,headers_enc,enabled,created_at,updated_at)
		 SELECT id, CASE WHEN row_number() OVER (PARTITION BY name ORDER BY created_at, id) = 1 THEN name
		 ELSE name || ' [' || id || ']' END, transport,command,args_json,env_enc,url,headers_enc,enabled,created_at,updated_at
		 FROM mcp_servers`,
		`DROP TABLE mcp_servers`,
		`ALTER TABLE mcp_servers_global RENAME TO mcp_servers`,
	}},
}

func (s *Store) migrate(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	for _, m := range migrations {
		if m.version <= version {
			continue
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		for _, stmt := range m.stmts {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("apply schema v%d: %w", m.version, err)
			}
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, m.version)); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

const schemaV1 = `
CREATE TABLE IF NOT EXISTS settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS automations (
 id TEXT PRIMARY KEY, name TEXT NOT NULL, prompt TEXT NOT NULL, agent_id TEXT NOT NULL,
 schedule TEXT NOT NULL, cron TEXT NOT NULL DEFAULT '', run_at INTEGER NOT NULL DEFAULT 0,
 timezone TEXT NOT NULL, session_mode TEXT NOT NULL, conversation_id TEXT NOT NULL DEFAULT '',
 expires_at INTEGER NOT NULL DEFAULT 0, enabled INTEGER NOT NULL DEFAULT 1, next_run_at INTEGER NOT NULL DEFAULT 0,
 created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_automations_due ON automations(enabled,next_run_at);
CREATE TABLE IF NOT EXISTS automation_runs (
 id TEXT PRIMARY KEY, automation_id TEXT NOT NULL REFERENCES automations(id) ON DELETE CASCADE,
 scheduled_at INTEGER NOT NULL, started_at INTEGER NOT NULL, ended_at INTEGER NOT NULL DEFAULT 0,
 conversation_id TEXT NOT NULL DEFAULT '', turn_id TEXT NOT NULL DEFAULT '', status TEXT NOT NULL, error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_automation_runs ON automation_runs(automation_id,started_at);
CREATE INDEX IF NOT EXISTS idx_automation_runs_turn ON automation_runs(turn_id);

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

CREATE TABLE IF NOT EXISTS conversation_acp_sessions (
  conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
  agent_id TEXT NOT NULL,
  agent_slug TEXT NOT NULL,
  session_id TEXT NOT NULL,
  PRIMARY KEY (conversation_id, agent_id, session_id)
);
INSERT OR IGNORE INTO conversation_acp_sessions (conversation_id, agent_id, agent_slug, session_id)
SELECT c.id, c.agent_id, a.slug, c.acp_session_id FROM conversations c
JOIN agents a ON a.id = c.agent_id WHERE c.acp_session_id <> '';

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
CREATE TRIGGER IF NOT EXISTS automation_turn_update AFTER UPDATE OF status ON turns
BEGIN UPDATE automation_runs SET status=NEW.status,error=NEW.error,ended_at=NEW.ended_at WHERE turn_id=NEW.id; END;
CREATE TRIGGER IF NOT EXISTS automation_turn_delete BEFORE DELETE ON turns
BEGIN UPDATE automation_runs SET status=CASE WHEN OLD.status IN ('queued','running','waiting_approval') THEN 'cancelled' ELSE OLD.status END,
  error=CASE WHEN OLD.status IN ('queued','running','waiting_approval') THEN 'Conversation deleted before the run completed' ELSE OLD.error END,
  ended_at=CASE WHEN OLD.ended_at=0 THEN CAST(strftime('%s','now') AS INTEGER)*1000 ELSE OLD.ended_at END WHERE turn_id=OLD.id; END;


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

CREATE TABLE IF NOT EXISTS whatsapp_messages (
  chat_jid TEXT NOT NULL,
  message_id TEXT NOT NULL,
  sender_jid TEXT NOT NULL DEFAULT '',
  sender_name TEXT NOT NULL DEFAULT '',
  chat_name TEXT NOT NULL DEFAULT '',
  timestamp_ms INTEGER NOT NULL,
  from_me INTEGER NOT NULL DEFAULT 0,
  is_group INTEGER NOT NULL DEFAULT 0,
  text TEXT NOT NULL DEFAULT '',
  raw_json TEXT NOT NULL DEFAULT '{}',
  is_live INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (chat_jid, message_id)
);
CREATE INDEX IF NOT EXISTS idx_whatsapp_messages_time ON whatsapp_messages(timestamp_ms DESC);

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
