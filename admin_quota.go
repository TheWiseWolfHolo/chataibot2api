package main

import (
	"sort"
	"strings"
	"time"
)

type AdminQuotaRow struct {
	JWT           string     `json:"jwt"`
	Quota         int        `json:"quota"`
	Status        string     `json:"status"`
	PoolBucket    string     `json:"pool_bucket"`
	PerfLabel     string     `json:"perf_label,omitempty"`
	LastLatencyMs int        `json:"last_latency_ms,omitempty"`
	DisabledUntil *time.Time `json:"disabled_until,omitempty"`
	LastCheckedAt *time.Time `json:"last_checked_at,omitempty"`
}

type AdminQuotaSummary struct {
	TotalCount     int `json:"total_count"`
	TotalQuota     int `json:"total_quota"`
	LowQuotaCount  int `json:"low_quota_count"`
	NearEmptyCount int `json:"near_empty_count"`
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
	case quota < lowQuotaThreshold:
		return "low"
	default:
		return "healthy"
	}
}

func adminQuotaStatusOrder(status string) int {
	switch strings.TrimSpace(status) {
	case "near-empty":
		return 0
	case "low":
		return 1
	case "healthy":
		return 2
	case "probe-error":
		return 3
	default:
		return 4
	}
}

func (a *App) AdminQuotaSnapshot() AdminQuotaSnapshot {
	rows := make([]AdminQuotaRow, 0)
	if a != nil && a.pool != nil {
		rows = append(rows, a.pool.AdminQuotaRows()...)
	}
	if len(rows) == 0 && a != nil && a.pool != nil {
		exported := a.pool.ExportAccounts()
		if len(exported) > 0 {
			rows = make([]AdminQuotaRow, 0, len(exported))
			for _, account := range exported {
				jwt := strings.TrimSpace(account.JWT)
				if jwt == "" {
					continue
				}
				rows = append(rows, AdminQuotaRow{
					JWT:        jwt,
					Quota:      account.Quota,
					Status:     deriveAdminQuotaStatus(account.Quota),
					PoolBucket: "persisted",
				})
			}
		}
	}

	for i := range rows {
		rows[i].Status = deriveAdminQuotaStatus(rows[i].Quota)
	}

	sort.Slice(rows, func(i, j int) bool {
		left := adminQuotaStatusOrder(rows[i].Status)
		right := adminQuotaStatusOrder(rows[j].Status)
		if left != right {
			return left < right
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
		if row.Quota >= 2 && row.Quota < lowQuotaThreshold {
			summary.LowQuotaCount++
		}
		if row.Quota < 5 {
			summary.NearEmptyCount++
		}
	}

	return AdminQuotaSnapshot{
		Summary: summary,
		Rows:    rows,
	}
}

func (a *App) ProbeQuota(jwts []string) AdminQuotaProbeResponse {
	checkedAt := time.Now().UTC()
	if a != nil && a.now != nil {
		checkedAt = a.now().UTC()
	}

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

		if a == nil || a.backend == nil {
			results = append(results, AdminQuotaProbeItem{
				JWT:   jwt,
				OK:    false,
				Error: "backend is not configured",
			})
			continue
		}

		quota, err := a.backend.GetCount(jwt)
		if err != nil {
			results = append(results, AdminQuotaProbeItem{
				JWT:   jwt,
				OK:    false,
				Error: err.Error(),
			})
			continue
		}

		results = append(results, AdminQuotaProbeItem{
			JWT:    jwt,
			Quota:  quota,
			Status: deriveAdminQuotaStatus(quota),
			OK:     true,
		})
	}

	return AdminQuotaProbeResponse{
		CheckedAt: checkedAt,
		Results:   results,
	}
}
