package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"chataibot2api/protocol"
)

type ImageBackend interface {
	UpdateUserSettings(jwtToken, aspectRatio string) bool
	GenerateImage(prompt, provider, version, jwtToken string) (string, error)
	EditImage(prompt, base64Data, model, jwtToken string) (string, error)
	MergeImage(prompt string, base64Images []string, mergeType, jwtToken string) (string, error)
	CreateChatContext(model, title, jwtToken string) (int, error)
	SendTextMessage(req UpstreamTextMessageRequest, jwtToken string) (TextCompletionResult, error)
	StreamTextMessage(ctx context.Context, req UpstreamTextMessageRequest, jwtToken string, emit func(TextStreamEvent) error) (TextCompletionResult, error)
	GetCount(jwtToken string) (int, error)
}

type PoolManager interface {
	Acquire(cost int) *Account
	Release(acc *Account)
	Status() PoolStatus
	AdminQuotaRows() []AdminQuotaRow
	StartFillTask(count int) FillTaskSnapshot
	StopFillTask(taskID string) (FillTaskSnapshot, error)
	Prune() PruneSummary
	ImportAccounts(accounts []*Account) ImportPoolResult
	RestoreAccounts(accounts []*Account) (RestorePoolResult, error)
	ExportAccounts() []ExportedAccount
}

type textResultObserver interface {
	ObserveTextResult(jwt string, latency time.Duration, err error)
}

type textRoutingPool interface {
	AcquireText(model string, cost int) *Account
	MarkTextModelUnsupported(jwt string, model string)
	ClearTextModelUnsupported(jwt string, model string)
}

type imageRetryPool interface {
	TryAcquireImage(cost int, excludedJWTs map[string]struct{}) *Account
}

type App struct {
	pool             PoolManager
	backend          ImageBackend
	now              func() time.Time
	cfg              Config
	legacyPoolClient *LegacyPoolClient
	migrationMu      sync.RWMutex
	migrationStatus  MigrationStatus
}

const (
	textRetryAttempts        = 3
	textStreamRetryAttempts  = 1
	textRetryCooldown        = 90 * time.Second
	imageRetryAttempts       = 2
	imageRetryCooldown       = 90 * time.Second
	maxTextContinuationTurns = 2
	minTruncationContentSize = 1200
)

var abruptTextEndingPattern = regexp.MustCompile(`[=\(\[\{,\.:+\-/*_'"<>]$`)
var htmlDocumentStartPattern = regexp.MustCompile(`(?i)<!doctype html>|<html(?:\s|>)`)

func NewApp(pool PoolManager, backend ImageBackend, cfg Config, now func() time.Time) *App {
	if now == nil {
		now = time.Now
	}

	var legacyPoolClient *LegacyPoolClient
	if strings.TrimSpace(cfg.LegacyPoolExportBaseURL) != "" {
		legacyPoolClient = &LegacyPoolClient{
			BaseURL:    cfg.LegacyPoolExportBaseURL,
			AdminToken: cfg.AdminToken,
		}
	}

	return &App{
		pool:             pool,
		backend:          backend,
		now:              now,
		cfg:              cfg,
		legacyPoolClient: legacyPoolClient,
	}
}

func (a *App) AdminMeta() AdminMeta {
	status := a.CurrentMigrationStatus()

	return AdminMeta{
		InstanceName:         strings.TrimSpace(a.cfg.InstanceName),
		ServiceLabel:         strings.TrimSpace(a.cfg.ServiceLabel),
		DeploySource:         strings.TrimSpace(a.cfg.DeploySource),
		ImageRef:             strings.TrimSpace(a.cfg.ImageRef),
		PublicBaseURL:        strings.TrimSpace(a.cfg.PublicBaseURL),
		PrimaryPublicBaseURL: strings.TrimSpace(a.cfg.PrimaryPublicBaseURL),
		IsPrimaryTarget:      isPrimaryTarget(a.cfg.PublicBaseURL, a.cfg.PrimaryPublicBaseURL),
		Version:              buildVersionString(),
		LastMigrationAt:      status.FinishedAt,
	}
}

func (a *App) CurrentMigrationStatus() MigrationStatus {
	a.migrationMu.RLock()
	defer a.migrationMu.RUnlock()

	status := a.migrationStatus
	return status
}

func (a *App) AdminCatalog() AdminCatalog {
	catalog := AdminCatalog{
		LowQuotaThreshold: lowQuotaThreshold,
		TextModels:        make([]AdminModelInfo, 0, len(textModelRouter)),
		ImageModels:       make([]AdminModelInfo, 0, len(modelRouter)),
	}

	for modelID, cfg := range textModelRouter {
		tiers := textAccessTiers(modelID)
		catalog.TextModels = append(catalog.TextModels, AdminModelInfo{
			ID:          publicModelID(modelID),
			Cost:        cfg.Cost,
			Category:    "text",
			MinimumTier: minimumTier(tiers),
			Internet:    cfg.Internet,
			AccessTiers: tiers,
			RuntimeNote: textRuntimeNote(modelID),
		})
	}
	sort.Slice(catalog.TextModels, func(i, j int) bool {
		return catalog.TextModels[i].ID < catalog.TextModels[j].ID
	})

	for modelID, cfg := range modelRouter {
		tiers := imageAccessTiers(modelID)
		catalog.ImageModels = append(catalog.ImageModels, AdminModelInfo{
			ID:            publicModelID(modelID),
			Cost:          cfg.Cost,
			Category:      "image",
			MinimumTier:   minimumTier(tiers),
			SupportsEdit:  strings.TrimSpace(cfg.EditMode) != "",
			SupportsMerge: strings.TrimSpace(cfg.MergeMode) != "",
			EditAccess:    imageEditAccess(modelID),
			RuntimeNote:   imageRuntimeNote(modelID),
			AccessTiers:   tiers,
			EditCost:      imageEditCost(modelID, cfg),
			MergeCostNote: imageMergeCostNote(modelID, cfg),
			RouteAdvice:   imageRouteAdvice(modelID),
		})
	}
	sort.Slice(catalog.ImageModels, func(i, j int) bool {
		return catalog.ImageModels[i].ID < catalog.ImageModels[j].ID
	})

	return catalog
}

