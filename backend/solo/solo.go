// Package solo provides an in-process launcher for the LeapMux Hub and Worker,
// suitable for solo/desktop mode where both run inside the same binary.
package solo

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/quartz"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/hub"
	hubconfig "github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/revocationwatcher"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/logging"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/internal/util/atomicfile"
	workerconfig "github.com/leapmux/leapmux/internal/worker/config"
	"github.com/leapmux/leapmux/internal/workerkeys"
	"github.com/leapmux/leapmux/lifecycle"
	"github.com/leapmux/leapmux/locallisten"
	"github.com/leapmux/leapmux/worker"

	"github.com/leapmux/leapmux/util/drain"
	"github.com/leapmux/leapmux/util/errwrap"
)

// workerSetupPollInterval is how often dev mode re-checks whether the first
// admin user has completed the /setup flow so the auto-registered local
// worker can come online.
//
// A var so tests can shorten it, matching workerDrainTimeout below: at 2s a test
// that needs the poller to be mid-setup when a teardown lands would have to sleep
// through a tick, and the alternative to shortening it is the timing dependency
// this package deliberately avoids.
var workerSetupPollInterval = 2 * time.Second

// workerDrainTimeout bounds how long the ordered shutdown waits for the Worker
// to finish draining before it stops holding the Hub open for it. See
// Instance.shutdown for why the Worker goes first at all.
//
// Sized against the drain it is waiting on: the Worker's own stops sum to
// about 8s of agent Stop, 7s of terminal hang-up, 6s of control-IPC retire
// and 5s of ShutdownFlushTimeout -- 26s with no stalled writer -- so 30s is
// that sum plus a margin. Everything before the flush is fast on an idle
// machine and has no limit on a busy one. So this is not a promise the drain
// fits; it is the point at which a Hub that is trying to exit stops waiting
// for a Worker that is not coming.
//
// A var so tests can shorten it, matching operationDrainTimeout and
// relayDrainTimeout in the desktop sidecar.
var workerDrainTimeout = 30 * time.Second

// previousHubWait is how long a solo launch waits out a predecessor's runtime
// lease: the lease's own duration plus a margin for the release that lands
// after it. A relaunch during that window would otherwise fail with
// "another Hub is already running" on a lease whose holder is already gone.
var previousHubWait = revocationwatcher.DefaultLeaseDuration + 5*time.Second

// previousHubProbeTimeout limits ONE dial of the predecessor probe: only a
// limited dial can time out, and a busy Windows named pipe retries the
// connect until its context ends, so an unlimited dial would block past the
// probe's whole deadline.
const previousHubProbeTimeout = 100 * time.Millisecond

// addressHeld makes ONE dial and answers whether a Hub still holds the
// address:
//   - A successful dial means a live listener.
//   - A dial that times out means a predecessor holds the address but cannot
//     answer while it drains.
//   - Any other failure means the address is free.
func addressHeld(ctx context.Context, dial func(context.Context) (net.Conn, error)) bool {
	probeCtx, cancel := context.WithTimeout(ctx, previousHubProbeTimeout)
	defer cancel()
	conn, err := dial(probeCtx)
	if err == nil {
		_ = conn.Close()
		return true
	}
	return errors.Is(err, context.DeadlineExceeded)
}

