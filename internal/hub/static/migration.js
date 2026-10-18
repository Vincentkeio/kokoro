(function () {
  'use strict';
  var root = document.querySelector('[data-panel-migration]');
  if (!root) return;
  var exportForm = root.querySelector('[data-backup-export]');
  var importForm = root.querySelector('[data-backup-import]');
  var startForm = root.querySelector('[data-migration-start]');
  var status = root.querySelector('[data-migration-status]');
  var stop = root.querySelector('[data-migration-stop]');
  var refresh = root.querySelector('[data-migration-refresh]');
  var restoreBusy = false, polling = false, hasPlan = false;
  function say(el, text, bad) { el.textContent = text; el.classList.toggle('bad', !!bad); }
  async function request(url, opts) {
    var resp = await fetch(url, Object.assign({ credentials: 'same-origin', headers: { 'Accept': 'application/json' } }, opts || {}));
    var data;
    try { data = await resp.json(); } catch (_) { throw new Error('会话可能已过期，请重新登录后再操作。'); }
    if (!resp.ok || !data.ok) throw new Error(data.error || '操作失败，请稍后重试。');
    return data;
  }
  exportForm.addEventListener('submit', async function (event) {
    event.preventDefault();
    if (restoreBusy) { event.preventDefault(); return; }
    var password = exportForm.elements.password, repeat = exportForm.elements.confirm_password;
    repeat.setCustomValidity(password.value === repeat.value ? '' : '两次输入的密码不一致');
    if (!exportForm.reportValidity()) { event.preventDefault(); return; }
    var button = exportForm.querySelector('button'), msg = root.querySelector('[data-export-status]');
    var id = window.crypto && crypto.randomUUID ? crypto.randomUUID() : String(Date.now());
    exportForm.elements.download_id.value = id;
    button.disabled = true; say(msg, '正在生成加密备份，完成后浏览器会开始下载。');
    try { var state = await request('/admin/backup/status'); if (state.busy) throw new Error('已有备份或恢复操作进行中，请稍后重试。'); exportForm.submit(); }
    catch (err) { button.disabled = false; say(msg, err.message, true); return; }
    var started = Date.now();
    var timer = setInterval(function () {
      var cookie = document.cookie.split('; ').find(function (part) { return part.indexOf('kokoro_backup_result=' + id + '.') === 0; });
      if (!cookie && Date.now() - started < 30 * 60 * 1000) return;
      clearInterval(timer); button.disabled = false;
      var ready = cookie && cookie.endsWith('.ready');
      say(msg, ready ? '备份已生成，请确认文件下载完成后再关闭旧面板。' : '未能完成备份，请检查磁盘空间、登录状态和密码后重试。', !ready);
      if (ready) exportForm.reset();
    }, 1000);
  });
  exportForm.elements.confirm_password.addEventListener('input', function () { this.setCustomValidity(''); });
  exportForm.elements.password.addEventListener('input', function () { exportForm.elements.confirm_password.setCustomValidity(''); });
  importForm.addEventListener('submit', async function (event) {
    event.preventDefault(); if (restoreBusy) return;
    var msg = root.querySelector('[data-import-status]');
    if (importForm.elements.confirm.value !== '覆盖并恢复') { say(msg, '请输入“覆盖并恢复”确认导入。', true); return; }
    if (!window.confirm('将覆盖当前面板的数据，并使当前登录失效。确认导入此备份？')) return;
    restoreBusy = true;
    var buttons = root.querySelectorAll('button'); buttons.forEach(function (b) { b.disabled = true; });
    say(msg, '正在上传并校验备份，请保持此页面打开。');
    try {
      var data = await request('/admin/backup/import', { method: 'POST', body: new FormData(importForm) });
      say(msg, data.message); importForm.reset();
      var link = document.createElement('a'); link.href = '/admin#sys'; link.textContent = '重新登录'; link.className = 'btn'; msg.appendChild(document.createTextNode(' ')); msg.appendChild(link);
    } catch (err) { restoreBusy = false; buttons.forEach(function (b) { b.disabled = false; }); say(msg, err.message, true); }
  });
  var labels = { waiting: '等待上线', upgrade_required: '需要升级探针', sent: '已下发', awaiting: '等待新面板上报', failed: '失败，稍后重试', arrived: '已到达新面板' };
  function render(plan) {
    hasPlan = !!plan; stop.hidden = !plan || !plan.active;
    startForm.querySelector('[type="submit"]').disabled = restoreBusy || !!(plan && plan.active);
    var table = root.querySelector('[data-migration-table]'), rows = root.querySelector('[data-migration-rows]');
    table.hidden = !plan; rows.replaceChildren(); if (!plan) { say(status, '尚未发起迁移。'); return; }
    var nodes = plan.nodes || [], count = nodes.filter(function (n) { return n.state === 'arrived'; }).length;
    say(status, (plan.active ? '迁移中' : count === nodes.length ? '迁移完成' : '已停止下发') + ' · 已到达 ' + count + ' / ' + nodes.length + ' · ' + plan.target + (plan.sync_error ? '；' + plan.sync_error : ''), !!plan.sync_error);
    nodes.forEach(function (n) {
      var tr = document.createElement('tr');
      [n.name, labels[n.state] || n.state, n.error || (n.state === 'upgrade_required' ? '升级后上线会自动继续，无需重装或重新注册。' : '')].forEach(function (value) { var td = document.createElement('td'); td.textContent = value; tr.appendChild(td); });
      rows.appendChild(tr);
    });
  }
  async function load() {
    if (polling || restoreBusy) return; polling = true; refresh.disabled = true;
    try { var data = await request('/admin/migration'); render(data.plan); }
    catch (err) { say(status, err.message, true); }
    finally { polling = false; refresh.disabled = restoreBusy; }
  }
  startForm.addEventListener('submit', async function (event) {
    event.preventDefault(); if (restoreBusy) return;
    if (!window.confirm('确认已在新面板导入最近的备份？验证通过后将通知探针更换连接地址。')) return;
    var button = startForm.querySelector('[type="submit"]'); button.disabled = true; say(status, '正在验证新面板身份…');
    try { var body = new URLSearchParams(new FormData(startForm)); body.set('action', 'start'); var data = await request('/admin/migration', { method: 'POST', body: body }); render(data.plan); }
    catch (err) { button.disabled = false; say(status, err.message, true); }
  });
  stop.addEventListener('click', async function () {
    if (!window.confirm('停止向未迁移节点下发指令？已经迁移或正在切换的节点不会自动回退。')) return;
    stop.disabled = true;
    try { await request('/admin/migration', { method: 'POST', body: new URLSearchParams({ action: 'cancel' }) }); await load(); }
    catch (err) { say(status, err.message, true); } finally { stop.disabled = false; }
  });
  refresh.addEventListener('click', load);
  function visible() { return !document.hidden && root.getClientRects().length > 0; }
  window.addEventListener('hashchange', function () { if (visible()) load(); });
  var tabs = document.querySelector('.atabs');
  if (tabs) tabs.addEventListener('click', function () { setTimeout(function () { if (visible()) load(); }, 0); });
  setInterval(function () { if (visible() && hasPlan) load(); }, 10000);
  if (visible()) load();
})();
