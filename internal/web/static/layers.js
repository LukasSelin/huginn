'use strict';

// The layers page: every live package stacked on a layer above the packages
// it refers to, frame by frame, drawn in 3D on a canvas. A package keeps its
// place on the journey map within its layer, so the view from above is the
// journey map and the view from the side is the architecture's layering.
// common.js provides h, $, api, plural, fmtDate, waitReady and fail;
// stack3d.js provides Stack3D, which draws the stack.
(() => {
  const PLAY_MS = 900; // per frame while playing
  const name = (p) => (p === '.' ? '(root)' : p);

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
    tipFor: -1, // the package the hover card describes
    find: '', // the search over package names, lower case
    found: [], // the packages it finds in the current frame, in list order
  };

  const stage = $('stage');
  const stack = Stack3D($('canvas'), {
    views: $('views'),
    onHover(p, x, y) {
      stage.classList.toggle('over', p >= 0);
      light(p);
      tip(p, x, y);
    },
    onPick(p) { pick(p === state.pick ? -1 : p); },
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
    state.tipFor = -1;
    if (state.pick >= 0 && !state.cur.at.has(state.pick)) state.pick = -1;
    if (state.hover >= 0 && !state.cur.at.has(state.hover)) state.hover = -1;
    stack.setLit(lit());
    overlay();
    list();
    picked();
  }

  function lit() { return state.pick >= 0 ? state.pick : state.hover; }

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
  const neighbours = (p) => stack.neighbours(p);

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

  // label is package p's name with the search marked in it, or null when
  // the search doesn't find it.
  function label(p) {
    const n = name(state.data.packages[p].path);
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

  function pick(p) {
    state.pick = p;
    state.tipFor = -1;
    for (const b of $('list').querySelectorAll('button')) b.setAttribute('aria-pressed', String(Number(b.dataset.p) === p));
    stack.setLit(lit());
    picked();
    caption();
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

  $('canvas').addEventListener('pointerleave', () => stage.classList.remove('over'));
  $('zoomIn').addEventListener('click', () => stack.zoomBy(1.25));
  $('zoomOut').addEventListener('click', () => stack.zoomBy(0.8));
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
    if (e.key === 'Escape') pick(-1);
    else if (e.key === 'ArrowLeft') { play(false); setFrame(state.frame - 1); }
    else if (e.key === 'ArrowRight') { play(false); setFrame(state.frame + 1); }
    else if (e.key === '+' || e.key === '=') stack.zoomBy(1.25);
    else if (e.key === '-') stack.zoomBy(0.8);
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
