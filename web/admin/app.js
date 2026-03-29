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
  catalog: null,
  migration: null,
  probeOverlay: new Map(),
  expandedJWTs: new Set(),
  filters: {
    status: 'all',
    bucket: 'all',
    query: '',
    sort: 'status-asc-quota-asc',
  },
  logs: [],
  refreshing: false,
  probing: false,
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
  switch (state.filters.sort) {
    case 'quota-desc':
      sorted.sort((a, b) => (b.quota - a.quota) || a.jwt.localeCompare(b.jwt));
      break;
    case 'updated-desc':
      sorted.sort((a, b) => {
        const left = a.last_checked_at ? new Date(a.last_checked_at).getTime() : 0;
        const right = b.last_checked_at ? new Date(b.last_checked_at).getTime() : 0;
        return (right - left) || (a.quota - b.quota) || a.jwt.localeCompare(b.jwt);
      });
      break;
    case 'status-asc-quota-asc':
    default:
      sorted.sort((a, b) => {
        const left = STATUS_ORDER[a.status] ?? 99;
        const right = STATUS_ORDER[b.status] ?? 99;
        if (left !== right) {
          return left - right;
        }
        if (a.quota !== b.quota) {
          return a.quota - b.quota;
        }
        return a.jwt.localeCompare(b.jwt);
      });
      break;
  }

  return sorted;
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

