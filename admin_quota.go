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