// waitForPreviousHub dials every local URL of the previous hub, every 250ms,
// until none of them answers or the deadline passes. At the deadline it
// returns nil -- NewServer then reports the existing error, which is the one
// verdict this probe is allowed to leave to anyone else. It checks ctx after
// each pass, because addressHeld reads a cancelled dial as free, and returns
// that error on cancel.
func waitForPreviousHub(ctx context.Context, urls []string, deadline time.Time) error {
	dials := make([]func(context.Context) (net.Conn, error), 0, len(urls))
	for _, url := range urls {
		dial, err := locallisten.Dialer(url)
		if err != nil {
			return fmt.Errorf("dial %s: %w", url, err)
		}
		dials = append(dials, dial)
	}
	loggedWaiting := false
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	for {
		held := false
		for _, dial := range dials {
			if addressHeld(ctx, dial) {
				held = true
				break
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !held {
			return nil
		}
		if !loggedWaiting {
			loggedWaiting = true
			slog.Info("waiting for the previous hub to release its address and lease",
				"deadline", deadline.Format(time.RFC3339))
		}
		if !time.Now().Add(250 * time.Millisecond).Before(deadline) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			timer.Reset(250 * time.Millisecond)
		}
	}
}

// deps supplies startup and Worker lifecycle collaborators. They are PER-CALL
// rather than package vars so a test's substitute cannot outlive its
// own test: the previous package-var seams were written by t.Cleanup while a
// leaked Start goroutine could still be reading them, which is a data race the
// tests had to avoid by discipline alone -- and which silently forbade
// t.Parallel() on every test that stubs one.
//
// These operations are seams because external fixtures cannot control every
// failure mode or its ordering:
//
//   - serveHub: hub.NewServer binds BOTH listeners before Serve is ever called,
//     so no fixture can induce a Serve-TIME failure by occupying an address or
//     pointing at an unwritable path -- every such attempt fails earlier, in
//     NewServer, on a path that never reaches the watcher. Without it, the "the
//     Hub died while serving" case and the gate that stops a startup failure being
//     reported twice had no coverage, and two tests aimed at them were passing on
//     a NewServer failure instead.
//   - waitReady: production waits for application readiness. A controlled wait
//     orders readiness after Hub exit when a test exercises the failure verdict.
//   - bringUpWorker: every real way to fail bring-up -- no admin user yet, an
//     unreadable state file, a store error -- fails AT ONCE, at the first
//     statement that touches the database or the disk, so nothing can hold Start
//     inside the bring-up window long enough for the startup to end there.
//   - runWorker: the Hub refuses its bundled Worker for good only after a retire
//     or a key change that a running solo process does not make, so no fixture
//     can end worker.Run while the context is live.
//
// defaultDeps gives the real implementations, and they stay directly callable,
// which is what keeps the seams honest: several tests drive Start straight
// through them, and TestSoloStart_TearsDownARealWorkerLaunchedJustBeforeTheHubDied
// WRAPS bringUpLocalWorker rather than replacing it, so the token accounting under
// the gate is exercised against the Worker goroutine production actually launches.
type deps struct {
	clock          quartz.Clock
	serveHub       func(ctx context.Context, s *hub.Server) error
	waitReady      func(ctx context.Context, server *hub.Server, listenURL string) error
	bringUpWorker  func(ctx context.Context, p workerBringUp) error
	runWorker      func(ctx context.Context, cfg worker.RunConfig) error
	closeDirectory func(*lifecycle.DirectoryLease) error
}

func defaultDeps() deps {
	return deps{
		clock:    quartz.NewReal(),
		serveHub: func(ctx context.Context, s *hub.Server) error { return s.Serve(ctx) },
		waitReady: func(ctx context.Context, server *hub.Server, _ string) error {
			select {
			case <-server.Ready():
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
		bringUpWorker:  bringUpLocalWorker,
		runWorker:      worker.Run,
		closeDirectory: (*lifecycle.DirectoryLease).Close,
	}
}

// nonLoopbackListenWarnMsg is the security warning the solo launcher prints
// when the hub answers on an address another machine can reach.
//
// A caller that reaches a passwordless solo hub over TCP can claim the account
// by setting its first password. Other protected RPCs stay closed. Setting a
// password closes the setup procedure and makes every TCP address require a
// sign-in.
//
// It is printed at STARTUP, once the hub has bound its sockets but before any
// request, so it cannot know whether a password exists yet -- which is why it
// states the condition rather than asserting the danger. The frontend makes
// the same demand where it can be acted on: it blocks the whole app with a
// password-setup screen for every TCP connection while the account has no
// password. That screen no longer depends on the listen address -- loopback
// reaches it too -- because the rule is now the TRANSPORT: only the local IPC
// socket is credential-free.
const nonLoopbackListenWarnMsg = "solo mode binds a non-loopback address, so this hub is reachable from other machines. " +
	"Until the \"solo\" account has a password, the first TCP caller can claim it by setting that password. " +
	"Other protected actions stay closed until setup completes. Open the app and set the password now. " +
	"Restrict access externally as well (firewall, Tailscale/WireGuard, SSH tunnel) if the network is not one you trust."

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
	// FlagSetName and Description head the help output. Empty values give
	// "leapmux <mode>" and the description of the mode.
	FlagSetName string
	Description string
	// DevMode runs in dev mode (binds to all interfaces, and bootstraps the
	// first administrator through /setup).
	DevMode bool
	// NoTCP disables the TCP listener. When true, the Hub only listens on
	// the local IPC socket (unix:<path> on Unix, npipe:<name> on Windows).
	// This is used by the desktop app to avoid opening a TCP port.
	NoTCP bool
	// ConfigureFlags registers caller-owned command flags without adding runtime settings.
	ConfigureFlags func(*flag.FlagSet)
	// AfterFlagsParsed runs after valid flags and metadata, before configuration I/O.
	AfterFlagsParsed func(*flag.FlagSet) error
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
	// workerDrained reports the worker-side work the HUB has to outlive. For a
	// RUNNING Worker that is the narrow thing -- the goodbye broadcast and the
	// outbound flush, announced by worker.Run's Drained hook before its local
	// teardown. Waiting on workerWork instead would hold the Hub up for a database
	// close it has no stake in, and report a slow one as a worker that failed to
	// drain.
	//
	// Start and the dev-mode poller hold a token here too, for the window in which
	// no Worker exists YET: bring-up can still Add (drain.Counter's
	// no-add-after-sample contract), so cancelHub must not run until that window
	// closes. The backstop warning therefore covers bring-up as well as the drain --
	// it can fire with no Worker goroutine in existence.
	workerDrained drain.Counter
	listenURL     string
	// accessURL is the address to open in a browser; see AccessURL.
	accessURL string
	hubErr    error         // set before hubDone is closed
	hubDone   chan struct{} // closed when the Hub goroutine exits
	// rootLease is the combined instance's containing lease. It
	// releases LAST, after every component's work joined -- the plan's
	// "all instance work is joined before releasing the combined root
	// lease".
	rootLease *lifecycle.DirectoryLease
	// closeDirectory is immutable for this instance; nil selects the real close.
	closeDirectory func(*lifecycle.DirectoryLease) error
	// workerLease is the worker component's lease while the worker has
	// not started. bringUpLocalWorker TRANSFERS it into worker.Run when
	// the worker goroutine launches; a worker that never starts leaves
	// it here, and the combined shutdown closes it. Idempotent Close
	// makes the transfer and the shutdown release safe together.
	workerLease *lifecycle.DirectoryLease
	// startupState and its mutex carry the combined startup snapshot the
	// Instance exposes; usable at the zero value, as the fields above
	// are, for the half-built instances the failure paths drive.
	startupMu      sync.Mutex
	startupState   combinedStartupState
	startupChanged chan struct{}
	failureOnce    sync.Once
	failureCh      chan struct{}
	// watcherDone closes when the goroutine that turns a cancelled parent
	// context (or a Hub that exited on its own) into an ordered teardown has
	// finished. join waits for it so no report or teardown step can still be in
	// flight after Stop returns.
	watcherDone chan struct{}
}

type workerStartupPhase uint8

const (
	workerStartupInitial workerStartupPhase = iota
	workerStartupPendingSetup
	workerStartupStarting
	workerStartupReady
	workerStartupFailed
)

type combinedStartupState struct {
	hubReady     bool
	workerPhase  workerStartupPhase
	registration lifecycle.RegistrationStatus
	connection   lifecycle.ConnectionStatus
	failure      error
}

// StartupSnapshot states where a combined startup stands. It is the
// Instance's own view, for the CLI's status surfaces: the hub's
// readiness, the worker's, and the deferred-setup window a fresh dev
// instance waits in.
type StartupSnapshot struct {
	// HubReady reports whether the Hub finished its initialization.
	HubReady bool
	// WorkerReady reports whether the embedded worker finished its
	// local initialization.
	WorkerReady bool
	// WorkerPendingSetup reports a fresh dev instance whose worker
	// waits for the first administrator: the instance is ready to
	// serve SETUP, not yet fully ready.
	WorkerPendingSetup bool
	// WorkerStarting reports initialization after setup becomes available.
	WorkerStarting bool
	Registration   lifecycle.RegistrationStatus
	Connection     lifecycle.ConnectionStatus
	Failure        error
}

// Startup snapshots the combined startup state.
func (i *Instance) Startup() StartupSnapshot {
	snapshot, _ := i.ObserveStartup()
	return snapshot
}

// ObserveStartup returns one snapshot and the signal for its next change.
// Reading both under one lock prevents lost state transitions.
func (i *Instance) ObserveStartup() (StartupSnapshot, <-chan struct{}) {
	i.startupMu.Lock()
	defer i.startupMu.Unlock()
	if i.startupChanged == nil {
		i.startupChanged = make(chan struct{})
	}
	state := i.startupState
	return StartupSnapshot{
		HubReady:           state.hubReady,
		WorkerReady:        state.workerPhase == workerStartupReady,
		WorkerPendingSetup: state.workerPhase == workerStartupPendingSetup,
		WorkerStarting:     state.workerPhase == workerStartupStarting,
		Registration:       state.registration.Clone(),
		Connection:         state.connection.Clone(),
		Failure:            state.failure,
	}, i.startupChanged
}

func (i *Instance) publishStartupLocked() {
	if i.startupChanged != nil {
		close(i.startupChanged)
	}
	i.startupChanged = make(chan struct{})
}

// markWorkerReady records the worker's local readiness.
func (i *Instance) markWorkerReady() {
	i.startupMu.Lock()
	defer i.startupMu.Unlock()
	if i.startupState.failure != nil {
		return
	}
	i.startupState.workerPhase = workerStartupReady
	i.publishStartupLocked()
}

// markWorkerPendingSetup records the deferred-setup window.
func (i *Instance) markWorkerPendingSetup() {
	i.startupMu.Lock()
	defer i.startupMu.Unlock()
	if i.startupState.failure != nil {
		return
	}
	i.startupState.workerPhase = workerStartupPendingSetup
	i.startupState.registration = lifecycle.RegistrationStatus{State: lifecycle.RegistrationPending}
	i.startupState.connection = lifecycle.ConnectionStatus{State: lifecycle.ConnectionPendingSetup}
	i.publishStartupLocked()
}

// markHubReady records the hub's readiness.
func (i *Instance) markHubReady() {
	i.startupMu.Lock()
	i.startupState.hubReady = true
	i.publishStartupLocked()
	i.startupMu.Unlock()
}

func (i *Instance) markWorkerStarting(registration lifecycle.RegistrationStatus) {
	i.startupMu.Lock()
	defer i.startupMu.Unlock()
	if i.startupState.failure != nil {
		return
	}
	i.startupState.workerPhase = workerStartupStarting
	i.startupState.registration = registration.Clone()
	i.startupState.connection = lifecycle.ConnectionStatus{State: lifecycle.ConnectionConnecting}
	i.publishStartupLocked()
}

func (i *Instance) updateWorkerConnection(state worker.ConnectionState) {
	connection := lifecycle.ConnectionStatus{State: lifecycle.ConnectionConnected}
	if !state.Connected {
		connection.State = lifecycle.ConnectionDisconnected
		if state.Err != nil {
			connection.LastError = new(state.Err.Error())
		}
	}
	i.startupMu.Lock()
	i.startupState.connection = connection
	i.publishStartupLocked()
	i.startupMu.Unlock()
}

// LocalListenURL returns the URL at which the Hub is accepting local IPC
// connections (unix:<path> on Unix, npipe:<name> on Windows). Callers that
// need to dial the Hub from within the same process tree (e.g. the desktop
// proxy) should use this rather than reconstructing a path.
//
// With several local listeners bound it reports the first, in configuration
// order; the rest still answer.
func (i *Instance) LocalListenURL() string {
	return i.listenURL
}

// AccessURL returns the address to open in a browser: the public_url
// setting, or one derived from the TCP address the Hub answers on. It is
// empty when the Hub binds no TCP address, as the desktop app's does. The
// launcher prints it; this package prints nothing to the terminal.
func (i *Instance) AccessURL() string {
	return i.accessURL
}

// WaitDone closes when Wait's subject -- the Hub goroutine -- has
// exited. A caller that only needs a termination signal selects on it;
// Wait and Stop remain the error reporters.
func (i *Instance) WaitDone() <-chan struct{} { return i.hubDone }

// Wait blocks until Hub shutdown and returns the combined failure. Worker
// failure triggers ordered shutdown; Hub cleanup failures remain observable.
// Without a Worker failure it preserves the Hub result verbatim.
func (i *Instance) Wait() error {
	<-i.hubDone
	failure := i.workerError()
	if failure == nil {
		return i.hubErr
	}
	if i.hubErr == nil || errwrap.IsCancellation(i.hubErr) {
		return failure
	}
	return errors.Join(failure, i.hubErr)
}

func (i *Instance) workerError() error {
	i.startupMu.Lock()
	defer i.startupMu.Unlock()
	return i.startupState.failure
}

func (i *Instance) failWorker(cause error) {
	i.failureOnce.Do(func() {
		i.startupMu.Lock()
		i.startupState.workerPhase = workerStartupFailed
		i.startupState.failure = cause
		i.publishStartupLocked()
		i.startupMu.Unlock()
		if i.failureCh != nil {
			close(i.failureCh)
		}
	})
}

func (i *Instance) workerExited(ctx context.Context, cause error) {
	if ctx.Err() != nil && (cause == nil || errwrap.IsCancellation(cause)) {
		return
	}
	if cause == nil {
		cause = errors.New("the embedded worker exited unexpectedly without an error")
	}
	i.failWorker(cause)
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
			"worker bring-up or drain did not finish before the hub shutdown deadline; stopping the hub anyway")
		if i.cancelHub != nil {
			i.cancelHub()
		}
	})
}

