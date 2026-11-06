package lifecycle

import (
	"os"

	"github.com/leapmux/leapmux/internal/util/filelock"
)

// probeExclusiveLock reports whether the lock file at path is held by
// any process, creating nothing. An absent file is a free lock reported
// with no error.
func probeExclusiveLock(path string) (bool, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	f, err := filelock.Open(path, false)
	if err != nil {
		// A path that cannot be opened cannot be proven held; report it
		// free rather than guessing liveness from an error.
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
