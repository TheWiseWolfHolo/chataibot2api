package main

import (
	"fmt"
	"sort"
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

type registrationTask struct {
	snapshot FillTaskSnapshot
}

type pooledAccount struct {
	account    *Account
	isReusable bool
}

type SimplePool struct {
	maxSize       int
	lowWatermark  int
	pruneInterval time.Duration
	workerCount   int
	registrar     func() (bool, string)
	quota         func(string) int

	mu       sync.Mutex
	cond     *sync.Cond
	ready    []*Account
	reusable []*Account
	borrowed int

	activeRegistrations   int
	registrationSuccesses int
	registrationFailures  int
	pruneChecks           int
	pruneRemoved          int
	autoFillActive        bool

	tasks map[string]*registrationTask
}

type PoolOptions struct {
	LowWatermark  int
	PruneInterval time.Duration
}

func NewSimplePool(poolSize int, workerCount int, registrar func() (bool, string), quota func(string) int) *SimplePool {
	return NewSimplePoolWithOptions(poolSize, workerCount, registrar, quota, PoolOptions{})
}

func NewSimplePoolWithOptions(poolSize int, workerCount int, registrar func() (bool, string), quota func(string) int, options PoolOptions) *SimplePool {
	if poolSize < 1 {
		poolSize = 1
	}
	lowWatermark := normalizeLowWatermark(poolSize, options.LowWatermark)
	pruneInterval := options.PruneInterval
	if pruneInterval == 0 {
		pruneInterval = 5 * time.Minute
	}

	pool := &SimplePool{
		maxSize:        poolSize,
		lowWatermark:   lowWatermark,
		pruneInterval:  pruneInterval,
		workerCount:    workerCount,
		registrar:      registrar,
		quota:          quota,
		ready:          make([]*Account, 0, poolSize),
		reusable:       make([]*Account, 0, poolSize),
		autoFillActive: true,
		tasks:          make(map[string]*registrationTask),
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
	return NewSimplePoolWithOptions(cfg.PoolSize, 3, CreateAccount, apiClient.GetCount, PoolOptions{
		LowWatermark:  cfg.PoolLowWatermark,
		PruneInterval: time.Duration(cfg.PoolPruneIntervalSeconds) * time.Second,
	})
}

func (p *SimplePool) registrationLoop() {
	for {
		p.mu.Lock()
		for !p.shouldAutoRegisterLocked() {
			p.cond.Wait()
		}
		p.mu.Unlock()

		if !p.createAndEnqueueAccount() {
			time.Sleep(3 * time.Second)
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

func (p *SimplePool) createAndEnqueueAccount() bool {
	p.mu.Lock()
	p.activeRegistrations++
	p.mu.Unlock()

	success, jwt := p.registrar()

	p.mu.Lock()
	defer p.mu.Unlock()
	p.activeRegistrations--

	if !success || jwt == "" {
		p.registrationFailures++
		return false
	}

	p.registrationSuccesses++
	p.ready = append(p.ready, &Account{JWT: jwt, Quota: 65})
	p.reconcileAutoFillLocked()
	p.cond.Broadcast()
	return true
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
			success := p.createAndEnqueueAccount()

			p.mu.Lock()
			if success {
				task.snapshot.Completed++
			} else {
				task.snapshot.Failed++
			}
			p.mu.Unlock()
		}

		finishedAt := time.Now()
		p.mu.Lock()
		task.snapshot.Status = "completed"
		task.snapshot.FinishedAt = &finishedAt
		p.mu.Unlock()
	}()

	return task.snapshot
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
