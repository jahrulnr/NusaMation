-- Browser annotation picks persist so composer chips survive a server restart:
-- the marked screenshot upload is already durable, so the descriptor must be
-- too. Picks are deleted when their browser session closes and swept by age.
CREATE TABLE IF NOT EXISTS annotation_picks (
    id              TEXT PRIMARY KEY,
    session_id      TEXT NOT NULL,
    conversation_id TEXT NOT NULL DEFAULT '',
    marker          INTEGER NOT NULL DEFAULT 0,
    descriptor      TEXT NOT NULL,           -- JSON store.AnnotationDescriptor
    upload_id       TEXT NOT NULL,
    created_at      INTEGER NOT NULL         -- unix millis
);
CREATE INDEX IF NOT EXISTS idx_annotation_picks_session ON annotation_picks(session_id);
CREATE INDEX IF NOT EXISTS idx_annotation_picks_created ON annotation_picks(created_at);
