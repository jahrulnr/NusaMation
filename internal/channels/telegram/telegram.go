package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/jahrulnr/nusamation/internal/channels"
	appstore "github.com/jahrulnr/nusamation/internal/store"
	"github.com/jahrulnr/nusamation/internal/textfmt"
)

// maxMessageUnits is Telegram's sendMessage text cap in UTF-16 code units —
// the unit Telegram counts, where an emoji outside the BMP uses two. The
// agent is told the same number through ResponseRules so nothing it wrote is
// silently dropped.
const maxMessageUnits = 4096

// pollTimeout is the getUpdates long-poll wait. The request context adds a
// margin on top so a healthy poll ends server-side, not on our deadline.
const (
	pollTimeout          = 25 * time.Second
	pollRequestTimeout   = 35 * time.Second
	tokenValidateTimeout = 15 * time.Second

	// maxMessageAge drops updates that sat undelivered while the bot was
	// offline — Telegram holds them for at most a day, and answering a
	// hours-old message reads like a glitch.
	maxMessageAge = 2 * time.Hour

	// pollErrorLimit stops the loop after this many consecutive getUpdates
	// failures (a revoked token 401s forever; a dead network is no better).
	pollErrorLimit = 6
	pollRetryDelay = 3 * time.Second
)

type Channel struct {
	store *appstore.Store
	mgr   *channels.Manager
	log   *slog.Logger

	// apiBase points the Bot API client elsewhere in tests; production code
	// never changes it from defaultAPIBase.
	apiBase string

	handler channels.InboundHandler

	mu           sync.Mutex
	settings     Settings
	tokenPresent bool
	me           *tgUser
	client       *apiClient
	state        string
	detail       string
	lastErr      string
	connecting   bool
	polling      bool
	started      bool
	stopped      bool
	pollCancel   context.CancelFunc
	// flowID identifies the current connect/poll flow. A disconnect or a new
	// token increments it, so an older flow's cleanup can never clear the
	// client, flags or state of the flow that replaced it.
	flowID   uint64
	lifeCtx  context.Context
	lifeStop context.CancelFunc

	blockedMu sync.Mutex
	blocked   []blockedEntry

	// typingMu tracks deliveries currently showing "typing" so Disconnect and
	// Stop can stop the indicator without touching the turn lifecycle.
	typingMu sync.Mutex
	typing   map[*delivery]struct{}

	// cmdMu serialises chat-scoped menu publishes — at most one Bot API
	// call is in flight, which trivially guarantees one per chat. lastCmds
	// fingerprints the last outcome per external id so identical syncs and
	// repeat deletes skip the API entirely.
	cmdMu    sync.Mutex
	lastCmds map[string]string

	// dispatchMu serialises handler calls: an album flush must never race a
	// normal message into the chat service.
	dispatchMu sync.Mutex

	// albums buffers one media group until its debounce elapses, keyed by
	// chat:thread:media_group_id.
	albumMu sync.Mutex
	albums  map[string]*albumBatch
}

// blockedEntry records a recent sender the allowlist rejected, so the web
// panel can surface it with a one-tap Allow that adds the numeric id —
// usernames are shown for context but never allowed (they can change).
type blockedEntry struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Username  string `json:"username,omitempty"`
	Chat      string `json:"chat"`
	ChatName  string `json:"chatName,omitempty"`
	IsGroup   bool   `json:"isGroup"`
	Timestamp int64  `json:"timestampMs"`
}

const blockedListLimit = 50

var _ channels.Channel = (*Channel)(nil)
var _ channels.Controller = (*Channel)(nil)
var _ channels.ResponseStyler = (*Channel)(nil)
var _ channels.SessionCommands = (*Channel)(nil)

