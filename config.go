package main

import (
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
)

type Config struct {
	PoolSize                 int
	PoolWorkerCount          int
	PoolLowWatermark         int
	PoolPruneIntervalSeconds int
	PoolRegistrationInterval int
	PoolFailureBackoff       int
	PoolFailureBackoffMax    int
	Port                     int
	MailAPIBaseURL           string
	MailDomain               string
	MailAdminToken           string
	APIBearerToken           string
	AdminToken               string
}

func LoadConfig(args []string, getenv func(string) string) (Config, error) {
	fs := flag.NewFlagSet("chataibot2api", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	poolFlag := fs.Int("pool", 10, "指定号池数量")
	poolWorkerCountFlag := fs.Int("pool-worker-count", 1, "号池注册并发数")
	poolLowWatermarkFlag := fs.Int("pool-low-watermark", 0, "号池自动补号低水位，达到后自动补回目标池大小")
	poolPruneIntervalFlag := fs.Int("pool-prune-interval", 300, "号池自动清理失效账号间隔（秒）")
	poolRegistrationIntervalFlag := fs.Int("pool-registration-interval", 15, "每次注册成功后的最小间隔（秒）")
	poolFailureBackoffFlag := fs.Int("pool-failure-backoff", 60, "注册失败后的退避时间（秒）")
	poolFailureBackoffMaxFlag := fs.Int("pool-failure-backoff-max", 900, "注册失败退避最大值（秒）")
	portFlag := fs.Int("port", 8080, "服务端口")
	mailAPIFlag := fs.String("api", "", "自建邮箱 API 地址")
	mailDomainFlag := fs.String("domain", "", "自建邮箱域名")
	mailTokenFlag := fs.String("token", "", "自建邮箱管理员密码")
	bearerTokenFlag := fs.String("bearer-token", "", "API 鉴权 Bearer Token")
	adminTokenFlag := fs.String("admin-token", "", "管理 API 鉴权 Bearer Token")

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}

	cfg := Config{
		PoolSize:                 *poolFlag,
		PoolWorkerCount:          *poolWorkerCountFlag,
		PoolLowWatermark:         *poolLowWatermarkFlag,
		PoolPruneIntervalSeconds: *poolPruneIntervalFlag,
		PoolRegistrationInterval: *poolRegistrationIntervalFlag,
		PoolFailureBackoff:       *poolFailureBackoffFlag,
		PoolFailureBackoffMax:    *poolFailureBackoffMaxFlag,
		Port:                     *portFlag,
		MailAPIBaseURL:           strings.TrimSpace(*mailAPIFlag),
		MailDomain:               strings.TrimSpace(*mailDomainFlag),
		MailAdminToken:           strings.TrimSpace(*mailTokenFlag),
		APIBearerToken:           strings.TrimSpace(*bearerTokenFlag),
		AdminToken:               strings.TrimSpace(*adminTokenFlag),
	}

	if value := strings.TrimSpace(getenv("POOL_SIZE")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, fmt.Errorf("invalid POOL_SIZE %q: %w", value, err)
		}
		cfg.PoolSize = parsed
	}
	if value := strings.TrimSpace(getenv("POOL_WORKER_COUNT")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, fmt.Errorf("invalid POOL_WORKER_COUNT %q: %w", value, err)
		}
		cfg.PoolWorkerCount = parsed
	}
	if value := strings.TrimSpace(getenv("POOL_LOW_WATERMARK")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, fmt.Errorf("invalid POOL_LOW_WATERMARK %q: %w", value, err)
		}
		cfg.PoolLowWatermark = parsed
	}
	if value := strings.TrimSpace(getenv("POOL_PRUNE_INTERVAL_SECONDS")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, fmt.Errorf("invalid POOL_PRUNE_INTERVAL_SECONDS %q: %w", value, err)
		}
		cfg.PoolPruneIntervalSeconds = parsed
	}
	if value := strings.TrimSpace(getenv("POOL_REGISTRATION_INTERVAL_SECONDS")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, fmt.Errorf("invalid POOL_REGISTRATION_INTERVAL_SECONDS %q: %w", value, err)
		}
		cfg.PoolRegistrationInterval = parsed
	}
	if value := strings.TrimSpace(getenv("POOL_FAILURE_BACKOFF_SECONDS")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, fmt.Errorf("invalid POOL_FAILURE_BACKOFF_SECONDS %q: %w", value, err)
		}
		cfg.PoolFailureBackoff = parsed
	}
	if value := strings.TrimSpace(getenv("POOL_FAILURE_BACKOFF_MAX_SECONDS")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, fmt.Errorf("invalid POOL_FAILURE_BACKOFF_MAX_SECONDS %q: %w", value, err)
		}
		cfg.PoolFailureBackoffMax = parsed
	}

	if value := strings.TrimSpace(getenv("PORT")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, fmt.Errorf("invalid PORT %q: %w", value, err)
		}
		cfg.Port = parsed
	}

	if value := strings.TrimSpace(getenv("MAIL_API_BASE_URL")); value != "" {
		cfg.MailAPIBaseURL = value
	}
	if value := strings.TrimSpace(getenv("MAIL_DOMAIN")); value != "" {
		cfg.MailDomain = value
	}
	if value := strings.TrimSpace(getenv("MAIL_ADMIN_TOKEN")); value != "" {
		cfg.MailAdminToken = value
	}
	if value := strings.TrimSpace(getenv("API_BEARER_TOKEN")); value != "" {
		cfg.APIBearerToken = value
	}
	if value := strings.TrimSpace(getenv("ADMIN_TOKEN")); value != "" {
		cfg.AdminToken = value
	}

	missing := make([]string, 0, 5)
	if cfg.MailAPIBaseURL == "" {
		missing = append(missing, "MAIL_API_BASE_URL")
	}
	if cfg.MailDomain == "" {
		missing = append(missing, "MAIL_DOMAIN")
	}
	if cfg.MailAdminToken == "" {
		missing = append(missing, "MAIL_ADMIN_TOKEN")
	}
	if cfg.APIBearerToken == "" {
		missing = append(missing, "API_BEARER_TOKEN")
	}
	if cfg.AdminToken == "" {
		missing = append(missing, "ADMIN_TOKEN")
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required config: %s", strings.Join(missing, ", "))
	}

	return cfg, nil
}
