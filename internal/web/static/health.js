'use strict';

// The health page: how the codebase as a whole evolved. Trend cards over the
// whole history, the files where edits and complexity pile up, and who last
// touched the live code.

const pct = (x) => `${Math.round(x * 100)}%`;
const ratio = (a, b) => (b ? a / b : 0);
const sum = (xs, f) => xs.reduce((n, x) => n + f(x), 0);
const editsOf = (b) => Object.values(b.edits).reduce((n, x) => n + x, 0);

// Verdicts say which way a trend points, and whether that way is good.
const VERDICTS = {
  watch: { label: 'Watch', icon: 'M7 17L17 7M9 7h8v8' },
  good: { label: 'Improving', icon: 'M5 12l5 5L20 7' },
  steady: { label: 'Steady', icon: 'M5 12h14M13 6l6 6-6 6' },
  info: { label: 'Context', icon: 'M12 8v.01M11 12h1v5h1' },
};

// trend compares the last value with the first, relative to tolerance.
function trend(first, last, higherIsBad, tolerance = 0.1) {
  if (first === 0 && last === 0) return 'steady';
  const change = ratio(last - first, Math.abs(first) || 1);
  if (Math.abs(change) <= tolerance) return 'steady';
  return (change > 0) === higherIsBad ? 'watch' : 'good';
}

// spark draws a series as a line, or as bars when each point stands alone
// (a share within one bucket rather than a running state).
function spark(values, { bars = false, label }) {
  const W = 200, H = 52, n = values.length;
  const max = Math.max(...values, 0), min = bars ? 0 : Math.min(...values);
  const span = max - min || 1;
  const y = (v) => H - 4 - ((v - min) / span) * (H - 10);
  const el = svg('svg', { viewBox: `0 0 ${W} ${H}`, preserveAspectRatio: 'none', role: 'img', 'aria-label': label, class: 'spark' });
  el.append(svg('line', { x1: 0, y1: H - 1, x2: W, y2: H - 1, class: 'base' }));
  if (bars) {
    const bw = W / n;
    values.forEach((v, i) => {
      if (v == null) return;
      el.append(svg('rect', { x: i * bw + bw * 0.15, y: y(v), width: Math.max(bw * 0.7, 1), height: Math.max(H - 4 - y(v), 1), rx: 1 }));
    });
    return el;
  }
  const xs = (i) => (n === 1 ? W / 2 : (i / (n - 1)) * W);
  const pts = values.map((v, i) => `${xs(i).toFixed(1)},${y(v).toFixed(1)}`).join(' ');
  el.append(svg('polyline', { points: `0,${H - 1} ${pts} ${W},${H - 1}`, class: 'area' }));
  el.append(svg('polyline', { points: pts, class: 'line' }));
  return el;
}

function card({ title, value, delta, verdict, series, why }) {
  const v = VERDICTS[verdict];
  return h('article', { class: `card panel v-${verdict}` },
    h('div', { class: 'cardhead' },
      h('h2', { class: 'h2' }, title),
      h('span', { class: 'verdict' },
        svg('svg', { width: 12, height: 12, viewBox: '0 0 24 24', fill: 'none', stroke: 'currentColor', 'stroke-width': 2.4, 'stroke-linecap': 'round', 'stroke-linejoin': 'round', 'aria-hidden': 'true' },
          svg('path', { d: v.icon })),
        v.label)),
    h('div', { class: 'value' }, h('span', { class: 'big' }, value), h('span', { class: 'muted small' }, delta)),
    series,
    h('div', { class: 'axis mono' }, ...axisLabels()),
    h('p', { class: 'why' }, why));
}

const state = { status: null, data: null };

// axisLabels names the ends of every series: by date, or by commit when the
// whole history fits in one day.
function axisLabels() {
  const B = state.data.buckets;
  const a = fmtDate(B[0].from), b = fmtDate(B.at(-1).to);
  if (a !== b) return [h('span', null, a), h('span', null, b)];
  return [h('span', null, B[0].first.slice(0, 7)), h('span', null, B.at(-1).last.slice(0, 7))];
}

