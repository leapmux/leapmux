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

	t.Run("skips event on no-op", func(t *testing.T) {
		n, err := RunCredentialMutation(context.Background(), func(_ context.Context, fn func(int) error) error { return fn(1) },
			func(context.Context, int) (*CredentialEvent, error) { return nil, nil },
			func(context.Context, int, CredentialEvent) error { return errors.New("must not emit") })
		require.NoError(t, err)
		assert.Zero(t, n)
	})
}
