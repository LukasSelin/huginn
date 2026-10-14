'use strict';

const RANK = Object.fromEntries(KINDS.map(([k], i) => [k, i]));
const ICON = { type: 'T', func: 'f', method: 'm', var: 'v', const: 'c' };

// Kinds of semantic change on the commit screen, in the report's display
// order.
const COMMIT_KINDS = [
  ['signature', 'Signature'], ['added', 'Added'], ['body', 'Body edit'],
  ['renamed', 'Renamed'], ['moved', 'Moved'], ['deleted', 'Deleted'],
];
const COMMIT_LABEL = Object.fromEntries(COMMIT_KINDS);

const state = {
  status: null, dirs: [], dir: null, file: null, sel: null, syms: [], symbol: null,
  // Commit screen: the revision in the URL, its report and the revision it
  // was fetched for, the last commit shown (for the Commit tab), and the
  // filters, which reset per commit.
  commit: null, report: null, reportRev: null, lastCommit: null, kinds: new Set(), commitFile: null,
};

// ---- navigation (kept in the URL hash so views can be linked) ----

function readHash() {
  const p = new URLSearchParams(location.hash.slice(1));
  return { dir: p.get('d'), file: p.get('f'), sel: p.get('s') ? Number(p.get('s')) : null, commit: p.get('c') };
}

// hashFor returns the URL hash of the current view changed by next. The
// symbol view's parameters are kept on the commit screen, so that going back
// to Symbols returns to where the reader was.
function hashFor(next) {
  const v = { dir: state.dir, file: state.file, sel: state.sel, commit: state.commit, ...next };
  const p = new URLSearchParams();
  if (v.dir) p.set('d', v.dir);
  if (v.file) p.set('f', v.file);
  if (v.sel) p.set('s', v.sel);
  if (v.commit) p.set('c', v.commit);
  return '#' + p.toString();
}

function go(next) {
  location.hash = hashFor(next);
}

async function route() {
  const want = readHash();
  state.commit = want.commit;
  showView(!!want.commit);
  if (want.commit) {
    await routeCommit(want.commit);
    return;
  }
  if (!want.dir || !state.dirs.some((d) => d.dir === want.dir)) {
    // Default to the directory with the most history.
    const busiest = state.dirs.reduce((a, b) => (b.changes > a.changes ? b : a), state.dirs[0]);
    if (!busiest) return;
    go({ dir: busiest.dir, file: null, sel: null, commit: null });
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

function showView(commit) {
  $('app').hidden = commit;
  $('commitView').hidden = !commit;
  $('navSymbols').toggleAttribute('aria-current', !commit);
  $('navCommit').toggleAttribute('aria-current', commit);
  $(commit ? 'navCommit' : 'navSymbols').setAttribute('aria-current', 'page');
  document.title = commit ? 'huginn · commit' : 'huginn · symbols';
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
      [...s.events].reverse().map((e) => h('a', { class: 'ev', href: hashFor({ commit: e.sha }), title: 'Open the semantic diff of this commit' },
        h('div', { class: 'rail' }, mark(e.kind), h('span', { class: 'bar' })),
        h('div', { class: 'body' },
          h('span', { class: 'meta' }, `${label[e.kind] || e.kind} · ${fmtDate(e.t)} · `, h('span', { class: 'mono' }, e.sha.slice(0, 7)), ` · ${e.author}`),
          h('span', { class: 'text' }, e.summary))))));
}

// ---- commit screen ----

async function routeCommit(rev) {
  if (!state.report || rev !== state.reportRev) {
    $('commitHead').replaceChildren(h('p', { class: 'muted' }, `Computing the semantic diff of ${rev.slice(0, 12)}…`));
    for (const id of ['commitFiles', 'chips', 'cards', 'blast']) $(id).replaceChildren();
    $('fileCount').textContent = '';
    $('crumbs').replaceChildren(
      h('button', { type: 'button', onclick: () => go({ commit: null }) }, state.status.repo), h('span', {}, '›'),
      h('span', { class: 'here' }, `commit ${rev.slice(0, 12)}`));
    let rep;
    try {
      rep = await api(`/api/commit/${encodeURIComponent(rev)}`);
    } catch (err) {
      if (state.commit === rev) $('commitHead').replaceChildren(h('p', { class: 'muted' }, String(err)));
      return;
    }
    if (state.commit !== rev) return; // navigated away meanwhile
    state.report = rep;
    state.reportRev = rev;
    state.lastCommit = rep.sha;
    state.kinds = new Set();
    state.commitFile = null;
  }
  renderCommit();
}

const fmtDateTime = (t) => new Date(t * 1000).toLocaleString(undefined, {
  year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit',
});
const dots = (kinds) => h('span', { class: 'dots' }, kinds.map((k) => h('span', { class: `dot m-${k}`, title: COMMIT_LABEL[k] })));
const loc = (path, line) => `${path}:${line}`;

