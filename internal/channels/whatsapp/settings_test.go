package whatsapp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/types"

	"github.com/jahrulnr/nusamation/internal/store"
)

func TestAllowlistAndNormalization(t *testing.T) {
	s := DefaultSettings()
	if s.allows([]string{"123@s.whatsapp.net"}, nil) {
		t.Fatal("empty allowlist should deny all until the operator opts in")
	}
	// The wildcard is the explicit allow-everything entry.
	s.AllowedNumbers = []string{"*"}
	s.Normalize()
	if len(s.AllowedNumbers) != 1 || s.AllowedNumbers[0] != "*" {
		t.Fatalf("normalized wildcard = %#v", s.AllowedNumbers)
	}
	if !s.allows([]string{"123@s.whatsapp.net"}, nil) {
		t.Fatal("wildcard should allow all")
	}
	// The wildcard must stand alone when mixed with numbers.
	mixed := Settings{AllowedNumbers: []string{"6281234567890", "*", "junk"}}
	mixed.Normalize()
	if len(mixed.AllowedNumbers) != 2 || mixed.AllowedNumbers[0] != "*" || mixed.AllowedNumbers[1] != "6281234567890" {
		t.Fatalf("mixed normalize = %#v", mixed.AllowedNumbers)
	}
	if err := (Settings{AllowedNumbers: []string{"*", "6281234567890"}}).ValidateAllowlist(); err == nil {
		t.Fatal("wildcard combined with numbers should be refused")
	}

	s.AllowedNumbers = []string{"+62 812-3456-7890", "1203630@g.us"}
	s.Normalize()
	if len(s.AllowedNumbers) != 2 {
		t.Fatalf("normalized allowlist = %#v", s.AllowedNumbers)
	}
	if !s.allows([]string{"6281234567890@s.whatsapp.net"}, nil) {
		t.Fatal("normalized phone should be allowed")
	}
	if !s.allows([]string{"999@s.whatsapp.net"}, []string{"1203630@g.us"}) {
		t.Fatal("allowlisted group should be allowed")
	}
	if s.allows([]string{"999@s.whatsapp.net"}, []string{"555@g.us"}) {
		t.Fatal("unknown sender/group should be denied")
	}
}

func TestGroupAllowlist(t *testing.T) {
	// Group entries keep their full @g.us JID: the identity survives the
	// save, so the panel picker can show which group a row is.
	s := Settings{AllowedGroups: []string{"1203630-1234@g.us:7", "Family Group", "1203630-1234@g.us"}}
	s.Normalize()
	if len(s.AllowedGroups) != 1 || s.AllowedGroups[0] != "1203630-1234@g.us" {
		t.Fatalf("normalized groups = %#v", s.AllowedGroups)
	}
	if !s.allows(nil, []string{"1203630-1234@g.us"}) {
		t.Fatal("the allowlisted group chat should be allowed even with no allowlisted sender")
	}
	if s.allows(nil, []string{"1203630-9999@g.us"}) {
		t.Fatal("a different group should be denied")
	}
	// A group JID can never whitelist an individual sender.
	if s.allows([]string{"1203630-1234@g.us"}, []string{"628@s.whatsapp.net"}) {
		t.Fatal("group entries must not match sender JIDs")
	}
	// A phone number stays required for one-to-one chats.
	if s.allows([]string{"6281234567890@s.whatsapp.net"}, nil) {
		t.Fatal("groups-only allowlist should still deny direct messages")
	}
	// Bare digits are ambiguous with a phone number, so they are refused.
	if NormalizeGroupJID("1203630") != "" || NormalizeGroupJID("1203630@lid") != "" || NormalizeGroupJID("team room@g.us") != "" {
		t.Fatal("only digits-and-hyphen @g.us JIDs are valid group entries")
	}
	// The wildcard must stand alone across both lists.
	mixed := Settings{AllowedNumbers: []string{"*"}, AllowedGroups: []string{"1203630-1234@g.us"}}
	mixed.Normalize()
	if err := mixed.ValidateAllowlist(); err == nil {
		t.Fatal("wildcard combined with groups should be refused")
	}
	// Legacy installs may keep a stripped group JID among the numbers; the
	// chat-JID comparison keeps working.
	legacy := Settings{AllowedNumbers: []string{"1203630"}}
	if !legacy.allows([]string{"999@s.whatsapp.net"}, []string{"1203630@g.us"}) {
		t.Fatal("legacy stripped group entry should still allow the group")
	}
}

func TestSettingsGroupPolicyNormalization(t *testing.T) {
	s := Settings{GroupPolicy: "wat"}
	s.Normalize()
	if s.GroupPolicy != "mention" {
		t.Fatalf("group policy = %q", s.GroupPolicy)
	}
	if NormalizeNumber("+62 (812) 3456") != "628123456" {
		t.Fatalf("NormalizeNumber = %q", NormalizeNumber("+62 (812) 3456"))
	}
}

