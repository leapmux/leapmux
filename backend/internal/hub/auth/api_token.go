package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// TokenPrefix is the canonical leading marker for LeapMux bearer tokens
// (CLI api_tokens and worker-minted delegation_tokens). It exists for
// log/grep ergonomics — the verifier doesn't trust the prefix and re-
// derives the (kind, id, secret) from the body of the bearer.
const TokenPrefix = "lmx_"

// BearerKind is the type tag embedded after the prefix so the
// validator knows which table to query — one PK lookup, not two.
// The kind character is part of the bearer string but NOT part of
// the stored row id, so existing rows remain valid without
// migration: the validator strips the kind before passing the id
// to the store.
type BearerKind byte

const (
	// BearerKindAPI marks bearers backed by api_tokens (durable CLI
	// / integration tokens). Wire form: "lmx_a<id>_<secret>".
	BearerKindAPI BearerKind = 'a'
	// BearerKindDelegation marks worker-minted delegation_tokens
	// (one-per-spawn ephemeral bearers). Wire form
	// "lmx_d<id>_<secret>".
	BearerKindDelegation BearerKind = 'd'
)

// AccessTokenTTL is the lifetime of a freshly minted CLI access token.
const AccessTokenTTL = 1 * time.Hour

// RefreshTokenTTL is how far a refresh moves the refresh window forward.
const RefreshTokenTTL = 90 * 24 * time.Hour

// AbsoluteTokenLifetime caps a CLI credential's whole life, measured from
// created_at rather than from the last rotation.
//
// Without it a refresh window that slides by RefreshTokenTTL on EVERY
// rotation is unlimited: a CLI that refreshes weekly keeps one browser
// consent alive for ever, and the consent is what the user actually gave.
// The cap turns "90 days since you last used it" into "one year since you
// authorized it", after which the device signs in again.
//
// Deliberately far longer than the refresh window, so it applies only to a
// credential in continuous use for a year -- an idle one expires
// on RefreshTokenTTL first.
const AbsoluteTokenLifetime = 365 * 24 * time.Hour

// RefreshWindowFor returns the refresh expiry to write for a token created at
// createdAt, refreshing at now: the ordinary window, clipped to the absolute
// lifetime. A non-positive result means the credential reached its ceiling and
// must not refresh again.
//
// One function, so the mint and the rotation cannot disagree about when a
// credential dies. A zero createdAt (a caller that did not load the row)
// yields the ordinary window rather than an instantly-expired token: failing
// closed here would revoke every live credential on a mapping slip, and the
// mint path legitimately has no row yet.
func RefreshWindowFor(createdAt, now time.Time) time.Duration {
	window := RefreshTokenTTL
	if createdAt.IsZero() {
		return window
	}
	if remaining := createdAt.Add(AbsoluteTokenLifetime).Sub(now); remaining < window {
		return remaining
	}
	return window
}

// AccessWindowFor returns the access-token lifetime to mint for a token
// created at createdAt, at now: the ordinary hour, clipped to the same
// absolute lifetime the refresh window respects.
//
// The clip is not decoration. validateRow reads expires_at ALONE, so the
// access token is the only thing that decides whether a bearer still
// authenticates -- and the last rotation before the ceiling wrote a full
// AccessTokenTTL past it, so the credential kept working for up to an hour
// after the hub answered its next refresh with "this credential reached its
// maximum lifetime". Clipping here makes the ceiling a property of the
// credential rather than of the refresh leg that happens to notice it.
func AccessWindowFor(createdAt, now time.Time) time.Duration {
	return min(AccessTokenTTL, RefreshWindowFor(createdAt, now))
}

// DelegationTokenTTL is the lifetime of a worker-minted delegation
// token. Short by design: agents that outlive the TTL refresh.
const DelegationTokenTTL = 1 * time.Hour

// RefreshReuseGrace is how long a previously-rotated refresh token is
// honoured as a benign retry after rotation. Reuse outside this window
// triggers compromise revocation.
const RefreshReuseGrace = 60 * time.Second

// The validator returns ErrInvalidToken for malformed bearer strings, and
// ErrTokenExpired for syntactically valid but expired or revoked tokens.
var (
	ErrInvalidToken  = errors.New("invalid token")
	ErrTokenExpired  = errors.New("token expired")
	ErrTokenRevoked  = errors.New("token revoked")
	ErrRefreshReused = errors.New("refresh token reused")
)

