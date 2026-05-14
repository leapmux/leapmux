package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
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
	if !k.valid() {
		return 0, "", "", ErrInvalidToken
	}
	rest = rest[1:]
	idx := strings.Index(rest, "_")
	if idx <= 0 || idx >= len(rest)-1 {
		return 0, "", "", ErrInvalidToken
	}
	return k, rest[:idx], rest[idx+1:], nil
}

// BearerID extracts the stored row id from a "lmx_<kind><id>_<secret>"
// string. Returns "" on malformed input. The kind char is part of the
// bearer's wire format but NOT part of the row PK, so it's stripped
// here.
func BearerID(bearer string) string {
	_, tokenID, _, err := ParseBearer(bearer)
	if err != nil {
		return ""
	}
	return tokenID
}

// valid reports whether kind is one of the registered bearer kinds.
// A bearer with an unrecognised kind char is rejected outright —
// the validator never queries the DB for tokens it doesn't know
// how to look up.
func (k BearerKind) valid() bool {
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
	access := MintAccessSecret()
	refresh := MintAccessSecret()
	return MintedBearerPair{
		AccessBearer:     FormatBearer(kind, tokenID, access),
		RefreshBearer:    FormatBearer(kind, tokenID, refresh),
		AccessHash:       v.HashSecret(access),
		RefreshHash:      v.HashSecret(refresh),
		AccessExpiresAt:  now.Add(accessTTL),
		RefreshExpiresAt: now.Add(refreshTTL),
	}
}

// VerifyBearerSecret confirms a bearer's secret matches the stored row
// hash, *without* rejecting already-revoked or already-expired rows.
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
	id, hash, lerr := v.lookupRow(ctx, kind, tokenID)
	if lerr != nil {
		return 0, "", ErrInvalidToken
	}
	if !hmac.Equal(v.HashSecret(secret), hash) {
		return 0, "", ErrInvalidToken
	}
	return kind, id, nil
}

// lookupRow fetches the (row_id, secret_hash) for the bearer kind/id
// pair without applying revocation/expiry checks. Returns an error if
// the kind is unknown or the row doesn't exist.
func (v *TokenValidator) lookupRow(ctx context.Context, kind BearerKind, tokenID string) (string, []byte, error) {
	switch kind {
	case BearerKindAPI:
		row, err := v.store.APITokens().GetByID(ctx, tokenID)
		if err != nil {
			return "", nil, err
		}
		return row.ID, row.SecretHash, nil
	case BearerKindDelegation:
		row, err := v.store.DelegationTokens().GetByID(ctx, tokenID)
		if err != nil {
			return "", nil, err
		}
		return row.ID, row.SecretHash, nil
	}
	return "", nil, ErrInvalidToken
}

// ValidateBearer resolves a "lmx_<kind><id>_<secret>" bearer into a
// UserInfo. The kind tag (one char immediately after the `lmx_`
// prefix) tells the validator which table holds the row, so this is
// always a single PK lookup rather than the older "try
// api_tokens, fall back to delegation_tokens" pattern.
//
// The cache is keyed by tokenID; revocations call `EvictBearer` to
// bust it immediately.
func (v *TokenValidator) ValidateBearer(ctx context.Context, bearer string) (*UserInfo, error) {
	kind, tokenID, secret, err := ParseBearer(bearer)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	fields, touch, after, err := v.loadValidateFields(ctx, kind, tokenID)
	if err != nil {
		return nil, err
	}
	user, err := v.validateRow(ctx, fields, secret, after)
	if err != nil {
		return nil, err
	}
	touch()
	return user, nil
}

