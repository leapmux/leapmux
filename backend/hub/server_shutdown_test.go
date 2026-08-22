package hub

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
)

// A Hub that stops because something failed must SAY so, at the moment it
// stops. Reporting the cause only through the aggregate error Serve returns is
// what left a user's bug report showing a Hub that stopped for no stated
// reason.
func TestLogShutdownCauseReportsAFatalCause(t *testing.T) {
	logs := testutil.CaptureDefaultLogger(t)
	ctx, cancel := context.WithCancelCause(context.Background())
	fatal := errors.New("revocation watcher lease lost: holder h1")
	cancel(fatal)

	logShutdownCause(ctx)

	out := logs.String()
	assert.Contains(t, out, "hub shutting down after a fatal error")
	assert.Contains(t, out, "revocation watcher lease lost", "the cause must be readable without the returned error")
	assert.Contains(t, out, "level=ERROR", "a fatal stop must not be logged at INFO alongside ordinary shutdowns")
}

// An exit somebody asked for carries nothing worth a field, and must not be
// dressed up as a failure: a Ctrl-C that logged at ERROR would train operators
// to ignore the level that matters.
func TestLogShutdownCauseStaysQuietForARequestedStop(t *testing.T) {
	logs := testutil.CaptureDefaultLogger(t)
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(nil) // what a Ctrl-C reaching Serve's parent context looks like

	logShutdownCause(ctx)

	out := logs.String()
	assert.Contains(t, out, "hub shutting down...")
	assert.NotContains(t, out, "fatal")
	assert.NotContains(t, out, "level=ERROR")
}

type closeErrorListener struct {
	err error
}

func (*closeErrorListener) Accept() (net.Conn, error) { return nil, net.ErrClosed }
func (l *closeErrorListener) Close() error            { return l.err }
func (*closeErrorListener) Addr() net.Addr            { return &net.TCPAddr{} }

func TestServerTeardownErrorsPreserveServeAndCleanupFailures(t *testing.T) {
	serveErr := errors.New("serve failed")
	tcpListenerErr := errors.New("tcp listener failed")
	localListenerErr := errors.New("local listener failed")
	httpShutdownErr := errors.New("http shutdown failed")
	httpCloseErr := errors.New("http close failed")
	watcherErr := errors.New("watcher close failed")
	storeErr := errors.New("store close failed")

	err := serverTeardownErrors{
		primary:       serveErr,
		tcpListener:   tcpListenerErr,
		localListener: localListenerErr,
		httpShutdown:  httpShutdownErr,
		httpClose:     httpCloseErr,
		watcherClose:  watcherErr,
		storeClose:    storeErr,
	}.finalize()
	require.ErrorIs(t, err, serveErr)
	require.ErrorIs(t, err, tcpListenerErr)
	require.ErrorIs(t, err, localListenerErr)
	require.ErrorIs(t, err, httpShutdownErr)
	require.ErrorIs(t, err, httpCloseErr)
	require.ErrorIs(t, err, watcherErr)
	require.ErrorIs(t, err, storeErr)
	require.ErrorContains(t, err, "TCP listener")
	require.ErrorContains(t, err, "local listener")
	require.ErrorContains(t, err, "shut down HTTP server")
	require.ErrorContains(t, err, "force-close HTTP server")
	require.ErrorContains(t, err, "close revocation watcher")
	require.ErrorContains(t, err, "close store")
}

func TestServerTeardownErrorsReturnNilWithoutFailures(t *testing.T) {
	require.NoError(t, (serverTeardownErrors{}).finalize())
}

// A watcher lease-loss that raced a listener error into Serve's select is left
// buffered in Errors() when the listener case wins; foldPendingWatcherError must
// recover it into primary so the aggregate reports the most process-fatal cause.
func TestFoldPendingWatcherErrorRecoversRacedLeaseLoss(t *testing.T) {
	leaseLost := errors.New("revocation watcher lease lost")
	watcherErrors := make(chan error, 1)
	watcherErrors <- leaseLost

	// The listener case won the select, so primary carries only the listener error.
	teardownErrs := serverTeardownErrors{tcpListener: errors.New("serve: use of closed network connection")}
	teardownErrs.foldPendingWatcherError(watcherErrors)

	require.ErrorIs(t, teardownErrs.finalize(), leaseLost, "the raced lease-loss must not be dropped")
	require.ErrorContains(t, teardownErrs.finalize(), "revocation watcher failed")
}

// When the select already consumed the watcher error (primary set),
// foldPendingWatcherError must NOT drain the channel again or overwrite it.
func TestFoldPendingWatcherErrorSkipsWhenPrimarySet(t *testing.T) {
	watcherErrors := make(chan error, 1)
	watcherErrors <- errors.New("a second fatal that must be ignored")

	primary := errors.New("revocation watcher failed: original cause")
	teardownErrs := serverTeardownErrors{primary: primary}
	teardownErrs.foldPendingWatcherError(watcherErrors)

	require.Equal(t, primary, teardownErrs.primary, "an already-set primary must be preserved")
	require.Len(t, watcherErrors, 1, "the channel must not be drained when primary is already set")
}

// With no fatal buffered, foldPendingWatcherError must be a non-blocking no-op.
func TestFoldPendingWatcherErrorNoOpWhenEmpty(t *testing.T) {
	teardownErrs := serverTeardownErrors{}
	teardownErrs.foldPendingWatcherError(make(chan error, 1))
	require.NoError(t, teardownErrs.finalize())
}

func TestConstructionServerErrorsPreserveListenerCloseFailures(t *testing.T) {
	primaryErr := errors.New("construction failed")
	tcpErr := errors.New("close TCP failed")
	localErr := errors.New("close local failed")

	err := acquiredResources{
		tcpLn:   &closeErrorListener{err: tcpErr},
		localLn: &closeErrorListener{err: localErr},
	}.close(primaryErr)
	require.ErrorIs(t, err, primaryErr)
	require.ErrorIs(t, err, tcpErr)
	require.ErrorIs(t, err, localErr)
	require.ErrorContains(t, err, "TCP listener")
	require.ErrorContains(t, err, "local listener")
}
