'use strict';

// Kinds of change, highest priority first: when several land in one time
// bucket, the first in this list is the one drawn.
const KINDS = [
  ['deleted', 'Deleted'], ['added', 'Added'], ['signature', 'Signature change'],
  ['renamed', 'Renamed'], ['moved', 'Moved'], ['body', 'Body edit'],
];
const RANK = Object.fromEntries(KINDS.map(([k], i) => [k, i]));
const DIAMOND = new Set(['signature', 'renamed']);
const ICON = { type: 'T', func: 'f', method: 'm', var: 'v', const: 'c' };

const state = { status: null, dirs: [], dir: null, file: null, sel: null, syms: [], symbol: null };

// h builds an element; strings become text nodes, never markup.
function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k === 'class') el.className = v;
    else if (k === 'style') el.style.cssText = v;
    else if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else el.setAttribute(k, v === true ? '' : v);
  }
  for (const kid of kids.flat()) {
    if (kid == null || kid === false) continue;
    el.append(kid instanceof Node ? kid : document.createTextNode(String(kid)));
  }
  return el;
}

const $ = (id) => document.getElementById(id);
const mark = (kind) => h('span', { class: `mark m-${kind}${DIAMOND.has(kind) ? ' diamond' : ''}`, 'aria-hidden': 'true' });
const fmtDate = (t) => new Date(t * 1000).toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' });

async function api(path) {
  const r = await fetch(path);
  if (!r.ok) throw new Error(`${path}: ${r.status} ${await r.text()}`);
  return r.json();
}

// ---- navigation (kept in the URL hash so views can be linked) ----

function readHash() {
  const p = new URLSearchParams(location.hash.slice(1));
  return { dir: p.get('d'), file: p.get('f'), sel: p.get('s') ? Number(p.get('s')) : null };
}

function go(next) {
  const v = { dir: state.dir, file: state.file, sel: state.sel, ...next };
  const p = new URLSearchParams();
  if (v.dir) p.set('d', v.dir);
  if (v.file) p.set('f', v.file);
  if (v.sel) p.set('s', v.sel);
  location.hash = p.toString();
}

async function route() {
  const want = readHash();
  if (!want.dir || !state.dirs.some((d) => d.dir === want.dir)) {
    // Default to the directory with the most history.
    const busiest = state.dirs.reduce((a, b) => (b.changes > a.changes ? b : a), state.dirs[0]);
    if (!busiest) return;
    go({ dir: busiest.dir, file: null, sel: null });
    return;
  }
  if (want.dir !== state.dir) {
    state.dir = want.dir;
    state.syms = (await api(`/api/dir?d=${encodeURIComponent(want.dir)}`)).symbols;
  }
  state.file = want.file;
  if (want.sel !== state.sel) {
    state.sel = want.sel;
    state.symbol = want.sel ? await api(`/api/symbol/${want.sel}`) : null;
  }
  render();
}

// ---- outline ----

// rows orders the directory's symbols as the outline shows them: per file,
// types with their methods nested, then funcs, then values; deleted symbols
// last. Lanes follow the same order.
function rows() {
  const live = state.syms.filter((s) => s.alive);
  const files = new Map();
  for (const s of live) {
    if (!files.has(s.file)) files.set(s.file, []);
    files.get(s.file).push(s);
  }
  const out = [];
  for (const [file, syms] of [...files].sort((a, b) => a[0].localeCompare(b[0]))) {
    out.push({ file, depth: 0 });
    const types = syms.filter((s) => s.kind === 'type');
    const typeNames = new Set(types.map((t) => t.name));
    const methods = syms.filter((s) => s.kind === 'method');
    for (const t of types) {
      out.push({ sym: t, file, depth: 1 });
      for (const m of methods.filter((m) => m.recv === t.name)) out.push({ sym: m, file, depth: 2 });
    }
    const rest = syms.filter((s) => s.kind !== 'type' && !(s.kind === 'method' && typeNames.has(s.recv)));
    const order = { method: 0, func: 1, var: 2, const: 3 };
    rest.sort((a, b) => order[a.kind] - order[b.kind] || a.line - b.line);
    for (const s of rest) out.push({ sym: s, file, depth: 1 });
  }
  for (const s of state.syms.filter((s) => !s.alive)) out.push({ sym: s, file: null, depth: 1, gone: true });
  return out;
}

// shortName drops the receiver of a method nested under its own type.
const shortName = (r) => (r.depth === 2 ? r.sym.name.replace(/^.*\./, '') : r.sym.name);

