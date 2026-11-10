const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { pathToFileURL } = require('node:url');

// Every native <select> in the UI is auto-enhanced by a MutationObserver in
// selects.js (SlimSelect 2.x, vendored). More than five options turns on the
// dropdown search; five or fewer stays a plain list. data-select-raw opts out.

const root = (...segments) => path.join(__dirname, '..', 'static', ...segments);
const selectsSrc = fs.readFileSync(root('components', 'selects.js'), 'utf8');

test('the vendored SlimSelect ESM loads and exports the widget', async () => {
  const mod = await import(pathToFileURL(root('vendor', 'slimselect.es.js')).href);
  assert.equal(typeof mod.default, 'function');
});

const vm = require('node:vm');
const helpers = vm.runInNewContext(`(() => {
  ${selectsSrc.match(/const SEARCH_MIN = \d+;/)[0]}
  const registry = new Map();
  ${selectsSrc.match(/export const selectWantsSearch = [^\n]+/)[0].replace('export ', '')}
  ${selectsSrc.match(/export const shouldEnhance = [^\n]+/)[0].replace('export ', '')}
  return { selectWantsSearch, shouldEnhance, registry };
})()`);

test('selects.js wires SlimSelect with the >5-option search threshold', () => {
  assert.match(selectsSrc, /import SlimSelect from "\.\.\/vendor\/slimselect\.es\.js"/);
  const sel = (n) => ({ querySelectorAll: () => new Array(n).fill(0) });
  assert.equal(helpers.selectWantsSearch(sel(6)), true, 'six options enables search');
  assert.equal(helpers.selectWantsSearch(sel(5)), false, 'five options stays plain');
  assert.equal(helpers.selectWantsSearch(sel(1)), false);
});

test('shouldEnhance skips opted-out and already-enhanced selects', () => {
  const { shouldEnhance, registry } = helpers;
  const plain = { dataset: {} };
  const optedOut = { dataset: { selectRaw: '' } };
  const enhanced = { dataset: {} };
  registry.set(enhanced, {});
  assert.equal(shouldEnhance(plain), true);
  assert.equal(shouldEnhance(optedOut), false, 'data-select-raw stays native');
  assert.equal(shouldEnhance(enhanced), false, 'no double enhance');
  assert.equal(shouldEnhance(null), false);
});

test('enhancement is wired into boot and styles into the cascade', () => {
  const app = fs.readFileSync(root('app.js'), 'utf8');
  assert.match(app, /initSelects\(\)/);
  const html = fs.readFileSync(root('index.html'), 'utf8');
  assert.match(html, /slimselect\.css/);
  const css = fs.readFileSync(root('app.css'), 'utf8');
  assert.match(css, /@import "\.\/components\/selects\.css"/);
});

test('no ?v= cache-bust specifiers were introduced', () => {
  for (const f of ['app.js', 'index.html', 'app.css']) {
    assert.doesNotMatch(fs.readFileSync(root(f), 'utf8'), /slimselect[^"']*\?v=/);
  }
});
