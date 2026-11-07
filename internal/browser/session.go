package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/inspector"
	"github.com/chromedp/cdproto/overlay"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

const (
	screencastQuality  = int64(65)
	navWait            = 30 * time.Second
	navCheckInterval   = 750 * time.Millisecond
	screenshotSettle   = 600 * time.Millisecond
	streamFrameBacklog = 8
	streamCtrlBacklog  = 16
	ioTimeout          = 5 * time.Second
)

// ioCtx bounds fire-and-forget CDP calls so a wedged renderer cannot stall
// the input read loop or the maint worker indefinitely.
func (s *Session) ioCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(s.tabCtx, ioTimeout)
}

// View is the sessionView wire shape (browser-api-contract §2).
type View struct {
	ID             string `json:"id"`
	ConversationID string `json:"conversationId"`
	URL            string `json:"url"`
	Title          string `json:"title"`
	Status         string `json:"status"`
	Width          int    `json:"width"`
	Height         int    `json:"height"`
	CanGoBack      bool   `json:"canGoBack"`
	CanGoForward   bool   `json:"canGoForward"`
	CreatedAt      int64  `json:"createdAt"`
	Error          string `json:"error"`
}

// NavResult is the navigate/history response shape (contract §3.5/§3.6).
type NavResult struct {
	URL          string `json:"url"`
	Title        string `json:"title"`
	CanGoBack    bool   `json:"canGoBack"`
	CanGoForward bool   `json:"canGoForward"`
}

// Meta is the {type:"meta"} stream message (contract §4.3).
type Meta struct {
	Type            string  `json:"type"`
	DeviceWidth     int64   `json:"deviceWidth"`
	DeviceHeight    int64   `json:"deviceHeight"`
	PageScaleFactor float64 `json:"pageScaleFactor"`
	ScrollOffsetX   float64 `json:"scrollOffsetX"`
	ScrollOffsetY   float64 `json:"scrollOffsetY"`
	URL             string  `json:"url"`
	Title           string  `json:"title"`
	CanGoBack       bool    `json:"canGoBack"`
	CanGoForward    bool    `json:"canGoForward"`
}

// Stream is the per-session screencast subscription a WS handler drains.
// Frames are complete JPEGs; control messages are JSON text payloads.
type Stream struct {
	frames chan []byte
	ctrl   chan []byte
	done   chan struct{}
	once   sync.Once
}

func newStream() *Stream {
	return &Stream{
		frames: make(chan []byte, streamFrameBacklog),
		ctrl:   make(chan []byte, streamCtrlBacklog),
		done:   make(chan struct{}),
	}
}

func (s *Stream) Frames() <-chan []byte  { return s.frames }
func (s *Stream) Control() <-chan []byte { return s.ctrl }
func (s *Stream) Done() <-chan struct{}  { return s.done }

// sendFrame enqueues a JPEG frame, dropping the oldest queued frame when the
// consumer stalls — a stuck tab must not stall Chromium's screencast.
func (s *Stream) sendFrame(data []byte) {
	select {
	case s.frames <- data:
		return
	default:
	}
	select {
	case <-s.frames:
	default:
	}
	select {
	case s.frames <- data:
	default:
	}
}

func (s *Stream) sendCtrl(payload []byte) {
	select {
	case s.ctrl <- payload:
	default:
	}
}

// Send queues a JSON control message (used by the WS handler for pong).
func (s *Stream) Send(payload []byte) { s.sendCtrl(payload) }

// closeWith queues the terminal {type:"closed"} message then releases the
// writer goroutine, which drains the control queue before closing the socket.
func (s *Stream) closeWith(code, message string) {
	s.once.Do(func() {
		b, _ := json.Marshal(map[string]any{"type": "closed", "code": code, "message": message})
		s.sendCtrl(b)
		close(s.done)
	})
}

