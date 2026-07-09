package service

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/leapmux/leapmux/internal/hub/auth"
)

func writeHTTPAuthError(w http.ResponseWriter, handler string, err error) {
	if errors.Is(err, auth.ErrHTTPUnauthenticated) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	slog.Error("HTTP authentication failed", "handler", handler, "error", err)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}
