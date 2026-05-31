// sync.js — fetches /state.json and renders the dashboard.
// Mirrors example.html's structure: status-X cards with card-l1 grid +
// card-detail.card-l2/l3/l4 progressive disclosure.
//
// Card depth is preserved across re-renders by card-id. Re-render is skipped
// entirely when /state.json content hasn't materially changed.

const depthMemory = {};
let lastStateHash = null;
let currentState = null;
// Multi-session UI state, recomputed per render() and consumed by deltaCard().
// sessionPalette: { sessionId → cssColor } when ≥2 distinct sessions exist; null otherwise.
// concurrencyIndex: { iterId → { overwrites: [files], crossSessions: [{file, session}] } }
let sessionPalette = null;
let concurrencyIndex = {};

async function load() {
  try {
    const r = await fetch('state.json');
    if (!r.ok) throw new Error(`HTTP ${r.status}: ${await r.text()}`);
    const s = await r.json();
    currentState = s;
    const hash = quickHash(s);
    if (hash !== lastStateHash) {
      render(s);
      lastStateHash = hash;
    }
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
  // Match the PROMPTS section count: only kind === 'iteration' entries.
  // Old entries with other kinds (e.g. commits) bloated the header number
  // and produced a mismatch with the section header.
  const iters = (s.iterations || []).filter(it => it.kind === 'iteration');
  if (iters.length === 0) return 'no prompts yet';
  const last = iters[iters.length - 1];
  return `${iters.length} prompt${iters.length === 1 ? '' : 's'} · last ${relTime(last.ts)}`;
}

function setStatus(state, text) {
  const el = document.getElementById('status');
  if (el) el.textContent = text;
}

function showError(msg) {
  document.getElementById('app').innerHTML = `<div class="err">${escapeHTML(msg)}</div>`;
  setStatus('error', 'error');
}

// (Velocity meter removed — was not actionable without ack tracking.)

// ===== Card-head emphasis: backticks → <code>, *stars* → <em> =====
// Allows claim/anchor statements like "POST /magic-link rate-limit on `gateway`"
// to render with amber emphasis on the code/em parts.
function renderEmphasis(s) {
  return escapeHTML(s)
    .replace(/`([^`]+)`/g, '<code>$1</code>')
    .replace(/\*([^*]+)\*/g, '<span class="em">$1</span>');
}

// ===== render =====
function render(s) {
  const app = document.getElementById('app');
  app.innerHTML = '';

  // v1 dashboard is prompts-only. Status banner, ANCHOR, CLAIMS sections,
  // and BLAST RADIUS are stripped for simplicity — their designs are saved
  // in /Users/jai/.claude/projects/-Users-jai-Documents-ai-vibelog/memory/
  // for revisit after the next compacted session.
  const allIters = s.iterations || [];
  // Prompts feed = iteration + external_edit, single linear timeline.
  const feed = allIters
    .filter(it => it.kind === 'iteration' || it.kind === 'external_edit')
    .slice()
    .sort((a, b) => new Date(b.ts) - new Date(a.ts));

  // Session palette only when ≥2 distinct (non-external) sessions are
  // present — keeps the single-session view uncluttered.
  const sessions = new Set(feed
    .filter(it => it.kind === 'iteration')
    .map(it => it.session_id)
    .filter(x => x));
  sessionPalette = sessions.size >= 2 ? buildSessionPalette([...sessions]) : null;

  // Concurrency index: iter id → list of other session_ids that touched any
  // of the same files within CONCURRENCY_WINDOW_MS.
  concurrencyIndex = buildConcurrencyIndex(feed);

  // No count here — the header sync-state already reports "X prompts · last …".
  app.appendChild(sectionHeader('PROMPTS · WHAT THE AGENT DID', ''));
  if (feed.length === 0) {
    app.appendChild(empty('(no prompts yet)'));
  } else {
    feed.forEach(it => {
      // Drop external_edit cards from the main feed — they're history-only
      // now; live drift lives in the leading card above.
      if (it.kind === 'external_edit') return;
      app.appendChild(deltaCard(it));
    });
  }

  // Rail timeline
  renderTimeline(allIters);

  // Restore preserved card depths
  document.querySelectorAll('.card[data-card-id]').forEach(card => {
    const id = card.dataset.cardId;
    if (depthMemory[id]) card.dataset.depth = depthMemory[id];
  });

  // Dev/docs helper: `?expand=N&card=M` opens the Mth card (0-indexed) to depth N
  // and auto-loads its diff details. Used for README screenshots.
  const params = new URLSearchParams(location.search);
  const expandDepth = parseInt(params.get('expand') || '0', 10);
  const cardIdx = parseInt(params.get('card') || '0', 10);
  if (expandDepth > 0) {
    const cards = document.querySelectorAll('#app .card[data-card-id]');
    const target = cards[cardIdx];
    if (target) {
      const max = parseInt(target.dataset.maxDepth || '0', 10);
      const d = Math.min(expandDepth, max);
      target.dataset.depth = String(d);
      // Auto-open all file-diff <details> inside the expanded card and trigger
      // their lazy-loaders so the diff text renders.
      target.querySelectorAll('details.file-diff').forEach(det => {
        det.open = true;
        loadFileDiff(det);
      });
    }
  }
}

function sectionHeader(title, count) {
  const h = document.createElement('div');
  h.className = 'section-header';
  h.innerHTML = `<div class="section-title">${escapeHTML(title)}</div><div class="section-count">${escapeHTML(count)}</div>`;
  return h;
}

// ===== Prompt cards =====
// L0: head (user prompt) + subtitle (assistant's last text block, clamped).
// L1: IMPLEMENTATION — the agent's full teach-back (every text block joined).
// L2: TOUCHED — clean file list (concrete data).
// L3: DIFFS — per-file prompt-to-prompt diffs (lazy-loaded).
// buildSessionPalette: stable hash-based mapping session_id → color. The
// palette is small (5 entries) and the choice is deterministic per id so it
// stays consistent across renders. Only invoked when ≥2 distinct sessions
// are present, so the chip never appears in single-session usage.
const SESSION_COLORS = ['#d4a14a', '#6c93b3', '#7aa86e', '#c46a52', '#b56cb3'];
function buildSessionPalette(sessionIds) {
  const map = {};
  for (const sid of sessionIds) {
    let h = 0;
    for (let i = 0; i < sid.length; i++) h = ((h << 5) - h + sid.charCodeAt(i)) | 0;
    map[sid] = SESSION_COLORS[Math.abs(h) % SESSION_COLORS.length];
  }
  return map;
}
function shortSession(sid) {
  if (!sid) return '';
  return sid.slice(0, 4);
}

// buildConcurrencyIndex: O(n) walk of the feed (oldest → newest) maintaining
// a per-file "last touch" record. For each iter, classify each touched file:
//   prior is external_edit → record under `overwrites` (highest urgency)
//   prior is a different session → record under `crossSessions`
//   same session OR no prior → no badge
// Result is keyed by iter id for cheap lookup in deltaCard.
function buildConcurrencyIndex(feed) {
  const oldFirst = [...feed].sort((a, b) => new Date(a.ts) - new Date(b.ts));
  const lastTouch = {}; // file → { id, session_id, kind }
  const result = {};
  for (const it of oldFirst) {
    const files = it.files_changed || [];
    if (it.kind === 'iteration') {
      const overwrites = [];
      const crossSessions = [];
      const priorByFile = {}; // for click-jump: file → prior iter id
      for (const f of files) {
        const prior = lastTouch[f];
        if (prior) {
          if (prior.kind === 'external_edit') {
            overwrites.push(f);
            priorByFile[f] = prior.id;
          } else if (prior.session_id && prior.session_id !== it.session_id) {
            crossSessions.push({ file: f, session: prior.session_id });
            priorByFile[f] = prior.id;
          }
        }
      }
      if (overwrites.length || crossSessions.length) {
        result[it.id] = { overwrites, crossSessions, priorByFile };
      }
    }
    // Update lastTouch for both iteration AND external_edit kinds — the next
    // iter touching this file should see whichever was most recent.
    for (const f of files) {
      lastTouch[f] = { id: it.id, session_id: it.session_id, kind: it.kind };
    }
  }
  return result;
}

// externalEditCard renders the synthetic row for an off-prompt file change.
// Expandable so clicking the card (or a click-jump from another card's
// badge) reveals the diff of what changed off-prompt.
function externalEditCard(it) {
  const files = it.files_changed || [];
  const head = files.length === 1
    ? `external edit on \`${files[0]}\``
    : `external edit on ${files.length} files`;
  const layers = [{
    label: 'show diff',
    html: `<div class="why-label">DIFFS · what changed off-prompt</div>` +
      `<div class="diff-files">` +
      files.map(f =>
        `<details class="file-diff" open><summary><code>${escapeHTML(f)}</code></summary>` +
        `<div class="diff" data-iter-id="${it.id}" data-file-path="${escapeHTML(f)}"><div class="diff-head">loading…</div></div>` +
        `</details>`
      ).join('') +
      `</div>`,
  }];
  return buildCard({
    statusClass: 'external-edit', cardID: 'ext-' + it.id, icon: '',
    head, subtitle: 'off-prompt change — not produced by an agent in this project',
    metaTsISO: it.ts, metaSuffix: '',
    initialDepth: 0, layers,
  });
}

