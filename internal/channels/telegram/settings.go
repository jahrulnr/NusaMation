package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	appstore "github.com/jahrulnr/nusamation/internal/store"
)

// SettingsKey holds the non-secret channel settings. The bot token lives in
// its own row (tokenKey) so a settings read never touches it, and the last
// getUpdates offset in offsetKey so a restart does not replay updates.
const (
	SettingsKey = "channel.telegram.settings"
	tokenKey    = "channel.telegram.token"
	offsetKey   = "channel.telegram.offset"
)

type Settings struct {
	Enabled   bool `json:"enabled"`
	AutoStart bool `json:"autoStart"`
	// AllowedUserIDs holds decimal Telegram user ids ("123456789"); the only
	// non-numeric entry permitted is a lone "*". AllowedGroupIDs holds chat
	// ids — negative for groups and supergroups ("-1001234567890").
	AllowedUserIDs  []string `json:"allowedUserIds"`
	AllowedGroupIDs []string `json:"allowedGroupIds"`
	GroupPolicy     string   `json:"groupPolicy"` // disabled, mention, all
}

// publicSettings is what Settings() hands the web panel: everything the
// operator can edit plus tokenSet, never the token itself.
type publicSettings struct {
	Enabled         bool     `json:"enabled"`
	AutoStart       bool     `json:"autoStart"`
	AllowedUserIDs  []string `json:"allowedUserIds"`
	AllowedGroupIDs []string `json:"allowedGroupIds"`
	GroupPolicy     string   `json:"groupPolicy"`
	TokenSet        bool     `json:"tokenSet"`
	BotUsername     string   `json:"botUsername,omitempty"`
}

func DefaultSettings() Settings {
	return Settings{
		Enabled: true, AutoStart: true, AllowedUserIDs: []string{}, AllowedGroupIDs: []string{},
		GroupPolicy: "mention",
	}
}

// failClosedSettings is the first-load fallback when stored settings cannot
// be read: the channel stays disabled rather than opening to everyone.
func failClosedSettings() Settings {
	s := DefaultSettings()
	s.Enabled = false
	s.AutoStart = false
	return s
}

// loadSettings returns the stored settings. A missing row yields defaults;
// any other failure (corrupt JSON, DB error) keeps the fallback so the
// channel does not silently flip its enabled state or allowlist on a bad read.
func (c *Channel) loadSettings(ctx context.Context, fallback Settings) Settings {
	s := DefaultSettings()
	err := c.store.GetJSONSetting(ctx, SettingsKey, &s)
	if err == nil {
		s.Normalize()
		return s
	}
	if appstore.IsNotFound(err) {
		return s
	}
	c.log.Warn("could not load Telegram settings; keeping previous values", "error", err)
	return fallback
}

var (
	userIDRe  = regexp.MustCompile(`^[0-9]+$`)
	groupIDRe = regexp.MustCompile(`^-[0-9]+$`)
)

func (s *Settings) Normalize() {
	if s.GroupPolicy != "disabled" && s.GroupPolicy != "mention" && s.GroupPolicy != "all" {
		s.GroupPolicy = "mention"
	}
	s.AllowedUserIDs = normalizeIDList(s.AllowedUserIDs, userIDRe, true)
	// Group allowlist entries are chat ids and therefore always negative.
	// A positive id here is a user id pasted into the wrong list; keeping it
	// would silently open that user's private chat, because a DM's chat_id
	// equals the user id.
	s.AllowedGroupIDs = normalizeIDList(s.AllowedGroupIDs, groupIDRe, false)
}

