const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

// Static freshness is backend-owned: /static/* serves a strong ETag with
// Cache-Control: no-cache, so browsers always revalidate. Specifiers carry
// no ?v= — every importer names the same URL, which is what keeps the
// module graph to one instance of each module (a mixed ?v= generation used
// to fork it silently).
const staticDir = path.join(__dirname, '..', 'static');

function walk(dir) {
  const out = [];
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) out.push(...walk(full));
    else out.push(full);
  }
  return out;
}

const ownFiles = walk(staticDir).filter((f) => !f.includes(`${path.sep}vendor${path.sep}`));

test('no ?v= cache-bust specifiers remain', () => {
  const hits = [];
  for (const file of ownFiles) {
    for (const m of fs.readFileSync(file, 'utf8').matchAll(/\?v=\d+/g)) {
      hits.push(`${path.relative(staticDir, file)}: ?v=${m[1]}`);
    }
  }
  assert.deepEqual(hits, [], `stale ?v= specifiers: ${hits.join('; ')}`);
});
