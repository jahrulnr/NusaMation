package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/skip2/go-qrcode"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	"go.mau.fi/whatsmeow/proto/waE2E"
	wastore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/jahrulnr/nusamation/internal/channels"
	"github.com/jahrulnr/nusamation/internal/config"
	appstore "github.com/jahrulnr/nusamation/internal/store"
	"github.com/jahrulnr/nusamation/internal/textfmt"
)

type Channel struct {
	cfg   config.Config
	store *appstore.Store
	mgr   *channels.Manager
	log   *slog.Logger

	// presence optionally overrides the WhatsApp on-network lookup for
	// allowlist validation; nil uses the live client.
	presenceLookup presenceChecker

	pacer pacer

	mu         sync.Mutex
	container  *sqlstore.Container
	client     *whatsmeow.Client
	handlerID  uint32
	handler    channels.InboundHandler
	settings   Settings
	state      string
	detail     string
	lastErr    string
	qrCode     string
	qrPNG      []byte
	qrPNGCode  string
	qrAt       time.Time
	qrCancel   context.CancelFunc
	lifeCtx    context.Context
	lifeStop   context.CancelFunc
	connecting bool
	started    bool
	stopped    bool
	sem        chan struct{}

	groupMu    sync.Mutex
	groupCache map[string]groupEntry

	blockedMu sync.Mutex
	blocked   []blockedEntry

	pollMu sync.Mutex
	polls  map[string][]string

	noticeMu sync.Mutex
	noticeAt map[string]time.Time
}

type groupEntry struct {
	name string
	at   time.Time
}

// blockedEntry records a recent sender the allowlist rejected, so the web
// panel can surface it with a one-tap allow action.
type blockedEntry struct {
	Sender    string `json:"sender"`
	Chat      string `json:"chat"`
	PushName  string `json:"pushName,omitempty"`
	Timestamp int64  `json:"timestampMs"`
}

const blockedListLimit = 50

var _ channels.Channel = (*Channel)(nil)
var _ channels.Controller = (*Channel)(nil)
var _ channels.MediaSender = (*Channel)(nil)
var _ channels.ResponseStyler = (*Channel)(nil)

// maxReplyRunes is the largest final reply sent as one WhatsApp message. It
// matches the truncation rule in ResponseRules, so the agent knows exactly
// how much of its text arrives.
const maxReplyRunes = 30000

// DeliveredTextLimit is the channel-level form of the delivery's cap so the
// chat layer can size mirrored digests (which use Notify, not a delivery)
// by the same rule the agent was told.
func (c *Channel) DeliveredTextLimit() int { return maxReplyRunes }

func New(st *appstore.Store, cfg config.Config, mgr *channels.Manager, log *slog.Logger) (*Channel, error) {
	if log == nil {
		log = slog.Default()
	}
	dsn := "file:" + cfg.WhatsAppDBPath() + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(8000)&_pragma=journal_mode(WAL)"
	container, err := sqlstore.New(context.Background(), "sqlite", dsn, waLogger{log: log.With("component", "whatsmeow"), module: "wa"})
	if err != nil {
		return nil, fmt.Errorf("open WhatsApp store: %w", err)
	}
	// Show up as "Chrome (NusaMation)" in the phone's linked-devices list.
	version, err := wastore.ParseVersion(config.Version)
	if err != nil {
		version = wastore.WAVersionContainer{0, 1, 0}
	}
	wastore.SetOSInfo(config.AppName, version)
	wastore.DeviceProps.PlatformType = waCompanionReg.DeviceProps_CHROME.Enum()
	ctx, cancel := context.WithCancel(context.Background())
	c := &Channel{
		cfg: cfg, store: st, mgr: mgr, log: log.With("component", "whatsapp"),
		container: container, settings: DefaultSettings(), state: "unpaired", lifeCtx: ctx, lifeStop: cancel,
		sem: make(chan struct{}, 4), groupCache: map[string]groupEntry{}, polls: map[string][]string{},
	}
	c.pacer = *newPacer()
	c.settings = c.loadSettings(context.Background(), failClosedSettings())
	go c.retentionLoop()
	return c, nil
}