function renderQuotaTableTools() {
  const rows = getFilteredRows();
  const disabled = state.probing || rows.length === 0 ? 'disabled' : '';
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
        </select>
      </label>
      <label class="field" style="min-width: 240px; flex: 1 1 240px;">
        <span>搜索 JWT</span>
        <input id="queryInput" type="search" placeholder="输入 token 片段" value="${escapeHtml(state.filters.query)}" />
      </label>
      <label class="field" style="min-width: 180px;">
        <span>排序</span>
        <select id="sortSelect">
          <option value="status-asc-quota-asc">按状态再按额度</option>
          <option value="quota-desc">额度从高到低</option>
          <option value="updated-desc">按最近更新时间</option>
        </select>
      </label>
      <button type="button" class="button primary" id="probeBtn" ${disabled}>${state.probing ? '核验中' : `实时核验当前筛选 (${rows.length})`}</button>
    </div>
  `;

  document.getElementById('statusFilter').value = state.filters.status;
  document.getElementById('bucketFilter').value = state.filters.bucket;
  document.getElementById('sortSelect').value = state.filters.sort;

  document.getElementById('statusFilter')?.addEventListener('change', (event) => {
    state.filters.status = event.target.value;
    renderQuotaTable();
  });
  document.getElementById('bucketFilter')?.addEventListener('change', (event) => {
    state.filters.bucket = event.target.value;
    renderQuotaTable();
  });
  document.getElementById('queryInput')?.addEventListener('input', (event) => {
    state.filters.query = event.target.value;
    renderQuotaTable();
  });
  document.getElementById('sortSelect')?.addEventListener('change', (event) => {
    state.filters.sort = event.target.value;
    renderQuotaTable();
  });
  document.getElementById('probeBtn')?.addEventListener('click', runProbeCurrentFilter);
}

function renderQuotaTable() {
  renderQuotaTableTools();
  const rows = getFilteredRows();
  probeState.textContent = state.probing
    ? '正在核验当前筛选结果'
    : `当前筛选 ${rows.length} 条；总览仍基于缓存快照`;

  if (!rows.length) {
    quotaTableWrap.innerHTML = '<div class="data-item"><strong>结果</strong><span>当前筛选没有匹配账号</span></div>';
    return;
  }

  quotaTableWrap.innerHTML = `
    <div class="table-scroll">
      <table class="model-table quota-table">
        <thead>
          <tr>
            <th>额度</th>
            <th>状态</th>
            <th>JWT</th>
            <th>池位</th>
            <th>最近更新时间</th>
          </tr>
        </thead>
        <tbody>
          ${rows.map((row) => {
            const expanded = state.expandedJWTs.has(row.jwt);
            const statusTone = toneForStatus(row.status);
            const statusLabel = STATUS_LABELS[row.status] || row.status || '未知';
            const jwtDisplay = expanded ? row.jwt : maskJWT(row.jwt);
            const note = row.probeState === 'live'
              ? '<small>实时</small>'
              : row.probeState === 'error'
                ? `<small>${escapeHtml(row.probeError || '核验失败')}</small>`
                : '<small>缓存</small>';
            return `
              <tr>
                <td><strong>${escapeHtml(String(row.quota))}</strong></td>
                <td><div class="model-capabilities">${pill(statusLabel, statusTone)} ${note}</div></td>
                <td>
                  <button type="button" class="button ghost quota-jwt-button" style="min-height: 44px; letter-spacing: 0.06em; text-transform: none; font-size: 0.84rem;" onclick="toggleJwtVisibility('${escapeHtml(row.jwt)}')">${escapeHtml(jwtDisplay)}</button>
                </td>
                <td>${escapeHtml(row.pool_bucket || 'unknown')}</td>
                <td>${formatDateTime(row.last_checked_at)}</td>
              </tr>
            `;
          }).join('')}
        </tbody>
      </table>
    </div>
  `;
}

async function runProbeCurrentFilter() {
  const rows = getFilteredRows();
  if (!rows.length || state.probing) {
    return;
  }

  state.probing = true;
  renderQuotaTable();
  try {
    const payload = await fetchJSON('/v1/admin/quota/probe', {
      method: 'POST',
      body: JSON.stringify({ jwts: rows.map((row) => row.jwt) }),
    });

    for (const item of payload.results || []) {
      state.probeOverlay.set(item.jwt, {
        ...item,
        checked_at: payload.checked_at,
      });
    }
    pushLog(`实时核验完成：${rows.length} 条`);
  } catch (error) {
    if (error.status === 401) {
      window.location.replace('/admin/login');
      return;
    }
    pushLog(`实时核验失败：${error.message}`, true);
  } finally {
    state.probing = false;
    renderQuotaTable();
  }
}

function modelTableRows(models, type) {
  if (!models?.length) {
    return `<tr><td colspan="4" class="subtle">暂无${type === 'text' ? '文本' : '图片'}模型</td></tr>`;
  }

  return models.map((item) => {
    const capabilities = [];
    if (type === 'text') {
      capabilities.push(item.internet ? modelCapabilityPill('联网', 'good') : modelCapabilityPill('标准'));
    } else {
      capabilities.push(item.supports_edit ? modelCapabilityPill('图生图', 'good') : modelCapabilityPill('仅生图'));
      if (item.supports_merge) {
        capabilities.push(modelCapabilityPill('拼图', 'warn'));
      }
    }

    return `
      <tr>
        <td class="model-name">${escapeHtml(item.id)}</td>
        <td>${type === 'text' ? '文本' : '图片'}</td>
        <td><div class="model-capabilities">${capabilities.join('')}</div></td>
        <td>${escapeHtml(String(item.cost))}</td>
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
              <tr><th>模型</th><th>类型</th><th>能力</th><th>Cost</th></tr>
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
              <tr><th>模型</th><th>类型</th><th>能力</th><th>Cost</th></tr>
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

    const [meta, snapshot, catalog, migration] = await Promise.all([
      fetchJSON('/v1/admin/meta', { method: 'GET' }),
      fetchJSON('/v1/admin/quota/snapshot', { method: 'GET' }),
      fetchJSON('/v1/admin/catalog', { method: 'GET' }),
      fetchJSON('/v1/admin/migration/status', { method: 'GET' }),
    ]);

    state.bannerError = '';
    state.session = session;
    state.meta = meta;
    state.snapshot = snapshot;
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