function renderCommit() {
  const r = state.report;
  $('crumbs').replaceChildren(
    h('button', { type: 'button', onclick: () => go({ commit: null }) }, state.status.repo), h('span', {}, '›'),
    h('span', { class: 'here' }, `commit ${r.sha.slice(0, 7)}`));
  renderCommitHead(r);
  renderCommitFiles(r);
  renderChips(r);
  renderCards(r);
  renderBlast(r);
}

function renderCommitHead(r) {
  const body = r.message.slice(r.subject.length).trim();
  const navBtn = (rev, label, aria) => (rev
    ? h('a', { class: 'btn', href: hashFor({ commit: rev }), 'aria-label': aria, title: rev.slice(0, 12) }, label)
    : h('span', { class: 'btn', 'aria-disabled': 'true', 'aria-label': aria }, label));
  $('commitHead').replaceChildren(
    h('div', { class: 'commit-meta' },
      h('div', { class: 'stack' },
        h('span', { class: 'eyebrow' }, 'Semantic commit diff'),
        h('h1', {}, r.subject || '(no message)'),
        h('span', { class: 'muted small' },
          h('span', { class: 'mono sha', title: r.sha }, r.sha.slice(0, 12)), ` · ${r.author} · ${fmtDateTime(r.t)}`,
          r.parents.length > 1 ? ` · merge of ${r.parents.length} parents` : ''),
        body && h('details', { class: 'msg' }, h('summary', { class: 'muted small' }, 'Full message'), h('pre', {}, body))),
      h('nav', { class: 'commit-nav', 'aria-label': 'Neighboring commits' },
        navBtn(r.nav.prev, '← Older', 'Previous commit'),
        navBtn(r.nav.next, 'Newer →', 'Next commit'))),
    h('p', { class: 'headline' },
      h('span', { class: 'plus' }, `+${r.added.toLocaleString()}`), ' ',
      h('span', { class: 'minus' }, `−${r.removed.toLocaleString()}`), ' lines → ',
      h('strong', {}, plural(r.semanticChanges, 'semantic change')),
      h('span', { class: 'muted' }, ` across ${plural(r.symbols, 'symbol')}`)),
    r.warnings ? h('p', { class: 'muted small' }, `⚠ ${r.warnings.join(' · ')}`) : '');
}

function renderCommitFiles(r) {
  $('fileCount').textContent = `${r.files.length} changed`;
  const row = (f) => {
    const on = state.commitFile === f.path;
    return h('button', {
      type: 'button', class: 'node file', 'aria-pressed': String(on), title: f.oldPath ? `${f.oldPath} → ${f.path}` : f.path,
      onclick: () => { state.commitFile = on ? null : f.path; renderCommit(); },
    }, dots(f.kinds), h('span', { class: 'lbl' }, f.path),
      h('span', { class: 'n' }, h('span', { class: 'plus' }, `+${f.added}`), ' ', h('span', { class: 'minus' }, `−${f.removed}`)));
  };
  $('commitFiles').replaceChildren(
    h('button', {
      type: 'button', class: 'node file', 'aria-pressed': String(!state.commitFile),
      onclick: () => { state.commitFile = null; renderCommit(); },
    }, h('span', { class: 'lbl' }, 'All files'), h('span', { class: 'n' }, r.changes.length)),
    ...(r.files.length ? r.files.map(row) : [h('p', { class: 'muted small' }, 'No Go files with semantic changes.')]));
}

function renderChips(r) {
  const chip = (key, label, n) => {
    const on = key ? state.kinds.has(key) : state.kinds.size === 0;
    return h('button', {
      type: 'button', class: 'chip', 'aria-pressed': String(on),
      onclick: () => {
        if (!key) state.kinds.clear();
        else if (!state.kinds.delete(key)) state.kinds.add(key);
        renderCommit();
      },
    }, key && h('span', { class: `dot m-${key}` }), label, h('span', { class: 'n' }, n));
  };
  $('chips').replaceChildren(
    chip(null, 'All', r.semanticChanges),
    ...COMMIT_KINDS.filter(([k]) => r.counts[k]).map(([k, l]) => chip(k, l, r.counts[k])));
}

function sigBlock(cls, sign, text) {
  return h('code', { class: `sig ${cls}` }, h('span', { class: 'sign', 'aria-hidden': 'true' }, sign), text);
}

function callerRows(cs) {
  return cs.map((c) => h('li', { class: 'caller' },
    h('span', { class: 'mono cname' }, c.name),
    h('span', { class: 'muted small mono' }, loc(c.path, c.line)),
    h('span', { class: 'tags' },
      h('span', { class: 'calls' }, `×${c.calls}`),
      c.approximate && h('span', { class: 'tag', title: 'Matched by method name only' }, 'approx.'),
      c.test && h('span', { class: 'tag' }, 'test'))));
}