// retentionLoop purges indexed WhatsApp messages older than the configured
// retention window (0 disables retention). It checks daily so a settings
// change takes effect without a restart.
func (c *Channel) retentionLoop() {
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		if days := c.settingsSnapshot().RetentionDays; days > 0 {
			c.purgeOlderThan(days)
		}
		select {
		case <-c.lifeCtx.Done():
			return
		case <-t.C:
		}
	}
}

func (c *Channel) purgeOlderThan(days int) {
	cutoff := time.Now().AddDate(0, 0, -days).UnixMilli()
	n, err := c.store.PurgeWhatsAppMessages(context.Background(), cutoff)
	if err != nil {
		c.log.Warn("WhatsApp retention purge failed", "error", err)
	} else if n > 0 {
		c.log.Info("purged indexed WhatsApp messages past retention", "removed", n, "days", days)
	}
}

// Groups lists the joined WhatsApp groups with their JIDs and names, each
// flagged with the current allowlist state so the panel picker renders
// add/remove instead of a copy-JID step.
func (c *Channel) Groups(ctx context.Context) ([]map[string]any, error) {
	client := c.clientCopy()
	if client == nil || !client.IsLoggedIn() {
		return nil, errors.New("WhatsApp is not connected")
	}
	list, err := client.GetJoinedGroups(ctx)
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	for _, g := range c.settingsSnapshot().AllowedGroups {
		allowed[g] = true
	}
	out := make([]map[string]any, 0, len(list))
	for _, g := range list {
		if g == nil || g.JID.IsEmpty() {
			continue
		}
		jid := g.JID.ToNonAD().String()
		out = append(out, map[string]any{
			"jid": jid, "name": g.GroupName.Name,
			"participants": g.ParticipantCount, "allowed": allowed[jid],
		})
	}
	return out, nil
}

func (c *Channel) SetHandler(h channels.InboundHandler) { c.handler = h }

func (c *Channel) Name() string  { return "whatsapp" }
func (c *Channel) Label() string { return "WhatsApp" }

// ResponseRules tells the agent to write replies WhatsApp can render: only
// WhatsApp markup, short messages, files referenced by path so they are
// delivered as attachments, and the explicit media send routes.
func (c *Channel) ResponseRules() string {
	return "WhatsApp markup: *bold*, _italic_, ~strikethrough~, `code`, code blocks, quotes and simple lists. " +
		"Avoid headings, tables, footnotes, HTML and Markdown links; write URLs in full. " +
		"Text over 30,000 characters is truncated. " +
		"Attach a file by writing [name](path) or ![name](path) in the reply; copy the absolute path verbatim from your tools — spaces, backslashes and a leading /C:/ drive are fine (C:\\Users\\me\\shot.png). " +
		"The link is removed and the file arrives as a real attachment. Never convert a photo into a sticker to send it. " +
		"For a send outside the reply use: nusamation whatsapp send with {\"target\":\"…\",\"type\":\"image|video|audio|document\",\"path\":\"…\"}."
}

