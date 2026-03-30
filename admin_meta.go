package main

import (
	"runtime/debug"
	"strings"
	"time"
)

type ExportedAccount struct {
	JWT   string `json:"jwt"`
	Quota int    `json:"quota"`
}

type AdminMeta struct {
	InstanceName         string     `json:"instance_name"`
	ServiceLabel         string     `json:"service_label,omitempty"`
	DeploySource         string     `json:"deploy_source,omitempty"`
	ImageRef             string     `json:"image_ref,omitempty"`
	PublicBaseURL        string     `json:"public_base_url"`
	PrimaryPublicBaseURL string     `json:"primary_public_base_url"`
	IsPrimaryTarget      bool       `json:"is_primary_target"`
	Version              string     `json:"version"`
	LastMigrationAt      *time.Time `json:"last_migration_at,omitempty"`
}

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

type AdminCatalog struct {
	LowQuotaThreshold int              `json:"low_quota_threshold"`
	TextModels        []AdminModelInfo `json:"text_models"`
	ImageModels       []AdminModelInfo `json:"image_models"`
}

type AdminModelInfo struct {
	ID            string   `json:"id"`
	Cost          int      `json:"cost"`
	Category      string   `json:"category"`
	MinimumTier   string   `json:"minimum_tier,omitempty"`
	Internet      bool     `json:"internet,omitempty"`
	SupportsEdit  bool     `json:"supports_edit,omitempty"`
	SupportsMerge bool     `json:"supports_merge,omitempty"`
	EditAccess    string   `json:"edit_access,omitempty"`
	RuntimeNote   string   `json:"runtime_note,omitempty"`
	AccessTiers   []string `json:"access_tiers,omitempty"`
	EditCost      int      `json:"edit_cost,omitempty"`
	MergeCostNote string   `json:"merge_cost_note,omitempty"`
	RouteAdvice   string   `json:"route_advice,omitempty"`
}

func buildVersionString() string {
	info, ok := debug.ReadBuildInfo()
	if !ok || info == nil {
		return "unknown"
	}

	revision := ""
	modified := ""
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = strings.TrimSpace(setting.Value)
		case "vcs.modified":
			modified = strings.TrimSpace(setting.Value)
		}
	}
	if revision != "" {
		if modified == "true" {
			return revision + "-dirty"
		}
		return revision
	}

	version := strings.TrimSpace(info.Main.Version)
	if version == "" {
		return "unknown"
	}
	return version
}
