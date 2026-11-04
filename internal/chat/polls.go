package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jahrulnr/nusamation/internal/channels"
	"github.com/jahrulnr/nusamation/internal/store"
	"github.com/jahrulnr/nusamation/internal/textfmt"
)

const (
	// WhatsApp caps polls at 12 options; beyond that fall back to text.
	pollMaxOptions  = 12
	pollOptionMax   = 90
	pollQuestionMax = 200
	// Multi-select polls emit one vote update per tap carrying the cumulative
	// selection — resolve after this much quiet time with the last update.
	pollMultiQuiet = 15 * time.Second
	// Pending polls older than this are treated as expired on lookup.
	pollWaiterTTL = 24 * time.Hour
)

// pollWaiter links a channel-sent poll message to the pending agent question
// it answers (an approval or a single-field form elicitation).
type pollWaiter struct {
	channel        string
	pollMessageID  string
	conversationID string
	kind           string // "approval" | "elicitation"
	approvalID     string
	elicitationID  string
	turnID         string            // turn the question belongs to — gates who may vote
	field          string            // elicitation: schema field the poll answers
	options        map[string]string // option name -> approval optionID (elicitation maps name->name)
	multi          bool              // the question allows several picks; resolve via debounce
	arrayField     bool              // elicitation: field expects a list answer
	boolField      bool              // elicitation: field expects a boolean (Yes/No options)
	createdAt      time.Time
	latest         []string
	timer          *time.Timer
	generation     uint64
}

func pollWaiterKey(channel, pollMessageID string) string {
	return channel + "|" + pollMessageID
}

func (s *Service) registerPollWaiter(w *pollWaiter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pollWaiters[pollWaiterKey(w.channel, w.pollMessageID)] = w
}

// dropPollWaiters removes every pending poll matching pred (used when the
// underlying question is resolved through another surface, e.g. the web UI).
func (s *Service) dropPollWaiters(pred func(*pollWaiter) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, w := range s.pollWaiters {
		if pred(w) {
			if w.timer != nil {
				w.timer.Stop()
			}
			delete(s.pollWaiters, k)
		}
	}
}

// notifyApprovalPoll tries to present an approval as a native poll on the
// origin channel. Returns false when the channel can't or the option list
// doesn't fit, so the caller can fall back to a text notification.
func (s *Service) notifyApprovalPoll(ctx context.Context, ch channels.Channel, target channels.Target, ap store.Approval) bool {
	asker, ok := ch.(channels.PollAsker)
	if !ok || len(ap.Options) == 0 || len(ap.Options) > pollMaxOptions {
		return false
	}
	optionIDs := map[string]string{}
	names := make([]string, 0, len(ap.Options))
	for _, o := range ap.Options {
		name := dedupePollOption(textfmt.Truncate(strings.TrimSpace(o.Name), pollOptionMax), optionIDs)
		if name == "" {
			name = dedupePollOption(textfmt.Truncate(o.OptionID, pollOptionMax), optionIDs)
		}
		if name == "" {
			continue
		}
		optionIDs[name] = o.OptionID
		names = append(names, name)
	}
	// The ID stays visible so /approve keeps working even if the poll dies.
	question := textfmt.Truncate("Approval needed: "+ap.Title+"\nID: "+ap.ID, pollQuestionMax)
	pollID, err := asker.SendPoll(ctx, target, question, names, 1)
	if err != nil {
		s.log.Warn("failed to send approval poll", "channel", ch.Name(), "error", err)
		return false
	}
	s.registerPollWaiter(&pollWaiter{
		channel: ch.Name(), pollMessageID: pollID, conversationID: ap.ConversationID,
		kind: "approval", approvalID: ap.ID, turnID: ap.TurnID, options: optionIDs, createdAt: time.Now(),
	})
	return true
}