// TokenValidator verifies api_token / delegation_token bearers against
// the hub store, applying caching + HMAC-pepper hashing. The same
// validator serves the request interceptor and the WebSocket relay
// upgrade path.
type TokenValidator struct {
	store  store.Store
	pepper []byte
	// Now is the clock seam. Every deadline this validator compares reads it,
	// so a test that expires a credential moves one field instead of waiting.
	//
	// It carried none, and it is the one deadline no other seam could move: a
	// bearer's own expiry, its absolute ceiling, and the refresh window all
	// read the wall clock here, while the services around it read their own
	// seam. Nil means time.Now, so production wires nothing -- the same
	// default the service-side clockSeam takes.
	Now func() time.Time
}

// now returns the validator's notion of the current instant.
func (v *TokenValidator) now() time.Time {
	if v.Now != nil {
		return v.Now()
	}
	return time.Now()
}

// NewTokenValidator returns a validator. Pepper must be at least 32 bytes;
// callers usually source it from the hub's encryption key material.
func NewTokenValidator(st store.Store, pepper []byte) (*TokenValidator, error) {
	if len(pepper) < 16 {
		return nil, fmt.Errorf("server pepper must be at least 16 bytes")
	}
	return &TokenValidator{
		store:  st,
		pepper: pepper,
	}, nil
}

// HashSecret returns the canonical secret_hash for a given raw secret
// (HMAC-SHA256 keyed by the server pepper). Exported so issuance code
// (handlers, admin CLI) can compute hashes the same way the validator
// does.
func (v *TokenValidator) HashSecret(secret string) []byte {
	mac := hmac.New(sha256.New, v.pepper)
	mac.Write([]byte(secret))
	return mac.Sum(nil)
}

// FormatBearer returns "lmx_<kind><id>_<secret>". The kind char is
// transport-only — the stored row id is just <id>.
func FormatBearer(kind BearerKind, tokenID, secret string) string {
	return TokenPrefix + string(byte(kind)) + tokenID + "_" + secret
}

// IsLeapMuxBearer is a cheap shape check: returns true if header value
// starts with "lmx_". Intended as a router-level discriminator before
// running the full validator.
func IsLeapMuxBearer(token string) bool {
	return strings.HasPrefix(token, TokenPrefix)
}

// ParseBearer splits "lmx_<kind><id>_<secret>" into its components.
// Returns ErrInvalidToken when the kind is unknown — that lets the
// validator reject malformed bearers without doing a DB lookup at
// all.
func ParseBearer(bearer string) (kind BearerKind, tokenID, secret string, err error) {
	if !strings.HasPrefix(bearer, TokenPrefix) {
		return 0, "", "", ErrInvalidToken
	}
	rest := bearer[len(TokenPrefix):]
	if rest == "" {
		return 0, "", "", ErrInvalidToken
	}
	k := BearerKind(rest[0])
	if !k.IsValid() {
		return 0, "", "", ErrInvalidToken
	}
	rest = rest[1:]
	idx := strings.Index(rest, "_")
	if idx <= 0 || idx >= len(rest)-1 {
		return 0, "", "", ErrInvalidToken
	}
	return k, rest[:idx], rest[idx+1:], nil
}

// IsValid reports whether kind is one of the registered bearer kinds.
// The validator rejects a bearer with an unrecognised kind char
// outright — it never queries the DB for tokens it doesn't know
// how to look up.
func (k BearerKind) IsValid() bool {
	switch k {
	case BearerKindAPI, BearerKindDelegation:
		return true
	default:
		return false
	}
}

// MintAccessSecret returns a fresh secret suitable for api_token /
// delegation_token issuance. The exposed bearer the user sees is
// FormatBearer(tokenID, secret).
func MintAccessSecret() string { return id.Generate() }

// MintedBearerPair carries the (access, refresh) outputs of one mint
// call. Hashes are what the row stores; bearers are what the client
// receives.
type MintedBearerPair struct {
	AccessBearer     string
	RefreshBearer    string
	AccessHash       []byte
	RefreshHash      []byte
	AccessExpiresAt  time.Time
	RefreshExpiresAt time.Time
}

