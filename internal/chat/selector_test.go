package chat

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jahrulnr/nusamation/internal/store"
)

func selectorTestOptions() []ConfigOption {
	return ParseConfigOptions(json.RawMessage(`[
		{"id":"mode","name":"Session Mode","category":"mode","type":"select","currentValue":"ask","options":[{"value":"ask","name":"Ask"},{"value":"bypass","name":"Bypass"}]},
		{"id":"model","name":"Model","category":"model","type":"select","currentValue":"fake-fast","options":[
			{"value":"fake-fast","name":"Fake Fast"},
			{"value":"fake-smart","name":"Fake Smart"},
			{"value":"deepseek/deepseek-v4.1-flash","name":"DeepSeek Flash"},
			{"value":"other/a-flash","name":"Other Flash"},
			{"value":"claude-opus-4.5","name":"Opus"},
			{"value":"swe-2","name":"SWE 2"},
			{"value":"weird[bracket]","name":"Bracket"}
		]},
		{"id":"effort","name":"Effort","category":"thought_level","type":"select","currentValue":"low","options":[{"value":"low"},{"value":"medium"},{"value":"high"}]},
		{"id":"fast","name":"Fast","category":"speed","type":"boolean","currentValue":false}
	]`))
}

func TestParseSelector(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		model string
		mods  []Modifier
		err   bool
	}{
		{name: "plain", in: "fake-smart", model: "fake-smart"},
		{name: "slashes and dots are data", in: "deepseek/deepseek-v4.1-flash", model: "deepseek/deepseek-v4.1-flash"},
		{name: "colons are data", in: "provider:model-v2", model: "provider:model-v2"},
		{name: "one modifier", in: "swe-2[effort=max]", model: "swe-2", mods: []Modifier{{Key: "effort", Value: "max"}}},
		{name: "two modifiers", in: "swe-2[effort=max,provider=devin]", model: "swe-2", mods: []Modifier{{Key: "effort", Value: "max"}, {Key: "provider", Value: "devin"}}},
		{name: "spaces around separators", in: "swe-2[ effort = max , provider = devin ]", model: "swe-2", mods: []Modifier{{Key: "effort", Value: "max"}, {Key: "provider", Value: "devin"}}},
		{name: "empty bracket", in: "swe-2[]", model: "swe-2"},
		{name: "trailing text after bracket is not a selector", in: "swe-2[effort=max] extra", model: "swe-2[effort=max] extra"},
		{name: "missing equals", in: "swe-2[effort]", err: true},
		{name: "empty key", in: "swe-2[=max]", err: true},
		{name: "open bracket only", in: "swe-2[effort=max", model: "swe-2[effort=max"},
		{name: "whitespace", in: "  fake-smart  ", model: "fake-smart"},
		{name: "empty", in: "", err: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			model, mods, err := ParseSelector(c.in)
			if c.err {
				if err == nil {
					t.Fatalf("ParseSelector(%q) expected error, got model=%q mods=%#v", c.in, model, mods)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseSelector(%q) error: %v", c.in, err)
			}
			if model != c.model {
				t.Fatalf("ParseSelector(%q) model = %q, want %q", c.in, model, c.model)
			}
			if len(mods) != len(c.mods) {
				t.Fatalf("ParseSelector(%q) mods = %#v, want %#v", c.in, mods, c.mods)
			}
			for i := range mods {
				if mods[i] != c.mods[i] {
					t.Fatalf("ParseSelector(%q) mod[%d] = %#v, want %#v", c.in, i, mods[i], c.mods[i])
				}
			}
		})
	}
}

