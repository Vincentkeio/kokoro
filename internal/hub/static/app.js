// Theme chart geometry shared by live, server-rendered and detail histories.
function kokoroChartPath(points, type, smooth) {
  if (!points.length) return '';
  function num(v) { return Number(v).toFixed(2); }
  var d='M'+num(points[0][0])+' '+num(points[0][1]);
  for (var i=1;i<points.length;i++) {
    var a=points[i-1], b=points[i];
    if (type==='step') { d+=' H'+num(b[0])+' V'+num(b[1]); continue; }
    if (smooth) {
      var before=points[Math.max(0,i-2)], after=points[Math.min(points.length-1,i+1)];
      var lo=Math.min(a[1],b[1]), hi=Math.max(a[1],b[1]);
      var c1=Math.max(lo,Math.min(hi,a[1]+(b[1]-before[1])/6));
      var c2=Math.max(lo,Math.min(hi,b[1]-(after[1]-a[1])/6));
      d+=' C'+num(a[0]+(b[0]-a[0])/3)+' '+num(c1)+' '+num(b[0]-(b[0]-a[0])/3)+' '+num(c2)+' '+num(b[0])+' '+num(b[1]);
    } else d+=' L'+num(b[0])+' '+num(b[1]);
  }
  return d;
}
// Every connected trace uses a cubic curve. Discrete theme types (bars, dots,
// steps) have their own rendering paths and never reach this curve helper.
function kokoroSmoothCharts() { return true; }
function fitKokoroChartMarkers(svg) {
  if(!svg || !svg.viewBox || !svg.viewBox.baseVal) return;
  var box=svg.viewBox.baseVal,rect=svg.getBoundingClientRect();
  if(!rect.width||!rect.height) return;
  svg.querySelectorAll('ellipse[data-screen-radius]').forEach(function(point){
    var radius=parseFloat(point.getAttribute('data-screen-radius'))||0;
    point.setAttribute('rx',(radius*box.width/rect.width).toFixed(2));
    point.setAttribute('ry',(radius*box.height/rect.height).toFixed(2));
  });
}
window.addEventListener('resize',function(){document.querySelectorAll('.rate-chart,.spark').forEach(fitKokoroChartMarkers);});

/* Kokoro 前端：只做两件事 —— 订阅 SSE 更新数值，画迷你曲线。
   不引任何框架，保持零 CDN 依赖，离线也能用。 */
