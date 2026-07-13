package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"connectrpc.com/connect"

	"github.com/leapmux/leapmux/internal/hub/store"
)

// Worker-bearer auth error sentinels. Callers translate to their
// preferred wire status (connect.CodeUnauthenticated, HTTP 401, …).
var (
	ErrMissingBearer      = errors.New("missing bearer")
	ErrInvalidWorkerToken = errors.New("invalid worker auth token")
	ErrWorkerDeleted      = errors.New("worker deleted")
	// ErrHTTPUnauthenticated distinguishes rejected credentials from
	// infrastructure failures that HTTP handlers must surface as 500s.
	ErrHTTPUnauthenticated = errors.New("http authentication failed")
)

// AuthenticateWorkerBearer resolves an HTTP "Authorization: Bearer …"
// header value to a live Worker row. Returns one of the package's
// Err* sentinels on any auth failure so callers can map them to the
// wire status of their RPC framework.
func AuthenticateWorkerBearer(ctx context.Context, st store.Store, headerValue string) (*store.Worker, error) {
	bearer, ok := BearerToken(headerValue)
	if !ok {
		return nil, ErrMissingBearer
	}
	w, err := st.Workers().GetByAuthToken(ctx, bearer)
	if err != nil {
		return nil, ErrInvalidWorkerToken
	}
	if w.DeletedAt != nil {
		return nil, ErrWorkerDeleted
	}
	return w, nil
}

// HTTPAuthOpts collects everything AuthenticateHTTP needs.
//
// Fields are intentionally optional: handlers that don't support
// bearers (no `Validator`) or solo mode (no `SoloUser`) leave those
// nil and the helper skips that rung. `Cookies` controls the
// fallback set — most HTTP handlers pass a single secure-mode
// mirroring their own configuration, but the api-auth endpoint
// tries both modes so it works whether the session was issued under
// TLS or plain HTTP.
//
// Store is required only when at least one entry in Cookies yields
// a session id.
type HTTPAuthOpts struct {
	Store     store.Store
	Validator *TokenValidator
	SoloUser  *UserInfo
	Contexts  *AuthContextRegistry
	// Cookies lists the secure modes to try, in order. Empty means
	// "no cookie fallback" (handlers that only accept bearer/solo).
	Cookies []bool
}

// AuthenticateHTTP resolves the caller of `r` through the standard
// hub auth ladder: solo override → leapmux bearer → session cookie.
// Returns the resolved UserInfo or a descriptive error.
//
// Each rung is optional: nil SoloUser, nil Validator, or empty
// Cookies cause that rung to no-op. Handlers that only support a
// subset of the rungs pass the subset they want — e.g. the
// `/ws/orgevents` and `/ws/channel` relays support all three;
// the `/auth/cli/*` endpoints support only cookies (and try both
// secure modes so a TLS-issued session still validates when the
// browser falls back to plain HTTP and vice versa).
func AuthenticateHTTP(ctx context.Context, r *http.Request, opts HTTPAuthOpts) (*UserInfo, error) {
	if opts.SoloUser != nil {
		return opts.Contexts.CurrentSyntheticUser(opts.SoloUser), nil
	}
	if opts.Validator != nil {
		if bearer, ok := BearerToken(r.Header.Get("Authorization")); ok && IsLeapMuxBearer(bearer) {
			user, err := opts.Validator.ValidateBearer(ctx, bearer)
			if err != nil {
				if connect.CodeOf(err) == connect.CodeUnauthenticated {
					return nil, fmt.Errorf("%w: invalid bearer", ErrHTTPUnauthenticated)
				}
				return nil, fmt.Errorf("validate bearer: %w", err)
			}
			return user, nil
		}
	}
	for _, secure := range opts.Cookies {
		token := SessionIDFromRequest(r, secure)
		if token == "" {
			continue
		}
		user, err := ValidateToken(ctx, opts.Store, token)
		if err != nil {
			if connect.CodeOf(err) == connect.CodeUnauthenticated {
				return nil, fmt.Errorf("%w: invalid session", ErrHTTPUnauthenticated)
			}
			return nil, err
		}
		return user, nil
	}
	return nil, fmt.Errorf("%w: no credentials", ErrHTTPUnauthenticated)
}
