const tokenInput = document.getElementById('adminToken');
const saveTokenBtn = document.getElementById('saveTokenBtn');
const refreshBtn = document.getElementById('refreshBtn');
const overviewCard = document.getElementById('overviewCard');
const persistenceCard = document.getElementById('persistenceCard');
const migrationCard = document.getElementById('migrationCard');
const controlsCard = document.getElementById('controlsCard');
const domainCard = document.getElementById('domainCard');
const retireCard = document.getElementById('retireCard');
const logCard = document.getElementById('logCard');

const state = {
  token: localStorage.getItem('holo_image_admin_token') || '',
  logs: [],
};

tokenInput.value = state.token;

saveTokenBtn.addEventListener('click', () => {
  state.token = tokenInput.value.trim();
  localStorage.setItem('holo_image_admin_token', state.token);
  renderLog('已保存 admin key');
});

refreshBtn.addEventListener('click', refreshAll);

function escapeHtml(value) {
  return String(value ?? '')
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&#39;');
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
  const level = isError ? 'ERROR' : 'INFO';
  state.logs.unshift(`${stamp} · ${level} · ${message}`);
  state.logs = state.logs.slice(0, 12);
  renderLog();
}

function renderStat(label, value) {
  return `
    <div class="stat">
      <span class="stat-label">${escapeHtml(label)}</span>
      <span class="stat-value">${escapeHtml(value)}</span>
    </div>
  `;
}

function renderLog() {
  const entries = state.logs.length > 0
    ? state.logs.map((entry) => `<li class="log-entry">${escapeHtml(entry)}</li>`).join('')
    : '<li class="log-entry">尚无操作记录</li>';
  logCard.innerHTML = `
    <h2>运行日志</h2>
    <p class="hint">所有失败会直接显示，不做静默兜底。</p>
    <ul class="log-list">${entries}</ul>
  `;
}

