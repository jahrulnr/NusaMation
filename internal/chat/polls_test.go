package chat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/jahrulnr/nusamation/internal/channels"
	"github.com/jahrulnr/nusamation/internal/events"
	"github.com/jahrulnr/nusamation/internal/store"
)

type fakePollChannel struct {
	name     string
	question string
	options  []string
	maxSel   int
	err      error
}

func (f *fakePollChannel) Name() string                { return f.name }
func (f *fakePollChannel) Label() string               { return f.name }
func (f *fakePollChannel) Start(context.Context) error { return nil }
func (f *fakePollChannel) Stop() error                 { return nil }
func (f *fakePollChannel) Status() channels.Status     { return channels.Status{Name: f.name} }
func (f *fakePollChannel) Begin(context.Context, channels.Target, string) (channels.Delivery, error) {
	return nil, errors.New("not implemented")
}
func (f *fakePollChannel) Notify(context.Context, channels.Target, string) error { return nil }
func (f *fakePollChannel) SendPoll(_ context.Context, _ channels.Target, question string, options []string, maxSelectable int) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.question, f.options, f.maxSel = question, options, maxSelectable
	return "POLLMSG1", nil
}

func testPollService() *Service {
	return &Service{
		log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		pollWaiters:  map[string]*pollWaiter{},
		elicitations: map[string]*elicitationWaiter{},
	}
}

func TestNotifyApprovalPoll(t *testing.T) {
	svc := testPollService()
	fc := &fakePollChannel{name: "whatsapp"}
	ap := store.Approval{
		ID: "ap1", ConversationID: "c1", Title: "Run rm -rf",
		Options: []store.PermissionOption{
			{OptionID: "o1", Name: "Allow once", Kind: "allow_once"},
			{OptionID: "o2", Name: "Allow always", Kind: "allow_always"},
			{OptionID: "o3", Name: "Reject", Kind: "reject_once"},
		},
	}
	if !svc.notifyApprovalPoll(context.Background(), fc, channels.Target{}, ap) {
		t.Fatal("poll-capable channel refused approval poll")
	}
	if fc.maxSel != 1 || len(fc.options) != 3 || fc.options[0] != "Allow once" {
		t.Fatalf("poll args = %#v max=%d", fc.options, fc.maxSel)
	}
	w := svc.pollWaiters[pollWaiterKey("whatsapp", "POLLMSG1")]
	if w == nil || w.approvalID != "ap1" || w.options["Allow always"] != "o2" {
		t.Fatalf("waiter = %#v", w)
	}

	// Too many options → caller falls back to text.
	many := store.Approval{ID: "ap2", Options: make([]store.PermissionOption, 13)}
	if svc.notifyApprovalPoll(context.Background(), fc, channels.Target{}, many) {
		t.Fatal("13-option approval should not fit a poll")
	}
	// Send failure → fallback.
	fc.err = errors.New("send failed")
	if svc.notifyApprovalPoll(context.Background(), fc, channels.Target{}, ap) {
		t.Fatal("send failure should report false")
	}
}

func TestResolvePollVoteApproval(t *testing.T) {
	svc := testPollService()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc.store = st
	svc.registerPollWaiter(&pollWaiter{
		channel: "whatsapp", pollMessageID: "P1", conversationID: "c1",
		kind: "approval", approvalID: "ap1",
		options:   map[string]string{"Allow once": "o1", "Reject": "o3"},
		createdAt: time.Now(),
	})
	// The voter guard passes (no turnID on the waiter), so the vote is
	// actionable: the waiter is consumed, then resolution hits the empty
	// store and errors at GetApproval.
	err = svc.resolvePollVote(context.Background(), channels.Inbound{
		Channel: "whatsapp", ExternalID: "62@s.whatsapp.net",
		PollVote: &channels.PollVote{PollMessageID: "P1", Selected: []string{"Allow once"}},
	})
	if err == nil {
		t.Fatal("expected store error resolving approval without a store")
	}
	if svc.pollWaiters[pollWaiterKey("whatsapp", "P1")] != nil {
		t.Fatal("waiter should be consumed by the vote")
	}
}

