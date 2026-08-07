// Package solo provides an in-process launcher for the LeapMux Hub and Worker,
// suitable for solo/desktop mode where both run inside the same binary.
package solo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/leapmux/leapmux/hub"
	hubconfig "github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/logging"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	workerconfig "github.com/leapmux/leapmux/internal/worker/config"
	"github.com/leapmux/leapmux/locallisten"
	"github.com/leapmux/leapmux/worker"

	"github.com/leapmux/leapmux/util/drain"
)

// workerSetupPollInterval is how often dev mode re-checks whether the first
// admin user has completed the /setup flow so the auto-registered local
// worker can come online.
const workerSetupPollInterval = 2 * time.Second

// workerDrainTimeout bounds how long the ordered shutdown waits for the Worker
// to finish draining before it stops holding the Hub open for it. See
// Instance.shutdown for why the Worker goes first at all.
//
// Sized against the drain it is waiting on: the Worker's own outbound flush is
// bounded by hub.ShutdownFlushTimeout (5s), and everything before it --
// broadcasting the terminal goodbye, stopping the background loops, draining
// in-flight starts -- is fast on an idle machine and unbounded on a busy one.
// So this is not a promise the drain fits; it is the point at which a Hub that
// is trying to exit stops waiting for a Worker that is not coming.
//
// A var so tests can shorten it, matching operationDrainTimeout and
// relayDrainTimeout in the desktop sidecar.
var workerDrainTimeout = 5 * time.Second

// serveHub runs the Hub. A var so tests can make Serve fail or exit on demand.
//
// It has to be a seam because hub.NewServer binds BOTH listeners before Serve is
// ever called, so no fixture can induce a Serve-TIME failure by occupying an
// address or pointing at an unwritable path -- every such attempt fails earlier,
// in NewServer, on a path that never reaches the watcher. Without this, the
// "the Hub died while serving" arm and the gate that stops a startup failure
// from being reported twice both had no coverage, and two tests aimed at them
// were passing on a NewServer failure instead.
var serveHub = func(ctx context.Context, s *hub.Server) error { return s.Serve(ctx) }

// bringUpWorker brings the local Worker up. A var so a test can park bring-up
// mid-flight and kill the Hub underneath it.
//
// It has to be a seam for the same reason serveHub does: nothing reaches the case
// from outside. Every real way to fail bring-up -- no admin user yet, an
// unreadable state file, a store error -- fails AT ONCE, at the first statement
// that touches the database or the disk, so no fixture can hold Start inside the
// bring-up window long enough for the Hub to die there. Without this, the gate
// that keeps a dead Hub from being blamed on the Worker, and the one that stops
// Start returning a healthy Instance for a Hub that already exited, both had no
// coverage at all.
//
// A package var rather than an Instance field because Start constructs the
// Instance itself: at the point a test would have to inject, there is nothing to
// inject into.
//
// bringUpLocalWorker stays directly callable, and
// TestSolo_AHubThatDiesWhileServingTearsTheInstanceDownWithoutReporting and
// TestSolo_AStopRequestedHubErrorIsReportedOnlyByStop drive Start through the
// real implementation, so the seam cannot drift away from what production does.
var bringUpWorker = bringUpLocalWorker

// nonLoopbackListenWarnMsg is the security warning emitted when solo mode
// binds to a non-loopback address. Solo mode injects a soloUser into the
// auth interceptor (see hub.NewServer and auth.NewInterceptor), so every
// request is auto-authenticated as the admin without credentials. Loopback
// keeps that contained to the host; anything else hands the admin role to
// anyone who can reach the port.
const nonLoopbackListenWarnMsg = "solo mode is binding to a non-loopback address — every request is auto-authenticated as the admin, " +
	"so anyone who can reach this port has full admin access without credentials. " +
	"Restrict access externally (firewall, Tailscale/WireGuard, SSH tunnel) or run `leapmux hub` for real authentication."

// Config configures the solo launcher.
type Config struct {
	// Listen is the TCP listen address (default: "127.0.0.1:4327").
	Listen string
	// ConfigDir overrides the default config directory.
	ConfigDir string
	// ConfigFile overrides the default config file path.
	ConfigFile string
	// Args are additional CLI flag arguments (passed to hub config loader).
	Args []string
	// CLIFlags restricts which flags are registered (nil = all hub flags).
	CLIFlags []string
	// ExtraFlags registers additional koanf-backed flags; nil uses the
	// desktop-oriented default (encryption-mode, use-login-shell).
	ExtraFlags []hubconfig.ExtraFlagDef
	// DevMode runs in dev mode (binds to all interfaces, logs "dev" banner).
	DevMode bool
	// SkipBanner suppresses the ASCII art banner and access URL.
	SkipBanner bool
	// NoTCP disables the TCP listener. When true, the Hub only listens on
	// the Unix domain socket. This is used by the desktop app to avoid
	// opening a TCP port.
	NoTCP bool
}

