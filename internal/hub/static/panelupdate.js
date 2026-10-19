(function () {
  'use strict';
  var root = document.querySelector('[data-panel-update]'); if (!root) return;
  var check = root.querySelector('[data-update-check]'), hub = root.querySelector('[data-update-hub]'), agents = root.querySelector('[data-update-agents]');
  var feedback = root.querySelector('[data-update-message]'), hubMsg = root.querySelector('[data-hub-update-message]'), agentsMsg = root.querySelector('[data-agents-update-message]');
  var auto = root.querySelector('[data-update-auto]'), autoStatus = root.querySelector('[data-auto-update-status]'), autoEnabled = false, autoSaving = false, autoRevision = 0;
  var target = '', hubAvailable = false, agentCount = 0, supported = false, busy = false, hubBusy = false, agentsBusy = false, polling = false;
  var names = { waiting: '待下发', running: '更新中', completed: '更新完成', current: '无需更新', failed: '更新失败', unsupported: '需手动更新' };
  function say(text, bad) { feedback.textContent = text; feedback.classList.toggle('bad', !!bad); }
  function buttons() { check.disabled = busy || hubBusy; hub.disabled = busy || hubBusy || agentsBusy || !target || !hubAvailable || !supported; agents.disabled = busy || hubBusy || agentsBusy || !target || !agentCount; }
  async function request(action, extra) {
    var controller = typeof AbortController === 'function' ? new AbortController() : null;
    var timeout = controller ? setTimeout(function () { controller.abort(); }, 15000) : null;
    var opts = { credentials: 'same-origin', headers: { Accept: 'application/json' } };
    if (controller) opts.signal = controller.signal;
    if (action) { opts.method = 'POST'; opts.body = new URLSearchParams(Object.assign({ action: action, target: target }, extra || {})); }
    try {
      var res = await fetch('/admin/update', opts), data;
      try { data = await res.json(); } catch (_) { throw new Error('无法读取更新状态，请检查登录或稍后重试。'); }
      if (!res.ok || !data.ok) throw new Error(data.error || '操作失败，请稍后重试。'); return data;
    } catch (e) {
      if (e && e.name === 'AbortError') throw new Error('读取更新状态超时，请稍后重试。');
      throw e;
    } finally { if (timeout) clearTimeout(timeout); }
  }
  function renderAuto(data) {
    if (autoSaving || typeof data.auto_update_enabled !== 'boolean') return;
    autoEnabled = data.auto_update_enabled;
    auto.classList.toggle('on', autoEnabled);
    auto.setAttribute('aria-checked', String(autoEnabled));
    auto.querySelector('.switch-text').textContent = autoEnabled ? '开启' : '关闭';
    auto.disabled = false;
    var parts = [];
    if (data.auto_update_last_check) parts.push('上次检查：' + new Date(data.auto_update_last_check).toLocaleString('zh-CN', { hour12: false }));
    if (data.auto_update_last_error) parts.push(data.auto_update_last_error);
    else if (data.auto_update_last_result) parts.push(data.auto_update_last_result);
    if (autoEnabled && data.auto_update_next_check) parts.push('下次检查：' + new Date(data.auto_update_next_check).toLocaleString('zh-CN', { hour12: false }));
    if (!autoEnabled) parts.push('自动更新已关闭');
    autoStatus.textContent = parts.join('；') || '自动更新已开启';
    autoStatus.classList.toggle('bad', !!data.auto_update_last_error);
  }
  function render(data, revision) {
    if (revision === autoRevision) renderAuto(data);
    supported = !!data.supported; hubBusy = !!data.hub && /^(queued|running)$/.test(data.hub.status); agentsBusy = false;
    if (data.hub && (!target || data.hub.target === target)) { hubMsg.textContent = data.hub.message; if (data.hub.status === 'completed' && data.hub.target === target) hubAvailable = false; }
    if (!supported) hubMsg.textContent = '当前服务环境不支持在线更新，请通过服务器管理菜单更新。';
    if (data.plan_error) agentsMsg.textContent = data.plan_error + '，无法执行探针更新。';
    var rows = root.querySelector('[data-update-rows]'); rows.replaceChildren();
    var nodes = data.plan && data.plan.nodes || [], done = 0, failed = 0;
    nodes.forEach(function (n) {
      if (n.state === 'waiting' || n.state === 'running') agentsBusy = true;
      if (n.state === 'completed' || n.state === 'current') done++;
      if (n.state === 'failed' || n.state === 'unsupported') failed++;
      var tr = document.createElement('tr'); [n.name, names[n.state] || n.state, n.message].forEach(function (value) { var td = document.createElement('td'); td.textContent = value || '—'; tr.appendChild(td); }); rows.appendChild(tr);
    });
    root.querySelector('[data-update-table]').hidden = !nodes.length;
    if (nodes.length && (!target || data.plan.target === target)) { agentsMsg.textContent = '共 ' + nodes.length + ' 台，完成 ' + done + ' 台' + (failed ? '，需处理 ' + failed + ' 台' : '') + (agentsBusy ? '，正在更新。' : '。'); if (!agentsBusy && !failed && data.plan.target === target) agentCount = 0; }
    buttons();
  }
  auto.addEventListener('click', async function () {
    if (autoSaving) return;
    autoSaving = true; auto.disabled = true; autoRevision++;
    try {
      var data = await request('auto', { enabled: autoEnabled ? '0' : '1' });
      autoSaving = false; renderAuto(data);
      say(autoEnabled ? '自动更新已开启，每天北京时间凌晨 1:00 检查。' : '自动更新已关闭。');
    } catch (e) { say(e.message, true); }
    finally { autoSaving = false; auto.disabled = false; }
  });
  check.addEventListener('click', async function () {
    if (busy) return; busy = true; buttons(); say('正在检查更新…');
    try { var data = await request('check'); target = data.target; hubAvailable = data.hub_available; agentCount = data.agent_count; supported = data.supported;
      hubMsg.textContent = !supported ? '当前服务环境不支持在线更新，请通过服务器管理菜单更新。' : hubAvailable ? '有可用更新。' : '无可用更新。';
      var onlineCount = data.online_count || 0;
      agentsMsg.textContent = onlineCount === 0 ? '没有在线的探针，无法下发更新。' : agentCount ? '有可用更新（' + agentCount + ' 台在线探针）。' : '无可用更新。'; say('检查完成。');
    } catch (e) { say(e.message, true); } finally { busy = false; buttons(); }
  });
  async function start(action) {
    if (busy || !target) return;
    if (!window.confirm(action === 'hub' ? '确认更新面板？面板会短暂重启，保留现有配置和历史数据。' : '确认更新在线探针？每台只下发一次，节点会短暂重连，保留现有配置和历史数据。')) return;
    busy = true; buttons(); say('正在启动更新…');
    try { await request(action); if (action === 'hub') hubBusy = true; else agentsBusy = true; say('更新任务已启动，可在这里查看进度。'); await poll(true); }
    catch (e) { say(e.message, true); } finally { busy = false; buttons(); }
  }
  hub.addEventListener('click', function () { start('hub'); }); agents.addEventListener('click', function () { start('agents'); });
  async function poll(force) {
    if (polling || document.hidden || (!force && !root.getClientRects().length)) return; polling = true;
    var revision = autoRevision;
    try { render(await request(), revision); } catch (e) {
      if (hubBusy) say('面板正在重启或重连，将自动刷新更新结果。');
      else if (auto.disabled && !autoSaving) {
        auto.querySelector('.switch-text').textContent = '读取失败';
        autoStatus.textContent = '读取失败：' + e.message;
        autoStatus.classList.add('bad');
      }
      if (force) say(e.message, true);
    } finally { polling = false; }
  }
  setInterval(function () { if (hubBusy || agentsBusy) poll(false); }, 5000);
  setInterval(function () { if (!hubBusy && !agentsBusy) poll(false); }, 30000);
  document.addEventListener('visibilitychange', function () { if (!document.hidden) poll(false); });
  window.addEventListener('hashchange', function () { setTimeout(function () { poll(false); }, 50); });
  // The admin tab switch uses a delegated click without changing the hash.
  document.addEventListener('click', function (event) { if (event.target.closest('[data-goto]')) setTimeout(function () { poll(false); }, 100); });
  poll(true);
})();
