package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/leapmux/leapmux/solo"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// startSolo launches a Hub and Worker in-process via the solo package.
// NoTCP is set so no TCP port is opened.
func (a *App) startSolo() error {
	inst, err := solo.Start(a.ctx, solo.Config{
		SkipBanner: true,
		NoTCP:      true,
	})
	if err != nil {
		return err
	}
	a.solo = inst

	// Wrap the default slog handler so log records are also forwarded
	// to the WebView console (for Web Inspector / F12).
	a.logHandler = newWebviewHandler(
		slog.Default().Handler(),
		func(js string) { wailsRuntime.WindowExecJS(a.ctx, js) },
	)
	slog.SetDefault(slog.New(a.logHandler))

	// The DOM is already ready (domReady fired during the launcher page),
	// so mark the handler ready immediately to flush any buffered logs.
	a.logHandler.SetReady()

	return nil
}

// stopSolo shuts down the in-process Hub and Worker.
func (a *App) stopSolo() {
	if a.solo == nil {
		return
	}
	// Restore the original inner handler before stopping.
	if a.logHandler != nil {
		slog.SetDefault(slog.New(a.logHandler.inner))
		a.logHandler = nil
	}
	a.solo.Stop()
	a.solo = nil
}

// waitForSoloReady polls for the Unix socket file until it exists.
func (a *App) waitForSoloReady(ctx context.Context, socketPath string) error {
	const (
		pollInterval = 100 * time.Millisecond
		timeout      = 30 * time.Second
	)

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
	return fmt.Errorf("LeapMux did not become ready within %s", timeout)
}
