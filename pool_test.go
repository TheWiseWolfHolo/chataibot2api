package main

import (
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
