'use strict';

// Helpers shared by the map and the symbol timeline.

// Kinds of change, highest priority first: when several land in one time
// bucket, the first in this list is the one drawn.
const KINDS = [
  ['deleted', 'Deleted'], ['added', 'Added'], ['signature', 'Signature change'],
  ['renamed', 'Renamed'], ['moved', 'Moved'], ['body', 'Body edit'],
];
const DIAMOND = new Set(['signature', 'renamed']);

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
const plural = (n, word) => `${n.toLocaleString()} ${word}${n === 1 ? '' : 's'}`;

async function api(path) {
  const r = await fetch(path);
  if (!r.ok) throw new Error(`${path}: ${r.status} ${await r.text()}`);
  return r.json();
}

// waitReady polls /api/status until indexing is done, reporting progress in
// the #loading element. It returns the final status, or null on failure.
async function waitReady() {
  for (;;) {
    const st = await api('/api/status');
    if (st.error) {
      $('loading').textContent = `Indexing failed: ${st.error}`;
      return null;
    }
    if (st.ready) return st;
    $('loading').textContent = `Indexing history… ${st.commits.toLocaleString()} commits`;
    await new Promise((r) => setTimeout(r, 500));
  }
}

function fail(err) {
  $('loading').hidden = false;
  $('loading').textContent = String(err);
}