func New(st *appstore.Store, mgr *channels.Manager, log *slog.Logger) *Channel {
	if log == nil {
		log = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &Channel{
		store: st, mgr: mgr, log: log.With("component", "telegram"),
		apiBase: defaultAPIBase, state: "unpaired",
		lifeCtx: ctx, lifeStop: cancel,
		lastCmds: map[string]string{},
		albums:   map[string]*albumBatch{},
		typing:   map[*delivery]struct{}{},
	}
	c.settings = c.loadSettings(context.Background(), failClosedSettings())
	c.refreshTokenFlag(context.Background())
	return c
}

func (c *Channel) SetHandler(h channels.InboundHandler) { c.handler = h }

func (c *Channel) Name() string  { return "telegram" }
func (c *Channel) Label() string { return "Telegram" }

// DeliveredTextLimit is the channel-level form of the per-message cap so the
// chat layer sizes mirrored digests (which use Notify) by the same rule the
// agent was told.
func (c *Channel) DeliveredTextLimit() int { return maxMessageUnits }

// ResponseRules tells the agent exactly what Telegram renders and what a
// reply can carry: one HTML-formatted message per finished turn, with file
// references that become real attachments.
func (c *Channel) ResponseRules() string {
	return "HTML only: <b>bold</b>, <i>italic</i>, <u>underline</u>, <s>strikethrough</s>, " +
		"<code>inline code</code>, <pre>code block</pre>, <blockquote>quote</blockquote>, and " +
		"<a href=\"https://example.com\">links</a> (http or https only, in double quotes). " +
		"No headings, tables, footnotes, HTML beyond those tags, or Markdown. " +
		"One message is sent when the turn finishes, never mid-turn; keep it under 4,096 characters — Telegram counts an emoji outside the basic plane as two. " +
		"Thoughts and tool calls stay on the web — only the final reply reaches Telegram. " +
		"Attach a file by writing [name](path) or ![name](path) in the reply; copy the absolute path verbatim from your tools — spaces and backslashes are fine. " +
		"The link is removed and the file arrives as a real attachment. Never convert a photo into a sticker to send it."
}

func (c *Channel) Start(ctx context.Context) error {
	c.mu.Lock()
	if c.started || c.stopped {
		c.mu.Unlock()
		return nil
	}
	c.started = true
	c.settings = c.loadSettings(ctx, c.settings)
	c.mu.Unlock()
	c.refreshTokenFlag(ctx)
	c.mu.Lock()
	if c.tokenPresent && c.state == "unpaired" {
		c.state = "disconnected"
		c.detail = "Bot token saved. Connect to start polling."
	}
	c.mu.Unlock()
	c.publish()
	s := c.settingsSnapshot()
	if s.Enabled && s.AutoStart && c.tokenSnapshot() {
		c.log.Info("auto-starting Telegram channel")
		if err := c.Connect(context.Background()); err != nil {
			c.log.Warn("Telegram auto-start failed", "error", err)
		}
	}
	return nil
}

func (c *Channel) Stop() error {
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return nil
	}
	c.stopped = true
	c.flowID++
	if c.pollCancel != nil {
		c.pollCancel()
		c.pollCancel = nil
	}
	c.lifeStop()
	c.client = nil
	c.connecting = false
	c.polling = false
	c.mu.Unlock()
	c.stopAllTyping()
	return nil
}

// Connect validates the token and starts the long-poll loop. Everything
// network-bound runs in a goroutine — this call never blocks the caller on
// api.telegram.org being reachable.
func (c *Channel) Connect(ctx context.Context) error {
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return errors.New("Telegram channel is stopped")
	}
	if !c.settings.Enabled {
		c.mu.Unlock()
		return errors.New("enable Telegram in channel settings first")
	}
	if c.polling || c.connecting {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	token, err := c.loadToken(ctx)
	if err != nil {
		c.setError(fmt.Errorf("read Telegram bot token: %w", err))
		return err
	}
	if token == "" {
		c.mu.Lock()
		c.state = "unpaired"
		c.detail = "No bot token saved. Create a bot with @BotFather and paste its token below."
		c.mu.Unlock()
		c.publish()
		return errors.New("no Telegram bot token saved")
	}

	c.mu.Lock()
	// Re-check after the token read: Disconnect or another Connect may have
	// won the race while the store was being read.
	if c.stopped || c.polling || c.connecting {
		c.mu.Unlock()
		return nil
	}
	flowCtx, cancel := context.WithCancel(c.lifeCtx)
	c.flowID++
	gen := c.flowID
	c.pollCancel = cancel
	c.connecting = true
	c.state = "connecting"
	c.detail = "Validating the bot token…"
	c.mu.Unlock()
	c.publish()
	go c.connectFlow(flowCtx, cancel, gen, token)
	return nil
}