// MintBearerPair generates a fresh (access, refresh) pair bound to
// tokenID for the given BearerKind. Centralises bearer formatting +
// hash + TTL derivation so api_token and delegation_token issuers
// can't drift on shape.
func (v *TokenValidator) MintBearerPair(kind BearerKind, tokenID string, now time.Time, accessTTL, refreshTTL time.Duration) MintedBearerPair {
	return v.newBearerPair(kind, tokenID, MintAccessSecret(), MintAccessSecret(), now, accessTTL, refreshTTL)
}

// newBearerPair assembles a MintedBearerPair from already-chosen access and
// refresh secrets. Both the fresh-mint (random secrets) and the deterministic
// refresh-derivation (pepper-derived secrets) paths funnel through here so the
// bearer wire format, secret hashing, and TTL derivation live in one place and
// cannot drift between them.
func (v *TokenValidator) newBearerPair(kind BearerKind, tokenID, access, refresh string, now time.Time, accessTTL, refreshTTL time.Duration) MintedBearerPair {
	return MintedBearerPair{
		AccessBearer:     FormatBearer(kind, tokenID, access),
		RefreshBearer:    FormatBearer(kind, tokenID, refresh),
		AccessHash:       v.HashSecret(access),
		RefreshHash:      v.HashSecret(refresh),
		AccessExpiresAt:  now.Add(accessTTL),
		RefreshExpiresAt: now.Add(refreshTTL),
	}
}

// DeriveRefreshBearerPair deterministically derives the next bearer pair from
// the submitted refresh hash. Every Hub with the same pepper derives the same
// pair, so a retry of a successfully rotated refresh can recover after process
// failure or when load balancing sends it to another Hub.
func (v *TokenValidator) DeriveRefreshBearerPair(
	kind BearerKind,
	tokenID string,
	refreshHash []byte,
	now time.Time,
	accessTTL, refreshTTL time.Duration,
) MintedBearerPair {
	access := v.deriveRefreshSecret("access", kind, tokenID, refreshHash)
	refresh := v.deriveRefreshSecret("refresh", kind, tokenID, refreshHash)
	return v.newBearerPair(kind, tokenID, access, refresh, now, accessTTL, refreshTTL)
}

