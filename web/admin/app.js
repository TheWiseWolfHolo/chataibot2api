const refreshBtn = document.getElementById('refreshBtn');
const logoutBtn = document.getElementById('logoutBtn');
const refreshState = document.getElementById('refreshState');
const topline = document.getElementById('topline');
const statusGrid = document.getElementById('statusGrid');
const poolSection = document.getElementById('poolSection');
const modelsSection = document.getElementById('modelsSection');
const migrationSection = document.getElementById('migrationSection');
const actionsSection = document.getElementById('actionsSection');
const dangerSection = document.getElementById('dangerSection');
const logSection = document.getElementById('logSection');

const state = {
  snapshot: null,
  logs: [],
  refreshing: false,
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

function shortValue(value) {
  if (!value) {
    return '—';
  }
  const text = String(value);
  return text.length > 42 ? `${text.slice(0, 39)}...` : text;
}

function pushLog(message, isError = false) {
  const stamp = new Date().toLocaleTimeString('zh-CN', { hour12: false });
  state.logs.unshift(`${stamp} · ${isError ? 'ERROR' : 'INFO'} · ${message}`);
  state.logs = state.logs.slice(0, 14);
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
      <span class="metric-value" title="${escapeHtml(value)}">${escapeHtml(shortValue(value))}</span>
      ${note ? `<span class="metric-note">${escapeHtml(note)}</span>` : ''}
    </article>
  `;
}

function pill(label, tone = '') {
  return `<span class="pill${tone ? ` ${tone}` : ''}">${escapeHtml(label)}</span>`;
}

function modelTag(model, note) {
  return `
    <span class="tag">
      <strong>${escapeHtml(model)}</strong>
      <small>${escapeHtml(note)}</small>
    </span>
  `;
}

function currentLastError(snapshot) {
  const candidates = [
    state.bannerError,
    snapshot?.migration?.last_error,
    snapshot?.pool?.last_registration_error,
    snapshot?.pool?.last_persist_error,
  ].filter(Boolean);
  return candidates[0] || '无';
}

function renderStatus(snapshot) {
  const { meta, pool, migration } = snapshot;
  const refreshAt = formatDateTime(snapshot.refreshedAt);
  const primaryLabel = meta.is_primary_target ? '当前实例就是主域名' : '当前实例不是主域名';
  const autoFillLabel = pool.auto_fill_active ? '自动补号运行中' : '自动补号空闲';

  statusGrid.innerHTML = [
    metric('当前实例', meta.instance_name || '未配置', meta.service_label || 'service_label 未配置'),
    metric('部署来源', meta.deploy_source || '未配置', meta.image_ref || 'image_ref 未配置'),
    metric('当前构建版本', meta.version || 'unknown'),
    metric('当前实例地址', meta.public_base_url || '未配置'),
    metric('主域名状态', primaryLabel, meta.primary_public_base_url || 'primary_public_base_url 未配置'),
    metric('当前总号数', formatCount(pool.total_count), `已持久化 ${formatCount(pool.persisted_count)}`),
    metric('低余额号', formatCount(pool.low_quota_count), autoFillLabel),
    metric('最后错误', currentLastError(snapshot), `最后刷新 ${refreshAt}`),
  ].join('');

  topline.textContent = `${meta.instance_name || '未配置实例'} · ${meta.service_label || '未配置 service'} · ${meta.deploy_source || '未配置来源'} · ${primaryLabel}`;

  const tone = state.bannerError ? 'danger' : pool.auto_fill_active ? 'warn' : 'good';
  setRefreshState(`最后刷新 ${refreshAt}`, tone);

  if (migration?.last_error) {
    pushLog(`最近迁移报错：${migration.last_error}`, true);
  }
}

function renderPool(snapshot) {
  const { pool } = snapshot;
  poolSection.innerHTML = `
    <div class="data-grid">
      <div class="data-item"><strong>总号数</strong><span>${formatCount(pool.total_count)}</span></div>
      <div class="data-item"><strong>ready / reusable / borrowed</strong><span>${formatCount(pool.ready_count)} / ${formatCount(pool.reusable_count)} / ${formatCount(pool.borrowed_count)}</span></div>
      <div class="data-item"><strong>目标池 / 低水位</strong><span>${formatCount(pool.target_count)} / ${formatCount(pool.low_watermark)}</span></div>
      <div class="data-item"><strong>低余额号</strong><span>${formatCount(pool.low_quota_count)}</span></div>
      <div class="data-item"><strong>注册并发</strong><span>${formatCount(pool.active_registrations)}</span></div>
      <div class="data-item"><strong>注册失败累计</strong><span>${formatCount(pool.registration_failures)}</span></div>
      <div class="data-item"><strong>上次落盘</strong><span>${formatDateTime(pool.last_persist_at)}</span></div>
      <div class="data-item"><strong>上次恢复</strong><span>${formatDateTime(pool.last_restore_at)}</span></div>
      <div class="data-item"><strong>持久化文件</strong><span>${escapeHtml(pool.persistence_path || '未配置')}</span></div>
      <div class="data-item"><strong>恢复装载 / 拒绝</strong><span>${formatCount(pool.restore_loaded)} / ${formatCount(pool.restore_rejected)}</span></div>
      <div class="data-item"><strong>prune 已删</strong><span>${formatCount(pool.prune_removed)}</span></div>
      <div class="data-item"><strong>下次重试</strong><span>${formatDateTime(pool.next_retry_at)}</span></div>
    </div>
    <div class="strip">
      ${pill(pool.persistence_enabled ? '已启用持久化' : '未启用持久化', pool.persistence_enabled ? 'good' : 'warn')}
      ${pill(pool.auto_fill_active ? '自动补号运行中' : '自动补号空闲', pool.auto_fill_active ? 'warn' : 'good')}
      ${pool.last_registration_error ? pill('存在注册报错', 'danger') : pill('最近无注册报错', 'good')}
      ${pool.last_persist_error ? pill('存在落盘报错', 'danger') : pill('最近无落盘报错', 'good')}
    </div>
  `;
}

function renderModels(snapshot) {
  const { catalog } = snapshot;
  const textTags = catalog.text_models
    .map((item) => {
      const notes = [];
      if (item.internet) {
        notes.push('联网');
      }
      notes.push(`cost ${item.cost}`);
      return modelTag(item.id, notes.join(' · '));
    })
    .join('');

  const imageTags = catalog.image_models
    .map((item) => {
      const notes = [];
      if (item.supports_edit) {
        notes.push('图生图');
      }
      if (item.supports_merge) {
        notes.push('拼图');
      }
      notes.push(`cost ${item.cost}`);
      return modelTag(item.id, notes.join(' · '));
    })
    .join('');

  modelsSection.innerHTML = `
    <div class="cluster">
      <div class="cluster-head">
        <h3>文本模型</h3>
        <span class="pill">${formatCount(catalog.text_models.length)} 个</span>
      </div>
      <div class="tag-list">${textTags || '<span class="subtle">暂无文本模型</span>'}</div>
    </div>
    <div class="cluster">
      <div class="cluster-head">
        <h3>图片模型</h3>
        <span class="pill">${formatCount(catalog.image_models.length)} 个</span>
      </div>
      <div class="tag-list">${imageTags || '<span class="subtle">暂无图片模型</span>'}</div>
    </div>
  `;
}

function renderMigration(snapshot) {
  const { migration, meta } = snapshot;
  migrationSection.innerHTML = `
    <div class="data-grid">
      <div class="data-item"><strong>来源地址</strong><span>${escapeHtml(meta.primary_public_base_url || '未配置')}</span></div>
      <div class="data-item"><strong>请求导入</strong><span>${formatCount(migration.requested)}</span></div>
      <div class="data-item"><strong>成功导入</strong><span>${formatCount(migration.imported)}</span></div>
      <div class="data-item"><strong>重复跳过</strong><span>${formatCount(migration.duplicates)}</span></div>
      <div class="data-item"><strong>拒绝导入</strong><span>${formatCount(migration.rejected)}</span></div>
      <div class="data-item"><strong>导入后总量</strong><span>${formatCount(migration.total_count || snapshot.pool.total_count)}</span></div>
      <div class="data-item"><strong>开始时间</strong><span>${formatDateTime(migration.started_at)}</span></div>
      <div class="data-item"><strong>结束时间</strong><span>${formatDateTime(migration.finished_at)}</span></div>
    </div>
    <div class="strip">
      ${migration.last_error ? pill(`迁移报错：${migration.last_error}`, 'danger') : pill('最近迁移没有报错', 'good')}
    </div>
  `;
}

function renderActions(snapshot) {
  const fillDefault = Math.max(snapshot.pool.low_watermark || 0, 50);
  actionsSection.innerHTML = `
    <div class="action-grid">
      <section class="action-card">
        <h3>补号</h3>
        <p>手动触发一次补号任务，不会改动阈值配置。</p>
        <div class="action-row">
          <label class="field" style="min-width: 180px;">
            <span>补号数量</span>
            <input id="fillCount" type="number" min="1" step="1" value="${fillDefault}" />
          </label>
          <button type="button" class="button primary" id="fillBtn">开始补号</button>
        </div>
      </section>
      <section class="action-card">
        <h3>清理坏号</h3>
        <p>检查当前号池并清掉无额度或明显失效的账号。</p>
        <div class="action-row">
          <button type="button" class="button secondary" id="pruneBtn">执行清理</button>
          <button type="button" class="button secondary" id="migrateBtn">导入旧池</button>
        </div>
      </section>
    </div>
  `;

  document.getElementById('fillBtn')?.addEventListener('click', runFill);
  document.getElementById('pruneBtn')?.addEventListener('click', runPrune);
  document.getElementById('migrateBtn')?.addEventListener('click', runMigration);
}

function renderDanger() {
  dangerSection.innerHTML = `
    <div class="action-card">
      <h3>旧实例退役</h3>
      <p>这个动作应当只在主域名切换、路由验证和号池迁移都完成后再触发。接口如果还没接通，会直接返回错误。</p>
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
    statusGrid.innerHTML = '<p class="subtle">正在读取后台状态。</p>';
    renderLogs();
    return;
  }
  renderStatus(state.snapshot);
  renderPool(state.snapshot);
  renderModels(state.snapshot);
  renderMigration(state.snapshot);
  renderActions(state.snapshot);
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

    const [meta, pool, catalog, migration] = await Promise.all([
      fetchJSON('/v1/admin/meta', { method: 'GET' }),
      fetchJSON('/v1/admin/pool', { method: 'GET' }),
      fetchJSON('/v1/admin/catalog', { method: 'GET' }),
      fetchJSON('/v1/admin/migration/status', { method: 'GET' }),
    ]);

    state.bannerError = '';
    state.snapshot = {
      session,
      meta,
      pool,
      catalog,
      migration,
      refreshedAt: new Date().toISOString(),
    };
    pushLog(`面板已刷新：${meta.instance_name || meta.service_label || '未命名实例'}`);
    renderSnapshot();
  } catch (error) {
    if (error.status === 401) {
      window.location.replace('/admin/login');
      return;
    }
    state.bannerError = error.message || '刷新失败';
    pushLog(state.bannerError, true);
    if (state.snapshot) {
      renderSnapshot();
    } else {
      topline.textContent = state.bannerError;
      setRefreshState('刷新失败', 'danger');
      renderLogs();
    }
  } finally {
    state.refreshing = false;
    refreshBtn.disabled = false;
  }
}

async function runFill() {
  const input = document.getElementById('fillCount');
  const count = Number(input?.value || 0);
  if (!Number.isInteger(count) || count < 1) {
    pushLog('补号数量必须是大于 0 的整数', true);
    return;
  }

  try {
    const result = await fetchJSON('/v1/admin/pool/fill', {
      method: 'POST',
      body: JSON.stringify({ count }),
    });
    pushLog(`补号任务已提交：task=${result.task_id || 'unknown'} requested=${result.requested || count}`);
    await refreshAll();
  } catch (error) {
    if (error.status === 401) {
      window.location.replace('/admin/login');
      return;
    }
    pushLog(`补号失败：${error.message}`, true);
  }
}

async function runPrune() {
  try {
    const result = await fetchJSON('/v1/admin/pool/prune', { method: 'POST' });
    pushLog(`坏号清理完成：checked=${result.checked || 0} removed=${result.removed || 0}`);
    await refreshAll();
  } catch (error) {
    if (error.status === 401) {
      window.location.replace('/admin/login');
      return;
    }
    pushLog(`清理失败：${error.message}`, true);
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
