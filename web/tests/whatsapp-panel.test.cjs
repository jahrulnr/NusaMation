const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const src = fs.readFileSync(path.join(__dirname, '..', 'static', 'components', 'whatsapp.js'), 'utf8');
const start = src.indexOf('export function whatsappPanel(');
const end = src.indexOf('\nexport function startQRTimer(', start);
if (start < 0 || end < 0) throw new Error('whatsappPanel function not found');

function panel(state) {
  // blockedSection reads the module-level S, so the sandbox must carry it too.
  const fn = vm.runInNewContext(`(${`function (S) { ${src.slice(start, end).trim().replace(/^export /, '')} return whatsappPanel(); }`})`, {
    esc: (s) => String(s ?? '').replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;'),
    icon: (n) => `<i>${n}</i>`,
    S: state,
  });
  return fn(state);
}

function state({ whatsappDisabled }) {
  return { whatsapp: null, channels: [], bootstrap: { app: { whatsappDisabled } } };
}

test('panel blames the env flag only when the process disabled WhatsApp', () => {
  const html = panel(state({ whatsappDisabled: true }));
  assert.match(html, /NUSAMATION_NO_WHATSAPP/);
});

test('a channel that failed to start no longer blames the env flag', () => {
  const html = panel(state({ whatsappDisabled: false }));
  assert.match(html, /failed to start/);
  assert.doesNotMatch(html, /NUSAMATION_NO_WHATSAPP/);
  assert.match(html, /server log/);
});

test('blocked senders render with a one-tap allow action', () => {
  const s = state({ whatsappDisabled: false });
  s.whatsappSettings = { allowedNumbers: ["6281234567890"] };
  s.channels = [{ name: "whatsapp", stats: { blocked: [
    { sender: "628999@s.whatsapp.net", chat: "628999@s.whatsapp.net", pushName: "Budi", timestampMs: 1 },
    { sender: "6281234567890@s.whatsapp.net", chat: "6281234567890@s.whatsapp.net", pushName: "", timestampMs: 2 },
  ] } }];
  s.whatsapp = s.channels[0];
  const html = panel(s);
  assert.match(html, /Blocked senders/);
  assert.match(html, /Budi/);
  assert.match(html, /data-action="wa-allow-sender" data-sender="628999@s.whatsapp.net"/);
  // A sender already on the allowlist renders as allowed and disabled.
  assert.match(html, /Allowed<\/button>/);
  assert.doesNotMatch(html, /data-sender="6281234567890@s.whatsapp.net" >/);
  assert.match(html, /data-sender="6281234567890@s.whatsapp.net" disabled/);
});

test('an empty blocked list renders no section', () => {
  const s = state({ whatsappDisabled: false });
  s.channels = [{ name: "whatsapp", stats: {} }];
  s.whatsapp = s.channels[0];
  const html = panel(s);
  assert.doesNotMatch(html, /Blocked senders/);
  assert.match(html, /wa-load-groups/);
  // The allowlist contract is spelled out in the panel copy.
  assert.match(html, /allows <em>no one<\/em>/);
  assert.match(html, /wa-allow-everyone/);
});

test('send pacing and queue limit render as bounded number inputs', () => {
  const s = state({ whatsappDisabled: false });
  s.whatsappSettings = { sendPacing: 12, queueCap: 55 };
  s.channels = [{ name: "whatsapp", stats: {} }];
  s.whatsapp = s.channels[0];
  const html = panel(s);
  assert.match(html, /data-change="wa-number" data-key="sendPacing"[\s\S]{0,120}value="12"/);
  assert.match(html, /data-change="wa-number" data-key="queueCap"[\s\S]{0,120}value="55"/);
  assert.match(html, /min="0" max="60"/);
  assert.match(html, /min="1" max="200"/);
  assert.match(html, /wait in a queue/);
});
