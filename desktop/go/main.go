package main

import (
	"log/slog"
	"os"
)

// Cross-language contract with desktop/rust/src/main.rs:
//
//   envDevEndpoint — when set, the sidecar listens on this endpoint (unix
//     socket path on Unix, named-pipe name on Windows) instead of using
//     stdio. The Rust shell uses this path in debug builds to reconnect
//     to an already-running sidecar across reloads.
//   envBinaryHash — hash the Rust shell computed from the sidecar binary;
//     the sidecar reports it back so the shell can detect stale processes.
const (
	envDevEndpoint = "LEAPMUX_DESKTOP_DEV_ENDPOINT"
	envBinaryHash  = "LEAPMUX_DESKTOP_BINARY_HASH"
)

func main() {
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})
	slog.SetDefault(slog.New(handler))

	if endpoint := os.Getenv(envDevEndpoint); endpoint != "" {
		if err := RunSocketServer(endpoint, os.Getenv(envBinaryHash)); err != nil {
			slog.Error("desktop sidecar exited", "error", err)
			os.Exit(1)
		}
		return
	}

	app := NewApp(os.Getenv(envBinaryHash))
	defer app.Shutdown()

	session := NewRPCSession(app, os.Stdin, os.Stdout, func() {
		app.Shutdown()
	})
	if err := session.Run(); err != nil {
		slog.Error("desktop sidecar exited", "error", err)
		os.Exit(1)
	}
}