func TestResolvePollVoteElicitation(t *testing.T) {
	svc := testPollService()
	res := make(chan ElicitationResponse, 1)
	svc.elicitations["el1"] = &elicitationWaiter{
		request: Elicitation{ID: "el1", ConversationID: "c1",
			Request: json.RawMessage(`{"mode":"form","requestedSchema":{"properties":{"strategy":{"type":"string","enum":["safe","full"]}},"required":["strategy"]}}`)},
		result: res,
	}
	svc.registerPollWaiter(&pollWaiter{
		channel: "whatsapp", pollMessageID: "P2", conversationID: "c1",
		kind: "elicitation", elicitationID: "el1", field: "strategy",
		options:   map[string]string{"safe": "safe", "full": "full"},
		createdAt: time.Now(),
	})
	if err := svc.resolvePollVote(context.Background(), channels.Inbound{
		Channel: "whatsapp", ExternalID: "62@s.whatsapp.net",
		PollVote: &channels.PollVote{PollMessageID: "P2", Selected: []string{"full"}},
	}); err != nil {
		t.Fatal(err)
	}
	got := <-res
	if got.Action != "accept" || got.Content["strategy"] != "full" {
		t.Fatalf("elicitation response = %#v", got)
	}
	if svc.pollWaiters[pollWaiterKey("whatsapp", "P2")] != nil {
		t.Fatal("waiter should be consumed")
	}
}