func TestLoadSettingsFailsClosedOnCorruptRow(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	c := &Channel{store: st, log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	// A missing row yields the built-in defaults.
	if got := c.loadSettings(ctx, failClosedSettings()); !got.Enabled {
		t.Fatal("missing settings row should yield defaults")
	}

	// A valid row round-trips.
	if err := st.SetJSONSetting(ctx, SettingsKey, Settings{Enabled: true, GroupPolicy: "all"}); err != nil {
		t.Fatal(err)
	}
	got := c.loadSettings(ctx, failClosedSettings())
	if !got.Enabled || got.GroupPolicy != "all" {
		t.Fatalf("stored settings = %#v", got)
	}

	// A corrupt row keeps the last-good settings rather than reopening the
	// channel with an empty allowlist.
	if err := st.SetSetting(ctx, SettingsKey, "{not json"); err != nil {
		t.Fatal(err)
	}
	if kept := c.loadSettings(ctx, got); !kept.Enabled || kept.GroupPolicy != "all" {
		t.Fatalf("corrupt settings lost last-good values: %#v", kept)
	}
	if closed := c.loadSettings(ctx, failClosedSettings()); closed.Enabled {
		t.Fatal("corrupt settings on first load did not fail closed")
	}
}

func TestResponseRulesCoversWhatsAppSurface(t *testing.T) {
	rules := (&Channel{}).ResponseRules()
	for _, marker := range []string{"*bold*", "_italic_", "~strikethrough~", "30,000", "attachment"} {
		if !strings.Contains(rules, marker) {
			t.Fatalf("rules %q missing %q", rules, marker)
		}
	}
}

// Saving an allowlist whose entries are all invalid must be refused, like the
// CLI, because it would normalize to a deny-everything list by accident. An
// intentional clear (`[]`) stays allowed and now means "deny everyone"; a
// wildcard mixed with numbers is refused outright.
func TestUpdateSettingsRefusesInvalidAllowlist(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	c := &Channel{store: st, log: slog.New(slog.NewTextHandler(io.Discard, nil)), settings: DefaultSettings()}

	err = c.UpdateSettings(ctx, []byte(`{"allowedNumbers":["Alice","not a number"]}`))
	if err == nil || !strings.Contains(err.Error(), "no valid numbers") {
		t.Fatalf("all-invalid allowlist error = %v, want refusal", err)
	}
	got := c.settingsSnapshot()
	if len(got.AllowedNumbers) != 0 {
		t.Fatalf("settings changed after refusal: %#v", got)
	}

	// One valid entry saves; invalid siblings are dropped by normalization.
	if err := c.UpdateSettings(ctx, []byte(`{"allowedNumbers":["+62 812-3456-7890","Alice"]}`)); err != nil {
		t.Fatal(err)
	}
	got = c.settingsSnapshot()
	if len(got.AllowedNumbers) != 1 || got.AllowedNumbers[0] != "6281234567890" {
		t.Fatalf("allowlist = %#v", got.AllowedNumbers)
	}

	// The wildcard must stand alone.
	err = c.UpdateSettings(ctx, []byte(`{"allowedNumbers":["*","6281234567890"]}`))
	if err == nil || !strings.Contains(err.Error(), "only allowlist entry") {
		t.Fatalf("mixed wildcard error = %v, want refusal", err)
	}
	if got := c.settingsSnapshot(); len(got.AllowedNumbers) != 1 || got.AllowedNumbers[0] != "6281234567890" {
		t.Fatalf("settings changed after wildcard refusal: %#v", got)
	}
	if err := c.UpdateSettings(ctx, []byte(`{"allowedNumbers":["*"]}`)); err != nil {
		t.Fatal(err)
	}
	if got := c.settingsSnapshot(); len(got.AllowedNumbers) != 1 || got.AllowedNumbers[0] != "*" {
		t.Fatalf("wildcard allowlist = %#v", got.AllowedNumbers)
	}

	// An explicit empty array is an intentional clear — and now denies all.
	if err := c.UpdateSettings(ctx, []byte(`{"allowedNumbers":[]}`)); err != nil {
		t.Fatal(err)
	}
	got = c.settingsSnapshot()
	if len(got.AllowedNumbers) != 0 {
		t.Fatalf("clear allowlist = %#v", got.AllowedNumbers)
	}
	if got.allows([]string{"6281234567890@s.whatsapp.net"}, nil) {
		t.Fatal("cleared allowlist must deny everyone")
	}

	// A patch without allowedNumbers leaves the list untouched.
	if err := c.UpdateSettings(ctx, []byte(`{"sendTyping":false}`)); err != nil {
		t.Fatal(err)
	}
	if got := c.settingsSnapshot(); len(got.AllowedNumbers) != 0 || got.SendTyping {
		t.Fatalf("patch without allowlist changed values: %#v", got)
	}
}

// The group allowlist is picker-driven, so a save that carries group entries
// refuses non-group values loudly instead of dropping them into a silent
// deny, and an intentional clear stays a clear.
func TestUpdateSettingsGroupAllowlist(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	c := &Channel{store: st, log: slog.New(slog.NewTextHandler(io.Discard, nil)), settings: DefaultSettings()}

	// A bare number is ambiguous with the numbers allowlist and refused.
	err = c.UpdateSettings(ctx, []byte(`{"allowedGroups":["6281234567890"]}`))
	if err == nil || !strings.Contains(err.Error(), "not a group JID") {
		t.Fatalf("bare number as group error = %v, want refusal", err)
	}
	if got := c.settingsSnapshot(); len(got.AllowedGroups) != 0 {
		t.Fatalf("settings changed after group refusal: %#v", got)
	}

	// Picker JIDs save verbatim and keep the group reachable.
	if err := c.UpdateSettings(ctx, []byte(`{"allowedGroups":["1203630-1234@g.us:7"]}`)); err != nil {
		t.Fatal(err)
	}
	got := c.settingsSnapshot()
	if len(got.AllowedGroups) != 1 || got.AllowedGroups[0] != "1203630-1234@g.us" {
		t.Fatalf("group allowlist = %#v", got.AllowedGroups)
	}
	if !got.allows(nil, []string{"1203630-1234@g.us"}) {
		t.Fatal("saved group should be allowed")
	}

	// The wildcard may not be mixed with groups either.
	err = c.UpdateSettings(ctx, []byte(`{"allowedNumbers":["*"],"allowedGroups":["1203630-1234@g.us"]}`))
	if err == nil || !strings.Contains(err.Error(), "only allowlist entry") {
		t.Fatalf("wildcard+groups error = %v, want refusal", err)
	}

	// An explicit empty array is an intentional clear; without the key the
	// stored groups stay untouched.
	if err := c.UpdateSettings(ctx, []byte(`{"allowedGroups":[]}`)); err != nil {
		t.Fatal(err)
	}
	if len(c.settingsSnapshot().AllowedGroups) != 0 {
		t.Fatal("group clear was not applied")
	}
}

// Pacing and queue-cap clamp into their configured ranges; defaults are 5 s
// and 20.
func TestSettingsPacingNormalization(t *testing.T) {
	s := Settings{SendPacing: -3, QueueCap: 0}
	s.Normalize()
	if s.SendPacing != 0 {
		t.Fatalf("negative pacing = %d", s.SendPacing)
	}
	if s.QueueCap != 1 {
		t.Fatalf("zero cap = %d", s.QueueCap)
	}
	s.SendPacing, s.QueueCap = 99, 99999
	s.Normalize()
	if s.SendPacing != 60 || s.QueueCap != 200 {
		t.Fatalf("huge pacing/cap = %d/%d", s.SendPacing, s.QueueCap)
	}
	def := DefaultSettings()
	if def.SendPacing != 5 || def.QueueCap != 20 {
		t.Fatalf("defaults = %d/%d, want 5/20", def.SendPacing, def.QueueCap)
	}
}

type fakePresence struct {
	resp  []types.IsOnWhatsAppResponse
	err   error
	calls [][]string
}

func (f *fakePresence) IsOnWhatsApp(_ context.Context, phones []string) ([]types.IsOnWhatsAppResponse, error) {
	f.calls = append(f.calls, phones)
	return f.resp, f.err
}

// Saving an allowlist through the running server verifies that bare numbers
// are registered WhatsApp accounts; the wildcard and @-JIDs pass unchecked.
func TestUpdateSettingsChecksPresence(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	c := &Channel{store: st, log: slog.New(slog.NewTextHandler(io.Discard, nil)), settings: DefaultSettings()}
	fake := &fakePresence{resp: []types.IsOnWhatsAppResponse{
		{Query: "6281234567890", IsIn: true}, {Query: "6281299998888", IsIn: false},
	}}
	c.presenceLookup = fake

	err = c.UpdateSettings(ctx, []byte(`{"allowedNumbers":["+62 812-3456-7890","+62 812-9999-8888","1203630@g.us"]}`))
	if err == nil || !strings.Contains(err.Error(), "6281299998888") {
		t.Fatalf("presence error = %v, want 6281299998888 refused", err)
	}
	if got := c.settingsSnapshot(); len(got.AllowedNumbers) != 0 {
		t.Fatalf("settings changed after refusal: %#v", got)
	}
	if len(fake.calls) != 1 || strings.Join(fake.calls[0], ",") != "6281234567890,6281299998888" {
		t.Fatalf("presence lookups = %#v (group JID must be skipped)", fake.calls)
	}

	// A lookup failure must not block the save.
	fake.err = errors.New("whatsapp down")
	if err := c.UpdateSettings(ctx, []byte(`{"allowedNumbers":["628123"]}`)); err != nil {
		t.Fatalf("lookup failure blocked the save: %v", err)
	}
	if got := c.settingsSnapshot(); len(got.AllowedNumbers) != 1 || got.AllowedNumbers[0] != "628123" {
		t.Fatalf("save after failed lookup = %#v", got.AllowedNumbers)
	}

	// Entries without a patch skip the check entirely.
	before := len(fake.calls)
	if err := c.UpdateSettings(ctx, []byte(`{"sendTyping":false}`)); err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != before {
		t.Fatal("presence checked without an allowlist patch")
	}
}
