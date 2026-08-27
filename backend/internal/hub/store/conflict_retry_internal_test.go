package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The retry POLICY is shared, so it is tested here rather than twice in the
// dialects that used to hold byte-identical copies of it. Each dialect keeps
// its own test that the WRAPPER routes through this, plus its own test of
// isRetryableConflict, which is the only genuinely per-driver part.
func TestRetryOnConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	conflict := errors.New("conflict")
	other := errors.New("something else")
	isConflict := func(err error) bool { return errors.Is(err, conflict) }

	t.Run("a success on the first attempt runs once", func(t *testing.T) {
		t.Parallel()
		calls := 0
		require.NoError(t, RetryOnConflict(ctx, isConflict, func() error {
			calls++
			return nil
		}))
		assert.Equal(t, 1, calls)
	})

	t.Run("a conflict is retried until it succeeds", func(t *testing.T) {
		t.Parallel()
		calls := 0
		require.NoError(t, RetryOnConflict(ctx, isConflict, func() error {
			calls++
			if calls < 3 {
				return conflict
			}
			return nil
		}))
		assert.Equal(t, 3, calls)
	})

	t.Run("a non-conflict error is final", func(t *testing.T) {
		t.Parallel()
		calls := 0
		err := RetryOnConflict(ctx, isConflict, func() error {
			calls++
			return other
		})
		assert.Equal(t, other, err)
		assert.Equal(t, 1, calls, "only isRetryable decides, never the loop")
	})

	// The cap is what stops a congested backend from holding a request open:
	// a caller that waits longer serves nobody, and the conflict itself is
	// what reaches them.
	t.Run("the attempt count is capped and the conflict still reaches the caller", func(t *testing.T) {
		t.Parallel()
		calls := 0
		err := RetryOnConflict(ctx, isConflict, func() error {
			calls++
			return conflict
		})
		assert.Equal(t, conflict, err)
		assert.Equal(t, ConflictRetryLimit, calls)
	})

	// A cancelled request stops at once, and the DATABASE error is what the
	// caller gets: "context canceled" would hide which statement conflicted.
	t.Run("a cancelled caller stops at once with the database error", func(t *testing.T) {
		t.Parallel()
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()

		calls := 0
		start := time.Now()
		err := RetryOnConflict(cancelled, isConflict, func() error {
			calls++
			return conflict
		})
		assert.Equal(t, conflict, err)
		assert.Equal(t, 1, calls)
		assert.Less(t, time.Since(start), ConflictRetryBaseDelay)
	})

	// The predicate is never consulted for a nil error, so a caller cannot
	// write one that reports a success as retryable and loops for ever.
	t.Run("a nil error never reaches the predicate", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, RetryOnConflict(ctx, func(error) bool {
			t.Fatal("the predicate must not be asked about a success")
			return true
		}, func() error { return nil }))
	})
}
