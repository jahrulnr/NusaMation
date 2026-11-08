const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

// Every ?v=N cache-bust specifier must share one generation per asset class:
// one for JS import specifiers, one for CSS. A stale file left on the old
// generation silently forks the module graph — two copies of state.js etc.
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

const versions = (file) => [...fs.readFileSync(file, 'utf8').matchAll(/\?v=(\d+)/g)].map((m) => m[1]);

const jsFiles = walk(staticDir).filter((f) => f.endsWith('.js'));
const jsGens = new Map();
for (const file of jsFiles) {
  for (const v of versions(file)) jsGens.set(v, [...(jsGens.get(v) || []), path.basename(file)]);
}

test('all JS import specifiers share one ?v= generation', () => {
  assert.equal(jsGens.size, 1,
    `JS ?v= generations diverged: ${[...jsGens.entries()].map(([v, fs]) => `v=${v} in ${fs.join(', ')}`).join('; ')}`);
});

test('index.html and app.css generations match their asset class', () => {
  const html = fs.readFileSync(path.join(staticDir, 'index.html'), 'utf8');
  const jsGen = [...jsGens.keys()][0];
  for (const m of html.matchAll(/app\.js\?v=(\d+)/g)) assert.equal(m[1], jsGen, 'index.html app.js generation');

  const cssImports = versions(path.join(staticDir, 'app.css'));
  const cssGen = new Set(cssImports);
  assert.equal(cssGen.size, 1, 'app.css @import generations diverged');
  for (const m of html.matchAll(/\.css\?v=(\d+)/g)) assert.equal(m[1], [...cssGen][0], 'index.html stylesheet generation');
});
