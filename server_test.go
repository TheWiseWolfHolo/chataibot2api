package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"chataibot2api/protocol"
)

type fakePool struct {
	acquiredAccount *Account
	acquiredCost    int
	released        *Account
	status          PoolStatus
	exported        []ExportedAccount
	adminRows       []AdminQuotaRow
	fillTask        FillTaskSnapshot
	pruneResult     PruneSummary
	importResult    ImportPoolResult
	imported        []*Account
	fillCounts      []int
	pruneCalls      int
}

func (f *fakePool) Acquire(cost int) *Account {
	f.acquiredCost = cost
	if f.acquiredAccount != nil {
		return f.acquiredAccount
	}
	return &Account{JWT: "fake-jwt", Quota: 65}
}

func (f *fakePool) Release(acc *Account) {
	f.released = acc
}

func (f *fakePool) Status() PoolStatus {
	return f.status
}

func (f *fakePool) StartFillTask(count int) FillTaskSnapshot {
	f.fillCounts = append(f.fillCounts, count)
	task := f.fillTask
	if task.ID == "" {
		task = FillTaskSnapshot{
			ID:        "task-1",
			Requested: count,
			Status:    "running",
		}
	}
	return task
}

func (f *fakePool) Prune() PruneSummary {
	f.pruneCalls++
	return f.pruneResult
}

func (f *fakePool) ImportAccounts(accounts []*Account) ImportPoolResult {
	f.imported = append([]*Account(nil), accounts...)
	if f.importResult.TotalCount == 0 {
		f.importResult = ImportPoolResult{
			Imported:   len(accounts),
			Duplicates: 0,
			TotalCount: len(accounts),
		}
	}
	return f.importResult
}

func (f *fakePool) ExportAccounts() []ExportedAccount {
	return append([]ExportedAccount(nil), f.exported...)
}

type fakeBackend struct {
	updateCalled bool
	lastRatio    string
	lastPrompt   string
	lastModel    string
	lastVersion  string
	lastEditMode string
	lastMerge    string
	lastImage    string
	lastImages   []string
	generateURL  string
	editURL      string
	mergeURL     string
	generateErr  error
	editErr      error
	mergeErr     error

	textContextModel string
	textContextTitle string
	textContextJWT   string
	textContextID    int
	textContextErr   error

	textRequest    UpstreamTextMessageRequest
	textRequestJWT string
	textResponse   TextCompletionResult
	textResponses  []TextCompletionResult
	textCallCount  int
	textErr        error

	textStreamRequest    UpstreamTextMessageRequest
	textStreamRequestJWT string
	textStreamEvents     []TextStreamEvent
	textStreamResponse   TextCompletionResult
	textStreamErr        error

	quotaByJWT    map[string]int
	getCountCalls []string
}

func (f *fakeBackend) UpdateUserSettings(_ string, aspectRatio string) bool {
	f.updateCalled = true
	f.lastRatio = aspectRatio
	return true
}

func (f *fakeBackend) GenerateImage(prompt, provider, version, _ string) (string, error) {
	f.lastPrompt = prompt
	f.lastModel = provider
	f.lastVersion = version
	if f.generateErr != nil {
		return "", f.generateErr
	}
	if f.generateURL == "" {
		f.generateURL = "https://img.example.com/generated.png"
	}
	return f.generateURL, nil
}

func (f *fakeBackend) EditImage(prompt, imageData, mode, _ string) (string, error) {
	f.lastPrompt = prompt
	f.lastImage = imageData
	f.lastEditMode = mode
	if f.editErr != nil {
		return "", f.editErr
	}
	if f.editURL == "" {
		f.editURL = "https://img.example.com/edited.png"
	}
	return f.editURL, nil
}

func (f *fakeBackend) MergeImage(prompt string, images []string, mode, _ string) (string, error) {
	f.lastPrompt = prompt
	f.lastImages = append([]string(nil), images...)
	f.lastMerge = mode
	if f.mergeErr != nil {
		return "", f.mergeErr
	}
	if f.mergeURL == "" {
		f.mergeURL = "https://img.example.com/merged.png"
	}
	return f.mergeURL, nil
}

func (f *fakeBackend) CreateChatContext(model, title, jwtToken string) (int, error) {
	f.textContextModel = model
	f.textContextTitle = title
	f.textContextJWT = jwtToken
	if f.textContextErr != nil {
		return 0, f.textContextErr
	}
	if f.textContextID == 0 {
		f.textContextID = 42
	}
	return f.textContextID, nil
}

func (f *fakeBackend) SendTextMessage(req UpstreamTextMessageRequest, jwtToken string) (TextCompletionResult, error) {
	f.textRequest = req
	f.textRequestJWT = jwtToken
	f.textCallCount++
	if f.textErr != nil {
		return TextCompletionResult{}, f.textErr
	}

	if len(f.textResponses) >= f.textCallCount {
		resp := f.textResponses[f.textCallCount-1]
		if resp.ChatModel == "" {
			resp.ChatModel = req.Model
		}
		if resp.Content == "" {
			resp.Content = "hello from text backend"
		}
		return resp, nil
	}
	if f.textResponse.ChatModel == "" {
		f.textResponse.ChatModel = req.Model
	}
	if f.textResponse.Content == "" {
		f.textResponse.Content = "hello from text backend"
	}
	return f.textResponse, nil
}

func (f *fakeBackend) StreamTextMessage(req UpstreamTextMessageRequest, jwtToken string, emit func(TextStreamEvent) error) (TextCompletionResult, error) {
	f.textStreamRequest = req
	f.textStreamRequestJWT = jwtToken
	if f.textStreamErr != nil {
		return TextCompletionResult{}, f.textStreamErr
	}

	events := append([]TextStreamEvent(nil), f.textStreamEvents...)
	if len(events) == 0 {
		events = []TextStreamEvent{
			{Type: "botType", ChatModel: req.Model},
			{Type: "chunk", Delta: "stream"},
			{Type: "chunk", Delta: "_ok"},
		}
	}
	for _, event := range events {
		if err := emit(event); err != nil {
			return TextCompletionResult{}, err
		}
	}

	resp := f.textStreamResponse
	if resp.ChatModel == "" {
		resp.ChatModel = req.Model
	}
	if resp.Content == "" {
		resp.Content = "stream_ok"
	}
	return resp, nil
}

func (f *fakeBackend) GetCount(jwtToken string) (int, error) {
	f.getCountCalls = append(f.getCountCalls, jwtToken)
	if f.quotaByJWT != nil {
		if quota, ok := f.quotaByJWT[jwtToken]; ok {
			return quota, nil
		}
	}
	return 65, nil
}

type fakePoolManager struct {
	*fakePool
}

func (f *fakePoolManager) AdminQuotaRows() []AdminQuotaRow {
	if f == nil || f.fakePool == nil {
		return nil
	}
	return append([]AdminQuotaRow(nil), f.adminRows...)
}

func newTestHandler() (*fakePool, *fakeBackend, http.Handler) {
	return newTestHandlerWithLegacyBaseURL("https://holo-image-api.zeabur.app")
}