// notifyElicitation surfaces a pending elicitation to the origin channel.
// Single-field choice questions become polls; anything else (multi-field,
// free text, URL mode) gets a pointer to the web UI — previously channel
// users heard nothing at all.
func (s *Service) notifyElicitation(a *activeTurn, e Elicitation) {
	a.mu.Lock()
	routing := a.conv.Routing
	a.mu.Unlock()
	if !routing.NotifyApprovals {
		return
	}
	ch, target, ok := s.originChannel(a)
	if !ok {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		spec := elicitationPollSpec(e.Request)
		if spec.ok && len(spec.choices) >= 2 && len(spec.choices) <= pollMaxOptions {
			if asker, ok := ch.(channels.PollAsker); ok {
				maxSel := 1
				if spec.array {
					maxSel = spec.maxSel
				}
				// Poll labels are truncated/deduped for the wire; the waiter
				// maps them back to the original const so validation passes.
				labels := make([]string, 0, len(spec.choices))
				options := map[string]string{}
				for _, c := range spec.choices {
					label := dedupePollOption(textfmt.Truncate(strings.TrimSpace(c), pollOptionMax), options)
					options[label] = c
					labels = append(labels, label)
				}
				pollID, err := asker.SendPoll(ctx, target, textfmt.Truncate(spec.question, pollQuestionMax), labels, maxSel)
				if err == nil {
					a.mu.Lock()
					turnID := a.turn.ID
					a.mu.Unlock()
					s.registerPollWaiter(&pollWaiter{
						channel: ch.Name(), pollMessageID: pollID, conversationID: e.ConversationID,
						kind: "elicitation", elicitationID: e.ID, turnID: turnID, field: spec.field, options: options,
						multi: spec.array, arrayField: spec.array, boolField: spec.boolean, createdAt: time.Now(),
					})
					return
				}
				s.log.Warn("failed to send elicitation poll", "channel", ch.Name(), "error", err)
			}
		}
		note := "The agent asked a question — open the web UI to answer it."
		if spec.question != "" {
			note = textfmt.OneLine(spec.question, 160) + "\n" + note
		}
		if err := ch.Notify(ctx, target, note); err != nil {
			s.log.Warn("failed to notify elicitation", "channel", ch.Name(), "error", err)
		}
	}()
}

type elicitPollSpec struct {
	question string
	field    string
	choices  []string
	array    bool
	boolean  bool
	maxSel   int
	ok       bool
}

// elicitationPollSpec reports whether an elicitation request is a single
// multiple-choice field that can ride a native poll: a string enum/oneOf, a
// boolean (Yes/No), or an array of enum/anyOf options (multi-select).
func elicitationPollSpec(raw json.RawMessage) elicitPollSpec {
	var req struct {
		Mode    string `json:"mode"`
		Message string `json:"message"`
		Schema  struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"requestedSchema"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return elicitPollSpec{}
	}
	if req.Mode != "form" || len(req.Schema.Properties) != 1 {
		return elicitPollSpec{}
	}
	spec := elicitPollSpec{ok: true, question: strings.TrimSpace(req.Message)}
	for name, rawProp := range req.Schema.Properties {
		spec.field = name
		var prop struct {
			Type  string   `json:"type"`
			Enum  []string `json:"enum"`
			OneOf []struct {
				Const string `json:"const"`
				Title string `json:"title"`
			} `json:"oneOf"`
			MaxItems *int `json:"maxItems"`
			Items    struct {
				Enum  []string `json:"enum"`
				AnyOf []struct {
					Const string `json:"const"`
					Title string `json:"title"`
				} `json:"anyOf"`
			} `json:"items"`
		}
		if err := json.Unmarshal(rawProp, &prop); err != nil {
			return elicitPollSpec{}
		}
		switch prop.Type {
		case "string":
			spec.choices = prop.Enum
			for _, o := range prop.OneOf {
				spec.choices = append(spec.choices, o.Const)
			}
		case "boolean":
			spec.boolean = true
			spec.choices = []string{"Yes", "No"}
		case "array":
			spec.array = true
			spec.choices = prop.Items.Enum
			for _, o := range prop.Items.AnyOf {
				spec.choices = append(spec.choices, o.Const)
			}
			spec.maxSel = len(spec.choices)
			if prop.MaxItems != nil && *prop.MaxItems > 0 && *prop.MaxItems < spec.maxSel {
				spec.maxSel = *prop.MaxItems
			}
		default:
			return elicitPollSpec{}
		}
	}
	if len(spec.choices) < 2 {
		return elicitPollSpec{}
	}
	trimmed := spec.choices[:0]
	for _, c := range spec.choices {
		if c = strings.TrimSpace(c); c != "" {
			trimmed = append(trimmed, c)
		}
	}
	spec.choices = trimmed
	if len(spec.choices) < 2 {
		return elicitPollSpec{}
	}
	if spec.question == "" {
		spec.question = "The agent asks: " + spec.field
	}
	return spec
}

// dedupePollOption keeps poll option labels unique so a vote maps to exactly
// one option id.
func dedupePollOption(base string, taken map[string]string) string {
	name := base
	for i := 2; ; i++ {
		if _, exists := taken[name]; !exists {
			return name
		}
		name = fmt.Sprintf("%s (%d)", base, i)
	}
}

