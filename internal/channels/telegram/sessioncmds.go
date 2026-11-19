package telegram

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jahrulnr/nusamation/internal/channels"
	"github.com/jahrulnr/nusamation/internal/textfmt"
)

// sessionCmdRe is Telegram's command-name grammar — names that fall outside
// it (uppercase, hyphens, spaces) cannot be published at all, so they are
// skipped rather than repaired.
var sessionCmdRe = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)

// menuFingerprint identifies the outcome last published for a chat: the
// marshalled command list, or "-" once the chat-scoped menu was deleted.
func menuFingerprint(cmds []botCommand) string {
	if cmds == nil {
		return "-"
	}
	b, _ := json.Marshal(cmds)
	return string(b)
}

// sessionMenu builds the chat-scoped list: the 12 stable commands first,
// then session commands whose names pass the grammar and do not collide
// with the stable set (or each other). Empty descriptions default to the
// command name; Telegram requires 3–256 character descriptions, so an entry
// that cannot meet the minimum is skipped rather than failing the whole
// setMyCommands call. The list stops at 100. nil means nothing extra survived
// — the caller publishes a delete instead so the chat falls back to the
// default menu.
func sessionMenu(cmds []channels.SessionCommand) []botCommand {
	seen := make(map[string]bool, len(menuCommands))
	out := make([]botCommand, 0, len(menuCommands)+len(cmds))
	for _, bc := range menuCommands {
		seen[bc.Command] = true
		out = append(out, bc)
	}
	for _, sc := range cmds {
		if len(out) >= 100 {
			break
		}
		if !sessionCmdRe.MatchString(sc.Name) || seen[sc.Name] {
			continue
		}
		desc := strings.TrimSpace(sc.Description)
		if desc == "" {
			desc = sc.Name
		}
		if utf8.RuneCountInString(desc) < 3 {
			continue
		}
		seen[sc.Name] = true
		out = append(out, botCommand{Command: sc.Name, Description: textfmt.TruncateRunes(desc, 256)})
	}
	if len(out) == len(menuCommands) {
		return nil
	}
	return out
}

// resetMenuFingerprints forgets what each chat's scoped menu last contained.
// A logout or a new bot token means those menus belong to the old bot, so the
// next sync must publish instead of trusting a stale fingerprint.
func (c *Channel) resetMenuFingerprints() {
	c.cmdMu.Lock()
	c.lastCmds = map[string]string{}
	c.cmdMu.Unlock()
}

// SyncSessionCommands publishes the session's advertised commands into the
// chat's "/" menu. It is deliberately quiet: a disconnected or tokenless
// adapter and a failed API call both resolve to nil — command metadata is
// cosmetic next to the turn itself, and the ACP reader must never stall on
// api.telegram.org. cmdMu serialises publishes; lastCmds dedupes repeats.
func (c *Channel) SyncSessionCommands(ctx context.Context, externalID string, cmds []channels.SessionCommand) error {
	client := c.clientCopy()
	if client == nil {
		return nil
	}
	chatID, err := strconv.ParseInt(externalID, 10, 64)
	if err != nil || chatID == 0 {
		return nil
	}
	merged := sessionMenu(cmds)
	fp := menuFingerprint(merged)

	c.cmdMu.Lock()
	defer c.cmdMu.Unlock()
	if c.lastCmds[externalID] == fp {
		return nil
	}
	scope := map[string]any{"type": "chat", "chat_id": chatID}
	callCtx, cancel := context.WithTimeout(ctx, tokenValidateTimeout)
	if merged == nil {
		err = client.deleteMyCommands(callCtx, scope)
	} else {
		err = client.setMyCommands(callCtx, merged, scope)
	}
	cancel()
	if err != nil {
		c.log.Warn("could not sync Telegram session commands", "chat", externalID, "error", err)
		return nil
	}
	c.lastCmds[externalID] = fp
	return nil
}

// ClearSessionCommands drops the chat-scoped menu so the chat returns to
// the default one — used when the link moves to a conversation whose
// session has not advertised commands yet.
func (c *Channel) ClearSessionCommands(ctx context.Context, externalID string) error {
	client := c.clientCopy()
	if client == nil {
		return nil
	}
	chatID, err := strconv.ParseInt(externalID, 10, 64)
	if err != nil || chatID == 0 {
		return nil
	}
	c.cmdMu.Lock()
	defer c.cmdMu.Unlock()
	if c.lastCmds[externalID] == "-" {
		return nil
	}
	scope := map[string]any{"type": "chat", "chat_id": chatID}
	callCtx, cancel := context.WithTimeout(ctx, tokenValidateTimeout)
	err = client.deleteMyCommands(callCtx, scope)
	cancel()
	if err != nil {
		c.log.Warn("could not clear Telegram session commands", "chat", externalID, "error", err)
		// The old menu is still live server-side; keep the fingerprint so a
		// later sync is compared against what Telegram actually has.
		return nil
	}
	c.lastCmds[externalID] = "-"
	return nil
}
