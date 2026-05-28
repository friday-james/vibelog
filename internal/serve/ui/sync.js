// sync.js — fetches /state.json and renders the dashboard.
// Mirrors example.html's structure: status-X cards with card-l1 grid +
// card-detail.card-l2/l3/l4 progressive disclosure.
//
// Card depth is preserved across re-renders by card-id. Re-render is skipped
// entirely when /state.json content hasn't materially changed.

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
    updateVelocity(s);
    setStatus('synced', syncStateText(s));
  } catch (e) {
    showError(e.message || String(e));
  }
}

function quickHash(s) {
  return JSON.stringify([
    s.claims?.length, s.iterations?.length,
    s.iterations?.[s.iterations.length - 1]?.id,
    s.iterations?.[s.iterations.length - 1]?.ts,
    s.anchor?.now?.statement, s.anchor?.now?.iteration_id,
    s.anchor?.intent?.statement, s.anchor?.approach?.statement,
    s.anchor?.approach?.last_changed,
  ]);
}

function nowHHMM() {
  return new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
}

function syncStateText(s) {
  const iters = s.iterations || [];
  if (iters.length === 0) return 'no iterations yet';
  const last = iters[iters.length - 1];
  return `${iters.length} iterations · last ${relTime(last.ts)}`;
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

// ===== VELOCITY (header AGENT/YOU bars) =====
// agent rate = iterations/min in last 5min; human rate = currently mocked to 0
// (we don't track ack events yet — Phase 4).
function updateVelocity(s) {
  const iters = (s.iterations || []).filter(it => it.kind === 'iteration');
  const windowMs = 5 * 60 * 1000;
  const cutoff = Date.now() - windowMs;
  const recent = iters.filter(it => new Date(it.ts).getTime() >= cutoff);
  const agentPerMin = recent.length / 5;
  const humanPerMin = 0;

  // Cap the bar at 4 iter/min visually; that's where it saturates.
  const agentPct = Math.min(100, (agentPerMin / 4) * 100);
  const humanPct = Math.min(100, (humanPerMin / 4) * 100);

  document.getElementById('vbarAgent').style.width = agentPct + '%';
  document.getElementById('vbarHuman').style.width = humanPct + '%';
  document.getElementById('vvalAgent').textContent = fmtRate(agentPerMin);
  document.getElementById('vvalHuman').textContent = fmtRate(humanPerMin);

  // DRIFT: agent ≫ human for sustained window. Since human=0 always for now,
  // any sustained agent activity triggers drift. Threshold tuned to avoid spurious.
  const drift = agentPerMin > 0.5 && humanPerMin < 0.1;
  document.getElementById('vdrift').classList.toggle('active', drift);
}

function fmtRate(r) {
  if (r === 0) return '0/min';
  if (r < 0.1) return r.toFixed(2) + '/min';
  return r.toFixed(1) + '/min';
}

// ===== Card-head emphasis: backticks → <code>, *stars* → <em> =====
// Allows claim/anchor statements like "POST /magic-link rate-limit on `gateway`"
// to render with amber emphasis on the code/em parts.
function renderEmphasis(s) {
  return escapeHTML(s)
    .replace(/`([^`]+)`/g, '<code>$1</code>')
    .replace(/\*([^*]+)\*/g, '<span class="em">$1</span>');
}

// ===== Card-meta phrasing (matches mockup's editorial voice) =====
function intentMeta(intent) {
  return `intent · stable · ${relTime(intent.established)}`;
}
function approachMeta(approach) {
  const ms = Date.now() - new Date(approach.last_changed).getTime();
  if (ms < 24 * 3600 * 1000) return 'approach · changed today';
  return `approach · changed ${relTime(approach.last_changed)}`;
}
function nowMeta(now) {
  return `now · iter #${now.iteration_id} · ${relTime(now.started)}`;
}
function claimMeta(c) {
  if (c.status === 'violated') {
    return c.established_in_iteration
      ? `violated · introduced iter #${c.established_in_iteration}`
      : 'violated';
  }
  if (c.status === 'suspected') {
    return c.established_in_iteration
      ? `suspected · awaiting iter #${c.established_in_iteration}`
      : 'suspected · awaiting verification';
  }
  if (c.status === 'holding') {
    return c.established_in_iteration
      ? `holding · iter #${c.established_in_iteration}`
      : 'holding';
  }
  return c.status;
}