func minimumTier(tiers []string) string {
	if len(tiers) == 0 {
		return ""
	}
	return strings.TrimSpace(tiers[0])
}

func textAccessTiers(modelID string) []string {
	switch strings.TrimSpace(modelID) {
	case "gpt-4.1-nano", "gpt-5.4-nano", "gpt-5.4-mini", "claude-3-haiku", "gemini-flash",
		"gpt-4.1", "o4-mini", "claude-4.6-sonnet", "deepseek", "deepseek-v3.2",
		"qwen3.5", "qwen3-thinking-2507", "gemini-3-flash", "grok", "gemini-pro",
		"gpt-5.4", "gpt-5.2", "gpt-5.1", "claude-4.5-haiku", "claude-3-sonnet",
		"perplexity", "gemini-2-flash-search", "gpt-4o-search-preview", "gemini-3-flash-search":
		return []string{"free", "standard", "premium", "batya", "business"}
	case "qwen3.5-plus", "qwen3-max", "claude-4.6-sonnet-high", "claude-3-sonnet-high",
		"gemini-3-pro", "gemini-3.1-pro", "gpt-5.4-high", "gpt-5.2-high", "gpt-5.1-high",
		"o3", "claude-4.6-opus", "claude-4.5-opus", "perplexity-pro", "o4-mini-deep-research":
		return []string{"premium", "batya", "business"}
	case "gpt-5.4-pro", "claude-3-opus", "o3-pro":
		return []string{"batya", "business"}
	default:
		return nil
	}
}

func textRuntimeNote(modelID string) string {
	switch strings.TrimSpace(modelID) {
	case "gpt-4.1-nano":
		return "默认文本模型"
	case "gpt-4.1":
		return "free 可用，3 点/次"
	case "claude-4.6-sonnet":
		return "free 可用，4 点/次"
	case "perplexity", "gemini-2-flash-search", "gpt-4o-search-preview", "gemini-3-flash-search":
		return "free 可用，带联网"
	default:
		return ""
	}
}

func imageEditAccess(modelID string) string {
	switch strings.TrimSpace(modelID) {
	case "GPT_IMAGE_HIGH", "GPT_IMAGE_1_5_HIGH", "GOOGLE-nano-banana-pro":
		return "subscription-gated"
	case "GPT_IMAGE", "GPT_IMAGE_1_5":
		return "cost-higher-than-generate"
	default:
		return ""
	}
}

func imageAccessTiers(modelID string) []string {
	switch strings.TrimSpace(modelID) {
	case "FLUX-schnell", "IDEOGRAM_TURBO", "IDEOGRAM", "FLUX-pro", "QWEN-lora", "GROK", "GPT_IMAGE", "GPT_IMAGE_1_5", "FLUX-ultra", "GOOGLE-nano-banana", "GOOGLE-nano-banana-2", "BYTEDANCE-seedream-4", "BYTEDANCE-seedream-5-lite":
		return []string{"free", "standard", "premium", "batya", "business"}
	case "RECRAFT-v3", "MIDJOURNEY-6.1", "MIDJOURNEY-7", "FLUX-kontext-max":
		return []string{"standard", "premium", "batya", "business"}
	case "GPT_IMAGE_HIGH", "GPT_IMAGE_1_5_HIGH", "GOOGLE-nano-banana-pro":
		return []string{"premium", "batya", "business"}
	default:
		return nil
	}
}

func imageEditCost(modelID string, cfg ModelConfig) int {
	if strings.TrimSpace(cfg.EditMode) == "" {
		return 0
	}
	return cfg.EditCost
}

func imageMergeCostNote(modelID string, cfg ModelConfig) string {
	if strings.TrimSpace(cfg.MergeMode) == "" {
		return ""
	}
	if len(cfg.MergeCosts) == 0 {
		if cfg.MergeCost <= 0 {
			return ""
		}
		return fmt.Sprintf("2图起 %d", cfg.MergeCost)
	}

	parts := make([]string, 0, 3)
	for _, count := range []int{2, 3, 4} {
		cost, ok := cfg.MergeCosts[count]
		if !ok || cost <= 0 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%d图 %d", count, cost))
	}
	return strings.Join(parts, " / ")
}

func imageRuntimeNote(modelID string) string {
	switch strings.TrimSpace(modelID) {
	case "GOOGLE-nano-banana":
		return "默认改图"
	case "GOOGLE-nano-banana-2":
		return "free 可用；较 nano-banana 更贵"
	case "QWEN-lora":
		return "最低成本改图"
	case "GPT_IMAGE":
		return "free 可用；改图更贵"
	case "GPT_IMAGE_1_5":
		return "默认生图；改图更贵"
	case "GPT_IMAGE_HIGH", "GPT_IMAGE_1_5_HIGH", "GOOGLE-nano-banana-pro":
		return "高细节生图；改图需高级权限"
	case "FLUX-schnell", "IDEOGRAM_TURBO", "IDEOGRAM", "FLUX-pro", "GROK", "FLUX-ultra", "BYTEDANCE-seedream-4", "BYTEDANCE-seedream-5-lite", "RECRAFT-v3", "MIDJOURNEY-6.1", "MIDJOURNEY-7":
		return "仅chat生图"
	case "FLUX-kontext-max":
		return "高级改图入口"
	default:
		return ""
	}
}