func (v *TokenValidator) deriveRefreshSecret(purpose string, kind BearerKind, tokenID string, refreshHash []byte) string {
	mac := hmac.New(sha256.New, v.pepper)
	mac.Write([]byte("leapmux-refresh-pair-v1"))
	mac.Write([]byte{0, byte(kind), 0})
	mac.Write([]byte(tokenID))
	mac.Write([]byte{0})
	mac.Write([]byte(purpose))
	mac.Write([]byte{0})
	mac.Write(refreshHash)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// VerifyBearerSecret confirms a bearer's secret matches a stored access,
// current-refresh, or previous-refresh hash, *without* rejecting already-
// revoked or already-expired rows.
// It is the primitive RFC 7009-style revocation needs: the revocation
// endpoint must reject callers who don't hold the secret (so a leaked
// token_id alone can't tear down a victim's session), but should still
// succeed idempotently when revoking a token that's already revoked /
// expired.
//
// On success returns the kind tag and the stored row id (with the kind
// char stripped, matching ValidateBearer). On parse failure, missing
// row, or secret mismatch returns ErrInvalidToken — never leaking
// which check failed, so the response can't be used to enumerate
// existing token_ids.
func (v *TokenValidator) VerifyBearerSecret(ctx context.Context, bearer string) (BearerKind, string, error) {
	kind, tokenID, secret, err := ParseBearer(bearer)
	if err != nil {
		return 0, "", ErrInvalidToken
	}
	id, hashes, lerr := v.lookupRowSecretHashes(ctx, kind, tokenID)
	if lerr != nil {
		if errors.Is(lerr, store.ErrNotFound) {
			return 0, "", ErrInvalidToken
		}
		return 0, "", lerr
	}
	presentedHash := v.HashSecret(secret)
	matched := false
	for _, hash := range hashes {
		if len(hash) > 0 && hmac.Equal(presentedHash, hash) {
			matched = true
		}
	}
	if !matched {
		return 0, "", ErrInvalidToken
	}
	return kind, id, nil
}

// lookupRowSecretHashes fetches every access/refresh secret that is entitled
// to revoke the bearer row, without applying revocation or expiry checks.
func (v *TokenValidator) lookupRowSecretHashes(ctx context.Context, kind BearerKind, tokenID string) (string, [][]byte, error) {
	switch kind {
	case BearerKindAPI:
		row, err := v.store.APITokens().GetByID(ctx, tokenID)
		if err != nil {
			return "", nil, err
		}
		return row.ID, [][]byte{row.SecretHash, row.RefreshHash, row.PreviousRefreshHash}, nil
	case BearerKindDelegation:
		row, err := v.store.DelegationTokens().GetByID(ctx, tokenID)
		if err != nil {
			return "", nil, err
		}
		return row.ID, [][]byte{row.SecretHash, row.RefreshHash}, nil
	}
	return "", nil, ErrInvalidToken
}

// ValidateBearer resolves a "lmx_<kind><id>_<secret>" bearer into a
// UserInfo. The kind tag (one char immediately after the `lmx_`
// prefix) tells the validator which table holds the row, so this is
// always a single PK lookup rather than the older "try
// api_tokens, fall back to delegation_tokens" pattern.
//
// Request interceptors cache successful bearer validations. Revocation
// paths apply CredentialLifecycleEffects to invalidate every cached secret and
// terminate work authorized by the token row.
func (v *TokenValidator) ValidateBearer(ctx context.Context, bearer string) (*UserInfo, error) {
	kind, tokenID, secret, err := ParseBearer(bearer)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	loaded, err := v.loadBearer(ctx, kind, tokenID)
	if err != nil {
		return nil, err
	}
	user, err := v.validateRow(ctx, loaded.fields, secret)
	if err != nil {
		return nil, err
	}
	user.Credential = loaded.credential
	loaded.touch()
	return user, nil
}

type loadedBearer struct {
	fields     validateRowFields
	touch      func()
	credential CredentialIdentity
}

// apiTokenExpired reports whether a CLI credential expired at now, by either
// of the two deadlines that limit it. See loadBearer.
//
// A nil expiresAt means a credential that never expires on its own; the
// ceiling still applies to it. A zero createdAt means a row the caller did not
// load a creation instant for, and this function skips the ceiling rather than
// treating the row as "created at the epoch, therefore expired" -- failing closed on a mapping
// slip would refuse every live credential at once.
func apiTokenExpired(expiresAt *time.Time, createdAt, now time.Time) bool {
	if expiresAt != nil && IsExpired(now, *expiresAt) {
		return true
	}
	return !createdAt.IsZero() && IsExpired(now, createdAt.Add(AbsoluteTokenLifetime))
}

// loadBearer projects each persisted bearer type into explicit shared
// validation data plus its post-validation touch operation.
func (v *TokenValidator) loadBearer(ctx context.Context, kind BearerKind, tokenID string) (loadedBearer, error) {
	switch kind {
	case BearerKindAPI:
		api, err := v.store.APITokens().GetByID(ctx, tokenID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return loadedBearer{}, connect.NewError(connect.CodeUnauthenticated, ErrInvalidToken)
			}
			return loadedBearer{}, connect.NewError(connect.CodeInternal, err)
		}
		// The APP's own retirement, joined onto the row. A hub that died
		// part-way through a disconnect would otherwise leave a live
		// credential on an app nobody can see any more; this refuses it at the
		// next request rather than waiting for the cascade to be retried.
		if api.ClientRevokedAt != nil {
			return loadedBearer{}, connect.NewError(connect.CodeUnauthenticated, ErrTokenRevoked)
		}
		// The stored grant, NARROWED at the moment the row is READ by BOTH
		// ceilings that govern it. A mint bug, a hand-edited row or a restored
		// backup therefore cannot produce an over-scoped credential that
		// authenticates, and neither can a registration whose owner has since
		// taken a permission back.
		//
		// An UNKNOWN scope token refuses the whole credential. Dropping it is
		// the failure that looks safe: a row holding two scopes whose second
		// became unknown would keep working as a narrower app, and nobody
		// would notice the vocabulary drifted.
		scopes, err := authscope.Parse(api.GrantedScopes)
		if err != nil {
			slog.WarnContext(ctx, "api token carries an unreadable grant",
				"token_id", api.ID, "err", err)
			return loadedBearer{}, connect.NewError(connect.CodeUnauthenticated, ErrInvalidToken)
		}
		// The KIND's ceiling: a property of what kind of credential this is,
		// at every validation rather than of the leg that happened to mint it.
		scopes = scopes.NarrowTo(CeilingFor(BearerKindAPI))
		// The APP's ceiling: what its registration says it may ask for.
		//
		// Read HERE and not only at the consent, for the reason
		// ClientElevationAllowed is read here one line down -- the two are the
		// same rule over two columns. An owner who removes a permission from a
		// registration means the app should no longer have it, and a ceiling
		// applied only at the mint would make that edit a silent no-op for
		// every credential already issued: their only remedy would be to
		// disconnect the app entirely.
		//
		// It NARROWS and never widens, so putting a permission back does not
		// hand it to a credential whose owner never consented to it -- the
		// stored grant is still the other half of the intersection.
		//
		// An unreadable registration refuses the credential rather than
		// admitting it, on the same argument as the grant above: a ceiling
		// nobody can parse is not a ceiling.
		clientCeiling, err := authscope.Parse(api.ClientScopes)
		if err != nil {
			slog.WarnContext(ctx, "api token identifies an app whose registered scopes are unreadable",
				"token_id", api.ID, "client_id", api.ClientID, "err", err)
			return loadedBearer{}, connect.NewError(connect.CodeUnauthenticated, ErrInvalidToken)
		}
		scopes = scopes.NarrowTo(clientCeiling)
		elevation := NewElevation(api.ElevationProvenAt, api.ElevationExpiresAt)
		if !api.ClientElevationAllowed {
			elevation = Elevation{}
		}
		return loadedBearer{
			fields: validateRowFields{
				Revoked: api.RevokedAt != nil,
				// TWO deadlines, and the credential expires at whichever comes
				// first: its own expires_at, and the ceiling
				// AbsoluteTokenLifetime puts on created_at.
				//
				// The ceiling used to be arithmetic at the mint and the
				// rotation alone -- AccessWindowFor and RefreshWindowFor -- so
				// it held only for a credential whose windows those two
				// functions wrote. A mint path that wrote its own expiry, and
				// there is one on the admin surface, put a row past the
				// ceiling that nothing afterwards re-read. Reading it HERE
				// makes the ceiling a property of the credential at every
				// validation rather than of the leg that happens to compute a
				// window, so no present or future issuer can write past it.
				//
				// The arithmetic stays as well: the refresh leg must still
				// answer "this credential reached its maximum lifetime" and
				// revoke the row, which is a better answer than a silent
				// expiry.
				//
				// This is the API-token branch alone. AbsoluteTokenLifetime is
				// a rule about a CLI credential's consent, and a worker mints a
				// delegation token for one spawn.
				Expired:        apiTokenExpired(api.ExpiresAt, api.CreatedAt, v.now()),
				SecretHash:     api.SecretHash,
				UserID:         api.UserID,
				RowID:          api.ID,
				CreatedAt:      api.CreatedAt,
				ExpiresAt:      ptrconv.DerefTime(api.ExpiresAt),
				AuthGeneration: api.AuthGeneration,
				Scopes:         scopes,
				// Through NewElevation, which refuses half a stored pair --
				// the same read the session path uses, so a repaired or
				// restored row cannot admit a restricted action on a factor
				// that was never proven.
				//
				// ZEROED when the app may not elevate. Reading the flag HERE,
				// at every validation, is what makes turning it off close a
				// live window on the next request rather than at the next
				// write -- the same argument apiTokenExpired makes for
				// AbsoluteTokenLifetime: read the ceiling at validation, so no
				// issuer, present or future, can write past it.
				Elevation: elevation,
			},
			touch:      func() { _ = v.store.APITokens().Touch(ctx, api.ID) },
			credential: APICredential(tokenID),
		}, nil

	case BearerKindDelegation:
		del, err := v.store.DelegationTokens().GetByID(ctx, tokenID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return loadedBearer{}, connect.NewError(connect.CodeUnauthenticated, ErrInvalidToken)
			}
			return loadedBearer{}, connect.NewError(connect.CodeInternal, err)
		}
		if del.WorkerID == "" {
			// A delegation row must always carry the worker that minted it: it
			// is the one limit on where the token may be used
			// (DelegationWorkerScope). An empty one is a data-integrity slip
			// that would make DelegationCredential panic -- and this runs as the
			// singleflight leader, so the panic re-fires into every follower
			// collapsed onto the same bearer key. Treat the malformed row as an
			// invalid token (permanent, not a retryable 500) instead.
			return loadedBearer{}, connect.NewError(connect.CodeUnauthenticated, ErrInvalidToken)
		}
		// The same read-time narrowing the api_tokens branch performs, and
		// here it carries the guarantee the deleted delegationAllowedProcedures
		// allowlist used to: a bearer minted for a process that reads untrusted
		// input can never administer the hub, whatever the row says.
		delScopes, err := authscope.Parse(del.GrantedScopes)
		if err != nil {
			slog.WarnContext(ctx, "delegation token carries an unreadable grant",
				"token_id", del.ID, "err", err)
			return loadedBearer{}, connect.NewError(connect.CodeUnauthenticated, ErrInvalidToken)
		}
		return loadedBearer{
			fields: validateRowFields{
				Revoked:        del.RevokedAt != nil,
				Expired:        IsExpired(v.now(), del.ExpiresAt),
				SecretHash:     del.SecretHash,
				UserID:         del.UserID,
				RowID:          del.ID,
				CreatedAt:      del.CreatedAt,
				ExpiresAt:      del.ExpiresAt,
				AuthGeneration: del.AuthGeneration,
				Scopes:         delScopes.NarrowTo(CeilingFor(BearerKindDelegation)),
			},
			touch:      func() { _ = v.store.DelegationTokens().Touch(ctx, del.ID) },
			credential: DelegationCredential(tokenID, del.WorkerID),
		}, nil
	}

	// parseBearer rejects unknown kinds; this case is unreachable but
	// kept as defence-in-depth so a future kind addition surfaces here
	// instead of silently falling through.
	return loadedBearer{}, connect.NewError(connect.CodeUnauthenticated, ErrInvalidToken)
}