func newTestHandlerWithLegacyBaseURL(legacyBaseURL string) (*fakePool, *fakeBackend, http.Handler) {
	pool := &fakePool{}
	backend := &fakeBackend{}
	cfg := Config{
		APIBearerToken:          "api-token",
		AdminToken:              "admin-token",
		InstanceName:            "test-instance",
		ServiceLabel:            "holo-image-api-eners",
		DeploySource:            "ghcr-preview",
		ImageRef:                "ghcr.io/thewisewolfholo/chataibot2api:main",
		PublicBaseURL:           "https://holo-image-api-eners.zeabur.app",
		PrimaryPublicBaseURL:    "https://holo-image-api.zeabur.app",
		LegacyPoolExportBaseURL: legacyBaseURL,
	}
	app := NewApp(&fakePoolManager{fakePool: pool}, backend, cfg, func() time.Time {
		return time.Unix(1_700_000_000, 0)
	})
	handler := NewServerHandler(cfg, app)
	return pool, backend, handler
}

func TestLoadConfigFailsWhenAdminTokenMissing(t *testing.T) {
	t.Helper()

	_, err := LoadConfig([]string{}, func(key string) string {
		values := map[string]string{
			"PORT":              "18080",
			"MAIL_API_BASE_URL": "https://mail.example.com",
			"MAIL_DOMAIN":       "example.com",
			"MAIL_ADMIN_TOKEN":  "mail-token",
			"API_BEARER_TOKEN":  "api-token",
		}
		return values[key]
	})
	if err == nil {
		t.Fatalf("expected missing config error, got nil")
	}
	if !strings.Contains(err.Error(), "ADMIN_TOKEN") {
		t.Fatalf("expected missing ADMIN_TOKEN error, got %v", err)
	}
}

func TestLoadConfigReadsPoolStorePathFromEnv(t *testing.T) {
	t.Helper()

	cfg, err := LoadConfig([]string{}, func(key string) string {
		values := map[string]string{
			"PORT":              "18080",
			"MAIL_API_BASE_URL": "https://mail.example.com",
			"MAIL_DOMAIN":       "example.com",
			"MAIL_ADMIN_TOKEN":  "mail-token",
			"API_BEARER_TOKEN":  "api-token",
			"ADMIN_TOKEN":       "admin-token",
			"POOL_STORE_PATH":   "/data/holo-image/pool.json",
		}
		return values[key]
	})
	if err != nil {
		t.Fatalf("expected config to load, got %v", err)
	}
	if cfg.PoolStorePath != "/data/holo-image/pool.json" {
		t.Fatalf("expected pool store path to load from env, got %+v", cfg)
	}
}

func TestLoadConfigReadsAdminMigrationFieldsFromEnv(t *testing.T) {
	t.Helper()

	cfg, err := LoadConfig([]string{}, func(key string) string {
		values := map[string]string{
			"PORT":                        "18080",
			"MAIL_API_BASE_URL":           "https://mail.example.com",
			"MAIL_DOMAIN":                 "example.com",
			"MAIL_ADMIN_TOKEN":            "mail-token",
			"API_BEARER_TOKEN":            "api-token",
			"ADMIN_TOKEN":                 "admin-token",
			"INSTANCE_NAME":               "holo-image-api-eners",
			"SERVICE_LABEL":               "holo-image-api-eners",
			"DEPLOY_SOURCE":               "ghcr-preview",
			"IMAGE_REF":                   "ghcr.io/thewisewolfholo/chataibot2api:main",
			"PUBLIC_BASE_URL":             "https://holo-image-api-eners.zeabur.app",
			"PRIMARY_PUBLIC_BASE_URL":     "https://holo-image-api.zeabur.app",
			"LEGACY_POOL_EXPORT_BASE_URL": "https://holo-image-api.zeabur.app",
		}
		return values[key]
	})
	if err != nil {
		t.Fatalf("expected config to load, got %v", err)
	}
	if cfg.InstanceName != "holo-image-api-eners" {
		t.Fatalf("expected instance name, got %+v", cfg)
	}
	if cfg.PublicBaseURL != "https://holo-image-api-eners.zeabur.app" {
		t.Fatalf("expected public base url, got %+v", cfg)
	}
	if cfg.PrimaryPublicBaseURL != "https://holo-image-api.zeabur.app" {
		t.Fatalf("expected primary public base url, got %+v", cfg)
	}
	if cfg.LegacyPoolExportBaseURL != "https://holo-image-api.zeabur.app" {
		t.Fatalf("expected legacy export URL, got %+v", cfg)
	}
	if cfg.ServiceLabel != "holo-image-api-eners" {
		t.Fatalf("expected service label, got %+v", cfg)
	}
	if cfg.DeploySource != "ghcr-preview" {
		t.Fatalf("expected deploy source, got %+v", cfg)
	}
	if cfg.ImageRef != "ghcr.io/thewisewolfholo/chataibot2api:main" {
		t.Fatalf("expected image ref, got %+v", cfg)
	}
}

func TestNewServerHandlerExposesPublicHealthz(t *testing.T) {
	t.Helper()

	_, _, handler := newTestHandler()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, recorder.Code)
	}
	if strings.TrimSpace(recorder.Body.String()) != "ok" {
		t.Fatalf("expected body ok, got %q", recorder.Body.String())
	}
}

func TestModelsEndpointListsSupportedModels(t *testing.T) {
	t.Helper()

	_, _, handler := newTestHandler()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer api-token")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, recorder.Code, recorder.Body.String())
	}

	var resp struct {
		Object string `json:"object"`
		Data   []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("expected JSON response, got error %v with body %s", err, recorder.Body.String())
	}
	if resp.Object != "list" {
		t.Fatalf("expected object=list, got %q", resp.Object)
	}

	modelIDs := make(map[string]struct{}, len(resp.Data))
	for _, item := range resp.Data {
		modelIDs[item.ID] = struct{}{}
	}
	if _, ok := modelIDs["gpt-image-1.5-high"]; !ok {
		t.Fatalf("expected gpt-image-1.5-high in model list, got %+v", resp.Data)
	}
	if _, ok := modelIDs["gpt-4.1"]; !ok {
		t.Fatalf("expected gpt-4.1 in model list, got %+v", resp.Data)
	}
	if _, ok := modelIDs["google-nano-banana"]; !ok {
		t.Fatalf("expected google-nano-banana in model list, got %+v", resp.Data)
	}
	if _, ok := modelIDs["gpt-4o-search-preview"]; ok {
		t.Fatalf("expected gated model gpt-4o-search-preview to be omitted, got %+v", resp.Data)
	}
	if _, ok := modelIDs["google-nano-banana-pro"]; ok {
		t.Fatalf("expected gated model google-nano-banana-pro to be omitted, got %+v", resp.Data)
	}
	if _, ok := modelIDs["google-nano-banana-2"]; ok {
		t.Fatalf("expected gated model google-nano-banana-2 to be omitted, got %+v", resp.Data)
	}
	if _, ok := modelIDs["midjourney-7"]; ok {
		t.Fatalf("expected gated model midjourney-7 to be omitted, got %+v", resp.Data)
	}
	if _, ok := modelIDs["gpt-5.4-pro"]; ok {
		t.Fatalf("expected hidden model gpt-5.4-pro to be omitted, got %+v", resp.Data)
	}
	if _, ok := modelIDs["o3-pro"]; ok {
		t.Fatalf("expected hidden model o3-pro to be omitted, got %+v", resp.Data)
	}
}

