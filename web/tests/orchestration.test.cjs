const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const src = fs.readFileSync(require('node:path').join(__dirname, '../static/components/conversations.js'), 'utf8');
const code = src.slice(src.indexOf('export async function toggleOrchestrationFlag'), src.indexOf('\nexport function openTeamPanel')).replace('export ', '');
for (const key of ['spawn', 'progressPings']) test(`toggling ${key} preserves read-only control`, async () => {
  const policy = { enabled: true, readOnly: true, scope: 'project', spawn: false, progressPings: false };
  const c = { id: 'caller', metadata: { orchestration: policy } };
  let body;
  const toggle = vm.runInNewContext(`(${code})`, {
    S: { conversations: [c], current: c },
    orchestrationPolicy: x => x.metadata.orchestration,
    api: async (url, opts) => { body = opts.body; return { conversation: c }; },
    upsertConversation() {}, openOrchestrationMenu() {}, toast() {},
  });
  await toggle(key, c.id);
  assert.equal(body.readOnly, true);
  assert.equal(body.enabled, true);
  assert.equal(body.scope, 'project');
  assert.equal(body[key], true);
});
