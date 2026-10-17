// Execute the production dialog handlers with delayed network responses. Verify
// that switching nodes cannot let an earlier response modify the current form.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, '../internal/hub/static/app.js'), 'utf8');

function element() {
  return {
    value: '', textContent: '', hidden: false, disabled: false, style: {}, dataset: {}, listeners: {},
    classList: { add() {}, remove() {} },
    addEventListener(name, fn) { (this.listeners[name] ||= []).push(fn); },
    setAttribute() {}, removeAttribute() {}, focus() {}, appendChild() {},
    querySelector() { return null; }, querySelectorAll() { return []; },
    emit(name, event = {}) { for (const fn of this.listeners[name] || []) fn(event); }
  };
}
function environment(code) {
  const ids = ['dlg-scrim','d-art','d-art-body','d-art-admin','d-cmt','d-cmt-body','d-cmt-admin','d-cmt-foot','d-cmt-form','d-cmt-msg',
    'buy-scrim','buy-send','buy-slug','buy-contact','buy-note','buy-title','buy-err','buy-form','buy-intro','buy-result'];
  const nodes = Object.fromEntries(ids.map(id => [id, element()]));
  const send = element();
  const fields = Object.assign([element(),element(),element(),send], { author: null, contact: null, content: null });
  [fields.author,fields.contact,fields.content] = fields;
  nodes['d-cmt-form'].elements = fields;
  nodes['d-cmt-form'].reset = () => fields.forEach(field => { field.value = ''; });
  nodes['d-cmt-form'].querySelector = () => send;
  const document = Object.assign(element(), { body: element(), activeElement: element(), getElementById: id => nodes[id] || null });
  const pending = [];
  const fetch = (url, options = {}) => {
    if (options.method !== 'POST') return Promise.resolve({ ok: true, json: async () => ({ ok: true, comments: [] }) });
    return new Promise(resolve => pending.push({ url, options, reply(data, ok = true) { resolve({ ok, json: async () => data }); } }));
  };
  vm.runInNewContext(code, { document, fetch, URLSearchParams, Map, Date, navigator: {}, setTimeout() { return 1; }, clearTimeout() {} });
  const open = (selector, dataset) => document.emit('click', { target: { closest: query => query === selector ? { dataset } : null } });
  return { nodes, send, fields, pending, open };
}
const flush = () => new Promise(setImmediate);

async function comments() {
  const start = source.indexOf('(function () {', source.indexOf('/* ---- 弹窗：文章'));
  const env = environment(source.slice(start, source.indexOf('/* ---- 复制后台 Agent', start)));
  const { nodes, send, fields, pending, open } = env;
  const submit = () => nodes['d-cmt-form'].emit('submit', { preventDefault() {} });
  open('[data-dlg]', { dlg: 'first', panel: 'cmt' });
  fields.content.value = 'first comment';
  submit(); submit();
  assert.equal(pending.length, 1, 'double click must make one request');
  open('[data-dlg]', { dlg: 'second', panel: 'cmt' });
  fields.content.value = 'second comment';
  submit();
  pending[0].reply({ ok: true });
  await flush();
  assert.equal(send.hidden, false, 'old success must not hide the new send button');
  assert.equal(send.disabled, true, 'old success must not enable a pending new request');
  assert.equal(fields.content.value, 'second comment', 'old success must not clear new input');
  pending[1].reply({ ok: true, pending: true });
  await flush();
  assert.equal(send.hidden, true);
  submit();
  assert.equal(pending.length, 2, 'a sent form must not submit again');
  open('[data-dlg]', { dlg: 'third', panel: 'cmt' });
  assert.equal(send.disabled, false);
  assert.equal(send.hidden, false);
}

async function purchases() {
  const start = source.indexOf('(function () {', source.indexOf('/* ---- 「我想买它」'));
  const { nodes, pending, open } = environment(source.slice(start));
  const send = nodes['buy-send'];
  const submit = () => send.emit('click');
  open('[data-buy]', { buy: 'first', buyname: 'first' });
  nodes['buy-contact'].value = 'first contact';
  submit(); submit();
  assert.equal(pending.length, 1);
  open('[data-buy]', { buy: 'second', buyname: 'second' });
  nodes['buy-contact'].value = 'second contact';
  submit();
  pending[0].reply({ ok: true, telegram_status: 'sent' });
  await flush();
  assert.equal(send.hidden, false, 'old purchase must not hide a new form');
  assert.equal(send.disabled, true);
  pending[1].reply({ error: 'rate limited' }, false);
  await flush();
  assert.equal(send.disabled, false);
  assert.equal(nodes['buy-err'].textContent, 'rate limited');
  submit();
  pending[2].reply({ ok: true, telegram_status: 'sent' });
  await flush();
  assert.equal(send.hidden, true);
  submit();
  assert.equal(pending.length, 3);
}

(async () => {
  await comments();
  await purchases();
  console.log('Dialog sending passed: duplicate submission guards, stale-response isolation and sent-state reset.');
})().catch(error => { console.error(error); process.exitCode = 1; });
