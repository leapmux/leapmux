package store

import (
	"context"
	"errors"

	"github.com/leapmux/leapmux/util/ctxutil"
)

var errAdminMembershipLockRequired = errors.New("restart the transaction with the administrator mutex")

// RequireAdminMembershipLock asks the outer transaction owner to restart when
// it lacks the mutex. Waiting here would reverse lock order after a target-user
// locking read.
func RequireAdminMembershipLock(held bool) error {
	if !held {
		return errAdminMembershipLockRequired
	}
	return nil
}

// RunAdminMembershipTransaction leaves ordinary operations outside the mutex.
// An administrator discovered under a user lock requests a complete rollback,
// then the retry acquires the shared mutex before beginning its transaction.
// run must finish rollback before returning the mutex requirement. Nested
// transactions leave that restart to their outer owner. The serving Hub's
// runtime lease supplies process exclusivity.
func RunAdminMembershipTransaction(ctx context.Context, mutex *ctxutil.Mutex, run func(held bool) error) error {
	err := run(false)
	if !errors.Is(err, errAdminMembershipLockRequired) {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := mutex.Lock(ctx); err != nil {
		return err
	}
	defer mutex.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return run(true)
}
