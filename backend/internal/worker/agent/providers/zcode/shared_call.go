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
	s.mu.Lock()
	if s.calls == nil {
		s.calls = map[K]*zcodeSharedCall[V]{}
	}
	call, running := s.calls[key]
	if running {
		call.waiters++
	} else {
		callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
		call = &zcodeSharedCall[V]{done: make(chan struct{}), cancel: cancel, waiters: 1}
		s.calls[key] = call
		go func() {
			value, err := fn(callCtx)
			s.mu.Lock()
			if s.calls[key] == call {
				delete(s.calls, key)
			}
			call.value, call.err = value, err
			s.mu.Unlock()
			cancel()
			close(call.done)
		}()
	}
	s.mu.Unlock()

	select {
	case <-call.done:
		return call.value, call.err
	case <-ctx.Done():
		s.mu.Lock()
		call.waiters--
		last := call.waiters == 0
		if last && s.calls[key] == call {
			delete(s.calls, key)
		}
		s.mu.Unlock()
		if last {
			call.cancel()
		}
		var zero V
		return zero, ctx.Err()
	}
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
