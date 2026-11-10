package browser

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/chromedp/chromedp"
)

func TestRegisterSecretsDeterministicSmallestTag(t *testing.T) {
	s := &Session{}
	s.RegisterSecrets(map[string]string{"v": "[[env:B]]"})
	s.RegisterSecrets(map[string]string{"v": "[[env:A]]"})
	if got := s.MaskSecrets("v v"); got != "[[env:A]] [[env:A]]" {
		t.Fatalf("smallest tag should win, got %q", got)
	}
}

func TestMaskSecretsLongestFirstNonRecursive(t *testing.T) {
	s := &Session{}
	s.RegisterSecrets(map[string]string{
		"secret":        "[[env:S]]",
		"secret-longer": "[[env:L]]",
		"[[env:S]]-tag": "[[env:M]]",
	})
	got := s.MaskSecrets("secret-longer and secret and [[env:S]]-tag")
	want := "[[env:L]] and [[env:S]] and [[env:M]]"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestMaskSecretsShortValuesMasked(t *testing.T) {
	s := &Session{}
	s.RegisterSecrets(map[string]string{"x": "[[env:X]]", "1": "[[env:ONE]]"})
	if got := s.MaskSecrets("x1x"); got != "[[env:X]][[env:ONE]][[env:X]]" {
		t.Fatalf("got %q", got)
	}
}

func TestMaskSecretsSessionIsolation(t *testing.T) {
	a, b := &Session{}, &Session{}
	a.RegisterSecrets(map[string]string{"mine": "[[env:A]]"})
	if got := b.MaskSecrets("mine"); got != "mine" {
		t.Fatalf("other session masked: %q", got)
	}
}

func TestSecretsConcurrentRegisterAndMask(t *testing.T) {
	s := &Session{}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				s.RegisterSecrets(map[string]string{"val": "[[env:T]]"})
			}
		}(i)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = s.MaskSecrets("val")
			}
		}()
	}
	wg.Wait()
	if got := s.MaskSecrets("val"); got != "[[env:T]]" {
		t.Fatalf("got %q", got)
	}
}

/* ---------------- browser-backed InsertSecretText ---------------- */

func secretSession(t *testing.T, ctx context.Context) *Session {
	t.Helper()
	return &Session{tabCtx: ctx, maintCh: make(chan func(), 8), ackCh: make(chan int64, 8)}
}

func TestInsertSecretTextReplacesAndMasks(t *testing.T) {
	if testing.Short() {
		t.Skip("browser test")
	}
	ctx, cancel := browserTestContext(t)
	defer cancel()
	html := `<input id="i" value="old-value"><input id="ro" readonly value="x"><input id="dis" disabled><input id="ml" maxlength="4" value="">`
	mustDo(t, ctx, chromedp.Navigate(htmlDataURL(html)))
	eval[any](t, ctx, `(() => { window.__nusaRefs = new Map(); window.__nusaRefs.set('i', document.getElementById('i')); window.__nusaRefs.set('ro', document.getElementById('ro')); window.__nusaRefs.set('dis', document.getElementById('dis')); window.__nusaRefs.set('ml', document.getElementById('ml')); window.__nusaRefs.set('cb', (() => { const c = document.createElement('input'); c.type='checkbox'; document.body.appendChild(c); return c; })()); })()`)

	s := secretSession(t, ctx)
	if err := s.InsertSecretText(ctx, "i", "s3cret", true); err != nil {
		t.Fatalf("replace fill: %v", err)
	}
	if got := eval[string](t, ctx, `document.getElementById('i').value`); got != "s3cret" {
		t.Fatalf("DOM value %q — fill must replace, not append", got)
	}
	if got := eval[string](t, ctx, `document.getElementById('i').style.webkitTextSecurity`); got == "" {
		t.Fatal("text-security style not applied")
	}
	if err := s.InsertSecretText(ctx, "ro", "x", true); !errors.Is(err, ErrNotActionable) {
		t.Fatalf("readonly: got %v", err)
	}
	if err := s.InsertSecretText(ctx, "dis", "x", true); !errors.Is(err, ErrNotActionable) {
		t.Fatalf("disabled: got %v", err)
	}
	if err := s.InsertSecretText(ctx, "missing", "x", true); !errors.Is(err, ErrRefNotFound) {
		t.Fatalf("missing ref: got %v", err)
	}
	if err := s.InsertSecretText(ctx, "cb", "x", true); !errors.Is(err, ErrInvalidAction) {
		t.Fatalf("checkbox: got %v", err)
	}
	// maxlength fails before altering the field
	if err := s.InsertSecretText(ctx, "ml", "too-long-secret", true); !errors.Is(err, ErrInvalidAction) {
		t.Fatalf("maxlength: got %v", err)
	}
	if got := eval[string](t, ctx, `document.getElementById('ml').value`); got != "" {
		t.Fatalf("maxlength check modified the field: %q", got)
	}
	// type without ref goes to the focused element and appends
	eval[any](t, ctx, `document.getElementById('i').focus()`)
	if err := s.InsertSecretText(ctx, "", "-tail", false); err != nil {
		t.Fatalf("append type: %v", err)
	}
	if got := eval[string](t, ctx, `document.getElementById('i').value`); got != "s3cret-tail" {
		t.Fatalf("append result %q", got)
	}
}

