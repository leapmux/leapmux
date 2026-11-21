package filelock

import (
	"context"
	"errors"
	"os"
	"time"
)

// Lock acquires an exclusive lock on file, waiting on contention and
// honoring ctx cancellation before and after acquisition. On success,
// ownership stays with the caller, who must call Unlock or Close.
//
// Native errors from TryLock retain their cause. A canceled context
// returns ctx.Err() and releases any lock it acquired. If release fails,
// the error includes both causes and the caller still owns the file.
func Lock(ctx context.Context, file *os.File) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		ok, err := TryLock(file)
		if err != nil {
			return err
		}
		if ok {
			// Check for cancellation that raced acquisition.
			if err := ctx.Err(); err != nil {
				if unlockErr := Unlock(file); unlockErr != nil {
					return errors.Join(err, unlockErr)
				}
				return err
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
