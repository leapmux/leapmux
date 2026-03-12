package frontend

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
)

// DevProxy returns an http.Handler that reverse-proxies requests to the
// given Vite dev server URL. It supports WebSocket upgrades for Vite HMR.
func DevProxy(target string) (http.Handler, error) {
	u, err := url.Parse(target)
	if err != nil {
		return nil, fmt.Errorf("parse dev frontend URL: %w", err)
	}

	proxy := httputil.NewSingleHostReverseProxy(u)

	// Preserve the original Director and ensure WebSocket upgrades
	// are forwarded correctly to the Vite dev server.
	originalDirector := proxy.Director
	proxy.Director = func(r *http.Request) {
		originalDirector(r)
		r.Host = u.Host
	}

	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		if r.Context().Err() != nil {
			// Client disconnected; nothing to do.
			return
		}
		slog.Error("dev proxy error", "err", err, "path", r.URL.Path)
		w.WriteHeader(http.StatusBadGateway)
	}

	return proxy, nil
}