func TestResolveModel(t *testing.T) {
	opts := selectorTestOptions()
	t.Run("exact with separators", func(t *testing.T) {
		c, err := ResolveModel(opts, "deepseek/deepseek-v4.1-flash")
		if err != nil || c.Value != "deepseek/deepseek-v4.1-flash" {
			t.Fatalf("exact match failed: %#v %v", c, err)
		}
	})
	t.Run("verbatim value containing bracket wins", func(t *testing.T) {
		c, err := ResolveModel(opts, "weird[bracket]")
		if err != nil || c.Value != "weird[bracket]" {
			t.Fatalf("bracket value should match verbatim: %#v %v", c, err)
		}
	})
	t.Run("prefix", func(t *testing.T) {
		c, err := ResolveModel(opts, "fake-sma")
		if err != nil || c.Value != "fake-smart" {
			t.Fatalf("prefix match failed: %#v %v", c, err)
		}
	})
	t.Run("segment", func(t *testing.T) {
		c, err := ResolveModel(opts, "opus")
		if err != nil || c.Value != "claude-opus-4.5" {
			t.Fatalf("segment match failed: %#v %v", c, err)
		}
	})
	t.Run("ambiguous segment reports candidates", func(t *testing.T) {
		_, err := ResolveModel(opts, "flash")
		var amb *AmbiguousError
		if !errors.As(err, &amb) {
			t.Fatalf("expected AmbiguousError, got %v", err)
		}
		if len(amb.Candidates) != 2 {
			t.Fatalf("expected 2 candidates, got %#v", amb.Candidates)
		}
	})
	t.Run("no match", func(t *testing.T) {
		if _, err := ResolveModel(opts, "nonexistent"); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("case-insensitive exact loses to exact", func(t *testing.T) {
		mixed := ParseConfigOptions(json.RawMessage(`[{"id":"model","category":"model","type":"select","options":[{"value":"Flash"},{"value":"flash"}]}]`))
		c, err := ResolveModel(mixed, "Flash")
		if err != nil || c.Value != "Flash" {
			t.Fatalf("exact-case should win: %#v %v", c, err)
		}
		c, err = ResolveModel(mixed, "FLASH")
		if err != nil || (c.Value != "Flash" && c.Value != "flash") {
			t.Fatalf("ci match failed: %#v %v", c, err)
		}
	})
	t.Run("no model option", func(t *testing.T) {
		none := ParseConfigOptions(json.RawMessage(`[{"id":"effort","category":"thought_level","type":"select","options":[{"value":"low"}]}]`))
		if _, err := ResolveModel(none, "low"); err == nil {
			t.Fatal("expected a clear error when no model option exists")
		}
	})
}

func TestResolveModifierKey(t *testing.T) {
	opts := selectorTestOptions()
	for _, key := range []string{"effort", "Effort", "EFFORT"} {
		if _, ok := ResolveModifierKey(opts, key); !ok {
			t.Fatalf("id lookup failed for %q", key)
		}
	}
	if opt, ok := ResolveModifierKey(opts, "thought_level"); !ok || opt.ID != "effort" {
		t.Fatalf("category lookup failed: %#v %v", opt, ok)
	}
	if opt, ok := ResolveModifierKey(opts, "effort"); !ok || opt.ID != "effort" {
		t.Fatalf("id lookup: %#v %v", opt, ok)
	}
	if opt, ok := ResolveModifierKey(opts, "Session Mode"); !ok || opt.ID != "mode" {
		t.Fatalf("name lookup failed: %#v %v", opt, ok)
	}
	if _, ok := ResolveModifierKey(opts, "nope"); ok {
		t.Fatal("unknown key resolved")
	}
	// ID wins over category: an option whose id is "mode" resolves before the
	// category scan, even when another option shares the category name.
	if opt, ok := ResolveModifierKey(opts, "mode"); !ok || opt.ID != "mode" {
		t.Fatalf("id should win: %#v %v", opt, ok)
	}
}

func TestResolveModifierValue(t *testing.T) {
	opts := selectorTestOptions()
	effort, _ := ResolveModifierKey(opts, "effort")
	if v, offers := ResolveModifierValue(effort, "high"); v != "high" || offers != nil {
		t.Fatalf("exact value: %q %#v", v, offers)
	}
	if v, _ := ResolveModifierValue(effort, "HIGH"); v != "high" {
		t.Fatalf("ci value should return canonical case: %q", v)
	}
	v, offers := ResolveModifierValue(effort, "max")
	if v != "" || strings.Join(offers, ",") != "low,medium,high" {
		t.Fatalf("unknown value should list offers: %q %#v", v, offers)
	}
	fast, _ := ResolveModifierKey(opts, "fast")
	if v, _ := ResolveModifierValue(fast, "TRUE"); v != "true" {
		t.Fatalf("boolean ci: %q", v)
	}
	if v, offers := ResolveModifierValue(fast, "on"); v != "" || len(offers) != 2 {
		t.Fatalf("boolean invalid should offer true/false: %q %#v", v, offers)
	}
	// Nested options are valid modifier values.
	nested := ParseConfigOptions(json.RawMessage(`[{"id":"model","category":"model","type":"select","options":[{"value":"group","options":[{"value":"inner-1"},{"value":"inner-2"}]}]}]`))[0]
	if v, _ := ResolveModifierValue(nested, "inner-2"); v != "inner-2" {
		t.Fatalf("nested value: %q", v)
	}
}

func TestEchoSelector(t *testing.T) {
	opts := selectorTestOptions()
	echo := EchoSelector(store.Conversation{Model: "fake-smart"}, opts)
	if echo != "fake-smart" {
		t.Fatalf("EchoSelector = %q", echo)
	}
	echo = EchoSelector(store.Conversation{}, opts)
	if echo != "fake-fast" {
		t.Fatalf("EchoSelector should read the model option current value, got %q", echo)
	}
	if strings.Contains(echo, "effort") || strings.Contains(echo, "[") {
		t.Fatalf("EchoSelector must print the model value only: %q", echo)
	}
}
