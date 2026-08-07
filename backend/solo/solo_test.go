package solo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/hub"
	hubconfig "github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/locallisten/locallistentest"
	"github.com/leapmux/leapmux/util/drain"
)

// soloLoadOptions builds the options Start would pass, with the config file
// pointed at a path that does not exist so a real one on the developer's machine
// cannot reach the assertions.
func soloLoadOptions(t *testing.T, devMode bool) hubconfig.LoadOptions {
	t.Helper()
	return hubconfig.LoadOptions{
		CLIFlags:          defaultCLIFlags(devMode),
		ExtraFlags:        defaultExtraFlags(),
		DefaultConfigDir:  t.TempDir(),
		DefaultConfigFile: filepath.Join(t.TempDir(), "absent.yaml"),
		SoloMode:          !devMode,
	}
}

// TestSoloExposesThePerUserCapsOnTheCommandLine pins the two knobs a solo user can
// actually reach. Solo is the mode where every socket belongs to one identity --
// an active tab holds two leases, and the desktop app, any CLI `remote` session
// and every worker's remoteipc watcher draw on the same allowance -- so a user who
// hits the cap is told to close a tab, advice they cannot act on if --help offers
// no way to raise it.
func TestSoloExposesThePerUserCapsOnTheCommandLine(t *testing.T) {
	t.Parallel()

	assert.Subset(t, defaultCLIFlags(false),
		[]string{"max-connections-per-user", "max-workers-per-user"})

	cfg, _, err := hubconfig.LoadWithOptions(
		[]string{"--max-connections-per-user=64", "--max-workers-per-user=8"},
		soloLoadOptions(t, false))
	require.NoError(t, err, "solo must accept the per-user caps as CLI flags")
	assert.Equal(t, int64(64), cfg.MaxConnectionsPerUser)
	assert.Equal(t, int64(8), cfg.MaxWorkersPerUser)
}

// TestSoloKeepsTheQueueBudgetsOutOfHelpButReachableInConfig covers the half of the
// contract that is easy to break by accident: CLIFlags decides only what earns a
// line in --help, because LoadWithOptions records every flag's default and koanf
// key BEFORE it consults that list. A budget absent from the CLI must still be
// settable, or auto-sizing becomes the only option a solo user has.
func TestSoloKeepsTheQueueBudgetsOutOfHelpButReachableInConfig(t *testing.T) {
	t.Parallel()

	assert.NotContains(t, defaultCLIFlags(false), "relay-queue-memory-budget",
		"the budgets auto-size off this machine; they do not belong in solo's --help")

	_, _, err := hubconfig.LoadWithOptions(
		[]string{"--relay-queue-memory-budget=268435456"}, soloLoadOptions(t, false))
	require.Error(t, err, "a flag outside the allowlist must not parse")

	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path,
		[]byte("relay_queue_memory_budget: 268435456\n"), 0o600))

	opts := soloLoadOptions(t, false)
	opts.DefaultConfigFile = path
	cfg, _, err := hubconfig.LoadWithOptions(nil, opts)
	require.NoError(t, err)
	assert.Equal(t, int64(268435456), cfg.RelayQueueMemoryBudget,
		"the config file must still reach a key the CLI does not expose")
}

// TestDevModeAddsPublicURL guards the one flag that is dev-only: dev serves a
// frontend from a different origin, solo does not.
func TestDevModeAddsPublicURL(t *testing.T) {
	t.Parallel()

	assert.Contains(t, defaultCLIFlags(true), "public-url")
	assert.NotContains(t, defaultCLIFlags(false), "public-url")
	assert.Subset(t, defaultCLIFlags(true),
		[]string{"max-connections-per-user", "max-workers-per-user"},
		"dev must not lose the caps when it gains public-url")
}

func TestListenIsNonLoopback(t *testing.T) {
	t.Parallel()
	cases := []struct {
		listen string
		want   bool
	}{
		// Empty / missing host → all interfaces → warn.
		{"", true},
		{":4327", true},
		// Wildcard binds → warn.
		{"0.0.0.0:4327", true},
		{"[::]:4327", true},
		// Loopback → no warn.
		{"127.0.0.1:4327", false},
		{"127.0.0.5:4327", false}, // entire 127.0.0.0/8 is loopback
		{"[::1]:4327", false},
		{"localhost:4327", false},
		// Non-loopback IPs → warn.
		{"192.168.1.10:4327", true},
		{"100.64.1.2:4327", true}, // Tailscale CGNAT range
		{"10.0.0.5:4327", true},
		// Unparseable / hostname-only → warn (conservative).
		{"garbage", true},
		{"hostonly:4327", true},
	}
	for _, tc := range cases {
		got := listenIsNonLoopback(tc.listen)
		if got != tc.want {
			t.Errorf("listenIsNonLoopback(%q) = %v, want %v", tc.listen, got, tc.want)
		}
	}
}

