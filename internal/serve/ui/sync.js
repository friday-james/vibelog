// sync.js — fetches /state.json and renders the dashboard.
// Mirrors example.html's structure: status-X cards with card-l1 grid +
// card-detail.card-l2/l3/l4 progressive disclosure. Polls every 2s; Phase 2
// will replace with SSE.

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

async function load() {
  try {
    const r = await fetch('/state.json');
    if (!r.ok) throw new Error(`HTTP ${r.status}: ${await r.text()}`);
    const s = await r.json();
    render(s);
    setStatus('synced', `last sync · ${nowHHMM()} · ${s.claims?.length ?? 0} claims · ${s.iterations?.length ?? 0} iterations`);
  } catch (e) {
    showError(e.message || String(e));
  }
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
  app.appendChild(anchorCard('anchor', '01', s.anchor.intent.statement,
    `intent · established · ${s.anchor.intent.established_by || '?'}`,
    s.anchor.intent.evidence));
  app.appendChild(anchorCard('anchor', '02', s.anchor.approach.statement,
    `approach · ${s.anchor.approach.change_reason || 'approach'}`,
    s.anchor.approach.evidence));
  app.appendChild(anchorCard('editing', '▸', s.anchor.now.statement,
    `now · iter #${s.anchor.now.iteration_id} · ${relTime(s.anchor.now.started)}`,
    [], 1));

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

  // Iterations in left rail
  renderTimeline(s.iterations || []);

  // Wire progressive disclosure
  wireCardHandlers();
}

function sectionHeader(title, count) {
  const h = document.createElement('div');
  h.className = 'section-header';
  h.innerHTML = `<div class="section-title">${escapeHTML(title)}</div><div class="section-count">${escapeHTML(count)}</div>`;
  return h;
}

function anchorCard(statusClass, icon, head, metaText, evidence, initialDepth = 0) {
  return buildCard(statusClass, icon, head, metaText, evidence, [], initialDepth);
}

function claimCard(c, statusClass) {
  const icon = STATUS_ICONS[statusClass] || '·';
  const meta = `${c.category} · ${c.severity} · ${c.id}`;
  return buildCard(statusClass, icon, c.statement, meta, c.evidence, c.related_claims || [], 0);
}

function buildCard(statusClass, icon, head, metaText, evidence, related, depth) {
  const card = document.createElement('article');
  card.className = 'card status-' + statusClass;
  card.dataset.depth = String(depth || 0);

  const l1 = document.createElement('div');
  l1.className = 'card-l1';
  l1.innerHTML = `
    <span class="card-icon">${escapeHTML(icon)}</span>
    <div class="card-head">${escapeHTML(head)}</div>
    <div class="card-meta">${escapeHTML(metaText)}</div>
  `;
  card.appendChild(l1);

  // collapse button (top right)
  const collapse = document.createElement('button');
  collapse.className = 'card-collapse';
  collapse.textContent = '▴ collapse';
  card.appendChild(collapse);

  // L2 — evidence
  if (evidence && evidence.length) {
    const l2 = document.createElement('div');
    l2.className = 'card-detail card-l2';
    l2.innerHTML = `<span class="why-label">EVIDENCE</span>` + renderEvidence(evidence);
    card.appendChild(l2);
  }

  // L3 — related claims (propagation)
  if (related && related.length) {
    const l3 = document.createElement('div');
    l3.className = 'card-detail card-l3';
    l3.innerHTML = `<span class="why-label">RELATED CLAIMS</span><ul class="prop-list">` +
      related.map(r => `<li><span class="prop-dir up">related</span>${escapeHTML(r)}</li>`).join('') +
      `</ul>`;
    card.appendChild(l3);
  }

  // more button (only if any depth content exists)
  if (evidence?.length || related?.length) {
    const more = document.createElement('button');
    more.className = 'card-more';
    more.textContent = 'show more';
    card.appendChild(more);
  }

  return card;
}

function renderEvidence(evidence) {
  const e = document.createElement('div');
  e.className = 'evidence';
  evidence.forEach(ev => {
    const row = document.createElement('div');
    row.className = 'ev-row';
    const tagClass = ev.type === 'missing' ? 'miss' : ev.type;
    const tag = ev.type === 'missing' ? `MISSING:${ev.kind || ''}` : ev.type.toUpperCase();
    const path = ev.path || ev.ref || ev.sha || '';
    const polarity = ev.polarity === 'negative' ? ' (✗)' : '';
    if (ev.type === 'missing') {
      row.innerHTML = `<span class="ev-tag miss">${escapeHTML(tag)}</span><span class="ev-empty">${escapeHTML(ev.note || '(no note)')}</span>`;
    } else {
      row.innerHTML = `<span class="ev-tag ${tagClass}">${escapeHTML(tag)}</span><span class="ev-path">${escapeHTML(path)}${escapeHTML(polarity)}</span>` +
        (ev.note ? ` <span class="ev-note">· ${escapeHTML(ev.note)}</span>` : '');
    }
    e.appendChild(row);
  });
  return e.outerHTML;
}

function compactItem(c) {
  const d = document.createElement('div');
  d.className = 'compact-item';
  const evHint = compactEvidenceHint(c.evidence);
  d.innerHTML = `<span class="ci-mark">✓</span><span class="ci-text">${escapeHTML(c.statement)}</span><span class="ci-evi">${escapeHTML(evHint)}</span>`;
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
  // newest first
  const sorted = iters.slice().sort((a, b) => new Date(b.ts) - new Date(a.ts));
  sorted.forEach((it, i) => {
    const item = document.createElement('div');
    item.className = 'vtl-item' + (i === 0 ? ' active' : i < 3 ? ' recent' : '') + (it.kind === 'commit' ? ' commit' : '');
    if (it.kind === 'commit') {
      item.innerHTML = `${escapeHTML(it.sha || '?')}<div class="vtl-sha">${escapeHTML(it.summary || '')} · ${relTime(it.ts)}</div>`;
    } else {
      item.innerHTML = `iter #${it.id} · ${escapeHTML(truncate(it.summary || '', 40))}<div class="vtl-when">${relTime(it.ts)}${i === 0 ? ' · active' : ''}</div>`;
    }
    tl.appendChild(item);
  });
}

function wireCardHandlers() {
  document.querySelectorAll('.card-more').forEach(btn => {
    btn.addEventListener('click', e => {
      e.stopPropagation();
      const card = btn.closest('.card');
      const d = parseInt(card.dataset.depth || '0', 10);
      if (d < 3) card.dataset.depth = String(d + 1);
    });
  });
  document.querySelectorAll('.card-collapse').forEach(btn => {
    btn.addEventListener('click', e => {
      e.stopPropagation();
      btn.closest('.card').dataset.depth = '0';
    });
  });
  document.querySelectorAll('.card-l1').forEach(l1 => {
    l1.addEventListener('click', e => {
      // Don't advance when clicking on a button inside the card
      if (e.target.tagName === 'BUTTON') return;
      const card = l1.closest('.card');
      const d = parseInt(card.dataset.depth || '0', 10);
      if (d < 3) card.dataset.depth = String(d + 1);
    });
  });
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

load();
setInterval(load, 2000);
