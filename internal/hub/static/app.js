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
    // 文字也要跟着变 —— 光换颜色的话，一台机器掉线后
    // 标签变红却还写着"在线"，比不更新更让人困惑。
    var t = el.querySelector('[data-field="dottext"]');
    if (t) t.textContent = on ? '在线' : '离线';
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
    // slug 优先取按钮自己带的 data-slug。
    // 详情页整页只有一个节点、能从路径拿；**首页一屏几十张卡片**，
    // 路径是 "/" 拿不到 —— 必须每张卡各带一个。
    var slug = btn.getAttribute('data-slug') ||
               window.__SLUG__ || (location.pathname.split('/')[2] || '');
    if (!slug) return;
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
      // 容器按两种布局找：详情页 .votebar、首页卡片 .actbar。
      // 不写死其中一个，否则换一处布局这功能就静默失效。
      var box = btn.closest('.votebar, .actbar') || btn.parentNode;
      var u = box.querySelector('[data-vote="up"]');
      var dn = box.querySelector('[data-vote="down"]');
      function setNum(el, v) {
        if (!el) return;
        var n = el.querySelector('b[data-count], .n');
        if (n) n.textContent = v;
      }
      setNum(u, d.up);
      setNum(dn, d.down);
      if (u) u.classList.toggle('on', d.mine > 0);
      if (dn) dn.classList.toggle('on', d.mine < 0);
      var sc = box.querySelector('.score');
      if (sc) sc.textContent = '净 ' + d.score;
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

/* ---- 文章弹窗 ----
   列表里只有标题+摘要，点开才显示全文。
   用原生 <dialog>：焦点圈定、ESC 关闭、背景滚动锁定都是浏览器给的，
   比自己搭遮罩层稳。 */
(function () {
  var list = document.querySelector('.art-list');
  if (!list) return;

  list.addEventListener('click', function (e) {
    var btn = e.target.closest('.art-item');
    if (!btn) return;
    var dlg = document.getElementById('art-' + btn.dataset.art);
    if (!dlg) return;
    if (typeof dlg.showModal === 'function') {
      dlg.showModal();
    } else {
      dlg.setAttribute('open', '');  // 老浏览器兜底
    }
  });

  // 关闭按钮 + 点遮罩关闭。
  // 点遮罩：<dialog> 本身占满整个对话框区域，所以 e.target === dlg
  // 就说明点的是空白处（内容都在子元素里）。
  document.querySelectorAll('.art-dlg').forEach(function (dlg) {
    dlg.addEventListener('click', function (e) {
      if (e.target === dlg) dlg.close();
    });
    var x = dlg.querySelector('.art-close');
    if (x) x.addEventListener('click', function () { dlg.close(); });
  });
})();

/* ---- 全网上下行速率折线 ----
   仪表盘和首页共用（首页的「全网速率」卡片下面也要一张波动图）。
   数据是 [{"t":ts,"up":B/s,"down":B/s}, ...]，1 分钟一个点。

   用 SVG 手画而不是引图表库：全站零依赖、无 CDN，一个折线不值得
   为此加 100KB 的库。viewBox 固定 600x180，靠 preserveAspectRatio
   拉伸适配 —— 所以坐标算完不用管实际像素宽。 */
function renderRateChart(svg, pts) {
  if (!svg || !pts || pts.length < 2) return;

  var W = 600, H = 180, pad = 8, i, x, y;
  var max = 0;
  for (i = 0; i < pts.length; i++) {
    if (pts[i].up > max) max = pts[i].up;
    if (pts[i].down > max) max = pts[i].down;
  }
  if (max <= 0) max = 1;

  function pathOf(key) {
    var d = '', step = (W - pad * 2) / (pts.length - 1);
    for (var k = 0; k < pts.length; k++) {
      x = (pad + k * step).toFixed(1);
      y = (H - pad - (pts[k][key] / max) * (H - pad * 2)).toFixed(1);
      d += (k === 0 ? 'M' : 'L') + x + ' ' + y + ' ';
    }
    return d;
  }
  var up = pathOf('up'), down = pathOf('down');

  // 三条等距横线当刻度：1/4 / 1/2 / 3/4 高度
  var grid = '';
  for (i = 1; i <= 3; i++) {
    y = (H - pad - (H - pad * 2) * i / 4).toFixed(1);
    grid += '<line class="dash-grid-line" x1="' + pad + '" y1="' + y +
            '" x2="' + (W - pad) + '" y2="' + y + '"/>';
  }
  svg.innerHTML = grid +
    '<path class="dash-area-up" d="M' + pad + ' ' + (H - pad) + ' L' + up.slice(1) +
      ' L' + (W - pad) + ' ' + (H - pad) + ' Z"/>' +
    '<path class="dash-line-up" d="' + up + '"/>' +
    '<path class="dash-line-down" d="' + down + '"/>';
}

