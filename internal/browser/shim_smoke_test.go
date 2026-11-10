package browser

// selectShimJS smoke test: opens the listbox via a real dispatched mouse
// event, checks the search field only appears above 5 options, and verifies
// filtering plus Enter-commit drive the underlying select's value + events.
// Opt-in like the other browser tests: NUSAMATION_BROWSER_TEST=1.

import (
	"context"
	"testing"

	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
)

func dispatchMouse(t *testing.T, ctx context.Context, x, y float64) {
	t.Helper()
	for _, ty := range []input.DispatchMouseEventType{
		input.DispatchMouseEventTypeMousePressed, input.DispatchMouseEventTypeMouseReleased,
	} {
		if _, err := chromedp.Call(ctx, input.DispatchMouseEvent, input.DispatchMouseEventParams{
			Type: ty, X: x, Y: y, Button: input.MouseButtonLeft, ClickCount: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSelectShimSmoke(t *testing.T) {
	ctx, cancel := browserTestContext(t)
	defer cancel()

	html := `<html><body>
<select id="small"><option value="a">Alpha</option><option value="b">Beta</option><option value="c">Gamma</option></select>
<select id="big" style="margin-top:60px"><option value="1">One</option><option value="2">Two</option>
<optgroup label="Tens"><option value="10">Ten</option><option value="20" disabled>Twenty</option></optgroup>
<option value="3">Three</option><option value="4">Four</option><option value="5">Five</option><option value="6">Six</option></select>
<script>window.changes=0;document.getElementById('big').addEventListener('change',()=>window.changes++);</script>
</body></html>`
	mustDo(t, ctx, chromedp.Navigate(htmlDataURL(html)))
	_ = eval[bool](t, ctx, selectShimJS+";true")

	// --- small select: listbox opens, NO search field (3 <= 5) ---
	r := eval[map[string]float64](t, ctx, `(()=>{const r=document.getElementById('small').getBoundingClientRect();return{x:r.left+r.width/2,y:r.top+r.height/2}})()`)
	dispatchMouse(t, ctx, r["x"], r["y"])
	if n := eval[int](t, ctx, `document.querySelectorAll('[data-nusa-listbox]').length`); n != 1 {
		t.Fatalf("small: listbox count = %d, want 1", n)
	}
	if eval[bool](t, ctx, `!!document.querySelector('[data-nusa-listbox-search]')`) {
		t.Fatal("small: search field present for <=5 options")
	}
	if n := eval[int](t, ctx, `document.querySelectorAll('[data-nusa-listbox] > div').length`); n != 3 {
		t.Fatalf("small: rows = %d, want 3", n)
	}
	// click second row -> value commits
	_ = eval[bool](t, ctx, `document.querySelectorAll('[data-nusa-listbox] > div')[1].dispatchEvent(new MouseEvent('click',{bubbles:true}))`)
	if v := eval[string](t, ctx, `document.getElementById('small').value`); v != "b" {
		t.Fatalf("small: value = %q, want b", v)
	}
	if n := eval[int](t, ctx, `document.querySelectorAll('[data-nusa-listbox]').length`); n != 0 {
		t.Fatalf("small: listbox still open after commit")
	}

	// --- big select: search field present (8 > 5), optgroup header, filter ---
	r = eval[map[string]float64](t, ctx, `(()=>{const r=document.getElementById('big').getBoundingClientRect();return{x:r.left+r.width/2,y:r.top+r.height/2}})()`)
	dispatchMouse(t, ctx, r["x"], r["y"])
	if !eval[bool](t, ctx, `!!document.querySelector('[data-nusa-listbox-search]')`) {
		t.Fatal("big: search field missing for >5 options")
	}
	// 8 rows + 1 optgroup header + 1 search = children of box: header is a div, so divs = 9
	if n := eval[int](t, ctx, `document.querySelectorAll('[data-nusa-listbox] > div').length`); n != 9 {
		t.Fatalf("big: div children = %d, want 9 (8 rows + 1 optgroup head)", n)
	}
	// type "t" -> One,Two,Three,Ten,(Twenty disabled but text matches) show; 3,4,5,6? "Three" has t; Four/Five/Six hide
	_ = eval[bool](t, ctx, `(()=>{const s=document.querySelector('[data-nusa-listbox-search]');s.value='t';s.dispatchEvent(new Event('input',{bubbles:true}));return true})()`)
	visible := eval[int](t, ctx, `[...document.querySelectorAll('[data-nusa-listbox] > div')].filter(d=>!d.hidden).length`)
	// 't' matches Two, Ten, Twenty, Three => 4 rows + the Tens group head
	if visible != 5 {
		t.Fatalf("big: visible divs after filter 't' = %d, want 5 (4 rows + group head)", visible)
	}
	// Enter commits the highlighted (first visible = Two)
	_ = eval[bool](t, ctx, `document.querySelector('[data-nusa-listbox-search]').dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true}))`)
	if v := eval[string](t, ctx, `document.getElementById('big').value`); v != "2" {
		t.Fatalf("big: value = %q, want 2", v)
	}
	if n := eval[int](t, ctx, `window.changes`); n != 1 {
		t.Fatalf("big: change events = %d, want 1", n)
	}
}
