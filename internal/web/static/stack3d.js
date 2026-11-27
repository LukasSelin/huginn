'use strict';

// Stack3D draws a repository's packages stacked in layers, in 3D on a
// canvas: every live package on a layer above the packages it refers to, at
// its place on the journey map within its layer, so the view from above is
// the journey map and the view from the side the architecture's layering.
// The layers page and the journey's architecture view both draw with it.
//
// Stack3D(canvas, opts) takes:
//   label(p)    the name drawn for package p (default: its path)
//   style(p)    how to draw package p, or null for the plain disc:
//               { fill, ring: [[colour, share], …], alpha, mark: 'focus' | 'followed', busy }
//   build       the journey's way of drawing: the camera rises with the
//               stack and, on Auto, starts above and tilts to the side as
//               layers appear, and packages drop onto their layer when born
//               and flash when they move up or down
//   views       a group of buttons with data-v of side, angle, top (and
//               auto, when building) that turn the camera
//   onHover(p, x, y, d), onPick(p)   the pointer over or a click on
//               package p, -1 for none, in pixels from the canvas's top
//               left; with onPickDep given, d is the dependency line under
//               the pointer when no disc is, or null
//   onPickDep(d)  a click on the line of dependency d, { from, to, refs,
//               cyc }; without it the lines can't be picked
//
// Dragging turns the camera round the point it looks at; dragging with the
// right or middle button, or with Shift held, moves that point across, the
// stack following the pointer; the wheel zooms toward the pointer.
//
// It returns an object to feed frames to and steer: see the end.
function Stack3D(canvas, opts = {}) {
  const W = 10; // the map's side, in world units
  const STACK = 0.6; // the tallest stack's height against the layers' side
  const DIST = 3.6; // the camera's distance from the stack at zoom 1, in half widths of the layers
  const DISC_MIN = 0.07; // smallest disc radius, in world units
  // A disc's radius against its package's bubble on the layout, which packs
  // the bubbles all but touching: at spread 1 the closest discs keep
  // SPREAD_ROOM between them, as on the journey map.
  const DISC = 1 / (1 + SPREAD_ROOM);
  const NAMED = 9; // narrowest disc radius on screen, in pixels, that is named unasked
  const MAX_LINES = 160; // dependencies drawn when no package is lit, strongest first
  const VIEWS = { side: [0.55, 0.04], angle: [0.55, 0.5], top: [0, 1.55] }; // [azimuth, elevation]
  const AUTO_EL = [1.5, 0.45]; // Auto's elevation over a single layer, and once the stack stands
  const AUTO_LAYERS = 3; // layers it takes Auto to come down to the side
  const ORBIT = 0.0016; // how far Auto turns per drawing while playing, in radians
  const SEGS = 48; // points around a disc
  const BORN = 70; // drawings a new package's flash lasts
  const MOVED = 45; // drawings the flash of a package changing layer lasts

  const label = opts.label || ((p) => (state.packages[p].path === '.' ? '(root)' : state.packages[p].path));
  const ctx = canvas.getContext('2d');
  const css = getComputedStyle(document.documentElement);
  const color = (n) => css.getPropertyValue(n).trim();
  const C = {
    text: color('--text'), text2: color('--text-2'), muted: color('--muted'), bg: color('--bg'),
    accent: color('--accent'), accentBg: color('--accent-bg'), moved: color('--moved'), deleted: color('--deleted'),
    disc: color('--bar'), rim: color('--body'), followed: '#C792EA',
  };
  const rgba = (hex, a) => {
    const n = parseInt(hex.slice(1), 16);
    return `rgba(${n >> 16},${(n >> 8) & 255},${n & 255},${a})`;
  };
  const reduced = matchMedia('(prefers-reduced-motion: reduce)');

  const state = {
    packages: [], // as /api/journey's, with their place on the map
    cur: null, // the current frame, unpacked: see unpack
    depth: 1, // the most layers in any frame
    shown: [], // per package: { y, r, a, layer, flash }, eased toward the current frame
    planes: [], // per layer: how far it is shown, eased
    box: { cx: 0, cz: 0, half: W / 2 }, // the packages' footprint: its centre and half its side
    gap: 1, // world units between layers
    // How far the packages are from the footprint's centre against the
    // layout, for the spread, and as shown, eased toward it.
    apart: 1, shownApart: 1,
    ty: 0, // the height of the stack's middle, eased
    // Where the camera looks against the stack's middle: across in layout
    // units, so that spreading the packages keeps the same ones in view,
    // and up in world units.
    pan: [0, 0, 0],
    az: VIEWS.angle[0], el: VIEWS.angle[1], zoom: 1,
    aim: null, // the [azimuth, elevation] the camera is turning to
    view: opts.build ? 'auto' : 'angle', // the views button pressed, '' once dragged
    lit: -1, // the package whose references are drawn
    strong: false, // whether the rest fade almost away, as for a package picked
    chosen: null, // { from, to }: the dependency drawn out over the rest, or null
    playing: false, // Auto turns round the stack while the history plays
    screen: [], // where each drawn disc landed, for hit tests
    lines: [], // where each drawn dependency landed: { d, x1, y1, x2, y2, w }
    dirty: true,
    running: false,
  };

  // unpack turns a frame, { pkgs: [[package, live, layer]], deps: [[from,
  // to, refs, cyclic]] }, into lookups: package → { live, layer }, the deps
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

  // setData takes the packages and every frame of the history, which fix
  // the footprint the layers span and the height between them.
  function setData(packages, frames) {
    state.packages = packages;
    let x0 = Infinity, x1 = -Infinity, z0 = Infinity, z1 = -Infinity;
    for (const p of packages) {
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
    for (const f of frames) for (const p of f.pkgs) depth = Math.max(depth, p[2] + 1);
    state.depth = depth;
    state.gap = depth > 1 ? (2 * half * STACK) / (depth - 1) : 0;
    if (state.shown.length !== packages.length) state.shown = packages.map(() => ({ y: 0, r: 0, a: 0, layer: -1, flash: 0 }));
    state.dirty = true;
  }

  function setFrame(f) {
    state.cur = unpack(f);
    if (state.lit >= 0 && !state.cur.at.has(state.lit)) state.lit = -1;
    state.dirty = true;
    return state.cur;
  }

  // radius is a package's disc radius for live symbols, its bubble on the
  // journey map scaled to them.
  function radius(p, live) {
    const pkg = state.packages[p];
    if (!live || !pkg.peak) return 0;
    return Math.max(DISC_MIN, pkg.r * W * DISC * Math.sqrt(live / pkg.peak));
  }
  // A package's place within its layer, moved out from the footprint's
  // centre by the spread, the discs keeping their size.
  const worldX = (p) => state.box.cx + state.shownApart * ((state.packages[p].x - 0.5) * W - state.box.cx);
  const worldZ = (p) => state.box.cz + state.shownApart * ((state.packages[p].y - 0.5) * W - state.box.cz);

  // setSpread sets the room between the packages, against SPREAD_ROOM. As
  // the discs on the layout all but touch at DISC, moving them out by
  // DISC + (1 − DISC)·spread leaves room in proportion: none at 0, and
  // SPREAD_ROOM at 1.
  function setSpread(s) {
    state.apart = DISC + (1 - DISC) * s;
    state.dirty = true;
  }

  // autoAim is where Auto points the camera: straight down on the map while
  // there is one layer, coming down to the side as the stack grows.
  function autoAim() {
    const layers = state.cur ? state.cur.layers : 1;
    const full = Math.min(AUTO_LAYERS, state.depth) - 1;
    const k = full > 0 ? Math.min(1, Math.max(0, layers - 1) / full) : 0;
    return [state.az + (state.playing && !reduced.matches ? ORBIT : 0), AUTO_EL[0] + (AUTO_EL[1] - AUTO_EL[0]) * k];
  }

  // ease moves every package's shown place and size toward the current
  // frame and the camera toward its aim, and reports whether anything is
  // still moving. Each call is one drawing's worth.
  function ease() {
    const k = reduced.matches ? 1 : 0.2;
    let moving = false;
    const step = (o, key, to, kk = k) => {
      const d = to - o[key];
      if (Math.abs(d) > 1e-3) { o[key] += d * kk; moving = true; } else o[key] = to;
    };
    const cur = state.cur;
    const drop = Math.max(state.gap, state.box.half * 0.25) * 1.4;
    state.shown.forEach((s, p) => {
      const at = cur?.at.get(p);
      if (at && opts.build && !reduced.matches) {
        // A package born drops onto its layer, full size; one that changes
        // layer flashes on its way.
        if (s.a < 0.05) {
          s.y = at.layer * state.gap + drop;
          s.r = radius(p, at.live);
          s.flash = BORN;
        } else if (s.layer >= 0 && at.layer !== s.layer) s.flash = Math.max(s.flash, MOVED);
      }
      if (at) s.layer = at.layer;
      else if (s.a < 0.05) s.layer = -1;
      if (s.flash > 0) { s.flash--; moving = true; }
      step(s, 'y', at ? at.layer * state.gap : s.y, opts.build ? 0.14 : k);
      step(s, 'r', at ? radius(p, at.live) : s.r);
      step(s, 'a', at ? 1 : 0);
    });
    step(state, 'shownApart', state.apart);
    const layers = cur ? cur.layers : 0;
    for (let l = 0; l < Math.max(layers, state.planes.length); l++) {
      const o = { v: state.planes[l] || 0 };
      step(o, 'v', l < layers ? 1 : 0);
      state.planes[l] = o.v;
    }
    while (state.planes.length && !state.planes[state.planes.length - 1]) state.planes.pop();
    // The stack's middle: of what stands now while building, of the tallest
    // it ever stands otherwise.
    const o = { v: state.ty };
    step(o, 'v', ((opts.build ? Math.max(1, layers) : state.depth) - 1) * state.gap / 2, 0.08);
    state.ty = o.v;
    if (state.view === 'auto') state.aim = autoAim();
    if (state.aim) {
      step(state, 'az', state.aim[0], state.view === 'auto' ? 1 : k);
      step(state, 'el', state.aim[1], state.view === 'auto' ? 0.06 : k);
      if (state.view !== 'auto' && state.az === state.aim[0] && state.el === state.aim[1]) state.aim = null;
    }
    return moving;
  }

  // dist is the camera's distance from the point it looks at, at zoom z.
  const dist = (z = state.zoom) => (DIST * state.box.half) / z;

  // target is the point the camera looks at and turns round: the stack's
  // middle, moved by the pan.
  function target() {
    const { cx, cz } = state.box, a = state.shownApart, [px, py, pz] = clampPan(state.pan);
    return [cx + a * px, state.ty + py, cz + a * pz];
  }

  // clampPan keeps the camera looking at the stack, within the box the
  // layers standing now span, so that the pan cannot lose it. Zooming
  // toward a disc stays within it, as the disc is.
  function clampPan([px, py, pz]) {
    const half = state.box.half, c = (v, l) => Math.max(-l, Math.min(l, v));
    return [c(px, half), c(py, state.ty), c(pz, half)];
  }

  // move moves the point the camera looks at by v, in world units.
  function move(v) {
    const a = state.shownApart, p = clampPan(state.pan);
    state.pan = clampPan([p[0] + v[0] / a, p[1] + v[1], p[2] + v[2] / a]);
    state.dirty = true;
  }

  // camera returns the view basis for the current angle, zoom and pan: a
  // point projects to the screen through project.
  function camera(w, hgt) {
    const [tx, ty, tz] = target();
    const d = dist();
    const cam = [tx + d * Math.cos(state.el) * Math.sin(state.az), ty + d * Math.sin(state.el), tz + d * Math.cos(state.el) * Math.cos(state.az)];
    const f = norm([tx - cam[0], ty - cam[1], tz - cam[2]]);
    const r = norm([-f[2], 0, f[0]]); // f × up
    const u = [r[1] * f[2] - r[2] * f[1], r[2] * f[0] - r[0] * f[2], r[0] * f[1] - r[1] * f[0]];
    return { cam, f, r, u, focal: Math.min(w, hgt) * 1.1, cx: w / 2, cy: hgt / 2 };
  }
  const onCanvas = () => camera(canvas.clientWidth, canvas.clientHeight);
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

  // paint draws the stack as it is shown now onto g, w by h pixels; on
  // screen it also keeps where each disc landed, for hit tests.
  function paint(g, w, hgt, onScreen) {
    const cur = state.cur;
    if (onScreen) {
      state.screen = [];
      state.lines = [];
    }
    if (!cur) return;
    const c = camera(w, hgt);
    const flat = Math.max(0.12, Math.abs(Math.sin(state.el))); // how round a disc looks from here

    // Layers, bottom up, each a faint square named at its leftmost corner,
    // as wide as the spread packages.
    const { cx: bx, cz: bz } = state.box;
    const half = state.box.half * state.shownApart;
    g.font = "11px 'JetBrains Mono', monospace";
    state.planes.forEach((v, l) => {
      if (v < 0.01) return;
      const y = l * state.gap;
      const pts = [[-half, -half], [half, -half], [half, half], [-half, half]].map(([x, z]) => project(c, bx + x, y, bz + z));
      g.beginPath();
      pts.forEach(([x, yy], i) => (i ? g.lineTo(x, yy) : g.moveTo(x, yy)));
      g.closePath();
      g.fillStyle = rgba(C.muted, 0.035 * v);
      g.fill();
      g.strokeStyle = rgba(C.muted, 0.2 * v);
      g.lineWidth = 1;
      g.stroke();
      const left = pts.reduce((a, b) => (b[0] < a[0] ? b : a));
      g.fillStyle = rgba(C.muted, 0.8 * v);
      g.textAlign = 'right';
      g.textBaseline = 'middle';
      g.fillText(`L${l}`, left[0] - 8, left[1]);
    });

    // Where each package lands this time round, and how it is drawn.
    const spots = new Map();
    state.shown.forEach((s, p) => {
      if (s.a < 0.02 || s.r <= 0) return;
      const [x, y, dz, sc] = project(c, worldX(p), s.y, worldZ(p));
      const st = (opts.style && opts.style(p)) || {};
      spots.set(p, { p, x, y, dz, rx: s.r * sc, ry: s.r * sc * flat, a: s.a * (st.alpha ?? 1), st });
    });

    // Dependencies: a lit package's own, or the strongest; cycles always;
    // the chosen one drawn out over the rest, last.
    const L = state.lit;
    const ch = state.chosen;
    const isChosen = (d) => !!ch && d.from === ch.from && d.to === ch.to;
    const end = (p) => !!ch && (p === ch.from || p === ch.to);
    const near = new Map(); // package → 'out' | 'in', next to the lit one
    let deps = cur.strongest;
    if (L >= 0) {
      deps = cur.deps.filter((d) => d.from === L || d.to === L);
      for (const d of deps) {
        if (d.from === L) near.set(d.to, 'out');
        else if (!near.has(d.from)) near.set(d.from, 'in');
      }
    }
    deps = deps.concat(cur.deps.filter((d) => (d.cyc || isChosen(d)) && !deps.includes(d)));
    if (ch) deps.sort((p, q) => isChosen(p) - isChosen(q));
    const dim = (p) => {
      if (end(p)) return 1;
      const k = L >= 0 && p !== L && !near.has(p) ? (state.strong ? 0.1 : 0.35) : 1;
      return ch ? Math.min(k, 0.35) : k;
    };
    for (const d of deps) {
      const a = spots.get(d.from), b = spots.get(d.to);
      if (!a || !b) continue;
      const weight = Math.sqrt(d.refs / cur.maxRefs);
      let hue = C.text2, alpha = 0.12 + 0.4 * weight, width = 0.8 + 1.4 * weight;
      if (d.cyc) {
        hue = C.deleted;
        alpha = 0.85 * Math.min(dim(d.from), dim(d.to));
        width = 1.6;
      } else if (L >= 0) {
        hue = d.from === L ? C.accent : C.moved;
        alpha = d.from === L ? 0.9 : 0.85;
        width = 1.2 + 1.6 * weight;
      }
      if (isChosen(d)) {
        if (!d.cyc && L < 0) hue = C.accent;
        alpha = 1;
        width += 1.8;
      } else if (ch) alpha *= 0.25;
      width *= Math.min(a.a, b.a);
      const tip = arrow(g, a, b, rgba(hue, alpha), width);
      if (onScreen && tip) state.lines.push({ d, x1: a.x, y1: a.y, x2: tip[0], y2: tip[1], w: width });
    }

    // Discs, far to near.
    const order = [...spots.values()].sort((p, q) => q.dz - p.dz);
    for (const s of order) {
      const isLit = s.p === L, rel = near.get(s.p), cyc = cur.inCycle.has(s.p), st = s.st;
      const sh = state.shown[s.p];
      const a = s.a * dim(s.p);
      const ring = [];
      for (let i = 0; i <= SEGS; i++) {
        const t = (i / SEGS) * 2 * Math.PI;
        ring.push(project(c, worldX(s.p) + sh.r * Math.cos(t), sh.y, worldZ(s.p) + sh.r * Math.sin(t)));
      }
      const outline = () => {
        g.beginPath();
        ring.forEach(([x, y], i) => (i ? g.lineTo(x, y) : g.moveTo(x, y)));
        g.closePath();
      };
      outline();
      g.globalAlpha = 0.92 * a;
      g.fillStyle = isLit ? C.accentBg : st.fill || C.disc;
      g.fill();
      if (st.busy && !isLit) {
        g.globalAlpha = 0.3 * a;
        g.lineWidth = 4;
        g.strokeStyle = C.accent;
        g.stroke();
      }
      if (st.ring && !isLit && !rel) {
        // Who knows it, as arcs round the rim.
        g.globalAlpha = a;
        g.lineWidth = Math.max(2, Math.min(5, 0.22 * s.rx));
        let from = 0;
        for (const [col, share] of st.ring) {
          const to = Math.min(SEGS, Math.round((from + share) * SEGS));
          const i0 = Math.round(from * SEGS);
          if (to > i0) {
            g.beginPath();
            for (let i = i0; i <= to; i++) (i === i0 ? g.moveTo : g.lineTo).call(g, ring[i][0], ring[i][1]);
            g.strokeStyle = col;
            g.stroke();
          }
          from += share;
        }
      } else {
        const marked = st.mark === 'followed' ? C.followed : st.mark === 'focus' ? C.accent : null;
        const strong = isLit || rel || cyc || marked;
        g.globalAlpha = (strong ? 0.95 : 0.55) * a;
        g.strokeStyle = isLit ? C.accent : rel === 'out' ? C.accent : rel === 'in' ? C.moved : marked || (cyc ? C.deleted : C.rim);
        g.lineWidth = strong ? 1.8 : 1;
        outline();
        g.stroke();
      }
      if (sh.flash > 0) {
        // A ring spreading out from a package born or moved.
        const born = sh.flash > MOVED;
        const k = 1 - sh.flash / (born ? BORN : MOVED);
        const grow = 1 + (born ? 1.4 : 0.8) * k;
        g.globalAlpha = (1 - k) * (born ? 0.9 : 0.6) * s.a;
        g.beginPath();
        g.ellipse(s.x, s.y, Math.max(3, s.rx) * grow + 2, Math.max(3 * flat, s.ry) * grow + 2, 0, 0, 2 * Math.PI);
        g.lineWidth = 1.5;
        g.strokeStyle = born ? C.accent : C.moved;
        g.stroke();
      }
      g.globalAlpha = 1;
      if (onScreen) state.screen.push(s);
    }

    // Names: the lit package and its neighbours first, then the marked
    // ones and the largest, skipping any that would overlap one already
    // placed.
    g.font = "11px 'IBM Plex Sans', system-ui, sans-serif";
    g.textAlign = 'center';
    g.textBaseline = 'bottom';
    const boxes = [];
    const rank = (s) => (s.p === L ? 4e6 : near.has(s.p) ? 3e6 + s.rx : s.st.mark ? 2e6 + s.rx : s.rx);
    for (const s of order.slice().sort((p, q) => rank(q) - rank(p))) {
      const forced = s.p === L || end(s.p) || (!ch && near.has(s.p)) || (L < 0 && !ch && s.st.mark);
      if (!forced && (s.rx < NAMED || L >= 0)) continue;
      const text = label(s.p);
      const tw = g.measureText(text).width;
      const box = [s.x - tw / 2 - 3, s.y - s.ry - 17, s.x + tw / 2 + 3, s.y - s.ry - 2];
      if (!forced && boxes.some((b) => box[0] < b[2] && box[2] > b[0] && box[1] < b[3] && box[3] > b[1])) continue;
      boxes.push(box);
      g.lineWidth = 3;
      g.strokeStyle = rgba(C.bg, 0.85 * s.a);
      g.strokeText(text, s.x, s.y - s.ry - 4);
      g.fillStyle = rgba(s.p === L || s.st.mark ? C.text : C.text2, s.a);
      g.fillText(text, s.x, s.y - s.ry - 4);
    }
  }

  // arrow draws a line from the middle of disc a to the rim of disc b, with
  // a head there, and returns where the head's point landed; null when the
  // discs overlap on screen and nothing is drawn.
  function arrow(g, a, b, col, width) {
    const dx = b.x - a.x, dy = b.y - a.y, len = Math.hypot(dx, dy);
    if (len < 1) return null;
    const ux = dx / len, uy = dy / len;
    const rim = 1 / Math.sqrt((ux / Math.max(b.rx, 0.5)) ** 2 + (uy / Math.max(b.ry, 0.5)) ** 2);
    const end = Math.max(0, len - rim - 1);
    const ex = a.x + ux * end, ey = a.y + uy * end;
    g.strokeStyle = col;
    g.lineWidth = width;
    g.beginPath();
    g.moveTo(a.x, a.y);
    g.lineTo(ex, ey);
    g.stroke();
    const hs = 4 + width * 1.5;
    g.fillStyle = col;
    g.beginPath();
    g.moveTo(ex, ey);
    g.lineTo(ex - ux * hs - uy * hs * 0.55, ey - uy * hs + ux * hs * 0.55);
    g.lineTo(ex - ux * hs + uy * hs * 0.55, ey - uy * hs - ux * hs * 0.55);
    g.closePath();
    g.fill();
    return [ex, ey];
  }

  function draw() {
    const dpr = devicePixelRatio || 1;
    const w = canvas.clientWidth, hgt = canvas.clientHeight;
    if (!w || !hgt) return;
    if (canvas.width !== Math.round(w * dpr) || canvas.height !== Math.round(hgt * dpr)) {
      canvas.width = Math.round(w * dpr);
      canvas.height = Math.round(hgt * dpr);
    }
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, w, hgt);
    paint(ctx, w, hgt, true);
  }

  function loop() {
    if (!state.running) return;
    if (ease() || state.dirty) {
      state.dirty = false;
      draw();
    }
    requestAnimationFrame(loop);
  }

  // under returns the disc drawn under the point, nearest first, or null.
  function under(x, y) {
    let best = null;
    for (const s of state.screen) {
      const rx = Math.max(s.rx, 6), ry = Math.max(s.ry, 6);
      if (((x - s.x) / rx) ** 2 + ((y - s.y) / ry) ** 2 <= 1 && (!best || s.dz < best.dz)) best = s;
    }
    return best;
  }
  // hit returns the package whose disc is under the point, -1 for none.
  const hit = (x, y) => under(x, y)?.p ?? -1;
  // lineAt returns the dependency whose line is drawn nearest the point,
  // within a few pixels of it, or null; of two as near, the one drawn
  // later, on top.
  function lineAt(x, y) {
    let best = null, bd = Infinity;
    for (const l of state.lines) {
      const dx = l.x2 - l.x1, dy = l.y2 - l.y1, len2 = dx * dx + dy * dy;
      const t = len2 ? Math.max(0, Math.min(1, ((x - l.x1) * dx + (y - l.y1) * dy) / len2)) : 0;
      const d = Math.hypot(x - (l.x1 + t * dx), y - (l.y1 + t * dy));
      if (d <= Math.max(5, l.w / 2 + 3) && d <= bd) {
        best = l.d;
        bd = d;
      }
    }
    return best;
  }
  // pointAt is what the point is over: a disc, or failing that, when lines
  // can be picked, a dependency's line.
  function pointAt(x, y) {
    const p = hit(x, y);
    return { p, d: p < 0 && opts.onPickDep ? lineAt(x, y) : null };
  }
  // depthAt is how far in front of camera c the point is: the depth of the
  // disc under it, or of the point the camera looks at. The wheel can turn
  // twice between drawings, so the depth is taken afresh.
  function depthAt(x, y, c) {
    const s = under(x, y);
    return s ? project(c, worldX(s.p), state.shown[s.p].y, worldZ(s.p))[2] : dist();
  }

  // neighbours lists what package p refers to and what refers to it in the
  // current frame, strongest first.
  function neighbours(p) {
    const out = [], into = [];
    if (state.cur) {
      for (const d of state.cur.deps) {
        if (d.from === p) out.push(d);
        else if (d.to === p) into.push(d);
      }
    }
    const by = (a, b) => b.refs - a.refs;
    return { out: out.sort(by), into: into.sort(by) };
  }

  // ---- the camera ----

  function pressView(v) {
    state.view = v;
    if (opts.views) for (const b of opts.views.children) b.setAttribute('aria-pressed', String(b.dataset.v === v));
  }

  function look(v) {
    pressView(v);
    state.dirty = true;
    if (v === 'auto') return;
    state.aim = VIEWS[v].slice();
    // Turn the short way round.
    const tau = 2 * Math.PI;
    state.az = ((state.az % tau) + tau) % tau;
    if (state.aim[0] - state.az > Math.PI) state.aim[0] -= tau;
    if (state.az - state.aim[0] > Math.PI) state.aim[0] += tau;
  }

  // zoomBy zooms by k toward the point (x, y) on the canvas, which stays
  // under the pointer, or toward the middle of the canvas without one.
  function zoomBy(k, x, y) {
    const z = Math.max(0.5, Math.min(8, state.zoom * k));
    if (x != null) {
      // The point P under the pointer, at depth D, lies (D − d)·f +
      // D·(sx·r − sy·u)/focal from the target. Moving the camera and the
      // target toward P by the share the distance shrinks keeps P where it
      // is on screen.
      const c = onCanvas(), sx = x - c.cx, sy = y - c.cy, d = dist(), D = depthAt(x, y, c), m = 1 - dist(z) / d;
      move([0, 1, 2].map((i) => ((D - d) * c.f[i] + (D * (sx * c.r[i] - sy * c.u[i])) / c.focal) * m));
    }
    state.zoom = z;
    state.dirty = true;
  }

  // panBy moves the stack dx, dy pixels on screen, as measured at depth in
  // front of the camera: by default, at the point it looks at.
  function panBy(dx, dy, depth = dist()) {
    const c = onCanvas(), m = depth / c.focal;
    move([0, 1, 2].map((i) => (dy * c.u[i] - dx * c.r[i]) * m));
  }

  if (opts.views) for (const b of opts.views.children) b.addEventListener('click', () => look(b.dataset.v));

  let drag = null;
  const at = (e) => {
    const r = canvas.getBoundingClientRect();
    return [e.clientX - r.left, e.clientY - r.top];
  };
  canvas.addEventListener('pointerdown', (e) => {
    if (e.button > 2) return;
    if (e.button === 1) e.preventDefault(); // no scrolling by the middle button
    // The left button turns the camera, unless Shift is held; the others
    // pan, the disc grabbed keeping under the pointer.
    const pan = e.button > 0 || e.shiftKey;
    drag = {
      x: e.clientX, y: e.clientY, lx: e.clientX, ly: e.clientY, az: state.az, el: state.el,
      pan, depth: pan ? depthAt(...at(e), onCanvas()) : 0, pick: e.button === 0, moved: false,
    };
    canvas.setPointerCapture(e.pointerId);
  });
  // The right button pans, so it opens no menu.
  canvas.addEventListener('contextmenu', (e) => e.preventDefault());
  canvas.addEventListener('pointermove', (e) => {
    if (drag) {
      const dx = e.clientX - drag.x, dy = e.clientY - drag.y;
      if (!drag.moved && Math.hypot(dx, dy) < 4) return;
      if (!drag.moved) {
        drag.moved = true;
        canvas.classList.add('dragging');
        if (!drag.pan) pressView('');
        opts.onHover?.(-1, 0, 0);
      }
      if (drag.pan) {
        panBy(e.clientX - drag.lx, e.clientY - drag.ly, drag.depth);
        drag.lx = e.clientX;
        drag.ly = e.clientY;
        return;
      }
      state.aim = null;
      state.az = drag.az - dx * 0.008;
      state.el = Math.max(0.02, Math.min(1.55, drag.el + dy * 0.006));
      state.dirty = true;
      return;
    }
    const [x, y] = at(e);
    const { p, d } = pointAt(x, y);
    canvas.classList.toggle('over', p >= 0 || !!d);
    opts.onHover?.(p, x, y, d);
  });
  const endDrag = (e) => {
    if (!drag) return;
    if (!drag.moved && drag.pick && e.type === 'pointerup') {
      const { p, d } = pointAt(...at(e));
      if (d) opts.onPickDep(d);
      else opts.onPick?.(p);
    }
    drag = null;
    canvas.classList.remove('dragging');
  };
  canvas.addEventListener('pointerup', endDrag);
  canvas.addEventListener('pointercancel', endDrag);
  canvas.addEventListener('pointerleave', () => {
    if (drag) return;
    canvas.classList.remove('over');
    opts.onHover?.(-1, 0, 0);
  });
  canvas.addEventListener('wheel', (e) => {
    e.preventDefault();
    zoomBy(Math.exp(-e.deltaY * (e.deltaMode === 1 ? 0.05 : e.deltaMode ? 1 : 0.0015)), ...at(e));
  }, { passive: false });
  new ResizeObserver(() => { state.dirty = true; }).observe(canvas);
  document.fonts?.ready.then(() => { state.dirty = true; });

  pressView(state.view);
  if (state.view !== 'auto') [state.az, state.el] = VIEWS[state.view];

  return {
    setData,
    setFrame, // takes a frame, returns it unpacked
    get cur() { return state.cur; },
    neighbours,
    hit,
    look, // 'side' | 'angle' | 'top' | 'auto'
    get view() { return state.view; },
    zoomBy, // (k), toward the middle; (k, x, y), toward that point on the canvas
    // fit zooms back out to the whole stack, looking at its middle.
    fit() {
      state.zoom = 1;
      state.pan = [0, 0, 0];
      state.dirty = true;
    },
    setSpread, // the room between the packages, against SPREAD_ROOM
    get zoom() { return state.zoom; },
    // setLit draws package p's references, -1 for the strongest of all;
    // strong fades the rest almost away.
    setLit(p, strong = false) {
      if (state.lit === p && state.strong === strong) return;
      state.lit = p;
      state.strong = strong;
      state.dirty = true;
    },
    // setChosen draws dependency d, { from, to }, out over the others, and
    // its two packages over theirs; null for none.
    setChosen(d) {
      const ch = state.chosen;
      if (ch === d || (ch && d && ch.from === d.from && ch.to === d.to)) return;
      state.chosen = d ? { from: d.from, to: d.to } : null;
      state.dirty = true;
    },
    setPlaying(on) { state.playing = on; state.dirty = true; },
    // fromAbove puts the camera over the map, to come down from there.
    fromAbove() { state.el = AUTO_EL[0]; state.dirty = true; },
    // reset empties the stack and puts the camera over the map, to build
    // the history up from nothing.
    reset() {
      for (const sh of state.shown) Object.assign(sh, { y: 0, r: 0, a: 0, layer: -1, flash: 0 });
      state.planes = [];
      state.ty = 0;
      state.el = AUTO_EL[0];
      state.dirty = true;
    },
    redraw() { state.dirty = true; },
    // start and stop drawing on the canvas, once per screen refresh.
    start() {
      if (state.running) return;
      state.running = true;
      state.dirty = true;
      requestAnimationFrame(loop);
    },
    stop() { state.running = false; },
    // step and paint draw away from the screen, one drawing at a time.
    step: ease,
    paint(g, w, hgt) { paint(g, w, hgt, false); },
  };
}
