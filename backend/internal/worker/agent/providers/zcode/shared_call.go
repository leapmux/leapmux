package zcode

import (
	"context"
	"sync"
	"time"
)

// zcodeSharedCalls runs one call for each key at a time, and shares its result with
// every caller that asks for the same key while the call runs. No result outlives
// its call, so a caller that arrives after the call ended starts a new one.
//
// The call runs on a context that no single caller owns: each caller's context
// derives from its own agent and deadline, and one agent that stops waiting must
// not end the call for the others. The call's own timeout limits it, and the last
// caller that stops waiting ends it.
type zcodeSharedCalls[K comparable, V any] struct {
	mu    sync.Mutex
	calls map[K]*zcodeSharedCall[V]
}

type zcodeSharedCall[V any] struct {
	ctx    context.Context
	done   chan struct{}
	cancel context.CancelFunc
	// waiters counts the callers that wait for the call. Guarded by
	// zcodeSharedCalls.mu.
	waiters int
	// value and err are written before done closes.
	value V
	err   error
}

// do returns the result of fn for key, joining a call for key that already runs.
func (s *zcodeSharedCalls[K, V]) do(ctx context.Context, key K, timeout time.Duration, fn func(context.Context) (V, error)) (V, error) {
	var zero V
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	s.mu.Lock()
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return zero, err
	}
	if s.calls == nil {
		s.calls = map[K]*zcodeSharedCall[V]{}
	}
	call, running := s.calls[key]
	if running && call.ctx.Err() != nil {
		delete(s.calls, key)
		running = false
	}
	if running {
		call.waiters++
	} else {
		callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
		if err := callCtx.Err(); err != nil {
			s.mu.Unlock()
			cancel()
			return zero, err
		}
		call = &zcodeSharedCall[V]{ctx: callCtx, done: make(chan struct{}), cancel: cancel, waiters: 1}
		s.calls[key] = call
		go func() {
			value, err := fn(callCtx)
			s.mu.Lock()
			if contextErr := callCtx.Err(); contextErr != nil {
				value, err = zero, contextErr
			}
			if s.calls[key] == call {
				delete(s.calls, key)
			}
			call.value, call.err = value, err
			close(call.done)
			s.mu.Unlock()
			cancel()
		}()
	}
	s.mu.Unlock()

	var waitErr error
	select {
	case <-call.done:
		return call.value, call.err
	case <-ctx.Done():
		waitErr = ctx.Err()
	case <-call.ctx.Done():
		waitErr = call.ctx.Err()
	}
	return s.finishWait(key, call, waitErr)
}

// finishWait gives an already-committed result precedence over cancellation.
// Otherwise, leaving the last waiter or an expired call permits replacement.
func (s *zcodeSharedCalls[K, V]) finishWait(key K, call *zcodeSharedCall[V], waitErr error) (V, error) {
	s.mu.Lock()
	select {
	case <-call.done:
		s.mu.Unlock()
		return call.value, call.err
	default:
	}
	call.waiters--
	last := call.waiters == 0
	if s.calls[key] == call && (last || call.ctx.Err() != nil) {
		delete(s.calls, key)
	}
	s.mu.Unlock()
	if last {
		call.cancel()
	}
	var zero V
	return zero, waitErr
}

// waiting reports how many callers wait for key. A test synchronizes on it.
func (s *zcodeSharedCalls[K, V]) waiting(key K) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if call, ok := s.calls[key]; ok {
		return call.waiters
	}
	return 0
}

// waitingInAll reports how many callers wait for any key. A test synchronizes on it.
func (s *zcodeSharedCalls[K, V]) waitingInAll() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := 0
	for _, call := range s.calls {
		total += call.waiters
	}
	return total
}

// publish changes a cache only while ctx still owns the current call. Cancellation
// and replacement use this same mutex, so an abandoned call cannot change a
// replacement's state. The callback must not call another method on s.
func (s *zcodeSharedCalls[K, V]) publish(ctx context.Context, key K, change func()) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	call := s.calls[key]
	if call == nil || call.ctx != ctx || ctx.Err() != nil {
		return false
	}
	change()
	return true
}

// zcodeCallCoordinator permits deterministic tests of delayed call admission.
type zcodeCallCoordinator[K comparable, V any] func(context.Context, K, time.Duration, func(context.Context) (V, error)) (V, error)
