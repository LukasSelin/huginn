'use strict';

// The review page: what a merge request changes since its merge base with
// the branch it goes into, optionally limited to folders. It opens on what
// the diff does not show (the code it adds or edits that reaches the
// network, files or other programs, the exported API it breaks, the
// existing code it edits and who runs that code, the imports it starts),
// then the packages it reaches, the code depending on what it changed, and
// the changes, the edits of existing code in full and the new code and
// tests folded away. What is reviewed lives in the URL, so a review can be
// linked to.
//
// Tracing what reaches outside the process takes capslock a while, so the
// page shows the review first and asks for the trace after.

const state = {
  rv: null,          // the review on show
  model: null,       // its relations graph
  focus: null,       // the package picked in the graph, filtering the lists
  folders: [],       // the scope being edited
  outsideOnly: false,
  withTests: false,  // the impact lists callers in test files too
  showAll: new Set(), // lists expanded past their first rows
  ctrl: null,        // aborts the review being fetched, and its trace
  tracing: false,    // capslock is tracing the review on show
  traceError: '',    // why asking for the trace failed
  graphView: 'packages', // the relations drawn between "packages" or "functions"
  part: null,        // drawing functions, the index of the part open, if any
  fn: null,          // and the change index of the function of it open, if any
};

const LIST_ROWS = 40;
const LOOK_EDITS = 6; // edits of existing code, and changes reaching outside, in "Look here first" before the rest fold away
const REACH_NAMES = 3;
const PATH_NAMES = 6; // calls named on a path out of the process
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
  const q = query();
  try {
    const r = await fetch('/api/review?' + q, { signal: ctrl.signal });
    if (!r.ok) throw new Error((await r.text()).trim() || `${r.status}`);
    const rv = await r.json();
    if (ctrl !== state.ctrl) return;
    Object.assign(state, { rv, focus: null, showAll: new Set(), tracing: rv.traceable && !rv.traced, traceError: '', part: null, fn: null });
    loading.hidden = true;
    $('out').hidden = false;
    render();
  } catch (e) {
    if (e.name === 'AbortError') return;
    $('out').hidden = true;
    loading.className = 'loading error';
    loading.textContent = String(e.message || e);
    return;
  }
  if (state.tracing) traceOutside(q, ctrl);
}

// traceOutside asks for the review q again, traced with capslock, and puts
// what reaches outside the process into "Look here first" and the order of
// the changes, leaving the rest of the page as the reader left it.
async function traceOutside(q, ctrl) {
  const traced = new URLSearchParams(q);
  traced.set('caps', '1');
  try {
    const r = await fetch('/api/review?' + traced, { signal: ctrl.signal });
    if (!r.ok) throw new Error((await r.text()).trim() || `${r.status}`);
    const rv = await r.json();
    if (ctrl !== state.ctrl) return;
    const cur = state.rv;
    if (rv.headHash === cur.headHash && rv.mergeBase === cur.mergeBase) {
      Object.assign(cur, { focus: rv.focus, traced: rv.traced, warnings: rv.warnings });
    }
  } catch (e) {
    if (e.name === 'AbortError' || ctrl !== state.ctrl) return;
    state.traceError = String(e.message || e);
  }
  state.tracing = false;
  warnings();
  look();
  changes();
}

// --- The report -------------------------------------------------------------

function render() {
  const rv = state.rv;
  const where = rv.scope.length ? rv.scope.join(', ') : 'whole repository';
  $('what').textContent = `${rv.headName} into ${rv.target} · merge base ${shortHash(rv.mergeBase)} · ${plural(rv.commits, 'commit')} · ${where}`;
  $('headline').textContent = rv.headline;
  $('kinds').replaceChildren(...KINDS.filter(([k]) => rv.counts[k]).map(([k, label]) => h('span', {}, mark(k), `${label} ${rv.counts[k]}`)));
  warnings();
  tiles();
  look();
  graphHead();
  drawGraph();
  seams();
  lists();
}

function warnings() {
  const ws = state.rv.warnings || [];
  $('warnings').hidden = !ws.length;
  $('warnings').replaceChildren(...ws.map((w) => h('li', {}, w)));
}

function lists() {
  focusBar();
  impact();
  relations();
  changes();
  files();
}

// reach sums up callers: call sites, declarations and packages.
function reach(cs) {
  return { sites: sum(cs, (c) => c.calls), decls: cs.length, pkgs: new Set(cs.map((c) => c.package)).size, approx: cs.some((c) => c.approximate) };
}
const production = (cs) => cs.filter((c) => !c.test);
const inTests = (cs) => cs.filter((c) => c.test);
const outsideCallers = () => reach(production(state.rv.blast.callers).filter((c) => c.outside));

// The key numbers count production code: tests often outnumber it many
// times, and rarely break in ways that matter to a reviewer.
function tiles() {
  const rv = state.rv, b = rv.blast, R = rv.relations;
  const p = reach(production(b.callers)), tests = reach(inTests(b.callers));
  const t = [{
    n: p.sites, label: 'call sites reached',
    sub: [plural(p.decls, 'declaration'), plural(p.pkgs, 'package'), tests.sites ? `${tests.sites.toLocaleString()} more in tests` : '', p.approx ? 'approx.' : ''].filter(Boolean).join(' · '),
  }];
  if (rv.scope.length) {
    const o = outsideCallers();
    t.push({ cls: 't-out', n: o.sites, label: 'outside the scope', sub: `${plural(o.decls, 'declaration')} · ${plural(o.pkgs, 'package')}` });
  }
  t.push(
    { cls: 't-added', n: R.addedDeps.length, label: 'new imports', sub: `${R.removedDeps.length} removed` },
    { cls: b.apiChanges.length ? 't-api' : '', n: b.apiChanges.length, label: 'exported API changes', sub: 'that can break callers' },
  );
  $('tiles').replaceChildren(...t.map((x) => h('div', { class: `tile ${x.cls || ''}` },
    h('strong', {}, x.n.toLocaleString()), h('span', { class: 'label' }, x.label), h('span', { class: 'sub' }, x.sub))));
}

// --- Look here first ---------------------------------------------------------

const LOOK_KIND = {
  external: 'External', breaking: 'Breaks API', behavior: 'Edits', dependency: 'New import', structure: 'Blocks',
};

// look lists what the review says to look at first, as review.Focus has it:
// the code the change adds or edits that reaches outside the process, the
// exported API it breaks, the edits of existing code, the largest reaching
// most first, the imports it starts and the warnings about the types it
// touches. The code reaching outside that leaves the changed code by
// the same call is listed under it. Past the first few, the edits and the
// code reaching outside fold away. Above them it says whether capslock is
// still tracing, and below them what it found when it found nothing, or
// how to have it.
function look() {
  const rv = state.rv, fs = rv.focus || [];
  const items = [];
  if (state.tracing) {
    items.push(h('li', { class: 'look-note look-tracing muted small', role: 'status' },
      h('span', { class: 'spin', 'aria-hidden': 'true' }), 'Tracing with capslock which changes reach the network, files or other programs…'));
  }
  const all = state.showAll.has('look');
  const shown = { external: 0, behavior: 0 }, hidden = { external: 0, behavior: 0 };
  for (const run of lookRuns(fs)) {
    const kind = run[0].kind;
    let fit = run;
    if (kind in shown && !all) {
      const n = Math.min(run.length, Math.max(0, LOOK_EDITS - shown[kind]));
      shown[kind] += n;
      hidden[kind] += run.length - n;
      if (!n) continue;
      fit = run.slice(0, n);
    }
    items.push(run.length > 1 ? lookGroup(fit, run) : lookItem(fit[0]));
  }
  const left = [
    hidden.external ? `${plural(hidden.external, 'more change')} reaching outside the process` : '',
    hidden.behavior ? `${plural(hidden.behavior, 'more edit')} of existing code` : '',
  ].filter(Boolean);
  if (!all && left.length) {
    items.push(h('li', { class: 'look-more' }, h('button', {
      class: 'more', type: 'button', onclick: () => { state.showAll.add('look'); look(); },
    }, `and ${left.join(' and ')}`)));
  }
  if (!fs.length) {
    items.push(h('li', { class: 'look-empty muted small' },
      'Nothing beyond new code: no existing code is edited and no import between packages starts.'));
  }
  if (rv.traced && !fs.some((f) => f.kind === 'external')) {
    items.push(h('li', { class: 'look-note muted small' },
      'Traced with capslock: nothing the change adds or edits reaches the network, files or other programs.'));
  } else if (state.traceError) {
    items.push(h('li', { class: 'look-note look-error small' }, `Could not trace with capslock: ${state.traceError}`));
  } else if (!rv.traceable) {
    items.push(h('li', { class: 'look-note muted small' }, 'To see which changes reach the network, files or other programs, install capslock (',
      h('code', {}, 'go install github.com/google/capslock/cmd/capslock@latest'), ') and restart huginn serve.'));
  }
  $('look').replaceChildren(...items);
}

// sameCall reports whether two steps are the same call, the same function
// called on the same line, as review.Focus.SameExit compares exits.
const sameCall = (a, b) => !!a && !!b && a.package === b.package && a.name === b.name && a.path === b.path && a.line === b.line;

// lookRuns cuts the focus into runs: each item reaching outside the process
// with those right after it sharing its exit, as the review orders them,
// and every other item alone.
function lookRuns(fs) {
  const runs = [];
  for (const f of fs) {
    const last = runs[runs.length - 1];
    if (last && sameCall(last[0].exit, f.exit)) last.push(f);
    else runs.push([f]);
  }
  return runs;
}

function lookItem(f) {
  return h('li', { class: `look-item l-${f.kind}` },
    h('span', { class: 'look-kind' }, LOOK_KIND[f.kind] || f.kind),
    h('div', { class: 'look-body' }, ...lookBody(f, false)));
}

