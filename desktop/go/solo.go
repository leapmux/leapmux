package main

import (
	"context"
	"log/slog"
	"os"
	"sync"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/solo"
)

type soloInstance interface {
	LocalListenURL() string
	Stop() error
	// Wait blocks until the Hub's serve loop ends and returns its terminal error.
	// It is what gives a Hub that dies ON ITS OWN a reporter: Stop only covers the
	// teardowns this process asked for.
	Wait() error
}

type soloRuntime struct {
	instance           soloInstance
	previousLogHandler slog.Handler
	// stopping is closed by stopSolo BEFORE it calls instance.Stop, so the watcher
	// below can tell a Hub that died from one this process asked to stop. Without
	// that distinction the watcher would re-report every ordinary shutdown, which
	// is the duplicate report solo's own watcher and the CLI's waitSolo are both
	// silent to avoid.
	//
	// Usable at its ZERO VALUE, which is what the tests need: they build bare
	// soloRuntime literals with only an instance, and only defaultStartSolo ever
	// launches a watcher to silence. A nil channel also reads correctly in the
	// watcher's select -- it can never be ready, so nothing is ever mistaken for a
	// requested teardown.
	stopping     chan struct{}
	stoppingOnce sync.Once
}

// silenceWatcher tells watchSoloInstance that what follows is a teardown this
// process asked for. Idempotent and nil-safe: Shutdown and SwitchMode both reach
// stopSolo, and a runtime built without a watcher has nothing to silence.
func (rt *soloRuntime) silenceWatcher() {
	if rt.stopping == nil {
		return
	}
	rt.stoppingOnce.Do(func() { close(rt.stopping) })
}

// desktopSoloConfig is the solo.Config the desktop sidecar always starts with.
//
// NoTCP keeps the packaged app off a default TCP port. When Rust's debug spawn
// sets LEAPMUX_HUB_DEV_FRONTEND, Args enable the hub DevProxy so Network Access
// extra listen addresses reach the same Vite/Bun server the webview uses.
// Release builds leave the env unset and keep the embedded SPA.
func desktopSoloConfig(devFrontend string) solo.Config {
	cfg := solo.Config{
		SkipBanner: true,
		NoTCP:      true,
	}
	if devFrontend != "" {
		cfg.Args = []string{"-dev-frontend", devFrontend}
	}
	return cfg
}

func desktopSoloConfigFromEnv() solo.Config {
	return desktopSoloConfig(os.Getenv(contracts.EnvDevFrontend))
}

// defaultStartSolo launches a Hub and Worker in-process via the solo package.
// NoTCP is set so no TCP port is opened. It is the default for App.startSolo,
// which is a function field so tests can substitute a blocking startup and
// assert lifecycleMu is not held across it.
func (a *App) defaultStartSolo(ctx context.Context) (*soloRuntime, error) {
	inst, err := solo.Start(ctx, desktopSoloConfigFromEnv())
	if err != nil {
		return nil, err
	}

	// Must happen after solo.Start(), which calls logging.Setup()
	// and replaces the default slog handler.
	prevHandler := slog.Default().Handler()
	logHandler := newWebviewHandler(prevHandler, a.EmitEvent)
	slog.SetDefault(slog.New(logHandler))
	rt := &soloRuntime{
		instance:           inst,
		previousLogHandler: prevHandler,
		stopping:           make(chan struct{}),
	}

	go watchSoloInstance(rt)
	return rt, nil
}

// watchSoloInstance is the desktop's only reporter for a Hub that dies ON ITS
// OWN. solo.Start returns once the startup is past its gates, and from then on
// the CLI learns of a Hub failure through waitSolo -- but this process had no
// equivalent, so the sidecar announced a successful launch and the UI then failed
// to connect with nothing naming the Hub.
//
// It reports NOTHING for a teardown this process asked for: stopSolo closes
// rt.stopping before it calls Stop, and Stop's return is that error's single
// reporter. Logging here as well would restore exactly the duplicate report
// solo's own watcher and the CLI's waitSolo are both silent to avoid.
//
// slog rather than a desktop event because the handler defaultStartSolo installs
// forwards it to the webview, so it lands in the app's own log with no new
// plumbing.
func watchSoloInstance(rt *soloRuntime) {
	err := rt.instance.Wait()
	select {
	case <-rt.stopping:
		return
	default:
	}
	if err != nil {
		slog.Error("the local hub stopped", "error", err)
	}
}

// stopSolo shuts down the in-process Worker and Hub, in that order. It is
// deliberately called OUTSIDE a.lifecycleMu (see disconnectAndStopSolo):
// instance.Stop() blocks on the Worker's drain and then the Hub's full graceful
// shutdown, so holding the lifecycle write lock across it would wedge every
// SidecarInfo/ProxyHTTP/SendChannelMessage reader for that window. It mutates
// only the process-global slog default, not lifecycle state.
func stopSolo(runtime *soloRuntime) error {
	if runtime == nil {
		return nil
	}
	// Before Stop, so the watcher goroutine can never mistake this teardown for a
	// Hub that failed on its own: Stop's return is this error's single reporter.
	runtime.silenceWatcher()
	err := runtime.instance.Stop()

	// Restore original log handler to stop forwarding to the WebView.
	if runtime.previousLogHandler != nil {
		slog.SetDefault(slog.New(runtime.previousLogHandler))
	}
	return err
}
