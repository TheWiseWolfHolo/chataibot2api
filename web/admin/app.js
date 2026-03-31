const refreshBtn = document.getElementById('refreshBtn');
const logoutBtn = document.getElementById('logoutBtn');
const refreshState = document.getElementById('refreshState');
const topline = document.getElementById('topline');
const quotaOverviewGrid = document.getElementById('quotaOverviewGrid');
const quotaTableTools = document.getElementById('quotaTableTools');
const quotaTableWrap = document.getElementById('quotaTableWrap');
const probeState = document.getElementById('probeState');
const modelsSection = document.getElementById('modelsSection');
const migrationSection = document.getElementById('migrationSection');
const dangerSection = document.getElementById('dangerSection');
const logSection = document.getElementById('logSection');

const STATUS_LABELS = {
  'near-empty': '接近没额度',
  low: '低余额',
  healthy: '健康',
  'probe-error': '核验失败',
};

const STATUS_ORDER = {
  'near-empty': 0,
  low: 1,
  healthy: 2,
  'probe-error': 3,
};

const state = {
  session: null,
  meta: null,
  snapshot: null,
  poolStatus: null,
  catalog: null,
  migration: null,
  probeOverlay: new Map(),
  expandedJWTs: new Set(),
  filters: {
    status: 'all',
    bucket: 'all',
    query: '',
    sortKey: 'status',
    sortDirection: 'asc',
  },
  pagination: {
    page: 1,
    pageSize: 30,
  },
  probeLimit: 30,
  fillCount: 30,
  logs: [],
  refreshing: false,
  probing: false,
  probeAbortController: null,
  filling: false,
  stoppingFill: false,
  bannerError: '',
};

function escapeHtml(value) {
  return String(value ?? '')
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&#39;');
}

function formatCount(value) {
  return Number(value ?? 0).toLocaleString('zh-CN');
}

function formatDateTime(value) {
  if (!value) {
    return '—';
  }
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return escapeHtml(value);
  }
  return date.toLocaleString('zh-CN', { hour12: false });
}

function toPositiveInt(value, fallback = 1) {
  const numeric = Number(value);
  if (!Number.isFinite(numeric)) {
    return fallback;
  }
  const rounded = Math.floor(numeric);
  if (rounded < 1) {
    return fallback;
  }
  return rounded;
}

function pushLog(message, isError = false) {
  const stamp = new Date().toLocaleTimeString('zh-CN', { hour12: false });
  state.logs.unshift(`${stamp} · ${isError ? 'ERROR' : 'INFO'} · ${message}`);
  state.logs = state.logs.slice(0, 16);
  renderLogs();
}

function setRefreshState(label, tone = '') {
  refreshState.textContent = label;
  refreshState.className = 'badge';
  if (tone) {
    refreshState.classList.add(tone);
  }
}

async function fetchJSON(path, options = {}) {
  const response = await fetch(path, {
    credentials: 'same-origin',
    ...options,
    headers: {
      'Content-Type': 'application/json',
      ...(options.headers || {}),
    },
  });

  const text = await response.text();
  let payload = {};
  if (text) {
    try {
      payload = JSON.parse(text);
    } catch {
      payload = { raw: text };
    }
  }

  if (response.status === 401) {
    const err = new Error(payload?.error?.message || payload?.message || '会话失效，请重新登录');
    err.status = 401;
    throw err;
  }
  if (!response.ok) {
    throw new Error(payload?.error?.message || payload?.message || payload?.raw || `HTTP ${response.status}`);
  }
  return payload;
}

async function ensureSession() {
  try {
    return await fetchJSON('/v1/admin/session/me', { method: 'GET' });
  } catch (error) {
    if (error.status === 401) {
      window.location.replace('/admin/login');
      return null;
    }
    throw error;
  }
}

function metric(label, value, note = '') {
  return `
    <article class="metric">
      <span class="metric-label">${escapeHtml(label)}</span>
      <span class="metric-value">${escapeHtml(String(value))}</span>
      ${note ? `<span class="metric-note">${escapeHtml(note)}</span>` : ''}
    </article>
  `;
}

function pill(label, tone = '') {
  return `<span class="pill${tone ? ` ${tone}` : ''}">${escapeHtml(label)}</span>`;
}

function modelCapabilityPill(label, tone = '') {
  return `<span class="pill${tone ? ` ${tone}` : ''}">${escapeHtml(label)}</span>`;
}

