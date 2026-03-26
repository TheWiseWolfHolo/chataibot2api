package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakePool struct {
	acquiredAccount *Account
	acquiredCost    int
	released        *Account
	status          PoolStatus
	fillTask        FillTaskSnapshot
	pruneResult     PruneSummary
	fillCounts      []int
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
	return f.pruneResult
}

type fakeImageBackend struct {
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
}

func (f *fakeImageBackend) UpdateUserSettings(_ string, aspectRatio string) bool {
	f.updateCalled = true
	f.lastRatio = aspectRatio
	return true
}

func (f *fakeImageBackend) GenerateImage(prompt, provider, version, _ string) (string, error) {
	f.lastPrompt = prompt
	f.lastModel = provider
	f.lastVersion = version
	if f.generateURL == "" {
		f.generateURL = "https://img.example.com/generated.png"
	}
	return f.generateURL, nil
}

func (f *fakeImageBackend) EditImage(prompt, imageData, mode, _ string) (string, error) {
	f.lastPrompt = prompt
	f.lastImage = imageData
	f.lastEditMode = mode
	if f.editURL == "" {
		f.editURL = "https://img.example.com/edited.png"
	}
	return f.editURL, nil
}

func (f *fakeImageBackend) MergeImage(prompt string, images []string, mode, _ string) (string, error) {
	f.lastPrompt = prompt
	f.lastImages = append([]string(nil), images...)
	f.lastMerge = mode
	if f.mergeURL == "" {
		f.mergeURL = "https://img.example.com/merged.png"
	}
	return f.mergeURL, nil
}

func (f *fakeImageBackend) GetCount(_ string) int {
	return 65
}

func newTestHandler() (*fakePool, *fakeImageBackend, http.Handler) {
	pool := &fakePool{}
	backend := &fakeImageBackend{}
	app := NewApp(pool, backend, func() time.Time {
		return time.Unix(1_700_000_000, 0)
	})
	handler := NewServerHandler(Config{
		APIBearerToken: "api-token",
		AdminToken:     "admin-token",
	}, app)
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
	found := false
	for _, item := range resp.Data {
		if item.ID == "gpt-image-1.5-high" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected gpt-image-1.5-high in model list, got %+v", resp.Data)
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
		"model":"google-nano-banana-2",
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
	if backend.lastMerge != "merge_google_nano_banana_2" || len(backend.lastImages) != 2 {
		t.Fatalf("unexpected merge call mode=%q images=%v", backend.lastMerge, backend.lastImages)
	}
}

func TestChatCompletionsRejectsUnsupportedTextChat(t *testing.T) {
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
	if !strings.Contains(recorder.Body.String(), "text chat") {
		t.Fatalf("expected unsupported text chat error, got %s", recorder.Body.String())
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