func (c *Channel) Start(ctx context.Context) error {
	c.mu.Lock()
	if c.started || c.stopped {
		c.mu.Unlock()
		return nil
	}
	c.started = true
	c.settings = c.loadSettings(ctx, c.settings)
	deviceErr := c.ensureClientLocked(ctx)
	c.mu.Unlock()
	if deviceErr != nil {
		c.setError(deviceErr)
		return deviceErr
	}
	c.publish()
	s := c.settingsSnapshot()
	c.mu.Lock()
	paired := c.client != nil && c.client.Store.ID != nil
	c.mu.Unlock()
	if s.Enabled && s.AutoStart && paired {
		c.log.Info("auto-starting WhatsApp channel")
		if err := c.Connect(context.Background()); err != nil {
			c.log.Warn("WhatsApp auto-start failed", "error", err)
		}
	} else if !paired {
		c.setState("unpaired", "Open Channels → WhatsApp and scan the QR code to link this device.")
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
	c.lifeStop()
	c.pacerStop()
	if c.qrCancel != nil {
		c.qrCancel()
	}
	if c.client != nil {
		c.client.Disconnect()
	}
	cont := c.container
	c.container = nil
	c.client = nil
	c.mu.Unlock()
	if cont != nil {
		_ = cont.Close()
	}
	return nil
}

func (c *Channel) ensureClientLocked(ctx context.Context) error {
	if c.container == nil {
		return errors.New("WhatsApp store is closed")
	}
	if c.client != nil {
		return nil
	}
	device, err := c.container.GetFirstDevice(ctx)
	if err != nil {
		return err
	}
	client := whatsmeow.NewClient(device, waLogger{log: c.log, module: "wa"})
	client.EnableAutoReconnect = true
	c.client = client
	c.handlerID = client.AddEventHandler(c.handleEvent)
	if device.ID != nil {
		c.state = "offline"
		c.detail = "Linked as +" + device.ID.User + ". Connect to start receiving messages."
	} else {
		c.state = "unpaired"
		c.detail = "Scan a QR code to link WhatsApp."
	}
	return nil
}

func (c *Channel) Connect(ctx context.Context) error {
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return errors.New("WhatsApp channel is stopped")
	}
	if !c.settings.Enabled {
		c.mu.Unlock()
		return errors.New("enable WhatsApp in channel settings first")
	}
	if err := c.ensureClientLocked(ctx); err != nil {
		c.mu.Unlock()
		return err
	}
	client := c.client
	if client == nil {
		c.mu.Unlock()
		return errors.New("WhatsApp client is not available")
	}
	if client.IsConnected() {
		c.state = "connected"
		c.mu.Unlock()
		c.publish()
		return nil
	}
	if c.connecting {
		c.mu.Unlock()
		return nil
	}
	c.connecting = true
	c.qrCode = ""
	c.qrPNG = nil
	c.qrPNGCode = ""
	c.state = "connecting"
	c.detail = "Connecting to WhatsApp…"
	c.mu.Unlock()
	c.publish()
	go c.connectLoop(client)
	return nil
}

func (c *Channel) connectLoop(client *whatsmeow.Client) {
	ctx, cancel := context.WithCancel(c.lifeCtx)
	c.mu.Lock()
	if c.qrCancel != nil {
		c.qrCancel()
	}
	c.qrCancel = cancel
	c.mu.Unlock()
	defer cancel()

	if client.Store.ID != nil {
		if err := client.Connect(); err != nil {
			if client.IsConnected() {
				c.setState("connected", "")
			} else {
				c.finishConnect()
				c.setError(fmt.Errorf("connect to WhatsApp: %w", err))
			}
			return
		}
		c.setState("connected", "")
		return
	}

	qrChan, err := client.GetQRChannel(ctx)
	if err != nil {
		c.finishConnect()
		c.setError(fmt.Errorf("start QR pairing: %w", err))
		return
	}
	if err := client.Connect(); err != nil {
		if client.IsConnected() {
			c.setState("connected", "")
		} else {
			c.finishConnect()
			c.setError(fmt.Errorf("connect to WhatsApp: %w", err))
		}
		return
	}
	for item := range qrChan {
		switch item.Event {
		case "code":
			c.mu.Lock()
			c.qrCode = item.Code
			c.qrAt = time.Now()
			c.qrPNG = nil
			c.qrPNGCode = ""
			c.state = "pairing"
			c.detail = "Scan this QR code from WhatsApp → Linked devices."
			c.lastErr = ""
			c.mu.Unlock()
			c.publish()
		case "success":
			c.mu.Lock()
			c.qrCode = ""
			c.qrPNG = nil
			c.qrPNGCode = ""
			c.state = "connected"
			c.detail = ""
			c.lastErr = ""
			c.connecting = false
			c.mu.Unlock()
			c.publish()
		case "timeout":
			c.finishConnect()
			c.setState("unpaired", "Pairing timed out. Start pairing again to get a fresh QR code.")
		case "error":
			c.finishConnect()
			msg := "pairing failed"
			if item.Error != nil {
				msg = item.Error.Error()
			}
			c.setError(errors.New(msg))
		case "err-unexpected-state":
			c.finishConnect()
			c.setState("connected", "WhatsApp is already paired.")
		case "err-client-outdated":
			c.finishConnect()
			c.setError(errors.New("WhatsApp web client is outdated; update NusaMation"))
		case "err-scanned-without-multidevice":
			c.finishConnect()
			c.setState("unpaired", "Enable linked devices on your phone, then scan again.")
		default:
			c.log.Debug("whatsapp QR event", "event", item.Event, "error", item.Error)
		}
	}
	c.finishConnect()
	if client.IsLoggedIn() {
		c.setState("connected", "")
	} else if c.status().State == "pairing" {
		c.setState("unpaired", "Pairing ended.")
	}
}