function planTierPill(tier) {
  const key = String(tier || '').trim().toLowerCase();
  const map = {
    free: { label: 'Free', tone: 'good' },
    standard: { label: 'Standard', tone: '' },
    premium: { label: 'Premium', tone: 'warn' },
    batya: { label: 'Batya', tone: 'warn' },
    business: { label: 'Business', tone: 'danger' },
  };
  const meta = map[key] || { label: tier || '未知', tone: '' };
  return modelCapabilityPill(meta.label, meta.tone);
}

function minimumTierPill(item) {
  const tier = String(item?.minimum_tier || item?.access_tiers?.[0] || '').trim();
  if (!tier) {
    return '<span class="subtle">—</span>';
  }
  return planTierPill(tier);
}

function toneForStatus(status) {
  switch (status) {
    case 'near-empty':
      return 'danger';
    case 'low':
      return 'warn';
    case 'probe-error':
      return 'danger';
    default:
      return 'good';
  }
}

function toneForPerfLabel(label) {
  switch (label) {
    case '超时隔离':
      return 'danger';
    case '慢号':
      return 'warn';
    default:
      return '';
  }
}

function maskJWT(jwt) {
  if (!jwt || jwt.length <= 18) {
    return jwt || '—';
  }
  return `${jwt.slice(0, 8)}...${jwt.slice(-6)}`;
}

function toggleJwtVisibility(jwt) {
  if (state.expandedJWTs.has(jwt)) {
    state.expandedJWTs.delete(jwt);
  } else {
    state.expandedJWTs.add(jwt);
  }
  renderQuotaTable();
}
window.toggleJwtVisibility = toggleJwtVisibility;

function effectiveRow(row) {
  const overlay = state.probeOverlay.get(row.jwt);
  if (!overlay) {
    return { ...row, probeState: 'cached' };
  }
  if (!overlay.ok) {
    return {
      ...row,
      probeState: 'error',
      probeError: overlay.error,
      last_checked_at: overlay.checked_at,
      status: 'probe-error',
    };
  }
  return {
    ...row,
    quota: overlay.quota,
    status: overlay.status,
    last_checked_at: overlay.checked_at,
    probeState: 'live',
  };
}

function allRows() {
  return (state.snapshot?.rows || []).map((row) => effectiveRow(row));
}

function compareByDirection(left, right, direction = 'asc') {
  const normalizedDirection = direction === 'desc' ? 'desc' : 'asc';
  if (left === right) {
    return 0;
  }
  if (normalizedDirection === 'desc') {
    return left < right ? 1 : -1;
  }
  return left < right ? -1 : 1;
}

function compareDateValues(left, right, direction = 'asc') {
  const leftValue = left ? new Date(left).getTime() : 0;
  const rightValue = right ? new Date(right).getTime() : 0;
  return compareByDirection(leftValue, rightValue, direction);
}

function compareTextValues(left, right, direction = 'asc') {
  const normalizedLeft = String(left || '');
  const normalizedRight = String(right || '');
  if (normalizedLeft === normalizedRight) {
    return 0;
  }
  if (direction === 'desc') {
    return normalizedRight.localeCompare(normalizedLeft);
  }
  return normalizedLeft.localeCompare(normalizedRight);
}

function sortArrow(key) {
  if (state.filters.sortKey !== key) {
    return '↕';
  }
  return state.filters.sortDirection === 'desc' ? '↓' : '↑';
}

function toggleSort(key) {
  if (state.filters.sortKey === key) {
    state.filters.sortDirection = state.filters.sortDirection === 'desc' ? 'asc' : 'desc';
  } else {
    state.filters.sortKey = key;
    state.filters.sortDirection = key === 'quota' || key === 'updated' ? 'desc' : 'asc';
  }
  state.pagination.page = 1;
  renderQuotaTable();
}
window.toggleSort = toggleSort;