function deltaCard(it) {
  const files = it.files_changed || [];
  const head = it.user_prompt && it.user_prompt.trim() ? it.user_prompt : (it.summary || '(no prompt captured)');
  const implRaw = (it.implementation || '').trim();
  const summaryRaw = (it.summary || '').trim();
  // L0 subtitle = the agent-submitted summary (via MCP). If only the
  // auto-generated placeholder ("no action" / "(no teach-back submitted)") is
  // present, suppress so the card stays clean.
  const isPlaceholder = /^(no action|\(no teach-back submitted\))$/.test(summaryRaw);
  // Subtitle priority: envelope summary (when meaningful) → first line of
  // response text (when only the heuristic is present, e.g. pure Q&A turns
  // where the agent didn't call set_implementation). Either way, the L0
  // card carries a one-line hint of what the turn was about.
  let subtitle = null;
  if (it.user_prompt) {
    if (summaryRaw && !isPlaceholder) {
      subtitle = summaryRaw;
    } else if (implRaw) {
      subtitle = deriveSubtitleFromImpl(implRaw);
    }
  }
  const fileHint = files.length > 0 ? ` · ${files.length} file${files.length === 1 ? '' : 's'}` : '';

  // Build the reveal layers in order. Pure-conversation turns get a RESPONSE
  // layer (renamed from IMPLEMENTATION since the wording would mislead). File-
  // touching turns get IMPLEMENTATION + touched + diffs.
  const layers = [];
  const hasFiles = files.length > 0;
  if (implRaw) {
    layers.push({
      label: hasFiles ? 'show implementation' : 'show response',
      html: `<div class="why-label">${hasFiles ? 'IMPLEMENTATION' : 'RESPONSE'}</div><div class="why-text">${renderMarkdownLite(implRaw)}</div>`,
    });
  }
  if (hasFiles) {
    // Single layer: the touched file list IS the diff affordance. Each path is
    // an expandable <details> that lazy-loads its diff on open (handled by the
    // global click handler). Previously TOUCHED (flat list) and DIFFS (same
    // list, expandable) were two separate layers — you saw the file names,
    // expanded again, then re-found the file. Merged so one expand on a known
    // path opens its diff directly.
    layers.push({
      label: 'show files touched',
      html: `<div class="why-label">TOUCHED · ${files.length} file${files.length === 1 ? '' : 's'} · expand any to diff vs previous prompt</div>` +
        `<div class="diff-files">` +
        files.map(f =>
          `<details class="file-diff"><summary><code>${escapeHTML(f)}</code></summary>` +
          `<div class="diff" data-iter-id="${it.id}" data-file-path="${escapeHTML(f)}"><div class="diff-head">expand to load diff</div></div>` +
          `</details>`
        ).join('') +
        `</div>`,
    });
  }

  // Session chip + concurrency badge — both opt-in:
  //   chip: only when multiple sessions exist in the feed
  //   badge: only when this iter overlaps another session on a file
  const badgesHTML = renderCardBadges(it);

  return buildCard({
    statusClass: 'delta', cardID: 'iter-' + it.id, icon: '',
    head, subtitle, badgesHTML, metaTsISO: it.ts, metaSuffix: fileHint,
    initialDepth: 0, layers,
  });
}

