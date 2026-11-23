const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// A failed prepare is not always an auth failure. Only auth failures may fall
// back to the agent's cached login methods; a rejected saved option must offer
// the options picker instead of a login flow.
const src = fs.readFileSync(path.join(__dirname, '..', 'static', 'components', 'requests.js'), 'utf8');
const body = src.match(/export function blockerAuthMethods\([\s\S]*?\n}\n/)[0].replace('export ', '');
const { blockerAuthMethods } = vm.runInNewContext(`${body}; ({ blockerAuthMethods })`);
// The helper runs in its own realm, so compare plain JSON rather than
// prototype identities.
const ids = (methods) => JSON.parse(JSON.stringify(methods)).map((m) => m.id);

const login = [{ id: 'browser', name: 'Log in with browser' }];

test('config failures never show cached login methods', () => {
  const agent = { authMethods: login };
  assert.deepEqual(ids(blockerAuthMethods({ code: 'agent_config_unavailable', message: 'nope' }, agent)), []);
  assert.deepEqual(ids(blockerAuthMethods({ code: 'prepare_failed', message: 'nope' }, agent)), []);
  assert.deepEqual(ids(blockerAuthMethods({ code: 'network', message: 'offline' }, agent)), []);
});

test('auth failures keep the login flow', () => {
  const agent = { authMethods: login };
  assert.deepEqual(ids(blockerAuthMethods({ code: 'agent_auth_required', message: 'login' }, agent)), ['browser']);
  assert.deepEqual(ids(blockerAuthMethods({ code: 'agent_auth_required', data: { authMethods: [{ id: 'terminal' }] } }, agent)), ['terminal']);
  // A failed attempt reopens the blocker so another method can be chosen.
  assert.deepEqual(ids(blockerAuthMethods({ code: 'auth_failed', message: 'nope' }, agent)), ['browser']);
});

test('a config failure renders an options hint and opens the picker', () => {
  assert.match(src, /error\.code === "agent_config_unavailable"/);
  assert.match(src, /data-action="open-session-options">Change options/);
  const events = fs.readFileSync(path.join(__dirname, '..', 'static', 'components', 'events.js'), 'utf8');
  assert.match(events, /case "open-session-options":/);
  assert.match(events, /const anchor = isMobile\(\)[\s\S]*?\.composer-mobile-settings[\s\S]*?openConfigMenu\(anchor\)/);
});
