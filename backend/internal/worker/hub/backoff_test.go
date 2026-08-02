package hub

import (
	"time"

	"github.com/leapmux/leapmux/internal/util/backoffutil"
)

// newFastBackoff creates a fast exponential backoff for testing.
func newFastBackoff() *backoffutil.Backoff {
	return backoffutil.NewBackoff(1*time.Millisecond, 10*time.Millisecond, 0)
}
