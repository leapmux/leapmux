package main

import (
	"log/slog"
	"os"
)

func main() {
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})
	slog.SetDefault(slog.New(handler))

	server := NewRPCServer(os.Stdin, os.Stdout)
	if err := server.Run(); err != nil {
		slog.Error("desktop sidecar exited", "error", err)
		os.Exit(1)
	}
}