// IsExpired treats expiry timestamps as exclusive upper limits: a credential
// is invalid at the recorded instant, not one clock tick afterward.
func IsExpired(now, expiresAt time.Time) bool {
	return !now.Before(expiresAt)
}

// validateRowFields is the union of fields ValidateBearer needs to
// classify a token row (whether it lives in api_tokens or
// delegation_tokens). loadBearer projects the per-table row into this
// shape so validateRow can stay table-agnostic.
type validateRowFields struct {
	Revoked    bool
	Expired    bool
	SecretHash []byte
	UserID     string
	RowID      string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	// Scopes is the credential's grant, already narrowed to its kind's
	// ceiling by loadBearer. The zero value reaches nothing, so a branch that
	// forgets to fill it denies rather than grants.
	Scopes         authscope.ScopeSet
	AuthGeneration int64
	// Elevation is the api_tokens step-up window. A delegation row has no
	// such pair and leaves it zero, which is the answer ElevationDeadline
	// needs anyway: it refuses a delegation credential on the kind first.
	Elevation Elevation
}

// validateRow runs the shared secret-match/revoked/expired/load-user path.
//
// validateRow verifies the secret FIRST, before it surfaces any revoked or
// expired state.
// token_id is non-secret (returned in JSON to /oauth/token, /oauth/token,
// and the worker delegation-mint endpoint), so a caller who knows only a victim's
// token_id must not be able to probe its existence or lifecycle: a wrong secret
// yields a uniform ErrInvalidToken, indistinguishable from loadBearer's not-found
// path. This mirrors the sibling VerifyBearerSecret, which deliberately never
// leaks which check failed. A legitimate secret-holder still learns
// revoked/expired below -- revocation and expiry leave secret_hash intact, so the
// access secret keeps matching -- so refresh-on-expiry is unaffected.
func (v *TokenValidator) validateRow(ctx context.Context, f validateRowFields, secret string) (*UserInfo, error) {
	if !hmac.Equal(v.HashSecret(secret), f.SecretHash) {
		return nil, connect.NewError(connect.CodeUnauthenticated, ErrInvalidToken)
	}
	if f.Revoked {
		return nil, connect.NewError(connect.CodeUnauthenticated, ErrTokenRevoked)
	}
	if f.Expired {
		return nil, connect.NewError(connect.CodeUnauthenticated, ErrTokenExpired)
	}
	user, err := v.loadUser(ctx, f.UserID)
	if err != nil {
		return nil, err
	}
	if f.AuthGeneration < user.UserAuthGeneration {
		return nil, connect.NewError(connect.CodeUnauthenticated, ErrTokenRevoked)
	}
	user.AuthenticatedAt = f.CreatedAt.UTC()
	user.CredentialExpiresAt = DeadlineAt(f.ExpiresAt.UTC())
	user.Scopes = f.Scopes
	user.Elevation = f.Elevation
	return user, nil
}