// Instance represents a running solo Hub+Worker pair.
//
// The Hub and the Worker get SEPARATE cancellation, and neither is a child of
// the context handed to Start. That detachment is the whole point: cancelling
// one context would tear both down at the same instant, and the Worker always
// won that race -- it closes its Connect stream in a couple of syscalls while
// the Hub is still working through its own shutdown -- so the Hub's shutdown
// notification landed on a stream that was already gone and the Worker's
// goodbye landed on a Hub that had already fenced it. See Instance.shutdown for
// the order they are cancelled in, and why.
//
// The cancels and the worker counter are usable at their zero values, which is
// what the TESTS need: solo_test.go builds bare &Instance{} literals to drive
// the teardown ordering without standing a Hub up. Production always goes
// through Start, which sets both cancels in the same struct literal. (hubDone is
// the exception -- Wait parks on it, so an Instance that will be waited on has
// to have one.)
type Instance struct {
	shutdownOnce sync.Once
	cancelWorker context.CancelFunc
	cancelHub    context.CancelFunc
	// workerWork covers the Worker goroutine, the dev-mode setup poller (which
	// can launch that goroutine after Start has returned), and Start's own
	// bring-up. It reports FULL exit, which is what Stop must join: worker.Run
	// closes the Worker's database as it unwinds.
	//
	// A drain.Counter rather than a sync.WaitGroup because shutdown has to wait
	// with a DEADLINE, and bridging a WaitGroup to a bounded wait needs a
	// spawned waiter that leaks when the wait is abandoned -- the leak the
	// sidecar already retired under issue #297.
	workerWork drain.Counter
	// workerDrained reports the narrower thing the HUB actually has to outlive:
	// the goodbye broadcast and the outbound flush, announced by worker.Run's
	// Drained hook before its local teardown. Waiting on workerWork instead
	// would hold the Hub up for a database close it has no stake in, and report
	// a slow one as a worker that "did not finish draining".
	workerDrained drain.Counter
	listenURL     string
	hubErr        error         // set before hubDone is closed
	hubDone       chan struct{} // closed when the Hub goroutine exits
	// watcherDone closes when the goroutine that turns a cancelled parent
	// context (or a Hub that exited on its own) into an ordered teardown has
	// finished. join waits for it so no report or teardown step can still be in
	// flight after Stop returns.
	watcherDone chan struct{}
}

// LocalListenURL returns the URL at which the Hub is accepting local IPC
// connections (unix:<path> on Unix, npipe:<name> on Windows). Callers that
// need to dial the Hub from within the same process tree (e.g. the desktop
// proxy) should use this rather than reconstructing a path.
func (i *Instance) LocalListenURL() string {
	return i.listenURL
}

// Wait blocks until the Hub exits (either via Stop or because it failed
// on its own) and returns its terminal error. Returns nil on clean
// shutdown or http.ErrServerClosed. Safe to call multiple times.
func (i *Instance) Wait() error {
	<-i.hubDone
	return i.hubErr
}

// shutdown runs the ordered teardown exactly once: the Worker drains FIRST,
// against a Hub that is still serving, and only then does the Hub begin its own
// graceful shutdown.
//
// The order is what makes the Worker's last words deliverable. worker.Run
// already runs its drain before tearing its own stream down, and that drain
// broadcasts the terminal "[Worker disconnected - Press Enter to restart]"
// notice -- documented in Service.Shutdown as the only chance a browser has to
// learn its terminal died, because the process is about to exit and nothing
// replays afterwards. Cancelling both at once defeated it from the other side:
// the Hub fences every worker connection early in its shutdown and stops the
// auth leases holding the channel relays open, so the notice was written to a
// transport that had already been torn down.
//
// It is idempotent and safe on a zero-value Instance, because it is reached
// from three directions -- Stop, the caller's context being cancelled, and the
// Hub exiting on its own -- and any of them can arrive first.
func (i *Instance) shutdown() {
	i.shutdownOnce.Do(func() {
		if i.cancelWorker != nil {
			i.cancelWorker()
		}
		// Waits for the DRAIN, not for the Worker's exit: everything after the
		// drain is local teardown the Hub has no reason to outlive. The timeout
		// is a backstop for a Worker that never reports draining at all, not the
		// mechanism -- an ordinary shutdown returns here as soon as the last
		// frame is away. Stop still joins the full exit afterwards, unbounded;
		// see its doc for why the database close is not optional.
		i.workerDrained.Wait(workerDrainTimeout,
			"worker did not report draining before the hub shutdown deadline; stopping the hub anyway")
		if i.cancelHub != nil {
			i.cancelHub()
		}
	})
}

// Stop gracefully shuts down the Worker and Hub, in that order, and returns the
// Hub's terminal error, including failures from shutdown cleanup such as runtime
// lease release.
//
// The final Worker wait is deliberately unbounded even though shutdown's is not:
// worker.Run closes the Worker's database as it unwinds, and returning from Stop
// with that still in flight would let the next ConnectSolo reopen the same file
// underneath it. A Worker that overran the drain budget has already cost the Hub
// nothing further by this point -- the Hub is stopped.
//
// It reports EXACTLY the Hub's terminal error. A Worker that failed to drain is
// logged by shutdown and never joined in: waitSolo documents itself as the single
// reporter of that error, and widening what Stop returns would make it report
// something else.
func (i *Instance) Stop() error {
	i.join()
	return i.Wait()
}