func imageRouteAdvice(modelID string) string {
	switch strings.TrimSpace(modelID) {
	case "FLUX-schnell":
		return "适合最低成本快速生图"
	case "IDEOGRAM_TURBO":
		return "适合更快的文本排版生图"
	case "IDEOGRAM":
		return "适合文本排版、Logo、生图"
	case "FLUX-pro", "FLUX-ultra":
		return "适合 Flux 系列高质量生图"
	case "GPT_IMAGE_1_5":
		return "适合默认生图；若只是改图，优先考虑 gemini-2.5-flash-image"
	case "GPT_IMAGE":
		return "适合 OpenAI 生图；若只是改图，成本高于 gemini-2.5-flash-image"
	case "GPT_IMAGE_HIGH", "GPT_IMAGE_1_5_HIGH":
		return "适合高细节生图，不建议作为默认改图入口"
	case "GOOGLE-nano-banana":
		return "适合默认改图与低门槛多图操作"
	case "GOOGLE-nano-banana-2":
		return "适合质量优先的免费改图/拼图，但成本高于 nano-banana"
	case "QWEN-lora":
		return "适合最低成本改图/拼图测试"
	case "GROK":
		return "适合 xAI 图像生成"
	case "BYTEDANCE-seedream-4", "BYTEDANCE-seedream-5-lite":
		return "适合复杂提示生图，不支持改图"
	case "RECRAFT-v3":
		return "适合设计类生图，需付费层级"
	case "MIDJOURNEY-6.1", "MIDJOURNEY-7":
		return "适合 Midjourney 风格生图，需付费层级"
	case "FLUX-kontext-max":
		return "适合高阶 Flux 改图，需付费层级"
	case "GOOGLE-nano-banana-pro":
		return "适合高质量改图/拼图，需付费层级"
	default:
		return ""
	}
}

func (a *App) setMigrationStatus(status MigrationStatus) {
	a.migrationMu.Lock()
	defer a.migrationMu.Unlock()
	a.migrationStatus = status
}

func (a *App) MigrateFromLegacy() MigrationStatus {
	started := a.now().UTC()
	status := MigrationStatus{
		StartedAt: &started,
	}

	if a.legacyPoolClient == nil {
		status.LastError = "legacy pool export is not configured"
		finished := a.now().UTC()
		status.FinishedAt = &finished
		a.setMigrationStatus(status)
		return status
	}

	exported, err := a.legacyPoolClient.ExportAccounts()
	if err != nil {
		status.LastError = fmt.Sprintf("failed to export legacy pool: %v", err)
		finished := a.now().UTC()
		status.FinishedAt = &finished
		a.setMigrationStatus(status)
		return status
	}

	status.Requested = len(exported)
	accounts := make([]*Account, 0, len(exported))
	for _, item := range exported {
		jwt := strings.TrimSpace(item.JWT)
		if jwt == "" || item.Quota < 2 {
			status.Rejected++
			continue
		}
		accounts = append(accounts, &Account{
			JWT:   jwt,
			Quota: item.Quota,
		})
	}

	result := a.pool.ImportAccounts(accounts)
	status.Imported = result.Imported
	status.Duplicates = result.Duplicates
	status.Overflow = result.Overflow
	status.TotalCount = result.TotalCount

	finished := a.now().UTC()
	status.FinishedAt = &finished
	a.setMigrationStatus(status)
	return status
}

func isPrimaryTarget(current string, primary string) bool {
	current = strings.TrimRight(strings.TrimSpace(current), "/")
	primary = strings.TrimRight(strings.TrimSpace(primary), "/")
	return current != "" && primary != "" && strings.EqualFold(current, primary)
}

func (a *App) Models() []string {
	models := make([]string, 0, len(modelRouter)+len(textModelRouter))
	for modelID, cfg := range modelRouter {
		if cfg.Hidden {
			continue
		}
		models = append(models, publicModelID(modelID))
	}
	for modelID, cfg := range textModelRouter {
		if cfg.Hidden {
			continue
		}
		models = append(models, publicModelID(modelID))
	}
	sort.Strings(models)
	return models
}