func (c *Channel) finishConnect() {
	c.mu.Lock()
	c.connecting = false
	c.qrCode = ""
	c.qrPNG = nil
	c.qrPNGCode = ""
	c.mu.Unlock()
	c.publish()
}

func (c *Channel) Disconnect() error {
	c.mu.Lock()
	if c.qrCancel != nil {
		c.qrCancel()
		c.qrCancel = nil
	}
	if c.client != nil {
		c.client.Disconnect()
	}
	c.connecting = false
	c.qrCode = ""
	c.qrPNG = nil
	c.qrPNGCode = ""
	c.state = "disconnected"
	c.detail = "Disconnected. Connect again to resume."
	c.mu.Unlock()
	c.publish()
	return nil
}

func (c *Channel) Logout(ctx context.Context) error {
	c.mu.Lock()
	if c.qrCancel != nil {
		c.qrCancel()
		c.qrCancel = nil
	}
	client := c.client
	c.mu.Unlock()
	if client != nil {
		loCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := client.Logout(loCtx)
		cancel()
		if err != nil {
			_ = client.Store.Delete(context.Background())
		}
		client.Disconnect()
	}
	c.mu.Lock()
	c.client = nil
	c.handlerID = 0
	c.state = "unpaired"
	c.detail = "Device unlinked. Scan a new QR code to pair again."
	c.qrCode = ""
	c.qrPNG = nil
	c.qrPNGCode = ""
	c.connecting = false
	if err := c.ensureClientLocked(context.Background()); err != nil {
		c.lastErr = err.Error()
	}
	c.mu.Unlock()
	c.publish()
	return nil
}

func (c *Channel) QR(ctx context.Context) ([]byte, error) {
	c.mu.Lock()
	code := c.qrCode
	if code == "" {
		c.mu.Unlock()
		return nil, errors.New("no pairing QR is available; press Connect to start pairing")
	}
	if c.qrPNGCode == code && len(c.qrPNG) > 0 {
		png := c.qrPNG
		c.mu.Unlock()
		return png, nil
	}
	c.mu.Unlock()
	png, err := qrcode.Encode(code, qrcode.Medium, 512)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.qrCode == code {
		c.qrPNG = png
		c.qrPNGCode = code
	}
	c.mu.Unlock()
	return png, nil
}

func (c *Channel) Status() channels.Status { return c.status() }

func (c *Channel) setState(state, detail string) {
	c.mu.Lock()
	c.state = state
	c.detail = detail
	c.connecting = false
	c.mu.Unlock()
	c.publish()
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
	c.qrCode = ""
	c.qrPNG = nil
	c.mu.Unlock()
	c.log.Warn("WhatsApp error", "error", err)
	c.publish()
}

func (c *Channel) publish() {
	if c.mgr == nil {
		return
	}
	c.mgr.PublishStatus(c.status())
}

