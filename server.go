package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

func NewServerHandler(cfg Config, app *App) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", HealthzHandler)
	mux.Handle("/v1/images/generations", BearerAuthMiddleware(cfg.APIBearerToken)(http.HandlerFunc(app.HandleImagesGenerations)))
	mux.Handle("/v1/models", BearerAuthMiddleware(cfg.APIBearerToken)(http.HandlerFunc(app.HandleModels)))
	mux.Handle("/v1/chat/completions", BearerAuthMiddleware(cfg.APIBearerToken)(http.HandlerFunc(app.HandleChatCompletions)))
	mux.Handle("/v1/admin/pool", BearerAuthMiddleware(cfg.AdminToken)(http.HandlerFunc(app.HandleAdminPoolStatus)))
	mux.Handle("/v1/admin/pool/fill", BearerAuthMiddleware(cfg.AdminToken)(http.HandlerFunc(app.HandleAdminPoolFill)))
	mux.Handle("/v1/admin/pool/prune", BearerAuthMiddleware(cfg.AdminToken)(http.HandlerFunc(app.HandleAdminPoolPrune)))
	return mux
}

func (a *App) HandleImagesGenerations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req OpenAIImageReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	resp, err := a.Generate(req)
	if err != nil {
		writeOpenAIError(w, statusCodeForError(err), err.Error(), errorTypeForError(err, "generation_error"))
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

func (a *App) HandleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	models := a.Models()
	data := make([]map[string]any, 0, len(models))
	for _, model := range models {
		data = append(data, map[string]any{
			"id":       model,
			"object":   "model",
			"created":  0,
			"owned_by": "holo-image-api",
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   data,
	})
}

func (a *App) HandleAdminPoolStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	writeJSON(w, http.StatusOK, a.pool.Status())
}

func (a *App) HandleAdminPoolFill(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		Count int `json:"count"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "Request body must be valid JSON", "invalid_request_error")
		return
	}
	if body.Count < 1 {
		writeOpenAIError(w, http.StatusBadRequest, "count must be >= 1", "invalid_request_error")
		return
	}

	task := a.pool.StartFillTask(body.Count)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"task_id":   task.ID,
		"requested": task.Requested,
		"status":    task.Status,
	})
}

func (a *App) HandleAdminPoolPrune(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	writeJSON(w, http.StatusOK, a.pool.Prune())
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
