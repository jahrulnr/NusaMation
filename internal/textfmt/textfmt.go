// Package textfmt contains small, dependency-free helpers for turning agent
// markdown into things that chat channels (especially WhatsApp) render well.
package textfmt

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	ansiRe    = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	linkRe    = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
	boldStar  = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)
	boldUnder = regexp.MustCompile(`__([^_\n]+)__`)
	headRe    = regexp.MustCompile(`(?m)^#{1,6}\s+(.+)$`)
)

func StripANSI(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

func Truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	const ellipsis = "…"
	if max <= len(ellipsis) {
		return s[:max]
	}
	cut := max - len(ellipsis)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimRight(s[:cut], " \n\t") + ellipsis
}

// ChatMarkdown converts common markdown constructs into WhatsApp-friendly
// text. It deliberately keeps fenced code blocks and lists intact.
func ChatMarkdown(s string) string {
	s = StripANSI(s)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = boldStar.ReplaceAllString(s, "*$1*")
	s = boldUnder.ReplaceAllString(s, "_${1}_")
	s = linkRe.ReplaceAllString(s, "$1 ($2)")
	s = headRe.ReplaceAllString(s, "*$1*")
	// Collapse the excessive blank lines agents like to emit.
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(s)
}

// OneLine collapses whitespace so it can be used in a notification title.
func OneLine(s string, max int) string {
	s = strings.Join(strings.Fields(StripANSI(s)), " ")
	return Truncate(s, max)
}
