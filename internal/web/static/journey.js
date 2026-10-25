'use strict';

// The journey page: the history played back frame by frame, one bubble per
// package, with the story told alongside. In story mode playback pauses at
// key moments to show what the commit changed; a package can be opened to
// watch its symbols, and one symbol followed through renames and moves.
// common.js provides KINDS, h, $, mark, api, plural, waitReady and fail;
// vendor/d3.min.js provides d3, for zooming, tweens, edges and packing.
(() => {
  const ACT = ['#2A2418', '#4A3A1E', '#7A5A26', '#B07F33', '#F0A64A'];
  const BASE_MS = 1100; // per frame at 1×
  const PAUSE_MS = 3800; // per key moment at 1×, in story mode
  const PER_FRAME = 3; // key moments shown per frame while playing
  const TAG_MS = 1800; // how long event tags stay on a package's symbols
  const MAX_TAGS = 8; // event tags per frame
  const DAY = 86400;
  const MAX_ZOOM = 8; // at least; a map of many packages zooms further, see place
  const NAMED = 48; // how wide on screen the deepest zoom draws a typical package
  const MAX_GROW = 6; // how far past its place a package grows on a sparse map
  const MAX_FIT = 4; // how far the map closes in on its first few packages
  const BUNDLE = 0.85; // how tightly dependencies between groups bundle, 0 to 1
  const OPEN = 60; // how wide on screen a group grows before it opens to show its packages
  // Where a bubble d pixels wide on screen is named: inside it if its name
  // fits there, else beneath it once it is NAME_UNDER wide, else not at
  // all. A closed group is always named beneath.
  const NAME_UNDER = 14;
  function nameAt(el, id, d) {
    if (d < NAME_UNDER) return 'small';
    if (isGroup(id) || d < 6.6 * el.firstChild.textContent.length + 10) return 'under';
    return '';
  }
  const nameClass = (el, id, d) => { const c = nameAt(el, id, d); return c ? ` ${c}` : ''; };
  // How many dependencies are drawn at once, by the lines setting: the
  // strongest with an end on screen; the others show when one end is lit.
  const LINES = { key: 30, hover: 0, all: Infinity };
  // A lit dependency's arrowheads, one way and both ways, in a 10×10 box.
  const ARROW = 'M2,1 L9,5 L2,9 z', ARROWS = 'M0,5 L4,1 L4,9 z M10,5 L6,1 L6,9 z';
  const reduced = matchMedia('(prefers-reduced-motion: reduce)');
  // How long size, colour, edge and counter changes take, at the current speed.
  const tweenMs = () => (reduced.matches || state.instant ? 0 : 600 / state.speed);
  const zoomMs = () => (reduced.matches ? 0 : 650);

  const state = {
    step: 'week',
    speed: 1,
    data: null, // the /api/journey response for the current step
    cache: {}, // responses by step
    pathIdx: new Map(), // package path → index
    frame: 0,
    timer: null,
    playing: false,
    queue: [], // key moments still to show in this frame while playing
    narrate: false,
    spoken: -1, // chapter last narrated
    maxEdits: 1,
    refs: null, // dependency stroke width by references between two packages
    bubbles: [], // elements by node: the packages by index, then the groups after them
    diam: [], // bubble diameters by package index, before zooming
    view: null, // the part of the map the frame shows: { cx, cy, z, fx, fy }
    zoom: null, // the stage's d3 zoom behaviour
    maxZoom: MAX_ZOOM,
    zt: d3.zoomIdentity, // its current transform
    outer: null, // the transform of the package map while a package is open
    story: true, // pause at key moments
    moments: [], // the story's key moments and the followed symbol's, by revision
    card: null, // the moment on the card
    reports: new Map(), // sha → promise of its /api/commit report
    views: new Map(), // "step dir keep" → /api/journey/package response
    drill: null, // the package looked into: { dir, pi, v, cur, names, at, dots, peak, pos, files, fileAt }
    follow: null, // the followed symbol: { id, sym, moments }
    switching: null, // the package a followed symbol is being chased into
    folDir: undefined, // the followed symbol's package at the last render
    colorBy: 'edits', // or 'knowledge': ring the packages by who knows them
    lines: 'key', // which dependencies are drawn unlit: 'key', 'hover' or 'all'
    who: '', // the author whose packages the map keeps, dimmed by their share, if one
    edges: null, // the stage's dependencies by key, at the current frame
    chains: [], // each package's groups, innermost first
    gchains: [], // each group's enclosing groups, innermost first
    unit: 0, // a bubble's radius per square root of a symbol, in the unit square
    open: new Set(), // the groups opened at the last render
    instant: false, // render without tweens, as when zooming opens a group
    hover: null, // the package under the pointer
    focused: null, // the package with keyboard focus
    picked: null, // the package clicked once: the map narrows to it and its neighbours
    links: null, // the package in the breakout: { dir, rev, v, nodes, adj, wires, lit }
    tip: null, // the node the hover card describes: { node } on the map or { sym } in a package
  };

  const fmtShort = (t) => new Date(t * 1000).toLocaleDateString(undefined, { month: 'short', day: 'numeric', timeZone: 'UTC' });
  const fmtBig = (t) => new Date(t * 1000).toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric', timeZone: 'UTC' });
  const base = (p) => (p === '.' ? '(root)' : p.split('/').pop());
  const dirOf = (p) => (p.includes('/') ? p.slice(0, p.lastIndexOf('/')) : '.');
  const encDir = (d) => (d === '.' ? '' : d.split('/').map(encodeURIComponent).join('/'));
  const shortName = (n) => n.slice(n.lastIndexOf('.') + 1);

  // Every author keeps one colour, whatever the step or the frame: a hue
  // from their name. Departed authors are grey.
  const GONE = '#5A5F68', OTHERS = '#2E343D', RING_IN = '#14181E';
  const hues = new Map();
  function authorColor(name) {
    let c = hues.get(name);
    if (!c) {
      let x = 2166136261;
      for (let i = 0; i < name.length; i++) x = Math.imul(x ^ name.charCodeAt(i), 16777619);
      c = `hsl(${(x >>> 0) % 360} 62% 62%)`;
      hues.set(name, c);
    }
    return c;
  }
  const pct = (x) => `${Math.round(100 * x)}%`;

  // ring draws a package's knowers as a ring: one arc per knower by share,
  // grey for departed ones, dark for everyone below the top few.
  function ring(knowers) {
    const stops = [];
    let at = 0;
    for (const [a, share, gone] of knowers) {
      const c = gone ? GONE : authorColor(state.data.authors[a]);
      stops.push(`${c} ${(100 * at).toFixed(2)}% ${(100 * (at + share)).toFixed(2)}%`);
      at += share;
    }
    if (at < 0.999) stops.push(`${OTHERS} ${(100 * at).toFixed(2)}% 100%`);
    return `radial-gradient(closest-side, ${RING_IN} 64%, transparent 66%), conic-gradient(${stops.join(', ')})`;
  }

  // shareOf is an author's share of a package in a frame, 0 when they are
  // not among its top knowers.
  function shareOf(s, a) {
    if (!s || !s.knowers) return 0;
    const k = s.knowers.find((x) => x[0] === a);
    return k ? k[1] : 0;
  }

  // scoped is frame f with only the picked author's packages, the ones they
  // are among the top knowers of, and the dependencies between them.
  function scoped(f) {
    if (!state.who) return f;
    const a = state.data.authors.indexOf(state.who);
    const pkgs = f.pkgs.filter((s) => shareOf(s, a) > 0);
    const keep = new Set(pkgs.map((s) => s.p));
    return { ...f, pkgs, deps: f.deps.filter(([x, y]) => keep.has(x) && keep.has(y)) };
  }

  function knowersText(s) {
    if (!s || !s.knowers) return '';
    const names = s.knowers.slice(0, 3).map(([a, share, gone]) => `${state.data.authors[a]}${gone ? ' (left)' : ''} ${pct(share)}`);
    let t = ` · knows it: ${names.join(', ')} · bus factor ${s.bus}`;
    if (s.orphaned > 0) t += ` · ${pct(s.orphaned)} orphaned`;
    return t;
  }

  // The revision index the playhead is at: the last one in the frame.
  const rev = () => state.data.frames[state.frame].total - 1;
  const chapterAt = (r) => {
    const cs = state.data.chapters;
    for (let i = cs.length - 1; i >= 0; i--) if (r >= cs[i].firstRev) return i;
    return 0;
  };
  const frameRange = (i) => {
    const f = state.data.frames[i];
    return [f.total - f.commits, f.total - 1];
  };
  const inFrame = (m, i) => {
    const [lo, hi] = frameRange(i);
    return m.rev >= lo && m.rev <= hi;
  };

  // ---------- Loading ----------

  async function load(step) {
    if (!state.cache[step]) state.cache[step] = await api(`/api/journey?step=${step}`);
    return state.cache[step];
  }

  async function setStep(step, keepRev) {
    stop();
    const at = keepRev ?? (state.data ? rev() : 0);
    state.step = step;
    state.data = await load(step);
    state.pathIdx = new Map(state.data.packages.map((p, i) => [p.path, i]));
    const { groups } = state.data;
    state.chains = state.data.packages.map((p) => {
      const c = [];
      for (let g = p.group; g >= 0; g = groups[g].parent) c.push(g);
      return c;
    });
    state.gchains = groups.map((g) => {
      const c = [];
      for (let u = g.parent; u >= 0; u = groups[u].parent) c.push(u);
      return c;
    });
    const sized = state.data.packages.find((p) => p.peak > 0);
    state.unit = sized ? sized.r / Math.sqrt(sized.peak) : 0;
    for (const b of $('step').querySelectorAll('button')) b.setAttribute('aria-pressed', String(b.dataset.v === step));
    let max = 1, maxRefs = 1;
    for (const f of state.data.frames) {
      for (const p of f.pkgs) max = Math.max(max, editsOf(p));
      for (const dep of f.deps) maxRefs = Math.max(maxRefs, dep[2] || 0);
    }
    state.maxEdits = max;
    buildAuthors();
    state.refs = d3.scaleSqrt().domain([0, maxRefs]).range([0.7, 2.6]).clamp(true);
    // Moments are per step; so is the card's.
    state.card = null;
    $('card').hidden = true;
    mergeMoments();
    // Stay on the same revision across steps.
    const fs = state.data.frames;
    let i = fs.findIndex((f) => f.total - 1 >= at);
    if (i < 0) i = fs.length - 1;
    state.frame = Math.max(0, i);
    // The package view is per step; open it again once the stage is built.
    const dir = state.drill && state.drill.dir;
    state.drill = null;
    buildRows();
    buildChapterList();
    buildStage();
    if (dir) await drill(dir);
    seek(state.frame, true);
  }

  const editsOf = (p) => (p.kinds ? Object.values(p.kinds).reduce((a, b) => a + b, 0) : 0);

  // Picks a default step that gives a playable number of frames.
  function defaultStep(st) {
    const days = (st.to - st.from) / DAY;
    if (days >= 540) return 'month';
    if (days >= 84) return 'week';
    return 'commit';
  }

  // report returns the commit's semantic diff, fetched once.
  function report(sha) {
    let p = state.reports.get(sha);
    if (!p) {
      p = api(`/api/commit/${sha}`);
      p.catch(() => state.reports.delete(sha));
      state.reports.set(sha, p);
    }
    return p;
  }

  // ---------- Stage ----------

  function buildStage() {
    const { packages } = state.data;
    const wrap = $('bubbles');
    wrap.replaceChildren();
    state.hover = state.focused = state.picked = null;
    state.bubbles = packages.map((p, i) => {
      // The first click narrows the map to the package and its neighbours;
      // a second one breaks it out in full.
      const choose = () => (state.picked === i ? openLinks(p.path).catch(fail) : pick(i));
      const el = h('button', { type: 'button', class: 'bubble gone', tabindex: '-1', onclick: choose },
        h('span', { class: 'bname' }, base(p.path)));
      wrap.append(hoverable(el, i));
      return el;
    });
    // Each group has a bubble too, drawn in place of its packages while it is
    // too small on screen to open; a click zooms in until it does.
    state.data.groups.forEach((g, gi) => {
      const el = h('button', { type: 'button', class: 'bubble agg gone', tabindex: '-1', onclick: () => zoomToGroup(gi) },
        h('span', { class: 'bname' }, `${groupName(gi)}/`));
      wrap.append(hoverable(el, packages.length + gi));
      state.bubbles.push(el);
    });
    state.diam = [];
    $('deps').replaceChildren();
    buildLayers();
    place();
  }

  // hoverable lights node id's dependencies and describes it while el is
  // under the pointer or has focus.
  function hoverable(el, id) {
    el.addEventListener('pointerenter', (e) => {
      if (e.pointerType === 'touch') return;
      setLit('hover', id);
      showTip({ node: id });
    });
    el.addEventListener('pointerleave', () => { setLit('hover', null); hideTip(); });
    el.addEventListener('focus', () => { setLit('focused', id); showTip({ node: id }); });
    el.addEventListener('blur', () => { setLit('focused', null); hideTip(); });
    return el;
  }

  // groupName is group g's path below its parent's.
  function groupName(g) {
    const { groups } = state.data;
    const up = groups[g].parent < 0 ? '.' : groups[groups[g].parent].path;
    return up === '.' ? groups[g].path : groups[g].path.slice(up.length + 1);
  }

  // place positions the bubbles, lines and symbols for the stage's size.
  function place() {
    if (!state.data) return;
    const st = $('stage');
    const W = st.clientWidth, H = st.clientHeight;
    // Leave room for the overlays at the top and bottom.
    const top = 86, bottom = 56;
    const size = Math.max(120, Math.min(W - 40, H - top - bottom));
    const x0 = (W - size) / 2, y0 = top + (H - top - bottom - size) / 2;
    state.geom = { size, x0, y0 };
    // Zooming goes as deep as it takes to name a typical package, so a map
    // of thousands can be read up close.
    const rs = state.data.packages.map((p) => p.r).filter((r) => r > 0).sort((a, b) => a - b);
    const mid = rs.length ? rs[rs.length >> 1] : 1;
    state.maxZoom = Math.min(128, Math.max(MAX_ZOOM, NAMED / (2 * mid * size)));
    state.zoom.scaleExtent([1, state.maxZoom]);
    state.data.packages.forEach((p, i) => placeBubble(i));
    $('deps').setAttribute('viewBox', `0 0 ${W} ${H}`);
    d3.select('#deps').selectAll(':scope > path').attr('d', (e) => (e.d = edgePath(e)));
    placeDisc({ x: x0 + size / 2, y: y0 + size / 2, r: 0.48 * size });
    state.zoom.extent([[0, 0], [W, H]]).translateExtent([[0, 0], [W, H]]);
    if (state.data.frames.length) render(false);
  }

  // fitView frames the packages live in frame f. Early on they huddle in a
  // corner of the finished map, so the view closes in on them, and eases out
  // to the whole map as it fills.
  function fitView(f) {
    const { packages } = state.data;
    const box = (ps) => {
      const b = { x0: Infinity, y0: Infinity, x1: -Infinity, y1: -Infinity };
      for (const p of ps) {
        b.x0 = Math.min(b.x0, p.x - p.r); b.x1 = Math.max(b.x1, p.x + p.r);
        b.y0 = Math.min(b.y0, p.y - p.r); b.y1 = Math.max(b.y1, p.y + p.r);
      }
      return b;
    };
    const all = box(packages);
    const fx = (all.x0 + all.x1) / 2, fy = (all.y0 + all.y1) / 2;
    const live = box(f.pkgs.filter((s) => s.live > 0).map((s) => packages[s.p]));
    if (live.x0 > live.x1) return { cx: fx, cy: fy, z: 1, fx, fy };
    const pad = 1.3;
    const z = Math.max(1, Math.min(MAX_FIT,
      (all.x1 - all.x0) / (pad * (live.x1 - live.x0)), (all.y1 - all.y0) / (pad * (live.y1 - live.y0))));
    // Keep the view inside the finished map, so that at z = 1 it is the map.
    const keep = (c, lo, hi) => {
      const half = (hi - lo) / 2 / z;
      return Math.min(hi - half, Math.max(lo + half, c));
    };
    return { cx: keep((live.x0 + live.x1) / 2, all.x0, all.x1), cy: keep((live.y0 + live.y1) / 2, all.y0, all.y1), z, fx, fy };
  }

  // at places package pi on the stage, through the frame's view.
  const at = (pi) => toStage(state.data.packages[pi]);

  // A node is a package, by index, or a group, numbered after the packages.
  const isGroup = (id) => id >= state.data.packages.length;
  const groupOf = (id) => state.data.groups[id - state.data.packages.length];
  const nodeAt = (id) => (isGroup(id) ? toStage(groupOf(id)) : at(id));
  const chainOf = (id) => (isGroup(id) ? state.gchains[id - state.data.packages.length] : state.chains[id]);
  const nodeName = (id) => (isGroup(id) ? `${groupOf(id).path}/` : state.data.packages[id].path);

  // toStage places a point of the map's unit square, a package's or a
  // group's center, on the stage through the frame's view.
  function toStage(p) {
    const { size, x0, y0 } = state.geom;
    const v = state.view;
    if (!v) return { x: x0 + p.x * size, y: y0 + p.y * size };
    return { x: x0 + size * (v.fx + (p.x - v.cx) * v.z), y: y0 + size * (v.fy + (p.y - v.cy) * v.z) };
  }

  function placeBubble(i) {
    const { x, y } = at(i);
    const el = state.bubbles[i];
    el.style.left = `${x}px`;
    el.style.top = `${y}px`;
  }

  // fold decides which groups are open at the frame's view and the zoom: the
  // outermost always, and any other on screen once it is OPEN pixels wide
  // there inside an open parent, so that a large map shows its parts before
  // their packages. The groups around the packages in keep stay open. It returns
  // the open groups and, per package, the node it is drawn as: itself, or
  // the outermost closed group around it.
  function fold(keep) {
    const { groups, packages } = state.data;
    const n = packages.length;
    const px = state.geom.size * state.view.z * state.zt.k;
    const st = $('stage'), W = st.clientWidth, H = st.clientHeight, t = state.zt;
    const seen = (g) => {
      const { x, y } = toStage(g), r = g.r * px;
      const sx = t.applyX(x), sy = t.applyY(y);
      return sx + r >= 0 && sx - r <= W && sy + r >= 0 && sy - r <= H;
    };
    const open = new Set();
    groups.forEach((g, i) => {
      if (g.parent < 0 || (open.has(g.parent) && g.r * px >= OPEN && seen(g))) open.add(i);
    });
    for (const p of keep) if (p != null && p < n) for (const g of state.chains[p]) open.add(g);
    const front = packages.map((p, i) => {
      const c = state.chains[i];
      for (let x = c.length - 1; x >= 0; x--) if (!open.has(c[x])) return n + c[x];
      return i;
    });
    return { open, front };
  }

  // kept are the packages whose groups stay open: the picked one and the
  // followed symbol's.
  function kept() {
    const fol = followNow();
    return [state.picked, fol.dir != null ? state.pathIdx.get(fol.dir) : undefined];
  }

  // edgePath draws dependency e between two nodes. Between nodes of different
  // groups it is bundled: it bends through the centers of the groups it
  // leaves and enters below the innermost one holding both, so that the dependencies between
  // two parts of the tree run together as one strand and fan out only at
  // their ends. Between packages of one group it curves away from the
  // group's middle. Its ends are under the bubbles. It notes on e where its
  // middle is and which way it runs there, for the arrow of a lit dependency.
  function edgePath(e) {
    const { groups } = state.data;
    const ca = chainOf(e.a), cb = chainOf(e.b);
    let i = ca.length - 1, k = cb.length - 1;
    while (i >= 0 && k >= 0 && ca[i] === cb[k]) { i--; k--; }
    const a = nodeAt(e.a), b = nodeAt(e.b);
    // Collects the curve as cubic Béziers, [x0, y0, c1x, c1y, c2x, c2y, x, y].
    const seg = [];
    const from = () => (seg.length ? seg[seg.length - 1].slice(6) : [a.x, a.y]);
    const pen = {
      moveTo() {},
      lineTo(x, y) { const [px, py] = from(); seg.push([px, py, px, py, x, y, x, y]); },
      bezierCurveTo(c1x, c1y, c2x, c2y, x, y) { seg.push([...from(), c1x, c1y, c2x, c2y, x, y]); },
      closePath() {},
    };
    if (i < 0 && k < 0) {
      const g = toStage(ca.length ? groups[ca[0]] : { x: 0.5, y: 0.5 });
      const mx = (a.x + b.x) / 2, my = (a.y + b.y) / 2;
      const len = Math.hypot(b.x - a.x, b.y - a.y) || 1;
      let nx = (a.y - b.y) / len, ny = (b.x - a.x) / len;
      if ((mx - g.x) * nx + (my - g.y) * ny < 0) { nx = -nx; ny = -ny; }
      // The quadratic through (cx, cy), as a cubic. A long one bends no
      // further than a short one would, so that it keeps to the map.
      const bend = Math.min(0.18 * len, 0.03 * state.geom.size * state.view.z);
      const cx = mx + nx * bend, cy = my + ny * bend;
      pen.bezierCurveTo(a.x + (2 / 3) * (cx - a.x), a.y + (2 / 3) * (cy - a.y), b.x + (2 / 3) * (cx - b.x), b.y + (2 / 3) * (cy - b.y), b.x, b.y);
    } else {
      const pts = [a];
      for (let x = 0; x <= i; x++) pts.push(toStage(groups[ca[x]]));
      for (let x = k; x >= 0; x--) pts.push(toStage(groups[cb[x]]));
      pts.push(b);
      d3.line().x((p) => p.x).y((p) => p.y).curve(d3.curveBundle.beta(BUNDLE)).context(pen)(pts);
    }
    e.mid = midway(seg);
    const p = d3.path();
    p.moveTo(a.x, a.y);
    for (const [, , c1x, c1y, c2x, c2y, x, y] of seg) p.bezierCurveTo(c1x, c1y, c2x, c2y, x, y);
    return p.toString();
  }

  // midway returns the point halfway along a chain of cubic Béziers, going
  // by their chords, and the direction they run there in degrees.
  function midway(seg) {
    if (!seg.length) return { x: 0, y: 0, deg: 0 };
    const len = seg.map(([x0, y0, , , , , x, y]) => Math.hypot(x - x0, y - y0));
    let rest = len.reduce((s, l) => s + l, 0) / 2, n = 0;
    while (n < seg.length - 1 && rest > len[n]) rest -= len[n++];
    const t = len[n] ? rest / len[n] : 0.5, u = 1 - t;
    const [x0, y0, x1, y1, x2, y2, x3, y3] = seg[n];
    const x = u * u * u * x0 + 3 * u * u * t * x1 + 3 * u * t * t * x2 + t * t * t * x3;
    const y = u * u * u * y0 + 3 * u * u * t * y1 + 3 * u * t * t * y2 + t * t * t * y3;
    let dx = 3 * u * u * (x1 - x0) + 6 * u * t * (x2 - x1) + 3 * t * t * (x3 - x2);
    let dy = 3 * u * u * (y1 - y0) + 6 * u * t * (y2 - y1) + 3 * t * t * (y3 - y2);
    if (!dx && !dy) { dx = x3 - x0; dy = y3 - y0; }
    return { x, y, deg: (Math.atan2(dy, dx) * 180) / Math.PI };
  }

  function actStep(e) {
    if (e <= 0) return 0;
    const s = 1 + Math.floor((3.999 * Math.log1p(e)) / Math.log1p(state.maxEdits));
    return Math.min(4, Math.max(1, s));
  }

  // growth returns, for frame f, how much of its place on the map each live
  // package fills. Early on every package is a sliver of its peak and the map
  // is mostly empty, so all of them grow alike: first until the fullest fills
  // its place, then on into the room left until two live ones would touch.
  function growth(f, z) {
    const { packages } = state.data;
    const live = f.pkgs.filter((s) => s.live > 0);
    if (!live.length) return () => 0;
    const grown = (s) => Math.sqrt(s.live / Math.max(1, packages[s.p].peak));
    const boost = 1 / Math.max(0.05, ...live.map(grown));
    const r = live.map((s) => packages[s.p].r * Math.min(1, boost * grown(s)));
    let room = MAX_GROW;
    for (let i = 0; i < live.length; i++) {
      // No wider than a fifth of the stage.
      room = Math.min(room, 0.1 / (r[i] * z));
      const a = packages[live[i].p];
      // Nor out of its group.
      const g = state.data.groups[a.group];
      if (g) room = Math.min(room, (g.r - Math.hypot(a.x - g.x, a.y - g.y)) / r[i]);
      for (let j = i + 1; j < live.length; j++) {
        const b = packages[live[j].p];
        room = Math.min(room, (0.92 * Math.hypot(a.x - b.x, a.y - b.y)) / (r[i] + r[j]));
      }
    }
    const k = Math.max(1, room);
    return (s) => k * Math.min(1, boost * grown(s));
  }

  function renderStage(frame) {
    const { packages } = state.data;
    // An author picked keeps only their packages on the map.
    const f = scoped(frame);
    const { size } = state.geom;
    const now = new Map(f.pkgs.map((p) => [p.p, p]));
    const focus = state.card && state.card.pkg != null ? state.pathIdx.get(state.card.pkg) : undefined;
    const fol = followNow();
    const folPkg = fol.dir != null ? state.pathIdx.get(fol.dir) : undefined;
    const know = state.colorBy === 'knowledge';
    const who = state.who ? state.data.authors.indexOf(state.who) : -1;
    state.view = fitView(f);
    const z = state.view.z;
    const grow = growth(f, z);
    const n = packages.length;
    const { open, front } = fold(kept());
    state.open = open;
    packages.forEach((p, i) => {
      const el = state.bubbles[i];
      const s = now.get(i);
      // Inside a closed group a package is drawn as the group.
      const live = s && front[i] === i ? s.live : 0;
      const a = actStep(s ? editsOf(s) : 0);
      // Too small to see, a bubble is drawn 8px wide on screen, at any zoom.
      const d = live > 0 ? 2 * p.r * size * z * grow(s) : p.r * size * z;
      placeBubble(i);
      state.diam[i] = d;
      el.style.setProperty('--d', `${d}px`);
      const ringed = know && live > 0 && s.knowers;
      el.style.backgroundColor = know ? RING_IN : ACT[a];
      el.style.backgroundImage = ringed ? ring(s.knowers) : '';
      // and dims each by their share of it.
      el.style.opacity = live > 0 && state.who ? String(0.35 + 0.65 * Math.min(1, shareOf(s, who) / 0.5)) : '';
      const cls = know ? `know${live > 0 ? '' : ' gone'}` : `a${a}${live > 0 ? '' : ' gone'}${a >= 3 ? ' busy' : ''}`;
      el.className = `bubble ${cls}${nameClass(el, i, d * state.zt.k)}${i === focus ? ' focus' : ''}${i === folPkg ? ' followed' : ''}`;
      const label = live > 0 ? `${p.path} · ${plural(live, 'symbol')}${s && s.kinds ? ` · ${describeKinds(s.kinds)}` : ''}${knowersText(s)}` : p.path;
      el.setAttribute('aria-label', i === state.picked ? `${label}. Show the functions crossing into and out of it`
        : `${label}. Show only it and the packages it shares dependencies with`);
      el.tabIndex = live > 0 && !state.drill ? 0 : -1;
    });

    // A closed group is one bubble at its center, as large as its live
    // symbols together and as busy as its busiest package. Its place was
    // made for every package it ever held at its peak, so all of them grow
    // alike, as far as the fullest still fits its place.
    const sums = new Map();
    for (const s of f.pkgs) {
      if (front[s.p] < n || s.live <= 0) continue;
      const t = sums.get(front[s.p]) || { live: 0, edits: 0, pkgs: 0 };
      t.live += s.live;
      t.edits = Math.max(t.edits, editsOf(s));
      t.pkgs++;
      sums.set(front[s.p], t);
    }
    let fill = MAX_GROW;
    for (const [id, t] of sums) fill = Math.min(fill, (0.85 * groupOf(id).r) / (state.unit * Math.sqrt(t.live)));
    fill = Math.max(1, fill);
    state.data.groups.forEach((g, gi) => {
      const id = n + gi, el = state.bubbles[id], t = sums.get(id);
      const live = t ? t.live : 0;
      const a = actStep(t ? t.edits : 0);
      const d = 2 * fill * state.unit * Math.sqrt(live) * size * z;
      const { x, y } = toStage(g);
      el.style.left = `${x}px`;
      el.style.top = `${y}px`;
      state.diam[id] = d;
      el.style.setProperty('--d', `${d}px`);
      el.style.backgroundColor = know ? RING_IN : ACT[a];
      const cls = know ? 'know' : `a${a}${a >= 3 ? ' busy' : ''}`;
      el.className = `bubble agg ${cls}${live > 0 ? '' : ' gone'}${nameClass(el, id, d * state.zt.k)}`
        + `${focus !== undefined && front[focus] === id ? ' focus' : ''}${folPkg !== undefined && front[folPkg] === id ? ' followed' : ''}`;
      el.setAttribute('aria-label', `${g.path}/ · ${plural(t ? t.pkgs : 0, 'package')} · ${plural(live, 'symbol')}. Zoom in to open it`);
      el.tabIndex = live > 0 && !state.drill ? 0 : -1;
    });

    // A node that zooming folded away or opened no longer has the pointer
    // or focus, nor a card.
    for (const key of ['hover', 'focused']) {
      if (state[key] != null && state.bubbles[state[key]].classList.contains('gone')) state[key] = null;
    }
    if (state.tip && state.tip.node != null && state.bubbles[state.tip.node].classList.contains('gone')) hideTip();

    // Dependencies between the nodes drawn, as wide as the references across
    // them; the ones the card's moment is about are lit up. New ones draw
    // themselves from end to end, dropped ones fade.
    const edges = new Map();
    for (const [pa, pb, refs] of f.deps) {
      const a = front[pa], b = front[pb];
      if (a === b) continue;
      const key = `${Math.min(a, b)}-${Math.max(a, b)}`;
      let e = edges.get(key);
      if (!e) edges.set(key, e = { key, a: Math.min(a, b), b: Math.max(a, b), refs: 0, ab: 0, ba: 0, hot: false });
      e.refs += refs || 0;
      if (a < b) e.ab += refs || 0;
      else e.ba += refs || 0;
      if (focus !== undefined && (state.card.kind === 'signature' ? pb === focus : pa === focus || pb === focus)) e.hot = true;
    }
    rankLines(edges.values());
    const ms = tweenMs();
    renderGroups(f, ms);
    for (const e of edges.values()) e.d = edgePath(e);
    d3.select('#deps').selectAll(':scope > path').data([...edges.values()], (e) => e.key).join(
      (enter) => enter.append('path')
        .attr('d', (e) => e.d)
        .attr('pathLength', 1).attr('stroke-dasharray', '1 1').attr('stroke-dashoffset', 1)
        .call((en) => en.transition().duration(ms).ease(d3.easeCubicOut).attr('stroke-dashoffset', 0)),
      (update) => update.call((u) => u.filter('.leaving').interrupt().classed('leaving', false).style('opacity', null).attr('stroke-dashoffset', 0))
        .call((u) => u.transition('view').duration(ms).ease(d3.easeCubicInOut).attr('d', (e) => e.d)),
      (exit) => exit.filter(':not(.leaving)').classed('leaving', true)
        .call((ex) => ex.transition().duration(ms).style('opacity', 0).remove()))
      .classed('hot', (e) => e.hot)
      .classed('faint', (e) => e.faint)
      .style('--w', (e) => `${e.hot ? Math.max(2.2, state.refs(e.refs)) : state.refs(e.refs)}px`);
    state.edges = edges;
    state.foldKey = [...open].join();
    renderLit(ms);
    renderTip();
  }

  // rankLines marks the dependencies not drawn unless lit: all but the
  // strongest few, by the lines setting, of those with an end on screen.
  // The card's are always drawn.
  function rankLines(edges) {
    const st = $('stage'), W = st.clientWidth, H = st.clientHeight, t = state.zt;
    const seen = (id) => {
      const { x, y } = nodeAt(id);
      const sx = t.applyX(x), sy = t.applyY(y);
      return sx >= 0 && sx <= W && sy >= 0 && sy <= H;
    };
    let n = 0;
    for (const e of [...edges].sort((p, q) => q.refs - p.refs)) {
      const drawn = n < LINES[state.lines] && (seen(e.a) || seen(e.b));
      if (drawn) n++;
      e.faint = !drawn && !e.hot;
    }
  }

  function setLines(v) {
    state.lines = LINES[v] !== undefined ? v : 'key';
    for (const b of $('lines').querySelectorAll('button')) b.setAttribute('aria-pressed', String(b.dataset.v === state.lines));
    if (!state.edges) return;
    rankLines(state.edges.values());
    d3.select('#deps').selectAll(':scope > path').classed('faint', (e) => e.faint);
  }

  // refold draws the stage again, at once, when zooming opens or closes a
  // group, and otherwise picks the lines to draw for what is now on screen.
  function refold() {
    if (!state.data || !state.data.frames.length || !state.view || state.drill) return;
    if ([...fold(kept()).open].join() === state.foldKey) {
      setLines(state.lines);
      return;
    }
    const f = state.data.frames[state.frame];
    state.instant = true;
    renderStage(f);
    state.instant = false;
    $('caption').textContent = caption(f);
  }

  // zoomToGroup closes in on group g until it fills most of the stage, which
  // opens it.
  function zoomToGroup(g) {
    const st = $('stage'), W = st.clientWidth, H = st.clientHeight;
    const grp = state.data.groups[g];
    const { x, y } = toStage(grp);
    const r = grp.r * state.geom.size * state.view.z;
    const k = Math.min(state.maxZoom, Math.max(1, (0.42 * Math.min(W, H)) / r));
    zoomTo(d3.zoomIdentity.translate(W / 2 - x * k, H / 2 - y * k).scale(k));
  }

  // renderGroups outlines the open groups holding live packages in frame f,
  // each inside its parent's, and names those large enough on screen. The
  // outermost group is the whole map and goes unmarked; a closed one is
  // drawn as a bubble instead.
  function renderGroups(f, ms) {
    const { groups } = state.data;
    const { size } = state.geom;
    const live = new Set();
    for (const s of f.pkgs) if (s.live > 0) for (const g of state.chains[s.p]) live.add(g);
    const shown = groups.map((g, i) => ({ ...g, i })).filter((g) => g.parent >= 0 && live.has(g.i) && state.open.has(g.i));
    const z = state.view.z;
    for (const g of shown) {
      Object.assign(g, toStage(g));
      g.rr = g.r * size * z;
      g.name = groupName(g.i);
    }
    d3.select('#clusters').selectAll(':scope > g').data(shown, (g) => g.i).join(
      (enter) => enter.append('g').attr('class', 'cluster').style('opacity', 0)
        .call((en) => en.append('circle').attr('cx', (g) => g.x).attr('cy', (g) => g.y).attr('r', (g) => g.rr))
        .call((en) => en.append('text').attr('dy', '1.3em').attr('x', (g) => g.x).attr('y', (g) => g.y - g.rr))
        .call((en) => en.transition().duration(ms).style('opacity', 1)),
      (update) => update.call((u) => u.select('circle').transition('view').duration(ms).ease(d3.easeCubicInOut)
        .attr('cx', (g) => g.x).attr('cy', (g) => g.y).attr('r', (g) => g.rr)),
      (exit) => exit.transition().duration(ms).style('opacity', 0).remove())
      .call((sel) => sel.select('text').text((g) => `${g.name}/`)
        .transition('view').duration(ms).ease(d3.easeCubicInOut).attr('x', (g) => g.x).attr('y', (g) => g.y - g.rr));
    nameGroups();
    cullNames();
  }

  // cullNames hides the names beneath bubbles that would run into the name
  // of a larger one, so that the names left can be read.
  function cullNames() {
    const t = state.zt, taken = [];
    const named = state.bubbles.map((el, id) => ({ el, id, d: (state.diam[id] || 0) * t.k }))
      .filter(({ el }) => !el.classList.contains('gone') && el.classList.contains('under'))
      .sort((a, b) => b.d - a.d);
    for (const { el, id, d } of named) {
      const { x, y } = nodeAt(id);
      const cx = t.applyX(x), top = t.applyY(y) + Math.max(d, 8) / 2 + 4;
      const w = 6.2 * el.firstChild.textContent.length + 4;
      const box = [cx - w / 2, top, cx + w / 2, top + 13];
      const hit = taken.some((b) => box[0] < b[2] && b[0] < box[2] && box[1] < b[3] && b[1] < box[3]);
      el.classList.toggle('unnamed', hit);
      if (!hit) taken.push(box);
    }
  }

  // nameGroups names the groups wide enough on screen to hold a name.
  function nameGroups() {
    const k = state.zt.k;
    d3.selectAll('#clusters > g').classed('named', (g) => g.rr * k >= 70);
  }

  // ---------- The hover card ----------

  // A package or symbol under the pointer, or with keyboard focus, is
  // described in a card beside it: what it holds at the playhead, how it
  // got there, and what a click on it does.
  function showTip(t) {
    state.tip = t;
    renderTip();
  }

  function hideTip() {
    state.tip = null;
    $('tip').hidden = true;
  }

  function renderTip() {
    const t = state.tip;
    const f = state.data && state.data.frames[state.frame];
    const body = !t || !f ? null : t.sym != null ? (state.drill ? symTip(t.sym) : null)
      : state.drill ? null : isGroup(t.node) ? groupTip(t.node, f) : pkgTip(t.node, f);
    const tip = $('tip');
    if (!body) {
      tip.hidden = true;
      return;
    }
    tip.replaceChildren(...body);
    tip.hidden = false;
    placeTip();
  }

  // placeTip sets the card beside its node, on whichever side has room.
  function placeTip() {
    const t = state.tip, tip = $('tip');
    if (!t || tip.hidden) return;
    let x, y, r;
    if (t.sym != null) ({ x, y, r } = state.drill.pos[t.sym]);
    else {
      ({ x, y } = nodeAt(t.node));
      r = (state.diam[t.node] || 0) / 2;
    }
    const z = state.zt, st = $('stage');
    const cx = z.applyX(x), cy = z.applyY(y), cr = r * z.k;
    const w = tip.offsetWidth, ht = tip.offsetHeight, gap = 12;
    let left = cx + cr + gap;
    if (left + w > st.clientWidth - 8) left = cx - cr - gap - w;
    left = Math.max(8, Math.min(st.clientWidth - w - 8, left));
    const top = Math.max(8, Math.min(st.clientHeight - ht - 8, cy - ht / 2));
    tip.style.left = `${left}px`;
    tip.style.top = `${top}px`;
  }

  const tipRow = (label, ...value) => h('div', { class: 'tip-row' }, h('span', { class: 'tl' }, label), h('span', { class: 'tv' }, ...value));

  function pkgTip(pi, f) {
    const p = state.data.packages[pi];
    const s = f.pkgs.find((x) => x.p === pi);
    const live = s ? s.live : 0;
    const out = [h('div', { class: 'tip-head' }, h('span', { class: 'eyebrow' }, 'Package'), h('span', { class: 'tip-name mono' }, p.path))];
    const { born, edited } = pkgHistory(pi);
    if (!live) {
      out.push(h('div', { class: 'muted small' }, born == null ? `Not written yet at ${fmtBig(f.t)}` : 'No live symbols at the playhead'));
      return out;
    }
    const rows = [tipRow('Symbols', `${live.toLocaleString()}`, h('span', { class: 'muted' }, ` · peak ${p.peak.toLocaleString()}`))];
    const kinds = s.kinds ? KINDS.filter(([k]) => s.kinds[k]) : [];
    const per = { commit: 'this commit', week: 'this week', month: 'this month' }[state.step];
    rows.push(tipRow(`Edits ${per}`, kinds.length
      ? h('span', { class: 'tip-kinds' }, kinds.map(([k, l]) => h('span', { class: 'cnt', title: l }, mark(k), String(s.kinds[k]))))
      : h('span', { class: 'muted' }, 'none')));
    let uses = 0, by = 0;
    for (const n of litDeps(pi).values()) {
      if (n.out) uses++;
      if (n.in) by++;
    }
    rows.push(tipRow('Uses →', plural(uses, 'package')), tipRow('Used by ←', plural(by, 'package')));
    if (born != null) rows.push(tipRow('First seen', fmtBig(born)));
    if (edited != null) rows.push(tipRow('Last edited', fmtBig(edited)));
    out.push(h('div', { class: 'tip-rows' }, rows));
    if (s.knowers && s.knowers.length) {
      out.push(h('div', { class: 'tip-know' },
        h('div', { class: 'eyebrow' }, `Who knows it · bus factor ${s.bus}`),
        s.knowers.slice(0, 3).map(([a, share, gone]) => {
          const name = state.data.authors[a];
          return h('div', { class: `tip-kn${gone ? ' gone' : ''}` },
            h('i', { style: `background:${gone ? GONE : authorColor(name)}` }), h('span', {}, `${name}${gone ? ' (left)' : ''}`), h('span', { class: 'mono' }, pct(share)));
        }),
        s.orphaned > 0 ? h('div', { class: 'muted small' }, `${pct(s.orphaned)} orphaned`) : null));
    }
    out.push(h('div', { class: 'tip-hint' }, state.picked === pi ? 'Click to break out the functions crossing it' : 'Click to show only it and its neighbours'));
    return out;
  }

  // groupTip describes the closed group drawn as node id: what its packages
  // hold together at the playhead.
  function groupTip(id, f) {
    const gi = id - state.data.packages.length;
    let pkgs = 0, live = 0;
    const kinds = {};
    for (const s of f.pkgs) {
      if (s.live <= 0 || !state.chains[s.p].includes(gi)) continue;
      pkgs++;
      live += s.live;
      for (const [k, v] of Object.entries(s.kinds || {})) kinds[k] = (kinds[k] || 0) + v;
    }
    const out = [h('div', { class: 'tip-head' }, h('span', { class: 'eyebrow' }, 'Directory'), h('span', { class: 'tip-name mono' }, nodeName(id)))];
    const per = { commit: 'this commit', week: 'this week', month: 'this month' }[state.step];
    const ks = KINDS.filter(([k]) => kinds[k]);
    let uses = 0, by = 0;
    for (const n of litDeps(id).values()) {
      if (n.out) uses++;
      if (n.in) by++;
    }
    out.push(h('div', { class: 'tip-rows' },
      tipRow('Packages', pkgs.toLocaleString()),
      tipRow('Symbols', live.toLocaleString()),
      tipRow(`Edits ${per}`, ks.length
        ? h('span', { class: 'tip-kinds' }, ks.map(([k, l]) => h('span', { class: 'cnt', title: l }, mark(k), String(kinds[k]))))
        : h('span', { class: 'muted' }, 'none')),
      tipRow('Uses →', plural(uses, 'neighbour')), tipRow('Used by ←', plural(by, 'neighbour'))));
    out.push(h('div', { class: 'tip-hint' }, 'Click to zoom in and open it'));
    return out;
  }

  // pkgHistory is when package pi first had live symbols and when it was
  // last edited, up to the playhead.
  function pkgHistory(pi) {
    let born = null, edited = null;
    const fs = state.data.frames;
    for (let i = 0; i <= state.frame; i++) {
      const s = fs[i].pkgs.find((x) => x.p === pi);
      if (!s) continue;
      if (born == null && s.live > 0) born = fs[i].t;
      if (editsOf(s) > 0) edited = fs[i].t;
    }
    return { born, edited };
  }

  const SYM_EVENT = { born: 'Added', died: 'Deleted', renamed: 'Renamed', in: 'Moved in', out: 'Moved out', signature: 'New signature' };

  function symTip(i) {
    const d = state.drill;
    if (!d.cur.has(i)) return null;
    const s = d.v.symbols[i];
    const stmts = d.cur.get(i);
    const evs = [];
    for (let fi = 0; fi <= state.frame; fi++) for (const e of d.v.frames[fi].ev) if (e.s === i) evs.push(e);
    const when = (e) => fmtShort(state.data.revTimes[e.rev] ?? 0);
    const what = (e) => (e.k === 'renamed' ? `from ${e.from}` : e.k === 'in' ? `from ${e.from}` : e.k === 'out' ? `to ${e.to}` : '');
    const out = [
      h('div', { class: 'tip-head' }, h('span', { class: 'eyebrow' }, s.kind), h('span', { class: 'tip-name mono' }, d.names[i])),
      h('div', { class: 'tip-rows' },
        tipRow('File', h('span', { class: 'mono' }, s.file)),
        tipRow('Size', plural(stmts, 'statement'), h('span', { class: 'muted' }, ` · peak ${s.peak.toLocaleString()}`))),
    ];
    if (evs.length) {
      out.push(h('ol', { class: 'tip-events' }, evs.slice(-4).map((e) => h('li', {},
        h('span', { class: 'mono muted' }, when(e)), h('span', {}, `${SYM_EVENT[e.k] || e.k} ${what(e)}`.trim())))));
    }
    if (evs.length > 4) out.push(h('div', { class: 'muted small' }, `and ${plural(evs.length - 4, 'earlier change')}`));
    const fid = state.follow ? state.follow.id : 0;
    out.push(h('div', { class: 'tip-hint' }, s.id === fid ? 'Being followed' : 'Click to follow it through renames and moves'));
    return out;
  }

  // ---------- A package's dependencies ----------

  // The package picked, or else the one hovered or focused, has its
  // dependencies lit: the packages it uses and the ones using it stay, marked
  // with the references across, and the rest of the map fades, or goes
  // altogether while a package is picked.
  const litPkg = () => (state.drill ? null : state.picked ?? state.hover ?? state.focused);

  function pick(pi) {
    if (state.picked === pi) return;
    state.picked = pi;
    render(false);
    if (state.data && state.data.frames.length) $('caption').textContent = caption(state.data.frames[state.frame]);
  }
  const unpick = () => pick(null);

  function setLit(key, pi) {
    if (state[key] === pi) return;
    state[key] = pi;
    renderLit();
    if (state.data && state.data.frames.length) $('caption').textContent = caption(state.data.frames[state.frame]);
  }

  // litDeps returns, for each package the lit one shares a dependency with,
  // the references from it to the lit one (in) and from the lit one to it
  // (out).
  function litDeps(lit) {
    const near = new Map();
    for (const e of (state.edges || new Map()).values()) {
      if (e.a !== lit && e.b !== lit) continue;
      const other = e.a === lit ? e.b : e.a;
      near.set(other, e.a === lit ? { out: e.ab, in: e.ba } : { out: e.ba, in: e.ab });
    }
    return near;
  }

  function renderLit(ms = 0) {
    const lit = litPkg();
    const near = lit == null ? new Map() : litDeps(lit);
    const picked = lit != null && lit === state.picked;
    $('stage').classList.toggle('lit', lit != null);
    $('stage').classList.toggle('picked', picked);
    state.bubbles.forEach((el, i) => {
      const n = near.get(i);
      el.classList.toggle('lit', i === lit);
      el.classList.toggle('near', !!n);
      // The packages filtered out can't be tabbed to.
      if (picked && i !== lit && !n) el.tabIndex = -1;
      let badge = el.querySelector('.nref');
      if (!n) {
        if (badge) badge.remove();
        return;
      }
      if (!badge) el.append(badge = h('span', { class: 'nref', 'aria-hidden': 'true' }));
      badge.className = `nref ${n.out && n.in ? 'both' : n.out ? 'out' : 'in'}`;
      badge.textContent = [n.out ? `→ ${n.out}` : '', n.in ? `← ${n.in}` : ''].filter(Boolean).join('  ');
    });
    // An arrow mid-curve points the way the references go; edges run from
    // the lower package index to the higher.
    const arrowed = [];
    d3.select('#deps').selectAll(':scope > path')
      .each(function (e) {
        const on = lit != null && (e.a === lit || e.b === lit);
        const out = on ? (e.a === lit ? e.ab : e.ba) : 0, inn = on ? (e.a === lit ? e.ba : e.ab) : 0;
        e.dir = !on ? '' : out && inn ? 'both' : out ? 'out' : 'in';
        // Which way along the path the arrow points: towards b is fwd.
        e.fwd = (e.a === lit) === (e.dir === 'out');
        d3.select(this).classed('on', on).classed('out', e.dir === 'out').classed('in', e.dir === 'in').classed('both', e.dir === 'both');
        if (on) arrowed.push(e);
      });
    d3.select('#arrows').raise().selectAll(':scope > g').data(arrowed, (e) => e.key).join(
      (enter) => enter.append('g').call((en) => en.append('path')).attr('transform', arrowAt),
      (update) => update.call((u) => u.transition('view').duration(ms).ease(d3.easeCubicInOut).attr('transform', arrowAt)))
      .attr('class', (e) => `arrow ${e.dir}`)
      .select('path').attr('d', (e) => (e.dir === 'both' ? ARROWS : ARROW));
  }

  // arrowAt places a lit dependency's arrow mid-curve, pointing the way its
  // references go, at a size that holds on screen as the map zooms.
  function arrowAt(e) {
    const m = e.mid;
    return `translate(${m.x},${m.y}) rotate(${m.deg + (e.fwd ? 0 : 180)}) scale(${1.4 / state.zt.k}) translate(-5,-5)`;
  }

  // litCaption sums up the lit package's dependencies.
  function litCaption(lit) {
    let uses = 0, usesRefs = 0, by = 0, byRefs = 0;
    for (const n of litDeps(lit).values()) {
      if (n.out) { uses++; usesRefs += n.out; }
      if (n.in) { by++; byRefs += n.in; }
    }
    const part = (n, refs) => (n ? `${plural(n, 'package')} (${plural(refs, 'reference')})` : 'none');
    return `${nodeName(lit)} · uses → ${part(uses, usesRefs)} · used by ← ${part(by, byRefs)}`;
  }

  // buildLayers adds the layers drawn with the dependencies: the groups
  // under them and the arrows of the lit ones over them.
  function buildLayers() {
    const svg = d3.select('#deps');
    svg.append('g').attr('id', 'clusters');
    svg.append('g').attr('id', 'arrows');
  }

  // ---------- Zoom ----------

  // The stage zooms with the scroll wheel or a pinch and pans by dragging;
  // scrolling out past the whole map scrolls the page again. Labels keep
  // their size on screen, so zooming in names more of the packages and
  // symbols.
  function setupZoom() {
    const st = $('stage');
    state.zoom = d3.zoom()
      .scaleExtent([1, state.maxZoom])
      .filter((e) => {
        if (e.target.closest && e.target.closest('.ov')) return false;
        if (e.type === 'wheel') return true;
        if (e.touches) return e.touches.length > 1 || state.zt.k > 1;
        return !e.button;
      })
      // A pinch arrives as small Ctrl+wheel steps and needs a boost; a mouse
      // wheel's steps are large, and a touchpad's two-finger scroll comes in
      // many small steps, so neither does.
      .wheelDelta((e) => -e.deltaY * (e.deltaMode === 1 ? 0.05 : e.deltaMode ? 1 : 0.002) * (e.ctrlKey && Math.abs(e.deltaY) < 50 ? 10 : 1))
      .on('start', () => st.classList.add('zooming'))
      .on('end', () => {
        st.classList.remove('zooming');
        refold();
      })
      .on('zoom', (e) => {
        state.zt = e.transform;
        applyZoom();
        refold();
      });
    d3.select(st).call(state.zoom).on('dblclick.zoom', null);
    st.style.touchAction = 'pan-y';
  }

  function applyZoom() {
    const t = state.zt;
    const w = $('world');
    w.style.transform = `translate(${t.x}px, ${t.y}px) scale(${t.k})`;
    w.style.setProperty('--k', String(t.k));
    d3.selectAll('#arrows > g').attr('transform', arrowAt);
    nameGroups();
    cullNames();
    $('zoomIn').disabled = t.k >= state.maxZoom - 1e-6;
    $('zoomOut').disabled = t.k <= 1 + 1e-6;
    $('zoomReset').disabled = t.k <= 1 + 1e-6 && Math.abs(t.x) < 0.5 && Math.abs(t.y) < 0.5;
    relabel();
    if (state.data) {
      placeFolTag();
      placeTip();
    }
  }

  function zoomTo(t, ms = zoomMs()) {
    d3.select('#stage').transition('zoom').duration(ms).ease(d3.easeCubicInOut).call(state.zoom.transform, t);
  }

  function zoomBy(f) {
    d3.select('#stage').transition('zoom').duration(reduced.matches ? 0 : 300).call(state.zoom.scaleBy, f);
  }

  // relabel names the bubbles and symbols big enough on screen to hold a name.
  function relabel() {
    const k = state.zt.k;
    state.bubbles.forEach((el, i) => {
      const c = nameAt(el, i, (state.diam[i] || 0) * k);
      el.classList.toggle('small', c === 'small');
      el.classList.toggle('under', c === 'under');
    });
    const d = state.drill;
    if (!d) return;
    d.dots.forEach((el, i) => {
      const on = d.cur.has(i) && 2 * d.pos[i].r * k >= 34;
      el.classList.toggle('labeled', on);
      el.firstChild.textContent = on ? shortName(d.names[i]) : '';
    });
    for (const [file, el] of d.files) {
      const p = d.fileAt.get(file);
      el.classList.toggle('named', !!p && p.r * k >= 44);
    }
  }

  function describeKinds(kinds) {
    return Object.entries(kinds).map(([k, n]) => `${n} ${k}`).join(', ');
  }

  // The followed symbol's name floats over its package, or its dot.
  function placeFolTag() {
    const fol = followNow();
    const pi = fol.dir != null ? state.pathIdx.get(fol.dir) : undefined;
    const tag = $('folTag');
    const { size, x0, y0 } = state.geom;
    let x, y, r;
    if (fol.e && state.drill && state.drill.dir === fol.dir) {
      const i = state.drill.v.symbols.findIndex((s) => s.id === state.follow.id);
      if (i >= 0 && state.drill.cur.has(i)) ({ x, y, r } = state.drill.pos[i]);
    } else if (fol.e && !state.drill && pi !== undefined) {
      ({ x, y } = at(pi));
      r = (state.diam[pi] || 2 * state.data.packages[pi].r * size) / 2;
    }
    tag.hidden = x === undefined;
    if (tag.hidden) return;
    // The tag sits outside the zoomed world, so that it keeps its size.
    const t = state.zt;
    tag.textContent = fol.e.name;
    tag.style.left = `${t.applyX(x)}px`;
    tag.style.top = `${t.applyY(y) - r * t.k - 6}px`;
  }

  // ---------- Overlays ----------

  function renderOverlays(f) {
    const { chapters } = state.data;
    $('date').textContent = fmtBig(f.t);
    const ci = chapterAt(rev());
    const c = chapters[ci];
    $('chapLabel').textContent = c ? `Chapter ${ci + 1} of ${chapters.length} · ${c.title}` : '';
    count('cCommits', f.total);
    count('cLive', f.live);
    count('cPkgs', f.pkgs.filter((p) => p.live > 0).length);
    renderKnowLegend(f);
    $('caption').textContent = caption(f);
    $('caption').hidden = !!state.card;
    const label = state.step === 'commit' ? `${f.sha.slice(0, 7)} · ` : '';
    $('frameLabel').textContent = `${label}frame ${state.frame + 1} of ${state.data.frames.length}`;
  }

  // count rolls a counter from the number it shows to v.
  function count(id, v) {
    const el = $(id);
    const from = el.shown ?? v;
    const sel = d3.select(el).interrupt('count');
    if (from === v || !tweenMs()) {
      el.shown = v;
      el.textContent = v.toLocaleString();
      return;
    }
    const i = d3.interpolateRound(from, v);
    sel.transition('count').duration(tweenMs()).ease(d3.easeCubicOut).tween('text', () => (t) => {
      el.shown = i(t);
      el.textContent = el.shown.toLocaleString();
    });
  }

  // The frame's key moment, or else what happened in it.
  function caption(f) {
    const lit = litPkg();
    if (lit != null) return litCaption(lit);
    let best = null;
    for (const m of state.moments) {
      if (inFrame(m, state.frame) && (!best || m.weight > best.weight)) best = m;
    }
    if (best) return best.text;
    if (f.commits === 0) return `No commits this ${state.step}`;
    let hot = null, n = 0;
    for (const p of f.pkgs) {
      const e = editsOf(p);
      if (e > n) { n = e; hot = p.p; }
    }
    const what = plural(f.commits, 'commit');
    return hot == null ? `${what}, no symbols changed` : `${what} · busiest in ${state.data.packages[hot].path} (${plural(n, 'edit')})`;
  }

  // ---------- Key moments ----------

  const KIND_LABEL = { birth: 'New package', death: 'Package gone', rename: 'Rename', move: 'Move', signature: 'Signature change', quiet: 'After a pause', busiest: 'Busiest week',
    release: 'Release', newcomer: 'New author', 'dep-added': 'New dependency', 'dep-removed': 'Dependency dropped', cycle: 'Import cycle', 'cycle-gone': 'Cycle broken',
    handoff: 'Handoff', orphaned: 'Orphaned' };

  function mergeMoments() {
    const all = [...state.data.moments, ...(state.follow ? state.follow.moments : [])];
    all.sort((a, b) => a.rev - b.rev || b.weight - a.weight);
    state.moments = all;
    if (state.card && !all.includes(state.card)) hideCard();
  }

  // The moments to pause at in frame i, at most PER_FRAME, by revision.
  function pausesIn(i) {
    const ms = state.moments.filter((m) => inFrame(m, i));
    return ms.sort((a, b) => b.weight - a.weight).slice(0, PER_FRAME).sort((a, b) => state.moments.indexOf(a) - state.moments.indexOf(b));
  }

  // stepMoment moves to the next (d = 1) or previous (d = -1) key moment.
  function stepMoment(d) {
    const ms = state.moments;
    let i = state.card ? ms.indexOf(state.card) : -1;
    if (i >= 0) i += d;
    else if (d > 0) i = ms.findIndex((m) => m.rev > rev());
    else i = ms.findLastIndex((m) => m.rev <= rev());
    if (i < 0 || i >= ms.length) return;
    const m = ms[i];
    seekRev(m.rev);
    showCard(m);
    if (state.playing) {
      state.queue = [];
      wait(PAUSE_MS, advance);
    }
  }

  function showCard(m) {
    state.card = m;
    const card = $('card');
    card.className = `ov ov-bl card k-${m.kind}${m.follow ? ' fol' : ''}`;
    const sum = h('div', { class: 'sum' }, h('span', { class: 'muted small' }, 'Reading the commit…'));
    card.replaceChildren(
      h('div', { class: 'card-head' },
        h('span', { class: 'dot', 'aria-hidden': 'true' }),
        h('span', { class: 'eyebrow' }, m.follow ? `Following · ${KIND_LABEL[m.kind] || m.kind}` : KIND_LABEL[m.kind] || m.kind),
        h('span', { class: 'muted small mono' }, `${fmtBig(m.t)} · ${m.sha.slice(0, 7)}`),
        h('button', { type: 'button', class: 'close', 'aria-label': 'Close the key moment (Escape)', onclick: hideCard }, '×')),
      h('p', { class: 'card-text' }, m.text),
      sum);
    card.hidden = false;
    report(m.sha).then((r) => { if (state.card === m) sum.replaceChildren(...summary(r, m)); })
      .catch((err) => { if (state.card === m) sum.replaceChildren(h('span', { class: 'muted small' }, `Could not read the commit: ${err.message}`)); });
    if (state.narrate && 'speechSynthesis' in window) speechSynthesis.speak(new SpeechSynthesisUtterance(m.text));
    render();
  }

  function hideCard() {
    if (!state.card) return;
    state.card = null;
    $('card').hidden = true;
    if (state.data) render();
  }

  // What the card's commit changed, the changes the moment is about first.
  function summary(r, m) {
    const want = { birth: 'added', death: 'deleted', rename: 'renamed', move: 'moved', signature: 'signature' }[m.kind];
    const names = m.follow ? [m.follow.name, m.follow.oldName].filter(Boolean) : [];
    const score = (c) => (names.includes(c.name) || names.includes(c.oldName) ? 4 : 0)
      + (want && c.kinds.includes(want) ? 2 : 0) + (m.pkg && dirOf(c.path) === m.pkg ? 1 : 0);
    const cs = r.changes.map((c, i) => [score(c), i, c]).sort((a, b) => b[0] - a[0] || a[1] - b[1]).map((x) => x[2]);
    const out = [];
    if (r.subject) out.push(h('p', { class: 'subject' }, r.subject));
    const counts = KINDS.filter(([k]) => r.counts[k]).map(([k, l]) => h('span', { class: 'cnt' }, mark(k), `${r.counts[k]} ${l.toLowerCase()}`));
    if (counts.length) out.push(h('div', { class: 'counts' }, counts));
    const shown = cs.slice(0, 3);
    if (shown.length) out.push(h('ul', { class: 'changes' }, shown.map((c) => h('li', {}, change(c)))));
    if (cs.length > shown.length) out.push(h('div', { class: 'muted small' }, `and ${plural(cs.length - shown.length, 'more change')}`));
    if (!r.changes.length) out.push(h('div', { class: 'muted small' }, 'No semantic changes to Go symbols.'));
    return out;
  }

  function change(c) {
    const head = h('div', { class: 'ch' }, mark(c.kind), h('span', { class: 'mono' }, c.name), h('span', { class: 'muted small' }, ` · ${c.symbol}`));
    const rows = [head];
    if (c.kinds.includes('renamed') && c.oldName) rows.push(h('div', { class: 'mono small delta' }, `${c.oldName} → ${c.name}`));
    if (c.kinds.includes('moved') && c.oldPath) rows.push(h('div', { class: 'mono small delta' }, `${c.oldPath} → ${c.path}`));
    if (c.kinds.includes('signature') && c.oldSignature) {
      rows.push(h('div', { class: 'mono small sig old' }, c.oldSignature), h('div', { class: 'mono small sig new' }, c.newSignature));
    } else if (c.kinds.includes('added') && c.newSignature) {
      rows.push(h('div', { class: 'mono small sig new' }, c.newSignature));
    }
    return rows;
  }

  // ---------- Looking into a package ----------

  async function drill(dir) {
    const pi = state.pathIdx.get(dir);
    if (pi === undefined) return;
    const keep = state.follow ? state.follow.id : 0;
    const key = `${state.step} ${dir} ${keep}`;
    let v = state.views.get(key);
    if (!v) {
      const step = state.step;
      try {
        v = await api(`/api/journey/package/${encDir(dir)}?step=${step}${keep ? `&keep=${keep}` : ''}`);
      } catch (err) {
        $('caption').textContent = `Could not open ${dir}: ${err.message}`;
        return;
      }
      state.views.set(key, v);
      if (step !== state.step) return; // the step changed meanwhile
    }
    const { size, x0, y0 } = state.geom;
    const st = $('stage');
    const o = at(pi);
    st.style.setProperty('--ox', `${o.x}px`);
    st.style.setProperty('--oy', `${o.y}px`);
    // The package map's zoom is kept for coming back; the package opens whole.
    if (!state.drill) state.outer = state.zt;
    hideTip();
    // Until a symbol is first packed it sits where the server placed it.
    const pos = v.symbols.map((s) => ({ x: x0 + s.x * size, y: y0 + s.y * size, r: 3 }));
    state.picked = null;
    state.drill = { dir, pi, v, cur: new Map(), names: [], at: -1, dots: [], peak: Math.max(1, ...v.symbols.map((s) => s.peak)), pos, files: new Map(), fileAt: new Map() };
    state.switching = null;
    for (const s of v.symbols) {
      if (!state.drill.files.has(s.file)) state.drill.files.set(s.file, h('div', { class: 'file gone' }, h('span', { class: 'fname' }, s.file)));
    }
    $('files').replaceChildren(...state.drill.files.values());
    $('dots').replaceChildren(...v.symbols.map((s) => {
      const i = state.drill.dots.length;
      const el = h('button', { type: 'button', class: `sym gone k-${s.kind}`, tabindex: '-1', onclick: () => followSym(s.id).catch(fail) }, h('span', { class: 'sname' }));
      el.addEventListener('pointerenter', (e) => { if (e.pointerType !== 'touch') showTip({ sym: i }); });
      el.addEventListener('pointerleave', hideTip);
      el.addEventListener('focus', () => showTip({ sym: i }));
      el.addEventListener('blur', hideTip);
      state.drill.dots.push(el);
      return el;
    }));
    $('tags').replaceChildren();
    $('drillName').textContent = dir;
    $('drillBar').hidden = false;
    $('syms').inert = false;
    $('bubbles').inert = true;
    st.classList.add('drilled');
    zoomTo(d3.zoomIdentity);
    render();
    if (document.activeElement && document.activeElement.closest('#bubbles')) $('back').focus();
  }

  function undrill() {
    if (!state.drill) return;
    const pi = state.drill.pi;
    state.drill = null;
    state.switching = null;
    hideTip();
    $('stage').classList.remove('drilled');
    $('drillBar').hidden = true;
    $('syms').inert = true;
    $('bubbles').inert = false;
    zoomTo(state.outer || d3.zoomIdentity);
    state.outer = null;
    render();
    if (state.bubbles[pi].tabIndex === 0) state.bubbles[pi].focus();
  }

  function placeDisc(p) {
    const disc = $('disc');
    disc.style.left = `${p.x - p.r}px`;
    disc.style.top = `${p.y - p.r}px`;
    disc.style.width = disc.style.height = `${2 * p.r}px`;
  }

  // pack lays the live symbols out as circles sized by their statements,
  // packed inside a circle per file, packed inside the package's. It returns
  // the package's circle, or null while nothing is live.
  function pack(d, minD, maxD) {
    const live = [...d.cur.keys()].sort((a, b) => a - b);
    d.fileAt = new Map();
    if (!live.length) return null;
    const byFile = d3.group(live, (i) => d.v.symbols[i].file);
    const root = d3.hierarchy({ children: [...byFile].map(([file, ids]) => ({ file, children: ids.map((i) => ({ i })) })) });
    d3.pack()
      .radius((n) => (minD + (maxD - minD) * Math.sqrt((d.cur.get(n.data.i) || 0) / d.peak)) / 2)
      .padding((n) => (n.depth ? 2 : 8))(root);
    // Scale down to fit the disc, never up: sizes stay comparable between frames.
    const { size, x0, y0 } = state.geom;
    const s = Math.min(1, (0.48 * size) / root.r);
    const at = (n) => ({ x: x0 + size / 2 + (n.x - root.x) * s, y: y0 + size / 2 + (n.y - root.y) * s, r: n.r * s });
    for (const f of root.children) {
      d.fileAt.set(f.data.file, at(f));
      for (const leaf of f.children) d.pos[leaf.data.i] = at(leaf);
    }
    return at(root);
  }

  const setCircle = (el, p) => {
    el.style.left = `${p.x}px`;
    el.style.top = `${p.y}px`;
    el.style.width = el.style.height = `${2 * p.r}px`;
  };

  // replayTo brings the package's live symbols and their names to frame fi.
  function replayTo(fi) {
    const d = state.drill;
    if (fi < d.at) d.at = -1;
    if (d.at < 0) {
      d.cur = new Map();
      d.names = d.v.symbols.map((s) => s.name);
    }
    for (let i = d.at + 1; i <= fi; i++) {
      const f = d.v.frames[i];
      for (const [s, n] of f.set) d.cur.set(s, n);
      for (const s of f.gone) d.cur.delete(s);
      for (const e of f.ev) if (e.k !== 'died') d.names[e.s] = e.name;
    }
    d.at = fi;
  }

  function renderDrill(animate) {
    const d = state.drill;
    replayTo(state.frame);
    const { size, x0, y0 } = state.geom;
    const n = Math.max(1, d.v.symbols.length);
    const maxD = Math.max(6, Math.min(64, (0.8 * size) / Math.sqrt(n)));
    const minD = Math.min(6, maxD);
    const fid = state.follow ? state.follow.id : 0;
    placeDisc(pack(d, minD, maxD) || { x: x0 + size / 2, y: y0 + size / 2, r: 0.48 * size });
    for (const [file, el] of d.files) {
      const p = d.fileAt.get(file);
      if (p) setCircle(el, p);
      el.classList.toggle('gone', !p);
    }
    d.v.symbols.forEach((s, i) => {
      const el = d.dots[i];
      const live = d.cur.has(i);
      const stmts = d.cur.get(i) || 0;
      if (live && el.classList.contains('gone')) {
        // A symbol coming to life appears in its place rather than gliding there.
        el.classList.add('jump');
        setCircle(el, d.pos[i]);
        void el.offsetWidth;
        el.classList.remove('jump');
      } else if (live) {
        setCircle(el, d.pos[i]);
      }
      el.classList.toggle('gone', !live);
      el.classList.toggle('followed', s.id === fid);
      const label = `${d.names[i]} · ${s.kind} · ${plural(stmts, 'statement')}`;
      el.setAttribute('aria-label', `${label}. Follow`);
      el.tabIndex = live ? 0 : -1;
    });
    relabel();
    const live = d.cur.size;
    $('drillNote').textContent = `${plural(live, 'live symbol')}${d.v.truncated ? ` · the biggest ${d.v.symbols.length.toLocaleString()} of ${d.v.total.toLocaleString()} shown` : ''}`;
    if (animate) animateEvents(d.v.frames[state.frame].ev);
    renderTip();
  }

  // The point on the package's rim facing package dir.
  function rim(dir) {
    const d = state.drill;
    const { size, x0, y0 } = state.geom;
    const a = state.data.packages[d.pi], b = state.data.packages[state.pathIdx.get(dir)];
    const ang = b ? Math.atan2(b.y - a.y, b.x - a.x) : -Math.PI / 2;
    return { x: x0 + size / 2 + 0.48 * size * Math.cos(ang), y: y0 + size / 2 + 0.48 * size * Math.sin(ang) };
  }

  function tag(x, y, text, cls) {
    const el = h('div', { class: `tag ${cls}`, style: `left:${x}px;top:${y}px` }, text);
    $('tags').append(el);
    setTimeout(() => el.remove(), TAG_MS);
  }

  // animateEvents shows what happened to the package's symbols in the frame
  // just played: births fade in and deletions fade out by themselves; moves
  // travel to or from the rim facing the other package; renames and new
  // signatures flash.
  function animateEvents(evs) {
    const d = state.drill;
    const motion = !reduced.matches;
    let tags = 0;
    for (const e of evs) {
      const el = d.dots[e.s];
      const { x, y } = d.pos[e.s];
      const more = tags < MAX_TAGS;
      if (e.k === 'out' || e.k === 'in') {
        const r = rim(e.k === 'out' ? e.to : e.from);
        if (more) { tag(r.x, r.y, e.k === 'out' ? `${shortName(e.name)} → ${e.to}` : `${shortName(e.name)} ← ${e.from}`, 'move'); tags++; }
        if (!motion) continue;
        el.style.setProperty('--dx', `${r.x - x}px`);
        el.style.setProperty('--dy', `${r.y - y}px`);
        if (e.k === 'out') {
          el.classList.add('leaving');
          setTimeout(() => el.classList.remove('leaving'), TAG_MS);
        } else {
          el.classList.add('arriving');
          void el.offsetWidth; // start from the rim
          el.classList.remove('arriving');
        }
      } else if (e.k === 'renamed' || e.k === 'signature') {
        if (more) { tag(x, y, e.k === 'renamed' ? `${e.from} → ${e.name}` : `${shortName(e.name)}: new signature`, e.k); tags++; }
        el.classList.remove('flash');
        void el.offsetWidth;
        el.classList.add('flash');
      }
    }
  }

  // ---------- The breakout ----------

  // A package chosen on the map breaks out into a panel of its own with only
  // the functions crossing into and out of it at the playhead: the ones of
  // other packages using it on the left, its own in the middle, and what
  // they use of other packages on the right, wired together.
  const linkViews = new Map(); // "dir rev" → /api/journey/links response

  async function openLinks(dir) {
    if (!state.pathIdx.has(dir)) return;
    state.links = { dir, rev: null, v: null, nodes: new Map(), adj: new Map(), wires: null, lit: null };
    $('boName').textContent = dir;
    $('boNote').textContent = 'Reading the references…';
    for (const id of ['boIn', 'boMid', 'boOut', 'boWires']) $(id).replaceChildren();
    $('breakout').hidden = false;
    $('boClose').focus();
    writeHash();
    await loadLinks();
  }

  function closeLinks(refocus = true) {
    if (!state.links) return;
    const pi = state.pathIdx.get(state.links.dir);
    state.links = null;
    $('breakout').hidden = true;
    writeHash();
    const el = state.bubbles[pi];
    if (refocus && el && el.tabIndex === 0) el.focus();
  }

  // loadLinks brings the breakout to the playhead's revision.
  async function loadLinks() {
    const L = state.links;
    if (!L || !state.data.frames.length) return;
    const r = rev();
    if (L.rev === r) return;
    L.rev = r;
    const key = `${L.dir} ${r}`;
    let v = linkViews.get(key);
    if (!v) {
      try {
        v = await api(`/api/journey/links/${encDir(L.dir)}?rev=${r}`);
      } catch (err) {
        if (state.links === L && L.rev === r) $('boNote').textContent = `Could not read the references: ${err.message}`;
        return;
      }
      linkViews.set(key, v);
    }
    if (state.links !== L || L.rev !== r) return;
    L.v = v;
    renderLinks();
  }

  function renderLinks() {
    const L = state.links;
    const { v } = L;
    L.nodes = new Map();
    L.adj = new Map();
    L.wires = [];
    const wire = (a, b, dir) => {
      L.wires.push([a, b, dir]);
      for (const [x, y] of [[a, b], [b, a]]) {
        if (!L.adj.has(x)) L.adj.set(x, new Set());
        L.adj.get(x).add(y);
      }
    };
    // node is one function's row; given an id it can be followed. A name
    // with no kind is not a live symbol of its own, e.g. a method's type.
    const node = (key, name, kind, id, known = !!kind) => {
      if (L.nodes.has(key)) return L.nodes.get(key);
      const attrs = { class: `bo-fn${id ? '' : ' static'}${known ? '' : ' missing'}`, 'data-key': key, 'data-id': id || null };
      const kids = [h('span', {}, name), kind ? h('span', { class: 'k' }, kind) : null];
      const el = id
        ? h('button', { type: 'button', ...attrs, title: `${name} · follow it`, onclick: () => followSym(id).catch(fail) }, kids)
        : h('div', { ...attrs, title: known ? name : `${name} · not a live symbol of its own` }, kids);
      el.addEventListener('pointerenter', () => lightLinks(key));
      el.addEventListener('pointerleave', () => lightLinks(null));
      el.addEventListener('focus', () => lightLinks(key));
      el.addEventListener('blur', () => lightLinks(null));
      L.nodes.set(key, el);
      return el;
    };
    const pkg = (dir, rows) => h('div', { class: 'bo-pkg' },
      h('button', { type: 'button', class: 'pk', title: `Break out ${dir}`, onclick: () => openLinks(dir).catch(fail) }, dir), rows);

    // The package's own functions: the ones others use and the ones using others.
    const own = new Map(v.symbols.map((s) => [s.name, s]));
    const mid = new Set();
    for (const l of v.in) for (const n of l.names) mid.add(n);
    for (const l of v.out) mid.add(l.name);
    const mids = [...mid].sort().map((n) => `m ${n}`);

    // Each side is its packages, each a list of rows wired to middle ones.
    const ins = d3.group(v.in, (l) => l.from), outs = d3.group(v.out, (l) => l.to);
    const inSide = [...ins].map(([dir, ls]) => ({ dir, rows: ls.map((l) => ({ key: `i ${l.id}`, l, to: l.names.map((n) => `m ${n}`) })) }));
    // What the package uses is named only, not looked up in its package.
    const outSide = [...outs].map(([dir, ls]) => {
      const names = new Map();
      for (const l of ls) {
        for (const n of l.names) {
          if (!names.has(n)) names.set(n, []);
          names.get(n).push(`m ${l.name}`);
        }
      }
      return { dir, rows: [...names.keys()].sort().map((n) => ({ key: `o ${dir} ${n}`, n, to: names.get(n) })) };
    });
    untangle(mids, [inSide, outSide]);

    const midRows = mids.map((k) => {
      const n = k.slice(2), s = own.get(n);
      return node(k, n, s && s.kind, s && s.id);
    });
    const inRows = inSide.map((g) => pkg(g.dir, g.rows.map(({ key, l, to }) => {
      for (const m of to) wire(key, m, 'in');
      return node(key, l.name, l.kind, l.id);
    })));
    const outRows = outSide.map((g) => pkg(g.dir, g.rows.map(({ key, n, to }) => {
      for (const m of to) wire(m, key, 'out');
      return node(key, n, null, 0, true);
    })));

    $('boIn').replaceChildren(...(inRows.length ? inRows : [h('p', { class: 'muted small' }, 'Nothing else uses it.')]));
    $('boMid').replaceChildren(...(midRows.length ? midRows : [h('p', { class: 'muted small' }, 'No function crosses into or out of it here.')]));
    $('boOut').replaceChildren(...(outRows.length ? outRows : [h('p', { class: 'muted small' }, 'It uses nothing else here.')]));
    const outNames = [...outs.values()].reduce((a, ls) => a + new Set(ls.flatMap((l) => l.names)).size, 0);
    $('boNote').textContent = `${fmtBig(state.data.frames[state.frame].t)} · used by ${plural(v.in.length, 'function')} in ${plural(ins.size, 'package')}`
      + ` · uses ${plural(outNames, 'name')} in ${plural(outs.size, 'package')}`;
    markFollowed();
    drawWires();
  }

  // untangle orders the breakout's rows so the wires run as level as they
  // can, in place: each side's packages and their rows move level with the
  // middle rows they are wired to, and the middle rows level with both
  // sides, a few rounds over. Each column is read as fractions of its
  // height, as the columns hold different numbers of rows.
  function untangle(mids, sides) {
    const pos = new Map();
    const place = (keys) => keys.forEach((k, i) => pos.set(k, keys.length > 1 ? i / (keys.length - 1) : 0.5));
    const level = (xs, at) => {
      for (const x of xs) x.at = at(x);
      xs.sort((a, b) => a.at - b.at);
    };
    const back = new Map(mids.map((k) => [k, []]));
    for (const side of sides) for (const g of side) for (const r of g.rows) for (const m of r.to) back.get(m).push(r.key);
    const levelSides = () => {
      for (const side of sides) {
        for (const g of side) level(g.rows, (r) => d3.mean(r.to, (m) => pos.get(m)));
        level(side, (g) => d3.mean(g.rows, (r) => r.at));
        place(side.flatMap((g) => g.rows.map((r) => r.key)));
      }
    };
    place(mids);
    levelSides();
    for (let round = 0; round < 3; round++) {
      const at = new Map(mids.map((k) => [k, d3.mean(back.get(k), (r) => pos.get(r))]));
      mids.sort((a, b) => at.get(a) - at.get(b));
      place(mids);
      levelSides();
    }
  }

  // drawWires curves a line from each function to the ones it refers to.
  function drawWires() {
    const L = state.links;
    if (!L || !L.wires) return;
    const box = $('boGrid').getBoundingClientRect();
    $('boWires').replaceChildren(...L.wires.map(([a, b, dir]) => {
      const ra = L.nodes.get(a).getBoundingClientRect(), rb = L.nodes.get(b).getBoundingClientRect();
      const x1 = ra.right - box.left, y1 = ra.top + ra.height / 2 - box.top;
      const x2 = rb.left - box.left, y2 = rb.top + rb.height / 2 - box.top;
      const dx = Math.max(24, (x2 - x1) / 2);
      return svg('path', { class: dir, 'data-a': a, 'data-b': b, d: `M${x1},${y1} C${x1 + dx},${y1} ${x2 - dx},${y2} ${x2},${y2}` });
    }));
    lightLinks(L.lit);
  }

  // lightLinks picks out one function, the ones it is wired to and the wires.
  // With none under the pointer, the followed function stays picked out.
  function lightLinks(hover) {
    const L = state.links;
    if (!L) return;
    L.lit = hover;
    const key = hover ?? followedKey();
    const near = key == null ? new Set() : new Set([key, ...(L.adj.get(key) || [])]);
    $('boGrid').classList.toggle('hl', key != null);
    for (const [k, el] of L.nodes) el.classList.toggle('on', near.has(k));
    for (const p of $('boWires').children) p.classList.toggle('on', p.dataset.a === key || p.dataset.b === key);
  }

  function markFollowed() {
    const fid = state.follow ? String(state.follow.id) : '';
    for (const el of state.links.nodes.values()) el.classList.toggle('followed', !!fid && el.dataset.id === fid);
    lightLinks(state.links.lit);
  }

  // followedKey is the breakout row of the followed function, its own
  // package's row before any other.
  function followedKey() {
    const fid = state.follow ? String(state.follow.id) : '';
    if (!fid) return null;
    let key = null;
    for (const [k, el] of state.links.nodes) {
      if (el.dataset.id !== fid) continue;
      if (k.startsWith('m ')) return k;
      key ??= k;
    }
    return key;
  }

  // ---------- Following a symbol ----------

  async function followSym(id) {
    if (state.follow && state.follow.id === id) return;
    const sym = await api(`/api/symbol/${id}`);
    state.follow = { id, sym, moments: followMoments(sym) };
    mergeMoments();
    $('symHits').hidden = true;
    // A truncated package view may have left the symbol out.
    if (state.drill && state.drill.v.truncated && !state.drill.v.symbols.some((s) => s.id === id)) await drill(state.drill.dir);
    render();
  }

  function unfollow() {
    state.follow = null;
    state.switching = null;
    mergeMoments();
    render();
  }

  // followMoments turns the symbol's events, other than body edits, into
  // key moments.
  function followMoments(sym) {
    const out = [];
    for (const e of sym.events) {
      const fl = new Set(e.flags);
      const who = e.oldName || e.name;
      let kind = null, text = '';
      if (fl.has('added')) {
        kind = 'birth';
        text = `${e.name} is added to ${e.path}`;
      } else if (fl.has('deleted')) {
        kind = 'death';
        text = `${who} is deleted from ${e.path}`;
      } else {
        const what = [];
        if (fl.has('renamed')) { kind = 'rename'; what.push(`is renamed ${e.name}`); }
        if (fl.has('moved')) { kind = kind || 'move'; what.push(`moves from ${e.oldPath} to ${e.path}`); }
        if (fl.has('signature')) { kind = kind || 'signature'; what.push('gets a new signature'); }
        text = `${who} ${what.join(' and ')}`;
      }
      if (kind) out.push({ kind, rev: e.rev, t: e.t, sha: e.sha, pkg: dirOf(e.path), text, weight: 9, follow: e });
    }
    return out;
  }

  // followNow is the followed symbol at the playhead: its latest event, and
  // its package while it is alive.
  function followNow() {
    if (!state.follow || !state.data.frames.length) return {};
    const r = rev();
    let e = null;
    for (const x of state.follow.sym.events) {
      if (x.rev > r) break;
      e = x;
    }
    if (!e) return { state: 'unborn' };
    if (e.flags.includes('deleted')) return { state: 'deleted', e };
    return { state: 'live', e, dir: dirOf(e.path) };
  }

  function renderFollow() {
    const box = $('fol');
    box.hidden = !state.follow;
    if (!state.follow) return;
    const { sym } = state.follow;
    const now = followNow();
    const e = now.e;
    const where = { unborn: `Not written yet; first appears ${fmtBig(sym.events[0].t)}`, deleted: `Deleted ${e ? fmtBig(e.t) : ''}` }[now.state] || e.path;
    const r = rev();
    box.replaceChildren(
      h('div', { class: 'fol-head' },
        h('span', { class: 'fol-name mono' }, e ? e.name : sym.events[0].name),
        h('span', { class: 'muted small' }, sym.kind),
        h('button', { type: 'button', class: 'linkish', onclick: unfollow }, 'Stop following')),
      h('div', { class: 'mono small sig' }, e ? e.signature : sym.events[0].signature),
      h('div', { class: `muted small${now.state === 'live' ? '' : ' fol-off'}` }, where),
      h('div', { class: 'fol-links small' },
        now.dir && (!state.drill || state.drill.dir !== now.dir)
          ? h('button', { type: 'button', class: 'linkish', onclick: () => drill(now.dir).catch(fail) }, `Look inside ${base(now.dir)}`) : null),
      h('ol', { class: 'fol-events' }, sym.events.map((x) => h('li', { class: x.rev <= r ? (x === e ? 'now' : 'past') : 'future' },
        h('button', { type: 'button', onclick: () => { stop(); seekRev(x.rev); } },
          mark(x.kind), h('span', { class: 'mono small' }, fmtShort(x.t)), h('span', { class: 'small' }, x.summary || x.kind))))));
  }

  // While following, the opened package follows the symbol when it moves
  // out of it.
  function chase() {
    const now = followNow();
    const was = state.folDir;
    state.folDir = now.dir;
    if (!state.drill || was !== state.drill.dir || !now.dir || now.dir === was || !state.pathIdx.has(now.dir)) return;
    state.switching = now.dir;
    const dir = now.dir;
    setTimeout(() => { if (state.switching === dir) drill(dir).catch(fail); }, reduced.matches ? 0 : 900);
  }

  let searchSeq = 0;
  async function search(q) {
    const seq = ++searchSeq;
    const hits = q.trim() ? await api(`/api/journey/symbols?q=${encodeURIComponent(q)}`) : [];
    if (seq !== searchSeq) return;
    const ul = $('symHits');
    ul.hidden = !q.trim();
    ul.replaceChildren(...(hits.length ? hits.map((x) => h('li', {},
      h('button', { type: 'button', onclick: () => { $('symQ').value = ''; followSym(x.id).catch(fail); } },
        h('span', { class: 'mono' }, x.name), h('span', { class: 'muted small' }, ` ${x.kind} · ${x.dir}${x.alive ? '' : ' · deleted'}`)))) : [h('li', { class: 'muted small' }, 'No symbol by that name.')]));
  }

  // ---------- Playback rows ----------

  // The time axis shared by the chapter bands and the periods.
  function axis() {
    const ts = state.data.revTimes;
    let lo = Infinity, hi = -Infinity;
    for (const t of ts) { lo = Math.min(lo, t); hi = Math.max(hi, t); }
    const unit = periodUnit(hi - lo);
    const start = periodStart(lo, unit);
    const periods = [];
    for (let s = start; s <= hi; s = nextPeriod(s, unit)) periods.push({ start: s, end: nextPeriod(s, unit), commits: 0, firstRev: -1 });
    ts.forEach((t, i) => {
      const p = periods.find((q) => t >= q.start && t < q.end) || periods[periods.length - 1];
      p.commits++;
      if (p.firstRev < 0) p.firstRev = i;
    });
    return { unit, lo: start, hi: periods[periods.length - 1].end, periods };
  }

  function periodUnit(span) {
    if (span >= 90 * DAY) return 'month';
    if (span >= 21 * DAY) return 'week';
    if (span >= 3 * DAY) return 'day';
    return 'hour';
  }

  function periodStart(t, unit) {
    const d = new Date(t * 1000);
    if (unit === 'month') return Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), 1) / 1000;
    if (unit === 'hour') return Math.floor(t / 3600) * 3600;
    const day = Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), d.getUTCDate()) / 1000;
    if (unit === 'day') return day;
    return day - ((d.getUTCDay() + 6) % 7) * DAY;
  }

  function nextPeriod(t, unit) {
    if (unit === 'month') {
      const d = new Date(t * 1000);
      return Date.UTC(d.getUTCFullYear(), d.getUTCMonth() + 1, 1) / 1000;
    }
    return t + { week: 7 * DAY, day: DAY, hour: 3600 }[unit];
  }

  function periodName(p, unit) {
    const d = new Date(p.start * 1000);
    if (unit === 'month') {
      const m = d.toLocaleDateString(undefined, { month: 'short', timeZone: 'UTC' });
      return d.getUTCMonth() === 0 ? `${m} ${d.getUTCFullYear()}` : m;
    }
    if (unit === 'hour') return d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', timeZone: 'UTC' });
    return fmtShort(p.start);
  }

  function buildRows() {
    const ax = axis();
    state.axis = ax;
    const pct = (t) => `${(100 * (t - ax.lo)) / (ax.hi - ax.lo)}%`;
    const width = (a, b) => `${(100 * (b - a)) / (ax.hi - ax.lo)}%`;
    $('periodLabel').textContent = { month: 'Months', week: 'Weeks', day: 'Days', hour: 'Hours (UTC)' }[ax.unit];

    const maxC = Math.max(1, ...ax.periods.map((p) => p.commits));
    $('periods').replaceChildren(...ax.periods.map((p, i) => {
      const label = `${periodName(p, ax.unit)}: ${plural(p.commits, 'commit')}`;
      const b = h('button', {
        type: 'button', class: 'period', 'aria-label': label, title: label,
        style: `left:${pct(p.start)};width:calc(${width(p.start, p.end)} - 2px)`,
        onclick: () => seekRev(p.firstRev >= 0 ? p.firstRev : firstRevAfter(p.start)),
      },
      h('span', { class: 'bar', style: `height:${p.commits ? Math.max(3, (22 * p.commits) / maxC) : 0}px` }),
      h('span', { class: 'pl' }, ax.periods.length > 40 && i % Math.ceil(ax.periods.length / 20) ? '' : periodName(p, ax.unit)));
      b.dataset.i = i;
      return b;
    }));

    const cs = state.data.chapters;
    const bands = cs.map((c, i) => {
      const from = i === 0 ? ax.lo : c.from;
      const to = i + 1 < cs.length ? cs[i + 1].from : ax.hi;
      const b = h('button', {
        type: 'button', class: 'band', title: `Chapter ${i + 1}: ${c.title}`,
        'aria-label': `Chapter ${i + 1}: ${c.title}, ${fmtShort(c.from)} to ${fmtShort(c.to)}`,
        style: `left:${pct(from)};width:calc(${width(from, to)} - 2px)`,
        onclick: () => seekRev(c.firstRev),
      }, `${i + 1} · ${c.title}`);
      return b;
    });
    $('bands').replaceChildren(...bands, ...releaseMarkers(pct));
  }

  // Releases sit on the chapter track as ticks; a chapter often ends with one.
  function releaseMarkers(pct) {
    return (state.data.releases || []).map((m) => {
      const label = `${m.tag} · ${fmtBig(m.t)}`;
      return h('button', {
        type: 'button', class: `rel${m.weight >= 3.5 ? ' major' : ''}`, title: m.text, 'aria-label': `Release ${label}`,
        style: `left:${pct(m.t)}`,
        onclick: () => seekRev(m.rev),
      });
    });
  }

  function firstRevAfter(t) {
    const ts = state.data.revTimes;
    const i = ts.findIndex((x) => x >= t);
    return i < 0 ? ts.length - 1 : i;
  }

  function renderRows(f) {
    const ax = state.axis;
    const t = state.data.revTimes[rev()] ?? f.t;
    const ci = chapterAt(rev());
    $('bands').querySelectorAll('.band').forEach((b, i) => {
      b.classList.toggle('cur', i === ci);
      b.classList.toggle('future', i > ci);
      b.setAttribute('aria-current', i === ci ? 'step' : 'false');
    });
    const r = rev();
    $('bands').querySelectorAll('.rel').forEach((b, i) => b.classList.toggle('past', state.data.releases[i].rev <= r));
    $('periods').querySelectorAll('.period').forEach((b) => {
      const p = ax.periods[b.dataset.i];
      const cur = t >= p.start && t < p.end;
      b.classList.toggle('cur', cur);
      b.classList.toggle('past', p.end <= t);
      b.setAttribute('aria-current', cur ? 'date' : 'false');
    });
    // The playhead sits over the tracks' column.
    const track = $('bands');
    const x = track.offsetLeft + (track.clientWidth * (t - ax.lo)) / (ax.hi - ax.lo);
    $('playhead').style.left = `${x}px`;
  }

  // ---------- Story ----------

  function buildChapterList() {
    const cs = state.data.chapters;
    $('chapters').replaceChildren(...cs.map((c, i) => h('li', {},
      h('button', { type: 'button', onclick: () => seekRev(c.firstRev) },
        h('span', { class: 'cn' }, String(i + 1)),
        h('span', {}, h('span', { class: 'ct' }, c.title), h('span', { class: 'cd' }, `${fmtShort(c.from)} – ${fmtBig(c.to)} · ${plural(c.commits, 'commit')}`))))));
  }

  function renderStory() {
    const cs = state.data.chapters;
    renderFollow();
    if (!cs.length) return;
    const r = rev();
    const ci = chapterAt(r);
    const c = cs[ci];
    $('chapNum').textContent = `Chapter ${ci + 1} of ${cs.length}`;
    $('chapTitle').textContent = c.title;
    $('chapDates').textContent = c.from === c.to ? fmtBig(c.from) : `${fmtBig(c.from)} – ${fmtBig(c.to)}`;
    $('chapText').textContent = c.text;
    const stat = (v, l, cls) => h('div', { class: 'stat' }, h('span', { class: `sv ${cls || ''}` }, v), h('span', { class: 'sl' }, l));
    $('chapStats').replaceChildren(
      stat(c.commits.toLocaleString(), 'commits'),
      h('div', { class: 'stat' }, h('span', { class: 'sv' }, h('span', { class: 'pos' }, `+${c.added}`), ' ', h('span', { class: 'neg' }, `−${c.removed}`)), h('span', { class: 'sl' }, 'symbols')),
      h('div', { class: 'stat', title: c.hottest || '' }, h('span', { class: 'sv mono' }, c.hottest ? base(c.hottest) : '—'), h('span', { class: 'sl' }, 'hottest package')),
      stat((c.authors ?? 0).toLocaleString(), c.authors === 1 ? 'author' : 'authors'),
      stat((c.themes?.pullRequests ?? 0).toLocaleString(), 'merged pull requests'),
      h('div', { class: 'stat', title: (c.releases || []).join(', ') }, h('span', { class: 'sv mono' }, c.releases?.length ? c.releases[c.releases.length - 1] : '—'),
        h('span', { class: 'sl' }, c.releases?.length > 1 ? `last of ${c.releases.length} releases` : 'release')),
    );
    renderThemes(c);

    // The last four moments up to the playhead.
    const past = state.moments.filter((m) => m.rev <= r).slice(-4);
    $('moments').replaceChildren(...(past.length ? past.map((m) => {
      const now = state.card ? m === state.card : inFrame(m, state.frame);
      return h('li', { class: `moment k-${m.kind}${now ? ' now' : ''}${m.follow ? ' fol' : ''}` },
        h('button', { type: 'button', 'aria-current': now ? 'true' : null, onclick: () => { seekRev(m.rev); showCard(m); } },
          h('span', { class: 'dot', 'aria-hidden': 'true' }),
          h('span', {}, h('div', { class: 'mt' }, m.text), h('div', { class: 'md mono' }, `${fmtBig(m.t)} · ${m.sha.slice(0, 7)}`))));
    }) : [h('li', { class: 'muted small' }, 'Nothing yet.')]));

    $('chapters').querySelectorAll('li').forEach((li, i) => {
      li.firstChild.setAttribute('aria-current', i === ci ? 'step' : 'false');
      li.classList.toggle('future', i > ci);
    });

    if (state.narrate && state.spoken !== ci) speak(ci);
  }

  // What the chapter's commit messages talk about, as counted chips.
  function renderThemes(c) {
    const th = c.themes || {};
    const chip = (kind, t) => h('li', { class: `chip ${kind}`, title: `${plural(t.count, 'commit')}` }, t.name, h('span', { class: 'cc' }, String(t.count)));
    const group = (label, kind, ts) => (ts && ts.length ? [h('li', { class: 'tl eyebrow' }, label), ...ts.map((t) => chip(kind, t))] : []);
    const items = [
      ...group('Types', 'type', th.types),
      ...group('Scopes', 'scope', th.scopes),
      ...group('Words', 'term', th.terms),
    ];
    $('chapThemes').replaceChildren(...(items.length ? [h('ul', { 'aria-label': 'Commit message themes' }, ...items)] : []));
  }

  // ---------- Narration ----------

  function speak(ci) {
    if (!('speechSynthesis' in window)) return;
    const c = state.data.chapters[ci];
    state.spoken = ci;
    speechSynthesis.cancel();
    speechSynthesis.speak(new SpeechSynthesisUtterance(`Chapter ${ci + 1}. ${c.title}. ${c.text}`));
  }

  function setNarrate(on) {
    state.narrate = on;
    $('narrate').setAttribute('aria-pressed', String(on));
    if (on) {
      state.spoken = -1;
      renderStory();
    } else if ('speechSynthesis' in window) {
      speechSynthesis.cancel();
    }
  }

  // ---------- Knowledge ----------

  // buildAuthors fills the author picker, keeping the author picked when
  // they are still there.
  function buildAuthors() {
    const sel = $('who');
    const authors = state.data.authors || [];
    if (state.who && !authors.includes(state.who)) state.who = '';
    sel.replaceChildren(h('option', { value: '' }, 'All authors'), ...authors.map((a) => h('option', { value: a }, a)));
    sel.value = state.who;
    sel.disabled = authors.length === 0;
  }

  // renderKnowLegend names who knows the code at the playhead: each
  // author's share of the live packages, weighed by their symbols.
  function renderKnowLegend(f) {
    const know = state.colorBy === 'knowledge';
    $('actLegend').hidden = know;
    const el = $('knowLegend');
    el.hidden = !know;
    if (!know) return;
    const by = new Map();
    let total = 0;
    for (const p of f.pkgs) {
      if (!p.live || !p.knowers) continue;
      total += p.live;
      for (const [a, share, gone] of p.knowers) {
        const k = by.get(a) || { share: 0, gone };
        k.share += share * p.live;
        k.gone = gone;
        by.set(a, k);
      }
    }
    const top = [...by.entries()].sort((x, y) => y[1].share - x[1].share || x[0] - y[0]).slice(0, 6);
    el.replaceChildren(...top.map(([a, k]) => {
      const name = state.data.authors[a];
      return h('span', { class: `kn${k.gone ? ' gone' : ''}`, title: `${name}${k.gone ? ', no commits for six months' : ''}: ${pct(k.share / total)} of what is known` },
        h('i', { style: `background:${k.gone ? GONE : authorColor(name)}` }), `${name} ${pct(k.share / total)}`);
    }), h('span', { class: 'kn' }, h('i', { style: `background:${GONE}` }), 'departed'));
  }

  function setColorBy(v) {
    state.colorBy = v === 'knowledge' ? 'knowledge' : 'edits';
    for (const b of $('colorBy').querySelectorAll('button')) b.setAttribute('aria-pressed', String(b.dataset.v === state.colorBy));
    if (state.data) render(false);
  }

  function setWho(name) {
    state.who = name || '';
    $('who').value = state.who;
    if (!state.data) return;
    // A package picked that the author doesn't know leaves the map with them.
    if (state.picked != null && state.who && !scoped(state.data.frames[state.frame]).pkgs.some((s) => s.p === state.picked)) state.picked = null;
    render(false);
  }

  // ---------- Playback ----------

  // render draws the current frame. animate plays the package's symbol
  // events, for a step forward by one frame.
  function render(story = true, animate = false) {
    const f = state.data.frames[state.frame];
    if (!f) return;
    renderStage(f);
    if (state.drill) renderDrill(animate);
    if (state.links) {
      if (state.links.v) markFollowed();
      loadLinks().catch(fail);
    }
    placeFolTag();
    renderOverlays(f);
    renderRows(f);
    if (story) renderStory();
    chase();
    writeHash();
  }

  function seek(i, force) {
    const n = state.data.frames.length;
    if (!n) return;
    i = Math.max(0, Math.min(n - 1, i));
    if (i === state.frame && !force) return;
    const from = state.frame;
    state.frame = i;
    if (state.card && !inFrame(state.card, i)) {
      state.card = null;
      $('card').hidden = true;
    }
    render(true, i === from + 1);
  }

  function seekRev(r) {
    const i = state.data.frames.findIndex((f) => f.total - 1 >= r);
    seek(i < 0 ? state.data.frames.length - 1 : i);
  }

  function wait(ms, fn) {
    clearTimeout(state.timer);
    state.timer = setTimeout(fn, ms / state.speed);
  }

  function play() {
    if (state.frame >= state.data.frames.length - 1) seek(0);
    state.playing = true;
    $('play').setAttribute('aria-label', 'Pause');
    $('playIcon').innerHTML = '<path d="M6 4h4v16H6zM14 4h4v16h-4z"></path>';
    wait(BASE_MS, tick);
  }

  // tick plays the next frame, then pauses at its key moments in story mode.
  function tick() {
    seek(state.frame + 1);
    state.queue = state.story ? pausesIn(state.frame) : [];
    advance();
  }

  function advance() {
    if (!state.playing) return;
    const m = state.queue.shift();
    if (m) {
      showCard(m);
      wait(PAUSE_MS, advance);
    } else if (state.frame >= state.data.frames.length - 1) {
      stop();
    } else {
      if (state.card) hideCard();
      wait(BASE_MS, tick);
    }
  }

  function stop() {
    clearTimeout(state.timer);
    state.timer = null;
    state.playing = false;
    state.queue = [];
    $('play').setAttribute('aria-label', 'Play');
    $('playIcon').innerHTML = '<path d="M7 4v16l13-8z"></path>';
  }

  const toggle = () => (state.playing ? stop() : play());

  function setSpeed(v) {
    state.speed = v;
    for (const b of $('speed').querySelectorAll('button')) b.setAttribute('aria-pressed', String(Number(b.dataset.v) === v));
    document.documentElement.style.setProperty('--tween', `${Math.round(600 / v)}ms`);
  }

  function setStory(on) {
    state.story = on;
    $('storyMode').setAttribute('aria-pressed', String(on));
    if (!on) state.queue = [];
    if (state.data) writeHash();
  }

  // ---------- Links ----------

  // writeHash keeps the view in the URL so that it can be shared: the step,
  // frame, chapter, the package looked into and the symbol followed.
  function writeHash() {
    const p = new URLSearchParams();
    p.set('step', state.step);
    p.set('f', String(state.frame));
    if (state.data.chapters.length) p.set('ch', String(chapterAt(rev()) + 1));
    if (state.drill) p.set('pkg', state.drill.dir);
    if (state.links) p.set('links', state.links.dir);
    if (state.follow) p.set('sym', String(state.follow.id));
    if (!state.story) p.set('story', '0');
    if (state.colorBy !== 'edits') p.set('color', state.colorBy);
    if (state.lines !== 'key') p.set('lines', state.lines);
    if (state.who) p.set('who', state.who);
    const s = `#${p.toString().replaceAll('%2F', '/')}`;
    if (s !== location.hash) {
      try { history.replaceState(null, '', s); } catch { /* ignore */ }
    }
  }

  // applyHash goes to the view a link names. A frame wins over a chapter.
  async function applyHash(first) {
    const p = new URLSearchParams(location.hash.slice(1));
    const step = p.get('step');
    if (first) {
      await setStep(['commit', 'week', 'month'].includes(step) ? step : defaultStep(state.status), 0);
    } else if (['commit', 'week', 'month'].includes(step) && step !== state.step) {
      await setStep(step);
    }
    setStory(p.get('story') !== '0');
    state.colorBy = p.get('color') === 'knowledge' ? 'knowledge' : 'edits';
    for (const b of $('colorBy').querySelectorAll('button')) b.setAttribute('aria-pressed', String(b.dataset.v === state.colorBy));
    setLines(p.get('lines'));
    state.who = state.data.authors.includes(p.get('who')) ? p.get('who') : '';
    $('who').value = state.who;
    if (!state.data.frames.length) return;
    const f = Number.parseInt(p.get('f'), 10), ch = Number.parseInt(p.get('ch'), 10);
    if (Number.isInteger(f)) seek(f);
    else if (state.data.chapters[ch - 1]) seekRev(state.data.chapters[ch - 1].firstRev);
    const sym = Number.parseInt(p.get('sym'), 10);
    if (sym > 0) await followSym(sym).catch(() => {});
    else if (state.follow) unfollow();
    const pkg = p.get('pkg');
    if (pkg && (!state.drill || state.drill.dir !== pkg)) await drill(pkg);
    else if (!pkg) undrill();
    const links = p.get('links');
    if (links && (!state.links || state.links.dir !== links)) await openLinks(links);
    else if (!links) closeLinks(false);
    render();
  }

  // ---------- Start ----------

  async function main() {
    const st = await waitReady();
    if (!st) return;
    state.status = st;
    $('repo').textContent = st.repo;
    $('swatches').replaceChildren(...ACT.map((c) => h('span', { style: `background:${c}` })));

    $('prev').addEventListener('click', () => { stop(); seek(state.frame - 1); });
    $('next').addEventListener('click', () => { stop(); seek(state.frame + 1); });
    $('prevMoment').addEventListener('click', () => stepMoment(-1));
    $('nextMoment').addEventListener('click', () => stepMoment(1));
    $('play').addEventListener('click', toggle);
    $('speed').addEventListener('click', (e) => { const b = e.target.closest('button'); if (b) setSpeed(Number(b.dataset.v)); });
    $('step').addEventListener('click', (e) => { const b = e.target.closest('button'); if (b) setStep(b.dataset.v).catch(fail); });
    $('storyMode').addEventListener('click', () => setStory(!state.story));
    $('colorBy').addEventListener('click', (e) => { const b = e.target.closest('button'); if (b) setColorBy(b.dataset.v); });
    $('lines').addEventListener('click', (e) => { const b = e.target.closest('button'); if (b) setLines(b.dataset.v); });
    $('who').addEventListener('change', (e) => setWho(e.target.value));
    $('narrate').addEventListener('click', () => setNarrate(!state.narrate));
    $('back').addEventListener('click', undrill);
    // A click on the map around the packages brings the rest of them back.
    $('stage').addEventListener('click', (e) => {
      if (e.target.closest('#world') && !e.target.closest('.bubble, #syms')) unpick();
    });
    $('boClose').addEventListener('click', () => closeLinks());
    $('boInside').addEventListener('click', () => {
      const { dir } = state.links;
      closeLinks(false);
      drill(dir).catch(fail);
    });
    new ResizeObserver(() => drawWires()).observe($('boGrid'));
    setupZoom();
    $('zoomIn').addEventListener('click', () => zoomBy(1.6));
    $('zoomOut').addEventListener('click', () => zoomBy(1 / 1.6));
    $('zoomReset').addEventListener('click', () => zoomTo(d3.zoomIdentity));
    if (!('speechSynthesis' in window)) {
      $('narrate').disabled = true;
      $('narrate').title = 'This browser cannot speak';
    }
    let debounce;
    $('symQ').addEventListener('input', (e) => {
      clearTimeout(debounce);
      debounce = setTimeout(() => search(e.target.value).catch(fail), 180);
    });
    $('symQ').addEventListener('keydown', (e) => {
      if (e.key === 'Enter') {
        const b = $('symHits').querySelector('button');
        if (b) { e.preventDefault(); b.click(); }
      } else if (e.key === 'Escape') {
        e.target.value = '';
        $('symHits').hidden = true;
      }
    });
    // Shortcuts belong to the page, not to fields being typed in.
    document.addEventListener('keydown', (e) => {
      if (e.defaultPrevented || e.altKey || e.ctrlKey || e.metaKey) return;
      if (e.target.closest && e.target.closest('input, textarea, select, [contenteditable]')) return;
      switch (e.key) {
        case ' ':
          if (e.target.closest('button, a')) return;
          e.preventDefault();
          toggle();
          break;
        case 'ArrowLeft':
          if (e.shiftKey) stepMoment(-1);
          else { stop(); seek(state.frame - 1); }
          break;
        case 'ArrowRight':
          if (e.shiftKey) stepMoment(1);
          else { stop(); seek(state.frame + 1); }
          break;
        case '[':
          stepMoment(-1);
          break;
        case ']':
          stepMoment(1);
          break;
        case '+':
        case '=':
          zoomBy(1.6);
          break;
        case '-':
          zoomBy(1 / 1.6);
          break;
        case '0':
          zoomTo(d3.zoomIdentity);
          break;
        case 'Escape':
          if (state.links) closeLinks();
          else if (state.card) hideCard();
          else if (state.picked != null && !state.drill) unpick();
          else undrill();
          break;
        default:
          return;
      }
    });
    window.addEventListener('hashchange', () => applyHash(false).catch(fail));
    new ResizeObserver(() => place()).observe($('stage'));

    setSpeed(1);
    $('loading').hidden = true;
    $('app').hidden = false;
    await applyHash(true);
    if (!state.data.frames.length) {
      $('caption').textContent = 'No history to play back.';
    }
  }

  main().catch(fail);
})();
