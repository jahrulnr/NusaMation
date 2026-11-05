package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jahrulnr/nusamation/internal/ids"
	"github.com/robfig/cron/v3"
)

func LocalTimezone() string {
	if zone := strings.TrimPrefix(os.Getenv("TZ"), ":"); zone != "" {
		if _, err := time.LoadLocation(zone); err == nil {
			return zone
		}
	}
	if name := time.Local.String(); name != "Local" {
		return name
	}
	if path, err := filepath.EvalSymlinks("/etc/localtime"); err == nil {
		if _, zone, ok := strings.Cut(path, "/zoneinfo/"); ok {
			if _, err := time.LoadLocation(zone); err == nil {
				return zone
			}
		}
	}
	return "Local"
}

type Automation struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Prompt         string `json:"prompt"`
	AgentID        string `json:"agentId"`
	Schedule       string `json:"schedule"`
	Cron           string `json:"cron"`
	RunAt          int64  `json:"runAt"`
	Timezone       string `json:"timezone"`
	SessionMode    string `json:"sessionMode"`
	ConversationID string `json:"conversationId"`
	ExpiresAt      int64  `json:"expiresAt"`
	Enabled        bool   `json:"enabled"`
	NextRunAt      int64  `json:"nextRunAt"`
	CreatedAt      int64  `json:"createdAt"`
	UpdatedAt      int64  `json:"updatedAt"`
}
type AutomationRun struct {
	ID             string `json:"id"`
	AutomationID   string `json:"automationId"`
	ScheduledAt    int64  `json:"scheduledAt"`
	StartedAt      int64  `json:"startedAt"`
	EndedAt        int64  `json:"endedAt"`
	ConversationID string `json:"conversationId"`
	TurnID         string `json:"turnId"`
	Status         string `json:"status"`
	Error          string `json:"error"`
}

func ValidateAutomation(a *Automation, now time.Time) error {
	a.Name = strings.TrimSpace(a.Name)
	a.Prompt = strings.TrimSpace(a.Prompt)
	a.Cron = strings.TrimSpace(a.Cron)
	if a.Name == "" || a.Prompt == "" || a.AgentID == "" {
		return errors.New("name, prompt and agent are required")
	}
	if len(a.Prompt) > 200000 {
		return errors.New("prompt exceeds 200000 bytes")
	}
	if a.SessionMode != "isolated" && a.SessionMode != "same" {
		return errors.New("session mode must be isolated or same")
	}
	if a.Timezone == "" {
		a.Timezone = LocalTimezone()
	}
	loc, err := time.LoadLocation(a.Timezone)
	if err != nil {
		return fmt.Errorf("invalid timezone: %w", err)
	}
	switch a.Schedule {
	case "once":
		if a.RunAt <= 0 {
			return errors.New("run time is required")
		}
		a.Cron = ""
		a.NextRunAt = a.RunAt
	case "cron":
		if len(strings.Fields(a.Cron)) != 5 {
			return errors.New("cron must have five fields")
		}
		schedule, err := cron.ParseStandard("CRON_TZ=" + a.Timezone + " " + a.Cron)
		if err != nil {
			return fmt.Errorf("invalid cron: %w", err)
		}
		next := schedule.Next(now.In(loc))
		if next.IsZero() {
			return errors.New("cron has no upcoming occurrence")
		}
		a.NextRunAt = next.UnixMilli()
		a.RunAt = 0
	default:
		return errors.New("schedule must be once or cron")
	}
	return nil
}

const automationSelect = `SELECT id,name,prompt,agent_id,schedule,cron,run_at,timezone,session_mode,conversation_id,expires_at,enabled,next_run_at,created_at,updated_at FROM automations`