// Stop gracefully shuts down the Worker and Hub, in that order, and returns the
// combined error, including failures from shutdown cleanup such as runtime
// lease release.
//
// The final Worker wait is deliberately unbounded even though shutdown's is not:
// worker.Run closes the Worker's database as it unwinds, and returning from Stop
// with that still in flight would let the next ConnectSolo reopen the same file
// underneath it. A Worker that overran the drain budget has already cost the Hub
// nothing further by this point -- the Hub is stopped.
//
// Wait may complete before local cleanup. Stop includes cleanup failures in its result.
// A drain deadline remains a warning; Stop still joins the full Worker exit.
func (i *Instance) Stop() error {
	cleanupErr := i.join()
	outcome := i.Wait()
	if cleanupErr != nil {
		return errors.Join(outcome, cleanupErr)
	}
	return outcome
}

// join runs the ordered teardown and waits out both halves. It is the ONE
// spelling of the wait set: Stop uses it, and so does every Start failure path,
// which used to hand-roll three different subsets of it and stay correct only
// by accident of which goroutines happened to be running yet.
//
// The Worker wait is deliberately unbounded even though shutdown's is not:
// worker.Run closes the Worker's database as it unwinds, and returning with that
// still in flight would let the next ConnectSolo reopen the same file underneath
// it. By this point the Hub is already stopped, so a slow Worker costs it
// nothing further.
func (i *Instance) join() error {
	i.shutdown()
	<-i.hubDone
	<-i.workerWork.DoneChan()
	// Last, because the watcher wakes on hubDone: joining it here is what makes
	// "Stop returned" mean the whole teardown is over, rather than leaving its
	// report to land on a caller that has already moved on.
	if i.watcherDone != nil {
		<-i.watcherDone
	}
	// The retained worker lease (a worker that never started) and the
	// combined root lease release here, after every component's work
	// joined. Idempotent Close keeps the double release with a
	// transferred worker lease a no-op.
	workerCloseErr := closeDirectoryLease(i.workerLease, i.closeDirectory)
	if workerCloseErr != nil && errors.Is(i.workerError(), workerCloseErr) {
		workerCloseErr = nil
	}
	rootCloseErr := closeDirectoryLease(i.rootLease, i.closeDirectory)
	return errors.Join(errwrap.Wrap(workerCloseErr, "close worker lease"), errwrap.Wrap(rootCloseErr, "close combined root lease"))
}

// startupFailure reports the error to attribute to a startup that is already
// over, or nil if it was still on its way up when asked. stage identifies the startup
// window, for the cases where nothing carries an error of its own.
//
// It exists because "is this startup still going?" has to be asked at the STAGE
// BOUNDARIES, not inside the cases of a select or a switch: hub.NewServer binds
// both listeners before Serve is ever called, so a bare connect-and-close
// readiness probe succeeds against a Hub whose Serve has already returned an error,
// and every case that asked the question separately answered it differently.
//
// It covers BOTH of the watcher's triggers, because both reach the caller the same
// way -- through a workerCtx the watcher has already cancelled, which makes the
// Worker report "context canceled" for something that is not its fault:
//
//   - The Hub exited. hubDone is closed, and hubErr is the cause.
//   - The CALLER cancelled. hubDone is still OPEN -- shutdown() cancels the Worker
//     and then parks in workerDrained.Wait, held by the token Start carries across
//     bring-up, so cancelHub has not run yet -- and ctx is the cause. Asking only
//     about hubDone here left this half blaming the Worker.
//
// It is TOTAL once either has happened: it never returns nil for a startup that is
// over. The readiness select relies on that, so its hubDone case can decide only to
// stop waiting and carry no error of its own.
//
// Reading hubErr after observing hubDone needs no further synchronisation: the Hub
// goroutine assigns it before closing hubDone. A nil hubDone reads as "still
// serving", which keeps this usable on a half-built Instance for the same reason
// shutdown and join are.
//
// This is a SAMPLE, not a guarantee: a Hub that exits a moment later still yields a
// healthy Instance, which is Wait's job to report. It closes the wide windows, not
// every window. It must NOT be asked after join(), which closes hubDone itself --
// there it would report every healthy Hub as exited.
func (i *Instance) startupFailure(ctx context.Context, stage string) error {
	if failure := i.workerError(); failure != nil {
		return fmt.Errorf("worker exited %s: %w", stage, failure)
	}
	select {
	case <-i.hubDone:
		// hubErr first, deliberately: a genuine teardown failure (lease release,
		// store close) that coincides with a cancel is more information than
		// context.Canceled, and %w keeps errors.Is(err, context.Canceled) working
		// for a caller that wants to tell the two apart.
		if i.hubErr != nil {
			return fmt.Errorf("hub serve exited %s: %w", stage, i.hubErr)
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("hub shut down %s: %w", stage, err)
		}
		return fmt.Errorf("hub serve exited %s", stage)
	default:
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("solo startup cancelled %s: %w", stage, err)
	}
	return nil
}

