# Holo Image Admin Migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把旧实例现有池子迁入新实例，为新实例增加内置 Admin WebUI，并最终让 `https://holo-image-api.zeabur.app` 切到新实例后退役老实例。

**Architecture:** 保持单一 Go 服务作为唯一业务承载体，在服务内增加 Admin WebUI、旧池导出接口、服务端迁移接口和实例元信息接口。新实例继续作为持久化主实例，旧实例仅短期提供 `/v1/admin/pool/export` 以支持迁移，完成后切换域名并退役。

**Tech Stack:** Go 1.25、标准库 `net/http` / `embed` / `encoding/json`、现有 `SimplePool`、Zeabur CLI、Zeabur generated/custom domain。

---

## File Structure

### Existing files to modify

- `config.go`
  - 增加 Admin WebUI/迁移相关配置项，例如实例名、最终域名、旧实例导出地址。
- `main.go`
  - 输出实例信息；保持启动路径对新配置兼容。
- `app.go`
  - 挂接迁移状态聚合、meta 逻辑和服务端迁移入口。
- `pool.go`
  - 暴露可安全导出的池快照；保持持久化逻辑与导出逻辑一致。
- `server.go`
  - 增加 `/admin`、`/admin/assets/*`、`/v1/admin/meta`、`/v1/admin/migrate-from-old`、`/v1/admin/migration/status`、`/v1/admin/pool/export`、`/v1/admin/retire-old`。
- `pool_test.go`
  - 为导出快照补测试。
- `server_test.go`
  - 为新 admin API、WebUI 路由、迁移逻辑、配置读取补测试。

### New files to create

- `admin_meta.go`
  - 定义实例元信息、迁移状态、退役状态结构和应用层辅助方法。
- `legacy_pool_client.go`
  - 负责从旧实例拉取 `/v1/admin/pool/export`。
- `admin_ui.go`
  - 通过 `embed.FS` 提供 `/admin` 和静态资源。
- `web/admin/index.html`
  - Admin WebUI 主页面。
- `web/admin/app.js`
  - 前端数据拉取、鉴权、按钮动作、渲染逻辑。
- `web/admin/styles.css`
  - 深色后台样式。

## Environment / Runtime Truth

### Current Zeabur targets

- **旧实例 service id:** `69c5591582fb34707a9f2a63`
- **新实例 service id:** `69c90776a972bb88a76369ca`
- **环境 id:** `69c558d376bc68ba374cc5cf`
- **项目 id:** `69c558d3e11ead6d2df0178e`
- **旧实例域名:** `https://holo-image-api.zeabur.app`
- **新实例域名:** `https://holo-image-api-eners.zeabur.app`

### New config keys to add

- `INSTANCE_NAME`
  - 例：`holo-image-api-eners`
- `PUBLIC_BASE_URL`
  - 当前实例的对外地址
- `PRIMARY_PUBLIC_BASE_URL`
  - 目标主域名，固定为 `https://holo-image-api.zeabur.app`
- `LEGACY_POOL_EXPORT_BASE_URL`
  - 旧实例导出地址，例：`https://holo-image-api.zeabur.app`

---

### Task 1: 增加池导出能力与实例元信息骨架

**Files:**
- Create: `admin_meta.go`
- Modify: `config.go`
- Modify: `pool.go`
- Test: `pool_test.go`
- Test: `server_test.go`

- [ ] **Step 1: 写配置读取失败/成功测试，锁定新增环境变量**

```go
func TestLoadConfigReadsAdminMigrationFieldsFromEnv(t *testing.T) {
	t.Helper()

	cfg, err := LoadConfig([]string{}, func(key string) string {
		values := map[string]string{
			"PORT":                        "18080",
			"MAIL_API_BASE_URL":           "https://mail.example.com",
			"MAIL_DOMAIN":                 "example.com",
			"MAIL_ADMIN_TOKEN":            "mail-token",
			"API_BEARER_TOKEN":            "api-token",
			"ADMIN_TOKEN":                 "admin-token",
			"INSTANCE_NAME":               "holo-image-api-eners",
			"PUBLIC_BASE_URL":             "https://holo-image-api-eners.zeabur.app",
			"PRIMARY_PUBLIC_BASE_URL":     "https://holo-image-api.zeabur.app",
			"LEGACY_POOL_EXPORT_BASE_URL": "https://holo-image-api.zeabur.app",
		}
		return values[key]
	})
	if err != nil {
		t.Fatalf("expected config to load, got %v", err)
	}
	if cfg.InstanceName != "holo-image-api-eners" {
		t.Fatalf("expected instance name, got %+v", cfg)
	}
	if cfg.LegacyPoolExportBaseURL != "https://holo-image-api.zeabur.app" {
		t.Fatalf("expected legacy export URL, got %+v", cfg)
	}
}
```

