package telegram

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jahrulnr/nusamation/internal/channels"
	appstore "github.com/jahrulnr/nusamation/internal/store"
)

// Inline candidates mirror the web composer's mentionChoices: rooms first
// (by label prefix or title substring), then projects (name substring or
// slug prefix). Telegram comfortably shows ~20 results; the split keeps a
// few projects reachable even when many rooms match.
const (
	inlineRoomCap    = 15
	inlineResultCap  = 20
	inlineAPITimeout = 8 * time.Second
)

// mentionTokenRe finds "@handle" candidates in a message sent via the bot —
// the pattern covers room labels, project slugs, and Telegram usernames;
// only tokens that resolve to a real room or project become references.
var mentionTokenRe = regexp.MustCompile(`@[A-Za-z0-9][A-Za-z0-9_-]*`)

// slugSpaceRe mirrors the web composer's slugification: lowercase, then one
// hyphen per whitespace run (no trim).
var slugSpaceRe = regexp.MustCompile(`\s+`)

func projectSlug(name string) string {
	return slugSpaceRe.ReplaceAllString(strings.ToLower(name), "-")
}

// splitInlineQuery separates the first token (the candidate filter) from
// the rest. The remainder keeps its original whitespace so the inserted
// message preserves the user's spacing: "cat  look" → "@cat  look".
func splitInlineQuery(query string) (token, rest string) {
	q := strings.TrimLeft(query, " ")
	if i := strings.IndexFunc(q, unicode.IsSpace); i >= 0 {
		return q[:i], q[i:]
	}
	return q, ""
}

// handleInlineQuery answers one inline_query. The query object carries no
// destination chat, so room candidates cannot exclude a "current" room the
// way the web composer does. An unallowed sender still gets an answer — an
// empty one — so their client stops spinning instead of waiting forever.
func (c *Channel) handleInlineQuery(ctx context.Context, q *tgInlineQuery) {
	if q.From == nil {
		return
	}
	var results []inlineResult
	s := c.settingsSnapshot()
	if s.Enabled && s.allows(strconv.FormatInt(q.From.ID, 10), "") {
		results = c.inlineResults(q.Query)
	}
	client := c.clientCopy()
	if client == nil {
		return
	}
	rctx, cancel := context.WithTimeout(ctx, inlineAPITimeout)
	defer cancel()
	if err := client.answerInlineQuery(rctx, q.ID, results); err != nil {
		c.log.Warn("answerInlineQuery failed", "error", err)
	}
}

// inlineResults builds article results: tapping one inserts
// "@handle<rest>" as the user's message, which arrives back through the bot
// as a via_bot message and resolves into a mention reference there.
func (c *Channel) inlineResults(query string) []inlineResult {
	token, rest := splitInlineQuery(query)
	q := strings.ToLower(token)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var out []inlineResult
	convs, err := c.store.ListConversations(ctx, appstore.ConversationFilter{Archived: false, Limit: 200})
	if err == nil {
		for _, conv := range convs {
			if conv.Label == "" {
				continue
			}
			if q != "" && !strings.HasPrefix(strings.ToLower(conv.Label), q) &&
				!strings.Contains(strings.ToLower(conv.Title), q) {
				continue
			}
			out = append(out, articleResult(conv.ID, conv.Label, conv.Title, "@"+conv.Label+rest))
			if len(out) >= inlineRoomCap {
				break
			}
		}
	}
	if projects, err := c.store.ListProjects(ctx); err == nil {
		for _, p := range projects {
			if len(out) >= inlineResultCap {
				break
			}
			if p.Name == "" {
				continue
			}
			slug := projectSlug(p.Name)
			if q != "" && !strings.Contains(strings.ToLower(p.Name), q) && !strings.HasPrefix(slug, q) {
				continue
			}
			out = append(out, articleResult(p.ID, slug, p.Name, "@"+slug+rest))
		}
	}
	return out
}

func articleResult(id, title, description, messageText string) inlineResult {
	return inlineResult{
		Type: "article", ID: id, Title: title, Description: description,
		InputMessageContent: map[string]any{"message_text": messageText},
	}
}

// inlinePickReferences resolves the "@token"s in a message sent via this
// bot's inline mode into channel references. Room labels win over project
// slugs; the bot's own @username and tokens that resolve to nothing are
// skipped, so a pick's leftover text can still mention real people. Only
// via_bot messages reach here — an ordinary "@cat" stays plain text, same
// as the web composer that never opened the picker.
func (c *Channel) inlinePickReferences(m *tgMessage, text string) []channels.Reference {
	if m.ViaBot == nil {
		return nil
	}
	c.mu.Lock()
	me := c.me
	c.mu.Unlock()
	if me == nil || me.Username == "" || !strings.EqualFold(m.ViaBot.Username, me.Username) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var refs []channels.Reference
	for _, loc := range mentionTokenRe.FindAllStringIndex(text, -1) {
		token := strings.ToLower(text[loc[0]+1 : loc[1]])
		if token == strings.ToLower(me.Username) {
			continue
		}
		ref := channels.Reference{Label: text[loc[0]:loc[1]], Start: loc[0], End: loc[1]}
		if conv, err := c.store.FindConversationByLabel(ctx, token); err == nil && !conv.Archived {
			refs = append(refs, channels.Reference{Kind: "conversation", ID: conv.ID,
				Label: ref.Label, Start: ref.Start, End: ref.End})
			continue
		}
		if id, ok := c.projectBySlug(ctx, token); ok {
			refs = append(refs, channels.Reference{Kind: "project", ID: id,
				Label: ref.Label, Start: ref.Start, End: ref.End})
		}
	}
	return refs
}

// projectBySlug matches a token to a project by slugified name — the same
// handle the web composer inserts for project mentions.
func (c *Channel) projectBySlug(ctx context.Context, slug string) (string, bool) {
	projects, err := c.store.ListProjects(ctx)
	if err != nil {
		return "", false
	}
	for _, p := range projects {
		if p.Name != "" && projectSlug(p.Name) == slug {
			return p.ID, true
		}
	}
	return "", false
}
