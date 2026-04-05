package auth

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"connectrpc.com/connect"

	"github.com/leapmux/leapmux/internal/hub/generated/db"
	pwdhash "github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/util/id"
)

// SessionDuration is the lifetime of a user session.
const SessionDuration = 24 * time.Hour

type contextKey int

const userKey contextKey = iota

// UserInfo contains the authenticated user's information.
type UserInfo struct {
	ID            string
	OrgID         string
	Username      string
	IsAdmin       bool
	EmailVerified bool
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

// Login validates credentials and creates a new session token.
// Returns the session ID, user, session expiry time, and any error.
func Login(ctx context.Context, q *db.Queries, username, password string) (string, *db.User, time.Time, error) {
	var zero time.Time
	user, err := q.GetUserByUsername(ctx, username)
	if err != nil {
		if err == sql.ErrNoRows {
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

	sessionID, expiresAt, sessionErr := CreateSession(ctx, q, user.ID)
	if sessionErr != nil {
		return "", nil, zero, connect.NewError(connect.CodeInternal, sessionErr)
	}

	return sessionID, &user, expiresAt, nil
}

// SessionMeta holds optional metadata for session creation.
type SessionMeta struct {
	UserAgent string
	IPAddress string
}

// CreateSession creates a new user session and returns the session ID and
// expiry time.
func CreateSession(ctx context.Context, q *db.Queries, userID string, meta ...SessionMeta) (string, time.Time, error) {
	sessionID := id.Generate()
	expiresAt := time.Now().Add(SessionDuration).UTC()
	params := db.CreateUserSessionParams{
		ID:        sessionID,
		UserID:    userID,
		ExpiresAt: expiresAt,
	}
	if len(meta) > 0 {
		params.UserAgent = meta[0].UserAgent
		params.IpAddress = meta[0].IPAddress
	}
	if err := q.CreateUserSession(ctx, params); err != nil {
		return "", time.Time{}, fmt.Errorf("create session: %w", err)
	}
	return sessionID, expiresAt, nil
}

// ValidateToken resolves a session token to a UserInfo. Returns an error if
// the token is invalid or expired. Uses a single joined query to avoid two
// sequential DB round-trips.
func ValidateToken(ctx context.Context, q *db.Queries, token string) (*UserInfo, error) {
	row, err := q.ValidateSessionWithUser(ctx, token)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid or expired token"))
		}
		return nil, fmt.Errorf("validate session: %w", err)
	}

	return &UserInfo{
		ID:            row.ID,
		OrgID:         row.OrgID,
		Username:      row.Username,
		IsAdmin:       row.IsAdmin == 1,
		EmailVerified: row.EmailVerified == 1,
	}, nil
}

// ResolveOrgID determines the effective org ID for a request.
// If requestedOrgID is empty, returns the user's personal org.
// Otherwise, verifies the user is a member of the requested org.
func ResolveOrgID(ctx context.Context, q *db.Queries, user *UserInfo, requestedOrgID string) (string, error) {
	if requestedOrgID == "" {
		return user.OrgID, nil
	}

	isMember, err := q.IsOrgMember(ctx, db.IsOrgMemberParams{
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