// ===== Evidence tag labels (editorial vocabulary per mockup) =====
function evTagLabel(ev) {
  if (ev.type === 'missing') {
    return {
      'test': 'NO TEST',
      'comms': 'NO COMMS',
      'decision': 'NO ADR',
      'verification': 'UNVERIFIED',
    }[ev.kind] || `MISSING:${ev.kind || '?'}`;
  }
  if (ev.type === 'decision') return 'ADR';
  return ev.type.toUpperCase();
}
function evTagClass(ev) {
  if (ev.type === 'missing') return 'miss';
  if (ev.type === 'test') return 'test';
  if (ev.type === 'code' || ev.type === 'decision' || ev.type === 'commit') return 'code';
  return ''; // default amber tag
}

// ===== render =====
function render(s) {
  const app = document.getElementById('app');
  app.innerHTML = '';

  // ANCHOR
  app.appendChild(sectionHeader('ANCHOR · WHAT YOU\'RE BUILDING', 'click any to expand'));
  app.appendChild(anchorCard('anchor', 'anchor-intent', '01',
    s.anchor.intent.statement, intentMeta(s.anchor.intent), s.anchor.intent));
  app.appendChild(anchorCard('anchor', 'anchor-approach', '02',
    s.anchor.approach.statement, approachMeta(s.anchor.approach), s.anchor.approach));
  // "now" L2 — synthesize evidence from the latest iteration's files_changed
  // so the auto-expanded card isn't empty (mockup has rich narrative; our
  // data doesn't, so we surface what we DO have).
  const latestIter = (s.iterations || []).slice().reverse().find(it => it.kind === 'iteration');
  const nowEvidence = [];
  if (latestIter?.files_changed?.length) {
    latestIter.files_changed.slice(0, 6).forEach(f => {
      nowEvidence.push({ type: 'code', path: f, polarity: 'positive', note: `touched in iter #${latestIter.id}` });
    });
  }
  app.appendChild(anchorCard('editing', 'anchor-now', '▸',
    s.anchor.now.statement, nowMeta(s.anchor.now), { evidence: nowEvidence }, 1));

  // DELTAS — 5 most recent entries (iterations + commits interleaved by ts)
  const allIters = s.iterations || [];
  const recent = allIters
    .slice()
    .sort((a, b) => new Date(b.ts) - new Date(a.ts))
    .slice(0, 5);
  app.appendChild(sectionHeader('DELTAS · WHAT CHANGED SINCE YOU LAST LOOKED',
    recent.length ? `${recent.length} unack'd · 0 ack'd` : 'none'));
  if (recent.length === 0) {
    app.appendChild(empty('(no iterations yet)'));
  } else {
    recent.forEach(it => {
      app.appendChild(it.kind === 'commit' ? commitCard(it) : deltaCard(it));
    });
  }

  // CLAIMS · VIOLATED
  const claims = s.claims || [];
  const violated = claims.filter(c => c.status === 'violated');
  app.appendChild(sectionHeader('CLAIMS · VIOLATED',
    violated.length === 0 ? 'none' : `${violated.length} violated · hard stop before merge`, 'violated'));
  if (violated.length === 0) app.appendChild(empty('(none)'));
  else violated.forEach(c => app.appendChild(claimCard(c, 'violated')));

  // CLAIMS · AT RISK FROM THIS EDIT (status=suspected, per audit #6)
  const suspected = claims.filter(c => c.status === 'suspected');
  app.appendChild(sectionHeader('CLAIMS · AT RISK FROM THIS EDIT',
    suspected.length === 0 ? 'none' : `${suspected.length} amber · derived from blast radius`, 'risk'));
  if (suspected.length === 0) app.appendChild(empty('(none)'));
  else suspected.forEach(c => app.appendChild(claimCard(c, 'risk')));

  // CLAIMS · HOLDING (compact list) — visible by default, matches example.html
  const holding = claims.filter(c => c.status === 'holding');
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

  // BLAST RADIUS · what this edit touches
  renderBlastRadius(app, allIters);

  // Rail timeline
  renderTimeline(allIters);

  // Restore preserved card depths
  document.querySelectorAll('.card[data-card-id]').forEach(card => {
    const id = card.dataset.cardId;
    if (depthMemory[id]) card.dataset.depth = depthMemory[id];
  });
}

