package lifecycle

import (
	"os"

	"github.com/leapmux/leapmux/internal/util/filelock"
)

// probeExclusiveLock reports whether the lock file at path is held by
// any process, creating nothing. An absent file is a free lock reported
// with no error.
func probeExclusiveLock(path string) (bool, error) {
	f, err := filelock.OpenExisting(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		// An open failure leaves liveness unknown. Preserve it so callers
		// cannot mistake failure for a released lock.
		return false, err
	}
	defer func() { _ = f.Close() }()
	held, err := filelock.TryLock(f)
	if err != nil {
		return false, err
	}
	if held {
		// This process took the lock; release it before answering.
		if unlockErr := filelock.Unlock(f); unlockErr != nil {
			return false, unlockErr
		}
		return false, nil
	}
	return true, nil
}
