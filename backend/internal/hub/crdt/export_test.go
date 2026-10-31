package crdt

import (
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
