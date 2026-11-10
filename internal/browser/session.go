package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	urlpkg "net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/inspector"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"github.com/jahrulnr/nusamation/internal/store"
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
	Profile        string `json:"profile"`
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
	Mobile          bool    `json:"mobile,omitempty"`
	Touch           bool    `json:"touch,omitempty"`
	DPR             float64 `json:"dpr,omitempty"`
	PageScaleFactor float64 `json:"pageScaleFactor"`
	ScrollOffsetX   float64 `json:"scrollOffsetX"`
	ScrollOffsetY   float64 `json:"scrollOffsetY"`
	URL             string  `json:"url"`
	Title           string  `json:"title"`
	CanGoBack       bool    `json:"canGoBack"`
	CanGoForward    bool    `json:"canGoForward"`
	// ViewportHeld is set only on the attach-time meta when this stream
	// replaced another live viewer: the viewport already has an owner, so the
	// new client should adopt it instead of resizing to its own pane.
	ViewportHeld bool `json:"viewportHeld,omitempty"`
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
	emuMu        sync.Mutex // serializes device-profile changes (emulate/resize)
	url          string
	title        string
	status       string
	errText      string
	width        int
	height       int
	device       DeviceOpts
	canBack      bool
	canFwd       bool
	lastActivity time.Time
	lastFrame    *page.ScreencastFrameMetadata
	lastMeta     Meta
	lastNavCheck time.Time
	pointer      pointerMark
	// streams are the attached viewers — every one gets the same frames and
	// meta (share-screen model). controller is the single viewer allowed to
	// drive input; agentLock is the agent's input lease, which mutes every
	// viewer until a human claims control back (human always preempts).
	streams    []*Stream
	controller *Stream
	agentLock  bool
	castCtx    context.Context
	castCancel context.CancelFunc

	busy atomic.Bool

	profile    string
	tabCtx     context.Context
	tabCancel  context.CancelFunc
	browserCtx context.Context

	ackCh   chan int64
	maintCh chan func()
}

func (s *Session) ID() string             { return s.id }
func (s *Session) ConversationID() string { return s.conversationID }

// Profile returns the browser profile this session's storage belongs to.
func (s *Session) Profile() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.profile
}

func (s *Session) touch() {
	s.mu.Lock()
	s.lastActivity = time.Now()
	s.mu.Unlock()
}

func (s *Session) idleFor(ttl time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.streams) == 0 && time.Since(s.lastActivity) > ttl
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
		ID: s.id, ConversationID: s.conversationID, Profile: s.profile, URL: s.url, Title: s.title,
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
	url, title, profile := s.url, s.title, s.profile
	s.mu.Unlock()
	s.maybeMeta()
	s.afterLoadHooks(url, title, profile)
}

// afterLoadHooks runs once per committed navigation: record the visit, drain
// any buffered login reports the injected tap could not hand to the binding,
// and offer the page the profile's saved credentials for this origin.
func (s *Session) afterLoadHooks(url, title, profile string) {
	if s.m.rec == nil || url == "" {
		return
	}
	if err := s.m.rec.RecordBrowserVisit(context.Background(), profile, url, title); err != nil {
		s.m.log.Warn("browser history record failed", "error", err)
	}
	if raw, err := s.evalQuiet(`JSON.stringify(window.__nusaLogins ? window.__nusaLogins.splice(0, 20) : [])`); err == nil {
		var payloads []string
		if json.Unmarshal(raw, &payloads) == nil {
			for _, p := range payloads {
				s.saveLoginPayload(p)
			}
		}
	}
	if u, err := urlpkg.Parse(url); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
		origin := u.Scheme + "://" + u.Host
		creds, err := s.m.rec.BrowserCredentials(context.Background(), profile, origin)
		if err == nil && len(creds) > 0 {
			s.fillLogin(creds[0])
		}
	}
}

