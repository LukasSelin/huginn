'use strict';

// The review page: what a merge request changes since its merge base with
// the branch it goes into, the code that depends on what it changed, and the
// imports and references between packages it starts and stops, optionally
// limited to folders. What is reviewed lives in the URL, so a review can be
// linked to.

const state = {
  rv: null,          // the review on show
  model: null,       // its relations graph
  focus: null,       // the package picked in the graph, filtering the lists
  folders: [],       // the scope being edited
  kinds: new Set(KINDS.map(([k]) => k)),
  outsideOnly: false,
  showAll: new Set(), // lists expanded past their first rows
  ctrl: null,        // aborts the review being fetched
};

const LIST_ROWS = 40;
const shortHash = (s) => s.slice(0, 7);
const sum = (xs, f) => xs.reduce((n, x) => n + f(x), 0);

// short names a package within the module: "internal/api/handler".
function short(pkg) {
  const m = state.rv && state.rv.module;
  if (m && pkg === m) return m.split('/').pop();
  if (m && pkg.startsWith(m + '/')) return pkg.slice(m.length + 1);
  return pkg;
}
const pkgCode = (pkg) => h('code', { title: pkg }, short(pkg));

// --- What to review ---------------------------------------------------------

function readURL() {
  const q = new URLSearchParams(location.search);
  $('head').value = q.get('head') || '';
  $('base').value = q.get('base') || '';
  state.folders = q.getAll('path').filter(Boolean);
  renderFolders();
}

function query() {
  const q = new URLSearchParams();
  const head = $('head').value.trim(), base = $('base').value.trim();
  if (head) q.set('head', head);
  if (base) q.set('base', base);
  for (const f of state.folders) q.append('path', f);
  return q;
}

function renderFolders() {
  const chips = state.folders.map((f, i) => h('span', { class: 'folder' }, f,
    h('button', {
      type: 'button', 'aria-label': `Remove ${f}`,
      onclick: () => { state.folders.splice(i, 1); renderFolders(); $('folder').focus(); },
    }, '×')));
  // The input stays where it is: moving it would take away its focus.
  for (const c of $('folders').querySelectorAll('.folder')) c.remove();
  $('folder').before(...chips);
}

// addFolder moves what is typed in the folder field into the scope.
function addFolder() {
  const v = $('folder').value.trim().replace(/\\/g, '/').replace(/^\.\/+/, '').replace(/\/+$/, '');
  $('folder').value = '';
  if (v && v !== '.' && !state.folders.includes(v)) state.folders.push(v);
  renderFolders();
}

async function loadOptions(head) {
  try {
    const o = await api('/api/review/options' + (head ? `?head=${encodeURIComponent(head)}` : ''));
    $('head').placeholder = o.head;
    $('base').placeholder = o.target || 'default branch';
    $('branches').replaceChildren(...o.branches.map((b) => h('option', { value: b })));
    $('dirs').replaceChildren(...o.dirs.filter((d) => d !== '.').map((d) => h('option', { value: d })));
  } catch (e) {
    // The pickers only help; a review still works without them.
  }
}

async function run() {
  if (state.ctrl) state.ctrl.abort();
  const ctrl = state.ctrl = new AbortController();
  const loading = $('loading');
  loading.hidden = false;
  loading.className = 'loading';
  loading.textContent = 'Reviewing…';
  try {
    const r = await fetch('/api/review?' + query(), { signal: ctrl.signal });
    if (!r.ok) throw new Error((await r.text()).trim() || `${r.status}`);
    const rv = await r.json();
    if (ctrl !== state.ctrl) return;
    Object.assign(state, { rv, focus: null, showAll: new Set() });
    loading.hidden = true;
    $('out').hidden = false;
    render();
  } catch (e) {
    if (e.name === 'AbortError') return;
    $('out').hidden = true;
    loading.className = 'loading error';
    loading.textContent = String(e.message || e);
  }
}

// --- The report -------------------------------------------------------------

function render() {
  const rv = state.rv;
  const where = rv.scope.length ? rv.scope.join(', ') : 'whole repository';
  $('what').textContent = `${rv.headName} into ${rv.target} · merge base ${shortHash(rv.mergeBase)} · ${plural(rv.commits, 'commit')} · ${where}`;
  $('headline').textContent = rv.headline;
  $('kinds').replaceChildren(...KINDS.filter(([k]) => rv.counts[k]).map(([k, label]) => h('span', {}, mark(k), `${label} ${rv.counts[k]}`)));
  const api = rv.blast.apiChanges;
  $('api').hidden = api.length === 0;
  $('api').replaceChildren('Exported API changed: ', ...api.flatMap((n, i) => [i ? ', ' : '', h('code', {}, n)]));
  $('warnings').hidden = !(rv.warnings && rv.warnings.length);
  $('warnings').replaceChildren(...(rv.warnings || []).map((w) => h('li', {}, w)));
  tiles();
  legend();
  drawGraph();
  seams();
  lists();
}

function lists() {
  focusBar();
  impact();
  relations();
  changes();
  files();
}

function outsideCallers() {
  const out = state.rv.blast.callers.filter((c) => c.outside);
  return { sites: sum(out, (c) => c.calls), decls: out.length, pkgs: new Set(out.map((c) => c.package)).size };
}

function tiles() {
  const rv = state.rv, b = rv.blast, R = rv.relations;
  const t = [{ n: b.callSites, label: 'call sites reached', sub: `${plural(b.callers.length, 'declaration')} · ${plural(b.packages.length, 'package')}${b.approximate ? ' · approx.' : ''}` }];
  if (rv.scope.length) {
    const o = outsideCallers();
    t.push({ cls: 't-out', n: o.sites, label: 'outside the scope', sub: `${plural(o.decls, 'declaration')} · ${plural(o.pkgs, 'package')}` });
  }
  t.push(
    { cls: 't-added', n: R.addedDeps.length, label: 'new imports', sub: `${R.removedDeps.length} removed` },
    { cls: 't-added', n: R.addedRefs.length, label: 'new references', sub: `${R.removedRefs.length} removed` },
    { cls: b.apiChanges.length ? 't-api' : '', n: b.apiChanges.length, label: 'exported API changes', sub: 'that can break callers' },
  );
  $('tiles').replaceChildren(...t.map((x) => h('div', { class: `tile ${x.cls || ''}` },
    h('strong', {}, x.n.toLocaleString()), h('span', { class: 'label' }, x.label), h('span', { class: 'sub' }, x.sub))));
}

