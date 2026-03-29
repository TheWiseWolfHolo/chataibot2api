package mail

import (
	"fmt"
	"net/http"
	"net/http/httptest"
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

	client := NewMailCFClient(server.URL, "wolfholo.me", "admin-token")

	address, err := client.NewMail()
	if err == nil {
		t.Fatalf("expected error when upstream mail API is not successful, got nil and address=%q", address)
	}
	if address != "" {
		t.Fatalf("expected empty address on failure, got %q", address)
	}
	if got, want := err.Error(), "HTTP 429"; got == "" || got[:len(want)] != want {
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

	client := NewMailCFClient(server.URL, "wolfholo.me", "admin-token")

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