func (a *App) Generate(req OpenAIImageReq) (OpenAIImageResp, error) {
	if a.pool == nil || a.backend == nil {
		return OpenAIImageResp{}, fmt.Errorf("app dependencies are not configured")
	}

	isMergeMode := len(req.Images) > 1
	isEditMode := req.Image != "" || len(req.Images) == 1

	if req.Model == "" {
		req.Model = defaultImageModelForRequest(isEditMode, isMergeMode)
	}

	modelCfg, exists := modelRouter[req.Model]
	if !exists || modelCfg.Hidden {
		return OpenAIImageResp{}, newStatusError(http.StatusBadRequest, fmt.Sprintf("Unsupported model: %s", req.Model))
	}

	if isMergeMode && modelCfg.MergeMode == "" {
		return OpenAIImageResp{}, newStatusError(http.StatusBadRequest, fmt.Sprintf("Model '%s' does not support image merging", req.Model))
	}
	if isEditMode && !isMergeMode && modelCfg.EditMode == "" {
		return OpenAIImageResp{}, newStatusError(http.StatusBadRequest, fmt.Sprintf("Model '%s' does not support image editing", req.Model))
	}

	requiredCost := modelCfg.Cost
	if isMergeMode {
		requiredCost = imageMergeCostForCount(modelCfg, len(req.Images))
	} else if isEditMode {
		requiredCost = modelCfg.EditCost
	}

	ratio := parseRatio(req.Size)
	imgURL, err := a.executeImageOperationWithRetry(requiredCost, ratio, func(jwt string) (string, error) {
		if isMergeMode {
			return a.backend.MergeImage(req.Prompt, req.Images, modelCfg.MergeMode, jwt)
		}
		if isEditMode {
			imgData := req.Image
			if imgData == "" {
				imgData = req.Images[0]
			}
			return a.backend.EditImage(req.Prompt, imgData, modelCfg.EditMode, jwt)
		}
		return a.backend.GenerateImage(req.Prompt, modelCfg.Provider, modelCfg.Version, jwt)
	})
	if err != nil {
		return OpenAIImageResp{}, err
	}

	return OpenAIImageResp{
		Created: a.now().Unix(),
		Data:    []ImageData{{URL: imgURL}},
	}, nil
}

func (a *App) executeImageOperationWithRetry(requiredCost int, ratio string, run func(jwt string) (string, error)) (string, error) {
	if a == nil || a.pool == nil || a.backend == nil {
		return "", fmt.Errorf("app dependencies are not configured")
	}

	var lastErr error
	triedJWTs := make(map[string]struct{})
	for attempt := 1; attempt <= imageRetryAttempts; attempt++ {
		acc := a.acquireImageAccount(requiredCost, attempt, triedJWTs)
		if acc == nil {
			if lastErr != nil {
				if statusCodeForError(lastErr) != http.StatusInternalServerError {
					return "", lastErr
				}
				return "", newTypedStatusError(http.StatusGatewayTimeout, fmt.Sprintf("Generation failed: no fresh image account available after retry (%v)", lastErr), "upstream_timeout")
			}
			return "", newStatusError(http.StatusInternalServerError, "image pool returned no account")
		}
		if jwt := strings.TrimSpace(acc.JWT); jwt != "" {
			triedJWTs[jwt] = struct{}{}
		}

		if !a.backend.UpdateUserSettings(acc.JWT, ratio) {
			lastErr = newStatusError(http.StatusInternalServerError, "Failed to update user settings")
			a.pool.Release(acc)
			if attempt < imageRetryAttempts {
				continue
			}
			return "", lastErr
		}

		startedAt := time.Now()
		imgURL, err := run(acc.JWT)
		if err == nil {
			a.pool.Release(acc)
			return imgURL, nil
		}

		lastErr = wrapImageBackendError("Generation failed", err)
		switch {
		case isUpstreamAccountLimitedError(err):
			a.pool.Release(acc)
			if attempt < imageRetryAttempts {
				continue
			}
			return "", lastErr
		case shouldRetryImageBackendError(err):
			a.observeTextAccount(acc.JWT, startedAt, err)
			a.releaseTextAccount(acc, imageRetryCooldown)
			if attempt < imageRetryAttempts {
				continue
			}
			return "", lastErr
		default:
			a.pool.Release(acc)
			return "", lastErr
		}
	}

	if lastErr != nil {
		return "", lastErr
	}
	return "", newTypedStatusError(http.StatusGatewayTimeout, "image generation timed out after retry", "upstream_timeout")
}

func defaultImageModelForRequest(isEditMode bool, isMergeMode bool) string {
	switch {
	case isMergeMode:
		return "GPT_IMAGE_1_5"
	case isEditMode:
		return "GOOGLE-nano-banana"
	default:
		return "GPT_IMAGE_1_5"
	}
}

func imageMergeCostForCount(cfg ModelConfig, imageCount int) int {
	if len(cfg.MergeCosts) == 0 {
		return cfg.MergeCost
	}
	if cost, ok := cfg.MergeCosts[imageCount]; ok && cost > 0 {
		return cost
	}
	if imageCount <= 2 {
		if cost, ok := cfg.MergeCosts[2]; ok && cost > 0 {
			return cost
		}
	}
	if imageCount >= 4 {
		if cost, ok := cfg.MergeCosts[4]; ok && cost > 0 {
			return cost
		}
	}
	if cost, ok := cfg.MergeCosts[3]; ok && cost > 0 {
		return cost
	}
	return cfg.MergeCost
}

