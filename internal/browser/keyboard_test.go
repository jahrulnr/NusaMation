package browser

import (
	"testing"

	"github.com/chromedp/chromedp"
)

func TestFocusedEditable(t *testing.T) {
	ctx, cancel := browserTestContext(t)
	defer cancel()
	mustDo(t, ctx, chromedp.Navigate(htmlDataURL(`<input id="text"><input id="check" type="checkbox"><textarea id="area"></textarea><div id="edit" contenteditable></div>`)))
	s := &Session{tabCtx: ctx}
	for _, tc := range []struct {
		id   string
		want bool
	}{{"text", true}, {"check", false}, {"area", true}, {"edit", true}} {
		_ = eval[bool](t, ctx, `(() => { document.getElementById("`+tc.id+`").focus(); return true })()`)
		got, err := s.FocusedEditable()
		if err != nil || got != tc.want {
			t.Errorf("%s: got %v, err %v; want %v", tc.id, got, err, tc.want)
		}
	}
}