// join runs the ordered teardown and waits out both halves. It is the ONE
// spelling of the wait set: Stop uses it, and so does every Start failure arm,
// which used to hand-roll three different subsets of it and stay correct only
// by accident of which goroutines happened to be running yet.
//
// The Worker wait is deliberately unbounded even though shutdown's is not:
// worker.Run closes the Worker's database as it unwinds, and returning with that
// still in flight would let the next ConnectSolo reopen the same file underneath
// it. By this point the Hub is already stopped, so a slow Worker costs it
// nothing further.
func (i *Instance) join() {
	i.shutdown()
	<-i.hubDone
	<-i.workerWork.DoneChan()
	// Last, because the watcher wakes on hubDone: joining it here is what makes
	// "Stop returned" mean the whole teardown is over, rather than leaving its
	// report to land on a caller that has already moved on.
	if i.watcherDone != nil {
		<-i.watcherDone
	}
}

// hubFailure reports the startup error to attribute to a Hub that has already
// exited, or nil if it was still serving when asked. stage names the startup
// window, for the case where the Hub exited without an error of its own.
//
// It exists because "has the Hub already exited?" has to be asked at the STAGE
// BOUNDARIES of startup, not inside the arms of a select or a switch: hub.NewServer
// binds both listeners before Serve is ever called, so a bare connect-and-close
// readiness probe succeeds against a Hub whose Serve has already returned an error,
// and every arm that asked the question separately answered it differently.
//
// It is TOTAL once hubDone is closed: it never returns nil for an exited Hub. The
// readiness select relies on that, so its hubDone arm can decide only to stop
// waiting and carry no error of its own.
//
// Reading hubErr after observing hubDone needs no further synchronisation: the Hub
// goroutine assigns it before closing hubDone. A nil hubDone reads as "still
// serving", which keeps this usable on a half-built Instance for the same reason
// shutdown and join are.
//
// This is a SAMPLE, not a guarantee: a Hub that exits a moment later still yields a
// healthy Instance, which is Wait's job to report. It closes the wide windows, not
// every window.
func (i *Instance) hubFailure(ctx context.Context, stage string) error {
	select {
	case <-i.hubDone:
	default:
		return nil
	}
	// hubErr first, deliberately: a genuine teardown failure (lease release, store
	// close) that coincides with a cancel is more information than context.Canceled,
	// and %w keeps errors.Is(err, context.Canceled) working for a caller that wants
	// to tell the two apart.
	if i.hubErr != nil {
		return fmt.Errorf("hub serve: %w", i.hubErr)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("hub shut down %s: %w", stage, err)
	}
	return fmt.Errorf("hub serve exited %s", stage)
}

