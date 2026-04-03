package main

import (
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
)

func init() {
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
	_ = mime.AddExtensionType(".map", "application/json")
}

// newSPAHandler creates an http.Handler that serves SPA assets from the
// given fs.FS with pre-compression support and SPA fallback routing.
func newSPAHandler(fsys fs.FS) http.Handler {
	return &spaHandler{fs: fsys}
}

type spaHandler struct {
	fs fs.FS
}

func (h *spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}

	if h.serveFile(w, r, path) {
		return
	}

	// If the path has a file extension, it's a missing static asset — 404.
	base := path[strings.LastIndex(path, "/")+1:]
	if strings.Contains(base, ".") {
		http.NotFound(w, r)
		return
	}

	// SPA fallback: serve index.html for route paths.
	h.serveFile(w, r, "index.html")
}

func (h *spaHandler) serveFile(w http.ResponseWriter, r *http.Request, path string) bool {
	accept := r.Header.Get("Accept-Encoding")

	if strings.Contains(accept, "br") {
		if h.tryCompressed(w, r, path, "br") {
			return true
		}
	}

	if strings.Contains(accept, "gzip") {
		if h.tryCompressed(w, r, path, "gzip") {
			return true
		}
	}

	f, err := h.fs.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()

	stat, err := f.Stat()
	if err != nil || stat.IsDir() {
		return false
	}

	setSPACacheHeaders(w, path)
	if ct := mime.TypeByExtension(filepath.Ext(path)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	http.ServeContent(w, r, path, stat.ModTime(), f.(io.ReadSeeker))
	return true
}

func (h *spaHandler) tryCompressed(w http.ResponseWriter, r *http.Request, originalPath, encoding string) bool {
	ext := ".br"
	if encoding == "gzip" {
		ext = ".gz"
	}

	f, err := h.fs.Open(originalPath + ext)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()

	stat, err := f.Stat()
	if err != nil || stat.IsDir() {
		return false
	}

	setSPACacheHeaders(w, originalPath)
	if ct := mime.TypeByExtension(filepath.Ext(originalPath)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Content-Encoding", encoding)
	w.Header().Set("Vary", "Accept-Encoding")
	http.ServeContent(w, r, originalPath, stat.ModTime(), f.(io.ReadSeeker))
	return true
}

func setSPACacheHeaders(w http.ResponseWriter, path string) {
	if strings.HasPrefix(path, "_build/") || strings.HasPrefix(path, "fonts/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		return
	}
	if strings.HasPrefix(path, "leapmux-service-worker-") || path == "index.html" {
		w.Header().Set("Cache-Control", "no-cache")
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
}
