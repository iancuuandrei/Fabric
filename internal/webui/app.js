(function () {
  'use strict';

  // Keep bearer token strictly in closure memory (never sessionStorage/localStorage).
  let authToken = '';

  const state = {
    episodes: [],
    runs: [],
    jobs: [],
    doctor: null,
    selected: null,
    tab: 'packet',
    autoRefresh: true,
    sseAbort: null,
    liveEvents: [],
  };

  const $ = (id) => document.getElementById(id);

  function el(tag, attrs, ...children) {
    const node = document.createElement(tag);
    if (attrs) {
      for (const [k, v] of Object.entries(attrs)) {
        if (v === undefined || v === null || v === false) continue;
        if (k === 'className') {
          node.className = v;
        } else if (k === 'dataset') {
          for (const [dk, dv] of Object.entries(v)) {
            if (dv !== undefined && dv !== null) node.dataset[dk] = String(dv);
          }
        } else {
          node.setAttribute(k, v === true ? '' : String(v));
        }
      }
    }
    for (const ch of children.flat()) {
      if (ch === undefined || ch === null || ch === false) continue;
      node.appendChild(typeof ch === 'object' ? ch : document.createTextNode(String(ch)));
    }
    return node;
  }

  function toast(msg) {
    const elToast = $('toast');
    elToast.textContent = msg;
    elToast.style.display = 'block';
    clearTimeout(elToast._t);
    elToast._t = setTimeout(() => { elToast.style.display = 'none'; }, 3400);
  }

  function shortId(id) { return (id || '').slice(0, 12); }

  async function api(path, opts = {}) {
    const headers = Object.assign({ 'Content-Type': 'application/json' }, opts.headers || {});
    if (authToken) headers['Authorization'] = 'Bearer ' + authToken;
    const res = await fetch(path, Object.assign({}, opts, { headers }));
    if (res.status === 401) {
      toast('Unauthorized — click "Auth Token" to enter Bearer token');
      throw new Error('unauthorized');
    }
    const ct = res.headers.get('content-type') || '';
    const data = ct.includes('application/json') ? await res.json() : await res.text();
    if (!res.ok) throw new Error((data && data.error) || String(data) || ('HTTP ' + res.status));
    return data;
  }

  async function refreshAll() {
    try {
      const [doc, eps, runs] = await Promise.allSettled([
        api('/v1/doctor'),
        api('/v1/episodes'),
        api('/v1/runs'),
      ]);
      if (doc.status === 'fulfilled') {
        state.doctor = doc.value;
        const ok = (doc.value.status || '').toLowerCase() === 'ok';
        $('doctorDot').className = 'dot ' + (ok ? 'ok' : 'warn');
        $('doctorText').textContent = 'Doctor: ' + (doc.value.status || 'unknown');
      } else {
        $('doctorDot').className = 'dot err';
        $('doctorText').textContent = 'Doctor: offline';
      }
      state.episodes = eps.status === 'fulfilled' && Array.isArray(eps.value) ? eps.value : [];
      state.runs = runs.status === 'fulfilled' && Array.isArray(runs.value) ? runs.value : [];
      renderMetrics();
      renderBoard();
      if (state.selected && state.tab !== 'events') {
        await renderDrawer();
      }
    } catch (e) {
      console.error(e);
    }
  }

  function renderMetrics() {
    const eps = state.episodes;
    const ready = eps.filter((e) => e.status === 'READY').length;
    const totalUSD = eps.reduce((a, e) => a + ((e.cost_estimate && e.cost_estimate.estimated_usd) || 0), 0);
    const totalTok = eps.reduce((a, e) => a + ((e.usage && (e.usage.input_tokens + e.usage.output_tokens)) || 0), 0);
    const saved = eps.reduce((a, e) => a + (e.masked_bytes_saved || 0), 0);
    $('mEpisodes').textContent = eps.length;
    $('mReadyRate').textContent = eps.length ? Math.round((ready / eps.length) * 100) + '% READY' : 'No episodes yet';
    $('mRuns').textContent = state.runs.length;
    $('mCost').textContent = '$' + totalUSD.toFixed(4);
    $('mTokens').textContent = totalTok.toLocaleString() + ' tokens';
    $('mSaved').textContent = (saved / 1024).toFixed(1) + ' KB';
  }

  function bucketFor(status) {
    const s = (status || '').toUpperCase();
    if (s === 'READY') return 'ready';
    if (s === 'RUNNING' || s === 'PLANNING' || s === 'WRITING' || s === 'ACTIVE') return 'active';
    if (s === 'FAILED' || s === 'BUDGET_EXCEEDED' || s === 'CANCELLED' || s === 'BLOCKED') return 'failed';
    return 'attention';
  }

  function chipClass(status) {
    const b = bucketFor(status);
    return b === 'ready' ? 'st-ready' : b === 'active' ? 'st-run' : b === 'failed' ? 'st-err' : 'st-warn';
  }

  function renderBoard() {
    const cols = { active: [], attention: [], ready: [], failed: [] };

    for (const job of state.jobs) {
      if (job.status === 'running') {
        cols.active.push({
          kind: 'episode', id: job.run_id || job.job_id, status: 'RUNNING',
          title: job.objective || 'Agent job running...', meta: ['live stream'], raw: job,
        });
      }
    }
    for (const ep of state.episodes) {
      const usd = ep.cost_estimate ? '$' + ep.cost_estimate.estimated_usd.toFixed(4) : '$0';
      const meta = [ep.model || 'model', `${ep.turns || 0}t`, `+${ep.diff_lines_added || 0}/-${ep.diff_lines_removed || 0}`, usd];
      if (ep.mutation && ep.mutation.total_mutants > 0) meta.push(`mut ${Math.round(ep.mutation.score * 100)}%`);
      cols[bucketFor(ep.status)].push({ kind: 'episode', id: ep.run_id, status: ep.status, title: ep.objective, meta, raw: ep });
    }
    for (const r of state.runs) {
      cols[bucketFor(r.state)].push({ kind: 'run', id: r.run_id, status: r.state, title: 'Classic Run ' + shortId(r.run_id), meta: [r.lifecycle || 'ACTIVE', 'plan ' + shortId(r.plan_id)], raw: r });
    }

    const map = [['active', 'colActive', 'cActive'], ['attention', 'colAttention', 'cAttention'], ['ready', 'colReady', 'cReady'], ['failed', 'colFailed', 'cFailed']];
    for (const [key, bodyId, cntId] of map) {
      const list = cols[key];
      $(cntId).textContent = list.length;
      const colBody = $(bodyId);
      if (!list.length) {
        colBody.replaceChildren(el('div', { className: 'empty' }, 'No items'));
        continue;
      }
      const cards = list.map((item) => {
        const selected = state.selected && state.selected.id === item.id ? ' selected' : '';
        return el('div', {
          className: 'card' + selected,
          dataset: { action: 'open-card', kind: item.kind, id: item.id },
        },
          el('div', { className: 'card-top' },
            el('span', { className: 'id-tag' }, shortId(item.id)),
            el('span', { className: 'kind-badge ' + (item.kind === 'run' ? 'kind-classic' : 'kind-agent') }, item.kind),
          ),
          el('div', { className: 'card-title' }, item.title),
          el('div', { className: 'card-meta' },
            el('span', { className: 'status-chip ' + chipClass(item.status) }, item.status),
            ...item.meta.map((m) => el('span', null, m)),
          ),
        );
      });
      colBody.replaceChildren(...cards);
    }
  }

  function stopSSE() {
    if (state.sseAbort) {
      state.sseAbort.abort();
      state.sseAbort = null;
    }
  }

  function openItem(kind, id) {
    stopSSE();
    state.selected = { kind, id };
    state.tab = 'packet';
    state.liveEvents = [];
    $('drawerBg').classList.add('open');
    document.querySelectorAll('.tab').forEach((t) => t.classList.toggle('active', t.dataset.tab === state.tab));
    void renderDrawer();
  }

  function renderDiffNode(diffText) {
    if (!diffText || !diffText.trim()) {
      return el('div', { className: 'empty' }, 'No uncommitted diff in worktree.');
    }
    const spans = diffText.split('\n').map((l) => {
      const cls = l.startsWith('+') && !l.startsWith('+++') ? 'd-add' : l.startsWith('-') && !l.startsWith('---') ? 'd-del' : l.startsWith('@@') ? 'd-hunk' : 'd-ctx';
      return el('span', { className: cls }, l);
    });
    return el('pre', { className: 'diff' }, ...spans);
  }

  async function startEpisodeSSE(id, showSteer) {
    stopSSE();
    state.liveEvents = [];
    const ctrl = new AbortController();
    state.sseAbort = ctrl;
    const headers = {};
    if (authToken) headers['Authorization'] = 'Bearer ' + authToken;
    try {
      const res = await fetch('/v1/episodes/' + encodeURIComponent(id) + '/events?follow=true', { headers, signal: ctrl.signal });
      if (!res.ok || !res.body) return;
      const reader = res.body.getReader();
      const dec = new TextDecoder();
      let buf = '';
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        buf += dec.decode(value, { stream: true });
        const parts = buf.split('\n\n');
        buf = parts.pop() || '';
        for (const frame of parts) {
          const dataLine = frame.split('\n').find((l) => l.startsWith('data:'));
          if (!dataLine) continue;
          try {
            const ev = JSON.parse(dataLine.slice(5).trim());
            state.liveEvents.push(ev);
          } catch { /* ignore malformed frame */ }
        }
        renderEventsList(showSteer, id);
      }
    } catch { /* aborted or closed stream */ }
  }

  function renderEventsList(showSteer, id) {
    const body = $('drawerBody');
    if (state.tab !== 'events') return;
    const existingInput = $('steerInput');
    const prevDraft = existingInput ? existingInput.value : '';
    const nodes = [];
    if (showSteer) {
      const inp = el('input', {
        id: 'steerInput',
        'aria-label': 'Live steering instruction',
        placeholder: 'Send live steering instruction to active agent turn...',
      });
      if (prevDraft) inp.value = prevDraft;
      nodes.push(el('div', { style: 'display:flex;gap:8px;margin-bottom:12px;' },
        inp,
        el('button', { type: 'button', className: 'primary', dataset: { action: 'steer', id } }, 'Steer'),
      ));
    }
    const evs = state.liveEvents;
    if (evs.length) {
      nodes.push(el('div', { className: 'timeline' }, ...evs.map((ev) =>
        el('div', { className: 'ev-row' },
          el('div', null,
            el('span', { className: 'ev-kind' }, `T${Number(ev.turn || 0)} ${ev.kind || ''}`),
            ' ',
            el('span', { style: 'color:#cbd5e1;' }, ev.detail || ''),
          ),
          el('span', { style: 'color:var(--muted);font-family:var(--mono);font-size:11px;' }, (ev.timestamp || '').slice(11, 19)),
        ),
      )));
    } else {
      nodes.push(el('div', { className: 'empty' }, 'Waiting for episode events...'));
    }
    body.replaceChildren(...nodes);
  }

  function makeKV(k, v) {
    return el('div', { className: 'kv' },
      el('div', { className: 'k' }, k),
      el('div', { className: 'v' }, v),
    );
  }

  function renderPacketNodes(ep) {
    const mut = ep.mutation ? `${Number(ep.mutation.killed)}/${Number(ep.mutation.total_mutants)} (${Math.round((ep.mutation.score || 0) * 100)}%)` : 'Not run';
    const nodes = [
      el('div', { className: 'kv-grid' },
        makeKV('Model', ep.model || ''),
        makeKV('Churn', `+${Number(ep.diff_lines_added || 0)} / -${Number(ep.diff_lines_removed || 0)}`),
        makeKV('Est. Cost', '$' + (((ep.cost_estimate && ep.cost_estimate.estimated_usd) || 0).toFixed(4))),
        makeKV('Mutation Kill', mut),
        makeKV('Turns / Tools', `${Number(ep.turns || 0)} / ${Number(ep.tool_calls || 0)}`),
        makeKV('Masking Saved', ((ep.masked_bytes_saved || 0) / 1024).toFixed(1) + ' KB'),
      ),
    ];
    if (ep.summary) {
      nodes.push(el('div', { style: 'margin-bottom:14px;' },
        el('strong', null, 'Summary'),
        el('p', { style: 'margin:6px 0;color:#cbd5e1;' }, ep.summary),
      ));
    }
    if (ep.reason) {
      nodes.push(el('div', { style: 'margin-bottom:14px;color:#fca5a5;' },
        el('strong', null, 'Reason: '),
        ep.reason,
      ));
    }
    if (ep.changed_files && ep.changed_files.length) {
      nodes.push(el('div', { style: 'margin-bottom:14px;' },
        el('strong', null, `Changed Files (${ep.changed_files.length})`),
        el('div', { style: 'margin-top:6px;display:flex;flex-wrap:wrap;gap:6px;' },
          ...ep.changed_files.map((f) => el('span', { className: 'pill' }, el('code', null, f))),
        ),
      ));
    }
    if (ep.verification && ep.verification.length) {
      nodes.push(el('strong', null, 'Verification Receipts'));
      nodes.push(el('table', null,
        el('thead', null, el('tr', null,
          el('th', null, 'Check'), el('th', null, 'Status'), el('th', null, 'Exit'), el('th', null, 'Duration'),
        )),
        el('tbody', null, ...ep.verification.map((v) =>
          el('tr', null,
            el('td', null, el('code', null, v.name || '')),
            el('td', null, el('span', { className: 'status-chip ' + (v.passed ? 'st-ready' : 'st-err') }, v.passed ? 'PASS' : 'FAIL')),
            el('td', null, String(Number(v.exit_code))),
            el('td', null, `${Number(v.duration_ms)}ms`),
          ),
        )),
      ));
    }
    if (ep.review_verdict) {
      nodes.push(el('div', null,
        el('strong', null, 'Independent Review: '),
        el('code', null, ep.review_verdict),
        el('p', { style: 'color:#cbd5e1;margin:4px 0;' }, ep.review_notes || ''),
      ));
    }
    return nodes;
  }

  async function renderDrawer() {
    if (!state.selected) return;
    const { kind, id } = state.selected;
    const body = $('drawerBody');
    const foot = $('drawerFoot');

    if (kind === 'episode') {
      const ep = state.episodes.find((e) => e.run_id === id);
      $('dId').textContent = id;
      $('dStatus').textContent = ep ? ep.status : 'RUNNING';
      $('dStatus').className = 'status-chip ' + chipClass(ep ? ep.status : 'RUNNING');
      $('dTitle').textContent = ep ? ep.objective : 'Live running episode...';

      if (state.tab === 'events') {
        const showSteer = !ep || ep.status === 'RUNNING';
        if (ep && Array.isArray(ep.events) && ep.events.length > 0) {
          state.liveEvents = ep.events;
          renderEventsList(showSteer, id);
        } else {
          renderEventsList(showSteer, id);
          void startEpisodeSSE(id, showSteer);
        }
      } else {
        stopSSE();
        if (!ep) {
          body.replaceChildren(el('div', { className: 'empty' }, 'Episode still in progress — switch to Live Events tab to watch turns stream in real time.'));
        } else if (state.tab === 'packet') {
          body.replaceChildren(...renderPacketNodes(ep));
        } else if (state.tab === 'diff') {
          body.replaceChildren(el('div', { className: 'empty' }, 'Loading worktree diff...'));
          try {
            const res = await api('/v1/episodes/' + encodeURIComponent(id) + '/diff');
            body.replaceChildren(renderDiffNode(res.diff || ''));
          } catch (e) {
            body.replaceChildren(el('div', { className: 'empty' }, e.message));
          }
        } else if (state.tab === 'checkpoints') {
          const snaps = ep.checkpoints || [];
          if (!snaps.length) {
            body.replaceChildren(el('div', { className: 'empty' }, 'No checkpoints recorded.'));
          } else {
            body.replaceChildren(el('div', { className: 'timeline' }, ...snaps.map((s) =>
              el('div', { className: 'ev-row' },
                el('div', null,
                  el('span', { className: 'ev-kind' }, `step #${Number(s.step)}`),
                  ` — ${s.label || ''} `,
                  el('span', { style: 'color:var(--muted);font-family:var(--mono);margin-left:6px;' }, shortId(s.commit)),
                ),
                el('button', {
                  type: 'button',
                  className: 'danger',
                  dataset: { action: 'rewind', id, step: s.step },
                }, `Rewind to #${Number(s.step)}`),
              ),
            )));
          }
        }
      }

      const canResume = ep && ep.status !== 'READY' && ep.status !== 'RUNNING';
      const actionGroup = el('div', { style: 'display:flex;gap:8px;flex-wrap:wrap;' },
        el('button', { type: 'button', dataset: { action: 'copy-packet', id } }, 'Copy Markdown Packet'),
        el('button', { type: 'button', dataset: { action: 'mutate', id } }, 'Run Mutation Audit'),
      );
      if (canResume) {
        actionGroup.appendChild(el('button', { type: 'button', dataset: { action: 'resume', id } }, 'Resume (+Budget)'));
      }
      foot.replaceChildren(
        actionGroup,
        el('button', { type: 'button', className: 'success', dataset: { action: 'apply', id } }, 'Apply Patch to Branch'),
      );
    } else {
      stopSSE();
      $('dId').textContent = id;
      const r = state.runs.find((x) => x.run_id === id) || {};
      $('dStatus').textContent = r.state || 'RUN';
      $('dStatus').className = 'status-chip ' + chipClass(r.state);
      $('dTitle').textContent = 'Classic Controller Run ' + shortId(id);
      const endpoint = state.tab === 'diff'
        ? '/v1/runs/' + encodeURIComponent(id) + '/diff'
        : '/v1/runs/' + encodeURIComponent(id);
      try {
        const res = await api(endpoint);
        body.replaceChildren(el('pre', { className: 'diff' }, JSON.stringify(res, null, 2)));
      } catch (e) {
        body.replaceChildren(el('div', { className: 'empty' }, e.message));
      }
      foot.replaceChildren(el('span', { style: 'color:var(--muted);font-size:12px;' }, 'Replay-validated classic run'));
    }
  }

  async function openGraphExplorer() {
    $('graphModal').classList.add('open');
    $('graphBody').replaceChildren(el('div', { className: 'empty' }, 'Building local Code Knowledge Graph...'));
    try {
      const g = await api('/v1/graph');
      const clusterPills = (g.clusters || []).slice(0, 12).map((c) =>
        el('span', { className: 'pill' }, el('code', null, c.name || ''), ` (${Number(c.file_count)}f / ${Number(c.node_count)}n)`),
      );
      const hubRows = (g.top_hubs || []).map((h) =>
        el('tr', null,
          el('td', null, el('code', null, h.name || '')),
          el('td', null, h.kind || ''),
          el('td', null, el('code', null, h.path || '')),
          el('td', null, String(Number(h.in_degree || 0))),
          el('td', null, String(Number(h.out_degree || 0))),
          el('td', null, Number(h.centrality || 0).toFixed(4)),
        ),
      );
      $('graphBody').replaceChildren(
        el('div', { className: 'kv-grid' },
          makeKV('Files Indexed', String(Number(g.files_count || 0))),
          makeKV('Graph Nodes', String((g.nodes || []).length)),
          makeKV('Typed Edges', String((g.edges || []).length)),
          makeKV('Clusters', String((g.clusters || []).length)),
        ),
        el('div', { style: 'margin-bottom:14px;' },
          el('strong', null, 'Top Clusters'),
          el('div', { style: 'margin-top:6px;display:flex;flex-wrap:wrap;gap:6px;' }, ...clusterPills),
        ),
        el('strong', null, 'Top Hub Nodes (PageRank Centrality)'),
        el('table', null,
          el('thead', null, el('tr', null,
            el('th', null, 'Node'), el('th', null, 'Kind'), el('th', null, 'Path'),
            el('th', null, 'In'), el('th', null, 'Out'), el('th', null, 'Score'),
          )),
          el('tbody', null, ...hubRows),
        ),
      );
    } catch (e) {
      $('graphBody').replaceChildren(el('div', { className: 'empty' }, e.message));
    }
  }

  function renderNodeList(list) {
    if (!list || !list.length) {
      return el('span', { style: 'color:var(--muted);font-size:12px;' }, 'None');
    }
    return list.map((n) =>
      el('span', { className: 'pill' },
        el('code', null, `${n.path || ''}${n.line ? ':' + Number(n.line) : ''}`),
        ` (${n.name || ''})`,
      ),
    );
  }

  async function traceImpact() {
    const q = $('impactQuery').value.trim();
    if (!q) {
      await openGraphExplorer();
      return;
    }
    $('graphBody').replaceChildren(el('div', { className: 'empty' }, 'Tracing multi-hop blast radius...'));
    try {
      const imp = await api('/v1/graph/impact?target=' + encodeURIComponent(q) + '&depth=2');
      $('graphBody').replaceChildren(
        el('div', { style: 'margin-bottom:12px;' },
          el('strong', null, `Matched (${(imp.matched_ids || []).length}): `),
          el('code', null, (imp.matched_ids || []).join(', ')),
        ),
        el('div', { style: 'margin-bottom:14px;' },
          el('strong', null, `Upstream Callers / Dependents (${(imp.callers || []).length})`),
          el('div', { style: 'margin-top:6px;display:flex;flex-wrap:wrap;gap:6px;' }, renderNodeList(imp.callers)),
        ),
        el('div', { style: 'margin-bottom:14px;' },
          el('strong', null, `Downstream Callees / Imports (${(imp.callees || []).length})`),
          el('div', { style: 'margin-top:6px;display:flex;flex-wrap:wrap;gap:6px;' }, renderNodeList(imp.callees)),
        ),
        el('div', null,
          el('strong', null, `Linked Documentation (${(imp.docs || []).length})`),
          el('div', { style: 'margin-top:6px;display:flex;flex-wrap:wrap;gap:6px;' }, renderNodeList(imp.docs)),
        ),
      );
    } catch (e) {
      $('graphBody').replaceChildren(el('div', { className: 'empty' }, e.message));
    }
  }

  // Delegated click handler — ZERO inline onclick attributes (strict CSP safe).
  $('app').addEventListener('click', (e) => {
    void (async () => {
      const target = e.target.closest('[data-action]');
      if (!target) return;
      const action = target.dataset.action;
      const id = target.dataset.id || '';
      switch (action) {
        case 'open-card':
          openItem(target.dataset.kind || 'episode', id);
          break;
        case 'copy-packet':
          try {
            const md = await api('/v1/episodes/' + encodeURIComponent(id) + '/packet?format=markdown');
            await navigator.clipboard.writeText(md);
            toast('Copied Markdown Review Packet to clipboard');
          } catch (err) { toast('Copy failed: ' + err.message); }
          break;
        case 'rewind': {
          const step = Number.parseInt(target.dataset.step || '', 10);
          if (!Number.isInteger(step)) return;
          if (!confirm(`Rewind episode ${shortId(id)} worktree to checkpoint #${step}?`)) return;
          try {
            await api('/v1/episodes/' + encodeURIComponent(id) + '/rewind', { method: 'POST', body: JSON.stringify({ step }) });
            toast('Worktree rewound to checkpoint #' + step);
            await refreshAll();
          } catch (err) { toast('Rewind failed: ' + err.message); }
          break;
        }
        case 'mutate':
          toast('Running diff-scoped mutation audit...');
          try {
            const rep = await api('/v1/episodes/' + encodeURIComponent(id) + '/mutate', { method: 'POST', body: '{}' });
            toast(`Mutation audit complete: ${rep.killed}/${rep.total_mutants} killed (${Math.round((rep.score || 0) * 100)}%)`);
            await refreshAll();
          } catch (err) { toast('Mutation audit failed: ' + err.message); }
          break;
        case 'apply': {
          const branch = prompt('Target new Git branch name (leave blank to apply to current branch):', 'fabric/apply-' + shortId(id));
          if (branch === null) return;
          try {
            await api('/v1/episodes/' + encodeURIComponent(id) + '/apply', { method: 'POST', body: JSON.stringify({ branch }) });
            toast('Patch applied cleanly' + (branch ? ' on branch ' + branch : ''));
          } catch (err) { toast('Apply refused: ' + err.message); }
          break;
        }
        case 'steer': {
          const inp = $('steerInput');
          const message = inp ? inp.value.trim() : '';
          if (!message) { toast('Enter a steering note first'); return; }
          try {
            await api('/v1/episodes/' + encodeURIComponent(id) + '/steer', { method: 'POST', body: JSON.stringify({ message }) });
            if (inp) inp.value = '';
            toast('Steering note queued for next agent turn');
          } catch (err) { toast('Steer failed: ' + err.message); }
          break;
        }
        case 'resume': {
          const raw = prompt('Additional max turns to grant (default 12):', '12');
          if (raw === null) return;
          const maxTurns = Number.parseInt(raw, 10) || 12;
          try {
            const job = await api('/v1/episodes/' + encodeURIComponent(id) + '/resume', { method: 'POST', body: JSON.stringify({ max_turns: maxTurns, max_usd: 3.0, async: true }) });
            state.jobs.push(Object.assign({ objective: 'Resuming ' + shortId(id) }, job));
            toast('Episode resumed (job ' + shortId(job.job_id) + ')');
            pollJob(job.job_id);
            await refreshAll();
          } catch (err) { toast('Resume failed: ' + err.message); }
          break;
        }
      }
    })();
  });

  document.querySelectorAll('.tab').forEach((btn) => {
    btn.addEventListener('click', () => {
      state.tab = btn.dataset.tab;
      document.querySelectorAll('.tab').forEach((t) => t.classList.toggle('active', t === btn));
      void renderDrawer();
    });
  });

  $('closeDrawer').addEventListener('click', () => { stopSSE(); $('drawerBg').classList.remove('open'); state.selected = null; });
  $('drawerBg').addEventListener('click', (e) => { if (e.target === $('drawerBg')) $('closeDrawer').click(); });
  $('refreshBtn').addEventListener('click', () => { void refreshAll(); });
  $('autoBtn').addEventListener('click', () => {
    state.autoRefresh = !state.autoRefresh;
    $('autoBtn').textContent = 'Auto-refresh: ' + (state.autoRefresh ? 'ON' : 'OFF');
  });
  $('tokenBtn').addEventListener('click', () => {
    const v = prompt('Enter Bearer token for Fabric API (kept in memory only; blank to clear):', authToken);
    if (v === null) return;
    authToken = v.trim();
    void refreshAll();
  });
  $('doctorPill').addEventListener('click', () => {
    if (!state.doctor) return;
    alert('Fabric Doctor:\n\n' + JSON.stringify(state.doctor, null, 2).slice(0, 1800));
  });
  $('graphBtn').addEventListener('click', () => { void openGraphExplorer(); });
  $('closeGraph').addEventListener('click', () => $('graphModal').classList.remove('open'));
  $('impactBtn').addEventListener('click', () => { void traceImpact(); });
  $('impactQuery').addEventListener('keydown', (e) => { if (e.key === 'Enter') void traceImpact(); });
  $('newRunBtn').addEventListener('click', () => $('newModal').classList.add('open'));
  $('cancelNew').addEventListener('click', () => $('newModal').classList.remove('open'));
  $('submitNew').addEventListener('click', () => {
    void (async () => {
      const objective = $('fObjective').value.trim();
      if (!objective) { toast('Objective is required'); return; }
      const payload = {
        objective,
        dialect: $('fDialect').value,
        model: $('fModel').value.trim(),
        max_usd: Number.parseFloat($('fMaxUSD').value) || 2.0,
        max_turns: Number.parseInt($('fMaxTurns').value, 10) || 24,
        allow_command: $('fAllowCmd').checked,
        mutation_audit: $('fMutate').checked,
        async: true,
      };
      try {
        const job = await api('/v1/episodes', { method: 'POST', body: JSON.stringify(payload) });
        job.objective = objective;
        state.jobs.push(job);
        $('newModal').classList.remove('open');
        $('fObjective').value = '';
        toast('Agent run launched (job ' + shortId(job.job_id) + ')');
        renderBoard();
        pollJob(job.job_id);
      } catch (e) { toast('Launch failed: ' + e.message); }
    })();
  });

  function pollJob(jobId) {
    const iv = setInterval(() => {
      void (async () => {
        try {
          const j = await api('/v1/jobs/' + encodeURIComponent(jobId));
          const idx = state.jobs.findIndex((x) => x.job_id === jobId);
          if (idx >= 0) state.jobs[idx] = Object.assign(state.jobs[idx], j);
          if (j.status !== 'running') {
            clearInterval(iv);
            toast('Agent job ' + shortId(jobId) + ' ' + j.status);
            await refreshAll();
          }
        } catch {
          clearInterval(iv);
        }
      })();
    }, 2000);
  }

  void refreshAll();
  setInterval(() => { if (state.autoRefresh) void refreshAll(); }, 5000);
})();
