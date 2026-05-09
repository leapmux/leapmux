package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	pwdhash "github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/usernames"
	"github.com/leapmux/leapmux/internal/util/id"
)

// SessionDuration is the lifetime of a user session.
const SessionDuration = 24 * time.Hour

type contextKey int

const userKey contextKey = iota

// UserInfo contains the authenticated user's information.
//
// Email and EmailVerified are loaded by ValidateToken from a JOIN on
// users; they're cached for sessionCacheTTL alongside the rest. Mutating
// either column on `users` (verify, change, admin reset) requires
// evicting the user's cached sessions — see SessionCache.EvictByUserID.
//
// DelegationWorkspaceID is set only when the request was authenticated
// by a delegation_tokens row (as opposed to a session cookie or an
// api_tokens bearer). It pins the request to the workspace the
// delegation was minted for; downstream authorization (notably
// ChannelService.OpenChannel) MUST narrow accessible-workspace
// reasoning to this single id rather than the user's full grant set,
// so a compromised delegation bearer cannot pivot beyond its scope.
//
// BearerTokenID is the api_tokens / delegation_tokens primary key
// when the request was authenticated by an `lmx_…` bearer; empty for
// cookie-based sessions. ChannelService.OpenChannel records it on
// the channelmgr entry so revocation paths can force-close every
// open channel that was authorized by a now-revoked token.
type UserInfo struct {
	ID                    string
	SessionID             string // session that authenticated this request
	OrgID                 string
	Username              string
	IsAdmin               bool
	Email                 string
	EmailVerified         bool
	DelegationWorkspaceID string
	BearerTokenID         string
}

// WithUser stores a UserInfo in the context.
func WithUser(ctx context.Context, u *UserInfo) context.Context {
	return context.WithValue(ctx, userKey, u)
}

// GetUser retrieves UserInfo from the context. Returns nil if not authenticated.
func GetUser(ctx context.Context) *UserInfo {
	u, _ := ctx.Value(userKey).(*UserInfo)
	return u
}

// MustGetUser retrieves UserInfo from the context, returning an error if not
// authenticated.
func MustGetUser(ctx context.Context) (*UserInfo, error) {
	u := GetUser(ctx)
	if u == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("not authenticated"))
	}
	return u, nil
}

// RevokeAllUserCredentials revokes every active api_tokens and
// delegation_tokens row for userID and bumps
// users.tokens_revoked_at. Returns (apiCount, delegationCount) so
// admin handlers can report what was killed.
//
// Caller-side concerns:
//   - The bearer cache lives in the running hub process; admin CLI
//     callers don't need to evict it (the revocation watcher closes
//     the loop). In-process callers should wrap this in
//     PropagateUserRevocation, which adds cache invalidation.
//   - Pass a transaction-scoped Store to keep the multi-row update
//     atomic; admin handlers wrap this in `RunInTransaction`.
//
// Returns the first store error and leaves later steps unrun, so a
// failed api-token revoke aborts before the delegation-token revoke
// or the watermark bump.
func RevokeAllUserCredentials(ctx context.Context, st store.Store, userID string) (int64, int64, error) {
	apiCount, err := st.APITokens().RevokeByUser(ctx, userID)
	if err != nil {
		return 0, 0, fmt.Errorf("revoke api tokens: %w", err)
	}
	delegationCount, err := st.DelegationTokens().RevokeByUser(ctx, userID)
	if err != nil {
		return apiCount, 0, fmt.Errorf("revoke delegation tokens: %w", err)
	}
	if _, err := st.Users().BumpTokensRevokedAt(ctx, userID); err != nil {
		return apiCount, delegationCount, fmt.Errorf("bump user tokens_revoked_at: %w", err)
	}
	return apiCount, delegationCount, nil
}

// PropagateUserRevocation revokes every live api_tokens and
// delegation_tokens row for userID, bumps users.tokens_revoked_at
// (so the hub's revocation watcher picks the event up
// cross-process), and busts the in-memory bearer cache so the
// revocation is observed immediately rather than after the 30s
// cache TTL.
//
// Hooked from in-process credential-rotation paths
// (UserService.ChangePassword today; future account-deactivation
// flows). Admin CLI commands run in a different process; they
// mutate the same DB rows directly and the watcher closes the
// loop. The function is idempotent and safe to call repeatedly.
//
// Returns the total count of credentials revoked and the first
// store error if any. Callers tend to treat revoke failures as
// warnings: the watcher's next sweep retries, and the row's TTL
// bounds the worst case.
func PropagateUserRevocation(ctx context.Context, st store.Store, sc *SessionCache, userID string) (int64, error) {
	if userID == "" {
		return 0, nil
	}
	apiCount, delegationCount, revErr := RevokeAllUserCredentials(ctx, st, userID)
	if sc != nil {
		// The user-wide sweeps Range every cached entry exactly once,
		// covering both the rows we just revoked and any issued between
		// listing and revoking. A prior list-then-evict pass paired with
		// the sweep was strictly redundant.
		sc.EvictBearersByUserID(userID)
		sc.EvictByUserID(userID)
	}
	return apiCount + delegationCount, revErr
}

