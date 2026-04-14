package main

import (
	"log/slog"
	"os"
)

func main() {
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})
	slog.SetDefault(slog.New(handler))

	if socketPath := os.Getenv("LEAPMUX_DESKTOP_DEV_SOCKET"); socketPath != "" {
		if err := RunSocketServer(socketPath, os.Getenv("LEAPMUX_DESKTOP_BINARY_HASH")); err != nil {
			slog.Error("desktop sidecar exited", "error", err)
			os.Exit(1)
		}
		return
	}

	app := NewApp(os.Getenv("LEAPMUX_DESKTOP_BINARY_HASH"))
	defer app.Shutdown()

	session := NewRPCSession(app, os.Stdin, os.Stdout, func() {
		app.Shutdown()
	})
	if err := session.Run(); err != nil {
		slog.Error("desktop sidecar exited", "error", err)
		os.Exit(1)
	}
}