function render() {
  const { buckets: B, hotspots, owners } = state.data;
  const first = B[0], last = B.at(-1);
  const revs = sum(B, (b) => b.revisions);
  $('span').textContent = `Health over time · ${plural(revs, 'commit')} · ${fmtDate(first.from)} – ${fmtDate(last.to)}`;

  // Headline: the single most actionable observation, then the context.
  const top = hotspots[0];
  const cpf = (b) => ratio(b.complexity, b.funcs);
  $('headline').textContent = top
    ? `${top.path} is where change and complexity meet: ${plural(top.edits, 'semantic edit')} on code with a cyclomatic complexity of ${top.complexity}.`
    : 'No hotspots yet: no file of live code has both edits and complexity.';
  const lede = [`The exported surface went from ${first.exported} to ${plural(last.exported, 'symbol')}.`];
  const cTrend = trend(cpf(first), cpf(last), true);
  if (cTrend === 'watch') lede.push(`Functions are getting more complex on average (${cpf(first).toFixed(1)} → ${cpf(last).toFixed(1)}).`);
  else if (cTrend === 'good') lede.push(`Functions are getting simpler on average (${cpf(first).toFixed(1)} → ${cpf(last).toFixed(1)}).`);
  if (state.data.busFactor === 1 && state.data.authors > 1) lede.push(`One person last touched more than half of the live code.`);
  $('lede').textContent = lede.join(' ');

  const added = sum(B, (b) => b.added), surviving = sum(B, (b) => b.surviving);
  const tail = B.slice(-Math.max(1, Math.ceil(B.length / 4)));
  const tailShare = ratio(sum(tail, (b) => b.reshaped), sum(tail, editsOf));
  const allShare = ratio(sum(B, (b) => b.reshaped), sum(B, editsOf));
  const cards = [
    card({
      title: 'Exported API surface', value: last.exported.toLocaleString(), delta: `from ${first.exported}`,
      verdict: 'info', series: spark(B.map((b) => b.exported), { label: 'Exported symbols over time' }),
      why: 'Every exported func, type, var and const is a promise other packages can lean on. Growth should track features, not leak internals.',
    }),
    card({
      title: 'Complexity per function', value: cpf(last).toFixed(1), delta: `from ${cpf(first).toFixed(1)} · ${plural(last.funcs, 'function')}`,
      verdict: cTrend, series: spark(B.map(cpf), { label: 'Mean cyclomatic complexity per function over time' }),
      why: 'Mean cyclomatic complexity of live functions and methods. A rising line means new logic is landing in ever larger branches.',
    }),
    card({
      title: 'Restructuring share', value: pct(tailShare), delta: `latest quarter · ${pct(allShare)} overall`,
      verdict: 'info', series: spark(B.map((b) => (editsOf(b) ? b.reshaped / editsOf(b) : null)), { bars: true, label: 'Share of edits that rename, move or change a signature, per period' }),
      why: 'Renames, moves and signature changes among all semantic edits. Some after each growth spurt is healthy: the design is being kept up.',
    }),
    card({
      title: 'Code survival', value: pct(ratio(surviving, added)), delta: `${surviving.toLocaleString()} of ${plural(added, 'added symbol')} still alive`,
      verdict: 'info', series: spark(B.map((b) => (b.added ? b.surviving / b.added : null)), { bars: true, label: 'Share of each period\'s new symbols still alive today' }),
      why: 'Of the symbols added in each period, how many exist today. Low survival in recent periods points to churn from unclear requirements.',
    }),
  ];
  if (state.data.tests) {
    const tr = (b) => ratio(b.testLines, b.lines);
    cards.push(card({
      title: 'Test lines per source line', value: tr(last).toFixed(2), delta: `from ${tr(first).toFixed(2)}`,
      verdict: trend(tr(first), tr(last), false), series: spark(B.map(tr), { label: 'Test lines per source line over time' }),
      why: 'Lines in test functions against lines in source declarations. A falling line means features are outpacing their tests.',
    }));
  }
  $('cards').replaceChildren(...cards);
  if (!state.data.tests) {
    $('cards').append(h('article', { class: 'card panel pending' },
      h('h2', { class: 'h2' }, 'Test lines per source line'),
      h('p', { class: 'why' }, 'Tests were left out of the index. Restart with ', h('code', null, 'huginn serve -tests'), ' to see how tests kept pace with the code.')));
  }

  renderHotspots(hotspots);
  renderOwners(owners);
}