func (c *Channel) handleEvent(evt any) {
	switch v := evt.(type) {
	case *events.Message:
		chatName := c.cheapChatName(v.Info)
		if err := c.indexMessage(context.Background(), v, chatName, true); err != nil {
			c.log.Warn("could not index WhatsApp message", "error", err)
		}
		c.enqueueMessage(v)
	case *events.UndecryptableMessage:
		// The payload is ciphertext, so it can be neither indexed nor
		// routed — but a message that silently vanishes looks like the bot
		// ignoring someone. Name the sender and chat at info level so a
		// failing session or missing key is diagnosable, then surface it on
		// the owning conversation (rate-limited to one notice per chat).
		c.log.Info("WhatsApp message could not be decrypted",
			"sender", v.Info.Sender.String(), "chat", v.Info.Chat.String(),
			"id", string(v.Info.ID), "unavailable", v.IsUnavailable, "type", string(v.UnavailableType))
		c.notifyUndecryptable(v.Info)
	case *events.Receipt:
		c.handleReceipt(v)
	case *events.HistorySync:
		go c.indexHistory(v)
	case *events.Connected:
		c.mu.Lock()
		c.state = "connected"
		c.detail = ""
		c.lastErr = ""
		c.connecting = false
		c.qrCode = ""
		c.qrPNG = nil
		c.mu.Unlock()
		c.publish()
		c.log.Info("WhatsApp connected", "self", c.selfID())
	case *events.Disconnected:
		c.mu.Lock()
		stopped := c.stopped
		if !stopped {
			c.state = "reconnecting"
			c.detail = "Connection lost; WhatsApp will reconnect automatically."
		}
		c.mu.Unlock()
		if !stopped {
			c.publish()
		}
	case *events.LoggedOut:
		c.mu.Lock()
		c.state = "unpaired"
		c.detail = "This WhatsApp device was unlinked from the phone."
		c.qrCode = ""
		c.qrPNG = nil
		c.mu.Unlock()
		c.publish()
	case *events.StreamReplaced:
		c.mu.Lock()
		c.state = "disconnected"
		c.detail = "Another process connected with the same WhatsApp session."
		c.mu.Unlock()
		c.publish()
	case *events.ConnectFailure:
		c.setError(fmt.Errorf("WhatsApp connection failure: %s", v.Message))
	case *events.QRScannedWithoutMultidevice:
		c.setState("pairing", "Enable linked devices / multi-device on your phone, then scan the QR again.")
	case *events.TemporaryBan:
		c.setError(fmt.Errorf("WhatsApp temporarily banned this connection: %s", v.String()))
	}
}

func (c *Channel) enqueueMessage(evt *events.Message) {
	// Gate synchronously so a rejected or uninteresting message never occupies
	// an intake slot, and a dropped message can still be answered.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if !c.gateInbound(ctx, evt) {
		cancel()
		return
	}
	cancel()
	select {
	case c.sem <- struct{}{}:
		go func() {
			defer func() { <-c.sem }()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			if err := c.handleMessage(ctx, evt); err != nil {
				c.log.Warn("failed to process WhatsApp message", "error", err)
			}
		}()
	default:
		c.log.Info("WhatsApp intake queue is full; the sender is told the assistant is busy",
			"chat", evt.Info.Chat.String(), "sender", evt.Info.Sender.String())
		go c.notifyBusy(evt.Info.Chat)
	}
}

// notifyBusy answers an allowed sender whose message could not be taken —
// media downloads can hold the intake slots for up to 90 s each, so silence
// would look like the agent ignoring them.
func (c *Channel) notifyBusy(chat types.JID) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, _ = c.sendText(ctx, chat, "The assistant is still working on the previous message. Please try again in a moment.")
}

func (c *Channel) selfID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client == nil || c.client.Store.ID == nil {
		return ""
	}
	return "+" + c.client.Store.ID.User
}

func (c *Channel) sendText(ctx context.Context, chat types.JID, text string) (types.MessageID, error) {
	return c.sendTextMention(ctx, chat, text, nil)
}

// sendTextMention sends plain text; in a group chat an optional mention JID is
// attached as ContextInfo.MentionedJID so WhatsApp highlights the recipient.
// The actual send runs through the per-chat pacing queue.
func (c *Channel) sendTextMention(ctx context.Context, chat types.JID, text string, mention *types.JID) (types.MessageID, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		text = "Done."
	}
	text = textfmt.TruncateRunes(text, maxReplyRunes)
	if client := c.clientCopy(); client == nil || !client.IsLoggedIn() {
		return "", errors.New("WhatsApp is not connected")
	}
	var id types.MessageID
	err := c.pacerSend(ctx, chat.String(), func(sendCtx context.Context) error {
		client := c.clientCopy()
		if client == nil || !client.IsLoggedIn() {
			return errors.New("WhatsApp is not connected")
		}
		var msg *waE2E.Message
		if mention != nil && !mention.IsEmpty() && chat.Server == types.GroupServer {
			msg = &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
				Text:        new(text),
				ContextInfo: &waE2E.ContextInfo{MentionedJID: []string{mention.String()}},
			}}
		} else {
			msg = &waE2E.Message{Conversation: new(text)}
		}
		resp, err := client.SendMessage(sendCtx, chat, msg)
		if err != nil {
			return err
		}
		c.indexSent(sendCtx, chat, resp, msg, "")
		id = resp.ID
		return nil
	})
	return id, err
}

