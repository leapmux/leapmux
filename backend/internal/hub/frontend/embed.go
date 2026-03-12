package frontend

import (
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path/filepath"
	"strings"

	genfrontend "github.com/leapmux/leapmux/internal/hub/generated/frontend"
)

func init() {
	// Ensure .webmanifest files are served with the correct MIME type.
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
	// Ensure .map (source map) files are served with the correct MIME type.
	_ = mime.AddExtensionType(".map", "application/json")
}

// Handler returns an http.Handler that serves the embedded frontend assets.
// It supports pre-compressed .br and .gz variants and SPA fallback routing.
func Handler() http.Handler {
	publicFS, _ := fs.Sub(genfrontend.PublicFS, "public")
	return &spaHandler{fs: publicFS}
}

type spaHandler struct {
	fs fs.FS
}

func (h *spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}

	// Try to serve the exact file (with pre-compression support).
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

// serveFile tries to serve a file with pre-compressed variants (Brotli, gzip).
// Returns true if the file was served.
func (h *spaHandler) serveFile(w http.ResponseWriter, r *http.Request, path string) bool {
	accept := r.Header.Get("Accept-Encoding")

	// Try Brotli first.
	if strings.Contains(accept, "br") {
		if h.tryCompressed(w, r, path, "br") {
			return true
		}
	}

	// Try gzip.
	if strings.Contains(accept, "gzip") {
		if h.tryCompressed(w, r, path, "gzip") {
			return true
		}
	}

	// Serve uncompressed.
	f, err := h.fs.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()

	stat, err := f.Stat()
	if err != nil || stat.IsDir() {
		return false
	}

	setCacheHeaders(w, path)
	if ct := mime.TypeByExtension(filepath.Ext(path)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	http.ServeContent(w, r, path, stat.ModTime(), f.(io.ReadSeeker))
	return true
}

// tryCompressed serves a pre-compressed variant (.br or .gz) if available.
func (h *spaHandler) tryCompressed(w http.ResponseWriter, r *http.Request, originalPath, encoding string) bool {
	ext := ".br"
	if encoding == "gzip" {
		ext = ".gz"
	}

	compressedPath := originalPath + ext
	f, err := h.fs.Open(compressedPath)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()

	stat, err := f.Stat()
	if err != nil || stat.IsDir() {
		return false
	}

	setCacheHeaders(w, originalPath)
	if ct := mime.TypeByExtension(filepath.Ext(originalPath)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Content-Encoding", encoding)
	w.Header().Set("Vary", "Accept-Encoding")
	http.ServeContent(w, r, originalPath, stat.ModTime(), f.(io.ReadSeeker))
	return true
}

// setCacheHeaders sets appropriate caching headers based on the file path.
func setCacheHeaders(w http.ResponseWriter, path string) {
	// Hashed build assets and fonts are immutable.
	if strings.HasPrefix(path, "_build/") || strings.HasPrefix(path, "fonts/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		return
	}
	// Service worker and index.html must not be cached.
	if strings.HasPrefix(path, "leapmux-service-worker-") || path == "index.html" {
		w.Header().Set("Cache-Control", "no-cache")
		return
	}
	// Default: short cache for other assets.
	w.Header().Set("Cache-Control", "public, max-age=3600")
}
