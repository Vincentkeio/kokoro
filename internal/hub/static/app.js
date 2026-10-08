/* Kokoro 前端：只做两件事 —— 订阅 SSE 更新数值，画迷你曲线。
   不引任何框架，保持零 CDN 依赖，离线也能用。 */
(function () {
  'use strict';

  function fmtSize(f) {
    var u = ['', 'K', 'M', 'G', 'T', 'P'], i = 0;
    while (f >= 1024 && i < u.length - 1) { f /= 1024; i++; }
    if (i === 0) return Math.round(f) + ' B';
    return (f >= 100 ? f.toFixed(0) : f.toFixed(1)) + ' ' + u[i] + 'B';
  }
  function fmtRate(n) { return n == null ? '-' : fmtSize(n) + '/s'; }
  function pct(used, total) {
    if (!total) return 0;
    var p = used / total * 100;
    return Math.max(0, Math.min(100, p));
  }

  function setText(root, field, value) {
    var el = root.querySelector('[data-field="' + field + '"]');
    if (el) el.textContent = value;
  }
  function setWidth(root, field, value) {
    var el = root.querySelector('[data-field="' + field + '"]');
    if (el) el.style.width = Math.max(0, Math.min(100, value)) + '%';
  }
  function setDot(root, on) {
    var el = root.querySelector('[data-field="dot"]');
    if (!el) return;
    el.classList.toggle('on', !!on);
    el.classList.toggle('off', !on);
  }

  // 把一份指标应用到页面上的对应节点区块
  function applyNode(el, m) {
    if (!m) { setDot(el, false); return; }
    setDot(el, true);
    var cpu = m.cpu ? m.cpu.usage : 0;
    var mem = m.mem ? pct(m.mem.used, m.mem.total) : 0;
    var disk = m.disk ? pct(m.disk.used, m.disk.total) : 0;
    setText(el, 'cpu', cpu.toFixed(1) + '%');
    setWidth(el, 'cpu-bar', cpu);
    setText(el, 'mem', mem.toFixed(0) + '%');
    setWidth(el, 'mem-bar', mem);
    setText(el, 'disk', disk.toFixed(0) + '%');
    setWidth(el, 'disk-bar', disk);
    if (m.net) {
      setText(el, 'up', fmtRate(m.net.up));
      setText(el, 'down', fmtRate(m.net.down));
    }
  }

  function applySnapshot(data) {
    var blocks = document.querySelectorAll('[data-node]');
    for (var i = 0; i < blocks.length; i++) {
      var id = blocks[i].getAttribute('data-node');
      applyNode(blocks[i], data[id]);
    }
    // 顶部在线数（首页）
    var onlineEl = document.querySelector('[data-field="online"]');
    if (onlineEl) {
      var n = 0;
      for (var k in data) if (data[k]) n++;
      onlineEl.textContent = String(n);
    }
  }

  // ---- 迷你曲线 ----
  function drawSpark(svg, values) {
    if (!svg || !values || values.length < 2) return;
    var w = 300, h = 60, pad = 2;
    var max = 0;
    for (var i = 0; i < values.length; i++) if (values[i] > max) max = values[i];
    if (max <= 0) max = 1;
    var step = (w - pad * 2) / (values.length - 1);
    var line = '', area = '';
    for (var j = 0; j < values.length; j++) {
      var x = (pad + j * step).toFixed(2);
      var y = (h - pad - (values[j] / max) * (h - pad * 2)).toFixed(2);
      line += (j === 0 ? 'M' : 'L') + x + ' ' + y + ' ';
    }
    area = line + 'L' + (w - pad) + ' ' + h + ' L' + pad + ' ' + h + ' Z';
    svg.innerHTML =
      '<path class="area" d="' + area + '"></path>' +
      '<path class="line" d="' + line + '"></path>';
  }

  function drawAllSparks() {
    var pts = window.__POINTS__ || [];
    if (!pts.length) return;
    var svgs = document.querySelectorAll('.spark');
    for (var i = 0; i < svgs.length; i++) {
      var kind = svgs[i].getAttribute('data-series');
      var vals = pts.map(function (p) {
        if (kind === 'cpu') return p.cpu || 0;
        if (kind === 'mem') return p.mu || 0;
        if (kind === 'up') return p.up || 0;
        return p.down || 0;
      });
      drawSpark(svgs[i], vals);
    }
  }

  // ---- 点赞 / 点踩 ----
  // 按钮上带 data-target(node|comment) / data-id / data-vote(up|down)。
  // 已经投过同一票再点一次就是撤票，服务端按「最后一票」记。
  function postVote(btn, vote) {
    var slug = window.__SLUG__ || (location.pathname.split('/')[2] || '');
    var body = new URLSearchParams();
    body.set('target', btn.getAttribute('data-target'));
    body.set('id', btn.getAttribute('data-id'));
    body.set('v', vote);
    body.set('json', '1');
    fetch('/n/' + slug + '/vote', {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded', 'X-Requested-With': 'XMLHttpRequest' },
      body: body.toString(),
      credentials: 'same-origin'
    }).then(function (r) { return r.json(); }).then(function (d) {
      if (!d || !d.ok) return;
      var wrap = btn.parentNode;
      var up = wrap.querySelector('.up b[data-count]');
      var down = wrap.querySelector('.down b[data-count]');
      // 评论的赞踩按钮各自独立，节点的两个按钮同在 .votebar 里
      if (btn.getAttribute('data-target') === 'node') {
        up = wrap.querySelector('.up b[data-count]');
        down = wrap.querySelector('.down b[data-count]');
      }
      if (up) up.textContent = d.up;
      if (down) down.textContent = d.down;
      var u = wrap.querySelector('.up'), dn = wrap.querySelector('.down');
      if (u) u.classList.toggle('on', d.mine > 0);
      if (dn) dn.classList.toggle('on', d.mine < 0);
      var sc = document.querySelector('.votebar .score');
      if (sc && btn.getAttribute('data-target') === 'node') sc.textContent = '净 ' + d.score;
    }).catch(function () { /* 失败就保持原样，用户可以整页刷新 */ });
  }

  function bindVotes() {
    var btns = document.querySelectorAll('.vote');
    for (var i = 0; i < btns.length; i++) {
      (function (btn) {
        btn.addEventListener('click', function () {
          var want = btn.getAttribute('data-vote');
          // 已经高亮说明投过这一票，再点 = 撤票
          postVote(btn, btn.classList.contains('on') ? 'cancel' : want);
        });
      })(btns[i]);
    }
  }

  // ---- 回复 ----
  function bindReply() {
    var links = document.querySelectorAll('[data-reply]');
    var field = document.getElementById('parent_id');
    var hint = document.getElementById('reply-hint');
    for (var i = 0; i < links.length; i++) {
      (function (a) {
        a.addEventListener('click', function () {
          if (!field) return;
          if (field.value === a.getAttribute('data-reply')) {
            field.value = '';
            if (hint) { hint.hidden = true; }
            return;
          }
          field.value = a.getAttribute('data-reply');
          if (hint) {
            hint.hidden = false;
            hint.textContent = '正在回复 ' + (a.getAttribute('data-author') || '某人') + '（再点一次取消）';
          }
          var ta = document.querySelector('.cform textarea');
          if (ta) ta.focus();
        });
      })(links[i]);
    }
  }

  // ---- 首页即时过滤 ----
  // 服务端 /search 负责全量检索；这里再补一层：列表页边打字边筛，不用回车。
  function bindFilter() {
    var box = document.querySelector('.search input');
    if (!box) return;
    var cards = document.querySelectorAll('.grid .card');
    if (!cards.length) return;
    box.addEventListener('input', function () {
      var q = box.value.trim().toLowerCase();
      for (var i = 0; i < cards.length; i++) {
        var hit = !q || cards[i].textContent.toLowerCase().indexOf(q) >= 0;
        cards[i].style.display = hit ? '' : 'none';
      }
    });
  }

  // ---- 启动 ----
  drawAllSparks();
  bindVotes();
  bindReply();
  bindFilter();

  if (typeof EventSource !== 'undefined') {
    var es = new EventSource('/api/v1/stream');
    es.addEventListener('metrics', function (ev) {
      var snap;
      try { snap = JSON.parse(ev.data); } catch (e) { return; /* 忽略坏帧 */ }
      applySnapshot(snap);
      // 让地球上的航线跑一个脉冲。用真实上报驱动，而不是自己定时假造——
      // 看到的光点就是"这台小鸡刚发来数据"这件事本身。
      var g = window.kokoroGlobe;
      if (g && snap) {
        for (var id in snap) {
          if (Object.prototype.hasOwnProperty.call(snap, id)) g.pulse(id);
        }
      }
    });
    es.addEventListener('node_offline', function (ev) {
      try {
        var d = JSON.parse(ev.data);
        var el = document.querySelector('[data-node="' + d.id + '"]');
        if (el) setDot(el, false);
      } catch (e) { /* 忽略 */ }
    });
    es.addEventListener('node_online', function (ev) {
      try {
        var d = JSON.parse(ev.data);
        var el = document.querySelector('[data-node="' + d.id + '"]');
        if (el) setDot(el, true);
      } catch (e) { /* 忽略 */ }
    });
  }
})();