func (c *Channel) parseTarget(target channels.Target) (types.JID, error) {
	raw := strings.TrimSpace(target.ExternalID)
	if raw == "" {
		return types.JID{}, errors.New("missing WhatsApp chat id")
	}
	if !strings.Contains(raw, "@") {
		raw += "@s.whatsapp.net"
	}
	jid, err := types.ParseJID(raw)
	if err != nil {
		return types.JID{}, fmt.Errorf("invalid WhatsApp JID: %w", err)
	}
	return jid, nil
}

func (c *Channel) Begin(ctx context.Context, target channels.Target, text string) (channels.Delivery, error) {
	jid, err := c.parseTarget(target)
	if err != nil {
		return nil, err
	}
	settings := c.settingsSnapshot()
	if !settings.Enabled {
		return nil, errors.New("WhatsApp channel is disabled")
	}
	d := &delivery{ch: c, chat: jid, sender: target.SenderID}
	if settings.SendTyping {
		d.startTyping()
	}
	return d, nil
}

func (c *Channel) Notify(ctx context.Context, target channels.Target, text string) error {
	jid, err := c.parseTarget(target)
	if err != nil {
		return err
	}
	_, err = c.sendTextMention(ctx, jid, text, mentionJID(jid, target.SenderID))
	return err
}

// mentionJID resolves the reply's target sender so group replies can @mention
// them; nil outside groups or with an unusable sender id.
func mentionJID(chat types.JID, sender string) *types.JID {
	if sender == "" || chat.Server != types.GroupServer {
		return nil
	}
	jid, err := types.ParseJID(sender)
	if err != nil || jid.IsEmpty() {
		return nil
	}
	return &jid
}

// SendPoll delivers a native WhatsApp poll so agent option questions
// (approvals, single/multi-select input) become tappable votes. The sent
// poll's options are cached so inbound votes decode back to names.
func (c *Channel) SendPoll(ctx context.Context, target channels.Target, question string, options []string, maxSelectable int) (string, error) {
	jid, err := c.parseTarget(target)
	if err != nil {
		return "", err
	}
	if !c.settingsSnapshot().Enabled {
		return "", errors.New("WhatsApp channel is disabled")
	}
	if len(options) == 0 {
		return "", errors.New("poll needs at least one option")
	}
	client := c.clientCopy()
	if client == nil || !client.IsLoggedIn() {
		return "", errors.New("WhatsApp is not connected")
	}
	// Trim once so the wire options match the names we cache for vote
	// hash-matching exactly.
	names := make([]string, 0, len(options))
	for _, o := range options {
		if o = strings.TrimSpace(o); o != "" {
			names = append(names, o)
		}
	}
	if len(names) == 0 {
		return "", errors.New("poll options are all empty")
	}
	if client := c.clientCopy(); client == nil || !client.IsLoggedIn() {
		return "", errors.New("WhatsApp is not connected")
	}
	var id string
	err = c.pacerSend(ctx, jid.String(), func(sendCtx context.Context) error {
		msg := client.BuildPollCreation(question, names, maxSelectable)
		resp, err := client.SendMessage(sendCtx, jid, msg)
		if err != nil {
			return err
		}
		c.indexSent(sendCtx, jid, resp, msg, "")
		c.cachePollOptions(jid.String(), resp.ID, names)
		// Persist the option names so a vote still decodes — and a stale tap
		// can still be answered — after a restart.
		if c.store != nil {
			if err := c.store.SetJSONSetting(sendCtx, sentPollKey(jid.ToNonAD().String(), resp.ID), names); err != nil {
				c.log.Warn("could not persist poll options", "error", err)
			}
		}
		id = resp.ID
		return nil
	})
	return id, err
}

// SendMedia uploads an image, video, audio clip or document to WhatsApp
// servers and delivers it to the target chat.
func (c *Channel) SendMedia(ctx context.Context, target channels.Target, media channels.OutboundMedia) error {
	_, err := c.sendMedia(ctx, target, media)
	return err
}