// lookGroup lists the items leaving the changed code by the same call
// together, as cmd/huginn's exitText and exitDetail say them: from the
// function where their paths meet (through) on to the capability, and
// where that function is declared, or from the call and where it is made
// when there is none; how many of the changes (run) reach outside through
// it, and where the path leaves the changed code and the repository's;
// then each item shown (fs), its path stopping where they meet.
function lookGroup(fs, run) {
  const first = run[0].capabilities[0];
  const exit = first.path[first.leavesChange], leave = first.path[first.leaves];
  let from = first.path.slice(first.leavesChange), elided = first.elided, where = `${exit.path}:${exit.line}`;
  const met = run.find((f) => f.capabilities[0].throughAt >= 0);
  if (met) {
    const c = met.capabilities[0];
    from = c.path.slice(c.throughAt);
    elided = c.elided;
  }
  const own = run.find((f) => f.capabilities[0].throughAt === 0);
  if (own) where = `${own.path}:${own.line}`;
  const at = (s) => h('code', {}, `${s.path}:${s.line}`);
  const differ = !sameCall(exit, leave);
  let detail;
  if (!run[0].through) detail = ['this call', ...(differ ? [', leaving the repository’s code at ', at(leave)] : [])];
  else if (differ) detail = ['it, leaving the changed code at ', at(exit), ' and the repository’s at ', at(leave)];
  else detail = ['it, leaving the repository’s code at ', at(leave)];
  return h('li', { class: 'look-item l-external look-group' },
    h('span', { class: 'look-kind' }, LOOK_KIND.external),
    h('div', { class: 'look-body' },
      h('div', { class: 'look-head' }, h('span', { class: 'cap-path' }, ...pathNodes(from, elided)),
        h('span', { class: 'muted code' }, where)),
      h('div', { class: 'look-what' }, `${plural(run.length, 'change')} reach outside through `, ...detail),
      h('ul', { class: 'look-members' }, ...fs.map((f) => h('li', { class: 'look-member' }, ...lookBody(f, true))))));
}

// lookBody is what an item says: its name and place, what the change does
// to its paths, what changed, whom it reaches and how it reaches outside
// the process. Grouped, its paths stop where the group's paths meet.
function lookBody(f, grouped) {
  const rv = state.rv;
  const ch = f.change >= 0 ? rv.changes[f.change] : null;
  let name;
  if (f.kind === 'dependency') {
    const [from, to] = f.name.split(' → ');
    name = h('span', {}, pkgCode(from), ' → ', pkgCode(to));
  } else if (ch) {
    name = h('a', { href: `#ch-${f.change}`, onclick: (ev) => { ev.preventDefault(); showChange(f.change); } }, h('code', {}, f.name));
  } else {
    name = h('code', {}, f.name);
  }
  const where = f.line ? `${f.path}:${f.line}` : f.path;
  const p = f.change >= 0 ? pathsOf(f.change) : null;
  return [
    h('div', { class: 'look-head' }, name, where ? h('span', { class: 'muted code' }, where) : null),
    p ? verdictLine(p, 'look-verdict') : null,
    h('div', { class: 'look-what' }, f.what, f.why ? h('span', { class: 'why' }, ` · ${f.why}`) : null),
    reachLine(f, ch),
    ...(f.capabilities || []).map((c) => capabilityLine(f, c, grouped)),
  ];
}

// capabilityLine says how an item reaches outside the process: "network
// new  http.Get → … → net.(*Dialer).DialContext  payments/payments.go:6",
// the place being where the call leaving the repository's code is made.
// Grouped with the items sharing the call it leaves the changed code by,
// the path stops where their paths meet, or at the call when it does not
// pass there.
function capabilityLine(f, c, grouped) {
  let steps = c.path, elided = c.elided, leave = c.leaves >= 0 ? c.path[c.leaves] : null;
  if (grouped && c.leavesChange >= 0 && sameCall(c.path[c.leavesChange], f.exit)) {
    steps = c.path.slice(0, (c.throughAt >= 0 ? c.throughAt : c.leavesChange) + 1);
    elided = 0;
    leave = null;
  }
  if (steps.length && steps[0].name === f.name) steps = steps.slice(1); // the item itself, unless a function literal of it
  return h('div', { class: 'look-cap small' },
    h('span', { class: `cap${c.new ? ' new' : ''}`, title: c.name }, c.label),
    c.new ? h('span', { class: 'tag new', title: 'not reached at the merge base' }, 'new') : null,
    h('span', { class: 'cap-path' }, ...pathNodes(steps, elided)),
    leave ? h('span', { class: 'muted code' }, `${leave.path}:${leave.line}`) : null);
}

// pathNodes names the calls on a path with arrows between them, "…"
// standing for the elided calls before the last and for the middle of a
// path with more than PATH_NAMES.
function pathNodes(steps, elided) {
  let names = [];
  steps.forEach((s, i) => {
    if (elided > 0 && i === steps.length - 1) names.push('…');
    names.push(s);
  });
  if (names.length > PATH_NAMES) names = [...names.slice(0, 2), '…', ...names.slice(-3)];
  return names.flatMap((s, i) => [i ? ' → ' : '', s === '…' ? '…' : h('code', { title: s.package }, s.name)]);
}

// reachLine says whom an item reaches: "called by a, b and 3 more · 29 in
// tests", or that nothing does.
function reachLine(f, ch) {
  if (f.kind !== 'breaking' && f.kind !== 'behavior' && f.kind !== 'external') return null;
  const verb = f.implementers ? 'implemented by' : ch && (ch.symbol === 'func' || ch.symbol === 'method') ? 'called by' : 'used by';
  const parts = [];
  if (f.reaches.length) {
    const names = f.reaches.slice(0, REACH_NAMES).flatMap((c, i) => [i ? ', ' : '', h('code', { title: `${c.path}:${c.line}` }, c.name),
      c.outside ? h('span', { class: 'tag out' }, 'outside') : null]);
    const more = f.reaches.length - REACH_NAMES;
    parts.push(h('span', {}, `${verb} `, ...names, more > 0 ? ` and ${more} more` : ''));
  } else if (f.implementers) {
    parts.push('no implementer found outside the change');
  } else {
    parts.push(f.tests ? 'only tests use it' : 'nothing calls it');
  }
  if (f.tests) parts.push(`${f.tests.toLocaleString()} in tests`);
  return h('div', { class: 'look-reach small' }, ...parts.flatMap((p, i) => [i ? ' · ' : '', p]));
}

// showChange scrolls to a change's card, showing every package first if
// the one picked in the graph hides it.
function showChange(i) {
  let card = $(`ch-${i}`);
  if (!card && state.focus) {
    pick(state.focus);
    card = $(`ch-${i}`);
  }
  if (!card) return;
  for (let d = card.closest('details'); d; d = d.parentElement.closest('details')) d.open = true;
  card.scrollIntoView({ behavior: 'smooth', block: 'center' });
  card.classList.remove('flash');
  void card.offsetWidth; // restart the animation
  card.classList.add('flash');
}

const GRAPH_NOTE = {
  packages: 'which packages the change reaches, and the imports and references it starts and stops · point at a package to trace it, pick it to filter the lists below',
  functions: 'the functions and methods it adds, edits and removes, split by the calls between them into parts to read one at a time · open a part, then a function, for every call in and out of it',
  layers: 'the packages it touches on the layers of the whole codebase, each a layer above everything it imports, at the merge base and at the head · point at a package to trace its imports, pick it to filter the lists below',
};
const GRAPH_KEY = 'huginn.review.graph'; // the graph last drawn, kept in this browser

// graphHead says which graph the relations panel draws, and what it shows.
function graphHead() {
  for (const b of $('graphView').children) b.setAttribute('aria-pressed', String(b.dataset.view === state.graphView));
  $('graphNote').textContent = GRAPH_NOTE[state.graphView];
  legend();
}

function setGraphView(v) {
  state.graphView = ['functions', 'layers'].includes(v) ? v : 'packages';
  try { localStorage.setItem(GRAPH_KEY, state.graphView); } catch { /* kept for this page only */ }
  if (!state.rv) return;
  graphHead();
  drawGraph();
}