// The literal omits both cancels and both wait groups on purpose: Instance is
// reachable at its zero value from every shutdown trigger, and a Stop that
// dereferenced a field Start had not filled in would wedge or panic on the
// startup failure paths that shut down a half-built instance.
func TestInstanceStopReturnsHubError(t *testing.T) {
	wantErr := errors.New("lease release failed")
	hubDone := make(chan struct{})
	close(hubDone)
	inst := &Instance{
		hubErr:  wantErr,
		hubDone: hubDone,
	}

	require.ErrorIs(t, inst.Stop(), wantErr)
}

// orderLog records a shutdown's steps so their ORDER can be asserted, which is
// the whole property under test — the Worker's last frames only reach the
// frontend if the Hub is still up to carry them.
type orderLog struct {
	mu    sync.Mutex
	steps []string
}

func (o *orderLog) mark(step string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.steps = append(o.steps, step)
}

func (o *orderLog) snapshot() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.steps...)
}

func TestInstanceShutdown_DrainsTheWorkerBeforeStoppingTheHub(t *testing.T) {
	var order orderLog
	hubDone := make(chan struct{})
	close(hubDone)

	workerCancelled := make(chan struct{})
	inst := &Instance{
		cancelWorker: func() {
			order.mark("worker cancelled")
			close(workerCancelled)
		},
		cancelHub: func() { order.mark("hub cancelled") },
		hubDone:   hubDone,
	}

	// Stands in for worker.Run: it does not report draining until it has been
	// cancelled, and its local teardown (the database close) outlives the drain,
	// exactly as production's does.
	inst.workerWork.Add()
	inst.workerDrained.Add()
	go func() {
		defer inst.workerWork.Done()
		<-workerCancelled
		order.mark("worker drained")
		inst.workerDrained.Done()
	}()

	inst.shutdown()

	assert.Equal(t, []string{"worker cancelled", "worker drained", "hub cancelled"}, order.snapshot())
}

func TestInstanceShutdown_StopsTheHubWhenTheWorkerWillNotDrain(t *testing.T) {
	orig := workerDrainTimeout
	workerDrainTimeout = 20 * time.Millisecond
	t.Cleanup(func() { workerDrainTimeout = orig })

	// Released only in cleanup, so the Worker is still parked when shutdown has
	// to decide whether to keep waiting for it.
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	hubDone := make(chan struct{})
	close(hubDone)
	var hubCancelled atomic.Bool
	inst := &Instance{
		cancelWorker: func() {},
		cancelHub:    func() { hubCancelled.Store(true) },
		hubDone:      hubDone,
	}
	inst.workerWork.Add()
	inst.workerDrained.Add()
	go func() {
		defer inst.workerWork.Done()
		<-release
	}()

	inst.shutdown()

	assert.True(t, hubCancelled.Load(),
		"a worker that never reports draining must not keep the hub up past the backstop")
}

func TestInstanceShutdown_IsIdempotent(t *testing.T) {
	hubDone := make(chan struct{})
	close(hubDone)
	var workerCancels, hubCancels atomic.Int32
	inst := &Instance{
		cancelWorker: func() { workerCancels.Add(1) },
		cancelHub:    func() { hubCancels.Add(1) },
		hubDone:      hubDone,
	}

	// Both triggers, twice: Stop, the caller's context and the Hub exiting on
	// its own all funnel here, and any of them can arrive more than once.
	inst.shutdown()
	require.NoError(t, inst.Stop())
	require.NoError(t, inst.Stop())

	assert.Equal(t, int32(1), workerCancels.Load())
	assert.Equal(t, int32(1), hubCancels.Load())
}