function sectionHeader(title, count, sev) {
  const h = document.createElement('div');
  h.className = 'section-header' + (sev ? ' sev-' + sev : '');
  h.innerHTML = `<div class="section-title">${escapeHTML(title)}</div><div class="section-count">${escapeHTML(count)}</div>`;
  return h;
}

// ===== Anchor cards =====
// L2: WHY prose (statement repeated as the explanation, since we don't have
// separate WHY text) + evidence box. L3: alternatives/decision (from extra
// evidence if any). L4: propagation (related_claims, if present).
function anchorCard(statusClass, cardID, icon, head, metaText, anchorSection, initialDepth = 0) {
  const evidence = anchorSection?.evidence || [];
  return buildCard({
    statusClass, cardID, icon, head, metaText, initialDepth,
    L2: whyBlock('WHY', head, evidence),
    L3: null,
    L4: null,
  });
}

// ===== Claim cards =====
function claimCard(c, statusClass) {
  const icon = STATUS_ICONS[statusClass] || '·';
  const meta = claimMeta(c);
  return buildCard({
    statusClass, cardID: 'claim-' + c.id, icon,
    head: c.statement, metaText: meta, initialDepth: 0,
    L2: whyBlock(statusClass === 'violated' ? 'WHY VIOLATED'
              : statusClass === 'risk' ? 'WHY AT RISK'
              : 'WHY HOLDING', c.statement, c.evidence),
    L3: c.related_claims?.length ? propagationBlock('RELATED CLAIMS', c.related_claims) : null,
    L4: null,
  });
}

// ===== Delta cards (recent iterations) =====
function deltaCard(it) {
  const files = it.files_changed || [];
  // Surface file count + first two basenames at L0 so the programmer's PR-review
  // muscle memory ("which files?") is answered without expanding.
  let fileHint = '';
  if (files.length > 0) {
    const names = files.slice(0, 2).map(f => f.split('/').pop());
    fileHint = ` · ${files.length} file${files.length === 1 ? '' : 's'}: ${names.join(', ')}${files.length > 2 ? '…' : ''}`;
  }
  const meta = `iter #${it.id} · ${relTime(it.ts)}${fileHint}`;
  // L2: WHY prose + claim-effects evidence list
  let claimsEv = [];
  (it.claims_added || []).forEach(cid => claimsEv.push({type: 'doc', path: 'claims#' + cid, note: 'CLAIM ADDED'}));
  (it.claims_violated || []).forEach(cid => claimsEv.push({type: 'missing', kind: 'verification', note: 'CLAIM VIOLATED: ' + cid}));
  const L2 = whyBlock('WHY', it.summary || '', claimsEv);

  // L3: stand-in for DIFF — files touched as a list (we don't store diff text)
  let L3 = null;
  if (it.files_changed?.length) {
    L3 = `<span class="why-label">DIFF · FILES TOUCHED</span>` +
      `<div class="diff"><div class="diff-head">iter #${it.id}</div><pre>` +
      it.files_changed.map(f => `<span class="ctx">${escapeHTML(f)}</span>`).join('') +
      `</pre></div>`;
  }

  // L4: propagation (claims affected by this iteration)
  let L4 = null;
  if (it.claims_added?.length || it.claims_violated?.length) {
    const items = [
      ...(it.claims_added || []).map(c => ({dir: 'down', text: c, label: 'added'})),
      ...(it.claims_violated || []).map(c => ({dir: 'risk', text: c, label: 'violated'})),
    ];
    L4 = `<span class="why-label">PROPAGATION</span><ul class="prop-list">` +
      items.map(i => `<li><span class="prop-dir ${i.dir}">${i.label}</span>${escapeHTML(i.text)}</li>`).join('') +
      `</ul>`;
  }

  return buildCard({
    statusClass: 'delta', cardID: 'iter-' + it.id, icon: 'Δ',
    head: it.summary || '(no summary)', metaText: meta, initialDepth: 0,
    L2, L3, L4,
  });
}

