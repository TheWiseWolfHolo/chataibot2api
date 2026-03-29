package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	adminSessionCookieName = "holo_admin_session"
	adminSessionTTL        = 72 * time.Hour
)

type AdminSession struct {
	ID        string
	ExpiresAt time.Time
}

type AdminSessionManager struct {
	mu       sync.Mutex
	now      func() time.Time
	ttl      time.Duration
	sessions map[string]AdminSession
}

func NewAdminSessionManager(ttl time.Duration, now func() time.Time) *AdminSessionManager {
	if ttl <= 0 {
		ttl = adminSessionTTL
	}
	if now == nil {
		now = time.Now
	}

	return &AdminSessionManager{
		now:      now,
		ttl:      ttl,
		sessions: make(map[string]AdminSession),
	}
}

func (m *AdminSessionManager) TTLSeconds() int {
	return int(m.ttl / time.Second)
}

func (m *AdminSessionManager) Create() (AdminSession, error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return AdminSession{}, err
	}

	session := AdminSession{
		ID:        hex.EncodeToString(tokenBytes),
		ExpiresAt: m.now().Add(m.ttl).UTC(),
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupExpiredLocked()
	m.sessions[session.ID] = session
	return session, nil
}

func (m *AdminSessionManager) Get(sessionID string) (AdminSession, bool) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return AdminSession{}, false
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	session, ok := m.sessions[sessionID]
	if !ok {
		return AdminSession{}, false
	}
	if !session.ExpiresAt.After(m.now()) {
		delete(m.sessions, sessionID)
		return AdminSession{}, false
	}
	return session, true
}

func (m *AdminSessionManager) Delete(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, sessionID)
}

func (m *AdminSessionManager) cleanupExpiredLocked() {
	now := m.now()
	for sessionID, session := range m.sessions {
		if !session.ExpiresAt.After(now) {
			delete(m.sessions, sessionID)
		}
	}
}

type AdminAuthenticator struct {
	adminToken     string
	expectedHeader string
	sessions       *AdminSessionManager
}

func NewAdminAuthenticator(adminToken string, sessions *AdminSessionManager) *AdminAuthenticator {
	return &AdminAuthenticator{
		adminToken:     strings.TrimSpace(adminToken),
		expectedHeader: "Bearer " + strings.TrimSpace(adminToken),
		sessions:       sessions,
	}
}

func (a *AdminAuthenticator) isBearerAuthorized(r *http.Request) bool {
	return strings.TrimSpace(r.Header.Get("Authorization")) == a.expectedHeader
}

func (a *AdminAuthenticator) sessionFromRequest(r *http.Request) (AdminSession, bool) {
	if a.sessions == nil {
		return AdminSession{}, false
	}

	cookie, err := r.Cookie(adminSessionCookieName)
	if err != nil {
		return AdminSession{}, false
	}
	return a.sessions.Get(cookie.Value)
}

func (a *AdminAuthenticator) IsAuthorized(r *http.Request) bool {
	if a.isBearerAuthorized(r) {
		return true
	}
	_, ok := a.sessionFromRequest(r)
	return ok
}

func (a *AdminAuthenticator) RequireAPI(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.IsAuthorized(r) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *AdminAuthenticator) RequireDashboard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.IsAuthorized(r) {
			http.Redirect(w, r, "/admin/login", http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *AdminAuthenticator) HandleSessionLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		AdminKey string `json:"admin_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "Request body must be valid JSON", "invalid_request_error")
		return
	}
	if strings.TrimSpace(body.AdminKey) != a.adminToken {
		writeOpenAIError(w, http.StatusUnauthorized, "invalid admin key", "authentication_error")
		return
	}

	session, err := a.sessions.Create()
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to create admin session", "session_error")
		return
	}

	http.SetCookie(w, buildAdminSessionCookie(r, session, a.sessions.ttl))
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"expires_in":    a.sessions.TTLSeconds(),
		"expires_at":    session.ExpiresAt.UTC(),
	})
}

func (a *AdminAuthenticator) HandleSessionMe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	session, ok := a.sessionFromRequest(r)
	if !ok {
		writeOpenAIError(w, http.StatusUnauthorized, "admin session is missing or expired", "authentication_error")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"expires_in":    maxInt(0, int(session.ExpiresAt.Sub(a.sessions.now()).Seconds())),
		"expires_at":    session.ExpiresAt.UTC(),
	})
}

func (a *AdminAuthenticator) HandleSessionLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if cookie, err := r.Cookie(adminSessionCookieName); err == nil {
		a.sessions.Delete(cookie.Value)
	}
	http.SetCookie(w, clearAdminSessionCookie(r))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func buildAdminSessionCookie(r *http.Request, session AdminSession, ttl time.Duration) *http.Cookie {
	return &http.Cookie{
		Name:     adminSessionCookieName,
		Value:    session.ID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   requestIsHTTPS(r),
		MaxAge:   int(ttl / time.Second),
		Expires:  session.ExpiresAt.UTC(),
	}
}

func clearAdminSessionCookie(r *http.Request) *http.Cookie {
	return &http.Cookie{
		Name:     adminSessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   requestIsHTTPS(r),
		MaxAge:   -1,
		Expires:  time.Unix(0, 0).UTC(),
	}
}

func requestIsHTTPS(r *http.Request) bool {
	if r == nil {
		return false
	}
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}

func maxInt(a int, b int) int {
	if a > b {
		return a
	}
	return b
}
