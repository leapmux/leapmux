package auth

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/crypto/bcrypt"

	"github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/id"
)

type contextKey int

const userKey contextKey = iota

// UserInfo contains the authenticated user's information.
type UserInfo struct {
	ID       string
	OrgID    string
	Username string
	IsAdmin  bool
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
func Login(ctx context.Context, q *db.Queries, username, password string) (string, *db.User, error) {
	user, err := q.GetUserByUsername(ctx, username)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid credentials"))
		}
		return "", nil, connect.NewError(connect.CodeInternal, fmt.Errorf("query user: %w", err))
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return "", nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid credentials"))
	}

	sessionID := id.Generate()
	expiresAt := time.Now().Add(24 * time.Hour).UTC()
	if err := q.CreateUserSession(ctx, db.CreateUserSessionParams{
		ID:        sessionID,
		UserID:    user.ID,
		ExpiresAt: expiresAt,
	}); err != nil {
		return "", nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create session: %w", err))
	}

	return sessionID, &user, nil
}

// ValidateToken resolves a session token to a UserInfo. Returns an error if
// the token is invalid or expired.
func ValidateToken(ctx context.Context, q *db.Queries, token string) (*UserInfo, error) {
	sess, err := q.GetUserSessionByID(ctx, token)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid or expired token"))
		}
		return nil, fmt.Errorf("query session: %w", err)
	}

	user, err := q.GetUserByID(ctx, sess.UserID)
	if err != nil {
		return nil, fmt.Errorf("query user: %w", err)
	}

	return &UserInfo{
		ID:       user.ID,
		OrgID:    user.OrgID,
		Username: user.Username,
		IsAdmin:  user.IsAdmin == 1,
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

// TokenFromHeader extracts a Bearer token from an Authorization header value.
func TokenFromHeader(authHeader string) string {
	const prefix = "Bearer "
	if strings.HasPrefix(authHeader, prefix) {
		return strings.TrimPrefix(authHeader, prefix)
	}
	return ""
}