/* ---- 主题：深浅色切换 + 列表形态切换 ----
   独立 IIFE，不依赖上面那个。这样即便 SSE 那段抛错，主题切换依然可用。
   两个偏好都存 cookie 交给服务端渲染——这样首屏就是对的，不会闪一下再变色。 */
(function () {
  'use strict';

  function setCookie(name, val, days) {
    var d = new Date();
    d.setTime(d.getTime() + days * 864e5);
    document.cookie = name + '=' + encodeURIComponent(val) +
      ';expires=' + d.toUTCString() + ';path=/;SameSite=Lax';
  }
  function getCookie(name) {
    var m = document.cookie.match(new RegExp('(?:^|;\\s*)' + name + '=([^;]*)'));
    return m ? decodeURIComponent(m[1]) : '';
  }

  /* 深浅色：改 html 的 data-k-mode，CSS 变量随之切换；同时记进 cookie，
     下次 SSR 直接输出目标模式，不需要闪烁。 */
  var toggle = document.querySelector('[data-toggle-mode]');
  if (toggle) {
    toggle.addEventListener('click', function () {
      var cur = document.documentElement.getAttribute('data-k-mode') || 'light';
      var next = cur === 'dark' ? 'light' : 'dark';
      document.documentElement.setAttribute('data-k-mode', next);
      setCookie('k_mode', next, 365);
      toggle.textContent = next === 'dark' ? '☀' : '☾';
      toggle.setAttribute('aria-label', next === 'dark' ? '切到浅色' : '切到深色');
    });
  }

  /* 列表形态：改 body 的 data-k-list-mode（纯 CSS 切换，无需刷新），
     同时写 cookie，让刷新后仍是同一形态。 */
  var modeBtns = document.querySelectorAll('[data-set-list]');
  for (var i = 0; i < modeBtns.length; i++) {
    modeBtns[i].addEventListener('click', function (ev) {
      var mode = ev.currentTarget.getAttribute('data-set-list');
      document.body.setAttribute('data-k-list-mode', mode);
      setCookie('k_list_mode', mode, 365);
      for (var j = 0; j < modeBtns.length; j++) {
        modeBtns[j].classList.toggle('on',
          modeBtns[j].getAttribute('data-set-list') === mode);
      }
    });
  }

  /* 皮肤切换现在是访客级权限：直接走链接 /pick/<id>，服务端写 cookie 后 303 回原页。
     所以这里不需要任何拦截，只需要：
       1. 乐观地把高亮挪到刚点的那个选项上（免得点了要等整页跳转才看到变化）；
       2. 让 <details> 下拉在点外面或按 Esc 时收起——
          原生 <details> 只在点 summary 时开合，不点它就一直挂着。 */
  var picker = document.querySelector('[data-skin-picker]');
  if (picker) {
    var items = picker.querySelectorAll('a[href^="/pick/"]');
    for (var k = 0; k < items.length; k++) {
      items[k].addEventListener('click', function () {
        var href = this.getAttribute('href');
        for (var j = 0; j < items.length; j++) {
          // /pick/default 是"恢复默认"，不该被点亮成"当前皮肤"。
          items[j].classList.toggle('on',
            items[j].getAttribute('href') === href && href.indexOf('/pick/default') < 0);
        }
      });
    }
    document.addEventListener('click', function (ev) {
      if (!picker.contains(ev.target)) picker.removeAttribute('open');
    });
    document.addEventListener('keydown', function (ev) {
      if (ev.key === 'Escape') picker.removeAttribute('open');
    });
  }
})();

