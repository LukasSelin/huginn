'use strict';

// The code map: a squarified treemap of package directories, colored (and by
// default sized) by what happened to them in a time window, over a timeline
// that picks the window.

const STEPS = ['#2A2418', '#4A3A1E', '#7A5A26', '#B07F33', '#F0A64A'];
const DARK_TEXT = 3; // steps from here on are light enough to need dark text
const WEEK = 7 * 86400;
const LANES = 8;
const PRESETS = { '30d': 30 * 86400, '90d': 90 * 86400, '1y': 365 * 86400 };
const KIND_LABEL = Object.fromEntries(KINDS);

const state = {
  status: null,
  view: { z: '', k: 0, sel: null, color: 'churn', size: 'color', tests: false, w: '90d', from: 0, to: 0 },
  map: null,    // last /api/map response for the current window
  series: null, // the timeline: weeks, commits and per-directory series
  detail: null, // last /api/map/{dir} response
};

// ---- navigation (kept in the URL hash so views can be linked) ----

function readHash() {
  const p = new URLSearchParams(location.hash.slice(1));
  const v = {
    z: p.get('z') || '', k: Number(p.get('k')) || 0, sel: p.get('s'),
    color: ['churn', 'recency', 'complexity'].includes(p.get('c')) ? p.get('c') : 'churn',
    size: p.get('a') === 'loc' ? 'loc' : 'color',
    tests: p.get('t') === '1', w: p.get('w') || '90d', from: Number(p.get('from')) || 0, to: Number(p.get('to')) || 0,
  };
  if (v.w !== 'custom' && !(v.w in PRESETS) && v.w !== 'all') v.w = '90d';
  return v;
}

function go(next) {
  const v = { ...state.view, ...next };
  const p = new URLSearchParams();
  if (v.z) p.set('z', v.z);
  if (v.k) p.set('k', v.k);
  if (v.sel) p.set('s', v.sel);
  if (v.color !== 'churn') p.set('c', v.color);
  if (v.size !== 'color') p.set('a', v.size);
  if (v.tests) p.set('t', '1');
  p.set('w', v.w);
  if (v.w === 'custom') {
    p.set('from', v.from);
    p.set('to', v.to);
  }
  location.hash = p.toString();
}

// windowOf resolves a view's window to unix seconds; 0 leaves an end open.
// Presets count back from the last commit, not from today.
function windowOf(v) {
  if (v.w === 'custom') return { from: v.from, to: v.to };
  if (v.w === 'all') return { from: 0, to: 0 };
  const end = state.status.to + 1;
  return { from: end - PRESETS[v.w], to: 0 };
}

const query = (v) => {
  const { from, to } = windowOf(v);
  const p = new URLSearchParams();
  if (from) p.set('from', from);
  if (to) p.set('to', to);
  if (v.tests) p.set('tests', '1');
  return p.toString();
};

async function route() {
  const prev = state.view;
  const v = readHash();
  state.view = v;
  const windowChanged = !state.map || query(prev) !== query(v);
  if (!state.series || prev.tests !== v.tests) {
    state.series = await api(`/api/map?series=1${v.tests ? '&tests=1' : ''}`);
  }
  if (windowChanged) state.map = await api(`/api/map?${query(v)}`);
  if (v.sel && (windowChanged || !state.detail || state.detail.dir !== v.sel)) {
    const d = v.sel === '.' ? '' : v.sel.split('/').map(encodeURIComponent).join('/');
    state.detail = await api(`/api/map/${d}?${query(v)}`).catch(() => null);
  } else if (!v.sel) {
    state.detail = null;
  }
  $('loading').hidden = true; // clears an earlier failure
  render();
}

// ---- color ----

// metric returns the value a tile is colored by; 0 means "nothing in the
// window" and always gets the darkest step.
function metric(t) {
  switch (state.view.color) {
    case 'recency': return t.last || 0;
    case 'complexity': return Math.abs(t.complexityDelta);
    default: return t.edits;
  }
}

// span is the stretch of time recency is measured over: the window, with open
// ends closed at the first edit and the last commit.
function span(tiles) {
  let { from, to } = windowOf(state.view);
  from = from || Math.min(...tiles.map((t) => t.last).filter(Boolean), state.status.from);
  to = to || state.status.to + 1;
  return { from, to };
}

