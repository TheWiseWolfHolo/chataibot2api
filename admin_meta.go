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
