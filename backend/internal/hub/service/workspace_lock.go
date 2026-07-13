package service

import (
	"context"
	"errors"
	"sync"

	"connectrpc.com/connect"
)

func workspaceLockError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return connect.NewError(connect.CodeDeadlineExceeded, err)
	}
	return connect.NewError(connect.CodeCanceled, err)
}

type keyedLock struct {
	mu      sync.Mutex
	entries map[string]*keyedLockEntry
}

type keyedLockEntry struct {
	gate chan struct{}
	refs int
}

func (l *keyedLock) lock(ctx context.Context, key string) (func(), error) {
	l.mu.Lock()
	if l.entries == nil {
		l.entries = make(map[string]*keyedLockEntry)
	}
	entry := l.entries[key]
	if entry == nil {
		entry = &keyedLockEntry{gate: make(chan struct{}, 1)}
		entry.gate <- struct{}{}
		l.entries[key] = entry
	}
	entry.refs++
	l.mu.Unlock()

	select {
	case <-ctx.Done():
		l.releaseReference(key, entry)
		return nil, ctx.Err()
	case <-entry.gate:
		return func() {
			entry.gate <- struct{}{}
			l.releaseReference(key, entry)
		}, nil
	}
}

func (l *keyedLock) releaseReference(key string, entry *keyedLockEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry.refs--
	if entry.refs == 0 && l.entries[key] == entry {
		delete(l.entries, key)
	}
}