function legend() {
  const line = (cls, color, text) => h('span', {}, h('i', { class: `line ${cls}`, style: `border-color: var(${color})` }), text);
  const box = (color, text, dash) => h('span', {}, h('i', { class: 'box', style: `border-color: var(${color})${dash ? '; border-style: dashed' : ''}` }), text);
  $('legend').replaceChildren(
    box('--accent', state.rv.scope.length ? 'in the scope' : 'changed'),
    box('--added', 'new package', true),
    line('', '--muted', 'calls what changed'),
    line('', '--added', 'new import'),
    line('dash', '--deleted', 'removed import'),
    line('', '--body', 'new reference, existing import'),
    line('dash', '--deleted', 'removed reference'));
}

// --- The relations graph ----------------------------------------------------

// graphModel places every package the change reaches in one of three
// columns: what is changed (or in the scope) in the middle, the packages
// that use it on the left, and those it uses on the right. Edges are the
// imports and references started and stopped, and the calls into what
// changed, tests left out.
function graphModel(rv) {
  const R = rv.relations, scoped = rv.scope.length > 0;
  const center = new Set(rv.changes.map((c) => c.package));
  if (scoped) {
    for (const c of rv.blast.callers) if (!c.outside && !c.test) center.add(c.package);
    const ends = (crossing, from, to) => {
      if (crossing !== 'inward') center.add(from);
      if (crossing !== 'outward') center.add(to);
    };
    for (const d of [...R.addedDeps, ...R.removedDeps]) ends(d.crossing, d.from, d.to);
    for (const r of [...R.addedRefs, ...R.removedRefs]) ends(r.crossing, r.fromPackage, r.toPackage);
    for (const p of R.newPackages) center.add(p);
  }

  const edges = new Map();
  const edge = (kind, from, to) => {
    const key = `${kind}|${from}|${to}`;
    let e = edges.get(key);
    if (!e) edges.set(key, e = { kind, from, to, weight: 0, lines: [], files: [] });
    return e;
  };
  const along = (refs, deps, kind, refKind) => {
    const isDep = new Set(deps.map((d) => `${d.from}|${d.to}`));
    for (const d of deps) edge(kind, d.from, d.to).files = d.files;
    for (const r of refs) {
      const e = edge(isDep.has(`${r.fromPackage}|${r.toPackage}`) ? kind : refKind, r.fromPackage, r.toPackage);
      e.weight++;
      e.lines.push(`${r.from} → ${r.to} ×${r.uses}${r.approximate ? ' (approx.)' : ''}`);
    }
  };
  along(R.addedRefs, R.addedDeps, 'added', 'ref-added');
  along(R.removedRefs, R.removedDeps, 'removed', 'ref-removed');
  for (const ch of rv.changes) {
    if (ch.kind === 'added') continue; // its callers are new code of the same change
    for (const cl of ch.callers) {
      if (cl.test || cl.package === ch.package) continue;
      const e = edge('impact', cl.package, ch.package);
      e.weight += cl.calls;
      e.lines.push(`${cl.name} → ${ch.name} ×${cl.calls}${cl.approximate ? ' (approx.)' : ''}`);
    }
  }

  const nodes = new Map();
  const node = (col, pkg) => {
    const id = `${col}:${pkg}`;
    let n = nodes.get(id);
    if (!n) nodes.set(id, n = { id, col, pkg, links: [] });
    return n;
  };
  for (const p of center) node('C', p);
  const list = [...edges.values()];
  for (const e of list) {
    const fc = center.has(e.from), tc = center.has(e.to);
    e.a = node(fc ? 'C' : 'L', e.from);
    e.b = node(tc ? 'C' : 'R', e.to);
    e.a.links.push(e);
    e.b.links.push(e);
  }
  // Edges between the same two packages fan out, so none hides another.
  const pairs = new Map();
  for (const e of list) {
    const key = `${e.a.id}|${e.b.id}`;
    pairs.set(key, [...(pairs.get(key) || []), e]);
  }
  for (const es of pairs.values()) es.forEach((e, i) => { e.offset = (i - (es.length - 1) / 2) * 7; });

  const changed = new Map();
  for (const c of rv.changes) changed.set(c.package, (changed.get(c.package) || 0) + 1);
  const isNew = new Set(R.newPackages), gone = new Set(R.gonePackages);
  for (const n of nodes.values()) {
    if (n.col === 'C') {
      n.badge = changed.get(n.pkg) ? plural(changed.get(n.pkg), 'change') : '';
    } else {
      // Calls into what changed, and references started or stopped.
      const calls = sum(n.links.filter((e) => e.kind === 'impact'), (e) => e.weight);
      const refs = sum(n.links.filter((e) => e.kind !== 'impact'), (e) => e.weight);
      const imports = n.links.filter((e) => (e.kind === 'added' || e.kind === 'removed') && !e.weight).length;
      n.badge = [calls ? `×${calls}` : '', refs ? plural(refs, 'ref') : '', imports && !refs ? 'import' : ''].filter(Boolean).join(' · ');
    }
    n.cls = [n.col === 'C' ? 'center' : '', isNew.has(n.pkg) ? 'new' : '', gone.has(n.pkg) ? 'gone' : ''].join(' ');
  }
  return { nodes: [...nodes.values()], edges: list };
}