func (a *App) CompleteTextChat(req chatCompletionRequest) (TextCompletionResult, error) {
	if a.pool == nil || a.backend == nil {
		return TextCompletionResult{}, fmt.Errorf("app dependencies are not configured")
	}

	messageReq, title, requiredCost, err := buildTextUpstreamRequest(req)
	if err != nil {
		return TextCompletionResult{}, newStatusError(http.StatusBadRequest, err.Error())
	}

	for attempt := 1; attempt <= textRetryAttempts; attempt++ {
		acc := a.acquireTextAccount(req.Model, requiredCost)
		attemptStartedAt := time.Now()

		chatID, err := a.backend.CreateChatContext(req.Model, title, acc.JWT)
		if err != nil {
			a.observeTextAccount(acc.JWT, attemptStartedAt, err)
			wrapped := wrapTextBackendError("failed to create chat context", err)
			if isTextModelUnsupportedError(err) {
				a.markTextModelUnsupported(acc.JWT, req.Model)
				a.pool.Release(acc)
				if attempt < textRetryAttempts {
					continue
				}
				return TextCompletionResult{}, wrapped
			}
			if shouldRetryTextBackendError(err) && attempt < textRetryAttempts {
				a.releaseTextAccount(acc, textRetryCooldown)
				continue
			}
			a.pool.Release(acc)
			return TextCompletionResult{}, wrapped
		}

		messageReq.ChatID = chatID
		resp, err := a.backend.SendTextMessage(messageReq, acc.JWT)
		if err != nil {
			a.observeTextAccount(acc.JWT, attemptStartedAt, err)
			wrapped := wrapTextBackendError("text generation failed", err)
			if isTextModelUnsupportedError(err) {
				a.markTextModelUnsupported(acc.JWT, req.Model)
				a.pool.Release(acc)
				if attempt < textRetryAttempts {
					continue
				}
				return TextCompletionResult{}, wrapped
			}
			if shouldRetryTextBackendError(err) && attempt < textRetryAttempts {
				a.releaseTextAccount(acc, textRetryCooldown)
				continue
			}
			a.pool.Release(acc)
			return TextCompletionResult{}, wrapped
		}

		resp, err = validateTextCompletionResult(req.Model, resp)
		if err != nil {
			a.observeTextAccount(acc.JWT, attemptStartedAt, err)
			a.pool.Release(acc)
			return TextCompletionResult{}, err
		}

		a.observeTextAccount(acc.JWT, attemptStartedAt, nil)
		a.clearTextModelUnsupported(acc.JWT, req.Model)
		a.pool.Release(acc)
		return resp, nil
	}

	return TextCompletionResult{}, newTypedStatusError(http.StatusGatewayTimeout, "text generation timed out after retry", "upstream_timeout")
}

func (a *App) StreamTextChat(ctx context.Context, req chatCompletionRequest, emit func(TextStreamEvent) error) (TextCompletionResult, error) {
	if a.pool == nil || a.backend == nil {
		return TextCompletionResult{}, fmt.Errorf("app dependencies are not configured")
	}

	messageReq, title, requiredCost, err := buildTextUpstreamRequest(req)
	if err != nil {
		return TextCompletionResult{}, newStatusError(http.StatusBadRequest, err.Error())
	}

	for attempt := 1; attempt <= textStreamRetryAttempts; attempt++ {
		acc := a.acquireTextAccount(req.Model, requiredCost)
		attemptStartedAt := time.Now()

		chatID, err := a.backend.CreateChatContext(req.Model, title, acc.JWT)
		if err != nil {
			a.observeTextAccount(acc.JWT, attemptStartedAt, err)
			wrapped := wrapTextBackendError("failed to create chat context", err)
			if isTextModelUnsupportedError(err) {
				a.markTextModelUnsupported(acc.JWT, req.Model)
				a.pool.Release(acc)
				if attempt < textStreamRetryAttempts {
					continue
				}
				return TextCompletionResult{}, wrapped
			}
			if shouldRetryTextBackendError(err) {
				a.releaseTextAccount(acc, textRetryCooldown)
				if attempt < textStreamRetryAttempts {
					continue
				}
				return TextCompletionResult{}, wrapped
			}
			a.pool.Release(acc)
			return TextCompletionResult{}, wrapped
		}

		messageReq.ChatID = chatID
		streamedAnyChunk := false
		streamedVisibleOutput := false
		observedLatency := false
		resp, err := a.backend.StreamTextMessage(ctx, messageReq, acc.JWT, func(event TextStreamEvent) error {
			if strings.EqualFold(strings.TrimSpace(event.Type), "botType") {
				if mismatchErr := ensureModelMatch(req.Model, event.ChatModel); mismatchErr != nil {
					return mismatchErr
				}
			}
			if (strings.EqualFold(strings.TrimSpace(event.Type), "reasoningContent") && event.ReasoningContent != "") ||
				(strings.EqualFold(strings.TrimSpace(event.Type), "chunk") && event.Delta != "") {
				streamedVisibleOutput = true
			}
			if strings.EqualFold(strings.TrimSpace(event.Type), "chunk") && event.Delta != "" {
				streamedAnyChunk = true
			}
			if !observedLatency && ((strings.EqualFold(strings.TrimSpace(event.Type), "reasoningContent") && event.ReasoningContent != "") ||
				(strings.EqualFold(strings.TrimSpace(event.Type), "chunk") && event.Delta != "")) {
				observedLatency = true
				a.observeTextAccount(acc.JWT, attemptStartedAt, nil)
			}
			if emit == nil {
				return nil
			}
			return emit(event)
		})
		if err != nil {
			if !observedLatency {
				a.observeTextAccount(acc.JWT, attemptStartedAt, err)
			}
			wrapped := wrapTextBackendError("text streaming failed", err)
			if !streamedVisibleOutput && isTextModelUnsupportedError(err) {
				a.markTextModelUnsupported(acc.JWT, req.Model)
				a.pool.Release(acc)
				if attempt < textStreamRetryAttempts {
					continue
				}
				return TextCompletionResult{}, wrapped
			}
			if !streamedVisibleOutput && shouldRetryTextBackendError(err) {
				a.releaseTextAccount(acc, textRetryCooldown)
				if attempt < textStreamRetryAttempts {
					continue
				}
				return TextCompletionResult{}, wrapped
			}
			a.pool.Release(acc)
			return TextCompletionResult{}, wrapped
		}

		resp, err = validateTextCompletionResult(req.Model, resp)
		if err != nil {
			if !observedLatency {
				a.observeTextAccount(acc.JWT, attemptStartedAt, err)
			}
			a.pool.Release(acc)
			return TextCompletionResult{}, err
		}
		if !observedLatency {
			observedLatency = true
			a.observeTextAccount(acc.JWT, attemptStartedAt, nil)
		}
		a.clearTextModelUnsupported(acc.JWT, req.Model)

		if emit != nil && !streamedAnyChunk && strings.TrimSpace(resp.Content) != "" {
			if err := emit(TextStreamEvent{Type: "chunk", Delta: resp.Content}); err != nil {
				a.pool.Release(acc)
				return TextCompletionResult{}, err
			}
		}

		a.pool.Release(acc)
		return resp, nil
	}

	return TextCompletionResult{}, newTypedStatusError(http.StatusGatewayTimeout, "text streaming timed out after retry", "upstream_timeout")
}

