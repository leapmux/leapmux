//go:build !windows

package main

import (
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
