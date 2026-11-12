package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/jahrulnr/nusamation/internal/browser"
)

// handleBrowserStream mirrors handleAuthTerminal: cookie auth via s.protected
// (GET, no CSRF), sameOrigin re-checked here, one writer goroutine owns all
// conn.Write calls (coder/websocket forbids concurrent writes).
func (s *Server) handleBrowserStream(w http.ResponseWriter, r *http.Request) {
	if !s.sameOrigin(r) {
		writeError(w, http.StatusForbidden, "bad_origin", "Request origin is not allowed.")
		return
	}
	if s.browser == nil {
		writeError(w, http.StatusServiceUnavailable, "browser_unavailable", "The embedded browser is not enabled.")
		return
	}
	sess, err := s.browser.Get(r.PathValue("id"))
	if err != nil {
		s.browserError(w, err, false)
		return
	}
	// Attach only subscribes a viewer — incumbent panes keep their stream.
	// It still runs after the upgrade succeeds so a failed handshake never
	// registers a dead viewer against the session.
	switch sess.View(false).Status {
	case "crashed":
		writeError(w, http.StatusServiceUnavailable, "browser_unavailable", "The browser process crashed.")
		return
	case "closed":
		writeError(w, http.StatusNotFound, "not_found", "Browser session not found.")
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true}) // sameOrigin was checked above
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "stream ended")
	sub, err := sess.Attach()
	if err != nil {
		// The socket is already upgraded; report the terminal state on the
		// wire so the client sees the contract's closed message.
		code, msg := "session_closed", "Browser session closed."
		if errors.Is(err, browser.ErrUnavailable) {
			code, msg = "browser_crashed", "The browser process crashed."
		}
		b, _ := json.Marshal(map[string]any{"type": "closed", "code": code, "message": msg})
		wctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		_ = conn.Write(wctx, websocket.MessageText, b)
		cancel()
		return
	}
	defer sess.Detach(sub)
	conn.SetReadLimit(256 << 10)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		s.browserWriteLoop(ctx, conn, sub)
	}()
	go func() {
		select {
		case <-writerDone:
			cancel()
		case <-ctx.Done():
		}
	}()
	for {
		typ, payload, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if typ == websocket.MessageBinary {
			continue
		}
		var msg browserInput
		if json.Unmarshal(payload, &msg) != nil {
			continue
		}
		s.browserInput(sess, sub, msg)
	}
}

type browserInput struct {
	Type            string  `json:"type"`
	Phase           string  `json:"phase"`
	X               float64 `json:"x"`
	Y               float64 `json:"y"`
	Button          string  `json:"button"`
	ClickCount      int64   `json:"clickCount"`
	DeltaX          float64 `json:"deltaX"`
	DeltaY          float64 `json:"deltaY"`
	Text            string  `json:"text"`
	Key             string  `json:"key"`
	Modifiers       int64   `json:"modifiers"`
	Width           int64   `json:"width"`
	Height          int64   `json:"height"`
	Preset          string  `json:"preset"`
	DPR             float64 `json:"dpr"`
	Mobile          *bool   `json:"mobile"`
	UA              *string `json:"ua"`
	Touch           *bool   `json:"touch"`
	KeyboardRequest int64   `json:"keyboardRequest"`
}

func (s *Server) browserInput(sess *browser.Session, sub *browser.Stream, msg browserInput) {
	// Control messages work on any attached stream: a viewer's takeover is
	// how it claims the input lease, and ping is a per-connection keepalive.
	switch msg.Type {
	case "takeover":
		sess.Takeover(sub)
		return
	case "ping":
		b, _ := json.Marshal(map[string]any{"type": "pong", "at": time.Now().UnixMilli()})
		sub.Send(b)
		return
	}
	// Everything else is input: only the controller's stream may drive, and
	// never while the agent holds the input lease.
	if !sess.InputAllowed(sub) {
		return
	}
	var err error
	switch msg.Type {
	case "mousemove":
		err = sess.MouseMove(msg.X, msg.Y)
	case "click":
		err = sess.Click(msg.X, msg.Y, msg.Button, msg.ClickCount)
	case "touch":
		err = sess.Touch(msg.Phase, msg.X, msg.Y)
		if err == nil && msg.Phase == "touchEnd" && msg.KeyboardRequest != 0 {
			var editable bool
			editable, err = sess.FocusedEditable()
			if err == nil {
				b, _ := json.Marshal(map[string]any{"type": "keyboard_focus", "request": msg.KeyboardRequest, "editable": editable})
				sub.Send(b)
			}
		}
	case "wheel":
		err = sess.Wheel(msg.X, msg.Y, msg.DeltaX, msg.DeltaY)
	case "text":
		err = sess.InsertText(msg.Text)
	case "key":
		err = sess.Key(msg.Key, msg.Modifiers)
	case "resize":
		err = sess.Resize(msg.Width, msg.Height)
	case "emulate":
		var w, h *int64
		var dpr *float64
		if msg.Width != 0 {
			w = &msg.Width
		}
		if msg.Height != 0 {
			h = &msg.Height
		}
		if msg.DPR != 0 {
			dpr = &msg.DPR
		}
		_, err = sess.ApplyDevicePatch(msg.Preset, browser.DevicePatch{
			Width: w, Height: h, DPR: dpr, Mobile: msg.Mobile, UserAgent: msg.UA, Touch: msg.Touch,
		})
	default:
		return
	}
	if err != nil {
		s.log.Debug("browser input dispatch failed", "type", msg.Type, "error", err)
	}
}

// browserWriteLoop is the single writer: binary frames and JSON control
// messages share one ordered path, control is preferred so meta precedes the
// frame it applies to, and a protocol Ping runs every 25 s.
func (s *Server) browserWriteLoop(ctx context.Context, conn *websocket.Conn, sub *browser.Stream) {
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	write := func(typ websocket.MessageType, payload []byte) bool {
		wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return conn.Write(wctx, typ, payload) == nil
	}
	drainCtrl := func() bool {
		for {
			select {
			case b := <-sub.Control():
				if !write(websocket.MessageText, b) {
					return false
				}
			default:
				return true
			}
		}
	}
	for {
		select {
		case b := <-sub.Control():
			if !write(websocket.MessageText, b) {
				return
			}
			continue
		default:
		}
		select {
		case b := <-sub.Control():
			if !write(websocket.MessageText, b) {
				return
			}
		case f := <-sub.Frames():
			if !write(websocket.MessageBinary, f) {
				return
			}
		case <-ticker.C:
			pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			_ = conn.Ping(pctx)
			cancel()
		case <-sub.Done():
			// The session sent its terminal {type:"closed"} — flush it, then
			// the handler's deferred Close ends the socket at status 1000.
			drainCtrl()
			return
		case <-ctx.Done():
			return
		}
	}
}
