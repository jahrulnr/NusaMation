const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const app = fs.readFileSync(path.join(__dirname, '..', 'static', 'components', 'markdown.js'), 'utf8');
const start = app.indexOf('export function setupDiagramZoom(');
const end = app.indexOf('\nexport async function renderDiagram(', start);
assert.ok(start >= 0 && end > start);
function setup() {
  const handlers = {};
  const svg = { style: {}, getBoundingClientRect() { const width = parseFloat(this.style.width) || 400; return { left: -viewport.scrollLeft, top: -viewport.scrollTop, width, height: width / 2 }; } }, label = { textContent: '' };
  const viewport = { scrollLeft: 0, scrollTop: 0, classList: { add() {}, remove() {} }, setPointerCapture() {}, getBoundingClientRect: () => ({ left: 0, top: 0, width: 200, height: 100 }), addEventListener: (name, fn) => { handlers[name] = fn; } };
  const figure = { addEventListener: (name, fn) => { handlers[name] = fn; } };
  const query = (selector) => ({ '.diagram-viewport': viewport, 'svg': svg, '.diagram-scale': label })[selector];
  const fn = vm.runInNewContext(`(${app.slice(start, end).trim().replace(/^export /, '')})`, { $: query, Math });
  fn(figure, 400);
  return { handlers, svg, label, viewport };
}
test('wheel and two-finger pinch resize the diagram', () => {
  const { handlers, svg, label, viewport } = setup();
  assert.equal(svg.style.width, '400px');
  let prevented = false;
  viewport.scrollLeft = 180;
  handlers.wheel({ deltaY: -100, clientX: 150, clientY: 50, preventDefault: () => { prevented = true; } });
  assert.ok(prevented && parseFloat(svg.style.width) > 400);
  assert.ok(viewport.scrollLeft > 180, 'zoom follows the point on the right');
  handlers.click({ target: { closest: () => ({ dataset: { diagramZoom: 'reset' } }) }, stopPropagation() {} });
  const touch = (x1, x2) => ({ touches: [{ clientX: x1, clientY: 0 }, { clientX: x2, clientY: 0 }], preventDefault() {} });
  handlers.touchstart(touch(0, 100));
  handlers.touchmove(touch(0, 150));
  assert.equal(svg.style.width, '600px');
  assert.equal(label.textContent, '150%');
});

test('mouse drag pans a zoomed diagram', () => {
  const { handlers, viewport } = setup();
  viewport.scrollLeft = 200;
  handlers.pointerdown({ pointerType: 'mouse', button: 0, clientX: 150, clientY: 50 });
  handlers.pointermove({ pointerType: 'mouse', pointerId: 1, clientX: 90, clientY: 50, preventDefault() {} });
  assert.equal(viewport.scrollLeft, 260);
  handlers.pointerup();
});
