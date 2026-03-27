package main

import (
	"fmt"
	"testing"
	"time"
)

func TestSimplePoolStartFillTaskCreatesAccountsAsynchronously(t *testing.T) {
	t.Helper()

	registerCalls := 0
	pool := NewSimplePool(10, 0, func() (bool, string) {
		registerCalls++
		return true, "jwt-fill-" + time.Now().Format("150405.000000")
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

	pool := NewSimplePool(10, 0, func() (bool, string) {
		return false, ""
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
	pool := NewSimplePoolWithOptions(4, 1, func() (bool, string) {
		registerCalls++
		return true, fmt.Sprintf("jwt-%d", registerCalls)
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
	pool := NewSimplePoolWithOptions(3, 1, func() (bool, string) {
		registerCalls++
		return true, fmt.Sprintf("jwt-%d", registerCalls)
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