// layout stacks each column, centered on the tallest, and orders the side
// columns by the middle packages they connect to, so lines cross less.
function layout(model, W) {
  const cols = { L: [], C: [], R: [] };
  for (const n of model.nodes) cols[n.col].push(n);
  cols.C.sort((a, b) => a.pkg.localeCompare(b.pkg));
  cols.C.forEach((n, i) => { n.i = i; });
  const bary = (n) => {
    const is = n.links.map((e) => (e.a === n ? e.b : e.a)).filter((m) => m.col === 'C').map((m) => m.i);
    return is.length ? sum(is, (i) => i) / is.length : Infinity;
  };
  for (const c of ['L', 'R']) cols[c].sort((a, b) => bary(a) - bary(b) || a.pkg.localeCompare(b.pkg));

  const NH = 30, GAP = 10, TOP = 30;
  const nodeW = Math.min(300, Math.max(170, (W - 180) / 3));
  const x = { L: 0, C: (W - nodeW) / 2, R: W - nodeW };
  const rows = Math.max(1, cols.L.length, cols.C.length, cols.R.length);
  for (const [c, ns] of Object.entries(cols)) {
    const top = TOP + ((rows - ns.length) * (NH + GAP)) / 2;
    ns.forEach((n, i) => Object.assign(n, { x: x[c], y: top + i * (NH + GAP), w: nodeW, h: NH }));
  }
  return { cols, x, nodeW, H: TOP + rows * (NH + GAP) + 6 };
}

function edgePath(e) {
  const { a, b } = e;
  const ay = a.y + a.h / 2 + e.offset, by = b.y + b.h / 2 + e.offset;
  if (a.col === b.col) {
    // Within the middle column: loop out to its left.
    const bulge = 36 + Math.abs(by - ay) * 0.3;
    return `M${a.x},${ay} C${a.x - bulge},${ay} ${b.x - bulge},${by} ${b.x - 2},${by}`;
  }
  const x0 = a.x + a.w, x1 = b.x - 2, mx = (x0 + x1) / 2;
  return `M${x0},${ay} C${mx},${ay} ${mx},${by} ${x1},${by}`;
}

const EDGE_LABEL = {
  added: '+ new import', removed: '− removed import', 'ref-added': '+ new references',
  'ref-removed': '− removed references', impact: 'calls what changed',
};
const EDGE_COLOR = { added: '--added', removed: '--deleted', 'ref-added': '--body', 'ref-removed': '--deleted', impact: '--muted' };

function edgeWidth(e) {
  const w = Math.log2(1 + Math.max(1, e.weight));
  return e.kind === 'impact' ? 1 + Math.min(5, w) : 1.5 + Math.min(3.5, w);
}

function drawGraph() {
  const box = $('graphBox');
  const model = state.model = graphModel(state.rv);
  if (model.edges.length === 0) {
    box.replaceChildren(h('p', { class: 'empty muted' },
      model.nodes.length ? 'Nothing outside the changed packages calls what changed, and no imports or references between packages started or stopped.' : 'No Go code changed.'));
    return;
  }
  const W = Math.max(box.clientWidth, 680);
  const L = layout(model, W);
  const el = svg('svg', { width: W, height: L.H, viewBox: `0 0 ${W} ${L.H}`, role: 'img', 'aria-label': 'Packages the change reaches and the relations between them' });

  const defs = svg('defs', {});
  for (const [kind, color] of Object.entries(EDGE_COLOR)) {
    defs.append(svg('marker', { id: `arr-${kind}`, viewBox: '0 0 10 10', refX: 9, refY: 5, markerWidth: 8, markerHeight: 8, markerUnits: 'userSpaceOnUse', orient: 'auto' },
      svg('path', { d: 'M0,1 L10,5 L0,9 z', style: `fill: var(${color})` })));
  }
  el.append(defs);

  const scoped = state.rv.scope.length > 0;
  const heads = scoped
    ? { L: 'Outside · uses it', C: 'In the scope', R: 'Outside · it uses' }
    : { L: 'Callers', C: 'Changed', R: 'Used by the change' };
  for (const c of ['L', 'C', 'R']) {
    if (L.cols[c].length) el.append(svg('text', { class: 'g-col', x: L.x[c] + L.nodeW / 2, y: 14, 'text-anchor': 'middle' }, heads[c]));
  }

  const edgeEls = new Map();
  // Impact first, so the relations the change starts and stops draw on top.
  const order = { impact: 0, 'ref-removed': 1, 'ref-added': 2, removed: 3, added: 4 };
  for (const e of [...model.edges].sort((p, q) => order[p.kind] - order[q.kind])) {
    const d = edgePath(e);
    const line = svg('path', { d, class: `g-edge ${e.kind}`, 'stroke-width': edgeWidth(e).toFixed(1), 'marker-end': `url(#arr-${e.kind})` });
    const tip = [`${EDGE_LABEL[e.kind]}: ${short(e.from)} → ${short(e.to)}`, ...e.lines.slice(0, 12)];
    if (e.lines.length > 12) tip.push(`… and ${e.lines.length - 12} more`);
    if (!e.lines.length && e.files.length) tip.push(`imported in ${e.files.join(', ')}`);
    const hit = svg('path', { d, class: 'g-edge-hit' }, svg('title', {}, tip.join('\n')));
    el.append(line, hit);
    edgeEls.set(e, line);
  }

  const nodeEls = new Map();
  for (const n of model.nodes) {
    const badgeW = n.badge ? n.badge.length * 6.6 + 10 : 0;
    const fit = Math.max(6, Math.floor((n.w - 18 - badgeW) / 7.3));
    let label = short(n.pkg);
    if (label.length > fit) label = '…' + label.slice(label.length - fit + 1);
    const g = svg('g', { class: `g-node ${n.cls}${state.focus === n.pkg ? ' picked' : ''}`, transform: `translate(${n.x},${n.y})`, tabindex: 0, role: 'button', 'aria-label': `${n.pkg}, ${n.badge}` },
      svg('title', {}, n.pkg),
      svg('rect', { width: n.w, height: n.h, rx: 6 }),
      svg('text', { x: 9, y: n.h / 2 + 4 }, label));
    if (n.badge) g.append(svg('text', { class: 'badge', x: n.w - 8, y: n.h / 2 + 4, 'text-anchor': 'end' }, n.badge));
    g.addEventListener('mouseenter', () => trace(el, n, edgeEls, nodeEls));
    g.addEventListener('mouseleave', () => untrace(el));
    g.addEventListener('focus', () => trace(el, n, edgeEls, nodeEls));
    g.addEventListener('blur', () => untrace(el));
    g.addEventListener('click', () => pick(n.pkg));
    g.addEventListener('keydown', (ev) => { if (ev.key === 'Enter' || ev.key === ' ') { ev.preventDefault(); pick(n.pkg); } });
    el.append(g);
    nodeEls.set(n, g);
  }
  box.replaceChildren(el);
}

