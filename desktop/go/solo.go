package main

import (
	"context"
	"log/slog"

	"github.com/leapmux/leapmux/solo"
)

type soloInstance interface {
	LocalListenURL() string
	Stop() error
}

type soloRuntime struct {
	instance           soloInstance
	previousLogHandler slog.Handler
}

// defaultStartSolo launches a Hub and Worker in-process via the solo package.
// NoTCP is set so no TCP port is opened. It is the default for App.startSolo,
// which is a function field so tests can substitute a blocking startup and
// assert lifecycleMu is not held across it.
func (a *App) defaultStartSolo(ctx context.Context) (*soloRuntime, error) {
	inst, err := solo.Start(ctx, solo.Config{
		SkipBanner: true,
		NoTCP:      true,
	})
	if err != nil {
		return nil, err
	}

	// Must happen after solo.Start(), which calls logging.Setup()
	// and replaces the default slog handler.
	prevHandler := slog.Default().Handler()
	logHandler := newWebviewHandler(prevHandler, a.EmitEvent)
	slog.SetDefault(slog.New(logHandler))
	return &soloRuntime{instance: inst, previousLogHandler: prevHandler}, nil
}

// stopSolo shuts down the in-process Hub and Worker. It is deliberately called
// OUTSIDE a.lifecycleMu (see disconnectLocked): instance.Stop() blocks on the
// Hub's full graceful shutdown, so holding the lifecycle write lock across it
// would wedge every SidecarInfo/ProxyHTTP/SendChannelMessage reader for that
// window. It mutates only the process-global slog default, not lifecycle state.
func stopSolo(runtime *soloRuntime) error {
	if runtime == nil {
		return nil
	}
	err := runtime.instance.Stop()

	// Restore original log handler to stop forwarding to the WebView.
	if runtime.previousLogHandler != nil {
		slog.SetDefault(slog.New(runtime.previousLogHandler))
	}
	return err
}
