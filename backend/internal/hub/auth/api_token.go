package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"

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

// RefreshTokenTTL is the lifetime of a CLI refresh token.
const RefreshTokenTTL = 90 * 24 * time.Hour

// DelegationTokenTTL is the lifetime of a worker-minted delegation
// token. Short by design: agents that outlive the TTL refresh.
const DelegationTokenTTL = 1 * time.Hour

// RefreshReuseGrace is how long a previously-rotated refresh token is
// honoured as a benign retry after rotation. Reuse outside this window
// triggers compromise revocation.
const RefreshReuseGrace = 60 * time.Second

// ErrInvalidToken is returned for malformed bearer strings. ErrTokenExpired
// is returned for syntactically valid but expired/revoked tokens.
var (
	ErrInvalidToken  = errors.New("invalid token")
	ErrTokenExpired  = errors.New("token expired")
	ErrTokenRevoked  = errors.New("token revoked")
	ErrRefreshReused = errors.New("refresh token reused")
)

// TokenValidator verifies api_token / delegation_token bearers against
// the hub store, applying caching + HMAC-pepper hashing. The same
// validator is used by the request interceptor and the WebSocket relay
// upgrade path.
type TokenValidator struct {
	store  store.Store
	pepper []byte
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
// A bearer with an unrecognised kind char is rejected outright —
// the validator never queries the DB for tokens it doesn't know
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
// bearer wire format, secret hashing, and TTL derivation are defined once and
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
		return loadedBearer{
			fields: validateRowFields{
				Revoked:        api.RevokedAt != nil,
				Expired:        api.ExpiresAt != nil && IsExpired(time.Now(), *api.ExpiresAt),
				SecretHash:     api.SecretHash,
				UserID:         api.UserID,
				RowID:          api.ID,
				CreatedAt:      api.CreatedAt,
				ExpiresAt:      ptrconv.DerefTime(api.ExpiresAt),
				AuthGeneration: api.AuthGeneration,
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
		if del.WorkspaceID == "" || del.WorkerID == "" {
			// A delegation row must always carry a workspace scope AND the worker
			// that minted it. An empty either is a data-integrity slip that would
			// make DelegationCredential panic -- and this runs as the singleflight
			// leader, so the panic re-fires into every follower collapsed onto
			// the same bearer key. Treat the malformed row as an invalid token
			// (permanent, not a retryable 500) instead.
			//
			// Both fields are guarded because the constructor requires both: the
			// minter joined workspace_id as a required field when it became the
			// bound on where a token may be used, and a guard that covers two of
			// three required fields leaves the third to panic on the one input
			// this function exists to reject cleanly.
			return loadedBearer{}, connect.NewError(connect.CodeUnauthenticated, ErrInvalidToken)
		}
		return loadedBearer{
			fields: validateRowFields{
				Revoked:        del.RevokedAt != nil,
				Expired:        IsExpired(time.Now(), del.ExpiresAt),
				SecretHash:     del.SecretHash,
				UserID:         del.UserID,
				RowID:          del.ID,
				CreatedAt:      del.CreatedAt,
				ExpiresAt:      del.ExpiresAt,
				AuthGeneration: del.AuthGeneration,
			},
			touch:      func() { _ = v.store.DelegationTokens().Touch(ctx, del.ID) },
			credential: DelegationCredential(tokenID, del.WorkspaceID, del.WorkerID),
		}, nil
	}

	// parseBearer rejects unknown kinds; this case is unreachable but
	// kept as defence-in-depth so a future kind addition surfaces here
	// instead of silently falling through.
	return loadedBearer{}, connect.NewError(connect.CodeUnauthenticated, ErrInvalidToken)
}

// IsExpired treats expiry timestamps as exclusive upper bounds: a credential
// is invalid at the recorded instant, not one clock tick afterward.
func IsExpired(now, expiresAt time.Time) bool {
	return !now.Before(expiresAt)
}

// validateRowFields is the union of fields ValidateBearer needs to
// classify a token row (whether it lives in api_tokens or
// delegation_tokens). loadBearer projects the per-table row into this
// shape so validateRow can stay table-agnostic.
type validateRowFields struct {
	Revoked        bool
	Expired        bool
	SecretHash     []byte
	UserID         string
	RowID          string
	CreatedAt      time.Time
	ExpiresAt      time.Time
	AuthGeneration int64
}

// validateRow runs the shared secret-match/revoked/expired/load-user path.
//
// The secret is verified FIRST, before any revoked/expired state is surfaced.
// token_id is non-secret (returned in JSON to /auth/cli/token, /auth/cli/refresh,
// and the worker delegation-mint endpoint), so a caller who knows only a victim's
// token_id must not be able to probe its existence or lifecycle: a wrong secret
// yields a uniform ErrInvalidToken, indistinguishable from loadBearer's not-found
// path. This mirrors the sibling VerifyBearerSecret, which is deliberately built to
// never leak which check failed. A legitimate secret-holder still learns
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
		OrgID:              u.OrgID,
		Username:           u.Username,
		IsAdmin:            u.IsAdmin,
		Email:              u.Email,
		EmailVerified:      u.EmailVerified,
		UserAuthGeneration: u.AuthGeneration,
	}, nil
}

// ValidateRefresh validates a presented refresh token against an api_tokens
// row, distinguishing benign retries (within the grace window) from reuse-
// after-rotation (compromise). If reuse is detected the row is revoked
// and ErrRefreshReused is returned.
//
// Returns the matched row on success along with whether the refresh matched the
// previous hash inside the grace window. A grace-window retry re-emits the
// cached access pair; a current-refresh match rotates the row. Refreshes are
// only valid against api_tokens (delegation tokens have a separate mint flow),
// so a bearer with the wrong kind is rejected upfront.
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
	now := time.Now()
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
