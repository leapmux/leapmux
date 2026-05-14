// Package service: API auth handler exposes the leapmux remote CLI auth
// flows (local-redirect with PKCE, RFC 8628 device-code) and the bearer
// refresh / revoke endpoints. Endpoints live at /auth/cli/*.
package service

import (
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/verifycode"
)

const (
	// CLIAuthCodeTTL is how long a one-shot local-redirect code lives.
	CLIAuthCodeTTL = 10 * time.Minute
	// DeviceCodeTTL is how long a device-code grant lives before expiring.
	DeviceCodeTTL = 10 * time.Minute
	// DeviceCodePollInterval is the recommended polling cadence the CLI
	// honours; the hub returns slow_down to throttle pollers exceeding it.
	DeviceCodePollInterval = 5 * time.Second
)

// APIAuthHandler implements /auth/cli/*. It depends only on the store +
// the TokenValidator (for HMAC-pepper hashing).
type APIAuthHandler struct {
	store      store.Store
	validator  *auth.TokenValidator
	cache      *auth.SessionCache      // for EvictBearer on revoke
	graceCache *auth.RefreshGraceCache // re-emit access pair on refresh retry
	hubURL     string
}

// NewAPIAuthHandler wires the handler. hubURL is used to build the
// device-code verification URLs returned to the CLI. graceCache may be
// nil — a missing cache is non-fatal (legitimate retries within the
// grace window will fall through to invalid_grant) but degrades the
// recovery story for torn rotations.
func NewAPIAuthHandler(st store.Store, v *auth.TokenValidator, cache *auth.SessionCache, graceCache *auth.RefreshGraceCache, hubURL string) *APIAuthHandler {
	return &APIAuthHandler{store: st, validator: v, cache: cache, graceCache: graceCache, hubURL: hubURL}
}

// RegisterRoutes mounts the handler's routes on the mux.
func (h *APIAuthHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/auth/cli/start", h.handleStart)
	mux.HandleFunc("/auth/cli/authorize", h.handleAuthorize)
	mux.HandleFunc("/auth/cli/device-authorization", h.handleDeviceAuthorization)
	mux.HandleFunc("/auth/cli/activate", h.handleActivate)
	mux.HandleFunc("/auth/cli/token", h.handleToken)
	mux.HandleFunc("/auth/cli/refresh", h.handleRefresh)
	mux.HandleFunc("/auth/cli/revoke", h.handleRevoke)
}

// --- Helpers ---

func (h *APIAuthHandler) requireSession(r *http.Request) *auth.UserInfo {
	// /api/auth/* endpoints only accept session cookies; bearer/solo
	// rungs are unwired by leaving Validator/SoloUser nil. Both
	// cookie modes are tried so a session issued under TLS still
	// works when the browser falls back to plain HTTP and vice versa.
	user, err := auth.AuthenticateHTTP(r.Context(), r, auth.HTTPAuthOpts{
		Store:   h.store,
		Cookies: []bool{false, true},
	})
	if err != nil {
		return nil
	}
	return user
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeOAuthError(w http.ResponseWriter, status int, code, description string) {
	writeJSON(w, status, map[string]string{
		"error":             code,
		"error_description": description,
	})
}

func generateUserCode() string {
	// Reuse verifycode.Generate which produces a 6-char alphanumeric
	// from an unambiguous alphabet — exactly the user-code shape we
	// want. The display form (XXX-XXX) is added by verifycode.Format
	// when we build verification_uri_complete.
	return verifycode.Generate()
}

func isLoopbackURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := u.Hostname()
	switch host {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}
