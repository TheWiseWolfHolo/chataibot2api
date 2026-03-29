package main

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type PoolStatus struct {
	ReadyCount            int                `json:"ready_count"`
	ReusableCount         int                `json:"reusable_count"`
	TotalCount            int                `json:"total_count"`
	TargetCount           int                `json:"target_count"`
	BorrowedCount         int                `json:"borrowed_count"`
	LowQuotaCount         int                `json:"low_quota_count"`
	WorkerCount           int                `json:"worker_count"`
	LowWatermark          int                `json:"low_watermark"`
	AutoFillActive        bool               `json:"auto_fill_active"`
	PruneIntervalSeconds  int                `json:"prune_interval_seconds"`
	ActiveRegistrations   int                `json:"active_registrations"`
	RegistrationSuccesses int                `json:"registration_successes"`
	RegistrationFailures  int                `json:"registration_failures"`
	FailureStreak         int                `json:"failure_streak"`
	LastRegistrationError string             `json:"last_registration_error,omitempty"`
	NextRetryAt           *time.Time         `json:"next_retry_at,omitempty"`
	PruneChecks           int                `json:"prune_checks"`
	PruneRemoved          int                `json:"prune_removed"`
	PersistenceEnabled    bool               `json:"persistence_enabled"`
	PersistencePath       string             `json:"persistence_path,omitempty"`
	PersistedCount        int                `json:"persisted_count"`
	RestoreLoaded         int                `json:"restore_loaded"`
	RestoreRejected       int                `json:"restore_rejected"`
	LastPersistError      string             `json:"last_persist_error,omitempty"`
	LastPersistAt         *time.Time         `json:"last_persist_at,omitempty"`
	LastRestoreAt         *time.Time         `json:"last_restore_at,omitempty"`
	Tasks                 []FillTaskSnapshot `json:"tasks"`
}

