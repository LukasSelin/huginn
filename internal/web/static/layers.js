'use strict';

// The layers page: every live package stacked on a layer above the packages
// it refers to, frame by frame, drawn in 3D on a canvas. A package keeps its
// place on the journey map within its layer, so the view from above is the
// journey map and the view from the side is the architecture's layering.
// Choosing one of a package's references, in its list or by its line in the
// view, opens that connection: the symbols on the one side that refer to the
// other, and what they use there.
// common.js provides h, $, api, plural, fmtDate, waitReady and fail;
// stack3d.js provides Stack3D, which draws the stack.
(() => {
  const PLAY_MS = 900; // per frame while playing
  const SHOWN = 30; // rows of a connection's list shown before the rest are asked for
  const LINKS_KEPT = 48; // /api/journey/links responses kept
  const name = (p) => (p === '.' ? '(root)' : p);
  const encDir = (d) => (d === '.' ? '' : d.split('/').map(encodeURIComponent).join('/'));
  const sameDep = (a, b) => !!a && !!b && a.from === b.from && a.to === b.to;
  const ordinal = (n) => {
    const s = ['th', 'st', 'nd', 'rd'], v = n % 100;
    return `${n}${s[(v - 20) % 10] || s[v] || s[0]}`;
  };

  const state = {
    step: 'week',
    data: null, // the /api/layers response for the current step
    cache: {}, // responses by step
    frame: 0,
    cur: null, // the current frame, unpacked by the stack
    playing: false,
    timer: null,
    hover: -1, // package under the pointer or a list entry
    pick: -1, // package picked by a click, kept across frames
    dep: null, // of its references the one chosen, { from, to }, kept across frames while it lasts
    hoverDep: null, // the reference under the pointer or a list entry
    depOpen: new Set(), // the lists of the chosen reference shown in full
    depLoad: null, // the latest read of the chosen reference's symbols, to drop the stale ones
    tipFor: '', // what the hover card describes: 'p<package>' or 'd<from>><to>'
    find: '', // the search over package names, lower case
    found: [], // the packages it finds in the current frame, in list order
  };

  const stage = $('stage');
  const stack = Stack3D($('canvas'), {
    views: $('views'),
    onHover(p, x, y, d) {
      stage.classList.toggle('over', p >= 0 || !!d);
      light(p);
      hoverDep(d);
      tip(p, d, x, y);
    },
    onPick(p) {
      if (p >= 0 && p === state.pick && state.dep) choose(null);
      else pick(p === state.pick ? -1 : p);
    },
    onPickDep(d) { choose(sameDep(d, state.dep) ? null : d); },
  });

  // ---- data ----

  async function load(step) {
    if (!state.cache[step]) state.cache[step] = await api(`/api/layers?step=${step}`);
    const data = state.cache[step];
    const t = state.data ? state.data.frames[state.frame]?.t : Infinity;
    state.step = step;
    state.data = data;
    stack.setData(data.packages, data.frames);
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
    state.cur = stack.setFrame(frames[state.frame]);
    $('frame').value = state.frame;
    state.tipFor = '';
    if (state.pick >= 0 && !state.cur.at.has(state.pick)) state.pick = -1;
    if (state.hover >= 0 && !state.cur.at.has(state.hover)) state.hover = -1;
    if (state.dep && (state.pick < 0 || !depOf(state.dep))) state.dep = null;
    if (state.hoverDep && !depOf(state.hoverDep)) state.hoverDep = null;
    stack.setLit(lit());
    stack.setChosen(shownDep());
    overlay();
    list();
    picked();
  }

  function lit() { return state.pick >= 0 ? state.pick : state.hover; }
  // shownDep is the reference drawn out in the view: the one pointed at,
  // else the one chosen.
  function shownDep() { return state.hoverDep || state.dep; }
  // depOf finds reference { from, to } in the current frame, or undefined.
  const depOf = (k) => state.cur.deps.find((d) => d.from === k.from && d.to === k.to);
  const pkgName = (p) => name(state.data.packages[p].path);

  // links reads the references into and out of package directory dir after
  // revision rev, once.
  const linkCache = new Map(); // "dir rev" → the response, promised
  function links(dir, rev) {
    const key = `${dir} ${rev}`;
    let p = linkCache.get(key);
    if (!p) {
      p = api(`/api/journey/links/${encDir(dir)}?rev=${rev}`);
      p.catch(() => linkCache.delete(key));
      linkCache.set(key, p);
      if (linkCache.size > LINKS_KEPT) linkCache.delete(linkCache.keys().next().value);
    }
    return p;
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
      ? `Showing the ${strongest.length} strongest of ${plain.toLocaleString()} references; point at a package for its own, or click a line for the symbols behind it.`
      : '';
  }

  // neighbours lists what package p refers to and what refers to it in the
  // current frame, strongest first.
  const neighbours = (p) => stack.neighbours(p);

  // tip shows the hover card on package p or, with none, on reference d.
  function tip(p, d, x, y) {
    const t = $('tip');
    const key = p >= 0 ? `p${p}` : d ? `d${d.from}>${d.to}` : '';
    if (!key) {
      t.hidden = true;
      state.tipFor = '';
      return;
    }
    if (key !== state.tipFor || t.hidden) {
      state.tipFor = key;
      if (p >= 0) fillTip(t, p);
      else fillDepTip(t, d);
    }
    t.hidden = false;
    const sw = stage.clientWidth, sh = stage.clientHeight;
    const tw = t.offsetWidth, th = t.offsetHeight;
    t.style.left = `${Math.min(x + 14, sw - tw - 8)}px`;
    t.style.top = `${Math.min(y + 14, sh - th - 8)}px`;
  }

  function fillTip(t, p) {
    const at = state.cur.at.get(p);
    const { out, into } = neighbours(p);
    const rows = [['Layer', `${at.layer} of ${state.cur.layers - 1}`], ['Live symbols', at.live.toLocaleString()],
      ['Refers to', plural(out.length, 'package')], ['Referred to by', plural(into.length, 'package')]];
    t.replaceChildren(...[
      h('div', { class: 'tip-name mono' }, pkgName(p)),
      h('dl', { class: 'tip-rows' }, rows.flatMap(([k, v]) => [h('dt', null, k), h('dd', null, v)])),
      state.cur.inCycle.has(p) && h('div', { class: 'tip-hint' }, 'In a cycle: some of what it refers to refers back to it.'),
      state.pick !== p && h('div', { class: 'tip-hint' }, 'Click to keep it lit'),
    ].filter(Boolean));
  }

  function fillDepTip(t, d) {
    const af = state.cur.at.get(d.from), at = state.cur.at.get(d.to);
    const rows = [['Layers', `${af?.layer ?? '–'} → ${at?.layer ?? '–'}`],
      ['Symbols referring', `${d.refs.toLocaleString()} of ${(af?.live ?? 0).toLocaleString()}`]];
    t.replaceChildren(...[
      h('div', { class: 'tip-name mono' }, pkgName(d.from), h('span', { class: 'tip-arrow' }, ' → '), pkgName(d.to)),
      h('dl', { class: 'tip-rows' }, rows.flatMap(([k, v]) => [h('dt', null, k), h('dd', null, v)])),
      d.cyc && h('div', { class: 'tip-hint' }, 'In a cycle: the two refer to each other, directly or through others.'),
      !sameDep(d, state.dep) && h('div', { class: 'tip-hint' }, 'Click for the symbols behind it'),
    ].filter(Boolean));
  }

  // refList lists references to open, naming each by its key end, 'to' or
  // 'from'.
  function refList(deps, key, cls) {
    return h('ul', { class: `refs ${cls}` }, deps.map((d) => {
      const q = d[key];
      const on = () => { light(q); hoverDep(d); };
      const off = () => { light(-1); hoverDep(null); };
      return h('li', null, h('button', {
        type: 'button', title: `${pkgName(d.from)} → ${pkgName(d.to)}: the symbols behind it`,
        onclick: () => choose(d), onmouseenter: on, onmouseleave: off, onfocus: on, onblur: off,
      },
      h('span', null, pkgName(q)),
      h('span', { class: 'n' }, `L${state.cur.at.get(q)?.layer ?? '–'} · ${plural(d.refs, 'symbol')}`)));
    }));
  }

  function picked() {
    if (state.dep) return connection();
    state.depLoad = null;
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
        h('span', { class: 'sel-name' }, pkgName(p)),
        h('button', { type: 'button', class: 'close', 'aria-label': 'Clear the choice (Escape)', onclick: () => pick(-1) }, '×')),
      h('div', { class: 'muted small' }, `Layer ${at.layer} · ${plural(at.live, 'live symbol')}`),
      state.cur.inCycle.has(p) && h('p', { class: 'cyc-note' }, 'Caught in a cycle: it and some of what it refers to refer to each other, so they share a layer. Go allows this only through tests or build variants.'),
      (out.length > 0 || into.length > 0) && h('p', { class: 'muted small' }, 'Choose a reference, here or by its line in the view, for the symbols behind it.'),
      h('h3', { class: 'eyebrow' }, `Refers to ${out.length}`),
      out.length ? refList(out, 'to', 'out') : h('p', { class: 'muted small' }, 'Nothing else in the repository: it sits on the bottom layer.'),
      h('h3', { class: 'eyebrow' }, `Referred to by ${into.length}`),
      into.length ? refList(into, 'from', 'in') : h('p', { class: 'muted small' }, 'Nothing in the repository.'),
    ].filter(Boolean));
  }

  // connection shows the chosen reference: where its two packages sit, how
  // much of the one refers to the other and, once read, the symbols that do
  // and what they use.
  function connection() {
    const d = depOf(state.dep);
    const { from, to } = d;
    const af = state.cur.at.get(from), at = state.cur.at.get(to);
    const back = depOf({ from: to, to: from });
    const { out } = neighbours(from);
    // Its rank among what from refers to, by symbols, and how many others
    // have as many.
    const rank = out.filter((x) => x.refs > d.refs).length + 1;
    const ties = out.filter((x) => x.refs === d.refs).length - 1;
    const end = (p) => h('button', { type: 'button', class: 'end', title: `Choose ${pkgName(p)}`, onclick: () => pick(p) }, pkgName(p));
    const facts = [
      ['Layers', !af || !at ? '–' : d.cyc ? `${af.layer} and ${at.layer}, in a cycle`
        : `${af.layer} → ${at.layer}, ${plural(af.layer - at.layer, 'layer')} down`],
      ['Symbols', `${d.refs.toLocaleString()} of the ${plural(af?.live ?? 0, 'live symbol')} in ${pkgName(from)} refer to it`],
      ['Strength', out.length === 1 ? 'the only package it refers to'
        : `${rank === 1 ? 'the' : `the ${ordinal(rank)}`} strongest${ties ? `, tied with ${plural(ties, 'other')},` : ''} of the ${out.length} packages it refers to`],
    ];
    // The symbols read for this reference stay up while a new frame's are
    // read, so that playing doesn't blink them away.
    const key = `${from}>${to}`;
    let body = $('conn');
    if (!body || body.dataset.dep !== key) {
      body = h('div', { id: 'conn', class: 'conn-body', 'data-dep': key }, h('p', { class: 'muted small' }, 'Reading the symbols…'));
    }
    $('pick').replaceChildren(...[
      h('div', { class: 'pick-head' },
        h('button', { type: 'button', class: 'back', title: 'Back to the package (Escape)', onclick: () => choose(null) }, `← ${pkgName(state.pick)}`),
        h('button', { type: 'button', class: 'close', 'aria-label': 'Clear the choice', onclick: () => pick(-1) }, '×')),
      h('div', { class: 'conn' }, end(from), h('span', { class: 'conn-arrow', 'aria-label': 'refers to' }, '→'), end(to)),
      h('dl', { class: 'facts' }, facts.flatMap(([k, v]) => [h('dt', null, k), h('dd', null, v)])),
      d.cyc && h('p', { class: 'cyc-note' }, back
        ? [`They refer to each other: ${pkgName(to)} refers back through ${plural(back.refs, 'symbol')}, so the two share a layer. `,
          h('button', { type: 'button', class: 'link', onclick: () => choose(back) }, 'Open the way back')]
        : `Caught in a cycle: ${pkgName(to)} reaches back to ${pkgName(from)} through other packages, so they share a layer.`),
      body,
    ].filter(Boolean));
    readConnection(body, d);
  }

  // readConnection reads the symbols behind reference d at the current
  // frame into body, unless another read starts first.
  async function readConnection(body, d) {
    const token = {};
    state.depLoad = token;
    const rev = state.data.frames[state.frame].total - 1;
    const fromDir = state.data.packages[d.from].path, toDir = state.data.packages[d.to].path;
    // While the frames go by, read only where they stop.
    if (!linkCache.has(`${fromDir} ${rev}`) || !linkCache.has(`${toDir} ${rev}`)) await new Promise((r) => setTimeout(r, 120));
    if (state.depLoad !== token) return;
    let fl, tl;
    try {
      [fl, tl] = await Promise.all([links(fromDir, rev), links(toDir, rev)]);
    } catch (err) {
      if (state.depLoad === token) body.replaceChildren(h('p', { class: 'muted small' }, `Could not read the symbols: ${err.message}`));
      return;
    }
    if (state.depLoad !== token) return;
    fillConnection(body, d, fl, tl);
  }

  // fillConnection lists what d's symbols use on the other side, how many
  // of them use each, and the symbols themselves; and, in a cycle, the
  // symbols referring back. fl and tl are the links of d's two packages.
  function fillConnection(body, d, fl, tl) {
    const fromDir = state.data.packages[d.from].path, toDir = state.data.packages[d.to].path;
    const uses = fl.out.filter((l) => l.to === toDir).sort((a, b) => b.refs - a.refs || a.name.localeCompare(b.name));
    const backs = fl.in.filter((l) => l.from === toDir).sort((a, b) => b.refs - a.refs || a.name.localeCompare(b.name));
    const theirs = new Map(tl.symbols.map((s) => [s.name, s]));
    const byName = new Map();
    for (const l of uses) for (const n of l.names) byName.set(n, (byName.get(n) || 0) + 1);
    const names = [...byName].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]));
    const total = uses.reduce((s, l) => s + l.refs, 0);

    const follow = (id) => `/journey#step=${state.step}&f=${state.frame}&sym=${id}`;
    const symName = (s, text) => (s
      ? h('a', { href: follow(s.id), title: `${text}: follow it on the journey` }, text)
      : h('span', { class: 'missing', title: `${text}: not a live symbol of its own` }, text));
    const kind = (k) => k && h('span', { class: `k k-${k}` }, k);
    const glow = (sel, on) => { for (const el of body.querySelectorAll(sel)) el.classList.toggle('hl', on); };
    const hover = (el, sel) => {
      el.addEventListener('mouseenter', () => glow(sel, true));
      el.addEventListener('mouseleave', () => glow(sel, false));
    };
    // rows lists the first SHOWN rows of a list, and the rest on asking.
    const rows = (id, items, row, cls) => {
      const ul = h('ul', { class: `conn-list ${cls}` }, (state.depOpen.has(id) ? items : items.slice(0, SHOWN)).map(row));
      if (state.depOpen.has(id) || items.length <= SHOWN) return ul;
      const more = h('button', { type: 'button', class: 'link more' }, `Show all ${items.length.toLocaleString()}`);
      more.addEventListener('click', () => {
        state.depOpen.add(id);
        ul.replaceChildren(...items.map(row));
        more.remove();
      });
      return [ul, more];
    };
    const nameRow = ([n, count]) => {
      const s = theirs.get(n);
      const li = h('li', { 'data-name': n }, h('span', null, symName(s, n), kind(s?.kind)), h('span', { class: 'n' }, plural(count, 'symbol')));
      hover(li, `.uses-of li[data-names~="${CSS.escape(n)}"]`);
      return li;
    };
    const symRow = (named) => (l) => {
      const li = h('li', { 'data-names': l.names.join(' ') },
        h('span', null, symName(l, l.name), kind(l.kind)),
        h('span', { class: 'n' }, plural(l.refs, 'ref')),
        h('span', { class: 'uses' }, `uses ${l.names.join(', ')}`));
      if (named) hover(li, l.names.map((n) => `.names li[data-name="${CSS.escape(n)}"]`).join(', '));
      return li;
    };

    body.replaceChildren(...[
      uses.length === 0 && h('p', { class: 'muted small' }, 'No symbols behind it could be read at this point of the history.'),
      uses.length > 0 && [
        h('p', { class: 'muted small' }, `They use ${plural(names.length, 'name')} from ${name(toDir)}, ${plural(total, 'time')} in all.`),
        h('h3', { class: 'eyebrow' }, `What it uses in ${name(toDir)} · ${names.length}`),
        rows('names', names, nameRow, 'names'),
        h('h3', { class: 'eyebrow' }, `Where ${name(fromDir)} uses it · ${uses.length}`),
        rows('uses', uses, symRow(true), 'uses-of'),
      ],
      backs.length > 0 && h('h3', { class: 'eyebrow' }, `Referring back from ${name(toDir)} · ${backs.length}`),
      backs.length > 0 && rows('back', backs, symRow(false), 'back'),
    ].filter(Boolean).flat(2));
  }

  // label is package p's name with the search marked in it, or null when
  // the search doesn't find it.
  function label(p) {
    const n = pkgName(p);
    const q = state.find;
    if (!q) return n;
    const i = n.toLowerCase().indexOf(q);
    return i < 0 ? null : [n.slice(0, i), h('mark', null, n.slice(i, i + q.length)), n.slice(i + q.length)];
  }

  function list() {
    const byLayer = [];
    for (const [p, at] of state.cur.at) (byLayer[at.layer] ||= []).push(p);
    const paths = state.data.packages;
    const q = state.find;
    const out = [];
    state.found = [];
    for (let l = byLayer.length - 1; l >= 0; l--) {
      const all = (byLayer[l] || []).sort((a, b) => paths[a].path.localeCompare(paths[b].path));
      const rows = all.map((p) => [p, label(p)]).filter(([, lb]) => lb);
      if (q && !rows.length) continue;
      state.found.push(...rows.map(([p]) => p));
      out.push(h('section', { class: 'layer' },
        h('h3', { class: 'eyebrow' }, h('span', null, `Layer ${l}`),
          h('span', { class: 'muted' }, q ? `${rows.length} of ${plural(all.length, 'package')}` : plural(all.length, 'package'))),
        h('ul', null, rows.map(([p, lb]) => h('li', null, h('button', {
          type: 'button', 'data-p': p, class: state.cur.inCycle.has(p) ? 'cyc' : null, 'aria-pressed': String(p === state.pick),
          onclick: () => pick(p === state.pick ? -1 : p), onmouseenter: () => light(p), onmouseleave: () => light(-1),
          onfocus: () => light(p), onblur: () => light(-1),
        }, h('span', null, lb), h('span', { class: 'n' }, state.cur.at.get(p).live.toLocaleString())))))));
    }
    $('list').replaceChildren(...out);
    findNote();
  }

  function findNote() {
    const n = state.found.length, all = state.cur.at.size;
    const note = !state.find ? ''
      : n === 0 ? `No package matches “${$('find').value.trim()}”.`
        : n === 1 ? `1 of ${plural(all, 'package')} matches · Enter picks it`
          : `${n} of ${plural(all, 'package')} match · Enter picks the first`;
    // Set only on a change, so playing the frames doesn't keep announcing it.
    if ($('findNote').textContent !== note) $('findNote').textContent = note;
  }

  function light(p) {
    if (state.hover === p) return;
    state.hover = p;
    for (const b of $('list').querySelectorAll('button')) b.classList.toggle('lit', Number(b.dataset.p) === p);
    stack.setLit(lit());
    caption();
  }

  // hoverDep draws reference d out in the view while it is pointed at, null
  // when none is.
  function hoverDep(d) {
    if (sameDep(d, state.hoverDep) || (!d && !state.hoverDep)) return;
    state.hoverDep = d ? { from: d.from, to: d.to } : null;
    stack.setChosen(shownDep());
  }

  // pick picks package p, -1 for none, and of its references dep, null for
  // none.
  function pick(p, dep = null) {
    if (!sameDep(dep, state.dep)) state.depOpen.clear();
    state.pick = p;
    state.dep = dep && { from: dep.from, to: dep.to };
    state.hoverDep = null;
    state.tipFor = '';
    for (const b of $('list').querySelectorAll('button')) b.setAttribute('aria-pressed', String(Number(b.dataset.p) === p));
    stack.setLit(lit());
    stack.setChosen(shownDep());
    picked();
    caption();
  }

  // choose opens reference d, null to go back to the package picked; when
  // the package picked is neither of d's, it picks the one referring.
  const choose = (d) => pick(d && state.pick !== d.from && state.pick !== d.to ? d.from : state.pick, d);

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

  $('canvas').addEventListener('pointerleave', () => stage.classList.remove('over'));
  $('zoomIn').addEventListener('click', () => stack.zoomBy(1.25));
  $('zoomOut').addEventListener('click', () => stack.zoomBy(0.8));
  $('zoomReset').addEventListener('click', () => stack.fit());
  const spread = spreadControl({ stage, more: $('spreadMore'), less: $('spreadLess'), onChange: (v) => stack.setSpread(v) });
  stack.setSpread(spread.value);
  for (const b of $('step').children) b.addEventListener('click', () => { play(false); load(b.dataset.v).catch(fail); });
  $('frame').addEventListener('input', (e) => { play(false); setFrame(Number(e.target.value)); });
  $('prev').addEventListener('click', () => { play(false); setFrame(state.frame - 1); });
  $('next').addEventListener('click', () => { play(false); setFrame(state.frame + 1); });
  $('play').addEventListener('click', () => play(!state.playing));
  const find = $('find');
  const setFind = (v) => {
    state.find = v.trim().toLowerCase();
    if (state.cur) list();
  };
  find.addEventListener('input', () => setFind(find.value));
  find.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && state.found.length) pick(state.found[0]);
    else if (e.key === 'Escape' && find.value) { find.value = ''; setFind(''); }
    else if (e.key === 'Escape') find.blur();
    else return;
    e.preventDefault();
  });
  document.addEventListener('keydown', (e) => {
    if (!state.data || e.target.closest?.('input, select, textarea') || e.metaKey || e.ctrlKey || e.altKey) return;
    if (e.key === 'Escape' && state.dep) choose(null);
    else if (e.key === 'Escape') pick(-1);
    else if (e.key === 'ArrowLeft') { play(false); setFrame(state.frame - 1); }
    else if (e.key === 'ArrowRight') { play(false); setFrame(state.frame + 1); }
    else if (e.key === '+' || e.key === '=') stack.zoomBy(1.25);
    else if (e.key === '-') stack.zoomBy(0.8);
    else if (e.key === '0') stack.fit();
    else if (e.key === '>') spread.by(1);
    else if (e.key === '<') spread.by(-1);
    else if (e.key === '/') find.focus();
    else return;
    e.preventDefault();
  });

  (async () => {
    const st = await waitReady();
    if (!st) return;
    $('repo').textContent = st.repo;
    $('loading').hidden = true;
    $('app').hidden = false;
    await load(state.step);
    stack.start();
  })().catch(fail);
})();
