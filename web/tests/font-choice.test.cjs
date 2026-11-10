const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const src = fs.readFileSync(path.join(__dirname, '..', 'static', 'components', 'state.js'), 'utf8');
const start = src.indexOf('export const UI_FONTS');
const end = src.indexOf('\nexport function composerFor', start);
assert.ok(start > 0 && end > start, 'font block found in state.js');

function makeFontApi(store = {}, dataset = {}, style = new Map()) {
  const code = src.slice(start, end).trim()
    .replace(/export /g, '')
    + '\n;return { UI_FONTS, UI_FONT_SIZES, uiFont, applyFont, setUiFont, uiFontSize, applyFontSize, setUiFontSize };';
  return vm.runInNewContext(`(function(){${code}})()`, {
    localStorage: {
      getItem: (k) => (k in store ? store[k] : null),
      setItem: (k, v) => { store[k] = String(v); },
      removeItem: (k) => { delete store[k]; },
    },
    document: { documentElement: { dataset, style: { setProperty: (k, v) => style.set(k, v), removeProperty: (k) => style.delete(k) } } },
  });
}

test('uiFont falls back to default on empty or invalid stored value', () => {
  assert.equal(makeFontApi({}).uiFont(), '');
  assert.equal(makeFontApi({ 'nusamation:font': 'papyrus' }).uiFont(), '');
  assert.equal(makeFontApi({ 'nusamation:font': 'serif' }).uiFont(), 'serif');
});

test('applyFont sets or clears the document data attribute', () => {
  const dataset = {};
  const api = makeFontApi({ 'nusamation:font': 'toddler' }, dataset);
  api.applyFont();
  assert.equal(dataset.font, 'toddler');
  api.setUiFont('');
  assert.equal(dataset.font, undefined);
});

test('setUiFont persists valid choices and normalizes unknown ones', () => {
  const store = {}, dataset = {};
  const api = makeFontApi(store, dataset);
  api.setUiFont('inter');
  assert.equal(store['nusamation:font'], 'inter');
  assert.equal(dataset.font, 'inter');
  api.setUiFont('bogus');
  assert.equal(store['nusamation:font'], 'bogus');
  assert.equal(dataset.font, undefined); // uiFont() fails closed to default
});

test('uiFontSize accepts only sizes in the supported range', () => {
  assert.equal(makeFontApi({}).uiFontSize(), '');
  assert.equal(makeFontApi({ 'nusamation:font-size': '8' }).uiFontSize(), '');
  assert.equal(makeFontApi({ 'nusamation:font-size': '21' }).uiFontSize(), '');
  assert.equal(makeFontApi({ 'nusamation:font-size': '14.5' }).uiFontSize(), '');
  assert.equal(makeFontApi({ 'nusamation:font-size': '12' }).uiFontSize(), '12');
});

test('applyFontSize maps the chosen px onto a zoom factor against the 14.5px base', () => {
  const style = new Map();
  makeFontApi({ 'nusamation:font-size': '20' }, {}, style).applyFontSize();
  assert.equal(style.get('--ui-zoom'), String(20 / 14.5));
  makeFontApi({ 'nusamation:font-size': '9' }, {}, style).applyFontSize();
  assert.equal(style.get('--ui-zoom'), String(9 / 14.5));
});

test('setUiFontSize persists, applies, and resets to no zoom', () => {
  const store = {}, style = new Map();
  const api = makeFontApi(store, {}, style);
  api.setUiFontSize('16');
  assert.equal(store['nusamation:font-size'], '16');
  assert.equal(style.get('--ui-zoom'), String(16 / 14.5));
  api.setUiFontSize('');
  assert.equal(store['nusamation:font-size'], undefined);
  assert.equal(style.has('--ui-zoom'), false);
  api.setUiFontSize('99'); // stored verbatim, normalized out by uiFontSize()
  assert.equal(style.has('--ui-zoom'), false);
});
