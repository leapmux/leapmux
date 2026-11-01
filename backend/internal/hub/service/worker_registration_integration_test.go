//go:build integration

package service_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/store"
	mysqlstore "github.com/leapmux/leapmux/internal/hub/store/mysql"
	"github.com/leapmux/leapmux/internal/hub/store/postgres"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

func TestRegisterConcurrentKeysRespectTheOwnerMaximum(t *testing.T) {
	for _, backend := range []string{"postgres", "mysql"} {
		t.Run(backend, func(t *testing.T) {
			st := registrationStore(t, backend)
			coordinator := &registrationOverlap{
				firstInserted: make(chan struct{}), secondReady: make(chan struct{}), release: make(chan struct{}),
			}
			env := regKeyEnvFromStore(t, st, testConfig(), maxWorkersPerUser(1), func(st store.Store) store.Store {
				return registrationOverlapStore{Store: st, coordinator: coordinator}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			owner := userid.MustNew(env.adminID(t))
			keys := []string{id.Generate(), id.Generate()}
			for _, key := range keys {
				require.NoError(t, st.RegistrationKeys().Create(ctx, store.CreateRegistrationKeyParams{
					ID: key, CreatedBy: owner, ExpiresAt: time.Now().Add(time.Hour),
				}))
			}
			type result struct {
				response *leapmuxv1.RegisterResponse
				err      error
			}
			register := func(key string, done chan<- result) {
				requestCtx, info := connect.NewClientContext(ctx)
				info.RequestHeader().Set("Authorization", "Bearer "+key)
				response, err := env.connectorClient.Register(requestCtx, &leapmuxv1.RegisterRequest{})
				done <- result{response: response, err: err}
			}
			firstDone, secondDone := make(chan result, 1), make(chan result, 1)
			go register(keys[0], firstDone)
			select {
			case <-coordinator.firstInserted:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			go register(keys[1], secondDone)
			select {
			case <-coordinator.secondReady:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			close(coordinator.release)
			first, second := <-firstDone, <-secondDone
			require.NoError(t, first.err)
			require.Error(t, second.err)
			assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(second.err))
			page, err := st.Workers().ListByUserID(ctx, store.ListWorkersByUserIDParams{
				RegisteredBy: owner, PageParams: store.PageParams{Limit: 10},
			})
			require.NoError(t, err)
			require.Len(t, page.Rows, 1)
			assert.Equal(t, first.response.GetWorkerId(), page.Rows[0].ID)
			remaining, err := st.RegistrationKeys().GetByID(ctx, keys[1])
			require.NoError(t, err)
			assert.True(t, remaining.ExpiresAt.After(time.Now()))
			require.NoError(t, st.Workers().MarkDeleted(ctx, first.response.GetWorkerId()))
			_, err = env.registerWithKey(t, keys[1])
			assert.NoError(t, err, "the refused key remains usable after a slot becomes available")
		})
	}
}

func registrationStore(t *testing.T, backend string) store.TestableStore {
	t.Helper()
	var st store.TestableStore
	var err error
	if backend == "postgres" {
		dsn := func(host, port string) string {
			return fmt.Sprintf("postgres://test:test@%s:%s/leapmux_test?sslmode=disable", host, port)
		}
		host, port := (storetest.SQLContainer{
			Image: "postgres:17-alpine", Port: 5432, Driver: "pgx", ReadyDSN: dsn,
			Env: map[string]string{"POSTGRES_USER": "test", "POSTGRES_PASSWORD": "test", "POSTGRES_DB": "leapmux_test"},
		}).Start(t)
		st, err = postgres.OpenTestable(context.Background(), config.PostgresConfig{DSN: dsn(host, port)})
	} else {
		dsn := func(host, port string) string {
			return fmt.Sprintf("test:test@tcp(%s:%s)/leapmux_test?parseTime=true", host, port)
		}
		host, port := (storetest.SQLContainer{
			Image: "mysql:8", Port: 3306, Driver: "mysql", ReadyDSN: dsn,
			Env: map[string]string{"MYSQL_ROOT_PASSWORD": "test", "MYSQL_DATABASE": "leapmux_test", "MYSQL_USER": "test", "MYSQL_PASSWORD": "test"},
		}).Start(t)
		st, err = mysqlstore.OpenTestable(config.MySQLConfig{DSN: dsn(host, port)})
	}
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.Migrator().Migrate(context.Background()))
	return st
}

type registrationOverlap struct {
	requests      atomic.Int32
	firstOnce     sync.Once
	secondOnce    sync.Once
	firstInserted chan struct{}
	secondReady   chan struct{}
	release       chan struct{}
}

type registrationOverlapStore struct {
	store.Store
	coordinator *registrationOverlap
	request     int32
	ownerLocked bool
}

func (s registrationOverlapStore) RunInTransaction(ctx context.Context, fn func(store.Store) error) error {
	s.request = s.coordinator.requests.Add(1)
	return s.Store.RunInTransaction(ctx, func(tx store.Store) error {
		s.Store = tx
		return fn(s)
	})
}

func (s registrationOverlapStore) RunInUserAuthTransaction(ctx context.Context, owner userid.UserID, fn func(store.Store) error) error {
	s.request = s.coordinator.requests.Add(1)
	if s.request == 2 {
		s.coordinator.secondOnce.Do(func() { close(s.coordinator.secondReady) })
	}
	return s.Store.RunInUserAuthTransaction(ctx, owner, func(tx store.Store) error {
		s.Store, s.ownerLocked = tx, true
		return fn(s)
	})
}

func (s registrationOverlapStore) Workers() store.WorkerStore {
	return registrationOverlapWorkers{WorkerStore: s.Store.Workers(), admission: s}
}

type registrationOverlapWorkers struct {
	store.WorkerStore
	admission registrationOverlapStore
}

func (s registrationOverlapWorkers) ListByUserID(ctx context.Context, p store.ListWorkersByUserIDParams) (store.Page[store.Worker], error) {
	page, err := s.WorkerStore.ListByUserID(ctx, p)
	if err == nil && s.admission.request == 2 && !s.admission.ownerLocked {
		// Without the owner lock, hold the second count after it observes the
		// uncommitted first insert. This reproduces the admission race.
		s.admission.coordinator.secondOnce.Do(func() { close(s.admission.coordinator.secondReady) })
		select {
		case <-s.admission.coordinator.release:
		case <-ctx.Done():
			return store.Page[store.Worker]{}, ctx.Err()
		}
	}
	return page, err
}

func (s registrationOverlapWorkers) Create(ctx context.Context, p store.CreateWorkerParams) error {
	if err := s.WorkerStore.Create(ctx, p); err != nil {
		return err
	}
	if s.admission.request == 1 {
		s.admission.coordinator.firstOnce.Do(func() { close(s.admission.coordinator.firstInserted) })
		select {
		case <-s.admission.coordinator.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
