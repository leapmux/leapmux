package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type failingSoloInstance struct {
	err error
}

func (f failingSoloInstance) LocalListenURL() string { return "" }
func (f failingSoloInstance) Wait() error            { return f.err }
func (f failingSoloInstance) Stop() error            { return f.err }

func TestAppShutdownReturnsSoloError(t *testing.T) {
	wantErr := errors.New("lease release failed")
	app := NewApp("")
	installTestConnection(app, mustNewProxy(t, "https://hub.example"), failingSoloInstance{err: wantErr}, "https://hub.example")

	require.ErrorIs(t, app.Shutdown(), wantErr)
	require.ErrorIs(t, app.Shutdown(), wantErr, "idempotent shutdown must retain its terminal error")
	require.Nil(t, app.connection)
}

func TestSwitchModeClearsStateWhenSoloStopFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	wantErr := errors.New("lease release failed")
	app := NewApp("")
	installTestConnection(app, mustNewProxy(t, "https://stale.example"), failingSoloInstance{err: wantErr}, "https://stale.example")
	app.config.Mode = "solo"
	app.config.HubURL = "https://stale.example"

	outcome, err := app.SwitchMode()
	require.NoError(t, err)
	require.Len(t, outcome.cleanupErrors, 1)
	require.ErrorIs(t, outcome.cleanupErrors[0], wantErr)
	require.Empty(t, app.config.Mode)
	require.Empty(t, app.config.HubURL)
	require.Nil(t, app.connection)
}

type blockingSoloInstance struct {
	entered chan struct{}
	release chan struct{}
}

func (b blockingSoloInstance) LocalListenURL() string { return "" }
func (b blockingSoloInstance) Wait() error            { return nil }
func (b blockingSoloInstance) Stop() error {
	close(b.entered)
	<-b.release
	return nil
}

func TestAppLifecycleOperationRejectsDuringShutdown(t *testing.T) {
	app := NewApp("")
	blocking := blockingSoloInstance{entered: make(chan struct{}), release: make(chan struct{})}
	installTestConnection(app, mustNewProxy(t, "https://hub.example"), blocking, "https://hub.example")
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- app.Shutdown() }()
	<-blocking.entered

	operationDone := make(chan error, 1)
	go func() { operationDone <- app.SetWindowSize(800, 600, WindowModeNormal) }()
	select {
	case err := <-operationDone:
		require.ErrorContains(t, err, "shutting down")
	case <-time.After(30 * time.Second):
		t.Fatal("lifecycle operation did not reject promptly during shutdown")
	}

	close(blocking.release)
	require.NoError(t, <-shutdownDone)
}

// A buffered signal must still start App.Shutdown. The registration is never
// stopped now, so nothing restores the default disposition: a signal that
// arrives while a drain runs is dropped, because force-terminating a process
// that is releasing the lease is what loses the lease.
func TestAwaitShutdownSignalStartsShutdown(t *testing.T) {
	app := NewApp("")
	sigCh := make(chan os.Signal, 1)
	sigCh <- syscall.SIGTERM

	awaitShutdownSignal(app, sigCh)

	require.ErrorIs(t, app.ctx.Err(), context.Canceled)
}

// awaitShutdownSignal must bow out when the App is shutting down through
// another path. It stops nothing: the registration lives until the process
// exits, so a late signal is dropped rather than escalated.
func TestAwaitShutdownSignalBowsOutOnContextCancel(t *testing.T) {
	app := NewApp("")

	sigCh := make(chan os.Signal, 1)
	watcherDone := make(chan struct{})

	go func() {
		awaitShutdownSignal(app, sigCh)
		close(watcherDone)
	}()

	// Trigger the normal shutdown path, which cancels app.ctx before cleanup.
	require.NoError(t, app.Shutdown())

	select {
	case <-watcherDone:
	case <-time.After(30 * time.Second):
		t.Fatal("awaitShutdownSignal did not return after context cancellation")
	}
}

// A blocked transport read must not prevent App shutdown from returning the
// session loop. Signal handling relies on this property so main can unwind and
// run all cleanup instead of calling os.Exit from a watcher goroutine.
func TestRPCSessionRunReturnsWhenAppShutsDown(t *testing.T) {
	app := NewApp("")
	reader, writer := io.Pipe()
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})

	runDone := make(chan error, 1)
	go func() {
		runDone <- NewRPCSession(app, reader, io.Discard, nil).Run()
	}()

	require.NoError(t, app.Shutdown())
	select {
	case err := <-runDone:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("RPCSession.Run stayed blocked in ReadFrame after App shutdown")
	}
}

func TestRunStdioSessionShutsDownAfterFatalReadError(t *testing.T) {
	app := NewApp("")
	err := runStdioSession(app, bytes.NewReader([]byte{1, 0xff}), io.Discard)

	require.Error(t, err)
	require.ErrorIs(t, app.ctx.Err(), context.Canceled)
}

// A clean stdio close (stdin EOF) must not turn a post-commit cleanup warning
// into a fatal session error. runStdioSession returns only the session error;
// cleanup warnings are surfaced separately by run's defer / LifecycleResult.
func TestRunStdioSessionCleanCloseIgnoresCleanupWarning(t *testing.T) {
	app := NewApp("")
	installTestConnection(app, mustNewProxy(t, "https://hub.example"), failingSoloInstance{err: errors.New("lease release failed")}, "https://hub.example")

	err := runStdioSession(app, bytes.NewReader(nil), io.Discard)
	require.NoError(t, err, "a clean close must not surface a cleanup warning as a session error")
	require.ErrorIs(t, app.ctx.Err(), context.Canceled, "the app must still shut down")
}
