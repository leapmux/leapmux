package sqlutil

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWithTransactionCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	independent := errors.New("independent failure")
	for _, err := range []error{nil, independent, fmt.Errorf("callback: %w", sql.ErrTxDone), errors.Join(sql.ErrTxDone, independent)} {
		if err == nil {
			assert.NoError(t, WithTransactionCancellation(ctx, err))
		} else {
			assert.Same(t, err, WithTransactionCancellation(ctx, err))
		}
	}
	assert.Same(t, sql.ErrTxDone, WithTransactionCancellation(t.Context(), sql.ErrTxDone))
	err := WithTransactionCancellation(ctx, sql.ErrTxDone)
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, err, sql.ErrTxDone)
}

func TestWithTransactionCancellationRetainsDeadline(t *testing.T) {
	ctx, cancel := context.WithDeadline(t.Context(), time.Unix(1, 0))
	defer cancel()
	err := WithTransactionCancellation(ctx, sql.ErrTxDone)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorIs(t, err, sql.ErrTxDone)
}
