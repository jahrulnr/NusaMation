package whatsapp

import (
	"context"
	"encoding/json"
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
}

func DefaultSettings() Settings {
	return Settings{
		Enabled: true, AutoStart: true, AllowedNumbers: []string{},
		GroupPolicy: "mention", ReadReceipts: true, SendTyping: true,
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
// channel does not silently reopen with an empty allowlist.
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
	if s.AllowedNumbers == nil {
		s.AllowedNumbers = []string{}
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(s.AllowedNumbers))
	for _, raw := range s.AllowedNumbers {
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

func (s Settings) allows(senderJIDs, chatJIDs []string) bool {
	if len(s.AllowedNumbers) == 0 {
		return true
	}
	allowed := map[string]bool{}
	for _, n := range s.AllowedNumbers {
		allowed[NormalizeNumber(n)] = true
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
	next := cur
	if err := json.Unmarshal(raw, &next); err != nil {
		return err
	}
	next.Normalize()
	if err := c.store.SetJSONSetting(ctx, SettingsKey, next); err != nil {
		return err
	}
	c.mu.Lock()
	c.settings = next
	c.mu.Unlock()
	c.publish()
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
