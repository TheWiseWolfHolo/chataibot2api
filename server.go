package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

func NewServerHandler(cfg Config, app *App) http.Handler {
	mux := http.NewServeMux()
	adminAssets, err := NewAdminUIHandler()
	if err != nil {
		panic(err)
	}

	mux.HandleFunc("/healthz", HealthzHandler)
	mux.HandleFunc("/admin", HandleAdminIndex)
	mux.HandleFunc("/admin/", HandleAdminIndex)
	mux.Handle("/admin/assets/", http.StripPrefix("/admin/assets/", adminAssets))
	mux.Handle("/v1/images/generations", BearerAuthMiddleware(cfg.APIBearerToken)(http.HandlerFunc(app.HandleImagesGenerations)))
	mux.Handle("/v1/models", BearerAuthMiddleware(cfg.APIBearerToken)(http.HandlerFunc(app.HandleModels)))
	mux.Handle("/v1/chat/completions", BearerAuthMiddleware(cfg.APIBearerToken)(http.HandlerFunc(app.HandleChatCompletions)))
	mux.Handle("/v1/admin/pool", BearerAuthMiddleware(cfg.AdminToken)(http.HandlerFunc(app.HandleAdminPoolStatus)))
	mux.Handle("/v1/admin/pool/fill", BearerAuthMiddleware(cfg.AdminToken)(http.HandlerFunc(app.HandleAdminPoolFill)))
	mux.Handle("/v1/admin/pool/import", BearerAuthMiddleware(cfg.AdminToken)(http.HandlerFunc(app.HandleAdminPoolImport)))
	mux.Handle("/v1/admin/pool/prune", BearerAuthMiddleware(cfg.AdminToken)(http.HandlerFunc(app.HandleAdminPoolPrune)))
	mux.Handle("/v1/admin/pool/export", BearerAuthMiddleware(cfg.AdminToken)(http.HandlerFunc(app.HandleAdminPoolExport)))
	mux.Handle("/v1/admin/meta", BearerAuthMiddleware(cfg.AdminToken)(http.HandlerFunc(app.HandleAdminMeta)))
	mux.Handle("/v1/admin/migration/status", BearerAuthMiddleware(cfg.AdminToken)(http.HandlerFunc(app.HandleAdminMigrationStatus)))
	mux.Handle("/v1/admin/migrate-from-old", BearerAuthMiddleware(cfg.AdminToken)(http.HandlerFunc(app.HandleAdminMigrateFromOld)))
	mux.Handle("/v1/admin/retire-old", BearerAuthMiddleware(cfg.AdminToken)(http.HandlerFunc(app.HandleAdminRetireOld)))
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

func (a *App) HandleAdminPoolImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		JWTs         []string `json:"jwts"`
		Validate     *bool    `json:"validate,omitempty"`
		MinimumQuota int      `json:"minimum_quota,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "Request body must be valid JSON", "invalid_request_error")
		return
	}
	if len(body.JWTs) == 0 {
		writeOpenAIError(w, http.StatusBadRequest, "jwts must contain at least one token", "invalid_request_error")
		return
	}

	validate := true
	if body.Validate != nil {
		validate = *body.Validate
	}

	minimumQuota := body.MinimumQuota
	if minimumQuota <= 0 {
		minimumQuota = 2
	}

	seen := make(map[string]struct{}, len(body.JWTs))
	accounts := make([]*Account, 0, len(body.JWTs))
	rejected := 0
	inputDuplicates := 0

	for _, raw := range body.JWTs {
		jwt := strings.TrimSpace(raw)
		if jwt == "" {
			rejected++
			continue
		}
		if _, ok := seen[jwt]; ok {
			inputDuplicates++
			continue
		}
		seen[jwt] = struct{}{}

		quota := 65
		if validate {
			quota = a.backend.GetCount(jwt)
			if quota < minimumQuota {
				rejected++
				continue
			}
		}

		accounts = append(accounts, &Account{
			JWT:   jwt,
			Quota: quota,
		})
	}

	result := a.pool.ImportAccounts(accounts)
	writeJSON(w, http.StatusOK, map[string]any{
		"requested":     len(body.JWTs),
		"validated":     len(accounts),
		"rejected":      rejected,
		"duplicates":    inputDuplicates + result.Duplicates,
		"imported":      result.Imported,
		"overflow":      result.Overflow,
		"total_count":   result.TotalCount,
		"validate":      validate,
		"minimum_quota": minimumQuota,
	})
}

func (a *App) HandleAdminPoolPrune(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	writeJSON(w, http.StatusOK, a.pool.Prune())
}

func (a *App) HandleAdminPoolExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"accounts": a.pool.ExportAccounts(),
	})
}

func (a *App) HandleAdminMeta(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	writeJSON(w, http.StatusOK, a.AdminMeta())
}

func (a *App) HandleAdminMigrationStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	writeJSON(w, http.StatusOK, a.CurrentMigrationStatus())
}

func (a *App) HandleAdminMigrateFromOld(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	status := a.MigrateFromLegacy()
	if status.LastError != "" {
		writeOpenAIError(w, http.StatusBadGateway, status.LastError, "migration_error")
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (a *App) HandleAdminRetireOld(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	writeOpenAIError(w, http.StatusNotImplemented, "retire-old is not automated yet; complete domain cutover verification first", "not_implemented")
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