// Start launches a Hub and Worker in-process. It returns an Instance that
// can stop the services and report their terminal cleanup error.
//
// A nil error means the Hub had not exited by the time Start returned -- not that
// it is still alive. Nothing can promise the latter: the Hub can fail one
// instruction after the return. Once Start has succeeded, Wait and Stop are the
// reporters of a Hub that dies; see the watcher goroutine below for why they are
// the only ones.
func Start(ctx context.Context, cfg Config) (*Instance, error) {
	logging.Setup()

	modeName := "solo"
	description := "Run Hub + Worker locally for single-user use."
	if cfg.DevMode {
		modeName = "dev"
		description = "Run Hub + Worker together for development."
	}
	flagSetName := "leapmux " + modeName

	defaultListen := cfg.Listen
	if defaultListen == "" {
		defaultListen = "127.0.0.1:4327"
		if cfg.DevMode {
			defaultListen = ":4327"
		}
	}

	configDir := cfg.ConfigDir
	if configDir == "" {
		configDir = "~/.config/leapmux/" + modeName
	}
	configFile := cfg.ConfigFile
	if configFile == "" {
		configFile = configDir + "/" + modeName + ".yaml"
	}

	cliFlags := cfg.CLIFlags
	if cliFlags == nil {
		cliFlags = defaultCLIFlags(cfg.DevMode)
	}

	extraFlags := cfg.ExtraFlags
	if extraFlags == nil {
		extraFlags = defaultExtraFlags()
	}

	hubCfg, _, err := hubconfig.LoadWithOptions(cfg.Args, hubconfig.LoadOptions{
		DefaultListen:     defaultListen,
		DefaultConfigDir:  configDir,
		DefaultConfigFile: configFile,
		FlagSetName:       flagSetName,
		Description:       description,
		CLIFlags:          cliFlags,
		ExtraFlags:        extraFlags,
		SoloMode:          !cfg.DevMode,
	})
	if err != nil {
		return nil, fmt.Errorf("load hub config: %w", err)
	}
	hubCfg.DevMode = cfg.DevMode

	if shouldWarnNonLoopback(cfg, hubCfg.Listen) {
		slog.Warn(nonLoopbackListenWarnMsg, "listen", hubCfg.Listen)
	}

	level, err := logging.ParseLevel(hubCfg.LogLevel)
	if err != nil {
		return nil, fmt.Errorf("invalid log level: %w", err)
	}
	logging.SetLevel(level)

	if cfg.NoTCP {
		hubCfg.Listen = ""
	}

	if !cfg.SkipBanner {
		logging.PrintBanner(modeName)
		logging.PrintBannerURL(hubCfg.PublicURL, hubCfg.Listen)
	}

	// Split data dir into hub and worker subdirectories.
	dataDir := hubCfg.DataDir
	hubCfg.DataDir = filepath.Join(dataDir, "hub")
	workerDataDir := filepath.Join(dataDir, "worker")

	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	server, err := hub.NewServer(hubCfg)
	if err != nil {
		return nil, fmt.Errorf("create hub server: %w", err)
	}

	// Resolved BEFORE the contexts exist, so no failure path has to cancel them:
	// Instance.shutdown stays the only place either cancel is ever reached.
	listenURL, err := hubCfg.LocalListenURL()
	if err != nil {
		return nil, fmt.Errorf("resolve local-listen URL: %w", err)
	}

	// Two sibling contexts, both DETACHED from ctx (values kept, cancellation
	// not), so cancellation reaches the Hub and the Worker in the order
	// Instance.shutdown imposes rather than all at once. The watcher goroutine
	// installed below is what turns a cancelled ctx back into a shutdown; it is
	// armed before any of this can block, so a caller that cancels during
	// startup still tears the instance down.
	detached := context.WithoutCancel(ctx)
	workerCtx, cancelWorker := context.WithCancel(detached)
	hubCtx, cancelHub := context.WithCancel(detached)
	inst := &Instance{
		cancelWorker: cancelWorker,
		cancelHub:    cancelHub,
		listenURL:    listenURL,
		hubDone:      make(chan struct{}),
		watcherDone:  make(chan struct{}),
	}

	// Start Hub. hubErr/hubDone publish the terminal error to Wait callers, and
	// hubDone is the ONLY "the Hub is finished" signal -- a second one (a wait
	// group closed just after it) could only ever disagree with it.
	//
	// serving is a scheduling handshake, not a readiness one: it says the
	// goroutine has been scheduled and is about to call Serve. Without it, a
	// Serve that fails immediately could stay entirely unobserved -- on a
	// single-P run the goroutine need not execute at all before Start finishes,
	// so Start would return an Instance for a Hub that had already failed and
	// the caller would only learn of it from Wait.
	serving := make(chan struct{})
	go func() {
		close(serving)
		inst.hubErr = serveHub(hubCtx, server)
		close(inst.hubDone)
	}()
	<-serving

	// One goroutine, two triggers, one ordered teardown. The caller cancelling
	// is the ordinary path (SIGINT, the desktop sidecar's own shutdown); the Hub
	// exiting on its own is the failure path, where the Worker must be torn down
	// too rather than left looping against a dead endpoint.
	//
	// It deliberately REPORTS NOTHING. Every way the Hub can fail already has
	// exactly one reporter: Start RETURNS a failure on the way up, and once Start
	// has succeeded the error reaches its caller through Wait and Stop -- which
	// waitSolo documents itself as the single reporter of. A log here would be a
	// second one for whichever of those the watcher happened to observe, which
	// is the duplicate report this shutdown work exists to remove, not a shape
	// of it to keep.
	go func() {
		defer close(inst.watcherDone)
		select {
		case <-ctx.Done():
		case <-inst.hubDone:
		}
		inst.shutdown()
	}()

	// Wait for Hub's local listener (unix socket or named pipe). The hubDone arm
	// is what keeps a Hub that failed instantly from paying out WaitReady's whole
	// 5s budget: hub.NewServer has already bound both listeners, so the probe
	// CONNECTS to a Hub whose Serve has returned and the wait would otherwise
	// succeed on a corpse.
	//
	// The select is a pure wait -- the same division waitSolo draws: both arms
	// choose WHEN to stop, never WHAT to report. That matters because both can be
	// ready at once for exactly the reason above, and Go picks between ready arms
	// arbitrarily; putting the verdict AFTER the select is what makes the pick
	// irrelevant. readyCh is buffered so the abandoned probe, which only cancelHub
	// inside join unblocks, cannot leak on its send.
	//
	// The verdict is hubFailure alone, with no "unless hubErr is context.Canceled"
	// carve-out. That guard dated from when hubCtx was a child of ctx and a parent
	// cancel hit Serve instantly; hubCtx is detached now, so hubErr can only be
	// context.Canceled in a microsecond window -- and when the guard did fire it
	// returned a nil Instance, making hubErr permanently unreachable and discarding
	// any non-ctx cause joined into it.
	readyCh := make(chan error, 1)
	go func() { readyCh <- locallisten.WaitReady(hubCtx, listenURL) }()
	var readyErr error
	select {
	case readyErr = <-readyCh:
	case <-inst.hubDone:
		// No error of its own: hubFailure is total once hubDone is closed.
	}
	if hubErr := inst.hubFailure(ctx, "before the listener became ready"); hubErr != nil {
		inst.join()
		return nil, hubErr
	}
	if readyErr != nil {
		inst.join()
		return nil, fmt.Errorf("wait for hub local listener: %w", readyErr)
	}

	// In dev mode the first admin may not exist yet; defer worker bringup
	// until /setup completes.
	statePath := filepath.Join(workerDataDir, "state.json")
	setupWorker := func(ctx context.Context) error {
		return bringUpWorker(ctx, &inst.workerWork, &inst.workerDrained, server, statePath, workerDataDir, hubCfg, listenURL, modeName)
	}
	// Hold a Worker-side token ACROSS bring-up. Without it a caller cancelling
	// during setup lets the watcher's shutdown sample an idle counter, cancel
	// the Hub, and only then have bringUpLocalWorker launch worker.Run against
	// an endpoint that is already going down -- Start would still return a
	// healthy-looking Instance. Holding the token means the counter is never
	// zero between the last ctx check and the Add that launches the Worker, so
	// drain.Counter's no-add-after-sample contract holds by construction.
	inst.workerWork.Add()
	inst.workerDrained.Add()
	err = setupWorker(workerCtx)

	// One spelling of "give the bring-up tokens back and tear the half-built
	// instance down", for the same reason join is one spelling of the wait set.
	// The Done()s have to precede join, or join's workerWork.DoneChan() never
	// closes and the teardown parks forever on tokens only this frame holds.
	failBringUp := func(cause error) (*Instance, error) {
		inst.workerDrained.Done()
		inst.workerWork.Done()
		inst.join()
		return nil, cause
	}

	// Bring-up is the only slow statement between the readiness verdict and the
	// return: SQLite, state-file I/O, and on a first launch ML-KEM + SLH-DSA
	// keygen. So it is the window in which a Serve failure most plausibly lands,
	// and the one place a Hub that died would otherwise be reported as the
	// Worker's fault -- the watcher cancels workerCtx, so bring-up comes back
	// saying "context canceled" -- or, when bring-up happened to succeed anyway,
	// not reported at all.
	//
	// Before the switch, so it covers all three arms, the dev-mode deferral
	// included: a poller must not be launched against a Hub that is already gone.
	// What remains after it is a go statement, two counter releases and a log
	// line -- no wait -- so a Hub dying past this point is indistinguishable from
	// one dying an instruction after Start returns, which Wait and Stop report.
	if hubErr := inst.hubFailure(ctx, "while the worker was starting"); hubErr != nil {
		return failBringUp(hubErr)
	}

	switch {
	case err == nil:
		// Worker is up.
	case errors.Is(err, store.ErrNotFound) && cfg.DevMode:
		slog.Info("dev mode: deferring worker auto-registration until first admin signs up via /setup")
		// Counted with the Worker, not the Hub: it is the thing that brings the
		// Worker up, so the ordered shutdown has to wait it out before it can
		// know no Worker is on its way. Added while the bring-up token is still
		// held, so the counter never dips to zero across the handover.
		inst.workerWork.Add()
		inst.workerDrained.Add()
		go pollForDeferredWorkerSetup(workerCtx, &inst.workerWork, &inst.workerDrained, setupWorker)
	default:
		return failBringUp(fmt.Errorf("auto-register worker: %w", err))
	}
	inst.workerDrained.Done()
	inst.workerWork.Done()

	slog.Info("leapmux "+modeName+" listening", "listen", hubCfg.Listen)

	return inst, nil
}