// ===== Commit cards =====
// L1: shortSHA + commit subject
// L2: WHY — lazy-loaded commit message body (the explanation layer)
// L3: DIFF — one .diff block per file
// Both L2 and L3 are populated by a single /git/show fetch (loadCommitDetails).
function commitCard(it) {
  const sha = it.sha || '';
  const shortSHA = sha.slice(0, 7);
  const meta = `commit · ${shortSHA} · ${relTime(it.ts)}`;
  // Use a graphic icon (◉) instead of the SHA so the 22px icon column doesn't
  // overflow into the head column. The SHA lives in card-meta where there's room.
  const L2 = `<span class="why-label">WHY</span>` +
    `<p class="why-text" data-commit-body="${escapeHTML(sha)}">${renderEmphasis(it.summary || '')}</p>`;
  const L3 = `<span class="why-label">DIFF · PER FILE</span>` +
    `<div class="diff-files" data-diff-sha="${escapeHTML(sha)}">` +
    `<div class="diff"><div class="diff-head">expand to load · ${escapeHTML(shortSHA)}</div></div>` +
    `</div>`;
  return buildCard({
    statusClass: 'delta', cardID: 'commit-' + sha, icon: '◉',
    head: it.summary || '(commit)', metaText: meta, initialDepth: 0,
    L2, L3, L4: null,
  });
}

// Lazy-load commit body (L2) and per-file diff blocks (L3) from a single
// /git/show fetch. Called when a commit card first reaches depth ≥ 1.
async function loadCommitDetails(card) {
  const container = card.querySelector('.diff-files[data-diff-sha]');
  if (!container || container.dataset.loaded === '1') return;
  container.dataset.loaded = '1';
  const sha = container.dataset.diffSha;
  container.innerHTML = `<div class="diff"><div class="diff-head">${escapeHTML(sha.slice(0, 7))} · loading…</div></div>`;
  try {
    const r = await fetch('/git/show/' + encodeURIComponent(sha));
    if (!r.ok) throw new Error(`HTTP ${r.status}`);
    const text = await r.text();
    const parsed = parseGitShow(text);

    // L2: replace the placeholder body with the parsed commit message body.
    const bodyEl = card.querySelector(`.why-text[data-commit-body]`);
    if (bodyEl && parsed.body) bodyEl.innerHTML = renderEmphasis(parsed.body);

    // L3: one .diff block per file.
    container.innerHTML = '';
    if (parsed.files.length === 0) {
      container.innerHTML = `<div class="diff"><div class="diff-head">no file changes</div></div>`;
      return;
    }
    parsed.files.forEach(f => {
      const block = document.createElement('div');
      block.className = 'diff';
      const head = document.createElement('div');
      head.className = 'diff-head';
      head.textContent = f.path;
      block.appendChild(head);
      block.appendChild(formatDiffLines(f.lines));
      container.appendChild(block);
    });
  } catch (e) {
    container.innerHTML = `<div class="diff"><div class="diff-head">error</div><pre><span class="del">${escapeHTML(String(e))}</span></pre></div>`;
    container.dataset.loaded = '0';
  }
}

