package storetest

import (
	"context"
	"errors"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// store.Store documents that a transaction callback MAY RUN MORE THAN ONCE:
// the postgres and mysql dialects retry the whole attempt when the backend
// aborts it for a serialization conflict. A caller that ASSIGNS through a
// captured variable is fine, because a re-run overwrites it. A caller that
// ACCUMULATES -- appends to a slice, adds to a counter, sends on a channel,
// fires a lifecycle effect -- is not, because the aborted attempt's
// contribution stays.
//
// That rule was prose plus a one-time manual audit, and SQLite never retries,
// so the DEFAULT test store exercised the single-run path only. No test
// therefore covered the one shape that matters, anywhere the code relies on
// it.
//
// DoubleRunStore is the mechanism. It runs every transaction callback twice:
// once against an attempt it then rolls back, and once for real. The database
// state is identical either way, so a correct caller cannot tell the
// difference -- and an accumulating one doubles its Go-side effect, which a
// test's own assertions then catch.
//
// It also makes a SQLite test exercise the retry path the distributed
// backends take, which no test did before.

// errDiscardRehearsal aborts the rehearsal attempt. It never reaches a
// caller: RunInTransaction below recognises it and runs the real attempt.
var errDiscardRehearsal = errors.New("storetest: discard the rehearsal attempt")

// DoubleRunStore wraps a store so every transaction callback runs twice.
//
// The embedded store.Store forwards every other method, so a dialect that
// grows an accessor needs no change here -- and no method can recurse into
// the wrapper, because the embedded value is the real store.
type DoubleRunStore struct {
	store.Store
}

// NewDoubleRunStore wraps st. See DoubleRunStore.
func NewDoubleRunStore(st store.Store) store.Store {
	return DoubleRunStore{Store: st}
}

// RunInTransaction runs fn against a rolled-back attempt, then for real.
//
// The rehearsal's own error is DISCARDED, deliberately. fn is entitled to
// fail on either attempt, and the answer that matters is the real one; a
// rehearsal that failed for a reason the real attempt does not repeat must
// not become the caller's error. A store-level failure that is not the
// rollback sentinel still propagates, because that one is not fn's answer at
// all.
func (d DoubleRunStore) RunInTransaction(ctx context.Context, fn func(tx store.Store) error) error {
	if err := d.Store.RunInTransaction(ctx, func(tx store.Store) error {
		_ = fn(tx)
		return errDiscardRehearsal
	}); err != nil && !errors.Is(err, errDiscardRehearsal) {
		return err
	}
	return d.Store.RunInTransaction(ctx, fn)
}

// TestHelper forwards the wrapped store's test-only operations.
//
// A wrapper is a different dynamic type, so `st.(store.TestableStore)` stops
// answering unless it carries the method itself -- and several suites reach
// for that interface to backdate a timestamp. Forwarding it unconditionally
// is right for a decorator that only ever wraps a test store: a wrapped store
// that has no helper panics with a message that states the cause, which is a
// louder answer than a type assertion that silently reports false.
func (d DoubleRunStore) TestHelper() store.TestHelper {
	testable, ok := d.Store.(store.TestableStore)
	if !ok {
		panic("storetest: DoubleRunStore wraps a store with no TestHelper")
	}
	return testable.TestHelper()
}

// RunInUserAuthTransaction takes the identical shape. See RunInTransaction.
func (d DoubleRunStore) RunInUserAuthTransaction(ctx context.Context, userID userid.UserID, fn func(tx store.Store) error) error {
	if err := d.Store.RunInUserAuthTransaction(ctx, userID, func(tx store.Store) error {
		_ = fn(tx)
		return errDiscardRehearsal
	}); err != nil && !errors.Is(err, errDiscardRehearsal) {
		return err
	}
	return d.Store.RunInUserAuthTransaction(ctx, userID, fn)
}