function getFilteredRows() {
  const query = state.filters.query.trim().toLowerCase();
  const rows = allRows().filter((row) => {
    if (state.filters.status !== 'all' && row.status !== state.filters.status) {
      return false;
    }
    if (state.filters.bucket !== 'all' && row.pool_bucket !== state.filters.bucket) {
      return false;
    }
    if (query && !String(row.jwt || '').toLowerCase().includes(query)) {
      return false;
    }
    return true;
  });

  const sorted = [...rows];
  switch (state.filters.sortKey) {
    case 'quota':
      sorted.sort((a, b) =>
        compareByDirection(a.quota, b.quota, state.filters.sortDirection) ||
        compareDateValues(a.last_checked_at, b.last_checked_at, 'desc') ||
        compareTextValues(a.jwt, b.jwt, 'asc')
      );
      break;
    case 'updated':
      sorted.sort((a, b) => {
        return compareDateValues(a.last_checked_at, b.last_checked_at, state.filters.sortDirection) ||
          compareByDirection(a.quota, b.quota, 'desc') ||
          compareTextValues(a.jwt, b.jwt, 'asc');
      });
      break;
    default:
      sorted.sort((a, b) => {
        const left = STATUS_ORDER[a.status] ?? 99;
        const right = STATUS_ORDER[b.status] ?? 99;
        return compareByDirection(left, right, state.filters.sortDirection) ||
          compareByDirection(a.quota, b.quota, state.filters.sortDirection) ||
          compareTextValues(a.jwt, b.jwt, 'asc');
      });
      break;
  }

  return sorted;
}

function getPaginationState(rows) {
  const totalPages = Math.max(1, Math.ceil(rows.length / state.pagination.pageSize));
  const page = Math.min(Math.max(state.pagination.page, 1), totalPages);
  const startIndex = (page - 1) * state.pagination.pageSize;
  const pagedRows = rows.slice(startIndex, startIndex + state.pagination.pageSize);
  return {
    totalPages,
    page,
    startIndex,
    pagedRows,
  };
}

function renderQuotaOverview() {
  const summary = state.snapshot?.summary || {
    total_count: 0,
    total_quota: 0,
    low_quota_count: 0,
    near_empty_count: 0,
  };

  quotaOverviewGrid.innerHTML = [
    metric('总号数', formatCount(summary.total_count)),
    metric('总剩余额度', formatCount(summary.total_quota)),
    metric('低余额号', formatCount(summary.low_quota_count), '2 <= quota < 10'),
    metric('接近没额度号', formatCount(summary.near_empty_count), 'quota < 5'),
  ].join('');
}

function fillTaskStatusLabel(status) {
  switch (status) {
    case 'running':
      return '补号中';
    case 'stopping':
      return '停止中';
    case 'stopped':
      return '已停止';
    case 'completed':
      return '已完成';
    default:
      return status || '未知状态';
  }
}

function currentFillTask() {
  const tasks = Array.isArray(state.poolStatus?.tasks) ? state.poolStatus.tasks : [];
  return tasks.find((task) => task?.status === 'running' || task?.status === 'stopping') || null;
}

