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
