package main

import (
	"embed"
	"net/http"
)

//go:embed dashboard
var dashboardAssets embed.FS

// dashboardSecurityPolicy bounds the operator console to same-origin script,
// style, and fetch; every other capability stays disabled.
const dashboardSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; " +
	"connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// serveDashboardAsset replies with one embedded asset under the console's
// strict headers.
func serveDashboardAsset(name, contentType string) http.HandlerFunc {
	data, err := dashboardAssets.ReadFile("dashboard/" + name)
	if err != nil {
		panic("missing embedded dashboard asset " + name)
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Security-Policy", dashboardSecurityPolicy)
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(data)
	}
}

// installDashboard serves the operator console from this origin only; the API
// routes keep their own handlers.
func installDashboard(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", serveDashboardAsset("index.html", "text/html; charset=utf-8"))
	mux.HandleFunc("GET /style.css", serveDashboardAsset("style.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET /app.js", serveDashboardAsset("app.js", "text/javascript; charset=utf-8"))
}