function trace(el, n, edgeEls, nodeEls) {
  el.classList.add('tracing');
  nodeEls.get(n).classList.add('hot');
  for (const e of n.links) {
    edgeEls.get(e).classList.add('hot');
    nodeEls.get(e.a === n ? e.b : e.a).classList.add('hot');
  }
}

function untrace(el) {
  el.classList.remove('tracing');
  for (const x of el.querySelectorAll('.hot')) x.classList.remove('hot');
}

function pick(pkg) {
  state.focus = state.focus === pkg ? null : pkg;
  for (const g of $('graphBox').querySelectorAll('.g-node')) {
    g.classList.toggle('picked', g.getAttribute('aria-label').startsWith(`${state.focus}, `));
  }
  lists();
}

function focusBar() {
  const bar = $('focus');
  bar.hidden = !state.focus;
  if (state.focus) {
    bar.replaceChildren('Showing what involves ', pkgCode(state.focus),
      h('button', { type: 'button', onclick: () => pick(state.focus) }, 'Show everything'));
  }
}

// --- The affinity matrix of each type it touches ------------------------------
//
// Each struct type the change touches is read as the affinity matrix
// technique reads a God object: a row per method and a column per field,
// marked R and W; the columns classified; hubs set aside; rows and columns
// reordered into blocks; the five patterns read off; the blocks checked
// against the callers, the history and the names; and the block to extract
// first named. The change's own effect on all that comes first.

const list = (xs) => xs || [];

// COLUMN_TYPES are the technique's column types and where each ends up.
const COLUMN_TYPES = [
  ['entity', 'Entity state', 'per instance, written by methods', 'stays with the core entity'],
  ['policy', 'Policy / config', 'only read; the same for every instance', 'a stateless policy or service'],
  ['collaborator', 'Collaborator', 'an I/O handle or service', 'the adapter that uses it'],
  ['infrastructure', 'Infrastructure', 'logging, metrics', 'ignored: each new type gets its own'],
  ['temporary', 'Temporary', 'used only during one operation', 'a method object for that operation'],
  ['unused', 'Unused', 'no method uses it', 'drop it, or find who does'],
];
const COLUMN_SHORT = { entity: 'state', policy: 'policy', collaborator: 'collab', infrastructure: 'infra', temporary: 'temp', unused: '–' };
const BLOCK_KIND = {
  entity: 'entity state: stays with the core entity',
  adapter: 'collaborators: the adapter that uses them',
  policy: 'policy: a stateless policy or service',
  'method object': 'temporaries: a method object for one operation',
  core: 'the hubs, and the rows that use only them',
};
const ROW_KIND = {
  core: ['core', 'uses only the hubs'],
  bridge: ['bridge row', 'marks no field, only calls other methods: orchestration for a thin coordinator'],
  free: ['free function', 'marks no field and calls nothing: the safest extraction'],
  tie: ['spans blocks', 'marks the fields of several blocks: split it with Extract Method first'],
};
const blockIndex = (r, label) => r.blocks.findIndex((b) => b.label === label);
const blockClass = (r, label) => (label === 'core' ? 'hub' : label ? `g${blockIndex(r, label) % 8}` : 'loose');
const realBlocks = (r) => list(r.blocks).filter((b) => b.label !== 'core').length;
// inModule writes the import paths in a text relative to the module.
const inModule = (text) => (state.rv.module ? text.split(state.rv.module + '/').join('') : text);

function seams() {
  const ts = list(state.rv.types);
  $('typesPanel').hidden = ts.length === 0;
  $('types').replaceChildren(...ts.map(typeCard));
}

function typeCard(t) {
  const r = t.after || t.before;
  const warns = t.notes.filter((n) => n.warn).length;
  const blocks = t.before && t.after ? `blocks ${realBlocks(t.before)} → ${realBlocks(t.after)}` : plural(realBlocks(r), 'block');
  const card = h('details', { class: 'type', open: warns > 0 || null },
    h('summary', {},
      h('code', { class: 'tname', title: t.package }, t.name),
      h('span', { class: 'muted code' }, `${t.path}:${t.line}`),
      h('span', { class: 'muted small' }, `${plural(r.methods.length, 'method')} · ${plural(r.fields.length, 'field')} · ${blocks} · LCOM4 ${r.lcom4}, ${r.lcom4Split} without hubs and bridges`),
      !t.before ? h('span', { class: 'tag' }, 'new') : null,
      !t.after ? h('span', { class: 'tag' }, 'removed') : null,
      warns ? h('span', { class: 'tag warn' }, `${warns} to look at`) : null));
  const body = h('div', { class: 'type-body' });
  body.append(section('What the change does', h('ul', { class: 'notes' }, t.notes.length
    ? t.notes.map((n) => h('li', { class: n.warn ? 'warn-note' : '' }, n.text))
    : h('li', { class: 'muted' }, 'It leaves the blocks as they were.'))));

  const view = h('div', { class: 'type-view' });
  const show = (which) => view.replaceChildren(...typeView(t, which === 'before' ? t.before : t.after, which));
  if (t.before && t.after) {
    const seg = h('div', { class: 'seg', role: 'group', 'aria-label': 'Show the type' });
    for (const which of ['after', 'before']) {
      seg.append(h('button', {
        type: 'button', 'aria-pressed': String(which === 'after'),
        onclick: (ev) => {
          for (const b of seg.children) b.setAttribute('aria-pressed', String(b === ev.currentTarget));
          show(which);
        },
      }, which === 'after' ? 'After the change' : 'Before'));
    }
    body.append(seg);
  }
  body.append(view);
  show(t.after ? 'after' : 'before');
  card.append(body);
  return card;
}

const section = (title, ...kids) => h('div', { class: 'facts' }, h('p', { class: 'sub-h' }, title), ...kids);

