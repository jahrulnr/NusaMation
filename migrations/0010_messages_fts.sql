-- Message search index. External content keeps the text in messages; the
-- triggers keep messages_fts in sync on insert, delete, and text updates.
-- Tool and thought rows are indexed too; callers filter by kind.

CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
  text,
  content='messages',
  content_rowid='rowid',
  tokenize='unicode61'
);

INSERT INTO messages_fts(rowid, text) SELECT rowid, text FROM messages;

CREATE TRIGGER IF NOT EXISTS messages_fts_ai AFTER INSERT ON messages BEGIN
  INSERT INTO messages_fts(rowid, text) VALUES (new.rowid, new.text);
END;

CREATE TRIGGER IF NOT EXISTS messages_fts_ad AFTER DELETE ON messages BEGIN
  INSERT INTO messages_fts(messages_fts, rowid, text) VALUES ('delete', old.rowid, old.text);
END;

CREATE TRIGGER IF NOT EXISTS messages_fts_au AFTER UPDATE OF text ON messages BEGIN
  INSERT INTO messages_fts(messages_fts, rowid, text) VALUES ('delete', old.rowid, old.text);
  INSERT INTO messages_fts(rowid, text) VALUES (new.rowid, new.text);
END;
