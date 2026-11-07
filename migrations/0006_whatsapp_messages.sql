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
