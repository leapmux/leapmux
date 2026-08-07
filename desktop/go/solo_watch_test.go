package main

import (
	"bytes"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// waitingSoloInstance reports a terminal error from Wait, standing in for a Hub
// that died on its own after the launch succeeded.
type waitingSoloInstance struct {
	err error
}

func (waitingSoloInstance) LocalListenURL() string { return "" }
func (waitingSoloInstance) Stop() error            { return nil }
func (w waitingSoloInstance) Wait() error          { return w.err }

// captureSlog points the default logger at a buffer for the duration of fn.
func captureSlog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	fn()
	return buf.String()
}

// A Hub that dies on its own is the case with no other reporter: soloInstance
// had no Wait, so the sidecar reported a successful launch and the UI's connect
// failure was never attributed to the Hub.
func TestWatchSoloInstance_ReportsAHubThatDiedOnItsOwn(t *testing.T) {
	wantErr := errors.New("revocation watcher failed")
	rt := &soloRuntime{
		instance: waitingSoloInstance{err: wantErr},
		stopping: make(chan struct{}),
	}

	out := captureSlog(t, func() { watchSoloInstance(rt) })

	assert.Contains(t, out, "the local hub stopped")
	assert.Contains(t, out, wantErr.Error())
}

// The other half, and the reason the guard exists: stopSolo already returns this
// error, and waitSolo documents itself as its single reporter. A second one here
// is the duplicate report the whole shutdown design removes.
func TestWatchSoloInstance_StaysQuietForATeardownWeAskedFor(t *testing.T) {
	rt := &soloRuntime{
		instance: waitingSoloInstance{err: errors.New("lease release failed")},
		stopping: make(chan struct{}),
	}
	close(rt.stopping) // what stopSolo does before calling Stop

	out := captureSlog(t, func() { watchSoloInstance(rt) })

	assert.Empty(t, out, "Stop owns this error; captured:\n"+out)
}

// A clean exit says nothing either way.
func TestWatchSoloInstance_SaysNothingForACleanExit(t *testing.T) {
	rt := &soloRuntime{
		instance: waitingSoloInstance{},
		stopping: make(chan struct{}),
	}

	out := captureSlog(t, func() { watchSoloInstance(rt) })

	assert.Empty(t, out)
}

// A soloRuntime built without a watcher -- which is every one the tests construct,
// since only defaultStartSolo launches one -- must survive stopSolo. Closing a nil
// channel panics, and stopSolo is reached from both Shutdown and SwitchMode.
func TestStopSolo_IsSafeOnARuntimeWithNoWatcher(t *testing.T) {
	rt := &soloRuntime{instance: waitingSoloInstance{}}

	require.NotPanics(t, func() { _ = stopSolo(rt) })
	require.NotPanics(t, func() { _ = stopSolo(rt) }, "both Shutdown and SwitchMode reach here")
}

// Silencing is idempotent: Shutdown and SwitchMode can both tear the same runtime
// down, and a second close of the same channel panics.
func TestStopSolo_SilencingTwiceDoesNotPanic(t *testing.T) {
	rt := &soloRuntime{
		instance: waitingSoloInstance{},
		stopping: make(chan struct{}),
	}

	require.NotPanics(t, func() { _ = stopSolo(rt) })
	require.NotPanics(t, func() { _ = stopSolo(rt) })
}

// stopSolo must close stopping BEFORE Stop, or the watcher races the teardown and
// reports an exit this process asked for.
func TestStopSolo_SilencesTheWatcherBeforeStopping(t *testing.T) {
	rt := &soloRuntime{
		instance: waitingSoloInstance{err: errors.New("lease release failed")},
		stopping: make(chan struct{}),
	}

	require.NoError(t, stopSolo(rt))

	select {
	case <-rt.stopping:
	default:
		t.Fatal("stopSolo must close stopping so the watcher knows the teardown was requested")
	}
}