function renderTree(all) {
  const tree = $('tree');
  tree.replaceChildren();
  const deleted = all.filter((r) => r.gone);
  const node = (r) => {
    if (!r.sym) {
      const open = state.file === r.file;
      return h('button', {
        type: 'button', role: 'treeitem', class: 'node', 'aria-selected': String(open && !state.sel),
        style: 'padding-left: 8px', onclick: () => go({ file: open ? null : r.file, sel: null }),
      }, h('span', { class: 'ico k-file' }, open ? '▾' : '▸'), h('span', { class: 'lbl' }, r.file));
    }
    const s = r.sym;
    return h('button', {
      type: 'button', role: 'treeitem', class: `node${r.gone ? ' gone' : ''}`,
      'aria-selected': String(state.sel === s.id), style: `padding-left: ${8 + r.depth * 14}px`,
      onclick: () => go({ sel: s.id }),
    }, h('span', { class: `ico k-${s.kind}` }, ICON[s.kind] || '?'),
      h('span', { class: 'lbl' }, shortName(r)),
      h('span', { class: 'n' }, s.changes));
  };
  tree.append(h('button', {
    type: 'button', role: 'treeitem', class: 'node', 'aria-selected': String(!state.file && !state.sel),
    onclick: () => go({ file: null, sel: null }),
  }, h('span', { class: 'ico k-file' }, '▾'), h('span', { class: 'lbl' }, state.dir)));
  for (const r of all) if (!r.gone) tree.append(node(r));
  if (deleted.length) {
    tree.append(h('details', {}, h('summary', { class: 'node muted' }, `Deleted (${deleted.length})`), deleted.map(node)));
  }
}

// ---- timeline ----

function axis(shown) {
  let from = Infinity;
  for (const s of shown) from = Math.min(from, s.marks[0].t);
  let to = state.status.to;
  if (!isFinite(from)) from = state.status.from;
  if (to <= from) to = from + 86400;
  const weeks = Math.ceil((to - from) / (7 * 86400));
  const n = Math.max(12, Math.min(52, weeks));
  const bucket = (t) => Math.min(n - 1, Math.max(0, Math.floor(((t - from) / (to - from)) * n)));
  return { from, to, n, bucket };
}

function renderLanes(all) {
  const lanes = $('lanes');
  lanes.replaceChildren();
  const shown = all.filter((r) => r.sym && (!state.file || r.file === state.file));
  if (!shown.length) {
    lanes.append(h('div', { class: 'empty' }, 'No symbols here.'));
    return null;
  }
  const ax = axis(shown.map((r) => r.sym));
  const long = ax.to - ax.from > 400 * 86400;
  const ticks = h('div', { class: 'ticks' });
  for (let i = 0; i <= 5; i++) {
    const t = ax.from + ((ax.to - ax.from) * i) / 5;
    const label = new Date(t * 1000).toLocaleDateString(undefined, long ? { month: 'short', year: 'numeric' } : { month: 'short', day: 'numeric' });
    ticks.append(h('span', { style: `left: ${Math.min(96, Math.max(4, i * 20))}%` }, label));
  }
  lanes.append(h('div', { class: 'axis' }, h('span', {}, 'declaration'), ticks));

  const cols = `grid-template-columns: repeat(${ax.n}, minmax(0, 1fr))`;
  for (const r of shown) {
    const s = r.sym;
    const cells = new Array(ax.n).fill(null);
    for (const m of s.marks) {
      const b = ax.bucket(m.t);
      if (cells[b] == null || RANK[m.k] < RANK[cells[b]]) cells[b] = m.k;
    }
    const first = ax.bucket(s.marks[0].t);
    const last = s.alive ? ax.n - 1 : ax.bucket(s.marks[s.marks.length - 1].t);
    const track = h('div', { class: 'track', style: cols },
      h('div', { class: 'life', style: `left: ${(first / ax.n) * 100}%; width: ${((last - first + 1) / ax.n) * 100}%` }),
      cells.map((k) => (k ? mark(k) : h('span'))));
    lanes.append(h('button', {
      type: 'button', class: `lane${r.gone ? ' gone' : ''}`, 'aria-pressed': String(state.sel === s.id),
      'aria-label': `${s.name}, ${s.changes} changes`, onclick: () => go({ sel: s.id }),
    }, h('span', { class: 'name', style: `padding-left: ${(r.depth - 1) * 14}px` }, shortName(r)), track));
  }
  return ax;
}

