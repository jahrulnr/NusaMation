package telegram

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeHTML(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"allowed tag", "<b>ok</b>", "<b>ok</b>"},
		{"plain text untouched", "hello", "hello"},
		{"script escaped", "<script>alert(1)</script>", "&lt;script&gt;alert(1)&lt;/script&gt;"},
		{"angle bracket in text", "a < b", "a &lt; b"},
		{"ampersand escaped", "a & b", "a &amp; b"},
		{"tag inside code is text", "<code><b>x</b></code>", "<code>&lt;b&gt;x&lt;/b&gt;</code>"},
		{"tag inside pre is text", "<pre>if a <b> b</pre>", "<pre>if a &lt;b&gt; b</pre>"},
		{"malformed tag keeps a real closer", "<pre>if a < b</pre>", "<pre>if a &lt; b</pre>"},
		{"open tag closed", "<b>open", "<b>open</b>"},
		{"mismatched closer is text", "<i>a</b>", "<i>a&lt;/b&gt;</i>"},
		{"stray closer is text", "a</b>", "a&lt;/b&gt;"},
		{"attributes not allowed", `<b class="x">hi</b>`, `&lt;b class="x"&gt;hi&lt;/b&gt;`},
		{"anchor kept", `<a href="https://example.com/a?b=1&c=2">l</a>`, `<a href="https://example.com/a?b=1&amp;c=2">l</a>`},
		{"non-http anchor is text", `<a href="javascript:alert(1)">l</a>`, `&lt;a href="javascript:alert(1)"&gt;l&lt;/a&gt;`},
		{"quoted url invalid", `<a href="http://x y">l</a>`, `&lt;a href="http://x y"&gt;l&lt;/a&gt;`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeHTML(tc.in); got != tc.want {
				t.Fatalf("sanitizeHTML(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestTruncateHTMLClosesTagsAtTheCut(t *testing.T) {
	got := truncateHTML(sanitizeHTML("<b>"+strings.Repeat("x", 1100)+"</b>"), maxCaptionUnits)
	if !strings.HasSuffix(got, "</b>") {
		t.Fatalf("cut caption lost its closing tag: %q", got[max(0, len(got)-20):])
	}
	if utf8.RuneCountInString(got) > maxCaptionUnits+len("<b></b>") {
		t.Fatalf("cut caption is %d runes", utf8.RuneCountInString(got))
	}
}

func TestResponseRulesCoversTelegramSurface(t *testing.T) {
	rules := (&Channel{}).ResponseRules()
	for _, marker := range []string{"<b>", "<code>", "<blockquote>", "<a href=", "4,096", "counts an emoji", "attachment"} {
		if !strings.Contains(rules, marker) {
			t.Fatalf("rules %q missing %q", rules, marker)
		}
	}
	if strings.Contains(rules, "Plain text only") {
		t.Fatalf("rules still promise plain text: %q", rules)
	}
}