/* ---- 时钟条 ----
   只做一件事：每秒把 .clock 里的时间格式化一遍。
   时区交给 Intl 处理，所以不用管夏令时——欧洲/北美一年换两次，
   自己算偏移一定会错。

   独立 IIFE：即便上面主题或 SSE 那段抛错，时钟依然会走。 */
(function () {
  'use strict';

  // 主机时钟用 data-offset（固定 UTC 偏移），多时区时钟用 data-tz（IANA 名）。
  // 两者都要认——服务端不一定设了 IANA 时区，主机时间只能靠偏移算。
  var cells = document.querySelectorAll('.hub-clock[data-offset], .hub-clock[data-tz], .clock[data-tz], .clock');
  if (!cells.length) return;

  function pad(n) { return n < 10 ? '0' + n : '' + n; }

  function tick() {
    var now = new Date();
    for (var i = 0; i < cells.length; i++) {
      var el = cells[i];
      var tEl = el.querySelector('.t');
      var dEl = el.querySelector('.d');
      if (!tEl) continue;
      var tz = el.getAttribute('data-tz');
      var offAttr = el.getAttribute('data-offset');
      var h, m, sec, dateStr;
      if (offAttr !== null && offAttr !== '') {
        // 固定偏移：把本地时间平移到目标时区，再按 UTC 取字段。
        // 不能用 Intl 的 Etc/GMT±X —— 那个符号是反的，很容易搞错。
        var shifted = new Date(now.getTime() + (+offAttr) * 60000);
        h = shifted.getUTCHours(); m = shifted.getUTCMinutes(); sec = shifted.getUTCSeconds();
        dateStr = shifted.getUTCFullYear() + '-' + pad(shifted.getUTCMonth() + 1) + '-' + pad(shifted.getUTCDate());
      } else if (!tz) {
        h = now.getHours(); m = now.getMinutes(); sec = now.getSeconds();
        dateStr = now.getFullYear() + '-' + pad(now.getMonth() + 1) + '-' + pad(now.getDate());
      } else {
        var parts = new Intl.DateTimeFormat('en-GB', {
          timeZone: tz, hour12: false,
          hour: '2-digit', minute: '2-digit', second: '2-digit',
          year: 'numeric', month: '2-digit', day: '2-digit'
        }).formatToParts(now).reduce(function (o, x) { o[x.type] = x.value; return o; }, {});
        h = +parts.hour; m = +parts.minute; sec = +parts.second;
        dateStr = parts.year + '-' + parts.month + '-' + parts.day;
      }
      tEl.textContent = pad(h) + ':' + pad(m) + ':' + pad(sec);
      if (dEl) dEl.textContent = dateStr;
    }
  }

  tick();
  setInterval(tick, 1000);
})();