function legend() {
  const line = (cls, color, text) => h('span', {}, h('i', { class: `line ${cls}`, style: `border-color: var(${color})` }), text);
  const box = (color, text, dash) => h('span', {}, h('i', { class: 'box', style: `border-color: var(${color})${dash ? '; border-style: dashed' : ''}` }), text);
  if (state.graphView === 'functions') {
    $('legend').replaceChildren(
      box('--accent', 'edited function'),
      box('--added', 'new function', true),
      box('--deleted', 'removed function', true),
      box('--line-strong', 'unchanged code'),
      line('', '--muted', 'calls what changed'),
      line('', '--body', 'call kept between changed functions'),
      line('', '--added', 'new call'),
      line('dash', '--deleted', 'removed call'));
    return;
  }
  if (state.graphView === 'layers') {
    $('legend').replaceChildren(
      box('--accent', 'package it changes'),
      box('--added', 'new package', true),
      box('--deleted', 'removed package', true),
      box('--line-strong', 'package it imports or moves'),
      line('', '--muted', 'import'),
      line('', '--added', 'new import'),
      line('dash', '--deleted', 'removed import'),
      h('span', {}, h('b', { class: 'move' }, '↑'), 'moved to a higher layer'));
    return;
  }
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

// drawGraph draws the relations as the graph picked: of packages, of
// functions as the change in parts, or of the layers of the codebase.
function drawGraph() {
  if (state.graphView === 'functions') drawLevels();
  else if (state.graphView === 'layers') drawLayers();
  else drawPackages();
}

function drawPackages() {
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
  if (state.graphView !== 'packages') {
    drawGraph();
  } else {
    for (const g of $('graphBox').querySelectorAll('.g-node')) {
      g.classList.toggle('picked', g.getAttribute('aria-label').startsWith(`${state.focus}, `));
    }
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

// --- The layers of the whole codebase -------------------------------------------
//
// The packages the change touches on the layers of the whole codebase, as
// the layers page stacks them: each a layer above everything it imports,
// worked out over the whole tree on either side of the change, so a package
// sits where it sits in the architecture. Every layer has a row, the
// highest on top, saying how many packages of the codebase it holds; only
// the packages the change touches sit on them, those it moves saying the
// layer they come from, and the imports between them drawn, those it starts
// and stops coloured.

// layerOf is a package's layer at the head, or at the merge base for one
// the change removes.
const layerOf = (p) => (p.after >= 0 ? p.after : p.before);

// layerNote says in a few words what the change does to a package: its
// changes, and whether it is new, removed or moved to another layer.
function layerNote(p) {
  const parts = [];
  if (p.before < 0) parts.push('new');
  else if (p.after < 0) parts.push('removed');
  else if (p.before !== p.after) parts.push(`${p.after > p.before ? '↑' : '↓'} from layer ${p.before}`);
  parts.push(p.changes ? plural(p.changes, 'change') : 'code unchanged');
  return parts.join(' · ');
}

// layersSummary sums the map up in a line: how many packages on how many of
// the codebase's layers, and which ones the change moves.
function layersSummary(L) {
  const used = new Set(L.packages.map(layerOf));
  const moved = L.packages.filter((p) => p.before >= 0 && p.after >= 0 && p.before !== p.after);
  const out = [`${plural(L.packages.length, 'package')} the change touches, on ${used.size} of the codebase's ${plural(L.sizes.length, 'layer')}`];
  if (moved.length) {
    const up = moved.filter((p) => p.after > p.before).length;
    out.push(`it moves ${plural(moved.length, 'package')}${up === moved.length ? ' up' : up ? '' : ' down'}: ` +
      moved.slice(0, 4).map((p) => `${short(p.package)} ${p.before} → ${p.after}`).join(', ') + (moved.length > 4 ? ` and ${moved.length - 4} more` : ''));
  } else {
    out.push('it moves no package to another layer');
  }
  const ends = (ps, word, side) => (ps.length ? [`${word} ${ps.slice(0, 4).map((p) => `${short(p.package)} on layer ${p[side]}`).join(', ')}${ps.length > 4 ? ` and ${ps.length - 4} more` : ''}`] : []);
  out.push(...ends(L.packages.filter((p) => p.before < 0), 'adds', 'after'), ...ends(L.packages.filter((p) => p.after < 0), 'removes', 'before'));
  return out.join(' · ');
}

function drawLayers() {
  const L = state.rv.layers, box = $('graphBox');
  if (!L || !L.packages.length) {
    box.replaceChildren(h('p', { class: 'empty muted' }, 'The change touches no package of the repository.'));
    return;
  }
  const top = Math.max(L.sizes.length - 1, ...L.packages.map(layerOf));
  const rows = Array.from({ length: top + 1 }, () => []);
  for (const p of L.packages) rows[layerOf(p)].push(p);

  // Order each row by where the packages it imports and is imported by
  // sit in theirs, so lines cross less.
  const nbrs = new Map(L.packages.map((p) => [p.package, []]));
  for (const im of L.imports) {
    nbrs.get(im.from).push(im.to);
    nbrs.get(im.to).push(im.from);
  }
  const pos = new Map();
  const place = (r) => r.forEach((p, i) => pos.set(p.package, (i + 0.5) / r.length));
  for (const r of rows) {
    r.sort((a, b) => a.package.localeCompare(b.package));
    place(r);
  }
  for (let it = 0; it < 6; it++) {
    for (const r of it % 2 ? rows : [...rows].reverse()) {
      const at = new Map(r.map((p) => {
        const ns = nbrs.get(p.package);
        return [p, ns.length ? sum(ns, (n) => pos.get(n)) / ns.length : pos.get(p.package)];
      }));
      r.sort((a, b) => at.get(a) - at.get(b) || a.package.localeCompare(b.package));
      place(r);
    }
  }

  const LABEL = 132, NW = 200, NH = 40, GX = 16, FULL = 88, EMPTY = 26, PAD = 8;
  const widest = Math.max(...rows.map((r) => r.length * NW + Math.max(0, r.length - 1) * GX));
  const W = Math.max(box.clientWidth, 680, LABEL + widest + 16);
  const area = W - LABEL - 16;
  const nodes = new Map(), bands = [];
  let y = PAD;
  for (let l = top; l >= 0; l--) {
    const r = rows[l], hgt = r.length ? FULL : EMPTY;
    bands.push({ l, y, hgt, full: r.length > 0 });
    const needed = r.length * NW + Math.max(0, r.length - 1) * GX;
    let x = LABEL + (area - needed) / 2;
    for (const p of r) {
      nodes.set(p.package, { p, x, y: y + (hgt - NH) / 2 });
      x += NW + GX;
    }
    y += hgt;
  }
  const H = y + PAD;
  const el = svg('svg', { class: 'layers-map', width: W, height: H, viewBox: `0 0 ${W} ${H}`, role: 'img', 'aria-label': 'The packages the change touches on the layers of the whole codebase' });
  const defs = svg('defs', {});
  for (const [kind, color] of Object.entries({ impact: '--muted', added: '--added', removed: '--deleted' })) {
    defs.append(svg('marker', { id: `ly-${kind}`, viewBox: '0 0 10 10', refX: 9, refY: 5, markerWidth: 8, markerHeight: 8, markerUnits: 'userSpaceOnUse', orient: 'auto' },
      svg('path', { d: 'M0,1 L10,5 L0,9 z', style: `fill: var(${color})` })));
  }
  el.append(defs);
  for (const b of bands) {
    el.append(svg('rect', { x: 0, y: b.y + 1, width: W, height: b.hgt - 2, rx: 6, class: `ly-band${b.full ? ' full' : ''}` }),
      svg('text', { x: 12, y: b.y + b.hgt / 2 + (b.full ? -2 : 4), class: 'ly-name' }, `layer ${b.l}`));
    if (b.full) el.append(svg('text', { x: 12, y: b.y + b.hgt / 2 + 13, class: 'ly-count' }, plural(L.sizes[b.l] || 0, 'package')));
    else el.append(svg('text', { x: 70, y: b.y + b.hgt / 2 + 4, class: 'ly-count' }, plural(L.sizes[b.l] || 0, 'package')));
  }

  const edgeEls = new Map();
  const links = new Map([...nodes.keys()].map((k) => [k, []]));
  const order = (im) => (im.before && im.after ? 0 : im.before ? 1 : 2);
  // An import whose straight way down would run behind another package
  // goes round its side instead, each further one a little wider.
  const behind = (a, b, ax, bx) => [...nodes.values()].some((n) => n !== a && n !== b && n.y > a.y && n.y < b.y &&
    n.x < Math.max(ax, bx) + 6 && n.x + NW > Math.min(ax, bx) - 6);
  const round = { left: 0, right: 0 };
  for (const im of [...L.imports].sort((a, b) => order(a) - order(b))) {
    const a = nodes.get(im.from), b = nodes.get(im.to);
    const kind = im.before && im.after ? 'impact' : im.after ? 'added' : 'removed';
    const ax = a.x + NW / 2, bx = b.x + NW / 2;
    let d;
    if (a.y < b.y && behind(a, b, ax, bx)) {
      const side = round.right <= round.left ? 'right' : 'left', k = round[side]++;
      const out = side === 'right' ? 1 : -1, bow = 36 + 18 * k;
      const x0 = side === 'right' ? a.x + NW : a.x, x1 = side === 'right' ? b.x + NW + 2 : b.x - 2;
      const y0 = a.y + NH / 2, y1 = b.y + NH / 2;
      d = `M${x0},${y0} C${x0 + out * bow},${y0} ${x1 + out * bow},${y1} ${x1},${y1}`;
    } else if (a.y < b.y) {
      const y0 = a.y + NH, y1 = b.y - 2, my = (y0 + y1) / 2;
      d = `M${ax},${y0} C${ax},${my} ${bx},${my} ${bx},${y1}`;
    } else if (a.y === b.y) {
      const y0 = a.y + NH, dip = y0 + 22;
      d = `M${ax},${y0} C${ax},${dip} ${bx},${dip} ${bx},${y0 + 2}`;
    } else {
      const y0 = a.y, y1 = b.y + NH + 2, my = (y0 + y1) / 2;
      d = `M${ax},${y0} C${ax},${my} ${bx},${my} ${bx},${y1}`; // pointing up: only an import the change stops
    }
    const tip = `${short(im.from)} ${kind === 'added' ? 'starts importing' : kind === 'removed' ? 'stops importing' : 'imports'} ${short(im.to)}`;
    const line = svg('path', { d, class: `g-edge ${kind}`, 'stroke-width': kind === 'impact' ? 1.25 : 2, 'marker-end': `url(#ly-${kind})` }, svg('title', {}, tip));
    el.append(line);
    edgeEls.set(line, im);
    links.get(im.from).push(line);
    links.get(im.to).push(line);
  }

  const nodeEls = new Map();
  for (const [pkg, n] of nodes) {
    const p = n.p;
    const cls = p.before < 0 ? 'new' : p.after < 0 ? 'gone' : p.changes ? 'center' : '';
    const moved = p.before >= 0 && p.after >= 0 && p.before !== p.after;
    const g = svg('g', {
      class: `g-node ly-node ${cls}${moved ? ' moved' : ''}${state.focus === pkg ? ' picked' : ''}`,
      transform: `translate(${n.x},${n.y})`, tabindex: 0, role: 'button', 'aria-label': `${pkg}, ${layerNote(p)}`,
    },
    svg('title', {}, `${pkg}\nlayer ${p.before < 0 ? '–' : p.before} at the merge base, ${p.after < 0 ? '–' : p.after} at the head\n${layerNote(p)}`),
    svg('rect', { width: NW, height: NH, rx: 6 }),
    svg('text', { x: 9, y: 16 }, cut(short(pkg), 26)),
    svg('text', { x: 9, y: 31, class: `badge${moved ? ' move' : ''}` }, layerNote(p)));
    const lit = (on) => {
      el.classList.toggle('tracing', on);
      g.classList.toggle('hot', on);
      for (const line of links.get(pkg)) {
        line.classList.toggle('hot', on);
        const im = edgeEls.get(line);
        nodeEls.get(im.from === pkg ? im.to : im.from).classList.toggle('hot', on);
      }
    };
    g.addEventListener('mouseenter', () => lit(true));
    g.addEventListener('mouseleave', () => lit(false));
    g.addEventListener('focus', () => lit(true));
    g.addEventListener('blur', () => lit(false));
    g.addEventListener('click', () => pick(pkg));
    g.addEventListener('keydown', (ev) => { if (ev.key === 'Enter' || ev.key === ' ') { ev.preventDefault(); pick(pkg); } });
    el.append(g);
    nodeEls.set(pkg, g);
  }
  box.replaceChildren(h('p', { class: 'lv-lead' }, layersSummary(L)), el);
}

// --- The change in parts -------------------------------------------------------
//
// The change drawn function by function, at three levels, since a change of
// a few hundred lines touches too many functions to read as one graph. The
// review splits them into parts (review.Part): the whole change shows those
// parts around a ring, a chord for the calls between two of them, beside
// the calls between packages and a card for each part; a part shows its
// functions around a ring, the rest of the change folded into single nodes,
// and as a matrix whose rows are its call tree; a function shows every call
// in and out of it, the chains that reach it, and its flow. Tests are left
// out, as in the graph of packages.

const FN_KIND = { added: 'new', deleted: 'removed', signature: 'signature', renamed: 'renamed', moved: 'moved', body: 'edited' };

const isFunc = (c) => c.symbol === 'func' || c.symbol === 'method';
const fnKey = (pkg, name) => `${pkg}|${name}`;
// bare drops the package name from a qualified name: "(*Server).Handle".
const bare = (name) => name.slice(name.indexOf('.') + 1);
const CALL_LABEL = { impact: 'calls', kept: 'keeps calling', new: 'starts calling', gone: 'stops calling' };
const CALL_COLOR = { impact: '--muted', kept: '--body', new: '--added', gone: '--deleted' };
const stmtsOf = (c) => (c.kind === 'deleted' ? c.stmtsBefore : c.stmtsAfter);
const cut = (s, n) => (s.length > n ? s.slice(0, n - 1) + '…' : s);

// callModel works out the calls around the review's changed functions: a
// node for every changed function or method outside tests, and for the
// unchanged code calling them or that they start or stop calling; an edge
// for each call into what changed ("impact"), each call between changed
// functions that stays ("kept"), and each call the change starts ("new") or
// stops ("gone").
function callModel(rv) {
  const nodes = new Map(), edges = new Map(), byOld = new Map(), changed = [];
  const node = (pkg, name, more) => {
    const key = fnKey(pkg, name);
    let n = nodes.get(key);
    if (!n) nodes.set(key, n = { key, pkg, name, links: [], ...more });
    return n;
  };
  rv.changes.forEach((c, i) => {
    if (!isFunc(c) || isTestFile(c)) return;
    const n = node(c.package, c.name, { change: i, kind: c.kind, path: c.path, line: c.line });
    if (n.change !== i) return; // a second init of the package
    changed.push(n);
    if (c.oldName) byOld.set(c.oldName, n);
  });
  const find = (pkg, name) => nodes.get(fnKey(pkg, name)) || byOld.get(name);
  const edge = (a, b, kind) => {
    const key = `${a.key}>${b.key}`;
    let e = edges.get(key);
    if (!e) {
      edges.set(key, e = { a, b, kind, weight: 0, approx: true });
      a.links.push(e);
      b.links.push(e);
    }
    return e;
  };
  for (const n of changed) {
    for (const ce of rv.changes[n.change].callees || []) {
      const t = find(ce.package, ce.name) || node(ce.package, ce.name, { path: ce.path, line: ce.line });
      if (t === n) continue;
      const e = edge(n, t, ce.started ? 'new' : 'gone');
      e.weight += ce.uses;
      e.approx = e.approx && ce.approximate;
    }
  }
  for (const n of changed) {
    for (const cl of rv.changes[n.change].callers) {
      if (cl.test) continue;
      const s = find(cl.package, cl.name) || node(cl.package, cl.name, { path: cl.path, line: cl.line });
      if (s === n || edges.has(`${s.key}>${n.key}`)) continue; // itself, or a call its callees say started
      const e = edge(s, n, s.change != null ? 'kept' : 'impact');
      e.weight += cl.calls;
      e.approx = e.approx && cl.approximate;
    }
  }
  return { changed, edges: [...edges.values()], unchanged: [...nodes.values()].filter((n) => n.change == null) };
}

// partsView returns the review's parts with the calls around them, worked
// out once a review: partOf gives the part of each changed function by its
// change index.
function partsView() {
  const rv = state.rv;
  if (rv.partsView) return rv.partsView;
  const model = callModel(rv);
  const parts = (rv.parts || []).map((p, i) => ({ ...p, i }));
  const partOf = new Map();
  for (const p of parts) for (const f of p.functions) partOf.set(f.change, p.i);
  const byChange = new Map(model.changed.map((n) => [n.change, n]));
  return (rv.partsView = { model, parts, partOf, byChange });
}

// groupOf returns the part of a node, or 'U' for unchanged code.
const groupOf = (pv, n) => (n.change != null && pv.partOf.has(n.change) ? pv.partOf.get(n.change) : 'U');
const partName = (p) => (p.loose ? 'Loose' : bare(state.rv.changes[p.hub].name));
const partLabel = (p) => (p.loose ? 'Loose' : `${p.i + 1} · ${partName(p)}`);

// partFacts sums a part up: its size, kinds and packages, the calls inside
// it, and the calls into it and out of it by group.
function partFacts(pv, p) {
  const cs = p.functions.map((f) => state.rv.changes[f.change]);
  const count = (m, k, n = 1) => m.set(k, (m.get(k) || 0) + n);
  const kinds = new Map(), pkgs = new Map(), into = new Map(), outOf = new Map();
  const callers = new Set(), callees = new Set();
  let inside = 0;
  for (const c of cs) {
    count(kinds, c.kind);
    count(pkgs, c.package);
  }
  for (const e of pv.model.edges) {
    const a = groupOf(pv, e.a), b = groupOf(pv, e.b);
    if (a === p.i && b === p.i) inside++;
    else if (b === p.i) {
      count(into, a);
      if (a === 'U') callers.add(e.a);
    } else if (a === p.i) {
      count(outOf, b);
      if (b === 'U') callees.add(e.b);
    }
  }
  return {
    n: cs.length, stmts: sum(cs, stmtsOf), kinds, inside, into, outOf, callers: [...callers], callees: [...callees],
    pkgs: [...pkgs].sort((x, y) => y[1] - x[1]).map(([k]) => k),
  };
}

// openLevel shows the change in parts, part (an index into the parts) when
// not null, and the function at change index fn of it when not null.
function openLevel(part, fn) {
  state.part = part;
  state.fn = fn;
  if (state.graphView !== 'functions') setGraphView('functions');
  else drawGraph();
}

// upLevel goes up a level, reporting whether there was one.
function upLevel() {
  if (state.fn != null) openLevel(state.part, null);
  else if (state.part != null) openLevel(null, null);
  else return false;
  return true;
}

function drawLevels() {
  const pv = partsView(), box = $('graphBox');
  if (!pv.parts.length) {
    box.replaceChildren(h('p', { class: 'empty muted' }, 'No function or method changed outside tests.'));
    return;
  }
  if (state.part != null && !pv.parts[state.part]) Object.assign(state, { part: null, fn: null });
  if (state.fn != null && pv.partOf.get(state.fn) !== state.part) state.fn = null;
  const body = state.fn != null ? fnLevel(pv, state.fn) : state.part != null ? partLevel(pv, pv.parts[state.part]) : wholeLevel(pv);
  box.replaceChildren(levelNav(pv), body);
}

// levelNav is the way back up the levels, and to the part or function
// before and after the one on show.
function levelNav(pv) {
  const crumb = (label, go, on) => (on
    ? h('span', { class: 'lv-crumb on', 'aria-current': 'page' }, label)
    : h('button', { type: 'button', class: 'lv-crumb', onclick: go }, label));
  const sep = () => h('span', { class: 'lv-sep', 'aria-hidden': 'true' }, '›');
  const items = [crumb('Whole change', () => openLevel(null, null), state.part == null)];
  let prev = null, next = null;
  if (state.part != null) {
    const p = pv.parts[state.part];
    items.push(sep(), crumb(partLabel(p), () => openLevel(p.i, null), state.fn == null));
    if (state.fn != null) {
      items.push(sep(), crumb(bare(state.rv.changes[state.fn].name), null, true));
      const k = p.functions.findIndex((f) => f.change === state.fn);
      const at = (f) => f && [bare(state.rv.changes[f.change].name), () => openLevel(p.i, f.change)];
      prev = at(p.functions[k - 1]);
      next = at(p.functions[k + 1]);
    } else {
      const at = (q) => q && [partLabel(q), () => openLevel(q.i, null)];
      prev = at(pv.parts[p.i - 1]);
      next = at(pv.parts[p.i + 1]);
    }
  }
  const step = (x, back) => (x ? h('button', { type: 'button', class: 'lv-step', onclick: x[1] }, back ? `‹ ${x[0]}` : `${x[0]} ›`) : null);
  return h('nav', { class: 'lv-nav', 'aria-label': 'Levels' }, ...items, h('span', { class: 'lv-steps' }, step(prev, true), step(next, false)));
}

const at = (cx, cy) => (a, r) => [cx + r * Math.cos(a), cy + r * Math.sin(a)];
const pt = (p) => p.map((v) => v.toFixed(1)).join(' ');

// --- Level 1: the whole change ---------------------------------------------------

function wholeLevel(pv) {
  const ring = partsRing(pv);
  const totals = { fns: pv.model.changed.length, unchanged: pv.model.unchanged.length };
  const cards = pv.parts.map((p) => partCard(pv, p, ring));
  return h('div', { class: 'lv lv-whole' },
    h('div', { class: 'lv-col' },
      h('p', { class: 'lv-lead' }, `${plural(totals.fns, 'function')} changed, split by the calls between them into ${plural(pv.parts.filter((p) => !p.loose).length, 'part')} to read one at a time`),
      ring, packageMatrix(pv)),
    h('div', { class: 'lv-col lv-cards' }, h('p', { class: 'sub-h' }, 'Parts, in reading order'), ...cards));
}

// partsRing draws the parts around a ring, an arc each as long as its
// functions are many, the unchanged code around them as one more, and a
// chord for the calls between two, as wide as they are many. Pointing at a
// part lights its chords; picking it opens it.
function partsRing(pv) {
  const groups = pv.parts.map((p) => ({ id: p.i, n: p.functions.length, p }));
  if (pv.model.unchanged.length) groups.push({ id: 'U', n: pv.model.unchanged.length });
  const links = new Map();
  for (const e of pv.model.edges) {
    const a = groupOf(pv, e.a), b = groupOf(pv, e.b);
    if (a === b) continue;
    const k = `${a}|${b}|${e.kind}`;
    const l = links.get(k) || { a, b, kind: e.kind, n: 0 };
    l.n++;
    links.set(k, l);
  }
  const W = 680, H = 520, CX = 340, CY = 260, R0 = 168, R1 = 188, GAP = groups.length > 1 ? 0.06 : 0;
  const on = at(CX, CY);
  const unit = (2 * Math.PI - GAP * groups.length) / sum(groups, (g) => g.n);
  const span = new Map();
  let a = -Math.PI / 2 + GAP / 2;
  for (const g of groups) {
    span.set(g.id, [a, a + g.n * unit]);
    a += g.n * unit + GAP;
  }
  const ends = new Map(), cursor = new Map();
  for (const l of links.values()) {
    ends.set(l.a, (ends.get(l.a) || 0) + l.n);
    ends.set(l.b, (ends.get(l.b) || 0) + l.n);
  }
  const take = (id, n) => {
    const [s0, s1] = span.get(id), pad = (s1 - s0) * 0.08;
    const from = cursor.has(id) ? cursor.get(id) : s0 + pad;
    const to = from + ((s1 - s0 - 2 * pad) * n) / ends.get(id);
    cursor.set(id, to);
    return [from, to];
  };
  const name = (id) => (id === 'U' ? 'unchanged code' : partLabel(pv.parts[id]));
  // Labels go to the largest arcs first, each where it overlaps none placed
  // before it; the others show theirs when pointed at.
  const boxes = [];
  for (const g of [...groups].sort((x, y) => y.n - x.n)) {
    const [s0, s1] = span.get(g.id), mid = (s0 + s1) / 2, [x, y] = on(mid, R1 + 14);
    const w = Math.max(cut(name(g.id), 28).length * 7, 90), right = Math.cos(mid) >= 0;
    const b = [right ? x : x - w, y - 16, right ? x + w : x, y + 16];
    g.labelled = boxes.every((o) => b[2] < o[0] || b[0] > o[2] || b[3] < o[1] || b[1] > o[3]);
    if (g.labelled) boxes.push(b);
  }
  const el = svg('svg', { class: 'ring parts', viewBox: `0 0 ${W} ${H}`, role: 'img', 'aria-label': 'The parts of the change and the calls between them' });
  const ribbons = [];
  for (const l of [...links.values()].sort((x, y) => y.n - x.n)) {
    const [a0, a1] = take(l.a, l.n), [b0, b1] = take(l.b, l.n);
    const d = `M${pt(on(a0, R0))} A${R0} ${R0} 0 0 1 ${pt(on(a1, R0))} Q${CX} ${CY} ${pt(on(b0, R0))} A${R0} ${R0} 0 0 1 ${pt(on(b1, R0))} Q${CX} ${CY} ${pt(on(a0, R0))} Z`;
    const r = svg('path', { d, class: `rb ${l.kind}` }, svg('title', {}, `${name(l.a)} ${CALL_LABEL[l.kind]} ${name(l.b)}: ${plural(l.n, 'call')}`));
    r.dataset.a = l.a;
    r.dataset.b = l.b;
    ribbons.push(r);
    el.append(r);
  }
  const arcs = new Map();
  for (const g of groups) {
    const [s0, s1] = span.get(g.id), large = s1 - s0 > Math.PI ? 1 : 0;
    const d = `M${pt(on(s0, R1))} A${R1} ${R1} 0 ${large} 1 ${pt(on(s1, R1))} L${pt(on(s1, R0))} A${R0} ${R0} 0 ${large} 0 ${pt(on(s0, R0))} Z`;
    const mid = (s0 + s1) / 2, [lx, ly] = on(mid, R1 + 14), right = Math.cos(mid) >= 0;
    const anchor = right ? 'start' : 'end';
    const arc = svg('g', {
      class: `arc${g.id === 'U' ? ' quiet' : ''}${g.p && g.p.loose ? ' loose' : ''}${g.labelled ? '' : ' unlabelled'}`,
      tabindex: g.id === 'U' ? -1 : 0, role: g.id === 'U' ? 'presentation' : 'button',
    },
      svg('title', {}, `${name(g.id)}: ${plural(g.n, 'function')}`),
      svg('path', { d }),
      svg('text', { x: lx.toFixed(1), y: (ly - 2).toFixed(1), 'text-anchor': anchor, class: 'arc-name' }, cut(name(g.id), 28)),
      svg('text', { x: lx.toFixed(1), y: (ly + 12).toFixed(1), 'text-anchor': anchor, class: 'arc-sub' },
        g.id === 'U' ? `${g.n} around them` : `${plural(g.n, 'function')}`));
    arcs.set(g.id, arc);
    if (g.id !== 'U') {
      arc.setAttribute('aria-label', `${name(g.id)}, ${plural(g.n, 'function')}`);
      arc.addEventListener('click', () => openLevel(g.id, null));
      arc.addEventListener('keydown', (ev) => { if (ev.key === 'Enter' || ev.key === ' ') { ev.preventDefault(); openLevel(g.id, null); } });
    }
    arc.addEventListener('mouseenter', () => el.lit(g.id));
    arc.addEventListener('mouseleave', () => el.lit(null));
    el.append(arc);
  }
  el.append(svg('text', { x: CX, y: CY - 4, 'text-anchor': 'middle', class: 'ring-n' }, String(pv.model.changed.length)),
    svg('text', { x: CX, y: CY + 14, 'text-anchor': 'middle', class: 'arc-sub' }, 'functions changed'));
  el.lit = (id) => {
    el.classList.toggle('tracing', id != null);
    for (const r of ribbons) r.classList.toggle('hot', id != null && (r.dataset.a === String(id) || r.dataset.b === String(id)));
    for (const [gid, arc] of arcs) arc.classList.toggle('hot', gid === id);
  };
  return el;
}

// packageMatrix counts the calls between packages: a row per package
// calling the columns, the diagonal staying inside one.
function packageMatrix(pv) {
  const changed = new Map();
  for (const n of pv.model.changed) changed.set(n.pkg, (changed.get(n.pkg) || 0) + 1);
  const pkgs = [...changed.keys()].sort((a, b) => changed.get(b) - changed.get(a) || a.localeCompare(b));
  for (const p of [...new Set(pv.model.unchanged.map((n) => n.pkg))].sort()) if (!changed.has(p)) pkgs.push(p);
  const cells = new Map();
  for (const e of pv.model.edges) {
    const k = `${e.a.pkg}|${e.b.pkg}`;
    const c = cells.get(k) || { n: 0, kinds: {} };
    c.n++;
    c.kinds[e.kind] = (c.kinds[e.kind] || 0) + 1;
    cells.set(k, c);
  }
  const most = Math.max(1, ...[...cells.values()].map((c) => c.n));
  const ORDER = ['new', 'kept', 'gone', 'impact'];
  const WORD = { new: 'new', kept: 'kept', gone: 'stopped', impact: 'from unchanged code' };
  const rows = pkgs.map((a) => h('tr', {}, h('th', { class: 'pm-row', title: a }, short(a)),
    pkgs.map((b) => {
      const c = cells.get(`${a}|${b}`);
      if (!c) return h('td', { class: a === b ? 'diag' : '' });
      const top = ORDER.reduce((x, k) => ((c.kinds[k] || 0) > (c.kinds[x] || 0) ? k : x), 'new');
      return h('td', {
        class: `on ${top}`, style: `--w: ${(0.2 + 0.6 * (c.n / most)).toFixed(2)}`,
        title: `${short(a)} → ${short(b)}: ${ORDER.filter((k) => c.kinds[k]).map((k) => `${c.kinds[k]} ${WORD[k]}`).join(' · ')}`,
      }, c.n);
    })));
  return h('div', { class: 'pm' },
    h('p', { class: 'sub-h' }, 'Calls between packages', h('span', { class: 'muted small' }, ' · row calls column, the diagonal stays inside one')),
    h('div', { class: 'scroll' }, h('table', { class: 'pm-table' },
      h('thead', {}, h('tr', {}, h('th', {}), pkgs.map((p) => h('th', { class: 'pm-col', title: p }, h('span', {}, short(p)))))),
      h('tbody', {}, rows))));
}

const KIND_ORDER = ['added', 'body', 'signature', 'renamed', 'moved', 'deleted'];

// kindBar is a bar of a part's changes by kind.
function kindBar(kinds) {
  return h('span', { class: 'kind-bar', 'aria-hidden': 'true' },
    KIND_ORDER.filter((k) => kinds.get(k)).map((k) => h('i', { class: `m-${k}`, style: `flex: ${kinds.get(k)}` })));
}

// names lists a few names of nodes: "a, b and 3 more".
function names(ns, few = 3) {
  const xs = ns.slice(0, few).map((n) => bare(n.name));
  const more = ns.length - xs.length;
  return more > 0 ? `${xs.join(', ')} and ${more} more` : xs.length > 1 ? `${xs.slice(0, -1).join(', ')} and ${xs.at(-1)}` : xs.join('');
}

function partCard(pv, p, ring) {
  const f = partFacts(pv, p);
  const kinds = KIND_ORDER.filter((k) => f.kinds.get(k)).map((k) => `${f.kinds.get(k)} ${FN_KIND[k]}`).join(' · ');
  const parts = (m) => [...m.keys()].filter((g) => g !== 'U').sort((x, y) => x - y).map((g) => g + 1);
  const links = [];
  if (p.loose) {
    links.push('linked to no other changed function: called through an interface, only from tests, or not at all');
  } else {
    if (f.callers.length) links.push(`called from ${names(f.callers)}`);
    const by = parts(f.into), to = parts(f.outOf);
    if (by.length) links.push(`called by ${by.join(', ')}`);
    if (to.length) links.push(`calls into ${to.join(', ')}`);
    if (f.callees.length) links.push(`calls ${plural(f.callees.length, 'unchanged function')}`);
    if (!links.length) links.push('no other part calls it, and it calls none');
  }
  const card = h('button', { type: 'button', class: `part-card${p.loose ? ' loose' : ''}`, onclick: () => openLevel(p.i, null) },
    h('span', { class: 'pc-head' },
      h('span', { class: 'pc-num' }, p.loose ? '·' : String(p.i + 1)),
      h('b', { class: 'pc-name', title: p.loose ? '' : state.rv.changes[p.hub].name }, partName(p)),
      h('span', { class: 'pc-pkgs code muted' }, f.pkgs.map(short).join(' · ')),
      h('span', { class: 'pc-open' }, 'Open ›')),
    h('span', { class: 'pc-stats' }, `${plural(f.n, 'function')} · ${f.stmts.toLocaleString()} stmts`, kindBar(f.kinds), h('span', { class: 'muted' }, kinds)),
    h('span', { class: `pc-links small${p.loose ? ' pc-warn' : ''}` }, links.join(' · ')));
  card.addEventListener('mouseenter', () => ring.lit(p.i));
  card.addEventListener('mouseleave', () => ring.lit(null));
  card.addEventListener('focus', () => ring.lit(p.i));
  card.addEventListener('blur', () => ring.lit(null));
  return card;
}

// --- Level 2: one part ------------------------------------------------------------

// partGraph folds the calls around a part: its own functions stay, each
// other part becomes one node and so does the unchanged code of each
// package.
function partGraph(pv, p) {
  const members = new Set(p.functions.map((f) => f.change));
  const nodes = new Map(), edges = new Map();
  const end = (n) => {
    let key, more;
    if (n.change != null && members.has(n.change)) {
      key = `f${n.change}`;
      more = { change: n.change, label: bare(n.name), kind: n.kind };
    } else {
      const g = groupOf(pv, n);
      key = g === 'U' ? `u${n.pkg}` : `p${g}`;
      more = g === 'U' ? { label: short(n.pkg), quiet: true } : { label: partLabel(pv.parts[g]), part: g };
      more.folded = true;
    }
    let x = nodes.get(key);
    if (!x) nodes.set(key, x = { key, names: new Set(), calls: false, called: false, ...more });
    if (x.folded) x.names.add(bare(n.name));
    return x;
  };
  for (const e of pv.model.edges) {
    const ina = e.a.change != null && members.has(e.a.change), inb = e.b.change != null && members.has(e.b.change);
    if (!ina && !inb) continue;
    const a = end(e.a), b = end(e.b);
    if (a.folded) a.calls = true;
    if (b.folded) b.called = true;
    const k = `${a.key}>${b.key}`;
    const x = edges.get(k) || { a, b, kind: e.kind, n: 0, weight: 0 };
    if (e.kind === 'new' || (e.kind === 'kept' && x.kind !== 'new')) x.kind = e.kind;
    x.n++;
    x.weight += e.weight;
    edges.set(k, x);
  }
  const fns = p.functions.map((f) => nodes.get(`f${f.change}`) || { key: `f${f.change}`, change: f.change, label: bare(state.rv.changes[f.change].name), kind: state.rv.changes[f.change].kind });
  const folded = [...nodes.values()].filter((x) => x.folded);
  return { fns, into: folded.filter((x) => x.calls), outOf: folded.filter((x) => !x.calls), edges: [...edges.values()] };
}

function partLevel(pv, p) {
  const f = partFacts(pv, p), g = partGraph(pv, p);
  const kinds = KIND_ORDER.filter((k) => f.kinds.get(k)).map((k) => `${f.kinds.get(k)} ${FN_KIND[k]}`).join(', ');
  return h('div', { class: 'lv lv-part' },
    h('p', { class: 'lv-lead' }, `${plural(f.n, 'function')} · ${f.stmts.toLocaleString()} stmts · ${kinds} · `, h('span', { class: 'code' }, f.pkgs.map(short).join(', ')),
      ' · the rest of the change folded into single nodes'),
    h('div', { class: 'lv-row' },
      h('div', { class: 'lv-col' }, partRing(p, g)),
      h('div', { class: 'lv-col lv-wide' }, treeMatrix(p, g), partEnds(g))));
}

// partRing draws a part's functions around a ring in reading order, what
// calls into it before them and what it calls after, each folded node a
// square. Pointing at a node lights its calls; picking a function opens it.
function partRing(p, g) {
  const items = [...g.into, null, ...g.fns, null, ...g.outOf].filter((x, i, xs) => x || (i > 0 && xs[i - 1]));
  if (items.at(-1) === null) items.pop();
  if (items.length > 1) items.push(null);
  const R = Math.max(150, Math.min(260, items.length * 6.5)), S = 2 * (R + 150), C = S / 2;
  const on = at(C, C), step = (2 * Math.PI) / items.length;
  const angle = new Map();
  items.forEach((x, i) => { if (x) angle.set(x.key, -Math.PI / 2 + i * step); });
  // Too tight for every label, a node shows its own when it or a neighbour
  // is pointed at.
  const crowded = (2 * Math.PI * R) / items.length < 13;
  const el = svg('svg', { class: `ring part${crowded ? ' crowded' : ''}`, viewBox: `0 0 ${S} ${S}`, role: 'img', 'aria-label': `The functions of ${partLabel(p)} and the calls around them` });
  const defs = svg('defs', {});
  for (const [kind, color] of Object.entries(CALL_COLOR)) {
    defs.append(svg('marker', { id: `pr-${kind}`, viewBox: '0 0 10 10', refX: 9, refY: 5, markerWidth: 7, markerHeight: 7, markerUnits: 'userSpaceOnUse', orient: 'auto' },
      svg('path', { d: 'M0,1 L10,5 L0,9 z', style: `fill: var(${color})` })));
  }
  el.append(defs);
  const lines = new Map();
  for (const e of g.edges) {
    const a = angle.get(e.a.key), b = angle.get(e.b.key);
    const outer = e.a.folded || e.b.folded;
    const d = `M${pt(on(a, R - (e.a.folded ? 10 : 7)))} C${pt(on(a, R * (outer ? 0.2 : 0.45)))} ${pt(on(b, R * (outer ? 0.2 : 0.45)))} ${pt(on(b, R - (e.b.folded ? 11 : 9)))}`;
    const line = svg('path', { d, class: `g-edge ${e.kind}`, 'stroke-width': (1.25 + Math.min(2.5, Math.log2(e.n))).toFixed(1), 'marker-end': `url(#pr-${e.kind})` },
      svg('title', {}, `${e.a.label} ${CALL_LABEL[e.kind]} ${e.b.label}${e.n > 1 ? `: ${plural(e.n, 'call')}` : ''}`));
    lines.set(e, line);
    el.append(line);
  }
  const marks = new Map();
  for (const x of items) {
    if (!x) continue;
    const a = angle.get(x.key), [cx, cy] = on(a, R), [lx, ly] = on(a, R + (x.folded ? 14 : 10));
    const right = Math.cos(a) >= -1e-9, deg = (a * 180) / Math.PI;
    const shape = x.folded
      ? svg('rect', { x: (cx - 7).toFixed(1), y: (cy - 7).toFixed(1), width: 14, height: 14, rx: 3, class: x.quiet ? 'fold quiet' : 'fold' })
      : svg('circle', { cx: cx.toFixed(1), cy: cy.toFixed(1), r: 5, class: `dot fk-${x.kind}` });
    const text = svg('text', {
      x: lx.toFixed(1), y: (ly + 4).toFixed(1), 'text-anchor': right ? 'start' : 'end',
      transform: `rotate(${(right ? deg : deg + 180).toFixed(1)} ${lx.toFixed(1)} ${ly.toFixed(1)})`,
      class: x.folded ? 'fold-name' : 'fn-name',
    }, cut(x.label, 26));
    const tip = x.folded ? `${x.label}: ${[...x.names].slice(0, 12).join(', ')}${x.names.size > 12 ? ', …' : ''}` : state.rv.changes[x.change].name;
    const mark = svg('g', { class: `rn${x.folded ? '' : ' pick'}`, tabindex: x.folded && x.part == null ? -1 : 0, role: 'button', 'aria-label': tip }, svg('title', {}, tip), shape, text);
    const go = () => (x.folded ? x.part != null && openLevel(x.part, null) : openLevel(p.i, x.change));
    mark.addEventListener('click', go);
    mark.addEventListener('keydown', (ev) => { if (ev.key === 'Enter' || ev.key === ' ') { ev.preventDefault(); go(); } });
    mark.addEventListener('mouseenter', () => lit(x));
    mark.addEventListener('mouseleave', () => lit(null));
    marks.set(x.key, mark);
    el.append(mark);
  }
  const lit = (x) => {
    el.classList.toggle('tracing', !!x);
    for (const m of marks.values()) m.classList.remove('hot');
    for (const [e, line] of lines) {
      const hot = !!x && (e.a === x || e.b === x);
      line.classList.toggle('hot', hot);
      if (hot) {
        marks.get(e.a.key).classList.add('hot');
        marks.get(e.b.key).classList.add('hot');
      }
    }
    if (x) marks.get(x.key).classList.add('hot');
  };
  return el;
}

const CALL_MARK = { new: '+', kept: '•', gone: '−', impact: '•' };

// treeMatrix is the part as a matrix: a row per function in reading order,
// indented as its call tree, each calling the columns it marks, the folded
// nodes it calls last; and how often what is outside calls in.
function treeMatrix(p, g) {
  const cols = [...g.fns, ...g.into.filter((x) => x.called), ...g.outOf];
  const cell = new Map(g.edges.map((e) => [`${e.a.key}>${e.b.key}`, e]));
  const fromOutside = new Map();
  for (const e of g.edges) if (e.a.folded && !e.b.folded) fromOutside.set(e.b.key, (fromOutside.get(e.b.key) || 0) + e.weight);
  const depth = new Map(p.functions.map((f) => [`f${f.change}`, f.depth]));
  const head = h('tr', {}, h('th', { class: 'corner' }, 'row calls column'), h('th', { class: 'tm-from', title: 'calls from outside the part' }, '←'),
    cols.map((c, j) => h('th', { class: `tm-col${j === g.fns.length ? ' gap' : ''}${c.folded ? ' folded' : ''}`, title: c.folded ? c.label : state.rv.changes[c.change].name }, h('span', {}, cut(c.label, 22)))));
  const rows = g.fns.map((r, i) => {
    const c = state.rv.changes[r.change];
    return h('tr', {},
      h('th', { class: 'tm-row' }, h('button', {
        type: 'button', style: `padding-left: ${6 + depth.get(r.key) * 12}px`, title: `${c.name} · ${FN_KIND[c.kind] || c.kind} · ${stmtsOf(c)} stmts`,
        onclick: () => openLevel(p.i, r.change),
      }, mark(c.kind), h('span', { class: 'tm-name' }, r.label), h('span', { class: 'muted' }, stmtsOf(c)))),
      h('td', { class: 'tm-from' }, fromOutside.get(r.key) ? `×${fromOutside.get(r.key)}` : ''),
      cols.map((col, j) => {
        const e = cell.get(`${r.key}>${col.key}`);
        const cls = [j === g.fns.length ? 'gap' : '', i === j ? 'diag' : '', e ? `on ${e.kind}` : ''].filter(Boolean).join(' ');
        return h('td', { class: cls, title: e ? `${r.label} ${CALL_LABEL[e.kind]} ${col.label}${e.n > 1 ? `: ${plural(e.n, 'function')}` : ''} ×${e.weight}` : null }, e ? CALL_MARK[e.kind] : '');
      }));
  });
  return h('div', { class: 'tm' },
    h('p', { class: 'sub-h' }, 'The part, in reading order', h('span', { class: 'muted small' }, ' · indented by who calls whom; pick a function to read it')),
    h('div', { class: 'scroll' }, h('table', { class: 'tm-table' }, h('thead', {}, head), h('tbody', {}, rows))));
}

// partEnds says where a part is entered from and what outside it it calls.
function partEnds(g) {
  const line = (cls, word, xs) => (xs.length ? h('p', {}, h('span', { class: cls }, word), ' ',
    ...xs.flatMap((x, i) => [i ? ' · ' : '', h('b', {}, x.label), ': ', h('span', { class: 'code' }, [...x.names].slice(0, 6).join(', ') + (x.names.size > 6 ? ', …' : ''))])) : null);
  return h('div', { class: 'lv-ends small' },
    line('in', '← entered from', g.into) || h('p', { class: 'muted' }, 'Nothing outside tests calls into it.'),
    line('out', '→ calls', g.outOf));
}

// --- Level 3: one function -----------------------------------------------------------

function fnLevel(pv, i) {
  const rv = state.rv, c = rv.changes[i], n = pv.byChange.get(i), p = pv.parts[pv.partOf.get(i)];
  const k = p.functions.findIndex((f) => f.change === i);
  const paths = pathsOf(i);
  const outline = h('div', { class: 'lv-outline' },
    h('p', { class: 'sub-h' }, partLabel(p), h('span', { class: 'muted small' }, ` · ${k + 1} of ${p.functions.length}`)),
    p.functions.map((f) => {
      const fc = rv.changes[f.change];
      return h('button', {
        type: 'button', class: `ol-row${f.change === i ? ' on' : ''}`, style: `padding-left: ${6 + f.depth * 12}px`,
        title: `${fc.name} · ${FN_KIND[fc.kind] || fc.kind} · ${stmtsOf(fc)} stmts`, 'aria-current': f.change === i ? 'true' : null,
        onclick: () => openLevel(p.i, f.change),
      }, mark(fc.kind), h('span', { class: 'ol-name' }, bare(fc.name)), h('span', { class: 'muted' }, stmtsOf(fc)));
    }));
  const card = h('div', { class: 'lv-fn' },
    h('div', { class: 'lv-fn-head' }, mark(c.kind), h('span', { class: `k k-${c.symbol}` }, c.symbol), h('code', { class: 'lv-fn-name' }, c.name),
      h('span', { class: 'muted code' }, `${c.path}:${c.line}`), h('span', { class: 'muted small' }, c.summary),
      h('button', { type: 'button', class: 'lv-step', onclick: () => showChange(i) }, 'Read the change')),
    chains(n), bowtie(pv, n, p),
    paths ? h('div', { class: 'lv-flow' }, h('p', { class: 'sub-h' }, 'Its flow', h('span', { class: 'muted small' }, ' · where the calls above happen')), pathsView(paths)) : null);
  return h('div', { class: 'lv lv-row lv-function' }, outline, card);
}

// chains lists a few chains of calls reaching n from code the change leaves
// alone, through the changed functions calling it: "main › serve › here".
function chains(n) {
  const out = [];
  const walk = (x, path, seen) => {
    if (out.length >= 3) return;
    const ins = x.links.filter((e) => e.b === x && e.kind !== 'gone' && !seen.has(e.a));
    if (!ins.length || path.length >= 6) {
      if (x !== n) out.push([x, ...path]);
      return;
    }
    const top = ins.filter((e) => e.a.change == null).sort((p, q) => q.weight - p.weight)[0];
    if (top) out.push([top.a, x, ...path]);
    for (const e of ins) if (e.a.change != null) walk(e.a, [x, ...path], new Set([...seen, x]));
  };
  walk(n, [], new Set([n]));
  return h('div', { class: 'lv-chains' },
    h('p', { class: 'sub-h' }, 'Reached from'),
    out.length
      ? out.slice(0, 3).map((ch) => h('div', { class: 'chain code' }, ch.flatMap((m, j) => [
        j ? h('span', { class: 'muted' }, ' › ') : null,
        m === n ? h('b', {}, 'here') : h('span', { class: m.change == null ? 'muted' : '', title: m.name }, bare(m.name))])))
      : h('div', { class: 'muted small' }, 'Nothing outside tests calls it.'));
}

// bowtie draws every call into a function on its left and out of it on its
// right, the calls out grouped into those within its part, into other parts
// and into unchanged code. Picking a changed function opens it.
function bowtie(pv, n, p) {
  const ins = n.links.filter((e) => e.b === n).sort((x, y) => y.weight - x.weight);
  const outs = n.links.filter((e) => e.a === n);
  const where = (e) => (e.b.change == null ? 2 : groupOf(pv, e.b) === p.i ? 0 : 1);
  const groups = [['in this part', 0], ['other parts', 1], ['unchanged code', 2]]
    .map(([word, w]) => [word, outs.filter((e) => where(e) === w).sort((x, y) => (x.kind === 'new' ? 0 : 1) - (y.kind === 'new' ? 0 : 1) || y.weight - x.weight)])
    .filter(([, es]) => es.length);
  const ROW = 26;
  const right = [];
  for (const [word, es] of groups) {
    right.push({ label: `${word} · ${es.length}` });
    for (const e of es) right.push({ e, other: e.b });
  }
  const rows = Math.max(ins.length, right.length, 2), H = rows * ROW, mid = H / 2;
  const topL = (H - ins.length * ROW) / 2, topR = (H - right.length * ROW) / 2;
  const box = h('div', { class: 'bowtie', style: `height: ${H}px` });
  const lines = svg('svg', { class: 'bt-lines', viewBox: `0 0 1000 ${H}`, preserveAspectRatio: 'none', 'aria-hidden': 'true' });
  const curve = (x0, y0, x1, y1, e) => lines.append(svg('path', {
    d: `M${x0} ${y0} C${(x0 + x1) / 2} ${y0} ${(x0 + x1) / 2} ${y1} ${x1} ${y1}`, class: `g-edge ${e.kind}${e.approx ? ' approx' : ''}`,
    'stroke-width': (1.5 + Math.min(2, Math.log2(Math.max(1, e.weight)))).toFixed(1), 'vector-effect': 'non-scaling-stroke',
  }));
  box.append(lines);
  const chip = (e, other, side, y) => {
    const changed = other.change != null;
    const opens = changed && pv.partOf.has(other.change);
    const word = e.kind === 'impact' ? `×${e.weight}` : `${e.kind === 'new' ? 'starts' : e.kind === 'gone' ? 'stops' : 'kept'} ×${e.weight}`;
    const attrs = {
      class: `bt-chip ${side} ${changed ? `fk-${other.kind}` : 'ctx'}`, style: `top: ${y}px`,
      title: `${other.name}${other.path ? ` · ${other.path}:${other.line}` : ''}${e.approx ? ' · matched by method name only' : ''}`,
    };
    const kids = [h('span', { class: 'bt-name' }, other.pkg !== n.pkg && !changed ? `${short(other.pkg).split('/').pop()}.${bare(other.name)}` : bare(other.name)),
      e.approx ? h('span', { class: 'muted' }, 'by name') : null, h('span', { class: `bt-word ${e.kind}` }, word)];
    return opens ? h('button', { ...attrs, type: 'button', onclick: () => openLevel(pv.partOf.get(other.change), other.change) }, ...kids) : h('span', attrs, ...kids);
  };
  ins.forEach((e, j) => {
    const y = topL + j * ROW;
    box.append(chip(e, e.a, 'left', y + 3));
    curve(300, y + ROW / 2, 378, mid, e);
  });
  right.forEach((r, j) => {
    const y = topR + j * ROW;
    if (r.label) {
      box.append(h('span', { class: 'bt-group', style: `top: ${y + 6}px` }, r.label));
      return;
    }
    box.append(chip(r.e, r.other, 'right', y + 3));
    curve(622, mid, 700, y + ROW / 2, r.e);
  });
  const c = state.rv.changes[n.change];
  box.append(h('div', { class: `bt-center fk-${c.kind}`, style: `top: ${mid - 24}px` },
    h('b', {}, bare(c.name)), h('span', { class: 'muted small' }, `${FN_KIND[c.kind] || c.kind} · ${short(c.package)}`)));
  if (!ins.length) box.append(h('span', { class: 'bt-none left', style: `top: ${mid - 8}px` }, 'nothing outside tests calls it'));
  if (!outs.length) box.append(h('span', { class: 'bt-none right', style: `top: ${mid - 8}px` }, 'starts and stops no call, and keeps none to changed code'));
  return h('div', { class: 'lv-bowtie' },
    h('div', { class: 'bt-heads sub-h' }, h('span', {}, `Called by · ${ins.length}`), h('span', {}, `Calls · ${outs.length}`)), box);
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

  // The whole reading is for planning a split rather than reviewing the
  // change, so it is drawn only when asked for.
  const reading = h('details', { class: 'reading' },
    h('summary', {}, 'Affinity reading', h('span', { class: 'muted' }, ' · the matrix, its blocks, the callers, the names and the change coupling, and where to start a split')));
  reading.addEventListener('toggle', () => {
    if (reading.open && reading.children.length === 1) reading.append(...affinityReading(t));
  });
  body.append(reading);
  card.append(body);
  return card;
}

// affinityReading draws a type's whole affinity reading, after the change
// with a switch to before it.
function affinityReading(t) {
  const out = [];
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
    out.push(seg);
  }
  out.push(view);
  show(t.after ? 'after' : 'before');
  return out;
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

// impact lists the code using what changed: production code, and the
// tests too when asked for.
function impact() {
  const rv = state.rv, b = rv.blast, scoped = rv.scope.length > 0;
  const p = reach(production(b.callers)), tests = reach(inTests(b.callers));
  $('outsideToggle').hidden = !scoped;
  $('testsToggle').hidden = tests.decls === 0;
  $('testsLabel').textContent = `with ${plural(tests.decls, 'test')}`;
  let note;
  if (p.decls) {
    note = `${plural(p.sites, 'call site')}${p.approx ? ' (approx.)' : ''} in ${plural(p.decls, 'declaration')}, ${plural(p.pkgs, 'package')}`;
    if (tests.decls) note += ` · ${tests.sites.toLocaleString()} more in tests`;
    if (scoped) note += ` · ${outsideCallers().sites.toLocaleString()} outside the scope`;
  } else {
    note = tests.decls ? `only tests use what it changed: ${plural(tests.sites, 'call site')}` : 'nothing outside the change uses what it changed';
  }
  $('impactNote').textContent = note;

  // Callers outside the scope first, tests last, then by uses.
  const first = (c) => (c.outside ? 0 : 1) + (c.test ? 2 : 0);
  let cs = [...focusedCallers()].sort((a, b) => first(a) - first(b) || b.calls - a.calls);
  if (!state.withTests) cs = cs.filter((c) => !c.test);
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

const isTestFile = (c) => c.path.endsWith('_test.go') || (c.oldPath || '').endsWith('_test.go');

// changes lists the changes in three groups: the edits of existing
// production code in full, in the order "Look here first" ranks them; the
// new code by file; and the changes to tests. The last two are folded
// away, since the merge request's diff shows them whole.
function changes() {
  const rv = state.rv, f = state.focus;
  const shown = rv.changes.map((c, i) => ({ c, i }))
    .filter(({ c }) => !f || c.package === f || c.callers.some((cl) => cl.package === f));
  if (!shown.length) {
    $('changes').replaceChildren(h('p', { class: 'muted small' }, 'No changes to list.'));
    return;
  }
  const rank = new Map((rv.focus || []).filter((x) => x.change >= 0).map((x, n) => [x.change, n]));
  const existing = shown.filter(({ c }) => !isTestFile(c) && c.kind !== 'added')
    .sort((a, b) => (rank.get(a.i) ?? Infinity) - (rank.get(b.i) ?? Infinity) || a.i - b.i);
  const added = shown.filter(({ c }) => !isTestFile(c) && c.kind === 'added');
  const tests = shown.filter(({ c }) => isTestFile(c));

  const out = [h('p', { class: 'sub-h' }, `Existing code it edits · ${existing.length}`)];
  if (existing.length) {
    const cards = limit('changes', existing).map(({ c, i }) => changeCard(c, i));
    out.push(...cards, more('changes', cards.length, existing.length) || '');
  } else {
    out.push(h('p', { class: 'muted small' }, 'None: it only adds code and changes tests.'));
  }

  if (added.length) {
    const byFile = groupBy(added, ({ c }) => c.path);
    out.push(h('p', { class: 'sub-h' }, `New code · ${plural(added.length, 'symbol')} in ${plural(byFile.size, 'file')}`));
    for (const [path, xs] of byFile) {
      out.push(h('details', { class: 'group' },
        h('summary', {}, h('code', {}, path), h('span', { class: 'muted small' }, ` · ${xs.length} new: ${symbolCounts(xs)}`)),
        h('div', { class: 'rows' }, xs.map(({ c, i }) => changeRow(c, i, `:${c.line}`)))));
    }
  }

  if (tests.length) {
    const files = new Set(tests.map(({ c }) => c.path)).size;
    out.push(h('details', { class: 'group tests' },
      h('summary', {}, h('span', { class: 'sub-h' }, 'Tests'), h('span', { class: 'muted small' }, ` · ${plural(tests.length, 'change')} in ${plural(files, 'file')}`)),
      h('div', { class: 'rows' }, tests.map(({ c, i }) => changeRow(c, i, `${c.path}:${c.line}`)))));
  }
  $('changes').replaceChildren(...out);
}

// groupBy groups xs by key, in the order the keys first appear.
function groupBy(xs, key) {
  const out = new Map();
  for (const x of xs) {
    const k = key(x);
    out.set(k, [...(out.get(k) || []), x]);
  }
  return out;
}

// symbolCounts counts changes by kind of declaration: "19 consts, 6 funcs".
function symbolCounts(xs) {
  const n = groupBy(xs, ({ c }) => c.symbol);
  return [...n].sort((a, b) => b[1].length - a[1].length).map(([s, ys]) => plural(ys.length, s)).join(', ');
}

// changeRow is a change in one line, for new code and tests. A new
// function says how much it decides, and opens on its flow as an outline.
function changeRow(c, i, where) {
  const p = pathsOf(i);
  const line = [mark(c.kind), h('span', { class: 'k' }, c.symbol), h('code', {}, c.name),
    h('span', { class: 'muted small' }, p ? p.verdict : c.summary), h('span', { class: 'muted code' }, where), connections(i)];
  if (!p || !p.hunks.length) return h('div', { class: 'row', id: `ch-${i}` }, ...line);
  return h('details', { class: 'row flow-outline', id: `ch-${i}` }, h('summary', {}, ...line), flowView(p));
}

// --- Paths ---------------------------------------------------------------------

// pathsOf returns what the change at index i does to its function's paths,
// or undefined when it has none.
function pathsOf(i) {
  const rv = state.rv;
  if (!rv.pathsBy) rv.pathsBy = new Map((rv.paths || []).map((p) => [p.change, p]));
  return rv.pathsBy.get(i);
}

const STEP_MARK = { same: '', renested: '~', edited: '±', added: '+', removed: '−', moved: '→', movedEdited: '→' };
const EXIT_MARK = { same: '', new: '+', gone: '−', changed: '~' };

// verdictLine is a change's verdict on its paths, coloured by its kind.
function verdictLine(p, cls) {
  return h('div', { class: `verdict v-${p.kind} ${cls || ''}` }, p.verdict);
}

// pathsView is what an edit does to its function's paths: the verdict, the
// changed steps cut into hunks, and the ways out when one changed.
function pathsView(p) {
  const out = [verdictLine(p)];
  if (p.hunks.length) out.push(flowView(p));
  if (p.exits.length) {
    out.push(h('div', { class: 'exits' }, h('p', { class: 'sub-h' }, 'Ways out'),
      h('ul', {}, p.exits.map((e) => h('li', { class: `ex e-${e.mark}` },
        h('span', { class: 'st-mark' }, EXIT_MARK[e.mark]), h('code', {}, e.what),
        e.when.length ? h('span', { class: 'when' }, ` ⇐ ${e.when.join(' ∧ ')}`) : null)))));
  }
  return h('div', { class: 'paths' }, out);
}

// flowView lists the steps of a compared flow to show, one line each,
// indented by depth, with a line for the unchanged steps a hunk skips and
// the decisions enclosing it.
function flowView(p) {
  const out = [];
  for (const hk of p.hunks) {
    if (hk.skipped) out.push(h('div', { class: 'st st-skip' }, `… ${plural(hk.skipped, 'unchanged step')}`));
    if (hk.breadcrumb.length) out.push(h('div', { class: 'st st-crumb' }, `in ${hk.breadcrumb.join(' › ')}`));
    for (const s of hk.steps) out.push(stepLine(s));
  }
  return h('div', { class: 'flow' }, out);
}

function stepLine(s) {
  let note = null;
  if (s.mark === 'renested') note = 're-nested';
  else if (s.mark === 'moved') note = `in ${s.to}`;
  else if (s.mark === 'movedEdited') note = `in ${s.to}`;
  const was = s.old ? h('div', { class: 'was' }, h('span', { class: 'muted' }, 'was '), h('code', {}, s.old)) : null;
  return h('div', { class: `st s-${s.mark} k-${s.kind}` },
    h('span', { class: 'st-mark' }, STEP_MARK[s.mark] || ''),
    h('div', { class: 'st-body', style: `margin-left:${s.depth * 16}px` },
      h('code', { title: s.text }, s.text), note ? h('span', { class: 'st-note' }, ` ${note}`) : null, was));
}

// connections offers to open a changed function in the change in parts,
// with every call in and out of it, or returns null for other changes.
function connections(i) {
  const pv = partsView();
  if (!pv.partOf.has(i)) return null;
  return h('button', {
    type: 'button', class: 'conn', title: 'every call in and out of it, in the relations above',
    onclick: (ev) => {
      ev.preventDefault(); // inside a summary, it would open or close the row
      openLevel(pv.partOf.get(i), i);
      $('graphTitle').scrollIntoView({ behavior: 'smooth', block: 'start' });
    },
  }, 'Connections');
}

// changeCard is a change in full: where it is, what changed and whom it
// reaches, a type's changed members, and its callers, production code first.
function changeCard(c, i) {
  const where = c.oldPath && (c.oldPath !== c.path || c.oldName)
    ? `${c.path}:${c.line}, was ${c.oldName || c.name} at ${c.oldPath}:${c.oldLine}` : `${c.path}:${c.line}`;
  let detail = null;
  if (c.members && c.members.length) {
    detail = membersView(c.members);
  } else if (c.oldSignature && c.newSignature && c.oldSignature !== c.newSignature) {
    detail = h('div', { class: 'sig' }, h('div', { class: 'minus' }, '− ' + c.oldSignature), h('div', { class: 'plus' }, '+ ' + c.newSignature));
  }
  const prod = production(c.callers), tests = inTests(c.callers);
  const label = [prod.length ? plural(prod.length, 'caller') : '', tests.length ? `${tests.length.toLocaleString()} in tests` : ''].filter(Boolean).join(' · ');
  const callers = c.callers.length
    ? h('details', {}, h('summary', {}, label),
      h('ul', {}, [...prod, ...tests].slice(0, 50).map((cl) => h('li', {}, h('code', {}, cl.name), ' ',
        h('span', { class: 'muted code' }, `${cl.path}:${cl.line} ×${cl.calls}`),
        cl.outside ? h('span', { class: 'tag out' }, 'outside') : null,
        cl.test ? h('span', { class: 'tag' }, 'test') : null))))
    : null;
  return h('div', { class: 'change', id: `ch-${i}` },
    h('div', { class: 'change-head' }, mark(c.kind), h('span', { class: 'k' }, c.symbol), h('code', {}, c.name),
      h('span', { class: 'muted code' }, where), connections(i)),
    h('div', { class: 'meta' }, c.summary,
      c.apiChange ? h('span', { class: 'api' }, ' · API change') : null),
    detail, pathsOf(i) ? pathsView(pathsOf(i)) : null, callers);
}

// membersView lists a type's changed members, each with whose code it
// breaks, and the implementers an interface change breaks.
function membersView(ms) {
  let impls = [];
  const rows = ms.map((m) => {
    const notes = [];
    if (m.breaks) notes.push(h('span', { class: 'tag warn' }, `breaks ${m.breaks}`));
    if (m.uses) notes.push(h('span', { class: 'tag' }, `${plural(m.uses, 'use')}${m.approximate ? ' (approx.)' : ''}`));
    if (m.unkeyed) notes.push(h('span', { class: 'tag warn' }, `breaks ${plural(m.unkeyed, 'unkeyed literal')}`));
    if (m.implementers && m.implementers.length) impls = m.implementers; // every member shares the one list
    const decl = !m.oldDecl ? [h('span', { class: 'plus' }, `+ ${m.member} ${m.newDecl}`)]
      : !m.newDecl ? [h('span', { class: 'minus' }, `− ${m.member} ${m.oldDecl}`)]
        : [h('span', { class: 'minus' }, `− ${m.member} ${m.oldDecl}`), h('br'), h('span', { class: 'plus' }, `+ ${m.member} ${m.newDecl}`)];
    return h('div', { class: 'member' }, ...decl, ...notes);
  });
  const implList = impls.length
    ? h('div', { class: 'member muted' }, 'implemented by ', ...impls.flatMap((c, i) => [i ? ', ' : '', h('code', { title: `${c.path}:${c.line}` }, c.name), c.test ? ' (test)' : '']))
    : null;
  return h('div', { class: 'sig members' }, rows, implList);
}

function files() {
  const fs = state.rv.files;
  $('filesNote').textContent = `${plural(fs.length, 'file')} · +${sum(fs, (f) => f.added).toLocaleString()} −${sum(fs, (f) => f.removed).toLocaleString()}`;
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
for (const b of $('graphView').children) b.addEventListener('click', () => setGraphView(b.dataset.view));
// Escape goes up a level of the change in parts.
document.addEventListener('keydown', (ev) => {
  if (ev.key === 'Escape' && state.rv && state.graphView === 'functions' && !/^(INPUT|SELECT|TEXTAREA)$/.test(ev.target.tagName) && upLevel()) {
    ev.preventDefault();
  }
});
$('outsideOnly').addEventListener('change', (ev) => { state.outsideOnly = ev.target.checked; impact(); });
$('withTests').addEventListener('change', (ev) => { state.withTests = ev.target.checked; impact(); });
window.addEventListener('popstate', () => { readURL(); run(); });
let resizing = 0;
window.addEventListener('resize', () => {
  clearTimeout(resizing);
  resizing = setTimeout(() => { if (state.rv) drawGraph(); }, 150);
});

try { setGraphView(localStorage.getItem(GRAPH_KEY)); } catch { /* the graph of packages */ }
readURL();
api('/api/status').then((st) => { $('repo').textContent = st.repo; }, () => {});
loadOptions($('head').value.trim());
run();
