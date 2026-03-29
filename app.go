package main

import (
	"encoding/json"
	"errors"
	"fmt"
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
	StreamTextMessage(req UpstreamTextMessageRequest, jwtToken string, emit func(TextStreamEvent) error) (TextCompletionResult, error)
	GetCount(jwtToken string) (int, error)
}

type PoolManager interface {
	Acquire(cost int) *Account
	Release(acc *Account)
	Status() PoolStatus
	StartFillTask(count int) FillTaskSnapshot
	Prune() PruneSummary
	ImportAccounts(accounts []*Account) ImportPoolResult
	ExportAccounts() []ExportedAccount
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
		if cfg.Hidden {
			continue
		}
		catalog.TextModels = append(catalog.TextModels, AdminModelInfo{
			ID:       modelID,
			Cost:     cfg.Cost,
			Category: "text",
			Internet: cfg.Internet,
		})
	}
	sort.Slice(catalog.TextModels, func(i, j int) bool {
		return catalog.TextModels[i].ID < catalog.TextModels[j].ID
	})

	for modelID, cfg := range modelRouter {
		if cfg.Hidden {
			continue
		}
		catalog.ImageModels = append(catalog.ImageModels, AdminModelInfo{
			ID:            modelID,
			Cost:          cfg.Cost,
			Category:      "image",
			SupportsEdit:  strings.TrimSpace(cfg.EditMode) != "",
			SupportsMerge: strings.TrimSpace(cfg.MergeMode) != "",
		})
	}
	sort.Slice(catalog.ImageModels, func(i, j int) bool {
		return catalog.ImageModels[i].ID < catalog.ImageModels[j].ID
	})

	return catalog
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
		models = append(models, modelID)
	}
	for modelID, cfg := range textModelRouter {
		if cfg.Hidden {
			continue
		}
		models = append(models, modelID)
	}
	sort.Strings(models)
	return models
}

func (a *App) Generate(req OpenAIImageReq) (OpenAIImageResp, error) {
	if a.pool == nil || a.backend == nil {
		return OpenAIImageResp{}, fmt.Errorf("app dependencies are not configured")
	}

	if req.Model == "" {
		req.Model = "gpt-image-1.5"
	}

	modelCfg, exists := modelRouter[req.Model]
	if !exists || modelCfg.Hidden {
		return OpenAIImageResp{}, newStatusError(http.StatusBadRequest, fmt.Sprintf("Unsupported model: %s", req.Model))
	}

	isMergeMode := len(req.Images) > 1
	isEditMode := req.Image != "" || len(req.Images) == 1

	if isMergeMode && modelCfg.MergeMode == "" {
		return OpenAIImageResp{}, newStatusError(http.StatusBadRequest, fmt.Sprintf("Model '%s' does not support image merging", req.Model))
	}
	if isEditMode && !isMergeMode && modelCfg.EditMode == "" {
		return OpenAIImageResp{}, newStatusError(http.StatusBadRequest, fmt.Sprintf("Model '%s' does not support image editing", req.Model))
	}

	requiredCost := modelCfg.Cost
	if isMergeMode {
		requiredCost = modelCfg.MergeCost
	} else if isEditMode {
		requiredCost = modelCfg.EditCost
	}

	ratio := parseRatio(req.Size)
	acc := a.pool.Acquire(requiredCost)
	defer a.pool.Release(acc)

	if !a.backend.UpdateUserSettings(acc.JWT, ratio) {
		return OpenAIImageResp{}, newStatusError(http.StatusInternalServerError, "Failed to update user settings")
	}

	var (
		imgURL string
		err    error
	)

	if isMergeMode {
		imgURL, err = a.backend.MergeImage(req.Prompt, req.Images, modelCfg.MergeMode, acc.JWT)
	} else if isEditMode {
		imgData := req.Image
		if imgData == "" {
			imgData = req.Images[0]
		}
		imgURL, err = a.backend.EditImage(req.Prompt, imgData, modelCfg.EditMode, acc.JWT)
	} else {
		imgURL, err = a.backend.GenerateImage(req.Prompt, modelCfg.Provider, modelCfg.Version, acc.JWT)
	}
	if err != nil {
		return OpenAIImageResp{}, wrapImageBackendError("Generation failed", err)
	}

	return OpenAIImageResp{
		Created: a.now().Unix(),
		Data:    []ImageData{{URL: imgURL}},
	}, nil
}

