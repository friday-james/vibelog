// sync.js — fetches /state.json and renders the dashboard.
// Mirrors example.html's structure: status-X cards with card-l1 grid +
// card-detail.card-l2/l3/l4 progressive disclosure.
//
// Card depth is preserved across re-renders by card-id. Without this every
// 2s poll would collapse any open card before the user could read it.

const STATUS_ICONS = {
  intent: '01',
  approach: '02',
  now: '▸',
  violated: '✕',
  risk: '⊘',
  holding: '✓',
  delta: 'Δ',
  anchor: '·',
};

// Card-depth memory keyed by card-id, survives across renders.
const depthMemory = {};
let lastStateHash = null;

async function load() {
  try {
    const r = await fetch('/state.json');
    if (!r.ok) throw new Error(`HTTP ${r.status}: ${await r.text()}`);
    const s = await r.json();
    const hash = quickHash(s);
    if (hash !== lastStateHash) {
      render(s);
      lastStateHash = hash;
    }
    setStatus('synced', `synced · ${nowHHMM()} · ${s.claims?.length ?? 0} claims · ${s.iterations?.length ?? 0} iterations`);
  } catch (e) {
    showError(e.message || String(e));
  }
}

function quickHash(s) {
  // Cheap signature: claim count, iteration count, last iter id+ts, anchor.now stmt.
  return JSON.stringify([
    s.claims?.length, s.iterations?.length,
    s.iterations?.[s.iterations.length - 1]?.id,
    s.iterations?.[s.iterations.length - 1]?.ts,
    s.anchor?.now?.statement, s.anchor?.now?.iteration_id,
    s.anchor?.intent?.statement, s.anchor?.approach?.statement,
  ]);
}

function nowHHMM() {
  return new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
}

function setStatus(state, text) {
  document.getElementById('status').textContent = text;
  const dot = document.getElementById('syncDot');
  if (dot) dot.classList.toggle('synced', state === 'synced');
}

function showError(msg) {
  document.getElementById('app').innerHTML = `<div class="err">${escapeHTML(msg)}</div>`;
  setStatus('error', 'error');
}

function render(s) {
  const app = document.getElementById('app');
  app.innerHTML = '';

  // ===== ANCHOR =====
  app.appendChild(sectionHeader('ANCHOR · WHAT YOU\'RE BUILDING', 'click any to expand'));
  app.appendChild(anchorCard('anchor', 'anchor-intent', '01',
    s.anchor.intent.statement,
    `intent · established · ${s.anchor.intent.established_by || '?'}`,
    s.anchor.intent.evidence, []));
  app.appendChild(anchorCard('anchor', 'anchor-approach', '02',
    s.anchor.approach.statement,
    `approach · ${shortReason(s.anchor.approach.change_reason)}`,
    s.anchor.approach.evidence, []));
  app.appendChild(anchorCard('editing', 'anchor-now', '▸',
    s.anchor.now.statement,
    `now · iter #${s.anchor.now.iteration_id} · ${relTime(s.anchor.now.started)}`,
    [], [], 1));

  // ===== DELTAS — recent iterations since last sync =====
  const iters = (s.iterations || []).slice();
  const recentIters = iters
    .filter(it => it.kind === 'iteration')
    .slice(-3)
    .reverse();
  app.appendChild(sectionHeader('DELTAS · WHAT CHANGED SINCE YOU LAST LOOKED',
    recentIters.length ? `${recentIters.length} recent` : 'none'));
  if (recentIters.length === 0) {
    app.appendChild(empty('(no iterations yet)'));
  } else {
    recentIters.forEach(it => app.appendChild(deltaCard(it)));
  }

  // ===== CLAIMS by status =====
  const claims = s.claims || [];
  const violated = claims.filter(c => c.status === 'violated');
  const suspected = claims.filter(c => c.status === 'suspected');
  const holding = claims.filter(c => c.status === 'holding');

  app.appendChild(sectionHeader('CLAIMS · VIOLATED',
    violated.length === 0 ? 'none' : `${violated.length} violated · hard stop`));
  if (violated.length === 0) app.appendChild(empty('(none)'));
  else violated.forEach(c => app.appendChild(claimCard(c, 'violated')));

  app.appendChild(sectionHeader('CLAIMS · SUSPECTED',
    suspected.length === 0 ? 'none' : `${suspected.length} amber · awaiting verification`));
  if (suspected.length === 0) app.appendChild(empty('(none)'));
  else suspected.forEach(c => app.appendChild(claimCard(c, 'risk')));

  app.appendChild(sectionHeader('CLAIMS · HOLDING (REFERENCE)',
    holding.length === 0 ? 'none' : `${holding.length} with positive evidence · click any to expand`));
  if (holding.length === 0) {
    app.appendChild(empty('(none)'));
  } else {
    const grid = document.createElement('div');
    grid.className = 'compact-list';
    holding.forEach(c => grid.appendChild(compactItem(c)));
    app.appendChild(grid);
  }

  // Rail timeline (interleaves commits + iterations chronologically, newest first)
  renderTimeline(iters);

  // Restore preserved depths
  document.querySelectorAll('.card[data-card-id]').forEach(card => {
    const id = card.dataset.cardId;
    if (depthMemory[id]) card.dataset.depth = depthMemory[id];
  });
}

