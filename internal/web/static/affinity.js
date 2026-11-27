'use strict';

// The affinity page: a type's methods against its fields, clustered into
// blocks, with how they change together, who calls them and what their
// names say. The hash holds the type (#type=ID, or #name=dir.Type until it
// resolves), the window (&months=) and the order (&order=decl).

(() => {
  const MONTHS = [[3, '3m'], [6, '6m'], [12, '12m'], [24, '24m'], [0, 'All']];
  const state = { months: 12, order: 'block', types: [], typesFor: null, rep: null, repKey: null, filter: '' };

  const bk = (n) => (n >= 0 ? `bk${n % 8}` : '');
  const pct = (x) => `${Math.round(100 * x)}%`;

  function readHash() {
    const p = new URLSearchParams(location.hash.slice(1));
    const months = p.has('months') ? Number(p.get('months')) : 12;
    return {
      type: p.get('type'), name: p.get('name'),
      months: Number.isInteger(months) && months >= 0 ? months : 12,
      order: p.get('order') === 'decl' ? 'decl' : 'block',
    };
  }

  function hashFor(id, months = state.months, order = state.order) {
    const p = new URLSearchParams();
    if (id != null) p.set('type', id);
    if (months !== 12) p.set('months', months);
    if (order === 'decl') p.set('order', 'decl');
    return `#${p}`;
  }

  async function route() {
    const want = readHash();
    state.months = want.months;
    state.order = want.order;
    renderSegs();
    if (state.typesFor !== state.months) {
      state.types = await api(`/api/affinity/types?months=${state.months}`);
      state.typesFor = state.months;
    }
    let url;
    if (want.type) url = `/api/affinity?type=${encodeURIComponent(want.type)}&months=${state.months}`;
    else if (want.name) url = `/api/affinity?name=${encodeURIComponent(want.name)}&months=${state.months}`;
    else if (state.types.length) url = `/api/affinity?type=${state.types[0].id}&months=${state.months}`;
    renderPicker();
    if (!url) {
      $('headline').textContent = 'No struct types with methods';
      return;
    }
    if (url !== state.repKey) {
      $('report').setAttribute('aria-busy', 'true');
      try {
        state.rep = await api(url);
        state.repKey = url;
        $('report').classList.remove('failed');
      } catch (err) {
        state.rep = null;
        state.repKey = null;
        $('report').classList.add('failed');
        $('where').textContent = '';
        $('lede').textContent = 'Pick a type from the list.';
        $('headline').replaceChildren(h('span', { class: 'error' }, String(err.message || err).replace(/^[^:]*: \d+ /, '')));
        renderPicker();
        return;
      } finally {
        $('report').removeAttribute('aria-busy');
      }
    }
    if (!want.type) history.replaceState(null, '', hashFor(state.rep.type.id));
    renderPicker();
    render();
  }

  // The picker: the window and the candidate types.
  function renderSegs() {
    $('monthSeg').replaceChildren(...MONTHS.map(([m, label]) => h('button', {
      type: 'button', 'aria-pressed': String(state.months === m),
      onclick: () => { location.hash = hashFor(state.rep ? state.rep.type.id : null, m); },
    }, label)));
    $('orderSeg').replaceChildren(...[['block', 'Clustered'], ['decl', 'Declaration order']].map(([o, label]) => h('button', {
      type: 'button', 'aria-pressed': String(state.order === o),
      onclick: () => { location.hash = hashFor(state.rep ? state.rep.type.id : null, state.months, o); },
    }, label)));
  }

  function renderPicker() {
    const q = state.filter.toLowerCase();
    const cur = state.rep && state.rep.type.id;
    const list = state.types.filter((c) => !q || `${c.dir}.${c.name}`.toLowerCase().includes(q));
    $('types').replaceChildren(...list.map((c) => h('li', {},
      h('button', {
        type: 'button', 'aria-current': String(c.id === cur),
        onclick: () => { location.hash = hashFor(c.id); },
      },
      h('span', { class: 'tn' }, c.name),
      h('span', { class: 'td' }, `${c.dir} · ${plural(c.methods, 'method')} · ${plural(c.fields, 'field')} · ${plural(c.split, 'block')} · ${plural(c.edits, 'edit')}`)))));
    if (!list.length) $('types').append(h('li', { class: 'muted small' }, 'No type matches'));
  }

  function render() {
    const r = state.rep;
    renderSummary(r);
    renderMatrix(r);
    renderBlocks(r);
    renderFindings(r);
    renderSteps(r);
    renderCoupling(r);
    renderClients(r);
    renderNames(r);
  }

  function renderSummary(r) {
    $('where').textContent = `${r.package} · ${r.type.path}:${r.type.line}`;
    $('headline').replaceChildren(r.dir === '.' ? '' : `${r.dir.split('/').pop()}.`, h('strong', {}, r.type.name),
      r.split > 1 ? ` holds ${plural(r.split, 'type')} waiting to come out` : r.struct ? ' holds together' : ' has no fields');
    const window = r.months ? `the last ${r.months} months` : 'all its history';
    const parts = [`${plural(r.rows.length, 'method')} over ${plural(r.columns.length, 'field')}.`];
    if (r.struct) parts.push(`Methods sharing a field or a call form ${plural(r.lcom4, 'group')}; with hubs and orchestration set aside, ${plural(r.split, 'block')}.`);
    parts.push(`${plural(r.coupling.commits, 'commit')} changed its methods in ${window}${r.coupling.massEdits ? `, leaving out ${plural(r.coupling.massEdits, 'mass edit')}` : ''}.`);
    $('lede').textContent = parts.join(' ');
    const stat = (n, label) => h('div', { class: 'stat' }, h('strong', {}, n), h('span', {}, label));
    $('stats').replaceChildren(
      stat(`${r.lcom4} → ${r.split}`, 'LCOM4 → blocks'),
      stat(r.rows.length, 'methods'), stat(r.columns.length, 'fields'),
      stat(r.clients.list.length, 'clients'), stat(r.coupling.commits, 'commits'));
  }

  // The matrix, with rows and columns in block order or as declared.
  function renderMatrix(r) {
    const table = $('matrix');
    const panel = table.closest('.panel');
    if (!r.struct || !r.columns.length || !r.rows.length) {
      table.replaceChildren();
      $('legend').replaceChildren(h('p', { class: 'empty-note' }, r.struct ? 'No fields or no methods: no matrix.' : 'Not a struct: no matrix.'));
      panel.querySelector('.hint').hidden = true;
      return;
    }
    panel.querySelector('.hint').hidden = false;
    const cols = r.columns.map((_, j) => j);
    const rows = r.rows.map((_, i) => i);
    const decl = state.order === 'decl';
    if (decl) {
      cols.sort((a, b) => r.columns[a].decl - r.columns[b].decl);
      rows.sort((a, b) => r.rows[a].decl - r.rows[b].decl);
    }
    const sepClass = (k) => {
      if (decl || k === 0) return '';
      const prev = r.columns[cols[k - 1]];
      const cur = r.columns[cols[k]];
      if (cur.block < 0 && prev.block >= 0) return 'sep2';
      return cur.block !== prev.block && cur.block >= 0 ? 'sep' : '';
    };
    const colX = (c) => c.block < 0;
    const colTitle = (c) => [`${c.name} ${c.type}`, `${c.class}${c.hub ? ' · hub' : ''}${c.shared ? ' · shared state' : ''}${c.block >= 0 ? ` · block ${r.blocks[c.block].letter}` : ''}`, c.reason].join('\n');

    const head = h('tr', {}, h('th', { class: 'corner', scope: 'col' }, decl ? 'as declared' : 'by block'),
      cols.map((j, k) => {
        const c = r.columns[j];
        return h('th', { scope: 'col', class: `colhead ${bk(c.block)} ${colX(c) ? 'x' : ''} ${sepClass(k)}`, 'data-c': j, title: colTitle(c) },
          h('span', {}, c.name));
      }),
      h('th', { class: 'callhead', scope: 'col' }, 'calls'));

    const body = rows.map((i) => {
      const row = r.rows[i];
      const cells = new Map(row.cells.map((c) => [c.col, c]));
      const tag = row.kind === 'member' ? r.blocks[row.block].letter : row.kind === 'bridge' ? 'br' : 'fn';
      const why = [row.reason, ...row.notes].join('\n');
      return h('tr', { 'data-r': i, class: `kind-${row.kind}` },
        h('th', { scope: 'row', class: bk(row.block), title: why },
          h('span', { class: 'tag' }, tag), h('a', { href: `/journey#sym=${row.id}` }, row.method)),
        cols.map((j, k) => {
          const c = cells.get(j);
          const col = r.columns[j];
          const cls = ['cell', sepClass(k)];
          if (colX(col)) cls.push('x');
          if (c && row.block >= 0 && row.block === col.block) cls.push('tint', bk(col.block));
          let text = '';
          let title = null;
          if (c) {
            text = c.r && c.w ? 'RW' : c.w ? 'W' : 'R';
            cls.push(text.toLowerCase());
            const verb = c.r && c.w ? 'reads and writes' : c.w ? 'writes' : 'reads';
            title = `${row.method} ${verb} ${col.name}${c.via ? ` (${c.via})` : ''} · ${plural(c.n, 'use')}`;
          }
          return h('td', { class: cls.join(' '), 'data-c': j, title }, text);
        }),
        h('td', { class: 'calls' }, row.calls.length ? `→ ${row.calls.map((c) => r.rows[c.row].method + (c.value ? ' (value)' : '')).join(', ')}` : ''));
    });
    table.replaceChildren(h('thead', {}, head), h('tbody', {}, body));

    const sw = (cls, text, label) => h('span', {}, h('span', { class: `sw ${cls}` }, text), label);
    $('legend').replaceChildren(
      sw('', 'W', 'written'), sw('', 'R', 'read'),
      ...r.blocks.map((b, n) => h('span', { class: bk(n) }, h('span', { class: 'letter' }, b.letter), b.label)),
      h('span', {}, h('span', { class: 'letter', style: '--bk: var(--muted)' }, 'br'), 'bridge'),
      h('span', {}, h('span', { class: 'letter', style: '--bk: var(--muted)' }, 'fn'), 'free'));
  }

  // Hovering lights up a row with its fields and callees, or a column.
  function hover(ev) {
    const table = $('matrix');
    table.querySelectorAll('.hl-r, .hl-c, .hl-call').forEach((el) => el.classList.remove('hl-r', 'hl-c', 'hl-call'));
    const cell = ev.target.closest && ev.target.closest('td, th');
    if (!cell || !table.contains(cell) || !state.rep) return;
    const c = cell.dataset.c;
    if (c != null) table.querySelectorAll(`[data-c="${c}"]`).forEach((el) => el.classList.add('hl-c'));
    const tr = cell.closest('tbody tr');
    if (!tr) return;
    tr.classList.add('hl-r');
    if (cell.tagName !== 'TH') return;
    for (const call of state.rep.rows[Number(tr.dataset.r)].calls) {
      const other = table.querySelector(`tr[data-r="${call.row}"]`);
      if (other) other.classList.add('hl-call');
    }
  }

  function signals(list) {
    return h('div', { class: 'signals' }, list.map((s) => h('span', { class: 'tag' }, s)));
  }

  function renderBlocks(r) {
    const names = (idx, of) => idx.map((k) => of[k]).join(', ');
    $('blocks').replaceChildren(...r.blocks.map((b, n) => {
      const sig = [];
      if (b.kind !== 'shared') sig.push(b.exclusive ? 'owns its writes' : 'shares writes');
      sig.push(`${plural(b.edits, 'edit')} in ${plural(b.commits, 'commit')}`);
      if (b.cohesion != null) sig.push(`${pct(b.cohesion)} of co-changes inside`);
      if (b.clients) sig.push(`${plural(b.clients, 'client')}${b.agreement != null ? `, ${pct(b.agreement)} only here` : ''}`);
      return h('article', { class: `panel block ${bk(n)}` },
        h('div', { class: 'block-head' }, h('span', { class: 'letter' }, b.letter), h('span', { class: 'block-label' }, b.label), h('span', { class: 'muted small' }, b.kind)),
        h('dl', { class: 'kv' },
          b.columns.length ? [h('dt', {}, 'fields'), h('dd', {}, names(b.columns, r.columns.map((c) => c.name)))] : null,
          b.rows.length ? [h('dt', {}, 'methods'), h('dd', {}, names(b.rows, r.rows.map((x) => x.method)))] : null),
        h('p', { class: 'why' }, b.reason),
        signals(sig),
        b.outside.length ? h('p', { class: 'muted small' }, 'changes with ', b.outside.flatMap((o, k) => [k ? ', ' : '', h('a', { href: `/journey#sym=${o.id}` }, o.name), ` (${o.n})`])) : null);
    }));
  }

  function renderFindings(r) {
    $('findings').replaceChildren(...(r.findings.length ? r.findings.map((f) => h('li', {},
      h('span', { class: 'fk' }, f.kind), h('span', {}, f.text), h('span', { class: 'adv' }, f.advice)))
      : [h('li', { class: 'empty-note' }, 'Nothing stands out.')]));
  }

  function renderSteps(r) {
    $('steps').replaceChildren(...r.steps.map((s) => h('li', {}, h('span', {}, s.text), s.signals.length ? signals(s.signals) : null)));
    $('noteTitle').hidden = !r.notes.length;
    $('notes').replaceChildren(...r.notes.map((n) => h('li', {}, n.text)));
  }

  // Change coupling: a lower-triangle grid in display order, and the pairs.
  function renderCoupling(r) {
    const c = r.coupling;
    $('coSub').textContent = `${plural(c.commits, 'commit')}${c.massEdits ? ` · ${plural(c.massEdits, 'commit')} changing over ${c.limit} methods left out` : ''} · strong from ${c.min} together`;
    const pairs = new Map(c.pairs.map((p) => [`${p.a}:${p.b}`, p]));
    const edited = r.rows.map((row, i) => i).filter((i) => r.rows[i].edits > 0);
    if (!edited.length) {
      $('heat').replaceChildren(h('tbody', {}, h('tr', {}, h('td', { class: 'empty-note', style: 'width:auto;background:none' }, 'No changes in the window.'))));
    } else {
      $('heat').replaceChildren(h('tbody', {}, edited.map((i, k) => h('tr', {},
        h('th', { scope: 'row', class: bk(r.rows[i].block), title: `${r.rows[i].method}: ${plural(r.rows[i].edits, 'edit')}` }, r.rows[i].method),
        edited.slice(0, k).map((j) => {
          const p = pairs.get(`${Math.min(i, j)}:${Math.max(i, j)}`);
          if (!p) return h('td', { title: `${r.rows[j].method} ↔ ${r.rows[i].method}: never together` });
          return h('td', {
            class: `on${p.cross ? ' cross' : ''}${p.strong ? ' strong' : ''}`, style: `--v: ${Math.max(0.15, p.logical).toFixed(2)}`,
            title: `${r.rows[j].method} ↔ ${r.rows[i].method}: ${plural(p.n, 'commit')} (${pct(p.logical)})${p.cross ? ', across blocks' : ''}`,
          });
        })))));
    }
    const strong = c.pairs.filter((p) => p.strong);
    $('pairs').replaceChildren(...(strong.length ? strong.slice(0, 12).map((p) => h('li', {},
      h('span', { class: 'pn' }, `${r.rows[p.a].method} ↔ ${r.rows[p.b].method}`), ` ${plural(p.n, 'commit')}, ${pct(p.logical)}`,
      p.text ? h('div', { class: 'ptext' }, p.text) : null))
      : [h('li', { class: 'empty-note' }, `No two methods changed together in ${plural(c.min, 'commit')} or more.`)]));
  }

  function renderClients(r) {
    const cs = r.clients;
    const used = [...new Set(cs.list.flatMap((c) => c.uses.map((u) => u.row)))].sort((a, b) => a - b);
    if (!cs.list.length) {
      $('clients').replaceChildren(h('tbody', {}, h('tr', {}, h('td', { class: 'empty-note' }, 'Nothing outside the type calls its methods.'))));
    } else {
      $('clients').replaceChildren(
        h('thead', {}, h('tr', {}, h('th', {}, 'client'), used.map((i) => h('th', { class: `m ${bk(r.rows[i].block)}` }, r.rows[i].method)), h('th', {}, ''))),
        h('tbody', {}, cs.list.map((c) => {
          const uses = new Map(c.uses.map((u) => [u.row, u]));
          return h('tr', {},
            h('td', { class: 'cname', title: c.callers.map((s) => s.name).join(', ') }, c.name),
            used.map((i) => {
              const u = uses.get(i);
              return h('td', { class: `d${u && u.approximate ? ' approx' : ''}`, title: u ? `${plural(u.calls, 'use')}${u.value ? ' as a value' : ''}${u.approximate ? ', matched by name' : ''}` : null }, u ? (u.approximate ? '~' : '●') : '');
            }),
            h('td', { class: 'small muted' }, c.text));
        })));
    }
    $('roles').replaceChildren(...(cs.roles.length > 1 ? cs.roles.map((role) => h('li', {}, role.text)) : []));
    $('uncalled').replaceChildren(...cs.uncalled.map((u) => h('li', {}, h('span', { class: 'mono' }, r.rows[u.row].method), h('span', { class: 'muted' }, ` · ${u.why}`))));
    $('ifaces').replaceChildren(...cs.list.filter((c) => c.interface).map((c) => h('details', { class: 'ifbox' },
      h('summary', {}, `Interface for ${c.name}`), h('pre', { class: 'iface' }, c.interface))));
  }

  function renderNames(r) {
    const n = r.names;
    const member = (w) => [...w.cols.map((j) => r.columns[j].name), ...w.rows.map((i) => r.rows[i].method)].join(', ');
    const out = [];
    if (n.words.length) {
      out.push(h('div', { class: 'words' }, n.words.slice(0, 14).map((w) => h('div', { class: 'word' },
        h('span', { class: 'w' }, w.word), h('span', { class: 'mem' }, member(w)),
        w.blocks.length > 1 ? h('span', { class: 'tag' }, `across ${w.blocks.map((b) => r.blocks[b].letter).join(', ')}`) : null))));
    }
    for (const f of n.flags) out.push(h('p', { class: 'small' }, h('span', { class: 'mono' }, r.columns[f.col].name), ` · ${f.why}`));
    if (n.groups.length > 1) {
      out.push(h('p', { class: 'small muted' }, 'Declared together:'));
      for (const g of n.groups) {
        out.push(h('p', { class: 'small' }, h('span', { class: 'mono' }, g.cols.map((j) => r.columns[j].name).join(', ')),
          g.agrees ? ' · within one block' : ` · across ${g.blocks.map((b) => r.blocks[b].letter).join(', ')}`));
      }
    }
    if (!out.length) out.push(h('p', { class: 'empty-note' }, 'The names share nothing.'));
    $('names').replaceChildren(...out);
  }

  async function main() {
    const st = await waitReady();
    if (!st) return;
    $('repo').textContent = st.repo;
    $('loading').hidden = true;
    $('app').hidden = false;
    $('filter').addEventListener('input', (e) => { state.filter = e.target.value; renderPicker(); });
    $('matrix').addEventListener('mouseover', hover);
    $('matrix').addEventListener('mouseleave', () => hover({ target: document.body }));
    window.addEventListener('hashchange', () => route().catch(fail));
    await route();
  }

  main().catch(fail);
})();
