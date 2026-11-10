package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

/* ---------------- secret registry ----------------
   The session keeps the raw values it was asked to type so the /api/browser-use
   response writer can swap them back for their opaque [[env:NAME]] tag. Values
   live only in memory and die with the session; there is no global redaction,
   just exact-match masking of what this session registered. */

// RegisterSecrets records raw -> tag bindings. When two names share a value the
// lexicographically smallest tag wins so masking output stays deterministic.
// Rotation and navigation only ever add bindings until the session closes.
func (s *Session) RegisterSecrets(bindings map[string]string) {
	if len(bindings) == 0 {
		return
	}
	s.secretsMu.Lock()
	defer s.secretsMu.Unlock()
	if s.secretTags == nil {
		s.secretTags = map[string]string{}
	}
	changed := false
	for raw, tag := range bindings {
		if raw == "" {
			continue
		}
		if old, ok := s.secretTags[raw]; !ok || tag < old {
			s.secretTags[raw] = tag
			changed = true
		}
	}
	if !changed {
		return
	}
	raws := make([]string, 0, len(s.secretTags))
	for raw := range s.secretTags {
		raws = append(raws, raw)
	}
	sort.Slice(raws, func(i, j int) bool {
		if len(raws[i]) != len(raws[j]) {
			return len(raws[i]) > len(raws[j])
		}
		return raws[i] < raws[j]
	})
	pairs := make([]string, 0, 2*len(raws))
	for _, raw := range raws {
		pairs = append(pairs, raw, s.secretTags[raw])
	}
	s.secretReplacer = strings.NewReplacer(pairs...)
}

// MaskSecrets replaces every registered raw value with its tag. Longest values
// run first (byte length, ties lexicographically) through a nonrecursive
// Replacer, so a secret that contains another registered value or a tag
// cannot expand or reorder the output.
func (s *Session) MaskSecrets(text string) string {
	if text == "" {
		return text
	}
	s.secretsMu.Lock()
	r := s.secretReplacer
	s.secretsMu.Unlock()
	if r == nil {
		return text
	}
	return r.Replace(text)
}

/* ---------------- secret text fill ---------------- */

// secretTargetJS resolves the fill target once so every later step runs
// against the same object handle: a ref via __nusaRefs, or the deep active
// element through open shadow roots when no ref is given.
const secretTargetJS = `(() => {
  const el = (%s) ? ((window.__nusaRefs && window.__nusaRefs.get(%s)) || null) : (() => {
    let node = document.activeElement;
    while (node && node.shadowRoot && node.shadowRoot.activeElement) node = node.shadowRoot.activeElement;
    return node;
  })();
  return el;
})()`

// secretPrepJS runs against the element handle. It validates the target as an
// editable textbox, focuses it (and proves focus landed), measures the current
// selection — including Range offsets inside contentEditable — and rejects
// maxlength overflows BEFORE any value, style or selection mutation. Only then
// does it mask the display and stage the replacement: select-all where the
// element supports it, an empty native-setter clear otherwise. It returns the
// metadata the finish call needs; nothing is stored on the DOM.
const secretPrepJS = `function(replace, insLen) {
  const el = this;
  if (!el || !el.isConnected) return {status: 'missing'};
  if (el.matches(':disabled') || el.getAttribute('aria-disabled') === 'true') return {status: 'disabled'};
  const tag = el.tagName;
  const ce = !!el.isContentEditable;
  const ty = (el.getAttribute('type') || '').toLowerCase();
  const nonText = ['checkbox','radio','button','submit','file','range','color','image','hidden','reset'].includes(ty);
  if (!ce && tag !== 'TEXTAREA' && (tag !== 'INPUT' || nonText)) return {status: 'noteditable'};
  if (el.readOnly || el.getAttribute('aria-readonly') === 'true') return {status: 'readonly'};
  el.scrollIntoView({block: 'center', inline: 'center', behavior: 'instant'});
  try { el.focus({preventScroll: true}); } catch (e) { el.focus(); }
  const deepActive = (root) => {
    let node = root.activeElement;
    while (node && node.shadowRoot && node.shadowRoot.activeElement) node = node.shadowRoot.activeElement;
    return node;
  };
  if (deepActive(document) !== el) return {status: 'focusmiss'};
  const cur = ce ? (el.textContent || '') : (el.value || '');
  let ss = cur.length, se = cur.length;
  if (ce) {
    const sel = window.getSelection();
    if (sel && sel.rangeCount && el.contains(sel.anchorNode)) {
      const r = sel.getRangeAt(0).cloneRange();
      const pre = document.createRange();
      pre.selectNodeContents(el);
      try { pre.setEnd(r.startContainer, r.startOffset); ss = pre.toString().length; se = ss + r.toString().length; } catch (e) {}
    }
  } else if (typeof el.selectionStart === 'number' && el.selectionStart !== null) {
    ss = el.selectionStart;
    se = el.selectionEnd;
  }
  const ml = typeof el.maxLength === 'number' ? el.maxLength : -1;
  const needed = replace ? insLen : (cur.length - (se - ss) + insLen);
  if (ml >= 0 && needed > ml) return {status: 'maxlength'};
  try { el.style.setProperty('-webkit-text-security', 'disc', 'important'); } catch (e) {}
  if (replace) {
    if (ce) {
      try {
        const r = document.createRange();
        r.selectNodeContents(el);
        const sel = window.getSelection();
        sel.removeAllRanges();
        sel.addRange(r);
      } catch (e) {}
    } else {
      let covered = false;
      try { el.select(); covered = el.selectionStart === 0 && el.selectionEnd === cur.length; } catch (e) {}
      if (!covered) { try { el.setSelectionRange(0, cur.length); covered = el.selectionStart === 0 && el.selectionEnd === cur.length; } catch (e) {} }
      if (!covered) {
        const proto = tag === 'TEXTAREA' ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
        const d = Object.getOwnPropertyDescriptor(proto, 'value');
        try { if (d && d.set) d.set.call(el, ''); else el.value = ''; } catch (e) { el.value = ''; }
        try { el.dispatchEvent(new Event('input', {bubbles: true})); } catch (e) {}
      }
    }
  }
  return {status: 'ok', cur: cur, ss: ss, se: se};
}`