function shortReason(s) {
  if (!s) return 'approach';
  return s.length > 60 ? s.slice(0, 60) + '…' : s;
}

function sectionHeader(title, count) {
  const h = document.createElement('div');
  h.className = 'section-header';
  h.innerHTML = `<div class="section-title">${escapeHTML(title)}</div><div class="section-count">${escapeHTML(count)}</div>`;
  return h;
}

function anchorCard(statusClass, cardID, icon, head, metaText, evidence, related, initialDepth = 0) {
  return buildCard({
    statusClass, cardID, icon, head, metaText, initialDepth,
    L2: evidenceBlock('EVIDENCE', evidence),
    L3: related?.length ? propagationBlock('RELATED', related) : null,
  });
}

function claimCard(c, statusClass) {
  const icon = STATUS_ICONS[statusClass] || '·';
  const meta = `${c.category} · ${c.severity} · ${c.id}`;
  return buildCard({
    statusClass, cardID: 'claim-' + c.id, icon,
    head: c.statement, metaText: meta, initialDepth: 0,
    L2: evidenceBlock('EVIDENCE', c.evidence),
    L3: c.related_claims?.length ? propagationBlock('RELATED CLAIMS', c.related_claims) : null,
  });
}

function deltaCard(it) {
  const filesCount = (it.files_changed || []).length;
  const meta = `iter #${it.id} · ${relTime(it.ts)}${filesCount ? ` · ${filesCount} file${filesCount === 1 ? '' : 's'}` : ''}`;
  // L2 = files_changed list (our "diff stand-in" — we don't store actual diff text)
  let L2 = null;
  if (it.files_changed?.length) {
    const e = document.createElement('div');
    e.className = 'card-detail-inner';
    e.innerHTML = `<span class="why-label">FILES TOUCHED</span>` + filesBlock(it.files_changed);
    L2 = e.outerHTML;
  }
  // L3 = claims added/violated by this iteration
  let L3 = null;
  if (it.claims_added?.length || it.claims_violated?.length) {
    const items = [
      ...(it.claims_added || []).map(c => ({dir: 'down', text: `+ ${c}`})),
      ...(it.claims_violated || []).map(c => ({dir: 'risk', text: `✗ ${c}`})),
    ];
    L3 = `<span class="why-label">CLAIMS AFFECTED</span><ul class="prop-list">` +
      items.map(i => `<li><span class="prop-dir ${i.dir}">${i.dir}</span>${escapeHTML(i.text)}</li>`).join('') +
      `</ul>`;
  }
  return buildCard({
    statusClass: 'delta', cardID: 'iter-' + it.id, icon: 'Δ',
    head: it.summary || '(no summary)', metaText: meta, initialDepth: 0,
    L2, L3,
  });
}

function buildCard({statusClass, cardID, icon, head, metaText, L2, L3, L4, initialDepth}) {
  const card = document.createElement('article');
  card.className = 'card status-' + statusClass;
  card.dataset.depth = String(initialDepth || 0);
  card.dataset.cardId = cardID;

  const l1 = document.createElement('div');
  l1.className = 'card-l1';
  l1.innerHTML = `
    <span class="card-icon">${escapeHTML(icon)}</span>
    <div class="card-head">${escapeHTML(head)}</div>
    <div class="card-meta">${escapeHTML(metaText)}</div>
  `;
  card.appendChild(l1);

  const collapse = document.createElement('button');
  collapse.className = 'card-collapse';
  collapse.textContent = '▴ collapse';
  card.appendChild(collapse);

  let hasDetail = false;
  if (L2) {
    const el = document.createElement('div');
    el.className = 'card-detail card-l2';
    el.innerHTML = L2;
    card.appendChild(el);
    hasDetail = true;
  }
  if (L3) {
    const el = document.createElement('div');
    el.className = 'card-detail card-l3';
    el.innerHTML = L3;
    card.appendChild(el);
    hasDetail = true;
  }
  if (L4) {
    const el = document.createElement('div');
    el.className = 'card-detail card-l4';
    el.innerHTML = L4;
    card.appendChild(el);
    hasDetail = true;
  }
  if (hasDetail) {
    const more = document.createElement('button');
    more.className = 'card-more';
    more.textContent = 'show more';
    card.appendChild(more);
  }
  return card;
}

