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

// selectShimJS replaces the native <select> popup with an in-page listbox.
// Headless Chromium renders that popup in browser chrome, outside the web
// contents the screencast streams, so selects in the pane open nothing at all.
// The shim intercepts pointer/keyboard intent on single-value dropdowns and
// commits through value + input/change, so page and framework handlers behave
// exactly as with the native widget. List-style selects (multiple or size>1)
// already render inline and are left alone. Selects with more than 5 options
// get a search field that filters rows live; shorter lists stay search-free.
const selectShimJS = `(function(){
  if (window.__nusaSelectShim) return;
  window.__nusaSelectShim = true;
  let open = null;

  const close = () => {
    if (!open) return;
    open.box.remove();
    open.sel.removeAttribute('aria-expanded');
    open = null;
  };
  // An option inside a disabled optgroup is unselectable even though its own
  // disabled attribute is unset, matching native popup behavior.
  const disabled = (o) => o.disabled || (o.parentElement && o.parentElement.tagName === 'OPTGROUP' && o.parentElement.disabled);
  const commit = (o) => {
    if (!open || !o || disabled(o)) return;
    const sel = open.sel;
    if (sel.options[sel.selectedIndex] === o) { close(); sel.focus({preventScroll: true}); return; }
    o.selected = true;
    sel.dispatchEvent(new Event('input', {bubbles: true, composed: true}));
    sel.dispatchEvent(new Event('change', {bubbles: true}));
    close();
    sel.focus({preventScroll: true});
  };
  const avail = it => !disabled(it.opt) && !it.hidden;
  const paint = () => {
    for (const it of open.items) { it.row.style.background = ''; it.row.style.color = ''; }
    const it = open.items[open.index];
    if (it && avail(it)) { it.row.style.background = 'Highlight'; it.row.style.color = 'HighlightText'; }
  };
  const move = (d) => {
    if (!open) return;
    const items = open.items;
    if (!items.length || !items.some(avail)) return;
    let i = open.index;
    if (d === 0) {
      if (!avail(items[i])) i = items.findIndex(avail);
    } else {
      do { i = (i + d + items.length) % items.length; } while (!avail(items[i]));
    }
    if (i < 0) return;
    open.index = i;
    paint();
    items[i].row.scrollIntoView({block: 'nearest'});
  };
  const show = (sel) => {
    close();
    const r = sel.getBoundingClientRect();
    const box = document.createElement('div');
    box.setAttribute('data-nusa-listbox', '');
    box.style.cssText = 'position:fixed;z-index:2147483647;background:Canvas;color:CanvasText;border:1px solid #8889;border-radius:6px;padding:3px 0;box-shadow:0 8px 28px rgba(0,0,0,.4);font:13px system-ui,sans-serif;max-height:45vh;max-width:' + (innerWidth - 8) + 'px;overflow-y:auto;overflow-x:hidden;min-width:' + Math.max(r.width, 90) + 'px;';
    const items = [];
    const groups = [];
    const addRow = (o, indent) => {
      const row = document.createElement('div');
      row.textContent = o.label || o.text;
      row.style.cssText = 'padding:4px ' + (indent ? '10px 4px 20px' : '10px') + ';white-space:nowrap;overflow:hidden;text-overflow:ellipsis;'
        + (disabled(o) ? 'opacity:.45' : 'cursor:default');
      row.addEventListener('mousedown', (e) => e.preventDefault());
      row.addEventListener('click', () => commit(o));
      row.addEventListener('mousemove', () => { if (row._i !== open.index) { open.index = row._i; paint(); } });
      box.appendChild(row);
      items.push({row: row, opt: o, hidden: false});
      return row;
    };
    for (const node of sel.children) {
      if (node.tagName === 'OPTGROUP') {
        const h = document.createElement('div');
        h.textContent = node.label || '';
        h.style.cssText = 'padding:4px 10px;font-size:11px;opacity:.6;font-weight:600';
        box.appendChild(h);
        const rows = [];
        for (const o of node.children) if (o.tagName === 'OPTION') rows.push(addRow(o, true));
        groups.push({head: h, rows: rows});
      } else if (node.tagName === 'OPTION') addRow(node, false);
    }
    if (!items.length) { box.remove(); return; }
    items.forEach((it, i) => { it.row._i = i; });
    let search = null;
    if (items.length > 5) {
      search = document.createElement('input');
      search.type = 'text';
      search.placeholder = 'Search…';
      search.setAttribute('data-nusa-listbox-search', '');
      search.style.cssText = 'display:block;box-sizing:border-box;width:100%;margin:0 0 3px;padding:5px 10px;border:0;border-bottom:1px solid #8884;background:Canvas;color:CanvasText;font:12px system-ui,sans-serif;outline:none;';
      search.addEventListener('input', () => {
        const q = search.value.trim().toLowerCase();
        for (const it of items) {
          it.hidden = !!q && !(it.opt.label || it.opt.text || '').toLowerCase().includes(q) && !(it.opt.value || '').toLowerCase().includes(q);
          it.row.hidden = it.hidden;
        }
        for (const g of groups) g.head.hidden = g.rows.every(rw => rw.hidden);
        move(0);
      });
      box.insertBefore(search, box.firstChild);
    }
    document.documentElement.appendChild(box);
    const bh = Math.min(box.offsetHeight, innerHeight - 8), bw = Math.min(box.offsetWidth, innerWidth - 8);
    const roomBelow = innerHeight - r.bottom;
    const up = roomBelow < bh && r.top > roomBelow;
    box.style.left = Math.max(4, Math.min(r.left, innerWidth - bw - 4)) + 'px';
    box.style.top = up ? Math.max(4, r.top - bh - 2) + 'px' : Math.min(r.bottom + 2, innerHeight - bh - 4) + 'px';
    let index = items.findIndex(it => it.opt === sel.options[sel.selectedIndex]);
    if (index < 0) index = items.findIndex(avail);
    open = {sel: sel, box: box, items: items, index: Math.max(0, index), search: search};
    sel.setAttribute('aria-expanded', 'true');
    move(0);
    if (search) search.focus({preventScroll: true});
  };

  document.addEventListener('mousedown', (e) => {
    if (e.button !== 0) return;
    const path = e.composedPath ? e.composedPath() : [];
    const sel = path.find(n => n && n.tagName === 'SELECT');
    if (open) {
      if (path.includes(open.box)) return;
      if (sel === open.sel) { e.preventDefault(); close(); return; }
      close();
    }
    if (!sel || sel.disabled || sel.multiple || sel.size > 1) return;
    e.preventDefault();
    sel.focus({preventScroll: true});
    show(sel);
  }, true);

  document.addEventListener('keydown', (e) => {
    if (open) {
      // Native popups swallow keys entirely; keep them from reaching app-level
      // handlers (Escape closing the underlying dialog, Enter submitting).
      e.stopPropagation();
      const searching = open.search && e.target === open.search;
      if (e.key === 'Escape') { e.preventDefault(); const s = open.sel; close(); s.focus({preventScroll: true}); }
      else if (e.key === 'ArrowDown') { e.preventDefault(); move(1); }
      else if (e.key === 'ArrowUp') { e.preventDefault(); move(-1); }
      else if (!searching && e.key === ' ') { e.preventDefault(); move(1); }
      else if (!searching && e.key === 'Home') { e.preventDefault(); open.index = open.items.findIndex(avail); move(0); }
      else if (!searching && e.key === 'End') { e.preventDefault(); for (let i = open.items.length - 1; i >= 0; i--) if (avail(open.items[i])) { open.index = i; break; } move(0); }
      else if (e.key === 'Enter' || e.key === 'Tab') {
        e.preventDefault();
        const it = open.items[open.index];
        if (it && avail(it)) commit(it.opt);
        else { const f = open.items.findIndex(avail); if (f >= 0) commit(open.items[f].opt); else close(); }
      }
      return;
    }
    const t = e.target;
    if (t && t.tagName === 'SELECT' && !t.disabled && !t.multiple && t.size <= 1
        && [' ', 'Enter', 'ArrowDown', 'ArrowUp'].includes(e.key)) {
      e.preventDefault();
      show(t);
    }
  }, true);
  // Scroll doesn't bubble but is visible at window during capture; a scroll
  // inside the listbox (including our own scrollIntoView) must not close it.
  window.addEventListener('scroll', (e) => { if (open && !open.box.contains(e.target)) close(); }, true);
  window.addEventListener('resize', close);
})()`