// TestDefaultExtraFlagsCarryWorkerScopedKnobs pins that solo's extra flags are the
// worker-scoped settings the embedded worker needs. max-incomplete-chunked is the
// load-bearing case: it is NOT a hub setting (the Hub's chunk-count cap is
// unreachable -- channelmgr admits one in-flight sequence per channel+direction),
// so solo is the only place it can be tuned for the embedded worker. If it ever
// drops out of this list again, `leapmux solo -max-incomplete-chunked=N` starts
// failing with "flag provided but not defined" while the docs still advertise it.
func TestDefaultExtraFlagsCarryWorkerScopedKnobs(t *testing.T) {
	byName := map[string]hubconfig.ExtraFlagDef{}
	for _, ef := range defaultExtraFlags() {
		byName[ef.Name] = ef
	}
	for _, name := range []string{"encryption-mode", "use-login-shell", "max-incomplete-chunked"} {
		require.Contains(t, byName, name, "solo must expose the worker-scoped %q flag", name)
	}

	chunked := byName["max-incomplete-chunked"]
	assert.Equal(t, "max_incomplete_chunked", chunked.KoanfKey,
		"the koanf key is what bringUpLocalWorker reads out of Extras")
	assert.Equal(t, "0", chunked.StrDefault,
		"0 must be the default so the worker applies channelwire.DefaultMaxIncompleteChunked")
	assert.Equal(t, "Timeout and limit options", chunked.Category,
		"it is a limit, not a server option -- the help output groups it accordingly")

	_, hasMaxMessageSizeExtra := byName["max-message-size"]
	assert.False(t, hasMaxMessageSizeExtra,
		"max-message-size is a first-class hub flag forwarded into worker.RunConfig, not an Extra")
}

// TestParseIntReadsTheStringTypedExtras covers the Extras -> RunConfig hop. Extras
// is string-typed (koanf reads every extra with k.String), so a malformed or absent
// value must degrade to the default rather than silently becoming 0-and-meaningful.
func TestParseIntReadsTheStringTypedExtras(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		defaultVal int
		want       int
	}{
		{"a set value", "8", 0, 8},
		{"surrounding whitespace", "  8  ", 0, 8},
		{"the unset default", "0", 0, 0},
		{"absent key", "", 0, 0},
		{"absent key with a non-zero default", "", 4, 4},
		{"garbage falls back", "lots", 4, 4},
		{"a float is not an int", "8.5", 4, 4},
		{"negative parses (the worker clamps <=0 to its default)", "-1", 0, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, parseInt(tt.in, tt.defaultVal))
		})
	}
}

// soloTestTimeout bounds every wait in this file that could otherwise hang: the
// contexts handed to Start, and the hang detectors on the goroutine-driven tests.
// It is a BACKSTOP, never a timing assertion -- each of those waits also has an
// explicit signal that arrives in microseconds on a working tree, so a run that
// reaches this budget has already failed.
const soloTestTimeout = 60 * time.Second

// stubServeHub swaps the Hub-serving seam for the duration of the test.
func stubServeHub(t *testing.T, fn func(context.Context, *hub.Server) error) {
	t.Helper()
	prev := serveHub
	serveHub = fn
	t.Cleanup(func() { serveHub = prev })
}

// stubWorkerBringUp swaps the Worker bring-up seam, absorbing the seven
// production-wiring parameters so each test states only what it does with the
// context and what it reports.
func stubWorkerBringUp(t *testing.T, fn func(context.Context) error) {
	t.Helper()
	prev := bringUpWorker
	bringUpWorker = func(ctx context.Context, _, _ *drain.Counter, _ *hub.Server,
		_, _ string, _ *hubconfig.Config, _, _ string) error {
		return fn(ctx)
	}
	t.Cleanup(func() { bringUpWorker = prev })
}

// serveUntilKilled stubs serveHub with a REAL s.Serve that runs until kill(),
// and reports serveErr once it has stopped.
//
// This is what makes the startup-window tests ORDERED rather than raced: the Hub
// is provably serving when Start passes readiness, and provably finished
// afterwards. A stub that failed immediately could not distinguish "the gate saw
// a dead Hub" from "the gate was never reached".
//
// kill is idempotent and never blocks, and t.Cleanup calls it, so a failed
// assertion cannot leave a Hub serving behind the test.
func serveUntilKilled(t *testing.T, serveErr error) (kill func()) {
	t.Helper()
	killed := make(chan struct{})
	stubServeHub(t, func(ctx context.Context, s *hub.Server) error {
		inner, cancel := context.WithCancel(ctx)
		defer cancel()
		go func() {
			select {
			case <-killed:
				cancel()
			case <-inner.Done():
				// Serve stopped on its own (or the Hub was cancelled); nothing to do.
			}
		}()
		_ = s.Serve(inner)
		return serveErr
	})
	var once sync.Once
	kill = func() { once.Do(func() { close(killed) }) }
	t.Cleanup(kill)
	return kill
}

