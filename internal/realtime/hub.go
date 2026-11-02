package realtime

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/jahrulnr/nusamation/internal/events"
)

type Hub struct {
	log     *slog.Logger
	seq     atomic.Uint64
	mu      sync.RWMutex
	clients map[*client]struct{}
	origins map[string]struct{}
}

type client struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte
	done chan struct{}
	once sync.Once
}

func NewHub(log *slog.Logger) *Hub {
	if log == nil {
		log = slog.Default()
	}
	return &Hub{log: log.With("component", "realtime"), clients: map[*client]struct{}{}, origins: map[string]struct{}{}}
}

// AllowOrigin adds an extra origin host (for example a public reverse-proxy
// hostname) that may open an authenticated websocket.
func (h *Hub) AllowOrigin(raw string) {
	u, err := url.Parse(raw)
	if err != nil {
		return
	}
	host := strings.ToLower(u.Host)
	if host == "" {
		return
	}
	h.mu.Lock()
	h.origins[host] = struct{}{}
	h.mu.Unlock()
}

func (h *Hub) Broadcast(ev events.Event) {
	ev.ID = h.seq.Add(1)
	if ev.At == 0 {
		ev.At = time.Now().UnixMilli()
	}
	b, err := json.Marshal(ev)
	if err != nil {
		h.log.Warn("failed to encode event", "type", ev.Type, "error", err)
		return
	}
	h.mu.RLock()
	clients := make([]*client, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.RUnlock()
	for _, c := range clients {
		select {
		case c.send <- b:
		default:
			// A slow browser must not stall the agent's notification loop.
			c.close(websocket.StatusPolicyViolation, "slow consumer")
		}
	}
}

func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

func (h *Hub) Serve(w http.ResponseWriter, r *http.Request) {
	if !h.originAllowed(r) {
		http.Error(w, "websocket origin is not allowed", http.StatusForbidden)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // origin is checked above
		CompressionMode:    websocket.CompressionContextTakeover,
	})
	if err != nil {
		h.log.Debug("websocket upgrade failed", "error", err)
		return
	}
	c := &client{hub: h, conn: conn, send: make(chan []byte, 256), done: make(chan struct{})}
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()

	hello, _ := json.Marshal(events.New("hello", "", map[string]any{
		"version": "0.1.0", "connectedAt": time.Now().UnixMilli(),
	}))
	c.send <- hello

	go c.writeLoop()
	c.readLoop()
}

func (h *Hub) originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // non-browser clients (CLI/tests) do not send Origin
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Host)
	if strings.EqualFold(host, strings.ToLower(r.Host)) {
		return true
	}
	h.mu.RLock()
	_, ok := h.origins[host]
	h.mu.RUnlock()
	return ok
}

func (c *client) writeLoop() {
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-c.done:
			return
		case b := <-c.send:
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err := c.conn.Write(ctx, websocket.MessageText, b)
			cancel()
			if err != nil {
				c.close(websocket.StatusGoingAway, "write failed")
				return
			}
		case <-ping.C:
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err := c.conn.Ping(ctx)
			cancel()
			if err != nil {
				c.close(websocket.StatusGoingAway, "ping failed")
				return
			}
		}
	}
}

func (c *client) readLoop() {
	defer c.close(websocket.StatusNormalClosure, "closed")
	ctx := context.Background()
	for {
		typ, data, err := c.conn.Read(ctx)
		if err != nil {
			return
		}
		if typ == websocket.MessageText {
			var msg struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(data, &msg) == nil && msg.Type == "ping" {
				pong, _ := json.Marshal(map[string]any{"type": "pong", "at": time.Now().UnixMilli()})
				select {
				case c.send <- pong:
				default:
				}
			}
		}
	}
}

func (c *client) close(code websocket.StatusCode, reason string) {
	c.once.Do(func() {
		close(c.done)
		c.hub.mu.Lock()
		delete(c.hub.clients, c)
		c.hub.mu.Unlock()
		_ = c.conn.Close(code, reason)
	})
}
