package telegram

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// maxCaptionUnits is Telegram's caption cap for every media method.
const maxCaptionUnits = 1024

// allowedHTMLTags is exactly what Telegram's HTML parse mode renders. Heading,
// table and footnote tags are deliberately absent: Telegram rejects them and a
// parse failure loses the whole message.
var allowedHTMLTags = map[string]bool{
	"b": true, "i": true, "u": true, "s": true,
	"code": true, "pre": true, "blockquote": true, "a": true,
}

// sanitizeHTML turns free text into a string Telegram's HTML parse mode
// accepts without losing anything:
//
//   - & < > in text become entities,
//   - only allowedHTMLTags survive, with matching closers,
//   - inside <code>/<pre> every other tag is literal text,
//   - unknown or mismatched tags are written as escaped text,
//   - tags still open at the end are closed.
//
// The chat layer delivers agent text that also body-renders on the web, so a
// malformed tag must never cost the whole message.
func sanitizeHTML(text string) string {
	var b strings.Builder
	b.Grow(len(text) + 16)
	stack := make([]string, 0, 4)
	for i := 0; i < len(text); {
		if text[i] != '<' {
			next := strings.IndexByte(text[i:], '<')
			if next < 0 {
				writeEscaped(&b, text[i:])
				break
			}
			writeEscaped(&b, text[i:i+next])
			i += next
			continue
		}
		rel := strings.IndexByte(text[i:], '>')
		if rel < 0 {
			writeEscaped(&b, text[i:])
			break
		}
		end := i + rel
		raw := text[i : end+1]
		name, rest, closing, ok := parseHTMLTag(text[i+1 : end])
		if !ok {
			// Not tag-shaped at all (e.g. "a < b"): only the bracket is
			// text, so a later real tag is still recognized.
			writeEscaped(&b, "<")
			i++
			continue
		}
		if !allowedHTMLTags[name] {
			writeEscaped(&b, raw)
			i = end + 1
			continue
		}
		// Inside code/pre only that block's own closer is markup. A nested
		// tag is content the user wrote, not formatting.
		if top := topTag(stack); top == "code" || top == "pre" {
			if closing && name == top {
				b.WriteString("</" + name + ">")
				stack = stack[:len(stack)-1]
			} else {
				writeEscaped(&b, raw)
			}
			i = end + 1
			continue
		}
		if closing {
			if len(stack) == 0 || stack[len(stack)-1] != name {
				writeEscaped(&b, raw)
			} else {
				b.WriteString("</" + name + ">")
				stack = stack[:len(stack)-1]
			}
			i = end + 1
			continue
		}
		if name == "a" {
			href, valid := anchorHref(rest)
			if !valid {
				writeEscaped(&b, raw)
				i = end + 1
				continue
			}
			b.WriteString(`<a href="`)
			writeAttrEscaped(&b, href)
			b.WriteString(`">`)
		} else if rest != "" {
			// Attributes are not part of the allowed set for these tags, so
			// the whole thing is text the agent wrote.
			writeEscaped(&b, raw)
			i = end + 1
			continue
		} else {
			b.WriteString("<" + name + ">")
		}
		stack = append(stack, name)
		i = end + 1
	}
	for j := len(stack) - 1; j >= 0; j-- {
		b.WriteString("</" + stack[j] + ">")
	}
	return b.String()
}

// truncateUTF16 cuts s to at most maxUnits UTF-16 code units, never splitting
// a rune. Telegram counts message and caption length in UTF-16, so a rune
// count lets astral emoji (two units each) exceed the limit after the source
// text passed the rune cap.
func truncateUTF16(s string, maxUnits int) string {
	if maxUnits <= 0 || utf16Units(s) <= maxUnits {
		return s
	}
	units := 0
	for i, r := range s {
		n := utf16.RuneLen(r)
		if n < 1 {
			n = 1
		}
		if units+n > maxUnits {
			return s[:i]
		}
		units += n
	}
	return s
}

func utf16Units(s string) int {
	n := 0
	for _, r := range s {
		u := utf16.RuneLen(r)
		if u < 1 {
			u = 1
		}
		n += u
	}
	return n
}

// truncateHTML clips already-sanitized HTML to max UTF-16 code units of text
// and closes whatever tags were still open at the cut, so a long caption
// cannot end in a dangling tag Telegram refuses.
func truncateHTML(s string, max int) string {
	if max <= 0 || utf16Units(s) <= max {
		return s
	}
	var b strings.Builder
	stack := make([]string, 0, 4)
	count := 0
	for i := 0; i < len(s); {
		if s[i] == '<' {
			rel := strings.IndexByte(s[i:], '>')
			if rel < 0 {
				break
			}
			end := i + rel
			raw := s[i : end+1]
			if strings.HasPrefix(raw, "</") {
				if len(stack) > 0 {
					stack = stack[:len(stack)-1]
				}
			} else {
				name := strings.TrimSuffix(strings.TrimPrefix(raw, "<"), ">")
				if k := strings.IndexAny(name, " \t\n"); k >= 0 {
					name = name[:k]
				}
				stack = append(stack, name)
			}
			b.WriteString(raw)
			i = end + 1
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		units := utf16.RuneLen(r)
		if units < 1 {
			units = 1
		}
		if count+units > max {
			break
		}
		b.WriteRune(r)
		count += units
		i += size
	}
	for j := len(stack) - 1; j >= 0; j-- {
		b.WriteString("</" + stack[j] + ">")
	}
	return b.String()
}

// parseHTMLTag splits the inside of <...> into a lowercase tag name and the
// trimmed remainder. A name that is not a plain short word is not a tag.
func parseHTMLTag(inner string) (name, rest string, closing, ok bool) {
	s := strings.TrimSpace(inner)
	if s == "" {
		return "", "", false, false
	}
	if strings.HasPrefix(s, "/") {
		name = strings.ToLower(strings.TrimSpace(s[1:]))
		if !validHTMLTagName(name) {
			return "", "", true, false
		}
		return name, "", true, true
	}
	if i := strings.IndexAny(s, " \t\n"); i >= 0 {
		name, rest = s[:i], strings.TrimSpace(s[i:])
	} else {
		name = s
	}
	name = strings.ToLower(name)
	if !validHTMLTagName(name) {
		return "", "", false, false
	}
	return name, rest, false, true
}

func validHTMLTagName(name string) bool {
	if name == "" || len(name) > 12 {
		return false
	}
	for _, r := range name {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return true
}

// anchorHref accepts only href="http…" — double quotes, no whitespace in the
// URL, http or https. Anything else means the tag is shown as text.
func anchorHref(rest string) (string, bool) {
	const prefix = `href="`
	if !strings.HasPrefix(rest, prefix) || !strings.HasSuffix(rest, `"`) || len(rest) <= len(prefix)+1 {
		return "", false
	}
	href := rest[len(prefix) : len(rest)-1]
	if strings.ContainsAny(href, "\"'<> \t\r\n") {
		return "", false
	}
	lower := strings.ToLower(href)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return "", false
	}
	return href, true
}

func topTag(stack []string) string {
	if len(stack) == 0 {
		return ""
	}
	return stack[len(stack)-1]
}

func writeEscaped(b *strings.Builder, s string) {
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		default:
			b.WriteRune(r)
		}
	}
}

func writeAttrEscaped(b *strings.Builder, s string) {
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		default:
			b.WriteRune(r)
		}
	}
}