// resolvePollVote routes a vote on a channel-sent poll to the pending
// approval or elicitation it answers. The waiter is consumed only when the
// vote is actionable — an undecodable selection or a vote from someone other
// than the prompter leaves it alive so a real vote can still land.
// Unknown/expired polls drop quietly — the poll was ours, not user content.
func (s *Service) resolvePollVote(ctx context.Context, in channels.Inbound) error {
	key := pollWaiterKey(in.Channel, in.PollVote.PollMessageID)
	s.mu.Lock()
	w := s.pollWaiters[key]
	if w != nil && time.Since(w.createdAt) > pollWaiterTTL {
		if w.timer != nil {
			w.timer.Stop()
		}
		delete(s.pollWaiters, key)
		w = nil
	}
	s.mu.Unlock()
	if w == nil {
		return nil
	}
	if !s.pollVoterAllowed(ctx, w.turnID, in.SenderID) {
		_ = s.reply(ctx, in, "Only the person who sent that request can answer it.")
		return nil
	}
	for _, option := range in.PollVote.Selected {
		if _, ok := w.options[option]; !ok {
			return nil
		}
	}
	if w.multi {
		s.mu.Lock()
		if s.pollWaiters[key] != w {
			s.mu.Unlock()
			return nil
		}
		w.latest = append([]string(nil), in.PollVote.Selected...)
		inbound := in
		w.generation++
		generation := w.generation
		if w.timer != nil {
			w.timer.Stop()
		}
		w.timer = time.AfterFunc(pollMultiQuiet, func() {
			s.mu.Lock()
			w := s.pollWaiters[key]
			if w == nil || w.generation != generation {
				s.mu.Unlock()
				return
			}
			sel := w.latest
			if len(sel) == 0 {
				// Deselected everything — no actionable answer; leave the
				// waiter armed for a later vote instead of killing the poll.
				w.timer = nil
				s.mu.Unlock()
				return
			}
			delete(s.pollWaiters, key)
			s.mu.Unlock()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = s.finishPollVote(ctx, inbound, w, sel)
		})
		s.mu.Unlock()
		return nil
	}
	if len(in.PollVote.Selected) == 0 {
		return nil
	}
	s.mu.Lock()
	if s.pollWaiters[key] != w {
		s.mu.Unlock()
		return nil
	}
	delete(s.pollWaiters, key)
	s.mu.Unlock()
	return s.finishPollVote(ctx, in, w, in.PollVote.Selected)
}

// pollVoterAllowed mirrors the /approve guard: only the sender recorded on
// the turn may answer its questions. Missing origins/senders keep the
// previous permissive behavior.
func (s *Service) pollVoterAllowed(ctx context.Context, turnID, senderID string) bool {
	if turnID == "" {
		return true
	}
	turn, err := s.store.GetTurn(ctx, turnID)
	if err != nil {
		return true
	}
	ref := turnOriginSender(turn)
	return ref == "" || senderID == "" || ref == senderID
}

// finishPollVote resolves the pending question with the decoded selection and
// confirms the answer back to the voter's chat.
func (s *Service) finishPollVote(ctx context.Context, in channels.Inbound, w *pollWaiter, sel []string) error {
	if len(sel) == 0 {
		return nil
	}
	switch w.kind {
	case "approval":
		optionID, chosen := "", ""
		for _, name := range sel {
			if id, ok := w.options[name]; ok {
				optionID, chosen = id, name
				break
			}
		}
		if optionID == "" {
			return nil
		}
		if _, err := s.ResolveApproval(ctx, w.approvalID, optionID); err != nil {
			return err
		}
		_ = s.reply(ctx, in, "Approval answered: "+chosen)
	case "elicitation":
		content := map[string]any{}
		switch {
		case w.boolField:
			content[w.field] = strings.EqualFold(sel[0], "yes")
		case w.arrayField:
			list := make([]any, 0, len(sel))
			for _, label := range sel {
				if v, ok := w.options[label]; ok {
					list = append(list, v)
				}
			}
			content[w.field] = list
		default:
			if v, ok := w.options[sel[0]]; ok {
				content[w.field] = v
			}
		}
		if len(content) == 0 {
			return nil
		}
		if err := s.ResolveElicitation(w.conversationID, w.elicitationID, ElicitationResponse{
			Action: "accept", Content: content,
		}); err != nil {
			return err
		}
		_ = s.reply(ctx, in, "Answered: "+strings.Join(sel, ", "))
	}
	return nil
}
