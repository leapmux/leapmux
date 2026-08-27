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
// nil and the helper skips that rung. `ReadCookie` turns the cookie
// rung on, and `SecureCookies` states which spelling this hub writes.
//
// Store is required only when ReadCookie is set.
type HTTPAuthOpts struct {
	Store     store.Store
	Validator *TokenValidator
	SoloUser  *UserInfo
	Contexts  *AuthContextRegistry
	// ReadCookie turns the session-cookie rung on. Handlers that accept
	// only a bearer or the solo user leave it false.
	ReadCookie bool
	// SecureCookies states whether this hub writes the __Host- prefixed
	// session cookie. It is the hub's OWN setting, never a guess from the
	// request.
	SecureCookies bool
}

// AuthenticateHTTP resolves the caller of `r` through the standard
// hub auth ladder: solo override → leapmux bearer → session cookie.
// Returns the resolved UserInfo or a descriptive error.
//
// Each rung is optional: nil SoloUser, nil Validator, or a false
// ReadCookie causes that rung to no-op. Handlers that only support a
// subset of the rungs pass the subset they want — e.g. the
// `/ws/userevents` and `/ws/channel` relays support all three;
// the `/auth/cli/*` endpoints support only cookies.
//
// The cookie rung is ASYMMETRIC, exactly as OAuthNonceFromRequest is,
// and for the same reason. AuthenticateHTTP reads the __Host- spelling
// first, and on a hub that writes it, it REFUSES the unprefixed name rather
// than trying it as a fallback: any plain-HTTP page on the registrable domain can plant
// `leapmux-session`, which is precisely what the __Host- prefix exists to
// prevent. Trying the unprefixed name FIRST — which the two most
// consequential HTTP surfaces did, the CLI consent legs and the leg that
// grants an elevation — handed a planted cookie priority over the real
// one. On a hub that does not write the prefixed spelling the fallback is
// safe and stays, so a session issued under TLS still validates after the
// operator turns the setting off.
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
	if opts.ReadCookie {
		token := SessionIDFromRequest(r, true)
		if token == "" && !opts.SecureCookies {
			token = SessionIDFromRequest(r, false)
		}
		if token != "" {
			user, err := ValidateToken(ctx, opts.Store, token)
			if err != nil {
				if connect.CodeOf(err) == connect.CodeUnauthenticated {
					return nil, fmt.Errorf("%w: invalid session", ErrHTTPUnauthenticated)
				}
				return nil, err
			}
			return user, nil
		}
	}
	return nil, fmt.Errorf("%w: no credentials", ErrHTTPUnauthenticated)
}