// saveLoginPayload persists one credential report from loginTapJS.
func (s *Session) saveLoginPayload(payload string) {
	if s.m.rec == nil {
		return
	}
	var p struct {
		Origin   string `json:"origin"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if json.Unmarshal([]byte(payload), &p) != nil || p.Password == "" {
		return
	}
	u, err := urlpkg.Parse(p.Origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return
	}
	if err := s.m.rec.SaveBrowserCredential(context.Background(), s.Profile(), p.Origin, p.Username, p.Password); err != nil {
		s.m.log.Warn("save browser credential failed", "error", err)
	}
}

// fillLogin fills the page's first password field (and its username sibling)
// with a saved credential, through the native setters frameworks listen to.
func (s *Session) fillLogin(c store.BrowserCredential) {
	user, _ := json.Marshal(c.Username)
	pass, _ := json.Marshal(c.Password)
	expr := `(() => {
	  const pw = [...document.querySelectorAll('input[type=password]')].find(i => !i.disabled && !i.readOnly);
	  if (!pw || pw.value) return 'no-field';
	  const put = (el, v) => { const d = Object.getOwnPropertyDescriptor(el.constructor.prototype, 'value'); d.set.call(el, v); el.dispatchEvent(new Event('input', {bubbles: true})); el.dispatchEvent(new Event('change', {bubbles: true})); };
	  const scope = pw.form || document;
	  const user = scope.querySelector('input[type=email],input[name*=user i],input[name*=email i],input[name*=login i],input[autocomplete*=username i]')
	    || [...scope.querySelectorAll('input[type=text],input:not([type])')].find(i => !i.disabled && !i.readOnly);
	  if (user && !user.value) put(user, ` + string(user) + `);
	  put(pw, ` + string(pass) + `);
	  return 'filled';
	})()`
	if got, err := s.evalQuiet(expr); err != nil || string(got) != `"filled"` {
		return
	}
}

// Autofill manually fills the page's login form from the profile's saved
// credential for the current origin; reports whether a field was filled.
func (s *Session) Autofill(ctx context.Context) (bool, error) {
	if s.m.rec == nil {
		return false, nil
	}
	s.mu.Lock()
	rawURL, profile := s.url, s.profile
	s.mu.Unlock()
	u, err := urlpkg.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return false, nil
	}
	creds, err := s.m.rec.BrowserCredentials(ctx, profile, u.Scheme+"://"+u.Host)
	if err != nil || len(creds) == 0 {
		return false, err
	}
	s.fillLogin(creds[0])
	raw, err := s.evalQuiet(`!![...document.querySelectorAll('input[type=password]')].find(i => i.value)`)
	return string(raw) == "true", err
}

// evalQuiet evaluates an expression on the live tab without touching the busy
// claim; callers tolerate failures (the page may be mid-navigation).
func (s *Session) evalQuiet(expr string) (json.RawMessage, error) {
	ctx, cancel := s.ioCtx()
	res, err := chromedp.Call(ctx, runtime.Evaluate, runtime.EvaluateParams{
		Expression: expr, ReturnByValue: new(bool),
	})
	cancel()
	if err != nil {
		return nil, err
	}
	return res.Result.Value, nil
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
	s.afterCommit(true)
	s.refreshNavState()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.navResultLocked(), nil
}

// Eval runs a JavaScript expression in the page and returns the JSON result
// value. Promises are awaited; exception text becomes the error. BigInt and
// non-finite numbers come back as strings (for example "10n", "NaN",
// "Infinity") because JSON has no representation for them; negative zero
// stays -0.
func (s *Session) Eval(ctx context.Context, expression string) (json.RawMessage, error) {
	if err := s.acquire(); err != nil {
		return nil, err
	}
	defer s.busy.Store(false)
	evalCtx, cancel := context.WithTimeout(s.tabCtx, 15*time.Second)
	yes := true
	res, err := chromedp.Call(evalCtx, runtime.Evaluate, runtime.EvaluateParams{
		Expression: expression, AwaitPromise: &yes, ReturnByValue: &yes,
	})
	cancel()
	if err != nil {
		return nil, err
	}
	if ex := res.ExceptionDetails; ex != nil {
		desc := ex.Text
		if ex.Exception != nil && ex.Exception.Description != "" {
			desc = ex.Exception.Description
		}
		return nil, fmt.Errorf("eval: %s", desc)
	}
	if u := res.Result.UnserializableValue; u != "" {
		if u == "-0" {
			return json.RawMessage("-0"), nil
		}
		b, err := json.Marshal(u)
		if err != nil {
			return nil, err
		}
		return b, nil
	}
	if len(res.Result.Value) == 0 {
		return json.RawMessage("null"), nil
	}
	return res.Result.Value, nil
}

// Snapshot returns the page as an indented aria-like text tree (snapshotJS).
// Interactive elements carry [ref=eN] markers that RefPoint/FocusRef resolve.
func (s *Session) Snapshot(ctx context.Context) (string, error) {
	raw, err := s.Eval(ctx, snapshotJS)
	if err != nil {
		return "", err
	}
	var tree string
	if err := json.Unmarshal(raw, &tree); err != nil {
		return "", fmt.Errorf("snapshot: %w", err)
	}
	return tree, nil
}

// ErrRefNotFound means the element ref is stale or unknown; the fix is a
// fresh Snapshot, since refs are re-stamped every snapshot.
var (
	ErrRefNotFound   = errors.New("element ref not found — run browser_snapshot again")
	ErrNotActionable = errors.New("element is not actionable")
	ErrInvalidAction = errors.New("invalid action")
)

// actionPoint is the actionPointExpr result: ok, missing, hidden, disabled,
// pointer-events-none, covered, or outside-viewport.
type actionPoint struct {
	Status string  `json:"status"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	By     string  `json:"by"`
}

// RefPoint resolves a snapshot ref to viewport coordinates of the element's
// center, scrolling it into view first. It also proves the element can receive
// the action: disabled, hidden, pointer-events:none, off-viewport, and covered
// elements fail with ErrNotActionable instead of hitting another node.
func (s *Session) RefPoint(ctx context.Context, ref string) (float64, float64, error) {
	raw, err := s.Eval(ctx, actionPointExpr(ref))
	if err != nil {
		return 0, 0, err
	}
	var p *actionPoint
	if err := json.Unmarshal(raw, &p); err != nil || p == nil {
		return 0, 0, ErrRefNotFound
	}
	switch p.Status {
	case "ok":
		return p.X, p.Y, nil
	case "missing":
		return 0, 0, ErrRefNotFound
	case "covered":
		by := p.By
		if by == "" {
			by = "another element"
		}
		return 0, 0, fmt.Errorf("%w: %s is covered by %s", ErrNotActionable, ref, by)
	case "hidden":
		return 0, 0, fmt.Errorf("%w: %s is not visible", ErrNotActionable, ref)
	case "disabled":
		return 0, 0, fmt.Errorf("%w: %s is disabled", ErrNotActionable, ref)
	case "pointer-events-none":
		return 0, 0, fmt.Errorf("%w: %s does not receive pointer events", ErrNotActionable, ref)
	case "outside-viewport":
		return 0, 0, fmt.Errorf("%w: %s is outside the viewport", ErrNotActionable, ref)
	default:
		return 0, 0, fmt.Errorf("%w: %s is %s", ErrNotActionable, ref, p.Status)
	}
}

// FocusRef focuses the ref'd element in page; false means focus did not land
// (or the ref is gone) and the caller should click it instead.
func (s *Session) FocusRef(ctx context.Context, ref string) (bool, error) {
	raw, err := s.Eval(ctx, focusExpr(ref))
	if err != nil {
		return false, err
	}
	var res *struct {
		Status  string `json:"status"`
		Focused bool   `json:"focused"`
	}
	if err := json.Unmarshal(raw, &res); err != nil || res == nil {
		return false, ErrRefNotFound
	}
	switch res.Status {
	case "ok":
		return res.Focused, nil
	case "missing":
		return false, ErrRefNotFound
	default:
		return false, fmt.Errorf("%w: %s is %s", ErrNotActionable, ref, res.Status)
	}
}

// fieldResult is the fillExpr result: detail is the per-field summary.
type fieldResult struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason"`
	Detail string `json:"detail"`
}