// LoadSoloUser looks up the bootstrapped solo user and maps it into a
// UserInfo suitable for synthetic authentication in solo mode. Returns
// store.ErrNotFound if the solo user has not been created yet.
func LoadSoloUser(ctx context.Context, st store.Store) (*UserInfo, error) {
	user, err := st.Users().GetByUsername(ctx, usernames.Solo)
	if err != nil {
		return nil, err
	}
	return &UserInfo{
		ID:       user.ID,
		OrgID:    user.OrgID,
		Username: user.Username,
		IsAdmin:  user.IsAdmin,
	}, nil
}

// Login validates credentials and creates a new session token.
// Returns the session ID, user, session expiry time, and any error.
func Login(ctx context.Context, st store.Store, username, password string) (string, *store.User, time.Time, error) {
	var zero time.Time
	user, err := st.Users().GetByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", nil, zero, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid credentials"))
		}
		return "", nil, zero, connect.NewError(connect.CodeInternal, fmt.Errorf("query user: %w", err))
	}

	match, err := pwdhash.Verify(user.PasswordHash, password)
	if err != nil {
		return "", nil, zero, connect.NewError(connect.CodeInternal, fmt.Errorf("verify password: %w", err))
	}
	if !match {
		return "", nil, zero, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid credentials"))
	}

	sessionID, expiresAt, sessionErr := CreateSession(ctx, st, user.ID)
	if sessionErr != nil {
		return "", nil, zero, connect.NewError(connect.CodeInternal, sessionErr)
	}

	return sessionID, user, expiresAt, nil
}

// SessionMeta holds optional metadata for session creation.
type SessionMeta struct {
	UserAgent string
	IPAddress string
}

// CreateSession creates a new user session and returns the session ID and
// expiry time.
func CreateSession(ctx context.Context, st store.Store, userID string, meta ...SessionMeta) (string, time.Time, error) {
	sessionID := id.Generate()
	expiresAt := time.Now().Add(SessionDuration).UTC()
	params := store.CreateSessionParams{
		ID:        sessionID,
		UserID:    userID,
		ExpiresAt: expiresAt,
	}
	if len(meta) > 0 {
		params.UserAgent = meta[0].UserAgent
		params.IPAddress = meta[0].IPAddress
	}
	if err := st.Sessions().Create(ctx, params); err != nil {
		return "", time.Time{}, fmt.Errorf("create session: %w", err)
	}
	return sessionID, expiresAt, nil
}

// ValidateToken resolves a session token to a UserInfo. Returns an error if
// the token is invalid or expired. Uses a single joined query to avoid two
// sequential DB round-trips.
func ValidateToken(ctx context.Context, st store.Store, token string) (*UserInfo, error) {
	row, err := st.Sessions().ValidateWithUser(ctx, token)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid or expired token"))
		}
		return nil, fmt.Errorf("validate session: %w", err)
	}

	return &UserInfo{
		ID:            row.UserID,
		OrgID:         row.OrgID,
		Username:      row.Username,
		IsAdmin:       row.IsAdmin,
		Email:         row.Email,
		EmailVerified: row.EmailVerified,
	}, nil
}

// RequireOrgAdmin verifies that the user is a member of the organization with
// owner or admin role. Returns a connect error on failure.
func RequireOrgAdmin(ctx context.Context, st store.Store, orgID, userID string) error {
	member, err := st.OrgMembers().GetByOrgAndUser(ctx, orgID, userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("not a member of this organization"))
		}
		return connect.NewError(connect.CodeInternal, err)
	}
	if member.Role != leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_OWNER && member.Role != leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_ADMIN {
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("insufficient permissions"))
	}
	return nil
}

// ResolveOrgID determines the effective org ID for a request.
// If requestedOrgID is empty, returns the user's personal org.
// Otherwise, verifies the user is a member of the requested org.
func ResolveOrgID(ctx context.Context, st store.Store, user *UserInfo, requestedOrgID string) (string, error) {
	if requestedOrgID == "" {
		return user.OrgID, nil
	}

	isMember, err := st.OrgMembers().IsMember(ctx, store.IsOrgMemberParams{
		OrgID:  requestedOrgID,
		UserID: user.ID,
	})
	if err != nil {
		return "", fmt.Errorf("check org membership: %w", err)
	}
	if !isMember {
		return "", connect.NewError(connect.CodeNotFound, fmt.Errorf("not a member of this organization"))
	}

	return requestedOrgID, nil
}
