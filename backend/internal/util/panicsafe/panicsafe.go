// Package panicsafe holds the one recover-and-log policy the Hub's detached
// goroutines share.
//
// Several places spawn a goroutine whose failure is one connection's problem
// rather than the process's -- a CRDT audit, a channel teardown, a periodic
// task, a worker-close dispatch. Each recovered and logged the same way, in its
// own five-line block, and none of them captured a stack: a recovered panic
// arrived in the log as a bare value with no origin, which is the difference
// between a diagnosable fault and a mystery.
//
// Deliberately NOT for every recover in the tree. A goroutine that has to do
// something else on the way out -- re-panic on a programming invariant, fence a
// connection, reply to the caller in-band -- is not following this policy and
// should not be bent into it; a helper with a knob per exception would be worse
// than the copies it replaced.
package panicsafe

import (
	"log/slog"
	"runtime/debug"
)

// RecoverAndLog recovers a panic and reports it once at Error, with the caller's
// message and fields plus the recovered value and a stack.
//
// MUST be deferred DIRECTLY:
//
//	defer panicsafe.RecoverAndLog(logger, "…", "key", value)
//
// recover() returns a value only when it is called by the deferred function
// itself, so wrapping this in a closure -- defer func() { RecoverAndLog(…) }() --
// silently recovers NOTHING and the panic keeps unwinding past it. There is no
// way to detect that from in here, which is why it is stated this loudly.
//
// A nil logger resolves to slog.Default(), so a caller with no logger of its own
// passes nil rather than keeping a second spelling of this policy.
func RecoverAndLog(logger *slog.Logger, msg string, fields ...any) {
	r := recover()
	if r == nil {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	// Copied rather than appended in place: `fields` may share a backing array
	// with the caller's own slice, and this is already the unwinding path.
	attrs := make([]any, 0, len(fields)+4)
	attrs = append(attrs, fields...)
	attrs = append(attrs, "panic", r, "stack", string(debug.Stack()))
	logger.Error(msg, attrs...)
}