// SelectRef sets options on the ref'd <select> by value or visible label.
func (s *Session) SelectRef(ctx context.Context, ref string, values []string) (string, error) {
	raw, err := s.Eval(ctx, selectExpr(ref, values))
	if err != nil {
		return "", err
	}
	var res *struct {
		Status string `json:"status"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(raw, &res); err != nil || res == nil {
		return "", ErrRefNotFound
	}
	switch res.Status {
	case "ok":
		return res.Detail, nil
	case "missing":
		return "", ErrRefNotFound
	case "disabled":
		return "", fmt.Errorf("%w: %s", ErrNotActionable, res.Detail)
	default:
		return "", fmt.Errorf("%w: %s", ErrInvalidAction, res.Detail)
	}
}

// FillFields sets every field through the native setter and fires input+change.
// Any field that is missing, disabled, read-only, or has no matching option
// fails the whole call with a per-field detail.
func (s *Session) FillFields(ctx context.Context, fields []FieldValue) (string, error) {
	raw, err := s.Eval(ctx, fillExpr(fields))
	if err != nil {
		return "", err
	}
	var res fieldResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", fmt.Errorf("fields: %w", err)
	}
	if res.OK {
		return res.Detail, nil
	}
	switch res.Reason {
	case "ref not found":
		return res.Detail, fmt.Errorf("%w: %s", ErrRefNotFound, res.Detail)
	case "disabled", "readonly":
		return res.Detail, fmt.Errorf("%w: %s", ErrNotActionable, res.Detail)
	default:
		return res.Detail, fmt.Errorf("%w: %s", ErrInvalidAction, res.Detail)
	}
}

// WaitForText polls until text appears (gone=false) or disappears (gone=true)
// in body.innerText, or the 10s deadline passes.
func (s *Session) WaitForText(ctx context.Context, text string, gone bool) error {
	needle, _ := json.Marshal(text)
	expr := fmt.Sprintf(`(document.body ? document.body.innerText : "").includes(%s)`, needle)
	deadline := time.Now().Add(10 * time.Second)
	for {
		raw, err := s.Eval(ctx, expr)
		switch {
		case err == nil:
			var found bool
			if json.Unmarshal(raw, &found) == nil && found != gone {
				return nil
			}
		case errors.Is(err, ErrBusy):
			// A navigation is mid-commit; the next poll sees the settled DOM.
		default:
			return err // dead/crashed tab is not a wait timeout
		}
		if time.Now().After(deadline) {
			want := "appear"
			if gone {
				want = "disappear"
			}
			return fmt.Errorf("timed out waiting for text to %s: %q", want, text)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
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
		s.afterCommit(true)
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
// pin markers are cleared, a fresh screenshot is forced, and meta is
// re-emitted. locked=true means the caller already holds s.busy (Navigate/
// History): the remover runs unconditionally because no pick can be in
// flight — busy serializes them — and same-document commits do not destroy
// the DOM marker. locked=false is the event-loop path (page-initiated
// pushState): the CAS keeps it atomic with a pick's busy claim so a pushState
// mid-pick can't strip the marker between paint and capture.
func (s *Session) afterCommit(locked bool) {
	if locked {
		s.removeMarker()
	} else if s.busy.CompareAndSwap(false, true) {
		s.removeMarker()
		s.busy.Store(false)
	}
	// Skipping during other busy ops is safe: cross-document commits destroy
	// the DOM marker anyway.
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

func (s *Session) removeMarker() {
	ctx, cancel := s.ioCtx()
	_, _ = chromedp.Call(ctx, runtime.Evaluate, runtime.EvaluateParams{Expression: markerRemover})
	cancel()
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
		s.afterCommit(false)
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

// watchDialogs dismisses JavaScript dialogs. A dialog stalls the renderer, so
// every later Runtime.evaluate, screenshot, or click waits until it is handled;
// one alert() can wedge the whole tool surface. Chromium resumes only after
// Page.handleJavaScriptDialog. The dismiss must never be queued or dropped —
// an undismissed dialog leaves the page input-locked while the stream looks
// alive — so each event runs on its own goroutine off the event iterator.
func (s *Session) watchDialogs() {
	for _, err := range cdp.Events[page.EventJavascriptDialogOpening](s.tabCtx, s.tab(), page.JavascriptDialogOpening) {
		if err != nil {
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(s.tabCtx, ioTimeout)
			_, _ = chromedp.Call(ctx, page.HandleJavaScriptDialog,
				page.HandleJavaScriptDialogParams{Accept: false})
			cancel()
		}()
	}
}

// watchLoginBindings receives credential reports the injected loginTapJS sends
// through the __nusaReportLogin binding; the binding is reinstalled on every
// new execution context because CDP bindings do not follow navigations.
func (s *Session) watchLoginBindings() {
	for ev, err := range cdp.Events[runtime.EventBindingCalled](s.tabCtx, s.tab(), runtime.BindingCalled) {
		if err != nil {
			return
		}
		if ev.Name == "__nusaReportLogin" {
			s.saveLoginPayload(ev.Payload)
		}
	}
}

func (s *Session) watchLoginContexts() {
	for _, err := range cdp.Events[runtime.EventExecutionContextCreated](s.tabCtx, s.tab(), runtime.ExecutionContextCreated) {
		if err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(s.tabCtx, ioTimeout)
		_, _ = chromedp.Call(ctx, runtime.AddBinding, runtime.AddBindingParams{Name: "__nusaReportLogin"})
		cancel()
	}
}

// swapTab replaces the session's live target — used by profile migration.
// Old watchers die with the old context and are restarted against the new tab;
// the session id, busy claim, attached viewers, and the input lease survive.
func (s *Session) swapTab(tabCtx context.Context, tabCancel context.CancelFunc, browserCtx context.Context, profile string) {
	s.mu.Lock()
	oldCancel := s.tabCancel
	oldBrowser := s.browserCtx
	oldID := s.tab().TargetID
	s.tabCtx = tabCtx
	s.tabCancel = tabCancel
	s.browserCtx = browserCtx
	s.profile = profile
	s.url = ""
	s.title = ""
	s.canBack = false
	s.canFwd = false
	s.lastFrame = nil
	// The old castCtx dies with the old tab — attached viewers must get a
	// fresh screencast epoch on the new target or they show one forced frame
	// and then nothing, looking live while every input reports no change.
	if s.castCancel != nil {
		s.castCancel()
	}
	s.castCtx, s.castCancel = nil, nil
	hasStream := len(s.streams) > 0
	var castCtx context.Context
	var castCancel context.CancelFunc
	if hasStream {
		s.castCtx, s.castCancel = context.WithCancel(tabCtx)
		castCtx, castCancel = s.castCtx, s.castCancel
	}
	s.mu.Unlock()
	oldCancel()
	ctx, c := context.WithTimeout(oldBrowser, 5*time.Second)
	_, _ = chromedp.CallBrowser(ctx, target.CloseTarget, target.CloseTargetParams{TargetID: oldID})
	c()
	go s.watchNav()
	go s.watchCrash()
	go s.watchDialogs()
	go s.watchLoginBindings()
	go s.watchLoginContexts()
	go s.maintLoop()
	s.refreshNavState()
	if hasStream {
		go s.castLoop(castCtx, castCancel)
		go s.forceScreenshot()
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
	for _, sub := range s.streams {
		sub.closeWith("browser_crashed", "The browser process crashed.")
	}
	s.streams = nil
	s.controller = nil
	s.agentLock = false
}

// maintLoop is the session's serialized CDP worker: screencast acks (issued
// off the event iterator, which must not run commands) and throttled
// nav-state checks.
func (s *Session) maintLoop() {
	// Bind the tab context once at start: swapTab replaces s.tabCtx, and a
	// worker that re-reads the field would keep draining a new tab's ack
	// queue alongside its successor — two workers, one stream of sessionIDs.
	ctx := s.tabCtx
	for {
		select {
		case id := <-s.ackCh:
			// Bounded like every other call: an ack whose reply never comes
			// (target detaching mid-call, wedged renderer) must not kill the
			// worker — without it, ackCh fills, onFrame blocks, and the whole
			// screencast freezes until the session is recreated.
			actx, cancel := context.WithTimeout(ctx, ioTimeout)
			if _, err := chromedp.Call(actx, page.ScreencastFrameAck,
				page.ScreencastFrameAckParams{SessionID: id}); err != nil {
				s.m.log.Debug("screencast ack failed", "session", s.id, "error", err)
			}
			cancel()
		case task := <-s.maintCh:
			task()
		case <-ctx.Done():
			return
		}
	}
}

// enqueue hands work to the maint worker without blocking the caller — the
// frame path (onFrame → maybeNavCheck) must never stall on a busy worker.
// Tasks are periodic or idempotent, so a full queue can safely drop.
func (s *Session) enqueue(task func()) {
	select {
	case s.maintCh <- task:
	case <-s.tabCtx.Done():
	default:
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
		DeviceWidth: int64(s.width), DeviceHeight: int64(s.height),
		Mobile: s.device.Mobile, Touch: s.device.Touch, DPR: s.device.DPR}
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
	subs := s.streams
	s.mu.Unlock()
	b, _ := json.Marshal(meta)
	for _, sub := range subs {
		sub.sendCtrl(b)
	}
}

// controlForLocked renders the per-viewer control state: the attached stream
// sees its own lease, every other viewer sees who actually holds the floor.
func (s *Session) controlForLocked(sub *Stream) []byte {
	owner := "none"
	switch {
	case s.agentLock:
		owner = "agent"
	case s.controller == sub:
		owner = "you"
	case s.controller != nil:
		owner = "other"
	}
	b, _ := json.Marshal(map[string]any{"type": "control", "owner": owner})
	return b
}

// broadcastControl pushes the current lease state to every attached viewer —
// sent on attach, takeover, agent lock/unlock, and controller handoff.
func (s *Session) broadcastControl() {
	s.mu.Lock()
	subs := s.streams
	payloads := make(map[*Stream][]byte, len(subs))
	for _, sub := range subs {
		payloads[sub] = s.controlForLocked(sub)
	}
	s.mu.Unlock()
	for sub, b := range payloads {
		sub.sendCtrl(b)
	}
}

// InputAllowed reports whether this viewer's connection may drive the page.
// Only the controller stream may input, and never while the agent holds the
// input lease — a human reclaims with a takeover, which always preempts.
func (s *Session) InputAllowed(sub *Stream) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.agentLock && s.controller != nil && s.controller == sub
}

// Takeover is a human claim on the input lease: the claiming viewer becomes
// the controller and any agent lock ends — a human always preempts the agent.
func (s *Session) Takeover(sub *Stream) {
	s.mu.Lock()
	if err := s.aliveLocked(); err != nil {
		s.mu.Unlock()
		return
	}
	attached := false
	for _, v := range s.streams {
		if v == sub {
			attached = true
			break
		}
	}
	if !attached {
		s.mu.Unlock()
		return
	}
	changed := s.controller != sub || s.agentLock
	s.controller = sub
	s.agentLock = false
	s.lastActivity = time.Now()
	s.mu.Unlock()
	if changed {
		s.broadcastControl()
	}
}

// SetAgentLock is the agent-side input lease (browser_lock): while held,
// every pane viewer keeps watching but their input is muted so a stray click
// cannot disturb the run. Releasing returns control to the current controller.
func (s *Session) SetAgentLock(on bool) {
	s.mu.Lock()
	if err := s.aliveLocked(); err != nil || s.agentLock == on {
		s.mu.Unlock()
		return
	}
	s.agentLock = on
	s.lastActivity = time.Now()
	s.mu.Unlock()
	s.broadcastControl()
}

// AgentLocked reports whether the agent currently holds the input lease.
func (s *Session) AgentLocked() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.agentLock
}

// Attach registers one more live viewer. Every stream gets the same frames
// and meta — attaching never displaces an existing viewer (§8.7). The first
// attach becomes the controller; the screencast belongs to the session, so a
// later attach only subscribes — no epoch restart, no frame gap. The attach
// meta tells a joining viewer the viewport is already owned, and a forced
// screenshot puts a frame on screen before the next repaint (§5).
func (s *Session) Attach() (*Stream, error) {
	s.mu.Lock()
	if err := s.aliveLocked(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	sub := newStream()
	held := len(s.streams) > 0
	s.streams = append(s.streams, sub)
	if s.controller == nil {
		s.controller = sub
	}
	s.lastActivity = time.Now()
	meta := s.metaLocked()
	s.lastMeta = meta
	mark := s.pointer
	ctrl := s.controlForLocked(sub)
	startCast := s.castCancel == nil
	if startCast {
		s.castCtx, s.castCancel = context.WithCancel(s.tabCtx)
	}
	castCtx, castCancel := s.castCtx, s.castCancel
	s.mu.Unlock()
	meta.ViewportHeld = held
	b, _ := json.Marshal(meta)
	sub.sendCtrl(b)
	sub.sendCtrl(ctrl)
	if mark.on {
		sub.Send(mark.json())
	}
	if startCast {
		go s.castLoop(castCtx, castCancel)
	}
	go s.forceScreenshot()
	return sub, nil
}

// Detach drops one viewer. The session and screencast stay alive for the
// remaining viewers; only the last detach stops the screencast (§8.2). When
// the controller leaves, the lease returns to the oldest attached viewer.
func (s *Session) Detach(sub *Stream) {
	s.mu.Lock()
	idx := -1
	for i, v := range s.streams {
		if v == sub {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.mu.Unlock()
		return
	}
	// Publish a fresh slice rather than deleting in place: fan-out sites
	// snapshot s.streams under the lock and iterate the same backing array
	// after it — an in-place delete would write elements they still read.
	next := make([]*Stream, 0, len(s.streams)-1)
	next = append(next, s.streams[:idx]...)
	next = append(next, s.streams[idx+1:]...)
	s.streams = next
	handoff := false
	if s.controller == sub {
		handoff = len(s.streams) > 0
		if handoff {
			s.controller = s.streams[0]
		} else {
			s.controller = nil
		}
	}
	s.lastActivity = time.Now()
	var cancel context.CancelFunc
	if len(s.streams) == 0 {
		cancel = s.castCancel
		s.castCancel = nil
		s.castCtx = nil
	}
	s.mu.Unlock()
	if handoff {
		s.broadcastControl()
	}
	if cancel != nil {
		cancel()
		// A racing Attach may already have opened a fresh screencast epoch —
		// a Stop issued now would freeze the new viewer's live view. Re-check
		// under the lock and hold it across the call so an attach that slips
		// in between orders itself after this stop instead of losing to it.
		s.mu.Lock()
		if len(s.streams) == 0 && s.castCtx == nil {
			ctx, c := context.WithTimeout(s.tabCtx, 5*time.Second)
			_, _ = chromedp.Call(ctx, page.StopScreencast, cdp.Empty{})
			c()
		}
		s.mu.Unlock()
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
		Format: "jpeg", Quality: new(screencastQuality), EveryNthFrame: 1,
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
	subs := s.streams
	s.mu.Unlock()
	if len(subs) == 0 {
		return
	}
	if changed {
		b, _ := json.Marshal(meta)
		for _, sub := range subs {
			sub.sendCtrl(b)
		}
	}
	for _, sub := range subs {
		sub.sendFrame(ev.Data)
	}
	s.maybeNavCheck()
}

// JPEG captures the current viewport. quality 0 uses the screencast quality.
func (s *Session) JPEG(quality int64) ([]byte, error) {
	if quality <= 0 {
		quality = screencastQuality
	}
	ctx, cancel := s.ioCtx()
	defer cancel()
	res, err := chromedp.Call(ctx, page.CaptureScreenshot,
		page.CaptureScreenshotParams{Format: "jpeg", Quality: &quality})
	if err != nil {
		return nil, err
	}
	return res.Data, nil
}

type pointerMark struct {
	x, y float64
	kind string
	on   bool
}

func (p pointerMark) json() []byte {
	b, _ := json.Marshal(map[string]any{"type": "pointer", "x": p.x, "y": p.y, "kind": p.kind})
	return b
}

// ShowPointer tells the attached live views where the agent pointer is.
// kind is "move", "click", or "scroll". The mark is kept and replayed on the
// next Attach, so a pane that opens after the action still sees it.
func (s *Session) ShowPointer(x, y float64, kind string) {
	mark := pointerMark{x: x, y: y, kind: kind, on: true}
	b := mark.json()
	s.mu.Lock()
	s.pointer = mark
	subs := s.streams
	s.mu.Unlock()
	for _, sub := range subs {
		sub.Send(b)
	}
}

// forceScreenshot pushes one JPEG frame; screencast only emits on repaint, so
// this covers stream attach and committed navigations on static pages.
func (s *Session) forceScreenshot() {
	ctx, cancel := s.ioCtx()
	res, err := chromedp.Call(ctx, page.CaptureScreenshot,
		page.CaptureScreenshotParams{Format: "jpeg", Quality: new(screencastQuality)})
	cancel()
	if err != nil {
		return
	}
	s.mu.Lock()
	subs := s.streams
	s.mu.Unlock()
	for _, sub := range subs {
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
	s.mu.Lock()
	mobile := s.device.Mobile
	s.mu.Unlock()
	if mobile {
		return s.Tap(x, y)
	}
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
	defer cancel()
	// Chromium requires both deltaX and deltaY on every mouseWheel event,
	// and cdproto's DispatchMouseEventParams omits zero values — so a pure
	// vertical scroll (deltaX=0) is rejected outright. Send the params
	// with both keys always present.
	params := struct {
		Type   input.DispatchMouseEventType `json:"type"`
		X      float64                      `json:"x"`
		Y      float64                      `json:"y"`
		DeltaX float64                      `json:"deltaX"`
		DeltaY float64                      `json:"deltaY"`
	}{Type: input.DispatchMouseEventTypeMouseWheel, X: x, Y: y, DeltaX: deltaX, DeltaY: deltaY}
	return s.tab().Call(ctx, "Input.dispatchMouseEvent", params, nil)
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

// Tap emits a single touch tap — the click equivalent when the session runs
// a mobile device profile. touchStart carries the point; touchEnd none.
func (s *Session) Tap(x, y float64) error {
	ctx, cancel := s.ioCtx()
	defer cancel()
	if _, err := chromedp.Call(ctx, input.DispatchTouchEvent,
		input.DispatchTouchEventParams{Type: input.DispatchTouchEventTypeTouchStart,
			TouchPoints: []*input.TouchPoint{{X: x, Y: y}}}); err != nil {
		return err
	}
	_, err := chromedp.Call(ctx, input.DispatchTouchEvent,
		input.DispatchTouchEventParams{Type: input.DispatchTouchEventTypeTouchEnd,
			TouchPoints: []*input.TouchPoint{}})
	return err
}

// DeviceOpts is one emulation state for a session: viewport metrics plus the
// mobile/UA/touch flags CDP applies together for device simulation.
type DeviceOpts struct {
	Width     int64
	Height    int64
	DPR       float64
	Mobile    bool
	UserAgent string
	Touch     bool
}

// Device returns the session's current emulation state.
func (s *Session) Device() DeviceOpts {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.device
}

// ApplyDevicePatch resolves preset+patch against the current device profile
// and applies the result, serialized with every other profile change so a
// concurrent resize cannot interleave CDP calls or commits.
func (s *Session) ApplyDevicePatch(preset string, p DevicePatch) (DeviceOpts, error) {
	s.emuMu.Lock()
	defer s.emuMu.Unlock()
	s.mu.Lock()
	cur := s.device
	s.mu.Unlock()
	o, err := ResolveDevice(cur, preset, p)
	if err != nil {
		return o, err
	}
	return o, s.emulate(o)
}

// Emulate applies a full device profile in one state change: viewport
// metrics, device scale factor, the mobile flag, the user agent override
// (empty restores the real UA), and touch emulation.
func (s *Session) Emulate(o DeviceOpts) error {
	s.emuMu.Lock()
	defer s.emuMu.Unlock()
	return s.emulate(o)
}

// emulate applies o; callers hold emuMu so profile changes serialize.
func (s *Session) emulate(o DeviceOpts) error {
	clamp := func(v int64) int64 {
		if v < 200 {
			return 200
		}
		if v > 4096 {
			return 4096
		}
		return v
	}
	o.Width, o.Height = clamp(o.Width), clamp(o.Height)
	if o.DPR <= 0 {
		o.DPR = 1
	}
	if o.DPR > 5 {
		o.DPR = 5
	}
	s.mu.Lock()
	prev := s.device
	s.mu.Unlock()
	ctx, cancel := s.ioCtx()
	defer cancel()
	if _, err := chromedp.Call(ctx, emulation.SetDeviceMetricsOverride,
		emulation.SetDeviceMetricsOverrideParams{Width: o.Width, Height: o.Height, DeviceScaleFactor: o.DPR, Mobile: o.Mobile}); err != nil {
		return err
	}
	// UA and touch overrides are profile state, not per-resize state — skip
	// the calls when nothing changed so a pane-drag resize stays one CDP call.
	if o.UserAgent != prev.UserAgent {
		if _, err := chromedp.Call(ctx, emulation.SetUserAgentOverride,
			emulation.SetUserAgentOverrideParams{UserAgent: o.UserAgent}); err != nil {
			return err
		}
	}
	if o.Touch != prev.Touch {
		if _, err := chromedp.Call(ctx, emulation.SetTouchEmulationEnabled,
			emulation.SetTouchEmulationEnabledParams{Enabled: o.Touch, MaxTouchPoints: 5}); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.device = o
	s.width, s.height = int(o.Width), int(o.Height)
	// Drop the stale frame reference: metaLocked reports frame dims over the
	// profile, and a page with no pixel damage (about:blank) may never send
	// another frame — the meta would then pin the old viewport forever.
	s.lastFrame = nil
	s.mu.Unlock()
	// Publish the profile change immediately; scroll offsets come back with
	// the next screencast frame.
	s.maybeMeta()
	// A page with no repaints leaves the transitional composite frame on
	// screen; a forced capture replaces it with a clean one.
	go s.forceScreenshot()
	return nil
}

// Resize changes the viewport metrics, keeping the rest of the device profile
// (mobile flag, UA, touch) that an emulate call may have set.
func (s *Session) Resize(width, height int64) error {
	s.emuMu.Lock()
	defer s.emuMu.Unlock()
	s.mu.Lock()
	o := s.device
	s.mu.Unlock()
	o.Width, o.Height = width, height
	return s.emulate(o)
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
	tabID := s.tab().TargetID
	s.status = "closed"
	cancel := s.castCancel
	s.castCancel = nil
	s.castCtx = nil
	subs := s.streams
	s.streams = nil
	s.controller = nil
	s.agentLock = false
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	for _, sub := range subs {
		sub.closeWith(code, message)
	}
	s.tabCancel()
	// The tab lives in the profile's default browser context; closing the
	// target is all the cleanup it needs — profile data persists on disk.
	ctx, c := context.WithTimeout(s.browserCtx, 5*time.Second)
	_, _ = chromedp.CallBrowser(ctx, target.CloseTarget, target.CloseTargetParams{TargetID: tabID})
	c()
	s.m.removeSession(s)
}