// normalizeIDList trims, drops invalid entries and de-duplicates while keeping
// entry order — a numeric sort would scatter ids the operator just typed.
// The wildcard passes through only for the user list; save paths refuse a
// list that mixes it with ids.
func normalizeIDList(in []string, re *regexp.Regexp, allowWildcard bool) []string {
	if in == nil {
		return []string{}
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, raw := range in {
		v := strings.TrimSpace(raw)
		if v == "*" {
			if allowWildcard && !seen["*"] {
				seen["*"] = true
				out = append(out, "*")
			}
			continue
		}
		if v == "" || !re.MatchString(v) || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// ValidateAllowlist refuses a saved allowlist that mixes the wildcard with
// concrete entries: "*" must be the only entry across users and groups.
func (s Settings) ValidateAllowlist() error {
	if slices.Contains(s.AllowedUserIDs, "*") && len(s.AllowedUserIDs)+len(s.AllowedGroupIDs) > 1 {
		return errors.New("* must be the only allowlist entry; it cannot be combined with user or group ids")
	}
	if slices.Contains(s.AllowedGroupIDs, "*") {
		return errors.New("* only belongs in the user allowlist; group ids are numeric chat ids")
	}
	return nil
}

// allows reports whether a sender/chat pair passes the allowlist. Empty lists
// deny everyone — silence is safer than broadcasting to whoever finds the
// bot. A lone "*" in the user list is the explicit allow-everyone opt-in;
// group policy still gates group traffic on top of this check.
func (s Settings) allows(senderID, chatID string) bool {
	if len(s.AllowedUserIDs) == 0 && len(s.AllowedGroupIDs) == 0 {
		return false
	}
	for _, id := range s.AllowedUserIDs {
		if id == "*" || id == senderID {
			return true
		}
	}
	// A group entry only matches a group chat. Private chats carry the
	// user's own positive id as chat_id, so matching groups there would
	// turn a mistyped group entry into a private allow.
	if strings.HasPrefix(chatID, "-") {
		for _, id := range s.AllowedGroupIDs {
			if id == chatID {
				return true
			}
		}
	}
	return false
}

func (c *Channel) Settings(ctx context.Context) (any, error) {
	c.mu.Lock()
	s := c.settings
	tokenSet := c.tokenPresent
	var botUsername string
	if c.me != nil {
		botUsername = c.me.Username
	}
	c.mu.Unlock()
	return publicSettings{
		Enabled: s.Enabled, AutoStart: s.AutoStart,
		AllowedUserIDs: s.AllowedUserIDs, AllowedGroupIDs: s.AllowedGroupIDs,
		GroupPolicy: s.GroupPolicy, TokenSet: tokenSet, BotUsername: botUsername,
	}, nil
}

func (c *Channel) UpdateSettings(ctx context.Context, raw json.RawMessage) error {
	c.mu.Lock()
	cur := c.settings
	c.mu.Unlock()
	// Track whether the patch actually carries an allowlist so an
	// intentional clear (`[]`) stays allowed — it now denies everyone —
	// while a list whose entries are all invalid, which would normalize to
	// the same denial by accident, is refused loudly. A single "*" grants
	// every sender (group policy still applies).
	var patch struct {
		AllowedUserIDs  *[]string `json:"allowedUserIds"`
		AllowedGroupIDs *[]string `json:"allowedGroupIds"`
		Token           *string   `json:"token"`
	}
	_ = json.Unmarshal(raw, &patch)
	next := cur
	// next shares the lists' backing arrays with the live settings;
	// json.Unmarshal reuses them in place when decoding a longer list,
	// which would corrupt c.settings even when this update is refused.
	next.AllowedUserIDs = append([]string(nil), cur.AllowedUserIDs...)
	next.AllowedGroupIDs = append([]string(nil), cur.AllowedGroupIDs...)
	if err := json.Unmarshal(raw, &next); err != nil {
		return err
	}
	if patch.AllowedUserIDs != nil {
		for _, id := range *patch.AllowedUserIDs {
			v := strings.TrimSpace(id)
			if v != "*" && !userIDRe.MatchString(v) {
				return fmt.Errorf("user allowlist entry %q is not a numeric Telegram user id", id)
			}
		}
	}
	if patch.AllowedGroupIDs != nil {
		for _, id := range *patch.AllowedGroupIDs {
			if !groupIDRe.MatchString(strings.TrimSpace(id)) {
				return fmt.Errorf("group allowlist entry %q must be a negative numeric Telegram chat id; user ids belong in the user allowlist", id)
			}
		}
	}
	next.Normalize()
	if err := next.ValidateAllowlist(); err != nil {
		return err
	}
	if patch.AllowedUserIDs != nil && len(*patch.AllowedUserIDs) > 0 && len(next.AllowedUserIDs) == 0 {
		return errors.New("allowlist has no valid user ids; an empty list allows no one — use * to allow every sender")
	}
	// A new token is verified against getMe before anything is written — a
	// typo must not overwrite a working token. An empty token field keeps
	// the saved one; only Logout removes it.
	var newToken string
	tokenChanged := false
	if patch.Token != nil {
		if tok := strings.TrimSpace(*patch.Token); tok != "" {
			meCtx, cancel := context.WithTimeout(ctx, tokenValidateTimeout)
			_, err := newAPIClient(tok, c.apiBase).getMe(meCtx)
			cancel()
			if err != nil {
				return fmt.Errorf("Telegram getMe rejected the new token: %w", err)
			}
			oldToken, err := c.loadToken(ctx)
			if err != nil {
				return err
			}
			if oldToken != tok {
				newToken, tokenChanged = tok, true
			}
		}
	}
	// Persist the non-secret settings first. If that write fails, the token
	// row must still be the old one — otherwise the caller sees an error
	// while the new token is already stored and half-applied.
	if err := c.store.SetJSONSetting(ctx, SettingsKey, next); err != nil {
		return err
	}
	c.mu.Lock()
	c.settings = next
	c.mu.Unlock()
	if newToken != "" {
		if err := c.saveToken(ctx, newToken); err != nil {
			c.publish()
			return err
		}
		if tokenChanged {
			// The chat-scoped menus belonged to the previous bot.
			c.resetMenuFingerprints()
		}
		c.mu.Lock()
		c.tokenPresent = true
		c.mu.Unlock()
	}
	c.publish()
	if !next.Enabled {
		return c.Disconnect()
	}
	if !c.tokenSnapshot() {
		return nil
	}
	if tokenChanged {
		c.mu.Lock()
		active := c.polling || c.connecting
		c.mu.Unlock()
		if active {
			// Connect alone would no-op while the old token's flow polls.
			return c.restartConnect(ctx)
		}
	}
	if next.AutoStart {
		return c.Connect(ctx)
	}
	return nil
}

/* ---------------- token + offset persistence ---------------- */

// loadToken opens the stored bot token. The row is sealed by the store
// sealer; a read failure means the token is unusable, which callers surface
// as "no token" rather than sending garbage to the Bot API.
func (c *Channel) loadToken(ctx context.Context) (string, error) {
	v, err := c.store.GetSetting(ctx, tokenKey)
	if err != nil {
		if appstore.IsNotFound(err) {
			return "", nil
		}
		return "", err
	}
	if strings.TrimSpace(v) == "" {
		return "", nil
	}
	tok, err := c.store.OpenString(v)
	if err != nil {
		return "", fmt.Errorf("open Telegram bot token: %w", err)
	}
	return tok, nil
}

func (c *Channel) saveToken(ctx context.Context, token string) error {
	enc, err := c.store.SealString(token)
	if err != nil {
		return fmt.Errorf("seal Telegram bot token: %w", err)
	}
	return c.store.SetSetting(ctx, tokenKey, enc)
}

// clearToken overwrites the token row — settings have no delete, and an
// empty value reads back as "no token" through loadToken.
func (c *Channel) clearToken(ctx context.Context) error {
	return c.store.SetSetting(ctx, tokenKey, "")
}

// refreshTokenFlag re-reads the token marker into the cached flag so
// Status()/Settings() never hit the database on every publish.
func (c *Channel) refreshTokenFlag(ctx context.Context) {
	tok, err := c.loadToken(ctx)
	c.mu.Lock()
	c.tokenPresent = err == nil && tok != ""
	c.mu.Unlock()
}

func (c *Channel) loadOffset(ctx context.Context) int64 {
	v, err := c.store.GetSetting(ctx, offsetKey)
	if err != nil {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// saveOffset persists the next expected update_id so a restart resumes where
// the poller left off instead of replaying already-handled updates.
func (c *Channel) saveOffset(ctx context.Context, offset int64) {
	if err := c.store.SetSetting(ctx, offsetKey, strconv.FormatInt(offset, 10)); err != nil {
		c.log.Warn("could not persist Telegram update offset", "error", err)
	}
}