// awaitBringUp blocks until Start has entered the bring-up seam.
//
// Its two failure arms are diagnostics, not assertions: done closing first means
// Start ran the REAL bring-up (the seam is not wired), and the budget expiring
// means Start never got there at all. Both would otherwise present as a test that
// hangs until the package deadline.
func awaitBringUp(t *testing.T, entered, done <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-done:
		t.Fatal("Start finished without reaching bring-up (is the seam wired?)")
	case <-time.After(soloTestTimeout):
		t.Fatal("Start never reached bring-up")
	}
}

// startFailureEnv points Start at a throwaway home with no TCP listener, so it
// gets all the way to Serve -- which the seam then fails -- instead of dying
// earlier in hub.NewServer.
//
// It returns the Config to hand Start. The local-listen override is what makes
// these tests independent of each other: SandboxHome redirects HOME, which is
// enough to move the Unix socket under a private directory, but the WINDOWS
// default is npipe:leapmux-hub-<SID> -- derived from the account, not the home
// -- so every Hub-starting test in this package bound the same process-global
// pipe name and the second one to reach it failed hub.NewServer with
// "Access is denied". That is what makes these three tests fail on Windows CI
// while passing on a developer's Unix machine.
func startFailureEnv(t *testing.T) Config {
	t.Helper()
	locallistentest.SandboxHome(t)
	return Config{
		SkipBanner: true,
		NoTCP:      true,
		// local-listen is not in solo's own --help allowlist, so the test has to
		// widen it to reach the flag.
		CLIFlags: append(defaultCLIFlags(false), "local-listen"),
		Args:     []string{"--local-listen=" + locallistentest.UniqueListenURL(t, "leapmux-hub-solo")},
	}
}

// devStartFailureEnv is startFailureEnv in DEV mode, which is where the
// deferral arm lives: dev uses real password auth rather than solo's injected
// soloUser, so on a fresh install there is no admin for the Worker to register
// under until somebody completes /setup.
func devStartFailureEnv(t *testing.T) Config {
	t.Helper()
	cfg := startFailureEnv(t)
	cfg.DevMode = true
	cfg.CLIFlags = append(defaultCLIFlags(true), "local-listen")
	return cfg
}

// A Hub that dies WHILE SERVING is surfaced by Start with the "hub serve:"
// attribution, rather than as the generic WaitReady timeout.
//
// The seam is what makes this reachable: hub.NewServer binds both listeners, so
// the pre-existing version of this test pointed at an unwritable socket path,
// failed in NewServer, and passed only because "create hub server" happens to
// contain the substring "hub serve".
func TestSoloStart_SurfacesAServeTimeFailure(t *testing.T) {
	cfg := startFailureEnv(t)
	wantErr := errors.New("revocation watcher seed failed")
	stubServeHub(t, func(context.Context, *hub.Server) error { return wantErr })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	inst, err := Start(ctx, cfg)

	require.Error(t, err)
	require.Nil(t, inst)
	assert.ErrorIs(t, err, wantErr)
	assert.True(t, strings.HasPrefix(err.Error(), "hub serve:"),
		"the Serve arm must attribute the failure, not merely mention it; got: "+err.Error())
}

// A Hub that dies on its way up has exactly ONE reporter: Start, which RETURNS
// the error. The watcher sees the same hubDone close and must stay quiet, or
// every failed launch is reported twice.
func TestSoloStart_DoesNotAlsoLogAServeTimeFailure(t *testing.T) {
	cfg := startFailureEnv(t)
	stubServeHub(t, func(context.Context, *hub.Server) error {
		return errors.New("revocation watcher seed failed")
	})

	// Stderr, not slog.Default(): Start's own logging.Setup installs a fresh
	// default logger, so a captured handler would be replaced before the line
	// under test could reach it and this would assert on an empty buffer.
	var err error
	out := testutil.CaptureStderr(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err = Start(ctx, cfg)
	})

	require.Error(t, err)
	assert.NotContains(t, out, "hub error",
		"the failure Start returns must not also be logged; captured:\n"+out)
}

