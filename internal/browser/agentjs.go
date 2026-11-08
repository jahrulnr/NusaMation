package browser

import (
	"encoding/json"
	"fmt"
)

// This file holds the JavaScript the agent-facing tools evaluate in the tab.
// The Playwright-style element flow is: browser_snapshot walks the DOM, stamps
// interactive elements with data-nusa-ref, and returns an indented aria-like
// tree; ref-taking actions resolve the attribute back to viewport coordinates.

const refAttr = "data-nusa-ref"

// consoleTapJS is installed with addScriptToEvaluateOnNewDocument so it runs
// before page scripts on every navigation. console_messages reads the buffer.
const consoleTapJS = `(function(){
  if (window.__nusaConsole) return;
  const buf = [];
  Object.defineProperty(window, '__nusaConsole', {value: buf});
  const fmt = a => {
    if (typeof a === 'string') return a;
    if (a instanceof Error) return a.stack || a.message || String(a);
    try { const s = JSON.stringify(a); return s === undefined ? String(a) : s; } catch (e) { return String(a); }
  };
  const push = (level, text) => { try { buf.push({level: level, text: text, ts: Date.now()}); if (buf.length > 200) buf.shift(); } catch (e) {} };
  for (const lvl of ['log', 'info', 'warn', 'error', 'debug']) {
    const orig = console[lvl] && console[lvl].bind(console);
    if (!orig) continue;
    console[lvl] = function (...args) { push(lvl, args.map(fmt).join(' ')); return orig(...args); };
  }
  window.addEventListener('error', e => push('error', '[pageerror] ' + e.message + ' @' + (e.filename || '') + ':' + (e.lineno || 0)));
  window.addEventListener('unhandledrejection', e => push('error', '[unhandledrejection] ' + fmt(e.reason)));
})()`

