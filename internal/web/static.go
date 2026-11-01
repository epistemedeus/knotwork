package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// The Vite build writes app.js, app.css, and lazy chunks here before the Go
// binary is compiled, so the UI ships as a single self-contained artifact.
//
//go:embed all:static
var staticFiles embed.FS

func (s *Server) registerStatic() {
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(sub))
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "chunks/") || strings.Contains(r.URL.Path, "-") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})))
}
