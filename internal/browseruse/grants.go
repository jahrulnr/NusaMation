// Package browseruse is the stdio MCP server named browser_use.
// The desktop process issues a token per conversation; the agent spawns this
// process, and every tool call posts back to that process over loopback.
package browseruse

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/jahrulnr/nusamation/internal/fsutil"
	"github.com/jahrulnr/nusamation/internal/ids"
)

// Grants maps callback tokens to conversation ids. Tokens are persisted to a
// JSON file so a MCP child spawned before a server restart keeps working —
// its environment still carries the token it was launched with.
type Grants struct {
	mu     sync.Mutex
	path   string
	byConv map[string]string
	byTok  map[string]string
}

// NewGrants restores previously issued tokens from path. An empty path keeps
// tokens in memory only (tests).
func NewGrants(path string) *Grants {
	g := &Grants{path: path, byConv: map[string]string{}, byTok: map[string]string{}}
	if path != "" {
		if raw, err := os.ReadFile(path); err == nil {
			var saved map[string]string
			if json.Unmarshal(raw, &saved) == nil {
				for conv, tok := range saved {
					if conv == "" || tok == "" {
						continue
					}
					g.byConv[conv] = tok
					g.byTok[tok] = conv
				}
			}
		}
	}
	return g
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
	g.saveLocked()
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
	g.saveLocked()
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

// saveLocked persists the map. Callers must hold g.mu. A failed write only
// means tokens minted in this process die with it — same as before.
func (g *Grants) saveLocked() {
	if g.path == "" {
		return
	}
	data, err := json.Marshal(g.byConv)
	if err != nil {
		return
	}
	dir, name := filepath.Dir(g.path), filepath.Base(g.path)
	_ = fsutil.WriteFileAtomic(dir, name, data, 0o600)
}