// sendMedia is SendMedia with the sent message id, so an explicit
// `whatsapp send` of a media type can report it.
func (c *Channel) sendMedia(ctx context.Context, target channels.Target, media channels.OutboundMedia) (string, error) {
	jid, err := c.parseTarget(target)
	if err != nil {
		return "", err
	}
	if !c.settingsSnapshot().Enabled {
		return "", errors.New("WhatsApp channel is disabled")
	}
	data := media.Data
	if len(data) == 0 && strings.TrimSpace(media.Path) != "" {
		// Check the size before reading so an oversized file never occupies
		// memory; the explicit send path is not workspace-confined (unlike
		// reply attachments) and is documented as server-readable paths.
		if info, err := os.Stat(media.Path); err == nil && info.Size() > maxMediaBytes {
			return "", fmt.Errorf("media exceeds the %d byte limit", maxMediaBytes)
		}
		data, err = os.ReadFile(media.Path)
		if err != nil {
			return "", fmt.Errorf("read media %q: %w", media.Path, err)
		}
	}
	if len(data) == 0 {
		return "", errors.New("media has no data or path")
	}
	if len(data) > maxMediaBytes {
		return "", fmt.Errorf("media exceeds the %d byte limit", maxMediaBytes)
	}
	mime := strings.TrimSpace(media.MIME)
	if mime == "" {
		mime = http.DetectContentType(data)
	}
	mime = strings.TrimSpace(strings.Split(mime, ";")[0])
	caption := strings.TrimSpace(media.Caption)
	name := strings.TrimSpace(media.Name)
	if name != "" {
		name = filepath.Base(name)
	} else if p := strings.TrimSpace(media.Path); p != "" {
		name = filepath.Base(p)
	}

	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	if client == nil || !client.IsLoggedIn() {
		return "", errors.New("WhatsApp is not connected")
	}
	mediaType := mediaTypeFor(mime)
	var id string
	err = c.pacerSend(ctx, jid.String(), func(sendCtx context.Context) error {
		sent, err := c.deliverMedia(sendCtx, jid, client, data, mediaType, mime, caption, name)
		id = sent
		return err
	})
	return id, err
}

// deliverMedia uploads and sends one attachment through the media client. It
// takes mediaClient rather than *whatsmeow.Client so the message construction
// and send call are testable without a WhatsApp connection.
func (c *Channel) deliverMedia(ctx context.Context, jid types.JID, client mediaClient, data []byte, mediaType whatsmeow.MediaType, mime, caption, name string) (string, error) {
	up, err := client.Upload(ctx, data, mediaType)
	if err != nil {
		return "", fmt.Errorf("upload media to WhatsApp: %w", err)
	}
	msg := mediaMessage(up, mediaType, mime, caption, name)
	resp, err := client.SendMessage(ctx, jid, msg)
	if err == nil {
		c.indexSent(ctx, jid, resp, msg, "")
	}
	return resp.ID, err
}

// mediaClient is the slice of *whatsmeow.Client the media path needs. Keeping
// it an interface lets tests assert the built waE2E message without a real
// WhatsApp connection.
type mediaClient interface {
	Upload(ctx context.Context, plaintext []byte, appInfo whatsmeow.MediaType) (whatsmeow.UploadResponse, error)
	SendMessage(ctx context.Context, to types.JID, message *waE2E.Message, extra ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error)
}

// mediaMessage builds the protobuf message for an uploaded attachment. The
// attachment class follows the MIME type, so a PNG arrives as a photo and an
// unknown type as a document with a filename.
func mediaMessage(up whatsmeow.UploadResponse, mediaType whatsmeow.MediaType, mime, caption, name string) *waE2E.Message {
	msg := &waE2E.Message{}
	switch mediaType {
	case whatsmeow.MediaImage:
		m := &waE2E.ImageMessage{
			URL:           &up.URL,
			DirectPath:    &up.DirectPath,
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    &up.FileLength,
			Mimetype:      new(mime),
		}
		if caption != "" {
			m.Caption = new(caption)
		}
		msg.ImageMessage = m
	case whatsmeow.MediaVideo:
		m := &waE2E.VideoMessage{
			URL:              &up.URL,
			DirectPath:       &up.DirectPath,
			MediaKey:         up.MediaKey,
			FileEncSHA256:    up.FileEncSHA256,
			FileSHA256:       up.FileSHA256,
			FileLength:       &up.FileLength,
			Mimetype:         new(mime),
			StreamingSidecar: up.StreamingSidecar,
		}
		if caption != "" {
			m.Caption = new(caption)
		}
		msg.VideoMessage = m
	case whatsmeow.MediaAudio:
		m := &waE2E.AudioMessage{
			URL:           &up.URL,
			DirectPath:    &up.DirectPath,
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    &up.FileLength,
			Mimetype:      new(mime),
		}
		// OGG/OPUS clips render as push-to-talk voice-note bubbles.
		if mime == "audio/ogg" || mime == "audio/opus" {
			m.PTT = new(true)
		}
		msg.AudioMessage = m
	default:
		fileName := strings.TrimSpace(name)
		if fileName == "" {
			fileName = "file"
		}
		m := &waE2E.DocumentMessage{
			URL:           &up.URL,
			DirectPath:    &up.DirectPath,
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    &up.FileLength,
			Mimetype:      new(mime),
			FileName:      new(fileName),
		}
		if caption != "" {
			m.Caption = new(caption)
		}
		msg.DocumentMessage = m
	}
	return msg
}