// soloState persists the auto-registered worker credentials.
type soloState struct {
	WorkerID         string `json:"worker_id"`
	AuthToken        string `json:"auth_token"`
	RegisteredBy     string `json:"registered_by,omitempty"`
	PublicKey        string `json:"public_key,omitempty"`
	PrivateKey       string `json:"private_key,omitempty"`
	MlkemPublicKey   string `json:"mlkem_public_key,omitempty"`
	MlkemPrivateKey  string `json:"mlkem_private_key,omitempty"`
	SlhdsaPublicKey  string `json:"slhdsa_public_key,omitempty"`
	SlhdsaPrivateKey string `json:"slhdsa_private_key,omitempty"`
}

// pollForDeferredWorkerSetup retries setupWorker on a ticker until it succeeds,
// the context is cancelled, or a non-ErrNotFound error occurs. Must be invoked
// as `go pollForDeferredWorkerSetup(...)` with work.Add() and drained.Add()
// already called — this function releases both on exit.
func pollForDeferredWorkerSetup(ctx context.Context, work, drained *drain.Counter, setupWorker func(context.Context) error) {
	defer work.Done()
	// Released here as well as inside bringUpLocalWorker: until a Worker exists
	// there is no drain to wait for, and a poller that gives up without ever
	// starting one must not hold the Hub for the backstop.
	defer drained.Done()
	ticker := time.NewTicker(workerSetupPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		err := setupWorker(ctx)
		if err == nil {
			return
		}
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		slog.Error("deferred worker setup failed", "error", err)
		return
	}
}