func TestInsertSecretTextChangeEventOnReplace(t *testing.T) {
	if testing.Short() {
		t.Skip("browser test")
	}
	ctx, cancel := browserTestContext(t)
	defer cancel()
	mustDo(t, ctx, chromedp.Navigate(htmlDataURL(`<input id="i" value="v"><script>window.__events=[];const el=document.getElementById('i');for(const n of ['input','change'])el.addEventListener(n,e=>__events.push(e.type));window.__nusaRefs=new Map([['i',el]]);</script>`)))
	s := secretSession(t, ctx)
	if err := s.InsertSecretText(ctx, "i", "new", true); err != nil {
		t.Fatal(err)
	}
	events := eval[[]string](t, ctx, `window.__events`)
	joined := strings.Join(events, ",")
	if !strings.Contains(joined, "input") || !strings.Contains(joined, "change") {
		t.Fatalf("want input+change events, got %q", joined)
	}
}

func TestInsertSecretTextEdgeTargets(t *testing.T) {
	if testing.Short() {
		t.Skip("browser test")
	}
	ctx, cancel := browserTestContext(t)
	defer cancel()
	html := `<input id="em" type="email" value="old@x.io"><input id="num" type="number" value="42"><div id="ce" contenteditable>mid text</div><div id="host"></div>`
	mustDo(t, ctx, chromedp.Navigate(htmlDataURL(html)))
	eval[any](t, ctx, `(() => {
	  const map = new Map();
	  map.set('em', document.getElementById('em'));
	  map.set('num', document.getElementById('num'));
	  map.set('ce', document.getElementById('ce'));
	  const h = document.getElementById('host').attachShadow({mode:'open'});
	  h.innerHTML = '<input id="sh" value="in-shadow">';
	  map.set('sh', h.getElementById('sh'));
	  window.__nusaRefs = map;
	})()`)
	s := secretSession(t, ctx)
	// email input: select() unsupported on some types — must still replace, not append
	if err := s.InsertSecretText(ctx, "em", "s3cret", true); err != nil {
		t.Fatalf("email replace: %v", err)
	}
	if got := eval[string](t, ctx, `document.getElementById('em').value`); got != "s3cret" {
		t.Fatalf("email value %q", got)
	}
	// number input normalizes non-numeric text — mismatch must surface, not claim ok
	if err := s.InsertSecretText(ctx, "num", "not-a-number", true); !errors.Is(err, ErrInvalidAction) {
		t.Fatalf("number normalize: got %v", err)
	}
	if err := s.InsertSecretText(ctx, "num", "77", true); err != nil {
		t.Fatalf("number replace: %v", err)
	}
	// contenteditable mid-caret append
	eval[any](t, ctx, `(() => { const el = document.getElementById('ce'); el.focus(); const r = document.createRange(); r.setStart(el.firstChild, 3); r.collapse(true); const s = window.getSelection(); s.removeAllRanges(); s.addRange(r); })()`)
	if err := s.InsertSecretText(ctx, "ce", "X", false); err != nil {
		t.Fatalf("ce type: %v", err)
	}
	if got := eval[string](t, ctx, `document.getElementById('ce').textContent`); got != "midX text" {
		t.Fatalf("ce result %q", got)
	}
	// open shadow root ref works
	if err := s.InsertSecretText(ctx, "sh", "sh-secret", true); err != nil {
		t.Fatalf("shadow ref: %v", err)
	}
	// focus redirected to another field fails without typing into it
	eval[any](t, ctx, `(() => { const el = document.getElementById('em'); el.focus = () => document.getElementById('num').focus(); })()`)
	if err := s.InsertSecretText(ctx, "em", "leak", false); !errors.Is(err, ErrNotActionable) {
		t.Fatalf("redirected focus: got %v", err)
	}
	if got := eval[string](t, ctx, `document.getElementById('num').value`); got != "77" {
		t.Fatalf("other field was typed into: %q", got)
	}
}
