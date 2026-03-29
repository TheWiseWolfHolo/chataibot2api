package main

import (
	"fmt"
	"testing"
	"time"
)

func TestSimplePoolStartFillTaskCreatesAccountsAsynchronously(t *testing.T) {
	t.Helper()

	registerCalls := 0
	pool := NewSimplePool(10, 0, func() (string, error) {
		registerCalls++
		return "jwt-fill-" + time.Now().Format("150405.000000"), nil
	}, func(_ string) int {
		return 65
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

func TestSimplePoolPruneRemovesInvalidAccounts(t *testing.T) {
	t.Helper()

	pool := NewSimplePool(10, 0, func() (string, error) {
		return "", fmt.Errorf("no account")
	}, func(jwt string) int {
		if jwt == "dead" {
			return 0
		}
		return 50
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

func TestSimplePoolAutoFillStopsAtTargetSize(t *testing.T) {
	t.Helper()

	registerCalls := 0
	pool := NewSimplePoolWithOptions(4, 1, func() (string, error) {
		registerCalls++
		return fmt.Sprintf("jwt-%d", registerCalls), nil
	}, func(_ string) int {
		return 65
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
	}, func(jwt string) int {
		if jwt == "dead" {
			return 0
		}
		return 65
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

func TestSimplePoolRegistrationLoopRespectsSuccessInterval(t *testing.T) {
	t.Helper()

	registerCalls := 0
	_ = NewSimplePoolWithOptions(3, 1, func() (string, error) {
		registerCalls++
		return fmt.Sprintf("jwt-%d", registerCalls), nil
	}, func(_ string) int {
		return 65
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
	}, func(_ string) int {
		return 65
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
	}, func(_ string) int {
		return 65
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