func scanAutomation(r rowScanner) (Automation, error) {
	var a Automation
	var enabled int
	err := r.Scan(&a.ID, &a.Name, &a.Prompt, &a.AgentID, &a.Schedule, &a.Cron, &a.RunAt, &a.Timezone, &a.SessionMode, &a.ConversationID, &a.ExpiresAt, &enabled, &a.NextRunAt, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	a.Enabled = enabled != 0
	return a, err
}
func (s *Store) GetAutomation(ctx context.Context, id string) (Automation, error) {
	return scanAutomation(s.db.QueryRowContext(ctx, automationSelect+` WHERE id=?`, id))
}
func (s *Store) ListAutomations(ctx context.Context) ([]Automation, error) {
	rows, err := s.db.QueryContext(ctx, automationSelect+` ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Automation{}
	for rows.Next() {
		a, err := scanAutomation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListDueAutomations returns only the enabled jobs due at now — the same
// predicate ClaimAutomation applies — so the scheduler tick does not load
// every row (prompts can be large) each cycle.
func (s *Store) ListDueAutomations(ctx context.Context, now time.Time) ([]Automation, error) {
	rows, err := s.db.QueryContext(ctx, automationSelect+
		` WHERE enabled=1 AND next_run_at>0 AND next_run_at<=? AND (expires_at=0 OR expires_at>=?) ORDER BY next_run_at ASC`,
		now.UnixMilli(), now.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Automation{}
	for rows.Next() {
		a, err := scanAutomation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func (s *Store) SaveAutomation(ctx context.Context, a Automation, now time.Time) (Automation, error) {
	if err := ValidateAutomation(&a, now); err != nil {
		return a, err
	}
	ag, err := s.GetAgent(ctx, a.AgentID)
	if err != nil {
		return a, err
	}
	if !ag.Enabled {
		return a, errors.New("agent is disabled")
	}
	if a.ID == "" {
		a.ID = ids.New("auto")
		a.CreatedAt = now.UnixMilli()
		a.UpdatedAt = a.CreatedAt
		_, err = s.db.ExecContext(ctx, `INSERT INTO automations(id,name,prompt,agent_id,schedule,cron,run_at,timezone,session_mode,conversation_id,expires_at,enabled,next_run_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, a.ID, a.Name, a.Prompt, a.AgentID, a.Schedule, a.Cron, a.RunAt, a.Timezone, a.SessionMode, "", a.ExpiresAt, boolInt(a.Enabled), a.NextRunAt, a.CreatedAt, a.UpdatedAt)
	} else {
		old, e := s.GetAutomation(ctx, a.ID)
		if e != nil {
			return a, e
		}
		if a.UpdatedAt != 0 && a.UpdatedAt != old.UpdatedAt {
			return a, errors.New("automation changed; reload it before saving")
		}
		if old.AgentID == a.AgentID && old.SessionMode == a.SessionMode {
			a.ConversationID = old.ConversationID
		}
		if old.Schedule == a.Schedule && old.Cron == a.Cron && old.RunAt == a.RunAt && old.Timezone == a.Timezone {
			a.NextRunAt = old.NextRunAt
		}
		var result sql.Result
		result, err = s.db.ExecContext(ctx, `UPDATE automations SET name=?,prompt=?,agent_id=?,schedule=?,cron=?,run_at=?,timezone=?,session_mode=?,conversation_id=?,expires_at=?,enabled=?,next_run_at=?,updated_at=? WHERE id=? AND updated_at=? AND next_run_at=?`, a.Name, a.Prompt, a.AgentID, a.Schedule, a.Cron, a.RunAt, a.Timezone, a.SessionMode, a.ConversationID, a.ExpiresAt, boolInt(a.Enabled), a.NextRunAt, now.UnixMilli(), a.ID, old.UpdatedAt, old.NextRunAt)
		if err == nil {
			if n, _ := result.RowsAffected(); n == 0 {
				return a, errors.New("automation changed; reload it before saving")
			}
		}
	}
	if err != nil {
		return a, err
	}
	return s.GetAutomation(ctx, a.ID)
}
func (s *Store) DeleteAutomation(ctx context.Context, id string) error {
	r, err := s.db.ExecContext(ctx, `DELETE FROM automations WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
func (s *Store) SetAutomationEnabled(ctx context.Context, id string, enabled bool) error {
	r, err := s.db.ExecContext(ctx, `UPDATE automations SET enabled=?,updated_at=? WHERE id=?`, boolInt(enabled), nowMS(), id)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
func (s *Store) SetAutomationConversation(ctx context.Context, id, agentID, previous, conv string) error {
	r, err := s.db.ExecContext(ctx, `UPDATE automations SET conversation_id=?,updated_at=? WHERE id=? AND agent_id=? AND session_mode='same' AND conversation_id=?`, conv, nowMS(), id, agentID, previous)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return errors.New("automation changed while starting its conversation")
	}
	return nil
}

// ClaimAutomation atomically advances a due job and records the claimed occurrence.
func (s *Store) ClaimAutomation(ctx context.Context, a Automation, now time.Time) (AutomationRun, bool, error) {
	var run AutomationRun
	if !a.Enabled || a.NextRunAt == 0 || a.NextRunAt > now.UnixMilli() || (a.ExpiresAt > 0 && now.UnixMilli() > a.ExpiresAt) {
		return run, false, nil
	}
	next := int64(0)
	if a.Schedule == "cron" {
		loc, err := time.LoadLocation(a.Timezone)
		if err != nil {
			return run, false, err
		}
		spec, err := cron.ParseStandard("CRON_TZ=" + a.Timezone + " " + a.Cron)
		if err != nil {
			return run, false, err
		}
		next = spec.Next(now.In(loc)).UnixMilli()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return run, false, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE automations SET next_run_at=?,updated_at=? WHERE id=? AND enabled=1 AND next_run_at=? AND updated_at=? AND (expires_at=0 OR expires_at>=?)`, next, now.UnixMilli(), a.ID, a.NextRunAt, a.UpdatedAt, now.UnixMilli())
	if err != nil {
		return run, false, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return run, false, nil
	}
	run = AutomationRun{ID: ids.New("run"), AutomationID: a.ID, ScheduledAt: a.NextRunAt, StartedAt: now.UnixMilli(), Status: "launching"}
	_, err = tx.ExecContext(ctx, `INSERT INTO automation_runs(id,automation_id,scheduled_at,started_at,status) VALUES(?,?,?,?,?)`, run.ID, run.AutomationID, run.ScheduledAt, run.StartedAt, run.Status)
	if err != nil {
		return run, false, err
	}
	if err = tx.Commit(); err != nil {
		return run, false, err
	}
	return run, true, nil
}

// CreateAutomationTurn commits the queue entry and its run link together, so
// a queue worker cannot start the turn before it can check the job's expiry.
func (s *Store) CreateAutomationTurn(ctx context.Context, runID, convID, messageID, channel, ref string) (Turn, error) {
	t := Turn{ID: ids.New("turn"), ConversationID: convID, UserMessageID: messageID, Status: "queued", OriginChannel: channel, OriginRef: ref, CreatedAt: nowMS()}
	if t.OriginChannel == "" {
		t.OriginChannel = "web"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return t, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO turns(id,conversation_id,user_message_id,status,origin_channel,origin_ref,queue_position,created_at) VALUES(?,?,?,?,?,?,(SELECT COUNT(*)+1 FROM turns WHERE conversation_id=? AND status IN ('queued','running','waiting_approval')),?)`, t.ID, convID, messageID, t.Status, t.OriginChannel, ref, convID, t.CreatedAt)
	if err != nil {
		return t, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE automation_runs SET conversation_id=?,turn_id=?,status='queued' WHERE id=? AND status='launching'`, convID, t.ID, runID)
	if err != nil {
		return t, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return t, ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return t, err
	}
	return s.GetTurn(ctx, t.ID)
}
func (s *Store) SetAutomationRunConversation(ctx context.Context, id, conv string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE automation_runs SET conversation_id=? WHERE id=?`, conv, id)
	return err
}
func (s *Store) FailAutomationRun(ctx context.Context, id, msg string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE automation_runs SET status='error',error=?,ended_at=? WHERE id=?`, msg, nowMS(), id)
	return err
}
func (s *Store) AutomationTurnExpired(ctx context.Context, turnID string, now time.Time) (bool, error) {
	var expired bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM automation_runs r JOIN automations a ON a.id=r.automation_id WHERE r.turn_id=? AND a.expires_at>0 AND a.expires_at<?)`, turnID, now.UnixMilli()).Scan(&expired)
	return expired, err
}
func (s *Store) InterruptAutomationRuns(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE automation_runs SET status='interrupted',error='Server stopped before the run was linked; check the chat for a submitted prompt',ended_at=? WHERE status='launching' AND started_at<?`, nowMS(), time.Now().Add(-5*time.Minute).UnixMilli())
	return err
}
func (s *Store) ListAutomationRuns(ctx context.Context, id string) ([]AutomationRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.id,r.automation_id,r.scheduled_at,r.started_at,COALESCE(t.ended_at,r.ended_at),r.conversation_id,r.turn_id,COALESCE(t.status,r.status),CASE WHEN t.error IS NOT NULL AND t.error<>'' THEN t.error ELSE r.error END FROM automation_runs r LEFT JOIN turns t ON t.id=r.turn_id WHERE r.automation_id=? ORDER BY r.started_at DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AutomationRun{}
	for rows.Next() {
		var r AutomationRun
		if err := rows.Scan(&r.ID, &r.AutomationID, &r.ScheduledAt, &r.StartedAt, &r.EndedAt, &r.ConversationID, &r.TurnID, &r.Status, &r.Error); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