// typeView reads one side of a type, in the order of a working session.
function typeView(t, r, which) {
  const out = [];
  const advice = list(r.advice);
  if (r.start || advice.length) {
    const b = r.start && r.blocks[blockIndex(r, r.start.block)];
    out.push(section('Where to start', h('ul', {},
      b ? h('li', { class: 'start' }, 'Extract block ', h('span', { class: `glabel ${blockClass(r, b.label)}` }, b.label),
        ` (${b.fields.join(', ')}) first: ${r.start.reason}`) : null,
      advice.map((a) => h('li', {}, a)))));
  }
  out.push(section('Affinity matrix', h('p', { class: 'muted small' },
    'Rows are methods and columns fields, R for read and W for write, reordered so each block sits on the diagonal. Hubs, used by most rows, and infrastructure are set aside on the right.'),
  matrix(r, new Set(which === 'after' ? t.changed : []))));
  out.push(columnTypes(r), blocksView(r), patterns(r));
  if (which === 'after') out.push(clientsView(r), namesView(r), couplingView(r));
  return out;
}

// matrix draws the affinity matrix: a band of block labels and one of
// column types over the field names, then a row per method.
function matrix(r, changed) {
  const fieldClass = (f) => (f.role === 'infrastructure' ? 'infra' : blockClass(r, f.block));
  const head = h('tr', {}, h('th', { class: 'corner' }, 'method \\ field'),
    r.fields.map((f) => h('th', {
      class: `fcol ${fieldClass(f)}`,
      title: `${f.name} ${f.type}\n${COLUMN_TYPES.find(([k]) => k === f.role)[1]}${f.hub ? ', hub' : ''}${f.operation ? ` of ${f.operation}` : ''}\nread by ${f.readers}, written by ${f.writers}`,
    }, h('span', {}, f.name))),
    h('th', { class: 'calls' }, 'calls'));
  const band = (cells) => h('tr', { class: 'bands' }, h('th', {}), cells, h('th', {}));
  const blocksBand = band(r.fields.map((f) => h('th', { class: fieldClass(f) }, f.hub ? 'hub' : f.role === 'infrastructure' ? '' : f.block || '·')));
  const typesBand = band(r.fields.map((f) => h('th', { class: `ctype ${fieldClass(f)}` }, h('span', {}, COLUMN_SHORT[f.role]))));
  const rows = r.methods.map((m) => {
    const uses = new Map(list(m.uses).map((u) => [u.field, u]));
    const kind = ROW_KIND[m.kind];
    return h('tr', { class: changed.has(m.name) ? 'changed' : '' },
      h('th', { class: 'mname' },
        h('code', {}, m.name),
        changed.has(m.name) ? h('span', { class: 'tag out', title: 'changed by the merge request' }, 'changed') : null,
        kind ? h('span', { class: `tag k-${m.kind}`, title: kind[1] }, m.kind === 'tie' ? `spans ${list(m.spans).join('·')}` : kind[0]) : null),
      r.fields.map((f, i) => {
        const u = uses.get(i);
        if (!u) return h('td', { class: 'cell' });
        const mark = (u.read ? 'R' : '') + (u.write ? 'W' : '');
        const own = m.block && f.block === m.block;
        return h('td', {
          class: `cell on ${own ? blockClass(r, m.block) : fieldClass(f)}${u.write ? ' w' : ''}${m.kind === 'tie' ? ' tie' : ''}${!own && f.block && m.block && f.block !== 'core' ? ' far' : ''}`,
          title: `${m.name} ${u.read && u.write ? 'reads and writes' : u.write ? 'writes' : 'reads'} ${f.name}`,
        }, mark);
      }),
      h('td', { class: 'calls code muted' }, list(m.calls).join(', ')));
  });
  return h('div', { class: 'scroll' }, h('table', { class: 'matrix' }, h('thead', {}, head, blocksBand, typesBand), h('tbody', {}, rows)));
}

// columnTypes lists the columns by type, with where each type ends up.
function columnTypes(r) {
  const rows = COLUMN_TYPES.map(([key, name, what, where]) => {
    const fs = r.fields.filter((f) => f.role === key);
    if (!fs.length) return null;
    return h('tr', {}, h('td', {}, h('strong', {}, name), h('div', { class: 'muted small' }, what)),
      h('td', { class: 'code' }, fs.map((f) => f.name + (f.hub ? ' (hub)' : '') + (f.operation ? ` (of ${f.operation})` : '')).join(', ')),
      h('td', { class: 'muted' }, where));
  });
  return section('Column types', h('div', { class: 'scroll' }, h('table', { class: 'data ctypes' },
    h('thead', {}, h('tr', {}, h('th', {}, 'Type'), h('th', {}, 'Columns'), h('th', {}, 'Ends up'))), h('tbody', {}, rows))));
}

const STANCE = { agrees: ['✓', 'agrees'], disagrees: ['✗', 'disagrees'], note: ['•', 'note'] };
const VERDICT = { extract: 'extract: the signals agree', check: 'check first: a signal disagrees', candidate: 'candidate' };

// blocksView lists the blocks, the candidate types, each checked against
// the other signals.
function blocksView(r) {
  const bs = list(r.blocks);
  if (!bs.length) return section('Blocks', h('p', { class: 'muted small' }, 'No block: no row marks a column outside the hubs.'));
  return section('Blocks, the candidate types', h('div', { class: 'blocks' }, bs.map((b) => h('div', { class: `block${r.start && r.start.block === b.label ? ' first' : ''}` },
    h('div', { class: 'block-head' },
      h('span', { class: `glabel ${blockClass(r, b.label)}` }, b.label),
      h('span', { class: 'small' }, BLOCK_KIND[b.kind]),
      b.label !== 'core' ? h('span', { class: `tag v-${b.verdict}` }, VERDICT[b.verdict]) : null,
      r.start && r.start.block === b.label ? h('span', { class: 'tag out' }, 'start here') : null),
    h('div', { class: 'small' }, h('code', {}, b.fields.join(', ') || '—'), h('span', { class: 'muted' }, ' · '), b.methods.join(', ') || '—'),
    b.label !== 'core' ? h('div', { class: 'muted small' },
      b.ownsWrites ? 'owns its writes' : 'others write its fields',
      state.rv.indexed ? ` · ${plural(b.changes, 'commit')} in the last year` : '',
      b.word ? ` · named «${b.word}»` : '') : null,
    list(b.evidence).length ? h('ul', { class: 'evidence' }, b.evidence.map((e) => h('li', { class: `st-${e.stance}` },
      h('span', { class: 'mark-st', title: `${e.signal} ${STANCE[e.stance][1]}` }, STANCE[e.stance][0]), ' ', h('span', { class: 'muted' }, `${e.signal}: `), inModule(e.text)))) : null))));
}

