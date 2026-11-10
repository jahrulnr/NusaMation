const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// Browser secret references: the composer offers opaque [[env:NAME]] tags for
// saved agent env vars only (hasValue + reference grammar), inserts at the
// live textarea caret, and rejects clicks once the bound room/agent changed.

const src = fs.readFileSync(path.join(__dirname, '..', 'static', 'components', 'browser-secrets.js'), 'utf8');

const slice = (startMarker, endMarker) => {
  const start = src.indexOf(startMarker);
  assert.ok(start >= 0, `missing ${startMarker}`);
  const end = endMarker ? src.indexOf(endMarker, start + startMarker.length) : -1;
  return src.slice(start, end < 0 ? undefined : end);
};

const helpers = vm.runInNewContext(
  slice('export const ENV_REF_NAME_RE', '\n/* ---------------- agent env rows').replace(/^export /gm, '') +
  '; ({ isValidEnvRefName, envRefTag, eligibleEnvRefs, envRefCopyEnabled, insertAtCaret, secretScopeActive })'
);

const { isValidEnvRefName, envRefTag, eligibleEnvRefs, envRefCopyEnabled, insertAtCaret, secretScopeActive } = helpers;

test('env reference grammar accepts identifier-shaped names only', () => {
  for (const ok of ['TOKEN', '_x', 'A1', 'a', '_']) assert.ok(isValidEnvRefName(ok), ok);
  for (const bad of ['9X', 'MY-KEY', '', 'A B', 'A.B', 'x]']) assert.ok(!isValidEnvRefName(bad), bad);
});

test('envRefTag renders the literal tag', () => {
  assert.equal(envRefTag('API_KEY'), '[[env:API_KEY]]');
});

test('eligibleEnvRefs keeps saved, valid, deduped names only', () => {
  const env = [
    { name: 'TOKEN', hasValue: true },
    { name: 'UNSET', hasValue: false },
    { name: 'MY-KEY', hasValue: true },
    { name: 'TOKEN', hasValue: true },
    { name: ' OTHER ', hasValue: true },
    { value: 'x' },
  ];
  // vm arrays carry a foreign prototype — spread before deepStrictEqual.
  assert.deepEqual([...eligibleEnvRefs(env)], ['TOKEN', 'OTHER']);
  assert.deepEqual([...eligibleEnvRefs(null)], []);
});

test('copy is enabled only for a saved name that still matches', () => {
  const saved = [{ name: 'TOKEN', hasValue: true }, { name: 'OLD', hasValue: false }];
  assert.equal(envRefCopyEnabled('TOKEN', saved), true);
  assert.equal(envRefCopyEnabled('RENAMED', saved), false);
  assert.equal(envRefCopyEnabled('MY-KEY', saved), false);
  assert.equal(envRefCopyEnabled('OLD', saved), false);
});

test('insertAtCaret splices without touching the rest of the draft', () => {
  const r1 = insertAtCaret('foo bar', 4, 7, '[[env:T]]');
  assert.equal(r1.value, 'foo [[env:T]]');
  assert.equal(r1.caret, 13);
  const r2 = insertAtCaret('foo', 3, 3, 'X');
  assert.equal(r2.value, 'fooX');
  assert.equal(r2.caret, 4);
  const r3 = insertAtCaret('', undefined, undefined, 'T');
  assert.equal(r3.value, 'T');
  assert.equal(r3.caret, 1);
  // Out-of-range selections clamp instead of throwing or corrupting text.
  const r4 = insertAtCaret('ab', 99, 99, 'T');
  assert.equal(r4.value, 'abT');
  assert.equal(r4.caret, 3);
});

test('secretScopeActive requires the same room and agent', () => {
  const scope = { key: 'cv_1', agentId: 'a1' };
  assert.equal(secretScopeActive(scope, 'cv_1', 'a1'), true);
  assert.equal(secretScopeActive(scope, 'cv_2', 'a1'), false);
  assert.equal(secretScopeActive(scope, 'cv_1', 'a2'), false);
  assert.equal(secretScopeActive(null, 'cv_1', 'a1'), false);
});

/* ---------------- scoped insertion ---------------- */

const insertSrc = slice('export function insertSecretRef', undefined).replace(/^export /gm, '');

function insertCtx(over = {}) {
  const owners = new Map([['cv_1', { draft: '' }]]);
  const saved = [];
  const ctx = {
    isValidEnvRefName, secretScopeActive, eligibleEnvRefs, insertAtCaret, envRefTag,
    agentById: () => ({ env: [{ name: 'TOKEN', hasValue: true }] }),
    currentAgent: () => ({ id: 'a1' }),
    composerFor: (k) => owners.get(k) || owners.set(k, { draft: '' }).get(k),
    composerKey: 'cv_1',
    $: () => null,
    autoGrow: () => {},
    scheduleDraftSave: (k) => saved.push(k),
    closePopover: () => {},
    owners, saved,
    ...over,
  };
  return vm.createContext(ctx);
}

function taStub(value, sel = value.length) {
  return {
    value, selectionStart: sel, selectionEnd: sel, isConnected: true,
    dataset: { composerKey: 'cv_1' },
    focused: false, range: null,
    focus() { this.focused = true; },
    setSelectionRange(a, b) { this.range = [a, b]; },
  };
}

const loadInsert = (ctx) => vm.runInNewContext(
  `let secretScope = null;\n${insertSrc}\n;({ insertSecretRef, setScope: (s) => { secretScope = s; } })`, ctx);