func (a *App) CompleteTextChat(req chatCompletionRequest) (TextCompletionResult, error) {
	if a.pool == nil || a.backend == nil {
		return TextCompletionResult{}, fmt.Errorf("app dependencies are not configured")
	}

	messageReq, title, requiredCost, err := buildTextUpstreamRequest(req)
	if err != nil {
		return TextCompletionResult{}, newStatusError(http.StatusBadRequest, err.Error())
	}

	acc := a.pool.Acquire(requiredCost)
	defer a.pool.Release(acc)

	chatID, err := a.backend.CreateChatContext(req.Model, title, acc.JWT)
	if err != nil {
		return TextCompletionResult{}, wrapTextBackendError("failed to create chat context", err)
	}

	messageReq.ChatID = chatID
	resp, err := a.backend.SendTextMessage(messageReq, acc.JWT)
	if err != nil {
		return TextCompletionResult{}, wrapTextBackendError("text generation failed", err)
	}

	resp, err = validateTextCompletionResult(req.Model, resp)
	if err != nil {
		return TextCompletionResult{}, err
	}

	return a.extendTruncatedTextCompletion(messageReq, acc.JWT, resp)
}

func (a *App) StreamTextChat(req chatCompletionRequest, emit func(TextStreamEvent) error) (TextCompletionResult, error) {
	if a.pool == nil || a.backend == nil {
		return TextCompletionResult{}, fmt.Errorf("app dependencies are not configured")
	}

	messageReq, title, requiredCost, err := buildTextUpstreamRequest(req)
	if err != nil {
		return TextCompletionResult{}, newStatusError(http.StatusBadRequest, err.Error())
	}

	acc := a.pool.Acquire(requiredCost)
	defer a.pool.Release(acc)

	chatID, err := a.backend.CreateChatContext(req.Model, title, acc.JWT)
	if err != nil {
		return TextCompletionResult{}, wrapTextBackendError("failed to create chat context", err)
	}

	messageReq.ChatID = chatID
	streamedAnyChunk := false
	resp, err := a.backend.StreamTextMessage(messageReq, acc.JWT, func(event TextStreamEvent) error {
		if strings.EqualFold(strings.TrimSpace(event.Type), "botType") {
			if mismatchErr := ensureModelMatch(req.Model, event.ChatModel); mismatchErr != nil {
				return mismatchErr
			}
		}
		if strings.EqualFold(strings.TrimSpace(event.Type), "chunk") && event.Delta != "" {
			streamedAnyChunk = true
		}
		if emit == nil {
			return nil
		}
		return emit(event)
	})
	if err != nil {
		return TextCompletionResult{}, wrapTextBackendError("text streaming failed", err)
	}

	resp, err = validateTextCompletionResult(req.Model, resp)
	if err != nil {
		return TextCompletionResult{}, err
	}

	if emit != nil && !streamedAnyChunk && shouldAttemptTextContinuation(messageReq.Text, resp.Content) {
		if err := emit(TextStreamEvent{Type: "chunk", Delta: resp.Content}); err != nil {
			return TextCompletionResult{}, err
		}
	}

	return a.continueTruncatedTextCompletion(messageReq, acc.JWT, resp, func(delta string) error {
		if emit == nil || delta == "" {
			return nil
		}
		return emit(TextStreamEvent{Type: "chunk", Delta: delta})
	})
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
