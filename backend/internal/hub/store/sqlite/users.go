package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/util/userid"
)

type userStore struct {
	conn *sqliteConn
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
		EmailVerified:         ptrconv.Int64ToBool(u.EmailVerified),
		PendingEmail:          u.PendingEmail,
		PendingEmailToken:     u.PendingEmailToken,
		PendingEmailExpiresAt: u.PendingEmailExpiresAt.Ptr(),
		PendingEmailAttempts:  u.PendingEmailAttempts,
		PasswordSet:           ptrconv.Int64ToBool(u.PasswordSet),
		IsAdmin:               ptrconv.Int64ToBool(u.IsAdmin),
		Prefs:                 u.Prefs,
		CreatedAt:             u.CreatedAt.Time,
		UpdatedAt:             u.UpdatedAt.Time,
		TokensRevokedAt:       u.TokensRevokedAt.Ptr(),
		AuthGeneration:        u.AuthGeneration,
		DeletedAt:             u.DeletedAt.Ptr(),
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
		EmailVerified:     ptrconv.BoolToInt64(p.EmailVerified),
		PasswordSet:       ptrconv.BoolToInt64(p.PasswordSet),
		IsAdmin:           ptrconv.BoolToInt64(p.IsAdmin),
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
	v, err := s.conn.q.ExistsByUsername(ctx, store.NormalizeUsername(username))
	if err != nil {
		return false, mapErr(err)
	}
	return v, nil
}

