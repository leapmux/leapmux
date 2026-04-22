// Package phasetrace provides env-var-gated structured timing traces
// used by perf e2e tests to reconstruct phase timelines from stderr.
// Production logs are unaffected: when the gating env var is not set
// to "1", every call is a single-bool-check no-op.
//
// Each domain constructs one Tracer with a fixed marker + message and
// exposes a typed wrapper (e.g. agent.TraceStartupPhase,
// service.traceTabClosePhase) so call sites don't pass marker strings
// directly.
package phasetrace

import (
	"log/slog"
	"os"
)

// Tracer emits structured timing log lines when its gate env var is
// "1". The marker is written as the first key-value pair on every
// line so grep-based log parsers can filter on it cheaply.
type Tracer struct {
	enabled bool
	marker  string
	message string
}

// New returns a Tracer gated on os.Getenv(envVar) == "1". The env var
// is read once at construction time; toggling it at runtime has no
// effect. marker is the value written as the "marker" key on every
// emitted line; message is the slog record's top-level message.
func New(envVar, marker, message string) *Tracer {
	return &Tracer{
		enabled: os.Getenv(envVar) == "1",
		marker:  marker,
		message: message,
	}
}

// Log emits a slog.Info record with marker=<marker> prepended to the
// caller-supplied key-value pairs. No-op when the tracer is disabled.
func (t *Tracer) Log(kvs ...any) {
	if !t.enabled {
		return
	}
	args := make([]any, 0, 2+len(kvs))
	args = append(args, "marker", t.marker)
	args = append(args, kvs...)
	slog.Info(t.message, args...)
}