// bringUpLocalWorker loads (or creates, then persists) the local worker's
// registration state, ensures the composite E2EE keypair is present, and
// launches the worker goroutine under wg. It returns store.ErrNotFound if no
// admin user exists yet; the caller is expected to retry after the first
// /setup signup completes.
func bringUpLocalWorker(
	ctx context.Context,
	work, drained *drain.Counter,
	server *hub.Server,
	statePath, workerDataDir string,
	hubCfg *hubconfig.Config,
	listenURL, modeName string,
) error {
	state, err := loadOrCreateWorkerState(ctx, server, statePath, workerDataDir)
	if err != nil {
		return err
	}

	// Ensure composite keypair for E2EE.
	if state.PublicKey == "" || state.PrivateKey == "" ||
		state.MlkemPublicKey == "" || state.MlkemPrivateKey == "" ||
		state.SlhdsaPublicKey == "" || state.SlhdsaPrivateKey == "" {
		ck, kpErr := noiseutil.GenerateCompositeKeypair()
		if kpErr != nil {
			return fmt.Errorf("generate composite keypair: %w", kpErr)
		}
		slhdsaPub, _ := ck.SlhdsaPublicKeyBytes()
		slhdsaPriv, _ := ck.SlhdsaPrivateKey.MarshalBinary()
		state.PublicKey = base64.StdEncoding.EncodeToString(ck.X25519Public)
		state.PrivateKey = base64.StdEncoding.EncodeToString(ck.X25519Private)
		state.MlkemPublicKey = base64.StdEncoding.EncodeToString(ck.MlkemPublicKeyBytes())
		state.MlkemPrivateKey = base64.StdEncoding.EncodeToString(ck.MlkemDecapsulationKey.Bytes())
		state.SlhdsaPublicKey = base64.StdEncoding.EncodeToString(slhdsaPub)
		state.SlhdsaPrivateKey = base64.StdEncoding.EncodeToString(slhdsaPriv)
		if err := persistState(statePath, state); err != nil {
			slog.Warn("failed to save keypair", "error", err)
		}
	}

	compositeKey, ckErr := noiseutil.RestoreCompositeKeypair(
		mustDecode64(state.PublicKey),
		mustDecode64(state.PrivateKey),
		mustDecode64(state.MlkemPrivateKey),
		mustDecode64(state.SlhdsaPublicKey),
		mustDecode64(state.SlhdsaPrivateKey),
	)
	if ckErr != nil {
		return fmt.Errorf("restore composite keypair: %w", ckErr)
	}

	slog.Info(modeName+" worker registered",
		"worker_id", state.WorkerID,
		"local", listenURL,
	)

	work.Add()
	drained.Add()
	// Guarded so the drain signal is released exactly once whether the Worker
	// reports draining or dies before it ever gets there -- an unreleased token
	// would make every shutdown pay the full backstop.
	var drainedOnce sync.Once
	releaseDrained := func() { drainedOnce.Do(drained.Done) }
	go func() {
		defer work.Done()
		defer releaseDrained()
		if wErr := worker.Run(ctx, worker.RunConfig{
			Drained:      releaseDrained,
			HubURL:       listenURL,
			DataDir:      workerDataDir,
			AuthToken:    state.AuthToken,
			CompositeKey: compositeKey,
			WorkerID:     state.WorkerID,
			DBMaxConns:   hubCfg.Storage.SQLite.MaxConns,
			DBCacheSize:  hubCfg.Storage.SQLite.CacheSize,
			DBMmapSize:   hubCfg.Storage.SQLite.MmapSize,
			// 0 (the default) lets the worker apply channelwire.DefaultMaxIncompleteChunked.
			MaxIncompleteChunked: parseInt(hubCfg.Extras["max_incomplete_chunked"], 0),
			MaxMessageSize:       hubCfg.MaxMessageSize,
			AgentStartupTimeout:  hubCfg.AgentStartupTimeout(),
			APITimeout:           hubCfg.APITimeout(),
			EncryptionMode:       workerconfig.ParseEncryptionMode(hubCfg.Extras["encryption_mode"]),
			UseLoginShell:        parseBool(hubCfg.Extras["use_login_shell"], true),
			RegisteredBy:         state.RegisteredBy,
		}); wErr != nil {
			slog.Error("worker error", "error", wErr)
		}
	}()

	return nil
}

// workerRegistrar is the slice of *hub.Server that worker-state loading needs:
// resolve a worker's owner, find the user to attribute a new local worker to, and
// register one. Depending on the three methods rather than the whole Server lets
// the state-file rules -- which owner wins, when to re-register -- be tested without
// binding listeners and standing up a database.
type workerRegistrar interface {
	GetWorkerOwner(ctx context.Context, workerID string) (string, error)
	GetAdminUser(ctx context.Context) (userID string, err error)
	RegisterWorker(ctx context.Context, userID string) (*hub.WorkerCredentials, error)
}

