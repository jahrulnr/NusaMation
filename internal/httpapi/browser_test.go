package httpapi

import "testing"

func TestNormalizeBrowserURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"", "about:blank", true},
		{"  ", "about:blank", true},
		{"example.com", "https://example.com", true},
		{" example.com ", "https://example.com", true},
		{"http://a.test/x", "http://a.test/x", true},
		{"https://a.test", "https://a.test", true},
		{"about:blank", "about:blank", true},
		{"data:text/html,<h1>x</h1>", "data:text/html,<h1>x</h1>", true},
		{"http://", "", false},
		{"https://", "", false},
		{"about:", "", false},
		{"javascript:alert(1)", "", false},
		{"file:///etc/passwd", "", false},
		{"ftp://a.test", "", false},
		{"//host-only", "", false},
		{"just text no dots", "https://just text no dots", false},
	}
	for _, c := range cases {
		got, ok := normalizeBrowserURL(c.in)
		if ok != c.ok {
			t.Errorf("normalizeBrowserURL(%q) ok=%v, want %v (got %q)", c.in, ok, c.ok, got)
			continue
		}
		if ok && got != c.want {
			t.Errorf("normalizeBrowserURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
