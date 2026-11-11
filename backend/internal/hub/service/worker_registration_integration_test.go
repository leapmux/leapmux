package service_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/workerkeys"
	"github.com/leapmux/leapmux/internal/workerkeys/workerkeystest"
)

func TestRegisterConcurrentKeysRespectTheOwnerMaximum(t *testing.T) {
	for _, backend := range []string{"postgres", "mysql"} {
		t.Run(backend, func(t *testing.T) {
			st := registrationStore(t, backend)
			coordinator := &registrationOverlap{
				firstInserted: make(chan struct{}), secondReady: make(chan struct{}), release: make(chan struct{}),
			}
			env := regKeyEnvFromStore(t, st, testConfig(), maxWorkersPerUser(1), regKeyEnvOptions{connectorStore: func(st store.Store) store.Store {
				return registrationOverlapStore{Store: st, coordinator: coordinator}
			}}, nil)
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
				var attempt [16]byte
				_, _ = rand.Read(attempt[:])
				response, err := env.connectorClient.Register(requestCtx, &leapmuxv1.RegisterRequest{
					Keys:      workerkeystest.Keys(0x31),
					AttemptId: hex.EncodeToString(attempt[:]),
				})
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

func TestConcurrentRegistrationOfOneAttemptReturnsRecoverableConflict(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres", "mysql"} {
		t.Run(backend, func(t *testing.T) {
			key := id.Generate()
			barrier := &registrationKeyReadBarrier{key: key, arrivals: make(chan struct{}, 2), release: make(chan struct{})}
			decorate := func(st store.Store) store.Store { return registrationKeyReadStore{Store: st, barrier: barrier} }
			var env *regKeyEnv
			if backend == "sqlite" {
				env = newRegKeyEnv(t, testConfig(), nil, regKeyEnvOptions{connectorStore: decorate})
			} else {
				env = regKeyEnvFromStore(t, registrationStore(t, backend), testConfig(), nil, regKeyEnvOptions{connectorStore: decorate}, nil)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			owner := userid.MustNew(env.adminID(t))
			require.NoError(t, env.store.RegistrationKeys().Create(ctx, store.CreateRegistrationKeyParams{
				ID: key, CreatedBy: owner, ExpiresAt: time.Now().Add(time.Hour),
			}))
			attempt := newRegisterAttempt(t, 0x51)
			type result struct {
				response *leapmuxv1.RegisterResponse
				err      error
			}
			results := make(chan result, 2)
			for range 2 {
				go func() {
					response, err := env.registerCall(t, ctx, key, attempt, false)
					results <- result{response, err}
				}()
			}
			for range 2 {
				select {
				case <-barrier.arrivals:
				case <-ctx.Done():
					t.Fatal("both registrations must read the key before either transaction starts")
				}
			}
			close(barrier.release)
			var registered *leapmuxv1.RegisterResponse
			var conflicts int
			for range 2 {
				select {
				case result := <-results:
					if result.err == nil {
						require.Nil(t, registered, "only one request may create the worker")
						registered = result.response
					} else {
						require.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(result.err), result.err)
						conflicts++
					}
				case <-ctx.Done():
					t.Fatal("the overlapping registrations did not finish")
				}
			}
			require.NotNil(t, registered)
			assert.Equal(t, 1, conflicts)
			page, err := env.store.Workers().ListByUserID(ctx, store.ListWorkersByUserIDParams{
				RegisteredBy: owner, PageParams: store.PageParams{Limit: 10},
			})
			require.NoError(t, err)
			require.Len(t, page.Rows, 1)
			assert.Equal(t, registered.GetWorkerId(), page.Rows[0].ID)
			assert.Equal(t, registered.GetAuthToken(), page.Rows[0].AuthToken)
		})
	}
}

type registrationKeyReadBarrier struct {
	key      string
	arrivals chan struct{}
	release  chan struct{}
}

type registrationKeyReadStore struct {
	store.Store
	barrier *registrationKeyReadBarrier
}

func (s registrationKeyReadStore) RegistrationKeys() store.RegistrationKeyStore {
	return registrationKeyReads{RegistrationKeyStore: s.Store.RegistrationKeys(), barrier: s.barrier}
}

type registrationKeyReads struct {
	store.RegistrationKeyStore
	barrier *registrationKeyReadBarrier
}

func (s registrationKeyReads) GetByID(ctx context.Context, key string) (*store.WorkerRegistrationKey, error) {
	row, err := s.RegistrationKeyStore.GetByID(ctx, key)
	if err != nil || key != s.barrier.key {
		return row, err
	}
	select {
	case s.barrier.arrivals <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-s.barrier.release:
		return row, nil
	case <-ctx.Done():
		return nil, ctx.Err()
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

// registerAttempt carries everything one durable registration attempt
// needs: the attempt identity, the key set, and the key material that
// signs the recovery challenge.
type registerAttempt struct {
	attemptID string
	keys      *leapmuxv1.WorkerPublicKeys
	composite *noise.CompositeKeypair
}

func newRegisterAttempt(t *testing.T, fill byte) registerAttempt {
	t.Helper()
	var attempt [16]byte
	_, err := rand.Read(attempt[:])
	require.NoError(t, err)
	composite, err := noise.GenerateCompositeKeypair()
	require.NoError(t, err)
	keys, err := workerkeys.Of(composite)
	require.NoError(t, err)
	return registerAttempt{
		attemptID: hex.EncodeToString(attempt[:]),
		keys:      keys,
		composite: composite,
	}
}

// registerCall performs one Register over the real service. loseResponse
// drops the reply after the handler has fully committed, reproducing the
// lost-response failure the recovery protocol exists for.
func (env *regKeyEnv) registerCall(t *testing.T, ctx context.Context, key string, attempt registerAttempt, loseResponse bool) (*leapmuxv1.RegisterResponse, error) {
	t.Helper()
	requestCtx, info := connect.NewClientContext(ctx)
	info.RequestHeader().Set("Authorization", "Bearer "+key)
	response, err := env.connectorClient.Register(requestCtx, &leapmuxv1.RegisterRequest{
		Keys:      attempt.keys,
		AttemptId: attempt.attemptID,
	})
	if err != nil || !loseResponse {
		return response, err
	}
	// The response arrived; the caller wants the attempt to look LOST.
	// The transaction has committed either way -- the register above was
	// real -- so dropping here reproduces "the hub consumed the key and
	// the client never saw the credentials".
	return nil, connect.NewError(connect.CodeUnavailable, "simulated lost response")
}

// TestRegisterRecoversAfterLostCommittedResponse runs the REAL
// registration service, loses the first response only after the
// transaction commits, and recovers the same worker and token through
// the challenge protocol with exactly one row.
func TestRegisterRecoversAfterLostCommittedResponse(t *testing.T) {
	env := newRegKeyEnv(t, testConfig(), nil, regKeyEnvOptions{})
	ctx := context.Background()
	admin := userid.MustNew(env.adminID(t))
	key := id.Generate()
	require.NoError(t, env.store.RegistrationKeys().Create(ctx, store.CreateRegistrationKeyParams{
		ID: key, CreatedBy: admin, ExpiresAt: time.Now().Add(time.Hour),
	}))

	attempt := newRegisterAttempt(t, 0x41)

	_, err := env.registerCall(t, ctx, key, attempt, true)
	require.Error(t, err, "the first response was lost after commit")

	// Exactly one row exists, pinned to this attempt with the digest.
	worker, rowErr := env.store.Workers().GetByRegistrationAttemptID(ctx, attempt.attemptID)
	require.NoError(t, rowErr, "the committed attempt must be findable")

	// Consuming the key expired it, so everything below recovers with a key
	// that no longer registers anything. The documentation promises that a
	// rerun recovers after the key's lifetime ends, and this is that case.
	consumedKey, keyErr := env.store.RegistrationKeys().GetByID(ctx, key)
	require.NoError(t, keyErr)
	require.True(t, consumedKey.ExpiresAt.Before(time.Now()), "the consumed key must be expired, got %s", consumedKey.ExpiresAt)

	// A plain retry of Register is AlreadyExists, not a second worker.
	_, err = env.registerCall(t, ctx, key, attempt, false)
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))

	// The recovery: fresh challenge, signature over the exact bytes,
	// credentials back.
	recovered := recoverThroughChallenge(t, ctx, env, key, attempt)
	assert.Equal(t, worker.ID, recovered.GetWorkerId(), "the same worker comes back")
	assert.Equal(t, worker.AuthToken, recovered.GetAuthToken(), "the original token comes back, not a new one")

	page, listErr := env.store.Workers().ListByUserID(ctx, store.ListWorkersByUserIDParams{
		RegisteredBy: admin, PageParams: store.PageParams{Limit: 10},
	})
	require.NoError(t, listErr)
	require.Len(t, page.Rows, 1, "recovery mints no second worker")
}

// recoveryMessageBytes builds the recovery signature's byte sequence
// INDEPENDENTLY of the service, so this file pins the protocol format:
// the prefix, both decoded identities, the nonce, and the key digest.
func recoveryMessageBytes(attemptID, challengeID string, nonce []byte, key string) []byte {
	digest := sha256.Sum256([]byte(key))
	message := append([]byte{}, "leapmux.worker.registration.recovery.v1\x00"...)
	attemptRaw, _ := hex.DecodeString(attemptID)
	challengeRaw, _ := hex.DecodeString(challengeID)
	message = append(message, attemptRaw...)
	message = append(message, challengeRaw...)
	message = append(message, nonce...)
	return append(message, digest[:]...)
}

// recoverThroughChallenge drives the two recovery RPCs and returns the
// recovered credentials.
func recoverThroughChallenge(t *testing.T, ctx context.Context, env *regKeyEnv, key string, attempt registerAttempt) *leapmuxv1.RegisterResponse {
	t.Helper()
	challengeCtx, challengeInfo := connect.NewClientContext(ctx)
	challengeInfo.RequestHeader().Set("Authorization", "Bearer "+key)
	challenge, err := env.connectorClient.GetRegistrationChallenge(challengeCtx,
		&leapmuxv1.GetRegistrationChallengeRequest{AttemptId: attempt.attemptID})
	require.NoError(t, err)

	message := recoveryMessageBytes(attempt.attemptID, challenge.GetChallengeId(), challenge.GetNonce(), key)
	signature, err := attempt.composite.SlhdsaPrivateKey.Sign(rand.Reader, message, nil)
	require.NoError(t, err)

	recoverCtx, recoverInfo := connect.NewClientContext(ctx)
	recoverInfo.RequestHeader().Set("Authorization", "Bearer "+key)
	recovered, err := env.connectorClient.RecoverRegistration(recoverCtx, &leapmuxv1.RecoverRegistrationRequest{
		AttemptId:   attempt.attemptID,
		ChallengeId: challenge.GetChallengeId(),
		Signature:   signature,
	})
	require.NoError(t, err)
	return recovered
}

// TestRegistrationRecoveryAcrossClientAndHubRestart restarts the client
// from its pending data (a fresh process state) and the Hub (an empty
// challenge store): a fresh challenge recovers the committed identity.
func TestRegistrationRecoveryAcrossClientAndHubRestart(t *testing.T) {
	env := newRegKeyEnv(t, testConfig(), nil, regKeyEnvOptions{})
	ctx := context.Background()
	admin := userid.MustNew(env.adminID(t))
	key := id.Generate()
	require.NoError(t, env.store.RegistrationKeys().Create(ctx, store.CreateRegistrationKeyParams{
		ID: key, CreatedBy: admin, ExpiresAt: time.Now().Add(time.Hour),
	}))
	attempt := newRegisterAttempt(t, 0x42)

	_, err := env.registerCall(t, ctx, key, attempt, true)
	require.Error(t, err)

	// A Hub restart empties the in-memory challenge store; the service
	// under test gets a fresh one, as a new process would.
	env.connector.ResetChallengeStore()

	// The "restarted client" reuses the SAME attempt identity and key
	// material, which is what its durable pending record holds.
	recovered := recoverThroughChallenge(t, ctx, env, key, attempt)
	worker, rowErr := env.store.Workers().GetByRegistrationAttemptID(ctx, attempt.attemptID)
	require.NoError(t, rowErr)
	assert.Equal(t, worker.ID, recovered.GetWorkerId())
	assert.Equal(t, worker.AuthToken, recovered.GetAuthToken())
}

// TestRecoveryRechecksWorkerAndOwner deletes the owner or the worker
// after the challenge is issued but before the recovery arrives: every
// case refuses credentials.
func TestRecoveryRechecksWorkerAndOwner(t *testing.T) {
	for _, tc := range []struct {
		name   string
		damage func(t *testing.T, ctx context.Context, env *regKeyEnv, workerID string)
	}{
		{"the worker is deleted", func(t *testing.T, ctx context.Context, env *regKeyEnv, workerID string) {
			require.NoError(t, env.store.Workers().MarkDeleted(ctx, workerID))
		}},
		{"the worker is deregistering", func(t *testing.T, ctx context.Context, env *regKeyEnv, workerID string) {
			_, err := env.store.Workers().ForceDeregister(ctx, workerID)
			require.NoError(t, err)
		}},
		{"the owner is deleted", func(t *testing.T, ctx context.Context, env *regKeyEnv, workerID string) {
			worker, err := env.store.Workers().GetByIDIncludeDeleted(ctx, workerID)
			require.NoError(t, err)
			require.NoError(t, env.store.Users().Delete(ctx, worker.RegisteredBy))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newRegKeyEnv(t, testConfig(), nil, regKeyEnvOptions{})
			ctx := context.Background()
			admin := userid.MustNew(env.adminID(t))
			key := id.Generate()
			require.NoError(t, env.store.RegistrationKeys().Create(ctx, store.CreateRegistrationKeyParams{
				ID: key, CreatedBy: admin, ExpiresAt: time.Now().Add(time.Hour),
			}))
			attempt := newRegisterAttempt(t, 0x43)
			_, err := env.registerCall(t, ctx, key, attempt, true)
			require.Error(t, err)
			worker, rowErr := env.store.Workers().GetByRegistrationAttemptID(ctx, attempt.attemptID)
			require.NoError(t, rowErr)

			// The challenge is issued BEFORE the damage lands, so the
			// recovery must catch it at its own recheck.
			challengeCtx, challengeInfo := connect.NewClientContext(ctx)
			challengeInfo.RequestHeader().Set("Authorization", "Bearer "+key)
			challenge, chErr := env.connectorClient.GetRegistrationChallenge(challengeCtx,
				&leapmuxv1.GetRegistrationChallengeRequest{AttemptId: attempt.attemptID})
			require.NoError(t, chErr)
			tc.damage(t, ctx, env, worker.ID)

			message := recoveryMessageBytes(attempt.attemptID, challenge.GetChallengeId(), challenge.GetNonce(), key)
			signature, signErr := attempt.composite.SlhdsaPrivateKey.Sign(rand.Reader, message, nil)
			require.NoError(t, signErr)
			recoverCtx, recoverInfo := connect.NewClientContext(ctx)
			recoverInfo.RequestHeader().Set("Authorization", "Bearer "+key)
			_, recoverErr := env.connectorClient.RecoverRegistration(recoverCtx, &leapmuxv1.RecoverRegistrationRequest{
				AttemptId:   attempt.attemptID,
				ChallengeId: challenge.GetChallengeId(),
				Signature:   signature,
			})
			require.Error(t, recoverErr, "a damaged worker or owner cannot recover credentials")
		})
	}
}