function renderQuotaTableTools() {
  const rows = getFilteredRows();
  const { page, totalPages, pagedRows } = getPaginationState(rows);
  const pageProbeDisabled = state.probing || pagedRows.length === 0 ? 'disabled' : '';
  const customProbeDisabled = state.probing || rows.length === 0 ? 'disabled' : '';
  const stopProbeDisabled = state.probing ? '' : 'disabled';
  const activeFillTask = currentFillTask();
  const hasActiveFillTask = Boolean(activeFillTask);
  const fillDisabled = state.filling ? 'disabled' : '';
  const stopFillDisabled = state.stoppingFill || !hasActiveFillTask ? 'disabled' : '';
  const fillCount = toPositiveInt(state.fillCount, 30);
  const probeLimit = Math.min(toPositiveInt(state.probeLimit, state.pagination.pageSize), Math.max(rows.length, 1));
  const fillTaskPill = activeFillTask
    ? pill(`任务 ${escapeHtml(activeFillTask.id)} · ${fillTaskStatusLabel(activeFillTask.status)} · ${formatCount(activeFillTask.completed || 0)}/${formatCount(activeFillTask.requested || 0)}`, activeFillTask.status === 'stopping' ? 'warn' : '')
    : '';
  quotaTableTools.innerHTML = `
    <div class="action-row quota-toolbar" style="margin-bottom: 16px; flex-wrap: wrap; align-items: end; gap: 12px;">
      <label class="field" style="min-width: 150px;">
        <span>状态</span>
        <select id="statusFilter">
          <option value="all">全部</option>
          <option value="near-empty">接近没额度</option>
          <option value="low">低余额</option>
          <option value="healthy">健康</option>
          <option value="probe-error">核验失败</option>
        </select>
      </label>
      <label class="field" style="min-width: 150px;">
        <span>池位</span>
        <select id="bucketFilter">
          <option value="all">全部</option>
          <option value="ready">ready</option>
          <option value="reusable">reusable</option>
          <option value="borrowed">borrowed</option>
          <option value="persisted">persisted</option>
        </select>
      </label>
      <label class="field" style="min-width: 240px; flex: 1 1 240px;">
        <span>搜索 JWT</span>
        <input id="queryInput" type="search" placeholder="输入 token 片段" value="${escapeHtml(state.filters.query)}" />
      </label>
      <label class="field" style="min-width: 140px;">
        <span>每页</span>
        <select id="pageSizeSelect">
          <option value="30">30</option>
          <option value="50">50</option>
          <option value="100">100</option>
          <option value="200">200</option>
        </select>
      </label>
      <label class="field" style="min-width: 140px;">
        <span>补号数量</span>
        <input id="fillCountInput" type="number" min="1" step="1" value="${escapeHtml(String(fillCount))}" />
      </label>
      <button type="button" class="button secondary" id="fillBtn" ${fillDisabled}>${state.filling ? '补号中' : '开始补号'}</button>
      <button type="button" class="button danger" id="stopFillBtn" ${stopFillDisabled}>${state.stoppingFill ? '停止中' : '停止补号'}</button>
      <button type="button" class="button primary" id="probePageBtn" ${pageProbeDisabled}>${state.probing ? '核验中' : `核验当前页 (${pagedRows.length})`}</button>
      <label class="field" style="min-width: 140px;">
        <span>核验前 N 条</span>
        <input id="probeLimitInput" type="number" min="1" step="1" max="${Math.max(rows.length, 1)}" value="${escapeHtml(String(probeLimit))}" />
      </label>
      <button type="button" class="button secondary" id="probeLimitBtn" ${customProbeDisabled}>${state.probing ? '核验中' : `核验前 ${probeLimit} 条`}</button>
      <button type="button" class="button danger" id="stopProbeBtn" ${stopProbeDisabled}>${state.probing ? '停止核验' : '停止核验'}</button>
    </div>
    <div class="strip">
      ${pill(`当前筛选 ${rows.length} 条`)}
      ${pill(`当前页 ${page} / ${totalPages}`)}
      ${pill(`排序 ${state.filters.sortKey} ${state.filters.sortDirection === 'desc' ? '↓' : '↑'}`)}
      ${fillTaskPill}
    </div>
  `;

  document.getElementById('statusFilter').value = state.filters.status;
  document.getElementById('bucketFilter').value = state.filters.bucket;
  document.getElementById('pageSizeSelect').value = String(state.pagination.pageSize);

  document.getElementById('statusFilter')?.addEventListener('change', (event) => {
    state.filters.status = event.target.value;
    state.pagination.page = 1;
    renderQuotaTable();
  });
  document.getElementById('bucketFilter')?.addEventListener('change', (event) => {
    state.filters.bucket = event.target.value;
    state.pagination.page = 1;
    renderQuotaTable();
  });
  document.getElementById('queryInput')?.addEventListener('input', (event) => {
    state.filters.query = event.target.value;
    state.pagination.page = 1;
    renderQuotaTable();
  });
  document.getElementById('pageSizeSelect')?.addEventListener('change', (event) => {
    state.pagination.pageSize = Number(event.target.value || 30);
    state.pagination.page = 1;
    renderQuotaTable();
  });
  document.getElementById('fillCountInput')?.addEventListener('input', (event) => {
    state.fillCount = toPositiveInt(event.target.value, state.fillCount || 30);
  });
  document.getElementById('probeLimitInput')?.addEventListener('input', (event) => {
    state.probeLimit = toPositiveInt(event.target.value, state.probeLimit || state.pagination.pageSize);
  });
  document.getElementById('fillBtn')?.addEventListener('click', runFill);
  document.getElementById('stopFillBtn')?.addEventListener('click', runStopFill);
  document.getElementById('probePageBtn')?.addEventListener('click', runProbeCurrentPage);
  document.getElementById('probeLimitBtn')?.addEventListener('click', runProbeCustomLimit);
  document.getElementById('stopProbeBtn')?.addEventListener('click', runStopProbe);
}

