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

func HandleAdminDashboardPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path != "/admin" && r.URL.Path != "/admin/" {
		http.NotFound(w, r)
		return
	}

	serveAdminHTML(w, "web/admin/index.html")
}

func HandleAdminLoginPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path != "/admin/login" {
		http.NotFound(w, r)
		return
	}

	serveAdminHTML(w, "web/admin/login.html")
}

func serveAdminHTML(w http.ResponseWriter, assetPath string) {
	payload, err := adminAssets.ReadFile(assetPath)
	if err != nil {
		http.Error(w, "admin ui unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(payload)
}
