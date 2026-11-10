-- Browser pane persistence: per-profile visit history and saved credentials.
-- Credentials are sealed by secret.key like agent/MCP env values.

CREATE TABLE IF NOT EXISTS browser_history (
  profile TEXT NOT NULL,
  url TEXT NOT NULL,
  title TEXT NOT NULL DEFAULT '',
  visits INTEGER NOT NULL DEFAULT 1,
  visited_at INTEGER NOT NULL,
  PRIMARY KEY (profile, url)
);

CREATE INDEX IF NOT EXISTS idx_browser_history_visited ON browser_history(visited_at DESC);

CREATE TABLE IF NOT EXISTS browser_credentials (
  id TEXT PRIMARY KEY,
  profile TEXT NOT NULL,
  origin TEXT NOT NULL,
  username TEXT NOT NULL,
  password_enc TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_browser_credentials_key ON browser_credentials(profile, origin, username);