function renderQuotaTable() {
  renderQuotaTableTools();
  const rows = getFilteredRows();
  const { totalPages, page, startIndex, pagedRows } = getPaginationState(rows);
  state.pagination.page = page;
  probeState.textContent = state.probing
    ? '正在执行额度核验'
    : `当前筛选 ${rows.length} 条；第 ${state.pagination.page} / ${totalPages} 页；总览仍基于缓存快照`;

  if (!rows.length) {
    quotaTableWrap.innerHTML = '<div class="data-item"><strong>结果</strong><span>当前筛选没有匹配账号</span></div>';
    return;
  }

  quotaTableWrap.innerHTML = `
    <div class="table-scroll">
      <table class="model-table quota-table">
        <thead>
          <tr>
            <th><button type="button" class="button ghost" id="sortQuotaBtn">额度 ${sortArrow('quota')}</button></th>
            <th><button type="button" class="button ghost" id="sortStatusBtn">状态 ${sortArrow('status')}</button></th>
            <th>JWT</th>
            <th>池位</th>
            <th><button type="button" class="button ghost" id="sortUpdatedBtn">最近更新时间 ${sortArrow('updated')}</button></th>
            <th>操作</th>
          </tr>
        </thead>
        <tbody>
          ${pagedRows.map((row) => {
            const expanded = state.expandedJWTs.has(row.jwt);
            const statusTone = toneForStatus(row.status);
            const statusLabel = STATUS_LABELS[row.status] || row.status || '未知';
            const jwtDisplay = expanded ? row.jwt : maskJWT(row.jwt);
            const notes = [];
            notes.push(
              row.probeState === 'live'
                ? '<small>实时</small>'
                : row.probeState === 'error'
                  ? `<small>${escapeHtml(row.probeError || '核验失败')}</small>`
                  : '<small>缓存</small>'
            );
            if (row.last_latency_ms) {
              notes.push(`<small>文本 ${escapeHtml(String(row.last_latency_ms))}ms</small>`);
            }
            if (row.disabled_until) {
              notes.push(`<small>隔离至 ${escapeHtml(formatDateTime(row.disabled_until))}</small>`);
            }
            const perfPill = row.perf_label
              ? pill(row.perf_label, toneForPerfLabel(row.perf_label))
              : '';
            return `
              <tr>
                <td><strong>${escapeHtml(String(row.quota))}</strong></td>
                <td><div class="model-capabilities">${pill(statusLabel, statusTone)} ${perfPill} ${notes.join(' ')}</div></td>
                <td>
                  <button type="button" class="button ghost quota-jwt-button" style="min-height: 44px; letter-spacing: 0.06em; text-transform: none; font-size: 0.84rem;" onclick="toggleJwtVisibility('${escapeHtml(row.jwt)}')">${escapeHtml(jwtDisplay)}</button>
                </td>
                <td>${escapeHtml(row.pool_bucket || 'unknown')}</td>
                <td>${formatDateTime(row.last_checked_at)}</td>
                <td><button type="button" class="button ghost" ${state.probing ? 'disabled' : ''} onclick="runProbeSingle('${escapeHtml(row.jwt)}')">单个核验</button></td>
              </tr>
            `;
          }).join('')}
        </tbody>
      </table>
    </div>
    <div class="action-row quota-pagination" style="margin-top: 16px; justify-content: space-between; flex-wrap: wrap; gap: 12px;">
      <div class="strip">
        ${pill(`当前显示 ${startIndex + 1}-${Math.min(startIndex + pagedRows.length, rows.length)}`)}
        ${pill(`总计 ${rows.length} 条`)}
      </div>
      <div class="action-row" style="gap: 10px;">
        <button type="button" class="button ghost" id="prevPageBtn" ${state.pagination.page <= 1 ? 'disabled' : ''}>上一页</button>
        <label class="field" style="min-width: 120px;">
          <span>跳到第几页</span>
          <input id="pageJumpInput" type="number" min="1" step="1" max="${totalPages}" value="${page}" />
        </label>
        <button type="button" class="button ghost" id="jumpPageBtn">跳转</button>
        <button type="button" class="button ghost" id="nextPageBtn" ${state.pagination.page >= totalPages ? 'disabled' : ''}>下一页</button>
      </div>
    </div>
  `;

  document.getElementById('sortQuotaBtn')?.addEventListener('click', () => toggleSort('quota'));
  document.getElementById('sortStatusBtn')?.addEventListener('click', () => toggleSort('status'));
  document.getElementById('sortUpdatedBtn')?.addEventListener('click', () => toggleSort('updated'));
  document.getElementById('prevPageBtn')?.addEventListener('click', () => {
    if (state.pagination.page <= 1) {
      return;
    }
    state.pagination.page -= 1;
    renderQuotaTable();
  });
  document.getElementById('nextPageBtn')?.addEventListener('click', () => {
    if (state.pagination.page >= totalPages) {
      return;
    }
    state.pagination.page += 1;
    renderQuotaTable();
  });
  const jumpToPage = () => {
    const input = document.getElementById('pageJumpInput');
    const targetPage = Math.min(Math.max(toPositiveInt(input?.value, page), 1), totalPages);
    state.pagination.page = targetPage;
    renderQuotaTable();
  };
  document.getElementById('jumpPageBtn')?.addEventListener('click', jumpToPage);
  document.getElementById('pageJumpInput')?.addEventListener('keydown', (event) => {
    if (event.key === 'Enter') {
      event.preventDefault();
      jumpToPage();
    }
  });
}

