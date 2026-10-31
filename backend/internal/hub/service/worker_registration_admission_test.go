package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

func TestRegisterRefusesADeletedRegistrationKeyCreator(t *testing.T) {
	t.Parallel()
	env := setupRegKeyEnv(t)
	ctx := context.Background()
	owner := userid.MustNew(env.adminID(t))
	key := id.Generate()
	require.NoError(t, env.store.RegistrationKeys().Create(ctx, store.CreateRegistrationKeyParams{
		ID: key, CreatedBy: owner, ExpiresAt: time.Now().Add(time.Hour),
	}))
	require.NoError(t, env.store.Users().Delete(ctx, owner.String()))

	_, err := env.registerWithKey(t, key)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	ownerID := owner.String()
	page, err := env.store.Workers().ListAdmin(ctx, store.ListWorkersAdminParams{
		UserID: &ownerID, PageParams: store.PageParams{Limit: 10},
	})
	require.NoError(t, err)
	assert.Empty(t, page.Rows)
	remaining, err := env.store.RegistrationKeys().GetByID(ctx, key)
	require.NoError(t, err)
	assert.True(t, remaining.ExpiresAt.After(time.Now()), "a refused registration must not consume its key")
}

func TestRegisterLocksTheOwnerBeforeItConsumesCountsAndCreates(t *testing.T) {
	t.Parallel()
	var calls []string
	env := newRegKeyEnv(t, testConfig(), maxWorkersPerUser(1), func(st store.Store) store.Store {
		return admissionStore{Store: st, calls: &calls}
	})
	ctx := context.Background()
	owner := userid.MustNew(env.adminID(t))
	key := id.Generate()
	require.NoError(t, env.store.RegistrationKeys().Create(ctx, store.CreateRegistrationKeyParams{
		ID: key, CreatedBy: owner, ExpiresAt: time.Now().Add(time.Hour),
	}))

	response, err := env.registerWithKey(t, key)
	require.NoError(t, err)
	assert.Equal(t, []string{"lock", "consume", "count", "create"}, calls)
	worker, err := env.store.Workers().GetByID(ctx, response.GetWorkerId())
	require.NoError(t, err)
	assert.Equal(t, owner.String(), worker.RegisteredBy)
}

func TestRegisterRefusesAConsumedKeyWithAnotherCreator(t *testing.T) {
	t.Parallel()
	for _, creator := range []string{"", "another-owner"} {
		t.Run(map[bool]string{false: "different creator", true: "blank creator"}[creator == ""], func(t *testing.T) {
			env := newRegKeyEnv(t, testConfig(), maxWorkersPerUser(1), func(st store.Store) store.Store {
				return changedKeyCreatorStore{Store: st, creator: creator}
			})
			ctx := context.Background()
			owner := userid.MustNew(env.adminID(t))
			key := id.Generate()
			require.NoError(t, env.store.RegistrationKeys().Create(ctx, store.CreateRegistrationKeyParams{
				ID: key, CreatedBy: owner, ExpiresAt: time.Now().Add(time.Hour),
			}))
			_, err := env.registerWithKey(t, key)
			require.Error(t, err)
			assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
			page, err := env.store.Workers().ListByUserID(ctx, store.ListWorkersByUserIDParams{
				RegisteredBy: owner, PageParams: store.PageParams{Limit: 10},
			})
			require.NoError(t, err)
			assert.Empty(t, page.Rows)
			remaining, err := env.store.RegistrationKeys().GetByID(ctx, key)
			require.NoError(t, err)
			assert.True(t, remaining.ExpiresAt.After(time.Now()), "the refused transaction must restore its key")
		})
	}
}

type changedKeyCreatorStore struct {
	store.Store
	creator string
}

func (s changedKeyCreatorStore) RunInUserAuthTransaction(ctx context.Context, owner userid.UserID, fn func(store.Store) error) error {
	return s.Store.RunInUserAuthTransaction(ctx, owner, func(tx store.Store) error {
		s.Store = tx
		return fn(s)
	})
}

func (s changedKeyCreatorStore) RegistrationKeys() store.RegistrationKeyStore {
	return changedKeyCreators{RegistrationKeyStore: s.Store.RegistrationKeys(), creator: s.creator}
}

type changedKeyCreators struct {
	store.RegistrationKeyStore
	creator string
}

func (s changedKeyCreators) Consume(ctx context.Context, key string) (*store.WorkerRegistrationKey, error) {
	row, err := s.RegistrationKeyStore.Consume(ctx, key)
	if err != nil {
		return nil, err
	}
	changed := *row
	changed.CreatedBy = s.creator
	return &changed, nil
}

// admissionStore rejects admission operations outside the live owner's lock.
// Its delegates still perform the real writes and enforce key consumption.
type admissionStore struct {
	store.Store
	calls     *[]string
	protected bool
	owner     userid.UserID
}

func (s admissionStore) RunInTransaction(ctx context.Context, fn func(store.Store) error) error {
	return s.Store.RunInTransaction(ctx, func(tx store.Store) error {
		s.Store = tx
		return fn(s)
	})
}

func (s admissionStore) RunInUserAuthTransaction(ctx context.Context, owner userid.UserID, fn func(store.Store) error) error {
	return s.Store.RunInUserAuthTransaction(ctx, owner, func(tx store.Store) error {
		// The store can retry the callback after a conflict. Inspect the
		// successful attempt rather than accumulating rolled-back calls.
		*s.calls = []string{"lock"}
		s.Store, s.protected, s.owner = tx, true, owner
		return fn(s)
	})
}

func (s admissionStore) RegistrationKeys() store.RegistrationKeyStore {
	return admissionKeys{RegistrationKeyStore: s.Store.RegistrationKeys(), admission: s}
}

func (s admissionStore) Workers() store.WorkerStore {
	return admissionWorkers{WorkerStore: s.Store.Workers(), admission: s}
}

type admissionKeys struct {
	store.RegistrationKeyStore
	admission admissionStore
}

func (s admissionKeys) Consume(ctx context.Context, key string) (*store.WorkerRegistrationKey, error) {
	if !s.admission.protected {
		return nil, errors.New("registration key consumption requires the live owner's lock")
	}
	*s.admission.calls = append(*s.admission.calls, "consume")
	return s.RegistrationKeyStore.Consume(ctx, key)
}

type admissionWorkers struct {
	store.WorkerStore
	admission admissionStore
}

func (s admissionWorkers) ListByUserID(ctx context.Context, p store.ListWorkersByUserIDParams) (store.Page[store.Worker], error) {
	if !s.admission.protected || !s.admission.owner.Matches(p.RegisteredBy.String()) {
		return store.Page[store.Worker]{}, errors.New("worker admission count requires the owner's lock")
	}
	*s.admission.calls = append(*s.admission.calls, "count")
	return s.WorkerStore.ListByUserID(ctx, p)
}

func (s admissionWorkers) Create(ctx context.Context, p store.CreateWorkerParams) error {
	if !s.admission.protected || !s.admission.owner.Matches(p.RegisteredBy.String()) {
		return errors.New("worker admission write requires the owner's lock")
	}
	*s.admission.calls = append(*s.admission.calls, "create")
	return s.WorkerStore.Create(ctx, p)
}
