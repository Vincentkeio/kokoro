/* Kokoro 探针 —— 点阵地球。
 *
 * 无任何外部依赖，纯手写 WebGL + Canvas2D。用它的理由：
 *   - 项目硬约束是「单二进制、无 CDN」，three.js / d3-geo 这类库都不能引；
 *   - 立体地球比平面地图更能表达「节点分布在全球」，而且昼夜线天然有时效感。
 *
 * 分工：
 *   WebGL 画陆地圆点（一次 draw call，几万个点也不掉帧）
 *   Canvas2D 画航线、脉冲、悬停提示（这些是矢量线条，用 2D 更省事也更好看）
 *
 * 无 WebGL 时自动降级到 Canvas2D 画点阵，功能不减，只是点稀疏一些。
 *
 * 用法：
 *   var g = new KokoroGlobe(hostEl, {
 *     landUrl: '/static/land.bin?v=6',   // 或 landInline: '<base64>'（本地预览用）
 *     hub:  { name: '东京', lat: 35.68, lon: 139.69 },
 *     places: [{ name: '洛杉矶', lat: 34.05, lon: -118.24, online: true }],
 *     colors: function () { return { day:'#c8d4e4', night:'#3a4658', arc:'#2f6feb', ... }; }
 *   });
 *   g.setPlaces({ places: [...] });
 */