function renderHotspots(hotspots) {
  if (!hotspots.length) {
    $('hotspots').replaceChildren(h('caption', { class: 'muted small' }, 'No file has both edits and complexity yet.'));
    return;
  }
  const max = hotspots[0].score;
  const head = h('thead', null, h('tr', null,
    h('th', { scope: 'col' }, 'File'),
    h('th', { scope: 'col', class: 'num' }, 'Edits'),
    h('th', { scope: 'col', class: 'num' }, 'Authors'),
    h('th', { scope: 'col', class: 'num' }, 'Complexity'),
    h('th', { scope: 'col', class: 'num' }, 'Lines'),
    h('th', { scope: 'col', class: 'bar' }, 'Score'),
    h('th', { scope: 'col' }, 'Most complex symbol')));
  const rows = hotspots.map((s) => h('tr', null,
    h('td', { class: 'mono' }, h('a', { href: `/map#s=${encodeURIComponent(s.dir)}&w=all` }, s.path)),
    h('td', { class: 'num' }, s.edits),
    h('td', { class: 'num' }, s.authors),
    h('td', { class: 'num' }, s.complexity),
    h('td', { class: 'num' }, s.lines),
    h('td', { class: 'bar' }, h('span', { class: 'meter', style: `width:${Math.max(2, Math.round((s.score / max) * 100))}%`, title: String(s.score) })),
    h('td', null, h('a', { class: 'mono', href: `/journey#sym=${s.top}` }, s.topName),
      ' ', h('span', { class: 'muted small' }, `cyclomatic ${s.topComplexity}`))));
  $('hotspots').replaceChildren(head, h('tbody', null, rows));
}

function renderOwners(owners) {
  const { authors, busFactor } = state.data;
  const live = sum(owners, (o) => o.symbols);
  const shown = owners.slice(0, 5);
  const rest = live - sum(shown, (o) => o.symbols);
  const shades = ['#F0A64A', '#B07F33', '#7A5A26', '#5BA8F5', '#2C4A5E'];
  const segs = shown.map((o, i) => h('span', { style: `width:${(o.symbols / live) * 100}%;background:${shades[i]}` }));
  if (rest > 0) segs.push(h('span', { style: `width:${(rest / live) * 100}%;background:var(--line-strong)` }));
  const note = busFactor <= 1
    ? (authors <= 1 ? 'Expected for a solo project. It is the first question to answer once a second engineer joins: who else can safely change the hotspots?'
      : 'One person last touched more than half of the live code. If they are away, most changes wait.')
    : `${busFactor} people together last touched more than half of the live code.`;
  $('owners').replaceChildren(
    h('div', { class: 'value' }, h('span', { class: 'big' }, busFactor), h('span', { class: 'muted small' }, `bus factor · ${plural(authors, 'author')} in total`)),
    h('div', { class: 'stack', role: 'img', 'aria-label': shown.map((o) => `${o.name} ${pct(o.symbols / live)}`).join(', ') }, segs),
    h('ul', { class: 'ownlist' }, shown.map((o, i) => h('li', null,
      h('span', { class: 'sw', style: `background:${shades[i]}`, 'aria-hidden': 'true' }),
      h('span', { class: 'who' }, o.name),
      h('span', { class: 'muted small' }, `${plural(o.symbols, 'symbol')} · ${pct(o.symbols / live)}`)))),
    h('p', { class: 'why' }, note),
    h('p', { class: 'muted small' }, 'Each live symbol is credited to whoever changed it last.'));
}

(async () => {
  try {
    state.status = await waitReady();
    if (!state.status) return;
    const st = state.status;
    $('repo').textContent = `${st.repo} · ${st.commits.toLocaleString()} commits · ${st.live.toLocaleString()} live symbols`;
    state.data = await api('/api/health');
    if (!state.data.buckets.length) throw new Error('No commits indexed.');
    render();
    $('loading').hidden = true;
    $('app').hidden = false;
  } catch (err) {
    fail(err);
  }
})();