// failStartup joins startup cleanup and preserves independent completion errors.
// Startup sampled its cause before both components finished. With no returned
// Instance, callers cannot use Wait or Stop to retrieve a later failure.
// Requested Hub cancellation is suppressed; independent joined errors remain.
// Do not call startupFailure again after the join: hubDone is always closed,
// so its fallback would report an unexpected exit caused by requested cleanup.
func (i *Instance) failStartup(cause error) error {
	cleanupErr := i.join()
	outcome := cause
	if failure := i.workerError(); failure != nil && !errors.Is(outcome, failure) {
		outcome = errors.Join(outcome, failure)
	}
	if i.hubErr != nil && !errwrap.IsCancellation(i.hubErr) && !errors.Is(outcome, i.hubErr) {
		outcome = errors.Join(outcome, fmt.Errorf("hub serve: %w", i.hubErr))
	}
	if cleanupErr != nil {
		outcome = errors.Join(outcome, cleanupErr)
	}
	return outcome
}

// soloLoadOptions derives the hub-config load options for a solo or dev launch:
// the mode name, the listen/config-dir/config-file defaults, and the two flag
// lists. Everything here is a pure function of Config -- no logging, no banner, no
// filesystem -- which is what lets it be table-tested without binding a listener.
//
// It is the SINGLE source of truth for those defaults. `leapmux solo` used to
// spell its own copies, and the flag lists drifted until
// `--max-connections-per-user` stopped parsing while solo's own test asserted it
// was exposed.
func soloLoadOptions(cfg Config) (hubconfig.LoadOptions, string) {
	modeName := "solo"
	description := "Run Hub + Worker locally for single-user use."
	if cfg.DevMode {
		modeName = "dev"
		description = "Run Hub + Worker together for development."
	}
	if cfg.Description != "" {
		description = cfg.Description
	}
	flagSetName := "leapmux " + modeName
	if cfg.FlagSetName != "" {
		flagSetName = cfg.FlagSetName
	}

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
		cliFlags = defaultCLIFlags()
	}

	extraFlags := cfg.ExtraFlags
	if extraFlags == nil {
		extraFlags = defaultExtraFlags()
	}

	return hubconfig.LoadOptions{
		DefaultListen:     defaultListen,
		DefaultConfigDir:  configDir,
		DefaultConfigFile: configFile,
		FlagSetName:       flagSetName,
		Description:       description,
		CLIFlags:          cliFlags,
		ExtraFlags:        extraFlags,
		SoloMode:          !cfg.DevMode,
		ConfigureFlags:    cfg.ConfigureFlags,
		AfterFlagsParsed:  cfg.AfterFlagsParsed,
	}, modeName
}