function applyProbePayload(payload) {
  for (const item of payload.results || []) {
    state.probeOverlay.set(item.jwt, {
      ...item,
      checked_at: payload.checked_at,
    });
  }
}

async function runProbeForJWTs(jwts, successLabel) {
  if (!jwts.length || state.probing) {
    return;
  }

  state.probing = true;
  state.probeAbortController = new AbortController();
  renderQuotaTable();
  try {
    const payload = await fetchJSON('/v1/admin/quota/probe', {
      method: 'POST',
      body: JSON.stringify({ jwts }),
      signal: state.probeAbortController.signal,
    });
    applyProbePayload(payload);
    pushLog(`${successLabel}：${jwts.length} 条`);
  } catch (error) {
    if (error?.name === 'AbortError') {
      pushLog('额度核验已停止');
      return;
    }
    if (error.status === 401) {
      window.location.replace('/admin/login');
      return;
    }
    pushLog(`额度核验失败：${error.message}`, true);
  } finally {
    state.probing = false;
    state.probeAbortController = null;
    renderQuotaTable();
  }
}

async function runProbeCurrentPage() {
  const rows = getFilteredRows();
  const { pagedRows } = getPaginationState(rows);
  await runProbeForJWTs(pagedRows.map((row) => row.jwt), '当前页核验完成');
}

async function runProbeCustomLimit() {
  const rows = getFilteredRows();
  const limit = Math.min(toPositiveInt(state.probeLimit, state.pagination.pageSize), rows.length);
  await runProbeForJWTs(rows.slice(0, limit).map((row) => row.jwt), `前 ${limit} 条核验完成`);
}

async function runProbeSingle(jwt) {
  await runProbeForJWTs([jwt], '单个核验完成');
}
window.runProbeSingle = runProbeSingle;

function runStopProbe() {
  if (!state.probing || !state.probeAbortController) {
    return;
  }
  state.probeAbortController.abort();
}

async function runFill() {
  if (state.filling) {
    return;
  }

  const count = toPositiveInt(state.fillCount, 30);
  state.filling = true;
  renderQuotaTableTools();
  try {
    const payload = await fetchJSON('/v1/admin/pool/fill', {
      method: 'POST',
      body: JSON.stringify({ count }),
    });
    pushLog(`补号任务已启动：requested=${payload.requested || count} task=${payload.task_id || 'unknown'}`);
    await refreshAll();
  } catch (error) {
    if (error.status === 401) {
      window.location.replace('/admin/login');
      return;
    }
    pushLog(`启动补号失败：${error.message}`, true);
  } finally {
    state.filling = false;
    renderQuotaTableTools();
  }
}

async function runStopFill() {
  const task = currentFillTask();
  if (!task || state.stoppingFill) {
    return;
  }

  state.stoppingFill = true;
  renderQuotaTableTools();
  try {
    const payload = await fetchJSON('/v1/admin/pool/fill/stop', {
      method: 'POST',
      body: JSON.stringify({ task_id: task.id }),
    });
    pushLog(`补号任务已请求停止：task=${payload.task_id || task.id} status=${payload.status || 'stopped'}`);
    await refreshAll();
  } catch (error) {
    if (error.status === 401) {
      window.location.replace('/admin/login');
      return;
    }
    pushLog(`停止补号失败：${error.message}`, true);
  } finally {
    state.stoppingFill = false;
    renderQuotaTableTools();
  }
}