// The watcher's OTHER arm: a Hub that dies after Start has handed off. The
// watcher must run the ordered teardown so the Worker is not left looping
// against a dead endpoint -- and must still say nothing, because Wait and Stop
// both hand the error to a caller that waitSolo documents as its single
// reporter.
func TestSolo_AHubThatDiesWhileServingTearsTheInstanceDownWithoutReporting(t *testing.T) {
	cfg := startFailureEnv(t)

	wantErr := errors.New("hub died mid-session")
	serving := make(chan struct{})
	var killHub atomic.Pointer[context.CancelFunc]
	stubServeHub(t, func(ctx context.Context, s *hub.Server) error {
		inner, cancel := context.WithCancel(ctx)
		killHub.Store(&cancel)
		close(serving)
		_ = s.Serve(inner)
		return wantErr
	})

	var inst *Instance
	out := testutil.CaptureStderr(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		var err error
		inst, err = Start(ctx, cfg)
		require.NoError(t, err, "the Hub must come up before this test can kill it")

		<-serving
		(*killHub.Load())()

		// Wait returns as hubDone closes, which is also what wakes the watcher,
		// so join the teardown before reading what it logged.
		assert.ErrorIs(t, inst.Wait(), wantErr)
		_ = inst.Stop()
	})

	assert.NotContains(t, out, "hub error",
		"Wait/Stop own the report; the watcher must not add a second one; captured:\n"+out)
}

// The other half of the report gate: an exit somebody ASKED for. Stop hands that
// error straight back and waitSolo documents itself as its single reporter, so
// logging it here as well would restore -- at the far end of the lifecycle --
// exactly the duplicate report the watcher's silence removes for startup.
func TestSolo_AStopRequestedHubErrorIsReportedOnlyByStop(t *testing.T) {
	cfg := startFailureEnv(t)

	wantErr := errors.New("lease release failed")
	stubServeHub(t, func(ctx context.Context, s *hub.Server) error {
		_ = s.Serve(ctx)
		// The shape of a real teardown cleanup failure: Serve returns non-nil
		// after an ordinary, requested shutdown.
		return wantErr
	})

	var stopErr error
	out := testutil.CaptureStderr(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		inst, err := Start(ctx, cfg)
		require.NoError(t, err)
		stopErr = inst.Stop()
	})

	assert.ErrorIs(t, stopErr, wantErr, "Stop must still hand the error back")
	assert.NotContains(t, out, "hub error",
		"an exit Stop asked for must not also be logged; captured:\n"+out)
}

// hubFailure is the ONE predicate Start asks at every stage boundary, so an
// exited Hub outranks whatever that stage happened to observe. The property
// under test is TOTALITY: once hubDone is closed it never returns nil. The
// readiness select leans on that -- its hubDone arm carries no error of its own
// -- so a hole here silently turns a dead Hub back into a healthy Instance.
//
// This is also where the readiness race is pinned. The race itself cannot be
// forced end-to-end: nothing observable sits between the Hub goroutine's start
// and the select, so no test can guarantee hubDone is closed AT the select
// without a sleep. The fix does not win that race, it makes the arm choice
// irrelevant -- which is a local property, testable exactly here.
func TestHubFailure_AnExitedHubOutranksWhateverTheStageSaw(t *testing.T) {
	t.Parallel()

	closedChan := func() chan struct{} {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	hubErr := errors.New("revocation watcher seed failed")
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name string
		// The literals are bare on purpose, like TestInstanceStopReturnsHubError's:
		// hubFailure must be safe on the zero-value Instance every shutdown trigger
		// can reach.
		inst    *Instance
		ctx     context.Context
		want    string
		wantIs  error
		wantNil bool
	}{
		{
			// Half-built Instances are reachable from every shutdown trigger, so
			// this must answer rather than block, like shutdown and join do.
			name:    "a nil hubDone reads as still serving",
			inst:    &Instance{},
			ctx:     context.Background(),
			wantNil: true,
		},
		{
			// hubErr is set and must still be ignored: the gate is hubDone, because
			// only hubDone orders the read of hubErr.
			name:    "an open hubDone outranks an already-set hubErr",
			inst:    &Instance{hubDone: make(chan struct{}), hubErr: hubErr},
			ctx:     context.Background(),
			wantNil: true,
		},
		{
			name:   "an exited hub reports its own error",
			inst:   &Instance{hubDone: closedChan(), hubErr: hubErr},
			ctx:    context.Background(),
			want:   "hub serve: revocation watcher seed failed",
			wantIs: hubErr,
		},
		{
			// Totality: no error of its own, no cancelled caller, and it STILL must
			// not return nil. This is the case the empty select arm depends on.
			name: "an exited hub with no error of its own names the stage",
			inst: &Instance{hubDone: closedChan()},
			ctx:  context.Background(),
			want: "hub serve exited while the worker was starting",
		},
		{
			// hubErr is nil here, so the cancelled caller is the best cause
			// available; %w keeps errors.Is reaching it.
			name:   "a cancelled caller is reported as the cause when the hub had none",
			inst:   &Instance{hubDone: closedChan()},
			ctx:    cancelledCtx,
			want:   "hub shut down while the worker was starting: context canceled",
			wantIs: context.Canceled,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.inst.hubFailure(tt.ctx, "while the worker was starting")
			if tt.wantNil {
				assert.NoError(t, got)
				return
			}
			require.Error(t, got, "hubFailure must be total once hubDone is closed")
			assert.Equal(t, tt.want, got.Error())
			if tt.wantIs != nil {
				assert.ErrorIs(t, got, tt.wantIs)
			}
		})
	}
}