// start is the one body every entry shares: the prepared launch, the
// supplied root lease (ownership already validated by StartPrepared),
// and the per-call dependency seams.
func start(ctx context.Context, cfg Config, d deps, prepared *PreparedConfig, root *lifecycle.DirectoryLease) (instance *Instance, result error) {
	logging.Setup()

	hubCfg := prepared.hubCfg
	modeName := prepared.role.String()

	level, err := logging.ParseLevel(hubCfg.LogLevel)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("invalid log level: %w", err), closeDirectoryLease(root, d.closeDirectory))
	}
	logging.SetLevel(level)

	// The combined root lease is held from here. Every failure below
	// releases it along with whatever this frame acquired.
	// Root acquisition already validated aliases and retained both components
	// under the same containing owner, before any runtime publication.
	hubLease := root.TakeComponent(lifecycle.RoleHub)
	workerLease := root.TakeComponent(lifecycle.RoleWorker)
	if hubLease == nil || workerLease == nil {
		return nil, errors.Join(
			errors.New("solo: the root lease must retain both component leases until startup"),
			closeDirectoryLease(hubLease, d.closeDirectory), closeDirectoryLease(workerLease, d.closeDirectory), closeDirectoryLease(root, d.closeDirectory))
	}
	// Release bookkeeping: until the Instance below owns them, this
	// frame releases every lease it received on the failure paths. The
	// root lease releases only while the Instance does NOT own it; a
	// committed startup hands all three leases to the Instance and its
	// components, and join() releases them.
	committed := false
	defer func() {
		if committed {
			return
		}
		for _, lease := range []*lifecycle.DirectoryLease{hubLease, workerLease, root} {
			if err := closeDirectoryLease(lease, d.closeDirectory); err != nil && !errors.Is(result, err) {
				result = errors.Join(result, err)
			}
		}
	}()
	hubCfg.DataDir = hubLease.Path()

	// NoTCP (the desktop) opens no TCP port: the bind set becomes the local
	// IPC half of what the launcher configured -- the local entries when it
	// named any, and otherwise the platform's local IPC URL. It runs after the
	// data-dir split because that default derives from the hub's own data
	// directory.
	if cfg.NoTCP {
		local, localErr := hubCfg.LocalListenURLs()
		if localErr != nil {
			return nil, fmt.Errorf("resolve local listen addresses: %w", localErr)
		}
		hubCfg.Listen = local
	}

	// Resolved BEFORE NewServer, so its failure paths cannot leak the server:
	// a LocalListenURLs error used to return after the bind, stranding both
	// listeners.
	localURLs, err := hubCfg.LocalListenURLs()
	if err != nil {
		return nil, fmt.Errorf("resolve local listen URL: %w", err)
	}
	if len(localURLs) == 0 {
		// Unreachable: ListenEntries guarantees a local IPC address. The
		// scalar below needs a list to take it from, and a panic in Start
		// would hide the configuration at fault.
		return nil, errors.New("hub bind set holds no local IPC address")
	}
	listenURL := localURLs[0]

	// Wait out a predecessor's lease and sockets BEFORE NewServer binds: a
	// relaunch during a predecessor's drain would otherwise fail at Listen
	// with "already in use", and one right after a crash would fail with
	// "another Hub is already running" for the whole lease TTL. Each dial is
	// limited by previousHubProbeTimeout, because only a limited dial can time
	// out -- a busy Windows named pipe retries the connect until its context
	// ends -- and an unlimited one would block past the deadline. At the
	// deadline this returns nil, and NewServer then reports the existing
	// error; waitForPreviousHub never invents a verdict of its own.
	probeDeadline := time.Now().Add(previousHubWait)
	if err := waitForPreviousHub(ctx, localURLs, probeDeadline); err != nil {
		return nil, fmt.Errorf("wait for the previous hub: %w", err)
	}
	hubCfg.RuntimeLeaseWait = time.Until(probeDeadline)

	// The component directories exist durably already: acquisition
	// created and validated them. NewServer takes the hub lease's
	// ownership here; its Serve releases it with the store cleanup.
	server, err := hub.NewServer(hubCfg, hubLease)
	if err != nil {
		return nil, fmt.Errorf("create hub server: %w", err)
	}
	// The server owns the hub lease from here; the deferred close above
	// is an idempotent no-op once its own cleanup ran.

	// AFTER NewServer, and reading the SOCKETS rather than the flag.
	//
	// -listen is no longer the whole answer: NewServer binds the stored
	// extra_listen_addresses too, so `leapmux solo` with the default loopback
	// -listen and a stored `*:4327` is reachable from other machines and said
	// nothing, and a desktop hub that gains its only TCP address from that
	// setting was suppressed outright by the NoTCP test. This is the same
	// predicate the password-setup screen reads, so the log line and the
	// screen cannot disagree about one hub.
	if shouldWarnNonLoopback(cfg, server.HasNonLoopbackAddress()) {
		slog.Warn(nonLoopbackListenWarnMsg, "listen", server.PrimaryListenAddr())
	}

	// Resolved after the server exists so the URL reflects the public_url
	// setting and the address the hub ACTUALLY answers on -- a stored extra
	// address can widen what -listen asked for (127.0.0.1:4327 merged into
	// *:4327), or supply the only TCP address there is on a desktop hub that
	// starts with none. The server owns both facts; see hub.Server.AccessURL.
	accessURL := server.AccessURL()

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
		cancelWorker:   cancelWorker,
		cancelHub:      cancelHub,
		listenURL:      listenURL,
		accessURL:      accessURL,
		hubDone:        make(chan struct{}),
		watcherDone:    make(chan struct{}),
		rootLease:      root,
		closeDirectory: d.closeDirectory,
		workerLease:    workerLease,
		failureCh:      make(chan struct{}),
	}
	// The Instance owns the root and worker leases now; its join()
	// releases them after the work joins. The server already owns the Hub
	// lease and releases it when Serve completes.
	committed = true

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
		inst.hubErr = d.serveHub(hubCtx, server)
		close(inst.hubDone)
	}()
	<-serving

	// One watcher handles caller cancellation, Hub completion, and Worker
	// failure. Each trigger uses the same ordered teardown.
	//
	// It deliberately REPORTS NOTHING. Every way the Hub can fail already has
	// one caller path: Start returns a failure during startup, and after Start
	// succeeds the error reaches its caller through Wait and Stop -- which
	// the combined CLI runner reports once. A log here would be a
	// second one for whichever of those the watcher happened to observe, which
	// is the duplicate report this shutdown work exists to remove, not a shape
	// of it to keep.
	go func() {
		defer close(inst.watcherDone)
		select {
		case <-ctx.Done():
		case <-inst.hubDone:
		case <-inst.failureCh:
		}
		inst.shutdown()
	}()

	// Wait for application readiness. The hubDone case ends the wait when Serve
	// fails before publishing Ready. A bound listener alone cannot prove that
	// application initialization completed.
	//
	// The select is a pure wait: both cases
	// choose WHEN to stop, never WHAT to report. That matters because both can be
	// ready at once for exactly the reason above, and Go picks between ready cases
	// arbitrarily; putting the verdict AFTER the select is what makes the pick
	// irrelevant. readyCh is buffered so the readiness wait cannot block on its
	// send after this caller leaves. The production wait ends when Ready closes
	// or cancelHub inside join cancels its context.
	//
	// The verdict is startupFailure alone, with no "unless hubErr is
	// context.Canceled" carve-out. That guard dated from when hubCtx was a child of
	// ctx and a parent cancel hit Serve instantly; hubCtx is detached now, so hubErr
	// can only be context.Canceled in a microsecond window -- and when the guard did
	// fire it returned a nil Instance, making hubErr permanently unreachable and
	// discarding any non-ctx cause joined into it.
	readyCh := make(chan error, 1)
	go func() { readyCh <- d.waitReady(hubCtx, server, listenURL) }()
	var readyErr error
	select {
	case readyErr = <-readyCh:
	case <-inst.hubDone:
		// No error of its own: startupFailure is total once hubDone is closed.
	}
	if startErr := inst.startupFailure(ctx, "before the Hub became ready"); startErr != nil {
		return nil, inst.failStartup(startErr)
	}

	if readyErr != nil {
		return nil, inst.failStartup(fmt.Errorf("wait for hub readiness: %w", readyErr))
	}
	inst.markHubReady()

	// In dev mode the first admin may not exist yet; defer worker bringup
	// until /setup completes.
	setupWorker := func(ctx context.Context) error {
		return d.bringUpWorker(ctx, workerBringUp{
			work:         &inst.workerWork,
			drained:      &inst.workerDrained,
			server:       server,
			workerLease:  workerLease,
			hubCfg:       hubCfg,
			listenURL:    listenURL,
			runWorker:    d.runWorker,
			onExit:       func(cause error) { inst.workerExited(workerCtx, cause) },
			onStarting:   inst.markWorkerStarting,
			onConnection: inst.updateWorkerConnection,
		})
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

	// One release, reached from every exit. The deferred call covers any future
	// return added inside this window; the explicit calls before failStartup are
	// what the failure paths need, because join waits on workerWork and would park
	// forever on tokens only this frame holds. sync.Once makes the pair idempotent,
	// so the two spellings cannot double-release and panic the counter -- the same
	// guard bringUpLocalWorker already uses for its own drain token.
	var releaseOnce sync.Once
	releaseBringUp := func() {
		releaseOnce.Do(func() {
			inst.workerDrained.Done()
			inst.workerWork.Done()
		})
	}
	defer releaseBringUp()

	err = setupWorker(workerCtx)

	// Bring-up is the only statement in this window with real work in it -- it
	// reads the state file and the worker's row; on a first launch, or when the
	// saved state is unusable, it also registers a worker (a DB write), generates
	// the keypair and writes the state file, and on a later launch it restores the
	// keypair with an SLH-DSA top-tree recompute -- some milliseconds against
	// microseconds for everything around it. So it is the window in which
	// a teardown most plausibly lands, and the one place a startup that was already
	// over would otherwise be reported as the Worker's fault: whichever trigger
	// fired, the watcher cancels workerCtx, so bring-up comes back saying "context
	// canceled" for something that is not its doing -- or, when bring-up happened
	// to succeed anyway, the launch went unreported entirely.
	//
	// Before the switch, so it covers all three cases, the dev-mode deferral
	// included: a poller must not be launched against a startup that is over.
	// What remains after it is a go statement, a counter release and a log line --
	// no wait -- so a Hub dying past this point is indistinguishable from one dying
	// an instruction after Start returns, which Wait and Stop report.
	if startErr := inst.startupFailure(ctx, "while the worker was starting"); startErr != nil {
		releaseBringUp()
		// A worker failure in the same window is usually the teardown's own echo --
		// the watcher cancelled workerCtx, so bring-up says "context canceled" -- and
		// reporting that is the misattribution this gate exists to stop. But an
		// INDEPENDENT failure (a locked database, an unreadable state file) is a
		// second fact the operator needs: dropping it sends them back for a relaunch
		// that hits the same wall with nothing in the logs from the first attempt.
		if err != nil && !errwrap.IsCancellation(err) {
			startErr = errors.Join(startErr, fmt.Errorf("worker bring-up: %w", err))
		}
		return nil, inst.failStartup(startErr)
	}

	switch {
	case err == nil:
		// Worker is up.
		inst.markWorkerReady()
	case errors.Is(err, errNoAdminYet) && cfg.DevMode:
		slog.Info("dev mode: deferring worker auto-registration until first admin signs up via /setup")
		inst.markWorkerPendingSetup()
		// Counted with the Worker, not the Hub: it is the thing that brings the
		// Worker up, so the ordered shutdown has to wait it out before it can
		// know no Worker is on its way. Added while the bring-up token is still
		// held, so the counter never dips to zero across the handover.
		inst.workerWork.Add()
		inst.workerDrained.Add()
		go inst.pollForDeferredWorkerSetup(workerCtx, setupWorker, d.clock)
	default:
		releaseBringUp()
		return nil, inst.failStartup(fmt.Errorf("auto-register worker: %w", err))
	}
	releaseBringUp()

	slog.Info("leapmux "+modeName+" listening", "listen", server.PrimaryListenAddr())

	return inst, nil
}

// soloState persists the auto-registered worker credentials.
type soloState struct {
	// PID is the process that wrote this file: stamped at write time by
	// persistState, and first in the JSON so a reader sees it before anything
	// else.
	PID          int    `json:"pid"`
	WorkerID     string `json:"worker_id"`
	AuthToken    string `json:"auth_token"`
	RegisteredBy string `json:"registered_by,omitempty"`
	// The same private-halves-only material the standalone Worker saves. The
	// public halves are derived from these, so a copy could only drift.
	workerconfig.KeyMaterial
}