// snapshotJS serializes the visible DOM into a compact tree. Interactive
// elements get [ref=eN]; landmarks keep their role; leaf text becomes a text
// line; generic wrappers are flattened so the output stays small.
const snapshotJS = `(() => {
  const REF = '` + refAttr + `';
  document.querySelectorAll('[' + REF + ']').forEach(e => e.removeAttribute(REF));
  let n = 0;
  const lines = [];
  const refs = new Map();
  window.__nusaRefs = refs;
  const skip = new Set(['SCRIPT','STYLE','NOSCRIPT','TEMPLATE','META','LINK','BR','HR','SVG','CANVAS','PATH','HEAD']);
  const roleFor = el => {
    const r = el.getAttribute('role');
    if (r) return r;
    const t = el.tagName;
    if (t === 'A') return 'link';
    if (t === 'BUTTON') return 'button';
    if (t === 'SELECT') return 'combobox';
    if (t === 'TEXTAREA') return 'textbox';
    if (t === 'SUMMARY') return 'button';
    if (t === 'OPTION') return 'option';
    if (t === 'INPUT') {
      const ty = (el.getAttribute('type') || 'text').toLowerCase();
      if (ty === 'hidden') return '';
      if (['button','submit','reset','image'].includes(ty)) return 'button';
      if (ty === 'checkbox') return 'checkbox';
      if (ty === 'radio') return 'radio';
      if (ty === 'range') return 'slider';
      return 'textbox';
    }
    if (/^H[1-6]$/.test(t)) return 'heading';
    if (t === 'IMG') return 'img';
    if (t === 'NAV') return 'navigation';
    if (t === 'MAIN') return 'main';
    if (t === 'FORM') return 'form';
    if (t === 'TABLE') return 'table';
    if (t === 'TR') return 'row';
    if (t === 'TD' || t === 'TH') return 'cell';
    if (t === 'UL' || t === 'OL') return 'list';
    if (t === 'LI') return 'listitem';
    if (t === 'FOOTER') return 'contentinfo';
    if (t === 'HEADER') return 'banner';
    if (t === 'ASIDE') return 'complementary';
    if (t === 'VIDEO' || t === 'AUDIO') return t.toLowerCase();
    if (t === 'IFRAME') return 'iframe';
    return '';
  };
  const interactive = el => {
    const t = el.tagName;
    if (['A','BUTTON','SELECT','TEXTAREA','SUMMARY'].includes(t)) return true;
    if (t === 'INPUT' && (el.getAttribute('type') || 'text').toLowerCase() !== 'hidden') return true;
    if (t === 'IFRAME' || el.isContentEditable) return true;
    const r = el.getAttribute('role');
    if (r && ['button','link','checkbox','radio','textbox','combobox','listbox','option','switch','tab','menuitem','menuitemcheckbox','menuitemradio','treeitem','slider','spinbutton','searchbox'].includes(r)) return true;
    if (el.hasAttribute('onclick')) return true;
    if (el.hasAttribute('tabindex') && el.tabIndex >= 0) return true;
    return false;
  };
  const rootOf = el => { try { return el.getRootNode(); } catch (e) { return document; } };
  const byId = (root, id) => {
    if (!root) return null;
    if (root.getElementById) return root.getElementById(id);
    try { return root.querySelector('#' + CSS.escape(id)); } catch (e) { return null; }
  };
  const labelledBy = el => {
    const ids = (el.getAttribute('aria-labelledby') || '').split(/\s+/).filter(Boolean);
    const root = rootOf(el);
    let s = '';
    for (const id of ids) {
      const n = byId(root, id);
      if (n) s += ' ' + (n.innerText || n.textContent || '');
    }
    return s.replace(/\s+/g, ' ').trim();
  };
  const labelFor = el => {
    let s = '';
    try { if (el.labels) for (const l of el.labels) s += ' ' + (l.innerText || l.textContent || ''); } catch (e) {}
    if (!s) {
      const id = el.getAttribute('id');
      const root = rootOf(el);
      if (id && root.querySelector) {
        const l = root.querySelector('label[for="' + CSS.escape(id) + '"]');
        if (l) s = l.innerText || l.textContent || '';
      }
    }
    return s.replace(/\s+/g, ' ').trim();
  };
  const nameOf = el => {
    let s = labelledBy(el) || el.getAttribute('aria-label') || labelFor(el) || el.getAttribute('alt') || el.getAttribute('title') || el.getAttribute('placeholder') || '';
    const t = el.tagName;
    if (!s && t === 'INPUT') {
      const ty = (el.getAttribute('type') || 'text').toLowerCase();
      if (['button','submit','reset'].includes(ty)) s = el.value || '';
      else s = el.getAttribute('value') || '';
    }
    if (!s && t === 'SELECT') { const o = el.options[el.selectedIndex]; s = o ? o.text : ''; }
    if (!s) s = (el.innerText || el.textContent || '');
    return s.replace(/\s+/g, ' ').trim().slice(0, 80);
  };
  const visible = el => {
    if (el.tagName === 'OPTION') return true;
    const cs = getComputedStyle(el);
    if (cs.display === 'none' || cs.visibility === 'hidden' || cs.visibility === 'collapse') return false;
    if (el.getAttribute('aria-hidden') === 'true') return false;
    if (cs.display === 'contents') return true;
    return el.getClientRects().length > 0 || el === document.body;
  };
  const stateOf = el => {
    const bits = [];
    if (el.matches(':disabled')) bits.push('disabled');
    if (el.checked) bits.push('checked');
    if (el.getAttribute('aria-expanded') === 'true') bits.push('expanded');
    if (el.getAttribute('aria-expanded') === 'false') bits.push('collapsed');
    if (el.required) bits.push('required');
    return bits.length ? ' [' + bits.join(', ') + ']' : '';
  };
  const MAX_DEPTH = 24, MAX_LINES = 600;
  let truncated = false;
  const walkChildren = (el, depth) => {
    for (const c of el.children) walk(c, depth);
    if (el.shadowRoot) for (const c of el.shadowRoot.children) walk(c, depth);
  };
  const walk = (el, depth) => {
    if (!el) return;
    if (depth > MAX_DEPTH || lines.length > MAX_LINES) { truncated = true; return; }
    if (skip.has(el.tagName) || !visible(el)) return;
    const role = roleFor(el);
    const name = nameOf(el);
    const interact = interactive(el);
    if (interact) {
      const ref = 'e' + (++n);
      el.setAttribute(REF, ref);
      refs.set(ref, el);
      lines.push('  '.repeat(depth) + '- ' + (role || 'generic') + (name ? ' "' + name.replace(/"/g, "'") + '"' : '') + stateOf(el) + ' [ref=' + ref + ']');
    } else if (role && (name || el.children.length === 0)) {
      lines.push('  '.repeat(depth) + '- ' + role + (name ? ' "' + name.replace(/"/g, "'") + '"' : ''));
    } else if (!role && el.children.length === 0 && name) {
      lines.push('  '.repeat(depth) + '- text "' + name.replace(/"/g, "'") + '"');
    } else {
      walkChildren(el, depth);
      return;
    }
    walkChildren(el, depth + 1);
  };
  walk(document.body || document.documentElement, 0);
  if (truncated) lines.push('- text "… snapshot truncated at ' + MAX_LINES + ' lines / depth ' + MAX_DEPTH + '; more elements may exist"');
  return lines.join('\n') || '(empty page)';
})()`

