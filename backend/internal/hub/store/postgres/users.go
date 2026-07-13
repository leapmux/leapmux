package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
)

type userStore struct {
	conn *pgConn
}

var _ store.UserStore = (*userStore)(nil)

func fromDBUser(u gendb.User) store.User {
	return store.User{
		ID:                    u.ID,
		OrgID:                 u.OrgID,
		Username:              u.Username,
		PasswordHash:          u.PasswordHash,
		DisplayName:           u.DisplayName,
		Email:                 u.Email,
		EmailVerified:         u.EmailVerified,
		PendingEmail:          u.PendingEmail,
		PendingEmailToken:     u.PendingEmailToken,
		PendingEmailExpiresAt: tsToTimePtr(u.PendingEmailExpiresAt),
		PendingEmailAttempts:  int64(u.PendingEmailAttempts),
		PasswordSet:           u.PasswordSet,
		IsAdmin:               u.IsAdmin,
		Prefs:                 u.Prefs,
		CreatedAt:             tsToTime(u.CreatedAt),
		UpdatedAt:             tsToTime(u.UpdatedAt),
		TokensRevokedAt:       tsToTimePtr(u.TokensRevokedAt),
		AuthGeneration:        u.AuthGeneration,
		DeletedAt:             tsToTimePtr(u.DeletedAt),
	}
}

func fromDBUsers(rows []gendb.User) []store.User {
	return store.MapSlice(rows, fromDBUser)
}

func (s *userStore) Create(ctx context.Context, p store.CreateUserParams) error {
	return mapErr(s.conn.q.CreateUser(ctx, gendb.CreateUserParams{
		ID:            p.ID,
		OrgID:         p.OrgID,
		Username:      store.NormalizeUsername(p.Username),
		PasswordHash:  p.PasswordHash,
		DisplayName:   p.DisplayName,
		Email:         store.NormalizeEmail(p.Email),
		EmailVerified: p.EmailVerified,
		PasswordSet:   p.PasswordSet,
		IsAdmin:       p.IsAdmin,
	}))
}

