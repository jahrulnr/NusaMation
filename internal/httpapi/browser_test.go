package httpapi

import "testing"

func TestNormalizeBrowserURL(t *testing.T) {
	cases := []struct {
		in       string
		want     string
		ok       bool
		implicit bool
	}{
		{"", "about:blank", true, false},
		{"  ", "about:blank", true, false},
		{"example.com", "https://example.com", true, true},
		{" example.com ", "https://example.com", true, true},
		{"localhost:8787", "https://localhost:8787", true, true},
		{"192.168.1.5:3000/x", "https://192.168.1.5:3000/x", true, true},
		{"http://a.test/x", "http://a.test/x", true, false},
		{"https://a.test", "https://a.test", true, false},
		{"about:blank", "about:blank", true, false},
		{"data:text/html,<h1>x</h1>", "data:text/html,<h1>x</h1>", true, false},
		{"file:///etc/passwd", "file:///etc/passwd", true, false},
		{"http://", "", false, false},
		{"https://", "", false, false},
		{"about:", "", false, false},
		{"file://", "", false, false},
		{"javascript:alert(1)", "https://www.google.com/search?q=javascript%3Aalert%281%29", true, false},
		{"ftp://a.test", "https://www.google.com/search?q=ftp%3A%2F%2Fa.test", true, false},
		{"//host-only", "https://www.google.com/search?q=%2F%2Fhost-only", true, false},
		{"just text no dots", "https://www.google.com/search?q=just+text+no+dots", true, false},
		{"myhost", "https://www.google.com/search?q=myhost", true, false},
		{"nas:5000/share", "https://nas:5000/share", true, true},
	}
	for _, c := range cases {
		got, implicit, ok := normalizeBrowserURL(c.in)
		if ok != c.ok {
			t.Errorf("normalizeBrowserURL(%q) ok=%v, want %v (got %q)", c.in, ok, c.ok, got)
			continue
		}
		if ok && got != c.want {
			t.Errorf("normalizeBrowserURL(%q) = %q, want %q", c.in, got, c.want)
		}
		if implicit != c.implicit {
			t.Errorf("normalizeBrowserURL(%q) implicit=%v, want %v", c.in, implicit, c.implicit)
		}
	}
}