func TestChatCompletionsWrapsGenerateAsMarkdown(t *testing.T) {
	t.Helper()

	pool, backend, handler := newTestHandler()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-image-1.5-high",
		"messages":[{"role":"user","content":"draw a cat hacker"}]
	}`))
	req.Header.Set("Authorization", "Bearer api-token")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, recorder.Code, recorder.Body.String())
	}
	if pool.acquiredCost != 40 {
		t.Fatalf("expected cost 40, got %d", pool.acquiredCost)
	}
	if !backend.updateCalled || backend.lastRatio != "auto" {
		t.Fatalf("expected update user settings with auto ratio, got called=%v ratio=%q", backend.updateCalled, backend.lastRatio)
	}
	if backend.lastPrompt != "draw a cat hacker" {
		t.Fatalf("expected prompt to pass through, got %q", backend.lastPrompt)
	}

	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("expected JSON response, got error %v with body %s", err, recorder.Body.String())
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "![](https://img.example.com/generated.png)" {
		t.Fatalf("unexpected chat content: %s", recorder.Body.String())
	}
}

func TestChatCompletionsSupportsEditAndMerge(t *testing.T) {
	t.Helper()

	_, backend, handler := newTestHandler()

	editReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"google-nano-banana",
		"messages":[{
			"role":"user",
			"content":[
				{"type":"text","text":"edit this"},
				{"type":"image_url","image_url":{"url":"data:image/png;base64,abc"}}
			]
		}]
	}`))
	editReq.Header.Set("Authorization", "Bearer api-token")
	editReq.Header.Set("Content-Type", "application/json")
	editRecorder := httptest.NewRecorder()
	handler.ServeHTTP(editRecorder, editReq)
	if editRecorder.Code != http.StatusOK {
		t.Fatalf("expected edit status %d, got %d with body %s", http.StatusOK, editRecorder.Code, editRecorder.Body.String())
	}
	if backend.lastEditMode != "edit_google_nano_banana" || backend.lastImage != "data:image/png;base64,abc" {
		t.Fatalf("unexpected edit call mode=%q image=%q", backend.lastEditMode, backend.lastImage)
	}

	mergeReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"google-nano-banana",
		"messages":[{
			"role":"user",
			"content":[
				{"type":"text","text":"merge these"},
				{"type":"image_url","image_url":{"url":"data:image/png;base64,aaa"}},
				{"type":"image_url","image_url":{"url":"data:image/png;base64,bbb"}}
			]
		}]
	}`))
	mergeReq.Header.Set("Authorization", "Bearer api-token")
	mergeReq.Header.Set("Content-Type", "application/json")
	mergeRecorder := httptest.NewRecorder()
	handler.ServeHTTP(mergeRecorder, mergeReq)
	if mergeRecorder.Code != http.StatusOK {
		t.Fatalf("expected merge status %d, got %d with body %s", http.StatusOK, mergeRecorder.Code, mergeRecorder.Body.String())
	}
	if backend.lastMerge != "merge_google_nano_banana" || len(backend.lastImages) != 2 {
		t.Fatalf("unexpected merge call mode=%q images=%v", backend.lastMerge, backend.lastImages)
	}
}

func TestChatCompletionsSupportsGptImage15EditAndMerge(t *testing.T) {
	t.Helper()

	_, backend, handler := newTestHandler()

	editReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-image-1.5",
		"messages":[{
			"role":"user",
			"content":[
				{"type":"text","text":"edit this"},
				{"type":"image_url","image_url":{"url":"data:image/png;base64,abc"}}
			]
		}]
	}`))
	editReq.Header.Set("Authorization", "Bearer api-token")
	editReq.Header.Set("Content-Type", "application/json")
	editRecorder := httptest.NewRecorder()
	handler.ServeHTTP(editRecorder, editReq)
	if editRecorder.Code != http.StatusOK {
		t.Fatalf("expected edit status %d, got %d with body %s", http.StatusOK, editRecorder.Code, editRecorder.Body.String())
	}
	if backend.lastEditMode != "edit_gpt_1_5" || backend.lastImage != "data:image/png;base64,abc" {
		t.Fatalf("unexpected gpt-image-1.5 edit call mode=%q image=%q", backend.lastEditMode, backend.lastImage)
	}

	mergeReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-image-1.5-high",
		"messages":[{
			"role":"user",
			"content":[
				{"type":"text","text":"merge these"},
				{"type":"image_url","image_url":{"url":"data:image/png;base64,aaa"}},
				{"type":"image_url","image_url":{"url":"data:image/png;base64,bbb"}}
			]
		}]
	}`))
	mergeReq.Header.Set("Authorization", "Bearer api-token")
	mergeReq.Header.Set("Content-Type", "application/json")
	mergeRecorder := httptest.NewRecorder()
	handler.ServeHTTP(mergeRecorder, mergeReq)
	if mergeRecorder.Code != http.StatusOK {
		t.Fatalf("expected merge status %d, got %d with body %s", http.StatusOK, mergeRecorder.Code, mergeRecorder.Body.String())
	}
	if backend.lastMerge != "merge_gpt_1_5_high" || len(backend.lastImages) != 2 {
		t.Fatalf("unexpected gpt-image-1.5-high merge call mode=%q images=%v", backend.lastMerge, backend.lastImages)
	}
}

func TestChatCompletionsRejectsHiddenImageModelsAsUnsupported(t *testing.T) {
	t.Helper()

	_, backend, handler := newTestHandler()

	editReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"google-nano-banana-pro",
		"messages":[{
			"role":"user",
			"content":[
				{"type":"text","text":"edit this"},
				{"type":"image_url","image_url":{"url":"data:image/png;base64,abc"}}
			]
		}]
	}`))
	editReq.Header.Set("Authorization", "Bearer api-token")
	editReq.Header.Set("Content-Type", "application/json")
	editRecorder := httptest.NewRecorder()
	handler.ServeHTTP(editRecorder, editReq)
	if editRecorder.Code != http.StatusBadRequest {
		t.Fatalf("expected hidden image model status %d, got %d with body %s", http.StatusBadRequest, editRecorder.Code, editRecorder.Body.String())
	}
	if !strings.Contains(editRecorder.Body.String(), "Unsupported model") {
		t.Fatalf("expected unsupported model response for hidden image model, got %s", editRecorder.Body.String())
	}
	if backend.lastEditMode != "" || backend.lastMerge != "" {
		t.Fatalf("expected hidden image model to be rejected before backend call, got edit=%q merge=%q", backend.lastEditMode, backend.lastMerge)
	}
}