// patterns reads off the technique's patterns beyond the blocks.
function patterns(r) {
  const items = [];
  for (const t of list(r.ties)) {
    items.push([true, 'Row spanning blocks', `${t.method} (${plural(t.stmts, 'statement')}) marks the fields of blocks ${t.blocks.join(' and ')}: split it with Extract Method until each piece falls in one block, then redraw the matrix.`]);
  }
  for (const s of list(r.sharedWrites)) {
    items.push([true, 'Column written from two blocks', `${s.field} is written by ${s.writers.join(', ')} in blocks ${s.blocks.join(', ')}: ownership follows writes, so let one block own it and the others pass the value in or ask the owner.`]);
  }
  const bridges = r.methods.filter((m) => m.kind === 'bridge').map((m) => m.name);
  if (bridges.length) items.push([false, 'Bridge rows', `${bridges.join(', ')} only ${bridges.length === 1 ? 'calls' : 'call'} other methods: orchestration for a thin coordinator or use case, not any block.`]);
  const free = r.methods.filter((m) => m.kind === 'free').map((m) => m.name);
  if (free.length) items.push([false, 'Empty rows', `${free.join(', ')} ${free.length === 1 ? 'marks' : 'mark'} no field: ${free.length === 1 ? 'a free function' : 'free functions'}, the safest to move out first.`]);
  for (const e of list(r.envy)) {
    items.push([false, 'Column read only through getters', `${e.field} is only read through ${e.getters.join(', ')}${list(e.clients).length ? `, by ${e.clients.map(short).join(', ')}` : ''}: the code using it outside probably belongs in its block.`]);
  }
  const hubs = r.fields.filter((f) => f.hub).map((f) => f.name);
  if (hubs.length) items.push([false, 'Hubs', `${hubs.join(', ')} ${hubs.length === 1 ? 'is' : 'are'} used by most rows and set aside: ${hubs.length === 1 ? 'it connects' : 'they connect'} everything and hide the blocks.`]);
  if (!items.length) return h('div', {});
  return section('Patterns', h('ul', { class: 'patterns' }, items.map(([w, name, text]) => h('li', { class: w ? 'warn-note' : '' }, h('strong', {}, name), ': ', text))));
}

function clientsView(r) {
  const rows = list(r.clients);
  const box = section('Callers × public methods');
  if (!r.methods.some((m) => m.exported)) {
    box.append(h('p', { class: 'muted small' }, 'Its methods are unexported, so only its own package uses them.'));
    return box;
  }
  if (!rows.length) {
    box.append(h('p', { class: 'muted small' }, 'No code outside the type calls its public methods directly; calls through interfaces are not seen.'));
  } else {
    const used = r.methods.filter((m) => rows.some((c) => c.methods.includes(m.name)));
    box.append(h('div', { class: 'scroll' }, h('table', { class: 'matrix clients' },
      h('thead', {}, h('tr', {}, h('th', { class: 'corner' }, 'caller \\ method'), used.map((m) => h('th', { class: `fcol ${blockClass(r, m.block)}` }, h('span', {}, m.name)))),
        h('tr', { class: 'bands' }, h('th', {}), used.map((m) => h('th', { class: blockClass(r, m.block) }, m.block || '·')))),
      h('tbody', {}, rows.map((c) => h('tr', {}, h('th', { class: 'mname' }, h('code', { title: c.package }, short(c.package))),
        used.map((m) => h('td', { class: `cell${c.methods.includes(m.name) ? ` on ${blockClass(r, m.block)}` : ''}` }, c.methods.includes(m.name) ? 'x' : ''))))))));
    const roles = list(r.roles);
    box.append(h('ul', {}, roles.map((ro) => {
      const bs = list(ro.blocks);
      const verdict = bs.length > 1 ? `spans blocks ${bs.join(', ')}: a use case; give it a facade rather than splitting along its lines`
        : bs.length === 1 ? `block ${bs[0]} only: the callers split as the fields do` : '';
      return h('li', {}, ro.clients.map(short).join(', '), h('span', { class: 'muted' }, ' use '), ro.methods.join(', '),
        verdict ? h('span', { class: 'muted' }, ` · ${verdict}`) : null);
    })));
    box.append(h('p', { class: 'muted small' }, 'Each role can be an interface declared on the callers\' side, naming only the methods it uses: split the interface before the implementation.'));
  }
  if (list(r.unused).length) box.append(h('p', { class: 'small' }, h('span', { class: 'muted' }, 'Public with no caller outside the type, so make it private or delete it: '), r.unused.join(', ')));
  return box;
}

function namesView(r) {
  const words = list(r.words), flags = list(r.flags);
  if (!words.length && !flags.length) return h('div', {});
  return section('Naming pass', h('ul', {},
    words.map((w) => h('li', {}, h('strong', {}, w.word), h('span', { class: 'muted' }, ' · '), w.members.join(', '))),
    flags.length ? h('li', {}, h('span', { class: 'muted' }, 'Mode flags or states, where each value may be a type of its own: '), flags.join(', ')) : null));
}

function couplingView(r) {
  const box = section('Change coupling');
  if (!state.rv.indexed) {
    box.append(h('p', { class: 'muted small' }, 'Once the history is indexed, this lists the methods changed in the same commits.'));
    return box;
  }
  const cs = list(r.coupled);
  if (!cs.length) {
    box.append(h('p', { class: 'muted small' }, 'No methods changed together often in the last year.'));
    return box;
  }
  box.append(h('ul', {}, cs.map((c) => h('li', { class: c.across ? 'warn-note' : '' },
    h('code', {}, c.a), ' and ', h('code', {}, c.b), ` changed together in ${c.together} commits (of ${c.aChanges} and ${c.bChanges})`,
    c.across ? h('span', { class: 'muted' }, ' · across blocks: a concept missing between them, a boundary in the wrong place, or one rule written twice') : h('span', { class: 'muted' }, ' · within a block: it confirms the block')))));
  return box;
}

