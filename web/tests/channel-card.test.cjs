const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const src = fs.readFileSync(path.join(__dirname, '..', 'static', 'components', 'settings.js'), 'utf8');

function cards(state) {
  const start = src.indexOf('export function channelCards(');
  const end = src.indexOf('\nexport function routingSettings(', start);
  if (start < 0 || end < 0) throw new Error('channel card functions not found');
  const body = src.slice(start, end).trim().replace(/^export /gm, '');
  const fn = vm.runInNewContext(`(function (S) { ${body}\n return channelCards(); })`, {
    S: state,
    esc: (s) => String(s ?? '').replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;'),
    icon: (n) => `<i>${n}</i>`,
  });
  return fn(state);
}

function state(channels) {
  return { channels, bootstrap: { app: {} } };
}

const telegram = (extra = {}) => ({
  name: 'telegram', label: 'Telegram', connected: false, loggedIn: true,
  state: 'disconnected', enabled: true, ...extra,
});

test('a disconnected Telegram card does not show the online dot', () => {
  const html = cards(state([telegram()]));
  assert.match(html, /status-dot warn/);
});

test('a connected Telegram card shows the online dot', () => {
  const html = cards(state([telegram({ connected: true, state: 'connected' })]));
  assert.match(html, /class="status-dot "/);
  assert.doesNotMatch(html, /status-dot warn/);
});

test('the Channels screen names a disabled Telegram adapter', () => {
  const s = state([]);
  s.bootstrap.app.telegramDisabled = true;
  const html = cards(s);
  assert.match(html, /Telegram is disabled for this process/);
  assert.match(html, /NUSAMATION_NO_TELEGRAM/);
});

test('the Channels screen names a failed Telegram start', () => {
  const html = cards(state([]));
  assert.match(html, /Telegram did not start for this process/);
});

test('About reports the Telegram availability', () => {
  assert.match(src, /<div class="k">Telegram<\/div>/);
  assert.match(src, /app\.telegramDisabled \? "disabled" : "available"/);
});