async function api(path, options = {}) {
  const token = state.token.trim();
  if (!token) {
    throw new Error('请先输入 admin key');
  }

  const headers = new Headers(options.headers || {});
  headers.set('Authorization', `Bearer ${token}`);
  if (options.body && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json');
  }

  const response = await fetch(path, {
    ...options,
    headers,
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
  if (!response.ok) {
    throw new Error(payload?.error?.message || payload?.message || payload?.raw || `HTTP ${response.status}`);
  }
  return payload;
}

function renderOverview(pool, meta) {
  overviewCard.innerHTML = `
    <div class="pill ${meta.is_primary_target ? 'success' : 'warning'}">
      ${meta.is_primary_target ? '当前实例已接管主域名' : '当前实例尚未接管主域名'}
    </div>
    <h2>总览</h2>
    <p class="hint">实例：${escapeHtml(meta.instance_name || '未配置')} · 版本：${escapeHtml(meta.version || 'unknown')}</p>
    <div class="stats">
      ${renderStat('总号数', pool.total_count ?? 0)}
      ${renderStat('可立即使用', (pool.ready_count ?? 0) + (pool.reusable_count ?? 0))}
      ${renderStat('低水位', pool.low_watermark ?? 0)}
      ${renderStat('自动补号', pool.auto_fill_active ? '开启' : '关闭')}
      ${renderStat('注册成功', pool.registration_successes ?? 0)}
      ${renderStat('注册失败', pool.registration_failures ?? 0)}
    </div>
  `;
}

function renderPersistence(pool) {
  persistenceCard.innerHTML = `
    <h2>持久化状态</h2>
    <p class="hint">持久化文件：${escapeHtml(pool.persistence_path || '未启用')}</p>
    <div class="stats">
      ${renderStat('持久化启用', pool.persistence_enabled ? '是' : '否')}
      ${renderStat('落盘数量', pool.persisted_count ?? 0)}
      ${renderStat('重启恢复', pool.restore_loaded ?? 0)}
      ${renderStat('恢复拒绝', pool.restore_rejected ?? 0)}
      ${renderStat('最后落盘', formatDateTime(pool.last_persist_at))}
      ${renderStat('最后恢复', formatDateTime(pool.last_restore_at))}
    </div>
  `;
}

function renderMigration(pool, migration, meta) {
  const statusClass = migration.last_error ? 'danger' : migration.imported > 0 ? 'success' : 'warning';
  migrationCard.innerHTML = `
    <div class="pill ${statusClass}">
      ${migration.last_error ? '最近迁移失败' : migration.imported > 0 ? '最近迁移已导入账号' : '等待迁移动作'}
    </div>
    <h2>迁移中心</h2>
    <p class="hint">旧池来源：${escapeHtml(meta.primary_public_base_url || '未配置')} → 当前实例</p>
    <div class="stats">
      ${renderStat('requested', migration.requested ?? 0)}
      ${renderStat('imported', migration.imported ?? 0)}
      ${renderStat('duplicates', migration.duplicates ?? 0)}
      ${renderStat('rejected', migration.rejected ?? 0)}
      ${renderStat('overflow', migration.overflow ?? 0)}
      ${renderStat('当前总数', migration.total_count ?? pool.total_count ?? 0)}
    </div>
    <div class="action-row" style="margin-top:16px;">
      <button id="migrateBtn" type="button">迁移旧实例池子</button>
      <span class="muted">started: ${escapeHtml(formatDateTime(migration.started_at))}</span>
      <span class="muted">finished: ${escapeHtml(formatDateTime(migration.finished_at))}</span>
    </div>
    ${migration.last_error ? `<p class="hint" style="margin-top:12px;color:#fca5a5;">${escapeHtml(migration.last_error)}</p>` : ''}
  `;
  document.getElementById('migrateBtn')?.addEventListener('click', runMigration);
}

function renderControls() {
  controlsCard.innerHTML = `
    <h2>池管理</h2>
    <p class="hint">直接对服务端管理接口进行操作。</p>
    <div class="action-row" style="margin-top:16px;">
      <button id="fill50Btn" type="button">补 50 个</button>
      <button id="fill200Btn" type="button">补 200 个</button>
      <button id="pruneBtn" type="button">清理失效号</button>
    </div>
  `;
  document.getElementById('fill50Btn')?.addEventListener('click', () => runFill(50));
  document.getElementById('fill200Btn')?.addEventListener('click', () => runFill(200));
  document.getElementById('pruneBtn')?.addEventListener('click', runPrune);
}

function renderDomain(meta) {
  domainCard.innerHTML = `
    <h2>域名切换状态</h2>
    <p class="hint">当前域名与目标主域名对比。</p>
    <div class="stats">
      ${renderStat('当前实例地址', meta.public_base_url || '未配置')}
      ${renderStat('目标主域名', meta.primary_public_base_url || '未配置')}
      ${renderStat('是否主实例', meta.is_primary_target ? '是' : '否')}
      ${renderStat('上次迁移', formatDateTime(meta.last_migration_at))}
    </div>
  `;
}

function renderRetire() {
  retireCard.innerHTML = `
    <h2>危险操作</h2>
    <p class="hint">当前版本不会假装已经自动退役老实例；调用会返回明确错误。</p>
    <div class="action-row" style="margin-top:16px;">
      <button id="retireBtn" type="button">退役老实例（当前未自动化）</button>
    </div>
  `;
  document.getElementById('retireBtn')?.addEventListener('click', runRetireOld);
}

async function refreshAll() {
  try {
    const [pool, meta, migration] = await Promise.all([
      api('/v1/admin/pool'),
      api('/v1/admin/meta'),
      api('/v1/admin/migration/status'),
    ]);
    renderOverview(pool, meta);
    renderPersistence(pool);
    renderMigration(pool, migration, meta);
    renderControls();
    renderDomain(meta);
    renderRetire();
    renderLog();
    pushLog('状态已刷新');
  } catch (error) {
    renderLog();
    pushLog(error.message || String(error), true);
  }
}

async function runPrune() {
  try {
    const result = await api('/v1/admin/pool/prune', { method: 'POST' });
    pushLog(`prune 完成：removed=${result.removed}, remaining=${result.remaining}`);
    await refreshAll();
  } catch (error) {
    pushLog(error.message || String(error), true);
  }
}

async function runFill(count) {
  try {
    const result = await api('/v1/admin/pool/fill', {
      method: 'POST',
      body: JSON.stringify({ count }),
    });
    pushLog(`fill 已触发：task=${result.task_id}, requested=${result.requested}`);
    await refreshAll();
  } catch (error) {
    pushLog(error.message || String(error), true);
  }
}

async function runMigration() {
  try {
    const result = await api('/v1/admin/migrate-from-old', { method: 'POST' });
    pushLog(`迁移完成：imported=${result.imported}, duplicates=${result.duplicates}, total=${result.total_count}`);
    await refreshAll();
  } catch (error) {
    pushLog(error.message || String(error), true);
  }
}

async function runRetireOld() {
  try {
    await api('/v1/admin/retire-old', { method: 'POST' });
  } catch (error) {
    pushLog(error.message || String(error), true);
  }
}

renderLog();
