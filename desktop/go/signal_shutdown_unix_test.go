//go:build !windows

package main

import (
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// installSignalShutdown must intercept a real SIGTERM and drive graceful
// shutdown. This is the regression guard for the bug:
// previously the sidecar had no signal handler, so a SIGTERM -- a Ctrl-C in the
// terminal running `task dev`, an operator's `kill`, or the OS at logout; never
// the Tauri shell, which only ever asks over RPC -- killed it instantly and
// orphaned the Hub runtime lease (fencing the next launch until its 30s TTL).
// signal.Notify intercepts the signal so the test binary itself is not terminated.
func TestInstallSignalShutdownReleasesOnSIGTERM(t *testing.T) {
	app := NewApp("")

	installSignalShutdown(app)

	require.NoError(t, syscall.Kill(syscall.Getpid(), syscall.SIGTERM))

	select {
	case <-app.ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("App was not shut down after SIGTERM")
	}
	require.NoError(t, app.Shutdown(), "signal-driven cleanup must finish successfully")
}

// A real signal that arrives while a drain is parked must not terminate the
// process: the registration is never stopped, so nothing restores the default
// disposition. Without that, the second SIGTERM would kill the whole test
// binary while solo.Stop parks below.
func TestSignalDuringAParkedDrainDoesNotForceTerminate(t *testing.T) {
	app := NewApp("")
	blocking := blockingSoloInstance{entered: make(chan struct{}), release: make(chan struct{})}
	installTestConnection(app, mustNewProxy(t, "https://hub.example"), blocking, "https://hub.example")
	installSignalShutdown(app)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(blocking.release) }) }
	t.Cleanup(release)

	require.NoError(t, syscall.Kill(syscall.Getpid(), syscall.SIGTERM))
	<-blocking.entered

	// The drain is now parked in solo.Stop. This second signal must be
	// dropped: with the default disposition restored it would kill the
	// process here.
	require.NoError(t, syscall.Kill(syscall.Getpid(), syscall.SIGTERM))

	release()
	select {
	case <-app.ctx.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("the drain did not finish after the release")
	}
	require.NoError(t, app.Shutdown())
}