func (s *userStore) GetByID(ctx context.Context, id string) (*store.User, error) {
	u, err := s.conn.q.GetUserByID(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	out := fromDBUser(u)
	return &out, nil
}

func (s *userStore) GetByIDIncludeDeleted(ctx context.Context, id string) (*store.User, error) {
	u, err := s.conn.q.GetUserByIDIncludeDeleted(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	out := fromDBUser(u)
	return &out, nil
}

func (s *userStore) GetByUsername(ctx context.Context, username string) (*store.User, error) {
	u, err := s.conn.q.GetUserByUsername(ctx, store.NormalizeUsername(username))
	if err != nil {
		return nil, mapErr(err)
	}
	out := fromDBUser(u)
	return &out, nil
}

func (s *userStore) GetByEmail(ctx context.Context, email string) (*store.User, error) {
	u, err := s.conn.q.GetUserByEmail(ctx, store.NormalizeEmail(email))
	if err != nil {
		return nil, mapErr(err)
	}
	out := fromDBUser(u)
	return &out, nil
}

func (s *userStore) GetFirstAdmin(ctx context.Context) (*store.User, error) {
	u, err := s.conn.q.GetFirstAdmin(ctx)
	if err != nil {
		return nil, mapErr(err)
	}
	out := fromDBUser(u)
	return &out, nil
}

func (s *userStore) ExistsByUsername(ctx context.Context, username string) (bool, error) {
	exists, err := s.conn.q.ExistsByUsername(ctx, store.NormalizeUsername(username))
	if err != nil {
		return false, mapErr(err)
	}
	return exists, nil
}

func (s *userStore) ExistsByEmail(ctx context.Context, email, excludeUserID string) (bool, error) {
	exists, err := s.conn.q.ExistsByEmail(ctx, gendb.ExistsByEmailParams{
		Email:         store.NormalizeEmail(email),
		ExcludeUserID: excludeUserID,
	})
	if err != nil {
		return false, mapErr(err)
	}
	return exists, nil
}

func (s *userStore) ConsumeVerificationAttempt(ctx context.Context, id string) (*store.User, error) {
	u, err := s.conn.q.ConsumeVerificationAttempt(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	out := fromDBUser(u)
	return &out, nil
}

func (s *userStore) GetPrefs(ctx context.Context, id string) (string, error) {
	prefs, err := s.conn.q.GetUserPrefs(ctx, id)
	return prefs, mapErr(err)
}

func (s *userStore) HasAny(ctx context.Context) (bool, error) {
	ok, err := s.conn.q.HasAnyUser(ctx)
	if err != nil {
		return false, mapErr(err)
	}
	return ok, nil
}

func (s *userStore) Count(ctx context.Context) (int64, error) {
	n, err := s.conn.q.CountUsers(ctx)
	return n, mapErr(err)
}

func (s *userStore) ListByOrgID(ctx context.Context, orgID string) ([]store.User, error) {
	rows, err := s.conn.q.ListUsersByOrgID(ctx, orgID)
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBUsers(rows), nil
}

func (s *userStore) ListByIDs(ctx context.Context, ids []string) ([]store.User, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.conn.q.ListUsersByIDs(ctx, ids)
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBUsers(rows), nil
}

func (s *userStore) ListAll(ctx context.Context, p store.ListAllUsersParams) ([]store.User, error) {
	params, err := listAllUsersParams(p.Cursor, p.Limit)
	if err != nil {
		return nil, err
	}
	rows, err := s.conn.q.ListAllUsers(ctx, params)
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBUsers(rows), nil
}

func (s *userStore) Search(ctx context.Context, p store.SearchUsersParams) ([]store.User, error) {
	params, err := searchUsersParams(p.Query, p.Cursor, p.Limit)
	if err != nil {
		return nil, err
	}
	rows, err := s.conn.q.SearchUsers(ctx, params)
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBUsers(rows), nil
}

// loadUserInfoCacheFields reads the current cached-UserInfo projection for id.
// RunUserInfoMutation calls it before and after a mutation to derive whether a
// user_info event fires; a missing row reports exists=false.
func loadUserInfoCacheFields(ctx context.Context, conn *pgConn, id string) (store.UserInfoCacheFields, bool, error) {
	u, err := conn.q.GetUserByID(ctx, id)
	if err != nil {
		mapped := mapErr(err)
		if errors.Is(mapped, store.ErrNotFound) {
			return store.UserInfoCacheFields{}, false, nil
		}
		return store.UserInfoCacheFields{}, false, mapped
	}
	return store.UserInfoCacheFieldsOf(fromDBUser(u)), true, nil
}

// runUserInfoMutation wires the shared RunUserInfoMutation to this dialect's
// projection read and user_info event insert, so each Update* method supplies
// only its UPDATE and the durable cache-invalidation is derived from the
// before/after projection rather than a hand-computed flag.
func (s *userStore) runUserInfoMutation(ctx context.Context, id string, mutate func(ctx context.Context, conn *pgConn) (userID string, updatedAt time.Time, ok bool, err error)) error {
	// Lock the user row before the before-read so a concurrent same-user mutation
	// cannot commit between the before and after cached-field projections and hide
	// a change (which would drop the durable user_info invalidation for that field).
	lockThenTx := func(ctx context.Context, fn func(*pgConn) error) error {
		return s.conn.withTransaction(ctx, func(conn *pgConn) error {
			if err := conn.q.LockUserRow(ctx, id); err != nil {
				return mapErr(err)
			}
			return fn(conn)
		})
	}
	return store.RunUserInfoMutation(ctx, lockThenTx,
		func(ctx context.Context, conn *pgConn) (store.UserInfoCacheFields, bool, error) {
			return loadUserInfoCacheFields(ctx, conn, id)
		},
		mutate,
		func(ctx context.Context, conn *pgConn, userID string, updatedAt time.Time) error {
			return insertRevocationEvent(ctx, conn, store.RevocationEventKindUserInfo, userID, userID, updatedAt, 0)
		},
	)
}

// updatedUserResult maps a RETURNING (id, updated_at) row plus error into the
// (userID, updatedAt, changed, err) tuple runUserInfoMutation expects: a mapped
// ErrNotFound (no row updated) is a silent no-op (changed=false, nil err), any
// other error propagates mapped, and a live row yields the parsed updated_at
// with changed=true. Shared by every cached-field Update* mutate closure so the
// not-found / RETURNING-time handling lives in one place instead of five
// identical tails.
func updatedUserResult(id string, updatedAt time.Time, valid bool, err error) (string, time.Time, bool, error) {
	if err != nil {
		mapped := mapErr(err)
		if errors.Is(mapped, store.ErrNotFound) {
			return "", time.Time{}, false, nil
		}
		return "", time.Time{}, false, mapped
	}
	at, err := sqlutil.RequireTime(updatedAt, valid, "updated_at")
	if err != nil {
		return "", time.Time{}, false, err
	}
	return id, at, true, nil
}

// UpdateProfile changes the username/display name; RunUserInfoMutation emits a
// user_info cache-invalidation event iff a cached field (the username) actually
// changed, so a display-name-only edit updates the row without an event and a
// missing id is a no-op with no event.
func (s *userStore) UpdateProfile(ctx context.Context, p store.UpdateUserProfileParams) error {
	return s.runUserInfoMutation(ctx, p.ID, func(ctx context.Context, conn *pgConn) (string, time.Time, bool, error) {
		row, err := conn.q.UpdateUserProfile(ctx, gendb.UpdateUserProfileParams{
			Username:    store.NormalizeUsername(p.Username),
			DisplayName: p.DisplayName,
			ID:          p.ID,
		})
		return updatedUserResult(row.ID, row.UpdatedAt.Time, row.UpdatedAt.Valid, err)
	})
}

func (s *userStore) UpdatePassword(ctx context.Context, p store.UpdateUserPasswordParams) error {
	return mapErr(s.conn.q.UpdateUserPassword(ctx, gendb.UpdateUserPasswordParams{
		PasswordHash: p.PasswordHash,
		ID:           p.ID,
	}))
}

// UpdateEmail changes the email and its verified flag; runUserInfoMutation emits
// a user_info cache-invalidation event iff a cached field (email/email_verified,
// an auth gate) actually changed. A missing id is a no-op with no event.
func (s *userStore) UpdateEmail(ctx context.Context, p store.UpdateUserEmailParams) error {
	return s.runUserInfoMutation(ctx, p.ID, func(ctx context.Context, conn *pgConn) (string, time.Time, bool, error) {
		row, err := conn.q.UpdateUserEmail(ctx, gendb.UpdateUserEmailParams{
			Email:         store.NormalizeEmail(p.Email),
			EmailVerified: p.EmailVerified,
			ID:            p.ID,
		})
		return updatedUserResult(row.ID, row.UpdatedAt.Time, row.UpdatedAt.Valid, err)
	})
}

// UpdateEmailVerified flips the email_verified auth gate; runUserInfoMutation
// emits a user_info cache-invalidation event iff the gate actually changed, so
// the change is observed cross-process without waiting out the cache TTL. A
// missing id is a no-op with no event.
func (s *userStore) UpdateEmailVerified(ctx context.Context, p store.UpdateUserEmailVerifiedParams) error {
	return s.runUserInfoMutation(ctx, p.ID, func(ctx context.Context, conn *pgConn) (string, time.Time, bool, error) {
		row, err := conn.q.UpdateUserEmailVerified(ctx, gendb.UpdateUserEmailVerifiedParams{
			EmailVerified: p.EmailVerified,
			ID:            p.ID,
		})
		return updatedUserResult(row.ID, row.UpdatedAt.Time, row.UpdatedAt.Valid, err)
	})
}

// UpdateAdmin flips the IsAdmin flag; runUserInfoMutation emits a user_info
// cache-invalidation event iff is_admin actually changed, dropping a stale
// cached UserInfo cross-process. A missing id is a no-op with no event.
func (s *userStore) UpdateAdmin(ctx context.Context, p store.UpdateUserAdminParams) error {
	return s.runUserInfoMutation(ctx, p.ID, func(ctx context.Context, conn *pgConn) (string, time.Time, bool, error) {
		row, err := conn.q.UpdateUserAdmin(ctx, gendb.UpdateUserAdminParams{
			IsAdmin: p.IsAdmin,
			ID:      p.ID,
		})
		return updatedUserResult(row.ID, row.UpdatedAt.Time, row.UpdatedAt.Valid, err)
	})
}

func (s *userStore) UpdatePrefs(ctx context.Context, p store.UpdateUserPrefsParams) error {
	return mapErr(s.conn.q.UpdateUserPrefs(ctx, gendb.UpdateUserPrefsParams{
		Prefs: p.Prefs,
		ID:    p.ID,
	}))
}

func (s *userStore) SetPendingEmail(ctx context.Context, p store.SetPendingEmailParams) error {
	return mapErr(s.conn.q.SetPendingEmail(ctx, gendb.SetPendingEmailParams{
		PendingEmail:          store.NormalizeEmail(p.PendingEmail),
		PendingEmailToken:     p.PendingEmailToken,
		PendingEmailExpiresAt: timePtrToTs(p.PendingEmailExpiresAt),
		ID:                    p.ID,
	}))
}

// PromotePendingEmail moves pending_email into email (email_verified=TRUE). A
// row with no pending email is a no-op; otherwise runUserInfoMutation emits a
// user_info cache-invalidation event iff the promotion changed a cached field
// (email/email_verified, an auth gate) -- the same guarantee its sibling
// UpdateEmail/UpdateEmailVerified give.
func (s *userStore) PromotePendingEmail(ctx context.Context, id string) error {
	return s.runUserInfoMutation(ctx, id, func(ctx context.Context, conn *pgConn) (string, time.Time, bool, error) {
		row, err := conn.q.PromotePendingEmail(ctx, id)
		return updatedUserResult(row.ID, row.UpdatedAt.Time, row.UpdatedAt.Valid, err)
	})
}

func (s *userStore) ClearPendingEmail(ctx context.Context, id string) error {
	return mapErr(s.conn.q.ClearPendingEmail(ctx, id))
}

func (s *userStore) ClearCompetingPendingEmails(ctx context.Context, p store.ClearCompetingPendingEmailsParams) error {
	return mapErr(s.conn.q.ClearCompetingPendingEmails(ctx, gendb.ClearCompetingPendingEmailsParams{
		PendingEmail: store.NormalizeEmail(p.PendingEmail),
		ID:           p.ExcludeID,
	}))
}

func (s *userStore) Delete(ctx context.Context, id string) error {
	return mapErr(s.conn.q.DeleteUser(ctx, id))
}

func (s *userStore) RevokeUserTokens(ctx context.Context, userID string) (int64, error) {
	return store.RunCredentialMutation(ctx, s.conn.withTransaction, func(ctx context.Context, conn *pgConn) (*store.CredentialEvent, error) {
		row, err := conn.q.BumpUserTokensRevokedAt(ctx, userID)
		if err != nil {
			mapped := mapErr(err)
			if errors.Is(mapped, store.ErrNotFound) {
				return nil, nil
			}
			return nil, mapped
		}
		revokedAt, err := sqlutil.RequireTime(row.TokensRevokedAt.Time, row.TokensRevokedAt.Valid, "tokens_revoked_at")
		if err != nil {
			return nil, err
		}
		return &store.CredentialEvent{Kind: store.RevocationEventKindUserTokens, SubjectID: row.ID, UserID: row.ID, At: revokedAt, UserAuthGeneration: row.AuthGeneration}, nil
	}, emitCredentialEvent)
}
