package browser

import (
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
)

func TestTouchGestureDispatch(t *testing.T) {
	ctx, cancel := browserTestContext(t)
	defer cancel()
	mustDo(t, ctx, chromedp.Navigate(htmlDataURL(`<body style="height:2000px"><script>
		window.events=[];
		for (const type of ['touchstart','touchmove','touchend'])
		  document.addEventListener(type, () => events.push(type));
	</script></body>`)))
	s := &Session{tabCtx: ctx}
	for _, step := range []struct {
		phase string
		y     float64
	}{{"touchStart", 300}, {"touchMove", 200}, {"touchEnd", 200}} {
		if err := s.Touch(step.phase, 100, step.y); err != nil {
			t.Fatal(err)
		}
	}
	got := eval[string](t, ctx, `events.join(',')`)
	if !strings.Contains(got, "touchstart,touchmove,touchend") {
		t.Fatalf("touch sequence = %q", got)
	}
	if err := s.Touch("invalid", 0, 0); err == nil {
		t.Fatal("invalid touch phase accepted")
	}
}