test('insert splices the tag at the live caret, updates draft and refocuses', () => {
  const ctx = insertCtx();
  const { insertSecretRef, setScope } = loadInsert(ctx);
  const ta = taStub('foo bar', 4);
  setScope({ key: 'cv_1', agentId: 'a1', ta });
  assert.equal(insertSecretRef('TOKEN'), true);
  assert.equal(ta.value, 'foo [[env:TOKEN]]bar');
  assert.equal(ctx.owners.get('cv_1').draft, 'foo [[env:TOKEN]]bar');
  assert.deepEqual(ta.range, [17, 17]);
  assert.equal(ta.focused, true);
  assert.deepEqual(ctx.saved, ['cv_1']);
});

test('typing since open moves the insertion point with the live caret', () => {
  const ctx = insertCtx();
  const { insertSecretRef, setScope } = loadInsert(ctx);
  const ta = taStub('', 0);
  setScope({ key: 'cv_1', agentId: 'a1', ta });
  ta.value = 'newer text';
  ta.selectionStart = ta.selectionEnd = 10;
  assert.equal(insertSecretRef('TOKEN'), true);
  assert.equal(ta.value, 'newer text[[env:TOKEN]]');
});

test('a click after switching room or agent is rejected and writes nothing', () => {
  const ctx = insertCtx();
  const { insertSecretRef, setScope } = loadInsert(ctx);
  const ta = taStub('keep me', 0);
  setScope({ key: 'cv_other', agentId: 'a1', ta });
  assert.equal(insertSecretRef('TOKEN'), false);
  setScope({ key: 'cv_1', agentId: 'a2', ta });
  assert.equal(insertSecretRef('TOKEN'), false);
  assert.equal(ta.value, 'keep me');
  assert.equal(ctx.owners.get('cv_1').draft, '');
});

test('unsaved or invalid names cannot be inserted', () => {
  const ctx = insertCtx({ agentById: () => ({ env: [{ name: 'TOKEN', hasValue: false }] }) });
  const { insertSecretRef, setScope } = loadInsert(ctx);
  setScope({ key: 'cv_1', agentId: 'a1', ta: taStub('', 0) });
  assert.equal(insertSecretRef('TOKEN'), false);
  assert.equal(insertSecretRef('BAD-NAME'), false);
});

test('a replaced textarea falls back to the current prompt of the bound room', () => {
  const fresh = taStub('x', 1);
  const stale = taStub('y', 0);
  stale.isConnected = false;
  const ctx = insertCtx({ $: (sel) => (sel === '#prompt' ? fresh : null) });
  const { insertSecretRef, setScope } = loadInsert(ctx);
  setScope({ key: 'cv_1', agentId: 'a1', ta: stale });
  assert.equal(insertSecretRef('TOKEN'), true);
  assert.equal(fresh.value, 'x[[env:TOKEN]]');
  assert.equal(stale.value, 'y');
});

/* ---------------- agent env row refresh ---------------- */

const rowSrc = (
  slice('function agentEnvSavedMatch', '\nexport async function copyEnvRef')
    .replace(/^export /gm, '')
);

function fakeRow({ name = 'TOKEN', value = '', hasValue = true } = {}) {
  const els = {
    '[name=env-name]': { value: name },
    '[name=env-value]': { value, placeholder: '', dataset: hasValue ? { hasValue: '1' } : {} },
    '[data-env-ref]': { textContent: '' },
    '[data-action="copy-env-ref"]': { disabled: null },
    '[data-env-value-label]': { textContent: '' },
  };
  return { els, querySelector: (s) => els[s] || null };
}

function rowCtx(env) {
  return vm.createContext({ S: { agentForm: { agent: { env } } }, isValidEnvRefName, envRefTag });
}

test('row refresh shows the live tag and enables copy only for saved matches', () => {
  const { refreshAgentEnvRow } = vm.runInNewContext(`${rowSrc}; ({ refreshAgentEnvRow })`, rowCtx([{ name: 'TOKEN', hasValue: true }]));
  const row = fakeRow();
  refreshAgentEnvRow(row);
  assert.equal(row.els['[data-env-ref]'].textContent, '[[env:TOKEN]]');
  assert.equal(row.els['[data-action="copy-env-ref"]'].disabled, false);
  assert.equal(row.els['[data-env-value-label]'].textContent, 'Saved value');
  // Renaming the row makes it unsaved: copy off, value reads as new.
  row.els['[name=env-name]'].value = 'RENAMED';
  refreshAgentEnvRow(row);
  assert.equal(row.els['[data-env-ref]'].textContent, '[[env:RENAMED]]');
  assert.equal(row.els['[data-action="copy-env-ref"]'].disabled, true);
  assert.equal(row.els['[data-env-value-label]'].textContent, 'New value');
  // An invalid name disables copy and keeps the literal preview.
  row.els['[name=env-name]'].value = 'MY-KEY';
  refreshAgentEnvRow(row);
  assert.equal(row.els['[data-action="copy-env-ref"]'].disabled, true);
  // Typing a value over a saved row marks it as a new value.
  row.els['[name=env-name]'].value = 'TOKEN';
  row.els['[name=env-value]'].value = 'typed';
  refreshAgentEnvRow(row);
  assert.equal(row.els['[data-env-value-label]'].textContent, 'New value');
});
