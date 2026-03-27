package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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

	return validateTextCompletionResult(req.Model, resp)
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