// errNoAdminYet means no admin user exists yet, so the local worker has nobody to
// register under until somebody completes the /setup flow. It is the ONLY
// condition dev mode defers on, and it is a distinct sentinel rather than a bare
// store.ErrNotFound because that error reaches bring-up from several lookups --
// deferring on any of them would turn a real registration failure into a launch
// that reports success and polls forever.
var errNoAdminYet = errors.New("no admin user yet")

// pollForDeferredWorkerSetup retries setupWorker on a ticker until it succeeds,
// the context is cancelled, or a non-errNoAdminYet error occurs. Must be invoked
// as a goroutine after workerWork.Add() and workerDrained.Add(). This method
// releases both counters on exit and reports independent setup failures.
func (i *Instance) pollForDeferredWorkerSetup(ctx context.Context, setupWorker func(context.Context) error, clock quartz.Clock) {
	defer i.workerWork.Done()
	// Released here as well as inside bringUpLocalWorker: until a Worker exists
	// there is no drain to wait for, and a poller that gives up without ever
	// starting one must not hold the Hub for the backstop.
	defer i.workerDrained.Done()
	ticker := clock.NewTicker(workerSetupPollInterval, workerSetupPollTag)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		err := setupWorker(ctx)
		if err == nil {
			i.markWorkerReady()
			return
		}
		if errors.Is(err, errNoAdminYet) {
			continue
		}
		if ctx.Err() != nil && errwrap.IsCancellation(err) {
			// Requested cancellation is already part of shutdown. A joined
			// storage or cleanup failure must still reach the instance outcome.
			return
		}
		i.failWorker(fmt.Errorf("deferred worker setup: %w", err))
		return
	}
}

const workerSetupPollTag = "solo-deferred-worker-setup"

// workerBringUp groups the dependencies for registration and worker startup.
// Named fields keep the injected runner and its call sites on one signature.
type workerBringUp struct {
	work, drained *drain.Counter
	server        *hub.Server
	// workerLease is the worker component's held lease. bringUpLocalWorker
	// TRANSFERS it into worker.Run when the worker goroutine launches;
	// until then the Instance retains it, and the combined shutdown
	// closes it. Paths derive from lease.Path().
	workerLease  *lifecycle.DirectoryLease
	hubCfg       *hubconfig.Config
	listenURL    string
	runWorker    func(ctx context.Context, cfg worker.RunConfig) error
	onExit       func(error)
	onStarting   func(lifecycle.RegistrationStatus)
	onConnection func(worker.ConnectionState)
}

// bringUpLocalWorker loads (or creates, then persists) the local worker's
// registration state, obtains the keypair that state reproduces, and launches the
// worker goroutine under work/drained. It returns errNoAdminYet if no admin
// user exists yet; the caller is expected to retry after the first /setup signup
// completes. Callers must key on THAT sentinel, not on the store.ErrNotFound
// underneath it, which also reaches here from lookups that are real failures.
func bringUpLocalWorker(ctx context.Context, p workerBringUp) error {
	state, compositeKey, err := loadOrCreateWorkerState(ctx, p.server, p.workerLease)
	if err != nil {
		return err
	}
	if p.onStarting != nil {
		p.onStarting(lifecycle.RegistrationStatus{State: lifecycle.RegistrationRegistered, WorkerID: new(state.WorkerID), HubURL: new(p.listenURL)})
	}

	// The worker pins carry the hub instance's settings (payload ceiling,
	// startup and API timeouts), which are DB-backed now: resolved from the
	// running hub's settings snapshot rather than the launcher's config.
	snap := p.server.SettingsManager().Snapshot(ctx)
	workerTimeouts := settings.KeyTimeouts

	// readiness carries the worker's own verdict to this bring-up: nil
	// after local initialization and Hub identity acceptance, or its failure
	// when it never got there. Start does not return a healthy Instance
	// for a worker whose database never opened.
	ready := make(chan error, 1)
	var readinessMu sync.Mutex
	var localReady, accepted bool
	reportReady := func(local bool) {
		readinessMu.Lock()
		defer readinessMu.Unlock()
		if local {
			localReady = true
		} else {
			accepted = true
		}
		if localReady && accepted {
			select {
			case ready <- nil:
			default:
			}
		}
	}

	p.work.Add()
	p.drained.Add()
	// Guarded so the drain signal is released exactly once whether the Worker
	// reports draining or dies before it ever gets there -- an unreleased token
	// would make every shutdown pay the full backstop.
	var drainedOnce sync.Once
	releaseDrained := func() { drainedOnce.Do(p.drained.Done) }
	go func() {
		defer p.work.Done()
		defer releaseDrained()
		wErr := p.runWorker(ctx, worker.RunConfig{
			Drained:   releaseDrained,
			HubURL:    p.listenURL,
			Directory: p.workerLease,
			OnReady:   func() { reportReady(true) },
			OnConnectionState: func(state worker.ConnectionState) {
				if p.onConnection != nil {
					p.onConnection(state)
				}
				if state.Connected {
					reportReady(false)
				}
			},
			AuthToken:    state.AuthToken,
			CompositeKey: compositeKey,
			WorkerID:     state.WorkerID,
			DBMaxConns:   p.hubCfg.Storage.SQLite.MaxConns,
			DBCacheSize:  p.hubCfg.Storage.SQLite.CacheSize,
			DBMmapSize:   p.hubCfg.Storage.SQLite.MmapSize,
			// 0 (the default) lets the worker apply contracts.DefaultMaxIncompleteChunked.
			MaxIncompleteChunked: parseInt(p.hubCfg.Extras["max_incomplete_chunked"], 0),
			MaxMessageSize:       int(settings.KeyMaxMessageSizeBytes.Of(snap)),
			AgentStartupTimeout:  workerTimeouts.Of(snap).AgentStartupTimeout(),
			APITimeout:           workerTimeouts.Of(snap).APITimeout(),
			// 0 (the default) lets the worker resolve the concurrency from this
			// machine's core count.
			AgentStartupConcurrency: parseInt(p.hubCfg.Extras["agent_startup_concurrency"], 0),
			EncryptionMode:          workerconfig.ParseEncryptionMode(p.hubCfg.Extras["encryption_mode"]),
			UseLoginShell:           parseBool(p.hubCfg.Extras["use_login_shell"], true),
			RegisteredBy:            state.RegisteredBy,
		})
		if wErr == nil && ctx.Err() == nil {
			wErr = errors.New("the embedded worker exited unexpectedly without an error")
		}
		if p.onExit != nil {
			p.onExit(wErr)
		}
		if wErr != nil {
			// A startup that failed before readiness ends the bring-up
			// with its cause; a teardown-caused cancel does not.
			select {
			case ready <- wErr:
			default:
			}
			if p.onExit == nil {
				slog.Error("worker error", "error", wErr)
			}
		}
	}()

	// The bring-up verdict: the worker's local readiness, its startup
	// failure, or the context's end. A caller cancelling during the
	// worker's initialization reports through the stage gates above,
	// not as the worker's fault.
	select {
	case err := <-ready:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// workerRegistrar is the slice of *hub.Server that worker-state loading needs:
// resolve a worker's registration, find the user to attribute a new local worker
// to, register one, and retire an auto-registered row the launcher can no longer
// use. Depending on the four methods rather than the whole Server lets the
// state-file rules -- which owner wins, when to re-register, when to retire -- be
// tested without binding listeners and standing up a database.
type workerRegistrar interface {
	GetWorkerRegistration(ctx context.Context, workerID string) (hub.WorkerRegistration, error)
	GetAdminUser(ctx context.Context) (userID string, err error)
	RegisterWorker(ctx context.Context, userID string, keys *leapmuxv1.WorkerPublicKeys) (*hub.WorkerCredentials, error)
	RetireLocalWorker(ctx context.Context, workerID string) error
}

// loadOrCreateWorkerState returns the local Worker's state and its keypair: the
// saved ones when they can be launched (reuseSavedWorkerState), and a freshly
// registered Worker's otherwise.
func loadOrCreateWorkerState(ctx context.Context, server workerRegistrar, lease *lifecycle.DirectoryLease) (*soloState, *noiseutil.CompositeKeypair, error) {
	// The component directory exists durably and is owned by this
	// instance: acquisition created and validated it before any state
	// read. Every path derives from the lease.
	statePath := filepath.Join(lease.Path(), "state.json")

	saved, savedKey, err := reuseSavedWorkerState(ctx, server, statePath)
	if err != nil || saved != nil {
		return saved, savedKey, err
	}

	userID, err := server.GetAdminUser(ctx)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// The ONE condition dev mode defers on. Tagged here, at the only lookup
			// that can mean it, so the deferral case does not have to key on a bare
			// store.ErrNotFound from anywhere under bring-up -- a not-found from the
			// registration below would otherwise be misread as "waiting for /setup",
			// and the launch would report success and poll forever instead of failing.
			return nil, nil, fmt.Errorf("%w: %w", errNoAdminYet, err)
		}
		return nil, nil, err
	}

	// The keypair is generated only once an admin exists, so the dev-mode poller
	// generates none while it waits for /setup.
	state := &soloState{RegisteredBy: userID}
	compositeKey, err := state.GenerateKeypair()
	if err != nil {
		return nil, nil, err
	}
	keys, err := workerkeys.Of(compositeKey)
	if err != nil {
		return nil, nil, fmt.Errorf("derive the local worker's public keys: %w", err)
	}

	creds, err := server.RegisterWorker(ctx, userID, keys)
	if err != nil {
		return nil, nil, fmt.Errorf("register worker for admin %q: %w", userID, err)
	}
	state.WorkerID = creds.WorkerID
	state.AuthToken = creds.AuthToken

	if err := persistState(statePath, state); err != nil {
		// The row is pinned to private keys that only this failed write would
		// have kept, so it can never be used: retire it rather than leave an
		// ACTIVE Worker nobody can launch. The retire runs even when the launch
		// is cancelled meanwhile, because nothing else would ever retire the row.
		retireErr := server.RetireLocalWorker(context.WithoutCancel(ctx), state.WorkerID)
		if retireErr != nil {
			return nil, nil, errors.Join(err, fmt.Errorf("retire the worker whose state could not be saved: %w", retireErr))
		}
		return nil, nil, err
	}

	return state, compositeKey, nil
}