// connectFlow validates the token once, publishes the "/" menu once, then
// runs the poll loop until Disconnect/Stop cancels it. It owns the
// connecting/polling flags end to end, but only while gen is still the
// channel's current flow — a reconnect started while this one was winding
// down must keep its own client and status.
func (c *Channel) connectFlow(ctx context.Context, cancel context.CancelFunc, gen uint64, token string) {
	defer cancel()
	client := newAPIClient(token, c.apiBase)

	meCtx, meCancel := context.WithTimeout(ctx, tokenValidateTimeout)
	me, err := client.getMe(meCtx)
	meCancel()
	if err != nil {
		c.mu.Lock()
		owner := c.flowID == gen
		if owner {
			c.connecting = false
			c.client = nil
			c.pollCancel = nil
		}
		c.mu.Unlock()
		if owner && ctx.Err() == nil {
			c.setError(fmt.Errorf("getMe failed — check the bot token: %w", err))
		}
		return
	}

	c.mu.Lock()
	if c.flowID != gen {
		c.mu.Unlock()
		return
	}
	c.client = client
	c.me = me
	c.state = "connected"
	c.detail = ""
	c.lastErr = ""
	c.connecting = false
	c.polling = true
	c.mu.Unlock()
	c.publish()
	c.log.Info("Telegram bot connected", "username", "@"+me.Username)

	// The "/" menu is published once per connect, not per poll — it is
	// static for this slice. A failure is cosmetic: stay connected and let
	// the status detail say the menu could not be set.
	menuCtx, menuCancel := context.WithTimeout(ctx, tokenValidateTimeout)
	if err := client.setMyCommands(menuCtx, menuCommands, map[string]any{"type": "default"}); err != nil {
		menuCancel()
		c.log.Warn("could not publish the Telegram command menu", "error", err)
		c.mu.Lock()
		owner := c.flowID == gen
		if owner {
			c.detail = "Connected, but the / command menu could not be published: " + err.Error()
		}
		c.mu.Unlock()
		if owner {
			c.publish()
		}
	} else {
		menuCancel()
	}

	c.pollLoop(ctx, client)

	c.mu.Lock()
	owner := c.flowID == gen
	if owner {
		c.polling = false
		c.client = nil
		c.pollCancel = nil
		if c.state == "connected" {
			c.state = "disconnected"
			c.detail = "Polling stopped. Connect again to resume."
		}
	}
	c.mu.Unlock()
	if owner {
		c.publish()
	}
}

// pollLoop is the single getUpdates loop. Offset moves forward only after a
// batch is fetched and is persisted per batch; in-flight messages that die
// mid-batch replay once and are deduplicated downstream by their
// chat_id:message_id key.
func (c *Channel) pollLoop(ctx context.Context, client *apiClient) {
	offset := c.loadOffset(ctx)
	failures := 0
	for {
		if ctx.Err() != nil {
			return
		}
		pollCtx, cancel := context.WithTimeout(ctx, pollRequestTimeout)
		updates, err := client.getUpdates(pollCtx, offset, int(pollTimeout/time.Second))
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// A 429 comes with the wait Telegram wants; honour it instead of
			// counting it toward the failure limit that stops polling.
			if wait, limited := retryAfterDelay(err); limited {
				c.log.Warn("Telegram rate limited getUpdates; waiting", "retryAfter", wait)
				select {
				case <-ctx.Done():
					return
				case <-time.After(wait):
				}
				continue
			}
			failures++
			c.log.Warn("Telegram getUpdates failed", "error", err, "consecutive", failures)
			c.mu.Lock()
			c.lastErr = err.Error()
			c.mu.Unlock()
			if failures >= pollErrorLimit {
				c.setError(fmt.Errorf("polling stopped after %d consecutive failures: %w", failures, err))
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(pollRetryDelay):
			}
			continue
		}
		failures = 0
		for i := range updates {
			if updates[i].UpdateID >= offset {
				offset = updates[i].UpdateID + 1
			}
			// allowed_updates pins message+inline_query; the nil guards stay
			// so a malformed batch cannot kill the loop.
			if updates[i].InlineQuery != nil {
				c.handleInlineQuery(ctx, updates[i].InlineQuery)
			}
			if updates[i].Message != nil {
				// Media downloads and album assembly run inline in this
				// loop; a large album on a slow link delays the next
				// getUpdates. Moving them off the loop would complicate the
				// album ordering, so this is a known trade-off.
				c.handleMessage(ctx, updates[i].Message)
			}
		}
		if len(updates) > 0 {
			c.saveOffset(context.Background(), offset)
		}
	}
}

