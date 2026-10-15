// Package phasetrace supplies optional structured traces for performance and end-to-end tests.
// Each domain selects an environment variable and a fixed marker.
// Calls do nothing unless that variable equals "1".
// Typed domain wrappers keep marker strings out of individual call sites.
package phasetrace

import (
	"log/slog"
	"os"
)

// Tracer writes structured records with its marker as the first field.
type Tracer struct {
	enabled bool
	marker  string
	message string
}

// New enables a tracer when envVar equals "1" at construction.
// Later environment changes do not affect it.
// Each record carries marker and the top-level message.
func New(envVar, marker, message string) *Tracer {
	return &Tracer{
		enabled: os.Getenv(envVar) == "1",
		marker:  marker,
		message: message,
	}
}

// Enabled reports the fixed choice captured when the tracer was constructed.
func (t *Tracer) Enabled() bool { return t.enabled }

// Log writes an Info record when the tracer is enabled.
func (t *Tracer) Log(kvs ...any) {
	if !t.enabled {
		return
	}
	args := make([]any, 0, 2+len(kvs))
	args = append(args, "marker", t.marker)
	args = append(args, kvs...)
	slog.Info(t.message, args...)
}

// LogLazy builds the record fields only when the tracer is enabled.
func (t *Tracer) LogLazy(fields func() []any) {
	if t.enabled {
		t.Log(fields()...)
	}
}