func TestResolvePollVoteMultiDebounce(t *testing.T) {
	svc := testPollService()
	svc.registerPollWaiter(&pollWaiter{
		channel: "whatsapp", pollMessageID: "P3", conversationID: "c1",
		kind: "elicitation", elicitationID: "el2", field: "tags", multi: true, arrayField: true,
		options:   map[string]string{"a": "a", "b": "b"},
		createdAt: time.Now(),
	})
	in := channels.Inbound{
		Channel: "whatsapp", ExternalID: "62@s.whatsapp.net",
		PollVote: &channels.PollVote{PollMessageID: "P3", Selected: []string{"a"}},
	}
	if err := svc.resolvePollVote(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	w := svc.pollWaiters[pollWaiterKey("whatsapp", "P3")]
	if w == nil {
		t.Fatal("multi-select waiter resolved too early")
	}
	if len(w.latest) != 1 || w.latest[0] != "a" || w.timer == nil {
		t.Fatalf("multi vote not debounced: %#v", w)
	}
	in.PollVote.Selected = []string{"a", "b"}
	if err := svc.resolvePollVote(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(w.latest) != 2 {
		t.Fatalf("latest selection = %#v", w.latest)
	}
	in.PollVote.Selected = nil
	if err := svc.resolvePollVote(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(w.latest) != 0 || w.timer == nil {
		t.Fatal("deselecting all must replace the previous selection")
	}
	w.timer.Stop()
	delete(svc.pollWaiters, pollWaiterKey("whatsapp", "P3"))
}

func TestResolvePollVoteInvalidKeepsWaiter(t *testing.T) {
	svc := testPollService()
	svc.registerPollWaiter(&pollWaiter{channel: "whatsapp", pollMessageID: "P", kind: "approval", options: map[string]string{"Yes": "yes"}, createdAt: time.Now()})
	if err := svc.resolvePollVote(context.Background(), channels.Inbound{Channel: "whatsapp", PollVote: &channels.PollVote{PollMessageID: "P", Selected: []string{"unknown"}}}); err != nil {
		t.Fatal(err)
	}
	if svc.pollWaiters[pollWaiterKey("whatsapp", "P")] == nil {
		t.Fatal("unknown option consumed the waiter")
	}
}

func TestResolvePollVoteGuardKeepsWaiter(t *testing.T) {
	svc := testPollService()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc.store = st
	enabled := true
	ag, err := st.CreateAgent(context.Background(), store.AgentInput{
		Name: "Fake", Slug: "fake-poll", Command: "true", Cwd: t.TempDir(), Enabled: &enabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	conv, err := st.CreateConversation(context.Background(), ag.ID, "t", t.TempDir(), "whatsapp", "", "")
	if err != nil {
		t.Fatal(err)
	}
	userMsg := &store.Message{ConversationID: conv.ID, Role: "user", Text: "go"}
	if err := st.AppendMessage(context.Background(), userMsg); err != nil {
		t.Fatal(err)
	}
	turn, err := st.CreateTurn(context.Background(), conv.ID, userMsg.ID, "whatsapp", `{"senderId":"owner"}`)
	if err != nil {
		t.Fatal(err)
	}
	svc.registerPollWaiter(&pollWaiter{
		channel: "whatsapp", pollMessageID: "P9", conversationID: "c1",
		kind: "approval", approvalID: "ap1", turnID: turn.ID,
		options:   map[string]string{"Allow once": "o1"},
		createdAt: time.Now(),
	})
	// A different group member taps — the waiter must survive so the
	// prompter's real vote can still land.
	if err := svc.resolvePollVote(context.Background(), channels.Inbound{
		Channel: "whatsapp", ExternalID: "grp@g.us", SenderID: "intruder",
		PollVote: &channels.PollVote{PollMessageID: "P9", Selected: []string{"Allow once"}},
	}); err != nil {
		t.Fatal(err)
	}
	if svc.pollWaiters[pollWaiterKey("whatsapp", "P9")] == nil {
		t.Fatal("guard-rejected vote consumed the waiter")
	}
	// The prompter's vote consumes it.
	if err := svc.resolvePollVote(context.Background(), channels.Inbound{
		Channel: "whatsapp", ExternalID: "grp@g.us", SenderID: "owner",
		PollVote: &channels.PollVote{PollMessageID: "P9", Selected: []string{"Allow once"}},
	}); err == nil {
		t.Fatal("expected store error resolving approval")
	}
	if svc.pollWaiters[pollWaiterKey("whatsapp", "P9")] != nil {
		t.Fatal("actionable vote did not consume the waiter")
	}
}

func TestResolvePollVoteEmptyKeepsWaiter(t *testing.T) {
	svc := testPollService()
	svc.registerPollWaiter(&pollWaiter{
		channel: "whatsapp", pollMessageID: "P8", kind: "approval", approvalID: "ap1",
		options:   map[string]string{"Allow once": "o1"},
		createdAt: time.Now(),
	})
	if err := svc.resolvePollVote(context.Background(), channels.Inbound{
		Channel: "whatsapp", PollVote: &channels.PollVote{PollMessageID: "P8"},
	}); err != nil {
		t.Fatal(err)
	}
	if svc.pollWaiters[pollWaiterKey("whatsapp", "P8")] == nil {
		t.Fatal("undecodable vote consumed the waiter")
	}
}

func TestNotifyElicitationRegistersPollWaiter(t *testing.T) {
	svc := testPollService()
	mgr := channels.NewManager(events.Nop{}, svc.log)
	fc := &fakePollChannel{name: "whatsapp"}
	mgr.Register(fc)
	svc.channels = mgr
	a := &activeTurn{
		conv: store.Conversation{
			ID:      "c1",
			Links:   []store.ChannelLink{{Channel: "whatsapp", ExternalID: "62@s.whatsapp.net"}},
			Routing: store.Routing{NotifyApprovals: true},
		},
		turn: store.Turn{ID: "t1", OriginChannel: "whatsapp", OriginRef: `{"externalId":"62@s.whatsapp.net"}`},
	}
	svc.notifyElicitation(a, Elicitation{ID: "el1", ConversationID: "c1", Request: json.RawMessage(
		`{"mode":"form","message":"pick one","requestedSchema":{"properties":{"s":{"type":"string","enum":["a","b"]}}}}`)})
	waiter := func() *pollWaiter {
		svc.mu.Lock()
		defer svc.mu.Unlock()
		return svc.pollWaiters[pollWaiterKey("whatsapp", "POLLMSG1")]
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && waiter() == nil {
		time.Sleep(20 * time.Millisecond)
	}
	w := waiter()
	if w == nil || w.elicitationID != "el1" || w.turnID != "t1" || w.field != "s" {
		t.Fatalf("waiter = %#v", w)
	}
	if fc.maxSel != 1 || fc.question != "pick one" || len(fc.options) != 2 {
		t.Fatalf("poll = %q %#v", fc.question, fc.options)
	}
}

func TestDropPollWaiters(t *testing.T) {
	svc := testPollService()
	svc.registerPollWaiter(&pollWaiter{
		channel: "whatsapp", pollMessageID: "P7", kind: "approval", approvalID: "ap1",
		createdAt: time.Now(),
	})
	svc.dropPollWaiters(func(w *pollWaiter) bool { return w.approvalID == "ap1" })
	if len(svc.pollWaiters) != 0 {
		t.Fatal("waiter not dropped")
	}
}

func TestResolvePollVoteUnknownPoll(t *testing.T) {
	svc := testPollService()
	if err := svc.resolvePollVote(context.Background(), channels.Inbound{
		Channel: "whatsapp", PollVote: &channels.PollVote{PollMessageID: "GHOST", Selected: []string{"x"}},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestElicitationPollSpec(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		ok   bool
		arr  bool
		bool bool
		max  int
	}{
		{"single enum", `{"mode":"form","message":"pick?","requestedSchema":{"properties":{"s":{"type":"string","enum":["a","b"]}}}}`, true, false, false, 0},
		{"oneOf", `{"mode":"form","requestedSchema":{"properties":{"s":{"type":"string","oneOf":[{"const":"x"},{"const":"y"}]}}}}`, true, false, false, 0},
		{"boolean", `{"mode":"form","requestedSchema":{"properties":{"ok":{"type":"boolean"}}}}`, true, false, true, 0},
		{"multi array", `{"mode":"form","requestedSchema":{"properties":{"tags":{"type":"array","items":{"enum":["a","b","c"]},"maxItems":2}}}}`, true, true, false, 2},
		{"multi no cap", `{"mode":"form","requestedSchema":{"properties":{"tags":{"type":"array","items":{"enum":["a","b","c"]}}}}}`, true, true, false, 3},
		{"two fields", `{"mode":"form","requestedSchema":{"properties":{"a":{"type":"string","enum":["x","y"]},"b":{"type":"boolean"}}}}`, false, false, false, 0},
		{"free text", `{"mode":"form","requestedSchema":{"properties":{"name":{"type":"string"}}}}`, false, false, false, 0},
		{"url mode", `{"mode":"url","url":"https://x"}`, false, false, false, 0},
	}
	for _, tc := range cases {
		spec := elicitationPollSpec(json.RawMessage(tc.raw))
		if spec.ok != tc.ok || spec.array != tc.arr || spec.boolean != tc.bool {
			t.Fatalf("%s: spec = %#v", tc.name, spec)
		}
		if tc.ok && spec.array && spec.maxSel != tc.max {
			t.Fatalf("%s: maxSel = %d, want %d", tc.name, spec.maxSel, tc.max)
		}
	}
	if spec := elicitationPollSpec(json.RawMessage(cases[3].raw)); spec.choices[0] != "a" || spec.field != "tags" {
		t.Fatalf("array spec = %#v", spec)
	}
	if spec := elicitationPollSpec(json.RawMessage(cases[2].raw)); len(spec.choices) != 2 || spec.choices[0] != "Yes" {
		t.Fatalf("boolean spec = %#v", spec)
	}
}

func TestDedupePollOption(t *testing.T) {
	taken := map[string]string{}
	if got := dedupePollOption("Yes", taken); got != "Yes" {
		t.Fatalf("first = %q", got)
	}
	taken["Yes"] = "o1"
	if got := dedupePollOption("Yes", taken); got != "Yes (2)" {
		t.Fatalf("second = %q", got)
	}
	taken["Yes (2)"] = "o2"
	if got := dedupePollOption("Yes", taken); got != "Yes (3)" {
		t.Fatalf("third = %q", got)
	}
}
