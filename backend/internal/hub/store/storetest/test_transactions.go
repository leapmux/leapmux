package storetest

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *Suite) testTransactions(t *testing.T) {
	t.Run("commit on success", func(t *testing.T) {
		st := s.NewStore(t)

		var userID string
		err := st.RunInTransaction(ctx, func(tx store.Store) error {
			user := SeedUser(t, tx, "tx-user")
			userID = user.ID
			return nil
		})
		require.NoError(t, err)

		user, err := st.Users().GetByID(ctx, userID)
		require.NoError(t, err)
		assert.Equal(t, "tx-user", user.Username)
	})

	t.Run("rollback on error", func(t *testing.T) {
		st := s.NewStore(t)

		var userID string
		err := st.RunInTransaction(ctx, func(tx store.Store) error {
			user := SeedUser(t, tx, "tx-rollback-user")
			userID = user.ID
			return errors.New("intentional error")
		})
		require.Error(t, err)

		_, err = st.Users().GetByID(ctx, userID)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	// A write-write conflict is a NORMAL answer from a distributed backend,
	// and the store is the part that must handle it: PostgreSQL, YugabyteDB,
	// CockroachDB, MySQL and TiDB all abort one transaction with a retryable
	// code and expect the client to run the whole unit of work again.
	//
	// The unit tests for the retry drive their wrappers by hand, so all of
	// them keep passing if withTransaction stops composing the retry. This
	// one runs two REAL conflicting transactions against a REAL backend and
	// requires both to succeed, which is the property a user actually has.
	//
	// The handshake is explicit rather than timed: the slow transaction reads
	// first, waits for the fast one to commit, and only then writes. Nothing
	// here sleeps hoping for a race.
	t.Run("two transactions conflicting on one row both succeed", func(t *testing.T) {
		if !s.ConcurrentWriteTransactions {
			t.Skip("this backend holds one write transaction at a time, so there is no conflict to resolve")
		}
		st := s.NewStore(t)
		user := SeedUser(t, st, "tx-conflict")

		// The slow transaction re-runs from the top on a retry, so the
		// handshake must fire on the FIRST attempt only -- a second wait
		// would block for ever on a channel nobody closes again.
		var attempts atomic.Int32
		fastCommitted := make(chan struct{})
		slowRead := make(chan struct{})
		var slowReadOnce sync.Once

		var slowErr, fastErr error
		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			slowErr = st.RunInTransaction(ctx, func(tx store.Store) error {
				attempt := attempts.Add(1)
				// Read the row into this transaction's snapshot.
				if _, err := tx.Users().GetByID(ctx, user.ID); err != nil {
					return err
				}
				if attempt == 1 {
					slowReadOnce.Do(func() { close(slowRead) })
					<-fastCommitted
				}
				return tx.Users().UpdateProfile(ctx, store.UpdateUserProfileParams{
					ID: user.ID, Username: user.Username, DisplayName: "slow",
				})
			})
		}()

		go func() {
			defer wg.Done()
			<-slowRead
			fastErr = st.RunInTransaction(ctx, func(tx store.Store) error {
				return tx.Users().UpdateProfile(ctx, store.UpdateUserProfileParams{
					ID: user.ID, Username: user.Username, DisplayName: "fast",
				})
			})
			close(fastCommitted)
		}()

		wg.Wait()

		require.NoError(t, fastErr, "the transaction that committed first must succeed")
		require.NoError(t, slowErr,
			"the transaction the backend aborted for a conflict must be run again, not returned as a failure")

		// How the backends get here differs, and both ways are correct.
		// CockroachDB and YugabyteDB ABORT the slow transaction with 40001
		// and it runs a second time -- measured: two attempts, and without
		// the retry this case fails with "restart transaction:
		// TransactionRetryWithProtoRefreshError ... (SQLSTATE 40001)".
		// PostgreSQL and MySQL make the slow UPDATE wait on the row lock
		// instead, so one attempt is enough there. The limit is what both
		// share: a backend that kept aborting must not retry without end.
		attempted := attempts.Load()
		assert.GreaterOrEqual(t, attempted, int32(1))
		assert.LessOrEqualf(t, attempted, int32(5),
			"the retry is capped; %d attempts means it retries without end", attempted)

		// The slow one wrote last, so its value is what survives. That is the
		// point of retrying the whole unit of work rather than one statement:
		// the re-run reads the committed state and writes on top of it.
		row, err := st.Users().GetByID(ctx, user.ID)
		require.NoError(t, err)
		assert.Equal(t, "slow", row.DisplayName)
	})

	t.Run("multiple operations in transaction", func(t *testing.T) {
		st := s.NewStore(t)

		err := st.RunInTransaction(ctx, func(tx store.Store) error {
			SeedUser(t, tx, "tx-multi-user")
			return nil
		})
		require.NoError(t, err)

		page, err := st.Users().ListAll(ctx, store.ListAllUsersParams{PageParams: store.PageParams{Limit: 10}})
		require.NoError(t, err)
		require.Len(t, page.Rows, 1)
		assert.Equal(t, "tx-multi-user", page.Rows[0].Username)
	})

	t.Run("nested reads within transaction", func(t *testing.T) {
		st := s.NewStore(t)

		err := st.RunInTransaction(ctx, func(tx store.Store) error {
			user := SeedUser(t, tx, "tx-read-user")

			got, err := tx.Users().GetByID(ctx, user.ID)
			require.NoError(t, err)
			assert.Equal(t, "tx-read-user", got.Username)

			return nil
		})
		require.NoError(t, err)
	})

	t.Run("rollback on error rolls back all entity types", func(t *testing.T) {
		st := s.NewStore(t)

		userID := id.Generate()
		err := st.RunInTransaction(ctx, func(tx store.Store) error {
			if err := tx.Users().Create(ctx, store.CreateUserParams{
				ID:            userID,
				Username:      "rollback-multi-user",
				PasswordHash:  "hash",
				DisplayName:   "RB",
				Email:         "rb@example.com",
				EmailVerified: true,
				PasswordSet:   true,
				IsAdmin:       false,
			}); err != nil {
				return err
			}
			return fmt.Errorf("intentional rollback")
		})
		require.Error(t, err)

		_, err = st.Users().GetByID(ctx, userID)
		assert.ErrorIs(t, err, store.ErrNotFound, "user should be rolled back")
	})

	t.Run("transaction isolation", func(t *testing.T) {
		st := s.NewStore(t)

		var userID string
		err := st.RunInTransaction(ctx, func(tx store.Store) error {
			user := SeedUser(t, tx, "tx-isolation-user")
			userID = user.ID

			_, err := tx.Users().GetByID(ctx, user.ID)
			require.NoError(t, err)

			return errors.New("intentional rollback")
		})
		require.Error(t, err)

		_, err = st.Users().GetByID(ctx, userID)
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("registration key consume rolls back with outer transaction", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "tx-registration-key-user")
		regID := SeedRegistrationKey(t, st, user.ID, time.Now().Add(5*time.Minute).UTC())

		err := st.RunInTransaction(ctx, func(tx store.Store) error {
			_, err := tx.RegistrationKeys().Consume(ctx, regID)
			if err != nil {
				return err
			}
			return errors.New("intentional rollback")
		})
		require.EqualError(t, err, "intentional rollback")

		consumed, err := st.RegistrationKeys().Consume(ctx, regID)
		require.NoError(t, err)
		assert.Equal(t, user.ID, consumed.CreatedBy)
	})
}