// sizer returns the area function for the tiles: live lines, or the color
// metric, so the biggest tiles are the ones the color calls out. Recency is
// scaled to 1–4 across the span, so the newest is at most four times the
// oldest; 0 means the tile is left out.
function sizer(tiles) {
  if (state.view.size === 'loc') return (t) => t.loc;
  if (state.view.color !== 'recency') return (t) => (t.loc > 0 ? metric(t) : 0);
  const { from, to } = span(tiles);
  return (t) => (t.loc > 0 && t.last ? 1 + (3 * Math.max(0, t.last - from)) / Math.max(1, to - from) : 0);
}

// scale maps metric values to steps 1–4 by quartiles of the non-zero values,
// or, for recency, by quarter of the window. It returns the step function and
// the legend labels for steps 0–4.
function scale(tiles) {
  if (state.view.color === 'recency') {
    const { from, to } = span(tiles);
    const q = (to - from) / 4;
    const step = (v) => (v ? 1 + Math.min(3, Math.max(0, Math.floor((v - from) / q))) : 0);
    const day = (t) => new Date(t * 1000).toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: '2-digit' });
    return { step, labels: ['no edits', ...[0, 1, 2, 3].map((i) => `since ${day(from + i * q)}`)] };
  }
  const vals = tiles.map(metric).filter((v) => v > 0).sort((a, b) => a - b);
  const cut = [0.25, 0.5, 0.75].map((p) => vals[Math.floor(p * (vals.length - 1))] ?? 0);
  const step = (v) => (v <= 0 ? 0 : v <= cut[0] ? 1 : v <= cut[1] ? 2 : v <= cut[2] ? 3 : 4);
  const max = vals[vals.length - 1] ?? 0;
  const range = (lo, hi) => (lo > hi ? null : lo === hi ? `${lo}` : `${lo}–${hi}`);
  const unit = state.view.color === 'complexity' ? '|Δ| ' : '';
  const labels = [state.view.color === 'complexity' ? 'no change' : 'no edits',
    range(1, cut[0]), range(cut[0] + 1, cut[1]), range(cut[1] + 1, cut[2]), range(cut[2] + 1, max)]
    .map((l, i) => (i && l ? unit + l : l));
  return { step, labels };
}

// ---- treemap layout ----

// squarify lays items (with .value > 0, sorted descending) out in the
// rectangle, keeping tiles close to square (Bruls, Huizing & van Wijk). It
// returns the items with x, y, w, h set.
function squarify(items, x, y, w, h) {
  const total = items.reduce((s, it) => s + it.value, 0);
  if (!items.length || total <= 0 || w <= 0 || h <= 0) return [];
  const k = (w * h) / total;
  const areas = items.map((it) => it.value * k);
  const out = [];
  const worst = (sum, min, max, side) => Math.max((side * side * max) / (sum * sum), (sum * sum) / (side * side * min));
  let i = 0;
  while (i < items.length) {
    const side = Math.min(w, h);
    let j = i, sum = 0, min = Infinity, max = 0, best = Infinity;
    while (j < items.length) {
      const a = areas[j];
      const r = worst(sum + a, Math.min(min, a), Math.max(max, a), side);
      if (r > best) break;
      best = r; sum += a; min = Math.min(min, a); max = Math.max(max, a); j++;
    }
    // The row is laid along the short side, as a strip of thickness sum/side.
    const thick = side > 0 ? sum / side : 0;
    let off = 0;
    for (let n = i; n < j; n++) {
      const len = thick > 0 ? areas[n] / thick : 0;
      out.push(w >= h
        ? { ...items[n], x, y: y + off, w: thick, h: len }
        : { ...items[n], x: x + off, y, w: len, h: thick });
      off += len;
    }
    if (w >= h) { x += thick; w -= thick; } else { y += thick; h -= thick; }
    i = j;
  }
  return out;
}