function evidenceBlock(label, evidence) {
  if (!evidence || !evidence.length) return null;
  let html = `<span class="why-label">${escapeHTML(label)}</span><div class="evidence">`;
  evidence.forEach(ev => {
    const tagClass = ev.type === 'missing' ? 'miss' : ev.type;
    const tag = ev.type === 'missing' ? `MISSING:${ev.kind || ''}` : ev.type.toUpperCase();
    const path = ev.path || ev.ref || ev.sha || '';
    const polarity = ev.polarity === 'negative' ? ' (✗)' : '';
    if (ev.type === 'missing') {
      html += `<div class="ev-row"><span class="ev-tag miss">${escapeHTML(tag)}</span><span class="ev-empty">${escapeHTML(ev.note || '(no note)')}</span></div>`;
    } else {
      const note = ev.note ? ` <span class="ev-note">· ${escapeHTML(ev.note)}</span>` : '';
      html += `<div class="ev-row"><span class="ev-tag ${tagClass}">${escapeHTML(tag)}</span><span class="ev-path">${escapeHTML(path)}${escapeHTML(polarity)}</span>${note}</div>`;
    }
  });
  html += `</div>`;
  return html;
}

function propagationBlock(label, items) {
  return `<span class="why-label">${escapeHTML(label)}</span><ul class="prop-list">` +
    items.map(i => `<li><span class="prop-dir up">related</span>${escapeHTML(i)}</li>`).join('') +
    `</ul>`;
}

function filesBlock(files) {
  return `<ul class="prop-list">` +
    files.map(f => `<li><span class="prop-dir down">file</span><code style="color:var(--text);font-family:var(--font-mono);font-size:11px">${escapeHTML(f)}</code></li>`).join('') +
    `</ul>`;
}

function compactItem(c) {
  const d = document.createElement('div');
  d.className = 'compact-item';
  d.dataset.cardId = 'compact-' + c.id;
  const evHint = compactEvidenceHint(c.evidence);
  d.innerHTML = `<span class="ci-mark">✓</span><span class="ci-text" title="${escapeHTML(c.id)}">${escapeHTML(c.statement)}</span><span class="ci-evi">${escapeHTML(evHint)}</span>`;
  return d;
}

function compactEvidenceHint(evidence) {
  if (!evidence || !evidence.length) return '';
  const kinds = new Set(evidence.map(e => e.type === 'missing' ? 'miss' : e.type));
  if (kinds.has('test')) return 'test ✓';
  if (kinds.has('code')) return 'code ✓';
  if (kinds.has('doc')) return 'doc ✓';
  if (kinds.has('decision')) return 'adr ✓';
  return [...kinds].join(', ');
}

function renderTimeline(iters) {
  const tl = document.getElementById('timeline');
  if (!tl) return;
  tl.innerHTML = '';
  const sorted = iters.slice().sort((a, b) => new Date(b.ts) - new Date(a.ts));
  sorted.forEach((it, i) => {
    const item = document.createElement('div');
    let klass = 'vtl-item';
    if (it.kind === 'commit') klass += ' commit';
    else if (i === 0) klass += ' active';
    else if (i < 3) klass += ' recent';
    item.className = klass;
    if (it.kind === 'commit') {
      item.innerHTML = `${escapeHTML(it.sha?.slice(0, 7) || '?')}<div class="vtl-sha">${escapeHTML(truncate(it.summary || '', 36))} · ${relTime(it.ts)}</div>`;
    } else {
      item.innerHTML = `iter #${it.id} · ${escapeHTML(truncate(it.summary || '', 36))}<div class="vtl-when">${relTime(it.ts)}${i === 0 ? ' · active' : ''}</div>`;
    }
    tl.appendChild(item);
  });
}

function empty(text) {
  const d = document.createElement('div'); d.className = 'empty'; d.textContent = text;
  return d;
}

function relTime(iso) {
  if (!iso) return '?';
  const d = new Date(iso);
  const s = Math.floor((Date.now() - d.getTime()) / 1000);
  if (s < 60) return `${s}s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  return `${Math.floor(h / 24)}d ago`;
}

function truncate(s, n) {
  return s.length > n ? s.slice(0, n - 1) + '…' : s;
}

function escapeHTML(s) {
  return String(s ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
}

// ===== Event delegation: handles clicks even after re-renders =====
document.addEventListener('click', e => {
  const card = e.target.closest('.card');
  if (!card) return;
  const id = card.dataset.cardId;

  if (e.target.closest('.card-collapse')) {
    e.stopPropagation();
    card.dataset.depth = '0';
    if (id) depthMemory[id] = '0';
    return;
  }
  if (e.target.closest('.card-more') || e.target.closest('.card-l1')) {
    e.stopPropagation();
    const d = parseInt(card.dataset.depth || '0', 10);
    if (d < 3) {
      card.dataset.depth = String(d + 1);
      if (id) depthMemory[id] = String(d + 1);
    }
  }
});

load();
setInterval(load, 2000);