function renderCardBadges(it) {
  // Badges call out what THIS iter overwrote. Each badge is a clickable
  // affordance that opens THIS card's own diff layer pre-focused on the
  // affected file — showing prior-content-vs-current-content inline so the
  // user sees what was actually replaced without scrolling away.
  const parts = [];
  const conc = concurrencyIndex[it.id];
  if (!conc) return '';
  // "overwrites" carries the legacy-external_edit case (rare now that we no
  // longer auto-emit external_edit rows) PLUS uncommitted-manual-edit cases
  // attached separately when leading-card data is wired.
  for (const f of conc.overwrites) {
    parts.push(`<a class="conc-badge overwrite" data-jump-to="iter-${it.id}" data-jump-file="${escapeHTML(f)}" title="click to see what was overwritten">⚠ overwrites a manual edit on <code>${escapeHTML(f)}</code> · show diff</a>`);
  }
  // Cross-session: prior touch was a different agent SESSION. NOT an
  // "external edit" — call it accurately so the user knows the source.
  for (const {file} of conc.crossSessions) {
    parts.push(`<a class="conc-badge cross" data-jump-to="iter-${it.id}" data-jump-file="${escapeHTML(file)}" title="click to see what changed">⇄ overwrites another agent session's edit on <code>${escapeHTML(file)}</code> · show diff</a>`);
  }
  return parts.length ? `<div class="card-badges">${parts.join('')}</div>` : '';
}

