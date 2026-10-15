'use strict';

// The journey page: the history played back frame by frame, one bubble per
// package, with the story told alongside. common.js provides h, $, api,
// plural, fmtDate, waitReady and fail.
(() => {
  const ACT = ['#2A2418', '#4A3A1E', '#7A5A26', '#B07F33', '#F0A64A'];
  const BASE_MS = 1100; // per frame at 1×
  const DAY = 86400;

  const state = {
    step: 'week',
    speed: 1,
    data: null, // the /api/journey response for the current step
    cache: {}, // responses by step
    frame: 0,
    timer: null,
    narrate: false,
    spoken: -1, // chapter last narrated
    maxEdits: 1,
    bubbles: [], // elements by package index
    lines: new Map(), // "a-b" → <line>
  };

  const fmtShort = (t) => new Date(t * 1000).toLocaleDateString(undefined, { month: 'short', day: 'numeric', timeZone: 'UTC' });
  const fmtBig = (t) => new Date(t * 1000).toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric', timeZone: 'UTC' });
  const base = (p) => (p === '.' ? '(root)' : p.split('/').pop());

  // The revision index the playhead is at: the last one in the frame.
  const rev = () => state.data.frames[state.frame].total - 1;
  const chapterAt = (r) => {
    const cs = state.data.chapters;
    for (let i = cs.length - 1; i >= 0; i--) if (r >= cs[i].firstRev) return i;
    return 0;
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
    for (const b of $('step').querySelectorAll('button')) b.setAttribute('aria-pressed', String(b.dataset.v === step));
    try { history.replaceState(null, '', `#step=${step}`); } catch { /* ignore */ }
    let max = 1;
    for (const f of state.data.frames) for (const p of f.pkgs) max = Math.max(max, editsOf(p));
    state.maxEdits = max;
    buildRows();
    buildChapterList();
    buildStage();
    // Stay on the same revision across steps.
    const fs = state.data.frames;
    let i = fs.findIndex((f) => f.total - 1 >= at);
    if (i < 0) i = fs.length - 1;
    seek(i, true);
  }

  const editsOf = (p) => (p.kinds ? Object.values(p.kinds).reduce((a, b) => a + b, 0) : 0);

  // Picks a default step that gives a playable number of frames.
  function defaultStep(st) {
    const days = (st.to - st.from) / DAY;
    if (days >= 540) return 'month';
    if (days >= 84) return 'week';
    return 'commit';
  }

  // ---------- Stage ----------

  function buildStage() {
    const { packages } = state.data;
    const wrap = $('bubbles');
    wrap.replaceChildren();
    state.bubbles = packages.map((p) => {
      const el = h('div', { class: 'bubble gone', title: p.path }, h('span', { class: 'bname' }, base(p.path)));
      wrap.append(el);
      return el;
    });
    $('deps').replaceChildren();
    state.lines = new Map();
    place();
  }

  // place positions the bubbles and lines for the stage's current size.
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
    packages.forEach((p, i) => {
      const el = state.bubbles[i];
      const s = now.get(i);
      const live = s ? s.live : 0;
      const a = actStep(s ? editsOf(s) : 0);
      const d = live > 0 ? Math.max(8, 2 * p.r * size * Math.sqrt(live / Math.max(1, p.peak))) : Math.max(8, p.r * size);
      el.style.width = el.style.height = `${d}px`;
      el.style.backgroundColor = ACT[a];
      el.className = `bubble a${a}${live > 0 ? '' : ' gone'}${a >= 3 ? ' busy' : ''}${d < 44 ? ' small' : ''}`;
      el.title = live > 0 ? `${p.path} · ${plural(live, 'symbol')}${s && s.kinds ? ` · ${describeKinds(s.kinds)}` : ''}` : p.path;
    });

    const svg = $('deps');
    const want = new Set();
    for (const [a, b] of f.deps) {
      const key = `${Math.min(a, b)}-${Math.max(a, b)}`;
      want.add(key);
      let line = state.lines.get(key);
      if (!line) {
        line = document.createElementNS('http://www.w3.org/2000/svg', 'line');
        state.lines.set(key, line);
        setLine(line, key);
        svg.append(line);
      }
    }
    for (const [key, line] of state.lines) line.classList.toggle('off', !want.has(key));
  }

  function describeKinds(kinds) {
    return Object.entries(kinds).map(([k, n]) => `${n} ${k}`).join(', ');
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
    const label = state.step === 'commit' ? `${f.sha.slice(0, 7)} · ` : '';
    $('frameLabel').textContent = `${label}frame ${state.frame + 1} of ${state.data.frames.length}`;
  }

  // The frame's key moment, or else what happened in it.
  function caption(f) {
    const lo = f.total - f.commits, hi = f.total - 1;
    let best = null;
    for (const m of state.data.moments) {
      if (m.rev >= lo && m.rev <= hi && (!best || m.weight > best.weight)) best = m;
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
    const f = state.data.frames[state.frame];
    const lo = f.total - f.commits;
    const past = state.data.moments.filter((m) => m.rev <= r).slice(-4);
    $('moments').replaceChildren(...(past.length ? past.map((m) => h('li', { class: `moment k-${m.kind}${m.rev >= lo ? ' now' : ''}`, 'aria-current': m.rev >= lo ? 'true' : null },
      h('span', { class: 'dot', 'aria-hidden': 'true' }),
      h('span', {}, h('div', { class: 'mt' }, m.text), h('div', { class: 'md mono' }, `${fmtBig(m.t)} · ${m.sha.slice(0, 7)}`)))) : [h('li', { class: 'muted small' }, 'Nothing yet.')]));

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

  function render(story = true) {
    const f = state.data.frames[state.frame];
    renderStage(f);
    renderOverlays(f);
    renderRows(f);
    if (story) renderStory();
  }

  function seek(i, force) {
    const n = state.data.frames.length;
    if (!n) return;
    i = Math.max(0, Math.min(n - 1, i));
    if (i === state.frame && !force) return;
    state.frame = i;
    render();
  }

  function seekRev(r) {
    const i = state.data.frames.findIndex((f) => f.total - 1 >= r);
    seek(i < 0 ? state.data.frames.length - 1 : i);
  }

  function play() {
    if (state.frame >= state.data.frames.length - 1) seek(0);
    $('play').setAttribute('aria-label', 'Pause');
    $('playIcon').innerHTML = '<path d="M6 4h4v16H6zM14 4h4v16h-4z"></path>';
    const tick = () => {
      seek(state.frame + 1);
      if (state.frame >= state.data.frames.length - 1) stop();
      else state.timer = setTimeout(tick, BASE_MS / state.speed);
    };
    state.timer = setTimeout(tick, BASE_MS / state.speed);
  }

  function stop() {
    clearTimeout(state.timer);
    state.timer = null;
    $('play').setAttribute('aria-label', 'Play');
    $('playIcon').innerHTML = '<path d="M7 4v16l13-8z"></path>';
  }

  const toggle = () => (state.timer ? stop() : play());

  function setSpeed(v) {
    state.speed = v;
    for (const b of $('speed').querySelectorAll('button')) b.setAttribute('aria-pressed', String(Number(b.dataset.v) === v));
    document.documentElement.style.setProperty('--tween', `${Math.round(600 / v)}ms`);
  }

  // ---------- Start ----------

  async function main() {
    const st = await waitReady();
    if (!st) return;
    $('repo').textContent = st.repo;
    $('swatches').replaceChildren(...ACT.map((c) => h('span', { style: `background:${c}` })));

    $('prev').addEventListener('click', () => { stop(); seek(state.frame - 1); });
    $('next').addEventListener('click', () => { stop(); seek(state.frame + 1); });
    $('play').addEventListener('click', toggle);
    $('speed').addEventListener('click', (e) => { const b = e.target.closest('button'); if (b) setSpeed(Number(b.dataset.v)); });
    $('step').addEventListener('click', (e) => { const b = e.target.closest('button'); if (b) setStep(b.dataset.v).catch(fail); });
    $('narrate').addEventListener('click', () => setNarrate(!state.narrate));
    if (!('speechSynthesis' in window)) {
      $('narrate').disabled = true;
      $('narrate').title = 'This browser cannot speak';
    }
    document.addEventListener('keydown', (e) => {
      if (e.target.closest('input, textarea, select') || e.altKey || e.ctrlKey || e.metaKey) return;
      if (e.key === ' ' && !e.target.closest('button')) { e.preventDefault(); toggle(); }
      else if (e.key === 'ArrowLeft') { stop(); seek(state.frame - 1); }
      else if (e.key === 'ArrowRight') { stop(); seek(state.frame + 1); }
    });
    new ResizeObserver(() => place()).observe($('stage'));

    setSpeed(1);
    const want = new URLSearchParams(location.hash.slice(1)).get('step');
    $('loading').hidden = true;
    $('app').hidden = false;
    await setStep(['commit', 'week', 'month'].includes(want) ? want : defaultStep(st), 0);
    if (!state.data.frames.length) {
      $('caption').textContent = 'No history to play back.';
    }
  }

  main().catch(fail);
})();