func (s *userStore) ExistsByEmail(ctx context.Context, email, excludeUserID string) (bool, error) {
	v, err := s.conn.q.ExistsByEmail(ctx, gendb.ExistsByEmailParams{
		Email:         store.NormalizeEmail(email),
		ExcludeUserID: excludeUserID,
	})
	if err != nil {
		return false, mapErr(err)
	}
	return v, nil
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
	n, err := s.conn.q.HasAnyUser(ctx)
	if err != nil {
		return false, mapErr(err)
	}
	return n, nil
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
func loadUserInfoCacheFields(ctx context.Context, conn *sqliteConn, id string) (store.UserInfoCacheFields, bool, error) {
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
// projection read and user_info event insert, so each Update* method supplies
// only its UPDATE and the durable cache-invalidation is derived from the
// before/after projection rather than a hand-computed flag.
func (s *userStore) runUserInfoMutation(ctx context.Context, id string, mutate func(ctx context.Context, conn *sqliteConn) (userID string, updatedAt time.Time, ok bool, err error)) error {
	// Lock the user row before the before-read so a concurrent same-user mutation
	// cannot commit between the before and after cached-field projections and hide
	// a change (which would drop the durable user_info invalidation for that field).
	lockThenTx := func(ctx context.Context, fn func(*sqliteConn) error) error {
		return s.conn.withTransaction(ctx, func(conn *sqliteConn) error {
			if err := conn.q.LockUserRow(ctx, id); err != nil {
				return mapErr(err)
			}
			return fn(conn)
		})
	}
	return store.RunUserInfoMutation(ctx, lockThenTx,
		func(ctx context.Context, conn *sqliteConn) (store.UserInfoCacheFields, bool, error) {
			return loadUserInfoCacheFields(ctx, conn, id)
		},
		mutate,
		func(ctx context.Context, conn *sqliteConn, userID string, updatedAt time.Time) error {
			return insertRevocationEvent(ctx, conn, store.RevocationEventKindUserInfo, userID, userID, updatedAt, 0)
		},
	)
}

// updatedUserResult maps a RETURNING (id, updated_at) row plus error into the
// (userID, updatedAt, changed, err) tuple runUserInfoMutation expects: a no-rows
// result (no row updated) is a silent no-op (changed=false, nil err), any other
// error propagates mapped, and a live row yields changed=true. Shared by every
// cached-field Update* mutate closure so the not-found handling lives in one
// place instead of five identical tails.
func updatedUserResult(id string, updatedAt time.Time, err error) (string, time.Time, bool, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, false, nil
	}
	if err != nil {
		return "", time.Time{}, false, mapErr(err)
	}
	return id, updatedAt, true, nil
}

// UpdateProfile changes the username/display name; RunUserInfoMutation emits a
// user_info cache-invalidation event iff a cached field (the username) actually
// changed, so a display-name-only edit updates the row without an event and a
// missing id is a no-op with no event.
func (s *userStore) UpdateProfile(ctx context.Context, p store.UpdateUserProfileParams) error {
	if err := p.Validate(); err != nil {
		return err
	}
	return s.runUserInfoMutation(ctx, p.ID, func(ctx context.Context, conn *sqliteConn) (string, time.Time, bool, error) {
		row, err := conn.q.UpdateUserProfile(ctx, gendb.UpdateUserProfileParams{
			Username:          store.NormalizeUsername(p.Username),
			DisplayName:       p.DisplayName,
			DisplayNameFolded: store.FoldSearchText(p.DisplayName),
			ID:                p.ID,
		})
		if err != nil {
			return updatedUserResult("", time.Time{}, err)
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
			return "", time.Time{}, false, err
		}
		return updatedUserResult(row.ID, row.UpdatedAt.Time, nil)
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
	return s.runUserInfoMutation(ctx, p.ID, func(ctx context.Context, conn *sqliteConn) (string, time.Time, bool, error) {
		row, err := conn.q.UpdateUserEmail(ctx, gendb.UpdateUserEmailParams{
			Email:         store.NormalizeEmail(p.Email),
			EmailVerified: ptrconv.BoolToInt64(p.EmailVerified),
			ID:            p.ID,
		})
		return updatedUserResult(row.ID, row.UpdatedAt.Time, err)
	})
}

// UpdateEmailVerified flips the email_verified auth gate; runUserInfoMutation
// emits a user_info cache-invalidation event iff the gate actually changed, so
// the change is observed cross-process without waiting out the cache TTL. A
// missing id is a no-op with no event.
func (s *userStore) UpdateEmailVerified(ctx context.Context, p store.UpdateUserEmailVerifiedParams) error {
	return s.runUserInfoMutation(ctx, p.ID, func(ctx context.Context, conn *sqliteConn) (string, time.Time, bool, error) {
		row, err := conn.q.UpdateUserEmailVerified(ctx, gendb.UpdateUserEmailVerifiedParams{
			EmailVerified: ptrconv.BoolToInt64(p.EmailVerified),
			ID:            p.ID,
		})
		return updatedUserResult(row.ID, row.UpdatedAt.Time, err)
	})
}

// UpdateAdmin flips the IsAdmin flag; runUserInfoMutation emits a user_info
// cache-invalidation event iff is_admin actually changed, dropping a stale
// cached UserInfo cross-process. A missing id is a no-op with no event.
func (s *userStore) UpdateAdmin(ctx context.Context, p store.UpdateUserAdminParams) error {
	return s.runUserInfoMutation(ctx, p.ID, func(ctx context.Context, conn *sqliteConn) (string, time.Time, bool, error) {
		row, err := conn.q.UpdateUserAdmin(ctx, gendb.UpdateUserAdminParams{
			IsAdmin: ptrconv.BoolToInt64(p.IsAdmin),
			ID:      p.ID,
		})
		return updatedUserResult(row.ID, row.UpdatedAt.Time, err)
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
		PendingEmailExpiresAt: sqltime.NewSQLiteNullTime(p.PendingEmailExpiresAt),
		ID:                    p.ID,
	}))
}

// PromotePendingEmail moves pending_email into email (email_verified=1). A row
// with no pending email is a no-op; otherwise runUserInfoMutation emits a
// user_info cache-invalidation event iff the promotion changed a cached field
// (email/email_verified, an auth gate) -- the same guarantee its sibling
// UpdateEmail/UpdateEmailVerified give.
func (s *userStore) PromotePendingEmail(ctx context.Context, id string) error {
	return s.runUserInfoMutation(ctx, id, func(ctx context.Context, conn *sqliteConn) (string, time.Time, bool, error) {
		row, err := conn.q.PromotePendingEmail(ctx, id)
		return updatedUserResult(row.ID, row.UpdatedAt.Time, err)
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
		func(ctx context.Context, conn *sqliteConn) error {
			return mapErr(conn.q.SoftDeleteUserPersonalOrg(ctx, id))
		},
		func(ctx context.Context, conn *sqliteConn) error {
			return mapErr(conn.q.DeleteUser(ctx, id))
		},
	)
}

func (s *userStore) RevokeUserTokens(ctx context.Context, userID userid.UserID) (int64, error) {
	owner, ok := store.OwnerFilter(userID)
	if !ok {
		// An unminted caller names no user, so a bulk mutation must refuse
		// rather than address every blank-owner row -- or report success
		// having changed nothing. See store.OwnerFilter.
		return 0, store.ErrInvalidArgument
	}
	return store.RunCredentialMutation(ctx, s.conn.withTransaction, func(ctx context.Context, conn *sqliteConn) (*store.CredentialEvent, error) {
		row, err := conn.q.BumpUserTokensRevokedAt(ctx, owner)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, mapErr(err)
		}
		revokedAt, err := sqlutil.RequireTime(row.TokensRevokedAt.Time, row.TokensRevokedAt.Valid, "tokens_revoked_at")
		if err != nil {
			return nil, err
		}
		return &store.CredentialEvent{Kind: store.RevocationEventKindUserTokens, SubjectID: row.ID, UserID: row.ID, At: revokedAt, UserAuthGeneration: row.AuthGeneration}, nil
	}, emitCredentialEvent)
}
