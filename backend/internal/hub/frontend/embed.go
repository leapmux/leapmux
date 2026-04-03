package frontend

import (
	"io/fs"
	"net/http"

	genfrontend "github.com/leapmux/leapmux/internal/hub/generated/frontend"
	"github.com/leapmux/leapmux/spautil"
)

// Handler returns an http.Handler that serves the embedded frontend assets.
// It supports pre-compressed .br and .gz variants and SPA fallback routing.
func Handler() http.Handler {
	publicFS, _ := fs.Sub(genfrontend.PublicFS, "public")
	return spautil.NewHandler(publicFS)
}