// What the readiness gate buys over the bring-up gate downstream, which would
// catch the same dead Hub a moment later: bring-up is not cheap. It opens SQLite,
// registers a worker (a DB WRITE), writes a state file and, on a first launch,
// generates an ML-KEM + SLH-DSA keypair -- seconds of work aimed at a Hub that is
// already shutting down, every bit of it discarded. A Hub known to be gone must
// stop the launch where it is discovered.
//
// The ordering is Start's own `serving` handshake, not a timing assumption: the
// Hub goroutine runs close(serving), the stub's immediate return and
// close(hubDone) consecutively, so hubDone is closed before the readiness verdict
// -- which in turn waits out a connect syscall.
func TestSoloStart_DoesNotBringTheWorkerUpAgainstAHubThatAlreadyExited(t *testing.T) {
	cfg := startFailureEnv(t)
	wantErr := errors.New("revocation watcher seed failed")
	stubServeHub(t, func(context.Context, *hub.Server) error { return wantErr })

	var broughtUp atomic.Bool
	stubWorkerBringUp(t, func(context.Context) error {
		broughtUp.Store(true)
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), soloTestTimeout)
	defer cancel()
	inst, err := Start(ctx, cfg)

	require.Error(t, err)
	require.Nil(t, inst)
	assert.ErrorIs(t, err, wantErr)
	assert.False(t, broughtUp.Load(),
		"a Hub already known to be gone must not have a Worker brought up against it")
}

// The literal macOS CI failure from PR #364: a Hub that dies while the Worker is
// coming up was reported as "auto-register worker: create worker: context
// canceled". The Worker only said that BECAUSE the Hub died -- the watcher
// cancels workerCtx the moment it observes hubDone -- so blaming it sends every
// reader after the wrong bug.
func TestSoloStart_AHubThatDiesDuringBringUpIsNotBlamedOnTheWorker(t *testing.T) {
	cfg := startFailureEnv(t)
	wantErr := errors.New("revocation watcher seed failed")
	kill := serveUntilKilled(t, wantErr)

	entered := make(chan struct{})
	stubWorkerBringUp(t, func(ctx context.Context) error {
		close(entered)
		// Returns only once the watcher has cancelled workerCtx, which it does
		// after observing hubDone -- so the gate provably runs against a Hub that
		// has already exited, with no sleep and no race.
		<-ctx.Done()
		return fmt.Errorf("create worker: %w", ctx.Err())
	})

	var inst *Instance
	var err error
	done := make(chan struct{})
	go func() {
		defer close(done)
		ctx, cancel := context.WithTimeout(context.Background(), soloTestTimeout)
		defer cancel()
		inst, err = Start(ctx, cfg)
	}()

	awaitBringUp(t, entered, done)
	kill()
	<-done

	require.Error(t, err)
	require.Nil(t, inst)
	assert.ErrorIs(t, err, wantErr)
	assert.True(t, strings.HasPrefix(err.Error(), "hub serve:"),
		"the Hub's death must be attributed to the Hub; got: "+err.Error())
	assert.NotContains(t, err.Error(), "auto-register worker",
		"the Worker only failed because the Hub did; got: "+err.Error())
}

