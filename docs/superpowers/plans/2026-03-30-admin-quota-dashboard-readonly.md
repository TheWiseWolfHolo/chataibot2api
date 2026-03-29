# Admin Quota Dashboard Read-Only Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在不触碰号池写路径的前提下，为后台加入只读额度看板：首屏总览 + 明细表 + 当前筛选实时核验，并把模型/迁移/危险操作/日志收纳为默认折叠区。

**Architecture:** 保持单一 Go 服务架构不变，在服务端新增两个只读 admin 接口：缓存快照接口和显式触发的只读 probe 接口；前端用快照驱动首屏总览与表格，用 probe 结果做本页会话内临时覆盖显示。池层只暴露脱离内部指针的只读行快照，不修改持久化、自动补号、清理、迁移等写路径。

**Tech Stack:** Go 1.25、标准库 `net/http` / `encoding/json` / `embed`、现有 `SimplePool`、原生 HTML/CSS/JS Admin UI、Go 测试框架。

---

## File Structure

### Existing files to modify

- `app.go`
  - 扩展 `PoolManager` 只读接口；增加额度快照/只读 probe 聚合逻辑。
- `pool.go`
  - 新增只读行快照导出 helper，携带 bucket 信息，返回 detached 值对象。
- `server.go`
  - 注册 `/v1/admin/quota/snapshot` 与 `/v1/admin/quota/probe`；实现 handler。
- `server_test.go`
  - 为新接口、只读边界、静态页面/脚本契约补测试。
- `pool_test.go`
  - 为池只读快照 helper 补测试。
- `web/admin/index.html`
  - 改成首屏总览 + 表格 + 默认折叠区结构，去掉 `LIVE` 装饰。
- `web/admin/app.js`
  - 改成读取 snapshot/probe，新建表格筛选、排序、JWT 展开、折叠区行为。
- `web/admin/styles.css`
  - 收敛首屏布局，保留 neo-brutalism 风格但减少噪音。

### New files to create

- `admin_quota.go`
  - 定义只读额度 DTO、状态派生逻辑、`App` 的 snapshot/probe 聚合方法。

### No-write boundary reminder

本计划不允许修改以下行为：

- `StartFillTask`
- `Prune`
- `ImportAccounts`
- `MigrateFromLegacy`
- `persistLocked`
- `restoreFromStore`
- 任何持久化文件格式

---

### Task 1: 为池层增加只读额度行快照

**Files:**
- Create: `admin_quota.go`
- Modify: `app.go`
- Modify: `pool.go`
- Test: `pool_test.go`

- [ ] **Step 1: 先写池层失败测试，锁定 bucket、去重和脱离内部指针的只读快照行为**

```go
func TestSimplePoolAdminQuotaRowsPreserveBucketsAndDedupe(t *testing.T) {
	t.Helper()

	pool := NewSimplePool(10, 0, func() (string, error) {
		return "", fmt.Errorf("not used")
	}, func(_ string) (int, error) {
		return 65, nil
	})

	ready := &Account{JWT: "jwt-ready", Quota: 18}
	reuse := &Account{JWT: "jwt-reuse", Quota: 7}
	borrowed := &Account{JWT: "jwt-borrowed", Quota: 4}

	pool.ready = []*Account{ready}
	pool.reusable = []*Account{reuse}
	pool.borrowed = map[*Account]string{borrowed: "jwt-borrowed"}

	rows := pool.AdminQuotaRows()
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %+v", rows)
	}

	seen := map[string]AdminQuotaRow{}
	for _, row := range rows {
		seen[row.JWT] = row
	}
	if seen["jwt-ready"].PoolBucket != "ready" {
		t.Fatalf("expected ready bucket, got %+v", seen["jwt-ready"])
	}
	if seen["jwt-reuse"].PoolBucket != "reusable" {
		t.Fatalf("expected reusable bucket, got %+v", seen["jwt-reuse"])
	}
	if seen["jwt-borrowed"].PoolBucket != "borrowed" {
		t.Fatalf("expected borrowed bucket, got %+v", seen["jwt-borrowed"])
	}

	rows[0].JWT = "mutated"
	if pool.ready[0].JWT != "jwt-ready" {
		t.Fatalf("expected detached snapshot, got pool mutation %+v", pool.ready[0])
	}
}
```