func (c *Channel) Disconnect() error {
	c.mu.Lock()
	// Invalidating the flow keeps the old connectFlow from clearing the
	// client/status of a reconnect started right after this returns.
	c.flowID++
	if c.pollCancel != nil {
		c.pollCancel()
		c.pollCancel = nil
	}
	c.client = nil
	c.connecting = false
	c.polling = false
	if c.state != "unpaired" {
		c.state = "disconnected"
		c.detail = "Disconnected. Connect again to resume polling."
	}
	c.mu.Unlock()
	// Stop typing first so no sendChatAction races the teardown.
	c.stopAllTyping()
	// Deliver whatever an album was still buffering; a disconnect must not
	// silently drop a half-arrived media group.
	c.flushAlbums()
	c.publish()
	return nil
}

// restartConnect replaces a running flow with one authenticated by the token
// that was just saved. Connect alone would no-op while the old token's flow
// is still polling.
func (c *Channel) restartConnect(ctx context.Context) error {
	c.mu.Lock()
	c.flowID++
	if c.pollCancel != nil {
		c.pollCancel()
		c.pollCancel = nil
	}
	c.client = nil
	c.connecting = false
	c.polling = false
	if c.state != "unpaired" {
		c.state = "disconnected"
		c.detail = "Bot token replaced. Reconnecting…"
	}
	c.mu.Unlock()
	c.publish()
	return c.Connect(ctx)
}

// Logout drops the saved bot token and stops polling; there is no remote
// session to end — the Bot API is token-authenticated, so forgetting the
// token is the whole logout.
func (c *Channel) Logout(ctx context.Context) error {
	_ = c.Disconnect()
	if err := c.clearToken(ctx); err != nil {
		return err
	}
	c.resetMenuFingerprints()
	c.mu.Lock()
	c.me = nil
	c.tokenPresent = false
	c.state = "unpaired"
	c.detail = "Bot token removed. Paste a new @BotFather token to connect again."
	c.mu.Unlock()
	c.publish()
	return nil
}

func (c *Channel) QR(ctx context.Context) ([]byte, error) {
	return nil, errors.New("Telegram bots do not pair with QR codes")
}

func (c *Channel) Status() channels.Status { return c.status() }

