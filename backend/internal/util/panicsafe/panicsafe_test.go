package panicsafe

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// logTo returns a logger writing JSON into buf, so a test can read back exactly
// which attributes a record carried rather than matching on a rendered line.
func logTo(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, nil))
}

func lastRecord(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	require.NotEmpty(t, buf.Bytes(), "expected a log record")
	var rec map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec))
	return rec
}

func TestRecoverAndLogSwallowsThePanicAndReportsIt(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	returned := false
	func() {
		defer RecoverAndLog(logTo(&buf), "boom", "worker_id", "w1")
		panic("exploded")
	}()
	returned = true

	assert.True(t, returned, "the goroutine's failure must not become the process's")

	rec := lastRecord(t, &buf)
	assert.Equal(t, "boom", rec["msg"])
	assert.Equal(t, "ERROR", rec["level"])
	assert.Equal(t, "w1", rec["worker_id"], "the caller's fields must survive")
	assert.Equal(t, "exploded", rec["panic"])
	assert.Contains(t, rec["stack"], "panicsafe",
		"a recovered panic with no stack is a value with no origin, which is what this helper exists to fix")
}

// The one way to hold it wrong, pinned so nobody "simplifies" the helper into a
// shape that must be wrapped. recover() returns a value only to the function the
// runtime deferred, so a closure between the two silently recovers nothing --
// and there is no way to detect that from inside.
func TestRecoverAndLogDoesNothingWhenWrappedInAClosure(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	escaped := func() (r any) {
		defer func() { r = recover() }()
		func() {
			// Deliberately wrong: the call the runtime defers is this closure,
			// not RecoverAndLog, so RecoverAndLog's own recover() sees nothing.
			defer func() { RecoverAndLog(logTo(&buf), "boom") }()
			panic("exploded")
		}()
		return nil
	}()

	assert.Equal(t, "exploded", escaped,
		"wrapped in a closure the panic keeps unwinding -- which is why the doc says MUST be deferred directly")
	assert.Empty(t, buf.String(), "and nothing is logged, so the mistake is silent")
}

func TestRecoverAndLogIsSilentWithoutAPanic(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	func() {
		defer RecoverAndLog(logTo(&buf), "boom")
	}()

	assert.Empty(t, buf.String(), "the ordinary path must not log")
}

// Three of the five call sites have no logger of their own.
func TestRecoverAndLogAcceptsANilLogger(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(logTo(&buf))
	t.Cleanup(func() { slog.SetDefault(prev) })

	func() {
		defer RecoverAndLog(nil, "boom", "channel_id", "c1")
		panic("exploded")
	}()

	rec := lastRecord(t, &buf)
	assert.Equal(t, "boom", rec["msg"])
	assert.Equal(t, "c1", rec["channel_id"])
	assert.Equal(t, "exploded", rec["panic"])
}
