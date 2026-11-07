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
CREATE TRIGGER IF NOT EXISTS automation_turn_update AFTER UPDATE OF status ON turns
BEGIN UPDATE automation_runs SET status=NEW.status,error=NEW.error,ended_at=NEW.ended_at WHERE turn_id=NEW.id; END;
CREATE TRIGGER IF NOT EXISTS automation_turn_delete BEFORE DELETE ON turns
BEGIN UPDATE automation_runs SET status=CASE WHEN OLD.status IN ('queued','running','waiting_approval') THEN 'cancelled' ELSE OLD.status END,
  error=CASE WHEN OLD.status IN ('queued','running','waiting_approval') THEN 'Conversation deleted before the run completed' ELSE OLD.error END,
  ended_at=CASE WHEN OLD.ended_at=0 THEN CAST(strftime('%s','now') AS INTEGER)*1000 ELSE OLD.ended_at END WHERE turn_id=OLD.id; END;
