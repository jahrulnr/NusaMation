package chat

import (
	"encoding/json"
	"testing"
)

func TestValidateElicitation(t *testing.T) {
	schema := json.RawMessage(`{"mode":"form","requestedSchema":{"type":"object","required":["trust","port"],"properties":{"trust":{"type":"boolean"},"port":{"type":"integer","minimum":1,"maximum":65535},"strategy":{"type":"string","oneOf":[{"const":"safe"},{"const":"full"}]},"tags":{"type":"array","items":{"enum":["a","b"]},"maxItems":2}}}}`)
	cases := []struct {
		name    string
		content map[string]any
		wantErr bool
	}{
		{"valid", map[string]any{"trust": true, "port": float64(8080), "strategy": "safe", "tags": []any{"a"}}, false},
		{"required", map[string]any{"trust": true}, true},
		{"invalid choice", map[string]any{"trust": true, "port": float64(80), "strategy": "missing"}, true},
		{"invalid number", map[string]any{"trust": true, "port": float64(2.5)}, true},
		{"invalid array", map[string]any{"trust": true, "port": float64(80), "tags": []any{"x"}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateElicitation(schema, tc.content); (err != nil) != tc.wantErr {
				t.Fatalf("validate = %v", err)
			}
		})
	}
}

func TestGroupedConfigChoices(t *testing.T) {
	opts := ParseConfigOptions(json.RawMessage(`[{"id":"model","category":"model","type":"select","currentValue":"m1","options":[{"group":"provider","name":"Provider","options":[{"value":"m1","name":"One"},{"value":"m2","name":"Two"}]}]}]`))
	if len(opts) != 1 || len(opts[0].Options) != 1 || len(opts[0].Options[0].Options) != 2 {
		t.Fatalf("grouped choices lost: %#v", opts)
	}
	if err := configValueAllowed(opts[0], "m2"); err != nil {
		t.Fatal(err)
	}
	if err := configValueAllowed(opts[0], "missing"); err == nil {
		t.Fatal("accepted an unknown choice")
	}
}
