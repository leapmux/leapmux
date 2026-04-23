package postgres

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
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
		PasswordSet:           u.PasswordSet,
		IsAdmin:               u.IsAdmin,
		Prefs:                 u.Prefs,
		CreatedAt:             tsToTime(u.CreatedAt),
		UpdatedAt:             tsToTime(u.UpdatedAt),
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

func (s *userStore) GetByPendingEmailToken(ctx context.Context, token string) (*store.User, error) {
	u, err := s.conn.q.GetUserByPendingEmailToken(ctx, token)
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

func (s *userStore) UpdateProfile(ctx context.Context, p store.UpdateUserProfileParams) error {
	return mapErr(s.conn.q.UpdateUserProfile(ctx, gendb.UpdateUserProfileParams{
		Username:    store.NormalizeUsername(p.Username),
		DisplayName: p.DisplayName,
		ID:          p.ID,
	}))
}

func (s *userStore) UpdatePassword(ctx context.Context, p store.UpdateUserPasswordParams) error {
	return mapErr(s.conn.q.UpdateUserPassword(ctx, gendb.UpdateUserPasswordParams{
		PasswordHash: p.PasswordHash,
		ID:           p.ID,
	}))
}

func (s *userStore) UpdateEmail(ctx context.Context, p store.UpdateUserEmailParams) error {
	return mapErr(s.conn.q.UpdateUserEmail(ctx, gendb.UpdateUserEmailParams{
		Email:         store.NormalizeEmail(p.Email),
		EmailVerified: p.EmailVerified,
		ID:            p.ID,
	}))
}

func (s *userStore) UpdateEmailVerified(ctx context.Context, p store.UpdateUserEmailVerifiedParams) error {
	return mapErr(s.conn.q.UpdateUserEmailVerified(ctx, gendb.UpdateUserEmailVerifiedParams{
		EmailVerified: p.EmailVerified,
		ID:            p.ID,
	}))
}

func (s *userStore) UpdateAdmin(ctx context.Context, p store.UpdateUserAdminParams) error {
	return mapErr(s.conn.q.UpdateUserAdmin(ctx, gendb.UpdateUserAdminParams{
		IsAdmin: p.IsAdmin,
		ID:      p.ID,
	}))
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

func (s *userStore) PromotePendingEmail(ctx context.Context, id string) error {
	return mapErr(s.conn.q.PromotePendingEmail(ctx, id))
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