func validateTextCompletionResult(requestedModel string, resp TextCompletionResult) (TextCompletionResult, error) {
	if err := ensureModelMatch(requestedModel, resp.ChatModel); err != nil {
		return TextCompletionResult{}, err
	}

	if strings.TrimSpace(resp.ChatModel) == "" {
		resp.ChatModel = requestedModel
	}
	if strings.TrimSpace(resp.Content) == "" {
		return TextCompletionResult{}, newStatusError(http.StatusBadGateway, "upstream returned empty text completion")
	}
	return resp, nil
}

func (a *App) extendTruncatedTextCompletion(messageReq UpstreamTextMessageRequest, jwtToken string, resp TextCompletionResult) (TextCompletionResult, error) {
	return a.continueTruncatedTextCompletion(messageReq, jwtToken, resp, nil)
}

func (a *App) continueTruncatedTextCompletion(messageReq UpstreamTextMessageRequest, jwtToken string, resp TextCompletionResult, emitDelta func(string) error) (TextCompletionResult, error) {
	result := resp
	if !shouldAttemptTextContinuation(messageReq.Text, result.Content) {
		return result, nil
	}

	for turn := 0; turn < maxTextContinuationTurns; turn++ {
		followUpReq := UpstreamTextMessageRequest{
			Text:                   buildTextContinuationPrompt(messageReq.Text, result.Content),
			ChatID:                 messageReq.ChatID,
			Model:                  messageReq.Model,
			WithPotentialQuestions: false,
		}

		next, err := a.backend.SendTextMessage(followUpReq, jwtToken)
		if err != nil {
			return TextCompletionResult{}, wrapTextBackendError("text continuation failed", err)
		}

		next, err = validateTextCompletionResult(messageReq.Model, next)
		if err != nil {
			return TextCompletionResult{}, err
		}
		if strings.TrimSpace(next.Content) == "" {
			return TextCompletionResult{}, newStatusError(http.StatusBadGateway, "upstream returned empty continuation")
		}

		mergedContent, appendedDelta := mergeTextContinuationWithDelta(result.Content, next.Content)
		if emitDelta != nil && appendedDelta != "" {
			if err := emitDelta(appendedDelta); err != nil {
				return TextCompletionResult{}, err
			}
		}
		result.Content = sanitizeMergedCodeContinuation(mergedContent)
		result.ChatModel = next.ChatModel

		if !shouldAttemptTextContinuation(messageReq.Text, result.Content) {
			break
		}
	}

	return result, nil
}

func shouldAttemptTextContinuation(prompt string, content string) bool {
	trimmed := strings.TrimSpace(content)
	if len([]rune(trimmed)) < minTruncationContentSize {
		return false
	}
	if !isCodeLikeRequest(prompt, trimmed) {
		return false
	}
	if hasUnclosedCodeFence(trimmed) {
		return true
	}
	return looksAbruptlyCut(trimmed)
}

func isCodeLikeRequest(prompt string, content string) bool {
	combined := strings.ToLower(strings.TrimSpace(prompt + "\n" + content))
	keywords := []string{
		"html", "css", "javascript", "single-file", "single file", "code", "```",
		"<!doctype", "<html", "<script", "function ", "const ", "let ",
		"单文件", "代码", "页面", "实现", "html 页面", "js", "css 和 js",
	}
	for _, keyword := range keywords {
		if strings.Contains(combined, keyword) {
			return true
		}
	}
	return false
}

func hasUnclosedCodeFence(content string) bool {
	return strings.Count(content, "```")%2 == 1
}

func looksAbruptlyCut(content string) bool {
	lines := strings.Split(strings.TrimRight(content, "\r\n\t "), "\n")
	if len(lines) == 0 {
		return false
	}
	last := strings.TrimSpace(lines[len(lines)-1])
	if last == "" {
		return false
	}
	if strings.HasSuffix(last, "</html>") || strings.HasSuffix(last, "</body>") || strings.HasSuffix(last, "</script>") || strings.HasSuffix(last, "</style>") {
		return false
	}

	lastRune := []rune(last)[len([]rune(last))-1]
	if abruptTextEndingPattern.MatchString(string(lastRune)) {
		return true
	}

	safeEndings := []string{";", "}", ")", "]", ".", "。", "!", "！", "?", "？", "\"", "'", "`", ","}
	for _, ending := range safeEndings {
		if strings.HasSuffix(last, ending) {
			return false
		}
	}

	return true
}

func buildTextContinuationPrompt(originalPrompt string, current string) string {
	summary := firstNRunes(strings.TrimSpace(originalPrompt), 700)
	tail := lastNRunes(strings.TrimSpace(current), 450)
	prompt := fmt.Sprintf(
		"Your previous code answer was cut off.\nOriginal request summary:\n%s\n\nThe last part already returned (do not repeat it):\n%s\n\nContinue with only the missing remainder starting immediately after the final character above. Do not explain, do not apologize, and do not repeat any existing text.",
		summary,
		tail,
	)
	if hasUnclosedCodeFence(current) {
		prompt += " If a markdown code block is already open, continue inside the same code block and close it when the code is complete."
	}
	return firstNRunes(prompt, 2200)
}

