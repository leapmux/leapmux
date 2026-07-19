package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

type userStore struct {
	conn *mysqlConn
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
		PendingEmailExpiresAt: ptrconv.NullTimeToPtr(u.PendingEmailExpiresAt),
		PendingEmailAttempts:  int64(u.PendingEmailAttempts),
		PasswordSet:           u.PasswordSet,
		IsAdmin:               u.IsAdmin,
		Prefs:                 u.Prefs,
		CreatedAt:             u.CreatedAt,
		UpdatedAt:             u.UpdatedAt,
		TokensRevokedAt:       ptrconv.NullTimeToPtr(u.TokensRevokedAt),
		AuthGeneration:        u.AuthGeneration,
		DeletedAt:             ptrconv.NullTimeToPtr(u.DeletedAt),
	}
}

func (s *userStore) Create(ctx context.Context, p store.CreateUserParams) error {
	if err := p.Validate(); err != nil {
		return err
	}
	return mapErr(s.conn.q.CreateUser(ctx, gendb.CreateUserParams{
		ID:                p.ID,
		OrgID:             p.OrgID,
		Username:          store.NormalizeUsername(p.Username),
		PasswordHash:      p.PasswordHash,
		DisplayName:       p.DisplayName,
		DisplayNameFolded: store.FoldSearchText(p.DisplayName),
		Email:             store.NormalizeEmail(p.Email),
		EmailVerified:     p.EmailVerified,
		PasswordSet:       p.PasswordSet,
		IsAdmin:           p.IsAdmin,
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

// ConsumeVerificationAttempt keeps MySQL's UPDATE and follow-up SELECT in one
// transaction so the row lock protects the exact post-increment state returned
// to this caller. PostgreSQL and SQLite use UPDATE ... RETURNING instead.
func (s *userStore) ConsumeVerificationAttempt(ctx context.Context, id string) (*store.User, error) {
	var out *store.User
	err := s.conn.withTransaction(ctx, func(conn *mysqlConn) error {
		res, err := conn.q.ConsumeVerificationAttempt(ctx, id)
		if err != nil {
			return mapErr(err)
		}
		rows, err := res.RowsAffected()
		if err != nil {
			return mapErr(err)
		}
		if rows == 0 {
			return store.ErrNotFound
		}
		u, err := conn.q.GetUserByID(ctx, id)
		if err != nil {
			return mapErr(err)
		}
		converted := fromDBUser(u)
		out = &converted
		return nil
	})
	return out, err
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

func (s *userStore) ListAll(ctx context.Context, p store.ListAllUsersParams) (store.Page[store.User], error) {
	return queryPage(ctx, p.Limit,
		func() (gendb.ListAllUsersParams, error) { return listAllUsersParams(p.Cursor, p.Limit) },
		s.conn.q.ListAllUsers, fromDBUser)
}

func (s *userStore) Search(ctx context.Context, p store.SearchUsersParams) (store.Page[store.User], error) {
	return queryPage(ctx, p.Limit,
		func() (gendb.SearchUsersParams, error) { return searchUsersParams(p.Query, p.Cursor, p.Limit) },
		s.conn.q.SearchUsers, fromDBUser)
}

// loadUserInfoCacheFields reads the current cached-UserInfo projection for id.
// RunUserInfoMutation calls it before and after a mutation to derive whether a
// user_info event fires; a missing row reports exists=false.
func loadUserInfoCacheFields(ctx context.Context, conn *mysqlConn, id string) (store.UserInfoCacheFields, bool, error) {
	u, err := conn.q.GetUserByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return store.UserInfoCacheFields{}, false, nil
	}
	if err != nil {
		return store.UserInfoCacheFields{}, false, mapErr(err)
	}
	return store.UserInfoCacheFieldsOf(fromDBUser(u)), true, nil
}

// runUserInfoMutation wires the shared RunUserInfoMutation to this dialect's
// projection read, locked clock reading, and user_info event insert, so the
// durable cache-invalidation is derived from the before/after cached-field
// projection rather than a hand-computed flag. MySQL has no RETURNING, so it
// locks the row FOR UPDATE to fix a single clock reading (shared by the UPDATE
// and the emitted event) before running update; a missing id is a no-op. update
// runs against the locked row and reports ok=false to force a no-op (e.g. a
// promote that matched no pending email).
func (s *userStore) runUserInfoMutation(ctx context.Context, id string, update func(ctx context.Context, conn *mysqlConn, updatedAt time.Time) (ok bool, err error)) error {
	// Lock the user row before the before-read so a concurrent same-user mutation
	// cannot commit between the before and after cached-field projections and hide
	// a change (which would drop the durable user_info invalidation for that field).
	lockThenTx := func(ctx context.Context, fn func(*mysqlConn) error) error {
		return s.conn.withTransaction(ctx, func(conn *mysqlConn) error {
			if err := conn.q.LockUserRow(ctx, id); err != nil {
				return mapErr(err)
			}
			return fn(conn)
		})
	}
	return store.RunUserInfoMutation(ctx, lockThenTx,
		func(ctx context.Context, conn *mysqlConn) (store.UserInfoCacheFields, bool, error) {
			return loadUserInfoCacheFields(ctx, conn, id)
		},
		func(ctx context.Context, conn *mysqlConn) (string, time.Time, bool, error) {
			row, err := conn.q.GetUserForUpdate(ctx, id)
			if errors.Is(err, sql.ErrNoRows) {
				return "", time.Time{}, false, nil
			}
			if err != nil {
				return "", time.Time{}, false, mapErr(err)
			}
			updatedAt := row.NowAt.UTC()
			ok, err := update(ctx, conn, updatedAt)
			if err != nil || !ok {
				return "", time.Time{}, false, err
			}
			return row.ID, updatedAt, true, nil
		},
		func(ctx context.Context, conn *mysqlConn, userID string, updatedAt time.Time) error {
			return insertRevocationEvent(ctx, conn, store.RevocationEventKindUserInfo, userID, userID, updatedAt, 0)
		},
	)
}

// requireSingleRowUpdate maps a rowsAffected result into the (changed, err) pair
// a cached-field mutate closure returns: it propagates a store error and treats
// anything but exactly one matched row as corruption (the row was locked live
// moments earlier under the same tx), so a cached-field Update* can never
// silently skip its invalidation event. action/id build the error message. The
// n==0-is-a-no-op case (PromotePendingEmail) keeps its own body.
func requireSingleRowUpdate(n int64, err error, action, id string) (bool, error) {
	if err != nil {
		return false, err
	}
	if n != 1 {
		return false, fmt.Errorf("%s %q: updated %d rows after locking live row", action, id, n)
	}
	return true, nil
}

// UpdateProfile changes the username/display name; RunUserInfoMutation emits a
// user_info cache-invalidation event iff a cached field (the username) actually
// changed, so a display-name-only edit updates the row without an event and a
// missing id is a no-op with no event.
func (s *userStore) UpdateProfile(ctx context.Context, p store.UpdateUserProfileParams) error {
	if err := p.Validate(); err != nil {
		return err
	}
	return s.runUserInfoMutation(ctx, p.ID, func(ctx context.Context, conn *mysqlConn, updatedAt time.Time) (bool, error) {
		n, err := rowsAffected(conn.q.UpdateUserProfile(ctx, gendb.UpdateUserProfileParams{
			Username:          store.NormalizeUsername(p.Username),
			DisplayName:       p.DisplayName,
			DisplayNameFolded: store.FoldSearchText(p.DisplayName),
			UpdatedAt:         updatedAt,
			ID:                p.ID,
		}))
		if ok, err := requireSingleRowUpdate(n, err, "update user profile", p.ID); !ok || err != nil {
			return ok, err
		}
		// Pair the username with a personal-org rename in the same transaction so
		// the org name (and /o/ slug) can never go stale -- mirroring the DeleteUser
		// + SoftDeleteUserPersonalOrg pairing in Delete. Idempotent for a
		// display-name-only edit (see RenameUserPersonalOrg's query doc), so it
		// runs unconditionally rather than only when the username changed.
		if err := mapErr(conn.q.RenameUserPersonalOrg(ctx, gendb.RenameUserPersonalOrgParams{
			OrgName: store.NormalizeUsername(p.Username),
			UserID:  p.ID,
		})); err != nil {
			return false, err
		}
		return true, nil
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
	return s.runUserInfoMutation(ctx, p.ID, func(ctx context.Context, conn *mysqlConn, updatedAt time.Time) (bool, error) {
		n, err := rowsAffected(conn.q.UpdateUserEmail(ctx, gendb.UpdateUserEmailParams{
			Email:         store.NormalizeEmail(p.Email),
			EmailVerified: p.EmailVerified,
			UpdatedAt:     updatedAt,
			ID:            p.ID,
		}))
		return requireSingleRowUpdate(n, err, "update user email", p.ID)
	})
}

// UpdateEmailVerified flips the email_verified auth gate; runUserInfoMutation
// emits a user_info cache-invalidation event iff the gate actually changed, so
// the change is observed cross-process without waiting out the cache TTL. A
// missing id is a no-op with no event.
func (s *userStore) UpdateEmailVerified(ctx context.Context, p store.UpdateUserEmailVerifiedParams) error {
	return s.runUserInfoMutation(ctx, p.ID, func(ctx context.Context, conn *mysqlConn, updatedAt time.Time) (bool, error) {
		n, err := rowsAffected(conn.q.UpdateUserEmailVerified(ctx, gendb.UpdateUserEmailVerifiedParams{
			EmailVerified: p.EmailVerified,
			UpdatedAt:     updatedAt,
			ID:            p.ID,
		}))
		return requireSingleRowUpdate(n, err, "update user email verified", p.ID)
	})
}

// UpdateAdmin flips the IsAdmin flag; runUserInfoMutation emits a user_info
// cache-invalidation event iff is_admin actually changed, dropping a stale
// cached UserInfo cross-process. A missing id is a no-op with no event.
func (s *userStore) UpdateAdmin(ctx context.Context, p store.UpdateUserAdminParams) error {
	return s.runUserInfoMutation(ctx, p.ID, func(ctx context.Context, conn *mysqlConn, updatedAt time.Time) (bool, error) {
		n, err := rowsAffected(conn.q.UpdateUserAdmin(ctx, gendb.UpdateUserAdminParams{
			IsAdmin:   p.IsAdmin,
			UpdatedAt: updatedAt,
			ID:        p.ID,
		}))
		return requireSingleRowUpdate(n, err, "update user admin", p.ID)
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
		PendingEmailExpiresAt: ptrconv.PtrToNullTime(p.PendingEmailExpiresAt),
		ID:                    p.ID,
	}))
}

// PromotePendingEmail moves pending_email into email (email_verified=1). A row
// with no pending email matches zero rows and is a no-op; otherwise
// runUserInfoMutation emits a user_info cache-invalidation event iff the
// promotion changed a cached field (email/email_verified, an auth gate) -- the
// same guarantee its sibling UpdateEmail/UpdateEmailVerified give.
func (s *userStore) PromotePendingEmail(ctx context.Context, id string) error {
	return s.runUserInfoMutation(ctx, id, func(ctx context.Context, conn *mysqlConn, updatedAt time.Time) (bool, error) {
		n, err := rowsAffected(conn.q.PromotePendingEmail(ctx, gendb.PromotePendingEmailParams{
			UpdatedAt: updatedAt,
			ID:        id,
		}))
		if err != nil {
			return false, err
		}
		// A row with no pending email matches zero rows -- a no-op with no event
		// (unlike the Update* siblings, which treat n != 1 as an error).
		if n == 0 {
			return false, nil
		}
		return true, nil
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
	// The personal-org soft-delete is paired with the user delete in one
	// transaction (rationale in store.DeleteUserWithPersonalOrg).
	return store.DeleteUserWithPersonalOrg(ctx, s.conn.withTransaction,
		func(ctx context.Context, conn *mysqlConn) error {
			return mapErr(conn.q.SoftDeleteUserPersonalOrg(ctx, id))
		},
		func(ctx context.Context, conn *mysqlConn) error {
			return mapErr(conn.q.DeleteUser(ctx, id))
		},
	)
}

func (s *userStore) RevokeUserTokens(ctx context.Context, userID string) (int64, error) {
	return store.RunCredentialMutation(ctx, s.conn.withTransaction, func(ctx context.Context, conn *mysqlConn) (*store.CredentialEvent, error) {
		row, err := conn.q.GetUserTokensRevocationForUpdate(ctx, userID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, mapErr(err)
		}
		updatedAt := row.NowAt.UTC()
		revokedAt := updatedAt
		nextGeneration := row.AuthGeneration + 1
		n, err := rowsAffected(conn.q.SetUserTokensRevokedAt(ctx, gendb.SetUserTokensRevokedAtParams{
			ID:              row.ID,
			TokensRevokedAt: sql.NullTime{Time: revokedAt, Valid: true},
			AuthGeneration:  nextGeneration,
			UpdatedAt:       updatedAt,
		}))
		if err != nil {
			return nil, err
		}
		if n != 1 {
			return nil, fmt.Errorf("bump user token revocation %q: updated %d rows after locking live row", row.ID, n)
		}
		return &store.CredentialEvent{Kind: store.RevocationEventKindUserTokens, SubjectID: row.ID, UserID: row.ID, At: revokedAt, UserAuthGeneration: nextGeneration}, nil
	}, emitCredentialEvent)
}