// A Hub that dies during bring-up must not yield an Instance, even when bring-up
// itself succeeds -- the case with no reporter at all on the desktop path, whose
// soloInstance interface is LocalListenURL/Stop with no Wait. There, Start
// returning nil means the sidecar announces a successful launch, installs its
// webview log handler, and the UI then fails to connect with nothing attributing
// it to the Hub.
func TestSoloStart_DoesNotReturnAnInstanceForAHubThatDiedDuringBringUp(t *testing.T) {
	tests := []struct {
		name     string
		serveErr error
	}{
		{"a hub that failed", errors.New("revocation watcher seed failed")},
		// The only place the stage string is observable: no hubErr, no cancelled
		// caller, so the message is all that is left to say the Hub is gone.
		{"a hub that merely exited", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := startFailureEnv(t)
			kill := serveUntilKilled(t, tt.serveErr)

			entered := make(chan struct{})
			stubWorkerBringUp(t, func(ctx context.Context) error {
				close(entered)
				<-ctx.Done()
				// Bring-up SUCCEEDED -- from a window in which the Hub had already
				// died. Nothing downstream of here asks again, so without the gate
				// Start hands back a healthy Instance for a stopped Hub.
				return nil
			})

			var inst *Instance
			var err error
			done := make(chan struct{})
			go func() {
				defer close(done)
				ctx, cancel := context.WithTimeout(context.Background(), soloTestTimeout)
				defer cancel()
				inst, err = Start(ctx, cfg)
			}()

			awaitBringUp(t, entered, done)
			kill()
			<-done

			require.Error(t, err, "a dead Hub must not be reported as a successful launch")
			require.Nil(t, inst)
			if tt.serveErr != nil {
				assert.ErrorIs(t, err, tt.serveErr)
				assert.True(t, strings.HasPrefix(err.Error(), "hub serve:"),
					"got: "+err.Error())
			} else {
				assert.Equal(t, "hub serve exited while the worker was starting", err.Error())
			}
		})
	}
}

// The deferral arm, which had no coverage at all before the bring-up seam
// existed: in dev mode a missing admin is what /setup is for, so the launch must
// SUCCEED and leave a poller behind rather than fail.
func TestSoloStart_DevModeDefersWorkerRegistrationUntilAnAdminExists(t *testing.T) {
	cfg := devStartFailureEnv(t)
	// A real Serve, but with its terminal error pinned to nil. With bring-up
	// stubbed, Start returns fast enough that Stop's cancel can land inside the
	// Hub's OWN startup -- seeding the revocation cursor, which surfaces as a
	// SQLite "interrupted" -- and that race has nothing to do with the arm under
	// test. Pinning it keeps the Stop assertion below about token accounting.
	serveUntilKilled(t, nil)
	// Re-entrant: the poller calls this again on every tick.
	stubWorkerBringUp(t, func(context.Context) error { return store.ErrNotFound })

	var inst *Instance
	var err error
	out := testutil.CaptureStderr(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), soloTestTimeout)
		defer cancel()
		inst, err = Start(ctx, cfg)
	})

	require.NoError(t, err, "a missing admin defers the Worker; it does not fail the launch; captured:\n"+out)
	require.NotNil(t, inst)
	assert.Contains(t, out, "deferring worker auto-registration",
		"the deferral is the user-visible half of this arm; captured:\n"+out)
	// Stop is the token-accounting assertion: the deferral hands a second worker
	// token pair to the poller while Start still holds its own, and an imbalance
	// on either side either wedges the teardown or trips drain.Counter's
	// negative-counter panic.
	assert.NoError(t, inst.Stop())
}

