package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSendRegisterRequestPrimesSignupSessionAndVerifyReusesIt(t *testing.T) {
	t.Helper()

	const sessionCookieName = "connect.sid"
	const sessionCookieValue = "test-session"

	var signupHits atomic.Int32
	var registerHits atomic.Int32
	var verifyHits atomic.Int32

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/app/auth/sign-up":
			signupHits.Add(1)
			http.SetCookie(w, &http.Cookie{
				Name:  sessionCookieName,
				Value: sessionCookieValue,
				Path:  "/",
			})
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		case r.Method == http.MethodPost && r.URL.Path == "/api/register":
			registerHits.Add(1)
			cookie, err := r.Cookie(sessionCookieName)
			if err != nil || cookie.Value != sessionCookieValue {
				http.Error(w, `{"message":"Отказано вдоступе"}`, http.StatusForbidden)
				return
			}
			if got := r.Header.Get("x-distribution-channel"); got != "web" {
				t.Fatalf("expected x-distribution-channel=web, got %q", got)
			}
			if got := r.Header.Get("Referer"); got != server.URL+"/app/auth/sign-up?variant=new" {
				t.Fatalf("expected referer to signup page, got %q", got)
			}

			var body RegisterRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("expected JSON register body, got %v", err)
			}
			if body.Email != "user@example.com" {
				t.Fatalf("expected register email to pass through, got %+v", body)
			}
			if body.MainSiteUrl != server.URL+"/api" {
				t.Fatalf("expected mainSiteUrl to use test api base, got %+v", body)
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"success":true}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/register/verify":
			verifyHits.Add(1)
			cookie, err := r.Cookie(sessionCookieName)
			if err != nil || cookie.Value != sessionCookieValue {
				http.Error(w, `{"message":"Отказано вдоступе"}`, http.StatusForbidden)
				return
			}

			var body VerifyRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("expected JSON verify body, got %v", err)
			}
			if body.Email != "user@example.com" || body.Token != "123456" {
				t.Fatalf("unexpected verify body: %+v", body)
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"jwtToken":"jwt-from-verify"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewAPIClient()
	client.webBaseURL = server.URL
	client.apiBaseURL = server.URL + "/api"

	if err := client.SendRegisterRequest("user@example.com"); err != nil {
		t.Fatalf("expected register request to succeed after signup preflight, got %v", err)
	}

	jwt, err := client.VerifyAccount("user@example.com", "123456")
	if err != nil {
		t.Fatalf("expected verify to reuse signup session, got %v", err)
	}
	if jwt != "jwt-from-verify" {
		t.Fatalf("expected verify to reuse signup session and return jwt, got %q", jwt)
	}

	if signupHits.Load() != 1 {
		t.Fatalf("expected 1 signup preflight hit, got %d", signupHits.Load())
	}
	if registerHits.Load() != 1 {
		t.Fatalf("expected 1 register hit, got %d", registerHits.Load())
	}
	if verifyHits.Load() != 1 {
		t.Fatalf("expected 1 verify hit, got %d", verifyHits.Load())
	}
}

func TestSendRegisterRequestSurfacesSignupPrimeFailures(t *testing.T) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/app/auth/sign-up" {
			http.Error(w, "blocked", http.StatusForbidden)
			return
		}
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
	}))
	defer server.Close()

	client := NewAPIClient()
	client.webBaseURL = server.URL
	client.apiBaseURL = server.URL + "/api"

	if err := client.SendRegisterRequest("user@example.com"); err == nil {
		t.Fatalf("expected register request to fail when signup priming is blocked")
	}
}

func TestGenerateSecurePasswordProducesConfiguredLength(t *testing.T) {
	t.Helper()

	password := generateSecurePassword(16)
	if len(password) != 16 {
		t.Fatalf("expected password length 16, got %d (%q)", len(password), password)
	}
	if strings.TrimSpace(password) == "" {
		t.Fatalf("expected non-empty password, got %q", password)
	}
}