- [ ] **Step 2: 运行测试，确认当前实现会失败**

Run:

```powershell
& 'C:\Program Files\Go\bin\go.exe' test ./... -count=1
```

Expected:

- `Config` 缺少 `InstanceName` / `PublicBaseURL` / `PrimaryPublicBaseURL` / `LegacyPoolExportBaseURL`
- 新测试失败

- [ ] **Step 3: 在 `config.go` 增加配置字段与 env 解析**

```go
type Config struct {
	PoolSize                 int
	PoolWorkerCount          int
	PoolLowWatermark         int
	PoolStorePath            string
	PoolPruneIntervalSeconds int
	PoolRegistrationInterval int
	PoolFailureBackoff       int
	PoolFailureBackoffMax    int
	Port                     int
	MailAPIBaseURL           string
	MailDomain               string
	MailAdminToken           string
	APIBearerToken           string
	AdminToken               string
	InstanceName             string
	PublicBaseURL            string
	PrimaryPublicBaseURL     string
	LegacyPoolExportBaseURL  string
}
```

```go
instanceNameFlag := fs.String("instance-name", "", "实例标识")
publicBaseURLFlag := fs.String("public-base-url", "", "当前实例对外地址")
primaryPublicBaseURLFlag := fs.String("primary-public-base-url", "", "最终主域名")
legacyPoolExportBaseURLFlag := fs.String("legacy-pool-export-base-url", "", "旧实例池导出地址")
```

```go
if value := strings.TrimSpace(getenv("INSTANCE_NAME")); value != "" {
	cfg.InstanceName = value
}
if value := strings.TrimSpace(getenv("PUBLIC_BASE_URL")); value != "" {
	cfg.PublicBaseURL = value
}
if value := strings.TrimSpace(getenv("PRIMARY_PUBLIC_BASE_URL")); value != "" {
	cfg.PrimaryPublicBaseURL = value
}
if value := strings.TrimSpace(getenv("LEGACY_POOL_EXPORT_BASE_URL")); value != "" {
	cfg.LegacyPoolExportBaseURL = value
}
```

- [ ] **Step 4: 为 `SimplePool` 增加安全导出快照与元信息结构**

Create `admin_meta.go` with:

```go
package main

import "time"

type ExportedAccount struct {
	JWT   string `json:"jwt"`
	Quota int    `json:"quota"`
}

type AdminMeta struct {
	InstanceName         string     `json:"instance_name"`
	PublicBaseURL        string     `json:"public_base_url"`
	PrimaryPublicBaseURL string     `json:"primary_public_base_url"`
	IsPrimaryTarget      bool       `json:"is_primary_target"`
	Version              string     `json:"version"`
	LastMigrationAt      *time.Time `json:"last_migration_at,omitempty"`
}
```

Modify `pool.go` interface and implementation:

```go
type PoolManager interface {
	Acquire(cost int) *Account
	Release(acc *Account)
	Status() PoolStatus
	StartFillTask(count int) FillTaskSnapshot
	Prune() PruneSummary
	ImportAccounts(accounts []*Account) ImportPoolResult
	ExportAccounts() []ExportedAccount
}
```

```go
func (p *SimplePool) ExportAccounts() []ExportedAccount {
	p.mu.Lock()
	defer p.mu.Unlock()

	accounts := p.snapshotAccountsLocked()
	exported := make([]ExportedAccount, 0, len(accounts))
	for _, acc := range accounts {
		if acc == nil {
			continue
		}
		exported = append(exported, ExportedAccount{
			JWT:   strings.TrimSpace(acc.JWT),
			Quota: acc.Quota,
		})
	}
	return exported
}
```

- [ ] **Step 5: 为导出快照补测试**

Add to `pool_test.go`:

```go
func TestSimplePoolExportAccountsReturnsDeduplicatedSnapshot(t *testing.T) {
	t.Helper()

	store := &memoryAccountStore{
		accounts: []*Account{
			{JWT: "jwt-ready", Quota: 65},
		},
	}
	pool := NewSimplePoolWithOptions(10, 0, func() (string, error) {
		return "", fmt.Errorf("no registration")
	}, func(_ string) int {
		return 65
	}, PoolOptions{Store: store})

	pool.ready = append(pool.ready, &Account{JWT: "jwt-ready-2", Quota: 42})
	exported := pool.ExportAccounts()
	if len(exported) != 2 {
		t.Fatalf("expected 2 exported accounts, got %+v", exported)
	}
}
```