func mediaTypeFor(mime string) whatsmeow.MediaType {
	switch {
	case strings.HasPrefix(mime, "image/"):
		return whatsmeow.MediaImage
	case strings.HasPrefix(mime, "video/"):
		return whatsmeow.MediaVideo
	case strings.HasPrefix(mime, "audio/"):
		return whatsmeow.MediaAudio
	default:
		return whatsmeow.MediaDocument
	}
}

func (c *Channel) presence(jid types.JID, state types.ChatPresence) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	if client == nil || !client.IsLoggedIn() {
		return
	}
	_ = client.SendChatPresence(ctx, jid, state, types.ChatPresenceMediaText)
}

// whatsAppTypingTTL is how long a composing presence stays on screen before
// WhatsApp clears it. The delivery refreshes inside that window.
const whatsAppTypingTTL = 10 * time.Second

type delivery struct {
	ch         *Channel
	chat       types.JID
	sender     string
	typingMu   sync.Mutex
	typingStop context.CancelFunc

	idMu sync.Mutex
	last string // message id of the most recent final send on this delivery
}

// startTyping shows composing for as long as this delivery is open. WhatsApp
// drops the indicator on its own, so it is refreshed until pauseTyping.
func (d *delivery) startTyping() {
	ctx, cancel := context.WithCancel(context.Background())
	d.typingMu.Lock()
	d.typingStop = cancel
	d.typingMu.Unlock()
	go keepTyping(ctx, whatsAppTypingTTL, func(state types.ChatPresence) {
		d.ch.presence(d.chat, state)
	})
}

func (d *delivery) pauseTyping() {
	d.typingMu.Lock()
	stop := d.typingStop
	d.typingStop = nil
	d.typingMu.Unlock()
	if stop != nil {
		stop()
	}
}

func keepTyping(ctx context.Context, every time.Duration, beat func(types.ChatPresence)) {
	if every <= 0 {
		every = whatsAppTypingTTL
	}
	beat(types.ChatPresenceComposing)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			beat(types.ChatPresencePaused)
			return
		case <-t.C:
			if ctx.Err() != nil {
				beat(types.ChatPresencePaused)
				return
			}
			beat(types.ChatPresenceComposing)
		}
	}
}

// ID returns the WhatsApp message id of the most recent final send on this
// delivery — empty until the first send succeeds. The chat layer records it
// on the turn's final assistant message so receipts can find it.
func (d *delivery) ID() string {
	d.idMu.Lock()
	defer d.idMu.Unlock()
	return d.last
}

// DeliveredTextLimit declares the per-message cap the chat layer truncates
// the final reply to. It matches the rule ResponseRules gives the agent, so
// nothing the agent was allowed to write is silently dropped.
func (d *delivery) DeliveredTextLimit() int { return maxReplyRunes }

func (d *delivery) Update(ctx context.Context, text string, final bool) error {
	if !final {
		return nil
	}
	defer d.pauseTyping()
	if strings.TrimSpace(text) == "" {
		return nil
	}
	id, err := d.ch.sendTextMention(ctx, d.chat, text, mentionJID(d.chat, d.sender))
	if err == nil && id != "" {
		d.idMu.Lock()
		d.last = string(id)
		d.idMu.Unlock()
	}
	return err
}

var _ channels.Delivery = (*delivery)(nil)
