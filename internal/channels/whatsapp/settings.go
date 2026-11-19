package whatsapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/types"

	"github.com/jahrulnr/nusamation/internal/channels"
	appstore "github.com/jahrulnr/nusamation/internal/store"
)

const SettingsKey = "channel.whatsapp.settings"

type Settings struct {
	Enabled        bool     `json:"enabled"`
	AutoStart      bool     `json:"autoStart"`
	AllowedNumbers []string `json:"allowedNumbers"`
	// AllowedGroups whitelists group chats by their full @g.us JID, kept
	// verbatim. A group has no identity an operator can type from memory
	// like a phone number, so this list is filled by the joined-groups
	// picker in the web panel (or the CLI group flags), not the numbers
	// textarea.
	AllowedGroups []string `json:"allowedGroups"`
	GroupPolicy   string   `json:"groupPolicy"` // disabled, mention, all
	ReadReceipts  bool     `json:"readReceipts"`
	SendTyping    bool     `json:"sendTyping"`
	// RetentionDays purges indexed messages older than this many days
	// (0 keeps the index forever).
	RetentionDays int `json:"retentionDays"`
	// SendPacing is the minimum gap in seconds between two messages sent to
	// the same chat (0 disables pacing); QueueCap bounds the per-chat send
	// queue. Both shape the human-like outbound cadence.
	SendPacing int `json:"sendPacing"`
	QueueCap   int `json:"queueCap"`
}

