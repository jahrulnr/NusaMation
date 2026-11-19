-- Room labels: short lowercase handles (@mention names) for conversations.
-- New conversations get a free animal name; the operator can rename a label.
-- Empty label means "not mentionable by handle" — the partial index keeps
-- many empty rows legal while every set label stays unique.
ALTER TABLE conversations ADD COLUMN label TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX IF NOT EXISTS idx_conversations_label ON conversations(label) WHERE label <> '';