// tree nests the tiles with any area by path segment; each node's value is
// the area under it.
function tree(tiles, size) {
  const root = { name: '', path: '', kids: new Map(), tile: null, value: 0 };
  for (const t of tiles) {
    if (size(t) <= 0) continue;
    let n = root;
    if (t.dir !== '.') {
      for (const seg of t.dir.split('/')) {
        const p = n.path ? `${n.path}/${seg}` : seg;
        if (!n.kids.has(seg)) n.kids.set(seg, { name: seg, path: p, kids: new Map(), tile: null, value: 0 });
        n = n.kids.get(seg);
      }
    }
    n.tile = t;
  }
  const sum = (n) => (n.value = (n.tile ? size(n.tile) : 0) + [...n.kids.values()].reduce((s, k) => s + sum(k), 0));
  sum(root);
  return root;
}

function find(root, path) {
  if (!path) return root;
  let n = root;
  for (const seg of path.split('/')) {
    n = n.kids.get(seg);
    if (!n) return null;
  }
  return n;
}

// leaves returns every tile at or under n, biggest first by size.
function leaves(n, size, out = []) {
  if (n.tile) out.push(n.tile);
  for (const k of n.kids.values()) leaves(k, size, out);
  return out.sort((a, b) => size(b) - size(a));
}

const rel = (dir, base) => {
  if (dir === '.') return '(root)';
  if (dir === base) return dir.slice(dir.lastIndexOf('/') + 1);
  return base && dir.startsWith(base + '/') ? dir.slice(base.length + 1) : dir;
};

// fit squarifies items, then folds the tail that came out too small to read
// into one "+N more" item and lays out again.
function fit(items, x, y, w, h, tooSmall, more) {
  let laid = squarify(items, x, y, w, h);
  const cut = laid.findIndex(tooSmall);
  if (cut >= 0 && cut < items.length - 1) {
    const rest = items.slice(cut);
    laid = squarify([...items.slice(0, cut), more(rest)], x, y, w, h);
  }
  return laid;
}

const smallLeaf = (r) => r.w < 58 || r.h < 34;

function renderTreemap() {
  const box = $('treemap');
  const W = box.clientWidth, H = box.clientHeight;
  const size = sizer(state.map.dirs);
  const root = tree(state.map.dirs, size);
  let at = find(root, state.view.z);
  if (!at) at = root;
  const { step } = scale(state.map.dirs);

  // The items at this level: the directory's own package, then its
  // subdirectories. A subtree with one package is shown as that package.
  let items = [];
  if (at.tile) items.push({ type: 'leaf', tile: at.tile, value: size(at.tile), label: rel(at.tile.dir, at.path) });
  for (const k of at.kids.values()) {
    if (k.value <= 0) continue;
    const ls = leaves(k, size);
    items.push(ls.length === 1
      ? { type: 'leaf', tile: ls[0], value: k.value, label: rel(ls[0].dir, at.path) }
      : { type: 'group', node: k, value: k.value, label: rel(k.path, at.path) });
  }
  items.sort((a, b) => b.value - a.value);
  const skip = Math.min(state.view.k, Math.max(0, items.length - 1));
  items = items.slice(skip);

  const kids = [];
  const laid = fit(items, 0, 0, W, H,
    (r) => (r.type === 'group' ? r.w < 96 || r.h < 64 : smallLeaf(r)),
    (rest) => ({ type: 'more', value: rest.reduce((s, r) => s + r.value, 0), count: rest.length, skip: skip + items.length - rest.length }));
  for (const r of laid) {
    if (r.type === 'leaf') kids.push(leafEl(r, step));
    else if (r.type === 'more') kids.push(moreEl(r, `+${r.count} more`, () => go({ k: r.skip })));
    else kids.push(groupEl(r, step, size));
  }
  if (!laid.length) {
    const why = state.view.size === 'loc' ? 'No live code here.'
      : state.view.color === 'complexity' ? 'No complexity change here in this window.' : 'No edits here in this window.';
    kids.push(h('div', { class: 'empty' }, why));
  }
  box.replaceChildren(...kids);
}

const place = (r, inset = 1) =>
  `left:${r.x + inset}px;top:${r.y + inset}px;width:${Math.max(0, r.w - 2 * inset)}px;height:${Math.max(0, r.h - 2 * inset)}px`;

