'use strict';

// The layers page: every live package stacked on a layer above the packages
// it refers to, frame by frame, drawn in 3D on a canvas. A package keeps its
// place on the journey map within its layer, so the view from above is the
// journey map and the view from the side is the architecture's layering.
// common.js provides h, $, api, plural, fmtDate, waitReady and fail.
(() => {
  const W = 10; // the map's side, in world units
  const STACK = 0.6; // the tallest stack's height against the layers' side
  const DIST = 3.6; // the camera's distance from the stack at zoom 1, in half widths of the layers
  const DISC_MIN = 0.07; // smallest disc radius, in world units
  const NAMED = 9; // narrowest disc radius on screen, in pixels, that is named unasked
  const MAX_LINES = 160; // dependencies drawn when no package is lit, strongest first
  const PLAY_MS = 900; // per frame while playing
  const VIEWS = { side: [0.55, 0.04], angle: [0.55, 0.5], top: [0, 1.55] }; // [azimuth, elevation]
  const SEGS = 32; // points around a disc

  const css = getComputedStyle(document.documentElement);
  const color = (name) => css.getPropertyValue(name).trim();
  const C = {
    text: color('--text'), text2: color('--text-2'), muted: color('--muted'), bg: color('--bg'),
    accent: color('--accent'), accentBg: color('--accent-bg'), moved: color('--moved'), deleted: color('--deleted'),
    disc: color('--bar'), rim: color('--body'),
  };
  const rgba = (hex, a) => {
    const n = parseInt(hex.slice(1), 16);
    return `rgba(${n >> 16},${(n >> 8) & 255},${n & 255},${a})`;
  };
  const name = (p) => (p === '.' ? '(root)' : p);
  const reduced = matchMedia('(prefers-reduced-motion: reduce)');

  const state = {
    step: 'week',
    data: null, // the /api/layers response for the current step
    cache: {}, // responses by step
    frame: 0,
    cur: null, // the current frame, unpacked: see unpack
    playing: false,
    timer: null,
    az: VIEWS.angle[0], el: VIEWS.angle[1], zoom: 1,
    aim: null, // the [azimuth, elevation] the camera is turning to
    hover: -1, // package under the pointer or a list entry
    pick: -1, // package picked by a click, kept across frames
    shown: [], // per package: { y, r, a }, eased toward the current frame
    box: { cx: 0, cz: 0, half: W / 2 }, // the packages' footprint: its centre and half its side
    gap: 1, // world units between layers
    top: 0, // the highest layer's height over the whole history, which the camera centres on
    tipFor: -1, // the package the hover card describes
    screen: [], // where each drawn disc landed, for hit tests
    dirty: true,
  };

  const canvas = $('canvas');
  const ctx = canvas.getContext('2d');
  const stage = $('stage');

  // ---- data ----

  // unpack turns a frame into lookups: package → { live, layer }, the deps
  // with what they touch, and the layers in use.
  function unpack(f) {
    const at = new Map();
    let layers = 0;
    for (const [p, live, layer] of f.pkgs) {
      at.set(p, { live, layer });
      layers = Math.max(layers, layer + 1);
    }
    const deps = f.deps.map(([from, to, refs, cyc]) => ({ from, to, refs, cyc: cyc === 1 }));
    const strongest = deps.filter((d) => !d.cyc).sort((a, b) => b.refs - a.refs).slice(0, MAX_LINES);
    const inCycle = new Set();
    for (const d of deps) if (d.cyc) inCycle.add(d.from).add(d.to);
    const maxRefs = deps.reduce((m, d) => Math.max(m, d.refs), 1);
    return { f, at, layers, deps, strongest, inCycle, maxRefs };
  }

  async function load(step) {
    if (!state.cache[step]) state.cache[step] = await api(`/api/layers?step=${step}`);
    const data = state.cache[step];
    const t = state.data ? state.data.frames[state.frame]?.t : Infinity;
    state.step = step;
    state.data = data;
    // The layers span the packages' footprint on the map, not the whole
    // square, and the camera frames that.
    let x0 = Infinity, x1 = -Infinity, z0 = Infinity, z1 = -Infinity;
    for (const p of data.packages) {
      if (!p.peak) continue;
      x0 = Math.min(x0, (p.x - p.r - 0.5) * W); x1 = Math.max(x1, (p.x + p.r - 0.5) * W);
      z0 = Math.min(z0, (p.y - p.r - 0.5) * W); z1 = Math.max(z1, (p.y + p.r - 0.5) * W);
    }
    if (x0 > x1) {
      x0 = z0 = -W / 2;
      x1 = z1 = W / 2;
    }
    const half = Math.max(x1 - x0, z1 - z0) / 2 + 0.4;
    state.box = { cx: (x0 + x1) / 2, cz: (z0 + z1) / 2, half };
    let depth = 1;
    for (const f of data.frames) for (const p of f.pkgs) depth = Math.max(depth, p[2] + 1);
    state.gap = depth > 1 ? (2 * half * STACK) / (depth - 1) : 0;
    state.top = (depth - 1) * state.gap;
    if (state.shown.length !== data.packages.length) state.shown = data.packages.map(() => ({ y: 0, r: 0, a: 0 }));
    // Keep to the same moment: the last frame starting at or before it.
    let i = data.frames.length - 1;
    while (i > 0 && data.frames[i].t > t) i--;
    $('frame').max = Math.max(0, data.frames.length - 1);
    for (const b of $('step').children) b.setAttribute('aria-pressed', String(b.dataset.v === step));
    $('empty').hidden = data.frames.length > 0 && data.packages.length > 0;
    setFrame(Math.max(0, i));
  }

  function setFrame(i) {
    const frames = state.data.frames;
    if (!frames.length) return;
    state.frame = Math.max(0, Math.min(frames.length - 1, i));
    state.cur = unpack(frames[state.frame]);
    $('frame').value = state.frame;
    state.tipFor = -1;
    if (state.pick >= 0 && !state.cur.at.has(state.pick)) state.pick = -1;
    if (state.hover >= 0 && !state.cur.at.has(state.hover)) state.hover = -1;
    overlay();
    list();
    picked();
    state.dirty = true;
  }

  // ---- the 3D view ----

  // radius is a package's disc radius for live symbols, its bubble on the
  // journey map scaled to them.
  function radius(p, live) {
    const pkg = state.data.packages[p];
    if (!live || !pkg.peak) return 0;
    return Math.max(DISC_MIN, pkg.r * W * 0.85 * Math.sqrt(live / pkg.peak));
  }
  const worldX = (p) => (state.data.packages[p].x - 0.5) * W;
  const worldZ = (p) => (state.data.packages[p].y - 0.5) * W;

  // ease moves every package's shown place and size toward the current
  // frame and the camera toward its aim, and reports whether anything is
  // still moving.
  function ease() {
    const k = reduced.matches ? 1 : 0.2;
    let moving = false;
    const step = (o, key, to) => {
      const d = to - o[key];
      if (Math.abs(d) > 1e-3) { o[key] += d * k; moving = true; } else o[key] = to;
    };
    state.shown.forEach((s, p) => {
      const at = state.cur?.at.get(p);
      step(s, 'y', at ? at.layer * state.gap : s.y);
      step(s, 'r', at ? radius(p, at.live) : s.r);
      step(s, 'a', at ? 1 : 0);
    });
    if (state.aim) {
      step(state, 'az', state.aim[0]);
      step(state, 'el', state.aim[1]);
      if (state.az === state.aim[0] && state.el === state.aim[1]) state.aim = null;
    }
    return moving;
  }

  // camera returns the view basis for the current angle and zoom: a point
  // projects to the screen through project.
  function camera(w, hgt) {
    const { cx, cz, half } = state.box;
    const ty = state.top / 2;
    const d = (DIST * half) / state.zoom;
    const cam = [cx + d * Math.cos(state.el) * Math.sin(state.az), ty + d * Math.sin(state.el), cz + d * Math.cos(state.el) * Math.cos(state.az)];
    const f = norm([cx - cam[0], ty - cam[1], cz - cam[2]]);
    const r = norm([-f[2], 0, f[0]]); // f × up
    const u = [r[1] * f[2] - r[2] * f[1], r[2] * f[0] - r[0] * f[2], r[0] * f[1] - r[1] * f[0]];
    return { cam, f, r, u, focal: Math.min(w, hgt) * 1.1, cx: w / 2, cy: hgt / 2 };
  }
  function norm(v) {
    const l = Math.hypot(v[0], v[1], v[2]) || 1;
    return [v[0] / l, v[1] / l, v[2] / l];
  }
  function project(c, x, y, z) {
    const vx = x - c.cam[0], vy = y - c.cam[1], vz = z - c.cam[2];
    const dz = Math.max(0.1, vx * c.f[0] + vy * c.f[1] + vz * c.f[2]);
    const s = c.focal / dz;
    return [c.cx + (vx * c.r[0] + vy * c.r[1] + vz * c.r[2]) * s, c.cy - (vx * c.u[0] + vy * c.u[1] + vz * c.u[2]) * s, dz, s];
  }

  function lit() { return state.pick >= 0 ? state.pick : state.hover; }

  function draw() {
    const dpr = devicePixelRatio || 1;
    const w = canvas.clientWidth, hgt = canvas.clientHeight;
    if (canvas.width !== Math.round(w * dpr) || canvas.height !== Math.round(hgt * dpr)) {
      canvas.width = Math.round(w * dpr);
      canvas.height = Math.round(hgt * dpr);
    }
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, w, hgt);
    state.screen = [];
    const cur = state.cur;
    if (!cur) return;
    const c = camera(w, hgt);
    const flat = Math.max(0.12, Math.abs(Math.sin(state.el))); // how round a disc looks from here

    // Layers, bottom up, each a faint square named at its leftmost corner.
    const { cx: bx, cz: bz, half } = state.box;
    ctx.font = "11px 'JetBrains Mono', monospace";
    for (let l = 0; l < cur.layers; l++) {
      const y = l * state.gap;
      const pts = [[-half, -half], [half, -half], [half, half], [-half, half]].map(([x, z]) => project(c, bx + x, y, bz + z));
      ctx.beginPath();
      pts.forEach(([x, yy], i) => (i ? ctx.lineTo(x, yy) : ctx.moveTo(x, yy)));
      ctx.closePath();
      ctx.fillStyle = rgba(C.muted, 0.035);
      ctx.fill();
      ctx.strokeStyle = rgba(C.muted, 0.2);
      ctx.lineWidth = 1;
      ctx.stroke();
      const left = pts.reduce((a, b) => (b[0] < a[0] ? b : a));
      ctx.fillStyle = rgba(C.muted, 0.8);
      ctx.textAlign = 'right';
      ctx.textBaseline = 'middle';
      ctx.fillText(`L${l}`, left[0] - 8, left[1]);
    }

    // Where each package lands this time round.
    const spots = new Map();
    state.shown.forEach((s, p) => {
      if (s.a < 0.02 || s.r <= 0) return;
      const [x, y, dz, sc] = project(c, worldX(p), s.y, worldZ(p));
      spots.set(p, { p, x, y, dz, rx: s.r * sc, ry: s.r * sc * flat, a: s.a });
    });

    // Dependencies: a lit package's own, or the strongest; cycles always.
    const L = lit();
    const near = new Map(); // package → 'out' | 'in', next to the lit one
    let deps = cur.strongest;
    if (L >= 0) {
      deps = cur.deps.filter((d) => d.from === L || d.to === L);
      for (const d of deps) {
        if (d.from === L) near.set(d.to, 'out');
        else if (!near.has(d.from)) near.set(d.from, 'in');
      }
    }
    deps = deps.concat(cur.deps.filter((d) => d.cyc && !deps.includes(d)));
    for (const d of deps) {
      const a = spots.get(d.from), b = spots.get(d.to);
      if (!a || !b) continue;
      const weight = Math.sqrt(d.refs / cur.maxRefs);
      let col = rgba(C.text2, 0.12 + 0.4 * weight), width = 0.8 + 1.4 * weight;
      if (d.cyc) {
        col = rgba(C.deleted, 0.85);
        width = 1.6;
      } else if (L >= 0) {
        col = d.from === L ? rgba(C.accent, 0.9) : rgba(C.moved, 0.85);
        width = 1.2 + 1.6 * weight;
      }
      arrow(a, b, col, width * Math.min(a.a, b.a));
    }

    // Discs, far to near.
    const order = [...spots.values()].sort((p, q) => q.dz - p.dz);
    for (const s of order) {
      const isLit = s.p === L, rel = near.get(s.p), cyc = cur.inCycle.has(s.p);
      const dim = L >= 0 && !isLit && !rel ? 0.35 : 1;
      ctx.beginPath();
      for (let i = 0; i <= SEGS; i++) {
        const t = (i / SEGS) * 2 * Math.PI;
        const r = state.shown[s.p].r;
        const [x, y] = project(c, worldX(s.p) + r * Math.cos(t), state.shown[s.p].y, worldZ(s.p) + r * Math.sin(t));
        i ? ctx.lineTo(x, y) : ctx.moveTo(x, y);
      }
      ctx.closePath();
      ctx.fillStyle = rgba(isLit ? C.accentBg : C.disc, 0.92 * s.a * dim);
      ctx.fill();
      ctx.strokeStyle = rgba(isLit ? C.accent : cyc ? C.deleted : rel === 'out' ? C.accent : rel === 'in' ? C.moved : C.rim, (isLit || rel || cyc ? 0.95 : 0.55) * s.a * dim);
      ctx.lineWidth = isLit || rel || cyc ? 1.6 : 1;
      ctx.stroke();
      state.screen.push(s);
    }

    // Names: the lit package and its neighbours first, then the largest,
    // skipping any that would overlap one already placed.
    ctx.font = "11px 'IBM Plex Sans', system-ui, sans-serif";
    ctx.textAlign = 'center';
    ctx.textBaseline = 'bottom';
    const boxes = [];
    const ranked = order.slice().sort((p, q) => rank(q) - rank(p));
    function rank(s) { return s.p === L ? 3e6 : near.has(s.p) ? 2e6 + s.rx : s.rx; }
    for (const s of ranked) {
      const forced = s.p === L || near.has(s.p);
      if (!forced && (s.rx < NAMED || L >= 0)) continue;
      const label = name(state.data.packages[s.p].path);
      const tw = ctx.measureText(label).width;
      const box = [s.x - tw / 2 - 3, s.y - s.ry - 17, s.x + tw / 2 + 3, s.y - s.ry - 2];
      if (!forced && boxes.some((b) => box[0] < b[2] && box[2] > b[0] && box[1] < b[3] && box[3] > b[1])) continue;
      boxes.push(box);
      ctx.lineWidth = 3;
      ctx.strokeStyle = rgba(C.bg, 0.85 * s.a);
      ctx.strokeText(label, s.x, s.y - s.ry - 4);
      ctx.fillStyle = rgba(s.p === L ? C.text : C.text2, s.a);
      ctx.fillText(label, s.x, s.y - s.ry - 4);
    }
  }

  // arrow draws a line from the middle of disc a to the rim of disc b, with
  // a head there.
  function arrow(a, b, col, width) {
    const dx = b.x - a.x, dy = b.y - a.y, len = Math.hypot(dx, dy);
    if (len < 1) return;
    const ux = dx / len, uy = dy / len;
    const rim = 1 / Math.sqrt((ux / Math.max(b.rx, 0.5)) ** 2 + (uy / Math.max(b.ry, 0.5)) ** 2);
    const end = Math.max(0, len - rim - 1);
    const ex = a.x + ux * end, ey = a.y + uy * end;
    ctx.strokeStyle = col;
    ctx.lineWidth = width;
    ctx.beginPath();
    ctx.moveTo(a.x, a.y);
    ctx.lineTo(ex, ey);
    ctx.stroke();
    const hs = 4 + width * 1.5;
    ctx.fillStyle = col;
    ctx.beginPath();
    ctx.moveTo(ex, ey);
    ctx.lineTo(ex - ux * hs - uy * hs * 0.55, ey - uy * hs + ux * hs * 0.55);
    ctx.lineTo(ex - ux * hs + uy * hs * 0.55, ey - uy * hs - ux * hs * 0.55);
    ctx.closePath();
    ctx.fill();
  }

  function loop() {
    if (ease() || state.dirty) {
      state.dirty = false;
      draw();
    }
    requestAnimationFrame(loop);
  }

  // hit returns the package whose disc is under the point, nearest first.
  function hit(x, y) {
    let best = -1, bestDz = Infinity;
    for (const s of state.screen) {
      const rx = Math.max(s.rx, 6), ry = Math.max(s.ry, 6);
      if (((x - s.x) / rx) ** 2 + ((y - s.y) / ry) ** 2 <= 1 && s.dz < bestDz) {
        best = s.p;
        bestDz = s.dz;
      }
    }
    return best;
  }

  // ---- words around the view ----

  function overlay() {
    const { f, at, layers, inCycle } = state.cur;
    const step = state.data.step;
    $('date').textContent = fmtDate(f.t);
    $('frameNote').textContent = step === 'commit'
      ? `commit ${f.sha.slice(0, 7)} · ${plural(f.live, 'live symbol')}`
      : `${plural(f.commits, 'commit')} this ${step} · ${plural(f.live, 'live symbol')}`;
    $('cLayers').textContent = layers.toLocaleString();
    $('cPkgs').textContent = at.size.toLocaleString();
    $('cCycles').textContent = inCycle.size.toLocaleString();
    $('frameLabel').textContent = `${state.frame + 1} / ${state.data.frames.length}`;
    caption();
  }

  function caption() {
    const { deps, strongest } = state.cur;
    const plain = deps.filter((d) => !d.cyc).length;
    $('caption').textContent = lit() < 0 && plain > strongest.length
      ? `Showing the ${strongest.length} strongest of ${plain.toLocaleString()} references; point at a package for its own.`
      : '';
  }

  // neighbours lists what package p refers to and what refers to it in the
  // current frame, strongest first.
  function neighbours(p) {
    const out = [], into = [];
    for (const d of state.cur.deps) {
      if (d.from === p) out.push(d);
      else if (d.to === p) into.push(d);
    }
    const by = (a, b) => b.refs - a.refs;
    return { out: out.sort(by), into: into.sort(by) };
  }

  function tip(p, x, y) {
    const t = $('tip');
    if (p < 0) {
      t.hidden = true;
      state.tipFor = -1;
      return;
    }
    if (p !== state.tipFor || t.hidden) fillTip(t, p);
    t.hidden = false;
    const sw = stage.clientWidth, sh = stage.clientHeight;
    const tw = t.offsetWidth, th = t.offsetHeight;
    t.style.left = `${Math.min(x + 14, sw - tw - 8)}px`;
    t.style.top = `${Math.min(y + 14, sh - th - 8)}px`;
  }

  function fillTip(t, p) {
    state.tipFor = p;
    const at = state.cur.at.get(p);
    const { out, into } = neighbours(p);
    const rows = [['Layer', `${at.layer} of ${state.cur.layers - 1}`], ['Live symbols', at.live.toLocaleString()],
      ['Refers to', plural(out.length, 'package')], ['Referred to by', plural(into.length, 'package')]];
    t.replaceChildren(...[
      h('div', { class: 'tip-name mono' }, name(state.data.packages[p].path)),
      h('dl', { class: 'tip-rows' }, rows.flatMap(([k, v]) => [h('dt', null, k), h('dd', null, v)])),
      state.cur.inCycle.has(p) && h('div', { class: 'tip-hint' }, 'In a cycle: some of what it refers to refers back to it.'),
      state.pick !== p && h('div', { class: 'tip-hint' }, 'Click to keep it lit'),
    ].filter(Boolean));
  }

  function refList(deps, key, cls) {
    return h('ul', { class: `refs ${cls}` }, deps.map((d) => {
      const q = d[key];
      return h('li', null, h('button', { type: 'button', onclick: () => pick(q), onmouseenter: () => light(q), onmouseleave: () => light(-1) },
        h('span', null, name(state.data.packages[q].path)),
        h('span', { class: 'n' }, `L${state.cur.at.get(q)?.layer ?? '–'} · ${plural(d.refs, 'symbol')}`)));
    }));
  }

  function picked() {
    const box = $('pick');
    const p = state.pick;
    if (p < 0) {
      box.replaceChildren(h('p', { class: 'muted small' }, 'Choose a package, in the view or the list below, to see what it refers to and what refers to it.'));
      return;
    }
    const at = state.cur.at.get(p);
    const { out, into } = neighbours(p);
    box.replaceChildren(...[
      h('div', { class: 'pick-head' },
        h('span', { class: 'sel-name' }, name(state.data.packages[p].path)),
        h('button', { type: 'button', class: 'close', 'aria-label': 'Clear the choice (Escape)', onclick: () => pick(-1) }, '×')),
      h('div', { class: 'muted small' }, `Layer ${at.layer} · ${plural(at.live, 'live symbol')}`),
      state.cur.inCycle.has(p) && h('p', { class: 'cyc-note' }, 'Caught in a cycle: it and some of what it refers to refer to each other, so they share a layer. Go allows this only through tests or build variants.'),
      h('h3', { class: 'eyebrow' }, `Refers to ${out.length}`),
      out.length ? refList(out, 'to', 'out') : h('p', { class: 'muted small' }, 'Nothing else in the repository: it sits on the bottom layer.'),
      h('h3', { class: 'eyebrow' }, `Referred to by ${into.length}`),
      into.length ? refList(into, 'from', 'in') : h('p', { class: 'muted small' }, 'Nothing in the repository.'),
    ].filter(Boolean));
  }

  function list() {
    const byLayer = [];
    for (const [p, at] of state.cur.at) (byLayer[at.layer] ||= []).push(p);
    const paths = state.data.packages;
    const out = [];
    for (let l = byLayer.length - 1; l >= 0; l--) {
      const ps = (byLayer[l] || []).sort((a, b) => paths[a].path.localeCompare(paths[b].path));
      out.push(h('section', { class: 'layer' },
        h('h3', { class: 'eyebrow' }, h('span', null, `Layer ${l}`), h('span', { class: 'muted' }, plural(ps.length, 'package'))),
        h('ul', null, ps.map((p) => h('li', null, h('button', {
          type: 'button', 'data-p': p, class: state.cur.inCycle.has(p) ? 'cyc' : null, 'aria-pressed': String(p === state.pick),
          onclick: () => pick(p === state.pick ? -1 : p), onmouseenter: () => light(p), onmouseleave: () => light(-1),
          onfocus: () => light(p), onblur: () => light(-1),
        }, h('span', null, name(paths[p].path)), h('span', { class: 'n' }, state.cur.at.get(p).live.toLocaleString())))))));
    }
    $('list').replaceChildren(...out);
  }

  function light(p) {
    if (state.hover === p) return;
    state.hover = p;
    for (const b of $('list').querySelectorAll('button')) b.classList.toggle('lit', Number(b.dataset.p) === p);
    caption();
    state.dirty = true;
  }

  function pick(p) {
    state.pick = p;
    state.tipFor = -1;
    for (const b of $('list').querySelectorAll('button')) b.setAttribute('aria-pressed', String(Number(b.dataset.p) === p));
    picked();
    caption();
    state.dirty = true;
  }

  // ---- playback and camera controls ----

  function play(on) {
    state.playing = on;
    clearInterval(state.timer);
    $('play').setAttribute('aria-label', on ? 'Pause' : 'Play');
    $('playIcon').innerHTML = on ? '<path d="M6 4h4v16H6zM14 4h4v16h-4z"></path>' : '<path d="M7 4v16l13-8z"></path>';
    if (!on) return;
    if (state.frame >= state.data.frames.length - 1) setFrame(0);
    state.timer = setInterval(() => {
      if (state.frame >= state.data.frames.length - 1) return play(false);
      setFrame(state.frame + 1);
    }, PLAY_MS);
  }

  function look(v) {
    state.aim = VIEWS[v].slice();
    // Turn the short way round.
    const tau = 2 * Math.PI;
    state.az = ((state.az % tau) + tau) % tau;
    if (state.aim[0] - state.az > Math.PI) state.aim[0] -= tau;
    if (state.az - state.aim[0] > Math.PI) state.aim[0] += tau;
    for (const b of $('views').children) b.setAttribute('aria-pressed', String(b.dataset.v === v));
  }
  const clearView = () => { for (const b of $('views').children) b.setAttribute('aria-pressed', 'false'); };
  const zoomBy = (k) => { state.zoom = Math.max(0.5, Math.min(8, state.zoom * k)); state.dirty = true; };

  let drag = null;
  stage.addEventListener('pointerdown', (e) => {
    if (e.target !== canvas) return;
    drag = { x: e.clientX, y: e.clientY, az: state.az, el: state.el, moved: false };
    canvas.setPointerCapture(e.pointerId);
  });
  stage.addEventListener('pointermove', (e) => {
    const r = canvas.getBoundingClientRect();
    const x = e.clientX - r.left, y = e.clientY - r.top;
    if (drag) {
      const dx = e.clientX - drag.x, dy = e.clientY - drag.y;
      if (!drag.moved && Math.hypot(dx, dy) < 4) return;
      if (!drag.moved) { drag.moved = true; stage.classList.add('dragging'); clearView(); tip(-1); }
      state.aim = null;
      state.az = drag.az - dx * 0.008;
      state.el = Math.max(0.02, Math.min(1.55, drag.el + dy * 0.006));
      state.dirty = true;
      return;
    }
    if (e.target !== canvas) return;
    const p = hit(x, y);
    stage.classList.toggle('over', p >= 0);
    light(p);
    tip(p, x, y);
  });
  const endDrag = (e) => {
    if (!drag) return;
    if (!drag.moved && e.type === 'pointerup') {
      const r = canvas.getBoundingClientRect();
      const p = hit(e.clientX - r.left, e.clientY - r.top);
      pick(p === state.pick ? -1 : p);
    }
    drag = null;
    stage.classList.remove('dragging');
  };
  stage.addEventListener('pointerup', endDrag);
  stage.addEventListener('pointercancel', endDrag);
  canvas.addEventListener('pointerleave', () => { if (!drag) { light(-1); tip(-1); stage.classList.remove('over'); } });
  stage.addEventListener('wheel', (e) => {
    if (e.target !== canvas) return;
    e.preventDefault();
    zoomBy(Math.exp(-e.deltaY * 0.0015));
  }, { passive: false });

  $('zoomIn').addEventListener('click', () => zoomBy(1.25));
  $('zoomOut').addEventListener('click', () => zoomBy(0.8));
  for (const b of $('views').children) b.addEventListener('click', () => look(b.dataset.v));
  for (const b of $('step').children) b.addEventListener('click', () => { play(false); load(b.dataset.v).catch(fail); });
  $('frame').addEventListener('input', (e) => { play(false); setFrame(Number(e.target.value)); });
  $('prev').addEventListener('click', () => { play(false); setFrame(state.frame - 1); });
  $('next').addEventListener('click', () => { play(false); setFrame(state.frame + 1); });
  $('play').addEventListener('click', () => play(!state.playing));
  document.addEventListener('keydown', (e) => {
    if (!state.data || e.target.closest?.('input, select, textarea') || e.metaKey || e.ctrlKey || e.altKey) return;
    if (e.key === 'Escape') pick(-1);
    else if (e.key === 'ArrowLeft') { play(false); setFrame(state.frame - 1); }
    else if (e.key === 'ArrowRight') { play(false); setFrame(state.frame + 1); }
    else if (e.key === '+' || e.key === '=') zoomBy(1.25);
    else if (e.key === '-') zoomBy(0.8);
    else return;
    e.preventDefault();
  });
  new ResizeObserver(() => { state.dirty = true; }).observe(stage);
  document.fonts?.ready.then(() => { state.dirty = true; });

  (async () => {
    const st = await waitReady();
    if (!st) return;
    $('repo').textContent = st.repo;
    $('loading').hidden = true;
    $('app').hidden = false;
    look('angle');
    state.az = state.aim[0];
    state.el = state.aim[1];
    await load(state.step);
    requestAnimationFrame(loop);
  })().catch(fail);
})();
