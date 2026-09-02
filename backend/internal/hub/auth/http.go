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
	// ErrHTTPForbidden marks a caller the hub AUTHENTICATED and then refused:
	// the credential is live, and its grant does not reach this endpoint.
	//
	// A separate sentinel from ErrHTTPUnauthenticated because the two ask the
	// client for different things. 401 says "present a credential", and a
	// client that obeys it re-runs a whole sign-in ceremony that ends in the
	// same refusal. 403 says "this credential is not enough", which is the
	// only answer that is true and the only one an app can act on -- by asking
	// its account for a wider grant.
	ErrHTTPForbidden = errors.New("http authorization failed")
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
	// SoloGate decides which solo callers may skip credentials. Nil builds a
	// throwaway one over Store, so the RULE holds whether or not a caller
	// passes it; only the latch is lost, which costs a store read per request
	// and never an admission. A hub passes the interceptor's gate, so both
	// ladders share one latch.
	SoloGate *SoloGate
	// ReadCookie turns the session-cookie rung on. Handlers that accept
	// only a bearer or the solo user leave it false.
	ReadCookie bool
	// SecureCookies states whether this hub writes the __Host- prefixed
	// session cookie. It is the hub's OWN setting, never a guess from the
	// request.
	SecureCookies bool
}

// soloGate returns the shared gate, or a throwaway one over the store. The
// fallback keeps the RULE correct without the caller's help; see SoloGate.
func (o HTTPAuthOpts) soloGate() *SoloGate {
	if o.SoloGate != nil {
		return o.SoloGate
	}
	return NewSoloGate(o.Store)
}

// AuthenticateHTTP resolves the caller of `r` through the standard
// hub auth ladder: solo override → leapmux bearer → session cookie.
// Returns the resolved UserInfo or a descriptive error.
//
// Each rung is optional: nil SoloUser, nil Validator, or a false
// ReadCookie causes that rung to no-op. Handlers that only support a
// subset of the rungs pass the subset they want — e.g. the
// `/ws/userevents` and `/ws/channel` relays support all three;
// the `/oauth/*` endpoints support only cookies.
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
	// The solo rung admits the callers SoloGate allows -- the local IPC
	// socket, and any transport while the account holds no password -- and
	// YIELDS to a presented bearer.
	//
	// A caller that presents an lmx_ bearer is ASKING to be its app: it
	// accepted a narrower grant on a consent screen, and answering "you are
	// the solo user, you may do anything" would discard that narrowing
	// silently. The scope model would then be inert on solo -- an agent handed
	// file:read would still hold every permission the account has.
	//
	// It yields on the PRESENCE of a leapmux bearer, not on its validity: a
	// revoked or malformed one falls through to the validator below and is
	// refused there. Falling back to the solo rung instead would make a
	// credential stronger by being broken.
	if opts.SoloUser != nil && opts.soloGate().CredentialFree(r.Context()) && !presentsLeapMuxBearer(r) {
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
		token := SessionIDFromCookieHeader(r.Header.Get("Cookie"), opts.SecureCookies)
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

// presentsLeapMuxBearer reports whether the request carries a leapmux bearer at
// all, valid or not. It is what the solo rung yields to; see AuthenticateHTTP.
//
// The predicate itself lives in headerPresentsLeapMuxBearer, the string form
// both auth doors share, so the yield rule has one body.
func presentsLeapMuxBearer(r *http.Request) bool {
	return headerPresentsLeapMuxBearer(r.Header.Get("Authorization"))
}
