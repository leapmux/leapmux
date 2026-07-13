package main

import (
	"io"
	"log/slog"
	"os"
)

// Cross-language contract with desktop/rust/src/main.rs:
//
//	envDevEndpoint — when set, the sidecar listens on this endpoint (unix
//	  socket path on Unix, named-pipe name on Windows) instead of using
//	  stdio. The Rust shell uses this path in debug builds to reconnect
//	  to an already-running sidecar across reloads.
//	envBinaryHash — hash the Rust shell computed from the sidecar binary;
//	  the sidecar reports it back so the shell can detect stale processes.
const (
	envDevEndpoint = "LEAPMUX_DESKTOP_DEV_ENDPOINT"
	envBinaryHash  = "LEAPMUX_DESKTOP_BINARY_HASH"
)

func main() {
	os.Exit(run())
}

func run() (exitCode int) {
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})
	slog.SetDefault(slog.New(handler))
	app := NewApp(os.Getenv(envBinaryHash))
	installSignalShutdown(app)
	defer func() {
		// Shutdown errors are post-commit cleanup warnings (relay/tunnel/solo
		// teardown, runtime-lease release), surfaced as non-fatal via
		// LifecycleResult.cleanup_errors on the RPC path. Log them here but do
		// not flip the exit code -- a successful session that hit a cleanup
		// warning is not a crash. exitCode is already set by the real-error
		// return paths below.
		if err := app.Shutdown(); err != nil {
			slog.Error("desktop sidecar shutdown cleanup failed", "error", err)
		}
	}()

	if endpoint := os.Getenv(envDevEndpoint); endpoint != "" {
		if err := RunSocketServer(endpoint, app); err != nil {
			slog.Error("desktop sidecar exited", "error", err)
			return 1
		}
		return 0
	}

	if err := runStdioSession(app, os.Stdin, os.Stdout); err != nil {
		slog.Error("desktop sidecar exited", "error", err)
		return 1
	}
	return 0
}

// runStdioSession runs a single stdio RPC session. A fatal transport read
// cancels the session and must also shut the App down (so a broken pipe does
// not leave the sidecar running), so Shutdown is invoked before returning.
// Only the session's own error is returned, though: post-commit cleanup
// warnings (relay/tunnel/lease teardown) are surfaced by run's defer and the
// RPC LifecycleResult.cleanup_errors path, not folded into the session error
// (which would flip an otherwise-clean close to a fatal exit).
func runStdioSession(app *App, reader io.Reader, writer io.Writer) error {
	session := NewRPCSession(app, reader, writer, func() {
		_ = app.Shutdown()
	})
	runErr := session.Run()
	_ = app.Shutdown()
	return runErr
}