type FillTaskSnapshot struct {
	ID         string     `json:"id"`
	Requested  int        `json:"requested"`
	Completed  int        `json:"completed"`
	Failed     int        `json:"failed"`
	Status     string     `json:"status"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

type PruneSummary struct {
	Checked   int `json:"checked"`
	Removed   int `json:"removed"`
	Remaining int `json:"remaining"`
}

type ImportPoolResult struct {
	Imported   int `json:"imported"`
	Duplicates int `json:"duplicates"`
	Overflow   int `json:"overflow"`
	TotalCount int `json:"total_count"`
}

type RestorePoolResult struct {
	Requested  int `json:"requested"`
	Restored   int `json:"restored"`
	Rejected   int `json:"rejected"`
	Duplicates int `json:"duplicates"`
	Overflow   int `json:"overflow"`
	TotalCount int `json:"total_count"`
}

type registrationTask struct {
	snapshot FillTaskSnapshot
}

type pooledAccount struct {
	account    *Account
	isReusable bool
}

type SimplePool struct {
	maxSize              int
	lowWatermark         int
	pruneInterval        time.Duration
	registrationInterval time.Duration
	failureBackoff       time.Duration
	maxFailureBackoff    time.Duration
	workerCount          int
	registrar            func() (string, error)
	quota                func(string) (int, error)

	mu       sync.Mutex
	cond     *sync.Cond
	ready    []*Account
	reusable []*Account
	borrowed map[*Account]string

	activeRegistrations   int
	registrationSuccesses int
	registrationFailures  int
	failureStreak         int
	lastRegistrationError string
	nextRetryAt           time.Time
	pruneChecks           int
	pruneRemoved          int
	autoFillActive        bool
	store                 AccountStore
	persistedCount        int
	restoreLoaded         int
	restoreRejected       int
	lastPersistError      string
	lastPersistAt         time.Time
	lastRestoreAt         time.Time
	pruneShadow           []pooledAccount

	tasks map[string]*registrationTask
}

type PoolOptions struct {
	LowWatermark         int
	PruneInterval        time.Duration
	RegistrationInterval time.Duration
	FailureBackoff       time.Duration
	MaxFailureBackoff    time.Duration
	Store                AccountStore
}

const lowQuotaThreshold = 10

func NewSimplePool(poolSize int, workerCount int, registrar func() (string, error), quota func(string) (int, error)) *SimplePool {
	return NewSimplePoolWithOptions(poolSize, workerCount, registrar, quota, PoolOptions{})
}

func NewSimplePoolWithOptions(poolSize int, workerCount int, registrar func() (string, error), quota func(string) (int, error), options PoolOptions) *SimplePool {
	if poolSize < 1 {
		poolSize = 1
	}
	lowWatermark := normalizeLowWatermark(poolSize, options.LowWatermark)
	pruneInterval := options.PruneInterval
	if pruneInterval == 0 {
		pruneInterval = 5 * time.Minute
	}
	registrationInterval := options.RegistrationInterval
	if registrationInterval < 0 {
		registrationInterval = 0
	}
	failureBackoff := options.FailureBackoff
	if failureBackoff <= 0 {
		failureBackoff = 3 * time.Second
	}
	maxFailureBackoff := options.MaxFailureBackoff
	if maxFailureBackoff <= 0 {
		maxFailureBackoff = 15 * time.Minute
	}
	if maxFailureBackoff < failureBackoff {
		maxFailureBackoff = failureBackoff
	}

	pool := &SimplePool{
		maxSize:              poolSize,
		lowWatermark:         lowWatermark,
		pruneInterval:        pruneInterval,
		registrationInterval: registrationInterval,
		failureBackoff:       failureBackoff,
		maxFailureBackoff:    maxFailureBackoff,
		workerCount:          workerCount,
		registrar:            registrar,
		quota:                quota,
		ready:                make([]*Account, 0, poolSize),
		borrowed:             make(map[*Account]string),
		reusable:             make([]*Account, 0, poolSize),
		autoFillActive:       true,
		store:                options.Store,
		tasks:                make(map[string]*registrationTask),
	}
	pool.cond = sync.NewCond(&pool.mu)
	pool.restoreFromStore()

	for i := 0; i < workerCount; i++ {
		go pool.registrationLoop()
	}
	if pruneInterval > 0 {
		go pool.pruneLoop()
	}

	return pool
}

func StartPool(cfg Config) *SimplePool {
	workerCount := cfg.PoolWorkerCount
	if workerCount < 1 {
		workerCount = 1
	}
	store := NewFileAccountStore(cfg.PoolStorePath)

	return NewSimplePoolWithOptions(cfg.PoolSize, workerCount, CreateAccount, apiClient.GetCount, PoolOptions{
		LowWatermark:         cfg.PoolLowWatermark,
		Store:                store,
		PruneInterval:        time.Duration(cfg.PoolPruneIntervalSeconds) * time.Second,
		RegistrationInterval: time.Duration(cfg.PoolRegistrationInterval) * time.Second,
		FailureBackoff:       time.Duration(cfg.PoolFailureBackoff) * time.Second,
		MaxFailureBackoff:    time.Duration(cfg.PoolFailureBackoffMax) * time.Second,
	})
}

func (p *SimplePool) registrationLoop() {
	successDelay := p.registrationSuccessDelay()
	failureDelay := p.registrationFailureDelay()

	for {
		p.mu.Lock()
		for !p.shouldAutoRegisterLocked() {
			p.cond.Wait()
		}
		p.mu.Unlock()

		success, delay := p.createAndEnqueueAccount()
		if success {
			if successDelay > 0 {
				time.Sleep(successDelay)
			}
			continue
		}
		if delay <= 0 {
			delay = failureDelay
		}
		if delay > 0 {
			time.Sleep(delay)
		}
	}
}

func (p *SimplePool) pruneLoop() {
	ticker := time.NewTicker(p.pruneInterval)
	defer ticker.Stop()

	for range ticker.C {
		p.Prune()
	}
}

func (p *SimplePool) createAndEnqueueAccount() (bool, time.Duration) {
	p.mu.Lock()
	p.activeRegistrations++
	p.mu.Unlock()

	jwt, err := p.registrar()
	now := time.Now()

	p.mu.Lock()
	defer p.mu.Unlock()
	p.activeRegistrations--

	if err != nil || jwt == "" {
		p.registrationFailures++
		p.failureStreak++
		if err != nil {
			p.lastRegistrationError = strings.TrimSpace(err.Error())
		} else {
			p.lastRegistrationError = "注册失败：未返回 JWT"
		}
		delay := p.failureDelayForLocked(p.lastRegistrationError)
		nextRetryAt := now.Add(delay)
		p.nextRetryAt = nextRetryAt
		return false, delay
	}

	p.registrationSuccesses++
	p.failureStreak = 0
	p.lastRegistrationError = ""
	p.nextRetryAt = time.Time{}
	p.ready = append(p.ready, &Account{JWT: jwt, Quota: 65})
	p.persistLocked()
	p.reconcileAutoFillLocked()
	p.cond.Broadcast()
	return true, 0
}

func (p *SimplePool) Acquire(cost int) *Account {
	p.mu.Lock()
	defer p.mu.Unlock()

	for {
		bestIdx := -1
		for i, acc := range p.reusable {
			if acc.Quota >= cost {
				if bestIdx == -1 || acc.Quota < p.reusable[bestIdx].Quota {
					bestIdx = i
				}
			}
		}
		if bestIdx != -1 {
			acc := p.reusable[bestIdx]
			p.reusable = append(p.reusable[:bestIdx], p.reusable[bestIdx+1:]...)
			p.borrowed[acc] = strings.TrimSpace(acc.JWT)
			return acc
		}

		if len(p.ready) > 0 {
			acc := p.ready[0]
			p.ready = append(p.ready[:0], p.ready[1:]...)
			p.borrowed[acc] = strings.TrimSpace(acc.JWT)
			return acc
		}

		p.reconcileAutoFillLocked()
		p.cond.Broadcast()
		p.cond.Wait()
	}
}

func (p *SimplePool) Release(acc *Account) {
	if acc == nil {
		return
	}

	if p.quota != nil {
		quota, err := p.quota(acc.JWT)
		if err != nil {
			fmt.Printf("[-] 刷新账号额度失败，保留原额度：jwt=%s err=%v\n", strings.TrimSpace(acc.JWT), err)
		} else {
			acc.Quota = quota
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	delete(p.borrowed, acc)
	if acc.Quota < 2 {
		p.persistLocked()
		p.reconcileAutoFillLocked()
		if p.autoFillActive {
			p.cond.Broadcast()
		}
		return
	}

	p.reusable = append(p.reusable, acc)
	p.reconcileAutoFillLocked()
	p.cond.Broadcast()
}

func (p *SimplePool) Status() PoolStatus {
	p.mu.Lock()
	defer p.mu.Unlock()

	tasks := make([]FillTaskSnapshot, 0, len(p.tasks))
	for _, task := range p.tasks {
		tasks = append(tasks, task.snapshot)
	}
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].StartedAt.After(tasks[j].StartedAt)
	})

	var nextRetryAt *time.Time
	if !p.nextRetryAt.IsZero() {
		ts := p.nextRetryAt
		nextRetryAt = &ts
	}

	visibleReady, visibleReusable := p.visiblePooledCountsLocked()

	return PoolStatus{
		ReadyCount:            visibleReady,
		ReusableCount:         visibleReusable,
		TotalCount:            visibleReady + visibleReusable,
		TargetCount:           p.maxSize,
		BorrowedCount:         len(p.borrowed),
		LowQuotaCount:         p.lowQuotaCountLocked(),
		WorkerCount:           p.workerCount,
		LowWatermark:          p.lowWatermark,
		AutoFillActive:        p.autoFillActive,
		PruneIntervalSeconds:  int(p.pruneInterval / time.Second),
		ActiveRegistrations:   p.activeRegistrations,
		RegistrationSuccesses: p.registrationSuccesses,
		RegistrationFailures:  p.registrationFailures,
		FailureStreak:         p.failureStreak,
		LastRegistrationError: p.lastRegistrationError,
		NextRetryAt:           nextRetryAt,
		PruneChecks:           p.pruneChecks,
		PruneRemoved:          p.pruneRemoved,
		PersistenceEnabled:    p.store != nil,
		PersistencePath:       p.persistencePath(),
		PersistedCount:        p.persistedCount,
		RestoreLoaded:         p.restoreLoaded,
		RestoreRejected:       p.restoreRejected,
		LastPersistError:      p.lastPersistError,
		LastPersistAt:         timePointer(p.lastPersistAt),
		LastRestoreAt:         timePointer(p.lastRestoreAt),
		Tasks:                 tasks,
	}
}

func (p *SimplePool) lowQuotaCountLocked() int {
	if p == nil {
		return 0
	}

	count := 0
	for _, item := range p.visiblePooledAccountsLocked() {
		if item.account != nil && item.account.Quota >= 2 && item.account.Quota < lowQuotaThreshold {
			count++
		}
	}
	for acc := range p.borrowed {
		if acc != nil && acc.Quota >= 2 && acc.Quota < lowQuotaThreshold {
			count++
		}
	}
	return count
}

func (p *SimplePool) StartFillTask(count int) FillTaskSnapshot {
	now := time.Now()
	taskID := fmt.Sprintf("fill-%d", now.UnixNano())
	task := &registrationTask{
		snapshot: FillTaskSnapshot{
			ID:        taskID,
			Requested: count,
			Status:    "running",
			StartedAt: now,
		},
	}

	p.mu.Lock()
	p.tasks[taskID] = task
	p.mu.Unlock()

	go func() {
		for i := 0; i < count; i++ {
			success, delay := p.createAndEnqueueAccount()

			p.mu.Lock()
			if success {
				task.snapshot.Completed++
			} else {
				task.snapshot.Failed++
			}
			p.mu.Unlock()

			if delay > 0 {
				time.Sleep(delay)
			}
		}

		finishedAt := time.Now()
		p.mu.Lock()
		task.snapshot.Status = "completed"
		task.snapshot.FinishedAt = &finishedAt
		p.mu.Unlock()
	}()

	return task.snapshot
}

func (p *SimplePool) ImportAccounts(accounts []*Account) ImportPoolResult {
	p.mu.Lock()
	defer p.mu.Unlock()

	existing := make(map[string]struct{}, len(p.ready)+len(p.reusable))
	for _, acc := range p.ready {
		if acc == nil {
			continue
		}
		jwt := strings.TrimSpace(acc.JWT)
		if jwt == "" {
			continue
		}
		existing[jwt] = struct{}{}
	}
	for _, acc := range p.reusable {
		if acc == nil {
			continue
		}
		jwt := strings.TrimSpace(acc.JWT)
		if jwt == "" {
			continue
		}
		existing[jwt] = struct{}{}
	}

	var result ImportPoolResult
	for _, acc := range accounts {
		if acc == nil {
			continue
		}
		jwt := strings.TrimSpace(acc.JWT)
		if jwt == "" {
			continue
		}
		if _, ok := existing[jwt]; ok {
			result.Duplicates++
			continue
		}
		imported := &Account{
			JWT:   jwt,
			Quota: acc.Quota,
		}
		p.ready = append(p.ready, imported)
		existing[jwt] = struct{}{}
		result.Imported++
	}

	result.TotalCount = len(p.ready) + len(p.reusable)
	p.persistLocked()
	p.reconcileAutoFillLocked()
	if result.Imported > 0 {
		p.cond.Broadcast()
	}
	return result
}

func (p *SimplePool) RestoreAccounts(accounts []*Account) (RestorePoolResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.borrowed) > 0 {
		return RestorePoolResult{}, fmt.Errorf("cannot restore while %d accounts are borrowed", len(p.borrowed))
	}

	var result RestorePoolResult
	result.Requested = len(accounts)

	restored := make([]*Account, 0, len(accounts))
	seen := make(map[string]struct{}, len(accounts))
	for _, acc := range accounts {
		if acc == nil {
			result.Rejected++
			continue
		}
		jwt := strings.TrimSpace(acc.JWT)
		if jwt == "" {
			result.Rejected++
			continue
		}
		if _, ok := seen[jwt]; ok {
			result.Duplicates++
			continue
		}
		seen[jwt] = struct{}{}

		if acc.Quota < 2 {
			result.Rejected++
			continue
		}
		restored = append(restored, &Account{
			JWT:   jwt,
			Quota: acc.Quota,
		})
	}

	p.ready = restored
	p.reusable = nil
	p.pruneShadow = nil
	result.Restored = len(restored)
	result.TotalCount = len(p.ready)
	p.persistLocked()
	p.reconcileAutoFillLocked()
	p.cond.Broadcast()
	return result, nil
}

func (p *SimplePool) ExportAccounts() []ExportedAccount {
	p.mu.Lock()
	defer p.mu.Unlock()

	snapshot := p.snapshotAccountsLocked()
	exported := make([]ExportedAccount, 0, len(snapshot))
	for _, account := range snapshot {
		if account == nil {
			continue
		}
		jwt := strings.TrimSpace(account.JWT)
		if jwt == "" {
			continue
		}
		exported = append(exported, ExportedAccount{
			JWT:   jwt,
			Quota: account.Quota,
		})
	}
	return exported
}

func (p *SimplePool) AdminQuotaRows() []AdminQuotaRow {
	p.mu.Lock()
	defer p.mu.Unlock()

	visiblePooled := p.visiblePooledAccountsLocked()
	totalCandidates := len(visiblePooled) + len(p.borrowed)
	rows := make([]AdminQuotaRow, 0, totalCandidates)
	seen := make(map[string]struct{}, totalCandidates)
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

	for _, item := range visiblePooled {
		if item.account == nil {
			continue
		}
		bucket := "ready"
		if item.isReusable {
			bucket = "reusable"
		}
		appendRow(item.account.JWT, item.account.Quota, bucket)
	}
	for acc, originalJWT := range p.borrowed {
		if acc == nil {
			continue
		}
		appendRow(originalJWT, acc.Quota, "borrowed")
	}

	return rows
}

func (p *SimplePool) Prune() PruneSummary {
	p.mu.Lock()
	pooled := make([]pooledAccount, 0, len(p.ready)+len(p.reusable))
	for _, acc := range p.ready {
		pooled = append(pooled, pooledAccount{account: acc})
	}
	for _, acc := range p.reusable {
		pooled = append(pooled, pooledAccount{account: acc, isReusable: true})
	}
	p.pruneShadow = clonePooledAccounts(pooled)
	p.ready = nil
	p.reusable = nil
	p.mu.Unlock()

	keptReady := make([]*Account, 0, len(pooled))
	keptReusable := make([]*Account, 0, len(pooled))
	summary := PruneSummary{}

	for _, item := range pooled {
		summary.Checked++
		quota := item.account.Quota
		if p.quota != nil {
			refreshedQuota, err := p.quota(item.account.JWT)
			if err != nil {
				fmt.Printf("[-] prune 刷新额度失败，跳过删除：jwt=%s err=%v\n", strings.TrimSpace(item.account.JWT), err)
			} else {
				quota = refreshedQuota
				item.account.Quota = quota
			}
		}

		if quota < 2 {
			summary.Removed++
			continue
		}

		if item.isReusable {
			keptReusable = append(keptReusable, item.account)
		} else {
			keptReady = append(keptReady, item.account)
		}
	}

	p.mu.Lock()
	p.ready = append(p.ready, keptReady...)
	p.reusable = append(p.reusable, keptReusable...)
	p.pruneShadow = nil
	p.pruneChecks += summary.Checked
	p.pruneRemoved += summary.Removed
	p.persistLocked()
	p.reconcileAutoFillLocked()
	summary.Remaining = len(p.ready) + len(p.reusable)
	if len(keptReady) > 0 || len(keptReusable) > 0 || p.autoFillActive {
		p.cond.Broadcast()
	}
	p.mu.Unlock()

	return summary
}

func normalizeLowWatermark(target int, lowWatermark int) int {
	if target < 1 {
		return 1
	}
	if lowWatermark > 0 {
		if lowWatermark > target {
			return target
		}
		return lowWatermark
	}
	if target >= 1000 {
		return 500
	}
	if target == 1 {
		return 1
	}
	half := target / 2
	if half < 1 {
		return 1
	}
	return half
}

func (p *SimplePool) shouldAutoRegisterLocked() bool {
	p.reconcileAutoFillLocked()
	if !p.autoFillActive {
		return false
	}
	return p.healthyCountLocked()+p.activeRegistrations < p.maxSize
}

func (p *SimplePool) reconcileAutoFillLocked() {
	if p.autoFillActive {
		if p.healthyCountLocked()+p.activeRegistrations >= p.maxSize {
			p.autoFillActive = false
		}
		return
	}
	if p.healthyCountLocked() < p.lowWatermark {
		p.autoFillActive = true
	}
}

func (p *SimplePool) healthyCountLocked() int {
	visibleReady, visibleReusable := p.visiblePooledCountsLocked()
	return visibleReady + visibleReusable + len(p.borrowed)
}

func (p *SimplePool) registrationSuccessDelay() time.Duration {
	if p == nil {
		return 0
	}
	if p.registrationInterval < 0 {
		return 0
	}
	return p.registrationInterval
}

func (p *SimplePool) registrationFailureDelay() time.Duration {
	if p == nil {
		return 3 * time.Second
	}
	delay := p.failureBackoff
	if delay <= 0 {
		return 3 * time.Second
	}
	return delay
}

func (p *SimplePool) failureDelayForLocked(lastErr string) time.Duration {
	delay := p.registrationFailureDelay()
	if delay <= 0 {
		delay = time.Second
	}

	for i := 1; i < p.failureStreak; i++ {
		if delay >= p.maxFailureBackoff {
			return p.maxFailureBackoff
		}
		if delay > p.maxFailureBackoff/2 {
			delay = p.maxFailureBackoff
			break
		}
		delay *= 2
	}

	errLower := strings.ToLower(lastErr)
	switch {
	case strings.Contains(errLower, "blocked temporarily for this ip"):
		if delay < 10*time.Minute {
			delay = 10 * time.Minute
		}
	case strings.Contains(errLower, "too many simultaneous requests"):
		if delay < 2*time.Minute {
			delay = 2 * time.Minute
		}
	}

	if p.maxFailureBackoff > 0 && delay > p.maxFailureBackoff {
		return p.maxFailureBackoff
	}
	return delay
}

func (p *SimplePool) restoreFromStore() {
	if p == nil || p.store == nil {
		return
	}

	accounts, err := p.store.Load()
	p.mu.Lock()
	defer p.mu.Unlock()

	p.lastRestoreAt = time.Now().UTC()
	if err != nil {
		p.lastPersistError = fmt.Sprintf("restore failed: %v", err)
		return
	}

	loaded := 0
	rejected := 0
	seen := make(map[string]struct{}, len(accounts))
	for _, acc := range accounts {
		if acc == nil {
			rejected++
			continue
		}
		jwt := strings.TrimSpace(acc.JWT)
		if jwt == "" {
			rejected++
			continue
		}
		if _, ok := seen[jwt]; ok {
			rejected++
			continue
		}
		seen[jwt] = struct{}{}

		if acc.Quota < 2 {
			rejected++
			continue
		}
		p.ready = append(p.ready, &Account{
			JWT:   jwt,
			Quota: acc.Quota,
		})
		loaded++
	}

	p.restoreLoaded = loaded
	p.restoreRejected = rejected
	p.persistedCount = loaded
	p.lastPersistError = ""
	p.reconcileAutoFillLocked()
}

func (p *SimplePool) persistLocked() {
	if p == nil || p.store == nil {
		return
	}

	snapshot := p.snapshotAccountsLocked()
	if err := p.store.Save(snapshot); err != nil {
		p.lastPersistError = err.Error()
		return
	}

	p.persistedCount = len(snapshot)
	p.lastPersistError = ""
	p.lastPersistAt = time.Now().UTC()
}

func (p *SimplePool) snapshotAccountsLocked() []*Account {
	if p == nil {
		return nil
	}

	visiblePooled := p.visiblePooledAccountsLocked()
	result := make([]*Account, 0, len(visiblePooled)+len(p.borrowed))
	seen := make(map[string]struct{}, len(result))
	appendAccount := func(jwt string, quota int) {
		jwt = strings.TrimSpace(jwt)
		if jwt == "" || quota < 2 {
			return
		}
		if _, ok := seen[jwt]; ok {
			return
		}
		seen[jwt] = struct{}{}
		result = append(result, &Account{
			JWT:   jwt,
			Quota: quota,
		})
	}

	for _, item := range visiblePooled {
		if item.account == nil {
			continue
		}
		appendAccount(item.account.JWT, item.account.Quota)
	}
	for acc, originalJWT := range p.borrowed {
		if acc == nil {
			continue
		}
		appendAccount(originalJWT, acc.Quota)
	}

	return result
}

func (p *SimplePool) visiblePooledAccountsLocked() []pooledAccount {
	if p == nil {
		return nil
	}

	visible := make([]pooledAccount, 0, len(p.ready)+len(p.reusable)+len(p.pruneShadow))
	visible = append(visible, clonePooledAccounts(p.pruneShadow)...)
	for _, acc := range p.ready {
		if acc == nil {
			continue
		}
		visible = append(visible, pooledAccount{account: acc})
	}
	for _, acc := range p.reusable {
		if acc == nil {
			continue
		}
		visible = append(visible, pooledAccount{account: acc, isReusable: true})
	}
	return visible
}

func (p *SimplePool) visiblePooledCountsLocked() (int, int) {
	readyCount := 0
	reusableCount := 0
	for _, item := range p.visiblePooledAccountsLocked() {
		if item.account == nil {
			continue
		}
		if item.isReusable {
			reusableCount++
		} else {
			readyCount++
		}
	}
	return readyCount, reusableCount
}

func clonePooledAccounts(accounts []pooledAccount) []pooledAccount {
	if len(accounts) == 0 {
		return nil
	}

	cloned := make([]pooledAccount, 0, len(accounts))
	for _, item := range accounts {
		if item.account == nil {
			continue
		}
		copyValue := *item.account
		cloned = append(cloned, pooledAccount{
			account:    &copyValue,
			isReusable: item.isReusable,
		})
	}
	return cloned
}


func (p *SimplePool) persistencePath() string {
	if p == nil || p.store == nil {
		return ""
	}
	return strings.TrimSpace(p.store.Path())
}

func timePointer(ts time.Time) *time.Time {
	if ts.IsZero() {
		return nil
	}
	value := ts
	return &value
}