function modelTableRows(models, type) {
  if (!models?.length) {
    return `<tr><td colspan="5" class="subtle">暂无${type === 'text' ? '文本' : '图片'}模型</td></tr>`;
  }

  return models.map((item) => {
    const capabilities = [];
    let costDetail = `生图 ${escapeHtml(String(item.cost))}`;

    if (type === 'text') {
      capabilities.push(item.internet ? modelCapabilityPill('联网', 'good') : modelCapabilityPill('标准'));
      if (item.runtime_note) {
        capabilities.push(modelCapabilityPill(item.runtime_note, item.runtime_note.includes('默认') ? 'good' : ''));
      }
    } else {
      if (item.edit_access === 'subscription-gated') {
        capabilities.push(modelCapabilityPill('生图', 'good'));
        capabilities.push(modelCapabilityPill('改图需会员', 'warn'));
      } else if (item.edit_access === 'cost-higher-than-generate') {
        capabilities.push(modelCapabilityPill('改图更贵', 'warn'));
        capabilities.push(modelCapabilityPill('仍可改图', 'good'));
      } else {
        capabilities.push(item.supports_edit ? modelCapabilityPill('图生图', 'good') : modelCapabilityPill('仅生图'));
      }
      if (item.supports_merge) {
        capabilities.push(modelCapabilityPill('拼图', 'warn'));
      }
      if (item.runtime_note) {
        const noteTone = item.runtime_note.includes('可用')
          ? 'good'
          : item.runtime_note.includes('会员')
            ? 'warn'
            : '';
        capabilities.push(modelCapabilityPill(item.runtime_note, noteTone));
      }
      if (item.route_advice) {
        capabilities.push(modelCapabilityPill(item.route_advice, ''));
      }
      const detailParts = [`生图 ${escapeHtml(String(item.cost))}`];
      if (item.edit_cost) {
        detailParts.push(`改图 ${escapeHtml(String(item.edit_cost))}`);
      }
      if (item.merge_cost_note) {
        detailParts.push(`拼图 ${escapeHtml(item.merge_cost_note)}`);
      }
      costDetail = detailParts.join(' · ');
    }

    return `
      <tr>
        <td class="model-name">${escapeHtml(item.id)}</td>
        <td>${type === 'text' ? '文本' : '图片'}</td>
        <td><div class="model-capabilities">${minimumTierPill(item)}</div></td>
        <td><div class="model-capabilities">${capabilities.join('')}</div></td>
        <td>${costDetail}</td>
      </tr>
    `;
  }).join('');
}

function renderModels() {
  const catalog = state.catalog || { text_models: [], image_models: [] };
  modelsSection.innerHTML = `
    <div class="models-stack">
      <section class="model-group">
        <div class="model-group-head">
          <h4>文本模型</h4>
          <span class="pill">${formatCount(catalog.text_models.length)} 个</span>
        </div>
        <div class="table-scroll">
          <table class="model-table">
            <thead>
              <tr><th>模型</th><th>类型</th><th>最低层级</th><th>能力</th><th>价格</th></tr>
            </thead>
            <tbody>${modelTableRows(catalog.text_models, 'text')}</tbody>
          </table>
        </div>
      </section>
      <section class="model-group">
        <div class="model-group-head">
          <h4>图片模型</h4>
          <span class="pill">${formatCount(catalog.image_models.length)} 个</span>
        </div>
        <div class="table-scroll">
          <table class="model-table">
            <thead>
              <tr><th>模型</th><th>类型</th><th>最低层级</th><th>能力</th><th>价格</th></tr>
            </thead>
            <tbody>${modelTableRows(catalog.image_models, 'image')}</tbody>
          </table>
        </div>
      </section>
    </div>
  `;
}

function renderMigration() {
  const migration = state.migration || {};
  const meta = state.meta || {};
  migrationSection.innerHTML = `
    <div class="data-grid">
      <div class="data-item"><strong>旧池来源</strong><span class="value-compact">${escapeHtml(meta.primary_public_base_url || '未配置')}</span></div>
      <div class="data-item"><strong>请求导入</strong><span>${formatCount(migration.requested || 0)}</span></div>
      <div class="data-item"><strong>成功导入</strong><span>${formatCount(migration.imported || 0)}</span></div>
      <div class="data-item"><strong>重复跳过</strong><span>${formatCount(migration.duplicates || 0)}</span></div>
      <div class="data-item"><strong>拒绝导入</strong><span>${formatCount(migration.rejected || 0)}</span></div>
      <div class="data-item"><strong>导入后总量</strong><span>${formatCount(migration.total_count || 0)}</span></div>
      <div class="data-item"><strong>开始时间</strong><span>${formatDateTime(migration.started_at)}</span></div>
      <div class="data-item"><strong>结束时间</strong><span>${formatDateTime(migration.finished_at)}</span></div>
    </div>
    <div class="strip">
      ${migration.last_error ? pill(`迁移报错：${migration.last_error}`, 'danger') : pill('最近迁移没有报错', 'good')}
      <button type="button" class="button secondary" id="migrateBtn">导入旧池</button>
    </div>
  `;
  document.getElementById('migrateBtn')?.addEventListener('click', runMigration);
}

