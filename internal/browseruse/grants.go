// Package browseruse is the stdio MCP server named browser_use.
// The desktop process issues a token per conversation; the agent spawns this
// process, and every tool call posts back to that process over loopback.
package browseruse

import (
	"sync"

	"github.com/jahrulnr/nusamation/internal/ids"
)

// Grants maps callback tokens to conversation ids. A conversation keeps the
// same token for the life of the process so a restarted MCP child still works.
type Grants struct {
	mu     sync.Mutex
	byConv map[string]string
	byTok  map[string]string
}

func NewGrants() *Grants {
	return &Grants{byConv: map[string]string{}, byTok: map[string]string{}}
}

// Token returns the stable callback token for a conversation.
func (g *Grants) Token(conversationID string) string {
	if g == nil || conversationID == "" {
		return ""
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if tok, ok := g.byConv[conversationID]; ok {
		return tok
	}
	tok := ids.Token(32)
	g.byConv[conversationID] = tok
	g.byTok[tok] = conversationID
	return tok
}

// Revoke drops the conversation's token so a deleted conversation cannot keep driving.
func (g *Grants) Revoke(conversationID string) {
	if g == nil || conversationID == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	tok, ok := g.byConv[conversationID]
	if !ok {
		return
	}
	delete(g.byConv, conversationID)
	delete(g.byTok, tok)
}

// Conversation resolves a callback token. The boolean is false for unknown tokens.
func (g *Grants) Conversation(token string) (string, bool) {
	if g == nil || token == "" {
		return "", false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	id, ok := g.byTok[token]
	return id, ok
}