function card(c) {
  let sig;
  if (c.kinds.includes('signature') && c.oldSignature) sig = [sigBlock('sig-old', '−', c.oldSignature), sigBlock('sig-new', '+', c.newSignature)];
  else if (c.kind === 'added') sig = sigBlock('sig-new', '+', c.newSignature);
  else if (c.kind === 'deleted') sig = sigBlock('sig-old', '−', c.oldSignature);
  else sig = sigBlock('', ' ', c.newSignature);
  const was = c.oldPath && (c.oldPath !== c.path || c.oldName)
    ? ` · was ${c.oldName || c.name} at ${loc(c.oldPath, c.oldLine)}` : '';
  return h('article', { class: `card c-${c.kind}` },
    h('header', { class: 'card-head' },
      h('span', { class: `badge b-${c.kind}` }, COMMIT_LABEL[c.kind]),
      c.kinds.filter((k) => k !== c.kind).map((k) => h('span', { class: `badge minor b-${k}` }, COMMIT_LABEL[k])),
      h('span', { class: 'muted small' }, c.symbol),
      h('span', { class: 'cname mono' }, c.name),
      c.exported && h('span', { class: 'tag' }, 'exported')),
    h('span', { class: 'muted small mono' }, loc(c.path, c.line), was),
    sig,
    c.summary && h('span', { class: 'summary mono' }, c.summary),
    h('span', { class: `impact${c.apiChange ? ' warn' : ''}` }, c.impact, c.apiChange ? ' · API change' : ''),
    c.callers.length > 0 && h('details', { class: 'callers' },
      h('summary', { class: 'small muted' }, plural(c.callers.length, 'caller')),
      h('ul', { class: 'caller-list' }, callerRows(c.callers))));
}

function renderCards(r) {
  const f = state.commitFile;
  const shown = r.changes.filter((c) => (!f || c.path === f || c.oldPath === f)
    && (state.kinds.size === 0 || c.kinds.some((k) => state.kinds.has(k))));
  let empty = 'No changes match the filters.';
  if (!r.changes.length) empty = r.noise.length ? 'No semantic changes: every Go file touched is noise.' : 'No Go symbols changed in this commit.';
  $('cards').replaceChildren(...(shown.length ? shown.map(card) : [h('div', { class: 'panel empty' }, empty)]));
}

function renderBlast(r) {
  const b = r.blast;
  const stat = (n, label) => h('div', { class: 'stat' }, h('strong', {}, n), h('span', { class: 'muted small' }, label));
  const LIMIT = 12;
  $('blast').replaceChildren(
    h('section', { class: 'stack' },
      h('h2', { class: 'eyebrow' }, 'Blast radius'),
      h('div', { class: 'stats' },
        stat(`${b.callSites.toLocaleString()}${b.approximate ? '~' : ''}`, b.approximate ? 'call sites (approx.)' : 'call sites'),
        stat(b.packages.length, 'packages affected'),
        stat(b.callers.length, 'calling declarations')),
      b.packages.length > 0 && h('ul', { class: 'pkgs mono small' }, b.packages.map((p) => h('li', {}, p)))),
    b.exportedApiChanged
      ? h('section', { class: 'api-warn', role: 'note' },
        h('strong', {}, '⚠ Exported API changed'),
        h('span', { class: 'small' }, 'Callers in other packages or modules may break.'),
        h('ul', { class: 'mono small' }, b.apiChanges.map((n) => h('li', {}, n))))
      : h('p', { class: 'muted small' }, 'No exported API changed.'),
    h('section', { class: 'stack' },
      h('h2', { class: 'eyebrow' }, 'Affected callers'),
      b.callers.length
        ? h('ul', { class: 'caller-list' }, callerRows(b.callers.slice(0, LIMIT)))
        : h('p', { class: 'muted small' }, 'No callers outside the change.'),
      b.callers.length > LIMIT && h('details', {},
        h('summary', { class: 'small muted' }, `${b.callers.length - LIMIT} more`),
        h('ul', { class: 'caller-list' }, callerRows(b.callers.slice(LIMIT))))),
    h('section', { class: 'stack' },
      h('h2', { class: 'eyebrow' }, 'Ignored as noise'),
      r.noise.length
        ? h('ul', { class: 'noise' }, r.noise.map((n) => h('li', {},
          h('span', { class: 'mono small' }, n.path),
          h('span', { class: 'muted small' }, `${n.reason} · +${n.added} −${n.removed}`))))
        : h('p', { class: 'muted small' }, 'Nothing.')));
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
  state.status = await waitReady();
  if (!state.status) return;
  const st = state.status;
  $('repo').textContent = `${st.repo} · ${st.commits.toLocaleString()} commits · ${st.live.toLocaleString()} live symbols`;
  state.dirs = await api('/api/dirs');
  $('dirList').replaceChildren(...state.dirs.map((d) => h('option', { value: d.dir }, `${d.live} live · ${d.changes} changes`)));
  $('dirInput').addEventListener('change', (e) => {
    if (state.dirs.some((d) => d.dir === e.target.value)) go({ dir: e.target.value, file: null, sel: null });
  });
  $('navSymbols').addEventListener('click', (e) => { e.preventDefault(); go({ commit: null }); });
  $('navCommit').addEventListener('click', (e) => { e.preventDefault(); go({ commit: state.lastCommit || 'HEAD' }); });
  $('loading').hidden = true;
  window.addEventListener('hashchange', () => route().catch(fail));
  await route();
}

boot().catch(fail);
