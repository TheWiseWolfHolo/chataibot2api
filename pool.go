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
	BorrowedCount         int                `json:"borrowed_count"`
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
	quota                func(string) int

	mu       sync.Mutex
	cond     *sync.Cond
	ready    []*Account
	reusable []*Account
	borrowed int

	activeRegistrations   int
	registrationSuccesses int
	registrationFailures  int
	failureStreak         int
	lastRegistrationError string
	nextRetryAt           time.Time
	pruneChecks           int
	pruneRemoved          int
	autoFillActive        bool

	tasks map[string]*registrationTask
}

type PoolOptions struct {
	LowWatermark         int
	PruneInterval        time.Duration
	RegistrationInterval time.Duration
	FailureBackoff       time.Duration
	MaxFailureBackoff    time.Duration
}

func NewSimplePool(poolSize int, workerCount int, registrar func() (string, error), quota func(string) int) *SimplePool {
	return NewSimplePoolWithOptions(poolSize, workerCount, registrar, quota, PoolOptions{})
}

func NewSimplePoolWithOptions(poolSize int, workerCount int, registrar func() (string, error), quota func(string) int, options PoolOptions) *SimplePool {
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
		reusable:             make([]*Account, 0, poolSize),
		autoFillActive:       true,
		tasks:                make(map[string]*registrationTask),
	}
	pool.cond = sync.NewCond(&pool.mu)

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

	return NewSimplePoolWithOptions(cfg.PoolSize, workerCount, CreateAccount, apiClient.GetCount, PoolOptions{
		LowWatermark:         cfg.PoolLowWatermark,
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
			p.borrowed++
			return acc
		}

		if len(p.ready) > 0 {
			acc := p.ready[0]
			p.ready = append(p.ready[:0], p.ready[1:]...)
			p.borrowed++
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

	quota := acc.Quota
	if p.quota != nil {
		quota = p.quota(acc.JWT)
	}
	acc.Quota = quota

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.borrowed > 0 {
		p.borrowed--
	}
	if acc.Quota < 2 {
		p.reconcileAutoFillLocked()
		if p.autoFillActive {
			p.cond.Broadcast()
		}
		return
	}

	if len(p.reusable) < p.maxSize {
		p.reusable = append(p.reusable, acc)
		p.reconcileAutoFillLocked()
		p.cond.Broadcast()
		return
	}

	minIdx := 0
	for i := 1; i < len(p.reusable); i++ {
		if p.reusable[i].Quota < p.reusable[minIdx].Quota {
			minIdx = i
		}
	}
	if acc.Quota > p.reusable[minIdx].Quota {
		p.reusable[minIdx] = acc
	}
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

	return PoolStatus{
		ReadyCount:            len(p.ready),
		ReusableCount:         len(p.reusable),
		TotalCount:            len(p.ready) + len(p.reusable),
		BorrowedCount:         p.borrowed,
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
		Tasks:                 tasks,
	}
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
		if len(p.ready)+len(p.reusable) >= p.maxSize {
			result.Overflow++
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
	p.reconcileAutoFillLocked()
	if result.Imported > 0 {
		p.cond.Broadcast()
	}
	return result
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
			quota = p.quota(item.account.JWT)
		}
		item.account.Quota = quota

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
	p.pruneChecks += summary.Checked
	p.pruneRemoved += summary.Removed
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
	return len(p.ready) + len(p.reusable) + p.borrowed
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
