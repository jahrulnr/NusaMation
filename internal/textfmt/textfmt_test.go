package textfmt

import (
	"strings"
	"testing"
)

func TestChatMarkdown(t *testing.T) {
	in := "## Heading\n\n**bold** and __italic__ and [link](https://example.com)\n\n" + "\x1b[31mred\x1b[0m"
	out := ChatMarkdown(in)
	if !strings.Contains(out, "*Heading*") || !strings.Contains(out, "*bold*") || !strings.Contains(out, "_italic_") {
		t.Fatalf("markdown conversion = %q", out)
	}
	if !strings.Contains(out, "link (https://example.com)") || strings.Contains(out, "\x1b") {
		t.Fatalf("link/ansi conversion = %q", out)
	}
}

func TestTruncateUTF8(t *testing.T) {
	s := strings.Repeat("é", 10)
	out := Truncate(s, 11)
	if len(out) > 11 || !strings.HasSuffix(out, "…") {
		t.Fatalf("truncate = %q (%d bytes)", out, len(out))
	}
}
