package main

import (
	"net/http"
	"strings"
)

func NewServerHandler(cfg Config, pool *SimplePool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", HealthzHandler)
	mux.Handle("/v1/images/generations", BearerAuthMiddleware(cfg.APIBearerToken)(ImageHandler(pool)))
	return mux
}

func HealthzHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func BearerAuthMiddleware(token string) func(http.Handler) http.Handler {
	expectedHeader := "Bearer " + token

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.TrimSpace(r.Header.Get("Authorization")) != expectedHeader {
				w.Header().Set("WWW-Authenticate", "Bearer")
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
