-- Annotation picks moved out of SQLite into per-pick JSON files under
-- <data>/browser-picks/ — ephemeral UI state should not ride migrations.
DROP TABLE IF EXISTS annotation_picks;
