package whatsapp

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jahrulnr/nusamation/internal/store"
)

func TestAllowlistAndNormalization(t *testing.T) {
	s := DefaultSettings()
	if !s.allows([]string{"123@s.whatsapp.net"}, nil) {
		t.Fatal("empty allowlist should allow all")
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
