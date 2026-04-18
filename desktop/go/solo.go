package main

import (
	"log/slog"

	"github.com/leapmux/leapmux/solo"
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

	// Must happen after solo.Start(), which calls logging.Setup()
	// and replaces the default slog handler.
	prevHandler := slog.Default().Handler()
	logHandler := newWebviewHandler(prevHandler, a.EmitEvent)
	slog.SetDefault(slog.New(logHandler))
	a.prevLogHandler = prevHandler

	return nil
}

// stopSolo shuts down the in-process Hub and Worker.
func (a *App) stopSolo() {
	if a.solo == nil {
		return
	}
	a.solo.Stop()
	a.solo = nil

	// Restore original log handler to stop forwarding to the WebView.
	if a.prevLogHandler != nil {
		slog.SetDefault(slog.New(a.prevLogHandler))
		a.prevLogHandler = nil
	}
}