func TestImagesGenerationsRejectsHiddenImageModelsAsUnsupported(t *testing.T) {
	t.Helper()

	_, backend, handler := newTestHandler()
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{
		"model":"midjourney-7",
		"prompt":"draw a hidden model probe"
	}`))
	req.Header.Set("Authorization", "Bearer api-token")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected hidden image model status %d, got %d with body %s", http.StatusBadRequest, recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "Unsupported model") {
		t.Fatalf("expected unsupported model response for hidden image generation, got %s", recorder.Body.String())
	}
	if backend.lastModel != "" {
		t.Fatalf("expected hidden image model to be rejected before backend call, got model=%q", backend.lastModel)
	}
}

func TestChatCompletionsSupportsTextChat(t *testing.T) {
	t.Helper()

	pool, backend, handler := newTestHandler()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-4.1",
		"messages":[
			{"role":"system","content":"You are concise."},
			{"role":"user","content":"Say hello in one word."}
		]
	}`))
	req.Header.Set("Authorization", "Bearer api-token")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, recorder.Code, recorder.Body.String())
	}
	if pool.acquiredCost != 3 {
		t.Fatalf("expected cost 3 for gpt-4.1, got %d", pool.acquiredCost)
	}
	if backend.textContextModel != "gpt-4.1" {
		t.Fatalf("expected text context model gpt-4.1, got %q", backend.textContextModel)
	}
	if !strings.Contains(backend.textRequest.Text, "You are concise.") || !strings.Contains(backend.textRequest.Text, "Say hello in one word.") {
		t.Fatalf("expected flattened prompt to include system and user content, got %q", backend.textRequest.Text)
	}
	if backend.textRequest.Model != "gpt-4.1" {
		t.Fatalf("expected text request model gpt-4.1, got %q", backend.textRequest.Model)
	}

	var resp struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("expected JSON response, got error %v with body %s", err, recorder.Body.String())
	}
	if resp.Model != "gpt-4.1" {
		t.Fatalf("expected response model gpt-4.1, got %q", resp.Model)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "hello from text backend" {
		t.Fatalf("unexpected text chat content: %s", recorder.Body.String())
	}
}

func TestChatCompletionsAutoContinuesTruncatedCodeResponses(t *testing.T) {
	t.Helper()

	_, backend, handler := newTestHandler()
	truncatedPrefix := "```html\n<!DOCTYPE html>\n<html>\n<body>\n<script>\n"
	truncatedBody := "const wheel = {\n  prizes: [1,2,3,4,5,6],\n  colors: ['#f00','#0f0','#00f'],\n};\nfunction draw(){\n  const ctx = canvas.getContext('2d');\n  ctx.fillStyle = '#fff';\n}\n"
	backend.textResponses = []TextCompletionResult{
		{
			ChatModel: "claude-4.6-sonnet",
			Content:   truncatedPrefix + strings.Repeat(truncatedBody, 8) + "ctx.shadowColor =",
		},
		{
			ChatModel: "claude-4.6-sonnet",
			Content:   "  prizes: 6 };\n</script>\n</body>\n</html>\n```",
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"claude-4.6-sonnet",
		"messages":[{"role":"user","content":"Write a single-file HTML page with CSS and JavaScript only. Return code only."}]
	}`))
	req.Header.Set("Authorization", "Bearer api-token")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, recorder.Code, recorder.Body.String())
	}
	if backend.textCallCount != 2 {
		t.Fatalf("expected 2 text backend calls for continuation, got %d", backend.textCallCount)
	}

	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("expected JSON response, got error %v with body %s", err, recorder.Body.String())
	}
	content := resp.Choices[0].Message.Content
	if !strings.Contains(content, "prizes: 6") || !strings.HasSuffix(strings.TrimSpace(content), "```") {
		t.Fatalf("expected stitched continuation content, got %s", recorder.Body.String())
	}
}

func TestSanitizeMergedCodeContinuationTrimsTrailingExplanationAfterHTML(t *testing.T) {
	t.Helper()

	input := "```html\n<!DOCTYPE html>\n<html>\n<body>\n<script>\nconsole.log('ok');\n</script>\n</body>\n</html>This is already the complete end of the HTML file."
	got := sanitizeMergedCodeContinuation(input)

	if !strings.HasSuffix(strings.TrimSpace(got), "```") {
		t.Fatalf("expected sanitizer to close code fence, got %q", got)
	}
	if strings.Contains(got, "This is already the complete end") {
		t.Fatalf("expected trailing explanation to be removed, got %q", got)
	}
	if !strings.Contains(got, "</html>\n```") {
		t.Fatalf("expected html document to be preserved and fenced, got %q", got)
	}
}

func TestSanitizeMergedCodeContinuationKeepsLastCompleteHTMLDocument(t *testing.T) {
	t.Helper()

	input := "```html\n<!DOCTYPE html>\n<html>\n<body>\nfirst\n</body>\n</html><!DOCTYPE html>\n<html>\n<body>\nsecond\n</body>\n</html>\n```"
	got := sanitizeMergedCodeContinuation(input)

	if strings.Contains(got, "first") {
		t.Fatalf("expected earlier duplicated html document to be dropped, got %q", got)
	}
	if !strings.Contains(got, "second") {
		t.Fatalf("expected last complete html document to be kept, got %q", got)
	}
	if !strings.HasSuffix(strings.TrimSpace(got), "```") {
		t.Fatalf("expected sanitized duplicated html to end with code fence, got %q", got)
	}
}

