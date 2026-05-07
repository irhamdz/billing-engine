package httpapi

// Interactive API documentation served from /docs.
//
// The OpenAPI 3.0 spec is hand-written at swagger/openapi.yaml; the Swagger UI
// assets under swagger/ui/ are vendored from swagger-ui-dist@5.17.14
// (https://unpkg.com/swagger-ui-dist@5.17.14/). Re-vendor by re-downloading
// those files when bumping the version. Everything is embedded into the binary
// so /docs works offline.

import (
	"embed"
	"net/http"
	"path"

	"github.com/go-chi/chi/v5"
)

//go:embed swagger/index.html swagger/openapi.yaml swagger/ui/*
var swaggerFS embed.FS

// serveDocsIndex serves the Swagger UI HTML at GET /docs.
func serveDocsIndex(w http.ResponseWriter, r *http.Request) {
	body, err := swaggerFS.ReadFile("swagger/index.html")
	if err != nil {
		http.Error(w, "docs not available", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// serveOpenAPISpec serves the raw spec at GET /docs/openapi.yaml. Go's mime
// package does not register .yaml by default, so set Content-Type explicitly.
func serveOpenAPISpec(w http.ResponseWriter, r *http.Request) {
	body, err := swaggerFS.ReadFile("swagger/openapi.yaml")
	if err != nil {
		http.Error(w, "spec not available", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// serveDocsAsset serves a vendored Swagger UI asset (CSS, JS, favicon) at
// GET /docs/{asset}. Limited to the flat file set under swagger/ui/.
func serveDocsAsset(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "asset")
	// Reject path traversal. chi already strips slashes from a single-segment
	// param, but be defensive.
	if name == "" || name != path.Base(name) {
		http.NotFound(w, r)
		return
	}
	body, err := swaggerFS.ReadFile("swagger/ui/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentTypeFor(name))
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func contentTypeFor(name string) string {
	switch path.Ext(name) {
	case ".css":
		return "text/css; charset=utf-8"
	case ".js":
		return "application/javascript; charset=utf-8"
	case ".png":
		return "image/png"
	case ".html":
		return "text/html; charset=utf-8"
	case ".yaml", ".yml":
		return "application/yaml"
	default:
		return "application/octet-stream"
	}
}
