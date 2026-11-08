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
  const fmt = a => { try { return typeof a === 'string' ? a : JSON.stringify(a); } catch (e) { return String(a); } };
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
  const nameOf = el => {
    let s = el.getAttribute('aria-label') || el.getAttribute('alt') || el.getAttribute('title') || el.getAttribute('placeholder') || '';
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
    if (el.disabled) bits.push('disabled');
    if (el.checked) bits.push('checked');
    if (el.getAttribute('aria-expanded') === 'true') bits.push('expanded');
    if (el.getAttribute('aria-expanded') === 'false') bits.push('collapsed');
    if (el.required) bits.push('required');
    return bits.length ? ' [' + bits.join(', ') + ']' : '';
  };
  const walk = (el, depth) => {
    if (!el || depth > 24 || lines.length > 600) return;
    if (skip.has(el.tagName) || !visible(el)) return;
    const role = roleFor(el);
    const name = nameOf(el);
    const interact = interactive(el);
    if (interact) {
      const ref = 'e' + (++n);
      el.setAttribute(REF, ref);
      lines.push('  '.repeat(depth) + '- ' + (role || 'generic') + (name ? ' "' + name.replace(/"/g, "'") + '"' : '') + stateOf(el) + ' [ref=' + ref + ']');
    } else if (role && (name || el.children.length === 0)) {
      lines.push('  '.repeat(depth) + '- ' + role + (name ? ' "' + name.replace(/"/g, "'") + '"' : ''));
    } else if (!role && el.children.length === 0 && name) {
      lines.push('  '.repeat(depth) + '- text "' + name.replace(/"/g, "'") + '"');
    } else {
      for (const c of el.children) walk(c, depth);
      return;
    }
    for (const c of el.children) walk(c, depth + 1);
  };
  walk(document.body || document.documentElement, 0);
  return lines.join('\n') || '(empty page)';
})()`

// refPointExpr returns {x,y} viewport center of the ref'd element after
// scrolling it into view, or null when the ref is stale.
func refPointExpr(ref string) string {
	q, _ := json.Marshal("[" + refAttr + `="` + ref + `"]`)
	return fmt.Sprintf(`(() => {
  const el = document.querySelector(%s);
  if (!el) return null;
  el.scrollIntoView({block: 'center', inline: 'center', behavior: 'instant'});
  const r = el.getBoundingClientRect();
  if (!r.width && !r.height) return null;
  return {x: r.left + r.width / 2, y: r.top + r.height / 2};
})()`, q)
}

// focusExpr focuses the ref'd element; returns true when focus landed.
func focusExpr(ref string) string {
	q, _ := json.Marshal("[" + refAttr + `="` + ref + `"]`)
	return fmt.Sprintf(`(() => {
  const el = document.querySelector(%s);
  if (!el) return null;
  el.scrollIntoView({block: 'center', inline: 'center', behavior: 'instant'});
  try { el.focus({preventScroll: true}); } catch (e) { el.focus(); }
  return document.activeElement === el;
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
// so framework-controlled inputs (React, Vue) register the value.
func fillExpr(fields []FieldValue) string {
	args, _ := json.Marshal(fields)
	return fmt.Sprintf(`((fields) => {
  const out = [];
  const setValue = (el, v) => {
    const proto = el.tagName === 'TEXTAREA' ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    const d = Object.getOwnPropertyDescriptor(proto, 'value');
    if (d && d.set) d.set.call(el, v); else el.value = v;
  };
  for (const f of fields) {
    const el = document.querySelector('[`+refAttr+`="' + f.ref + '"]');
    if (!el) { out.push(f.ref + ': ref not found'); continue; }
    const ty = (f.type || el.getAttribute('type') || '').toLowerCase();
    if (ty === 'checkbox' || ty === 'radio' || el.type === 'checkbox' || el.type === 'radio') {
      el.checked = ['true', 'on', 'checked', '1'].includes(String(f.value).toLowerCase());
    } else if (el.isContentEditable) {
      el.innerText = f.value;
    } else if (el.tagName === 'SELECT') {
      const o = [...el.options].find(o => o.value === f.value || o.text === f.value);
      if (o) { el.value = o.value; } else { out.push(f.ref + ': no matching option'); continue; }
    } else {
      setValue(el, f.value);
    }
    el.dispatchEvent(new Event('input', {bubbles: true}));
    el.dispatchEvent(new Event('change', {bubbles: true}));
    out.push(f.ref + ': filled');
  }
  return out.join('; ');
})(%s)`, args)
}

// selectExpr selects values (or visible labels) on a <select>.
func selectExpr(ref string, values []string) string {
	q, _ := json.Marshal("[" + refAttr + `="` + ref + `"]`)
	vs, _ := json.Marshal(values)
	return fmt.Sprintf(`((selector, values) => {
  const el = document.querySelector(selector);
  if (!el) return null;
  if (el.tagName !== 'SELECT') return 'element is not a select';
  el.scrollIntoView({block: 'center', inline: 'center', behavior: 'instant'});
  let matched = 0;
  for (const v of values) {
    const o = [...el.options].find(o => o.value === v || o.text === v);
    if (o) { o.selected = true; matched++; }
  }
  el.dispatchEvent(new Event('input', {bubbles: true}));
  el.dispatchEvent(new Event('change', {bubbles: true}));
  return 'selected ' + matched + ' of ' + values.length;
})(%s, %s)`, q, vs)
}