(function (global) {
  'use strict';

  var DEG = Math.PI / 180;
  var LAND_MAGIC = 'KLD1';

  /* ---------- 小工具 ---------- */

  function clamp(v, a, b) { return v < a ? a : (v > b ? b : v); }

  // 经纬度 -> 单位球坐标。x 朝屏幕右，y 朝北，z 朝观察者。
  //
  // ⚠️ 东经往 **-z** 方向转，不是 +z。这决定了地球是不是镜像的。
  //
  // 推导（别凭"地图上东在右"的直觉，那个直觉在这里会翻车）：
  //   侧视图：右=+x，上=+y，出屏=+z（cross(x,y)=z ✓）
  //   俯视图（从北极上方往下看）：要满足 cross(右,上)=出屏=+y，
  //     取右=+x 则上必须=-z。所以正前方(+z)在俯视图里朝下。
  //   地球自转从北极上方看是逆时针，在俯视图里即 +x→-z→-x→+z→+x。
  //   正前方的点(+z)下一刻走到 +x，即**屏幕右侧**；
  //   而这个点是被地球带着往**东**走的。=> 东 = 右。
  // 若写成 +z，东就跑到左边，地球整体镜像（实测香港会被画到东京右边）。
  function toVec(lat, lon, out) {
    var p = lat * DEG, l = lon * DEG;
    var cp = Math.cos(p);
    out[0] = cp * Math.cos(l);
    out[1] = Math.sin(p);
    out[2] = -cp * Math.sin(l);
    return out;
  }

  // 绕 Y 轴（偏航）再绕 X 轴（俯仰）。顺序固定，前后端一致就不会乱。
  function rotate(v, yaw, pitch, out) {
    var cy = Math.cos(yaw), sy = Math.sin(yaw);
    var x = v[0] * cy + v[2] * sy;
    var z = -v[0] * sy + v[2] * cy;
    var y = v[1];
    var cx = Math.cos(pitch), sx = Math.sin(pitch);
    var y2 = y * cx - z * sx;
    var z2 = y * sx + z * cx;
    out[0] = x; out[1] = y2; out[2] = z2;
    return out;
  }

  /* ---------- 陆地掩码解码 ---------- */

  // 文件头：magic(4) + nlon(u16) + nlat(u16) + latMax(f32) + latMin(f32) + step(f32)
  function decodeLand(buf) {
    var head = 20;
    var magic = String.fromCharCode(buf[0], buf[1], buf[2], buf[3]);
    if (magic !== LAND_MAGIC) throw new Error('陆地掩码格式不对: ' + magic);
    var dv = new DataView(buf.buffer, buf.byteOffset, buf.byteLength);
    var nlon = dv.getUint16(4, true);
    var nlat = dv.getUint16(6, true);
    var latMax = dv.getFloat32(8, true);
    var latMin = dv.getFloat32(12, true);
    var step = dv.getFloat32(16, true);
    if (buf.length < head + Math.ceil(nlon / 8) * nlat) {
      throw new Error('陆地掩码长度不足');
    }
    return { bits: buf.subarray(head), nlon: nlon, nlat: nlat, latMax: latMax, latMin: latMin, step: step };
  }

  function isLand(m, col, row) {
    if (col < 0 || col >= m.nlon || row < 0 || row >= m.nlat) return false;
    var stride = (m.nlon + 7) >> 3;
    return (m.bits[row * stride + (col >> 3)] & (1 << (7 - (col & 7)))) !== 0;
  }

  // 掩码 -> 单位球顶点数组。高纬度按比例抽稀：
  // 0.5° 的网格在两极会挤成一坨，抽掉一半反而更均匀。
  function buildLandDots(m) {
    var pts = [], col, row, lat, lon, keep;
    for (row = 0; row < m.nlat; row++) {
      lat = m.latMax - (row + 0.5) * m.step;
      var al = Math.abs(lat);
      for (col = 0; col < m.nlon; col++) {
        if (!isLand(m, col, row)) continue;
        keep = true;
        if (al > 62 && (col & 1)) keep = false;
        else if (al > 74 && (col & 3)) keep = false;
        if (!keep) continue;
        lon = -180 + (col + 0.5) * m.step;
        pts.push(toVec(lat, lon, [0, 0, 0]));
      }
    }
    var out = new Float32Array(pts.length * 3);
    for (var i = 0; i < pts.length; i++) {
      out[i * 3] = pts[i][0]; out[i * 3 + 1] = pts[i][1]; out[i * 3 + 2] = pts[i][2];
    }
    return out;
  }

  /* ---------- 昼夜 ---------- */

  // 太阳直射点。用简化的低精度公式就够——这里只需要"大概对"，
  // 卫星级的精度对这个面板没有任何意义。
  function subsolar(date) {
    var start = Date.UTC(date.getUTCFullYear(), 0, 0);
    var dayOfYear = (date.getTime() - start) / 86400000;
    // 赤纬：一年周期，春分前后约 0
    var decl = -23.44 * Math.cos(2 * Math.PI * (dayOfYear + 10) / 365.24);
    // 直射经度：UTC 正午在 0°，每小时西移 15°
    var utcHours = date.getUTCHours() + date.getUTCMinutes() / 60 + date.getUTCSeconds() / 3600;
    var lon = (12 - utcHours) * 15;
    while (lon > 180) lon -= 360;
    while (lon < -180) lon += 360;
    return { lat: decl, lon: lon };
  }

  /* ---------- 着色器 ---------- */

  var VERT = [
    'attribute vec3 aPos;',
    'uniform mat3 uRot;',
    'uniform float uSize;',
    'uniform vec2 uScale;',   // 把单位球映射到像素半径 R：x 用 2R/w，y 用 2R/h
    'varying float vZ;',
    'varying float vLit;',
    'uniform vec3 uSun;',
    'void main() {',
    '  vec3 p = uRot * aPos;',
    '  vZ = p.z;',
    // 光照：aPos 和 uSun 都必须是**地球固连坐标系**里的向量。
    //
    // 关键点：uRot 是**相机**旋转，不是地球自转，所以它绝不能参与光照计算。
    //   - 只转 aPos 不转 uSun（写成 dot(p, uSun)）=> 两个向量不在同一坐标系，
    //     结果是错的，实测日本白天却是暗的；
    //   - 两个都不转（写成 dot(aPos, uSun)）=> 正确。点积对旋转不变，
    //     也正好对应物理：太阳方向在地球固连系里随时间转，大陆不动。
    '  vLit = smoothstep(-0.22, 0.18, dot(aPos, uSun));',
    // ⚠️ 不能直接写 vec4(p.x, p.y, 0, 1)：NDC 在 x/y 上的像素尺度不同
    // （画布是 1268x520 这种扁的），直接写会把球拉成椭球，
    // 而且和 2D 叠加层（那边是正圆）对不上，航线会画到球外面去。
    '  gl_Position = vec4(p.x * uScale.x, p.y * uScale.y, 0.0, 1.0);',
    '  gl_PointSize = uSize;',
    '}'
  ].join('\n');

  var FRAG = [
    'precision mediump float;',
    'uniform vec3 uDay;',
    'uniform vec3 uNight;',
    'varying float vZ;',
    'varying float vLit;',
    'void main() {',
    // 背面的点丢掉即可——正交投影下它们不该出现
    '  if (vZ <= 0.0) discard;',
    // 画成圆点，边缘一像素羽化，避免方块感
    '  vec2 d = gl_PointCoord - vec2(0.5);',
    '  float r = length(d);',
    '  if (r > 0.5) discard;',
    '  float a = 1.0 - smoothstep(0.34, 0.5, r);',
    '  vec3 c = mix(uNight, uDay, vLit);',
    '  gl_FragColor = vec4(c, a);',
    '}'
  ].join('\n');


  /* 球面（海洋）的着色器。
     为什么需要它：点阵只画**陆地**，海洋是空的，结果整颗球看起来
     是一堆悬在太空里的点，而不是"点落在球面上"。
     补一层实心球面之后，点才有"贴在表面"的参照物。 */
  var DISC_VERT = [
    'attribute vec2 aQuad;',
    'uniform vec2 uScale;',
    'varying vec2 vP;',
    'void main() {',
    '  vP = aQuad;',
    '  gl_Position = vec4(aQuad * uScale, 0.0, 1.0);',
    '}'
  ].join('\n');

  var DISC_FRAG = [
    'precision mediump float;',
    'uniform vec3 uSunView;',
    'uniform vec3 uOceanDay;',
    'uniform vec3 uOceanNight;',
    'varying vec2 vP;',
    'void main() {',
    '  float r = length(vP);',
    '  if (r > 1.0) discard;',
    // 正交投影下，屏幕位置直接给出法线的 x/y，z 由单位球条件补出来
    '  float z = sqrt(max(0.0, 1.0 - r * r));',
    '  vec3 n = vec3(vP.x, vP.y, z);',
    // 太阳方向要转到**视图坐标系**：这里的法线是视图空间的
    '  float lit = smoothstep(-0.22, 0.18, dot(n, uSunView));',
    '  vec3 c = mix(uOceanNight, uOceanDay, lit);',
    // 边缘压暗一点，球感更强
    '  c *= 0.88 + 0.12 * z;',
    '  gl_FragColor = vec4(c, 1.0);',
    '}'
  ].join('\n');

  function compile(gl, type, src) {
    var s = gl.createShader(type);
    gl.shaderSource(s, src);
    gl.compileShader(s);
    if (!gl.getShaderParameter(s, gl.COMPILE_STATUS)) {
      throw new Error('着色器编译失败: ' + gl.getShaderInfoLog(s));
    }
    return s;
  }

  // 求让某个经纬度正对镜头的 yaw / pitch。
  //
  // 旋转 R = Rx(pitch) * Ry(yaw)，要求 R*v = (0,0,1)。
  // 解出来（v 的构造见 toVec，注意 z 那一项是负的）：
  //   cos(yaw) = -sin(λ), sin(yaw) = -cos(λ)  =>  yaw = -λ - π/2
  //   y 分量为 0                              =>  pitch = φ
  // 验算 λ=0：yaw=-π/2，把本初子午线转到正前方 ✓
  //      λ=90°E：yaw=-π，把 90°E 转到正前方 ✓
  // ⚠️ 改 toVec 的符号就必须同步改这里，两者是绑定的；只改一边会导致
  //    地球画得挺好但主机跑到边缘去（实测过一次）。
  function hubAngles(lat, lon) {
    return { yaw: -lon * DEG - Math.PI / 2, pitch: lat * DEG };
  }


  // 求一组地理点的"重心方向"（单位向量平均后归一化）。
  //
  // 只取方向、不关心距离：地球是球面，经纬度的算术平均在换日线附近会翻车。
  // 结果再转回经纬度，交给 hubAngles 得到相机角度。
  function centroidFocus(hub, places) {
    var sum = [0, 0, 0], n = 0, v;
    if (hub && hub.lat != null) {
      v = toVec(hub.lat, hub.lon, [0, 0, 0]);
      sum[0] += v[0]; sum[1] += v[1]; sum[2] += v[2]; n++;
    }
    for (var i = 0; places && i < places.length; i++) {
      if (places[i].lat == null) continue;
      v = toVec(places[i].lat, places[i].lon, [0, 0, 0]);
      sum[0] += v[0]; sum[1] += v[1]; sum[2] += v[2]; n++;
    }
    if (!n) return null;
    var len = Math.sqrt(sum[0] * sum[0] + sum[1] * sum[1] + sum[2] * sum[2]);
    if (len < 1e-6) return null;   // 点正好分布在对跖两侧，方向无意义
    sum[0] /= len; sum[1] /= len; sum[2] /= len;
    var lat = Math.asin(Math.max(-1, Math.min(1, sum[1]))) / DEG;
    var cp = Math.cos(lat * DEG);
    if (Math.abs(cp) < 1e-6) return { lat: lat, lon: 0 };
    // toVec 里 z = -cosφ·sinλ，所以反解要带负号
    var lon = Math.atan2(-sum[2] / cp, sum[0] / cp) / DEG;
    return { lat: lat, lon: lon };
  }


  // withAlpha 把 "#2f6feb" 这类颜色转成 "rgba(47,111,235,0.5)"。
  // 渐变渐隐要用：Canvas2D 的渐变色标只认 CSS 颜色字符串，不能直接给透明度参数。
  function withAlpha(hex, a) {
    var c = parseColor(hex, '#2f6feb');
    return 'rgba(' + Math.round(c[0] * 255) + ',' + Math.round(c[1] * 255) + ',' +
           Math.round(c[2] * 255) + ',' + a + ')';
  }

  // 缩放上下限。0.55 时点已经很密，2.6 时球会顶到画布边缘。
  var MIN_ZOOM = 0.55, MAX_ZOOM = 2.6;

  function parseColor(hex, fallback) {
    if (!hex) hex = fallback;
    hex = String(hex).trim().replace('#', '');
    if (hex.length === 3) hex = hex[0] + hex[0] + hex[1] + hex[1] + hex[2] + hex[2];
    var n = parseInt(hex, 16);
    if (isNaN(n)) n = parseInt(String(fallback).replace('#', ''), 16);
    return [((n >> 16) & 255) / 255, ((n >> 8) & 255) / 255, (n & 255) / 255];
  }

  /* ---------- 主类 ---------- */

  function KokoroGlobe(host, opts) {
    opts = opts || {};
    this.host = host;
    this.opts = opts;
    // 必须在这里就接住 opts.places。之前只写了 this.places = []，
    // 结果初始化时节点列表永远是空的——地球画得好好的，一条航线都没有。
    this.places = (opts.places || []).slice();
    this.hub = opts.hub || null;
    // 初始视角对准**所有节点的重心**。
    //
    // 为什么不是"对准主机"：主机在洛杉矶、小鸡在东京时，两者相距 79°，
    // 对准主机就等于把小鸡按在球的最边缘——那边正好是昼夜交界的浅色海面，
    // 点看起来就像"飘在球外面的空中"。对准重心后两者各距中心约 40°，
    // 都稳稳落在正面。
    //
    // 重心用**单位向量的平均**再归一化，不能直接对经纬度取平均：
    // 经度在 ±180 处会绕回来（东京 139°E 与洛杉矶 118°W 直接平均会得到
    // 一个错误的方向）。
    var focus = centroidFocus(opts.hub, opts.places);
    var init = focus ? hubAngles(focus.lat, focus.lon) : { yaw: -2.0, pitch: 0.42 };
    this.yaw = (opts.yaw != null ? opts.yaw : init.yaw);
    this.pitch = (opts.pitch != null ? opts.pitch : init.pitch);
    this.velYaw = 0;
    this.velPitch = 0;
    this.dragging = false;
    this.visible = true;
    this.dpr = Math.min(2, global.devicePixelRatio || 1);
    // 缩放倍数：滚轮调，作用在半径 R 上（见 _resize）。
    // 上下限不是随便定的：太小点会挤成一团，太大球会顶出画布被裁。
    this.zoom = 1;
    this.pulseAt = {};      // key -> 上次上报时间，用来跑脉冲
    this._raf = 0;
    this._destroyed = false;

    this._buildDom();
    this._loadLand();
    this._bind();
  }

  KokoroGlobe.prototype._buildDom = function () {
    var h = this.host;
    h.classList.add('kglobe');
    h.innerHTML = '';

    this.glCanvas = document.createElement('canvas');
    this.glCanvas.className = 'kglobe-gl';
    this.ovCanvas = document.createElement('canvas');
    this.ovCanvas.className = 'kglobe-ov';
    // 不再创建悬停提示节点：地球只是展示，节点的具体信息在卡片/详情页看。
    h.appendChild(this.glCanvas);
    h.appendChild(this.ovCanvas);

    this.ctx2d = this.ovCanvas.getContext('2d');
  };

  KokoroGlobe.prototype._loadLand = function () {
    var self = this;
    if (this.opts.landInline) {
      // 本地预览走这条路：file:// 下 fetch 会被 CORS 拦掉
      var bin = global.atob(this.opts.landInline);
      var buf = new Uint8Array(bin.length);
      for (var i = 0; i < bin.length; i++) buf[i] = bin.charCodeAt(i);
      self._onLand(buf);
      return;
    }
    var req = new XMLHttpRequest();
    req.open('GET', this.opts.landUrl || '/static/land.bin', true);
    req.responseType = 'arraybuffer';
    req.onload = function () {
      if (req.status >= 200 && req.status < 300 && req.response) {
        self._onLand(new Uint8Array(req.response));
      } else {
        self._fail('陆地掩码加载失败 HTTP ' + req.status);
      }
    };
    req.onerror = function () { self._fail('陆地掩码加载失败'); };
    req.send();
  };

  KokoroGlobe.prototype._fail = function (why) {
    if (this.host) {
      this.host.classList.add('kglobe-broken');
      this.host.setAttribute('data-error', why);
    }
    if (global.console) global.console.warn('[kglobe] ' + why);
  };

  KokoroGlobe.prototype._onLand = function (buf) {
    try {
      this.mask = decodeLand(buf);
    } catch (e) {
      this._fail(e.message);
      return;
    }
    this.dots = buildLandDots(this.mask);
    this._initGL();
    this._resize();
    this._start();
    if (this.opts.onReady) this.opts.onReady(this);
  };

  KokoroGlobe.prototype._initGL = function () {
    var attrs = { antialias: false, alpha: true, depth: false, powerPreference: 'low-power' };
    var gl = null;
    try {
      gl = this.glCanvas.getContext('webgl', attrs) ||
           this.glCanvas.getContext('experimental-webgl', attrs);
    } catch (e) { gl = null; }
    this.gl = gl;

    if (!gl) {
      // 降级：用 2D 画，点抽稀一半，信息量不减只是稀一点
      this.glCanvas.style.display = 'none';
      this.ctxDots = this.ovCanvas.getContext('2d');
      this.fallback = true;
      if (this.ovCanvas.parentNode) this.ovCanvas.classList.add('is-fallback');
      return;
    }

    var prog = gl.createProgram();
    gl.attachShader(prog, compile(gl, gl.VERTEX_SHADER, VERT));
    gl.attachShader(prog, compile(gl, gl.FRAGMENT_SHADER, FRAG));
    gl.linkProgram(prog);
    if (!gl.getProgramParameter(prog, gl.LINK_STATUS)) {
      this._fail('着色器链接失败: ' + gl.getProgramInfoLog(prog));
      this.gl = null;
      return;
    }
    gl.useProgram(prog);
    this.prog = prog;

    this.buf = gl.createBuffer();
    gl.bindBuffer(gl.ARRAY_BUFFER, this.buf);
    gl.bufferData(gl.ARRAY_BUFFER, this.dots, gl.STATIC_DRAW);

    this.loc = {
      pos: gl.getAttribLocation(prog, 'aPos'),
      rot: gl.getUniformLocation(prog, 'uRot'),
      size: gl.getUniformLocation(prog, 'uSize'),
      scale: gl.getUniformLocation(prog, 'uScale'),
      sun: gl.getUniformLocation(prog, 'uSun'),
      day: gl.getUniformLocation(prog, 'uDay'),
      night: gl.getUniformLocation(prog, 'uNight')
    };
    gl.enableVertexAttribArray(this.loc.pos);
    gl.vertexAttribPointer(this.loc.pos, 3, gl.FLOAT, false, 0, 0);

    // 球面程序
    var dprog = gl.createProgram();
    gl.attachShader(dprog, compile(gl, gl.VERTEX_SHADER, DISC_VERT));
    gl.attachShader(dprog, compile(gl, gl.FRAGMENT_SHADER, DISC_FRAG));
    gl.linkProgram(dprog);
    if (!gl.getProgramParameter(dprog, gl.LINK_STATUS)) {
      this._fail('球面着色器链接失败: ' + gl.getProgramInfoLog(dprog));
      this.gl = null;
      return;
    }
    this.dprog = dprog;
    this.dloc = {
      quad: gl.getAttribLocation(dprog, 'aQuad'),
      scale: gl.getUniformLocation(dprog, 'uScale'),
      sunView: gl.getUniformLocation(dprog, 'uSunView'),
      oceanDay: gl.getUniformLocation(dprog, 'uOceanDay'),
      oceanNight: gl.getUniformLocation(dprog, 'uOceanNight')
    };
    // 一个覆盖 [-1,1]^2 的四边形；圆形轮廓由片元着色器裁出来
    this.quadBuf = gl.createBuffer();
    gl.bindBuffer(gl.ARRAY_BUFFER, this.quadBuf);
    gl.bufferData(gl.ARRAY_BUFFER,
      new Float32Array([-1, -1, 1, -1, -1, 1, -1, 1, 1, -1, 1, 1]), gl.STATIC_DRAW);

    gl.disable(gl.DEPTH_TEST);
    gl.enable(gl.BLEND);
    gl.blendFunc(gl.SRC_ALPHA, gl.ONE_MINUS_SRC_ALPHA);
    gl.clearColor(0, 0, 0, 0);
  };

  KokoroGlobe.prototype._bind = function () {
    var self = this;
    var c = this.ovCanvas;

    this._onDown = function (ev) {
      self.dragging = true;
      self.velYaw = 0; self.velPitch = 0;
      self.lastX = ev.clientX; self.lastY = ev.clientY;
      c.setPointerCapture && c.setPointerCapture(ev.pointerId);
      c.classList.add('grabbing');
    };
    this._onMove = function (ev) {
      if (self.dragging) {
        var dx = ev.clientX - self.lastX, dy = ev.clientY - self.lastY;
        self.lastX = ev.clientX; self.lastY = ev.clientY;
        self.velYaw = dx * 0.006;
        // 拖拽方向：往**下**拖 = 把球面往下拉 = 上方（更北）的点转到正面，
        // 所以可见纬度**升高**，pitch 应该**变大**。
        // 原来写成 -dy 是反的（往下拖反而看到南半球）。
        self.velPitch = dy * 0.006;
        self.yaw += self.velYaw;
        self.pitch = clamp(self.pitch + self.velPitch, -1.35, 1.35);
      }
      // 不拖拽时什么都不做：地球没有悬停提示（见 _buildDom 的说明）。
      // 这里曾经调 _hover()，而那个方法已经删掉 —— 不清理的话
      // 鼠标每次在地球上移动都会抛 TypeError。
    };
    this._onUp = function (ev) {
      self.dragging = false;
      c.classList.remove('grabbing');
      c.releasePointerCapture && ev.pointerId != null && c.releasePointerCapture(ev.pointerId);
    };
    this._onLeave = function () {
      self.dragging = false;
    };

    c.addEventListener('pointerdown', this._onDown);
    c.addEventListener('pointermove', this._onMove);
    c.addEventListener('pointerup', this._onUp);
    c.addEventListener('pointercancel', this._onUp);
    c.addEventListener('pointerleave', this._onLeave);

    // 滚轮缩放。passive:false 才允许 preventDefault ——
    // 不拦住的话页面会跟着一起滚，缩放体验很糟。
    this._onWheel = function (ev) {
      if (!self.visible) return;
      var step = ev.deltaY < 0 ? 1.12 : 1 / 1.12;
      var next = clamp(self.zoom * step, MIN_ZOOM, MAX_ZOOM);
      // ⚠️ 到上下限时**不要** preventDefault：让页面正常滚动。
      // 否则鼠标一停在地球上就再也滚不动页面，非常难受。
      if (next === self.zoom) return;
      ev.preventDefault();
      self.zoom = next;
      self._resize();
    };
    c.addEventListener('wheel', this._onWheel, { passive: false });

    this._onResize = function () { self._resize(); };
    global.addEventListener('resize', this._onResize);

    // 滚出视口或切到别的标签页时停掉 rAF，别白烧 CPU
    if (global.IntersectionObserver) {
      this._io = new IntersectionObserver(function (es) {
        for (var i = 0; i < es.length; i++) self.visible = es[i].isIntersecting;
      }, { threshold: 0.01 });
      this._io.observe(this.host);
    }
    this._onVis = function () {
      if (!document.hidden) self._start();
    };
    document.addEventListener('visibilitychange', this._onVis);
  };

  KokoroGlobe.prototype._resize = function () {
    var r = this.host.getBoundingClientRect();
    this.w = Math.max(1, Math.round(r.width));
    this.h = Math.max(1, Math.round(r.height));
    var dpr = this.dpr;
    for (var i = 0; i < 2; i++) {
      var cv = i ? this.ovCanvas : this.glCanvas;
      cv.width = Math.round(this.w * dpr);
      cv.height = Math.round(this.h * dpr);
      cv.style.width = this.w + 'px';
      cv.style.height = this.h + 'px';
    }
    this.cx = this.w / 2;
    this.cy = this.h / 2;
    // 半径留 6% 余量，航线弧顶会甩出球面一点，不然会被画布裁掉
    // 半径留 6% 余量，航线弧顶会甩出球面一点，不然会被画布裁掉。
    // zoom 直接乘在半径上：点阵、球面、标记、航线全都走 R，
    // 所以缩放一处生效、各层不会错位。
    this.R = Math.min(this.w, this.h) / 2 * 0.88 * this.zoom;

    // ⚠️ 叠加层必须按 dpr 缩放。
    //
    // 画布的**实际像素**是 w*dpr × h*dpr，而绘制代码用的是 CSS 坐标
    // （cx = w/2、半径 R 都是 CSS 单位）。不设变换的话，所有 2D 绘制
    // 都会以 1/dpr 的比例落在偏左上角的位置。
    //
    // 表现：高分屏（Windows 显示缩放 125%/150%）上，航线、节点标记、主机
    // 全都不在球面上；地球一转，它们绕着一个更小、偏移了的圆心转，
    // 于是"点跑到球外面去了"。dpr=1 时完全看不出问题——
    // 这也是它在无头截图里一直复现不出来的原因。
    //
    // 注意顺序：给 canvas.width 赋值会**重置**上下文变换，
    // 所以这一句必须放在改完尺寸之后。
    if (this.ctx2d) this.ctx2d.setTransform(dpr, 0, 0, dpr, 0, 0);

    if (this.gl) this.gl.viewport(0, 0, this.glCanvas.width, this.glCanvas.height);
  };

  KokoroGlobe.prototype.setPlaces = function (cfg) {
    if (cfg && cfg.places) this.places = cfg.places.slice();
    if (cfg && cfg.hub) {
      this.hub = cfg.hub;
      if (cfg.focus) {
        var a = hubAngles(cfg.hub.lat, cfg.hub.lon);
        this.yaw = a.yaw; this.pitch = a.pitch;
      }
    }
  };

  // agent 上报时调一下，对应那条航线上会跑一个光点过去
  KokoroGlobe.prototype.pulse = function (key) {
    this.pulseAt[key] = performance.now();
  };

  KokoroGlobe.prototype._colors = function () {
    if (this.opts.colors) return this.opts.colors();
    return { day: '#93a4bd', night: '#3b465c', oceanDay: '#f2f6fb', oceanNight: '#d8dfea',
             arc: '#2f6feb', hub: '#2f6feb', ok: '#1a7f37', off: '#8b949e' };
  };

  KokoroGlobe.prototype._start = function () {
    if (this._raf || this._destroyed) return;
    var self = this;
    var reduce = global.matchMedia && global.matchMedia('(prefers-reduced-motion: reduce)').matches;
    this.reduce = reduce;
    function frame(t) {
      self._raf = global.requestAnimationFrame(frame);
      if (document.hidden || !self.visible) return;
      self._step(reduce, t);
    }
    this._raf = global.requestAnimationFrame(frame);
  };

  KokoroGlobe.prototype._step = function (reduce, now) {
    // 松手后的惯性 + 静止时的自动旋转
    if (!this.dragging) {
      if (Math.abs(this.velYaw) > 1e-4 || Math.abs(this.velPitch) > 1e-4) {
        this.yaw += this.velYaw;
        this.pitch = clamp(this.pitch + this.velPitch, -1.35, 1.35);
        this.velYaw *= 0.94;
        this.velPitch *= 0.94;
      } else if (!reduce) {
        this.yaw += 0.0011;
      }
    }
    this._drawGL();
    this._drawOverlay(now || performance.now());
  };

  KokoroGlobe.prototype._rotMatrix = function () {
    var cy = Math.cos(this.yaw), sy = Math.sin(this.yaw);
    var cx = Math.cos(this.pitch), sx = Math.sin(this.pitch);
    // 先绕 Y 偏航，再绕 X 俯仰 —— 展开成一个 mat3（列主序）
    // R = Rx * Ry
    return new Float32Array([
      cy,        sx * sy,   -cx * sy,
      0,         cx,         sx,
      sy,       -sx * cy,    cx * cy
    ]);
  };

  KokoroGlobe.prototype._drawGL = function () {
    var gl = this.gl;
    if (!gl) return;
    var c = this._colors();
    gl.clear(gl.COLOR_BUFFER_BIT);

    var rot = this._rotMatrix();
    var sun = this.opts.sun || subsolar(new Date());
    var sv = toVec(sun.lat, sun.lon, [0, 0, 0]);
    // 球面片元的法线在**视图空间**，所以太阳也要转过去
    var svv = rotate(sv, this.yaw, this.pitch, [0, 0, 0]);

    // 先画实心球面（海洋），再画陆地点——顺序不能反，
    // 否则球面会把点盖住。
    if (this.dprog) {
      gl.useProgram(this.dprog);
      gl.bindBuffer(gl.ARRAY_BUFFER, this.quadBuf);
      gl.enableVertexAttribArray(this.dloc.quad);
      gl.vertexAttribPointer(this.dloc.quad, 2, gl.FLOAT, false, 0, 0);
      gl.uniform2f(this.dloc.scale, 2 * this.R / this.w, 2 * this.R / this.h);
      gl.uniform3f(this.dloc.sunView, svv[0], svv[1], svv[2]);
      gl.uniform3fv(this.dloc.oceanDay, parseColor(c.oceanDay, '#f2f6fb'));
      gl.uniform3fv(this.dloc.oceanNight, parseColor(c.oceanNight, '#d8dfea'));
      gl.drawArrays(gl.TRIANGLES, 0, 6);
    }

    gl.useProgram(this.prog);
    gl.bindBuffer(gl.ARRAY_BUFFER, this.buf);
    gl.enableVertexAttribArray(this.loc.pos);
    gl.vertexAttribPointer(this.loc.pos, 3, gl.FLOAT, false, 0, 0);
    gl.uniformMatrix3fv(this.loc.rot, false, rot);

    // opts.sun 可以用来钉死太阳方向，方便做受控验证（比如"把太阳放到主机
    // 正上方，中心必须是亮的"）。生产不传，按当前时间算。
    gl.uniform3f(this.loc.sun, sv[0], sv[1], sv[2]);
    gl.uniform3fv(this.loc.day, parseColor(c.day, '#c9d4e2'));
    gl.uniform3fv(this.loc.night, parseColor(c.night, '#39435a'));
    gl.uniform1f(this.loc.size, Math.max(1.1, this.R / 108) * this.dpr);
    gl.uniform2f(this.loc.scale, 2 * this.R / this.w, 2 * this.R / this.h);

    gl.drawArrays(gl.POINTS, 0, this.dots.length / 3);
  };

  KokoroGlobe.prototype._project = function (lat, lon) {
    var v = rotate(toVec(lat, lon, [0, 0, 0]), this.yaw, this.pitch, [0, 0, 0]);
    return { x: this.cx + v[0] * this.R, y: this.cy - v[1] * this.R, z: v[2] };
  };

  // 两点之间的大圆，采样后逐点旋转投影。返回若干段（跨到背面时断开）。
  KokoroGlobe.prototype._arc = function (a, b, n) {
    n = n || 64;
    var p1 = [a.lat * DEG, a.lon * DEG], p2 = [b.lat * DEG, b.lon * DEG];
    var v1 = toVec(a.lat, a.lon, [0, 0, 0]), v2 = toVec(b.lat, b.lon, [0, 0, 0]);
    var d = clamp(v1[0] * v2[0] + v1[1] * v2[1] + v1[2] * v2[2], -1, 1);
    var ang = Math.acos(d);
    var segs = [], cur = [];
    var sd = Math.sin(ang);
    for (var i = 0; i <= n; i++) {
      var t = i / n, v;
      if (ang < 1e-6) {
        v = v1;
      } else {
        var A = Math.sin((1 - t) * ang) / sd, B = Math.sin(t * ang) / sd;
        v = [A * v1[0] + B * v2[0], A * v1[1] + B * v2[1], A * v1[2] + B * v2[2]];
      }
      var r = rotate(v, this.yaw, this.pitch, [0, 0, 0]);
      // 弧线**紧贴球面**——之前加 lift 让弧线微微拱起（约 3%），结果
      // 沿弧线跑的画飞艇（传输特效）在屏幕上读起来像在球壳外面绕。
      // 现在 lift=1，弧线和飞艇都贴着陆地表面走，方向清清楚楚。
      var pt = { x: this.cx + r[0] * this.R, y: this.cy - r[1] * this.R, z: r[2] };
      // 背面（z<0）断开，否则会看到线从球里穿出来
      if (pt.z < -0.02) {
        if (cur.length > 1) segs.push(cur);
        cur = [];
        continue;
      }
      cur.push(pt);
    }
    if (cur.length > 1) segs.push(cur);
    return { segs: segs, ang: ang };
  };

  KokoroGlobe.prototype._drawOverlay = function (now) {
    var g = this.ctx2d;
    if (!g) return;
    var c = this._colors();
    // 清屏用 CSS 尺寸即可：上面已经设过 dpr 变换，坐标系是 CSS 的
    g.clearRect(0, 0, this.w, this.h);

    if (this.fallback) this._drawFallbackDots(g, c);

    // ⚠️ 这里不画球体的边光/描边环，也不画雷达波。
    // 详见 _resize 上方与本函数中的说明：点阵地球靠点的分布暗示球，不靠描边。

    var hub = this.hub;

    // ---- 航线 ----
    //
    // 设计原则：**只保留一个动效**。
    // 之前同时有"虚线流动"+"亮线跑"+"光点飞"，三套叠在一起互相打架，
    // 看着又乱又廉价。现在简化成：
    //   静态发丝底纹（表示链路存在）
    //   + 一颗**彗星**从节点飞向主机（表示数据在传）
    // 彗星尾巴渐隐、头端最亮带一点辉光，是那种"一条能量流过光纤"的感觉。
    if (hub) {
      var hubP = this._project(hub.lat, hub.lon);
      for (var i = 0; i < this.places.length; i++) {
        var p = this.places[i];
        if (p.lat == null || p.lon == null) continue;
        var arc = this._arc(hub, p);
        if (!arc.segs.length) continue;

        var on = p.online !== false;
        var col = on ? (c.arc || '#2f6feb') : (c.off || '#8b949e');
        var np = this._project(p.lat, p.lon);

        // 底纹：一条很淡的发丝线，从主机向节点渐隐
        g.save();
        g.lineCap = 'round';
        g.lineWidth = 1;
        g.strokeStyle = (function () {
          var grd = g.createLinearGradient(hubP.x, hubP.y, np.x, np.y);
          grd.addColorStop(0, withAlpha(col, on ? 0.55 : 0.30));
          grd.addColorStop(1, withAlpha(col, on ? 0.14 : 0.10));
          return grd;
        })();
        this._strokeSegs(g, arc.segs);
        g.restore();

        if (!on || this.reduce) continue;

        // 彗星：把各段拼成一条有序点列，再在上面取一小段窗口
        var pts = [];
        for (var si = 0; si < arc.segs.length; si++) {
          pts = pts.concat(arc.segs[si]);
        }
        if (pts.length < 3) continue;

        var n = pts.length;
        // 错开一点相位，免得所有彗星整齐地一起跑
        var phase = ((now / 2600) + i * 0.17) % 1;
        // 方向：节点 -> 主机。_arc 是从主机画到节点，所以头端从末尾往回走。
        var head = (1 - phase) * (n - 1);
        var tail = 18;

        g.save();
        g.lineCap = 'round';
        g.strokeStyle = col;
        for (var k = tail; k >= 1; k--) {
          var idx = Math.floor(head) + k;
          if (idx < 1 || idx >= n) continue;
          var a = 1 - k / (tail + 1);
          g.globalAlpha = a * a;
          g.lineWidth = 0.6 + a * 2.6;
          g.beginPath();
          g.moveTo(pts[idx - 1].x, pts[idx - 1].y);
          g.lineTo(pts[idx].x, pts[idx].y);
          g.stroke();
        }
        // 头端：一颗带辉光的小亮点
        var hi = Math.max(0, Math.min(n - 1, Math.round(head)));
        var hp = pts[hi];
        g.globalAlpha = 1;
        g.shadowColor = col;
        g.shadowBlur = 10;
        g.fillStyle = col;
        g.beginPath();
        g.arc(hp.x, hp.y, 2.6, 0, Math.PI * 2);
        g.fill();
        g.restore();
      }
    }

    // ---- 节点 ----
    // 干净的圆点，不带大辉光。上报到来时外扩一圈很淡的环（克制版"心跳"），
    // 取代原来的"光点沿线飞过去"。
    for (var j = 0; j < this.places.length; j++) {
      var q = this.places[j];
      if (q.lat == null || q.lon == null) continue;
      var sp = this._project(q.lat, q.lon);
      if (sp.z <= 0) continue;
      var online = q.online !== false;
      var dotCol = online ? (c.ok || '#1a7f37') : (c.off || '#8b949e');

      var last = this.pulseAt[q.key];
      if (last != null) {
        var e = (now - last) / 800;
        if (e >= 0 && e <= 1) {
          g.save();
          g.globalAlpha = (1 - e) * 0.45;
          g.strokeStyle = dotCol;
          g.lineWidth = 1;
          g.beginPath();
          g.arc(sp.x, sp.y, 3 + e * 9, 0, Math.PI * 2);
          g.stroke();
          g.restore();
        } else if (e > 1) {
          delete this.pulseAt[q.key];
        }
      }

      g.save();
      g.fillStyle = dotCol;
      g.beginPath();
      g.arc(sp.x, sp.y, online ? 2.8 : 2.2, 0, Math.PI * 2);
      g.fill();
      g.restore();
    }

    // ---- 主机 ----
    // 换掉了原来的三圈扩散雷达波。现在是一个小圆点 + 一层很淡的静态光晕，
    // 光晕只有极缓慢的呼吸（±6%），安静但能看出"这里是中心"。
    if (hub) {
      var hp2 = this._project(hub.lat, hub.lon);
      if (hp2.z > 0) {
        var breath = 1 + 0.06 * Math.sin(now / 2600);
        var halo = g.createRadialGradient(hp2.x, hp2.y, 0, hp2.x, hp2.y, 16 * breath);
        halo.addColorStop(0, withAlpha(c.hub || '#2f6feb', 0.28));
        halo.addColorStop(1, withAlpha(c.hub || '#2f6feb', 0));
        g.fillStyle = halo;
        g.beginPath();
        g.arc(hp2.x, hp2.y, 16 * breath, 0, Math.PI * 2);
        g.fill();

        g.save();
        g.fillStyle = c.hub || '#2f6feb';
        g.beginPath();
        g.arc(hp2.x, hp2.y, 3.4, 0, Math.PI * 2);
        g.fill();
        g.restore();
      }
    }
  };

  KokoroGlobe.prototype._strokeSegs = function (g, segs) {
    for (var s = 0; s < segs.length; s++) {
      var pts = segs[s];
      g.beginPath();
      g.moveTo(pts[0].x, pts[0].y);
      for (var i = 1; i < pts.length; i++) g.lineTo(pts[i].x, pts[i].y);
      g.stroke();
    }
  };

  // 按弧长参数 [t0, t1] 描一段（用来画"流动的那一截"）
  // 无 WebGL 时的点阵降级：从掩码每隔一格取一点
  KokoroGlobe.prototype._drawFallbackDots = function (g, c) {
    if (!this.mask) return;
    if (!this._fbCache) {
      var m = this.mask, out = [];
      for (var row = 0; row < m.nlat; row += 2) {
        var lat = m.latMax - (row + 0.5) * m.step;
        for (var col = 0; col < m.nlon; col += 2) {
          if (!isLand(m, col, row)) continue;
          out.push(lat, -180 + (col + 0.5) * m.step);
        }
      }
      this._fbCache = out;
    }
    // 降级路径也要先铺一层球面，否则同样会像"一堆飘着的点"
    var oc = g.createRadialGradient(this.cx - this.R * 0.3, this.cy - this.R * 0.3, this.R * 0.1,
                                    this.cx, this.cy, this.R);
    oc.addColorStop(0, c.oceanDay || '#f2f6fb');
    oc.addColorStop(1, c.oceanNight || '#d8dfea');
    g.fillStyle = oc;
    g.beginPath();
    g.arc(this.cx, this.cy, this.R, 0, Math.PI * 2);
    g.fill();

    var sun = this.opts.sun || subsolar(new Date());
    var sunV = toVec(sun.lat, sun.lon, [0, 0, 0]);
    var pts = this._fbCache;
    for (var i = 0; i < pts.length; i += 2) {
      var v = rotate(toVec(pts[i], pts[i + 1], [0, 0, 0]), this.yaw, this.pitch, [0, 0, 0]);
      if (v[2] <= 0) continue;
      var lit = v[0] * sunV[0] + v[1] * sunV[1] + v[2] * sunV[2];
      g.fillStyle = lit > 0 ? (c.day || '#c9d4e2') : (c.night || '#39435a');
      g.fillRect(this.cx + v[0] * this.R, this.cy - v[1] * this.R, 1.6, 1.6);
    }
  };

  /* ---------- 悬停提示 ---------- */


  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"]/g, function (ch) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[ch];
    });
  }

  KokoroGlobe.prototype.destroy = function () {
    this._destroyed = true;
    if (this._raf) global.cancelAnimationFrame(this._raf);
    if (this._io) this._io.disconnect();
    global.removeEventListener('resize', this._onResize);
    if (this.ovCanvas) this.ovCanvas.removeEventListener('wheel', this._onWheel);
    document.removeEventListener('visibilitychange', this._onVis);
    if (this.host) this.host.innerHTML = '';
  };

  global.KokoroGlobe = KokoroGlobe;
})(window);