function leafEl(r, step) {
  const t = r.tile;
  const s = step(metric(t));
  const lines = [h('span', { class: 'tname' }, r.label)];
  if (r.h >= 44) lines.push(h('span', { class: 'tcount' }, `${plural(t.commits, 'commit')} · ${plural(t.edits, 'change')}`));
  const room = Math.floor((r.h - 50) / 16);
  for (const hot of t.hot.slice(0, Math.max(0, Math.min(2, room)))) lines.push(h('span', { class: 'thot' }, `${hot.name} ×${hot.edits}`));
  return h('button', {
    type: 'button', class: `tile${s >= DARK_TEXT ? ' dark' : ''}${r.w < 40 ? ' bare' : ''}`,
    style: `${place(r)};background:${STEPS[s]}`, 'aria-pressed': String(state.view.sel === t.dir),
    title: `${t.dir} — ${t.loc.toLocaleString()} lines · ${plural(t.commits, 'commit')} · ${plural(t.edits, 'semantic edit')}`,
    onclick: () => go({ sel: t.dir }),
  }, lines);
}

function moreEl(r, label, onclick) {
  return h('button', { type: 'button', class: 'tile more', style: place(r), title: 'Show the smaller packages', onclick }, h('span', { class: 'tname' }, label));
}

function groupEl(r, step, size) {
  const n = r.node;
  const HEAD = 20, PAD = 3;
  const inner = leaves(n, size).map((t) => ({ type: 'leaf', tile: t, value: size(t), label: rel(t.dir, n.path) }));
  const laid = fit(inner, PAD, HEAD, r.w - 2 * PAD - 2, r.h - HEAD - PAD - 2, smallLeaf,
    (rest) => ({ type: 'more', value: rest.reduce((s, x) => s + x.value, 0), count: rest.length }));
  const zoom = () => go({ z: n.path, k: 0 });
  return h('div', { class: 'group', style: place(r) },
    h('button', { type: 'button', class: 'ghead', onclick: zoom, title: `Zoom into ${n.path}` },
      h('span', { class: 'mono' }, `${r.label}/`), h('span', { class: 'muted' }, ` ${inner.length} packages`)),
    laid.map((x) => (x.type === 'more' ? moreEl(x, `+${x.count}`, zoom) : leafEl(x, step))));
}

function renderZoom() {
  const parts = state.view.z ? state.view.z.split('/') : [];
  const crumbs = [h('button', { type: 'button', onclick: () => go({ z: '', k: 0 }) }, state.status.repo)];
  parts.forEach((seg, i) => {
    const p = parts.slice(0, i + 1).join('/');
    crumbs.push(h('span', {}, '›'), i === parts.length - 1 && !state.view.k
      ? h('span', { class: 'here' }, seg)
      : h('button', { type: 'button', onclick: () => go({ z: p, k: 0 }) }, seg));
  });
  if (state.view.k) crumbs.push(h('span', {}, '›'), h('span', { class: 'here' }, 'smaller packages'));
  $('zoom').replaceChildren(...crumbs);
}

function renderScale() {
  const { labels } = scale(state.map.dirs);
  $('scale').replaceChildren(...STEPS.map((c, i) => labels[i] == null ? null
    : h('span', { class: 'sw' }, h('span', { class: 'chip', style: `background:${c}` }), labels[i])));
}

// ---- timeline ----

// lanes picks the busiest packages under the current zoom, plus the
// selected one.
function lanes() {
  const z = state.view.z;
  const under = state.series.dirs.filter((d) => d.series && (!z || d.dir === z || d.dir.startsWith(z + '/')));
  const total = (d) => d.series.reduce((s, [, n]) => s + n, 0);
  const top = [...under].sort((a, b) => total(b) - total(a)).slice(0, LANES);
  const sel = state.series.dirs.find((d) => d.dir === state.view.sel);
  if (sel && sel.series && !top.includes(sel)) top.push(sel);
  return top;
}

