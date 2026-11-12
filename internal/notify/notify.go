// Package notify turns server-side events into host OS notifications —
// notify-send on Linux, osascript on macOS, a PowerShell balloon on
// Windows. Broadcaster wraps the realtime hub so event producers stay
// unchanged; the wrapper decides which event types deserve a desktop
// notification.
package notify

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jahrulnr/nusamation/internal/events"
	"github.com/jahrulnr/nusamation/internal/store"
	"github.com/jahrulnr/nusamation/internal/textfmt"
)

// Urgency maps to the platform backend: notify-send urgency levels on Linux,
// the ToolTipIcon on Windows. macOS has no urgency concept.
type Urgency string

const (
	UrgencyLow      Urgency = "low"
	UrgencyNormal   Urgency = "normal"
	UrgencyCritical Urgency = "critical"
	// DeliveryTimeout leaves time for the platform notification backend to start.
	DeliveryTimeout = 20 * time.Second
)

type Notification struct {
	Title   string
	Body    string
	Urgency Urgency
	// Icon is a freedesktop icon name (dialog-question, dialog-error, …);
	// other backends map it to their closest equivalent.
	Icon string
}

// Sender delivers a notification to the host OS.
type Sender interface {
	// Name identifies the backend ("notify-send", "powershell", "none").
	Name() string
	Available() bool
	Notify(ctx context.Context, n Notification) error
}

// Null is the fallback when no notification backend exists on this host.
type Null struct{ Reason string }

func (n Null) Name() string    { return "none" }
func (n Null) Available() bool { return false }
func (n Null) Notify(context.Context, Notification) error {
	return errors.New(n.Reason)
}

// Detect picks the platform backend. When none is usable it returns Null —
// the app works without desktop notifications.
func Detect() Sender { return platformSender() }

// Broadcaster forwards every event to the wrapped hub and, as a side effect,
// emits OS notifications for events worth surfacing outside the web UI.
type Broadcaster struct {
	inner  events.Broadcaster
	sender Sender
	store  *store.Store
	log    *slog.Logger
	muted  atomic.Bool
	warned atomic.Bool
}

type Options struct {
	Sender Sender
	Store  *store.Store
	Log    *slog.Logger
}