// The gate sits BEFORE the switch, so it covers the deferral arm too. Without
// that placement a dev-mode launch whose Hub died during bring-up would return a
// healthy Instance AND leave a poller ticking every 2s against a Hub that is
// already gone.
func TestSoloStart_DoesNotDeferWorkerSetupOntoAHubThatDied(t *testing.T) {
	cfg := devStartFailureEnv(t)
	wantErr := errors.New("revocation watcher seed failed")
	kill := serveUntilKilled(t, wantErr)

	entered := make(chan struct{})
	stubWorkerBringUp(t, func(ctx context.Context) error {
		close(entered)
		<-ctx.Done()
		// The arm that would defer, if the gate let it get that far.
		return store.ErrNotFound
	})

	var inst *Instance
	var err error
	done := make(chan struct{})
	out := testutil.CaptureStderr(t, func() {
		go func() {
			defer close(done)
			ctx, cancel := context.WithTimeout(context.Background(), soloTestTimeout)
			defer cancel()
			inst, err = Start(ctx, cfg)
		}()
		awaitBringUp(t, entered, done)
		kill()
		<-done
	})

	require.Error(t, err)
	require.Nil(t, inst)
	assert.ErrorIs(t, err, wantErr)
	assert.True(t, strings.HasPrefix(err.Error(), "hub serve:"), "got: "+err.Error())
	assert.NotContains(t, out, "deferring worker auto-registration",
		"a Hub that is gone has nothing to defer to; captured:\n"+out)
}

// The over-reach guard on the gate above, and the first coverage of the switch's
// default arm: with the Hub still serving, a bring-up failure is still the
// Worker's, reported with the Worker's attribution.
func TestSoloStart_AWorkerBringUpFailureIsStillTheWorkers(t *testing.T) {
	tests := []struct {
		name       string
		bringUpErr error
	}{
		{"a generic failure", errors.New("generate composite keypair: no entropy")},
		// Outside dev mode there is nothing to defer to: no /setup flow will ever
		// create the admin this is waiting for, so it must fail the launch rather
		// than fall into the deferral arm.
		{"no admin user outside dev mode", store.ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// No serveHub stub: the Hub is real and still serving, which is the
			// whole point -- the gate must not claim a failure that is not its own.
			cfg := startFailureEnv(t)
			stubWorkerBringUp(t, func(context.Context) error { return tt.bringUpErr })

			ctx, cancel := context.WithTimeout(context.Background(), soloTestTimeout)
			defer cancel()
			inst, err := Start(ctx, cfg)

			require.Error(t, err)
			require.Nil(t, inst)
			assert.ErrorIs(t, err, tt.bringUpErr)
			assert.True(t, strings.HasPrefix(err.Error(), "auto-register worker:"),
				"a live Hub leaves the failure the Worker's; got: "+err.Error())
			assert.NotContains(t, err.Error(), "hub serve",
				"the Hub was still serving; got: "+err.Error())
		})
	}
}

// The point of the drain signal: the Hub stops as soon as the last frame is
// away, NOT when the Worker has finished its local teardown. Waiting on the
// latter held the Hub up for a database close it has no stake in, and reported a
// slow one as a worker that failed to drain.
func TestInstanceShutdown_StopsTheHubOnTheDrainNotTheWorkersFullExit(t *testing.T) {
	orig := workerDrainTimeout
	workerDrainTimeout = 20 * time.Millisecond
	t.Cleanup(func() { workerDrainTimeout = orig })

	hubDone := make(chan struct{})
	close(hubDone)
	var hubCancelled atomic.Bool
	workerCancelled := make(chan struct{})
	inst := &Instance{
		cancelWorker: func() { close(workerCancelled) },
		cancelHub:    func() { hubCancelled.Store(true) },
		hubDone:      hubDone,
	}

	// Drains promptly, then stays alive far past the backstop -- the shape of a
	// worker whose database close is slow.
	teardownDone := make(chan struct{})
	t.Cleanup(func() { <-teardownDone })
	releaseTeardown := make(chan struct{})
	t.Cleanup(func() { close(releaseTeardown) })
	inst.workerWork.Add()
	inst.workerDrained.Add()
	go func() {
		defer close(teardownDone)
		defer inst.workerWork.Done()
		<-workerCancelled
		inst.workerDrained.Done()
		<-releaseTeardown
	}()

	start := time.Now()
	logs := testutil.CaptureStderr(t, func() { inst.shutdown() })

	assert.True(t, hubCancelled.Load())
	assert.Less(t, time.Since(start), workerDrainTimeout,
		"the hub must stop on the drain signal, not wait out the backstop for a teardown it has no stake in")
	assert.NotContains(t, logs, "did not report draining",
		"a worker that drained promptly must not be reported as failing to")
}
