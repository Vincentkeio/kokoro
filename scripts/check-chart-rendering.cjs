// Exercise the production renderer, including discontinuities and range redraws.
// No browser packages are needed: SVG output is inspected before DOM painting.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, '../internal/hub/static/app.js'), 'utf8');
const geometry = source.slice(0, source.indexOf("window.addEventListener('resize'"));
const renderer = source.slice(source.indexOf('  function styleServerSparks(root)'), source.indexOf('  // 主机时钟不再做'));
let chartType = 'area';
const documentListeners = {};
const rootClasses = new Set();
const document = {
  hidden: false,
  documentElement: { classList: { toggle(name, state) { state ? rootClasses.add(name) : rootClasses.delete(name); } } },
  addEventListener(name, listener) { documentListeners[name] = listener; },
  body: { getAttribute: () => chartType },
  querySelectorAll: () => [],
  createElementNS: (_, tag) => ({ tagName: tag, attrs: {}, setAttribute(k, v) { this.attrs[k] = v; } })
};
const context = vm.createContext({
  document, window: { addEventListener() {} },
  getComputedStyle: () => ({ getPropertyValue: () => '2.5' })
});
vm.runInContext(geometry + renderer, context);
document.hidden = true;
documentListeners.visibilitychange();
assert.ok(rootClasses.has('k-effects-paused'), 'hidden documents must pause theme motion');
document.hidden = false;
documentListeners.visibilitychange();
assert.ok(!rootClasses.has('k-effects-paused'), 'visible documents must resume theme motion');
function svg(d) {
  return {
    dataset: {}, innerHTML: '', viewBox: { baseVal: { width: 260, height: 40 } },
    matches: () => true, querySelector: () => ({ getAttribute: () => d }),
    querySelectorAll: () => [], getBoundingClientRect: () => ({ width: 520, height: 80 })
  };
}
function render(d, type) {
  chartType = type;
  const element = svg(d);
  context.styleServerSparks(element);
  return element.innerHTML;
}
for (const type of ['area', 'line', 'step']) {
  const continuous = render('M10 30 L20 15 L30 25', type);
  assert.equal((continuous.match(/class="sp-line"/g) || []).length, 1, `${type}: connected points must stay connected`);
  assert.equal((continuous.match(/spark-dot/g) || []).length, 0, `${type}: a trace must not become dots`);
  assert.ok(continuous.includes(type === 'step' ? ' H' : ' C'), `${type}: expected chart geometry is missing`);
  const gaps = render('M10 30 L20 15 M200 25 L210 20', type);
  assert.equal((gaps.match(/class="sp-line"/g) || []).length, 2, `${type}: missing intervals must stay disconnected`);
}
const sparse = render('M10 30 M200 25 L210 20', 'line');
assert.equal((sparse.match(/spark-dot/g) || []).length, 1, 'an isolated sample must appear exactly once');
assert.equal((sparse.match(/class="sp-line"/g) || []).length, 1);
const bars = render('M200 25 L210 20', 'bar');
assert.equal((bars.match(/<rect/g) || []).length, 2);
assert.ok(!bars.includes('x="0.'), 'late samples must not be stretched across the whole chart');

// Memory must use RAM capacity across low/high usage windows, without rounding
// it to a traffic-style peak. Unknown capacity retains the automatic fallback.
const detail = source.slice(source.indexOf('  function niceCeil(v)'), source.indexOf('  // ---- 点赞 / 点踩 ----'));
vm.runInContext(detail, context);
let detailScale;
context.drawChartAxes = (_, scale) => { detailScale = scale; };
const ram = 487272448;
for (const used of [160 * 1024 * 1024, 400 * 1024 * 1024]) {
  const memory = { innerHTML: '', getAttribute: () => String(ram) };
  context.drawSpark(memory, [used, used], 'memory');
  assert.equal(detailScale, ram, 'memory scale must equal actual RAM capacity');
}
context.drawSpark({ innerHTML: '', getAttribute: () => '0' }, [100, 120], 'bytes');
assert.equal(detailScale, 200, 'unknown RAM capacity must retain automatic scaling');

const update = source.slice(source.indexOf('  function setCardRange(card, select, key)'), source.indexOf("  document.querySelectorAll('[data-home-range]')"));
vm.runInContext("var allowedRanges=['1h','6h','12h','1d','7d','30d']; var nodeRangePrefix='test.';" + update, context);
const oldSvg = { childNodes: [{ tagName: 'rect' }, { tagName: 'ellipse' }, { tagName: 'path' }], replaceChildren(...nodes) { this.childNodes = nodes; } };
const select = { disabled: false };
const card = {
  getAttribute: () => 'test-node', setAttribute() {}, removeAttribute() {},
  querySelector: selector => selector === '.spark' ? oldSvg : null
};
context.fetch = async () => ({ ok: true, json: async () => ({ line: 'M10 30 L20 20', area: '', peak: '1 KB/s' }) });
context.localStorage = { setItem() {} };
// The range updater attaches an inspector in production. This isolated test
// exercises SVG replacement, so provide the small interface it consumes.
context.initTrafficInspector = () => ({ setData() {} });
context.window.kokoroRerenderServerSpark = element => {
  assert.equal(element.childNodes.length, 2, 'range changes must remove all previous bars, dots and segments');
  assert.deepEqual(element.childNodes.map(n => n.attrs.class), ['sp-area', 'sp-line']);
};
context.setCardRange(card, select, '1d').then(() => {
  assert.equal(select.disabled, false);
  console.log('Chart rendering passed: connected curves, real gaps, isolated samples, time-aligned bars and complete range redraws.');
}).catch(error => { console.error(error); process.exitCode = 1; });