// --- The lists --------------------------------------------------------------

// more returns a button that lists everything in the list named key, or
// null when it shows everything already.
function more(key, shown, total) {
  if (shown >= total) return null;
  return h('button', { class: 'more', type: 'button', onclick: () => { state.showAll.add(key); lists(); } }, `Show all ${total.toLocaleString()}`);
}
const limit = (key, xs) => (state.showAll.has(key) ? xs : xs.slice(0, LIST_ROWS));

// focusedCallers are the callers to list: all of them, or with a package
// picked, those of what changed in it, or those in it.
function focusedCallers() {
  const rv = state.rv, f = state.focus;
  if (!f) return rv.blast.callers;
  const own = rv.changes.filter((c) => c.package === f && c.kind !== 'added');
  if (!own.length) return rv.blast.callers.filter((c) => c.package === f);
  const by = new Map();
  for (const ch of own) {
    for (const cl of ch.callers) {
      const key = `${cl.package}|${cl.path}|${cl.name}`;
      const x = by.get(key);
      if (x) x.calls += cl.calls;
      else by.set(key, { ...cl });
    }
  }
  return [...by.values()].sort((a, b) => b.calls - a.calls || a.name.localeCompare(b.name));
}

function impact() {
  const rv = state.rv, b = rv.blast, scoped = rv.scope.length > 0;
  $('outsideToggle').hidden = !scoped;
  let note = b.callSites
    ? `${plural(b.callSites, 'call site')}${b.approximate ? ' (approx.)' : ''} in ${plural(b.callers.length, 'declaration')}, ${plural(b.packages.length, 'package')}`
    : 'nothing outside the change uses what it changed';
  if (scoped && b.callSites) note += ` · ${outsideCallers().sites.toLocaleString()} outside the scope`;
  $('impactNote').textContent = note;

  // Callers outside the scope first, tests last, then by uses.
  const first = (c) => (c.outside ? 0 : 1) + (c.test ? 2 : 0);
  let cs = [...focusedCallers()].sort((a, b) => first(a) - first(b) || b.calls - a.calls);
  if (scoped && state.outsideOnly) cs = cs.filter((c) => c.outside);
  const table = $('callers');
  if (!cs.length) {
    table.replaceChildren(h('tbody', {}, h('tr', {}, h('td', { class: 'muted' }, 'No callers to list.'))));
    return;
  }
  const rows = limit('callers', cs).map((c) => h('tr', {},
    h('td', { class: 'code' }, h('code', {}, c.name),
      c.outside ? h('span', { class: 'tag out' }, 'outside') : null,
      c.test ? h('span', { class: 'tag' }, 'test') : null,
      c.approximate ? h('span', { class: 'tag', title: 'matched by method name only' }, 'approx.') : null),
    h('td', { class: 'code muted' }, `${c.path}:${c.line}`),
    h('td', { class: 'num' }, `×${c.calls}`)));
  const m = more('callers', rows.length, cs.length);
  table.replaceChildren(
    h('thead', {}, h('tr', {}, h('th', {}, 'Caller'), h('th', {}, 'Where'), h('th', { class: 'num' }, 'Uses'))),
    h('tbody', {}, rows, m ? h('tr', {}, h('td', { colspan: 3 }, m)) : null));
}

function crossingTag(crossing) {
  if (crossing === 'outward') return h('span', { class: 'tag out' }, 'out of the scope');
  if (crossing === 'inward') return h('span', { class: 'tag out' }, 'into the scope');
  return null;
}

function refItem(sign, r) {
  return h('li', {}, sign ? h('span', { class: `sign ${sign === '+' ? 'plus' : 'minus'}` }, sign) : null,
    h('code', {}, r.from), ' → ', h('code', {}, r.to), ` ×${r.uses}`,
    r.approximate ? h('span', { class: 'tag', title: 'matched by method name only' }, 'approx.') : null,
    ' ', h('span', { class: 'where code' }, `${r.path}:${r.line}`));
}

function relations() {
  const R = state.rv.relations, f = state.focus;
  const touches = (a, b) => !f || a === f || b === f;
  const isNew = new Set(R.newPackages), gone = new Set(R.gonePackages);
  const out = [];
  const pkgs = [...R.newPackages.map((p) => ['new package', p]), ...R.gonePackages.map((p) => ['removed package', p])].filter(([, p]) => touches(p, p));
  if (pkgs.length) {
    out.push(h('div', { class: 'dep' }, h('div', { class: 'dep-head' },
      ...pkgs.flatMap(([what, p], i) => [i ? ' · ' : '', `${what} `, pkgCode(p)]))));
  }
  const used = new Set();
  const dep = (sign, d, refs) => {
    const along = refs.filter((r) => r.fromPackage === d.from && r.toPackage === d.to);
    along.forEach((r) => used.add(r));
    const key = `dep|${sign}|${d.from}|${d.to}`;
    const shown = limit(key, along);
    const m = more(key, shown.length, along.length);
    return h('div', { class: 'dep' },
      h('div', { class: 'dep-head' },
        h('span', { class: `sign ${sign === '+' ? 'plus' : 'minus'}` }, sign),
        pkgCode(d.from), ' → ', pkgCode(d.to),
        crossingTag(d.crossing),
        isNew.has(d.to) ? h('span', { class: 'tag' }, 'new package') : null,
        gone.has(d.to) ? h('span', { class: 'tag' }, 'removed package') : null),
      along.length
        ? h('ul', {}, shown.map((r) => refItem('', r)), m ? h('li', {}, m) : null)
        : h('ul', {}, h('li', { class: 'muted' }, `imported in ${d.files.join(', ')}`)));
  };
  for (const d of R.addedDeps) if (touches(d.from, d.to)) out.push(dep('+', d, R.addedRefs));
  for (const d of R.removedDeps) if (touches(d.from, d.to)) out.push(dep('−', d, R.removedRefs));
  for (const d of R.addedDeps) R.addedRefs.filter((r) => r.fromPackage === d.from && r.toPackage === d.to).forEach((r) => used.add(r));
  for (const d of R.removedDeps) R.removedRefs.filter((r) => r.fromPackage === d.from && r.toPackage === d.to).forEach((r) => used.add(r));

  // The references started and stopped along imports that stay, by the
  // pair of packages they run between.
  const rest = new Map();
  for (const [sign, refs] of [['+', R.addedRefs], ['−', R.removedRefs]]) {
    for (const r of refs) {
      if (used.has(r) || !touches(r.fromPackage, r.toPackage)) continue;
      const key = `${r.fromPackage}|${r.toPackage}`;
      rest.set(key, [...(rest.get(key) || []), [sign, r]]);
    }
  }
  if (rest.size) out.push(h('p', { class: 'sub-h' }, 'Along imports that stay'));
  for (const [key, refs] of [...rest].sort(([a], [b]) => a.localeCompare(b))) {
    const [from, to] = key.split('|');
    const shown = limit(`along|${key}`, refs);
    const m = more(`along|${key}`, shown.length, refs.length);
    out.push(h('div', { class: 'dep' },
      h('div', { class: 'dep-head' }, h('span', { class: 'sign' }), pkgCode(from), ' → ', pkgCode(to), crossingTag(refs[0][1].crossing)),
      h('ul', {}, shown.map(([s, r]) => refItem(s, r)), m ? h('li', {}, m) : null)));
  }
  if (!out.length) out.push(h('p', { class: 'muted small' }, f ? 'None involve this package.' : 'No imports or references between packages started or stopped.'));
  $('rels').replaceChildren(...out);
}

