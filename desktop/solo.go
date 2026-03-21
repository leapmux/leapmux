package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/leapmux/leapmux/solo"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// startSolo launches a Hub and Worker in-process via the solo package.
func (a *App) startSolo() error {
	inst, err := solo.Start(a.ctx, solo.Config{
		SkipBanner: true,
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

// waitForSoloReady polls the solo instance until it responds to GetSystemInfo.
func (a *App) waitForSoloReady(ctx context.Context) error {
	const (
		pollInterval = 200 * time.Millisecond
		timeout      = 30 * time.Second
	)

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := probeHub("http://127.0.0.1:4327"); err == nil {
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