func (c *Channel) status() channels.Status {
	// Read the blocked ring before taking c.mu. recordBlocked publishes after
	// releasing blockedMu, and publish -> status must never take the locks in
	// the opposite order.
	blocked := c.blockedList()
	c.mu.Lock()
	defer c.mu.Unlock()
	selfID := ""
	if c.me != nil && c.me.Username != "" {
		selfID = "@" + c.me.Username
	}
	state := c.state
	if state == "" {
		state = "disconnected"
	}
	st := channels.Status{
		Name: "telegram", Kind: "telegram", Label: "Telegram", Enabled: c.settings.Enabled,
		Connected: c.polling, LoggedIn: c.me != nil, State: state, Detail: c.detail,
		SelfID: selfID, Manageable: true, UpdatedAt: time.Now().UnixMilli(),
		Stats: map[string]any{
			"tokenSet":    c.tokenPresent,
			"groupPolicy": c.settings.GroupPolicy,
			"autoStart":   c.settings.AutoStart,
			"lastError":   c.lastErr,
			"blocked":     blocked,
		},
	}
	if c.me != nil {
		st.Stats["botUsername"] = "@" + c.me.Username
		st.Stats["botName"] = c.me.FirstName
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

func (c *Channel) tokenSnapshot() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tokenPresent
}

func (c *Channel) clientCopy() *apiClient {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.client
}

func (c *Channel) setError(err error) {
	if err == nil {
		return
	}
	c.mu.Lock()
	c.state = "error"
	c.lastErr = err.Error()
	c.detail = err.Error()
	c.connecting = false
	c.mu.Unlock()
	c.log.Warn("Telegram error", "error", err)
	c.publish()
}

func (c *Channel) publish() {
	if c.mgr == nil {
		return
	}
	c.mgr.PublishStatus(c.status())
}

/* ---------------- inbound ---------------- */

// handleMessage gates one update and hands a passing message to the chat
// service through the registered inbound handler. The adapter itself never
// imports internal/chat. Messages carrying media are downloaded first; an
// album is buffered and delivered as one inbound after its items settle.
func (c *Channel) handleMessage(ctx context.Context, m *tgMessage) {
	if !c.gateInbound(m) {
		return
	}
	in, ok := c.toInboundMedia(ctx, m)
	if !ok {
		// Media that failed to download still produced a text marker; an
		// update with neither text nor attachments is dropped.
		return
	}
	if m.MediaGroupID != "" {
		c.bufferAlbum(m, in)
		return
	}
	c.dispatch(ctx, in)
}

// dispatch calls the inbound handler with dispatchMu held, so an album flush
// and an ordinary message never enter the chat service concurrently.
func (c *Channel) dispatch(ctx context.Context, in channels.Inbound) {
	if c.handler == nil {
		return
	}
	c.dispatchMu.Lock()
	defer c.dispatchMu.Unlock()
	if err := c.handler.HandleInbound(ctx, in); err != nil {
		c.log.Warn("Telegram inbound rejected", "error", err)
	}
}

// gateInbound applies every pre-dispatch check in order: enabled, chat type,
// group policy, allowlist, message age. A failure returns silently — except
// allowlist rejections, which land in the blocked ring so the panel can
// offer an Allow button.
func (c *Channel) gateInbound(m *tgMessage) bool {
	s := c.settingsSnapshot()
	if !s.Enabled {
		return false
	}
	isGroup := m.Chat.Type == "group" || m.Chat.Type == "supergroup"
	// Channel chats are skipped entirely; anything that is not private,
	// group or supergroup has no defined handling here.
	if !isGroup && m.Chat.Type != "private" {
		return false
	}
	if m.From == nil || m.From.ID == 0 {
		return false
	}
	chatID := strconv.FormatInt(m.Chat.ID, 10)
	senderID := strconv.FormatInt(m.From.ID, 10)
	if isGroup {
		switch s.GroupPolicy {
		case "disabled":
			return false
		case "mention":
			// A mention or a "/command" is what makes the bot listen in a
			// group; ordinary chatter is ignored.
			if !c.mentionsMe(m) && !strings.HasPrefix(strings.TrimSpace(m.Text), "/") {
				return false
			}
		}
		// "all" accepts whatever Telegram delivers — which is still limited
		// by the bot's privacy mode configured in BotFather.
	}
	// Drop stale updates before the allowlist runs: a message that sat
	// undelivered for hours must not enter the blocked ring and re-publish
	// channel status.
	if m.Date > 0 && time.Since(time.Unix(m.Date, 0)) > maxMessageAge {
		return false
	}
	if !s.allows(senderID, chatID) {
		c.recordBlocked(m, senderID, chatID, isGroup)
		return false
	}
	return true
}

// mentionsMe reports whether the message addresses the bot: a "@botname"
// mention entity or literal text, a text_mention resolving to the bot's id,
// a reply to a message the bot sent, or a message the user sent through
// this bot's inline mode (the inserted text names a room, not the bot).
func (c *Channel) mentionsMe(m *tgMessage) bool {
	c.mu.Lock()
	me := c.me
	c.mu.Unlock()
	if me == nil {
		return false
	}
	if m.ViaBot != nil && ((me.ID != 0 && m.ViaBot.ID == me.ID) ||
		(me.Username != "" && strings.EqualFold(m.ViaBot.Username, me.Username))) {
		return true
	}
	if me.Username != "" {
		needle := "@" + strings.ToLower(me.Username)
		if textMentions(m.Text, m.Entities, needle) || textMentions(m.Caption, m.CaptionEntities, needle) {
			return true
		}
	}
	for _, group := range [][]tgEntity{m.Entities, m.CaptionEntities} {
		for _, e := range group {
			if e.Type == "text_mention" && e.User != nil && e.User.ID == me.ID {
				return true
			}
		}
	}
	if m.ReplyToMessage != nil && m.ReplyToMessage.From != nil && me.ID != 0 &&
		m.ReplyToMessage.From.ID == me.ID {
		return true
	}
	return false
}

// textMentions reports a literal or entity mention of needle in one text
// block. A literal @name only counts as a whole token: "@nusa_bot_extra" and
// "x@nusa_bot" are other usernames or addresses, not a mention of this bot.
// Captions carry their own entity list, so both are checked.
func textMentions(text string, entities []tgEntity, needle string) bool {
	if needle == "" {
		return false
	}
	if containsMentionToken(text, needle) {
		return true
	}
	for _, e := range entities {
		if e.Type == "mention" && strings.EqualFold(entityText(text, e), needle) {
			return true
		}
	}
	return false
}

// containsMentionToken matches an ASCII @username case-insensitively at a
// username boundary. Telegram usernames are ASCII letters, digits and
// underscores, so a byte scan is enough and cannot be thrown off by Unicode
// lowercasing changing string length.
func containsMentionToken(text, needle string) bool {
	needle = strings.ToLower(needle)
	if len(needle) == 0 || len(text) < len(needle) {
		return false
	}
	for i := 0; i+len(needle) <= len(text); i++ {
		if asciiLower(text[i]) != needle[0] {
			continue
		}
		match := true
		for j := 1; j < len(needle); j++ {
			if asciiLower(text[i+j]) != needle[j] {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		beforeOK := true
		if i > 0 {
			r, _ := utf8.DecodeLastRuneInString(text[:i])
			beforeOK = !isUsernameRune(r)
		}
		after := i + len(needle)
		afterOK := true
		if after < len(text) {
			r, _ := utf8.DecodeRuneInString(text[after:])
			afterOK = !isUsernameRune(r)
		}
		if beforeOK && afterOK {
			return true
		}
	}
	return false
}

func isUsernameRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

func asciiLower(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}

// entityText slices an entity's coverage out of the message. Telegram counts
// offsets in UTF-16 code units, so runes alone misplace entities after emoji.
func entityText(text string, e tgEntity) string {
	u := utf16.Encode([]rune(text))
	if e.Offset < 0 || e.Length <= 0 || e.Offset+e.Length > len(u) {
		return ""
	}
	return string(utf16.Decode(u[e.Offset : e.Offset+e.Length]))
}

func (c *Channel) toInbound(m *tgMessage) channels.Inbound {
	chatID := strconv.FormatInt(m.Chat.ID, 10)
	isGroup := m.Chat.Type == "group" || m.Chat.Type == "supergroup"
	var senderID, senderName string
	if m.From != nil {
		senderID = strconv.FormatInt(m.From.ID, 10)
		senderName = displayName(m.From)
	}
	chatName := m.Chat.Title
	if !isGroup {
		chatName = senderName
	}
	thread := ""
	if m.MessageThreadID != 0 {
		thread = strconv.FormatInt(m.MessageThreadID, 10)
	}
	text := strings.TrimSpace(m.Text)
	return channels.Inbound{
		Channel:    "telegram",
		ExternalID: chatID,
		ThreadID:   thread,
		SenderID:   senderID,
		SenderName: senderName,
		ChatName:   chatName,
		// message_id is unique per chat only — the composite id is what
		// ClaimInbound keys on, so dedupe survives identical ids in
		// different chats.
		MessageID:  chatID + ":" + strconv.FormatInt(m.MessageID, 10),
		Text:       text,
		References: c.inlinePickReferences(m, text),
		IsGroup:    isGroup,
		MentionsMe: c.mentionsMe(m),
		Timestamp:  m.Date * 1000,
	}
}

// toInboundMedia maps a message and downloads whatever media it carries.
// Captions become the prompt text; a failed download becomes the same
// bracketed marker WhatsApp uses so the agent knows an attachment was lost.
func (c *Channel) toInboundMedia(ctx context.Context, m *tgMessage) (channels.Inbound, bool) {
	in := c.toInbound(m)
	text := in.Text
	if caption := strings.TrimSpace(m.Caption); caption != "" {
		text = joinText(text, caption)
	}
	var attachments []channels.Attachment
	for _, cand := range mediaCandidates(m) {
		att, marker := c.downloadInboundMedia(ctx, m, cand)
		if marker != "" {
			text = joinText(text, marker)
			continue
		}
		attachments = append(attachments, att)
	}
	in.Text = strings.TrimSpace(text)
	in.Attachments = attachments
	return in, in.Text != "" || len(attachments) > 0
}

// displayName is "first last" with the @username appended when the account
// has one — the username is how the operator recognises who to allow.
func displayName(u *tgUser) string {
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if u.Username != "" {
		if name == "" {
			return "@" + u.Username
		}
		return name + " (@" + u.Username + ")"
	}
	return name
}

// recordBlocked keeps a bounded ring of allowlist rejections for the
// settings panel, deduped on sender+chat so a chatty stranger cannot flood it.
// The ring lock is released before publish: publish -> status reads the ring
// itself, and Go mutexes are not reentrant.
func (c *Channel) recordBlocked(m *tgMessage, senderID, chatID string, isGroup bool) {
	entry := blockedEntry{
		ID:       senderID,
		Chat:     chatID,
		IsGroup:  isGroup,
		ChatName: m.Chat.Title,
	}
	if m.From != nil {
		entry.Name = displayName(m.From)
		entry.Username = m.From.Username
	}
	c.blockedMu.Lock()
	for _, e := range c.blocked {
		if e.ID == entry.ID && e.Chat == entry.Chat {
			c.blockedMu.Unlock()
			return
		}
	}
	entry.Timestamp = time.Now().UnixMilli()
	c.blocked = append(c.blocked, entry)
	if len(c.blocked) > blockedListLimit {
		c.blocked = c.blocked[len(c.blocked)-blockedListLimit:]
	}
	c.blockedMu.Unlock()
	c.publish()
}

func (c *Channel) blockedList() []blockedEntry {
	c.blockedMu.Lock()
	defer c.blockedMu.Unlock()
	out := make([]blockedEntry, len(c.blocked))
	copy(out, c.blocked)
	return out
}

/* ---------------- outbound ---------------- */

func parseTarget(target channels.Target) (chatID, threadID int64, err error) {
	raw := strings.TrimSpace(target.ExternalID)
	if raw == "" {
		return 0, 0, errors.New("missing Telegram chat id")
	}
	chatID, err = strconv.ParseInt(raw, 10, 64)
	if err != nil || chatID == 0 {
		return 0, 0, fmt.Errorf("invalid Telegram chat id %q", raw)
	}
	if t := strings.TrimSpace(target.ThreadID); t != "" {
		threadID, _ = strconv.ParseInt(t, 10, 64)
	}
	return chatID, threadID, nil
}

// maxSendRetryAfter bounds a send retry driven by a 429 hint: a long wait
// would outlive the caller's delivery budget, and re-uploading a large file
// after it is worse than reporting the failure.
const maxSendRetryAfter = 10 * time.Second

// waitRetryAfter sleeps out a small 429 hint when ctx still has room. It
// reports whether the caller should retry once.
func (c *Channel) waitRetryAfter(ctx context.Context, err error) bool {
	delay, ok := retryAfterDelay(err)
	if !ok || delay > maxSendRetryAfter {
		return false
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= delay {
		return false
	}
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// sendText delivers one HTML-formatted message. message_thread_id is carried
// so forum-topic replies land back in their own topic instead of merging into
// the group's main stream. Command replies, /help and approval pings all use
// this path, so the sanitizer protects every outgoing text. If Telegram still
// refuses the entities, the original text is sent unparsed rather than lost.
func (c *Channel) sendText(ctx context.Context, chatID, threadID int64, text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		text = "Done."
	}
	text = textfmt.TruncateRunes(text, maxMessageUnits)
	// The rune cap above keeps the source sane; Telegram counts UTF-16 code
	// units, so astral emoji need the second cut before sanitizing.
	text = truncateUTF16(text, maxMessageUnits)
	client := c.clientCopy()
	if client == nil {
		return "", errors.New("Telegram bot is not connected")
	}
	m, err := client.sendMessage(ctx, chatID, threadID, sanitizeHTML(text), "HTML")
	if err != nil && c.waitRetryAfter(ctx, err) {
		m, err = client.sendMessage(ctx, chatID, threadID, sanitizeHTML(text), "HTML")
	}
	if err != nil && isParseError(err) {
		c.log.Warn("Telegram rejected the message HTML; resending it unparsed", "error", err)
		m, err = client.sendMessage(ctx, chatID, threadID, text, "")
	}
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(chatID, 10) + ":" + strconv.FormatInt(m.MessageID, 10), nil
}

func (c *Channel) Begin(ctx context.Context, target channels.Target, text string) (channels.Delivery, error) {
	chatID, threadID, err := parseTarget(target)
	if err != nil {
		return nil, err
	}
	if !c.settingsSnapshot().Enabled {
		return nil, errors.New("Telegram channel is disabled")
	}
	d := &delivery{ch: c, chat: chatID, thread: threadID}
	d.startTyping()
	return d, nil
}

func (c *Channel) Notify(ctx context.Context, target channels.Target, text string) error {
	chatID, threadID, err := parseTarget(target)
	if err != nil {
		return err
	}
	_, err = c.sendText(ctx, chatID, threadID, text)
	return err
}

/* ---------------- typing ---------------- */

// telegramTypingEvery refreshes the indicator before Telegram's roughly five
// second expiry. Tests shorten it.
var telegramTypingEvery = 4 * time.Second

func (c *Channel) trackTyping(d *delivery) {
	c.typingMu.Lock()
	c.typing[d] = struct{}{}
	c.typingMu.Unlock()
}

func (c *Channel) untrackTyping(d *delivery) {
	c.typingMu.Lock()
	delete(c.typing, d)
	c.typingMu.Unlock()
}

// stopAllTyping cancels every open delivery's indicator. Called by Disconnect
// and Stop so a stopped channel does not keep calling sendChatAction.
func (c *Channel) stopAllTyping() {
	c.typingMu.Lock()
	list := make([]*delivery, 0, len(c.typing))
	for d := range c.typing {
		list = append(list, d)
	}
	c.typingMu.Unlock()
	for _, d := range list {
		d.pauseTyping()
	}
}

func (c *Channel) sendTyping(ctx context.Context, chatID, threadID int64) bool {
	client := c.clientCopy()
	if client == nil {
		return false
	}
	callCtx, cancel := context.WithTimeout(ctx, tokenValidateTimeout)
	defer cancel()
	return client.sendChatAction(callCtx, chatID, threadID) == nil
}

// keepTypingTelegram shows "typing" until ctx is cancelled or the bot can no
// longer send (disconnected). It never queues a message.
func keepTypingTelegram(ctx context.Context, ch *Channel, chatID, threadID int64) {
	every := telegramTypingEvery
	if every <= 0 {
		every = 4 * time.Second
	}
	if !ch.sendTyping(ctx, chatID, threadID) {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if ctx.Err() != nil {
				return
			}
			if !ch.sendTyping(ctx, chatID, threadID) {
				return
			}
		}
	}
}

// delivery is a turn's outbound handle. Intermediate updates are dropped —
// Telegram gets one message per completed turn, same contract as WhatsApp.
type delivery struct {
	ch     *Channel
	chat   int64
	thread int64

	typingMu   sync.Mutex
	typingStop context.CancelFunc

	idMu sync.Mutex
	last string // "chatID:messageID" of the most recent final send
}

var _ channels.Delivery = (*delivery)(nil)

func (d *delivery) ID() string {
	d.idMu.Lock()
	defer d.idMu.Unlock()
	return d.last
}

func (d *delivery) DeliveredTextLimit() int { return maxMessageUnits }

func (d *delivery) startTyping() {
	ctx, cancel := context.WithCancel(d.ch.lifeCtx)
	d.typingMu.Lock()
	d.typingStop = cancel
	d.typingMu.Unlock()
	d.ch.trackTyping(d)
	go keepTypingTelegram(ctx, d.ch, d.chat, d.thread)
}

func (d *delivery) pauseTyping() {
	d.typingMu.Lock()
	stop := d.typingStop
	d.typingStop = nil
	d.typingMu.Unlock()
	if stop != nil {
		stop()
		d.ch.untrackTyping(d)
	}
}

func (d *delivery) Update(ctx context.Context, text string, final bool) error {
	if !final {
		return nil
	}
	defer d.pauseTyping()
	if strings.TrimSpace(text) == "" {
		return nil
	}
	id, err := d.ch.sendText(ctx, d.chat, d.thread, text)
	if err == nil {
		d.idMu.Lock()
		d.last = id
		d.idMu.Unlock()
	}
	return err
}
