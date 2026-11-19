-- WhatsApp message index upgrades: a per-message receipt state for outgoing
-- messages (delivered/read), a full-text search index like messages_fts, and
-- retention support for the channel's purge.

ALTER TABLE whatsapp_messages ADD COLUMN receipt TEXT NOT NULL DEFAULT '';

CREATE VIRTUAL TABLE IF NOT EXISTS whatsapp_messages_fts USING fts5(
  text,
  content='whatsapp_messages',
  content_rowid='rowid',
  tokenize='unicode61'
);

INSERT INTO whatsapp_messages_fts(rowid, text) SELECT rowid, text FROM whatsapp_messages;

CREATE TRIGGER IF NOT EXISTS whatsapp_messages_fts_ai AFTER INSERT ON whatsapp_messages BEGIN
  INSERT INTO whatsapp_messages_fts(rowid, text) VALUES (new.rowid, new.text);
END;

CREATE TRIGGER IF NOT EXISTS whatsapp_messages_fts_ad AFTER DELETE ON whatsapp_messages BEGIN
  INSERT INTO whatsapp_messages_fts(whatsapp_messages_fts, rowid, text) VALUES ('delete', old.rowid, old.text);
END;

CREATE TRIGGER IF NOT EXISTS whatsapp_messages_fts_au AFTER UPDATE OF text ON whatsapp_messages BEGIN
  INSERT INTO whatsapp_messages_fts(whatsapp_messages_fts, rowid, text) VALUES ('delete', old.rowid, old.text);
  INSERT INTO whatsapp_messages_fts(rowid, text) VALUES (new.rowid, new.text);
END;

CREATE INDEX IF NOT EXISTS idx_whatsapp_messages_chat_time ON whatsapp_messages(chat_jid, timestamp_ms);