func loadOrCreateWorkerState(ctx context.Context, server workerRegistrar, statePath, workerDataDir string) (*soloState, error) {
	if err := os.MkdirAll(workerDataDir, 0o750); err != nil {
		return nil, fmt.Errorf("create worker data dir: %w", err)
	}

	data, err := os.ReadFile(statePath)
	if err == nil {
		var s soloState
		unmarshalErr := json.Unmarshal(data, &s)
		if unmarshalErr == nil && s.WorkerID != "" && s.AuthToken != "" {
			// Take the owner from the DB, which is the authority: workers.registered_by
			// is NOT NULL and set at registration, and it is the fact requireWorkerOwner
			// gates the whole machine on. The state file's copy is a cache that can lag
			// it, and an empty one is not something to paper over -- a worker launched
			// with no owner has every machine-scoped family (file, git, sysinfo, tunnel)
			// permanently dead for its own legitimate user, failing closed in a way that
			// reads exactly like a genuine cross-tenant refusal.
			//
			// This replaces a backfill from GetAdminUser. Sourcing ownership from
			// "whoever the admin is now" answers a different question that merely shares
			// an answer on a fresh single-user install: on any install where the admin
			// changed, it silently reassigned the machine to them.
			owner, dbErr := server.GetWorkerOwner(ctx, s.WorkerID)
			if dbErr == nil {
				if owner == "" {
					// Unreachable via the schema (NOT NULL, set at registration), so this
					// means a hand-edited row or a mint path that dropped it. Re-register
					// rather than launch an ownerless worker.
					slog.Warn("saved worker has no recorded owner, re-registering", "worker_id", s.WorkerID)
				} else {
					if s.RegisteredBy != owner {
						// The file disagreed with the DB (or predates the field). The DB wins;
						// refresh the cache so the next launch matches without a second query.
						s.RegisteredBy = owner
						if err := persistState(statePath, &s); err != nil {
							// The in-memory correction still applies; a failed write only
							// costs the cache and forces this DB lookup again next launch.
							slog.Warn("failed to refresh worker state cache", "error", err)
						}
					}
					return &s, nil
				}
			} else if errors.Is(dbErr, store.ErrNotFound) {
				// The worker row is genuinely gone (deleted, or never persisted):
				// re-register a fresh identity below.
				slog.Warn("saved worker not found in DB, re-registering", "worker_id", s.WorkerID)
			} else {
				// A transient store failure (e.g. sqlite "database is locked"
				// racing another writer at startup) is NOT a deletion. Treating it
				// as one would discard the saved WorkerID and re-register a brand-new
				// identity, orphaning every workspace and tab still pointed at the old
				// worker. Fail the launch so a retry can find the row intact.
				return nil, fmt.Errorf("look up saved worker %q owner: %w", s.WorkerID, dbErr)
			}
		} else {
			// The file exists but is not a usable worker state -- a partial write
			// (power loss or OS crash mid-write, before this launch made writes
			// atomic), a hand-edit, or a truncated tail. The saved identity is
			// unrecoverable from these bytes, so re-registering a fresh worker
			// below is the only forward path, but it ORPHANS every workspace and
			// tab still pointing at the old worker_id -- the same loss the
			// transient-DB-error guard above exists to prevent. The DB analogue
			// fails the launch to keep the saved identity; here the identity is
			// already gone, so the best we can do is fail LOUDLY (Error, not a
			// silent fall-through) and preserve the corrupt file so an operator
			// can re-link the orphaned workspaces before the reaper collects them.
			if unmarshalErr != nil {
				slog.Error("saved worker state is unreadable; re-registering a fresh worker, which orphans the previous one",
					"state_path", statePath, "err", unmarshalErr)
			} else {
				slog.Error("saved worker state is missing its worker id or auth token; re-registering a fresh worker, which orphans the previous one",
					"state_path", statePath)
			}
			preserveCorruptState(statePath, data)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read state: %w", err)
	}

	userID, err := server.GetAdminUser(ctx)
	if err != nil {
		return nil, err
	}

	creds, err := server.RegisterWorker(ctx, userID)
	if err != nil {
		return nil, err
	}

	state := &soloState{
		WorkerID:     creds.WorkerID,
		AuthToken:    creds.AuthToken,
		RegisteredBy: userID,
	}

	if err := persistState(statePath, state); err != nil {
		return nil, err
	}

	return state, nil
}

// persistState writes the worker state file with the one encoding and mode
// (pretty JSON, 0o600) every writer must agree on. Callers keep their own
// error posture -- the keypair backfill and the RegisteredBy cache refresh log
// and continue, the initial registration fails the launch -- but the file's
// format contract lives here so a change cannot silently miss a write site.
//
// The write is atomic (temp file in the same directory, then rename) so a
// crash or power loss mid-write can never leave a half-written file behind.
// loadOrCreateWorkerState treats such a file as a lost identity and
// re-registers a fresh worker, orphaning every workspace still pointed at the
// old one -- the same loss a transient-DB-error at startup would cause if it
// were misread as a deletion -- so the write that produces the file the next
// launch reads must not be able to corrupt it.
func persistState(statePath string, s *soloState) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	dir := filepath.Dir(statePath)
	tmp, err := os.CreateTemp(dir, ".worker-state-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp state file: %w", err)
	}
	tmpName := tmp.Name()
	removeTmp := true
	defer func() {
		if removeTmp {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write state: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close state: %w", err)
	}
	if err := os.Rename(tmpName, statePath); err != nil {
		return fmt.Errorf("commit state: %w", err)
	}
	removeTmp = false
	return nil
}