func Wrap(inner events.Broadcaster, opts Options) *Broadcaster {
	if inner == nil {
		inner = events.Nop{}
	}
	if opts.Sender == nil {
		opts.Sender = Detect()
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	b := &Broadcaster{
		inner: inner, sender: opts.Sender, store: opts.Store,
		log: opts.Log.With("component", "notify"),
	}
	b.warnUnavailable()
	if p, ok := opts.Sender.(interface{ Probe(context.Context) }); ok {
		go func() {
			p.Probe(context.Background())
			b.warnUnavailable()
		}()
	}
	return b
}

// warnUnavailable logs once that no usable OS notification backend exists —
// e.g. a headless SBC/VPS — so operators see why the settings section is
// hidden instead of silently missing notifications.
func (b *Broadcaster) warnUnavailable() {
	if b.sender.Available() || b.warned.Swap(true) {
		return
	}
	reason := ""
	switch x := b.sender.(type) {
	case Null:
		reason = x.Reason
	case interface{ why() string }:
		reason = x.why()
	}
	b.log.Warn("desktop notifications unavailable on this host", "backend", b.sender.Name(), "reason", reason)
}

func (b *Broadcaster) Broadcast(ev events.Event) {
	b.inner.Broadcast(ev)
	switch ev.Type {
	case "approval.created", "turn.completed":
		if b.muted.Load() || !b.sender.Available() {
			return
		}
		go b.notifyEvent(ev)
	}
}

// Mute suppresses OS notifications permanently. The app calls it during
// shutdown so the agent processes being stopped do not raise "agent error"
// popups while the user intentionally restarts or updates.
func (b *Broadcaster) Mute() { b.muted.Store(true) }

// Available reports whether an OS notification backend exists on this host.
func (b *Broadcaster) Available() bool { return b.sender.Available() }

// ProbeAvailable re-checks a backend whose delivery breaker may have tripped;
// the settings UI uses it so a missing or recovered daemon shows up without
// waiting for the next send.
func (b *Broadcaster) ProbeAvailable() bool {
	if p, ok := b.sender.(interface{ probeState() bool }); ok {
		return p.probeState()
	}
	return b.sender.Available()
}

// Backend names the detected backend for display/diagnostics.
func (b *Broadcaster) Backend() string { return b.sender.Name() }

// Enabled reads the notify.enabled setting; notifications default to on.
func (b *Broadcaster) Enabled(ctx context.Context) bool {
	if b.store == nil {
		return true
	}
	v, err := b.store.GetSetting(ctx, "notify.enabled")
	return err != nil || v != "false"
}

// Send delivers a one-off OS notification honoring the enabled setting and
// shutdown mute. Used for events that are not chat events (e.g. a new
// release being available).
func (b *Broadcaster) Send(ctx context.Context, n Notification) {
	if b.muted.Load() || !b.sender.Available() || !b.Enabled(ctx) {
		return
	}
	if err := b.sender.Notify(ctx, n); err != nil {
		b.log.Warn("desktop notification failed", "title", n.Title, "error", err)
	}
}

// Test sends a sample notification regardless of the enabled flag, so the
// settings dialog can prove the OS backend works.
func (b *Broadcaster) Test(ctx context.Context) error {
	return b.sender.Notify(ctx, Notification{
		Title: "NusaMation", Body: "Desktop notifications are working.",
		Urgency: UrgencyNormal, Icon: "dialog-information",
	})
}

func (b *Broadcaster) notifyEvent(ev events.Event) {
	ctx, cancel := context.WithTimeout(context.Background(), DeliveryTimeout)
	defer cancel()
	if !b.Enabled(ctx) {
		return
	}
	n := b.build(ctx, ev)
	if n == nil || b.muted.Load() {
		return
	}
	if err := b.sender.Notify(ctx, *n); err != nil {
		b.log.Warn("desktop notification failed", "type", ev.Type, "error", err)
	}
}

func (b *Broadcaster) build(ctx context.Context, ev events.Event) *Notification {
	switch ev.Type {
	case "approval.created":
		var p struct {
			Approval store.Approval `json:"approval"`
		}
		if decode(ev.Data, &p) != nil || p.Approval.ID == "" || p.Approval.Status != "pending" {
			return nil
		}
		ap := p.Approval
		meta := joinNonEmpty(" · ", ap.ConversationName, ap.AgentName)
		if ap.Risk != "" {
			meta = joinNonEmpty(" · ", meta, "risk: "+ap.Risk)
		}
		return &Notification{
			Title:   "Approval needed",
			Body:    joinNonEmpty("\n", textfmt.OneLine(ap.Title, 200), meta),
			Urgency: UrgencyCritical, Icon: "dialog-question",
		}
	case "turn.completed":
		var p struct {
			Status string `json:"status"`
			Error  string `json:"error"`
		}
		if decode(ev.Data, &p) != nil {
			return nil
		}
		var title, body, icon string
		var urg Urgency
		switch p.Status {
		case "completed":
			title, urg, icon = "Agent finished", UrgencyNormal, "dialog-information"
		case "error":
			title, urg, icon = "Agent error", UrgencyCritical, "dialog-error"
		default:
			return nil
		}
		body = b.conversationTitle(ctx, ev.ConversationID)
		if p.Status == "error" && p.Error != "" {
			body = joinNonEmpty("\n", body, textfmt.OneLine(p.Error, 200))
		}
		return &Notification{Title: title, Body: body, Urgency: urg, Icon: icon}
	}
	return nil
}

func (b *Broadcaster) conversationTitle(ctx context.Context, id string) string {
	if b.store == nil || id == "" {
		return ""
	}
	conv, err := b.store.GetConversation(ctx, id)
	if err != nil {
		return ""
	}
	return textfmt.OneLine(conv.Title, 120)
}

func decode(data any, dst any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dst)
}

func joinNonEmpty(sep string, parts ...string) string {
	keep := parts[:0]
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			keep = append(keep, p)
		}
	}
	return strings.Join(keep, sep)
}
