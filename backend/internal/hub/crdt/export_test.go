package crdt

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