function renderDanger() {
  dangerSection.innerHTML = `
    <div class="action-card">
      <h3>旧实例退役</h3>
      <p>只在主域名切换、路由验证和号池迁移都完成后再点。</p>
      <div class="action-row">
        <button type="button" class="button danger" id="retireBtn">尝试退役旧实例</button>
      </div>
    </div>
  `;
  document.getElementById('retireBtn')?.addEventListener('click', runRetire);
}

function renderLogs() {
  const items = state.logs.length
    ? state.logs.map((entry) => `<li class="log-item">${escapeHtml(entry)}</li>`).join('')
    : '<li class="log-item">还没有操作记录。</li>';
  logSection.innerHTML = `<ul class="log-list">${items}</ul>`;
}

function renderSnapshot() {
  if (!state.snapshot) {
    quotaOverviewGrid.innerHTML = '<p class="subtle">正在读取后台状态。</p>';
    quotaTableWrap.innerHTML = '<p class="subtle">正在读取后台状态。</p>';
    renderLogs();
    return;
  }

  renderQuotaOverview();
  renderQuotaTable();
  renderModels();
  renderMigration();
  renderDanger();
  renderLogs();
}

async function refreshAll() {
  if (state.refreshing) {
    return;
  }
  state.refreshing = true;
  refreshBtn.disabled = true;
  setRefreshState('刷新中', 'warn');

  try {
    const session = await ensureSession();
    if (!session) {
      return;
    }

    const [meta, snapshot, poolStatus, catalog, migration] = await Promise.all([
      fetchJSON('/v1/admin/meta', { method: 'GET' }),
      fetchJSON('/v1/admin/quota/snapshot', { method: 'GET' }),
      fetchJSON('/v1/admin/pool', { method: 'GET' }),
      fetchJSON('/v1/admin/catalog', { method: 'GET' }),
      fetchJSON('/v1/admin/migration/status', { method: 'GET' }),
    ]);

    state.bannerError = '';
    state.session = session;
    state.meta = meta;
    state.snapshot = snapshot;
    state.poolStatus = poolStatus;
    state.catalog = catalog;
    state.migration = migration;
    state.probeOverlay.clear();

    topline.textContent = '首屏只看额度总览和号池明细。';
    setRefreshState(`最后刷新 ${formatDateTime(new Date().toISOString())}`, 'good');
    pushLog('额度看板已刷新');
    renderSnapshot();
  } catch (error) {
    if (error.status === 401) {
      window.location.replace('/admin/login');
      return;
    }
    state.bannerError = error.message || '刷新失败';
    topline.textContent = '暂时拿不到后台状态。';
    setRefreshState('刷新失败', 'danger');
    pushLog(state.bannerError, true);
    renderSnapshot();
  } finally {
    state.refreshing = false;
    refreshBtn.disabled = false;
  }
}

async function runMigration() {
  try {
    const result = await fetchJSON('/v1/admin/migrate-from-old', { method: 'POST' });
    pushLog(`旧池导入完成：imported=${result.imported || 0} duplicates=${result.duplicates || 0}`);
    await refreshAll();
  } catch (error) {
    if (error.status === 401) {
      window.location.replace('/admin/login');
      return;
    }
    pushLog(`导入旧池失败：${error.message}`, true);
  }
}

async function runRetire() {
  const confirmed = window.confirm('确认尝试退役旧实例？这一步应该只在切域名和验证全部完成后执行。');
  if (!confirmed) {
    return;
  }

  try {
    const result = await fetchJSON('/v1/admin/retire-old', { method: 'POST' });
    pushLog(`退役结果：${JSON.stringify(result)}`);
    await refreshAll();
  } catch (error) {
    if (error.status === 401) {
      window.location.replace('/admin/login');
      return;
    }
    pushLog(`退役失败：${error.message}`, true);
  }
}

async function logout() {
  try {
    await fetchJSON('/v1/admin/session/logout', { method: 'POST' });
  } catch (error) {
    pushLog(`退出登录失败：${error.message}`, true);
  } finally {
    window.location.replace('/admin/login');
  }
}

refreshBtn.addEventListener('click', refreshAll);
logoutBtn.addEventListener('click', logout);

renderLogs();
refreshAll();
