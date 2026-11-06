package crdt

import (
	"log/slog"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// AlreadyMarshaledForTest reports whether this frame's marshal has already been
// paid, WITHOUT paying it.
//
// The property it exists to pin: by the time a frame reaches a subscriber's
// Send, it is already marshaled. The subscriber queue charges the frame against
// the shared byte budget on the way in and asking its size is what forces the
// marshal, so a frame arriving unmarshaled would serialize a proto -- up to a
// multi-megabyte batch frame -- inside the projection lock that gates every
// SubmitOps, presence update and projection read for that user.
func (e *MarshaledEvent) AlreadyMarshaledForTest() bool { return e.marshaled.Load() }

// EpochRotationForTest exposes epochRotation, the pure decision the compaction
// pass makes, so its boundaries can be tested without a manager.
func EpochRotationForTest(s *leapmuxv1.UserCrdtState, now time.Time) (int64, *timestamppb.Timestamp, bool) {
	return epochRotation(s, now)
}

// NewRegistryWithWedgedJanitorForTest returns a Registry whose janitor never
// reports that it stopped, so Shutdown spends its budget on that wait. A
// disabled janitor leaves no goroutine to race the swap.
func NewRegistryWithWedgedJanitorForTest(factory ManagerFactory, logger *slog.Logger) *Registry {
	r := NewRegistry(factory, logger, WithManagerIdleTTL(0))
	r.doneCh = make(chan struct{})
	return r
}

// PreparePendingPresenceClearForTest creates one owned resource that Discard must stop.
func (m *Manager) PreparePendingPresenceClearForTest() {
	m.presenceCtl.OnConnect("discard-observer")
	m.presenceCtl.OnDisconnect("discard-observer")
}

// PendingPresenceClearsForTest observes teardown without process-wide goroutine counts.
func (m *Manager) PendingPresenceClearsForTest() int {
	m.presenceCtl.mu.Lock()
	defer m.presenceCtl.mu.Unlock()
	return len(m.presenceCtl.clearTimers)
}