// actionPointExpr resolves a ref and verifies it can receive a pointer action.
// It returns {status:'ok', x, y} or a reason: missing, hidden, disabled,
// pointer-events-none, covered, outside-viewport.
func actionPointExpr(ref string) string {
	q, _ := json.Marshal(ref)
	return fmt.Sprintf(`(() => {
  const el = (window.__nusaRefs && window.__nusaRefs.get(%s)) || null;
  if (!el || !el.isConnected) return {status: 'missing'};
  el.scrollIntoView({block: 'center', inline: 'center', behavior: 'instant'});
  const cs = getComputedStyle(el);
  if (cs.display === 'none' || cs.visibility === 'hidden' || cs.visibility === 'collapse') return {status: 'hidden'};
  const r = el.getBoundingClientRect();
  if (!r.width && !r.height) return {status: 'hidden'};
  if (el.matches(':disabled') || el.getAttribute('aria-disabled') === 'true') return {status: 'disabled'};
  if (cs.pointerEvents === 'none') return {status: 'pointer-events-none'};
  const x = r.left + r.width / 2, y = r.top + r.height / 2;
  if (x < 0 || y < 0 || x >= innerWidth || y >= innerHeight) return {status: 'outside-viewport', x, y};
  const deepHit = (x, y) => {
    let node = document.elementFromPoint(x, y);
    while (node && node.shadowRoot) {
      const inner = node.shadowRoot.elementFromPoint(x, y);
      if (!inner || inner === node) break;
      node = inner;
    }
    return node;
  };
  const composedContains = (parent, child) => {
    let node = child;
    while (node) {
      if (node === parent) return true;
      node = node.parentNode || node.host || null;
    }
    return false;
  };
  const hit = deepHit(x, y);
  if (!hit) return {status: 'outside-viewport', x, y};
  if (hit !== el && !composedContains(el, hit)) {
    return {status: 'covered', x, y, by: (hit.id ? '#' + hit.id : hit.tagName.toLowerCase())};
  }
  return {status: 'ok', x, y};
})()`, q)
}

// focusExpr focuses the ref'd element; status is missing, disabled, readonly,
// or ok with focused=true when focus landed.
func focusExpr(ref string) string {
	q, _ := json.Marshal(ref)
	return fmt.Sprintf(`(() => {
  const el = (window.__nusaRefs && window.__nusaRefs.get(%s)) || null;
  if (!el || !el.isConnected) return {status: 'missing'};
  el.scrollIntoView({block: 'center', inline: 'center', behavior: 'instant'});
  if (el.matches(':disabled') || el.getAttribute('aria-disabled') === 'true') return {status: 'disabled'};
  if (el.readOnly || el.getAttribute('aria-readonly') === 'true') return {status: 'readonly'};
  try { el.focus({preventScroll: true}); } catch (e) { el.focus(); }
  const deepActive = (root) => {
    let node = root.activeElement;
    while (node && node.shadowRoot && node.shadowRoot.activeElement) node = node.shadowRoot.activeElement;
    return node;
  };
  return {status: 'ok', focused: deepActive(document) === el};
})()`, q)
}