function renderTimeline() {
  const el = $('timeline');
  const s = state.series;
  const LABEL = 150, BARS = 44, AXIS = 16, LANE = 14, GAP = 3;
  const W = Math.max(200, el.clientWidth - LABEL - 8);
  const ls = lanes();
  const H = BARS + AXIS + ls.length * (LANE + GAP);
  const n = Math.max(1, s.weeks);
  const cw = W / n;
  const t0 = s.weekStart, t1 = s.weekStart + n * WEEK;
  const x = (t) => ((t - t0) / (t1 - t0)) * W;

  const plot = svg('svg', { width: W, height: H, viewBox: `0 0 ${W} ${H}`, class: 'tlplot', role: 'img',
    'aria-label': `Commits per week, peaking at ${Math.max(0, ...s.commits)}, and edits in ${ls.length} packages` });
  const maxC = Math.max(1, ...s.commits);
  s.commits.forEach((c, i) => {
    if (!c) return;
    const bh = Math.max(1, (c / maxC) * (BARS - 4));
    plot.append(svg('rect', { x: i * cw + (cw > 3 ? 0.5 : 0), y: BARS - bh, width: Math.max(0.6, cw - (cw > 3 ? 1 : 0)), height: bh, fill: 'var(--bar)' }));
  });

  // Axis ticks at year (or month, for short histories) boundaries.
  const long = t1 - t0 > 2 * 365 * 86400;
  const d = new Date(t0 * 1000);
  let tick = long ? new Date(d.getFullYear() + 1, 0, 1) : new Date(d.getFullYear(), d.getMonth() + 1, 1);
  const every = Math.max(1, Math.ceil((t1 - t0) / (long ? 365 * 86400 : 30 * 86400) / Math.max(2, W / 80)));
  for (let i = 0; tick.getTime() / 1000 < t1; i++) {
    const tx = x(tick.getTime() / 1000);
    if (i % every === 0) {
      plot.append(svg('line', { x1: tx, x2: tx, y1: BARS, y2: BARS + 4, stroke: 'var(--line-strong)' }));
      const lbl = svg('text', { x: tx + 3, y: BARS + 13, class: 'tick' });
      lbl.textContent = tick.toLocaleDateString(undefined, long ? { year: 'numeric' } : { month: 'short', year: '2-digit' });
      plot.append(lbl);
    }
    tick = long ? new Date(tick.getFullYear() + 1, 0, 1) : new Date(tick.getFullYear(), tick.getMonth() + 1, 1);
  }

  // One heat strip per package; nothing is drawn before it existed.
  const cells = ls.flatMap((l) => l.series.map(([, v]) => v)).sort((a, b) => a - b);
  const cut = [0.25, 0.5, 0.75].map((p) => cells[Math.floor(p * (cells.length - 1))] ?? 0);
  const step = (v) => (v <= 0 ? 0 : v <= cut[0] ? 1 : v <= cut[1] ? 2 : v <= cut[2] ? 3 : 4);
  const labels = [h('div', { class: 'tlh', style: `height:${BARS + AXIS}px` }, h('span', { class: 'muted small' }, 'commits / week'))];
  ls.forEach((l, li) => {
    const y = BARS + AXIS + li * (LANE + GAP);
    const isSel = l.dir === state.view.sel;
    plot.append(svg('rect', { x: l.born * cw, y, width: W - l.born * cw, height: LANE, fill: STEPS[0] }));
    for (const [wi, v] of l.series) {
      plot.append(svg('rect', { x: wi * cw, y, width: Math.max(1, cw), height: LANE, fill: STEPS[step(v)] }));
    }
    if (isSel) plot.append(svg('rect', { x: l.born * cw - 1, y: y - 1, width: W - l.born * cw + 2, height: LANE + 2, fill: 'none', stroke: 'var(--text)', 'stroke-width': 1 }));
    labels.push(h('button', {
      type: 'button', class: `tll mono${isSel ? ' sel' : ''}`, style: `height:${LANE}px;margin-bottom:${GAP}px`,
      title: l.dir, onclick: () => go({ sel: l.dir }),
    }, l.dir));
  });

  // The window, drawn over everything; drag anywhere to set a new one.
  const win = windowOf(state.view);
  const wx0 = win.from ? Math.max(0, x(win.from)) : 0, wx1 = win.to ? Math.min(W, x(win.to)) : W;
  const shade = (a, b) => svg('rect', { x: a, y: 0, width: Math.max(0, b - a), height: H, class: 'tlshade' });
  const left = shade(0, wx0), right = shade(wx1, W);
  const frame = svg('rect', { x: wx0, y: 0.5, width: Math.max(1, wx1 - wx0), height: H - 1, class: 'tlwin' });
  plot.append(left, right, frame);

  const toT = (px) => t0 + (Math.min(W, Math.max(0, px)) / W) * (t1 - t0);
  const snap = (t) => t0 + Math.round((t - t0) / WEEK) * WEEK;
  plot.addEventListener('pointerdown', (e) => {
    plot.setPointerCapture(e.pointerId);
    const bx = plot.getBoundingClientRect().left;
    const a = e.clientX - bx;
    let b = a;
    const draw = () => {
      const lo = Math.min(a, b), hi = Math.max(a, b);
      left.setAttribute('width', lo);
      right.setAttribute('x', hi);
      right.setAttribute('width', Math.max(0, W - hi));
      frame.setAttribute('x', lo);
      frame.setAttribute('width', Math.max(1, hi - lo));
      $('windowLabel').textContent = rangeLabel(toT(lo), toT(hi));
    };
    const move = (ev) => { b = ev.clientX - bx; draw(); };
    const up = () => {
      plot.removeEventListener('pointermove', move);
      plot.removeEventListener('pointerup', up);
      let from = snap(toT(Math.min(a, b))), to = snap(toT(Math.max(a, b)));
      if (to - from < WEEK) to = from + WEEK; // a click picks one week
      go({ w: 'custom', from, to });
    };
    plot.addEventListener('pointermove', move);
    plot.addEventListener('pointerup', up);
    draw();
  });

  el.replaceChildren(h('div', { class: 'tllabels', style: `width:${LABEL}px` }, labels), plot);
}

