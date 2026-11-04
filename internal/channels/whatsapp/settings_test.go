package whatsapp

import (
	"strings"
	"testing"
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

func TestResponseRulesCoversWhatsAppSurface(t *testing.T) {
	rules := (&Channel{}).ResponseRules()
	for _, marker := range []string{"*bold*", "_italic_", "~strikethrough~", "30,000", "attachment"} {
		if !strings.Contains(rules, marker) {
			t.Fatalf("rules %q missing %q", rules, marker)
		}
	}
}
