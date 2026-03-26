package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoadConfigFailsWhenRequiredConfigMissing(t *testing.T) {
	t.Helper()

	_, err := LoadConfig([]string{}, func(key string) string {
		values := map[string]string{
			"PORT":              "18080",
			"MAIL_API_BASE_URL": "https://mail.example.com",
			"MAIL_DOMAIN":       "example.com",
		}
		return values[key]
	})
	if err == nil {
		t.Fatalf("expected missing config error, got nil")
	}
	if !strings.Contains(err.Error(), "MAIL_ADMIN_TOKEN") || !strings.Contains(err.Error(), "API_BEARER_TOKEN") {
		t.Fatalf("expected missing MAIL_ADMIN_TOKEN and API_BEARER_TOKEN error, got %v", err)
	}
}

func TestBearerAuthMiddlewareRejectsMissingAuthorizationHeader(t *testing.T) {
	t.Helper()

	protected := BearerAuthMiddleware("top-secret")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	recorder := httptest.NewRecorder()

	protected.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, recorder.Code)
	}
}

func TestBearerAuthMiddlewareAllowsValidAuthorizationHeader(t *testing.T) {
	t.Helper()

	called := false
	protected := BearerAuthMiddleware("top-secret")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	req.Header.Set("Authorization", "Bearer top-secret")
	recorder := httptest.NewRecorder()

	protected.ServeHTTP(recorder, req)

	if !called {
		t.Fatalf("expected wrapped handler to be called")
	}
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, recorder.Code)
	}
}

func TestNewServerHandlerExposesPublicHealthz(t *testing.T) {
	t.Helper()

	handler := NewServerHandler(Config{
		APIBearerToken: "top-secret",
	}, nil)

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
