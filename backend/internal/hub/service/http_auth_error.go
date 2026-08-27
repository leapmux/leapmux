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
	// A live credential whose grant does not reach this endpoint. See
	// auth.ErrHTTPForbidden: answering 401 here would send the app back
	// through a sign-in ceremony that ends in the same refusal.
	if errors.Is(err, auth.ErrHTTPForbidden) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	slog.Error("HTTP authentication failed", "handler", handler, "error", err)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}