/* 页面上所有 [data-rate-chart] 用同一份数据画 ——
   数据放在 <script id="rate-data" type="application/json"> 里。 */
(function () {
  var el = document.getElementById('rate-data');
  if (!el) return;
  var pts;
  try { pts = JSON.parse(el.textContent); } catch (e) { return; }
  document.querySelectorAll('[data-rate-chart]').forEach(function (svg) {
    renderRateChart(svg, pts);
  });
})();

/* ---- 侧边栏：文章 / 评论（两个**独立**面板）----
   点卡片上的「文章 N」出来文章栏，点「评论 N」出来评论栏。
   默认收起，点遮罩 / ESC / ✕ 收起。数据按需拉。 */
(function () {
  var scrim = document.getElementById('drawer-scrim');
  var panels = {
    art: { el: document.getElementById('d-art'),
           body: document.getElementById('d-art-body'),
           admin: document.getElementById('d-art-admin'), foot: null },
    cmt: { el: document.getElementById('d-cmt'),
           body: document.getElementById('d-cmt-body'),
           admin: document.getElementById('d-cmt-admin'),
           foot: document.getElementById('d-cmt-foot') }
  };
  if (!scrim || !panels.art.el) return;

  var st = { slug: '', kind: 'art', page: 1, expanded: false, cur: null };

  function esc(s) {
    return String(s == null ? '' : s)
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
  }

  function open(slug, kind) {
    st.slug = slug;
    st.kind = kind === 'cmt' ? 'cmt' : 'art';
    st.page = 1;
    st.expanded = false;
    st.cur = panels[st.kind];
    Object.keys(panels).forEach(function (k) { panels[k].el.hidden = true; });
    st.cur.el.hidden = false;
    st.cur.el.setAttribute('aria-hidden', 'false');
    scrim.hidden = false;
    document.body.style.overflow = 'hidden';  // 栏开着时锁住背景滚动
    load();
  }

  function close() {
    if (!st.cur) return;
    var el = st.cur.el;
    el.classList.add('closing');
    scrim.classList.add('closing');
    setTimeout(function () {
      el.hidden = true;
      scrim.hidden = true;
      el.classList.remove('closing');
      scrim.classList.remove('closing');
      el.setAttribute('aria-hidden', 'true');
      document.body.style.overflow = '';
      st.cur = null;
    }, 180);
  }

  function load() {
    var cur = st.cur;
    if (!cur) return;
    cur.body.innerHTML = '<p class="d-empty">加载中…</p>';
    if (cur.foot) cur.foot.innerHTML = '';
    // ⚠️ 面板 key（art/cmt）和接口路径（articles/comments）**不是一回事**，
    // 必须查表 —— 直接拼 st.kind 会请求 /n/x/art.json，那个端点是 404。
    var path = st.kind === 'art' ? 'articles' : 'comments';
    var url = '/n/' + encodeURIComponent(st.slug) + '/' + path + '.json?page=' + st.page;
    fetch(url, { credentials: 'same-origin' })
      .then(function (r) { return r.json(); })
      .then(function (d) {
        if (!d || !d.ok) { cur.body.innerHTML = '<p class="d-empty">加载失败</p>'; return; }
        renderAdmin(cur, d);
        if (st.kind === 'art') renderArticles(cur, d); else renderComments(cur, d);
      })
      .catch(function () { cur.body.innerHTML = '<p class="d-empty">加载失败</p>'; });
  }

  function renderAdmin(cur, d) {
    cur.admin.innerHTML = '';
    if (!d.is_admin) return;   // 访客不显示任何管理入口
    var href = st.kind === 'art' ? d.edit_url : d.manage_url;
    if (!href) return;
    var a = document.createElement('a');
    a.className = 'btn small ghost';
    a.href = href;
    a.textContent = st.kind === 'art' ? (d.total ? '编辑文章' : '写文章') : '管理评论';
    cur.admin.appendChild(a);
  }

  function renderArticles(cur, d) {
    if (!d.articles || !d.articles.length) {
      cur.body.innerHTML = '<p class="d-empty">无文章</p>';
      return;
    }
    var a = d.articles[0];
    // HTML 是服务端渲染好的 Markdown（已消毒），直接插入
    cur.body.innerHTML = '<article class="d-art">' +
      '<h3>' + esc(a.title) + '</h3>' +
      '<p class="d-when">' + esc(a.when) + '</p>' +
      '<div class="md">' + a.html + '</div></article>';
  }

  function renderComments(cur, d) {
    if (!d.comments || !d.comments.length) {
      cur.body.innerHTML = '<p class="d-empty">还没有评论。</p>';
      return;
    }
    // 默认只显示第一条，其余折叠 —— 评论一展开就占满整屏，
    // 访客多数只想扫一眼最新那条。
    var show = st.expanded ? d.comments : d.comments.slice(0, 1);
    var html = '';
    show.forEach(function (c) {
      html += '<div class="dcmt">' +
        '<div class="dcmt-head"><b>' + esc(c.author) + '</b><span>' + esc(c.when) + '</span></div>' +
        '<div class="dcmt-body">' + c.html + '</div></div>';
    });
    if (d.comments.length > 1) {
      html += '<button type="button" class="d-expand" data-dexpand>' +
        (st.expanded ? '收起评论' : '展开本页其余 ' + (d.comments.length - 1) + ' 条') +
        '</button>';
    }
    cur.body.innerHTML = html;
    var ex = cur.body.querySelector('[data-dexpand]');
    if (ex) ex.addEventListener('click', function () {
      st.expanded = !st.expanded;
      renderComments(cur, d);
    });
    renderFoot(cur, d);
  }

  function renderFoot(cur, d) {
    if (!cur.foot) return;
    cur.foot.innerHTML = '';
    if (!d.pages || d.pages <= 1) return;
    var prev = document.createElement('button');
    prev.type = 'button'; prev.className = 'btn small ghost'; prev.textContent = '上一页';
    prev.disabled = st.page <= 1;
    prev.addEventListener('click', function () {
      if (st.page > 1) { st.page--; st.expanded = false; load(); }
    });
    var next = document.createElement('button');
    next.type = 'button'; next.className = 'btn small ghost'; next.textContent = '下一页';
    next.disabled = st.page >= d.pages;
    next.addEventListener('click', function () {
      if (st.page < d.pages) { st.page++; st.expanded = false; load(); }
    });
    var info = document.createElement('span');
    info.className = 'hint';
    info.textContent = '第 ' + st.page + ' / ' + d.pages + ' 页 · 共 ' + d.total + ' 条';
    cur.foot.appendChild(prev); cur.foot.appendChild(next); cur.foot.appendChild(info);
  }

  // 卡片上的入口
  document.addEventListener('click', function (e) {
    var btn = e.target.closest('[data-drawer]');
    if (btn) { open(btn.dataset.drawer, btn.dataset.panel); return; }
    if (e.target.closest('[data-dclose]')) { close(); }
  });

  scrim.addEventListener('click', close);
  document.addEventListener('keydown', function (e) {
    if (e.key === 'Escape' && st.cur) close();
  });
})();
/* ---- 后台选项卡 ----
   走 location.hash，所以老链接（#profile / #comments …）仍然能直达某一页。
   ⚠️ 直接用 hidden 属性而不是 class：这些面板里有表单，
   藏起来的那些不该能被 Tab 键聚焦到。 */