// loginTapJS reports submitted logins to the backend so the pane's password
// manager can offer them back on later visits. Submissions are pushed to the
// __nusaReportLogin CDP binding (survives navigation) and mirrored to a window
// buffer for contexts where the binding is not installed yet (the first load
// can race addBinding); the session drains both paths. Only the password's own
// origin is recorded — data never leaves the app.
const loginTapJS = `(function(){
  if (window.__nusaLoginTap) return;
  window.__nusaLoginTap = true;
  window.__nusaLogins = window.__nusaLogins || [];
  const report = (target) => {
    try {
      const scope = target && (target.tagName === 'FORM' ? target : target.form) || document;
      const pw = [...scope.querySelectorAll('input[type=password]')].find(i => i.value && !i.disabled);
      if (!pw) return;
      const form = pw.form || scope;
      const user = form.querySelector('input[type=email],input[name*=user i],input[name*=email i],input[name*=login i],input[autocomplete*=username i]')
        || [...form.querySelectorAll('input[type=text],input:not([type])')].find(i => i.value && !i.disabled);
      const payload = JSON.stringify({origin: location.origin, username: user ? user.value : '', password: pw.value});
      if (typeof window.__nusaReportLogin === 'function') window.__nusaReportLogin(payload);
      else { window.__nusaLogins.push(payload); if (window.__nusaLogins.length > 20) window.__nusaLogins.shift(); }
    } catch (e) {}
  };
  document.addEventListener('submit', e => report(e.target), true);
  // SPAs often fake-submit with Enter or a plain button click.
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && e.target && e.target.tagName === 'INPUT' && e.target.type === 'password') report(e.target);
  }, true);
  document.addEventListener('click', (e) => {
    const t = e.target && e.target.closest ? e.target.closest('button,[type=submit],input[type=submit]') : null;
    if (t && t.form && t.form.querySelector('input[type=password]')) report(t);
  }, true);
  const offer = e => {
    const input = e.target;
    if (!input || input.tagName !== 'INPUT' || input.disabled || input.readOnly) return;
    const form = input.form || document;
    const password = form.querySelector('input[type=password]:not([disabled]):not([readonly])');
    if (!password || !['password', 'text', 'email'].includes(input.type || 'text')) return;
    window.__nusaLoginTarget = input;
    if (typeof window.__nusaReportLogin === 'function') window.__nusaReportLogin(JSON.stringify({kind:'focus', origin:location.origin}));
  };
  let lastFocusTarget = null, lastFocusAt = 0;
  document.addEventListener('focusin', e => { lastFocusTarget = e.target; lastFocusAt = Date.now(); offer(e); }, true);
  document.addEventListener('click', e => { if (e.target !== lastFocusTarget || Date.now() - lastFocusAt > 300) offer(e); }, true);
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