type Session struct {
	m              *Manager
	id             string
	conversationID string
	createdAt      int64

	mu           sync.Mutex
	url          string
	title        string
	status       string
	errText      string
	width        int
	height       int
	canBack      bool
	canFwd       bool
	lastActivity time.Time
	lastFrame    *page.ScreencastFrameMetadata
	lastMeta     Meta
	lastNavCheck time.Time
	stream       *Stream
	castCtx      context.Context
	castCancel   context.CancelFunc
	overlayReady bool

	busy atomic.Bool

	tabCtx       context.Context
	tabCancel    context.CancelFunc
	browserCtx   context.Context
	browserCtxID cdp.BrowserContextID

	ackCh   chan int64
	maintCh chan func()
}

func (s *Session) ID() string             { return s.id }
func (s *Session) ConversationID() string { return s.conversationID }

func (s *Session) touch() {
	s.mu.Lock()
	s.lastActivity = time.Now()
	s.mu.Unlock()
}

func (s *Session) idleFor(ttl time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stream == nil && time.Since(s.lastActivity) > ttl
}

// View returns the sessionView. When refresh is true and the tab is alive it
// re-reads navigation state first so url/title reflect in-page changes.
func (s *Session) View(refresh bool) View {
	if refresh {
		s.refreshNavState()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.viewLocked()
}

func (s *Session) viewLocked() View {
	return View{
		ID: s.id, ConversationID: s.conversationID, URL: s.url, Title: s.title,
		Status: s.status, Width: s.width, Height: s.height,
		CanGoBack: s.canBack, CanGoForward: s.canFwd,
		CreatedAt: s.createdAt, Error: s.errText,
	}
}

func (s *Session) navResultLocked() *NavResult {
	return &NavResult{URL: s.url, Title: s.title, CanGoBack: s.canBack, CanGoForward: s.canFwd}
}

func (s *Session) aliveLocked() error {
	switch s.status {
	case "crashed":
		return fmt.Errorf("%w: %s", ErrUnavailable, s.errText)
	case "closed":
		return ErrNotFound
	}
	return nil
}

// refreshNavState syncs url/title/history flags from the tab's navigation
// history. Best-effort: failures leave the last-known values in place.
// chromedp calls need the tab context, so it always runs on s.tabCtx.
func (s *Session) refreshNavState() {
	ctx, cancel := context.WithTimeout(s.tabCtx, 10*time.Second)
	res, err := chromedp.Call(ctx, page.GetNavigationHistory, cdp.Empty{})
	cancel()
	if err != nil || len(res.Entries) == 0 {
		return
	}
	idx := int(res.CurrentIndex)
	if idx < 0 || idx >= len(res.Entries) {
		return
	}
	e := res.Entries[idx]
	s.mu.Lock()
	if e.URL != "" {
		s.url = e.URL
	}
	s.title = e.Title
	s.canBack = idx > 0
	s.canFwd = idx < len(res.Entries)-1
	s.mu.Unlock()
	s.maybeMeta()
}

func (s *Session) acquire() error {
	s.mu.Lock()
	err := s.aliveLocked()
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if !s.busy.CompareAndSwap(false, true) {
		return ErrBusy
	}
	s.touch()
	return nil
}

// Navigate commits a navigation and waits for the load event (same-document
// navigations return immediately), then reports the nav state (§3.5).
func (s *Session) Navigate(ctx context.Context, url string) (*NavResult, error) {
	if err := s.acquire(); err != nil {
		return nil, err
	}
	defer s.busy.Store(false)
	loadCh, stopLoad := s.waitLoad()
	defer stopLoad()
	navCtx, cancel := context.WithTimeout(s.tabCtx, navWait)
	res, err := chromedp.Call(navCtx, page.Navigate, page.NavigateParams{URL: url})
	cancel()
	if err != nil {
		return nil, s.navError(err)
	}
	if res.ErrorText != "" {
		return nil, &NavigateError{Reason: res.ErrorText}
	}
	if res.LoaderID != "" {
		// A new document committed; wait for its load event so the response
		// carries the final title. Slow pages resolve at the timeout.
		select {
		case <-loadCh:
		case <-s.tabCtx.Done():
			return nil, fmt.Errorf("%w: browser session ended", ErrUnavailable)
		case <-time.After(navWait):
		}
	}
	s.afterCommit()
	s.refreshNavState()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.navResultLocked(), nil
}

// History performs back/forward/reload; a missing history entry is a 200
// no-op returning current state (§3.6).
func (s *Session) History(ctx context.Context, action string) (*NavResult, error) {
	if action != "back" && action != "forward" && action != "reload" {
		return nil, fmt.Errorf("unknown history action %q", action)
	}
	if err := s.acquire(); err != nil {
		return nil, err
	}
	defer s.busy.Store(false)
	navCtx, cancel := context.WithTimeout(s.tabCtx, navWait)
	defer cancel()
	wait := true
	var commitCh <-chan struct{}
	var stopCommit func()
	if action == "reload" {
		commitCh, stopCommit = s.waitLoad()
	} else {
		commitCh, stopCommit = s.waitCommit()
	}
	defer stopCommit()
	switch action {
	case "reload":
		if _, err := chromedp.Call(navCtx, page.Reload, page.ReloadParams{}); err != nil {
			return nil, s.navError(err)
		}
	default:
		hist, err := chromedp.Call(navCtx, page.GetNavigationHistory, cdp.Empty{})
		if err != nil {
			return nil, s.navError(err)
		}
		idx := int(hist.CurrentIndex)
		target := idx - 1
		if action == "forward" {
			target = idx + 1
		}
		if target < 0 || target >= len(hist.Entries) {
			wait = false
			break
		}
		_, err = chromedp.Call(navCtx, page.NavigateToHistoryEntry,
			page.NavigateToHistoryEntryParams{EntryID: hist.Entries[target].ID})
		if err != nil {
			// The entry can vanish between GetNavigationHistory and the
			// navigate call; contract §3.6 treats a missing entry as a no-op.
			if strings.Contains(err.Error(), "entry") {
				wait = false
				break
			}
			return nil, s.navError(err)
		}
	}
	if wait {
		select {
		case <-commitCh:
		case <-s.tabCtx.Done():
			return nil, fmt.Errorf("%w: browser session ended", ErrUnavailable)
		case <-time.After(navWait):
		}
		s.afterCommit()
	}
	s.refreshNavState()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.navResultLocked(), nil
}

func (s *Session) navError(err error) error {
	if errors.Is(err, context.Canceled) && s.tabCtx.Err() != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return &NavigateError{Reason: err.Error()}
}

// waitLoad subscribes to the load event before the caller issues a command so
// no event is missed; the returned stop ends the subscription.
func (s *Session) waitLoad() (<-chan struct{}, func()) {
	ctx, cancel := context.WithCancel(s.tabCtx)
	ch := make(chan struct{}, 1)
	go func() {
		for _, err := range cdp.Events[page.EventLoadEventFired](ctx, s.tab(), page.LoadEventFired) {
			if err != nil {
				return
			}
			select {
			case ch <- struct{}{}:
			default:
			}
			return
		}
	}()
	return ch, cancel
}

// waitCommit fires on the first main-frame commit: frameNavigated covers
// bfcache restores and same-document navigations that never emit a load
// event, with loadEventFired as the fallback.
func (s *Session) waitCommit() (<-chan struct{}, func()) {
	ctx, cancel := context.WithCancel(s.tabCtx)
	ch := make(chan struct{}, 1)
	fire := func() {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	go func() {
		for ev, err := range cdp.Events[page.EventFrameNavigated](ctx, s.tab(), page.FrameNavigated) {
			if err != nil {
				return
			}
			if ev.Frame != nil && ev.Frame.ParentID == "" {
				fire()
				return
			}
		}
	}()
	go func() {
		for _, err := range cdp.Events[page.EventLoadEventFired](ctx, s.tab(), page.LoadEventFired) {
			if err != nil {
				return
			}
			fire()
			return
		}
	}()
	return ch, cancel
}

func (s *Session) tab() *chromedp.Target {
	return chromedp.FromContext(s.tabCtx).Target
}

// afterCommit runs on every committed navigation (contract §5): painted
// highlights are cleared, backend node ids go stale, a fresh screenshot is
// forced, and meta is re-emitted.
func (s *Session) afterCommit() {
	// Clear the painted highlight of the outgoing document; fails harmlessly
	// when the overlay was never enabled.
	ctx, cancel := s.ioCtx()
	_, _ = chromedp.Call(ctx, overlay.HideHighlight, cdp.Empty{})
	cancel()
	s.enqueueNavCheck()
	go func() {
		select {
		case <-time.After(screenshotSettle):
		case <-s.tabCtx.Done():
			return
		}
		s.forceScreenshot()
	}()
}

// watchNav tracks in-page commits (link clicks, history.pushState) for the
// session's whole life so annotation state resets even without a stream.
func (s *Session) watchNav() {
	for ev, err := range cdp.Events[page.EventFrameNavigated](s.tabCtx, s.tab(), page.FrameNavigated) {
		if err != nil {
			return
		}
		if ev.Frame == nil || ev.Frame.ParentID != "" {
			continue
		}
		s.afterCommit()
	}
}

func (s *Session) watchCrash() {
	for _, err := range cdp.Events[inspector.EventTargetCrashed](s.tabCtx, s.tab(), inspector.TargetCrashed) {
		if err != nil {
			return
		}
		s.markCrashed("tab crashed")
		return
	}
}

func (s *Session) markCrashed(reason string) {
	s.mu.Lock()
	s.markCrashedLocked(reason)
	s.mu.Unlock()
}

func (s *Session) markCrashedLocked(reason string) {
	if s.status != "active" {
		return
	}
	s.status = "crashed"
	s.errText = reason
	s.lastActivity = time.Now()
	if s.castCancel != nil {
		s.castCancel()
		s.castCancel = nil
		s.castCtx = nil
	}
	if s.stream != nil {
		s.stream.closeWith("browser_crashed", "The browser process crashed.")
		s.stream = nil
	}
}

// maintLoop is the session's serialized CDP worker: screencast acks (issued
// off the event iterator, which must not run commands) and throttled
// nav-state checks.
func (s *Session) maintLoop() {
	for {
		select {
		case id := <-s.ackCh:
			_, _ = chromedp.Call(s.tabCtx, page.ScreencastFrameAck,
				page.ScreencastFrameAckParams{SessionID: id})
		case task := <-s.maintCh:
			task()
		case <-s.tabCtx.Done():
			return
		}
	}
}

func (s *Session) enqueue(task func()) {
	select {
	case s.maintCh <- task:
	case <-s.tabCtx.Done():
	}
}

func (s *Session) enqueueNavCheck() {
	s.enqueue(func() {
		s.mu.Lock()
		s.lastNavCheck = time.Now()
		s.mu.Unlock()
		s.refreshNavState()
	})
}

func (s *Session) maybeNavCheck() {
	s.mu.Lock()
	due := time.Since(s.lastNavCheck) > navCheckInterval
	if due {
		s.lastNavCheck = time.Now()
	}
	s.mu.Unlock()
	if due {
		s.enqueueNavCheck()
	}
}

// meta builds the current {type:"meta"} payload. Callers hold s.mu.
func (s *Session) metaLocked() Meta {
	m := Meta{Type: "meta", URL: s.url, Title: s.title, CanGoBack: s.canBack, CanGoForward: s.canFwd,
		DeviceWidth: int64(s.width), DeviceHeight: int64(s.height)}
	if fm := s.lastFrame; fm != nil {
		m.DeviceWidth = int64(fm.DeviceWidth)
		m.DeviceHeight = int64(fm.DeviceHeight)
		m.PageScaleFactor = fm.PageScaleFactor
		m.ScrollOffsetX = fm.ScrollOffsetX
		m.ScrollOffsetY = fm.ScrollOffsetY
	}
	return m
}

// maybeMeta re-emits {type:"meta"} when any field changed.
func (s *Session) maybeMeta() {
	s.mu.Lock()
	meta := s.metaLocked()
	if meta == s.lastMeta {
		s.mu.Unlock()
		return
	}
	s.lastMeta = meta
	sub := s.stream
	s.mu.Unlock()
	if sub == nil {
		return
	}
	b, _ := json.Marshal(meta)
	sub.sendCtrl(b)
}

// Attach registers the one live stream: the previous stream gets
// closed/stream_replaced (§8.7), meta goes out first, screencast starts, and a
// screenshot is forced so a frame always precedes repaints (§5).
func (s *Session) Attach() (*Stream, error) {
	s.mu.Lock()
	if err := s.aliveLocked(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	sub := newStream()
	old := s.stream
	s.stream = sub
	s.lastActivity = time.Now()
	meta := s.metaLocked()
	s.lastMeta = meta
	// Every attach gets a fresh screencast epoch: the previous loop (if any)
	// is cancelled so exactly one castLoop runs and forwards to this stream.
	if s.castCancel != nil {
		s.castCancel()
	}
	s.castCtx, s.castCancel = context.WithCancel(s.tabCtx)
	castCtx, castCancel := s.castCtx, s.castCancel
	s.mu.Unlock()
	if old != nil {
		old.closeWith("stream_replaced", "Another client attached to this browser session.")
	}
	b, _ := json.Marshal(meta)
	sub.sendCtrl(b)
	go s.castLoop(castCtx, castCancel)
	go s.forceScreenshot()
	return sub, nil
}

// Detach drops the stream and stops the screencast; the session stays alive
// for re-attach (§8.2).
func (s *Session) Detach(sub *Stream) {
	s.mu.Lock()
	if s.stream != sub {
		s.mu.Unlock()
		return
	}
	s.stream = nil
	cancel := s.castCancel
	s.castCancel = nil
	s.castCtx = nil
	s.lastActivity = time.Now()
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		ctx, c := context.WithTimeout(s.tabCtx, 5*time.Second)
		_, _ = chromedp.Call(ctx, page.StopScreencast, cdp.Empty{})
		c()
	}
}

// castLoop runs the screencast for one stream epoch: the event subscription is
// created before startScreencast so no frame is lost, and every frame is
// acked via the maint worker and forwarded as a binary WS message.
func (s *Session) castLoop(ctx context.Context, cancel context.CancelFunc) {
	defer func() {
		cancel()
		s.mu.Lock()
		if s.castCtx == ctx {
			s.castCtx = nil
			s.castCancel = nil
		}
		s.mu.Unlock()
	}()
	events := cdp.Events[page.EventScreencastFrame](ctx, s.tab(), page.ScreencastFrame)
	if _, err := chromedp.Call(ctx, page.StartScreencast, page.StartScreencastParams{
		Format: "jpeg", Quality: ptr(screencastQuality), EveryNthFrame: 1,
	}); err != nil {
		return
	}
	for ev, err := range events {
		if err != nil || ctx.Err() != nil {
			return
		}
		s.onFrame(ev)
	}
}

func (s *Session) onFrame(ev page.EventScreencastFrame) {
	select {
	case s.ackCh <- ev.SessionID:
	case <-s.tabCtx.Done():
		return
	}
	s.mu.Lock()
	s.lastFrame = ev.Metadata
	meta := s.metaLocked()
	changed := meta != s.lastMeta
	s.lastMeta = meta
	sub := s.stream
	s.mu.Unlock()
	if sub == nil {
		return
	}
	if changed {
		b, _ := json.Marshal(meta)
		sub.sendCtrl(b)
	}
	sub.sendFrame(ev.Data)
	s.maybeNavCheck()
}

// forceScreenshot pushes one JPEG frame; screencast only emits on repaint, so
// this covers stream attach and committed navigations on static pages.
func (s *Session) forceScreenshot() {
	ctx, cancel := s.ioCtx()
	res, err := chromedp.Call(ctx, page.CaptureScreenshot,
		page.CaptureScreenshotParams{Format: "jpeg", Quality: ptr(screencastQuality)})
	cancel()
	if err != nil {
		return
	}
	s.mu.Lock()
	sub := s.stream
	s.mu.Unlock()
	if sub != nil {
		sub.sendFrame(res.Data)
	}
}

// ---------- input (§4.4) ----------

func (s *Session) MouseMove(x, y float64) error {
	ctx, cancel := s.ioCtx()
	_, err := chromedp.Call(ctx, input.DispatchMouseEvent,
		input.DispatchMouseEventParams{Type: input.DispatchMouseEventTypeMouseMoved, X: x, Y: y})
	cancel()
	return err
}

func (s *Session) Click(x, y float64, button string, clickCount int64) error {
	if err := s.MouseMove(x, y); err != nil {
		return err
	}
	btn := input.MouseButton(button)
	if btn == "" {
		btn = input.MouseButtonLeft
	}
	if clickCount < 1 {
		clickCount = 1
	}
	ctx, cancel := s.ioCtx()
	defer cancel()
	for _, t := range []input.DispatchMouseEventType{
		input.DispatchMouseEventTypeMousePressed, input.DispatchMouseEventTypeMouseReleased,
	} {
		if _, err := chromedp.Call(ctx, input.DispatchMouseEvent,
			input.DispatchMouseEventParams{Type: t, X: x, Y: y, Button: btn, ClickCount: clickCount}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Session) Wheel(x, y, deltaX, deltaY float64) error {
	ctx, cancel := s.ioCtx()
	_, err := chromedp.Call(ctx, input.DispatchMouseEvent,
		input.DispatchMouseEventParams{Type: input.DispatchMouseEventTypeMouseWheel, X: x, Y: y, DeltaX: deltaX, DeltaY: deltaY})
	cancel()
	return err
}

func (s *Session) InsertText(text string) error {
	ctx, cancel := s.ioCtx()
	_, err := chromedp.Call(ctx, input.InsertText, input.InsertTextParams{Text: text})
	cancel()
	return err
}

var domKeyRunes = func() map[string]rune {
	m := map[string]rune{}
	for r, k := range kb.Keys {
		if _, ok := m[k.Key]; !ok {
			m[k.Key] = r
		}
	}
	return m
}()

// Key dispatches a DOM KeyboardEvent.key name (down + optional char + up) via
// the chromedp/kb mapping.
func (s *Session) Key(name string, modifiers int64) error {
	var r rune
	if len([]rune(name)) == 1 {
		r = []rune(name)[0]
	} else {
		var ok bool
		r, ok = domKeyRunes[name]
		if !ok {
			return fmt.Errorf("unknown key %q", name)
		}
	}
	ctx, cancel := s.ioCtx()
	defer cancel()
	for _, p := range kb.Encode(r) {
		p.Modifiers |= modifiers
		if _, err := chromedp.Call(ctx, input.DispatchKeyEvent, *p); err != nil {
			return err
		}
	}
	return nil
}

// Resize overrides the device metrics; each dimension clamps to [200, 4096].
func (s *Session) Resize(width, height int64) error {
	clamp := func(v int64) int64 {
		if v < 200 {
			return 200
		}
		if v > 4096 {
			return 4096
		}
		return v
	}
	width, height = clamp(width), clamp(height)
	ctx, cancel := s.ioCtx()
	_, err := chromedp.Call(ctx, emulation.SetDeviceMetricsOverride,
		emulation.SetDeviceMetricsOverrideParams{Width: width, Height: height, DeviceScaleFactor: 1, Mobile: false})
	cancel()
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.width, s.height = int(width), int(height)
	s.mu.Unlock()
	return nil
}

// Close tears the session down (DELETE, idle GC, shutdown): the stream gets
// closed/session_closed, the tab and its BrowserContext are disposed, and
// pick records are dropped via removeSession.
func (s *Session) Close() {
	s.closeWith("session_closed", "Browser session closed.")
}

func (s *Session) closeWith(code, message string) {
	s.mu.Lock()
	if s.status == "closed" {
		s.mu.Unlock()
		return
	}
	s.status = "closed"
	cancel := s.castCancel
	s.castCancel = nil
	s.castCtx = nil
	sub := s.stream
	s.stream = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if sub != nil {
		sub.closeWith(code, message)
	}
	s.tabCancel()
	ctx, c := context.WithTimeout(s.browserCtx, 5*time.Second)
	_, _ = chromedp.CallBrowser(ctx, target.DisposeBrowserContext,
		target.DisposeBrowserContextParams{BrowserContextID: s.browserCtxID})
	c()
	s.m.removeSession(s)
}
