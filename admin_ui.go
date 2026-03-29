package main

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed web/admin/*
var adminAssets embed.FS

func NewAdminUIHandler() (http.Handler, error) {
	sub, err := fs.Sub(adminAssets, "web/admin")
	if err != nil {
		return nil, err
	}
	return http.FileServer(http.FS(sub)), nil
}

func HandleAdminIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path != "/admin" && r.URL.Path != "/admin/" {
		http.NotFound(w, r)
		return
	}

	payload, err := adminAssets.ReadFile("web/admin/index.html")
	if err != nil {
		http.Error(w, "admin ui unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(payload)
}
