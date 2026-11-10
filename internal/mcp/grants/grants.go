// Package grants maps per-conversation bearer tokens to conversation ids.
// Tokens are issued by the desktop process, persisted so an MCP child that
// outlives a restart keeps working, and resolved by the loopback bridge.
package grants

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/jahrulnr/nusamation/internal/fsutil"
	"github.com/jahrulnr/nusamation/internal/ids"
)

// Mode selects how tokens are persisted. Raw keeps the historical
// browser_use format; Hashed stores only a digest and keeps the raw token in
// memory.
type Mode int

const (
	Raw Mode = iota
	Hashed
)

// Grants maps callback tokens to conversation ids.
type Grants struct {
	mu     sync.Mutex
	path   string
	mode   Mode
	byConv map[string]string // conversation -> raw token (Raw) or digest (Hashed)
	byTok  map[string]string // raw token (Raw) or digest (Hashed) -> conversation
	raw    map[string]string // Hashed: conversation -> raw token in memory
}

// New restores the historical raw-token file used by browser_use.
func New(path string) *Grants { return newGrants(path, Raw) }

// NewHashed stores only token digests. A token restored from a digest cannot
// be reissued; Token mints a new one when the process needs to inject it.
func NewHashed(path string) *Grants { return newGrants(path, Hashed) }

func newGrants(path string, mode Mode) *Grants {
	g := &Grants{
		path: path, mode: mode,
		byConv: map[string]string{}, byTok: map[string]string{}, raw: map[string]string{},
	}
	if path == "" {
		return g
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return g
	}
	var saved map[string]string
	if json.Unmarshal(raw, &saved) != nil {
		return g
	}
	for conv, value := range saved {
		if conv == "" || value == "" {
			continue
		}
		if mode == Raw {
			g.byConv[conv] = value
			g.byTok[value] = conv
			continue
		}
		digest := value
		if !strings.HasPrefix(digest, "sha256:") {
			// Migrate a legacy raw file in place: the raw token still resolves
			// through its digest, but is not kept in memory.
			digest = hashToken(value)
		}
		g.byConv[conv] = digest
		g.byTok[digest] = conv
	}
	return g
}

// Token returns a stable callback token for a conversation.
func (g *Grants) Token(conversationID string) string {
	if g == nil || conversationID == "" {
		return ""
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.mode == Raw {
		if tok, ok := g.byConv[conversationID]; ok {
			return tok
		}
		tok := ids.Token(32)
		g.byConv[conversationID] = tok
		g.byTok[tok] = conversationID
		g.saveLocked()
		return tok
	}
	if tok, ok := g.raw[conversationID]; ok {
		return tok
	}
	tok := ids.Token(32)
	digest := hashToken(tok)
	if old, ok := g.byConv[conversationID]; ok {
		delete(g.byTok, old)
	}
	g.raw[conversationID] = tok
	g.byConv[conversationID] = digest
	g.byTok[digest] = conversationID
	g.saveLocked()
	return tok
}

// Conversation resolves a callback token. The boolean is false for unknown
// tokens, including a raw token issued before a hashed restart.
func (g *Grants) Conversation(token string) (string, bool) {
	if g == nil || token == "" {
		return "", false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	key := token
	if g.mode == Hashed {
		key = hashToken(token)
	}
	id, ok := g.byTok[key]
	return id, ok
}

// Revoke drops the conversation's token so a deleted conversation cannot keep
// driving.
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
	delete(g.raw, conversationID)
	g.saveLocked()
}

// saveLocked persists the map. Callers must hold g.mu. A failed write only
// means tokens minted in this process die with it.
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

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "sha256:" + hex.EncodeToString(sum[:])
}