// deriveSubtitleFromImpl: take the first meaningful line of the response text,
// strip markdown chrome, and cap length. Used as the L0 subtitle on Q&A turns
// where no MCP envelope summary was submitted.
function deriveSubtitleFromImpl(impl) {
  const lines = String(impl).trim().split('\n');
  // First non-empty line that isn't a pure formatting marker
  const first = lines.find(l => l.trim() && !/^[\-=*_]+$/.test(l.trim())) || '';
  // Strip leading markdown: heading markers, list bullets, blockquote, surrounding **/*
  const cleaned = first
    .replace(/^[>#\-*]+\s*/, '')
    .replace(/^\*\*([^*]+)\*\*$/, '$1')
    .replace(/\*+/g, '')
    .replace(/`/g, '')
    .trim();
  return cleaned.length > 110 ? cleaned.slice(0, 107) + '…' : cleaned;
}

// renderMarkdownLite: paragraphs, line breaks, backtick code, *em*. Cheap.
function renderMarkdownLite(s) {
  const paras = String(s).split(/\n{2,}/);
  return paras.map(p => '<p>' + renderEmphasis(p).replace(/\n/g, '<br>') + '</p>').join('');
}

// buildCard renders a card with N reveal layers. Each layer has a label
// (shown on the "show X" button when it's the next to reveal) and an html
// string. Layers are slotted into card-l2/l3/l4 in order; empty/missing
// layers don't render and don't get a button. For backwards compatibility
// with the older anchor/claim callers, L2/L3/L4 props are accepted and
// converted into a layers array with generic 'expand' labels.
function buildCard({statusClass, cardID, icon, head, subtitle, badgesHTML, metaText, metaTsISO, metaSuffix, L2, L3, L4, layers, initialDepth}) {
  if (!layers) {
    layers = [L2, L3, L4].filter(Boolean).map(html => ({label: 'expand', html}));
  }
  const card = document.createElement('article');
  card.className = 'card status-' + statusClass;
  card.dataset.depth = String(initialDepth || 0);
  card.dataset.cardId = cardID;
  card.dataset.maxDepth = String(layers.length);
  if (layers.length > 0) {
    card.dataset.layerLabels = JSON.stringify(layers.map(l => l.label));
  }

  const l1 = document.createElement('div');
  const hasIcon = icon && String(icon).trim() !== '';
  const noToggle = layers.length === 0;
  l1.className = 'card-l1' + (hasIcon ? '' : ' no-icon') + (noToggle ? ' no-toggle' : '');
  const subtitleHTML = subtitle
    ? `<div class="card-sub">${renderEmphasis(subtitle)}</div>`
    : '';
  const iconHTML = hasIcon ? `<span class="card-icon">${escapeHTML(icon)}</span>` : '';
  // Meta: when an ISO ts + optional suffix are provided, render with data-ts
  // attributes so tickTimestamps() can refresh the relTime portion in place.
  // Legacy callers still pass a static metaText.
  const metaHTML = metaTsISO
    ? `<div class="card-meta" data-ts="${escapeHTML(metaTsISO)}" data-ts-suffix="${escapeHTML(metaSuffix || '')}">${escapeHTML(relTime(metaTsISO) + (metaSuffix || ''))}</div>`
    : `<div class="card-meta">${escapeHTML(metaText || '')}</div>`;
  l1.innerHTML = `
    ${iconHTML}
    <div class="card-l1-mid">
      <div class="card-head">${renderEmphasis(head)}</div>
      ${subtitleHTML}
      ${badgesHTML || ''}
    </div>
    ${metaHTML}
  `;
  card.appendChild(l1);

  layers.forEach((layer, idx) => {
    const el = document.createElement('div');
    el.className = `card-detail card-l${idx + 2}`;
    el.innerHTML = layer.html;
    card.appendChild(el);
  });

  if (layers.length > 0) {
    const more = document.createElement('button');
    more.className = 'card-more';
    more.textContent = layers[0].label;
    card.appendChild(more);
  }
  return card;
}

// labelForCard returns the label that should appear on the "show more" button
// when the card is at the given depth (i.e. what's *next* to reveal).
function labelForCard(card, depth) {
  try {
    const labels = JSON.parse(card.dataset.layerLabels || '[]');
    return labels[depth] || '';
  } catch (_) {
    return '';
  }
}


// ===== RAIL timeline =====
function renderTimeline(iters) {
  const tl = document.getElementById('timeline');
  if (!tl) return;
  tl.innerHTML = '';
  // Prompt-only rail (no commits)
  const sorted = iters.filter(it => it.kind === 'iteration')
    .slice().sort((a, b) => new Date(b.ts) - new Date(a.ts));
  const railTitle = document.getElementById('railTitle');
  if (railTitle) railTitle.textContent = 'PROMPTS';
  sorted.forEach((it, i) => {
    const item = document.createElement('div');
    let klass = 'vtl-item';
    if (i === 0) klass += ' active';
    else if (i < 3) klass += ' recent';
    item.className = klass;
    item.dataset.kind = 'iteration';
    item.dataset.iterId = String(it.id);
    const railHead = it.user_prompt && it.user_prompt.trim() ? it.user_prompt : (it.summary || '?');
    const activeSuffix = i === 0 ? ' · active' : '';
    item.innerHTML = `<div class="vtl-text">${escapeHTML(railHead)}</div><div class="vtl-when" data-ts="${escapeHTML(it.ts)}" data-ts-suffix="${escapeHTML(activeSuffix)}">${escapeHTML(relTime(it.ts) + activeSuffix)}</div>`;
    tl.appendChild(item);
  });
}

// Lazy-load the prompt-based per-file diff: compares this iter's snapshot
// of the file against the most recent prior iter that touched it. Older
// iterations (pre-snapshotting) will return 404 — show a helpful message.
async function loadFileDiff(detailsEl) {
  const diffEl = detailsEl.querySelector('.diff[data-file-path]');
  if (!diffEl || diffEl.dataset.loaded === '1') return;
  diffEl.dataset.loaded = '1';
  const path = diffEl.dataset.filePath;
  const iterId = diffEl.dataset.iterId;
  diffEl.innerHTML = `<div class="diff-head">loading…</div>`;
  try {
    const r = await fetch(`prompt/${encodeURIComponent(iterId)}/diff?path=${encodeURIComponent(path)}`);
    const text = await r.text();
    if (r.status === 404) {
      diffEl.innerHTML = `<div class="diff-head">${escapeHTML(path)}</div><pre><span class="ctx">${escapeHTML(text)}</span></pre>`;
      return;
    }
    if (!r.ok) throw new Error('HTTP ' + r.status + ': ' + text);
    diffEl.innerHTML = '';
    const head = document.createElement('div');
    head.className = 'diff-head';
    head.textContent = path + ' · prompt #' + iterId + ' vs previous snapshot';
    diffEl.appendChild(head);
    diffEl.appendChild(formatDiffLines(text.split('\n')));
  } catch (e) {
    diffEl.innerHTML = `<div class="diff-head">error</div><pre><span class="del">${escapeHTML(String(e))}</span></pre>`;
    diffEl.dataset.loaded = '0';
  }
}

// Render an array of diff lines into a colored <pre> (used for /git/file-diff).
function formatDiffLines(lines) {
  const pre = document.createElement('pre');
  lines.forEach(line => {
    const span = document.createElement('span');
    span.textContent = line + '\n';
    if (line.startsWith('+++') || line.startsWith('---')) span.className = 'diff-file';
    else if (line.startsWith('@@')) span.className = 'diff-hunk';
    else if (line.startsWith('+')) span.className = 'add';
    else if (line.startsWith('-')) span.className = 'del';
    else if (line.startsWith('commit ') || line.startsWith('Author:') || line.startsWith('Date:') || line.startsWith('index ') || line.startsWith('new file') || line.startsWith('deleted file')) span.className = 'diff-meta';
    else span.className = 'ctx';
    pre.appendChild(span);
  });
  return pre;
}

function scrollToCard(cardID, expandToDepth = 0) {
  const card = document.querySelector(`.card[data-card-id="${cardID}"]`);
  if (!card) return false;
  if (expandToDepth > 0) {
    card.dataset.depth = String(expandToDepth);
    depthMemory[cardID] = String(expandToDepth);
  }
  card.scrollIntoView({behavior: 'smooth', block: 'center'});
  card.classList.add('flash');
  setTimeout(() => card.classList.remove('flash'), 1200);
  return true;
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
  // per-file <details> toggling triggers lazy-load of its diff
  const fileDiff = e.target.closest('details.file-diff > summary');
  if (fileDiff) {
    const det = fileDiff.parentElement;
    setTimeout(() => { if (det.open) loadFileDiff(det); }, 0);
    return;
  }


  // rail prompt items navigate to their main-column card
  const railItem = e.target.closest('.vtl-item');
  if (railItem?.dataset?.iterId) {
    e.stopPropagation();
    scrollToCard('iter-' + railItem.dataset.iterId, 0);
    return;
  }

  // Click on a conc-badge: jump to + expand the prior iter's card, and
  // open its diff details for the specific file at stake.
  const jump = e.target.closest('[data-jump-to]');
  if (jump) {
    e.preventDefault();
    e.stopPropagation();
    const target = jump.dataset.jumpTo;
    const file = jump.dataset.jumpFile;
    const targetCard = document.querySelector(`.card[data-card-id="${target}"]`);
    if (!targetCard) return;
    // Expand to max depth so the diff layer is visible
    const max = parseInt(targetCard.dataset.maxDepth || '0', 10);
    if (max > 0) {
      targetCard.dataset.depth = String(max);
      depthMemory[target] = String(max);
      const more = targetCard.querySelector('.card-more');
      if (more) more.textContent = labelForCard(targetCard, max);
    }
    // Auto-open the file's diff details
    if (file) {
      const det = targetCard.querySelector(`details.file-diff:has(div[data-file-path="${CSS.escape(file)}"])`);
      if (det) { det.open = true; loadFileDiff(det); }
    }
    targetCard.scrollIntoView({behavior: 'smooth', block: 'center'});
    targetCard.classList.add('flash');
    setTimeout(() => targetCard.classList.remove('flash'), 1200);
    return;
  }

  const card = e.target.closest('.card');
  if (!card) return;
  const id = card.dataset.cardId;
  const max = parseInt(card.dataset.maxDepth || '0', 10);
  const setDepth = (newDepth) => {
    card.dataset.depth = String(newDepth);
    if (id) depthMemory[id] = String(newDepth);
    const more = card.querySelector('.card-more');
    if (more) more.textContent = labelForCard(card, newDepth);
  };

  // card-more: increment, stop at max depth
  if (e.target.closest('.card-more')) {
    e.stopPropagation();
    const d = parseInt(card.dataset.depth || '0', 10);
    if (d < max) setDepth(d + 1);
    return;
  }
  // Click on an open child block toggles the NEXT child block.
  //   click card-l2 ↔ show/hide card-l3
  //   click card-l3 ↔ show/hide card-l4
  //   click card-l4 → step back to card-l3 visible
  // Don't trigger inside an interactive child (link, button, file-diff toggle).
  const l4 = e.target.closest('.card-l4');
  if (l4 && !e.target.closest('button, a, summary, details')) {
    e.stopPropagation();
    setDepth(2);
    return;
  }
  const l3 = e.target.closest('.card-l3');
  if (l3 && !e.target.closest('button, a, summary, details')) {
    e.stopPropagation();
    const d = parseInt(card.dataset.depth || '0', 10);
    setDepth(d >= 3 ? 2 : 3);
    return;
  }
  const l2 = e.target.closest('.card-l2');
  if (l2 && !e.target.closest('button, a, summary, details')) {
    e.stopPropagation();
    const d = parseInt(card.dataset.depth || '0', 10);
    setDepth(d >= 2 ? 1 : 2);
    return;
  }
  // card-l1 click: TOGGLE (open/close) at depth 0↔1. Skip when there's
  // nothing to reveal (no layers → no point pretending the card is clickable).
  if (e.target.closest('.card-l1') && !e.target.closest('button') && max > 0) {
    e.stopPropagation();
    const d = parseInt(card.dataset.depth || '0', 10);
    setDepth(d === 0 ? 1 : 0);
  }
});

// Live ticker for relative timestamps: rewrites the text of any element
// carrying data-ts="<iso>" (with optional data-ts-suffix). The state-poll
// load() runs every 2s but only re-renders on hash change, so cards left
// untouched would otherwise show stale "5s ago" forever.
function tickTimestamps() {
  document.querySelectorAll('[data-ts]').forEach(el => {
    const iso = el.dataset.ts;
    if (!iso) return;
    el.textContent = relTime(iso) + (el.dataset.tsSuffix || '');
  });
  if (currentState) setStatus('synced', syncStateText(currentState));
}

// Multi-project switcher: probe /projects.json at startup. Single-project mode
// returns 404 and the tabs stay hidden. Multi-project mode renders a row of
// clickable tabs under the header (one per project, the current one styled
// active). Click a tab → navigate to /p/<name>/.
async function setupProjectSwitcher() {
  try {
    const r = await fetch('/projects.json', { cache: 'no-store' });
    if (!r.ok) return;
    const projects = await r.json();
    if (!Array.isArray(projects) || projects.length < 2) return;  // 1 project = no tab strip clutter
    const el = document.getElementById('projSwitcher');
    if (!el) return;
    const m = location.pathname.match(/^\/p\/([^/]+)\//);
    const current = m ? m[1] : '';
    const tabs = document.createElement('nav');
    tabs.className = 'proj-tabs';
    tabs.setAttribute('aria-label', 'project tabs');
    for (const p of projects) {
      const tab = document.createElement('a');
      tab.className = 'proj-tab' + (p.name === current ? ' active' : '');
      tab.href = `/p/${encodeURIComponent(p.name)}/`;
      tab.textContent = p.name;
      tabs.appendChild(tab);
    }
    el.appendChild(tabs);
    el.hidden = false;
  } catch (_) { /* single-project mode; ignore */ }
}

setupProjectSwitcher();
load();
setInterval(load, 2000);
setInterval(tickTimestamps, 1000);