- [ ] **Step 6: 运行测试，确认通过**

Run:

```powershell
& 'C:\Program Files\Go\bin\go.exe' test ./... -count=1
```

Expected:

- 所有测试通过

- [ ] **Step 7: Commit**

```bash
git add config.go admin_meta.go pool.go pool_test.go server_test.go
git commit -m "feat: add admin migration config and pool export"
```

---

### Task 2: 增加旧实例导出接口、实例 meta 接口、迁移状态接口

**Files:**
- Create: `legacy_pool_client.go`
- Modify: `app.go`
- Modify: `server.go`
- Test: `server_test.go`

- [ ] **Step 1: 写 failing tests，锁定 admin 新接口**

Add to `server_test.go`:

```go
func TestAdminPoolExportRequiresAdminTokenAndReturnsSnapshot(t *testing.T) {
	t.Helper()

	pool, _, handler := newTestHandler()
	pool.exported = []ExportedAccount{
		{JWT: "jwt-1", Quota: 65},
		{JWT: "jwt-2", Quota: 12},
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/pool/export", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"jwt":"jwt-1"`) {
		t.Fatalf("expected exported jwt in response, got %s", rec.Body.String())
	}
}
```

```go
func TestAdminMetaEndpointReturnsInstanceInformation(t *testing.T) {
	t.Helper()

	_, _, handler := newTestHandler()
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/meta", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"instance_name":"test-instance"`) {
		t.Fatalf("expected instance name in response, got %s", rec.Body.String())
	}
}
```

- [ ] **Step 2: 运行测试，确认失败**

Run:

```powershell
& 'C:\Program Files\Go\bin\go.exe' test ./... -count=1
```

Expected:

- `fakePool` 缺少 `ExportAccounts`
- `/v1/admin/pool/export`、`/v1/admin/meta` 路由不存在

- [ ] **Step 3: 实现旧池导出客户端与迁移状态结构**

Create `legacy_pool_client.go`:

```go
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type LegacyPoolClient struct {
	BaseURL    string
	AdminToken string
	Client     *http.Client
}

type LegacyPoolExportResponse struct {
	Accounts []ExportedAccount `json:"accounts"`
}