func mergeTextContinuation(existing string, continuation string) string {
	merged, _ := mergeTextContinuationWithDelta(existing, continuation)
	return merged
}

func mergeTextContinuationWithDelta(existing string, continuation string) (string, string) {
	mergedContinuation := stripRedundantCodeFence(existing, continuation)

	existingRunes := []rune(existing)
	continuationRunes := []rune(mergedContinuation)
	maxOverlap := minInt(len(existingRunes), len(continuationRunes), 240)
	for overlap := maxOverlap; overlap >= 24; overlap-- {
		if string(existingRunes[len(existingRunes)-overlap:]) == string(continuationRunes[:overlap]) {
			appended := string(continuationRunes[overlap:])
			return string(existingRunes) + appended, appended
		}
	}

	return existing + mergedContinuation, mergedContinuation
}

func sanitizeMergedCodeContinuation(content string) string {
	if htmlDoc, ok := extractLastCompleteHTMLDocument(content); ok {
		if prefix := leadingCodeFencePrefix(content); prefix != "" {
			return prefix + strings.TrimLeft(htmlDoc, "\r\n\t ") + "\n```"
		}
		return strings.TrimSpace(htmlDoc)
	}

	return content
}

func extractLastCompleteHTMLDocument(content string) (string, bool) {
	matches := htmlDocumentStartPattern.FindAllStringIndex(content, -1)
	if len(matches) == 0 {
		return "", false
	}

	lower := strings.ToLower(content)
	lastComplete := ""
	for _, match := range matches {
		start := match[0]
		endOffset := strings.Index(lower[start:], "</html>")
		if endOffset == -1 {
			continue
		}
		lastComplete = content[start : start+endOffset+len("</html>")]
	}
	if lastComplete == "" {
		return "", false
	}
	return lastComplete, true
}

func leadingCodeFencePrefix(content string) string {
	trimmed := strings.TrimLeft(content, "\r\n\t ")
	if !strings.HasPrefix(trimmed, "```") {
		return ""
	}
	newlineIndex := strings.Index(trimmed, "\n")
	if newlineIndex == -1 {
		return "```\n"
	}
	return trimmed[:newlineIndex+1]
}

func stripRedundantCodeFence(existing string, continuation string) string {
	if !hasUnclosedCodeFence(existing) {
		return continuation
	}

	trimmed := strings.TrimLeft(continuation, "\r\n\t ")
	prefixes := []string{"```html\r\n", "```html\n", "```\r\n", "```\n"}
	for _, prefix := range prefixes {
		if strings.HasPrefix(trimmed, prefix) {
			return strings.TrimPrefix(trimmed, prefix)
		}
	}
	return continuation
}

func minInt(values ...int) int {
	if len(values) == 0 {
		return 0
	}
	min := values[0]
	for _, value := range values[1:] {
		if value < min {
			min = value
		}
	}
	return min
}