- [ ] **Step 2: 跑单测确认当前实现还没有这个 helper**

Run:

```powershell
& 'C:\Program Files\Go\bin\go.exe' test ./... -run TestSimplePoolAdminQuotaRowsPreserveBucketsAndDedupe -count=1
```

Expected:

- 编译失败，提示 `AdminQuotaRow` / `AdminQuotaRows` 未定义

- [ ] **Step 3: 在 `admin_quota.go` 定义只读 DTO 和状态派生函数**

```go
package main

import "time"

type AdminQuotaRow struct {
	JWT           string     `json:"jwt"`
	Quota         int        `json:"quota"`
	Status        string     `json:"status"`
	PoolBucket    string     `json:"pool_bucket"`
	LastCheckedAt *time.Time `json:"last_checked_at,omitempty"`
}

type AdminQuotaSummary struct {
	TotalCount      int `json:"total_count"`
	TotalQuota      int `json:"total_quota"`
	LowQuotaCount   int `json:"low_quota_count"`
	NearEmptyCount  int `json:"near_empty_count"`
}

type AdminQuotaSnapshot struct {
	Summary AdminQuotaSummary `json:"summary"`
	Rows    []AdminQuotaRow   `json:"rows"`
}

type AdminQuotaProbeItem struct {
	JWT    string `json:"jwt"`
	Quota  int    `json:"quota,omitempty"`
	Status string `json:"status,omitempty"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
}

type AdminQuotaProbeResponse struct {
	CheckedAt time.Time             `json:"checked_at"`
	Results   []AdminQuotaProbeItem `json:"results"`
}

func deriveAdminQuotaStatus(quota int) string {
	switch {
	case quota < 5:
		return "near-empty"
	case quota < 10:
		return "low"
	default:
		return "healthy"
	}
}
```

- [ ] **Step 4: 扩展 `PoolManager` 并在 `pool.go` 实现只读快照 helper**

Modify `app.go` interface:

```go
type PoolManager interface {
	Acquire(cost int) *Account
	Release(acc *Account)
	Status() PoolStatus
	StartFillTask(count int) FillTaskSnapshot
	Prune() PruneSummary
	ImportAccounts(accounts []*Account) ImportPoolResult
	ExportAccounts() []ExportedAccount
	AdminQuotaRows() []AdminQuotaRow
}
```

Modify `pool.go`:

```go
func (p *SimplePool) AdminQuotaRows() []AdminQuotaRow {
	p.mu.Lock()
	defer p.mu.Unlock()

	rows := make([]AdminQuotaRow, 0, len(p.ready)+len(p.reusable)+len(p.borrowed))
	seen := make(map[string]struct{})
	appendRow := func(jwt string, quota int, bucket string) {
		jwt = strings.TrimSpace(jwt)
		if jwt == "" {
			return
		}
		if _, ok := seen[jwt]; ok {
			return
		}
		seen[jwt] = struct{}{}
		rows = append(rows, AdminQuotaRow{
			JWT:        jwt,
			Quota:      quota,
			Status:     deriveAdminQuotaStatus(quota),
			PoolBucket: bucket,
		})
	}

	for _, acc := range p.ready {
		if acc != nil {
			appendRow(acc.JWT, acc.Quota, "ready")
		}
	}
	for _, acc := range p.reusable {
		if acc != nil {
			appendRow(acc.JWT, acc.Quota, "reusable")
		}
	}
	for acc, originalJWT := range p.borrowed {
		if acc != nil {
			appendRow(originalJWT, acc.Quota, "borrowed")
		}
	}
	return rows
}
```

- [ ] **Step 5: 运行单测并提交**

Run:

```powershell
& 'C:\Program Files\Go\bin\go.exe' test ./... -run TestSimplePoolAdminQuotaRowsPreserveBucketsAndDedupe -count=1
```

Expected:

- PASS

Commit:

```powershell
git add admin_quota.go app.go pool.go pool_test.go
git commit -m "feat: add read-only admin quota pool snapshot"
```

---

### Task 2: 增加 snapshot/probe 两个只读 admin 接口

**Files:**
- Modify: `admin_quota.go`
- Modify: `app.go`
- Modify: `server.go`
- Modify: `server_test.go`

- [ ] **Step 1: 先写接口失败测试，锁定 summary 统计与 probe 只读边界**

Add to `server_test.go`:

```go
type fakePool struct {
	acquiredAccount *Account
	acquiredCost    int
	released        *Account
	status          PoolStatus
	exported        []ExportedAccount
	adminRows       []AdminQuotaRow
	fillTask        FillTaskSnapshot
	pruneResult     PruneSummary
	importResult    ImportPoolResult
	imported        []*Account
	fillCounts      []int
	pruneCalls      int
}