func (v *TokenValidator) loadUser(ctx context.Context, userID string) (*UserInfo, error) {
	u, err := v.store.Users().GetByID(ctx, userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("user not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("query user: %w", err))
	}
	if u.DeletedAt != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("user deleted"))
	}
	// A blank users.id fails closed in the same shape as the two rejections
	// above, rather than panicking: this runs per bearer validation, so corrupt
	// store data must deny the request, not crash the handler goroutine.
	id, ok := userid.New(u.ID)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("user not found"))
	}
	return &UserInfo{
		ID:                 id,
		Username:           u.Username,
		IsAdmin:            u.IsAdmin,
		Email:              u.Email,
		EmailVerified:      u.EmailVerified,
		UserAuthGeneration: u.AuthGeneration,
	}, nil
}

// ValidateRefresh validates a presented refresh token against an api_tokens
// row, distinguishing benign retries (within the grace window) from reuse-
// after-rotation (compromise). On a detected reuse it revokes the row and
// returns ErrRefreshReused.
//
// Returns the matched row on success along with whether the refresh matched the
// previous hash inside the grace window. A grace-window retry re-emits the
// cached access pair; a current-refresh match rotates the row. Refreshes are
// only valid against api_tokens (delegation tokens have a separate mint flow),
// so this function rejects a bearer with the wrong kind upfront.
func (v *TokenValidator) ValidateAPIRefresh(ctx context.Context, refresh string) (row *store.APIToken, retry bool, err error) {
	kind, tokenID, secret, perr := ParseBearer(refresh)
	if perr != nil {
		return nil, false, perr
	}
	if kind != BearerKindAPI {
		return nil, false, ErrInvalidToken
	}
	row, err = v.store.APITokens().GetByID(ctx, tokenID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, false, ErrInvalidToken
		}
		return nil, false, err
	}
	hashed := v.HashSecret(secret)
	currentMatches := len(row.RefreshHash) > 0 && hmac.Equal(hashed, row.RefreshHash)
	previousMatches := len(row.PreviousRefreshHash) > 0 && hmac.Equal(hashed, row.PreviousRefreshHash)
	if !currentMatches && !previousMatches {
		return nil, false, ErrInvalidToken
	}
	if row.RevokedAt != nil {
		return nil, false, ErrTokenRevoked
	}
	now := v.now()
	if currentMatches {
		if row.RefreshExpiresAt != nil && IsExpired(now, *row.RefreshExpiresAt) {
			return nil, false, ErrTokenExpired
		}
		if err := v.validateCredentialGeneration(ctx, row.UserID, row.AuthGeneration); err != nil {
			return nil, false, err
		}
		return row, false, nil
	}
	if previousMatches {
		// Within grace window: benign retry; outside: revoke.
		if row.PreviousRefreshExpiresAt != nil && !IsExpired(now, *row.PreviousRefreshExpiresAt) {
			if err := v.validateCredentialGeneration(ctx, row.UserID, row.AuthGeneration); err != nil {
				return nil, false, err
			}
			return row, true, nil
		}
		if _, err := v.store.APITokens().Revoke(ctx, row.ID); err != nil {
			return nil, false, fmt.Errorf("revoke reused refresh token: %w", err)
		}
		return nil, false, ErrRefreshReused
	}
	return nil, false, ErrInvalidToken
}

func (v *TokenValidator) validateCredentialGeneration(ctx context.Context, userID string, credentialGeneration int64) error {
	u, err := v.store.Users().GetByID(ctx, userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ErrTokenRevoked
		}
		return err
	}
	if u.DeletedAt != nil || credentialGeneration < u.AuthGeneration {
		return ErrTokenRevoked
	}
	return nil
}
