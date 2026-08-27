package store

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunCredentialMutation(t *testing.T) {
	t.Run("emits in transaction", func(t *testing.T) {
		inTransaction := false
		emitted := false
		n, err := RunCredentialMutation(context.Background(), func(_ context.Context, fn func(int) error) error {
			inTransaction = true
			defer func() { inTransaction = false }()
			return fn(1)
		}, func(context.Context, int) (*CredentialEvent, error) {
			return &CredentialEvent{SubjectID: "token", UserID: "user"}, nil
		}, func(_ context.Context, _ int, event CredentialEvent) error {
			assert.True(t, inTransaction)
			assert.Equal(t, "token", event.SubjectID)
			emitted = true
			return nil
		})
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)
		assert.True(t, emitted)
	})

	// The retry contract: the postgres and mysql dialects repeat the WHOLE
	// transaction when the backend aborts it, so this callback can run more
	// than once. A first attempt that wrote its row and then lost its commit
	// must not report that row through an attempt that changed nothing.
	t.Run("a failed retry reports no affected row", func(t *testing.T) {
		attempts := 0
		aborted := errors.New("conflict on the second attempt")
		n, err := RunCredentialMutation(context.Background(), func(_ context.Context, fn func(int) error) error {
			// Attempt one runs to the end and then loses its commit.
			attempts++
			if err := fn(1); err != nil {
				return err
			}
			// Attempt two fails before the mutation reports an event.
			attempts++
			return fn(1)
		}, func(context.Context, int) (*CredentialEvent, error) {
			if attempts > 1 {
				return nil, aborted
			}
			return &CredentialEvent{SubjectID: "token", UserID: "user"}, nil
		}, func(context.Context, int, CredentialEvent) error { return nil })
		require.ErrorIs(t, err, aborted)
		assert.Equal(t, 2, attempts, "the harness must have run the callback twice")
		assert.Zero(t, n, "a rolled-back attempt's row must not be reported")
	})

	t.Run("skips event on no-op", func(t *testing.T) {
		n, err := RunCredentialMutation(context.Background(), func(_ context.Context, fn func(int) error) error { return fn(1) },
			func(context.Context, int) (*CredentialEvent, error) { return nil, nil },
			func(context.Context, int, CredentialEvent) error { return errors.New("must not emit") })
		require.NoError(t, err)
		assert.Zero(t, n)
	})
}
