/* Kokoro 探针 —— 平滑地图地球。
 *
 * 无任何外部依赖，纯手写 WebGL + Canvas2D。用它的理由：
 *   - 项目硬约束是「单二进制、无 CDN」，three.js / d3-geo 这类库都不能引；
 *   - 立体地球比平面地图更能表达「节点分布在全球」，而且昼夜线天然有时效感。
 *
 * 分工：
 *   WebGL 以抗锯齿纹理填充陆地和海洋，Canvas2D 只叠加静态节点标记。
 *
 * 无 WebGL 时自动降级到 Canvas2D 像素地图，不使用点阵。
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

  // 将紧凑掩码展开为无重复采样的单通道纹理。线性过滤会柔化海岸线，
  // 连续色块替代屏幕点阵，避免高 DPI 屏幕产生摩尔纹。
  //
  // 单通道要表达三态，用三个灰度档位（着色器按阈值拆开）：
  //   0   = 海洋
  //   140 = 陆地
  //   255 = "点亮区域"（这个国家有节点）
  // 之所以不打成 0/1 二进制，是因为还要保住海岸线的线性插值过渡。
  function landTextureData(m, lit) {
    var pixels = new Uint8Array(m.nlon * m.nlat);
    for (var y = 0; y < m.nlat; y++) {
      var row = m.nlat - 1 - y;
      for (var x = 0; x < m.nlon; x++) {
        if (!isLand(m, x, row)) continue;
        var i = y * m.nlon + x;
        pixels[i] = (lit && lit[i]) ? 255 : 140;
      }
    }
    return pixels;
  }

  // 把 "start:len,start:len" 展开成逐格标记。索引是纹理序
  // （与 landTextureData 的遍历顺序一致），所以不用再做行列换算。
  function decodeLitRuns(s, total) {
    if (!s) return null;
    var out = new Uint8Array(total);
    var parts = String(s).split(',');
    for (var i = 0; i < parts.length; i++) {
      var p = parts[i].split(':');
      var start = parseInt(p[0], 10);
      var n = parseInt(p[1], 10);
      if (!isFinite(start) || !isFinite(n) || n <= 0) continue;
      var end = Math.min(total, start + n);
      for (var k = start < 0 ? 0 : start; k < end; k++) out[k] = 1;
    }
    return out;
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
    'uniform vec3 uLandDay;',
    'uniform vec3 uLandNight;',
    'uniform vec3 uLitDay;',
    'uniform vec3 uLitNight;',
    'uniform float uYaw;',
    'uniform float uPitch;',
    'uniform float uLatMin;',
    'uniform float uLatMax;',
    'uniform sampler2D uLand;',
    'varying vec2 vP;',
    'void main() {',
    '  float r = length(vP);',
    '  if (r > 1.0) discard;',
    // 正交投影下，屏幕位置直接给出法线的 x/y，z 由单位球条件补出来
    '  float z = sqrt(max(0.0, 1.0 - r * r));',
    '  vec3 nView = vec3(vP.x, vP.y, z);',
    '  float cp = cos(uPitch), sp = sin(uPitch);',
    '  float cy = cos(uYaw), sy = sin(uYaw);',
    '  float y1 = nView.y * cp + nView.z * sp;',
    '  float z1 = -nView.y * sp + nView.z * cp;',
    '  vec3 n = vec3(nView.x * cy - z1 * sy, y1, nView.x * sy + z1 * cy);',
    // 太阳方向要转到**视图坐标系**：这里的法线是视图空间的
    '  float lit = smoothstep(-0.2, 0.24, dot(nView, uSunView));',
    '  float lat = asin(clamp(n.y, -1.0, 1.0));',
    '  float lon = atan(-n.z, n.x);',
    '  vec2 uv = vec2(lon / 6.2831853 + 0.5, (lat - uLatMin) / (uLatMax - uLatMin));',
    '  float landSample = 0.0;',
    '  if (uv.y >= 0.0 && uv.y <= 1.0) landSample = texture2D(uLand, uv).r;',
    // 三态拆解：0=海、0.55=陆、1=点亮。阈值按线性插值的 50% 点选，
    // 这样和原来"陆地=1、海=0"的二分版相比海岸线位置不变。
    '  float land = smoothstep(0.19, 0.36, landSample);',
    '  float litArea = smoothstep(0.68, 0.88, landSample);',
    '  vec3 ocean = mix(uOceanNight, uOceanDay, lit);',
    '  vec3 continent = mix(uLandNight, uLandDay, lit);',
    '  vec3 c = mix(ocean, continent, land);',
    // 点亮区域：有节点的国家整片换成高亮色。
    // 阈值必须跟 landTextureData 的 140/255 档位对齐（140/255≈0.55）。
    '  vec3 litCol = mix(uLitNight, uLitDay, lit);',
    '  c = mix(c, litCol, litArea);',
    '  c *= 0.9 + 0.1 * z;',
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


  // 航线的大圆采样点只跟两端经纬度有关，和镜头角度无关。
  // 预计算一次，每帧只做旋转 + 投影；否则动画循环里每条航线都要重算
  // acos/sin，节点一多就会把手机拖到掉帧。
  var ROUTE_SAMPLES = 48;

  function buildRoute(a, b, n) {
    var v1 = toVec(a.lat, a.lon, [0, 0, 0]);
    var v2 = toVec(b.lat, b.lon, [0, 0, 0]);
    var d = clamp(v1[0] * v2[0] + v1[1] * v2[1] + v1[2] * v2[2], -1, 1);
    var ang = Math.acos(d);
    var sd = Math.sin(ang);
    var vs = new Float32Array((n + 1) * 3);
    for (var i = 0; i <= n; i++) {
      var t = i / n, x, y, z;
      if (ang < 1e-6 || sd < 1e-6) {
        // 对跖点附近大圆不唯一，退化成线性插值再归一化。
        x = v1[0] + (v2[0] - v1[0]) * t;
        y = v1[1] + (v2[1] - v1[1]) * t;
        z = v1[2] + (v2[2] - v1[2]) * t;
        var len = Math.sqrt(x * x + y * y + z * z) || 1;
        x /= len; y /= len; z /= len;
      } else {
        var A = Math.sin((1 - t) * ang) / sd, B = Math.sin(t * ang) / sd;
        x = A * v1[0] + B * v2[0];
        y = A * v1[1] + B * v2[1];
        z = A * v1[2] + B * v2[2];
      }
      vs[i * 3] = x; vs[i * 3 + 1] = y; vs[i * 3 + 2] = z;
    }
    return { vs: vs, n: n };
  }

  var DEFAULT_COLORS = {
    day: '#93a4bd', night: '#3b465c', oceanDay: '#f2f6fb', oceanNight: '#d8dfea',
    arc: '#2f6feb', hub: '#2f6feb', ok: '#1a7f37', off: '#8b949e'
  };


  // withAlpha 把 "#2f6feb" 这类颜色转成 "rgba(47,111,235,0.5)"。
  // 渐变渐隐要用：Canvas2D 的渐变色标只认 CSS 颜色字符串，不能直接给透明度参数。
  // 同一主题下颜色和透明度组合是有限的，缓存起来避免每帧重复解析颜色。
  var alphaCache = Object.create(null);

  function withAlpha(hex, a) {
    var key = (hex || '') + '|' + a;
    var hit = alphaCache[key];
    if (hit) return hit;
    var c = parseColor(hex, '#2f6feb');
    return alphaCache[key] = 'rgba(' + Math.round(c[0] * 255) + ',' + Math.round(c[1] * 255) + ',' +
           Math.round(c[2] * 255) + ',' + a + ')';
  }

  // 缩放上下限。0.55 时点已经很密，2.6 时球会顶到画布边缘。
  var MIN_ZOOM = 0.55, MAX_ZOOM = 2.6;

  function parseColor(hex, fallback) {
    if (!hex) hex = fallback;
    hex = String(hex).trim();
    var rgb = hex.match(/^rgba?\(\s*(\d+(?:\.\d+)?)\s*,\s*(\d+(?:\.\d+)?)\s*,\s*(\d+(?:\.\d+)?)/i);
    if (rgb) return [clamp(parseFloat(rgb[1]) / 255, 0, 1), clamp(parseFloat(rgb[2]) / 255, 0, 1), clamp(parseFloat(rgb[3]) / 255, 0, 1)];
    hex = hex.replace('#', '');
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
    this.effects = Object.assign({
      showRoutes: true, showFlow: true, autoRotate: true,
      routeOpacity: .38, routeMutedOpacity: .17, routeWidth: .85,
      routeUnderlayOpacity: .1, routeUnderlayWidth: 2.8,
      flowPeriod: 6100, flowLength: .23, flowOpacity: 1, flowGlow: 7,
      flowGlowWidth: 4.2, flowCoreWidth: 1.9, flowHeadSize: 2.25,
      flowGlintSize: 1.25, flowPulseWindow: 1800,
      nodeSize: 2.1, nodeMutedSize: 1.7, nodeRingSize: 3.4,
      nodeMutedRingSize: 2.8, nodeHaloSize: 6.2,
      hubSize: 2.1, hubRingSize: 5, hubRingWidth: 1.5, rotationSpeed: 1
    }, opts.effects || {});
    // 必须在这里就接住 opts.places。之前只写了 this.places = []，
    // 结果初始化时节点列表永远是空的——地球画得好好的，一条航线都没有。
    this.places = (opts.places || []).slice();
    // "点亮区域"：服务端算好的格子 run（纹理序）。为空就是纯陆地配色。
    this.litRuns = opts.litRuns || '';
    this.hub = opts.hub || null;
    // 航线几何只随节点/主机变化，预计算后每帧复用。
    this.routes = [];
    this._buildRoutes();
    this._cachedColors = null;
    this._colorsAt = 0;
    this._rgbCache = Object.create(null);
    this._reducedMotion = !!(global.matchMedia && global.matchMedia('(prefers-reduced-motion: reduce)').matches);
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

    this.labelLayer = document.createElement('div');
    this.labelLayer.className = 'globe-labels';
    h.appendChild(this.labelLayer);
    this.nodeLabels = {};
    this.labelMode = 'labels';

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

    // 球面与海岸线使用同一张平滑纹理，不再绘制密集圆点。
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
      oceanNight: gl.getUniformLocation(dprog, 'uOceanNight'),
      landDay: gl.getUniformLocation(dprog, 'uLandDay'),
      landNight: gl.getUniformLocation(dprog, 'uLandNight'),
      litDay: gl.getUniformLocation(dprog, 'uLitDay'),
      litNight: gl.getUniformLocation(dprog, 'uLitNight'),
      yaw: gl.getUniformLocation(dprog, 'uYaw'),
      pitch: gl.getUniformLocation(dprog, 'uPitch'),
      latMin: gl.getUniformLocation(dprog, 'uLatMin'),
      latMax: gl.getUniformLocation(dprog, 'uLatMax'),
      land: gl.getUniformLocation(dprog, 'uLand')
    };
    // 一个覆盖 [-1,1]^2 的四边形；圆形轮廓由片元着色器裁出来
    this.quadBuf = gl.createBuffer();
    gl.bindBuffer(gl.ARRAY_BUFFER, this.quadBuf);
    gl.bufferData(gl.ARRAY_BUFFER,
      new Float32Array([-1, -1, 1, -1, -1, 1, -1, 1, 1, -1, 1, 1]), gl.STATIC_DRAW);

    this.landTex = gl.createTexture();
    gl.bindTexture(gl.TEXTURE_2D, this.landTex);
    gl.pixelStorei(gl.UNPACK_ALIGNMENT, 1);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
    this._uploadLand();

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
        self._render();
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

    // 容器尺寸变了就重算绘制分辨率。
    //
    // 为什么不能只靠 window 的 resize：.globe 的高度来自 CSS
    // （.home-band 的 --band-h 含 vh，窄屏还走另一套媒体查询），
    // 任何「容器变了但窗口没变」的情况都不会发 window resize ——
    // 比如字体加载把卡片撑高、媒体查询切换、父级 margin/边框调整。
    // 那种时候画布就与容器脱节了（正是"地球被截断"的成因）。
    // ResizeObserver 盯的是元素本身，这类变化一个都跑不掉。
    if (global.ResizeObserver) {
      this._ro = new global.ResizeObserver(function () { self._resize(); });
      this._ro.observe(this.host);
    }

    // 滚出视口或切到别的标签页时停掉 rAF，别白烧 CPU
    function pauseBackdrop() {
      var body = self.host.closest('.globe-body');
      if (body) body.classList.toggle('is-motion-paused', document.hidden || !self.visible);
    }
    if (global.IntersectionObserver) {
      this._io = new IntersectionObserver(function (es) {
        for (var i = 0; i < es.length; i++) {
          self.visible = es[i].isIntersecting;
          pauseBackdrop();
          if (self.visible) self._start();
          else if (self._raf) { global.cancelAnimationFrame(self._raf); self._raf = 0; }
        }
      }, { threshold: 0.01 });
      this._io.observe(this.host);
    }
    this._onVis = function () {
      pauseBackdrop();
      if (!document.hidden) self._start();
      else if (self._raf) { global.cancelAnimationFrame(self._raf); self._raf = 0; }
    };
    document.addEventListener('visibilitychange', this._onVis);
    pauseBackdrop();
  };

  KokoroGlobe.prototype._resize = function () {
    var r = this.host.getBoundingClientRect();
    // 容器塌成 0（还没布局好、或被 display:none 藏起来）时直接跳过：
    // 设成 1x1 会让地球缩成一个点，而之后尺寸恢复时未必有第二次机会。
    // 漏掉的那次由 ResizeObserver 补上。
    if (r.width < 2 || r.height < 2) return;
    this.w = Math.max(1, Math.round(r.width));
    this.h = Math.max(1, Math.round(r.height));
    // dpr 每次都重读：把窗口拖到缩放比例不同的另一块显示器时它会变，
    // 只在构造时取一次的话，换屏之后点阵会糊。
    this.dpr = Math.min(2, global.devicePixelRatio || 1);
    var dpr = this.dpr;
    for (var i = 0; i < 2; i++) {
      var cv = i ? this.ovCanvas : this.glCanvas;
      cv.width = Math.round(this.w * dpr);
      cv.height = Math.round(this.h * dpr);
      // ⚠️ **这里不要再写 cv.style.width / cv.style.height**。
      //
      // 画布的「显示尺寸」交给 CSS（.globe canvas { width:100%; height:100% }），
      // 它永远等于容器尺寸；这个函数只管「绘制分辨率」（canvas.width/height）。
      //
      // 反面教材（2026-10-10 boss 报的 bug）：一旦 JS 把 style 宽高写死，
      // 容器后来变矮（窗口改小、--band-h 的 vh 变化、字体加载把卡片撑高…）
      // 而 window 没发 resize 时，画布就停在旧高度上，多出来的部分被
      // .globe 的 overflow:hidden 裁掉 —— 表现成"地球下半部分被截断"。
      //
      // 交给 CSS 之后，哪怕这一帧的 backing 尺寸暂时落后，浏览器也只是
      // 把位图整体缩放一下（短暂发糊），**绝不会裁掉内容**；
      // 下一帧 ResizeObserver 就会把分辨率纠正回来。
    }
    this.cx = this.w / 2;
    this.cy = this.h / 2;
    // 半径留 6% 余量，航线弧顶会甩出球面一点，不然会被画布裁掉
    // 半径留 6% 余量，航线弧顶会甩出球面一点，不然会被画布裁掉。
    // zoom 直接乘在半径上：点阵、球面、标记、航线全都走 R，
    // 所以缩放一处生效、各层不会错位。
    // Use the shared card body as the size reference in both annotation modes.
    // Reserve the list's width even while labels are visible so toggling only
    // moves the globe horizontally; it never changes its apparent diameter.
    var body = this.host.closest('.globe-body');
    var bodyWidth = body ? body.clientWidth : this.w;
    this.R = Math.min(bodyWidth * .31, this.h * .42, bodyWidth * .28) * this.zoom;

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
    this._render();
  };

  KokoroGlobe.prototype._buildRoutes = function () {
    this.routes = [];
    if (!this.hub) return;
    for (var i = 0; i < this.places.length; i++) {
      var p = this.places[i];
      if (!p || p.lat == null || p.lon == null) { this.routes.push(null); continue; }
      this.routes.push(buildRoute(this.hub, p, ROUTE_SAMPLES));
    }
  };

  KokoroGlobe.prototype.setPlaces = function (cfg) {
    var routesDirty = false;
    if (cfg && cfg.places) {
      this.places = cfg.places.slice();
      routesDirty = true;
    }
    // 点亮区域只在"哪些国家有节点"变化时才重传纹理。
    if (cfg && cfg.litRuns !== undefined && cfg.litRuns !== this.litRuns) {
      this.litRuns = cfg.litRuns || '';
      this._uploadLand();
    }
    if (cfg && cfg.hub) {
      this.hub = cfg.hub;
      routesDirty = true;
      if (cfg.focus) {
        var a = hubAngles(cfg.hub.lat, cfg.hub.lon);
        this.yaw = a.yaw; this.pitch = a.pitch;
      }
    }
    if (routesDirty) this._buildRoutes();
    this._render();
  };

  // agent 上报时调一下，对应那条航线上会跑一个光点过去
  KokoroGlobe.prototype.pulse = function (key) {
    this.pulseAt[key] = performance.now();
  };

  // 主题色来自 CSS 变量，而读一个变量要走"插探针元素 + getComputedStyle"，
  // 也就是一次强制样式重算。绘制一帧要在按节点、按航线、按标签的循环里反复问
  // 颜色（见 _drawFlow：每个地点一次），一帧能问出几十上百次重算——整页会
  // 因此掉到个位数帧率，滚动、切主题都跟着卡。
  //
  // 所以一帧只算一次：_render / _step 开头清缓存，同一帧内所有调用复用同一份。
  // 颜色本身只在切换主题（整页重载）或改 token 时才会变，一帧的粒度足够。
  KokoroGlobe.prototype._colors = function () {
    if (this._frameColors) return this._frameColors;
    if (this.opts.colors) {
      // 颜色读取要走 getComputedStyle，代价不低；一帧问一次还嫌多。
      // 缓存一秒足够跟上主题切换，又不会把样式重算压在每一帧上。
      var now = global.performance && global.performance.now ? global.performance.now() : Date.now();
      if (!this._cachedColors || now - this._colorsAt > 1000) {
        this._cachedColors = this.opts.colors();
        this._colorsAt = now;
        this._rgbCache = Object.create(null);
      }
      this._frameColors = this._cachedColors;
    } else {
      this._frameColors = DEFAULT_COLORS;
    }
    return this._frameColors;
  };

  // 主题面板改 token 后可以主动清缓存，不用等下一秒。
  KokoroGlobe.prototype.refreshColors = function () {
    this._cachedColors = null;
    this._render();
  };

  KokoroGlobe.prototype._rgb = function (hex, fallback) {
    var key = hex || fallback || '';
    var v = this._rgbCache[key];
    if (!v) {
      v = parseColor(hex, fallback);
      this._rgbCache[key] = v;
    }
    return v;
  };

  // 一帧只算一次 sin/cos。之前每个投影点都调 rotate()，那里面又有 4 次
  // 三角函数——节点一多，光算角度就能吃掉大半帧。
  KokoroGlobe.prototype._setView = function () {
    this._cosYaw = Math.cos(this.yaw); this._sinYaw = Math.sin(this.yaw);
    this._cosPitch = Math.cos(this.pitch); this._sinPitch = Math.sin(this.pitch);
  };

  KokoroGlobe.prototype._rotateVec = function (v, out) {
    var x1 = v[0] * this._cosYaw + v[2] * this._sinYaw;
    var z1 = -v[0] * this._sinYaw + v[2] * this._cosYaw;
    out[0] = x1;
    out[1] = v[1] * this._cosPitch - z1 * this._sinPitch;
    out[2] = v[1] * this._sinPitch + z1 * this._cosPitch;
    return out;
  };

  KokoroGlobe.prototype._start = function () {
    if (this._destroyed || !this.visible || document.hidden) return;
    this._render();
    var reduced = global.matchMedia && global.matchMedia('(prefers-reduced-motion: reduce)').matches;
    this._reducedMotion = !!reduced;
    var animate = this.effects.autoRotate || (this.effects.showRoutes && this.effects.showFlow && !!this.hub);
    if (this._raf || reduced || !animate) return;
    var self = this;
    function tick(now) {
      self._raf = 0;
      if (self._destroyed || !self.visible || document.hidden) return;
      self._step(false, now);
      self._raf = global.requestAnimationFrame(tick);
    }
    this._raf = global.requestAnimationFrame(tick);
  };

  KokoroGlobe.prototype._render = function () {
    if (this._destroyed || !this.w || !this.h) return;
    this._frameColors = null;
    this._drawGL();
    this._drawOverlay(performance.now());
  };

  KokoroGlobe.prototype._step = function (reduce, now) {
    this._frameColors = null;
    // 低频兜底自检：ResizeObserver 不是万能的（浏览器还跑着缓存下来的
    // 旧 JS、观察器被别的脚本断开、祖先元素高度变化都可能漏）。
    // 每秒比对一次容器高度，对不上就重设分辨率 —— 只差一度也要修，
    // 宁可偶尔多算一次，也不能让画布停在旧尺寸上被裁掉下半球。
    if (!this._sizeCheckAt || now - this._sizeCheckAt > 1000) {
      this._sizeCheckAt = now;
      if (this.host && this.host.clientHeight !== this._lastHostH) {
        this._lastHostH = this.host.clientHeight;
        this._resize();
      }
    }
    // 松手后的惯性 + 静止时的自动旋转
    var moved = false;
    if (!this.dragging) {
      if (Math.abs(this.velYaw) > 1e-4 || Math.abs(this.velPitch) > 1e-4) {
        this.yaw += this.velYaw;
        this.pitch = clamp(this.pitch + this.velPitch, -1.35, 1.35);
        this.velYaw *= 0.94;
        this.velPitch *= 0.94;
        moved = true;
      } else if (!reduce && this.effects.autoRotate) {
        this.yaw += 0.0011 * this.effects.rotationSpeed;
        moved = this.effects.rotationSpeed > 0;
      }
    }
    if (moved) this._drawGL();
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
    if (!gl || !this.dprog) return;
    this._setView();
    var c = this._colors();
    gl.clear(gl.COLOR_BUFFER_BIT);

    var sun = this.opts.sun || subsolar(new Date());
    var sv = toVec(sun.lat, sun.lon, this._sv || (this._sv = [0, 0, 0]));
    var svv = this._rotateVec(sv, this._svv || (this._svv = [0, 0, 0]));
    gl.useProgram(this.dprog);
    gl.bindBuffer(gl.ARRAY_BUFFER, this.quadBuf);
    gl.enableVertexAttribArray(this.dloc.quad);
    gl.vertexAttribPointer(this.dloc.quad, 2, gl.FLOAT, false, 0, 0);
    gl.uniform2f(this.dloc.scale, 2 * this.R / this.w, 2 * this.R / this.h);
    gl.uniform3f(this.dloc.sunView, svv[0], svv[1], svv[2]);
    gl.uniform3fv(this.dloc.oceanDay, this._rgb(c.oceanDay, '#f2f6fb'));
    gl.uniform3fv(this.dloc.oceanNight, this._rgb(c.oceanNight, '#d8dfea'));
    gl.uniform3fv(this.dloc.landDay, this._rgb(c.day, '#c9d4e2'));
    gl.uniform3fv(this.dloc.landNight, this._rgb(c.night, '#39435a'));
    gl.uniform3fv(this.dloc.litDay, this._rgb(c.litDay || c.flow, '#e8c880'));
    gl.uniform3fv(this.dloc.litNight, this._rgb(c.litNight || c.flow, '#c39a45'));
    gl.uniform1f(this.dloc.yaw, this.yaw);
    gl.uniform1f(this.dloc.pitch, this.pitch);
    gl.uniform1f(this.dloc.latMin, this.mask.latMin * DEG);
    gl.uniform1f(this.dloc.latMax, this.mask.latMax * DEG);
    gl.activeTexture(gl.TEXTURE0);
    gl.bindTexture(gl.TEXTURE_2D, this.landTex);
    gl.uniform1i(this.dloc.land, 0);
    gl.drawArrays(gl.TRIANGLES, 0, 6);
  };

  // 上传（或刷新）陆地纹理。点亮区域变了就重传一次，不必重建程序。
  KokoroGlobe.prototype._uploadLand = function () {
    var gl = this.gl;
    if (!gl || !this.mask || !this.landTex) return;
    var total = this.mask.nlon * this.mask.nlat;
    var lit = decodeLitRuns(this.litRuns, total);
    gl.bindTexture(gl.TEXTURE_2D, this.landTex);
    gl.pixelStorei(gl.UNPACK_ALIGNMENT, 1);
    gl.texImage2D(gl.TEXTURE_2D, 0, gl.LUMINANCE, this.mask.nlon, this.mask.nlat, 0,
      gl.LUMINANCE, gl.UNSIGNED_BYTE, landTextureData(this.mask, lit));
  };

  KokoroGlobe.prototype._project = function (lat, lon) {
    var v = toVec(lat, lon, this._v3 || (this._v3 = [0, 0, 0]));
    var x1 = v[0] * this._cosYaw + v[2] * this._sinYaw;
    var z1 = -v[0] * this._sinYaw + v[2] * this._cosYaw;
    var y1 = v[1] * this._cosPitch - z1 * this._sinPitch;
    var z2 = v[1] * this._sinPitch + z1 * this._cosPitch;
    return { x: this.cx + x1 * this.R, y: this.cy - y1 * this.R, z: z2 };
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

  // 把预计算的大圆采样点旋转、投影到可复用的 Float32Array。
  // 返回值只用于 "能不能画" 的快速判断，不再为每个点新建对象。
  KokoroGlobe.prototype._projectRoute = function (route) {
    var n = route.n, vs = route.vs;
    if (!this._px || this._px.length < n + 1) {
      this._px = new Float32Array(n + 1);
      this._py = new Float32Array(n + 1);
      this._pz = new Float32Array(n + 1);
    }
    var cosYaw = this._cosYaw, sinYaw = this._sinYaw;
    var cosPitch = this._cosPitch, sinPitch = this._sinPitch;
    var R = this.R, cx = this.cx, cy = this.cy;
    var visible = 0, run = 0;
    for (var i = 0; i <= n; i++) {
      var a = i * 3, x = vs[a], y = vs[a + 1], z = vs[a + 2];
      var x1 = x * cosYaw + z * sinYaw;
      var z1 = -x * sinYaw + z * cosYaw;
      var y1 = y * cosPitch - z1 * sinPitch;
      var z2 = y * sinPitch + z1 * cosPitch;
      this._px[i] = cx + x1 * R;
      this._py[i] = cy - y1 * R;
      this._pz[i] = z2;
      if (z2 >= -0.02) { run++; if (run === 2) visible++; }
      else run = 0;
    }
    this._pcount = n;
    this._pvisible = visible;
  };

  KokoroGlobe.prototype._strokeProjected = function (g) {
    var px = this._px, py = this._py, pz = this._pz, n = this._pcount;
    var open = false;
    for (var i = 0; i <= n; i++) {
      if (pz[i] < -0.02) { open = false; continue; }
      if (!open) { g.moveTo(px[i], py[i]); open = true; }
      else g.lineTo(px[i], py[i]);
    }
  };

  KokoroGlobe.prototype._drawOverlay = function (now) {
    var g = this.ctx2d;
    if (!g) return;
    now = now || performance.now();
    this._setView();
    var c = this._colors();
    g.clearRect(0, 0, this.w, this.h);
    if (this.fallback) this._drawFallbackMap(g, c);

    // 航线贴着球面的大圆走，先画底线，再沿方向绘制细小的信号流光。
    // 大圆采样点已在 _buildRoutes 里按经纬度预计算，这里只做旋转和投影。
    if (this.hub && this.effects.showRoutes) {
      if (this.routes.length !== this.places.length) this._buildRoutes();
      var hp = this._project(this.hub.lat, this.hub.lon);
      // The origin may be behind the globe while part of its route remains
      // visible in front; _projectRoute clips per sample.
      for (var r = 0; r < this.places.length; r++) {
        var place = this.places[r];
        var route = this.routes[r];
        if (!place || place.lat == null || place.lon == null || !route) continue;
        var end = this._project(place.lat, place.lon);
        if (!isFinite(hp.x) || !isFinite(hp.y) || !isFinite(end.x) || !isFinite(end.y)) continue;
        this._projectRoute(route);
        if (!this._pvisible) continue;
        var onlineRoute = place.online !== false;
        g.save();
        g.lineCap = 'round';
        g.lineJoin = 'round';
        g.strokeStyle = c.arc || c.hub || '#2f6feb';
        g.globalAlpha = onlineRoute ? this.effects.routeUnderlayOpacity : this.effects.routeUnderlayOpacity * .45;
        g.lineWidth = this.effects.routeUnderlayWidth;
        g.beginPath();
        this._strokeProjected(g);
        g.stroke();
        g.globalAlpha = onlineRoute ? this.effects.routeOpacity : this.effects.routeMutedOpacity;
        g.lineWidth = this.effects.routeWidth;
        // 主机端稍亮，沿途平缓淡出，形成制图式的方向感。
        var trace = g.createLinearGradient(hp.x, hp.y, end.x, end.y);
        trace.addColorStop(0, c.hub || c.arc || '#2f6feb');
        trace.addColorStop(1, c.arc || c.hub || '#2f6feb');
        g.strokeStyle = trace;
        g.beginPath();
        this._strokeProjected(g);
        g.stroke();
        g.restore();
        if (onlineRoute && this.effects.showFlow && !this._reducedMotion) {
          this._drawRouteFlow(g, place, route, now, r);
        }
      }
    }

    // 节点使用固定大小的实心标记，辅以极淡的静态晕圈提高识别度。
    for (var j = 0; j < this.places.length; j++) {
      var q = this.places[j];
      if (q.lat == null || q.lon == null) continue;
      var sp = this._project(q.lat, q.lon);
      if (sp.z <= 0) continue;
      var online = q.online !== false;
      var dotCol = online ? (c.ok || '#1a7f37') : (c.off || '#8b949e');
      g.beginPath();
      g.arc(sp.x, sp.y, online ? this.effects.nodeHaloSize : this.effects.nodeHaloSize * .84, 0, Math.PI * 2);
      g.fillStyle = dotCol;
      g.globalAlpha = online ? 0.09 : 0.055;
      g.fill();
      g.globalAlpha = 1;
      g.beginPath();
      g.arc(sp.x, sp.y, online ? this.effects.nodeRingSize : this.effects.nodeMutedRingSize, 0, Math.PI * 2);
      g.fillStyle = c.oceanDay || '#fff';
      g.fill();
      g.beginPath();
      g.fillStyle = dotCol;
      g.arc(sp.x, sp.y, online ? this.effects.nodeSize : this.effects.nodeMutedSize, 0, Math.PI * 2);
      g.fill();
    }

    this._drawNodeLabels(g, c);

    // 主机用单圈细描边标记；强调位置，但保持全图静止。
    if (this.hub) {
      var hp2 = this._project(this.hub.lat, this.hub.lon);
      if (hp2.z > 0) {
        g.beginPath();
        g.arc(hp2.x, hp2.y, this.effects.hubRingSize, 0, Math.PI * 2);
        g.strokeStyle = c.hub || '#2f6feb';
        g.lineWidth = this.effects.hubRingWidth;
        g.stroke();
        g.beginPath();
        g.arc(hp2.x, hp2.y, this.effects.hubSize, 0, Math.PI * 2);
        g.fillStyle = c.hub || '#2f6feb';
        g.fill();
      }
    }
  };

  KokoroGlobe.prototype.setLabelMode = function (mode) {
    this.labelMode = mode === 'list' ? 'list' : 'labels';
    this.labelLayer.hidden = this.labelMode === 'list';
    this._resize();
    this._render();
  };

  // Leader lines stay attached to projected locations. Back-facing locations
  // attach at the horizon with dashed lines, keeping every node discoverable.
  KokoroGlobe.prototype._drawNodeLabels = function (g, colors) {
    if (this.labelMode === 'list') return;
    var self = this, sides = [[], []], active = {};
    this.places.forEach(function (place) {
      if (place.lat == null || place.lon == null) return;
      var p = self._project(place.lat, place.lon);
      var dx = p.x-self.cx, dy=p.y-self.cy, length=Math.hypot(dx,dy)||1;
      if (p.z<=0) { p.x=self.cx+dx/length*self.R; p.y=self.cy+dy/length*self.R; }
      var side = dx<0 ? 0 : 1;
      sides[side].push({place:place,p:p});
    });
    sides.forEach(function (points, side) {
      points.sort(function(a,b){ return a.p.y-b.p.y || String(a.place.key).localeCompare(String(b.place.key)); });
      var gap=Math.min(30,(self.h-30)/Math.max(1,points.length));
      points.forEach(function (entry,index) {
        var place=entry.place, p=entry.p, key=place.key;
        active[key]=true;
        var label=self.nodeLabels[key];
        if (!label) {
          label=document.createElement('div'); label.className='globe-node-label';
          var status=document.createElement('span'); status.className='globe-node-light';
          status.setAttribute('aria-hidden','true'); label.appendChild(status);
          var original=document.querySelector('.globe-node-entry[data-node-key="'+CSS.escape(key)+'"] .flag');
          if (original) label.appendChild(original.cloneNode(true));
          var text=document.createElement('span'); text.textContent=place.city||place.sub||place.name;
          label.appendChild(text); label.title=place.name+' · '+text.textContent;
          self.labelLayer.appendChild(label); self.nodeLabels[key]=label;
        }
        var onlineState = place.online ? '1' : '0';
        if (label.dataset.online !== onlineState) {
          label.dataset.online = onlineState;
          label.classList.toggle('is-online', !!place.online);
          label.classList.toggle('is-offline', !place.online);
          label.setAttribute('aria-label', (place.online ? '在线' : '离线') + ' · ' + (place.name || place.city || '节点'));
        }
        var previous=index ? points[index-1].y : 8-gap;
        var y=clamp(p.y-10,previous+gap,self.h-22-(points.length-index-1)*gap);
        entry.y=y;
        var width=Math.min(104,Math.max(72,self.w*.25));
        var x=side ? self.w-width-5 : 5;
        // 慢速自转时标签每帧只挪零点几像素，逐帧写 style 会白白触发
        // 样式重算。超过半像素再写，视觉上看不出差别，DOM 写入量小一个量级。
        if (label._w !== width) { label.style.width=width+'px'; label._w=width; }
        if (label._x == null || Math.abs(label._x-x) > .5 || Math.abs(label._y-y) > .5) {
          label.style.transform='translate('+x+'px,'+y+'px)';
          label._x=x; label._y=y;
        }
        var behind=p.z<=0;
        if (label._behind !== behind) { label.classList.toggle('is-behind',behind); label._behind=behind; }
        g.save(); g.strokeStyle=colors.arc; g.lineWidth=.7;
        g.globalAlpha=p.z<=0 ? .35 : .7;
        if (p.z<=0) g.setLineDash([2,3]);
        g.beginPath(); g.moveTo(p.x,p.y);
        g.lineTo(side ? x-8 : x+width+8,y+10);
        g.lineTo(side ? x : x+width,y+10); g.stroke(); g.restore();
      });
    });
    Object.keys(this.nodeLabels).forEach(function(key){
      if (!active[key]) { self.nodeLabels[key].remove(); delete self.nodeLabels[key]; }
    });
  };

  // 把流光限制在球面正面：不会穿过地球，也不画轨道或整圈脉冲。
  KokoroGlobe.prototype._drawRouteFlow = function (g, place, route, now, index) {
    if (!this.hub || !this.effects.showFlow) return;
    var phase = ((now / this.effects.flowPeriod) + index * 0.307) % 1;
    var headT = 0.07 + phase * 0.86;
    // A longer tapered flow reads as data moving through a route, rather than
    // a short dot blinking along the line.
    var tailT = Math.max(0.012, headT - this.effects.flowLength);
    var steps = 14;
    if (!this._fx || this._fx.length < steps + 1) {
      this._fx = new Float32Array(steps + 1);
      this._fy = new Float32Array(steps + 1);
    }
    var fx = this._fx, fy = this._fy, count = 0;
    var vs = route.vs, n = route.n;
    var cosYaw = this._cosYaw, sinYaw = this._sinYaw;
    var cosPitch = this._cosPitch, sinPitch = this._sinPitch;
    var R = this.R, cx = this.cx, cy = this.cy;
    for (var i = 0; i <= steps; i++) {
      // buildRoute 的 t=0 在主机、t=1 在节点；流光要反着走（节点 → 主机）。
      var t = 1 - (tailT + (headT - tailT) * i / steps);
      var x = t * n, j = Math.floor(x);
      if (j >= n) j = n - 1;
      if (j < 0) j = 0;
      var f = x - j, a = j * 3, b = a + 3;
      var vx = vs[a] + (vs[b] - vs[a]) * f;
      var vy = vs[a + 1] + (vs[b + 1] - vs[a + 1]) * f;
      var vz = vs[a + 2] + (vs[b + 2] - vs[a + 2]) * f;
      var x1 = vx * cosYaw + vz * sinYaw;
      var z1 = -vx * sinYaw + vz * cosYaw;
      var y1 = vy * cosPitch - z1 * sinPitch;
      var z2 = vy * sinPitch + z1 * cosPitch;
      if (z2 < 0.015) continue;
      fx[count] = cx + x1 * R;
      fy[count] = cy - y1 * R;
      count++;
    }
    if (count < 2) return;
    var recentPulse = place.key && this.pulseAt[place.key] && now - this.pulseAt[place.key] < this.effects.flowPulseWindow;
    var colors = this._colors();
    var color = colors.flow || colors.hub || colors.arc || '#8ed8bc';
    var headX = fx[count - 1], headY = fy[count - 1];
    var trail = g.createLinearGradient(fx[0], fy[0], headX, headY);
    trail.addColorStop(0, withAlpha(color, 0.015));
    trail.addColorStop(0.42, withAlpha(color, recentPulse ? 0.16 : 0.1));
    trail.addColorStop(0.82, withAlpha(color, recentPulse ? 0.58 : 0.42));
    trail.addColorStop(1, withAlpha(color, recentPulse ? 0.98 : 0.82));

    g.save();
    g.lineCap = 'round';
    g.lineJoin = 'round';
    g.beginPath();
    g.moveTo(fx[0], fy[0]);
    for (var k = 1; k < count; k++) g.lineTo(fx[k], fy[k]);
    // 原来这里用 shadowBlur 做外发光。手机上每帧给几十条路径做阴影会
    // 持续压着 GPU/CPU，时间一长就降频——改成更宽的半透明描边，观感接近，
    // 但走普通路径，不再每帧重新光栅化阴影。
    g.globalAlpha = 0.3 * this.effects.flowOpacity;
    g.lineWidth = this.effects.flowGlowWidth * 1.5;
    g.strokeStyle = withAlpha(color, recentPulse ? 0.24 : 0.16);
    g.stroke();
    g.globalAlpha = this.effects.flowOpacity;
    g.lineWidth = recentPulse ? this.effects.flowCoreWidth * 1.3 : this.effects.flowCoreWidth;
    g.strokeStyle = trail;
    g.stroke();

    g.globalAlpha = (recentPulse ? 0.3 : 0.2) * this.effects.flowOpacity;
    g.fillStyle = color;
    g.beginPath();
    g.arc(headX, headY, recentPulse ? this.effects.flowGlowWidth * 1.5 : this.effects.flowGlowWidth * 1.25, 0, Math.PI * 2);
    g.fill();
    g.globalAlpha = 1;
    g.fillStyle = colors.flowCore || '#f5fff9';
    g.beginPath();
    g.arc(headX, headY, recentPulse ? this.effects.flowHeadSize * 1.2 : this.effects.flowHeadSize, 0, Math.PI * 2);
    g.fill();
    g.restore();
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

  // 无 WebGL 时以低分辨率像素纹理绘制连续海岸线，而非回退到点阵。
  KokoroGlobe.prototype._drawFallbackMap = function (g, c) {
    if (!this.mask) return;
    var w = this.w, h = this.h, m = this.mask;
    if (!this._fbCanvas) this._fbCanvas = document.createElement('canvas');
    if (this._fbCanvas.width !== w || this._fbCanvas.height !== h) {
      this._fbCanvas.width = w; this._fbCanvas.height = h;
    }
    var ctx = this._fbCanvas.getContext('2d');
    var image = ctx.createImageData(w, h), data = image.data;
    var sun = this.opts.sun || subsolar(new Date());
    var sunV = toVec(sun.lat, sun.lon, [0, 0, 0]);
    var landDay = parseColor(c.day, '#c9d4e2'), landNight = parseColor(c.night, '#39435a');
    var seaDay = parseColor(c.oceanDay, '#f2f6fb'), seaNight = parseColor(c.oceanNight, '#d8dfea');
    var cp = Math.cos(this.pitch), sp = Math.sin(this.pitch);
    var cy = Math.cos(this.yaw), sy = Math.sin(this.yaw);
    for (var y = 0; y < h; y++) {
      var ny = (this.cy - y - 0.5) / this.R;
      for (var x = 0; x < w; x++) {
        var nx = (x + 0.5 - this.cx) / this.R, r2 = nx * nx + ny * ny;
        if (r2 > 1) continue;
        var nz = Math.sqrt(1 - r2);
        var y1 = ny * cp + nz * sp, z1 = -ny * sp + nz * cp;
        var gx = nx * cy - z1 * sy, gy = y1, gz = nx * sy + z1 * cy;
        var lat = Math.asin(clamp(gy, -1, 1)) / DEG;
        var lon = Math.atan2(-gz, gx) / DEG;
        var row = Math.floor((m.latMax - lat) / m.step);
        var col = Math.floor((lon + 180) / m.step);
        var land = isLand(m, col, row);
        var lit = clamp((nx * sunV[0] + ny * sunV[1] + nz * sunV[2] + 0.2) / 0.44, 0, 1);
        var a = land ? landNight : seaNight, b = land ? landDay : seaDay;
        var shade = 0.9 + 0.1 * nz, at = (y * w + x) * 4;
        data[at] = Math.round((a[0] + (b[0] - a[0]) * lit) * shade * 255);
        data[at + 1] = Math.round((a[1] + (b[1] - a[1]) * lit) * shade * 255);
        data[at + 2] = Math.round((a[2] + (b[2] - a[2]) * lit) * shade * 255);
        data[at + 3] = 255;
      }
    }
    ctx.putImageData(image, 0, 0);
    g.drawImage(this._fbCanvas, 0, 0, w, h);
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
    if (this._ro) this._ro.disconnect();
    global.removeEventListener('resize', this._onResize);
    if (this.ovCanvas) this.ovCanvas.removeEventListener('wheel', this._onWheel);
    document.removeEventListener('visibilitychange', this._onVis);
    if (this.host) this.host.innerHTML = '';
  };

  global.KokoroGlobe = KokoroGlobe;
})(window);
