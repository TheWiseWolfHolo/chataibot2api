package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

type ImageBackend interface {
	UpdateUserSettings(jwtToken, aspectRatio string) bool
	GenerateImage(prompt, provider, version, jwtToken string) (string, error)
	EditImage(prompt, base64Data, model, jwtToken string) (string, error)
	MergeImage(prompt string, base64Images []string, mergeType, jwtToken string) (string, error)
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
	models := make([]string, 0, len(modelRouter))
	for modelID := range modelRouter {
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
		return OpenAIImageResp{}, newStatusError(http.StatusInternalServerError, fmt.Sprintf("Generation failed: %v", err))
	}

	return OpenAIImageResp{
		Created: a.now().Unix(),
		Data:    []ImageData{{URL: imgURL}},
	}, nil
}

type statusError struct {
	StatusCode int
	Message    string
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

func statusCodeForError(err error) int {
	if err == nil {
		return http.StatusOK
	}

	var withStatus *statusError
	if ok := errorAs(err, &withStatus); ok && withStatus != nil {
		return withStatus.StatusCode
	}

	return http.StatusInternalServerError
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

func errorAs(err error, target **statusError) bool {
	if err == nil {
		return false
	}
	statusErr, ok := err.(*statusError)
	if !ok {
		return false
	}
	*target = statusErr
	return true
}
