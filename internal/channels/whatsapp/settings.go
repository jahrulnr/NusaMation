package whatsapp

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jahrulnr/nusamation/internal/channels"
	appstore "github.com/jahrulnr/nusamation/internal/store"
)

const SettingsKey = "channel.whatsapp.settings"

type Settings struct {
	Enabled        bool     `json:"enabled"`
	AutoStart      bool     `json:"autoStart"`
	AllowedNumbers []string `json:"allowedNumbers"`
	GroupPolicy    string   `json:"groupPolicy"` // disabled, mention, all
	ReadReceipts   bool     `json:"readReceipts"`
	SendTyping     bool     `json:"sendTyping"`
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
		Enabled: true, AutoStart: true, AllowedNumbers: []string{},
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
}

// ValidateAllowlist refuses a saved allowlist that mixes the wildcard with
// concrete numbers: "*" must be the single entry.
func (s Settings) ValidateAllowlist() error {
	if len(s.AllowedNumbers) > 1 {
		if slices.Contains(s.AllowedNumbers, "*") {
			return errors.New("* must be the only allowlist entry; it cannot be combined with numbers")
		}
	}
	return nil
}

func (s Settings) allows(senderJIDs, chatJIDs []string) bool {
	// An empty allowlist denies everyone: the operator may simply not have
	// filled the form yet, so silence is safer than a broadcast reply. A
	// single "*" entry is the explicit opt-in to allow all senders.
	if len(s.AllowedNumbers) == 0 {
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
	for _, j := range append(append([]string{}, senderJIDs...), chatJIDs...) {
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
	}
	_ = json.Unmarshal(raw, &patch)
	next := cur
	// next shares AllowedNumbers' backing array with the live settings;
	// json.Unmarshal reuses that array in place when decoding a longer list,
	// which would corrupt c.settings even when this update is refused.
	next.AllowedNumbers = append([]string(nil), cur.AllowedNumbers...)
	if err := json.Unmarshal(raw, &next); err != nil {
		return err
	}
	next.Normalize()
	if err := next.ValidateAllowlist(); err != nil {
		return err
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
