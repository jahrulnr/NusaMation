const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const src = fs.readFileSync(path.join(__dirname, '..', 'static', 'components', 'telegram.js'), 'utf8');
const eventsSrc = fs.readFileSync(path.join(__dirname, '..', 'static', 'components', 'events.js'), 'utf8');
const start = src.indexOf('export function telegramPanel(');
const end = src.indexOf('\nexport function updateTelegramPanel(', start);
if (start < 0 || end < 0) throw new Error('telegramPanel function not found');

function panel(state) {
  const fn = vm.runInNewContext(`(${`function (S) { ${src.slice(start, end).trim().replace(/^export /, '')} return telegramPanel(); }`})`, {
    esc: (s) => String(s ?? '').replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;'),
    icon: (n) => `<i>${n}</i>`,
    S: state,
  });
  return fn(state);
}

function state() {
  return {
    telegram: { name: 'telegram', connected: true, state: 'connected', selfID: '4242' },
    telegramSettings: { enabled: true, autoStart: true, groupPolicy: 'mention', allowedUserIds: [], allowedGroupIds: [], tokenSet: true, botUsername: 'nusa_bot' },
    channels: [],
    bootstrap: { app: {} },
  };
}

test('the panel explains the slash menu and inline mentions', () => {
  const html = panel(state());
  assert.match(html, /session commands/);
  assert.match(html, /@botname/);
  assert.match(html, /\/setinline/);
  assert.match(html, /@BotFather/);
});

test('an empty user allowlist still says it allows no one', () => {
  const html = panel(state());
  assert.match(html, /allows <em>no one<\/em>/);
  assert.match(html, /tg-allow-everyone/);
});

test('the group allowlist is disabled while the user wildcard is active', () => {
  const s = state();
  s.telegramSettings.allowedUserIds = ['*'];
  const html = panel(s);
  assert.match(html, /data-action="tg-save-groups" disabled/);
  assert.match(html, /wildcard <code class="mono">\*<\/code> is active/);
});

test('the group allowlist saves normally without the wildcard', () => {
  const html = panel(state());
  assert.doesNotMatch(html, /data-action="tg-save-groups" disabled/);
});

test('blocked group actions are disabled under the wildcard', () => {
  const s = state();
  s.telegramSettings.allowedUserIds = ['*'];
  s.telegram.stats = { blocked: [{ id: '111', chat: '-900', isGroup: true, name: 'Ana', chatName: 'Team' }] };
  const html = panel(s);
  assert.match(html, /data-action="tg-allow-group"[^>]*disabled/);
  assert.match(html, /Remove the \* user wildcard first/);
});

function everyoneFn(env) {
  const start = src.indexOf('export async function allowTgEveryone(');
  if (start < 0) throw new Error('allowTgEveryone not found');
  return vm.runInNewContext(`(${src.slice(start).trim().replace(/^export /, '')})`, env);
}

test('allow everyone clears a group allowlist in the same save', async () => {
  const calls = [];
  const confirmations = [];
  let pending = null;
  const state = { telegramSettings: { allowedUserIds: [], allowedGroupIds: ['-900'] } };
  const fn = everyoneFn({
    S: state,
    saveTgSettings: async (patch) => { calls.push(patch); return true; },
    confirmModal: (title, message, onConfirm) => { confirmations.push(message); pending = onConfirm; },
    toast: () => {},
  });
  await fn();
  assert.equal(confirmations.length, 1);
  assert.match(confirmations[0], /group allowlist will be cleared/);
  assert.equal(typeof pending, 'function');
  await pending();
  assert.equal(JSON.stringify(calls[0]), JSON.stringify({ allowedUserIds: ['*'], allowedGroupIds: [] }));
});

test('allow everyone saves directly when there is no group allowlist', async () => {
  const calls = [];
  const state = { telegramSettings: { allowedUserIds: [], allowedGroupIds: [] } };
  const fn = everyoneFn({
    S: state,
    saveTgSettings: async (patch) => { calls.push(patch); return true; },
    confirmModal: () => { throw new Error('confirmation was not expected'); },
    toast: () => {},
  });
  await fn();
  assert.equal(JSON.stringify(calls), JSON.stringify([{ allowedUserIds: ['*'], allowedGroupIds: [] }]));
});

test('a failed save keeps the Telegram panel mounted', () => {
  const start = src.indexOf('export async function saveTgSettings(');
  const end = src.indexOf('\nexport async function refreshTelegram(', start);
  const fn = src.slice(start, end);
  assert.match(fn, /Could not save Telegram settings/);
  assert.doesNotMatch(fn, /renderSettings\(\)/);
});

test('the user allowlist toast only claims duplicates were removed', () => {
  const start = eventsSrc.indexOf('case "tg-save-users"');
  const end = eventsSrc.indexOf('case "tg-save-groups"', start);
  const block = eventsSrc.slice(start, end);
  assert.match(block, /duplicate/);
  assert.doesNotMatch(block, /invalid or duplicate/);
});
