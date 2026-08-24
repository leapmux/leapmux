package webauthn

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
)

func newSignCountTestService(t *testing.T) (*Service, store.Store, string, string, []byte) {
	t.Helper()
	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.Migrator().Migrate(context.Background()))

	key, err := keystore.GenerateKey()
	require.NoError(t, err)
	ks, err := keystore.New(map[uint32][32]byte{1: key})
	require.NoError(t, err)

	svc, err := NewService(RPConfig{
		RPID:          "localhost",
		RPDisplayName: "LeapMux",
		RPOrigins:     []string{"http://localhost"},
	}, st, ks)
	require.NoError(t, err)

	userID := id.Generate()
	require.NoError(t, st.Users().Create(context.Background(), store.CreateUserParams{
		ID:           userID,
		Username:     "scuser" + userID[:8],
		PasswordHash: "hash",
		DisplayName:  "SC User",
		PasswordSet:  true,
	}))
	rowID := id.Generate()
	credID := []byte("cred-" + rowID)
	now := time.Now().UTC()
	require.NoError(t, st.PasskeyCredentials().Create(context.Background(), store.CreatePasskeyCredentialParams{
		ID:           rowID,
		UserID:       userID,
		CredentialID: credID,
		PublicKey:    []byte("pk"),
		SignCount:    10,
		Transports:   "[]",
		FriendlyName: "Key",
		KeyVersion:   1,
		CreatedAt:    now,
	}))
	return svc, st, userID, rowID, credID
}

func TestApplySignCountUpdate_Succeeds(t *testing.T) {
	svc, st, userID, rowID, credID := newSignCountTestService(t)
	require.NoError(t, svc.applySignCountUpdate(context.Background(), credID, userID, 11, time.Now().UTC()))

	row, err := st.PasskeyCredentials().GetByID(context.Background(), rowID)
	require.NoError(t, err)
	assert.EqualValues(t, 11, row.SignCount)
}

func TestApplySignCountUpdate_EqualNonZeroIsClone(t *testing.T) {
	svc, st, userID, _, credID := newSignCountTestService(t)
	now := time.Now().UTC()
	require.NoError(t, st.PasskeyCredentials().UpdateSignCount(context.Background(), store.UpdatePasskeySignCountParams{
		CredentialID: credID, UserID: userID, SignCount: 11, LastUsedAt: now,
	}))

	err := svc.applySignCountUpdate(context.Background(), credID, userID, 11, now)
	assert.ErrorIs(t, err, ErrCloneDetected, "a second assertion at an already-applied non-zero count is a clone")
}

func TestApplySignCountUpdate_ZeroCounterDoesNotClone(t *testing.T) {
	svc, st, userID, rowID, credID := newSignCountTestService(t)
	now := time.Now().UTC()
	require.NoError(t, st.PasskeyCredentials().UpdateSignCount(context.Background(), store.UpdatePasskeySignCountParams{
		CredentialID: credID, UserID: userID, SignCount: 0, LastUsedAt: now,
	}))

	require.NoError(t, svc.applySignCountUpdate(context.Background(), credID, userID, 0, now))
	row, err := st.PasskeyCredentials().GetByID(context.Background(), rowID)
	require.NoError(t, err)
	assert.EqualValues(t, 0, row.SignCount)
}

func TestApplySignCountUpdate_RetriesPastConcurrentAdvance(t *testing.T) {
	svc, st, userID, rowID, credID := newSignCountTestService(t)
	now := time.Now().UTC()
	// Simulate another finish that advanced 10 -> 11 while we still aim for 12.
	require.NoError(t, st.PasskeyCredentials().UpdateSignCount(context.Background(), store.UpdatePasskeySignCountParams{
		CredentialID: credID, UserID: userID, SignCount: 11, LastUsedAt: now,
	}))

	err := svc.applySignCountUpdate(context.Background(), credID, userID, 12, now)
	require.NoError(t, err)

	row, err := st.PasskeyCredentials().GetByID(context.Background(), rowID)
	require.NoError(t, err)
	assert.EqualValues(t, 12, row.SignCount)
}

func TestApplySignCountUpdate_RejectsBehindCounter(t *testing.T) {
	svc, _, userID, _, credID := newSignCountTestService(t)
	err := svc.applySignCountUpdate(context.Background(), credID, userID, 9, time.Now().UTC())
	assert.ErrorIs(t, err, ErrCloneDetected)
}

func TestApplySignCountUpdate_ConcurrentSameTarget(t *testing.T) {
	svc, st, userID, rowID, credID := newSignCountTestService(t)
	now := time.Now().UTC()

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := svc.applySignCountUpdate(context.Background(), credID, userID, 11, now)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)

	successes := 0
	clones := 0
	for err := range errs {
		if errors.Is(err, ErrCloneDetected) {
			clones++
			continue
		}
		require.NoError(t, err)
		successes++
	}
	assert.Equal(t, 1, successes, "exactly one concurrent writer may commit the increment")
	assert.Equal(t, 7, clones, "the other writers must fail clone detection")
	row, err := st.PasskeyCredentials().GetByID(context.Background(), rowID)
	require.NoError(t, err)
	assert.EqualValues(t, 11, row.SignCount)
}
