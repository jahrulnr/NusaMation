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
	"google.golang.org/protobuf/proto"

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
}

type groupEntry struct {
	name string
	at   time.Time
}

var _ channels.Channel = (*Channel)(nil)
var _ channels.Controller = (*Channel)(nil)
var _ channels.MediaSender = (*Channel)(nil)

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
		sem: make(chan struct{}, 4), groupCache: map[string]groupEntry{},
	}
	c.settings = c.loadSettings(context.Background())
	return c, nil
}

func (c *Channel) SetHandler(h channels.InboundHandler) { c.handler = h }

func (c *Channel) Name() string  { return "whatsapp" }
func (c *Channel) Label() string { return "WhatsApp" }

func (c *Channel) Start(ctx context.Context) error {
	c.mu.Lock()
	if c.started || c.stopped {
		c.mu.Unlock()
		return nil
	}
	c.started = true
	c.settings = c.loadSettings(ctx)
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
		c.enqueueMessage(v)
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
		c.log.Warn("dropping WhatsApp message because the intake queue is full")
	}
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
	text = strings.TrimSpace(text)
	if text == "" {
		text = "Done."
	}
	text = textfmt.Truncate(text, 30000)
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	if client == nil || !client.IsLoggedIn() {
		return "", errors.New("WhatsApp is not connected")
	}
	resp, err := client.SendMessage(ctx, chat, &waE2E.Message{Conversation: proto.String(text)})
	if err != nil {
		return "", err
	}
	return resp.ID, nil
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
	d := &delivery{ch: c, chat: jid, stream: settings.StreamEdits}
	if !settings.StreamEdits {
		return d, nil
	}
	id, err := c.sendText(ctx, jid, text)
	if err != nil {
		return nil, err
	}
	d.id = id
	if settings.SendTyping {
		go c.presence(jid, types.ChatPresenceComposing)
	}
	return d, nil
}

func (c *Channel) Notify(ctx context.Context, target channels.Target, text string) error {
	jid, err := c.parseTarget(target)
	if err != nil {
		return err
	}
	_, err = c.sendText(ctx, jid, text)
	return err
}

// SendMedia uploads an image, video, audio clip or document to WhatsApp
// servers and delivers it to the target chat.
func (c *Channel) SendMedia(ctx context.Context, target channels.Target, media channels.OutboundMedia) error {
	jid, err := c.parseTarget(target)
	if err != nil {
		return err
	}
	if !c.settingsSnapshot().Enabled {
		return errors.New("WhatsApp channel is disabled")
	}
	data := media.Data
	if len(data) == 0 && strings.TrimSpace(media.Path) != "" {
		data, err = os.ReadFile(media.Path)
		if err != nil {
			return fmt.Errorf("read media %q: %w", media.Path, err)
		}
	}
	if len(data) == 0 {
		return errors.New("media has no data or path")
	}
	if len(data) > maxMediaBytes {
		return fmt.Errorf("media exceeds the %d byte limit", maxMediaBytes)
	}
	mime := strings.TrimSpace(media.MIME)
	if mime == "" {
		mime = http.DetectContentType(data)
	}
	mime = strings.TrimSpace(strings.Split(mime, ";")[0])
	caption := strings.TrimSpace(media.Caption)

	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	if client == nil || !client.IsLoggedIn() {
		return errors.New("WhatsApp is not connected")
	}
	mediaType := mediaTypeFor(mime)
	up, err := client.Upload(ctx, data, mediaType)
	if err != nil {
		return fmt.Errorf("upload media to WhatsApp: %w", err)
	}
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
			Mimetype:      proto.String(mime),
		}
		if caption != "" {
			m.Caption = proto.String(caption)
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
			Mimetype:         proto.String(mime),
			StreamingSidecar: up.StreamingSidecar,
		}
		if caption != "" {
			m.Caption = proto.String(caption)
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
			Mimetype:      proto.String(mime),
		}
		// OGG/OPUS clips render as push-to-talk voice-note bubbles.
		if mime == "audio/ogg" || mime == "audio/opus" {
			m.PTT = proto.Bool(true)
		}
		msg.AudioMessage = m
	default:
		fileName := strings.TrimSpace(media.Name)
		if fileName != "" {
			fileName = filepath.Base(fileName)
		} else if p := strings.TrimSpace(media.Path); p != "" {
			fileName = filepath.Base(p)
		}
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
			Mimetype:      proto.String(mime),
			FileName:      proto.String(fileName),
		}
		if caption != "" {
			m.Caption = proto.String(caption)
		}
		msg.DocumentMessage = m
	}
	_, err = client.SendMessage(ctx, jid, msg)
	return err
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

type delivery struct {
	ch     *Channel
	chat   types.JID
	id     types.MessageID
	stream bool
}

func (d *delivery) ID() string { return string(d.id) }

func (d *delivery) Update(ctx context.Context, text string, final bool) error {
	if !d.stream {
		if !final {
			return nil
		}
		_, err := d.ch.sendText(ctx, d.chat, text)
		return err
	}
	if strings.TrimSpace(text) == "" {
		text = "Done."
	}
	text = textfmt.Truncate(text, 30000)
	d.ch.mu.Lock()
	client := d.ch.client
	d.ch.mu.Unlock()
	if client == nil || !client.IsLoggedIn() {
		return errors.New("WhatsApp is not connected")
	}
	if d.id == "" {
		id, err := d.ch.sendText(ctx, d.chat, text)
		if err != nil {
			return err
		}
		d.id = id
		return nil
	}
	_, err := client.SendMessage(ctx, d.chat, client.BuildEdit(d.chat, d.id, &waE2E.Message{
		Conversation: proto.String(text),
	}))
	if err != nil && final {
		// Editing can fail on older clients or after a message is too old.
		// Sending a fresh final message is better than leaving "On it…".
		_, sendErr := d.ch.sendText(ctx, d.chat, text)
		if sendErr == nil {
			d.ch.presence(d.chat, types.ChatPresencePaused)
			return nil
		}
		return err
	}
	if final {
		d.ch.presence(d.chat, types.ChatPresencePaused)
	}
	return nil
}

var _ channels.Delivery = (*delivery)(nil)