function renderSize(ax) {
  const panel = $('sizePanel');
  const sym = state.symbol;
  if (!sym || !ax || !sym.events.some((e) => e.stmts || e.complexity)) {
    panel.hidden = true;
    return;
  }
  panel.hidden = false;
  $('sizeTitle').textContent = `${sym.name} — body size & complexity`;
  // Value at the end of each bucket: the state after the last event in or
  // before it.
  const stmts = new Array(ax.n).fill(null), cc = new Array(ax.n).fill(null);
  let i = 0, cur = null;
  for (let b = 0; b < ax.n; b++) {
    while (i < sym.events.length && ax.bucket(sym.events[i].t) <= b) cur = sym.events[i++];
    if (cur && cur.kind !== 'deleted') [stmts[b], cc[b]] = [cur.stmts, cur.complexity];
  }
  const W = ax.n * 10, H = 72;
  const maxS = Math.max(1, ...stmts), maxC = Math.max(1, ...cc);
  const ns = 'http://www.w3.org/2000/svg';
  const svg = document.createElementNS(ns, 'svg');
  svg.setAttribute('viewBox', `0 0 ${W} ${H}`);
  svg.setAttribute('preserveAspectRatio', 'none');
  svg.setAttribute('role', 'img');
  svg.setAttribute('aria-label', `Statements peak at ${maxS}, cyclomatic complexity at ${maxC}`);
  stmts.forEach((v, b) => {
    if (v == null) return;
    const r = document.createElementNS(ns, 'rect');
    const bh = Math.max(1, (v / maxS) * (H - 8));
    Object.entries({ x: b * 10 + 1.5, y: H - bh, width: 7, height: bh, fill: 'var(--bar)' }).forEach(([k, val]) => r.setAttribute(k, val));
    svg.append(r);
  });
  const pts = cc.map((v, b) => (v == null ? null : `${b * 10 + 5},${H - 4 - (v / maxC) * (H - 12)}`)).filter(Boolean);
  if (pts.length > 1) {
    const pl = document.createElementNS(ns, 'polyline');
    Object.entries({ points: pts.join(' '), fill: 'none', stroke: 'var(--accent)', 'stroke-width': 2, 'vector-effect': 'non-scaling-stroke' })
      .forEach(([k, val]) => pl.setAttribute(k, val));
    svg.append(pl);
  }
  $('sizeChart').replaceChildren(svg);
}

// ---- detail ----

function renderDetail() {
  const d = $('detail');
  const s = state.symbol;
  if (!s) {
    d.replaceChildren(h('p', { class: 'muted' }, 'Select a symbol to see its lineage.'));
    return;
  }
  const label = Object.fromEntries(KINDS);
  d.replaceChildren(
    h('div', { class: 'stack' },
      h('span', { class: 'eyebrow' }, s.alive ? 'Selected symbol' : 'Selected symbol · deleted'),
      h('span', { class: 'sel-name' }, s.name),
      h('code', { class: 'sig' }, s.signature),
      h('span', { class: 'muted small' }, `${s.path}:${s.start}–${s.end} · ${s.authors} author${s.authors === 1 ? '' : 's'} · ${s.events.length} change${s.events.length === 1 ? '' : 's'}`)),
    h('section', { class: 'lineage' },
      h('h2', { class: 'h2', style: 'margin-bottom: 12px' }, 'Lineage'),
      [...s.events].reverse().map((e) => h('div', { class: 'ev' },
        h('div', { class: 'rail' }, mark(e.kind), h('span', { class: 'bar' })),
        h('div', { class: 'body' },
          h('span', { class: 'meta' }, `${label[e.kind] || e.kind} · ${fmtDate(e.t)} · `, h('span', { class: 'mono' }, e.sha.slice(0, 7)), ` · ${e.author}`),
          h('span', { class: 'text' }, e.summary))))));
}

// ---- page ----

function render() {
  $('crumbs').replaceChildren(
    h('button', { type: 'button', onclick: () => go({ file: null, sel: null }) }, state.status.repo), h('span', {}, '›'),
    state.file
      ? [h('button', { type: 'button', onclick: () => go({ file: null, sel: null }) }, state.dir), h('span', {}, '›'), h('span', { class: 'here' }, state.file)]
      : h('span', { class: 'here' }, state.dir));
  $('dirInput').value = state.dir;
  const all = rows();
  renderTree(all);
  const ax = renderLanes(all);
  renderSize(ax);
  renderDetail();
}

async function boot() {
  $('legend').append(...KINDS.map(([k, l]) => h('span', {}, mark(k), l)));
  for (;;) {
    state.status = await api('/api/status');
    if (state.status.error) {
      $('loading').textContent = `Indexing failed: ${state.status.error}`;
      return;
    }
    if (state.status.ready) break;
    $('loading').textContent = `Indexing history… ${state.status.commits.toLocaleString()} commits`;
    await new Promise((r) => setTimeout(r, 500));
  }
  const st = state.status;
  $('repo').textContent = `${st.repo} · ${st.commits.toLocaleString()} commits · ${st.live.toLocaleString()} live symbols`;
  state.dirs = await api('/api/dirs');
  $('dirList').replaceChildren(...state.dirs.map((d) => h('option', { value: d.dir }, `${d.live} live · ${d.changes} changes`)));
  $('dirInput').addEventListener('change', (e) => {
    if (state.dirs.some((d) => d.dir === e.target.value)) go({ dir: e.target.value, file: null, sel: null });
  });
  $('loading').hidden = true;
  $('app').hidden = false;
  window.addEventListener('hashchange', () => route().catch(fail));
  await route();
}

function fail(err) {
  $('loading').hidden = false;
  $('loading').textContent = String(err);
}

boot().catch(fail);