func TestChatCompletionsDoesNotContinueCompleteTextResponse(t *testing.T) {
	t.Helper()

	_, backend, handler := newTestHandler()
	backend.textResponse = TextCompletionResult{
		ChatModel: "gpt-4.1",
		Content:   "All done.",
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-4.1",
		"messages":[{"role":"user","content":"Say all done."}]
	}`))
	req.Header.Set("Authorization", "Bearer api-token")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, recorder.Code, recorder.Body.String())
	}
	if backend.textCallCount != 1 {
		t.Fatalf("expected 1 text backend call without continuation, got %d", backend.textCallCount)
	}
}

func TestChatCompletionsStreamsMarkdown(t *testing.T) {
	t.Helper()

	_, _, handler := newTestHandler()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-image-1.5-high",
		"stream":true,
		"messages":[{"role":"user","content":"draw a cat hacker"}]
	}`))
	req.Header.Set("Authorization", "Bearer api-token")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "text/event-stream") && !strings.Contains(recorder.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("expected SSE content type, got headers=%v body=%s", recorder.Header(), recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "![](https://img.example.com/generated.png)") {
		t.Fatalf("expected markdown image in stream body, got %s", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "[DONE]") {
		t.Fatalf("expected [DONE] in stream body, got %s", recorder.Body.String())
	}
}

func TestChatCompletionsStreamsTextChat(t *testing.T) {
	t.Helper()

	_, backend, handler := newTestHandler()
	backend.textStreamEvents = []TextStreamEvent{
		{Type: "botType", ChatModel: "gpt-4.1"},
		{Type: "chunk", Delta: "hello"},
		{Type: "chunk", Delta: " world"},
	}
	backend.textStreamResponse = TextCompletionResult{
		ChatModel: "gpt-4.1",
		Content:   "hello world",
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-4.1",
		"stream":true,
		"messages":[{"role":"user","content":"Say hello"}]
	}`))
	req.Header.Set("Authorization", "Bearer api-token")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("expected SSE content type, got headers=%v body=%s", recorder.Header(), recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"role":"assistant"`) {
		t.Fatalf("expected assistant role prelude in stream body, got %s", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"model":"gpt-4.1"`) {
		t.Fatalf("expected stream model gpt-4.1, got %s", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"content":"hello"`) || !strings.Contains(recorder.Body.String(), `"content":" world"`) {
		t.Fatalf("expected streamed text deltas, got %s", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "[DONE]") {
		t.Fatalf("expected [DONE] in stream body, got %s", recorder.Body.String())
	}
	if backend.textCallCount != 0 {
		t.Fatalf("expected no continuation call for complete stream, got %d", backend.textCallCount)
	}
}

func TestChatCompletionsSplitsLargeSingleStreamChunk(t *testing.T) {
	t.Helper()

	_, backend, handler := newTestHandler()
	backend.textStreamEvents = []TextStreamEvent{
		{Type: "botType", ChatModel: "gpt-4.1-nano"},
		{Type: "chunk", Delta: "1\n2\n3\n4\n5"},
	}
	backend.textStreamResponse = TextCompletionResult{
		ChatModel: "gpt-4.1-nano",
		Content:   "1\n2\n3\n4\n5",
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-4.1-nano",
		"stream":true,
		"messages":[{"role":"user","content":"Count to five"}]
	}`))
	req.Header.Set("Authorization", "Bearer api-token")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	body := recorder.Body.String()
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, recorder.Code, body)
	}
	if !strings.Contains(body, `"role":"assistant"`) {
		t.Fatalf("expected assistant role prelude in stream body, got %s", body)
	}
	if !strings.Contains(body, `"content":"1`) || !strings.Contains(body, `"content":"2`) || !strings.Contains(body, `"content":"5"`) {
		t.Fatalf("expected split line-by-line deltas in stream body, got %s", body)
	}
	if strings.Count(body, `"object":"chat.completion.chunk"`) < 6 {
		t.Fatalf("expected multiple chat completion chunks after splitting, got %s", body)
	}
	if !strings.Contains(body, "[DONE]") {
		t.Fatalf("expected [DONE] in stream body, got %s", body)
	}
}

func TestChatCompletionsStreamsTextChatAutoContinuesTruncatedCodeResponses(t *testing.T) {
	t.Helper()

	_, backend, handler := newTestHandler()
	truncatedPrefix := "```html\n<!DOCTYPE html>\n<html>\n<body>\n<script>\n"
	truncatedBody := "const wheel = {\n  prizes: [1,2,3,4,5,6],\n  colors: ['#f00','#0f0','#00f'],\n};\nfunction draw(){\n  const ctx = canvas.getContext('2d');\n  ctx.fillStyle = '#fff';\n}\n"
	truncatedContent := truncatedPrefix + strings.Repeat(truncatedBody, 8) + "ctx.shadowColor ="
	backend.textStreamEvents = []TextStreamEvent{
		{Type: "botType", ChatModel: "claude-4.6-sonnet"},
		{Type: "chunk", Delta: truncatedPrefix + strings.Repeat(truncatedBody, 4)},
		{Type: "chunk", Delta: strings.Repeat(truncatedBody, 4) + "ctx.shadowColor ="},
	}
	backend.textStreamResponse = TextCompletionResult{
		ChatModel: "claude-4.6-sonnet",
		Content:   truncatedContent,
	}
	backend.textResponses = []TextCompletionResult{
		{
			ChatModel: "claude-4.6-sonnet",
			Content:   "  prizes: 6 };\n</script>\n</body>\n</html>\n```",
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"claude-4.6-sonnet",
		"stream":true,
		"messages":[{"role":"user","content":"Write a single-file HTML page with CSS and JavaScript only. Return code only."}]
	}`))
	req.Header.Set("Authorization", "Bearer api-token")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("expected SSE content type, got headers=%v body=%s", recorder.Header(), recorder.Body.String())
	}
	if backend.textCallCount != 1 {
		t.Fatalf("expected 1 continuation call for truncated stream, got %d", backend.textCallCount)
	}
	if !strings.Contains(recorder.Body.String(), "ctx.shadowColor =") {
		t.Fatalf("expected truncated stream prefix to be preserved, got %s", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "prizes: 6") {
		t.Fatalf("expected continuation delta in stream body, got %s", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "[DONE]") {
		t.Fatalf("expected [DONE] in stream body, got %s", recorder.Body.String())
	}
}

func TestChatCompletionsRejectsModelDowngrade(t *testing.T) {
	t.Helper()

	_, backend, handler := newTestHandler()
	backend.textResponse = TextCompletionResult{
		ChatModel: "gpt-4.1-nano",
		Content:   "downgraded",
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-4.1",
		"messages":[{"role":"user","content":"Hello"}]
	}`))
	req.Header.Set("Authorization", "Bearer api-token")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadGateway, recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "gpt-4.1-nano") || !strings.Contains(recorder.Body.String(), "gpt-4.1") {
		t.Fatalf("expected downgrade error to mention actual/requested models, got %s", recorder.Body.String())
	}
}

func TestChatCompletionsSurfacesSubscriptionErrorsAsForbidden(t *testing.T) {
	t.Helper()

	_, backend, handler := newTestHandler()
	backend.textErr = &protocol.UpstreamError{
		StatusCode: http.StatusForbidden,
		Message:    "The model is available through subscriptions Pro 🚀, Business 💼",
		Type:       "CanNotChangeGPTModel",
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-4.1",
		"messages":[{"role":"user","content":"Hello"}]
	}`))
	req.Header.Set("Authorization", "Bearer api-token")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusForbidden, recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "subscriptions Pro") {
		t.Fatalf("expected subscription guidance in response, got %s", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "CanNotChangeGPTModel") {
		t.Fatalf("expected upstream error type to be preserved, got %s", recorder.Body.String())
	}
}

func TestChatCompletionsSurfacesImageSubscriptionErrorsAsForbidden(t *testing.T) {
	t.Helper()

	_, backend, handler := newTestHandler()
	backend.editErr = &protocol.UpstreamError{
		StatusCode: http.StatusForbidden,
		Message:    "The model is available through subscriptions Standard ⭐, Premium 💎, Pro 🚀, Business 💼",
		Type:       "CanNotUseGptImageGenerate",
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-image-1.5",
		"messages":[{
			"role":"user",
			"content":[
				{"type":"text","text":"edit this"},
				{"type":"image_url","image_url":{"url":"data:image/png;base64,abc"}}
			]
		}]
	}`))
	req.Header.Set("Authorization", "Bearer api-token")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusForbidden, recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "subscriptions Standard") {
		t.Fatalf("expected subscription guidance in response, got %s", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "CanNotUseGptImageGenerate") {
		t.Fatalf("expected upstream image error type to be preserved, got %s", recorder.Body.String())
	}
}

func TestChatCompletionsRejectsHiddenTextModelsAsUnsupported(t *testing.T) {
	t.Helper()

	_, backend, handler := newTestHandler()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-5.4-pro",
		"messages":[{"role":"user","content":"Hello"}]
	}`))
	req.Header.Set("Authorization", "Bearer api-token")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "Unsupported model") {
		t.Fatalf("expected unsupported model response, got %s", recorder.Body.String())
	}
	if backend.textContextModel != "" {
		t.Fatalf("expected hidden model to be rejected before backend context creation, got context model %q", backend.textContextModel)
	}
}

func TestChatCompletionsRejectsTextImageInputs(t *testing.T) {
	t.Helper()

	_, _, handler := newTestHandler()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-4.1",
		"messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":"data:image/png;base64,abc"}}]}]
	}`))
	req.Header.Set("Authorization", "Bearer api-token")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "image inputs are not supported") {
		t.Fatalf("expected unsupported image input error, got %s", recorder.Body.String())
	}
}

func TestChatCompletionsRejectsUnknownModel(t *testing.T) {
	t.Helper()

	_, _, handler := newTestHandler()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-4o-mini",
		"messages":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("Authorization", "Bearer api-token")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "Unsupported model") {
		t.Fatalf("expected unsupported model error, got %s", recorder.Body.String())
	}
}

