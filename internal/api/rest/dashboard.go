package rest

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed dashboard/*
var dashboardAssets embed.FS

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/dashboard" {
		http.Redirect(w, r, "/dashboard/", http.StatusTemporaryRedirect)
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+strings.TrimPrefix(r.URL.Path, "/dashboard/")), "/")
	if name == "." || name == "" {
		name = "index.html"
	}
	assets, err := fs.Sub(dashboardAssets, "dashboard")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	clone := r.Clone(r.Context())
	if name == "index.html" {
		clone.URL.Path = "/"
	} else {
		clone.URL.Path = "/" + name
	}
	http.FileServer(http.FS(assets)).ServeHTTP(w, clone)
}