func DefaultSettings() Settings {
	return Settings{
		Enabled: true, AutoStart: true, AllowedNumbers: []string{}, AllowedGroups: []string{},
		GroupPolicy: "mention", ReadReceipts: true, SendTyping: true, RetentionDays: 0,
		SendPacing: 5, QueueCap: 20,
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
	c.log.Warn("could not load WhatsApp settings; keeping previous values", "error", err)
	return fallback
}

func (s *Settings) Normalize() {
	if s.GroupPolicy != "disabled" && s.GroupPolicy != "mention" && s.GroupPolicy != "all" {
		s.GroupPolicy = "mention"
	}
	if s.RetentionDays < 0 {
		s.RetentionDays = 0
	}
	if s.RetentionDays > 3650 {
		s.RetentionDays = 3650
	}
	if s.SendPacing < 0 {
		s.SendPacing = 0
	}
	if s.SendPacing > 60 {
		s.SendPacing = 60
	}
	if s.QueueCap < 1 {
		s.QueueCap = 1
	}
	if s.QueueCap > 200 {
		s.QueueCap = 200
	}
	if s.AllowedNumbers == nil {
		s.AllowedNumbers = []string{}
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(s.AllowedNumbers))
	for _, raw := range s.AllowedNumbers {
		// "*" is the explicit allow-everyone token; it passes through so the
		// save paths can refuse a list that mixes it with numbers.
		if strings.TrimSpace(raw) == "*" {
			if !seen["*"] {
				seen["*"] = true
				out = append(out, "*")
			}
			continue
		}
		v := NormalizeNumber(raw)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Strings(out)
	s.AllowedNumbers = out
	if s.AllowedGroups == nil {
		s.AllowedGroups = []string{}
	}
	gseen := map[string]bool{}
	gout := make([]string, 0, len(s.AllowedGroups))
	for _, raw := range s.AllowedGroups {
		// Invalid entries drop silently here; the save paths refuse them
		// loudly before the list ever reaches this fallback.
		g := NormalizeGroupJID(raw)
		if g == "" || gseen[g] {
			continue
		}
		gseen[g] = true
		gout = append(gout, g)
	}
	sort.Strings(gout)
	s.AllowedGroups = gout
}

// NormalizeGroupJID canonicalizes one group allowlist entry: lowercase, no
// device suffix, @g.us required. A bare digit string is refused — it is
// ambiguous with a phone number and belongs in the numbers allowlist.
func NormalizeGroupJID(raw string) string {
	v := strings.TrimSpace(strings.ToLower(raw))
	if v == "" || v == "*" {
		return ""
	}
	if i := strings.IndexByte(v, ':'); i >= 0 {
		v = v[:i]
	}
	if !strings.HasSuffix(v, "@g.us") {
		return ""
	}
	user := strings.TrimSuffix(v, "@g.us")
	if user == "" || strings.TrimLeft(user, "0123456789-") != "" {
		return ""
	}
	return user + "@g.us"
}

// ValidateAllowlist refuses a saved allowlist that mixes the wildcard with
// concrete entries: "*" must be the only entry across numbers and groups.
func (s Settings) ValidateAllowlist() error {
	if len(s.AllowedNumbers)+len(s.AllowedGroups) > 1 {
		if slices.Contains(s.AllowedNumbers, "*") {
			return errors.New("* must be the only allowlist entry; it cannot be combined with numbers or groups")
		}
	}
	return nil
}

func (s Settings) allows(senderJIDs, chatJIDs []string) bool {
	// Empty lists deny everyone: the operator may simply not have filled
	// the form yet, so silence is safer than a broadcast reply. A single
	// "*" entry is the explicit opt-in to allow all senders.
	if len(s.AllowedNumbers) == 0 && len(s.AllowedGroups) == 0 {
		return false
	}
	wildcard := false
	allowed := map[string]bool{}
	for _, n := range s.AllowedNumbers {
		if n == "*" {
			wildcard = true
			continue
		}
		allowed[NormalizeNumber(n)] = true
	}
	if wildcard {
		return true
	}
	groups := map[string]bool{}
	for _, g := range s.AllowedGroups {
		groups[strings.ToLower(g)] = true
	}
	// Group entries match only the chat a message arrived in, so a group
	// JID can never whitelist an individual. Numbers match sender and
	// chat — a legacy install may still keep a stripped group JID among
	// them, and that keeps working.
	for _, j := range chatJIDs {
		if groups[strings.ToLower(j)] {
			return true
		}
		if allowed[NormalizeNumber(j)] {
			return true
		}
	}
	for _, j := range senderJIDs {
		if allowed[NormalizeNumber(j)] {
			return true
		}
	}
	return false
}

func NormalizeNumber(raw string) string {
	v := strings.TrimSpace(strings.ToLower(raw))
	v = strings.TrimPrefix(v, "+")
	v = strings.ReplaceAll(v, " ", "")
	v = strings.ReplaceAll(v, "-", "")
	for _, suffix := range []string{"@s.whatsapp.net", "@lid", "@g.us", "@broadcast"} {
		v = strings.TrimSuffix(v, suffix)
	}
	if i := strings.IndexByte(v, ':'); i >= 0 {
		v = v[:i]
	}
	v = strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, v)
	return v
}

// presenceChecker is the slice of the whatsmeow client used to verify that
// allowlist numbers are registered WhatsApp accounts. Kept as an interface so
// tests can fake the lookup.
type presenceChecker interface {
	IsOnWhatsApp(ctx context.Context, phones []string) ([]types.IsOnWhatsAppResponse, error)
}

// validateAllowlistPresence refuses a save whose bare-number entries are not
// registered WhatsApp accounts, so a typo can never become a silent
// "bot never replies". Runs on the raw patch entries — normalization strips
// the @g.us suffix and would hide which entry is a group. Skips the wildcard,
// @-JIDs (groups and LIDs), and everything when the channel is not connected
// or the lookup API fails.
func (c *Channel) validateAllowlistPresence(ctx context.Context, entries []string) error {
	var bare []string
	for _, n := range entries {
		if n == "*" || strings.Contains(n, "@") {
			continue
		}
		if v := NormalizeNumber(n); v != "" {
			bare = append(bare, v)
		}
	}
	if len(bare) == 0 {
		return nil
	}
	checker := c.presenceLookup
	if checker == nil {
		client := c.clientCopy()
		if client == nil || !client.IsLoggedIn() {
			return nil
		}
		checker = client
	}
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	responses, err := checker.IsOnWhatsApp(checkCtx, bare)
	if err != nil {
		c.log.Warn("could not verify allowlist numbers on WhatsApp; saving unchecked", "error", err)
		return nil
	}
	onWhatsApp := map[string]bool{}
	for _, r := range responses {
		onWhatsApp[strings.TrimPrefix(r.Query, "+")] = r.IsIn
	}
	var missing []string
	for _, n := range bare {
		if !onWhatsApp[n] {
			missing = append(missing, "+"+n)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("allowlist number(s) not on WhatsApp: %s — fix or remove them", strings.Join(missing, ", "))
	}
	return nil
}

func (c *Channel) Settings(ctx context.Context) (any, error) {
	c.mu.Lock()
	s := c.settings
	c.mu.Unlock()
	return s, nil
}

func (c *Channel) UpdateSettings(ctx context.Context, raw json.RawMessage) error {
	c.mu.Lock()
	cur := c.settings
	c.mu.Unlock()
	// Track whether the patch actually carries an allowlist so an
	// intentional clear (`[]`) stays allowed — it now denies everyone —
	// while a list whose entries are all invalid, which would normalize to
	// the same denial by accident, is refused like the CLI. A single "*"
	// grants everyone.
	var patch struct {
		AllowedNumbers *[]string `json:"allowedNumbers"`
		AllowedGroups  *[]string `json:"allowedGroups"`
	}
	_ = json.Unmarshal(raw, &patch)
	next := cur
	// next shares the lists' backing arrays with the live settings;
	// json.Unmarshal reuses them in place when decoding a longer list,
	// which would corrupt c.settings even when this update is refused.
	next.AllowedNumbers = append([]string(nil), cur.AllowedNumbers...)
	next.AllowedGroups = append([]string(nil), cur.AllowedGroups...)
	if err := json.Unmarshal(raw, &next); err != nil {
		return err
	}
	next.Normalize()
	if err := next.ValidateAllowlist(); err != nil {
		return err
	}
	if patch.AllowedGroups != nil {
		// A typo in the picker or CLI must not become a silent dead
		// entry, so refuse non-group values before Normalize drops them.
		for _, g := range *patch.AllowedGroups {
			if NormalizeGroupJID(g) == "" {
				return fmt.Errorf("group allowlist entry %q is not a group JID (...@g.us); phone numbers belong in the numbers allowlist", g)
			}
		}
	}
	if patch.AllowedNumbers != nil {
		if err := c.validateAllowlistPresence(ctx, *patch.AllowedNumbers); err != nil {
			return err
		}
	}
	if patch.AllowedNumbers != nil && len(*patch.AllowedNumbers) > 0 && len(next.AllowedNumbers) == 0 {
		return errors.New("allowlist has no valid numbers; an empty list allows no one — use * to allow everyone")
	}
	if err := c.store.SetJSONSetting(ctx, SettingsKey, next); err != nil {
		return err
	}
	c.mu.Lock()
	c.settings = next
	c.mu.Unlock()
	c.publish()
	if next.RetentionDays > 0 && next.RetentionDays != cur.RetentionDays {
		go c.purgeOlderThan(next.RetentionDays)
	}
	if !next.Enabled {
		_ = c.Disconnect()
	} else if next.Enabled && next.AutoStart {
		_ = c.Connect(ctx)
	}
	return nil
}

func (c *Channel) status() channels.Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	connected, loggedIn := false, false
	selfID := ""
	if c.client != nil {
		connected = c.client.IsConnected()
		loggedIn = c.client.IsLoggedIn()
		if c.client.Store.ID != nil {
			selfID = "+" + c.client.Store.ID.User
		}
	}
	state := c.state
	if state == "" {
		state = "disconnected"
	}
	st := channels.Status{
		Name: "whatsapp", Kind: "whatsapp", Label: "WhatsApp", Enabled: c.settings.Enabled,
		Connected: connected, LoggedIn: loggedIn, State: state, Detail: c.detail,
		SelfID: selfID, Manageable: true, UpdatedAt: nowMillis(),
		Stats: map[string]any{
			"paired": c.client != nil && c.client.Store.ID != nil,
			"qr":     c.qrCode != "", "groupPolicy": c.settings.GroupPolicy,
			"autoStart": c.settings.AutoStart,
			"lastError": c.lastErr,
			"blocked":   c.blockedList(),
		},
	}
	if c.lastErr != "" && c.detail == "" {
		st.Detail = c.lastErr
	}
	return st
}

func (c *Channel) settingsSnapshot() Settings {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.settings
}

func nowMillis() int64 {
	return time.Now().UnixMilli()
}