func TestAdminPoolEndpointsRequireAdminTokenAndExposeStatus(t *testing.T) {
	t.Helper()

	pool, _, handler := newTestHandler()
	pool.status = PoolStatus{
		ReadyCount:            2,
		ReusableCount:         1,
		TotalCount:            3,
		WorkerCount:           3,
		ActiveRegistrations:   1,
		RegistrationSuccesses: 5,
		RegistrationFailures:  2,
	}

	unauthReq := httptest.NewRequest(http.MethodGet, "/v1/admin/pool", nil)
	unauthRecorder := httptest.NewRecorder()
	handler.ServeHTTP(unauthRecorder, unauthReq)
	if unauthRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized status, got %d", unauthRecorder.Code)
	}

	authReq := httptest.NewRequest(http.MethodGet, "/v1/admin/pool", nil)
	authReq.Header.Set("Authorization", "Bearer admin-token")
	authRecorder := httptest.NewRecorder()
	handler.ServeHTTP(authRecorder, authReq)
	if authRecorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, authRecorder.Code, authRecorder.Body.String())
	}
	if !strings.Contains(authRecorder.Body.String(), `"total_count":3`) {
		t.Fatalf("expected pool status in response, got %s", authRecorder.Body.String())
	}
}

func TestAdminFillAndPruneEndpointsUsePoolManager(t *testing.T) {
	t.Helper()

	pool, _, handler := newTestHandler()
	pool.fillTask = FillTaskSnapshot{ID: "task-42", Requested: 3, Status: "running"}
	pool.pruneResult = PruneSummary{Checked: 4, Removed: 2, Remaining: 2}

	fillReq := httptest.NewRequest(http.MethodPost, "/v1/admin/pool/fill", strings.NewReader(`{"count":3}`))
	fillReq.Header.Set("Authorization", "Bearer admin-token")
	fillReq.Header.Set("Content-Type", "application/json")
	fillRecorder := httptest.NewRecorder()
	handler.ServeHTTP(fillRecorder, fillReq)
	if fillRecorder.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusAccepted, fillRecorder.Code, fillRecorder.Body.String())
	}
	if len(pool.fillCounts) != 1 || pool.fillCounts[0] != 3 {
		t.Fatalf("expected fill count 3, got %v", pool.fillCounts)
	}
	if !strings.Contains(fillRecorder.Body.String(), `"task_id":"task-42"`) {
		t.Fatalf("expected task id in response, got %s", fillRecorder.Body.String())
	}

	pruneReq := httptest.NewRequest(http.MethodPost, "/v1/admin/pool/prune", nil)
	pruneReq.Header.Set("Authorization", "Bearer admin-token")
	pruneRecorder := httptest.NewRecorder()
	handler.ServeHTTP(pruneRecorder, pruneReq)
	if pruneRecorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, pruneRecorder.Code, pruneRecorder.Body.String())
	}
	if !strings.Contains(pruneRecorder.Body.String(), `"removed":2`) {
		t.Fatalf("expected prune summary in response, got %s", pruneRecorder.Body.String())
	}
}

func TestAdminPoolImportRequiresAdminTokenAndFiltersInvalidJWTs(t *testing.T) {
	t.Helper()

	pool, backend, handler := newTestHandler()
	backend.quotaByJWT = map[string]int{
		"jwt-good": 17,
		"jwt-low":  1,
	}
	pool.importResult = ImportPoolResult{
		Imported:   1,
		Duplicates: 0,
		TotalCount: 1,
	}

	unauthReq := httptest.NewRequest(http.MethodPost, "/v1/admin/pool/import", strings.NewReader(`{"jwts":["jwt-good"]}`))
	unauthReq.Header.Set("Content-Type", "application/json")
	unauthRecorder := httptest.NewRecorder()
	handler.ServeHTTP(unauthRecorder, unauthReq)
	if unauthRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized status, got %d", unauthRecorder.Code)
	}

	authReq := httptest.NewRequest(http.MethodPost, "/v1/admin/pool/import", strings.NewReader(`{
		"jwts":[" jwt-good ","","jwt-low","jwt-good"]
	}`))
	authReq.Header.Set("Authorization", "Bearer admin-token")
	authReq.Header.Set("Content-Type", "application/json")
	authRecorder := httptest.NewRecorder()
	handler.ServeHTTP(authRecorder, authReq)
	if authRecorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, authRecorder.Code, authRecorder.Body.String())
	}

	if len(pool.imported) != 1 {
		t.Fatalf("expected exactly 1 validated account to reach pool import, got %d", len(pool.imported))
	}
	if pool.imported[0].JWT != "jwt-good" || pool.imported[0].Quota != 17 {
		t.Fatalf("unexpected imported account: %+v", pool.imported[0])
	}
	if len(backend.getCountCalls) != 2 {
		t.Fatalf("expected quota validation for unique non-empty jwts only, got %v", backend.getCountCalls)
	}
	if backend.getCountCalls[0] != "jwt-good" || backend.getCountCalls[1] != "jwt-low" {
		t.Fatalf("unexpected validation order/calls: %v", backend.getCountCalls)
	}
	if !strings.Contains(authRecorder.Body.String(), `"requested":4`) {
		t.Fatalf("expected request count in response, got %s", authRecorder.Body.String())
	}
	if !strings.Contains(authRecorder.Body.String(), `"rejected":2`) {
		t.Fatalf("expected rejected count in response, got %s", authRecorder.Body.String())
	}
	if !strings.Contains(authRecorder.Body.String(), `"imported":1`) {
		t.Fatalf("expected imported count in response, got %s", authRecorder.Body.String())
	}
}