// reuseSavedWorkerState returns the saved local Worker state and its keypair when
// they can be launched. It returns nil values when there is nothing to launch: no
// file, or a file that it discarded. The caller then registers a fresh Worker,
// whose state overwrites the file.
//
// A saved state with a worker ID is usable only when it holds a token, the row has
// an owner and is ACTIVE, the keypair restores, and the keys it yields equal the
// ones the Hub pinned. Every other saved state takes the one path of
// discardSavedState.
//
// An error fails the launch and keeps state.json, so the next launch can retry.
// A transient store failure takes this path, and so does a failed retire.
func reuseSavedWorkerState(ctx context.Context, server workerRegistrar, statePath string) (*soloState, *noiseutil.CompositeKeypair, error) {
	data, err := os.ReadFile(statePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read state: %w", err)
	}

	// A file that is not a usable worker state comes from a partial write (power
	// loss or OS crash mid-write, before this launch made writes atomic), a
	// hand-edit, or a truncated tail. The saved identity is unrecoverable from
	// these bytes, so re-registering a fresh worker is the only forward path, but
	// it ORPHANS every workspace and tab still pointing at the old worker_id --
	// the same loss the transient-DB-error guard below exists to prevent. The DB
	// analogue fails the launch to keep the saved identity; here the identity is
	// already gone, so the best we can do is fail LOUDLY (Error, not a silent
	// fall-through) and preserve the corrupt file so an operator can re-link the
	// orphaned workspaces before the reaper collects them.
	var s soloState
	if err := json.Unmarshal(data, &s); err != nil {
		slog.Error("saved worker state is unreadable; re-registering a fresh worker, which orphans the previous one",
			"state_path", statePath, "err", err)
		return nil, nil, discardSavedState(ctx, server, statePath, data, "")
	}
	if s.WorkerID == "" {
		slog.Error("saved worker state is missing its worker id; re-registering a fresh worker, which orphans the previous one",
			"state_path", statePath)
		return nil, nil, discardSavedState(ctx, server, statePath, data, "")
	}

	// Take the owner from the DB, which is the authority: workers.registered_by
	// is NOT NULL and set at registration, and it is the fact that
	// the worker's owner gate uses to control the whole machine. The state
	// file's copy is a cache that can lag
	// it, and an empty one is not something to paper over -- a worker launched
	// with no owner has every machine-scoped family (file, git, sysinfo, tunnel)
	// permanently dead for its own legitimate user, failing closed in a way that
	// reads exactly like a genuine cross-tenant refusal.
	//
	// This replaces a backfill from GetAdminUser. Sourcing ownership from
	// "whoever the admin is now" answers a different question that merely shares
	// an answer on a fresh single-user install: on any install where the admin
	// changed, it silently reassigned the machine to them.
	registration, err := server.GetWorkerRegistration(ctx, s.WorkerID)
	if errors.Is(err, store.ErrNotFound) {
		// The worker row is genuinely gone (deleted, or never persisted), so there
		// is no row to retire.
		slog.Warn("saved worker not found in DB, re-registering", "worker_id", s.WorkerID)
		return nil, nil, discardSavedState(ctx, server, statePath, data, "")
	}
	if err != nil {
		// A transient store failure (for example a disk I/O error) is NOT
		// a deletion. Treating it as one would discard the saved WorkerID
		// and re-register a brand-new identity, orphaning every workspace
		// and tab still pointed at the old worker. Fail the launch so a
		// retry can find the row intact.
		return nil, nil, fmt.Errorf("look up saved worker %q owner: %w", s.WorkerID, err)
	}

	compositeKey, reason := usableLocalKeypair(&s, registration)
	if reason != "" {
		slog.Error("saved worker state is unusable; retiring it and re-registering a fresh worker, which orphans the previous one",
			"worker_id", s.WorkerID, "reason", reason)
		return nil, nil, discardSavedState(ctx, server, statePath, data, s.WorkerID)
	}

	if s.RegisteredBy != registration.Owner {
		// The file disagreed with the DB (or predates the field). The DB wins;
		// refresh the cache so the next launch matches without a second query.
		s.RegisteredBy = registration.Owner
		if err := persistState(statePath, &s); err != nil {
			// The in-memory correction still applies; a failed write only
			// costs the cache and forces this DB lookup again next launch.
			slog.Warn("failed to refresh worker state cache", "error", err)
		}
	}
	return &s, compositeKey, nil
}

// discardSavedState is the one way a launch gives up a saved worker state: it
// retires rowID, when the state identifies a row that exists, and then moves the
// file aside.
//
// The order matters. A retire that fails is a store error, which must fail the
// launch and leave state.json in place, so the next launch can retry. A file that
// is unreadable or that lacks the worker ID identifies no row, and neither does a
// saved ID that the store does not know, so such a caller passes an empty rowID.
func discardSavedState(ctx context.Context, server workerRegistrar, statePath string, data []byte, rowID string) error {
	if rowID != "" {
		if err := server.RetireLocalWorker(ctx, rowID); err != nil {
			if !errors.Is(err, hub.ErrNotAutoRegistered) {
				return fmt.Errorf("retire unusable local worker %q: %w", rowID, err)
			}
			slog.Warn("saved worker is not an auto-registered worker; leaving its row alone",
				"worker_id", rowID, "error", err)
		}
	}
	preserveDiscardedState(statePath, data)
	return nil
}