(function () {
  'use strict';

  var selectedTests = document.getElementById('node-selected-tests');
  if (selectedTests) {
    var nodeChecks = Array.prototype.slice.call(document.querySelectorAll('[data-node-select-item]'));
    var masterCheck = document.querySelector('[data-node-select-all]');
    var selectedButton = selectedTests.querySelector('[data-test-selected]');
    var selectedCount = selectedTests.querySelector('[data-node-selection-count]');
    function updateNodeSelection() {
      var count = nodeChecks.filter(function (box) { return box.checked; }).length;
      selectedButton.disabled = count === 0;
      selectedCount.textContent = '已选 ' + count + ' / ' + nodeChecks.length + ' 台';
      if (masterCheck) {
        masterCheck.checked = nodeChecks.length > 0 && count === nodeChecks.length;
        masterCheck.indeterminate = count > 0 && count < nodeChecks.length;
      }
    }
    nodeChecks.forEach(function (box) { box.addEventListener('change', updateNodeSelection); });
    selectedTests.querySelectorAll('[data-node-select]').forEach(function (button) {
      button.addEventListener('click', function () {
        var action = button.getAttribute('data-node-select');
        nodeChecks.forEach(function (box) { box.checked = action === 'invert' ? !box.checked : action === 'all'; });
        updateNodeSelection();
      });
    });
    if (masterCheck) masterCheck.addEventListener('change', function () {
      nodeChecks.forEach(function (box) { box.checked = masterCheck.checked; });
      updateNodeSelection();
    });
    selectedTests.addEventListener('submit', function (event) {
      if (!nodeChecks.some(function (box) { return box.checked; })) event.preventDefault();
    });
    updateNodeSelection();
    window.addEventListener('pageshow', updateNodeSelection);
  }

  // Fit every card text region to its allocated width, including live values.
  var cardTextSelector='.card-head h2, .meta, .status, .tag, .specs dt, .specs dd .spec-value, .gauges .gl, .gauges em, .spark-cap span, .net-num > span, .net-num .lat em, .uptime-head, .uptime-head h4, .uptime-pct, .uptime-sub, .uptime-empty span, .uptime-empty b, .task-line, .task-result-label, .task-result-value, .netcard .nl, .netcard .nv, .nqr .nm, .nqr .ms, .nqr .ls, .card-actions .btn, .card-actions .act, .vote-actions .act, .netq-empty span, .netq-empty b';
  function fitTextRegions(textRegions) {
    textRegions.forEach(function(el){el.style.fontSize='';});
    var sizes = textRegions.map(function(el) {
      var width=el.clientWidth, contentWidth=el.scrollWidth;
      if(width && contentWidth>width) {
        var size=parseFloat(getComputedStyle(el).fontSize);
        return Math.max(9,size*Math.max(0,width-1)/contentWidth).toFixed(2)+'px';
      }
      return '';
    });
    textRegions.forEach(function(el, i) {
      if (sizes[i]) el.style.fontSize = sizes[i];
    });
  }
  function fitCardText(card) {
    if(!card || !card.isConnected) return;
    fitTextRegions(Array.from(card.querySelectorAll(cardTextSelector)));
  }
  function fitSwapValues() {
    document.querySelectorAll('.card').forEach(fitCardText);
  }
  fitSwapValues(); window.addEventListener('resize',fitSwapValues);
  if(document.fonts && document.fonts.ready) document.fonts.ready.then(fitSwapValues);

  // 只重算尺寸或内容变化的那张卡，并把同一帧内的变化合并起来。
  // 之前每张卡里的每个 ResizeObserver / MutationObserver 都会扫描整页所有卡，
  // 卡片多时形成近似 N² 的布局测量，换主题和 SSE 更新时尤其明显。
  var pendingCardFits=new Set(), pendingTextFits=new Set(), cardFitFrame=0;
  function queueCardTextFit(card, region) {
    if(!card || !card.isConnected) return;
    if (region) pendingTextFits.add(region); else pendingCardFits.add(card);
    if(cardFitFrame) return;
    cardFitFrame=requestAnimationFrame(function(){
      cardFitFrame=0;
      pendingCardFits.forEach(function (item) {
        item.querySelectorAll(cardTextSelector).forEach(function (el) { pendingTextFits.add(el); });
      });
      var regions=Array.from(pendingTextFits).filter(function (el) { return el.isConnected; });
      pendingCardFits.clear();
      pendingTextFits.clear();
      fitTextRegions(regions);
    });
  }
  if(window.ResizeObserver) {
    var swapObserver=new ResizeObserver(function(entries){
      entries.forEach(function(entry){queueCardTextFit(entry.target);});
    });
    document.querySelectorAll('.card').forEach(function(card){swapObserver.observe(card);});
  }
  // SSE 更新卡片内容时，仅复测对应卡片。
  if(window.MutationObserver){
    document.querySelectorAll('.card').forEach(function(card){
      var cardTextObserver=new MutationObserver(function(records){
        records.forEach(function (record) {
          var target = record.target.nodeType === 1 ? record.target : record.target.parentElement;
          var region = target && target.closest(cardTextSelector);
          queueCardTextFit(card, region && card.contains(region) ? region : null);
        });
      });
      cardTextObserver.observe(card,{childList:true,characterData:true,subtree:true});
    });
  }

  // Fit benchmark summaries to their available width without allowing a second line.
  var benchSums = Array.prototype.slice.call(document.querySelectorAll('.bench-sum'));
  if (benchSums.length) {
    function fitBenchSum(el) {
      el.style.fontSize = '12px';
      var width=el.clientWidth, contentWidth=el.scrollWidth;
      if (width && contentWidth > width) {
        el.style.fontSize = Math.max(9,12*width/contentWidth).toFixed(2) + 'px';
      }
    }
    function fitBenchSums() { benchSums.forEach(fitBenchSum); }
    fitBenchSums();
    window.addEventListener('resize', fitBenchSums);
    if (window.ResizeObserver) {
      var benchResize = new ResizeObserver(function(entries){
        entries.forEach(function(entry){
          var items=entry.target.querySelectorAll('.bench-sum');
          if(entry.target.matches('.bench-sum')) fitBenchSum(entry.target);
          else Array.prototype.forEach.call(items,fitBenchSum);
        });
      });
      benchSums.forEach(function (el) { benchResize.observe(el.parentElement); });
    }
  }

  // 首页卡片密度是访客偏好，按组共享并保存在本机浏览器。
  var cardColumns = document.querySelector('[data-card-columns]');
  if (cardColumns) {
    var allowedColumns = { '3': true, '4': true, '5': true };
    var savedColumns = '';
    try { savedColumns = localStorage.getItem('kokoro.cardColumns') || ''; } catch (_) {}
    var initialColumns = allowedColumns[savedColumns] ? savedColumns : '3';
    function applyCardColumns(value) {
      if (!allowedColumns[value]) value = '3';
      document.body.setAttribute('data-k-card-columns', value);
      document.body.style.setProperty('--k-card-columns', value);
      cardColumns.value = value;
      window.requestAnimationFrame(function () { window.dispatchEvent(new Event('resize')); });
    }
    applyCardColumns(initialColumns);
    cardColumns.addEventListener('change', function () {
      var value = cardColumns.value;
      if (!allowedColumns[value]) return;
      try { localStorage.setItem('kokoro.cardColumns', value); } catch (_) {}
      applyCardColumns(value);
    });
  }

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
    if (el && el.textContent !== value) el.textContent = value;
  }
  function setWidth(root, field, value) {
    var el = root.querySelector('[data-field="' + field + '"]');
    var width = Math.max(0, Math.min(100, value)) + '%';
    if (el && el.style.width !== width) el.style.width = width;
  }
  function setUsageLevel(root, field, value) {
    var level = value >= 85 ? 'bad' : value >= 65 ? 'warn' : 'ok';
    var bar = root.querySelector('[data-field="' + field + '-bar"]');
    var label = root.querySelector('[data-field="' + field + '"]');
    [bar, label].forEach(function (el) {
      if (!el || el.classList.contains('lv-' + level)) return;
      el.classList.remove('lv-ok', 'lv-warn', 'lv-bad');
      el.classList.add('lv-' + level);
    });
  }
  function setDot(root, on) {
    var el = root.querySelector('[data-field="dot"]');
    if (!el) return;
    var wasOn = el.classList.contains('on');
    el.classList.toggle('on', !!on);
    el.classList.toggle('off', !on);
    // 文字也要跟着变 —— 光换颜色的话，一台机器掉线后
    // 标签变红却还写着"在线"，比不更新更让人困惑。
    var t = el.querySelector('[data-field="dottext"]');
    if (t) t.textContent = on ? '在线' : '离线';
    var nodeID = root.getAttribute('data-node');
    if (nodeID) {
      var entry = document.querySelector('.globe-node-entry[data-node-key="' + CSS.escape(nodeID) + '"] .globe-node-status');
      if (entry) { entry.textContent = on ? '在线' : '离线'; entry.classList.toggle('is-online',!!on); }
      var globe = window.kokoroGlobe;
      if (globe) {
        var changed = false;
        globe.places.forEach(function(place){ if (place.key === nodeID && place.online !== !!on) { place.online=!!on; changed=true; } });
        if (changed) globe.setPlaces({places:globe.places});
      }
    }
    if (!on) updateIPConnectivity(root, null, false);
    if (wasOn !== !!on) {
      var onlineEl = document.querySelector('[data-field="online"]');
      if (onlineEl) {
        var count = parseInt(onlineEl.textContent, 10);
        if (!isFinite(count)) count = 0;
        onlineEl.textContent = String(Math.max(0, count + (on ? 1 : -1)));
      }
      var offlineEl = document.querySelector('[data-field="offline"]');
      if (offlineEl) offlineEl.textContent = String(Math.max(0, (parseInt(offlineEl.textContent,10)||0) + (on ? -1 : 1)));
    }
  }

  // 把一份指标应用到页面上的对应节点区块
  function applyNode(el, m) {
    if (!m) return;
    var status = el.querySelector('[data-field="dot"]');
    updateIPConnectivity(el, m.connectivity, !status || status.classList.contains('on'));
    var cpu = m.cpu ? m.cpu.usage : 0;
    var mem = m.mem ? pct(m.mem.used, m.mem.total) : 0;
    var disk = m.disk ? pct(m.disk.used, m.disk.total) : 0;
    setText(el, 'cpu', cpu.toFixed(1) + '%');
    setWidth(el, 'cpu-bar', cpu);
    setUsageLevel(el, 'cpu', cpu);
    setText(el, 'mem', mem.toFixed(0) + '%');
    setWidth(el, 'mem-bar', mem);
    setUsageLevel(el, 'mem', mem);
    setText(el, 'disk', disk.toFixed(0) + '%');
    setWidth(el, 'disk-bar', disk);
    setUsageLevel(el, 'disk', disk);
    if (m.mem) {
      var swapTotal = Number(m.mem.swap_total) || 0;
      var swapUsed = Number(m.mem.swap_used) || 0;
      setText(el, 'swap', swapTotal > 0 ? fmtSize(swapUsed) + ' / ' + fmtSize(swapTotal) : '无');
    }
    if (m.net) {
      setText(el, 'up', fmtRate(m.net.up));
      setText(el, 'down', fmtRate(m.net.down));
    }
  }

  function updateIPConnectivity(root, probe, online) {
    root.querySelectorAll('[data-ip-family]').forEach(function(indicator){
      var fresh = probe && probe.checked_at > 0 && Date.now()-probe.checked_at < 180000;
      var ok = fresh && probe[indicator.dataset.ipFamily.toLowerCase()];
      var state = online && fresh ? (ok ? 'online' : 'unreachable') : 'unknown';
      var className = 'ip-indicator is-' + state;
      if (indicator.className !== className) indicator.className = className;
      var text = !online ? '离线' : !probe ? '未探测' : !fresh ? '待更新' : ok ? '连通' : '无响应';
      var label = indicator.querySelector('span');
      if (label.textContent !== text) label.textContent = text;
    });
  }

  function applySnapshot(data) {
    var blocks = document.querySelectorAll('[data-node]');
    for (var i = 0; i < blocks.length; i++) {
      var id = blocks[i].getAttribute('data-node');
      applyNode(blocks[i], data[id]);
    }
  }

  // ---- 详情页历史趋势图 ----
  // 详情页曲线是“读数”用途：永远画面积填充 + 网格刻度，主题只影响配色与平滑。
  // （首页卡片的小图不从这里走，仍按主题的图表类型渲染。）
  function niceCeil(v) {
    if (!(v > 0)) return 1;
    var base = Math.pow(10, Math.floor(Math.log10(v)));
    var steps = [1, 2, 4, 8];
    for (var i = 0; i < steps.length; i++) {
      if (steps[i] * base >= v) return steps[i] * base;
    }
    return 10 * base;
  }

  function fmtAxisValue(v, unit) {
    if (unit === 'pct') return Math.round(v) + '';
    return fmtSize(v);
  }

  // __POINTS__ 的 t 是 Unix 毫秒（服务端直接塞 store 的 ts），历史上有别处
  // 按秒传过 —— 用 1e12 归一化，两种都能画对。spanSec 是秒。
  function fmtTimeLabel(ts, spanSec) {
    var ms = ts > 1e12 ? ts : ts * 1000;
    var d = new Date(ms);
    function two(n) { return (n < 10 ? '0' : '') + n; }
    if (spanSec <= 48 * 3600) return two(d.getHours()) + ':' + two(d.getMinutes());
    return two(d.getMonth() + 1) + '-' + two(d.getDate());
  }

  function drawSpark(svg, values, unit) {
    if (!svg || !values || values.length < 2) return;
    var w = 300, h = 60, pad = 2;
    var dataMax = 0;
    for (var i = 0; i < values.length; i++) if (values[i] > dataMax) dataMax = values[i];
    // CPU 固定 0-100 刻度；其余序列向上取整到 1/2/4/8×10^k，保证四等分刻度都是干净数字。
    var scaleMax = unit === 'pct' ? 100 : niceCeil(dataMax);
    var step = (w - pad * 2) / (values.length - 1);
    var curvePoints = [];
    for (var j = 0; j < values.length; j++) {
      var v = Math.min(values[j], scaleMax);
      curvePoints.push([pad + j * step, h - pad - (v / scaleMax) * (h - pad * 2)]);
    }
    var line = kokoroChartPath(curvePoints, 'line', kokoroSmoothCharts());
    var area = line + ' L' + (w - pad) + ' ' + h + ' L' + pad + ' ' + h + ' Z';
    var inner = '';
    for (var g = 0; g <= 4; g++) {
      var gy = pad + (h - pad * 2) * g / 4;
      inner += '<line class="grid" x1="0" y1="' + gy.toFixed(2) + '" x2="' + w + '" y2="' + gy.toFixed(2) + '" vector-effect="non-scaling-stroke"></line>';
    }
    inner += '<path class="area" d="' + area + '"></path><path class="line" d="' + line + '"></path>';
    svg.innerHTML = inner;
    drawChartAxes(svg, scaleMax, unit);
  }

  // 刻度文字放在 SVG 外的 HTML 轨道里：图表是 preserveAspectRatio="none" 拉伸渲染，
  // SVG 内文字会跟着变形。轨道里的 top 百分比必须与上面 w/h/pad 常量保持同一公式。
  function drawChartAxes(svg, scaleMax, unit) {
    var chart = svg.closest('.chart');
    if (!chart) return;
    var rail = chart.querySelector('[data-yaxis]');
    if (rail) {
      var html = '';
      for (var k = 0; k <= 4; k++) {
        var top = (2 + 56 * k / 4) / 60 * 100;
        html += '<span style="top:' + top.toFixed(2) + '%">' + fmtAxisValue(scaleMax * (4 - k) / 4, unit) + '</span>';
      }
      rail.innerHTML = html;
    }
    var xAxis = chart.querySelector('[data-xaxis]');
    var pts = window.__POINTS__ || [];
    if (xAxis && pts.length >= 2) {
      var t0 = pts[0].t, t1 = pts[pts.length - 1].t;
      var tm = pts[Math.floor((pts.length - 1) / 2)].t;
      // 单位看绝对时间戳（毫秒 ≈1.79e12，秒 ≈1.79e9），不能看差值 —
      // 一小时的跨度和阈值差着六个数量级，差值判不出单位。
      var raw = Math.max(0, t1 - t0);
      var span = t1 > 1e12 ? raw / 1000 : raw; // 统一成秒
      xAxis.innerHTML = '<span>' + fmtTimeLabel(t0, span) + '</span>' +
        '<span>' + fmtTimeLabel(tm, span) + '</span>' +
        '<span>' + fmtTimeLabel(t1, span) + '</span>';
    }
  }

  function drawAllSparks() {
    var pts = window.__POINTS__ || [];
    if (!pts.length) return;
    var svgs = document.querySelectorAll('.spark[data-series]');
    for (var i = 0; i < svgs.length; i++) {
      var kind = svgs[i].getAttribute('data-series');
      var vals = pts.map(function (p) {
        if (kind === 'cpu') return p.cpu || 0;
        if (kind === 'mem') return p.mu || 0;
        if (kind === 'up') return p.up || 0;
        return p.down || 0;
      });
      drawSpark(svgs[i], vals, kind === 'cpu' ? 'pct' : 'bytes');
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

  /* ---- 详情页「网络质量」分省表的展开/收起 ----
     委托到 document：这块表是 SSE netq 事件的替换目标（整块 outerHTML），
     direct binding 每次替换就失效。行数读按钮的 data-total（模板渲染时
     写的），不在 JS 里硬编码。 */
  function bindNQToggle() {
    var btn = document.getElementById('nq-toggle');
    if (!btn || btn.dataset.bound) return;
    btn.dataset.bound = '1';
    btn.addEventListener('click', function () {
      var rows = document.querySelectorAll('.nq-extra');
      var open = rows.length && rows[0].classList.contains('nq-hidden');
      for (var i = 0; i < rows.length; i++) {
        rows[i].classList.toggle('nq-hidden', !open);
      }
      btn.textContent = open
        ? '收起 ▴'
        : '展开全部 ' + (btn.dataset.total || rows.length) + ' 个省 ▾';
    });
  }

  function bindNQRange() {
    if (document.documentElement.dataset.nqRangeBound) return;
    document.documentElement.dataset.nqRangeBound = '1';
    document.addEventListener('click', function (ev) {
      var btn = ev.target && ev.target.closest ? ev.target.closest('[data-nq-range]') : null;
      if (!btn) return;
      var detail = document.getElementById('nq-detail');
      var slug = document.body && document.body.dataset.nodeSlug;
      if (!detail || !slug) return;
      var key = btn.dataset.nqRange || '1h';
      var url = new URL(window.location.href);
      if (key === '1h') url.searchParams.delete('netq_range');
      else url.searchParams.set('netq_range', key);
      window.history.replaceState({}, '', url.toString());
      detail.dataset.waveRange = key;
      detail.classList.add('nq-loading');
      fetch('/n/' + encodeURIComponent(slug) + '/netq?range=' + encodeURIComponent(key), { cache: 'no-store' })
        .then(function (res) { if (!res.ok) throw new Error('network quality refresh failed'); return res.text(); })
        .then(function (html) { if (html) { detail.outerHTML = html; bindNQToggle(); } })
        .catch(function () { detail.classList.remove('nq-loading'); });
    });
  }

  // Server-rendered card and provincial charts also follow the chosen shape.
  function styleServerSparks(root) {
    var svgs=[];
    if (root.matches && root.matches('.spark:not([data-series]), .nqwave')) svgs.push(root);
    if (root.querySelectorAll) svgs=svgs.concat(Array.prototype.slice.call(root.querySelectorAll('.spark:not([data-series]), .nqwave')));
    var type=document.body.getAttribute('data-k-chart-type')||'area';
    svgs.forEach(function(svg) {
      if (svg.dataset.kRendered) return;
      var source=svg.querySelector('.sp-line, .wl');
      if (!source) return;
      var nums=(source.getAttribute('d')||'').match(/[-+]?\d*\.?\d+(?:e[-+]?\d+)?/gi)||[];
      var points=[];
      for(var i=0;i+1<nums.length;i+=2) points.push([Number(nums[i]),Number(nums[i+1])]);
      if(points.length<2) return;
      var box=svg.viewBox.baseVal, w=box.width,h=box.height, html='';
      if(type==='bar') {
        var bw=w/points.length;
        points.forEach(function(p,i){var y=Math.max(0,Math.min(h,p[1]));html+='<rect class="spark-bar" x="'+(i*bw+1).toFixed(2)+'" y="'+y.toFixed(2)+'" width="'+Math.max(.5,bw-2).toFixed(2)+'" height="'+Math.max(.5,h-y).toFixed(2)+'"/>';});
      } else if(type==='dot') {
        points.forEach(function(p){html+='<ellipse class="spark-dot" cx="'+p[0]+'" cy="'+p[1]+'"/>';});
      } else {
        var line=kokoroChartPath(points,type,kokoroSmoothCharts());
        if(type==='area') html+='<path class="sp-area" d="'+line+' L'+points[points.length-1][0]+' '+h+' L'+points[0][0]+' '+h+' Z"/>';
        html+='<path class="sp-line" d="'+line+'"/>';
      }
      svg.dataset.kRendered=type;
      svg.innerHTML=html;
      fitKokoroChartMarkers(svg);
      fitSparkDots(svg);
    });
  }
  // Non-uniform SVG scaling must not turn round data markers into tall ovals.
  function fitSparkDots(svg) {
    var box=svg.viewBox.baseVal, rect=svg.getBoundingClientRect();
    if(!rect.width || !rect.height) return;
    var radius=parseFloat(getComputedStyle(svg).getPropertyValue('--kokoro-chart-point-radius'));
    if(!Number.isFinite(radius)) radius=2.5;
    svg.querySelectorAll('ellipse.spark-dot').forEach(function(dot){
      dot.setAttribute('rx',Math.max(0,radius)*box.width/rect.width);
      dot.setAttribute('ry',Math.max(0,radius)*box.height/rect.height);
    });
  }
  window.addEventListener('resize',function(){document.querySelectorAll('.spark,.nqwave').forEach(fitSparkDots);});
  styleServerSparks(document);
  if(window.MutationObserver) new MutationObserver(function(records){records.forEach(function(rec){Array.prototype.forEach.call(rec.addedNodes,function(node){if(node.nodeType===1)styleServerSparks(node);});});}).observe(document.body,{childList:true,subtree:true});

  // 主机时钟不再做"按容器宽度整体缩放"。
  //
  // 原来（console 布局）时钟横铺整张卡，三段挤不下时会缩小字号；
  // 现在它在总览统计行左侧、按内容宽度自然排（flex:0 0 auto），
  // 再缩放只会把 23px 的时刻压成 12px —— boss 2026-10-17 报的
  // "时间还是太小"就是这么来的。字号一律交给 CSS（.t/.d/.tz）。

  // ---- 启动 ----
  drawAllSparks();
  bindVotes();
  bindReply();
  bindFilter();
  bindNQToggle();
  bindNQRange();

  var liveBar = document.querySelector('[data-admin-task-live]');
  var liveClose = liveBar && liveBar.querySelector('[data-admin-task-live-close]');
  var liveTimer = 0;
  var liveFadeTimer = 0;
  function consumeTaskFlash() {
    if (!liveBar || liveBar.hidden || !window.history || !window.history.replaceState) return;
    try {
      var url = new URL(window.location.href);
      var keys = /\/admin\/tasks\/?$/.test(url.pathname) ? ['msg', 'err'] : ['taskmsg', 'taskmsg_error'];
      var changed = false;
      keys.forEach(function (key) {
        if (url.searchParams.has(key)) {
          url.searchParams.delete(key);
          changed = true;
        }
      });
      if (changed) window.history.replaceState(window.history.state, '', url.pathname + url.search + url.hash);
    } catch (e) { /* 保留可显示的消息；URL 清理失败不影响页面 */ }
  }
  function clearLiveTimers() {
    if (liveTimer) window.clearTimeout(liveTimer);
    if (liveFadeTimer) window.clearTimeout(liveFadeTimer);
    liveTimer = 0;
    liveFadeTimer = 0;
  }
  function scheduleLiveDismiss() {
    if (!liveBar) return;
    clearLiveTimers();
    liveBar.classList.remove('is-fading');
    liveTimer = window.setTimeout(function () {
      liveBar.classList.add('is-fading');
      liveFadeTimer = window.setTimeout(function () {
        liveBar.hidden = true;
        liveBar.classList.remove('is-fading');
        liveFadeTimer = 0;
      }, 240);
    }, 3000);
  }
  function showLiveMessage(message, status) {
    if (!liveBar) return;
    var label = liveBar.querySelector('[data-admin-task-live-text]');
    if (!label) return;
    label.textContent = message;
    liveBar.dataset.status = status || '';
    liveBar.hidden = false;
    scheduleLiveDismiss();
  }
  if (liveBar && !liveBar.hidden) {
    consumeTaskFlash();
    scheduleLiveDismiss();
  }

  document.addEventListener('kokoro:admin-feedback', function (event) {
    showLiveMessage(event.detail.message, event.detail.status);
  });

  var liveStreams = [];
  var streamOpenedOnce = false, needsFragmentSync = document.hidden, fragmentSyncController = null;
  function syncPanelFragments() {
    if (!document.querySelector('[id^="task-card-"], [id^="netq-rows-"], #nq-detail')) return;
    if (fragmentSyncController) fragmentSyncController.abort();
    var controller = window.AbortController ? new AbortController() : null;
    fragmentSyncController = controller;
    var syncURL = new URL(window.location.href);
    var detail = document.getElementById('nq-detail');
    if (detail && detail.dataset.waveRange) syncURL.searchParams.set('netq_range', detail.dataset.waveRange);
    // Resync only live fragments after returning to a tab. Forms and dialogs
    // stay intact; a completed task must not remain "running" after a pause.
    fetch(syncURL.toString(), { credentials: 'same-origin', cache: 'no-store', signal: controller ? controller.signal : undefined })
      .then(function (response) { if (!response.ok) throw new Error('sync'); return response.text(); })
      .then(function (html) {
        if (document.hidden || fragmentSyncController !== controller) return;
        var fresh = new DOMParser().parseFromString(html, 'text/html');
        document.querySelectorAll('[id^="task-card-"], [id^="netq-rows-"], #nq-detail').forEach(function (current) {
          var next = fresh.getElementById(current.id);
          if (next && current.id === 'nq-detail' && current.querySelector('.nq-extra:not(.nq-hidden)')) {
            next.querySelectorAll('.nq-extra').forEach(function (row) { row.classList.remove('nq-hidden'); });
            var toggle = next.querySelector('#nq-toggle');
            if (toggle) toggle.textContent = '收起 ▴';
          }
          if (next && current.outerHTML !== next.outerHTML) current.replaceWith(document.importNode(next, true));
        });
        bindNQToggle();
      })
      .catch(function () { /* Live metrics remain connected; retry on the next reconnect. */ });
  }
  function disconnectStreams() {
    needsFragmentSync = true;
    if (fragmentSyncController) fragmentSyncController.abort();
    liveStreams.forEach(function (stream) { stream.close(); });
    liveStreams = [];
  }
  function connectStreams() {
    if (typeof EventSource === 'undefined' || document.hidden || liveStreams.length ||
        !document.querySelector('[data-node], [data-admin-task-live], [data-task-history], [data-global-task-list]')) return;
    var streamSlug = document.body && document.body.getAttribute('data-node-slug');
    var es = new EventSource('/api/v1/stream' + (streamSlug ? '?node=' + encodeURIComponent(streamSlug) : ''));
    liveStreams.push(es);
    es.addEventListener('open', function () {
      if (streamOpenedOnce || needsFragmentSync) syncPanelFragments();
      streamOpenedOnce = true;
      needsFragmentSync = false;
    });
    es.addEventListener('node_status', function (ev) {
      var statuses;
      try { statuses = JSON.parse(ev.data); } catch (e) { return; }
      var blocks = document.querySelectorAll('[data-node]');
      for (var i = 0; i < blocks.length; i++) {
        var id = blocks[i].getAttribute('data-node');
        if (Object.prototype.hasOwnProperty.call(statuses, id)) setDot(blocks[i], statuses[id]);
      }
    });
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
    // 三网探测结果：一轮打完入库就推（不等 2 秒的指标上报）。
    // payload 是服务端渲染好的 HTML 片段，outerHTML 整块替换 ——
    // 与 SSR 首屏同一套模板，不会"刷新后变了画法"。
    es.addEventListener('netq', function (ev) {
      var d;
      try { d = JSON.parse(ev.data); } catch (e) { return; }
      if (d.rows && d.node) {
        var box = document.getElementById('netq-rows-' + d.node);
        if (box) box.outerHTML = d.rows;
      }
      if (d.detail) {
        var det = document.getElementById('nq-detail');
        if (det) {
          var range = det.dataset.waveRange || '1h';
          if (range === '1h') {
            det.outerHTML = d.detail;
            bindNQToggle(); // 整块换了，展开按钮要重新绑
          } else {
            var slug = document.body && document.body.dataset.nodeSlug;
            if (slug) fetch('/n/' + encodeURIComponent(slug) + '/netq?range=' + encodeURIComponent(range), { cache: 'no-store' })
              .then(function (res) { return res.ok ? res.text() : ''; })
              .then(function (html) { var current = document.getElementById('nq-detail'); if (html && current) { current.outerHTML = html; bindNQToggle(); } });
          }
        }
      }
    });
    es.addEventListener('task', function (ev) {
      var d;
      try { d = JSON.parse(ev.data); } catch (e) { return; }
      if (!d.node || !d.card) return;
      var card = document.getElementById('task-card-' + d.node);
      if (card) card.outerHTML = d.card;
    });
    es.addEventListener('task_status', function (ev) {
      if (!document.body || !document.body.hasAttribute('data-admin')) return;
      var d;
      try { d = JSON.parse(ev.data); } catch (e) { return; }
      if (!d.message) return;
      showLiveMessage(d.message, d.status);
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

    // 任务历史包含 Agent 原始输出，只从管理员专用 SSE 接收。
    // 首屏由服务端渲染；EventSource 重连时服务端会再发一份当前历史。
    var taskHistory = document.querySelector('[data-task-history]');
    if (document.body && document.body.hasAttribute('data-admin') && taskHistory) {
      var taskNodeID = taskHistory.getAttribute('data-task-history');
      var taskEvents = new EventSource('/admin/stream/tasks?node=' + encodeURIComponent(taskNodeID));
      liveStreams.push(taskEvents);
      taskEvents.addEventListener('task_history', function (ev) {
        var d;
        try { d = JSON.parse(ev.data); } catch (e) { return; }
        if (d.node !== taskNodeID || typeof d.html !== 'string') return;
        var current = document.getElementById('task-history-' + taskNodeID);
        if (current) current.outerHTML = d.html;
      });
    }
    var globalTaskList = document.querySelector('[data-global-task-list]');
    if (document.body && document.body.hasAttribute('data-admin') && globalTaskList) {
      var globalTaskEvents = new EventSource('/admin/stream/tasks?all=1');
      liveStreams.push(globalTaskEvents);
      globalTaskEvents.addEventListener('task_list', function (ev) {
        var d;
        try { d = JSON.parse(ev.data); } catch (e) { return; }
        if (typeof d.html !== 'string') return;
        var current = document.getElementById('admin-task-list-rows');
        if (current) current.outerHTML = d.html;
      });
    }
  }
  document.addEventListener('visibilitychange', function () {
    if (document.hidden) disconnectStreams(); else connectStreams();
  });
  window.addEventListener('pagehide', disconnectStreams);
  window.addEventListener('pageshow', connectStreams);
  connectStreams();
  if (liveClose) liveClose.addEventListener('click', function () {
    clearLiveTimers();
    liveBar.hidden = true;
    liveBar.classList.remove('is-fading');
  });
})();

// Save the visibility switch in place. Sending the desired state makes retries
// idempotent and avoids a POST + redirect + complete admin-page rebuild.
(function () {
  document.addEventListener('submit', function (event) {
    var form = event.target.closest('[data-node-visibility]');
    if (!form || !window.fetch) return;
    event.preventDefault();
    var button = form.querySelector('button[type="submit"]');
    if (button.disabled) return;
    var wasShown = button.getAttribute('aria-checked') === 'true';
    function paint(shown) {
      button.classList.toggle('on', shown);
      button.setAttribute('aria-checked', String(shown));
      button.querySelector('.switch-text').textContent = shown ? '显示中' : '已隐藏';
    }
    var data = new URLSearchParams(new FormData(form));
    data.set('visibility', wasShown ? 'private' : 'public');
    button.disabled = true;
    button.setAttribute('aria-busy', 'true');
    paint(!wasShown);
    fetch(form.getAttribute('action'), { method: 'POST', body: data, credentials: 'same-origin', headers: { Accept: 'application/json' } })
      .then(function (response) {
        return response.json().then(function (result) {
          if (!response.ok || !result.ok) throw new Error(result.error || '保存展示状态失败，请重试');
          paint(result.visibility !== 'private');
        });
      })
      .catch(function (error) {
        paint(wasShown);
        document.dispatchEvent(new CustomEvent('kokoro:admin-feedback', { detail: { message: error.message || '网络异常，请重试', status: 'failed' } }));
      })
      .finally(function () { button.disabled = false; button.removeAttribute('aria-busy'); });
  });
})();

// Save ordering IDs independently and keep the admin table in the same order
// as the server's sort_order, name, id ordering without a full-page reload.
(function () {
  function compareText(a, b) { return a < b ? -1 : (a > b ? 1 : 0); }
  function sortRows(rows) {
    rows.sort(function (a, b) {
      return (Number(a.dataset.nodeSort) - Number(b.dataset.nodeSort)) ||
        compareText(a.dataset.nodeName || '', b.dataset.nodeName || '') ||
        compareText(a.dataset.nodeId || '', b.dataset.nodeId || '');
    });
  }
  function reorderGroups(tbody) {
    var groups = [];
    Array.from(tbody.children).forEach(function (row) {
      if (row.matches('.admin-node-group')) groups.push({ header: row, rows: [] });
      else if (row.matches('[data-node-group-row]') && groups.length) groups[groups.length - 1].rows.push(row);
    });
    groups.forEach(function (group) {
      sortRows(group.rows);
      group.rows.forEach(function (row) { tbody.appendChild(row); });
    });
    groups.sort(function (a, b) {
      var ar = a.rows[0], br = b.rows[0];
      if (!ar) return br ? 1 : 0;
      if (!br) return -1;
      return (Number(ar.dataset.nodeSort) - Number(br.dataset.nodeSort)) ||
        compareText(ar.dataset.nodeName || '', br.dataset.nodeName || '') ||
        compareText(ar.dataset.nodeId || '', br.dataset.nodeId || '');
    });
    groups.forEach(function (group) {
      tbody.appendChild(group.header);
      group.rows.forEach(function (row) { tbody.appendChild(row); });
    });
  }
  document.addEventListener('submit', function (event) {
    var form = event.target.closest('[data-node-sort-form]');
    if (!form || !window.fetch) return;
    event.preventDefault();
    var input = form.querySelector('[name="sort_order"]');
    var button = form.querySelector('button[type="submit"]');
    if (!input || !button || button.disabled || !form.reportValidity()) return;
    var row = form.closest('[data-node-group-row]');
    var oldValue = row ? row.dataset.nodeSort : input.value;
    var data = new URLSearchParams(new FormData(form));
    button.disabled = true;
    button.setAttribute('aria-busy', 'true');
    fetch(form.getAttribute('action'), { method: 'POST', body: data, credentials: 'same-origin', headers: { Accept: 'application/json' } })
      .then(function (response) {
        return response.json().then(function (result) {
          if (!response.ok || !result.ok) throw new Error(result.error || '保存排序 ID 失败，请重试');
          return result;
        });
      })
      .then(function (result) {
        input.value = String(result.sort_order);
        if (row) {
          row.dataset.nodeSort = String(result.sort_order);
          reorderGroups(row.closest('tbody'));
        }
        document.dispatchEvent(new CustomEvent('kokoro:admin-feedback', { detail: { message: '排序 ID 已保存', status: 'done' } }));
      })
      .catch(function (error) {
        input.value = oldValue;
        document.dispatchEvent(new CustomEvent('kokoro:admin-feedback', { detail: { message: error.message || '网络异常，请重试', status: 'failed' } }));
      })
      .finally(function () { button.disabled = false; button.removeAttribute('aria-busy'); });
  });
})();

/* ---- 节点 SWAP 调整弹窗 ---- */
(function () {
  var dialog = document.getElementById('swap-dialog');
  if (!dialog) return;
  var idInput = dialog.querySelector('[data-swap-node-id]');
  var nodeName = dialog.querySelector('[data-swap-node-name]');
  var sizeInput = dialog.querySelector('#swap-size-gib');
  document.querySelectorAll('[data-swap-open]').forEach(function (button) {
    button.addEventListener('click', function () {
      idInput.value = button.dataset.nodeId || '';
      nodeName.textContent = button.dataset.nodeName || '';
      if (typeof dialog.showModal === 'function') dialog.showModal();
      else dialog.setAttribute('open', '');
      sizeInput.focus();
      sizeInput.select();
    });
  });
  var close = dialog.querySelector('[data-swap-close]');
  if (close) close.addEventListener('click', function () { dialog.close(); });
  dialog.addEventListener('click', function (event) {
    if (event.target === dialog) dialog.close();
  });
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

/* Render result popovers in a detached body-level surface. Keeping a popover
   inside a card that itself uses backdrop-filter makes some browsers repaint
   every overlapping glass card continuously while the top layer is visible. */
(function () {
  var active = null;
  var dismissed = null;
  function close() {
    if (!active) return;
    var pop = active.pop;
    pop.remove();
    active = null;
  }
  function dismiss() {
    if (!active) return;
    var host = active.host;
    var returnFocus = active.pop.contains(document.activeElement);
    dismissed = host;
    close();
    var trigger = host.querySelector('.task-summary');
    if (returnFocus && trigger) trigger.focus({preventScroll:true});
  }
  function open(host, x, y) {
    if (active && active.host === host) return;
    close();
    var source = host.querySelector('.task-pop');
    if (!source) return;
    var pop = source.cloneNode(true);
    pop.removeAttribute('popover');
    pop.classList.add('is-open');
    document.body.appendChild(pop);
    pop.style.left = '12px'; pop.style.top = '12px';
    var rect = pop.getBoundingClientRect();
    var left = Math.max(12, Math.min(x - 60, window.innerWidth - rect.width - 12));
    var top = Math.max(12, Math.min(y - 24, window.innerHeight - rect.height - 12));
    pop.style.left = left + 'px'; pop.style.top = top + 'px';
    active = {host:host, pop:pop};
  }
  document.addEventListener('pointerover', function (event) {
    if (event.pointerType === 'touch') return;
    var host = event.target.closest('.task-hover');
    if (host && host !== dismissed) open(host, event.clientX, event.clientY);
  });
  document.addEventListener('pointerout', function (event) {
    if (dismissed && (!event.relatedTarget || !dismissed.contains(event.relatedTarget))) dismissed = null;
    if (!active) return;
    var next = event.relatedTarget;
    if (next && (active.host.contains(next) || active.pop.contains(next))) return;
    if (active.host.contains(event.target) || active.pop.contains(event.target)) close();
  });
  document.addEventListener('focusin', function (event) {
    var host = event.target.closest('.task-hover');
    if (!host || host === dismissed) return;
    var rect = host.getBoundingClientRect();
    open(host, rect.left + 40, rect.top + 20);
  });
  document.addEventListener('focusout', function (event) {
    if (!active) return;
    var next = event.relatedTarget;
    if (!next || (!active.host.contains(next) && !active.pop.contains(next))) close();
  });
  document.addEventListener('click', function (event) {
    if (event.target.closest('[data-task-pop-close]')) { dismiss(); return; }
    var line = event.target.closest('.task-summary');
    if (line) { var rect = line.getBoundingClientRect(); open(line.closest('.task-hover'), event.clientX || rect.left + 40, event.clientY || rect.top + 20); }
    else if (active && !active.host.contains(event.target) && !active.pop.contains(event.target)) close();
  });
  document.addEventListener('keydown', function (event) { if (event.key === 'Escape') dismiss(); });
  window.addEventListener('resize', close);
  window.addEventListener('scroll', close);
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

  var type = (document.body && document.body.getAttribute('data-k-chart-type')) || 'area';
  function pathOf(key) {
    var points=[], step=(W-pad*2)/(pts.length-1);
    for (var k=0;k<pts.length;k++) points.push([pad+k*step,H-pad-(pts[k][key] / max) * (H - pad * 2)]);
    return kokoroChartPath(points,type,kokoroSmoothCharts());
  }
  var up = pathOf('up'), down = pathOf('down');

  // 三条等距横线当刻度：1/4 / 1/2 / 3/4 高度
  var grid = '';
  for (i = 1; i <= 3; i++) {
    y = (H - pad - (H - pad * 2) * i / 4).toFixed(1);
    grid += '<line class="dash-grid-line" x1="' + pad + '" y1="' + y +
            '" x2="' + (W - pad) + '" y2="' + y + '"/>';
  }

  // 图表形态由主题的 layout.charts.type 决定，值在 body 的 data-k-chart-type 上。
  // ⚠️ 早先这个属性**前端根本没读**，主题里选 bar 也照样画 area ——
  // 是 2026-10-11 做毛玻璃主题（那份把能配的全配了一遍）时才发现的。
  if (type === 'bar') {
    // 两组窄柱并排，仍然保留上、下行各自的颜色与变化。
    var bw = (W - pad * 2) / pts.length;
    var bars = '';
    for (i = 0; i < pts.length; i++) {
      var colW = Math.max(1, (bw - 1) / 2);
      var upH = Math.max(pts[i].up > 0 ? 1 : 0, (pts[i].up / max) * (H - pad * 2));
      var downH = Math.max(pts[i].down > 0 ? 1 : 0, (pts[i].down / max) * (H - pad * 2));
      var left = pad + i * bw;
      bars += '<rect class="dash-bar-up" x="' + left.toFixed(1) + '" y="' + (H - pad - upH).toFixed(1) +
              '" width="' + colW.toFixed(1) + '" height="' + upH.toFixed(1) + '"/>';
      bars += '<rect class="dash-bar-down" x="' + (left + colW + 1).toFixed(1) + '" y="' + (H - pad - downH).toFixed(1) +
              '" width="' + colW.toFixed(1) + '" height="' + downH.toFixed(1) + '"/>';
    }
    svg.innerHTML = grid + bars;
    return;
  }

  if (type === 'dot') {
    var rateDots = '';
    for (i = 0; i < pts.length; i++) {
      x = (pad + i * ((W - pad * 2) / (pts.length - 1))).toFixed(1);
      var uy = (H - pad - (pts[i].up / max) * (H - pad * 2)).toFixed(1);
      var dy = (H - pad - (pts[i].down / max) * (H - pad * 2)).toFixed(1);
      rateDots += '<circle class="dash-point" cx="' + x + '" cy="' + uy + '" r="2.5"/>' +
        '<circle class="dash-point-down" cx="' + x + '" cy="' + dy + '" r="2.5"/>';
    }
    svg.innerHTML = grid + rateDots;
  } else {
    var rateAreas=type==='area' ?
      '<path class="dash-area-up" d="M'+pad+' '+(H-pad)+' L'+up.slice(1)+' L'+(W-pad)+' '+(H-pad)+' Z"/>' : '';
    svg.innerHTML = grid + rateAreas +
      '<path class="dash-line-up" d="' + up + '"/>' +
      '<path class="dash-line-down" d="' + down + '"/>';
  }

  // 数据点。半径由主题的 --kokoro-chart-point-radius 决定，0 = 不画。
  // ⚠️ 这个 token 一直存在、但前端从来没接过 —— 2026-10-11 查"主题里写了
  // 却没生效的东西"时才翻出来，和 charts.type:"bar" 是同一批漏接的。
  // 点太密时不画（一小时 60 个点会糊成一片），阈值 40 是折中：
  // 详情页 12 个点、首页 60 个点，前者画、后者不画。
  var pr = 0;
  try {
    pr = parseFloat(window.getComputedStyle(svg).getPropertyValue(
      '--kokoro-chart-point-radius')) || 0;
  } catch (e) { /* 老浏览器没有这个 API，当没配处理 */ }
  if (type !== 'dot' && type !== 'bar' && pr > 0 && pts.length <= 40) {
    var dots = '';
    for (i = 0; i < pts.length; i++) {
      x = (pad + i * ((W - pad * 2) / (pts.length - 1))).toFixed(1);
      y = (H - pad - (pts[i].up / max) * (H - pad * 2)).toFixed(1);
      dots += '<circle class="dash-point" cx="' + x + '" cy="' + y +
              '" r="' + pr + '"/>';
    }
    svg.innerHTML += dots;
  }
  fitKokoroChartMarkers(svg);
}

/* ---- 全网 CPU / 内存平均使用率 ----
   首页「全网速率」卡里的第二张图，和 renderRateChart 共用坐标系
   （viewBox 600x180、preserveAspectRatio 拉伸、HTML 覆盖层放刻度）。

   两处不一样：
   1) 纵轴固定 0–100%，不按峰值自适应 —— 占用率按峰值缩放松紧不定，
      两台空闲机器能把 50% 画成贴顶，跟速率图放一起更没法比；
   2) 两条线都有面积（CPU / 内存都是"占用"，没有主次之分），
      所以不跟主题的 charts.type:"bar" 走 —— 两组并排柱子在 60 个
      采样点上会糊成一片，而速率图只画一条，bar 也看得清。 */
function renderResChart(svg, pts) {
  if (!svg || !pts || pts.length < 2) return;

  var W = 600, H = 180, pad = 8, i, x, y;
  // 取值夹到 0–100：采集偶发毛刺（瞬时 100%+ 或负值）不该把线画出界
  function clamp(v) { return v < 0 ? 0 : (v > 100 ? 100 : v); }

  function pathOf(key) {
    var points=[], step=(W-pad*2)/(pts.length-1);
    for (var k=0;k<pts.length;k++) points.push([pad+k*step,H-pad-clamp(pts[k][key]) * (H - pad * 2) / 100]);
    return kokoroChartPath(points,type,kokoroSmoothCharts());
  }
  var type = (document.body && document.body.getAttribute('data-k-chart-type')) || 'area';
  var cpu = pathOf('cpu'), mem = pathOf('mem');

  // 刻度线：25 / 50 / 75 三档（0 和 100 分别是上下边界，不再画）
  var grid = '';
  for (i = 1; i <= 3; i++) {
    y = (H - pad - (H - pad * 2) * i / 4).toFixed(1);
    grid += '<line class="dash-grid-line" x1="' + pad + '" y1="' + y +
            '" x2="' + (W - pad) + '" y2="' + y + '"/>';
  }

  if (type === 'bar') {
    var bw = (W - pad * 2) / pts.length, bars = '';
    for (i = 0; i < pts.length; i++) {
      var cw = Math.max(1, (bw - 1) / 2), base = pad + i * bw;
      var chCPU = clamp(pts[i].cpu) * (H - pad * 2) / 100;
      var chMem = clamp(pts[i].mem) * (H - pad * 2) / 100;
      bars += '<rect class="res-bar-cpu" x="' + base.toFixed(1) + '" y="' + (H - pad - chCPU).toFixed(1) + '" width="' + cw.toFixed(1) + '" height="' + Math.max(chCPU > 0 ? 1 : 0, chCPU).toFixed(1) + '"/>' +
        '<rect class="res-bar-mem" x="' + (base + cw + 1).toFixed(1) + '" y="' + (H - pad - chMem).toFixed(1) + '" width="' + cw.toFixed(1) + '" height="' + Math.max(chMem > 0 ? 1 : 0, chMem).toFixed(1) + '"/>';
    }
    svg.innerHTML = grid + bars;
    return;
  }
  if (type === 'dot') {
    var dots = '';
    for (i = 0; i < pts.length; i++) {
      x = (pad + i * ((W - pad * 2) / (pts.length - 1))).toFixed(1);
      var yc = (H - pad - clamp(pts[i].cpu) * (H - pad * 2) / 100).toFixed(1);
      var ym = (H - pad - clamp(pts[i].mem) * (H - pad * 2) / 100).toFixed(1);
      dots += '<circle class="res-dot-cpu" cx="' + x + '" cy="' + yc + '" r="2.5"/>' +
        '<circle class="res-dot-mem" cx="' + x + '" cy="' + ym + '" r="2.5"/>';
    }
    svg.innerHTML = grid + dots;
    return;
  }
  // 先画内存的面积与线，再画 CPU —— CPU 是这张图的主角，
  // 后画压在上面，交叉处不会被内存色盖住。
  svg.innerHTML = grid +
    (type === 'area' ? '<path class="res-area-mem" d="M' + pad + ' ' + (H - pad) + ' L' + mem.slice(1) +
      ' L' + (W - pad) + ' ' + (H - pad) + ' Z"/>' +
      '<path class="res-area-cpu" d="M' + pad + ' ' + (H - pad) + ' L' + cpu.slice(1) +
      ' L' + (W - pad) + ' ' + (H - pad) + ' Z"/>' : '') +
    '<path class="res-line-mem" d="' + mem + '"/>' +
    '<path class="res-line-cpu" d="' + cpu + '"/>';
  fitKokoroChartMarkers(svg);
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

/* CPU / 内存占用率图：同一个套路，数据在 <script id="res-data"> 里。
   ⚠️ 不能并进上面那个 IIFE —— 两个数据块各自独立存在，
   有一边没数据（采样点还不够）时另一边照样要画。 */
(function () {
  var el = document.getElementById('res-data');
  if (!el) return;
  var pts;
  try { pts = JSON.parse(el.textContent); } catch (e) { return; }
  document.querySelectorAll('[data-res-chart]').forEach(function (svg) {
    renderResChart(svg, pts);
  });
})();

/* ---- 弹窗：文章 / 评论（两个**独立**弹窗）----
   点卡片上的「文章 N」弹出文章，点「评论 N」弹出评论。
   默认关着，点遮罩 / ESC / ✕ 关闭。数据按需拉。

   原来是右侧滑出的侧边栏，改成居中弹窗 ——
   侧边栏贴着屏幕边缘，正文容易被拉到很窄；居中更好读。 */
(function () {
  var scrim = document.getElementById('dlg-scrim');
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
  var requestID = 0, returnFocus = null, closeTimer = null;
  var drawerCache = new Map(), prefetchTimer = null;
  function dataURL(slug, kind, page) {
    return '/n/' + encodeURIComponent(slug) + '/' + (kind === 'art' ? 'articles' : 'comments') + '.json?page=' + page;
  }
  function drawerData(url) {
    var entry = drawerCache.get(url);
    if (entry && Date.now() - entry.at < 10000) return entry.promise;
    entry = { at: Date.now() };
    entry.promise = fetch(url, { credentials: 'same-origin', cache: 'no-store' })
      .then(function (response) { if (!response.ok) throw new Error('load'); return response.json(); })
      .catch(function (error) { if (drawerCache.get(url) === entry) drawerCache.delete(url); throw error; });
    drawerCache.set(url, entry);
    if (drawerCache.size > 40) drawerCache.delete(drawerCache.keys().next().value);
    return entry.promise;
  }
  function warmDrawer(event) {
    var button = event.target.closest('[data-dlg]');
    if (!button || (navigator.connection && navigator.connection.saveData)) return;
    clearTimeout(prefetchTimer);
    prefetchTimer = setTimeout(function () {
      drawerData(dataURL(button.dataset.dlg, button.dataset.panel === 'cmt' ? 'cmt' : 'art', 1)).catch(function () {});
    }, event.type === 'focusin' ? 0 : 120);
  }
  document.addEventListener('pointerover', warmDrawer);
  document.addEventListener('focusin', warmDrawer);
  document.addEventListener('pointerout', function (event) {
    var button = event.target.closest('[data-dlg]');
    if (button && !button.contains(event.relatedTarget)) clearTimeout(prefetchTimer);
  });

  function esc(s) {
    return String(s == null ? '' : s)
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
  }

  function open(slug, kind) {
    if (closeTimer) clearTimeout(closeTimer);
    scrim.classList.remove('closing');
    returnFocus = document.activeElement;
    st.slug = slug;
    st.kind = kind === 'cmt' ? 'cmt' : 'art';
    st.page = 1;
    st.expanded = false;
    st.cur = panels[st.kind];
    Object.keys(panels).forEach(function (k) { panels[k].el.hidden = true; panels[k].el.classList.remove('closing'); panels[k].el.setAttribute('aria-hidden', 'true'); });
    st.cur.el.hidden = false;
    st.cur.el.setAttribute('aria-hidden', 'false');
    // 留言表单只在评论弹窗出现；文章弹窗没有表单
    var cf = document.getElementById('d-cmt-form');
    if (cf) {
      cf.hidden = st.kind !== 'cmt';
      var cm = document.getElementById('d-cmt-msg');
      if (cm) { cm.textContent = ''; cm.className = 'hint'; }
    }
    scrim.hidden = false;
    document.body.style.overflow = 'hidden';  // 栏开着时锁住背景滚动
    var closeButton = st.cur.el.querySelector('[data-dlgclose]');
    if (closeButton) closeButton.focus();
    load();
  }

  function close() {
    if (!st.cur) return;
    requestID++;
    var el = st.cur.el;
    el.classList.add('closing');
    scrim.classList.add('closing');
    closeTimer = setTimeout(function () {
      el.hidden = true;
      scrim.hidden = true;
      el.classList.remove('closing');
      scrim.classList.remove('closing');
      el.setAttribute('aria-hidden', 'true');
      document.body.style.overflow = '';
      st.cur = null;
      if (returnFocus && returnFocus.isConnected) returnFocus.focus();
    }, 180);
  }

  function load() {
    var cur = st.cur;
    if (!cur) return;
    var loadingRequest = ++requestID, loadingKind = st.kind;
    cur.body.setAttribute('aria-busy', 'true');
    cur.body.innerHTML = '<p class="d-empty">加载中…</p>';
    if (cur.foot) cur.foot.innerHTML = '';
    // ⚠️ 面板 key（art/cmt）和接口路径（articles/comments）**不是一回事**，
    // 必须查表 —— 直接拼 st.kind 会请求 /n/x/art.json，那个端点是 404。
    var url = dataURL(st.slug, st.kind, st.page);
    drawerData(url)
      .then(function (d) {
        if (loadingRequest !== requestID || st.cur !== cur) return;
        cur.body.removeAttribute('aria-busy');
        if (!d || !d.ok) { cur.body.innerHTML = '<p class="d-empty">加载失败</p>'; return; }
        renderAdmin(cur, d);
        if (loadingKind === 'art') renderArticles(cur, d); else renderComments(cur, d);
      })
      .catch(function () { if (loadingRequest !== requestID) return; cur.body.removeAttribute('aria-busy'); cur.body.innerHTML = '<p class="d-empty">加载失败，请稍后重试。</p><button type="button" class="btn ghost small" data-dretry>重新加载</button>'; });
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
    var btn = e.target.closest('[data-dlg]');
    if (btn) { open(btn.dataset.dlg, btn.dataset.panel); return; }
    if (e.target.closest('[data-dretry]')) { drawerCache.delete(dataURL(st.slug, st.kind, st.page)); load(); return; }
    if (e.target.closest('[data-dlgclose]')) { close(); }
  });

  // 留言表单：卡片弹窗里直接发（详情页的留言区已下线，这里是唯一入口）。
  // 提交走 JSON，成功后就地刷新列表，不跳页。
  var cf = document.getElementById('d-cmt-form');
  if (cf) cf.addEventListener('submit', function (ev) {
    ev.preventDefault();
    var msg = document.getElementById('d-cmt-msg');
    var btn = cf.querySelector('button[type="submit"]');
    var body = new URLSearchParams();
    body.set('author', cf.elements.author.value || '');
    body.set('contact', cf.elements.contact.value || '');
    body.set('content', cf.elements.content.value || '');
    body.set('json', '1');
    if (btn) btn.disabled = true;
    if (msg) { msg.textContent = '发送中…'; msg.className = 'hint'; }
    fetch('/n/' + encodeURIComponent(st.slug) + '/comment', {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded', 'X-Requested-With': 'XMLHttpRequest' },
      body: body.toString(),
      credentials: 'same-origin'
    }).then(function (r) {
      return r.json().catch(function () { return { ok: false, error: '返回不是 JSON' }; });
    }).then(function (d) {
      if (btn) btn.disabled = false;
      if (!d || !d.ok) {
        if (msg) { msg.textContent = '失败：' + ((d && d.error) || '未知错误'); msg.className = 'hint bad'; }
        return;
      }
      cf.reset();
      var commentPrefix = '/n/' + encodeURIComponent(st.slug) + '/comments.json';
      drawerCache.forEach(function (_, key) { if (key.indexOf(commentPrefix) === 0) drawerCache.delete(key); });
      if (msg) {
        msg.textContent = d.pending ? '已提交，站长审核后显示' : '已发布';
        msg.className = 'hint ok';
      }
      st.page = 1;
      st.expanded = false;
      load();  // 刷新列表（已发布的会立刻出现）
    }).catch(function () {
      if (btn) btn.disabled = false;
      if (msg) { msg.textContent = '发送失败，稍后再试'; msg.className = 'hint bad'; }
    });
  });

  // ⚠️ 只有点在**遮罩本身**时才关。
  // 弹窗是遮罩的子元素，不判 target 的话点弹窗内部也会冒泡上来关掉它 ——
  // 表现成"点哪儿都关"，正文根本没法看。
  scrim.addEventListener('click', function (e) {
    if (e.target === scrim) close();
  });
  document.addEventListener('keydown', function (e) {
    if (e.key === 'Escape' && st.cur) close();
    if (e.key === 'Tab' && st.cur && !scrim.hidden) {
      var focusable = Array.prototype.filter.call(st.cur.el.querySelectorAll('a[href],button,input,textarea,select,[tabindex="0"]'), function (el) { return !el.disabled && el.getClientRects().length; });
      if (!focusable.length) return;
      var first=focusable[0], last=focusable[focusable.length-1];
      if (e.shiftKey && document.activeElement===first) {e.preventDefault();last.focus();}
      else if (!e.shiftKey && document.activeElement===last) {e.preventDefault();first.focus();}
    }
  });
})();

/* ---- 复制后台 Agent 安装命令 ---- */
(function () {
  document.querySelectorAll('.install-command[data-install-url]').forEach(function(command){
    var input=command.querySelector('[data-install-node-name]');
    var code=command.querySelector('code.cmd');
    if (!input || !code) return;
    function update(){
      var name=input.value.trim();
      var invalid=/[\u0000-\u001f\u007f-\u009f]/.test(name) || Array.from(name).length>128;
      input.setCustomValidity(invalid ? '节点名称最多 128 个字符，不能包含换行或控制字符' : '');
      var url=command.dataset.installUrl+(name ? '?name='+encodeURIComponent(name) : '');
      code.textContent="curl -fsSL '"+url.replace(/'/g,"'\"'\"'")+"' | sh";
      var button=command.querySelector('[data-copy-install]');if (button) button.disabled=invalid;
    }
    input.addEventListener('input',update);update();
  });
  function copyText(text) {
    function fallback() {
      return new Promise(function (resolve, reject) {
        var field = document.createElement('textarea');
        field.value = text;
        field.setAttribute('readonly', '');
        field.style.position = 'fixed';
        field.style.opacity = '0';
        document.body.appendChild(field);
        field.select();
        try {
          if (document.execCommand('copy')) resolve(); else reject();
        } catch (err) { reject(err); }
        field.remove();
      });
    }
    if (navigator.clipboard && navigator.clipboard.writeText) {
      return navigator.clipboard.writeText(text).catch(fallback);
    }
    return fallback();
  }

  document.addEventListener('click', function (e) {
    if (!e.target || !e.target.closest) return;
    var button = e.target.closest('[data-copy-install],[data-copy-code]');
    if (!button || button.disabled) return;
    var articleCode = button.hasAttribute('data-copy-code');
    var command = button.closest(articleCode ? '.md-codeblock' : '.install-command');
    var code = command && command.querySelector(articleCode ? 'pre code' : 'code.cmd');
    if (!code) return;

    var original = button.textContent;
    button.disabled = true;
    button.textContent = '复制中…';
    copyText(articleCode ? code.textContent : code.textContent.trim()).then(function () {
      button.textContent = '已复制';
    }, function () {
      button.textContent = '复制失败';
    }).then(function () {
      setTimeout(function () {
        button.textContent = original;
        button.disabled = false;
      }, 1500);
    });
  });
})();

/* ---- 「获取本站皮肤」弹窗 ----
   皮肤下拉最底部打开本站主题列表；列表中的按钮逐套复制完整包地址。
   和上面文章/评论那套弹窗是两套独立 DOM ——
   那个挂在首页（#dlg-scrim），这个挂在 skinpicker 模板里，
   所以首页和详情页都点得开。 */
(function () {
  var scrim = document.getElementById('skinget-scrim');
  if (!scrim) return;

  function open() {
    // 先收起皮肤下拉，否则菜单会浮在弹窗上面
    var dd = document.querySelector('[data-skin-picker]');
    if (dd) dd.open = false;
    scrim.hidden = false;
    document.body.style.overflow = 'hidden';
  }
  function close() {
    scrim.hidden = true;
    document.body.style.overflow = '';
  }

  function copyText(text) {
    // http 下 navigator.clipboard 不可用（需要安全上下文），
    // 所以必须有 execCommand 的退路，否则站内 http 部署时按钮是死的。
    function fallback() {
      return new Promise(function (resolve, reject) {
        var field = document.createElement('textarea');
        field.value = text;
        field.setAttribute('readonly', '');
        field.style.position = 'fixed';
        field.style.opacity = '0';
        document.body.appendChild(field);
        field.select();
        try {
          if (document.execCommand('copy')) resolve(); else reject();
        } catch (err) { reject(err); }
        field.remove();
      });
    }
    if (navigator.clipboard && navigator.clipboard.writeText) {
      return navigator.clipboard.writeText(text).catch(fallback);
    }
    return fallback();
  }

  document.addEventListener('click', function (e) {
    if (!e.target || !e.target.closest) return;
    if (e.target.closest('[data-skinget]')) { open(); return; }
    if (e.target.closest('[data-skingetclose]')) { close(); return; }

    var copy = e.target.closest('[data-skingetcopy]');
    if (copy) {
      var done = function () {
        copy.textContent = '已复制';
        setTimeout(function () { copy.textContent = '复制获取地址'; }, 1500);
      };
      copyText(copy.getAttribute('data-skinget-url') || '').then(done, function () {
        copy.textContent = '复制失败';
        setTimeout(function () { copy.textContent = '复制获取地址'; }, 1800);
      });
      return;
    }
    if (e.target === scrim) close();
  });

  document.addEventListener('keydown', function (e) {
    if (e.key === 'Escape' && !scrim.hidden) close();
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
  // ⚠️ 只收带 data-goto 的页签。`<a class="atab">`（如「主题」——它跳到
  // 独立的 /admin/themes 页面）没有 dataset.goto，混进来会被当成"当前页签"
  // 高亮，点击时还会被改写成 #undefined。
  var tabs = Array.prototype.slice.call(nav.querySelectorAll('.atab[data-goto]'));
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
    if (h === 'buy-intents') {
      var intents = document.getElementById(h);
      if (intents && intents.tagName === 'DETAILS') intents.open = true;
    }
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

/* ---- 后台节点分组管理 ---- */
(function () {
  document.addEventListener('click', function (e) {
    var tag = e.target.closest('[data-group-action]');
    if (!tag) return;
    var manager = tag.closest('.group-manager');
    if (!manager) return;
    var name = tag.getAttribute('data-group-action');
    manager.querySelectorAll('[data-group-action]').forEach(function (item) {
      item.setAttribute('aria-pressed', item === tag ? 'true' : 'false');
    });
    manager.querySelectorAll('[data-group-actions]').forEach(function (actions) {
      actions.hidden = actions.getAttribute('data-group-actions') !== name;
    });
  });
})();

/* ---- 首页节点分组筛选 ---- */
(function () {
  var filters = Array.prototype.slice.call(document.querySelectorAll('[data-home-group-filter]'));
  var cards = Array.prototype.slice.call(document.querySelectorAll('[data-home-node-card]'));
  var empty = document.querySelector('[data-home-group-empty]');
  if (!filters.length || !cards.length) return;
  filters.forEach(function (button) {
    button.addEventListener('click', function () {
      var key = button.getAttribute('data-home-group-filter');
      var visible = 0;
      filters.forEach(function (item) {
        var active = item === button;
        item.classList.toggle('is-active', active);
        item.setAttribute('aria-pressed', active ? 'true' : 'false');
      });
      cards.forEach(function (card) {
        var show = key === 'all' || card.getAttribute('data-home-node-group') === key;
        card.hidden = !show;
        if (show) visible++;
      });
      if (empty) empty.hidden = visible !== 0;
    });
  });
})();

/* ---- 首页节点卡片曲线时间范围 ---- */
(function () {
  var nodeRangePrefix = 'kokoro.node-range.';
  var allowedRanges = ['1h', '6h', '12h', '1d', '7d', '30d'];

  function setCardRange(card, select, key) {
    var id = card && card.getAttribute('data-node');
    if (!id || allowedRanges.indexOf(key) === -1) return Promise.reject(new Error('invalid node range'));
    select.disabled = true;
    card.setAttribute('aria-busy', 'true');
    return fetch('/api/v1/card-series?id=' + encodeURIComponent(id) + '&range=' + encodeURIComponent(key), { cache: 'no-store' })
      .then(function (res) { if (!res.ok) throw new Error('node chart refresh failed'); return res.json(); })
      .then(function (data) {
        var svg = card.querySelector('.spark');
        if (!svg && data.line) {
          var ns = 'http://www.w3.org/2000/svg';
          svg = document.createElementNS(ns, 'svg');
          svg.setAttribute('class', 'spark');
          svg.setAttribute('viewBox', '0 0 260 40');
          svg.setAttribute('preserveAspectRatio', 'none');
          svg.setAttribute('aria-hidden', 'true');
          var empty = card.querySelector('.spark-empty');
          if (empty) empty.replaceWith(svg);
        }
        if (svg) {
          var line = svg.querySelector('.sp-line');
          var area = svg.querySelector('.sp-area');
          if (!line) { line = document.createElementNS('http://www.w3.org/2000/svg', 'path'); line.setAttribute('class', 'sp-line'); svg.appendChild(line); }
          if (!area) { area = document.createElementNS('http://www.w3.org/2000/svg', 'path'); area.setAttribute('class', 'sp-area'); svg.insertBefore(area, line); }
          if (line) line.setAttribute('d', data.line || '');
          if (area) area.setAttribute('d', data.area || '');
        }
        var label = card.querySelector('[data-spark-label]');
        if (label) label.textContent = data.line ? '峰值 ' + data.peak : (data.info || '暂无数据');
        try { localStorage.setItem(nodeRangePrefix + id, key); } catch (_) {}
      })
      .finally(function () {
        select.disabled = false;
        card.removeAttribute('aria-busy');
      });
  }

  document.querySelectorAll('[data-home-range]').forEach(function (select) {
    if (!select.form) return;
    select.addEventListener('change', function () {
      // 总览时间范围明确作用于全部节点，清除单卡覆盖，保证全局选择生效。
      try {
        for (var i = localStorage.length - 1; i >= 0; i--) {
          var key = localStorage.key(i);
          if (key && key.indexOf(nodeRangePrefix) === 0) localStorage.removeItem(key);
        }
      } catch (_) {}
      if (select.form.requestSubmit) select.form.requestSubmit();
      else select.form.submit();
    });
  });

  document.querySelectorAll('[data-node-range]').forEach(function (select) {
    var card = select.closest('[data-home-node-card]');
    var id = card && card.getAttribute('data-node');
    if (!card || !id) return;
    var initial = select.value;
    var saved = null;
    try { saved = localStorage.getItem(nodeRangePrefix + id); } catch (_) {}
    if (allowedRanges.indexOf(saved) !== -1 && saved !== initial) {
      select.value = saved;
      setCardRange(card, select, saved).catch(function () {
        select.value = initial;
        select.setAttribute('data-last-range', initial);
      });
    }
    select.addEventListener('change', function () {
      var previous = select.getAttribute('data-last-range') || initial;
      var chosen = select.value;
      setCardRange(card, select, chosen).then(function () {
        select.setAttribute('data-last-range', chosen);
      }).catch(function () {
        select.value = previous;
      });
    });
    select.setAttribute('data-last-range', select.value);
  });
})();

/* ---- 从节点行选择后批量加入分组 ---- */
(function () {
  var dialog = document.getElementById('node-group-assign-dialog');
  if (!dialog) return;
  var inputs = dialog.querySelector('[data-group-assign-node-inputs]');
  var count = dialog.querySelector('[data-group-assign-count]');
  var form = dialog.querySelector('[data-group-assign-form]');
  var checkboxes = Array.prototype.slice.call(document.querySelectorAll('[data-node-select-item]'));
  var buttons = Array.prototype.slice.call(document.querySelectorAll('[data-group-assign-open]'));
  function selectedNodes() {
    return checkboxes.filter(function (box) { return box.checked; });
  }
  function syncButtons() {
    var hasGroups = !!(form && form.querySelector('select option'));
    var enabled = hasGroups && selectedNodes().length > 0;
    buttons.forEach(function (button) { button.disabled = !enabled; });
  }
  document.addEventListener('change', function (event) {
    if (event.target.matches('[data-node-select-item], [data-node-select-all]')) syncButtons();
  });
  buttons.forEach(function (button) {
    button.addEventListener('click', function () {
      var selected = selectedNodes();
      if (!selected.length) return;
      inputs.replaceChildren();
      selected.forEach(function (box) {
        var hidden = document.createElement('input');
        hidden.type = 'hidden';
        hidden.name = 'node_ids';
        hidden.value = box.value;
        inputs.appendChild(hidden);
      });
      count.textContent = '已选择 ' + selected.length + ' 台节点；加入后会从原分组移出。';
      if (typeof dialog.showModal === 'function') dialog.showModal();
      else dialog.setAttribute('open', '');
    });
  });
  var close = dialog.querySelector('[data-group-assign-close]');
  if (close) close.addEventListener('click', function () { dialog.close(); });
  dialog.addEventListener('click', function (event) {
    if (event.target === dialog) dialog.close();
  });
  syncButtons();
})();

/* ---- 到期日：固定 yyyy/mm/dd 显示与原生日历 ----
   原生 date 输入框的占位格式跟系统区域设置走，lang 属性无法可靠覆盖；
   所以用文本框显示固定格式，保留一个透明的原生日期控件供日历按钮调用。
   服务端仍收到并保存 ISO YYYY-MM-DD。 */
(function () {
  function parseDate(raw) {
    var m = /^\s*(\d{4})[-/](\d{1,2})[-/](\d{1,2})\s*$/.exec(raw || '');
    if (!m) return '';
    var year = Number(m[1]), month = Number(m[2]), day = Number(m[3]);
    var leap = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0);
    var days = [31, leap ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31];
    if (month < 1 || month > 12 || day < 1 || day > days[month - 1]) return '';
    return m[1] + '-' + String(month).padStart(2, '0') + '-' + String(day).padStart(2, '0');
  }

  document.querySelectorAll('[data-exp-date]').forEach(function (text) {
    var entry = text.closest('.date-entry');
    var picker = entry && entry.querySelector('[data-exp-picker]');
    var button = entry && entry.querySelector('[data-exp-picker-button]');
    if (!picker || !button) return;
    var initial = parseDate(text.value);
    if (initial) {
      text.value = initial.replace(/-/g, '/');
      picker.value = initial;
    }
    text.addEventListener('input', function () {
      picker.value = parseDate(text.value);
    });
    picker.addEventListener('change', function () {
      if (picker.value) text.value = picker.value.replace(/-/g, '/');
    });
    button.addEventListener('click', function () {
      if (text.disabled) return;
      try {
        if (typeof picker.showPicker === 'function') picker.showPicker();
        else { picker.focus(); picker.click(); }
      } catch (e) {
        picker.focus();
        picker.click();
      }
    });
  });
})();

/* ---- 到期日：「永久」勾选框 ----
   勾上就禁掉日期框（禁用的控件浏览器不提交，所以后端只认勾选框）。
   同时把日期框清空 —— 留着旧日期会让人以为两个值都生效。

   ⚠️ 必须**逐行**绑定：节点表格里每行都有自己的日期框和勾选框，
   以前只 querySelector 拿第一个，第二台之后的机器勾「永久」
   根本禁不掉日期框（两个值打架，存进去的还不是勾的那个）。
   行内配对用 closest('.fld-row') —— 不靠文档顺序赌位置。 */
(function () {
  document.querySelectorAll('[data-exp-forever]').forEach(function (forever) {
    var row = forever.closest('.fld-row');
    var date = row && row.querySelector('[data-exp-date]');
    var picker = row && row.querySelector('[data-exp-picker]');
    if (!date) return;

    function sync() {
      date.disabled = forever.checked;
      if (picker) picker.disabled = forever.checked;
      if (forever.checked) date.value = '';
    }
    forever.addEventListener('change', sync);
    sync();
  });
})();

/* ---- Markdown 工具栏 ----
   按钮靠 data-md 带一段「包裹模板」，`|` 是光标落点。 */
(function () {
  var ta = document.getElementById('p-body');
  var bar = document.querySelector('.mdbar');
  var tip = document.getElementById('md-tip');
  var file = document.getElementById('md-file');
  if (!ta || !bar) return;

  var tipTimer = null;
  function say(text, kind) {
    tip.textContent = text || '';
    tip.className = 'mdtip' + (kind ? ' ' + kind : '');
    if (tipTimer) clearTimeout(tipTimer);
    if (text) tipTimer = setTimeout(function () { tip.textContent = ''; }, 4000);
  }

  // ⚠️ 用 execCommand('insertText') 而不是直接改 ta.value：
  // 直接赋值会把浏览器的**撤销历史整个清掉**，站长按 Ctrl+Z 一下
  // 就退回"打开页面时"的状态，刚刚写的一大段全没了。
  // execCommand 虽然被标成 deprecated，但它是唯一能保住撤销栈的办法。
  function insert(text) {
    ta.focus();
    var ok = false;
    try { ok = document.execCommand('insertText', false, text); } catch (e) { ok = false; }
    if (ok) return;
    // 退路：老浏览器不支持时手工改，至少功能能用（撤销会丢）
    var s = ta.selectionStart, e = ta.selectionEnd;
    ta.value = ta.value.slice(0, s) + text + ta.value.slice(e);
    ta.selectionStart = ta.selectionEnd = s + text.length;
  }

  // 把模板套在选中内容上。tpl 里的 | 是光标落点。
  function wrap(tpl) {
    var at = tpl.indexOf('|');
    var head = at < 0 ? tpl : tpl.slice(0, at);
    var tail = at < 0 ? '' : tpl.slice(at + 1);
    var s = ta.selectionStart, e = ta.selectionEnd;
    var sel = ta.value.slice(s, e);

    // 行首类模板（## / - / > ）：光标在行中间时先补个换行，
    // 否则会接在上一句后面变成正文的一部分。
    var isLinePrefix = /^(#{1,6} |[-*] |\d+\. |> )/.test(head);
    if (isLinePrefix && s > 0 && ta.value[s - 1] !== '\n') {
      head = '\n' + head;
    }
    // 选中的是多行时，每行都加前缀
    if (isLinePrefix && sel.indexOf('\n') >= 0) {
      sel = sel.split('\n').map(function (l) { return head.replace(/^\n/, '') + l; }).join('\n');
      head = '';
    }
    insert(head + sel + tail);
    if (!sel) {
      // 没选中就给个占位词，选着它方便直接改写
      var ph = isLinePrefix ? '文字' : (head === '**' ? '粗体'
        : head === '*' ? '斜体' : head === '~~' ? '删除线'
        : head === '`' ? '代码' : '');
      if (ph) {
        insert(ph);
        ta.setSelectionRange(ta.selectionStart - ph.length, ta.selectionStart);
      }
    }
  }

  bar.addEventListener('click', function (e) {
    var b = e.target.closest('.mdbtn');
    if (!b) return;
    if (b.hasAttribute('data-md-font')) return toggleFontMenu();
    if (b.hasAttribute('data-md-preview')) return togglePreview();
    if (b.hasAttribute('data-md-link')) return addLink();
    if (b.hasAttribute('data-md-img-url')) return addImageLink();
    if (b.hasAttribute('data-md-code-block')) return insert('\n\n```sh\n' + (ta.value.slice(ta.selectionStart,ta.selectionEnd) || '# 在这里输入代码') + '\n```\n');
    if (b.hasAttribute('data-md-table')) return insert('\n\n| 项目 | 说明 |\n| --- | --- |\n| 内容 | 内容 |\n');
    if (b.hasAttribute('data-md-img')) return file.click();
    if (b.hasAttribute('data-md')) wrap(b.dataset.md);
  });

  // ---- 预览 ----
  // 结果由**服务端**渲染（/admin/preview），用的是前台同一个 RenderMarkdown，
  // 所以预览 == 访客看到的。
  var pv = document.getElementById('p-preview');
  var inPreview = false;

  function togglePreview() {
    inPreview = !inPreview;
    if (inPreview) { renderPreview(); } else { ta.hidden = false; pv.hidden = true; say('已回到编辑'); }
  }

  function renderPreview() {
    say('渲染中…');
    // 用 urlencoded 而不是 FormData：少一层 multipart 解析，
    // 服务端也不用为了它走 ParseMultipartForm。
    fetch('/admin/preview', {
      method: 'POST',
      headers: {'Content-Type': 'application/x-www-form-urlencoded;charset=UTF-8'},
      body: 'md=' + encodeURIComponent(ta.value),
      credentials: 'same-origin',
    })
      .then(function (r) { return r.json(); })
      .then(function (d) {
        if (!d.ok) { say('预览失败', 'err'); return; }
        if (d.html && d.html.trim()) {
          pv.innerHTML = d.html;
        } else {
          pv.innerHTML = '<p class="empty">（还没有内容）</p>';
        }
        ta.hidden = true;   // 用 hidden 而不是删掉 —— 删了会丢掉没保存的正文
        pv.hidden = false;
        say('预览中（再点一次回到编辑）');
      })
      .catch(function () { say('预览失败', 'err'); });
  }

  // ---- 字体 / 字号 ----
  var fmenu = document.getElementById('md-fontmenu');

  function toggleFontMenu() {
    if (!fmenu) return;
    fmenu.hidden = !fmenu.hidden;
    if (!fmenu.hidden) {
      // 菜单浮在按钮下方：按钮父级要能定位
      var r = bar.getBoundingClientRect();
      fmenu.style.top = (window.scrollY + r.bottom + 2) + 'px';
      fmenu.style.left = (window.scrollX + r.left) + 'px';
    }
  }

  if (fmenu) {
    fmenu.addEventListener('click', function (e) {
      var i = e.target.closest('.mdmenu-i');
      if (!i) return;
      wrap(i.dataset.md);
      fmenu.hidden = true;
    });
    // 点到别处就收起
    document.addEventListener('click', function (e) {
      if (fmenu.hidden) return;
      if (e.target.closest('#md-fontmenu') || e.target.closest('[data-md-font]')) return;
      fmenu.hidden = true;
    });
  }

  function addLink() {
    var s = ta.selectionStart, e = ta.selectionEnd;
    var text = ta.value.slice(s, e) || '链接文字';
    var url = window.prompt('链接地址（https://…）', 'https://');
    if (!url) return;
    // 只接受 http(s)：javascript: 之类的伪协议插进 Markdown 里，
    // 渲染出来就是个能点的 XSS 入口。
    if (!/^https?:\/\//i.test(url.trim())) {
      say('只接受 http / https 开头的地址', 'err');
      return;
    }
    insert('[' + text + '](' + url.trim() + ')');
  }

  function addImageLink() {
    var alt = ta.value.slice(ta.selectionStart,ta.selectionEnd) || '图片';
    var address = window.prompt('图片直链（https://…，指向图片文件）','https://');
    if (!address) return;
    address=address.trim();
    if (!/^https?:\/\//i.test(address) || /[\r\n]/.test(address)) {say('请输入 http 或 https 图片直链','err');return;}
    alt=alt.replace(/[\[\]\\]/g,'\\$&');
    address=address.replace(/</g,'%3C').replace(/>/g,'%3E');
    insert('\n!['+alt+'](<'+address+'>)\n');
    say('已插入图片链接，可点预览查看');
  }

  // 粘贴图片：截图之后直接 Ctrl+V 是最顺手的流程，
  // 比"先存到磁盘再点按钮选文件"少两步。
  //
  // ⚠️ 要在 paste 事件里**同步**把文件取出来。clipboardData 在事件
  // 处理返回之后就失效了，异步里再读会拿到空值 ——
  // 表现成"粘贴没反应"，而且不报错。
  ta.addEventListener('paste', function (e) {
    var items = (e.clipboardData && e.clipboardData.items) || [];
    var img = null;
    for (var i = 0; i < items.length; i++) {
      if (items[i].type && items[i].type.indexOf('image/') === 0) {
        img = items[i].getAsFile();
        break;
      }
    }
    if (!img) return;      // 普通文本粘贴，交给浏览器自己处理
    e.preventDefault();    // 别让浏览器把图片当二进制塞进文本框
    upload(img);
  });

  // 也支持直接往编辑器里**拖文件**
  ta.addEventListener('drop', function (e) {
    var dt = e.dataTransfer;
    if (!dt || !dt.files || !dt.files.length) return;
    var f = dt.files[0];
    if (!f.type || f.type.indexOf('image/') !== 0) return;
    e.preventDefault();
    upload(f);
  });

  // upload 是上传 + 插入的共用逻辑，按钮/粘贴/拖拽三条路都走它。
  function upload(f) {
    if (f.size > 2 << 20) { say('图片不能超过 2MB', 'err'); return; }
    say('上传中…');
    var fd = new FormData();
    fd.append('file', f);
    fetch('/admin/upload', { method: 'POST', body: fd, credentials: 'same-origin' })
      .then(function (r) { return r.json().then(function (d) { return { ok: r.ok, d: d }; }); })
      .then(function (res) {
        if (!res.ok || !res.d.ok) {
          say((res.d && res.d.error) || '上传失败', 'err');
          return;
        }
        // 粘贴的图没有文件名，给个通用的 alt
        var alt = (f.name || '图片').replace(/\.[^.]+$/, '') || '图片';
        insert('![' + alt + '](' + res.d.url + ')');
        say('已插入 ' + (res.d.size / 1024).toFixed(0) + ' KB', 'ok');
      })
      .catch(function () { say('上传失败', 'err'); });
  }

  file.addEventListener('change', function () {
    var f = file.files && file.files[0];
    file.value = '';           // 允许连续上传同一个文件
    if (f) upload(f);
  });

  // 快捷键。只在正文框里生效，别影响别处的输入框。
  ta.addEventListener('keydown', function (e) {
    if (!(e.ctrlKey || e.metaKey)) return;
    var k = e.key.toLowerCase();
    if (k === 'b') { e.preventDefault(); wrap('**|**'); }
    else if (k === 'i') { e.preventDefault(); wrap('*|*'); }
    else if (k === 'k') { e.preventDefault(); addLink(); }
    else if (k === 'p') { e.preventDefault(); togglePreview(); }
  });
})();

/* ---- 「我想买它」----
   点按钮 → 弹窗填联系方式 → POST /api/v1/buy → 服务端落库 + 推 TG。
   公开接口，服务端自己限流；这里只负责收内容和反馈结果。 */
(function () {
  var scrim = document.getElementById('buy-scrim');
  var btn = document.getElementById('buy-send');
  if (!scrim || !btn) return;

  var slug = document.getElementById('buy-slug');
  var contact = document.getElementById('buy-contact');
  var note = document.getElementById('buy-note');
  var title = document.getElementById('buy-title');
  var err = document.getElementById('buy-err');
  var form = document.getElementById('buy-form');
  var intro = document.getElementById('buy-intro');
  var result = document.getElementById('buy-result');

  function open(s, name) {
    slug.value = s;
    contact.value = '';
    note.value = '';
    contact.disabled = false;
    note.disabled = false;
    btn.disabled = false;
    btn.hidden = false;
    btn.textContent = '发送';
    form.hidden = false;
    intro.hidden = false;
    result.hidden = true;
    result.textContent = '';
    if (title) title.textContent = '我想买它 · ' + (name || '');
    err.hidden = true;
    scrim.hidden = false;
    setTimeout(function () { contact.focus(); }, 30);
  }

  function closeBuy() { scrim.hidden = true; }

  document.addEventListener('click', function (e) {
    var b = e.target.closest('[data-buy]');
    if (b) { open(b.dataset.buy, b.dataset.buyname); return; }
    if (e.target.closest('[data-buyclose]')) { closeBuy(); return; }
    // 点遮罩本身才关（弹窗是它的子元素）
    if (e.target === scrim) closeBuy();
  });

  document.addEventListener('keydown', function (e) {
    if (e.key === 'Escape' && !scrim.hidden) closeBuy();
  });

  btn.addEventListener('click', function () {
    var c = (contact.value || '').trim();
    if (!c) {
      err.textContent = '请填一下联系方式';
      err.hidden = false;
      contact.focus();
      return;
    }
    err.hidden = true;
    var old = btn.textContent;
    btn.disabled = true;
    btn.textContent = '发送中…';

    var body = new URLSearchParams();
    body.set('slug', slug.value);
    body.set('contact', c);
    body.set('note', (note.value || '').trim());

    fetch('/api/v1/buy', {
      method: 'POST',
      headers: {'Content-Type': 'application/x-www-form-urlencoded;charset=UTF-8'},
      body: body.toString(),
      credentials: 'same-origin',
    })
      .then(function (r) {
        return r.json().catch(function () { return {}; }).then(function (d) {
          return { ok: r.ok, d: d };
        });
      })
      .then(function (res) {
        btn.disabled = false;
        btn.textContent = old;
        if (!res.ok) {
          err.textContent = (res.d && res.d.error) || '发送失败，请稍后再试';
          err.hidden = false;
          return;
        }
        var labels = {
          sent: '已送达',
          failed: '发送失败',
          unconfigured: '未配置或未启用',
          quiet_hours: '免打扰时段，暂未发送',
        };
        var telegramStatus = (res.d && res.d.telegram_status) || 'unconfigured';
        var webhookStatus = (res.d && res.d.webhook_status) || 'unconfigured';
        var deliverySummary = 'Telegram：' + (labels[telegramStatus] || '状态未知') +
          '；Webhook：' + (labels[webhookStatus] || '状态未知');
        var delivered = telegramStatus === 'sent' || webhookStatus === 'sent';
        if (!delivered) {
          err.textContent = '消息已保存到站内，站长可在后台查看。' + deliverySummary + '。也可以去评论区留言：';
          var comments = document.createElement('a');
          comments.href = '/n/' + encodeURIComponent(slug.value) + '#comments';
          comments.textContent = '去评论区留言';
          err.appendChild(comments);
          err.hidden = false;
          contact.disabled = true;
          note.disabled = true;
          btn.disabled = true;
          btn.textContent = '已保存';
          return;
        }
        // 至少一个渠道送达；仍明确展示另一个渠道状态，并保留可重复打开的表单结构。
        form.hidden = true;
        intro.hidden = true;
        result.textContent = '购买消息已保存到站内，站长可以在后台查看。' + deliverySummary;
        result.hidden = false;
        btn.hidden = true;
      })
      .catch(function () {
        btn.disabled = false;
        btn.textContent = old;
        err.textContent = '网络异常，请稍后再试';
        err.hidden = false;
      });
  });
})();
