package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// browserTestContext launches a throwaway headless Chromium. These tests are
// opt-in because CI has no browser: run them with
//
//	NUSAMATION_BROWSER_TEST=1 go test ./internal/browser -run 'TestSnapshot|TestAction|TestFill|TestShadow|TestEval|TestDialog'
//
// NUSAMATION_BROWSER_TEST_BIN overrides the browser executable.
func browserTestContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	if os.Getenv("NUSAMATION_BROWSER_TEST") == "" {
		t.Skip("set NUSAMATION_BROWSER_TEST=1 to run browser-backed tests")
	}
	bin := os.Getenv("NUSAMATION_BROWSER_TEST_BIN")
	if bin == "" {
		for _, name := range []string{"google-chrome", "chromium", "chromium-browser", "chrome", "msedge"} {
			if p, err := exec.LookPath(name); err == nil {
				bin = p
				break
			}
		}
	}
	if bin == "" {
		t.Skip("no Chrome/Chromium binary found")
	}
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts, chromedp.ExecPath(bin), chromedp.Headless)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	tabCtx, cancelTab := chromedp.NewContext(allocCtx)
	ctx, cancelTimeout := context.WithTimeout(tabCtx, 45*time.Second)
	return ctx, func() {
		cancelTimeout()
		cancelTab()
		cancelAlloc()
	}
}

func htmlDataURL(html string) string {
	return "data:text/html;base64," + base64.StdEncoding.EncodeToString([]byte(html))
}

func mustDo(t *testing.T, ctx context.Context, actions ...chromedp.Action[chromedp.Void]) {
	t.Helper()
	if err := chromedp.Do(ctx, actions...); err != nil {
		t.Fatal(err)
	}
}