func (c *LegacyPoolClient) ExportAccounts() ([]ExportedAccount, error) {
	if strings.TrimSpace(c.BaseURL) == "" {
		return nil, fmt.Errorf("legacy pool export base url is empty")
	}
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(c.BaseURL, "/")+"/v1/admin/pool/export", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.AdminToken))
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("legacy export returned status %d", resp.StatusCode)
	}
	var payload LegacyPoolExportResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return payload.Accounts, nil
}
```

- [ ] **Step 4: 在 `app.go` 增加迁移状态与 meta 读取**

Add to `app.go`:

```go
type MigrationStatus struct {
	Requested  int        `json:"requested"`
	Imported   int        `json:"imported"`
	Duplicates int        `json:"duplicates"`
	Rejected   int        `json:"rejected"`
	Overflow   int        `json:"overflow"`
	TotalCount int        `json:"total_count"`
	LastError  string     `json:"last_error,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}
```

```go
type App struct {
	pool             PoolManager
	backend          ImageBackend
	now              func() time.Time
	cfg              Config
	legacyPoolClient *LegacyPoolClient
	migrationMu      sync.RWMutex
	migrationStatus  MigrationStatus
}
```

And helper methods:

```go
func (a *App) AdminMeta() AdminMeta
func (a *App) CurrentMigrationStatus() MigrationStatus
func (a *App) MigrateFromLegacy() MigrationStatus
```

- [ ] **Step 5: 在 `server.go` 挂接口**

Register:

```go
mux.Handle("/v1/admin/meta", BearerAuthMiddleware(cfg.AdminToken)(http.HandlerFunc(app.HandleAdminMeta)))
mux.Handle("/v1/admin/migration/status", BearerAuthMiddleware(cfg.AdminToken)(http.HandlerFunc(app.HandleAdminMigrationStatus)))
mux.Handle("/v1/admin/migrate-from-old", BearerAuthMiddleware(cfg.AdminToken)(http.HandlerFunc(app.HandleAdminMigrateFromOld)))
mux.Handle("/v1/admin/pool/export", BearerAuthMiddleware(cfg.AdminToken)(http.HandlerFunc(app.HandleAdminPoolExport)))
```

Implement:

```go
func (a *App) HandleAdminPoolExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"accounts": a.pool.ExportAccounts(),
	})
}
```

```go
func (a *App) HandleAdminMeta(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, a.AdminMeta())
}
```

```go
func (a *App) HandleAdminMigrationStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, a.CurrentMigrationStatus())
}
```

```go
func (a *App) HandleAdminMigrateFromOld(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	status := a.MigrateFromLegacy()
	if status.LastError != "" {
		writeOpenAIError(w, http.StatusBadGateway, status.LastError, "migration_error")
		return
	}
	writeJSON(w, http.StatusOK, status)
}
```

- [ ] **Step 6: 扩展 fakePool / newTestHandler 以支持新接口**

Add to `server_test.go` test doubles:

```go
type fakePool struct {
	status   PoolStatus
	exported []ExportedAccount
	imported []*Account
	// existing fields...
}

func (f *fakePool) ExportAccounts() []ExportedAccount {
	return append([]ExportedAccount(nil), f.exported...)
}
```

Update `newTestHandler()` config:

```go
handler := NewServerHandler(Config{
	APIBearerToken:          "api-token",
	AdminToken:              "admin-token",
	InstanceName:            "test-instance",
	PublicBaseURL:           "https://new.example.com",
	PrimaryPublicBaseURL:    "https://primary.example.com",
	LegacyPoolExportBaseURL: "https://old.example.com",
}, app)
```

- [ ] **Step 7: 运行测试，确认通过**

Run:

```powershell
& 'C:\Program Files\Go\bin\go.exe' test ./... -count=1
```

Expected:

- 所有新增 admin API 测试通过

- [ ] **Step 8: Commit**

```bash
git add app.go server.go legacy_pool_client.go server_test.go
git commit -m "feat: add admin export meta and migration endpoints"
```

---

### Task 3: 增加内置 Admin WebUI

**Files:**
- Create: `admin_ui.go`
- Create: `web/admin/index.html`
- Create: `web/admin/app.js`
- Create: `web/admin/styles.css`
- Modify: `server.go`
- Test: `server_test.go`

- [ ] **Step 1: 写 WebUI 路由 failing tests**

Add to `server_test.go`:

```go
func TestAdminUIRoutesServeHTMLAndAssets(t *testing.T) {
	t.Helper()

	_, _, handler := newTestHandler()

	pageReq := httptest.NewRequest(http.MethodGet, "/admin", nil)
	pageRec := httptest.NewRecorder()
	handler.ServeHTTP(pageRec, pageReq)
	if pageRec.Code != http.StatusOK {
		t.Fatalf("expected admin page 200, got %d body=%s", pageRec.Code, pageRec.Body.String())
	}
	if !strings.Contains(pageRec.Body.String(), "Holo Image Admin") {
		t.Fatalf("expected admin shell html, got %s", pageRec.Body.String())
	}

	assetReq := httptest.NewRequest(http.MethodGet, "/admin/assets/app.js", nil)
	assetRec := httptest.NewRecorder()
	handler.ServeHTTP(assetRec, assetReq)
	if assetRec.Code != http.StatusOK {
		t.Fatalf("expected admin asset 200, got %d body=%s", assetRec.Code, assetRec.Body.String())
	}
}
```

- [ ] **Step 2: 运行测试，确认失败**

Run:

```powershell
& 'C:\Program Files\Go\bin\go.exe' test ./... -count=1
```

Expected:

- `/admin` 与 `/admin/assets/app.js` 返回 404

- [ ] **Step 3: 用 `embed` 实现静态资源服务**

Create `admin_ui.go`:

```go
package main

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed web/admin/*
var adminAssets embed.FS

func NewAdminUIHandler() (http.Handler, error) {
	sub, err := fs.Sub(adminAssets, "web/admin")
	if err != nil {
		return nil, err
	}
	return http.FileServer(http.FS(sub)), nil
}

func HandleAdminIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/admin" {
		http.NotFound(w, r)
		return
	}
	if !strings.EqualFold(r.Method, http.MethodGet) {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	http.ServeFileFS(w, r, adminAssets, "web/admin/index.html")
}
```

- [ ] **Step 4: 注册 `/admin` 与静态资源路由**

Modify `server.go`:

```go
adminUI, err := NewAdminUIHandler()
if err != nil {
	panic(err)
}
```

```go
mux.HandleFunc("/admin", HandleAdminIndex)
mux.Handle("/admin/assets/", http.StripPrefix("/admin/assets/", adminUI))
```

- [ ] **Step 5: 写第一版页面与脚本**

Create `web/admin/index.html`:

```html
<!DOCTYPE html>
<html lang="zh-CN">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <title>Holo Image Admin</title>
  <link rel="stylesheet" href="/admin/assets/styles.css" />
</head>
<body>
  <div id="app">
    <header class="topbar">
      <div>
        <h1>Holo Image Admin</h1>
        <p class="muted">新实例唯一管理面板</p>
      </div>
      <div class="auth-box">
        <input id="adminToken" type="password" placeholder="输入 admin key" />
        <button id="saveTokenBtn">保存</button>
        <button id="refreshBtn">刷新状态</button>
      </div>
    </header>

    <main class="grid">
      <section class="card" id="overviewCard"></section>
      <section class="card" id="persistenceCard"></section>
      <section class="card" id="migrationCard"></section>
      <section class="card" id="controlsCard"></section>
      <section class="card danger" id="retireCard"></section>
      <section class="card" id="logCard"></section>
    </main>
  </div>
  <script src="/admin/assets/app.js"></script>
</body>
</html>
```

Create `web/admin/app.js`:

```javascript
const tokenInput = document.getElementById('adminToken');
const saveTokenBtn = document.getElementById('saveTokenBtn');
const refreshBtn = document.getElementById('refreshBtn');

const state = {
  token: localStorage.getItem('holo_image_admin_token') || '',
};

tokenInput.value = state.token;

saveTokenBtn.addEventListener('click', () => {
  state.token = tokenInput.value.trim();
  localStorage.setItem('holo_image_admin_token', state.token);
  renderLog('已保存 admin key');
});

refreshBtn.addEventListener('click', refreshAll);

async function api(path, options = {}) {
  const headers = new Headers(options.headers || {});
  headers.set('Authorization', `Bearer ${state.token}`);
  if (!headers.has('Content-Type') && options.body) {
    headers.set('Content-Type', 'application/json');
  }
  const res = await fetch(path, { ...options, headers });
  const text = await res.text();
  let data = {};
  try { data = text ? JSON.parse(text) : {}; } catch { data = { raw: text }; }
  if (!res.ok) throw new Error(data?.error?.message || data?.message || text || `HTTP ${res.status}`);
  return data;
}
```

Create `web/admin/styles.css`:

```css
body {
  margin: 0;
  font-family: Inter, "PingFang SC", "Microsoft YaHei", sans-serif;
  background: #0b1020;
  color: #e6edf3;
}
.topbar { display: flex; justify-content: space-between; gap: 16px; padding: 24px 32px; }
.grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(320px, 1fr)); gap: 16px; padding: 0 32px 32px; }
.card { background: #121a2b; border: 1px solid #23314f; border-radius: 18px; padding: 20px; box-shadow: 0 12px 36px rgba(0,0,0,.28); }
.danger { border-color: #7f1d1d; }
.muted { color: #94a3b8; }
button { cursor: pointer; }
input { background: #0f172a; color: #e6edf3; border: 1px solid #334155; border-radius: 12px; padding: 10px 12px; }
```

- [ ] **Step 6: 把页面接到现有 admin API 上**

Extend `web/admin/app.js` with:

```javascript
async function refreshAll() {
  const [pool, meta, migration] = await Promise.all([
    api('/v1/admin/pool'),
    api('/v1/admin/meta'),
    api('/v1/admin/migration/status'),
  ]);
  renderOverview(pool, meta);
  renderPersistence(pool);
  renderMigration(pool, migration);
  renderControls();
}

async function runPrune() {
  const result = await api('/v1/admin/pool/prune', { method: 'POST' });
  renderLog(`prune 完成：removed=${result.removed}, remaining=${result.remaining}`);
  await refreshAll();
}

async function runFill(count) {
  const result = await api('/v1/admin/pool/fill', {
    method: 'POST',
    body: JSON.stringify({ count }),
  });
  renderLog(`fill 已触发：task=${result.task_id}, requested=${result.requested}`);
  await refreshAll();
}

async function runMigration() {
  const result = await api('/v1/admin/migrate-from-old', { method: 'POST' });
  renderLog(`迁移完成：imported=${result.imported}, duplicates=${result.duplicates}, total=${result.total_count}`);
  await refreshAll();
}
```

- [ ] **Step 7: 运行测试，确认通过**

Run:

```powershell
& 'C:\Program Files\Go\bin\go.exe' test ./... -count=1
```

Expected:

- `/admin`、静态资源、admin API 测试通过

- [ ] **Step 8: Commit**

```bash
git add admin_ui.go web/admin/index.html web/admin/app.js web/admin/styles.css server.go server_test.go
git commit -m "feat: add embedded admin web ui"
```

---

### Task 4: 实现迁移按钮、旧实例导出接入与退役占位动作

**Files:**
- Modify: `admin_meta.go`
- Modify: `app.go`
- Modify: `server.go`
- Modify: `web/admin/app.js`
- Test: `server_test.go`

- [ ] **Step 1: 为迁移 happy path 和失败路径写 failing tests**

Add to `server_test.go`:

```go
func TestAdminMigrateFromOldImportsLegacySnapshot(t *testing.T) {
	t.Helper()

	legacy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer admin-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"accounts": []map[string]any{
				{"jwt": "jwt-old-1", "quota": 65},
				{"jwt": "jwt-old-2", "quota": 18},
			},
		})
	}))
	defer legacy.Close()

	pool, backend, handler := newTestHandlerWithLegacyBaseURL(legacy.URL)
	backend.quotaByJWT = map[string]int{}

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/migrate-from-old", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(pool.imported) != 2 {
		t.Fatalf("expected 2 imported legacy accounts, got %+v", pool.imported)
	}
}
```

- [ ] **Step 2: 运行测试，确认失败**

Run:

```powershell
& 'C:\Program Files\Go\bin\go.exe' test ./... -count=1
```

Expected:

- `newTestHandlerWithLegacyBaseURL` 缺失
- 迁移逻辑未实现

- [ ] **Step 3: 实现 `App.MigrateFromLegacy()` 与最近迁移状态记录**

Add to `app.go`:

```go
func (a *App) MigrateFromLegacy() MigrationStatus {
	started := a.now().UTC()
	status := MigrationStatus{StartedAt: &started}

	if a.legacyPoolClient == nil {
		status.LastError = "legacy pool export is not configured"
		finished := a.now().UTC()
		status.FinishedAt = &finished
		a.setMigrationStatus(status)
		return status
	}

	exported, err := a.legacyPoolClient.ExportAccounts()
	if err != nil {
		status.LastError = fmt.Sprintf("failed to export legacy pool: %v", err)
		finished := a.now().UTC()
		status.FinishedAt = &finished
		a.setMigrationStatus(status)
		return status
	}

	status.Requested = len(exported)
	accounts := make([]*Account, 0, len(exported))
	for _, item := range exported {
		if strings.TrimSpace(item.JWT) == "" || item.Quota < 2 {
			status.Rejected++
			continue
		}
		accounts = append(accounts, &Account{JWT: item.JWT, Quota: item.Quota})
	}

	result := a.pool.ImportAccounts(accounts)
	status.Imported = result.Imported
	status.Duplicates = result.Duplicates
	status.Overflow = result.Overflow
	status.TotalCount = result.TotalCount
	finished := a.now().UTC()
	status.FinishedAt = &finished
	a.setMigrationStatus(status)
	return status
}
```

- [ ] **Step 4: 为 `/v1/admin/retire-old` 提供明确占位行为**

在第一版里不要伪装成功，直接返回“尚未自动化，但已经检查到目标 service id 与切换前置条件”：

```go
func (a *App) HandleAdminRetireOld(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeOpenAIError(w, http.StatusNotImplemented, "retire-old is not automated yet; complete domain cutover verification first", "not_implemented")
}
```

并在 WebUI 中把它展示为“危险操作 / 当前未自动化”。

- [ ] **Step 5: 在 UI 中接上迁移中心与危险操作区**

Extend `web/admin/app.js`:

```javascript
function renderMigration(pool, migration) {
  document.getElementById('migrationCard').innerHTML = `
    <h2>迁移中心</h2>
    <p>旧池迁移到新实例并持久化</p>
    <div class="stats">
      <div>requested: ${migration.requested ?? 0}</div>
      <div>imported: ${migration.imported ?? 0}</div>
      <div>duplicates: ${migration.duplicates ?? 0}</div>
      <div>rejected: ${migration.rejected ?? 0}</div>
      <div>total: ${migration.total_count ?? pool.total_count}</div>
    </div>
    <button id="migrateBtn">迁移旧实例池子</button>
  `;
  document.getElementById('migrateBtn')?.addEventListener('click', runMigration);
}
```

```javascript
function renderControls() {
  document.getElementById('controlsCard').innerHTML = `
    <h2>池管理</h2>
    <button id="fill50Btn">补 50 个</button>
    <button id="pruneBtn">清理失效号</button>
  `;
  document.getElementById('fill50Btn')?.addEventListener('click', () => runFill(50));
  document.getElementById('pruneBtn')?.addEventListener('click', runPrune);

  document.getElementById('retireCard').innerHTML = `
    <h2>危险操作</h2>
    <p class="muted">迁移完成并切域名后才允许退役老实例</p>
    <button id="retireBtn" disabled>退役老实例（未自动化）</button>
  `;
}
```

- [ ] **Step 6: 运行测试，确认通过**

Run:

```powershell
& 'C:\Program Files\Go\bin\go.exe' test ./... -count=1
```

Expected:

- 迁移 happy path / 失败 path 测试通过
- UI 路由测试继续通过

- [ ] **Step 7: Commit**

```bash
git add app.go admin_meta.go server.go web/admin/app.js server_test.go legacy_pool_client.go
git commit -m "feat: add legacy pool migration workflow"
```

---

### Task 5: 部署到新旧实例并完成迁移验证

**Files:**
- Modify: `C:\Users\HeLuo\AppData\Local\Temp\holo-image-api-deploy\*` (临时部署目录，由同步脚本生成)
- Verify: Zeabur 环境与域名状态

- [ ] **Step 1: 同步部署目录到最新仓库内容**

Run:

```powershell
$src='C:\Users\HeLuo\.config\superpowers\worktrees\source\feat-text-chat-compat'
$dst='C:\Users\HeLuo\AppData\Local\Temp\holo-image-api-deploy'
if (Test-Path $dst) { Remove-Item -LiteralPath $dst -Recurse -Force }
New-Item -ItemType Directory -Path $dst | Out-Null
Get-ChildItem -LiteralPath $src -Force | ForEach-Object {
  Copy-Item -LiteralPath $_.FullName -Destination $dst -Recurse -Force
}
```

Expected:

- 临时部署目录包含最新 `admin_ui.go`、`web/admin/*`、`legacy_pool_client.go`

- [ ] **Step 2: 给新实例写入迁移所需变量**

Run:

```powershell
zeabur variable update `
  --id 69c90776a972bb88a76369ca `
  --env-id 69c558d376bc68ba374cc5cf `
  --key "INSTANCE_NAME=holo-image-api-eners" `
  --key "PUBLIC_BASE_URL=https://holo-image-api-eners.zeabur.app" `
  --key "PRIMARY_PUBLIC_BASE_URL=https://holo-image-api.zeabur.app" `
  --key "LEGACY_POOL_EXPORT_BASE_URL=https://holo-image-api.zeabur.app" `
  --yes --interactive=false --json
```

Run:

```powershell
zeabur variable update `
  --id 69c5591582fb34707a9f2a63 `
  --env-id 69c558d376bc68ba374cc5cf `
  --key "INSTANCE_NAME=holo-image-api" `
  --key "PUBLIC_BASE_URL=https://holo-image-api.zeabur.app" `
  --key "PRIMARY_PUBLIC_BASE_URL=https://holo-image-api.zeabur.app" `
  --yes --interactive=false --json
```

Expected:

- 新实例具备迁移配置
- 旧实例具备导出所需元信息

- [ ] **Step 3: 部署到旧实例，确保导出接口存在**

Run:

```powershell
zeabur deploy --service-id 69c5591582fb34707a9f2a63 --environment-id 69c558d376bc68ba374cc5cf --json
```

Expected:

- 旧实例部署成功

Verify:

```powershell
curl.exe -sS -H "Authorization: Bearer sk-878030051Xsz..." https://holo-image-api.zeabur.app/v1/admin/pool/export
```

Expected:

- 返回 `{"accounts":[...]}`，非 404

- [ ] **Step 4: 部署到新实例，确保 WebUI 与迁移接口存在**

Run:

```powershell
zeabur deploy --service-id 69c90776a972bb88a76369ca --environment-id 69c558d376bc68ba374cc5cf --json
```

Verify:

```powershell
curl.exe -sS -i https://holo-image-api-eners.zeabur.app/admin
curl.exe -sS -H "Authorization: Bearer sk-878030051Xsz..." https://holo-image-api-eners.zeabur.app/v1/admin/meta
curl.exe -sS -H "Authorization: Bearer sk-878030051Xsz..." https://holo-image-api-eners.zeabur.app/v1/admin/migration/status
```

Expected:

- `/admin` 返回 200 HTML
- `/v1/admin/meta` 返回实例信息
- `/v1/admin/migration/status` 返回状态结构

- [ ] **Step 5: 执行旧池迁移并验证新实例持久化增长**

Run:

```powershell
curl.exe -sS -X POST `
  -H "Authorization: Bearer sk-878030051Xsz..." `
  https://holo-image-api-eners.zeabur.app/v1/admin/migrate-from-old
```

Then verify:

```powershell
curl.exe -sS -H "Authorization: Bearer sk-878030051Xsz..." https://holo-image-api-eners.zeabur.app/v1/admin/pool
curl.exe -sS -H "Authorization: Bearer sk-878030051Xsz..." https://holo-image-api-eners.zeabur.app/v1/admin/migration/status
```

Expected:

- `imported > 0` 或至少 `duplicates > 0`
- `persisted_count` 上升
- `total_count` 不下降

- [ ] **Step 6: 重启新实例，验证迁移后仍能恢复**

Run:

```powershell
zeabur service restart --id 69c90776a972bb88a76369ca --env-id 69c558d376bc68ba374cc5cf -y --interactive=false --json
```

Verify:

```powershell
curl.exe -sS -H "Authorization: Bearer sk-878030051Xsz..." https://holo-image-api-eners.zeabur.app/v1/admin/pool
```

Expected:

- `restore_loaded > 0`
- `persisted_count > 0`

- [ ] **Step 7: Commit deployment notes if repo docs changed**

```bash
git status --short
```

Expected:

- 若无 repo 文件变化，则本步跳过 commit
- 若补充了 repo 内运行说明，则提交 `docs: update admin migration rollout notes`

---

### Task 6: 切旧域名到新实例并退役老实例

**Files:**
- No code changes expected
- Verify: Zeabur domain / service state

- [ ] **Step 1: 记录切换前两边状态**

Run:

```powershell
curl.exe -sS -H "Authorization: Bearer sk-878030051Xsz..." https://holo-image-api.zeabur.app/v1/admin/pool
curl.exe -sS -H "Authorization: Bearer sk-878030051Xsz..." https://holo-image-api-eners.zeabur.app/v1/admin/pool
```

Expected:

- 保存切换前 old/new 对比

- [ ] **Step 2: 从旧实例解绑旧域名**

Run:

```powershell
zeabur domain delete `
  --id 69c5591582fb34707a9f2a63 `
  --env-id 69c558d376bc68ba374cc5cf `
  --domain holo-image-api.zeabur.app `
  --yes --interactive=false --json
```

Expected:

- 旧实例不再持有 `holo-image-api.zeabur.app`

- [ ] **Step 3: 把旧域名绑定到新实例**

Run:

```powershell
zeabur domain create `
  --id 69c90776a972bb88a76369ca `
  --env-id 69c558d376bc68ba374cc5cf `
  --domain holo-image-api.zeabur.app `
  --yes --interactive=false --json
```

Expected:

- 新实例持有 `holo-image-api.zeabur.app`

- [ ] **Step 4: 验证旧域名已命中新实例**

Run:

```powershell
curl.exe -sS -i https://holo-image-api.zeabur.app/healthz
curl.exe -sS -H "Authorization: Bearer sk-878030051Xsz..." https://holo-image-api.zeabur.app/v1/admin/meta
curl.exe -sS -H "Authorization: Bearer sk-878030051Xsz..." https://holo-image-api.zeabur.app/v1/admin/pool
```

Expected:

- `/healthz` 返回 200
- `/v1/admin/meta` 的 `instance_name` 为 `holo-image-api-eners`
- `/v1/admin/pool` 的 `restore_loaded > 0`

- [ ] **Step 5: 挂起老实例，做短暂观察**

Run:

```powershell
zeabur service suspend --id 69c5591582fb34707a9f2a63 --env-id 69c558d376bc68ba374cc5cf -y --interactive=false --json
```

Expected:

- 旧实例停止提供服务
- 旧域名仍正常命中新实例

- [ ] **Step 6: 最终验证主域名 + Admin WebUI**

Run:

```powershell
curl.exe -sS -i https://holo-image-api.zeabur.app/admin
curl.exe -sS -H "Authorization: Bearer sk-878030051Xsz..." https://holo-image-api.zeabur.app/v1/admin/pool
curl.exe -sS -H "Authorization: Bearer sk-878030051Xsz..." https://holo-image-api.zeabur.app/v1/models
```

Expected:

- `/admin` 返回 200 HTML
- `/v1/admin/pool` 正常
- `/v1/models` 正常

- [ ] **Step 7: 如果观察窗口通过，删除老实例**

Run:

```powershell
zeabur service delete --id 69c5591582fb34707a9f2a63 -y --interactive=false --json
```

Expected:

- 老实例删除成功
- 项目内只剩新实例为主

---

## Self-Review

### Spec coverage

- **新实例成为唯一真实实例** → Task 5 + Task 6
- **旧池迁移到新实例** → Task 2 + Task 4 + Task 5
- **旧域名切到新实例** → Task 6
- **Admin WebUI 挂在新实例** → Task 3 + Task 5
- **复用 admin key** → Task 3 / Task 4 前端与服务端调用
- **老实例退役** → Task 6

### Placeholder scan

- 无 `TODO` / `TBD`
- 所有接口、路径、命令、service id、env id 已给出

### Type consistency

- `ExportedAccount` 作为导出载荷
- `MigrationStatus` 作为迁移状态结构
- `LegacyPoolClient.ExportAccounts()` 作为旧实例拉取入口
- `PoolManager.ExportAccounts()` 作为池导出入口
