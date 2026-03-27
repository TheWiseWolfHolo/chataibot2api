package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
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
}

type PoolManager interface {
	Acquire(cost int) *Account
	Release(acc *Account)
	Status() PoolStatus
	StartFillTask(count int) FillTaskSnapshot
	Prune() PruneSummary
}

type App struct {
	pool    PoolManager
	backend ImageBackend
	now     func() time.Time
}

const (
	maxTextContinuationTurns = 2
	minTruncationContentSize = 1200
)

var abruptTextEndingPattern = regexp.MustCompile(`[=\(\[\{,\.:+\-/*_'"<>]$`)

func NewApp(pool PoolManager, backend ImageBackend, now func() time.Time) *App {
	if now == nil {
		now = time.Now
	}

	return &App{
		pool:    pool,
		backend: backend,
		now:     now,
	}
}

func (a *App) Models() []string {
	models := make([]string, 0, len(modelRouter)+len(textModelRouter))
	for modelID := range modelRouter {
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
	if !exists {
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
	resp, err := a.backend.StreamTextMessage(messageReq, acc.JWT, func(event TextStreamEvent) error {
		if strings.EqualFold(strings.TrimSpace(event.Type), "botType") {
			if mismatchErr := ensureModelMatch(req.Model, event.ChatModel); mismatchErr != nil {
				return mismatchErr
			}
		}
		if emit == nil {
			return nil
		}
		return emit(event)
	})
	if err != nil {
		return TextCompletionResult{}, wrapTextBackendError("text streaming failed", err)
	}

	return validateTextCompletionResult(req.Model, resp)
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
	result := resp
	if !shouldAttemptTextContinuation(messageReq.Text, result.Content) {
		return result, nil
	}

	for turn := 0; turn < maxTextContinuationTurns; turn++ {
		followUpReq := UpstreamTextMessageRequest{
			Text:                   buildTextContinuationPrompt(result.Content),
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

		result.Content = mergeTextContinuation(result.Content, next.Content)
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

func buildTextContinuationPrompt(current string) string {
	prompt := "Continue exactly from where your previous answer stopped. Output only the remaining content with no introduction and no repeated text."
	if hasUnclosedCodeFence(current) {
		return prompt + " The previous answer already started a markdown code block; continue inside the same code block and close it when the code is complete."
	}
	return prompt
}

func mergeTextContinuation(existing string, continuation string) string {
	mergedContinuation := stripRedundantCodeFence(existing, continuation)

	existingRunes := []rune(existing)
	continuationRunes := []rune(mergedContinuation)
	maxOverlap := minInt(len(existingRunes), len(continuationRunes), 240)
	for overlap := maxOverlap; overlap >= 24; overlap-- {
		if string(existingRunes[len(existingRunes)-overlap:]) == string(continuationRunes[:overlap]) {
			return string(existingRunes) + string(continuationRunes[overlap:])
		}
	}

	return existing + mergedContinuation
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
