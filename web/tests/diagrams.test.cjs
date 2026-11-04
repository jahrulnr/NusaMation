const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const code = fs.readFileSync(path.join(__dirname, '..', 'static', 'vendor', 'pele.min.js'));
const pele = import(`data:text/javascript;base64,${code.toString('base64')}`);

test('Pele renders the Gantt and pie examples used in chat', async () => {
  const { render } = await pele;
  const fixtures = [
    ['gantt\n title Rilis v1\n dateFormat YYYY-MM-DD\n section Backend\n API :a1, 2026-11-01, 5d\n section Frontend\n UI :after a1, 4d', ['Rilis v1', 'Backend', 'Frontend']],
    ['pie title Kanal chat\n "Web" : 60\n "WhatsApp" : 30\n "API" : 10', ['Kanal chat', 'Web', 'WhatsApp', 'API']],
  ];
  for (const [source, labels] of fixtures) {
    const result = render(source, { idPrefix: 'test-diagram', responsive: false });
    assert.match(result.svg, /<svg\b/);
    assert.ok(result.width > 0 && result.height > 0);
    for (const label of labels) assert.ok(result.svg.includes(label), label);
  }
});
test('unsupported diagram remains an explicit render error', async () => {
  const { render } = await pele;
  assert.throws(() => render('unsupportedDiagram\n something'));
});