// Parse `git show` output into {body, files: [{path, lines[]}]}.
// The body is the commit message between the Date: line and the first
// `diff --git` boundary (mockup-style indentation: 4 spaces, stripped).
function parseGitShow(text) {
  const lines = text.split('\n');
  let body = '';
  const files = [];
  let i = 0;
  let afterDate = false;
  for (; i < lines.length; i++) {
    if (lines[i].startsWith('Date:')) { afterDate = true; continue; }
    if (lines[i].startsWith('diff --git')) break;
    if (afterDate) {
      if (lines[i].startsWith('    ')) body += lines[i].slice(4) + '\n';
      else if (body && lines[i].trim() === '') body += '\n';
    }
  }
  body = body.trim();
  // Per-file split on `diff --git` boundaries
  let cur = null;
  for (; i < lines.length; i++) {
    if (lines[i].startsWith('diff --git')) {
      if (cur) files.push(cur);
      const m = lines[i].match(/diff --git a\/(\S+) b\//);
      cur = { path: m ? m[1] : '(unknown)', lines: [] };
      continue; // header line itself is replaced by .diff-head; don't include in body
    }
    if (cur) cur.lines.push(lines[i]);
  }
  if (cur) files.push(cur);
  return { body, files };
}

// Render an array of diff lines into a colored <pre>.
function formatDiffLines(lines) {
  const pre = document.createElement('pre');
  lines.forEach(line => {
    const span = document.createElement('span');
    span.textContent = line + '\n';
    if (line.startsWith('+++') || line.startsWith('---')) span.className = 'diff-file';
    else if (line.startsWith('@@')) span.className = 'diff-hunk';
    else if (line.startsWith('+')) span.className = 'add';
    else if (line.startsWith('-')) span.className = 'del';
    else if (line.startsWith('index ') || line.startsWith('new file') || line.startsWith('deleted file') || line.startsWith('similarity')) span.className = 'diff-meta';
    else span.className = 'ctx';
    pre.appendChild(span);
  });
  return pre;
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
    <div class="card-head">${renderEmphasis(head)}</div>
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

// whyBlock = WHY label + evidence box.
// (Mockup pattern from example.html L761-769. We don't have separate narrative
// data, so the previous "prose" arg just repeated the head — removed.)
function whyBlock(label, _unused, evidence) {
  let html = `<div class="why-label">${escapeHTML(label)}</div>`;
  if (evidence && evidence.length) {
    html += `<div class="evidence">`;
    evidence.forEach(ev => {
      const tag = evTagLabel(ev);
      const tagClass = evTagClass(ev);
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
  }
  return html;
}

function propagationBlock(label, items) {
  return `<span class="why-label">${escapeHTML(label)}</span><ul class="prop-list">` +
    items.map(i => `<li><span class="prop-dir up">related</span>${escapeHTML(i)}</li>`).join('') +
    `</ul>`;
}

function compactItem(c) {
  const d = document.createElement('div');
  d.className = 'compact-item';
  d.dataset.cardId = 'compact-' + c.id;
  const evHint = compactEvidenceHint(c.evidence);
  d.innerHTML = `<span class="ci-mark">✓</span><span class="ci-text" title="${escapeHTML(c.id)}">${renderEmphasis(c.statement)}</span><span class="ci-evi">${escapeHTML(evHint)}</span>`;
  return d;
}

function compactEvidenceHint(evidence) {
  if (!evidence || !evidence.length) return '';
  const kinds = new Set(evidence.map(e => e.type === 'missing' ? 'miss' : e.type));
  if (kinds.has('test')) return 'test ✓';
  if (kinds.has('code')) return 'code ✓';
  if (kinds.has('benchmark')) return 'infra ✓';
  if (kinds.has('doc')) return 'doc ✓';
  if (kinds.has('decision')) return 'adr ✓';
  return [...kinds].join(', ');
}

// ===== BLAST RADIUS — files touched by the most recent agent iteration =====
function renderBlastRadius(app, allIters) {
  const lastIter = [...allIters].reverse().find(it => it.kind === 'iteration');
  const files = lastIter?.files_changed || [];
  const unverified = files.filter(f => /(_test\.go|tests?\/|\.yaml$|\.json$)/.test(f) ? false : true).length;
  app.appendChild(sectionHeader('BLAST RADIUS · WHAT THIS EDIT TOUCHES',
    files.length ? `${files.length} locations · ${unverified} unverified` : 'none'));
  if (files.length === 0) {
    app.appendChild(empty('(no recent file changes)'));
    return;
  }
  const list = document.createElement('div');
  list.className = 'blast-list';
  files.forEach((f, i) => list.appendChild(blastItem(f, i, files.length)));
  app.appendChild(list);
}

function blastItem(path, idx, total) {
  const sev = blastSeverity(path, idx, total);
  const why = blastWhy(path);
  const status = blastStatus(path);
  const item = document.createElement('div');
  item.className = 'blast-item';
  item.innerHTML = `
    <span class="blast-sev ${sev.toLowerCase()}">${sev}</span>
    <span class="blast-path">${escapeHTML(path)}</span>
    <span class="blast-why">${escapeHTML(why)}</span>
    <span class="blast-status${status.unverified ? ' unverified' : ''}">${escapeHTML(status.label)}</span>
  `;
  return item;
}

function blastSeverity(path, idx, total) {
  if (/_test\.go$|^tests?\//.test(path)) return 'MED';
  if (/^docs?\//.test(path) || /\.md$/.test(path)) return 'LOW';
  if (idx < 2) return 'HIGH';
  if (idx < Math.ceil(total / 2)) return 'MED';
  return 'LOW';
}
function blastWhy(path) {
  if (/_test\.go$|^tests?\//.test(path)) return 'tests';
  if (/^docs?\//.test(path) || /\.md$/.test(path)) return 'docs';
  if (/^cmd\//.test(path)) return 'CLI entry';
  if (/^internal\/serve/.test(path)) return 'server / UI';
  if (/^internal\/model/.test(path)) return 'core data model';
  if (/^internal\//.test(path)) return 'internal package';
  if (/^\.sync\//.test(path)) return '.sync/ state';
  return 'change';
}
function blastStatus(path) {
  if (/_test\.go$/.test(path)) return {label: 'verified', unverified: false};
  if (/^\.sync\//.test(path)) return {label: 'data', unverified: false};
  return {label: 'unverified', unverified: true};
}

// ===== RAIL timeline =====
function renderTimeline(iters) {
  const tl = document.getElementById('timeline');
  if (!tl) return;
  tl.innerHTML = '';
  const sorted = iters.slice().sort((a, b) => new Date(b.ts) - new Date(a.ts));
  // Rail title gets a time-window suffix (mockup: "ITERATIONS · 16h")
  const railTitle = document.getElementById('railTitle');
  if (railTitle) {
    if (sorted.length >= 2) {
      const newest = new Date(sorted[0].ts);
      const oldest = new Date(sorted[sorted.length - 1].ts);
      railTitle.textContent = `ITERATIONS · ${spanLabel(newest - oldest)}`;
    } else {
      railTitle.textContent = 'ITERATIONS';
    }
  }
  // Render items first; then measure and place the SINCE LAST SYNC band over
  // the top N (currently top 3 — Phase 4 will read .sync/local/ack.json).
  const bandCount = Math.min(3, sorted.length);
  sorted.forEach((it, i) => {
    const item = document.createElement('div');
    let klass = 'vtl-item';
    if (it.kind === 'commit') klass += ' commit';
    else if (i === 0) klass += ' active';
    else if (i < 3) klass += ' recent';
    item.className = klass;
    item.dataset.kind = it.kind;
    if (it.kind === 'commit') {
      item.dataset.sha = it.sha || '';
      item.innerHTML = `${escapeHTML(it.sha?.slice(0, 7) || '?')}<div class="vtl-sha">${escapeHTML(truncate(it.summary || '', 32))}</div><div class="vtl-when">${relTime(it.ts)}</div>`;
    } else {
      item.dataset.iterId = String(it.id);
      item.innerHTML = `iter #${it.id} · ${escapeHTML(truncate(it.summary || '', 36))}<div class="vtl-when">${relTime(it.ts)}${i === 0 ? ' · active' : ''}</div>`;
    }
    tl.appendChild(item);
  });
  // Measure actual item heights and place the SINCE LAST SYNC band BEHIND the
  // first N items. Insert the band as the FIRST child of .vtl so the items
  // (later siblings) render on top of it.
  if (bandCount > 0) {
    const items = tl.querySelectorAll('.vtl-item');
    if (items.length) {
      const first = items[0];
      const last = items[bandCount - 1];
      const top = first.offsetTop;
      const bottom = last.offsetTop + last.offsetHeight;
      const band = document.createElement('div');
      band.className = 'vtl-band';
      band.style.top = (top - 4) + 'px';
      band.style.height = (bottom - top + 8) + 'px';
      band.innerHTML = `<div class="vtl-band-label">SINCE LAST SYNC</div>`;
      tl.insertBefore(band, tl.firstChild);
    }
  }
}

function scrollToCard(cardID, expandToDepth = 0) {
  const card = document.querySelector(`.card[data-card-id="${cardID}"]`);
  if (!card) return false;
  if (expandToDepth > 0) {
    card.dataset.depth = String(expandToDepth);
    depthMemory[cardID] = String(expandToDepth);
    if (cardID.startsWith('commit-')) loadCommitDetails(card);
  }
  card.scrollIntoView({behavior: 'smooth', block: 'center'});
  card.classList.add('flash');
  setTimeout(() => card.classList.remove('flash'), 1200);
  return true;
}

// ===== depth-button rail handlers =====
function handleDepthCmd(cmd) {
  if (cmd === 'claims') {
    document.querySelector('.section-header .section-title:where(:not([data-x]))')?.scrollIntoView();
    const target = [...document.querySelectorAll('.section-title')].find(t => t.textContent.includes('VIOLATED'));
    target?.closest('.section-header')?.scrollIntoView({behavior: 'smooth', block: 'start'});
  } else if (cmd === 'risks') {
    const target = [...document.querySelectorAll('.section-title')].find(t => t.textContent.includes('AT RISK'));
    target?.closest('.section-header')?.scrollIntoView({behavior: 'smooth', block: 'start'});
  } else if (cmd === 'why') {
    // Open the most recent delta's WHY (L2), not just scroll to the section.
    const firstDelta = document.querySelector('.card.status-delta[data-card-id]');
    if (firstDelta) scrollToCard(firstDelta.dataset.cardId, 2);
  } else if (cmd === 'model') {
    alert('?model not in v1 — Phase 6 will surface architecture from CONTEXT.md / ADRs.');
  }
}

function empty(text) {
  const d = document.createElement('div'); d.className = 'empty'; d.textContent = text;
  return d;
}

function spanLabel(ms) {
  const m = Math.floor(ms / 60000);
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 48) return `${h}h`;
  return `${Math.floor(h / 24)}d`;
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

// ===== Event delegation (survives DOM rebuilds) =====
document.addEventListener('click', e => {
  // depth buttons in rail
  const depthBtn = e.target.closest('.depth-btn');
  if (depthBtn) {
    e.stopPropagation();
    handleDepthCmd(depthBtn.dataset.depthCmd);
    return;
  }

  // rail timeline items navigate to their main-column card
  const railItem = e.target.closest('.vtl-item');
  if (railItem) {
    if (railItem.dataset.kind === 'commit' && railItem.dataset.sha) {
      e.stopPropagation();
      scrollToCard('commit-' + railItem.dataset.sha, 3); // expand to L3 (diff)
      return;
    }
    if (railItem.dataset.kind === 'iteration' && railItem.dataset.iterId) {
      e.stopPropagation();
      scrollToCard('iter-' + railItem.dataset.iterId, 0);
      return;
    }
  }

  const card = e.target.closest('.card');
  if (!card) return;
  const id = card.dataset.cardId;
  if (e.target.closest('.card-collapse')) {
    e.stopPropagation();
    card.dataset.depth = '0';
    if (id) depthMemory[id] = '0';
    return;
  }
  // card-more: increment, stop at 3 (matches mockup line 1205)
  if (e.target.closest('.card-more')) {
    e.stopPropagation();
    const d = parseInt(card.dataset.depth || '0', 10);
    if (d < 3) {
      const newDepth = d + 1;
      card.dataset.depth = String(newDepth);
      if (id) depthMemory[id] = String(newDepth);
      // Commit cards: trigger lazy-load at L1 so the explanation appears
      // as soon as the user expands (before they reach the diff at L3).
      if (newDepth >= 1 && id?.startsWith('commit-')) loadCommitDetails(card);
    }
    return;
  }
  // card-l1 click: increment, wrap 3→0 (matches mockup line 1222)
  if (e.target.closest('.card-l1') && !e.target.closest('button')) {
    e.stopPropagation();
    const d = parseInt(card.dataset.depth || '0', 10);
    const newDepth = d < 3 ? d + 1 : 0;
    card.dataset.depth = String(newDepth);
    if (id) depthMemory[id] = String(newDepth);
    if (newDepth >= 1 && id?.startsWith('commit-')) loadCommitDetails(card);
  }
});

load();
setInterval(load, 2000);
