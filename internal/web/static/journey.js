'use strict';

// The journey page: the history played back frame by frame, one bubble per
// package, with the story told alongside. In story mode playback pauses at
// key moments to show what the commit changed; a package can be opened to
// watch its symbols, and one symbol followed through renames and moves.
// common.js provides KINDS, h, $, mark, api, plural, waitReady and fail.
(() => {
  const ACT = ['#2A2418', '#4A3A1E', '#7A5A26', '#B07F33', '#F0A64A'];
  const BASE_MS = 1100; // per frame at 1×
  const PAUSE_MS = 3800; // per key moment at 1×, in story mode
  const PER_FRAME = 3; // key moments shown per frame while playing
  const TAG_MS = 1800; // how long event tags stay on a package's symbols
  const MAX_TAGS = 8; // event tags per frame
  const DAY = 86400;
  const reduced = matchMedia('(prefers-reduced-motion: reduce)');

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
    bubbles: [], // elements by package index
    lines: new Map(), // "a-b" → <line>
    story: true, // pause at key moments
    moments: [], // the story's key moments and the followed symbol's, by revision
    card: null, // the moment on the card
    reports: new Map(), // sha → promise of its /api/commit report
    views: new Map(), // "step dir keep" → /api/journey/package response
    drill: null, // the package looked into: { dir, pi, v, cur, names, at, dots, peak }
    follow: null, // the followed symbol: { id, sym, moments }
    switching: null, // the package a followed symbol is being chased into
    folDir: undefined, // the followed symbol's package at the last render
  };

  const fmtShort = (t) => new Date(t * 1000).toLocaleDateString(undefined, { month: 'short', day: 'numeric', timeZone: 'UTC' });
  const fmtBig = (t) => new Date(t * 1000).toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric', timeZone: 'UTC' });
  const base = (p) => (p === '.' ? '(root)' : p.split('/').pop());
  const dirOf = (p) => (p.includes('/') ? p.slice(0, p.lastIndexOf('/')) : '.');
  const encDir = (d) => (d === '.' ? '' : d.split('/').map(encodeURIComponent).join('/'));
  const shortName = (n) => n.slice(n.lastIndexOf('.') + 1);

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
    for (const b of $('step').querySelectorAll('button')) b.setAttribute('aria-pressed', String(b.dataset.v === step));
    let max = 1;
    for (const f of state.data.frames) for (const p of f.pkgs) max = Math.max(max, editsOf(p));
    state.maxEdits = max;
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
    state.bubbles = packages.map((p) => {
      const el = h('button', { type: 'button', class: 'bubble gone', tabindex: '-1', onclick: () => drill(p.path).catch(fail) },
        h('span', { class: 'bname' }, base(p.path)));
      wrap.append(el);
      return el;
    });
    $('deps').replaceChildren();
    state.lines = new Map();
    place();
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
    state.data.packages.forEach((p, i) => {
      const el = state.bubbles[i];
      el.style.left = `${x0 + p.x * size}px`;
      el.style.top = `${y0 + p.y * size}px`;
    });
    $('deps').setAttribute('viewBox', `0 0 ${W} ${H}`);
    for (const [key, line] of state.lines) setLine(line, key);
    const disc = $('disc');
    disc.style.left = `${x0 + 0.02 * size}px`;
    disc.style.top = `${y0 + 0.02 * size}px`;
    disc.style.width = disc.style.height = `${0.96 * size}px`;
    if (state.drill) placeDots();
    if (state.data.frames.length) render(false);
  }

  function setLine(line, key) {
    const [a, b] = key.split('-').map(Number);
    const { size, x0, y0 } = state.geom;
    const pa = state.data.packages[a], pb = state.data.packages[b];
    line.setAttribute('x1', x0 + pa.x * size);
    line.setAttribute('y1', y0 + pa.y * size);
    line.setAttribute('x2', x0 + pb.x * size);
    line.setAttribute('y2', y0 + pb.y * size);
  }

  function actStep(e) {
    if (e <= 0) return 0;
    const s = 1 + Math.floor((3.999 * Math.log1p(e)) / Math.log1p(state.maxEdits));
    return Math.min(4, Math.max(1, s));
  }

  function renderStage(f) {
    const { packages } = state.data;
    const { size } = state.geom;
    const now = new Map(f.pkgs.map((p) => [p.p, p]));
    const focus = state.card && state.card.pkg != null ? state.pathIdx.get(state.card.pkg) : undefined;
    const fol = followNow();
    const folPkg = fol.dir != null ? state.pathIdx.get(fol.dir) : undefined;
    packages.forEach((p, i) => {
      const el = state.bubbles[i];
      const s = now.get(i);
      const live = s ? s.live : 0;
      const a = actStep(s ? editsOf(s) : 0);
      const d = live > 0 ? Math.max(8, 2 * p.r * size * Math.sqrt(live / Math.max(1, p.peak))) : Math.max(8, p.r * size);
      el.style.width = el.style.height = `${d}px`;
      el.style.backgroundColor = ACT[a];
      el.className = `bubble a${a}${live > 0 ? '' : ' gone'}${a >= 3 ? ' busy' : ''}${d < 44 ? ' small' : ''}${i === focus ? ' focus' : ''}${i === folPkg ? ' followed' : ''}`;
      const label = live > 0 ? `${p.path} · ${plural(live, 'symbol')}${s && s.kinds ? ` · ${describeKinds(s.kinds)}` : ''}` : p.path;
      el.title = label;
      el.setAttribute('aria-label', `${label}. Look inside`);
      el.tabIndex = live > 0 && !state.drill ? 0 : -1;
    });

    // Dependencies; the ones the card's moment is about are lit up.
    const svg = $('deps');
    const want = new Set(), hot = new Set();
    for (const [a, b] of f.deps) {
      const key = `${Math.min(a, b)}-${Math.max(a, b)}`;
      want.add(key);
      if (focus !== undefined && (state.card.kind === 'signature' ? b === focus : a === focus || b === focus)) hot.add(key);
      let line = state.lines.get(key);
      if (!line) {
        line = document.createElementNS('http://www.w3.org/2000/svg', 'line');
        state.lines.set(key, line);
        setLine(line, key);
        svg.append(line);
      }
    }
    for (const [key, line] of state.lines) {
      line.classList.toggle('off', !want.has(key));
      line.classList.toggle('hot', hot.has(key));
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
      if (i >= 0 && state.drill.cur.has(i)) {
        const s = state.drill.v.symbols[i];
        x = x0 + s.x * size;
        y = y0 + s.y * size;
        r = state.drill.dots[i].offsetWidth / 2;
      }
    } else if (fol.e && !state.drill && pi !== undefined) {
      const p = state.data.packages[pi];
      x = x0 + p.x * size;
      y = y0 + p.y * size;
      r = state.bubbles[pi].offsetWidth / 2 || p.r * size;
    }
    tag.hidden = x === undefined;
    if (tag.hidden) return;
    tag.textContent = fol.e.name;
    tag.style.left = `${x}px`;
    tag.style.top = `${y - r - 6}px`;
  }

  // ---------- Overlays ----------

  function renderOverlays(f) {
    const { chapters } = state.data;
    $('date').textContent = fmtBig(f.t);
    const ci = chapterAt(rev());
    const c = chapters[ci];
    $('chapLabel').textContent = c ? `Chapter ${ci + 1} of ${chapters.length} · ${c.title}` : '';
    $('cCommits').textContent = f.total.toLocaleString();
    $('cLive').textContent = f.live.toLocaleString();
    $('cPkgs').textContent = f.pkgs.filter((p) => p.live > 0).length.toLocaleString();
    $('caption').textContent = caption(f);
    $('caption').hidden = !!state.card;
    const label = state.step === 'commit' ? `${f.sha.slice(0, 7)} · ` : '';
    $('frameLabel').textContent = `${label}frame ${state.frame + 1} of ${state.data.frames.length}`;
  }

  // The frame's key moment, or else what happened in it.
  function caption(f) {
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
    release: 'Release', newcomer: 'New author', 'dep-added': 'New dependency', 'dep-removed': 'Dependency dropped', cycle: 'Import cycle', 'cycle-gone': 'Cycle broken' };

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
      sum,
      h('a', { class: 'card-link', href: `/symbols#c=${m.sha}` }, 'Open the commit →'));
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
    const p = state.data.packages[pi];
    const { size, x0, y0 } = state.geom;
    const st = $('stage');
    st.style.setProperty('--ox', `${x0 + p.x * size}px`);
    st.style.setProperty('--oy', `${y0 + p.y * size}px`);
    state.drill = { dir, pi, v, cur: new Map(), names: [], at: -1, dots: [], peak: Math.max(1, ...v.symbols.map((s) => s.peak)) };
    state.switching = null;
    $('dots').replaceChildren(...v.symbols.map((s) => {
      const el = h('button', { type: 'button', class: `sym gone k-${s.kind}`, tabindex: '-1', onclick: () => followSym(s.id).catch(fail) }, h('span', { class: 'sname' }));
      state.drill.dots.push(el);
      return el;
    }));
    $('tags').replaceChildren();
    $('drillName').textContent = dir;
    $('drillBar').hidden = false;
    $('syms').inert = false;
    $('bubbles').inert = true;
    st.classList.add('drilled');
    placeDots();
    render();
    if (document.activeElement && document.activeElement.closest('#bubbles')) $('back').focus();
  }

  function undrill() {
    if (!state.drill) return;
    const pi = state.drill.pi;
    state.drill = null;
    state.switching = null;
    $('stage').classList.remove('drilled');
    $('drillBar').hidden = true;
    $('syms').inert = true;
    $('bubbles').inert = false;
    render();
    if (state.bubbles[pi].tabIndex === 0) state.bubbles[pi].focus();
  }

  function placeDots() {
    const d = state.drill;
    const { size, x0, y0 } = state.geom;
    d.v.symbols.forEach((s, i) => {
      d.dots[i].style.left = `${x0 + s.x * size}px`;
      d.dots[i].style.top = `${y0 + s.y * size}px`;
    });
  }

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
    const { size } = state.geom;
    const n = Math.max(1, d.v.symbols.length);
    const maxD = Math.max(6, Math.min(64, (0.8 * size) / Math.sqrt(n)));
    const minD = Math.min(6, maxD);
    const fid = state.follow ? state.follow.id : 0;
    d.v.symbols.forEach((s, i) => {
      const el = d.dots[i];
      const live = d.cur.has(i);
      const stmts = d.cur.get(i) || 0;
      const dd = minD + (maxD - minD) * Math.sqrt(stmts / d.peak);
      if (live) el.style.width = el.style.height = `${dd}px`;
      el.classList.toggle('gone', !live);
      el.classList.toggle('followed', s.id === fid);
      el.classList.toggle('labeled', live && dd >= 34);
      el.firstChild.textContent = live && dd >= 34 ? shortName(d.names[i]) : '';
      const label = `${d.names[i]} · ${s.kind} · ${plural(stmts, 'statement')}`;
      el.title = label;
      el.setAttribute('aria-label', `${label}. Follow`);
      el.tabIndex = live ? 0 : -1;
    });
    const live = d.cur.size;
    $('drillNote').textContent = `${plural(live, 'live symbol')}${d.v.truncated ? ` · the biggest ${d.v.symbols.length.toLocaleString()} of ${d.v.total.toLocaleString()} shown` : ''}`;
    if (animate) animateEvents(d.v.frames[state.frame].ev);
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
    const { size, x0, y0 } = state.geom;
    const motion = !reduced.matches;
    let tags = 0;
    for (const e of evs) {
      const el = d.dots[e.s], s = d.v.symbols[e.s];
      const x = x0 + s.x * size, y = y0 + s.y * size;
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
          ? h('button', { type: 'button', class: 'linkish', onclick: () => drill(now.dir).catch(fail) }, `Look inside ${base(now.dir)}`) : null,
        h('a', { href: `/symbols#d=${encodeURIComponent(dirOf(sym.path))}&s=${sym.id}` }, 'Symbol timeline →')),
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

  // ---------- Playback ----------

  // render draws the current frame. animate plays the package's symbol
  // events, for a step forward by one frame.
  function render(story = true, animate = false) {
    const f = state.data.frames[state.frame];
    if (!f) return;
    renderStage(f);
    if (state.drill) renderDrill(animate);
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
    if (state.follow) p.set('sym', String(state.follow.id));
    if (!state.story) p.set('story', '0');
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
    $('narrate').addEventListener('click', () => setNarrate(!state.narrate));
    $('back').addEventListener('click', undrill);
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
        case 'Escape':
          if (state.card) hideCard();
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