func eval[T any](t *testing.T, ctx context.Context, expression string) T {
	t.Helper()
	got, err := chromedp.Run(ctx, chromedp.Evaluate[T](expression))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func snapshotText(t *testing.T, ctx context.Context) string {
	t.Helper()
	return eval[string](t, ctx, snapshotJS)
}

func refsFor(t *testing.T, ctx context.Context, ids ...string) map[string]string {
	t.Helper()
	raw, _ := json.Marshal(ids)
	expr := `(() => { const m = {}; for (const id of ` + string(raw) + `) { const el = document.getElementById(id); m[id] = el ? el.getAttribute('data-nusa-ref') : null; } return m; })()`
	return eval[map[string]string](t, ctx, expr)
}

// refsByMap reads the snapshot registry, which also contains elements inside
// open shadow roots (document.getElementById cannot see those).
func refsByMap(t *testing.T, ctx context.Context) map[string]string {
	t.Helper()
	expr := `(() => { const m = {}; const refs = window.__nusaRefs; if (!refs) return m; for (const [ref, el] of refs) { if (el && el.id) m[el.id] = ref; } return m; })()`
	return eval[map[string]string](t, ctx, expr)
}

func TestSnapshotResolvesLabelsAndDisclosesTruncation(t *testing.T) {
	ctx, cancel := browserTestContext(t)
	defer cancel()

	html := `<!doctype html><meta charset="utf-8"><body>
<label><input type="checkbox" id="cb"> Check me</label>
<label id="mail" for="email">Account email</label>
<input id="email" aria-labelledby="mail">
<input id="search" placeholder="Search">
<label for="country">Country</label>
<select id="country"><option value="us">United States</option></select>
</body>`
	mustDo(t, ctx, chromedp.Navigate(htmlDataURL(html)))
	tree := snapshotText(t, ctx)
	for _, want := range []string{
		`checkbox "Check me"`,
		`textbox "Account email"`,
		`textbox "Search"`,
		`combobox "Country"`,
	} {
		if !strings.Contains(tree, want) {
			t.Errorf("snapshot missing %q:\n%s", want, tree)
		}
	}

	bulk := `<!doctype html><body>` + strings.Repeat(`<button>bulk</button>`, 700) + `</body>`
	mustDo(t, ctx, chromedp.Navigate(htmlDataURL(bulk)))
	tree = snapshotText(t, ctx)
	if !strings.Contains(tree, "snapshot truncated") {
		t.Fatalf("snapshot did not disclose truncation:\n%s", tree)
	}
}

func TestActionPointRejectsCoveredAndDisabled(t *testing.T) {
	ctx, cancel := browserTestContext(t)
	defer cancel()

	html := `<!doctype html><body>
<button id="btn">Go</button>
<button id="off" disabled>Off</button>
<fieldset disabled><button id="fs">Fieldset off</button></fieldset>
<div id="cover" style="position:fixed;inset:0;z-index:10"></div>
</body>`
	mustDo(t, ctx, chromedp.Navigate(htmlDataURL(html)))
	tree := snapshotText(t, ctx)
	if !strings.Contains(tree, `button "Fieldset off" [disabled]`) {
		t.Fatalf("fieldset-disabled button state missing:\n%s", tree)
	}
	refs := refsFor(t, ctx, "btn", "off", "fs")

	s := &Session{tabCtx: ctx, maintCh: make(chan func(), 8), ackCh: make(chan int64, 8)}
	if _, _, err := s.RefPoint(ctx, refs["btn"]); !errors.Is(err, ErrNotActionable) {
		t.Fatalf("RefPoint covered error = %v, want ErrNotActionable", err)
	}

	p := eval[actionPoint](t, ctx, actionPointExpr(refs["btn"]))
	if p.Status != "covered" || p.By != "#cover" {
		t.Fatalf("covered = %+v, want covered by #cover", p)
	}

	mustDo(t, ctx, chromedp.Evaluate[chromedp.Void](`document.getElementById('cover').remove()`))
	if _, _, err := s.RefPoint(ctx, refs["off"]); !errors.Is(err, ErrNotActionable) {
		t.Fatalf("RefPoint disabled error = %v, want ErrNotActionable", err)
	}
	if _, _, err := s.RefPoint(ctx, refs["fs"]); !errors.Is(err, ErrNotActionable) {
		t.Fatalf("RefPoint fieldset-disabled error = %v, want ErrNotActionable", err)
	}
	p = eval[actionPoint](t, ctx, actionPointExpr(refs["off"]))
	if p.Status != "disabled" {
		t.Fatalf("disabled = %+v, want disabled", p)
	}

	if _, _, err := s.RefPoint(ctx, refs["btn"]); err != nil {
		t.Fatalf("RefPoint ok error = %v", err)
	}
	p = eval[actionPoint](t, ctx, actionPointExpr(refs["btn"]))
	if p.Status != "ok" {
		t.Fatalf("ok = %+v, want ok", p)
	}
}

type selectResult struct {
	Status string `json:"status"`
	Detail string `json:"detail"`
}

func TestFillAndSelectRejectBadTargets(t *testing.T) {
	ctx, cancel := browserTestContext(t)
	defer cancel()

	html := `<!doctype html><body>
<input id="ok">
<input id="ro" readonly>
<input id="dis" disabled>
<select id="sel"><option value="a">Alpha</option></select>
<select id="seldis" disabled><option value="a">Alpha</option></select>
</body>`
	mustDo(t, ctx, chromedp.Navigate(htmlDataURL(html)))
	_ = snapshotText(t, ctx)
	refs := refsFor(t, ctx, "ok", "ro", "dis", "sel", "seldis")

	s := &Session{tabCtx: ctx, maintCh: make(chan func(), 8), ackCh: make(chan int64, 8)}
	if _, err := s.FillFields(ctx, []FieldValue{{Ref: refs["dis"], Value: "x"}}); !errors.Is(err, ErrNotActionable) {
		t.Fatalf("FillFields disabled error = %v, want ErrNotActionable", err)
	}
	if _, err := s.FillFields(ctx, []FieldValue{{Ref: refs["ro"], Value: "x"}}); !errors.Is(err, ErrNotActionable) {
		t.Fatalf("FillFields readonly error = %v, want ErrNotActionable", err)
	}
	if _, err := s.FillFields(ctx, []FieldValue{{Ref: refs["ok"], Value: "hello"}}); err != nil {
		t.Fatalf("FillFields ok error = %v", err)
	}
	if focused, err := s.FocusRef(ctx, refs["ok"]); err != nil || !focused {
		t.Fatalf("FocusRef ok = %v, %v", focused, err)
	}
	if _, err := s.FocusRef(ctx, refs["ro"]); !errors.Is(err, ErrNotActionable) {
		t.Fatalf("FocusRef readonly error = %v, want ErrNotActionable", err)
	}
	if _, err := s.SelectRef(ctx, refs["sel"], []string{"missing"}); !errors.Is(err, ErrInvalidAction) {
		t.Fatalf("SelectRef no-match error = %v, want ErrInvalidAction", err)
	}

	fr := eval[fieldResult](t, ctx, fillExpr([]FieldValue{{Ref: refs["dis"], Value: "x"}}))
	if fr.OK || fr.Reason != "disabled" {
		t.Fatalf("disabled fill = %+v, want disabled", fr)
	}
	fr = eval[fieldResult](t, ctx, fillExpr([]FieldValue{{Ref: refs["ro"], Value: "x"}}))
	if fr.OK || fr.Reason != "readonly" {
		t.Fatalf("readonly fill = %+v, want readonly", fr)
	}
	fr = eval[fieldResult](t, ctx, fillExpr([]FieldValue{{Ref: refs["ok"], Value: "hello"}}))
	if !fr.OK {
		t.Fatalf("ok fill = %+v, want ok", fr)
	}

	sr := eval[selectResult](t, ctx, selectExpr(refs["sel"], []string{"missing"}))
	if sr.Status != "no-match" {
		t.Fatalf("no-match = %+v, want no-match", sr)
	}
	sr = eval[selectResult](t, ctx, selectExpr(refs["seldis"], []string{"a"}))
	if sr.Status != "disabled" {
		t.Fatalf("disabled select = %+v, want disabled", sr)
	}
	sr = eval[selectResult](t, ctx, selectExpr(refs["sel"], []string{"a", "b"}))
	if sr.Status != "single-multiple" {
		t.Fatalf("single multiple = %+v, want single-multiple", sr)
	}
}

func TestShadowDOMRefsAndActions(t *testing.T) {
	ctx, cancel := browserTestContext(t)
	defer cancel()

	html := `<!doctype html><body>
<div id="host"></div>
<div id="shost"><button id="slotted">Slotted button</button></div>
<script>
  const root = document.getElementById('host').attachShadow({mode:'open'});
  root.innerHTML = '<label id="sl">Shadow email</label><input id="se" aria-labelledby="sl"><button id="sb">Shadow button</button><select id="ss"><option value="a">Alpha</option></select>';
  window.shadowClicks = 0;
  root.querySelector('#sb').addEventListener('click', () => { window.shadowClicks++; });
  document.getElementById('shost').attachShadow({mode:'open'}).innerHTML = '<slot></slot>';
  window.slottedClicks = 0;
  document.getElementById('slotted').addEventListener('click', () => { window.slottedClicks++; });
</script>
</body>`
	mustDo(t, ctx, chromedp.Navigate(htmlDataURL(html)))
	tree := snapshotText(t, ctx)
	for _, want := range []string{`textbox "Shadow email"`, `button "Shadow button"`, `combobox "Alpha"`, `button "Slotted button"`} {
		if !strings.Contains(tree, want) {
			t.Errorf("shadow snapshot missing %q:\n%s", want, tree)
		}
	}
	refs := refsByMap(t, ctx)
	for _, id := range []string{"se", "sb", "ss", "slotted"} {
		if refs[id] == "" {
			t.Fatalf("shadow element %q has no ref; refs=%v", id, refs)
		}
	}

	s := &Session{tabCtx: ctx, maintCh: make(chan func(), 8), ackCh: make(chan int64, 8)}
	if _, err := s.FillFields(ctx, []FieldValue{{Ref: refs["se"], Value: "shadow@example.com"}}); err != nil {
		t.Fatalf("shadow fill: %v", err)
	}
	if got := eval[string](t, ctx, `document.getElementById('host').shadowRoot.querySelector('#se').value`); got != "shadow@example.com" {
		t.Fatalf("shadow input value = %q", got)
	}
	if focused, err := s.FocusRef(ctx, refs["se"]); err != nil || !focused {
		t.Fatalf("shadow focus = %v, %v", focused, err)
	}
	if _, err := s.SelectRef(ctx, refs["ss"], []string{"a"}); err != nil {
		t.Fatalf("shadow select: %v", err)
	}

	x, y, err := s.RefPoint(ctx, refs["sb"])
	if err != nil {
		t.Fatalf("shadow ref point: %v", err)
	}
	if err := s.Click(x, y, "left", 1); err != nil {
		t.Fatalf("shadow click: %v", err)
	}
	if got := eval[int](t, ctx, `window.shadowClicks`); got != 1 {
		t.Fatalf("shadow clicks = %d, want 1", got)
	}

	// Slotted light DOM is rendered through a shadow slot; it must remain
	// actionable (not reported as covered by the slot).
	px, py, err := s.RefPoint(ctx, refs["slotted"])
	if err != nil {
		t.Fatalf("slotted ref point: %v", err)
	}
	if err := s.Click(px, py, "left", 1); err != nil {
		t.Fatalf("slotted click: %v", err)
	}
	if got := eval[int](t, ctx, `window.slottedClicks`); got != 1 {
		t.Fatalf("slotted clicks = %d, want 1", got)
	}

	// An overlay inside the same shadow root must block the button.
	mustDo(t, ctx, chromedp.Evaluate[chromedp.Void](`
    (() => {
      const ov = document.createElement('div');
      ov.id = 'sover';
      ov.style.cssText = 'position:fixed;inset:0;z-index:10';
      document.getElementById('host').shadowRoot.appendChild(ov);
    })()`))
	p := eval[actionPoint](t, ctx, actionPointExpr(refs["sb"]))
	if p.Status != "covered" || p.By != "#sover" {
		t.Fatalf("shadow covered = %+v, want covered by #sover", p)
	}
}

func TestEvalUnserializableValues(t *testing.T) {
	ctx, cancel := browserTestContext(t)
	defer cancel()
	// Allocate the browser on the test context first: Eval uses its own
	// timeout context, and the first chromedp.Call would otherwise tie the
	// browser lifetime to that short-lived context.
	mustDo(t, ctx, chromedp.Navigate("about:blank"))

	s := &Session{tabCtx: ctx, maintCh: make(chan func(), 8), ackCh: make(chan int64, 8)}
	cases := []struct {
		expr string
		want string
	}{
		{`10n`, `"10n"`},
		{`NaN`, `"NaN"`},
		{`Infinity`, `"Infinity"`},
		{`-Infinity`, `"-Infinity"`},
		{`-0`, `-0`},
		{`1+1`, `2`},
	}
	for _, tc := range cases {
		got, err := s.Eval(ctx, tc.expr)
		if err != nil {
			t.Fatalf("Eval(%s): %v", tc.expr, err)
		}
		if string(got) != tc.want {
			t.Errorf("Eval(%s) = %s, want %s", tc.expr, got, tc.want)
		}
	}
}

func TestDialogAutoDismiss(t *testing.T) {
	ctx, cancel := browserTestContext(t)
	defer cancel()
	if _, err := chromedp.Call(ctx, page.Enable, page.EnableParams{}); err != nil {
		t.Fatal(err)
	}

	s := &Session{
		tabCtx:  ctx,
		maintCh: make(chan func(), 8),
		ackCh:   make(chan int64, 8),
	}
	go s.maintLoop()
	go s.watchDialogs()
	// Give the event iterator time to subscribe before the dialog opens.
	mustDo(t, ctx, chromedp.Sleep(200*time.Millisecond))

	html := `<!doctype html><body><button id="a" onclick="alert('hi')">Alert</button></body>`
	mustDo(t, ctx, chromedp.Navigate(htmlDataURL(html)))

	got := eval[int](t, ctx, `(document.getElementById('a').click(), 1)`)
	if got != 1 {
		t.Fatalf("click = %d, want 1", got)
	}
	if v := eval[int](t, ctx, `1+1`); v != 2 {
		t.Fatalf("eval = %d, want 2", v)
	}
}