const rangeLabel = (from, to) => `${fmtDate(from)} – ${fmtDate(to)}`;

function renderWindow() {
  const v = state.view;
  const { from, to } = windowOf(v);
  const label = rangeLabel(from || state.status.from, to || state.status.to);
  $('windowLabel').textContent = label;
  for (const b of $('presets').querySelectorAll('button')) b.setAttribute('aria-pressed', String(b.dataset.w === v.w));
  const tiles = state.map.dirs.filter((t) => t.loc > 0);
  const edits = state.map.dirs.reduce((s, t) => s + t.edits, 0);
  $('subtitle').textContent = `${plural(tiles.length, 'package')} · ${plural(edits, 'semantic edit')} · ${label}`;
}

// ---- inspector ----

function stat(label, value, cls) {
  return h('div', { class: 'stat' }, h('span', { class: `sv${cls ? ' ' + cls : ''}` }, value), h('span', { class: 'sl' }, label));
}

const signed = (n) => (n > 0 ? `+${n}` : n < 0 ? `−${-n}` : '0');

function renderInspector() {
  const el = $('inspector');
  const d = state.detail;
  if (!d) {
    const busiest = [...state.map.dirs].sort((a, b) => b.edits - a.edits).slice(0, 6);
    el.replaceChildren(
      h('span', { class: 'eyebrow' }, 'Package'),
      h('p', { class: 'muted' }, state.view.sel ? `${state.view.sel} has no symbols in this view.` : 'Select a package to inspect it.'),
      h('h2', { class: 'h2' }, 'Busiest in this window'),
      h('div', { class: 'hotlist' }, busiest.filter((t) => t.edits).map((t) => hotRow(t.dir, t.edits, busiest[0].edits, () => go({ sel: t.dir })))));
    return;
  }
  const follow = (id) => `/journey#sym=${id}`;
  const maxHot = Math.max(1, ...d.hot.map((x) => x.edits));
  el.replaceChildren(
    h('div', { class: 'stack' },
      h('span', { class: 'eyebrow' }, 'Package'),
      h('span', { class: 'sel-name' }, d.dir === '.' ? `${state.status.repo} (root)` : d.dir),
      h('span', { class: 'muted small' }, `${d.loc.toLocaleString()} lines · ${plural(d.symbols, 'live symbol')} · cyclomatic ${d.complexity} · ${plural(d.authors, 'author')}`)),
    h('div', { class: 'stats' },
      stat('commits', d.commits.toLocaleString()),
      stat('semantic edits', d.edits.toLocaleString()),
      stat('symbols added / removed', h('span', {}, h('span', { class: 'pos' }, `+${d.added}`), ' / ', h('span', { class: 'neg' }, `−${d.removed}`))),
      stat('complexity Δ', signed(d.complexityDelta), d.complexityDelta > 0 ? 'neg' : d.complexityDelta < 0 ? 'pos' : '')),
    h('div', { class: 'legend' }, KINDS.filter(([k]) => d.kinds[k]).map(([k, l]) => h('span', {}, mark(k), `${l} ${d.kinds[k]}`))),
    h('section', { class: 'stack' },
      h('h2', { class: 'h2' }, 'Hot symbols'),
      d.hot.length
        ? h('div', { class: 'hotlist' }, d.hot.map((x) => hotRow(x.name, x.edits, maxHot, () => { location.href = follow(x.id); })))
        : h('span', { class: 'muted small' }, 'No edits in this window.')),
    h('a', { class: 'btn', href: `/journey#${new URLSearchParams({ pkg: d.dir })}` }, 'Open in the journey →'),
    h('section', { class: 'stack' },
      h('h2', { class: 'h2' }, 'Recent semantic changes'),
      d.recent.length ? h('div', { class: 'recent' }, d.recent.map((e) => h('a', { class: 'rc', href: follow(e.id) },
        mark(e.kind),
        h('span', { class: 'rcbody' },
          h('span', {}, h('span', { class: 'mono' }, e.name), h('span', { class: 'muted' }, ` · ${(KIND_LABEL[e.kind] || e.kind).toLowerCase()}`)),
          h('span', { class: 'rcsum mono' }, e.summary),
          h('span', { class: 'muted small' }, `${fmtDate(e.t)} · ${e.sha.slice(0, 7)} · ${e.author}`)))))
        : h('span', { class: 'muted small' }, 'None in this window.')));
}

