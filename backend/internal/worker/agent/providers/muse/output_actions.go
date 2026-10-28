package muse

import (
	"errors"
	"sync"
)

var errMuseOutputLocked = errors.New("the Muse output dispatcher still owns the write")

// outputDispatcher owns publication locks separately from native session data.
// A retained enrichment needs these locks without retaining the whole agent.
type outputDispatcher struct {
	mu             sync.Mutex
	finalizationMu sync.Mutex
	actions        []func()
	draining       bool
}

// checkPublicationLocks refuses a callback that an ordered writer reaches
// while this dispatcher holds a lock. Observers may enter it again.
func (dispatcher *outputDispatcher) checkPublicationLocks() error {
	if dispatcher == nil {
		return nil
	}
	if !dispatcher.mu.TryLock() {
		return errMuseOutputLocked
	}
	dispatcher.mu.Unlock()
	if !dispatcher.finalizationMu.TryLock() {
		return errMuseOutputLocked
	}
	dispatcher.finalizationMu.Unlock()
	return nil
}

// deferOutputAction queues an output action after the caller releases dispatcher.mu.
func (a *Agent) deferOutputAction(apply func()) {
	a.dispatcher.actions = append(a.dispatcher.actions, apply)
}

// drainDeferredOutputActions preserves native arrival order through observer
// re-entry. Actions run after the calling dispatcher releases its mutex.
func (a *Agent) drainDeferredOutputActions() {
	a.dispatcher.mu.Lock()
	if a.dispatcher.draining {
		a.dispatcher.mu.Unlock()
		return
	}
	a.dispatcher.draining = true
	finished := false
	defer func() {
		if !finished {
			a.dispatcher.mu.Lock()
			a.dispatcher.draining = false
			a.dispatcher.mu.Unlock()
		}
	}()
	for {
		if len(a.dispatcher.actions) == 0 {
			a.dispatcher.actions = nil
			a.dispatcher.draining = false
			finished = true
			a.dispatcher.mu.Unlock()
			return
		}
		apply := a.dispatcher.actions[0]
		a.dispatcher.actions[0] = nil
		a.dispatcher.actions = a.dispatcher.actions[1:]
		a.dispatcher.mu.Unlock()
		apply()
		a.dispatcher.mu.Lock()
	}
}