// loadValidateFields loads the per-kind row and projects it into the
// shared validateRowFields plus the kind-specific after-closure and a
// fire-and-forget Touch callback. Collapsing the per-kind path here
// keeps ValidateBearer free of the error-wrapping boilerplate that
// used to repeat in every case arm.
func (v *TokenValidator) loadValidateFields(ctx context.Context, kind BearerKind, tokenID string) (validateRowFields, func(), func(*UserInfo), error) {
	switch kind {
	case BearerKindAPI:
		api, err := v.store.APITokens().GetByID(ctx, tokenID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return validateRowFields{}, nil, nil, connect.NewError(connect.CodeUnauthenticated, ErrInvalidToken)
			}
			return validateRowFields{}, nil, nil, connect.NewError(connect.CodeInternal, err)
		}
		return validateRowFields{
				Revoked:    api.RevokedAt != nil,
				Expired:    api.ExpiresAt != nil && time.Now().After(*api.ExpiresAt),
				SecretHash: api.SecretHash,
				UserID:     api.UserID,
				RowID:      api.ID,
			},
			func() { _ = v.store.APITokens().Touch(ctx, api.ID) },
			nil,
			nil

	case BearerKindDelegation:
		del, err := v.store.DelegationTokens().GetByID(ctx, tokenID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return validateRowFields{}, nil, nil, connect.NewError(connect.CodeUnauthenticated, ErrInvalidToken)
			}
			return validateRowFields{}, nil, nil, connect.NewError(connect.CodeInternal, err)
		}
		return validateRowFields{
				Revoked:    del.RevokedAt != nil,
				Expired:    time.Now().After(del.ExpiresAt),
				SecretHash: del.SecretHash,
				UserID:     del.UserID,
				RowID:      del.ID,
			},
			func() { _ = v.store.DelegationTokens().Touch(ctx, del.ID) },
			// Pin the user info to the delegation's workspace scope.
			// Callers (ChannelService.OpenChannel today; future authz
			// checks tomorrow) avoid handing the bearer the user's full
			// accessible-workspace list this way.
			func(u *UserInfo) { u.DelegationWorkspaceID = del.WorkspaceID },
			nil
	}

	// parseBearer rejects unknown kinds; this case is unreachable but
	// kept as defence-in-depth so a future kind addition surfaces here
	// instead of silently falling through.
	return validateRowFields{}, nil, nil, connect.NewError(connect.CodeUnauthenticated, ErrInvalidToken)
}

// validateRowFields is the union of fields ValidateBearer needs to
// classify a token row (whether it lives in api_tokens or
// delegation_tokens). lookupRow projects the per-table row into this
// shape so validateRow can stay table-agnostic.
type validateRowFields struct {
	Revoked    bool
	Expired    bool
	SecretHash []byte
	UserID     string
	RowID      string
}

// validateRow runs the shared revoked/expired/secret-match/load-user
// path. Callers supply per-row policy via `f` and per-table side
// effects (workspace pinning) via `after`.
func (v *TokenValidator) validateRow(ctx context.Context, f validateRowFields, secret string, after func(*UserInfo)) (*UserInfo, error) {
	if f.Revoked {
		return nil, connect.NewError(connect.CodeUnauthenticated, ErrTokenRevoked)
	}
	if f.Expired {
		return nil, connect.NewError(connect.CodeUnauthenticated, ErrTokenExpired)
	}
	if !hmac.Equal(v.HashSecret(secret), f.SecretHash) {
		return nil, connect.NewError(connect.CodeUnauthenticated, ErrInvalidToken)
	}
	user, err := v.loadUser(ctx, f.UserID)
	if err != nil {
		return nil, err
	}
	user.BearerTokenID = f.RowID
	if after != nil {
		after(user)
	}
	return user, nil
}

func (v *TokenValidator) loadUser(ctx context.Context, userID string) (*UserInfo, error) {
	u, err := v.store.Users().GetByID(ctx, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("user not found"))
	}
	if u.DeletedAt != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("user deleted"))
	}
	return &UserInfo{
		ID:            u.ID,
		OrgID:         u.OrgID,
		Username:      u.Username,
		IsAdmin:       u.IsAdmin,
		Email:         u.Email,
		EmailVerified: u.EmailVerified,
	}, nil
}

// ValidateRefresh validates a presented refresh token against an api_tokens
// row, distinguishing benign retries (within the grace window) from reuse-
// after-rotation (compromise). If reuse is detected the row is revoked
// and ErrRefreshReused is returned.
//
// Returns the matched row on success along with whether the caller should
// rotate (true for first use of current refresh, false for grace-window
// retry — same access pair will be returned). Refreshes are only valid
// against api_tokens (delegation tokens have a separate mint flow), so
// a bearer with the wrong kind is rejected upfront.
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
	if row.RevokedAt != nil {
		return nil, false, ErrTokenRevoked
	}
	hashed := v.HashSecret(secret)
	if len(row.RefreshHash) > 0 && hmac.Equal(hashed, row.RefreshHash) {
		return row, false, nil
	}
	if len(row.PreviousRefreshHash) > 0 && hmac.Equal(hashed, row.PreviousRefreshHash) {
		// Within grace window → benign retry; outside → revoke.
		if row.PreviousRefreshExpiresAt != nil && time.Now().Before(*row.PreviousRefreshExpiresAt) {
			return row, true, nil
		}
		_, _ = v.store.APITokens().Revoke(ctx, row.ID)
		return nil, false, ErrRefreshReused
	}
	return nil, false, ErrInvalidToken
}