function hotRow(name, n, max, onclick) {
  return h('button', { type: 'button', class: 'hot', onclick, title: name },
    h('span', { class: 'hn mono' }, name),
    h('span', { class: 'hb' }, h('span', { style: `width:${Math.max(4, (n / max) * 100)}%` })),
    h('span', { class: 'hc' }, n));
}

// ---- page ----

function render() {
  const v = state.view;
  for (const r of document.querySelectorAll('input[name=color]')) r.checked = r.value === v.color;
  for (const r of document.querySelectorAll('input[name=size]')) r.checked = r.value === v.size;
  $('areaNote').textContent = `Area: ${v.size === 'loc' ? 'lines in live symbols'
    : v.color === 'recency' ? 'how recent the last edit is' : v.color === 'complexity' ? 'size of the complexity change' : 'semantic edits'}.`;
  $('tests').checked = v.tests;
  renderWindow();
  renderZoom();
  renderScale();
  renderTreemap();
  renderTimeline();
  renderInspector();
}

async function boot() {
  state.status = await waitReady();
  if (!state.status) return;
  const st = state.status;
  $('repo').textContent = `${st.repo} · ${st.commits.toLocaleString()} commits · ${st.live.toLocaleString()} live symbols`;
  for (const r of document.querySelectorAll('input[name=color]')) r.addEventListener('change', () => go({ color: r.value }));
  for (const r of document.querySelectorAll('input[name=size]')) r.addEventListener('change', () => go({ size: r.value }));
  $('tests').addEventListener('change', (e) => go({ tests: e.target.checked }));
  for (const b of $('presets').querySelectorAll('button')) b.addEventListener('click', () => go({ w: b.dataset.w }));
  $('loading').hidden = true;
  $('app').hidden = false;
  await route();
  const can = state.map.can;
  $('tests').disabled = !can.tests;
  $('testsOpt').classList.toggle('disabled', !can.tests);
  $('testsHint').hidden = can.tests;
  window.addEventListener('hashchange', () => route().catch(fail));
  let raf = 0;
  window.addEventListener('resize', () => {
    cancelAnimationFrame(raf);
    raf = requestAnimationFrame(() => { renderTreemap(); renderTimeline(); });
  });
}

boot().catch(fail);