func (f *fakePool) AdminQuotaRows() []AdminQuotaRow {
	return append([]AdminQuotaRow(nil), f.adminRows...)
}

func (f *fakePool) Prune() PruneSummary {
	f.pruneCalls++
	return f.pruneResult
}
```

```go
func TestAdminQuotaSnapshotEndpointReturnsSummaryAndRows(t *testing.T) {
	t.Helper()

	pool, _, handler := newTestHandler()
	pool.adminRows = []AdminQuotaRow{
		{JWT: "jwt-healthy", Quota: 16, Status: "healthy", PoolBucket: "ready"},
		{JWT: "jwt-low", Quota: 7, Status: "low", PoolBucket: "reusable"},
		{JWT: "jwt-near", Quota: 3, Status: "near-empty", PoolBucket: "borrowed"},
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/quota/snapshot", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"total_count":3`) {
		t.Fatalf("expected total_count=3, got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"total_quota":26`) {
		t.Fatalf("expected total_quota=26, got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"low_quota_count":2`) {
		t.Fatalf("expected low_quota_count=2, got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"near_empty_count":1`) {
		t.Fatalf("expected near_empty_count=1, got %s", rec.Body.String())
	}
}
```

```go
func TestAdminQuotaProbeEndpointIsReadOnlyAndReturnsRowResults(t *testing.T) {
	t.Helper()

	pool, backend, handler := newTestHandler()
	backend.quotaByJWT = map[string]int{
		"jwt-a": 12,
		"jwt-b": 4,
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/quota/probe", strings.NewReader(`{"jwts":["jwt-a","jwt-b"]}`))
	req.Header.Set("Authorization", "Bearer admin-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(backend.getCountCalls) != 2 {
		t.Fatalf("expected 2 quota calls, got %v", backend.getCountCalls)
	}
	if pool.pruneCalls != 0 {
		t.Fatalf("probe must not call prune, got %d", pool.pruneCalls)
	}
	if len(pool.fillCounts) != 0 {
		t.Fatalf("probe must not call fill, got %v", pool.fillCounts)
	}
	if len(pool.imported) != 0 {
		t.Fatalf("probe must not import, got %+v", pool.imported)
	}
	if !strings.Contains(rec.Body.String(), `"status":"healthy"`) || !strings.Contains(rec.Body.String(), `"status":"near-empty"`) {
		t.Fatalf("expected derived statuses in probe response, got %s", rec.Body.String())
	}
}
```

- [ ] **Step 2: 运行测试，确认路由和应用层逻辑当前尚未存在**

Run:

```powershell
& 'C:\Program Files\Go\bin\go.exe' test ./... -run 'TestAdminQuota(SnapshotEndpointReturnsSummaryAndRows|ProbeEndpointIsReadOnlyAndReturnsRowResults)$' -count=1
```

Expected:

- FAIL，提示缺少 `AdminQuotaRows` / `/v1/admin/quota/*` 路由或 handler

- [ ] **Step 3: 在 `admin_quota.go` 增加 `App` 聚合逻辑，保持只读**

```go
func (a *App) AdminQuotaSnapshot() AdminQuotaSnapshot {
	rows := append([]AdminQuotaRow(nil), a.pool.AdminQuotaRows()...)
	sort.Slice(rows, func(i, j int) bool {
		order := map[string]int{"near-empty": 0, "low": 1, "healthy": 2, "probe-error": 3}
		if order[rows[i].Status] != order[rows[j].Status] {
			return order[rows[i].Status] < order[rows[j].Status]
		}
		if rows[i].Quota != rows[j].Quota {
			return rows[i].Quota < rows[j].Quota
		}
		return rows[i].JWT < rows[j].JWT
	})

	summary := AdminQuotaSummary{}
	for _, row := range rows {
		summary.TotalCount++
		summary.TotalQuota += row.Quota
		if row.Quota >= 2 && row.Quota < 10 {
			summary.LowQuotaCount++
		}
		if row.Quota < 5 {
			summary.NearEmptyCount++
		}
	}
	return AdminQuotaSnapshot{Summary: summary, Rows: rows}
}

func (a *App) ProbeQuota(jwts []string) AdminQuotaProbeResponse {
	checkedAt := a.now().UTC()
	results := make([]AdminQuotaProbeItem, 0, len(jwts))
	seen := make(map[string]struct{}, len(jwts))
	for _, raw := range jwts {
		jwt := strings.TrimSpace(raw)
		if jwt == "" {
			continue
		}
		if _, ok := seen[jwt]; ok {
			continue
		}
		seen[jwt] = struct{}{}

		quota, err := a.backend.GetCount(jwt)
		if err != nil {
			results = append(results, AdminQuotaProbeItem{JWT: jwt, OK: false, Error: err.Error()})
			continue
		}
		results = append(results, AdminQuotaProbeItem{
			JWT: jwt,
			Quota: quota,
			Status: deriveAdminQuotaStatus(quota),
			OK: true,
		})
	}
	return AdminQuotaProbeResponse{CheckedAt: checkedAt, Results: results}
}
```

- [ ] **Step 4: 在 `server.go` 注册并实现两个只读 handler**

Register routes:

```go
mux.Handle("/v1/admin/quota/snapshot", adminAuth.RequireAPI(http.HandlerFunc(app.HandleAdminQuotaSnapshot)))
mux.Handle("/v1/admin/quota/probe", adminAuth.RequireAPI(http.HandlerFunc(app.HandleAdminQuotaProbe)))
```

Add handlers:

```go
func (a *App) HandleAdminQuotaSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, a.AdminQuotaSnapshot())
}

func (a *App) HandleAdminQuotaProbe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		JWTs []string `json:"jwts"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "Request body must be valid JSON", "invalid_request_error")
		return
	}
	writeJSON(w, http.StatusOK, a.ProbeQuota(body.JWTs))
}
```

- [ ] **Step 5: 运行测试并提交**

Run:

```powershell
& 'C:\Program Files\Go\bin\go.exe' test ./... -run 'TestAdminQuota(SnapshotEndpointReturnsSummaryAndRows|ProbeEndpointIsReadOnlyAndReturnsRowResults)$' -count=1
```

Expected:

- PASS

Commit:

```powershell
git add admin_quota.go app.go server.go server_test.go
git commit -m "feat: add read-only admin quota endpoints"
```

---

### Task 3: 精简后台首屏结构并把次级模块折叠起来

**Files:**
- Modify: `web/admin/index.html`
- Modify: `web/admin/styles.css`
- Test: `server_test.go`

- [ ] **Step 1: 先写页面结构失败测试，锁定去噪和折叠布局契约**

Add to `server_test.go`:

```go
func TestHandleAdminDashboardPageServesQuotaFirstLayout(t *testing.T) {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	rec := httptest.NewRecorder()
	HandleAdminDashboardPage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, ">LIVE<") {
		t.Fatalf("expected LIVE decoration removed, got %s", body)
	}
	if !strings.Contains(body, `id="quotaOverviewSection"`) {
		t.Fatalf("expected quota overview section, got %s", body)
	}
	if !strings.Contains(body, `id="quotaTableSection"`) {
		t.Fatalf("expected quota table section, got %s", body)
	}
	if !strings.Contains(body, `<details class="surface fold-panel"`) {
		t.Fatalf("expected collapsed secondary sections, got %s", body)
	}
}
```

- [ ] **Step 2: 跑测试确认当前 HTML 还是旧结构**

Run:

```powershell
& 'C:\Program Files\Go\bin\go.exe' test ./... -run TestHandleAdminDashboardPageServesQuotaFirstLayout -count=1
```

Expected:

- FAIL，因为旧页面还包含 `LIVE` 且没有新的 `quotaOverviewSection` / `quotaTableSection`

- [ ] **Step 3: 重写 `index.html` 的首屏结构，保留风格但减少噪音**

Replace the main dashboard body with a layout like:

```html
<body class="admin-body">
  <div class="page-decor" aria-hidden="true">
    <span class="float-tag float-tag-red">POOL</span>
    <span class="float-tag float-tag-yellow">ADMIN</span>
  </div>

  <div class="admin-shell">
    <header class="surface topbar">
      <div class="brand-rack">
        <p class="eyebrow">holo-image-api</p>
        <h1>后台控制台</h1>
        <p class="subtle" id="topline">首屏只保留额度总览与明细表。</p>
      </div>
      <div class="topbar-side">
        <span class="badge status-badge" id="refreshState">未刷新</span>
        <div class="topbar-actions">
          <button type="button" class="button secondary" id="refreshBtn">刷新</button>
          <button type="button" class="button ghost" id="logoutBtn">退出</button>
        </div>
      </div>
    </header>

    <main class="admin-main admin-main-tight">
      <section class="surface summary-surface" id="quotaOverviewSection">
        <div class="section-head compact-head">
          <div>
            <p class="eyebrow">QUOTA</p>
            <h2>额度总览</h2>
          </div>
          <p class="section-note">总览基于缓存快照</p>
        </div>
        <div class="overview-strip" id="quotaOverviewGrid"></div>
      </section>

      <section class="surface table-surface" id="quotaTableSection">
        <div class="section-head compact-head">
          <div>
            <p class="eyebrow">DETAIL</p>
            <h2>号池明细</h2>
          </div>
          <p class="section-note" id="probeState">默认展示缓存额度</p>
        </div>
        <div id="quotaTableTools"></div>
        <div id="quotaTableWrap"></div>
      </section>

      <details class="surface fold-panel" id="modelsFold"><summary>模型支持</summary><div id="modelsSection"></div></details>
      <details class="surface fold-panel" id="migrationFold"><summary>迁移结果</summary><div id="migrationSection"></div></details>
      <details class="surface fold-panel" id="dangerFold"><summary>危险操作</summary><div id="dangerSection"></div></details>
      <details class="surface fold-panel" id="logFold"><summary>日志</summary><div id="logSection"></div></details>
    </main>
  </div>

  <script src="/admin/assets/app.js"></script>
</body>
```

- [ ] **Step 4: 在 `styles.css` 增加更克制的首屏样式与折叠区样式**

Add/update styles like:

```css
.admin-main-tight {
  display: grid;
  gap: 18px;
}

.overview-strip {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 14px;
}

.table-surface {
  background: linear-gradient(180deg, rgba(255, 217, 61, 0.14) 0%, #ffffff 100%);
}

.fold-panel {
  padding: 0;
  overflow: hidden;
}

.fold-panel > summary {
  cursor: pointer;
  list-style: none;
  padding: 20px 24px;
  font-weight: 900;
  text-transform: uppercase;
  letter-spacing: 0.12em;
}

.fold-panel > summary::-webkit-details-marker {
  display: none;
}

.fold-panel[open] > div {
  padding: 0 24px 24px;
}

@media (max-width: 960px) {
  .overview-strip {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
```

- [ ] **Step 5: 运行测试并提交**

Run:

```powershell
& 'C:\Program Files\Go\bin\go.exe' test ./... -run TestHandleAdminDashboardPageServesQuotaFirstLayout -count=1
```

Expected:

- PASS

Commit:

```powershell
git add web/admin/index.html web/admin/styles.css server_test.go
git commit -m "feat: simplify admin dashboard layout"
```

---

### Task 4: 把前端切到 snapshot/probe 驱动的额度看板

**Files:**
- Modify: `web/admin/app.js`
- Modify: `server_test.go`

- [ ] **Step 1: 先写静态资产失败测试，锁定新前端必须依赖 snapshot/probe 端点**

Add to `server_test.go`:

```go
func TestAdminDashboardAssetContainsQuotaEndpoints(t *testing.T) {
	t.Helper()

	_, _, handler := newTestHandler()
	req := httptest.NewRequest(http.MethodGet, "/admin/assets/app.js", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/v1/admin/quota/snapshot") {
		t.Fatalf("expected snapshot endpoint usage, got %s", body)
	}
	if !strings.Contains(body, "/v1/admin/quota/probe") {
		t.Fatalf("expected probe endpoint usage, got %s", body)
	}
	if !strings.Contains(body, "toggleJwtVisibility") {
		t.Fatalf("expected JWT expand behavior, got %s", body)
	}
}
```

- [ ] **Step 2: 跑测试确认旧 `app.js` 还没有这些能力**

Run:

```powershell
& 'C:\Program Files\Go\bin\go.exe' test ./... -run TestAdminDashboardAssetContainsQuotaEndpoints -count=1
```

Expected:

- FAIL，因为旧脚本只吃 `/v1/admin/pool`、没有 probe/jwt 展开逻辑

- [ ] **Step 3: 重写 `web/admin/app.js` 的状态和渲染逻辑**

Use a state shape like:

```js
const state = {
  session: null,
  snapshot: null,
  probeOverlay: new Map(),
  filters: {
    status: 'all',
    bucket: 'all',
    query: '',
    sort: 'status-asc-quota-asc',
  },
  expandedJWTs: new Set(),
  logs: [],
  refreshing: false,
  probing: false,
  bannerError: '',
};
```

Key fetch path:

```js
async function refreshAll() {
  const session = await ensureSession();
  if (!session) return;

  const [meta, snapshot, catalog, migration] = await Promise.all([
    fetchJSON('/v1/admin/meta', { method: 'GET' }),
    fetchJSON('/v1/admin/quota/snapshot', { method: 'GET' }),
    fetchJSON('/v1/admin/catalog', { method: 'GET' }),
    fetchJSON('/v1/admin/migration/status', { method: 'GET' }),
  ]);

  state.session = session;
  state.snapshot = snapshot;
  state.probeOverlay.clear();
  renderSnapshot({ meta, snapshot, catalog, migration });
}
```

Key row overlay behavior:

```js
function effectiveRow(row) {
  const overlay = state.probeOverlay.get(row.jwt);
  if (!overlay) return { ...row, probeState: 'cached' };
  if (!overlay.ok) {
    return { ...row, probeState: 'error', probeError: overlay.error };
  }
  return {
    ...row,
    quota: overlay.quota,
    status: overlay.status,
    last_checked_at: overlay.checked_at,
    probeState: 'live',
  };
}
```

Probe action:

```js
async function runProbeCurrentFilter() {
  const rows = getFilteredRows();
  const jwts = rows.map((row) => row.jwt);
  const payload = await fetchJSON('/v1/admin/quota/probe', {
    method: 'POST',
    body: JSON.stringify({ jwts }),
  });

  for (const item of payload.results || []) {
    state.probeOverlay.set(item.jwt, {
      ...item,
      checked_at: payload.checked_at,
    });
  }
  renderQuotaTable();
}
```

JWT toggle helper:

```js
function toggleJwtVisibility(jwt) {
  if (state.expandedJWTs.has(jwt)) {
    state.expandedJWTs.delete(jwt);
  } else {
    state.expandedJWTs.add(jwt);
  }
  renderQuotaTable();
}
```

- [ ] **Step 4: 把总览、工具栏、表格渲染出来，并保留总览基于缓存的口径**

Render snippets:

```js
function renderQuotaOverview() {
  const summary = state.snapshot?.summary || { total_count: 0, total_quota: 0, low_quota_count: 0, near_empty_count: 0 };
  quotaOverviewGrid.innerHTML = [
    metric('总号数', formatCount(summary.total_count)),
    metric('总剩余额度', formatCount(summary.total_quota)),
    metric('低余额号', formatCount(summary.low_quota_count), '2 <= quota < 10'),
    metric('接近没额度号', formatCount(summary.near_empty_count), 'quota < 5'),
  ].join('');
}
```

```js
function renderQuotaTableTools() {
  quotaTableTools.innerHTML = `
    <div class="table-toolbar">
      <select id="statusFilter">...</select>
      <select id="bucketFilter">...</select>
      <input id="queryInput" type="search" placeholder="搜索 JWT" />
      <select id="sortSelect">...</select>
      <button type="button" class="button primary" id="probeBtn">实时核验当前筛选</button>
    </div>
  `;
}
```

```js
function maskJWT(jwt) {
  if (!jwt || jwt.length <= 16) return jwt;
  return `${jwt.slice(0, 8)}...${jwt.slice(-6)}`;
}
```

- [ ] **Step 5: 运行测试并提交**

Run:

```powershell
& 'C:\Program Files\Go\bin\go.exe' test ./... -run TestAdminDashboardAssetContainsQuotaEndpoints -count=1
```

Expected:

- PASS

Commit:

```powershell
git add web/admin/app.js server_test.go
git commit -m "feat: add read-only admin quota dashboard client"
```

---

### Task 5: 完整验证只读边界与运行态口径

**Files:**
- Modify: `server_test.go` (if any small assertion gaps remain)
- No new product files unless verification reveals a real issue

- [ ] **Step 1: 运行完整 Go 测试套件**

Run:

```powershell
& 'C:\Program Files\Go\bin\go.exe' test ./... -count=1
```

Expected:

- 所有测试 PASS

- [ ] **Step 2: 本地启动服务，进行浏览器只读 smoke**

Run:

```powershell
$env:PORT='18080'
$env:MAIL_API_BASE_URL='https://apimail.wolfholo.top'
$env:MAIL_DOMAIN='wolfholo.me'
$env:MAIL_ADMIN_TOKEN='mail-token'
$env:API_BEARER_TOKEN='api-token'
$env:ADMIN_TOKEN='admin-token'
& 'C:\Program Files\Go\bin\go.exe' run .
```

Manual check:

- `/admin` 首屏只看到总览 + 表格
- `LIVE` 装饰消失
- 模型/迁移/危险操作/日志默认折叠
- JWT 默认脱敏，可展开
- 点“实时核验当前筛选”后只刷新当前筛选行的展示

- [ ] **Step 3: 对 live 行为做只读边界核验，确认 probe 不改变池状态**

Before probe:

```powershell
$headers = @{ Authorization = 'Bearer sk-878030051Xsz...' }
$beforePool = Invoke-RestMethod -Uri 'https://holo-image-api.zeabur.app/v1/admin/pool' -Headers $headers -Method Get
$beforeExport = Invoke-RestMethod -Uri 'https://holo-image-api.zeabur.app/v1/admin/pool/export' -Headers $headers -Method Get
```

Trigger UI probe only for a narrow filter result, then run:

```powershell
$afterPool = Invoke-RestMethod -Uri 'https://holo-image-api.zeabur.app/v1/admin/pool' -Headers $headers -Method Get
$afterExport = Invoke-RestMethod -Uri 'https://holo-image-api.zeabur.app/v1/admin/pool/export' -Headers $headers -Method Get

if ($beforePool.total_count -ne $afterPool.total_count) { throw 'total_count changed unexpectedly' }
if ($beforePool.persisted_count -ne $afterPool.persisted_count) { throw 'persisted_count changed unexpectedly' }
if ($beforePool.prune_removed -ne $afterPool.prune_removed) { throw 'prune_removed changed unexpectedly' }
if (($beforeExport.accounts | Measure-Object).Count -ne ($afterExport.accounts | Measure-Object).Count) { throw 'exported account count changed unexpectedly' }
```

Expected:

- 所有断言通过
- 只读 probe 不触发任何池管理性变化

- [ ] **Step 4: 提交最终实现分支结果**

```powershell
git add admin_quota.go app.go pool.go pool_test.go server.go server_test.go web/admin/index.html web/admin/app.js web/admin/styles.css
git commit -m "feat: add read-only admin quota dashboard"
```

- [ ] **Step 5: 推送分支并准备合并/部署**

```powershell
git push origin HEAD
```

Expected:

- 远端分支更新成功

---

## Self-Review Checklist

### Spec coverage

- 只读 snapshot 接口：Task 2
- 只读 probe 接口：Task 2
- 首屏总览 + 明细表：Task 3 + Task 4
- 默认折叠次级模块：Task 3
- JWT 默认脱敏、点击展开：Task 4
- 总览基于缓存快照、probe 只影响行级显示：Task 4
- probe 不写号池：Task 2 + Task 5
- 去掉 `LIVE` 并收敛风格：Task 3

### Placeholder scan

- 无 `TODO` / `TBD` / “类似 Task N” 占位语
- 所有代码步骤都有明确代码片段
- 所有验证步骤都有明确命令

### Type consistency

- `AdminQuotaRow`：池层只读行对象
- `AdminQuotaSnapshot`：快照响应
- `AdminQuotaProbeResponse`：probe 响应
- `deriveAdminQuotaStatus()`：唯一状态派生函数

### Explicit scope reminder

实现过程中如果发现需要改动 `persistLocked()`、`Prune()`、`StartFillTask()` 或持久化格式，说明方案越界，必须停下重新审视 spec，而不是顺手改掉。
