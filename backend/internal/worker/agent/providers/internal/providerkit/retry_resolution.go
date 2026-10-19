package providerkit

import (
	"sync"

	"github.com/leapmux/leapmux/generated/contracts"
)

// LeapMuxNotificationServices writes a worker notification.
type LeapMuxNotificationServices interface {
	PersistLeapMuxNotification(content map[string]interface{})
}

// RetryResolution records whether the next model output resolves a pending retry.
type RetryResolution struct {
	mu      sync.Mutex
	pending bool
}

// NoteRetry records a persisted retry that the provider will attempt.
func (r *RetryResolution) NoteRetry() {
	r.mu.Lock()
	r.pending = true
	r.mu.Unlock()
}

// Resolve writes one resolution before the first model output after a retry.
// The sink runs outside the state lock.
func (r *RetryResolution) Resolve(sink LeapMuxNotificationServices) {
	r.mu.Lock()
	pending := r.pending
	r.pending = false
	r.mu.Unlock()
	if pending {
		sink.PersistLeapMuxNotification(map[string]interface{}{
			contracts.NotificationFieldType: contracts.NotificationTypeApiRetryResolved,
		})
	}
}

// Reset forgets a pending retry at the turn end or failure.
func (r *RetryResolution) Reset() {
	r.mu.Lock()
	r.pending = false
	r.mu.Unlock()
}