function changes() {
  const rv = state.rv, f = state.focus;
  const present = KINDS.filter(([k]) => rv.counts[k]);
  $('kindChips').replaceChildren(...present.map(([k, label]) => h('button', {
    type: 'button', class: 'chip', 'aria-pressed': String(state.kinds.has(k)),
    onclick: () => { state.kinds.has(k) ? state.kinds.delete(k) : state.kinds.add(k); changes(); },
  }, mark(k), label, h('span', { class: 'n' }, rv.counts[k]))));

  const cs = rv.changes.filter((c) => c.kinds.some((k) => state.kinds.has(k)) &&
    (!f || c.package === f || c.callers.some((cl) => cl.package === f)));
  if (!cs.length) {
    $('changes').replaceChildren(h('p', { class: 'muted small' }, 'No changes to list.'));
    return;
  }
  const items = limit('changes', cs).map((c) => {
    const where = c.oldPath && (c.oldPath !== c.path || c.oldName)
      ? `${c.path}:${c.line}, was ${c.oldName || c.name} at ${c.oldPath}:${c.oldLine}` : `${c.path}:${c.line}`;
    const sig = c.oldSignature && c.newSignature && c.oldSignature !== c.newSignature
      ? h('div', { class: 'sig' }, h('div', { class: 'minus' }, '− ' + c.oldSignature), h('div', { class: 'plus' }, '+ ' + c.newSignature))
      : null;
    const callers = c.callers.length
      ? h('details', {}, h('summary', {}, plural(c.callers.length, 'caller')),
        h('ul', {}, c.callers.slice(0, 50).map((cl) => h('li', {}, h('code', {}, cl.name), ' ',
          h('span', { class: 'muted code' }, `${cl.path}:${cl.line} ×${cl.calls}`),
          cl.outside ? h('span', { class: 'tag out' }, 'outside') : null,
          cl.test ? h('span', { class: 'tag' }, 'test') : null))))
      : null;
    return h('div', { class: 'change' },
      h('div', { class: 'change-head' }, mark(c.kind), h('span', { class: 'k' }, c.symbol), h('code', {}, c.name),
        h('span', { class: 'muted code' }, where)),
      h('div', { class: 'meta' }, [c.summary, c.impact].filter(Boolean).join(' · '),
        c.apiChange ? h('span', { class: 'api' }, ' · API change') : null),
      sig, callers);
  });
  $('changes').replaceChildren(...items, more('changes', items.length, cs.length) || '');
}

function files() {
  const fs = state.rv.files;
  $('files').replaceChildren(
    h('thead', {}, h('tr', {}, h('th', {}, 'File'), h('th', { class: 'num' }, 'Lines'), h('th', {}, 'Symbols'))),
    h('tbody', {}, fs.length ? fs.map((f) => h('tr', {},
      h('td', { class: 'code' }, f.oldPath ? `${f.oldPath} → ${f.path}` : f.path),
      h('td', { class: 'num' }, h('span', { style: 'color: var(--added)' }, `+${f.added}`), ' ', h('span', { style: 'color: var(--deleted)' }, `−${f.removed}`)),
      h('td', { class: 'small' }, f.changes ? `${plural(f.changes, 'symbol')}: ${f.kinds.join(', ')}` : '')))
      : h('tr', {}, h('td', { class: 'muted', colspan: 3 }, 'No Go files changed.'))));
}

// --- Wiring -----------------------------------------------------------------

$('folder').addEventListener('keydown', (ev) => {
  if (ev.key === 'Enter' && $('folder').value.trim()) {
    ev.preventDefault();
    addFolder();
  } else if (ev.key === 'Backspace' && !$('folder').value && state.folders.length) {
    state.folders.pop();
    renderFolders();
  }
});
// Picking from the list of folders adds it at once.
$('folder').addEventListener('input', (ev) => {
  if (ev.inputType === 'insertReplacementText' || ev.inputType === undefined) {
    const v = $('folder').value.trim();
    if ([...$('dirs').options].some((o) => o.value === v)) addFolder();
  }
});
$('head').addEventListener('change', () => loadOptions($('head').value.trim()));
$('pick').addEventListener('submit', (ev) => {
  ev.preventDefault();
  addFolder();
  const q = query().toString();
  history.pushState(null, '', q ? `?${q}` : location.pathname);
  run();
});
$('outsideOnly').addEventListener('change', (ev) => { state.outsideOnly = ev.target.checked; impact(); });
window.addEventListener('popstate', () => { readURL(); run(); });
let resizing = 0;
window.addEventListener('resize', () => {
  clearTimeout(resizing);
  resizing = setTimeout(() => { if (state.rv) drawGraph(); }, 150);
});

readURL();
api('/api/status').then((st) => { $('repo').textContent = st.repo; }, () => {});
loadOptions($('head').value.trim());
run();