// usableLocalKeypair returns the keypair of a saved local Worker state that can
// be launched. When the state cannot be launched, it returns no keypair and a
// reason for the log: the state has no token, the row has no owner or is not
// ACTIVE, the Hub pinned keys this state cannot reproduce, or the keys it does
// reproduce are not the pinned ones.
//
// The check has to restore the keypair to compare its keys with the pin, so it
// returns that keypair: the restore recomputes the SLH-DSA public key, and a
// caller that restored it again would pay that cost twice on every launch.
func usableLocalKeypair(s *soloState, registration hub.WorkerRegistration) (*noiseutil.CompositeKeypair, string) {
	// The ID still identifies the row, which no launch can connect as without its
	// token, so the row must not stay ACTIVE and offline for good.
	if s.AuthToken == "" {
		return nil, "the auth token is missing"
	}
	// Unreachable via the schema (NOT NULL, set at registration), so this means a
	// hand-edited row or a mint path that dropped it.
	if registration.Owner == "" {
		return nil, "the worker row has no recorded owner"
	}
	if registration.Status != leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE {
		return nil, "the worker row is " + registration.Status.String()
	}
	if !s.HasKeypair() {
		return nil, "the keypair is missing"
	}
	compositeKey, err := s.CompositeKeypair()
	if err != nil {
		return nil, "the keypair does not restore: " + err.Error()
	}
	keys, err := workerkeys.Of(compositeKey)
	if err != nil {
		return nil, "the keypair yields no usable public keys: " + err.Error()
	}
	if !workerkeys.Equal(keys, registration.PublicKeys) {
		return nil, "the saved keys differ from the keys the hub pinned"
	}
	return compositeKey, ""
}

// persistState writes the worker state file with the one encoding and mode
// (pretty JSON, 0o600) every writer must agree on. Callers keep their own
// error posture -- the RegisteredBy cache refresh logs and continues, the
// initial registration fails the launch -- but the file's format contract
// lives here so a change cannot silently miss a write site.
//
// The write is atomic (temp file in the same directory, then rename) so a
// crash or power loss mid-write can never leave a half-written file behind.
// loadOrCreateWorkerState treats such a file as a lost identity and
// re-registers a fresh worker, orphaning every workspace still pointed at the
// old one -- the same loss a transient-DB-error at startup would cause if it
// were misread as a deletion -- so the write that produces the file the next
// launch reads must not be able to corrupt it.
func persistState(statePath string, s *soloState) error {
	s.PID = os.Getpid()
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	// Atomic, with mode 0600 surviving the rename: a crash mid-write must not
	// leave a truncated credentials file. atomicfile.WriteFile keeps the
	// temporary name unique per call, which is what makes the write safe
	// between processes -- every long-lived command rotates this token by
	// itself, and a shared temporary name would interleave their bytes.
	if err := atomicfile.WriteFile(statePath, data, 0o600); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	return nil
}

// preserveDiscardedState renames a worker-state file that this launch discards
// aside, so a later launch does not re-log the same orphaning and an operator can
// inspect or re-link it. Best-effort: a failure leaves the bytes in place for the
// next launch to warn about again, which is still better than silently
// overwriting.
func preserveDiscardedState(statePath string, data []byte) {
	sidecar := fmt.Sprintf("%s.corrupt-%d", statePath, time.Now().UnixNano())
	// RENAME, not copy. A copy leaves the unreadable file exactly where the next
	// load will read it again, and the dev-mode poller re-enters this path every
	// tick until somebody completes /setup -- re-logging the orphaning and writing
	// a fresh sidecar each time, unbounded. Moving it aside makes the next load see
	// os.ErrNotExist and re-register once.
	//
	// Best-effort, and the fallback still copies: leaving the bytes in place for
	// the next launch to warn about again beats losing them.
	if err := os.Rename(statePath, sidecar); err == nil {
		return
	}
	if err := os.WriteFile(sidecar, data, 0o600); err != nil {
		slog.Warn("could not preserve corrupt worker state for forensics", "sidecar", sidecar, "err", err)
	}
}

// shouldWarnNonLoopback reports whether solo.Start should emit the
// non-loopback security warning.
//
// It takes the hub's own answer about its LIVE listeners, so the warning
// covers every address the hub bound and not only the one -listen gave. Dev
// mode is exempt: it uses real password authentication from the start, so
// there is no credential-free state to warn about.
//
// The NoTCP test is gone with the string. A desktop hub binds no TCP address
// of its own, which used to make the whole question moot -- but it can gain
// one from the extra_listen_addresses setting, and that address exposes the
// hub exactly as much as a -listen one would.
func shouldWarnNonLoopback(cfg Config, exposed bool) bool {
	return !cfg.DevMode && exposed
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
		{Name: "use-login-shell", KoanfKey: "use_login_shell", Usage: "wrap each agent invocation in the user's login shell", StrDefault: "true"},
		{Name: "max-incomplete-chunked", KoanfKey: "max_incomplete_chunked", Usage: "maximum in-flight chunked sequences per channel for the embedded worker (default 4)", StrDefault: "0", Category: "Timeout and limit options"},
		{Name: "agent-startup-concurrency", KoanfKey: "agent_startup_concurrency", Usage: fmt.Sprintf("how many agents the embedded worker brings back at once after a restart (0 = %d, or the CPU count when that is lower)", workerconfig.DefaultMaxStartupConcurrency), StrDefault: "0", Category: "Timeout and limit options"},
	}
}

// defaultCLIFlags lists the subset of the HUB's own flags a solo or dev launcher
// puts on the command line. The rest of the hub's table is still reachable in
// these modes -- LoadWithOptions records every flag's default and koanf key
// before it consults this list, so the config file and LEAPMUX_HUB_* env vars
// see the whole surface. This list only decides what earns a line in --help.
//
// The list shrank to the process's bootstrap flags because everything else an
// operator tunes -- timeouts, per-user caps, SMTP, signup, session policy --
// moved into the hub_settings table: `leapmux control admin settings set limits
// '{"max_connections_per_user":...}'` changes them on a RUNNING solo or dev
// instance, where the old flags needed a relaunch. Solo is still the mode
// where the connection cap actually binds (every socket belongs to the one
// local user: an active tab holds two leases, and the desktop app, any CLI
// `remote` session and every worker's controlipc watcher draw on the same
// allowance) -- it is just tuned through the settings table now.
//
// use-login-shell is deliberately absent even though `leapmux solo
// --use-login-shell=false` works: this list holds HUB flags, and that one is an
// ExtraFlag (see defaultExtraFlags), which LoadWithOptions registers unconditionally
// without consulting this list. Listing it here was inert -- it matched no hub flag
// -- and implied the wrong home for it.
//
// The three queue budgets are deliberately NOT here. They auto-size off this
// machine's memory limit, which is almost always the right answer on a laptop,
// and are DB-backed restart-class settings for the rare case it is not.
func defaultCLIFlags() []string {
	return []string{
		"listen", "data-dir", "dev-frontend",
		"storage-sqlite-max-conns", "storage-sqlite-cache-size", "storage-sqlite-mmap-size",
		"log-level",
	}
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

// closeDirectoryLease keeps the per-start close dependency usable by incomplete
// instances. A missing lease has no release operation or cleanup failure.
func closeDirectoryLease(lease *lifecycle.DirectoryLease, operation func(*lifecycle.DirectoryLease) error) error {
	if lease == nil {
		return nil
	}
	if operation != nil {
		return operation(lease)
	}
	return lease.Close()
}
