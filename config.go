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
	PoolStorePath            string
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
	InstanceName             string
	ServiceLabel             string
	DeploySource             string
	ImageRef                 string
	PublicBaseURL            string
	PrimaryPublicBaseURL     string
	LegacyPoolExportBaseURL  string
}

func LoadConfig(args []string, getenv func(string) string) (Config, error) {
	fs := flag.NewFlagSet("chataibot2api", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	poolFlag := fs.Int("pool", 10, "指定号池数量")
	poolWorkerCountFlag := fs.Int("pool-worker-count", 1, "号池注册并发数")
	poolLowWatermarkFlag := fs.Int("pool-low-watermark", 0, "号池自动补号低水位，达到后自动补回目标池大小")
	poolStorePathFlag := fs.String("pool-store-path", "", "号池持久化文件路径")
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
	instanceNameFlag := fs.String("instance-name", "", "实例标识")
	serviceLabelFlag := fs.String("service-label", "", "部署平台中的服务标识")
	deploySourceFlag := fs.String("deploy-source", "", "部署来源，如 zeabur-live / ghcr-preview")
	imageRefFlag := fs.String("image-ref", "", "当前实例镜像标识")
	publicBaseURLFlag := fs.String("public-base-url", "", "当前实例对外地址")
	primaryPublicBaseURLFlag := fs.String("primary-public-base-url", "", "最终主域名")
	legacyPoolExportBaseURLFlag := fs.String("legacy-pool-export-base-url", "", "旧实例池导出地址")

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}

	cfg := Config{
		PoolSize:                 *poolFlag,
		PoolWorkerCount:          *poolWorkerCountFlag,
		PoolLowWatermark:         *poolLowWatermarkFlag,
		PoolStorePath:            strings.TrimSpace(*poolStorePathFlag),
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
		InstanceName:             strings.TrimSpace(*instanceNameFlag),
		ServiceLabel:             strings.TrimSpace(*serviceLabelFlag),
		DeploySource:             strings.TrimSpace(*deploySourceFlag),
		ImageRef:                 strings.TrimSpace(*imageRefFlag),
		PublicBaseURL:            strings.TrimSpace(*publicBaseURLFlag),
		PrimaryPublicBaseURL:     strings.TrimSpace(*primaryPublicBaseURLFlag),
		LegacyPoolExportBaseURL:  strings.TrimSpace(*legacyPoolExportBaseURLFlag),
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
	if value := strings.TrimSpace(getenv("POOL_STORE_PATH")); value != "" {
		cfg.PoolStorePath = value
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
	if value := strings.TrimSpace(getenv("INSTANCE_NAME")); value != "" {
		cfg.InstanceName = value
	}
	if value := strings.TrimSpace(getenv("SERVICE_LABEL")); value != "" {
		cfg.ServiceLabel = value
	}
	if value := strings.TrimSpace(getenv("DEPLOY_SOURCE")); value != "" {
		cfg.DeploySource = value
	}
	if value := strings.TrimSpace(getenv("IMAGE_REF")); value != "" {
		cfg.ImageRef = value
	}
	if value := strings.TrimSpace(getenv("PUBLIC_BASE_URL")); value != "" {
		cfg.PublicBaseURL = value
	}
	if value := strings.TrimSpace(getenv("PRIMARY_PUBLIC_BASE_URL")); value != "" {
		cfg.PrimaryPublicBaseURL = value
	}
	if value := strings.TrimSpace(getenv("LEGACY_POOL_EXPORT_BASE_URL")); value != "" {
		cfg.LegacyPoolExportBaseURL = value
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
