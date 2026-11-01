package service_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
)

func newSelectorStore(t *testing.T) store.Store {
	t.Helper()
	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.Migrator().Migrate(context.Background()))
	return st
}

func newSelectorUser(t *testing.T, st store.Store, username string) *store.User {
	t.Helper()
	hash, err := password.Hash("testpass123")
	require.NoError(t, err)
	user, err := service.CreateUser(context.Background(), st, service.CreateUserParams{
		Username: username, PasswordHash: hash, DisplayName: username, FirstCredentialExempt: true,
	})
	require.NoError(t, err)
	return user
}

// lookupRecordingStore counts every user lookup a selector can trigger, so a
// test can assert that a refused selector reaches no store call.
type lookupRecordingStore struct {
	store.Store
	lookups atomic.Int64
}

func (s *lookupRecordingStore) Users() store.UserStore {
	return &lookupRecordingUsers{UserStore: s.Store.Users(), lookups: &s.lookups}
}

type lookupRecordingUsers struct {
	store.UserStore
	lookups *atomic.Int64
}

func (u *lookupRecordingUsers) GetByID(ctx context.Context, id string) (*store.User, error) {
	u.lookups.Add(1)
	return u.UserStore.GetByID(ctx, id)
}

func (u *lookupRecordingUsers) GetByIDIncludeDeleted(ctx context.Context, id string) (*store.User, error) {
	u.lookups.Add(1)
	return u.UserStore.GetByIDIncludeDeleted(ctx, id)
}

func (u *lookupRecordingUsers) GetByUsername(ctx context.Context, username string) (*store.User, error) {
	u.lookups.Add(1)
	return u.UserStore.GetByUsername(ctx, username)
}

// selectorFixture holds one live user and one soft-deleted user, so each
// column can show which of them an id and a username reach.
type selectorFixture struct {
	st       *lookupRecordingStore
	live     *store.User
	deleted  *store.User
	delUname string
}

func newSelectorFixture(t *testing.T) selectorFixture {
	t.Helper()
	st := newSelectorStore(t)
	live := newSelectorUser(t, st, "alice")
	deleted := newSelectorUser(t, st, "gone")
	require.NoError(t, st.Users().Delete(context.Background(), deleted.ID))
	return selectorFixture{st: &lookupRecordingStore{Store: st}, live: live, deleted: deleted, delUname: "gone"}
}

// TestResolveUserSelector covers every input of a request that addresses ONE
// user. ResolveUserSelector holds that rule for both the online admin RPCs
// and the offline `leapmux recover` verbs, and returns each violation as a
// sentinel so each surface can word it for its own audience.
func TestResolveUserSelector(t *testing.T) {
	f := newSelectorFixture(t)

	for _, tc := range []struct {
		name    string
		sel     *leapmuxv1.UserSelector
		wantID  string
		wantErr error
		lookups int64
	}{
		{"an absent selector is refused", nil, "", service.ErrNoUserSelector, 0},
		{"a selector with no case is refused", &leapmuxv1.UserSelector{}, "", service.ErrEmptyUserSelector, 0},
		{"an empty id is refused", service.SelectUserByID(""), "", service.ErrEmptyUserSelector, 0},
		{"an empty username is refused", service.SelectUserByUsername(""), "", service.ErrEmptyUserSelector, 0},
		{"an id resolves its live user", service.SelectUserByID(f.live.ID), f.live.ID, nil, 1},
		{"a username resolves its live user", service.SelectUserByUsername("alice"), f.live.ID, nil, 1},
		{"an unknown id stays store.ErrNotFound", service.SelectUserByID("00000000000000000000000000"), "", store.ErrNotFound, 1},
		{"an unknown username stays store.ErrNotFound", service.SelectUserByUsername("nobody"), "", store.ErrNotFound, 1},
		// One user is addressed only while live, by either handle.
		{"a soft-deleted user's id is not found", service.SelectUserByID(f.deleted.ID), "", store.ErrNotFound, 1},
		{"a soft-deleted user's username is not found", service.SelectUserByUsername(f.delUname), "", store.ErrNotFound, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.st.lookups.Store(0)
			user, err := service.ResolveUserSelector(context.Background(), f.st, tc.sel)
			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, user)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.wantID, user.ID)
			}
			assert.Equal(t, tc.lookups, f.st.lookups.Load(), "a refused selector must reach no lookup")
		})
	}
}

// TestResolveUserSelectorFilter covers every input of a listing's OPTIONAL
// owner filter. Only an absent selector means "every user": a present one that
// selects nothing is refused, so a malformed filter never widens a listing.
func TestResolveUserSelectorFilter(t *testing.T) {
	f := newSelectorFixture(t)

	for _, tc := range []struct {
		name    string
		sel     *leapmuxv1.UserSelector
		wantID  *string
		wantErr error
		lookups int64
	}{
		{"an absent selector is no filter", nil, nil, nil, 0},
		{"a selector with no case is refused", &leapmuxv1.UserSelector{}, nil, service.ErrEmptyUserSelector, 0},
		{"an empty id is refused", service.SelectUserByID(""), nil, service.ErrEmptyUserSelector, 0},
		{"an empty username is refused", service.SelectUserByUsername(""), nil, service.ErrEmptyUserSelector, 0},
		{"an id filters to its live user", service.SelectUserByID(f.live.ID), &f.live.ID, nil, 1},
		{"a username filters to its live user", service.SelectUserByUsername("alice"), &f.live.ID, nil, 1},
		// An admin listing surfaces a soft-deleted owner's rows for audit, so
		// the id path finds that owner. A username is freed on soft-delete, so
		// the name path does not.
		{"an id also finds a soft-deleted user", service.SelectUserByID(f.deleted.ID), &f.deleted.ID, nil, 1},
		{"a soft-deleted user's username is not found", service.SelectUserByUsername(f.delUname), nil, store.ErrNotFound, 1},
		{"an unknown id stays store.ErrNotFound", service.SelectUserByID("00000000000000000000000000"), nil, store.ErrNotFound, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.st.lookups.Store(0)
			id, err := service.ResolveUserSelectorFilter(context.Background(), f.st, tc.sel)
			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tc.wantID, id)
			assert.Equal(t, tc.lookups, f.st.lookups.Load(), "a refused selector must reach no lookup")
		})
	}
}
