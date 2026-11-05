package automation

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jahrulnr/nusamation/internal/chat"
	"github.com/jahrulnr/nusamation/internal/store"
)

type Scheduler struct {
	Store *store.Store
	Chat  *chat.Service
	Log   *slog.Logger
}

func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		s.Tick(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *Scheduler) Tick(ctx context.Context, now time.Time) {
	started := time.Now()
	if err := s.Store.InterruptAutomationRuns(ctx); err != nil {
		s.Log.Warn("automation recovery", "error", err)
	}
	jobs, err := s.Store.ListDueAutomations(ctx, now)
	if err != nil {
		s.Log.Warn("automation list", "error", err)
		return
	}
	for _, job := range jobs {
		if ctx.Err() != nil {
			return
		}
		run, ok, err := s.Store.ClaimAutomation(ctx, job, now.Add(time.Since(started)))
		if err != nil {
			s.Log.Warn("automation claim", "job", job.ID, "error", err)
			continue
		}
		if !ok {
			continue
		}
		if err = s.launch(ctx, job, run); err != nil {
			_ = s.Store.FailAutomationRun(context.Background(), run.ID, err.Error())
			s.Log.Warn("automation launch", "job", job.ID, "error", err)
		}
	}
}
func (s *Scheduler) launch(ctx context.Context, job store.Automation, run store.AutomationRun) error {
	convID := job.ConversationID
	if job.SessionMode == "same" && convID != "" {
		conv, err := s.Store.GetConversation(ctx, convID)
		if store.IsNotFound(err) {
			convID = ""
		} else if err != nil {
			return err
		} else if conv.Archived {
			return fmt.Errorf("automation conversation is archived")
		} else if conv.AgentID != job.AgentID {
			return fmt.Errorf("automation conversation agent changed")
		}
	}
	if job.SessionMode == "isolated" || convID == "" {
		conv, err := s.Chat.CreateConversation(ctx, job.AgentID, "Automation · "+job.Name, "", "web", "")
		if err != nil {
			return err
		}
		convID = conv.ID
		if job.SessionMode == "same" {
			if err = s.Store.SetAutomationConversation(ctx, job.ID, job.AgentID, job.ConversationID, convID); err != nil {
				return err
			}
		}
	}
	if job.ExpiresAt > 0 && time.Now().UnixMilli() > job.ExpiresAt {
		return fmt.Errorf("automation expired before submission")
	}
	if err := s.Store.SetAutomationRunConversation(ctx, run.ID, convID); err != nil {
		return err
	}
	_, _, err := s.Chat.Submit(ctx, chat.SubmitInput{ConversationID: convID, Text: job.Prompt, Channel: "web", AutomationRunID: run.ID})
	if err != nil {
		return err
	}
	return nil
}