// preserveCorruptState renames an unreadable worker-state file aside so a
// later launch does not re-log the same orphaning and an operator can inspect
// or re-link it. Best-effort: a failure leaves the bytes in place for the next
// launch to warn about again, which is still better than silently overwriting.
func preserveCorruptState(statePath string, data []byte) {
	sidecar := fmt.Sprintf("%s.corrupt-%d", statePath, time.Now().UnixNano())
	if err := os.WriteFile(sidecar, data, 0o600); err != nil {
		slog.Warn("could not preserve corrupt worker state for forensics", "sidecar", sidecar, "err", err)
	}
}

func mustDecode64(s string) []byte {
	b, _ := base64.StdEncoding.DecodeString(s)
	return b
}

// shouldWarnNonLoopback reports whether solo.Start should emit the
// non-loopback security warning. Dev mode uses real password auth so it is
// exempt; NoTCP means there is no TCP listener to warn about.
func shouldWarnNonLoopback(cfg Config, listen string) bool {
	return !cfg.DevMode && !cfg.NoTCP && listenIsNonLoopback(listen)
}

// listenIsNonLoopback reports whether `listen` would expose the hub on
// something other than a loopback address. Used only to drive the solo-mode
// security warning; the heuristic stays conservative — empty/missing host or
// an unparseable hostname is treated as non-loopback so the warning errs on
// the side of being shown. Wildcard addresses ("0.0.0.0", "::") parse as
// non-loopback IPs, so `net.IP.IsLoopback` already handles them.
func listenIsNonLoopback(listen string) bool {
	if listen == "" {
		return true
	}
	host, _, err := net.SplitHostPort(listen)
	if err != nil || host == "" {
		return true
	}
	if strings.EqualFold(host, "localhost") {
		return false
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
}

// defaultExtraFlags are the koanf-backed flags solo/dev registers on top of the
// hub's own flag table. Every one of them is a WORKER-scoped setting: solo embeds
// a Worker, but the hub config file is the only config file solo reads, so these
// are the sole way to tune the embedded Worker. They are "extras" precisely
// because they are not hub settings and must not appear on `leapmux hub`.
//
// max-incomplete-chunked is here rather than in the hub's flag table because the
// Hub has no chunk-count cap to tune: channelmgr's interleaving guard admits only
// ONE in-flight chunked sequence per channel+direction, which is strictly stronger
// than any count cap. The cap is real on the WORKER (channel.NewManager -> the
// reassembly budget in session.go), which is what this forwards to. Do not "restore"
// it as a hub flag -- it would be dead there.
func defaultExtraFlags() []hubconfig.ExtraFlagDef {
	return []hubconfig.ExtraFlagDef{
		{Name: "encryption-mode", KoanfKey: "encryption_mode", Usage: "encryption mode (classic, post-quantum)", StrDefault: "post-quantum"},
		{Name: "use-login-shell", KoanfKey: "use_login_shell", Usage: "wrap claude invocation in user's login shell", StrDefault: "true"},
		{Name: "max-incomplete-chunked", KoanfKey: "max_incomplete_chunked", Usage: "maximum in-flight chunked sequences per channel for the embedded worker (default 4)", StrDefault: "0", Category: "Timeout and limit options"},
	}
}

// defaultCLIFlags names the subset of the HUB's own flags a solo or dev launcher
// puts on the command line. The rest of the hub's table is still reachable in
// these modes -- LoadWithOptions records every flag's default and koanf key
// before it consults this list, so the config file and LEAPMUX_HUB_* env vars
// see the whole surface. This list only decides what earns a line in --help.
//
// max-connections-per-user and max-workers-per-user are here because solo is the
// mode where they actually bind. Every socket belongs to the one local user: an
// active tab holds two leases, and the desktop app, any CLI `remote` session and
// every worker's remoteipc watcher draw on the same allowance, so the default of
// 32 is well under 16 tabs. A user who hits it is told to close a tab, which is
// the one piece of advice a --help with no such flag cannot act on.
//
// The three queue budgets are deliberately NOT here. They auto-size off this
// machine's memory limit, which is almost always the right answer on a laptop,
// and a config file still reaches them for the rare case it is not.
func defaultCLIFlags(devMode bool) []string {
	flags := []string{
		"listen", "data-dir", "dev-frontend",
		"storage-sqlite-max-conns", "storage-sqlite-cache-size", "storage-sqlite-mmap-size",
		"api-timeout-seconds", "agent-startup-timeout-seconds", "worktree-create-timeout-seconds",
		"log-level", "use-login-shell",
		"max-connections-per-user", "max-workers-per-user",
	}
	if devMode {
		flags = append(flags, "public-url")
	}
	return flags
}

// parseInt parses a string as an int, returning defaultVal if the string is
// empty or not a valid integer. It is the int counterpart of parseBool, used to
// read the string-typed Extras map that carries solo's worker-scoped flags.
func parseInt(s string, defaultVal int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return defaultVal
	}
	return n
}

// parseBool parses a string as a boolean, returning defaultVal if the string
// is empty or not recognized.
func parseBool(s string, defaultVal bool) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1", "yes":
		return true
	case "false", "0", "no":
		return false
	default:
		return defaultVal
	}
}