(function () {
  var nav = document.querySelector('.atabs');
  if (!nav) return;
  var panels = Array.prototype.slice.call(
    document.querySelectorAll('.admin > .panel[data-atab]'));
  var tabs = Array.prototype.slice.call(nav.querySelectorAll('.atab'));
  if (!panels.length) return;

  var DEFAULT = 'nodes';

  // hash 既可能是页签名（#nodes），也可能是某个区块的 id（#profile 是两者同名，
  // #hubgeo / #theme-fetch / #account / #hostself 则是区块 id）。
  // 两种都认，免得老链接失效。
  function tabOfHash() {
    var h = (location.hash || '').replace(/^#/, '');
    if (!h) return DEFAULT;
    for (var i = 0; i < tabs.length; i++) {
      if (tabs[i].dataset.goto === h) return h;
    }
    var el = document.getElementById(h);
    if (el && el.dataset && el.dataset.atab) return el.dataset.atab;
    return DEFAULT;
  }

  function show(tab, scrollTo) {
    panels.forEach(function (pn) {
      pn.classList.toggle('on', pn.dataset.atab === tab);
    });
    tabs.forEach(function (t) {
      t.classList.toggle('on', t.dataset.goto === tab);
      t.setAttribute('aria-selected', t.dataset.goto === tab ? 'true' : 'false');
    });
    if (scrollTo) {
      var el = document.getElementById(scrollTo);
      if (el && el.dataset.atab === tab) el.scrollIntoView({ block: 'start' });
    }
  }

  function apply() {
    var h = (location.hash || '').replace(/^#/, '');
    var tab = tabOfHash();
    show(tab, h);
  }

  tabs.forEach(function (t) {
    t.addEventListener('click', function () {
      // 换页签时清掉 hash 里的区块锚点，免得刚切过来又被滚走
      history.replaceState(null, '', '#' + t.dataset.goto);
      show(t.dataset.goto, null);
    });
  });

  window.addEventListener('hashchange', apply);
  apply();
})();