func TestAdminPoolExportRequiresAdminTokenAndReturnsSnapshot(t *testing.T) {
	t.Helper()

	pool, _, handler := newTestHandler()
	pool.exported = []ExportedAccount{
		{JWT: "jwt-1", Quota: 65},
		{JWT: "jwt-2", Quota: 12},
	}

	unauthReq := httptest.NewRequest(http.MethodGet, "/v1/admin/pool/export", nil)
	unauthRec := httptest.NewRecorder()
	handler.ServeHTTP(unauthRec, unauthReq)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized export status, got %d", unauthRec.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/pool/export", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"jwt":"jwt-1"`) || !strings.Contains(rec.Body.String(), `"jwt":"jwt-2"`) {
		t.Fatalf("expected exported jwts in response, got %s", rec.Body.String())
	}
}

func TestAdminQuotaSnapshotEndpointReturnsSummaryAndRows(t *testing.T) {
	t.Helper()

	pool, _, handler := newTestHandler()
	pool.adminRows = []AdminQuotaRow{
		{JWT: "jwt-healthy", Quota: 16, Status: "healthy", PoolBucket: "ready"},
		{JWT: "jwt-low", Quota: 7, Status: "low", PoolBucket: "reusable"},
		{JWT: "jwt-near", Quota: 3, Status: "near-empty", PoolBucket: "borrowed"},
	}

	unauthReq := httptest.NewRequest(http.MethodGet, "/v1/admin/quota/snapshot", nil)
	unauthRec := httptest.NewRecorder()
	handler.ServeHTTP(unauthRec, unauthReq)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized snapshot status, got %d body=%s", unauthRec.Code, unauthRec.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/quota/snapshot", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var payload AdminQuotaSnapshot
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("expected valid snapshot payload, got err=%v body=%s", err, rec.Body.String())
	}
	if payload.Summary.TotalCount != 3 {
		t.Fatalf("expected total_count=3, got %+v", payload.Summary)
	}
	if payload.Summary.TotalQuota != 26 {
		t.Fatalf("expected total_quota=26, got %+v", payload.Summary)
	}
	if payload.Summary.LowQuotaCount != 2 {
		t.Fatalf("expected low_quota_count=2, got %+v", payload.Summary)
	}
	if payload.Summary.NearEmptyCount != 1 {
		t.Fatalf("expected near_empty_count=1, got %+v", payload.Summary)
	}
	if len(payload.Rows) != 3 {
		t.Fatalf("expected 3 rows, got %+v", payload.Rows)
	}
	if payload.Rows[0].JWT != "jwt-near" || payload.Rows[0].PoolBucket != "borrowed" || payload.Rows[0].Status != "near-empty" {
		t.Fatalf("expected near-empty row first, got %+v", payload.Rows)
	}
	if payload.Rows[1].JWT != "jwt-low" || payload.Rows[1].PoolBucket != "reusable" || payload.Rows[1].Status != "low" {
		t.Fatalf("expected low row second, got %+v", payload.Rows)
	}
	if payload.Rows[2].JWT != "jwt-healthy" || payload.Rows[2].PoolBucket != "ready" || payload.Rows[2].Status != "healthy" {
		t.Fatalf("expected healthy row last, got %+v", payload.Rows)
	}
}

func TestAdminQuotaProbeEndpointIsReadOnlyAndReturnsRowResults(t *testing.T) {
	t.Helper()

	pool, backend, handler := newTestHandler()
	backend.quotaByJWT = map[string]int{
		"jwt-a": 12,
		"jwt-b": 4,
	}

	unauthReq := httptest.NewRequest(http.MethodPost, "/v1/admin/quota/probe", strings.NewReader(`{"jwts":["jwt-a"]}`))
	unauthReq.Header.Set("Content-Type", "application/json")
	unauthRec := httptest.NewRecorder()
	handler.ServeHTTP(unauthRec, unauthReq)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized probe status, got %d body=%s", unauthRec.Code, unauthRec.Body.String())
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/quota/probe", strings.NewReader(`{"jwts":[" jwt-a ","","jwt-b","jwt-a"]}`))
	req.Header.Set("Authorization", "Bearer admin-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload AdminQuotaProbeResponse
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatalf("expected valid probe payload, got err=%v body=%s", err, rec.Body.String())
	}
	if len(backend.getCountCalls) != 2 {
		t.Fatalf("expected 2 quota calls, got %v", backend.getCountCalls)
	}
	if backend.getCountCalls[0] != "jwt-a" || backend.getCountCalls[1] != "jwt-b" {
		t.Fatalf("expected trimmed and deduped quota calls, got %v", backend.getCountCalls)
	}
	if pool.pruneCalls != 0 {
		t.Fatalf("probe must not call prune, got %d", pool.pruneCalls)
	}
	if len(pool.fillCounts) != 0 {
		t.Fatalf("probe must not call fill, got %v", pool.fillCounts)
	}
	if len(pool.imported) != 0 {
		t.Fatalf("probe must not import, got %+v", pool.imported)
	}
	expectedCheckedAt := time.Unix(1_700_000_000, 0).UTC()
	if !payload.CheckedAt.Equal(expectedCheckedAt) {
		t.Fatalf("expected checked_at=%s, got %+v", expectedCheckedAt, payload.CheckedAt)
	}
	if len(payload.Results) != 2 {
		t.Fatalf("expected 2 probe results, got %+v", payload.Results)
	}
	if !payload.Results[0].OK || payload.Results[0].JWT != "jwt-a" || payload.Results[0].Quota != 12 || payload.Results[0].Status != "healthy" {
		t.Fatalf("expected first probe result for jwt-a, got %+v", payload.Results)
	}
	if !payload.Results[1].OK || payload.Results[1].JWT != "jwt-b" || payload.Results[1].Quota != 4 || payload.Results[1].Status != "near-empty" {
		t.Fatalf("expected second probe result for jwt-b, got %+v", payload.Results)
	}
}

func TestAdminMetaEndpointReturnsInstanceInformation(t *testing.T) {
	t.Helper()

	_, _, handler := newTestHandler()
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/meta", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"instance_name":"test-instance"`) {
		t.Fatalf("expected instance name in response, got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"primary_public_base_url":"https://holo-image-api.zeabur.app"`) {
		t.Fatalf("expected primary public base url in response, got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"service_label":"holo-image-api-eners"`) {
		t.Fatalf("expected service label in response, got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"deploy_source":"ghcr-preview"`) {
		t.Fatalf("expected deploy source in response, got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"image_ref":"ghcr.io/thewisewolfholo/chataibot2api:main"`) {
		t.Fatalf("expected image ref in response, got %s", rec.Body.String())
	}
}

func TestAdminMigrationStatusEndpointReturnsCurrentState(t *testing.T) {
	t.Helper()

	_, _, handler := newTestHandler()
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/migration/status", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"total_count":0`) {
		t.Fatalf("expected migration status payload, got %s", rec.Body.String())
	}
}

func TestAdminCatalogEndpointReturnsTextAndImageModels(t *testing.T) {
	t.Helper()

	_, _, handler := newTestHandler()
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/catalog", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"gpt-4.1"`) {
		t.Fatalf("expected text model in catalog, got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"gpt-image-1.5"`) {
		t.Fatalf("expected image model in catalog, got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"low_quota_threshold":10`) {
		t.Fatalf("expected low quota threshold metadata, got %s", rec.Body.String())
	}
}

func TestHandleAdminDashboardPageServesQuotaFirstLayout(t *testing.T) {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	rec := httptest.NewRecorder()
	HandleAdminDashboardPage(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	if strings.Contains(body, ">LIVE<") {
		t.Fatalf("expected LIVE decoration removed, got %s", body)
	}
	if !strings.Contains(body, `id="quotaOverviewSection"`) {
		t.Fatalf("expected quota overview section, got %s", body)
	}
	if !strings.Contains(body, `id="quotaTableSection"`) {
		t.Fatalf("expected quota table section, got %s", body)
	}
	if !strings.Contains(body, `<details class="surface fold-panel"`) {
		t.Fatalf("expected collapsed secondary sections, got %s", body)
	}
}

func TestAdminUIRoutesServeLoginPageAndAssets(t *testing.T) {
	t.Helper()

	_, _, handler := newTestHandler()

	pageReq := httptest.NewRequest(http.MethodGet, "/admin/login", nil)
	pageRec := httptest.NewRecorder()
	handler.ServeHTTP(pageRec, pageReq)
	if pageRec.Code != http.StatusOK {
		t.Fatalf("expected login page 200, got %d body=%s", pageRec.Code, pageRec.Body.String())
	}
	if !strings.Contains(pageRec.Body.String(), "后台登录") {
		t.Fatalf("expected login html, got %s", pageRec.Body.String())
	}

	assetReq := httptest.NewRequest(http.MethodGet, "/admin/assets/login.js", nil)
	assetRec := httptest.NewRecorder()
	handler.ServeHTTP(assetRec, assetReq)
	if assetRec.Code != http.StatusOK {
		t.Fatalf("expected admin asset 200, got %d body=%s", assetRec.Code, assetRec.Body.String())
	}
	if !strings.Contains(assetRec.Body.String(), "session/login") {
		t.Fatalf("expected admin asset content, got %s", assetRec.Body.String())
	}
}

func TestAdminDashboardAssetContainsQuotaEndpoints(t *testing.T) {
	t.Helper()

	_, _, handler := newTestHandler()
	req := httptest.NewRequest(http.MethodGet, "/admin/assets/app.js", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/v1/admin/quota/snapshot") {
		t.Fatalf("expected snapshot endpoint usage, got %s", body)
	}
	if !strings.Contains(body, "/v1/admin/quota/probe") {
		t.Fatalf("expected probe endpoint usage, got %s", body)
	}
	if !strings.Contains(body, "toggleJwtVisibility") {
		t.Fatalf("expected JWT expand behavior, got %s", body)
	}
}

func TestAdminRequiresSessionAndRedirectsWhenMissing(t *testing.T) {
	t.Helper()

	_, _, handler := newTestHandler()

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected redirect to login, got %d body=%s", rec.Code, rec.Body.String())
	}
	if location := rec.Header().Get("Location"); location != "/admin/login" {
		t.Fatalf("expected redirect location /admin/login, got %q", location)
	}
}

func TestAdminSessionLoginLogoutAndCookieAccess(t *testing.T) {
	t.Helper()

	pool, _, handler := newTestHandler()
	pool.status = PoolStatus{TotalCount: 9}

	badLoginReq := httptest.NewRequest(http.MethodPost, "/v1/admin/session/login", strings.NewReader(`{"admin_key":"wrong"}`))
	badLoginReq.Header.Set("Content-Type", "application/json")
	badLoginRec := httptest.NewRecorder()
	handler.ServeHTTP(badLoginRec, badLoginReq)
	if badLoginRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized login, got %d body=%s", badLoginRec.Code, badLoginRec.Body.String())
	}

	loginReq := httptest.NewRequest(http.MethodPost, "/v1/admin/session/login", strings.NewReader(`{"admin_key":"admin-token"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	loginReq.Header.Set("X-Forwarded-Proto", "https")
	loginRec := httptest.NewRecorder()
	handler.ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("expected successful login, got %d body=%s", loginRec.Code, loginRec.Body.String())
	}
	if !strings.Contains(loginRec.Body.String(), `"expires_in":259200`) {
		t.Fatalf("expected session ttl in response, got %s", loginRec.Body.String())
	}

	cookies := loginRec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected exactly one cookie, got %d", len(cookies))
	}
	sessionCookie := cookies[0]
	if sessionCookie.Name != "holo_admin_session" {
		t.Fatalf("unexpected session cookie %+v", sessionCookie)
	}
	if !sessionCookie.HttpOnly {
		t.Fatalf("expected HttpOnly session cookie, got %+v", sessionCookie)
	}
	if !sessionCookie.Secure {
		t.Fatalf("expected Secure cookie when forwarded proto is https, got %+v", sessionCookie)
	}
	if sessionCookie.MaxAge != 259200 {
		t.Fatalf("expected 3-day ttl, got %+v", sessionCookie)
	}

	meReq := httptest.NewRequest(http.MethodGet, "/v1/admin/session/me", nil)
	meReq.AddCookie(sessionCookie)
	meRec := httptest.NewRecorder()
	handler.ServeHTTP(meRec, meReq)
	if meRec.Code != http.StatusOK {
		t.Fatalf("expected session me success, got %d body=%s", meRec.Code, meRec.Body.String())
	}
	if !strings.Contains(meRec.Body.String(), `"authenticated":true`) {
		t.Fatalf("expected authenticated session payload, got %s", meRec.Body.String())
	}

	adminPageReq := httptest.NewRequest(http.MethodGet, "/admin", nil)
	adminPageReq.AddCookie(sessionCookie)
	adminPageRec := httptest.NewRecorder()
	handler.ServeHTTP(adminPageRec, adminPageReq)
	if adminPageRec.Code != http.StatusOK {
		t.Fatalf("expected authenticated admin page, got %d body=%s", adminPageRec.Code, adminPageRec.Body.String())
	}
	if !strings.Contains(adminPageRec.Body.String(), "额度总览") {
		t.Fatalf("expected admin dashboard html, got %s", adminPageRec.Body.String())
	}
	if !strings.Contains(adminPageRec.Body.String(), "号池明细") {
		t.Fatalf("expected project-focused admin dashboard html, got %s", adminPageRec.Body.String())
	}

	poolReq := httptest.NewRequest(http.MethodGet, "/v1/admin/pool", nil)
	poolReq.AddCookie(sessionCookie)
	poolRec := httptest.NewRecorder()
	handler.ServeHTTP(poolRec, poolReq)
	if poolRec.Code != http.StatusOK {
		t.Fatalf("expected cookie-authenticated admin api access, got %d body=%s", poolRec.Code, poolRec.Body.String())
	}
	if !strings.Contains(poolRec.Body.String(), `"total_count":9`) {
		t.Fatalf("expected pool data after cookie auth, got %s", poolRec.Body.String())
	}

	logoutReq := httptest.NewRequest(http.MethodPost, "/v1/admin/session/logout", nil)
	logoutReq.AddCookie(sessionCookie)
	logoutRec := httptest.NewRecorder()
	handler.ServeHTTP(logoutRec, logoutReq)
	if logoutRec.Code != http.StatusOK {
		t.Fatalf("expected logout success, got %d body=%s", logoutRec.Code, logoutRec.Body.String())
	}
	logoutCookies := logoutRec.Result().Cookies()
	if len(logoutCookies) != 1 || logoutCookies[0].MaxAge >= 0 {
		t.Fatalf("expected cookie clearing response, got %+v", logoutCookies)
	}

	meAfterLogoutReq := httptest.NewRequest(http.MethodGet, "/v1/admin/session/me", nil)
	meAfterLogoutReq.AddCookie(sessionCookie)
	meAfterLogoutRec := httptest.NewRecorder()
	handler.ServeHTTP(meAfterLogoutRec, meAfterLogoutReq)
	if meAfterLogoutRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized after logout, got %d body=%s", meAfterLogoutRec.Code, meAfterLogoutRec.Body.String())
	}
}

func TestAdminMigrateFromOldImportsLegacySnapshot(t *testing.T) {
	t.Helper()

	legacy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer admin-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"accounts": []map[string]any{
				{"jwt": "jwt-old-1", "quota": 65},
				{"jwt": "jwt-old-2", "quota": 18},
				{"jwt": "", "quota": 18},
				{"jwt": "jwt-too-low", "quota": 1},
			},
		})
	}))
	defer legacy.Close()

	pool, _, handler := newTestHandlerWithLegacyBaseURL(legacy.URL)
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/migrate-from-old", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(pool.imported) != 2 {
		t.Fatalf("expected 2 imported legacy accounts, got %+v", pool.imported)
	}
	if pool.imported[0].JWT != "jwt-old-1" || pool.imported[1].JWT != "jwt-old-2" {
		t.Fatalf("unexpected imported accounts: %+v", pool.imported)
	}
	if !strings.Contains(rec.Body.String(), `"requested":4`) || !strings.Contains(rec.Body.String(), `"rejected":2`) {
		t.Fatalf("expected migration stats in response, got %s", rec.Body.String())
	}
}

func TestAdminRetireOldReturnsNotImplementedInsteadOfPretendingSuccess(t *testing.T) {
	t.Helper()

	_, _, handler := newTestHandler()
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/retire-old", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "retire-old is not automated yet") {
		t.Fatalf("expected explicit not implemented response, got %s", rec.Body.String())
	}
}
