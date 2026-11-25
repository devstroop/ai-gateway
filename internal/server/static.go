package server

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// staticDir returns the directory to serve as the SPA bundle, or "" if none.
func staticDir() string {
	if v := os.Getenv("STATIC_DIR"); v != "" {
		return v
	}
	// Convenience default for local runs from the repo root.
	if info, err := os.Stat("web/dist"); err == nil && info.IsDir() {
		return "web/dist"
	}
	return ""
}

// spaHandler serves static files and falls back to index.html for client-side routes.
func spaHandler(dir string) http.Handler {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return http.NotFoundHandler()
	}
	realDir, err := filepath.EvalSymlinks(absDir)
	if err != nil {
		return http.NotFoundHandler()
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Never intercept API or health routes.
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/v1/") ||
			r.URL.Path == "/health" || r.URL.Path == "/ready" {
			http.NotFound(w, r)
			return
		}
		// Reject backslashes in URL paths.
		if strings.Contains(r.URL.Path, "\\") {
			http.NotFound(w, r)
			return
		}
		// Clean the URL path (forward slashes) and convert to an OS path.
		clean := path.Clean(r.URL.Path)
		clean = strings.TrimPrefix(clean, "/")
		osPath := filepath.FromSlash(clean)
		full := filepath.Join(absDir, osPath)

		// Confinement check using OS-aware separators.
		rel, err := filepath.Rel(absDir, full)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
			http.NotFound(w, r)
			return
		}

		// If the file exists, resolve symlinks and serve it.
		info, err := os.Stat(full)
		if err == nil && !info.IsDir() {
			realFull, err := filepath.EvalSymlinks(full)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			rel2, err2 := filepath.Rel(realDir, realFull)
			if err2 != nil || rel2 == ".." || strings.HasPrefix(rel2, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel2) {
				http.NotFound(w, r)
				return
			}
			http.ServeFile(w, r, realFull)
			return
		}

		// SPA fallback.
		http.ServeFile(w, r, filepath.Join(absDir, "index.html"))
	})
}
