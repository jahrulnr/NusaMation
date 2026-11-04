package channels

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"sync"

	"github.com/jahrulnr/nusamation/internal/events"
)

type Status struct {
	Name       string         `json:"name"`
	Kind       string         `json:"kind"`
	Label      string         `json:"label"`
	Enabled    bool           `json:"enabled"`
	Connected  bool           `json:"connected"`
	LoggedIn   bool           `json:"loggedIn"`
	State      string         `json:"state"`
	Detail     string         `json:"detail,omitempty"`
	SelfID     string         `json:"selfId,omitempty"`
	QR         string         `json:"qr,omitempty"`
	Manageable bool           `json:"manageable"`
	Stats      map[string]any `json:"stats,omitempty"`
	UpdatedAt  int64          `json:"updatedAt"`
}

type Target struct {
	Channel     string `json:"channel"`
	ExternalID  string `json:"externalId"`
	ThreadID    string `json:"threadId,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	IsGroup     bool   `json:"isGroup,omitempty"`
}

type Attachment struct {
	Name string `json:"name"`
	MIME string `json:"mime"`
	Size int64  `json:"size"`
	Data []byte `json:"-"`
}

type Inbound struct {
	Channel     string       `json:"channel"`
	ExternalID  string       `json:"externalId"`
	ThreadID    string       `json:"threadId,omitempty"`
	SenderID    string       `json:"senderId"`
	SenderName  string       `json:"senderName"`
	ChatName    string       `json:"chatName,omitempty"`
	MessageID   string       `json:"messageId"`
	Text        string       `json:"text"`
	Attachments []Attachment `json:"attachments,omitempty"`
	IsGroup     bool         `json:"isGroup"`
	MentionsMe  bool         `json:"mentionsMe"`
	Timestamp   int64        `json:"timestamp"`
	PollVote    *PollVote    `json:"pollVote,omitempty"`
	Raw         any          `json:"-"`
}

type InboundHandler interface {
	HandleInbound(ctx context.Context, in Inbound) error
}

type Delivery interface {
	ID() string
	Update(ctx context.Context, text string, final bool) error
}

// OutboundMedia is a file the agent asked to deliver to a chat channel.
// Exactly one of Data (in-memory bytes) or Path (local file) must be set.
type OutboundMedia struct {
	Name    string `json:"name,omitempty"`
	MIME    string `json:"mime,omitempty"`
	Caption string `json:"caption,omitempty"`
	Path    string `json:"path,omitempty"`
	Data    []byte `json:"-"`
}

// MediaSender is implemented by channels that can deliver binary attachments
// (WhatsApp, Telegram). The chat service probes for it after the final text.
type MediaSender interface {
	SendMedia(ctx context.Context, target Target, media OutboundMedia) error
}

// ResponseStyler is implemented by channels whose delivery surface has its
// own message format (WhatsApp's limited markup, the web UI's full Markdown).
// The chat service appends the rules to the prompt so the agent writes for
// the surface its reply will be delivered on.
type ResponseStyler interface {
	ResponseRules() string
}

// PollAsker is implemented by channels that can present a multiple-choice
// question as a native poll (WhatsApp). The chat service uses it to turn
// agent option questions — permission requests, single/multi-select input —
// into a tappable vote instead of a text command. maxSelectable=1 renders a
// single-choice poll; higher values allow picking several options. Returns
// the channel-side message id of the sent poll so incoming votes can be
// matched back via Inbound.PollVote.
type PollAsker interface {
	SendPoll(ctx context.Context, target Target, question string, options []string, maxSelectable int) (pollMessageID string, err error)
}

// PollVote carries a decoded vote on a poll the channel itself sent — e.g. an
// approval or question the agent asked through PollAsker. Votes on polls the
// user created arrive as regular Text instead.
type PollVote struct {
	PollMessageID string   `json:"pollMessageId"`
	Selected      []string `json:"selected"`
}

type Channel interface {
	Name() string
	Label() string
	Start(ctx context.Context) error
	Stop() error
	Status() Status
	// Begin starts a streaming outbound message. Channels that cannot edit can
	// return a delivery that only sends on final update.
	Begin(ctx context.Context, target Target, text string) (Delivery, error)
	Notify(ctx context.Context, target Target, text string) error
}

// Controller is implemented by channels that can be paired or configured at
// runtime (WhatsApp, Telegram bots, ...). The HTTP API probes for it.
type Controller interface {
	Connect(ctx context.Context) error
	Disconnect() error
	Logout(ctx context.Context) error
	QR(ctx context.Context) ([]byte, error)
	Settings(ctx context.Context) (any, error)
	UpdateSettings(ctx context.Context, raw json.RawMessage) error
}

type Manager struct {
	mu       sync.RWMutex
	channels map[string]Channel
	log      *slog.Logger
	bus      events.Broadcaster
}

func NewManager(bus events.Broadcaster, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{channels: map[string]Channel{}, log: log.With("component", "channels"), bus: bus}
}

func (m *Manager) Register(c Channel) {
	m.mu.Lock()
	m.channels[c.Name()] = c
	m.mu.Unlock()
}

func (m *Manager) Get(name string) (Channel, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.channels[name]
	return c, ok
}

func (m *Manager) StartAll(ctx context.Context) {
	m.mu.RLock()
	list := make([]Channel, 0, len(m.channels))
	for _, c := range m.channels {
		list = append(list, c)
	}
	m.mu.RUnlock()
	for _, c := range list {
		if err := c.Start(ctx); err != nil {
			m.log.Warn("channel failed to start", "channel", c.Name(), "error", err)
		}
	}
}

func (m *Manager) StopAll() {
	m.mu.RLock()
	list := make([]Channel, 0, len(m.channels))
	for _, c := range m.channels {
		list = append(list, c)
	}
	m.mu.RUnlock()
	for _, c := range list {
		_ = c.Stop()
	}
}

func (m *Manager) Statuses() []Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Status, 0, len(m.channels))
	for _, c := range m.channels {
		out = append(out, c.Status())
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name == "web" {
			return true
		}
		if out[j].Name == "web" {
			return false
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func (m *Manager) PublishStatus(s Status) {
	if m.bus != nil {
		m.bus.Broadcast(events.New("channel.status", "", s))
	}
}
