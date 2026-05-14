package remote

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// Out / Err are package-level writers callers can override in tests.
//
// Successful and failed JSON envelopes both go to Out so a script
// running `leapmux remote … | jq` sees a single coherent stream of
// output. The non-zero exit code (returned via the error wrapping
// EmitError emits) is the only signal a caller needs to distinguish
// success from failure; mixing JSON across stdout and stderr would
// force every consumer into `cmd 2>&1 | jq`-style hacks that
// re-introduce the noise stderr was meant to filter out.
//
// Plain-prose interactive output from the auth flow is also routed
// through Out; that's deliberate (a user reading the device-code
// prompt is not piping the output into jq), and the few commands
// that print prose live in `auth.go` and don't share their stdout
// with a JSON envelope in the same invocation.
//
// Err is reserved for diagnostics that fall outside the JSON
// contract: warnings, debug logs, or catastrophic errors raised
// before the dispatcher reached an EmitError call site.
var (
	Out io.Writer = os.Stdout
	Err io.Writer = os.Stderr
)

// EmitData writes a successful JSON response.
func EmitData(v any) error {
	return writeJSON(Out, map[string]any{"data": v})
}

// EmitError writes an error JSON response and returns a non-nil error
// so the caller can `return EmitError(...)`. The envelope goes to Out
// alongside successful responses; the returned error propagates up
// to main.handleRunError which sets the process exit code to 1.
//
// The returned error implements `EmittedError() bool` so the
// top-level error handler can suppress its plain-text "error: …"
// fallback for failures that were already surfaced as a JSON
// envelope here. Catastrophic errors that bypass EmitError (for
// example, an init-time crash before any command runs) still get
// the fallback so the user isn't left staring at a silent failure.
func EmitError(code, message string) error {
	_ = writeJSON(Out, map[string]any{"error": map[string]string{"code": code, "message": message}})
	return &emittedError{code: code, message: message}
}

// emittedError carries an EmittedError marker so main.handleRunError
// knows the JSON envelope already reached the user.
type emittedError struct {
	code    string
	message string
}

func (e *emittedError) Error() string { return fmt.Sprintf("%s: %s", e.code, e.message) }

// EmittedError reports that the underlying CLI command already wrote
// the standard `{"error": {...}}` envelope to stdout. Implemented by
// the error returned from EmitError; checked at the top of the
// dispatcher so it doesn't double-print.
func (e *emittedError) EmittedError() bool { return true }

// IsEmitted reports whether err (or any error in its chain) was
// produced by EmitError / EmitErrorWith. Top-level handlers use this
// to decide whether to write a plain-text fallback to stderr.
func IsEmitted(err error) bool {
	var e interface{ EmittedError() bool }
	return errors.As(err, &e) && e.EmittedError()
}

// EmitErrorWith wraps a Go error into the standard envelope.
func EmitErrorWith(code string, err error) error {
	return EmitError(code, err.Error())
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