func firstNRunes(value string, limit int) string {
	runes := []rune(value)
	if limit <= 0 || len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func lastNRunes(value string, limit int) string {
	runes := []rune(value)
	if limit <= 0 || len(runes) <= limit {
		return value
	}
	return string(runes[len(runes)-limit:])
}

func ensureModelMatch(requestedModel string, actualModel string) error {
	requested := strings.TrimSpace(requestedModel)
	actual := strings.TrimSpace(actualModel)
	if actual == "" || requested == "" {
		return nil
	}
	if requested != actual {
		return newStatusError(http.StatusBadGateway, fmt.Sprintf("upstream model mismatch: requested %s, got %s", requested, actual))
	}
	return nil
}

func wrapTextBackendError(prefix string, err error) error {
	if err == nil {
		return nil
	}
	var upstreamErr *protocol.UpstreamError
	if errors.As(err, &upstreamErr) && upstreamErr != nil {
		return newTypedStatusError(upstreamErr.StatusCode, upstreamErr.Message, upstreamErr.Type)
	}
	if shouldRetryTextBackendError(err) {
		return newTypedStatusError(http.StatusGatewayTimeout, fmt.Sprintf("%s: %v", prefix, err), "upstream_timeout")
	}
	if statusCodeForError(err) != http.StatusInternalServerError {
		return err
	}
	return newStatusError(http.StatusInternalServerError, fmt.Sprintf("%s: %v", prefix, err))
}

func wrapImageBackendError(prefix string, err error) error {
	if err == nil {
		return nil
	}

	var upstreamErr *protocol.UpstreamError
	if errors.As(err, &upstreamErr) && upstreamErr != nil {
		return newTypedStatusError(upstreamErr.StatusCode, upstreamErr.Message, upstreamErr.Type)
	}
	if statusCodeForError(err) != http.StatusInternalServerError {
		return err
	}
	return newStatusError(http.StatusInternalServerError, fmt.Sprintf("%s: %v", prefix, err))
}

func shouldRetryImageBackendError(err error) bool {
	if err == nil {
		return false
	}
	return isTextTimeoutError(err) || isRetryableTextTransportError(err)
}

type statusError struct {
	StatusCode int
	Message    string
	ErrorType  string
}

func (e *statusError) Error() string {
	return e.Message
}

func newStatusError(statusCode int, message string) error {
	return &statusError{
		StatusCode: statusCode,
		Message:    message,
	}
}

func newTypedStatusError(statusCode int, message string, errorType string) error {
	return &statusError{
		StatusCode: statusCode,
		Message:    message,
		ErrorType:  strings.TrimSpace(errorType),
	}
}

func statusCodeForError(err error) int {
	if err == nil {
		return http.StatusOK
	}

	var withStatus *statusError
	if errors.As(err, &withStatus) && withStatus != nil {
		return withStatus.StatusCode
	}

	return http.StatusInternalServerError
}

func errorTypeForError(err error, fallback string) string {
	if strings.TrimSpace(fallback) == "" {
		fallback = "invalid_request_error"
	}
	if err == nil {
		return fallback
	}

	var withStatus *statusError
	if errors.As(err, &withStatus) && withStatus != nil && strings.TrimSpace(withStatus.ErrorType) != "" {
		return strings.TrimSpace(withStatus.ErrorType)
	}
	return fallback
}

func shouldRetryTextBackendError(err error) bool {
	if err == nil {
		return false
	}

	var upstreamErr *protocol.UpstreamError
	if errors.As(err, &upstreamErr) && upstreamErr != nil {
		return upstreamErr.StatusCode == http.StatusUnauthorized ||
			upstreamErr.StatusCode == http.StatusTooManyRequests ||
			upstreamErr.StatusCode >= http.StatusInternalServerError
	}

	return isTextTimeoutError(err) || isRetryableTextTransportError(err)
}

func isTextTimeoutError(err error) bool {
	if err == nil {
		return false
	}

	var timeoutErr net.Error
	if errors.As(err, &timeoutErr) && timeoutErr.Timeout() {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	errLower := strings.ToLower(err.Error())
	return strings.Contains(errLower, "timed out") ||
		strings.Contains(errLower, "timeout") ||
		strings.Contains(errLower, "did not complete in time") ||
		strings.Contains(errLower, "context deadline exceeded") ||
		strings.Contains(errLower, "client.timeout exceeded")
}

func isRetryableTextTransportError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) {
		return true
	}

	errLower := strings.ToLower(err.Error())
	return strings.Contains(errLower, "unexpected eof") ||
		strings.Contains(errLower, "connection reset by peer") ||
		strings.Contains(errLower, "broken pipe") ||
		strings.Contains(errLower, "server closed idle connection") ||
		strings.Contains(errLower, "stream error") ||
		strings.Contains(errLower, "transport connection broken")
}

func isTextModelUnsupportedError(err error) bool {
	return isUpstreamAccountLimitedError(err)
}

func isUpstreamAccountLimitedError(err error) bool {
	if err == nil {
		return false
	}

	var upstreamErr *protocol.UpstreamError
	if !errors.As(err, &upstreamErr) || upstreamErr == nil {
		return false
	}
	if upstreamErr.StatusCode != http.StatusForbidden {
		return false
	}

	msg := strings.ToLower(strings.TrimSpace(upstreamErr.Message))
	return strings.Contains(msg, "subscribe") ||
		strings.Contains(msg, "subscription") ||
		strings.Contains(msg, "advanced models") ||
		strings.Contains(msg, "more requests") ||
		strings.Contains(msg, "productive day")
}

func (a *App) observeTextAccount(jwtToken string, startedAt time.Time, err error) {
	if a == nil || a.pool == nil || startedAt.IsZero() {
		return
	}
	observer, ok := a.pool.(textResultObserver)
	if !ok {
		return
	}
	latency := time.Since(startedAt)
	if latency < 0 {
		latency = 0
	}
	observer.ObserveTextResult(jwtToken, latency, err)
}

func (a *App) acquireTextAccount(model string, cost int) *Account {
	if a == nil || a.pool == nil {
		return nil
	}
	if routingPool, ok := a.pool.(textRoutingPool); ok {
		return routingPool.AcquireText(model, cost)
	}
	return a.pool.Acquire(cost)
}

func (a *App) acquireImageAccount(cost int, attempt int, excludedJWTs map[string]struct{}) *Account {
	if a == nil || a.pool == nil {
		return nil
	}
	if attempt > 1 {
		if retryPool, ok := a.pool.(imageRetryPool); ok {
			return retryPool.TryAcquireImage(cost, excludedJWTs)
		}
	}
	return a.pool.Acquire(cost)
}

func (a *App) markTextModelUnsupported(jwtToken string, model string) {
	if a == nil || a.pool == nil {
		return
	}
	if routingPool, ok := a.pool.(textRoutingPool); ok {
		routingPool.MarkTextModelUnsupported(jwtToken, model)
	}
}

func (a *App) clearTextModelUnsupported(jwtToken string, model string) {
	if a == nil || a.pool == nil {
		return
	}
	if routingPool, ok := a.pool.(textRoutingPool); ok {
		routingPool.ClearTextModelUnsupported(jwtToken, model)
	}
}

func (a *App) releaseTextAccount(acc *Account, cooldown time.Duration) {
	if acc == nil || a.pool == nil {
		return
	}
	if cooldown > 0 {
		if coolingPool, ok := a.pool.(interface {
			Cooldown(*Account, time.Duration)
		}); ok {
			coolingPool.Cooldown(acc, cooldown)
			return
		}
	}
	a.pool.Release(acc)
}

func writeJSON(w http.ResponseWriter, statusCode int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeOpenAIError(w http.ResponseWriter, statusCode int, message string, errorType string) {
	if strings.TrimSpace(errorType) == "" {
		errorType = "invalid_request_error"
	}

	writeJSON(w, statusCode, map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    errorType,
		},
	})
}
