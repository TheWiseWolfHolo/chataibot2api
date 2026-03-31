package main

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"chataibot2api/protocol"
)

type memoryAccountStore struct {
	accounts []*Account
	loadErr  error
	saveErr  error
	loads    int
	saves    int
}

func (s *memoryAccountStore) Load() ([]*Account, error) {
	s.loads++
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	return cloneAccounts(s.accounts), nil
}

func (s *memoryAccountStore) Save(accounts []*Account) error {
	s.saves++
	if s.saveErr != nil {
		return s.saveErr
	}
	s.accounts = cloneAccounts(accounts)
	return nil
}

func (s *memoryAccountStore) Path() string {
	return "memory://pool"
}

func cloneAccounts(accounts []*Account) []*Account {
	if len(accounts) == 0 {
		return nil
	}
	cloned := make([]*Account, 0, len(accounts))
	for _, acc := range accounts {
		if acc == nil {
			continue
		}
		copyValue := *acc
		cloned = append(cloned, &copyValue)
	}
	return cloned
}

func (f *fakePool) AdminQuotaRows() []AdminQuotaRow {
	if f == nil {
		return nil
	}
	return nil
}

func TestSimplePoolStartFillTaskCreatesAccountsAsynchronously(t *testing.T) {
	t.Helper()

	registerCalls := 0
	pool := NewSimplePool(10, 0, func() (string, error) {
		registerCalls++
		return "jwt-fill-" + time.Now().Format("150405.000000"), nil
	}, func(_ string) (int, error) {
		return 65, nil
	})

	task := pool.StartFillTask(2)
	if task.ID == "" {
		t.Fatalf("expected task id, got empty")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status := pool.Status()
		if len(status.Tasks) > 0 && status.Tasks[0].Status == "completed" {
			if status.Tasks[0].Completed != 2 {
				t.Fatalf("expected 2 completed registrations, got %+v", status.Tasks[0])
			}
			if status.ReadyCount != 2 {
				t.Fatalf("expected ready count 2, got %+v", status)
			}
			if registerCalls != 2 {
				t.Fatalf("expected 2 register calls, got %d", registerCalls)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("expected fill task to complete before deadline, latest status=%+v", pool.Status())
}

func TestSimplePoolTryAcquireImageSkipsExcludedJWTs(t *testing.T) {
	t.Helper()

	pool := NewSimplePool(10, 0, nil, nil)
	pool.ready = []*Account{
		{JWT: "jwt-excluded", Quota: 65},
		{JWT: "jwt-fresh", Quota: 65},
	}

	acc := pool.TryAcquireImage(10, map[string]struct{}{
		"jwt-excluded": {},
	})

	if acc == nil {
		t.Fatalf("expected a fresh account, got nil")
	}
	if acc.JWT != "jwt-fresh" {
		t.Fatalf("expected jwt-fresh, got %+v", acc)
	}
	if len(pool.ready) != 1 || pool.ready[0].JWT != "jwt-excluded" {
		t.Fatalf("expected excluded account to remain ready, got %+v", pool.ready)
	}
}

func TestSimplePoolStopFillTaskStopsFurtherRegistrations(t *testing.T) {
	t.Helper()

	firstCompleted := make(chan struct{})
	secondStarted := make(chan struct{})
	releaseSecond := make(chan struct{})
	registerCalls := 0
	pool := NewSimplePoolWithOptions(10, 0, func() (string, error) {
		registerCalls++
		switch registerCalls {
		case 1:
			close(firstCompleted)
			return "jwt-stop-1", nil
		case 2:
			close(secondStarted)
			<-releaseSecond
			return "jwt-stop-2", nil
		default:
			return fmt.Sprintf("jwt-stop-%d", registerCalls), nil
		}
	}, func(_ string) (int, error) {
		return 65, nil
	}, PoolOptions{})

	task := pool.StartFillTask(5)
	if task.ID == "" {
		t.Fatalf("expected task id, got empty")
	}

	select {
	case <-firstCompleted:
	case <-time.After(2 * time.Second):
		t.Fatalf("expected first registration to complete")
	}

	select {
	case <-secondStarted:
	case <-time.After(2 * time.Second):
		t.Fatalf("expected second registration to start")
	}

	stoppedTask, err := pool.StopFillTask(task.ID)
	if err != nil {
		t.Fatalf("expected stop to succeed, got %v", err)
	}
	if stoppedTask.ID != task.ID {
		t.Fatalf("expected stopped task id %q, got %+v", task.ID, stoppedTask)
	}
	if stoppedTask.Status != "stopping" && stoppedTask.Status != "stopped" {
		t.Fatalf("expected stop to return stopping/stopped, got %+v", stoppedTask)
	}

	close(releaseSecond)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status := pool.Status()
		if len(status.Tasks) == 0 {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		snapshot := status.Tasks[0]
		if snapshot.Status == "stopped" {
			if snapshot.Completed != 2 {
				t.Fatalf("expected stop to preserve the in-flight registration and halt before a third one, got %+v", snapshot)
			}
			if registerCalls != 2 {
				t.Fatalf("expected no registration calls after the in-flight one, got %d", registerCalls)
			}
			if status.ReadyCount != 2 {
				t.Fatalf("expected exactly two ready accounts after stop, got %+v", status)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("expected fill task to stop before deadline, latest status=%+v", pool.Status())
}

func TestSimplePoolPruneRemovesInvalidAccounts(t *testing.T) {
	t.Helper()

	pool := NewSimplePool(10, 0, func() (string, error) {
		return "", fmt.Errorf("no account")
	}, func(jwt string) (int, error) {
		if jwt == "dead" {
			return 0, nil
		}
		return 50, nil
	})
	pool.ready = []*Account{
		{JWT: "live", Quota: 65},
		{JWT: "dead", Quota: 65},
	}
	pool.reusable = []*Account{
		{JWT: "reuse", Quota: 10},
	}

	summary := pool.Prune()

	if summary.Checked != 3 {
		t.Fatalf("expected checked 3, got %+v", summary)
	}
	if summary.Removed != 1 {
		t.Fatalf("expected removed 1, got %+v", summary)
	}
	if summary.Remaining != 2 {
		t.Fatalf("expected remaining 2, got %+v", summary)
	}

	status := pool.Status()
	if status.TotalCount != 2 || status.PruneRemoved != 1 {
		t.Fatalf("unexpected pool status after prune: %+v", status)
	}
}

func TestSimplePoolPruneKeepsAccountsWhenQuotaRefreshFails(t *testing.T) {
	t.Helper()

	pool := NewSimplePool(10, 0, func() (string, error) {
		return "", fmt.Errorf("no account")
	}, func(jwt string) (int, error) {
		return 0, fmt.Errorf("upstream quota endpoint failed for %s", jwt)
	})
	pool.ready = []*Account{
		{JWT: "keep-me", Quota: 65},
	}

	summary := pool.Prune()

	if summary.Checked != 1 {
		t.Fatalf("expected checked 1, got %+v", summary)
	}
	if summary.Removed != 0 {
		t.Fatalf("expected removed 0 when quota refresh fails, got %+v", summary)
	}
	if summary.Remaining != 1 {
		t.Fatalf("expected remaining 1, got %+v", summary)
	}

	status := pool.Status()
	if status.TotalCount != 1 {
		t.Fatalf("expected account to remain in pool, got %+v", status)
	}
}

func TestSimplePoolPruneKeepsReadVisibilityWhileQuotaRefreshInFlight(t *testing.T) {
	t.Helper()

	entered := make(chan struct{})
	release := make(chan struct{})
	signalOnce := make(chan struct{}, 1)

	pool := NewSimplePool(10, 0, func() (string, error) {
		return "", fmt.Errorf("no account")
	}, func(jwt string) (int, error) {
		select {
		case signalOnce <- struct{}{}:
			close(entered)
		default:
		}
		<-release
		if jwt == "keep-low" {
			return 4, nil
		}
		return 50, nil
	})
	pool.ready = []*Account{
		{JWT: "keep-healthy", Quota: 65},
	}
	pool.reusable = []*Account{
		{JWT: "keep-low", Quota: 7},
	}

	done := make(chan PruneSummary, 1)
	go func() {
		done <- pool.Prune()
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatalf("expected prune quota refresh to start")
	}

	status := pool.Status()
	if status.TotalCount != 2 {
		t.Fatalf("expected status total count to stay visible during prune, got %+v", status)
	}

	rows := pool.AdminQuotaRows()
	if len(rows) != 2 {
		t.Fatalf("expected admin rows to stay visible during prune, got %+v", rows)
	}

	exported := pool.ExportAccounts()
	if len(exported) != 2 {
		t.Fatalf("expected export accounts to stay visible during prune, got %+v", exported)
	}

	close(release)

	select {
	case summary := <-done:
		if summary.Remaining != 2 {
			t.Fatalf("expected prune to keep both accounts, got %+v", summary)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("expected prune to finish after releasing quota refresh")
	}
}

func TestSimplePoolRestoreAccountsReplacesExistingPoolExactly(t *testing.T) {
	t.Helper()

	store := &memoryAccountStore{}
	pool := NewSimplePoolWithOptions(5, 0, func() (string, error) {
		return "", fmt.Errorf("no account")
	}, func(_ string) (int, error) {
		return 65, nil
	}, PoolOptions{
		Store: store,
	})
	pool.ready = []*Account{
		{JWT: "old-ready", Quota: 65},
	}
	pool.reusable = []*Account{
		{JWT: "old-reusable", Quota: 8},
	}

	result, err := pool.RestoreAccounts([]*Account{
		{JWT: "new-a", Quota: 65},
		{JWT: "new-b", Quota: 9},
		{JWT: "new-a", Quota: 12},
		{JWT: "", Quota: 65},
	})
	if err != nil {
		t.Fatalf("expected restore to succeed, got %v", err)
	}
	if result.Restored != 2 || result.Duplicates != 1 || result.Rejected != 1 || result.TotalCount != 2 {
		t.Fatalf("unexpected restore result %+v", result)
	}

	status := pool.Status()
	if status.TotalCount != 2 || status.ReadyCount != 2 || status.ReusableCount != 0 {
		t.Fatalf("expected exact replacement after restore, got %+v", status)
	}

	exported := pool.ExportAccounts()
	if len(exported) != 2 {
		t.Fatalf("expected exactly 2 exported accounts, got %+v", exported)
	}

	jwts := map[string]int{}
	for _, item := range exported {
		jwts[item.JWT] = item.Quota
	}
	if jwts["new-a"] != 65 || jwts["new-b"] != 9 {
		t.Fatalf("unexpected restored export set %+v", exported)
	}
	if store.saves == 0 {
		t.Fatalf("expected restore to persist snapshot")
	}
}

func TestSimplePoolImportAccountsDoesNotClampToTargetCount(t *testing.T) {
	t.Helper()

	pool := NewSimplePoolWithOptions(2, 0, func() (string, error) {
		return "", fmt.Errorf("no account")
	}, func(_ string) (int, error) {
		return 65, nil
	}, PoolOptions{
		LowWatermark: 1,
	})

	result := pool.ImportAccounts([]*Account{
		{JWT: "import-a", Quota: 65},
		{JWT: "import-b", Quota: 11},
		{JWT: "import-c", Quota: 9},
	})

	if result.Imported != 3 || result.Overflow != 0 || result.TotalCount != 3 {
		t.Fatalf("expected import beyond target count without overflow, got %+v", result)
	}

	status := pool.Status()
	if status.TargetCount != 2 || status.TotalCount != 3 {
		t.Fatalf("expected target count preserved and total count exceed it, got %+v", status)
	}
}

func TestSimplePoolRestoreAccountsDoesNotClampToTargetCount(t *testing.T) {
	t.Helper()

	pool := NewSimplePoolWithOptions(2, 0, func() (string, error) {
		return "", fmt.Errorf("no account")
	}, func(_ string) (int, error) {
		return 65, nil
	}, PoolOptions{
		LowWatermark: 1,
	})

	result, err := pool.RestoreAccounts([]*Account{
		{JWT: "restore-a", Quota: 65},
		{JWT: "restore-b", Quota: 8},
		{JWT: "restore-c", Quota: 7},
	})
	if err != nil {
		t.Fatalf("expected restore to succeed, got %v", err)
	}
	if result.Restored != 3 || result.Overflow != 0 || result.TotalCount != 3 {
		t.Fatalf("expected restore beyond target count without overflow, got %+v", result)
	}

	status := pool.Status()
	if status.TargetCount != 2 || status.TotalCount != 3 {
		t.Fatalf("expected target count preserved and restored total count exceed it, got %+v", status)
	}
}

func TestSimplePoolRestoreFromStoreDoesNotClampToTargetCount(t *testing.T) {
	t.Helper()

	store := &memoryAccountStore{
		accounts: []*Account{
			{JWT: "store-a", Quota: 65},
			{JWT: "store-b", Quota: 9},
			{JWT: "store-c", Quota: 7},
		},
	}

	pool := NewSimplePoolWithOptions(2, 0, func() (string, error) {
		return "", fmt.Errorf("no account")
	}, func(_ string) (int, error) {
		return 65, nil
	}, PoolOptions{
		Store:        store,
		LowWatermark: 1,
	})

	status := pool.Status()
	if status.TargetCount != 2 || status.TotalCount != 3 || status.RestoreLoaded != 3 {
		t.Fatalf("expected restore-from-store beyond target count, got %+v", status)
	}
}

func TestSimplePoolAutoFillStopsAtTargetSize(t *testing.T) {
	t.Helper()

	registerCalls := 0
	pool := NewSimplePoolWithOptions(4, 1, func() (string, error) {
		registerCalls++
		return fmt.Sprintf("jwt-%d", registerCalls), nil
	}, func(_ string) (int, error) {
		return 65, nil
	}, PoolOptions{
		LowWatermark: 2,
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status := pool.Status()
		if status.TotalCount == 4 && status.ActiveRegistrations == 0 && !status.AutoFillActive {
			time.Sleep(150 * time.Millisecond)
			if pool.Status().TotalCount != 4 {
				t.Fatalf("expected pool to stay at target size, got %+v", pool.Status())
			}
			if registerCalls != 4 {
				t.Fatalf("expected exactly 4 registrations, got %d", registerCalls)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("expected autofill to reach target size before deadline, latest status=%+v registerCalls=%d", pool.Status(), registerCalls)
}

func TestSimplePoolReleaseDropsInvalidAccountAndTriggersAutoRefill(t *testing.T) {
	t.Helper()

	registerCalls := 0
	pool := NewSimplePoolWithOptions(3, 1, func() (string, error) {
		registerCalls++
		return fmt.Sprintf("jwt-%d", registerCalls), nil
	}, func(jwt string) (int, error) {
		if jwt == "dead" {
			return 0, nil
		}
		return 65, nil
	}, PoolOptions{
		LowWatermark: 3,
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if pool.Status().TotalCount == 3 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pool.Status().TotalCount != 3 {
		t.Fatalf("expected initial autofill to reach 3, got %+v", pool.Status())
	}

	acc := pool.Acquire(1)
	acc.JWT = "dead"
	pool.Release(acc)

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status := pool.Status()
		if status.TotalCount == 3 && status.ActiveRegistrations == 0 && registerCalls >= 4 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("expected invalid release to trigger refill, latest status=%+v registerCalls=%d", pool.Status(), registerCalls)
}

func TestSimplePoolAcquirePrefersDrainCheapQuotaForCheapRequests(t *testing.T) {
	t.Helper()

	pool := NewSimplePool(10, 0, func() (string, error) {
		return "", fmt.Errorf("no account")
	}, func(_ string) (int, error) {
		return 65, nil
	})

	pool.ready = []*Account{
		{JWT: "jwt-premium", Quota: 65},
		{JWT: "jwt-high", Quota: 15},
		{JWT: "jwt-low", Quota: 7},
		{JWT: "jwt-drain", Quota: 1},
	}

	acc := pool.Acquire(1)
	if acc == nil {
		t.Fatalf("expected acquired account, got nil")
	}
	if acc.JWT != "jwt-drain" {
		t.Fatalf("expected 1-cost request to prefer drain-cheap account, got %+v", acc)
	}
}

func TestSimplePoolAcquirePreservesPremiumForMidCostRequests(t *testing.T) {
	t.Helper()

	pool := NewSimplePool(10, 0, func() (string, error) {
		return "", fmt.Errorf("no account")
	}, func(_ string) (int, error) {
		return 65, nil
	})

	pool.ready = []*Account{
		{JWT: "jwt-premium", Quota: 65},
		{JWT: "jwt-normal-high", Quota: 18},
		{JWT: "jwt-normal-low", Quota: 7},
	}

	acc := pool.Acquire(15)
	if acc == nil {
		t.Fatalf("expected acquired account, got nil")
	}
	if acc.JWT != "jwt-normal-high" {
		t.Fatalf("expected mid-cost request to avoid premium when 12-39 account exists, got %+v", acc)
	}
}

func TestSimplePoolAcquireUsesPremiumForHighCostRequests(t *testing.T) {
	t.Helper()

	pool := NewSimplePool(10, 0, func() (string, error) {
		return "", fmt.Errorf("no account")
	}, func(_ string) (int, error) {
		return 65, nil
	})

	pool.ready = []*Account{
		{JWT: "jwt-premium", Quota: 45},
		{JWT: "jwt-normal-high", Quota: 18},
		{JWT: "jwt-normal-low", Quota: 7},
	}

	acc := pool.Acquire(40)
	if acc == nil {
		t.Fatalf("expected acquired account, got nil")
	}
	if acc.JWT != "jwt-premium" {
		t.Fatalf("expected high-cost request to use premium account, got %+v", acc)
	}
}

func TestSimplePoolAcquireTextPrefersHigherQuotaForCheapRequests(t *testing.T) {
	t.Helper()

	pool := NewSimplePool(10, 0, func() (string, error) {
		return "", fmt.Errorf("no account")
	}, func(_ string) (int, error) {
		return 65, nil
	})

	pool.ready = []*Account{
		{JWT: "jwt-premium", Quota: 65},
		{JWT: "jwt-high", Quota: 15},
		{JWT: "jwt-low", Quota: 7},
		{JWT: "jwt-drain", Quota: 1},
	}

	acc := pool.AcquireText("gpt-4.1-nano", 1)
	if acc == nil {
		t.Fatalf("expected acquired account, got nil")
	}
	if acc.JWT != "jwt-high" {
		t.Fatalf("expected cheap text request to prefer stable mid-tier account, got %+v", acc)
	}
}

func TestSimplePoolAcquireTextSkipsUnsupportedModelAccount(t *testing.T) {
	t.Helper()

	pool := NewSimplePool(10, 0, func() (string, error) {
		return "", fmt.Errorf("no account")
	}, func(_ string) (int, error) {
		return 65, nil
	})

	unsupported := &Account{JWT: "jwt-blocked", Quota: 15}
	healthy := &Account{JWT: "jwt-healthy", Quota: 15}
	pool.ready = []*Account{unsupported, healthy}
	pool.MarkTextModelUnsupported(unsupported.JWT, "gpt-4.1")

	acc := pool.AcquireText("gpt-4.1", 3)
	if acc == nil {
		t.Fatalf("expected acquired account, got nil")
	}
	if acc.JWT != "jwt-healthy" {
		t.Fatalf("expected model-blocked account to be skipped, got %+v", acc)
	}
}

func TestSimplePoolObserveSlowAccountMarksItAndDeprioritizesIt(t *testing.T) {
	t.Helper()

	pool := NewSimplePool(10, 0, func() (string, error) {
		return "", fmt.Errorf("no account")
	}, func(_ string) (int, error) {
		return 65, nil
	})

	slow := &Account{JWT: "jwt-slow", Quota: 18}
	fast := &Account{JWT: "jwt-fast", Quota: 18}
	pool.ready = []*Account{slow, fast}

	pool.ObserveTextResult(slow.JWT, 9*time.Second, nil)

	rows := pool.AdminQuotaRows()
	rowByJWT := map[string]AdminQuotaRow{}
	for _, row := range rows {
		rowByJWT[row.JWT] = row
	}
	if rowByJWT["jwt-slow"].PerfLabel != "慢号" {
		t.Fatalf("expected slow account to be marked 慢号, got %+v", rowByJWT["jwt-slow"])
	}
	if rowByJWT["jwt-slow"].LastLatencyMs < 9000 {
		t.Fatalf("expected slow account latency to be recorded, got %+v", rowByJWT["jwt-slow"])
	}

	acc := pool.Acquire(1)
	if acc == nil || acc.JWT != "jwt-fast" {
		t.Fatalf("expected healthy account to be preferred over marked slow account, got %+v", acc)
	}
}

func TestSimplePoolObserveTimeoutIsolatesAccountTemporarily(t *testing.T) {
	t.Helper()

	pool := NewSimplePool(10, 0, func() (string, error) {
		return "", fmt.Errorf("no account")
	}, func(_ string) (int, error) {
		return 65, nil
	})

	slow := &Account{JWT: "jwt-timeout", Quota: 18}
	fast := &Account{JWT: "jwt-fast", Quota: 18}
	pool.ready = []*Account{slow, fast}

	pool.ObserveTextResult(slow.JWT, 18*time.Second, fakeTimeoutError{})

	rows := pool.AdminQuotaRows()
	rowByJWT := map[string]AdminQuotaRow{}
	for _, row := range rows {
		rowByJWT[row.JWT] = row
	}
	if rowByJWT["jwt-timeout"].PerfLabel != "超时隔离" {
		t.Fatalf("expected timeout account to be marked 超时隔离, got %+v", rowByJWT["jwt-timeout"])
	}
	if rowByJWT["jwt-timeout"].DisabledUntil == nil || rowByJWT["jwt-timeout"].DisabledUntil.IsZero() {
		t.Fatalf("expected timeout account to have disabled_until, got %+v", rowByJWT["jwt-timeout"])
	}

	acc := pool.Acquire(1)
	if acc == nil || acc.JWT != "jwt-fast" {
		t.Fatalf("expected isolated timeout account to be skipped, got %+v", acc)
	}
}

func TestSimplePoolRegistrationLoopRespectsSuccessInterval(t *testing.T) {
	t.Helper()

	registerCalls := 0
	_ = NewSimplePoolWithOptions(3, 1, func() (string, error) {
		registerCalls++
		return fmt.Sprintf("jwt-%d", registerCalls), nil
	}, func(_ string) (int, error) {
		return 65, nil
	}, PoolOptions{
		RegistrationInterval: 250 * time.Millisecond,
	})

	time.Sleep(120 * time.Millisecond)
	if registerCalls != 1 {
		t.Fatalf("expected exactly one registration before success interval elapsed, got %d", registerCalls)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if registerCalls >= 2 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("expected registration loop to resume after success interval, got %d", registerCalls)
}

func TestSimplePoolRegistrationLoopRespectsFailureBackoff(t *testing.T) {
	t.Helper()

	registerCalls := 0
	_ = NewSimplePoolWithOptions(3, 1, func() (string, error) {
		registerCalls++
		return "", fmt.Errorf("temporary failure")
	}, func(_ string) (int, error) {
		return 65, nil
	}, PoolOptions{
		FailureBackoff: 250 * time.Millisecond,
	})

	time.Sleep(120 * time.Millisecond)
	if registerCalls != 1 {
		t.Fatalf("expected exactly one failed registration before failure backoff elapsed, got %d", registerCalls)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if registerCalls >= 2 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("expected registration loop to retry after failure backoff, got %d", registerCalls)
}

func TestSimplePoolRegistrationLoopUsesExponentialFailureBackoffAndTracksError(t *testing.T) {
	t.Helper()

	registerCalls := 0
	pool := NewSimplePoolWithOptions(3, 1, func() (string, error) {
		registerCalls++
		return "", fmt.Errorf("Access denied: registration blocked temporarily for this IP.")
	}, func(_ string) (int, error) {
		return 65, nil
	}, PoolOptions{
		FailureBackoff:    40 * time.Millisecond,
		MaxFailureBackoff: 160 * time.Millisecond,
	})

	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		if registerCalls >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if registerCalls < 2 {
		t.Fatalf("expected at least 2 registration attempts, got %d", registerCalls)
	}

	status := pool.Status()
	if status.FailureStreak < 2 {
		t.Fatalf("expected failure streak to be tracked, got %+v", status)
	}
	if status.LastRegistrationError == "" {
		t.Fatalf("expected last registration error to be exposed, got %+v", status)
	}
	if status.NextRetryAt == nil || status.NextRetryAt.IsZero() {
		t.Fatalf("expected next retry time to be exposed, got %+v", status)
	}

	firstSleepObservedAt := time.Now()
	attemptsAfterTwo := registerCalls
	time.Sleep(60 * time.Millisecond)
	if registerCalls != attemptsAfterTwo {
		t.Fatalf("expected exponential backoff to delay third attempt longer than base interval; attempts grew from %d to %d within %s", attemptsAfterTwo, registerCalls, time.Since(firstSleepObservedAt))
	}

	deadline = time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if registerCalls >= 3 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("expected third attempt after exponential backoff, got %d", registerCalls)
}

func TestSimplePoolRestoresAccountsFromStoreBeforeAutoFill(t *testing.T) {
	t.Helper()

	store := &memoryAccountStore{
		accounts: []*Account{
			{JWT: "jwt-restore-1", Quota: 65},
			{JWT: "jwt-restore-2", Quota: 32},
		},
	}

	registerCalls := 0
	pool := NewSimplePoolWithOptions(2, 1, func() (string, error) {
		registerCalls++
		return fmt.Sprintf("jwt-new-%d", registerCalls), nil
	}, func(_ string) (int, error) {
		return 65, nil
	}, PoolOptions{
		Store:                store,
		RegistrationInterval: time.Hour,
	})

	time.Sleep(80 * time.Millisecond)
	status := pool.Status()
	if status.TotalCount != 2 {
		t.Fatalf("expected restored pool size 2, got %+v", status)
	}
	if registerCalls != 0 {
		t.Fatalf("expected no auto registration after restore filled target, got %d", registerCalls)
	}
	if !status.PersistenceEnabled || status.PersistencePath != "memory://pool" {
		t.Fatalf("expected persistence metadata in status, got %+v", status)
	}
	if status.RestoreLoaded != 2 || status.RestoreRejected != 0 {
		t.Fatalf("expected restore counters 2/0, got %+v", status)
	}
}

func TestSimplePoolImportAndPrunePersistAccounts(t *testing.T) {
	t.Helper()

	store := &memoryAccountStore{}
	pool := NewSimplePoolWithOptions(10, 0, func() (string, error) {
		return "", fmt.Errorf("no account")
	}, func(jwt string) (int, error) {
		if jwt == "dead" {
			return 0, nil
		}
		return 65, nil
	}, PoolOptions{
		Store: store,
	})

	result := pool.ImportAccounts([]*Account{
		{JWT: "live", Quota: 65},
		{JWT: "dead", Quota: 65},
	})
	if result.Imported != 2 {
		t.Fatalf("expected 2 imports, got %+v", result)
	}
	if store.saves == 0 || len(store.accounts) != 2 {
		t.Fatalf("expected imports to be persisted, saves=%d accounts=%+v", store.saves, store.accounts)
	}

	summary := pool.Prune()
	if summary.Removed != 1 {
		t.Fatalf("expected prune to remove invalid account, got %+v", summary)
	}
	if len(store.accounts) != 1 || store.accounts[0].JWT != "live" {
		t.Fatalf("expected persisted store to keep only live account, got %+v", store.accounts)
	}
}

func TestSimplePoolPruneKeepsOneQuotaAccount(t *testing.T) {
	t.Helper()

	store := &memoryAccountStore{}
	pool := NewSimplePoolWithOptions(10, 0, func() (string, error) {
		return "", fmt.Errorf("no account")
	}, func(jwt string) (int, error) {
		if jwt == "one-left" {
			return 1, nil
		}
		return 65, nil
	}, PoolOptions{
		Store: store,
	})

	result := pool.ImportAccounts([]*Account{
		{JWT: "one-left", Quota: 65},
	})
	if result.Imported != 1 {
		t.Fatalf("expected 1 import, got %+v", result)
	}

	summary := pool.Prune()
	if summary.Removed != 0 {
		t.Fatalf("expected prune to keep quota=1 account, got %+v", summary)
	}
	if len(store.accounts) != 1 || store.accounts[0].JWT != "one-left" || store.accounts[0].Quota != 1 {
		t.Fatalf("expected persisted store to keep quota=1 account, got %+v", store.accounts)
	}
}

func TestSimplePoolReleaseInvalidAccountUpdatesPersistence(t *testing.T) {
	t.Helper()

	store := &memoryAccountStore{
		accounts: []*Account{
			{JWT: "live", Quota: 65},
		},
	}
	pool := NewSimplePoolWithOptions(1, 0, func() (string, error) {
		return "", fmt.Errorf("no account")
	}, func(_ string) (int, error) {
		return 0, nil
	}, PoolOptions{
		Store: store,
	})

	acc := pool.Acquire(1)
	if acc == nil {
		t.Fatalf("expected acquired account, got nil")
	}
	pool.Release(acc)

	if len(store.accounts) != 0 {
		t.Fatalf("expected invalid released account to be removed from persisted store, got %+v", store.accounts)
	}
	status := pool.Status()
	if status.PersistedCount != 0 {
		t.Fatalf("expected persisted count 0 after invalid release, got %+v", status)
	}
}

func TestSimplePoolReleaseKeepsOneQuotaAccountReusableAndExportable(t *testing.T) {
	t.Helper()

	store := &memoryAccountStore{
		accounts: []*Account{
			{JWT: "live", Quota: 65},
		},
	}
	pool := NewSimplePoolWithOptions(1, 0, func() (string, error) {
		return "", fmt.Errorf("no account")
	}, func(_ string) (int, error) {
		return 1, nil
	}, PoolOptions{
		Store: store,
	})

	acc := pool.Acquire(1)
	if acc == nil {
		t.Fatalf("expected acquired account, got nil")
	}
	pool.Release(acc)

	reused := pool.Acquire(1)
	if reused == nil || reused.JWT != "live" || reused.Quota != 1 {
		t.Fatalf("expected quota=1 account to remain reusable, got %+v", reused)
	}
	pool.Release(reused)

	exported := pool.ExportAccounts()
	if len(exported) != 1 || exported[0].JWT != "live" || exported[0].Quota != 1 {
		t.Fatalf("expected quota=1 account to remain exportable, got %+v", exported)
	}
}

func TestSimplePoolReleaseRetires401Account(t *testing.T) {
	t.Helper()

	store := &memoryAccountStore{
		accounts: []*Account{
			{JWT: "expired", Quota: 65},
		},
	}
	pool := NewSimplePoolWithOptions(1, 0, func() (string, error) {
		return "", fmt.Errorf("no account")
	}, func(_ string) (int, error) {
		return 0, &protocol.UpstreamError{
			StatusCode: http.StatusUnauthorized,
			Message:    "expired",
		}
	}, PoolOptions{
		Store: store,
	})

	acc := pool.Acquire(1)
	if acc == nil {
		t.Fatalf("expected acquired account, got nil")
	}
	pool.Release(acc)

	if len(store.accounts) != 0 {
		t.Fatalf("expected 401 account to be retired from persisted store, got %+v", store.accounts)
	}
	if pool.Status().TotalCount != 0 {
		t.Fatalf("expected 401 account to leave live pool, got %+v", pool.Status())
	}
}

func TestSimplePoolRestoreAcceptsOneQuotaAccount(t *testing.T) {
	t.Helper()

	pool := NewSimplePool(10, 0, func() (string, error) {
		return "", fmt.Errorf("no account")
	}, func(_ string) (int, error) {
		return 65, nil
	})

	result, err := pool.RestoreAccounts([]*Account{
		{JWT: "one-left", Quota: 1},
		{JWT: "dead", Quota: 0},
	})
	if err != nil {
		t.Fatalf("expected restore to succeed, got %v", err)
	}
	if result.Restored != 1 || result.Rejected != 1 {
		t.Fatalf("expected restore to keep quota=1 and reject quota=0, got %+v", result)
	}
	if pool.Status().TotalCount != 1 {
		t.Fatalf("expected restored total count 1, got %+v", pool.Status())
	}
}

func TestSimplePoolExportAccountsReturnsSnapshotAcrossReadyReusableAndBorrowed(t *testing.T) {
	t.Helper()

	pool := NewSimplePool(10, 0, func() (string, error) {
		return "", fmt.Errorf("no account")
	}, func(_ string) (int, error) {
		return 65, nil
	})
	pool.ready = []*Account{
		{JWT: "jwt-ready", Quota: 65},
	}
	pool.reusable = []*Account{
		{JWT: "jwt-reuse", Quota: 17},
	}
	borrowed := &Account{JWT: "jwt-borrowed", Quota: 33}
	pool.borrowed[borrowed] = "jwt-borrowed"

	exported := pool.ExportAccounts()
	if len(exported) != 3 {
		t.Fatalf("expected 3 exported accounts, got %+v", exported)
	}

	seen := make(map[string]int, len(exported))
	for _, account := range exported {
		seen[account.JWT] = account.Quota
	}
	if seen["jwt-ready"] != 65 || seen["jwt-reuse"] != 17 || seen["jwt-borrowed"] != 33 {
		t.Fatalf("unexpected exported snapshot: %+v", exported)
	}
}

func TestSimplePoolStatusReportsTargetAndLowQuotaCount(t *testing.T) {
	t.Helper()

	pool := NewSimplePool(10, 0, func() (string, error) {
		return "", fmt.Errorf("no account")
	}, func(_ string) (int, error) {
		return 65, nil
	})
	pool.ready = []*Account{
		{JWT: "jwt-healthy", Quota: 65},
		{JWT: "jwt-low", Quota: 6},
	}
	pool.reusable = []*Account{
		{JWT: "jwt-low-2", Quota: 9},
	}

	status := pool.Status()
	if status.TargetCount != 10 {
		t.Fatalf("expected target count 10, got %+v", status)
	}
	if status.LowQuotaCount != 2 {
		t.Fatalf("expected low quota count 2, got %+v", status)
	}
}

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
	borrowedDuplicate := &Account{JWT: "shadow-account", Quota: 99}

	pool.ready = []*Account{ready}
	pool.reusable = []*Account{reuse}
	pool.borrowed = map[*Account]string{
		borrowed:          "jwt-borrowed",
		borrowedDuplicate: "jwt-ready",
	}

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
	if seen["jwt-ready"].Quota != 18 {
		t.Fatalf("expected dedupe to keep the first-seen ready row, got %+v", seen["jwt-ready"])
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