// FieldValue is one browser_fill_form entry.
type FieldValue struct {
	Ref   string `json:"ref"`
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
}

// fillExpr sets every field through the native setter then fires input+change
// so framework-controlled inputs (React, Vue) register the value. It returns
// {ok, reason, detail}; any failed field makes the whole call fail.
func fillExpr(fields []FieldValue) string {
	args, _ := json.Marshal(fields)
	return fmt.Sprintf(`((fields) => {
  const out = [];
  let ok = true, reason = '';
  const fail = (why, msg) => { ok = false; if (!reason) reason = why; out.push(msg); };
  const setValue = (el, v) => {
    const proto = el.tagName === 'TEXTAREA' ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    const d = Object.getOwnPropertyDescriptor(proto, 'value');
    if (d && d.set) d.set.call(el, v); else el.value = v;
  };
  for (const f of fields) {
    const el = (window.__nusaRefs && window.__nusaRefs.get(f.ref)) || null;
    if (!el || !el.isConnected) { fail('ref not found', f.ref + ': ref not found'); continue; }
    if (el.matches(':disabled') || el.getAttribute('aria-disabled') === 'true') { fail('disabled', f.ref + ': disabled'); continue; }
    const ty = (f.type || el.getAttribute('type') || '').toLowerCase();
    const checkable = ty === 'checkbox' || ty === 'radio' || el.type === 'checkbox' || el.type === 'radio';
    if (!checkable && el.tagName !== 'SELECT' && !el.isContentEditable && (el.readOnly || el.getAttribute('aria-readonly') === 'true')) {
      fail('readonly', f.ref + ': read-only'); continue;
    }
    if (checkable) {
      el.checked = ['true', 'on', 'checked', '1'].includes(String(f.value).toLowerCase());
    } else if (el.isContentEditable) {
      el.innerText = f.value;
    } else if (el.tagName === 'SELECT') {
      const o = [...el.options].find(o => o.value === f.value || o.text.trim() === String(f.value).trim());
      if (o) { el.value = o.value; } else { fail('no matching option', f.ref + ': no matching option'); continue; }
    } else if (!('value' in el)) {
      fail('not fillable', f.ref + ': not fillable'); continue;
    } else {
      setValue(el, f.value);
    }
    el.dispatchEvent(new Event('input', {bubbles: true}));
    el.dispatchEvent(new Event('change', {bubbles: true}));
    out.push(f.ref + ': filled');
  }
  return {ok: ok, reason: reason, detail: out.join('; ')};
})(%s)`, args)
}

// selectExpr selects values (or visible labels) on a <select>. It returns
// {status, detail}; a single select rejects more than one value, and a
// zero-match selection is an error instead of a silent change event.
func selectExpr(ref string, values []string) string {
	r, _ := json.Marshal(ref)
	vs, _ := json.Marshal(values)
	return fmt.Sprintf(`((ref, values) => {
  const el = (window.__nusaRefs && window.__nusaRefs.get(ref)) || null;
  if (!el || !el.isConnected) return {status: 'missing', detail: 'ref not found'};
  if (el.tagName !== 'SELECT') return {status: 'not-select', detail: 'element is not a select'};
  if (el.matches(':disabled') || el.getAttribute('aria-disabled') === 'true') return {status: 'disabled', detail: 'select is disabled'};
  if (!el.multiple && values.length > 1) return {status: 'single-multiple', detail: 'a single select accepts one value'};
  el.scrollIntoView({block: 'center', inline: 'center', behavior: 'instant'});
  let matched = 0;
  for (const v of values) {
    const want = String(v).trim();
    const o = [...el.options].find(o => o.value === v || o.text.trim() === want);
    if (o) { o.selected = true; matched++; }
  }
  if (matched === 0) return {status: 'no-match', detail: 'no matching option: ' + values.join(', ')};
  el.dispatchEvent(new Event('input', {bubbles: true}));
  el.dispatchEvent(new Event('change', {bubbles: true}));
  return {status: 'ok', detail: 'selected ' + matched + ' of ' + values.length};
})(%s, %s)`, r, vs)
}
