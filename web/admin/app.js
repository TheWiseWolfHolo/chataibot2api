const tokenInput = document.getElementById('adminToken');
const saveTokenBtn = document.getElementById('saveTokenBtn');
const refreshBtn = document.getElementById('refreshBtn');
const heroMetrics = document.getElementById('heroMetrics');
const commandStatus = document.getElementById('commandStatus');
const overviewCard = document.getElementById('overviewCard');
const persistenceCard = document.getElementById('persistenceCard');
const modelsCard = document.getElementById('modelsCard');
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
setConnectionState(Boolean(state.token), state.token ? '待刷新' : '未连接');

saveTokenBtn.addEventListener('click', () => {
  state.token = tokenInput.value.trim();
  localStorage.setItem('holo_image_admin_token', state.token);
  setConnectionState(Boolean(state.token), state.token ? '待刷新' : '未连接');
  pushLog(state.token ? 'admin key 已保存' : 'admin key 已清空');
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

function setConnectionState(online, label) {
  commandStatus.textContent = label;
  commandStatus.classList.toggle('online', online);
}

function pushLog(message, isError = false) {
  const stamp = new Date().toLocaleTimeString('zh-CN', { hour12: false });
  state.logs.unshift(`${stamp} · ${isError ? 'ERROR' : 'INFO'} · ${message}`);
  state.logs = state.logs.slice(0, 12);
  renderLog();
}

function renderLog() {
  const items = state.logs.length > 0
    ? state.logs.map((entry) => `<li class="log-entry">${escapeHtml(entry)}</li>`).join('')
    : '<li class="log-entry">日志还没开始滚动，先点一次“刷新面板”。</li>';

  logCard.innerHTML = `
    <h2>操作日志</h2>
    <p class="panel-lead">这里只显示结果和错误，不写奇怪的自我解释。</p>
    <ul class="log-list">${items}</ul>
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

  const response = await fetch(path, { ...options, headers });
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

function heroChip(label, value) {
  return `<span class="hero-chip">${escapeHtml(label)} <strong>${escapeHtml(value)}</strong></span>`;
}

function statTile(label, value, note = '') {
  return `
    <div class="stat-tile">
      <span class="stat-label">${escapeHtml(label)}</span>
      <span class="stat-value">${escapeHtml(value)}</span>
      ${note ? `<span class="stat-note">${escapeHtml(note)}</span>` : ''}
    </div>
  `;
}

function signalTile(label, value, note = '') {
  return `
    <div class="signal-tile">
      <span class="signal-label">${escapeHtml(label)}</span>
      <span class="signal-value">${escapeHtml(value)}</span>
      ${note ? `<span class="signal-note">${escapeHtml(note)}</span>` : ''}
    </div>
  `;
}

function modelTag(model, extra = '') {
  return `<span class="model-tag">${escapeHtml(model)}${extra ? `<small>${escapeHtml(extra)}</small>` : ''}</span>`;
}

function renderHero(pool, meta, catalog) {
  heroMetrics.innerHTML = [
    heroChip('主域名', meta.is_primary_target ? '已接管' : '未接管'),
    heroChip('总号数', formatCount(pool.total_count)),
    heroChip('低余额号', formatCount(pool.low_quota_count)),
    heroChip('文本模型', formatCount(catalog.text_models.length)),
    heroChip('图片模型', formatCount(catalog.image_models.length)),
  ].join('');
}

function renderOverview(pool) {
  const availableCount = (pool.ready_count ?? 0) + (pool.reusable_count ?? 0);

  overviewCard.innerHTML = `
    <h2>号池总览</h2>
    <p class="panel-lead">首屏直接看总量、低余额、坏号清理和补号压力，不再让你自己猜。</p>
    <div class="stats-grid">
      ${statTile('当前总号数', formatCount(pool.total_count), `目标池 ${formatCount(pool.target_count)}`)}
      ${statTile('可直接使用', formatCount(availableCount), `borrowed ${formatCount(pool.borrowed_count)}`)}
      ${statTile('低余额号', formatCount(pool.low_quota_count), `阈值 < ${formatCount(10)}`)}
      ${statTile('已清理坏号', formatCount(pool.prune_removed), `累计 prune ${formatCount(pool.prune_checks)}`)}
    </div>
    <div class="section-flag-row">
      <span class="flag ${pool.auto_fill_active ? 'success' : 'warning'}">${pool.auto_fill_active ? '自动补号已开启' : '自动补号已暂停'}</span>
      <span class="flag ${pool.active_registrations > 0 ? 'warning' : 'success'}">当前注册并发 ${formatCount(pool.active_registrations)}</span>
      <span class="flag ${pool.registration_failures > 0 ? 'danger' : 'success'}">注册失败 ${formatCount(pool.registration_failures)}</span>
      <span class="flag">低水位 ${formatCount(pool.low_watermark)}</span>
    </div>
  `;
}

function renderPersistence(pool) {
  persistenceCard.innerHTML = `
    <h2>持久化与恢复</h2>
    <p class="panel-lead">看这块就知道服务是不是能自己活下去。</p>
    <div class="signal-grid">
      ${signalTile('已落盘', formatCount(pool.persisted_count), pool.persistence_path || '未配置落盘路径')}
      ${signalTile('重启恢复', formatCount(pool.restore_loaded), `拒绝 ${formatCount(pool.restore_rejected)}`)}
      ${signalTile('最后落盘', formatDateTime(pool.last_persist_at))}
      ${signalTile('最后恢复', formatDateTime(pool.last_restore_at))}
    </div>
  `;
}

function renderModels(catalog) {
  const textTags = catalog.text_models.map((model) => {
    const marks = [];
    if (model.internet) {
      marks.push('联网');
    }
    marks.push(`cost ${model.cost}`);
    return modelTag(model.id, marks.join(' · '));
  }).join('');

  const imageTags = catalog.image_models.map((model) => {
    const marks = [];
    if (model.supports_edit) {
      marks.push('编辑');
    }
    if (model.supports_merge) {
      marks.push('拼图');
    }
    marks.push(`cost ${model.cost}`);
    return modelTag(model.id, marks.join(' · '));
  }).join('');

  modelsCard.innerHTML = `
    <h2>模型支持</h2>
    <p class="panel-lead">文本和图片模型都直接摊开显示，不再靠猜。</p>
    <div class="model-layout">
      <div class="model-cluster">
        <div class="cluster-head">
          <h3>文本模型</h3>
          <span>${formatCount(catalog.text_models.length)} 个</span>
        </div>
        <div class="model-tags">${textTags}</div>
      </div>
      <div class="model-cluster">
        <div class="cluster-head">
          <h3>图片模型</h3>
          <span>${formatCount(catalog.image_models.length)} 个</span>
        </div>
        <div class="model-tags">${imageTags}</div>
      </div>
    </div>
  `;
}

function renderMigration(pool, migration, meta) {
  const migrationFlag = migration.imported > 0
    ? '<span class="flag success">最近迁移有新增</span>'
    : '<span class="flag">最近没有新的迁移结果</span>';

  migrationCard.innerHTML = `
    <h2>迁移中心</h2>
    <p class="panel-lead">旧池来源固定为当前主域名，导入结果直接在这里看。</p>
    <div class="section-flag-row">
      ${migrationFlag}
      <span class="flag">来源 ${escapeHtml(meta.primary_public_base_url || '未配置')}</span>
    </div>
    <div class="signal-grid">
      ${signalTile('请求导入', formatCount(migration.requested))}
      ${signalTile('成功导入', formatCount(migration.imported))}
      ${signalTile('重复跳过', formatCount(migration.duplicates))}
      ${signalTile('导入后总量', formatCount(migration.total_count || pool.total_count))}
    </div>
    <div class="action-row" style="margin-top:18px;">
      <button id="migrateBtn" type="button">迁移旧池到当前实例</button>
    </div>
    <div class="section-flag-row">
      <span class="flag">开始 ${escapeHtml(formatDateTime(migration.started_at))}</span>
      <span class="flag">完成 ${escapeHtml(formatDateTime(migration.finished_at))}</span>
    </div>
  `;

  document.getElementById('migrateBtn')?.addEventListener('click', runMigration);
}

function renderControls(pool) {
  controlsCard.innerHTML = `
    <h2>快速操作</h2>
    <p class="panel-lead">保留最常用的动作，别把首页做成按钮垃圾场。</p>
    <ul class="stack-list">
      <li class="stack-item">
        <div>
          <strong>自动补号</strong>
          <span>低于 ${formatCount(pool.low_watermark)} 自动补到 ${formatCount(pool.target_count)}</span>
        </div>
        <code>${pool.auto_fill_active ? 'ON' : 'OFF'}</code>
      </li>
      <li class="stack-item">
        <div>
          <strong>手动补号</strong>
          <span>紧急补充 50 / 200 个</span>
        </div>
        <code>FILL</code>
      </li>
      <li class="stack-item">
        <div>
          <strong>坏号清理</strong>
          <span>主动 prune 当前池子里的无效账号</span>
        </div>
        <code>PRUNE</code>
      </li>
    </ul>
    <div class="action-row" style="margin-top:18px;">
      <button id="fill50Btn" type="button">补 50 个</button>
      <button id="fill200Btn" type="button">补 200 个</button>
      <button id="pruneBtn" type="button">清理坏号</button>
    </div>
  `;

  document.getElementById('fill50Btn')?.addEventListener('click', () => runFill(50));
  document.getElementById('fill200Btn')?.addEventListener('click', () => runFill(200));
  document.getElementById('pruneBtn')?.addEventListener('click', runPrune);
}

function renderDomain(meta, pool) {
  domainCard.innerHTML = `
    <h2>域名与实例</h2>
    <p class="panel-lead">这一块只说主域名、当前实例和恢复状态，不拐弯。</p>
    <ul class="stack-list">
      <li class="stack-item">
        <div>
          <strong>主域名</strong>
          <span>当前对外入口</span>
        </div>
        <code>${escapeHtml(meta.public_base_url || '未配置')}</code>
      </li>
      <li class="stack-item">
        <div>
          <strong>实例标识</strong>
          <span>当前接管流量的服务实例名</span>
        </div>
        <code>${escapeHtml(meta.instance_name || '未配置')}</code>
      </li>
      <li class="stack-item">
        <div>
          <strong>恢复加载</strong>
          <span>最近重启后实际恢复进内存的账号数</span>
        </div>
        <code>${formatCount(pool.restore_loaded)}</code>
      </li>
    </ul>
  `;
}

function renderRetire() {
  retireCard.innerHTML = `
    <h2>高危操作</h2>
    <p class="panel-lead">老实例退役现在还是手动流程，这里只保留明确提示，不写怪话。</p>
    <div class="section-flag-row">
      <span class="flag danger">自动退役暂未开放</span>
      <span class="flag">先确认域名和恢复状态都正常</span>
    </div>
    <div class="action-row" style="margin-top:18px;">
      <button id="retireBtn" type="button">检查退役接口</button>
    </div>
  `;

  document.getElementById('retireBtn')?.addEventListener('click', runRetireOld);
}

async function refreshAll() {
  try {
    const [pool, meta, migration, catalog] = await Promise.all([
      api('/v1/admin/pool'),
      api('/v1/admin/meta'),
      api('/v1/admin/migration/status'),
      api('/v1/admin/catalog'),
    ]);

    renderHero(pool, meta, catalog);
    renderOverview(pool);
    renderPersistence(pool);
    renderModels(catalog);
    renderMigration(pool, migration, meta);
    renderControls(pool);
    renderDomain(meta, pool);
    renderRetire();
    setConnectionState(true, '已连接');
    pushLog('面板状态已刷新');
  } catch (error) {
    setConnectionState(false, '连接失败');
    pushLog(error.message || String(error), true);
  }
}

async function runPrune() {
  try {
    const result = await api('/v1/admin/pool/prune', { method: 'POST' });
    pushLog(`坏号清理完成：移除 ${result.removed}，剩余 ${result.remaining}`);
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
    pushLog(`补号已触发：任务 ${result.task_id}，请求 ${result.requested}`);
    await refreshAll();
  } catch (error) {
    pushLog(error.message || String(error), true);
  }
}

async function runMigration() {
  try {
    const result = await api('/v1/admin/migrate-from-old', { method: 'POST' });
    pushLog(`迁移完成：新增 ${result.imported}，重复 ${result.duplicates}，总量 ${result.total_count}`);
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
