package filelock

import (
	"context"
	"os"
	"time"
)

// Lock acquires an exclusive lock on file, waiting on contention and
// honoring ctx cancellation before and after acquisition. On success,
// ownership stays with the caller, who must call Unlock or Close.
//
// Native errors from TryLock retain their cause. A canceled context
// returns ctx.Err() without holding the lock.
func Lock(ctx context.Context, file *os.File) error {
	for {
		ok, err := TryLock(file)
		if err != nil {
			return err
		}
		if ok {
			// Check for cancellation that raced acquisition.
			if ctx.Err() != nil {
				_ = Unlock(file)
				return ctx.Err()
			}
			return nil
		}
		// Lock is held by another process. Wait briefly, then retry.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}
