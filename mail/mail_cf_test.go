package mail

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMailCFClientNewMailReturnsErrorOnNonSuccessStatus(t *testing.T) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/admin/new_address" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		http.Error(w, "blocked", http.StatusTooManyRequests)
	}))
	defer server.Close()

	client := NewMailCFClient(server.URL, []string{"wolfholo.me"}, "admin-token")

	address, err := client.NewMail()
	if err == nil {
		t.Fatalf("expected error when upstream mail API is not successful, got nil and address=%q", address)
	}
	if address != "" {
		t.Fatalf("expected empty address on failure, got %q", address)
	}
	if got, want := err.Error(), "所有邮箱域名创建失败：wolfholo.me -> HTTP 429"; !strings.HasPrefix(got, want) {
		t.Fatalf("expected HTTP status in error, got %v", err)
	}
}

func TestMailCFClientFetchAndExtractCodeReturnsErrorOnNonSuccessStatus(t *testing.T) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/admin/mails"; got != want {
			t.Fatalf("unexpected path: %s", got)
		}
		http.Error(w, "unavailable", http.StatusBadGateway)
	}))
	defer server.Close()

	client := NewMailCFClient(server.URL, []string{"wolfholo.me"}, "admin-token")

	next, code, err := client.FetchAndExtractCode("foo@wolfholo.me")
	if err == nil {
		t.Fatalf("expected fetch error, got nil")
	}
	if next || code != "" {
		t.Fatalf("expected no progress and no code on error, got next=%v code=%q err=%v", next, code, err)
	}
	if got, want := fmt.Sprint(err), "HTTP 502"; got == "" || got[:len(want)] != want {
		t.Fatalf("expected HTTP status in error, got %v", err)
	}
}

func TestMailCFClientNewMailFallsBackToNextDomain(t *testing.T) {
	t.Helper()

	var seenDomains []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/admin/new_address" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}

		var payload struct {
			Domain string `json:"domain"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("expected JSON body, got %v", err)
		}
		seenDomains = append(seenDomains, payload.Domain)

		if payload.Domain == "first.example" {
			http.Error(w, "blocked", http.StatusTooManyRequests)
			return
		}

		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	client := NewMailCFClient(server.URL, []string{"first.example", "second.example"}, "admin-token")

	address, err := client.NewMail()
	if err != nil {
		t.Fatalf("expected second domain fallback to succeed, got %v", err)
	}
	if len(seenDomains) != 2 {
		t.Fatalf("expected both domains to be attempted, got %v", seenDomains)
	}
	if seenDomains[0] != "first.example" || seenDomains[1] != "second.example" {
		t.Fatalf("expected fallback order first->second, got %v", seenDomains)
	}
	if got, want := address[len(address)-len("second.example"):], "second.example"; got != want {
		t.Fatalf("expected address to use second domain, got %q", address)
	}
}

func TestMailCFClientNewMailRotatesDomainStartIndex(t *testing.T) {
	t.Helper()

	var firstSeen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Domain string `json:"domain"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("expected JSON body, got %v", err)
		}
		firstSeen = append(firstSeen, payload.Domain)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	client := NewMailCFClient(server.URL, []string{"one.example", "two.example", "three.example"}, "admin-token")

	if _, err := client.NewMail(); err != nil {
		t.Fatalf("first NewMail failed: %v", err)
	}
	if _, err := client.NewMail(); err != nil {
		t.Fatalf("second NewMail failed: %v", err)
	}
	if len(firstSeen) < 2 {
		t.Fatalf("expected two create attempts, got %v", firstSeen)
	}
	if firstSeen[0] == firstSeen[1] {
		t.Fatalf("expected round-robin start domain to rotate, got %v", firstSeen)
	}
}
