package main

import (
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

// installSignalShutdown arranges for the first SIGINT or SIGTERM to trigger a
// graceful shutdown before the sidecar returns through its normal transport
// path.
//
// The signal does NOT come from the Tauri shell. It spawns the sidecar as a plain
// std::process::Child, stores it in a field it never reads, and sends no signal
// anywhere: app quit is a cooperative Shutdown RPC, and the one force-kill primitive
// that could SIGTERM (force_kill_sidecar) was deleted for trusting a PID the peer
// reported about itself. So a signal here is from whoever ELSE can reach the process
// group -- a Ctrl-C in the terminal running `task dev`, an operator's `kill`, or the
// OS at logout/reboot.
//
// That makes this handler the only thing standing between those paths and a lost
// lease. With none installed, Go's default SIGINT/SIGTERM disposition exits the
// process immediately, skipping App.Shutdown and orphaning the Hub's DB-backed
// runtime lease -- which then fences the next launch until its 30s TTL elapses (the
// "quit and restart shortly after" startup failure).
//
// App.Shutdown -> solo.Stop waits for the Hub's full graceful shutdown,
// including the revocation watcher's lease release. RPCSession.Run observes App
// cancellation independently of a blocked transport read, so both stdio and
// socket modes unwind normally after cleanup.
func installSignalShutdown(app *App) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		awaitShutdownSignal(app, sigCh, func() { signal.Stop(sigCh) })
	}()
}

// awaitShutdownSignal runs the signal-to-shutdown sequence. It is split from
// installSignalShutdown so ordering is unit-testable without real signals.
//
// If the App is already shutting down through another path (a Shutdown RPC or
// the idle timeout), app.ctx is cancelled and this goroutine bows out via the
// first select case; the caller-owned Shutdown handles cleanup.
func awaitShutdownSignal(app *App, sigCh <-chan os.Signal, stopNotify func()) {
	select {
	case <-app.ctx.Done():
		stopNotify()
		return
	case sig := <-sigCh:
		// Restore the default disposition before potentially blocking cleanup so
		// a second SIGINT/SIGTERM can still force termination.
		stopNotify()
		slog.Info("desktop sidecar received signal, shutting down", "signal", sig)
		if err := app.Shutdown(); err != nil {
			slog.Error("desktop sidecar graceful shutdown failed", "error", err)
		}
	}
}