// secretFinishJS compares the element's actual text to the intended result and
// fires change for replace fills (matching the native-setter contract). The
// secret and the prep metadata arrive as call arguments, never as JS source.
const secretFinishJS = `function(text, st) {
  const el = this;
  if (!el || !el.isConnected) return {status: 'gone'};
  if (st && st.replace) { try { el.dispatchEvent(new Event('change', {bubbles: true})); } catch (e) {} }
  const ce = !!el.isContentEditable;
  const actual = ce ? (el.textContent || '') : (el.value !== undefined ? el.value : '');
  const base = String(st && st.cur || '');
  const expected = st && st.replace ? text : base.slice(0, (st && st.ss) || 0) + text + base.slice((st && st.se) || 0);
  return {status: actual === expected ? 'ok' : 'mismatch'};
}`

func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }

// InsertSecretText types secret text into an editable element through real CDP
// text input — the raw value never appears in a JavaScript expression. With
// replace it replaces the existing contents so the call keeps fill-form
// semantics; without ref it types into the deep-focused element like "type".
// One element handle and one busy claim span the whole sequence.
func (s *Session) InsertSecretText(ctx context.Context, ref, text string, replace bool) error {
	if err := s.acquire(); err != nil {
		return err
	}
	defer s.busy.Store(false)

	refJSON, _ := json.Marshal(ref)
	byValue := true

	objCtx, objCancel := context.WithTimeout(s.tabCtx, 10*time.Second)
	objRes, err := chromedp.Call(objCtx, runtime.Evaluate, runtime.EvaluateParams{
		Expression: fmt.Sprintf(secretTargetJS, refJSON, refJSON),
	})
	objCancel()
	if err != nil {
		return err
	}
	if objRes.Result == nil || objRes.Result.ObjectID == "" {
		if ref != "" {
			return ErrRefNotFound
		}
		return fmt.Errorf("%w: no editable element has focus", ErrInvalidAction)
	}
	objID := objRes.Result.ObjectID
	defer func() {
		relCtx, relCancel := context.WithTimeout(s.tabCtx, 5*time.Second)
		_, _ = chromedp.Call(relCtx, runtime.ReleaseObject, runtime.ReleaseObjectParams{ObjectID: objID})
		relCancel()
	}()

	call := func(decl string, args ...any) (runtime.CallFunctionOnResult, error) {
		callArgs := make([]*runtime.CallArgument, 0, len(args))
		for _, a := range args {
			b, _ := json.Marshal(a)
			callArgs = append(callArgs, &runtime.CallArgument{Value: b})
		}
		cctx, cancel := context.WithTimeout(s.tabCtx, 10*time.Second)
		res, err := chromedp.Call(cctx, runtime.CallFunctionOn, runtime.CallFunctionOnParams{
			FunctionDeclaration: decl,
			ObjectID:            objID,
			Arguments:           callArgs,
			ReturnByValue:       &byValue,
		})
		cancel()
		return res, err
	}

	prepRes, err := call(secretPrepJS, replace, utf16Len(text))
	if err != nil {
		return err
	}
	if prepRes.Result == nil {
		return fmt.Errorf("%w: secret fill prepare failed", ErrInvalidAction)
	}
	if prepRes.ExceptionDetails != nil {
		return fmt.Errorf("%w: secret fill prepare failed", ErrInvalidAction)
	}
	var prep struct {
		Status string `json:"status"`
		Cur    string `json:"cur"`
		SS     int    `json:"ss"`
		SE     int    `json:"se"`
	}
	if err := json.Unmarshal(prepRes.Result.Value, &prep); err != nil {
		return fmt.Errorf("secret fill prepare: %w", err)
	}
	switch prep.Status {
	case "ok":
	case "missing":
		if ref != "" {
			return ErrRefNotFound
		}
		return fmt.Errorf("%w: no editable element has focus", ErrInvalidAction)
	case "disabled", "focusmiss":
		return fmt.Errorf("%w: target is not actionable", ErrNotActionable)
	case "readonly":
		return fmt.Errorf("%w: target is read-only", ErrNotActionable)
	case "maxlength":
		return fmt.Errorf("%w: value would exceed the field's maxlength", ErrInvalidAction)
	default:
		return fmt.Errorf("%w: target is not a text-editable element", ErrInvalidAction)
	}

	ioctx, cancel := s.ioCtx()
	_, err = chromedp.Call(ioctx, input.InsertText, input.InsertTextParams{Text: text})
	cancel()
	if err != nil {
		return err
	}

	meta := map[string]any{"replace": replace, "cur": prep.Cur, "ss": prep.SS, "se": prep.SE}
	finRes, err := call(secretFinishJS, text, meta)
	if err != nil {
		return err
	}
	if finRes.Result == nil || finRes.ExceptionDetails != nil {
		return fmt.Errorf("%w: could not confirm the filled value", ErrInvalidAction)
	}
	var out struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(finRes.Result.Value, &out); err != nil {
		return fmt.Errorf("secret fill verify: %w", err)
	}
	if out.Status != "ok" {
		return fmt.Errorf("%w: the applied value did not match the intended text", ErrInvalidAction)
	}
	return nil
}
