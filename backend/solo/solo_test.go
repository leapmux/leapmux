package solo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/hub"
	hubconfig "github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/store"
	sqlitest "github.com/leapmux/leapmux/internal/hub/store/sqlite"
	"github.com/leapmux/leapmux/internal/logging"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/testutil"
	workerconfig "github.com/leapmux/leapmux/internal/worker/config"
	workerhub "github.com/leapmux/leapmux/internal/worker/hub"
	"github.com/leapmux/leapmux/lifecycle"
	"github.com/leapmux/leapmux/locallisten"
	"github.com/leapmux/leapmux/locallisten/locallistentest"
	"github.com/leapmux/leapmux/worker"
)

// testLoadOptions builds the options Start would pass, through the PRODUCTION
// derivation, with the config file pointed at a path that does not exist so a real
// one on the developer's machine cannot reach the assertions.
//
// It delegates rather than restating the option construction: a hand-rolled copy
// is what let the CLI's flag list drift out from under these very assertions.
// soloLoadOptions is the single source of truth for what `leapmux solo` and
// `leapmux dev` default to. It became one because the CLI carried a second copy
// that drifted until --max-connections-per-user stopped parsing; a table test is
// what makes the next drift a red build instead of a support question.
func TestSoloLoadOptionsDerivesEachModesDefaults(t *testing.T) {
	t.Parallel()

	solo, soloMode := soloLoadOptions(Config{})
	assert.Equal(t, "solo", soloMode)
	assert.Equal(t, "127.0.0.1:4327", solo.DefaultListen, "solo binds loopback only")
	assert.Equal(t, "~/.config/leapmux/solo", solo.DefaultConfigDir)
	assert.Equal(t, "~/.config/leapmux/solo/solo.yaml", solo.DefaultConfigFile)
	assert.Equal(t, "leapmux solo", solo.FlagSetName)
	assert.True(t, solo.SoloMode, "solo mode injects the soloUser; dev does not")

	dev, devMode := soloLoadOptions(Config{DevMode: true})
	assert.Equal(t, "dev", devMode)
	assert.Equal(t, ":4327", dev.DefaultListen, "dev serves a frontend from another origin")
	assert.Equal(t, "~/.config/leapmux/dev/dev.yaml", dev.DefaultConfigFile)
	assert.False(t, dev.SoloMode)

	// The lists the CLI used to duplicate. Asserting them HERE is what makes the
	// binary and the library provably the same, now that runSolo passes neither.
	assert.Equal(t, defaultCLIFlags(), solo.CLIFlags)
	assert.Equal(t, defaultCLIFlags(), dev.CLIFlags)
	assert.Equal(t, defaultExtraFlags(), solo.ExtraFlags)

	// An explicit Config still wins over every derived default.
	custom, _ := soloLoadOptions(Config{
		Listen:     "0.0.0.0:9999",
		ConfigDir:  "/tmp/cd",
		ConfigFile: "/tmp/cf.yaml",
		CLIFlags:   []string{"listen"},
		ExtraFlags: []hubconfig.ExtraFlagDef{},
	})
	assert.Equal(t, "0.0.0.0:9999", custom.DefaultListen)
	assert.Equal(t, "/tmp/cd", custom.DefaultConfigDir)
	assert.Equal(t, "/tmp/cf.yaml", custom.DefaultConfigFile)
	assert.Equal(t, []string{"listen"}, custom.CLIFlags)
	assert.Empty(t, custom.ExtraFlags, "an explicit empty list is not 'unset'")
}

// The warning asks the HUB whether it is exposed, so it covers every address
// the hub bound and not only the one -listen gave.
//
// Which addresses count as exposed is listenset.Addr.IsLoopback's rule, and
// TestIsLoopback pins it there; what this pins is the decision built on top of
// it. The NoTCP case is the one that changed: a desktop hub binds no TCP
// address of its own and used to be exempt outright, but it can gain one from
// the extra_listen_addresses setting, and that address exposes the hub exactly
// as much as a -listen one would.
func TestShouldWarnNonLoopback(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		cfg     Config
		exposed bool
		want    bool
	}{
		{"solo on loopback stays quiet", Config{}, false, false},
		{"solo on an exposed address warns", Config{}, true, true},
		// Dev mode authenticates with a real password from the start, so
		// there is no credential-free state to warn about.
		{"dev mode is exempt however it binds", Config{DevMode: true}, true, false},
		// A desktop hub starts with no TCP address, and the setting can give
		// it one. The warning follows the sockets, not the flag.
		{"a desktop hub that gained an address warns", Config{NoTCP: true}, true, true},
		{"a desktop hub with only its socket stays quiet", Config{NoTCP: true}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, shouldWarnNonLoopback(tc.cfg, tc.exposed))
		})
	}
}

// The literal omits both cancels and both wait groups on purpose: Instance is
// reachable at its zero value from every shutdown trigger, and a Stop that
// dereferenced a field Start had not filled in would wedge or panic on the
// startup failure paths that shut down a half-built instance.
func TestInstanceStopReturnsHubError(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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

// The backstop warning can fire with NO Worker in existence: Start holds a
// workerDrained token across bring-up, and the dev-mode poller holds one for its
// whole life, both covering a window in which no worker.Run has been launched.
// Naming the Worker there sends the reader hunting a goroutine never created.
func TestInstanceShutdown_DoesNotBlameAWorkerThatNeverExisted(t *testing.T) {
	orig := workerDrainTimeout
	workerDrainTimeout = 20 * time.Millisecond
	t.Cleanup(func() { workerDrainTimeout = orig })

	hubDone := make(chan struct{})
	close(hubDone)
	inst := &Instance{
		cancelWorker: func() {},
		cancelHub:    func() {},
		hubDone:      hubDone,
	}
	// Exactly the token Start carries across setupWorker -- and nothing else, so
	// the backstop expires with no Worker goroutine to attribute it to.
	inst.workerDrained.Add()

	// logging.Setup INSIDE the capture: it binds a fresh handler to os.Stderr at
	// call time, and the package-init default still points at the real one, so a
	// capture without it reads empty -- and every assertion on it passes
	// vacuously.
	logs := testutil.CaptureStderr(t, func() {
		logging.Setup()
		inst.shutdown()
	})

	assert.Contains(t, logs, "did not finish before the hub shutdown deadline",
		"the backstop must still report that it gave up")
	assert.NotContains(t, logs, "worker did not report draining",
		"no Worker exists here; blaming one sends the reader after a goroutine "+
			"that was never launched; captured:\n"+logs)
}

func TestInstanceShutdown_IsIdempotent(t *testing.T) {
	t.Parallel()

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

// TestDefaultCLIFlagsCarryTheBindBootstrapFlags pins that `leapmux solo` and
// `leapmux dev` accept --listen and --data-dir. They are bind-time bootstrap
// flags: the hub's default IPC socket path is `<data-dir>/hub/hub.sock`, and on
// macOS that path must fit the 104-byte sun_path limit -- a run directory under
// a deep checkout (a worktree under .tmp/wt/) blows past it and the bind fails
// with "invalid argument". A launcher that cannot shorten the socket path has
// no way out, and --listen carries that now: `--listen unix:<short>/hub.sock`
// names the local socket. The old --local-listen flag is gone with the second
// bind knob it served.
func TestDefaultCLIFlagsCarryTheBindBootstrapFlags(t *testing.T) {
	t.Parallel()

	flags := defaultCLIFlags()
	for _, name := range []string{"listen", "data-dir"} {
		require.Contains(t, flags, name, "solo and dev must expose the bootstrap flag %q", name)
	}
	require.NotContains(t, flags, "local-listen",
		"the second bind knob is gone; --listen carries both address kinds")
}

// TestDefaultExtraFlagsCarryWorkerScopedKnobs pins that solo's extra flags are the
// worker-scoped settings the embedded worker needs. max-incomplete-chunked is the
// load-bearing case: it is NOT a hub setting (the Hub's chunk-count cap is
// unreachable -- channelmgr admits one in-flight sequence per channel+direction),
// so solo is the only place it can be tuned for the embedded worker. If it ever
// drops out of this list again, `leapmux solo -max-incomplete-chunked=N` starts
// failing with "flag provided but not defined" while the docs still advertise it.
func TestDefaultExtraFlagsCarryWorkerScopedKnobs(t *testing.T) {
	t.Parallel()

	byName := map[string]hubconfig.ExtraFlagDef{}
	for _, ef := range defaultExtraFlags() {
		byName[ef.Name] = ef
	}
	for _, name := range []string{"encryption-mode", "use-login-shell", "max-incomplete-chunked", "agent-startup-concurrency"} {
		require.Contains(t, byName, name, "solo must expose the worker-scoped %q flag", name)
	}

	// agent-startup-concurrency is worker-scoped for the same reason
	// max-incomplete-chunked is: it caps the EMBEDDED worker's boot-time resume
	// sweep, and the hub has no such pool. Solo is therefore the only place the
	// desktop app can tune it.
	startupConcurrency := byName["agent-startup-concurrency"]
	assert.Equal(t, "agent_startup_concurrency", startupConcurrency.KoanfKey,
		"the koanf key is what bringUpLocalWorker reads out of Extras")
	assert.Equal(t, "0", startupConcurrency.StrDefault,
		"0 must be the default so the worker resolves the concurrency from this machine's core count")
	assert.Equal(t, "Timeout and limit options", startupConcurrency.Category,
		"it is a limit, not a server option -- the help output groups it accordingly")
	assert.Contains(t, startupConcurrency.Usage, strconv.Itoa(workerconfig.DefaultMaxStartupConcurrency),
		"the usage string must derive the default from the constant; a hand-written number makes -help lie the day it changes")

	chunked := byName["max-incomplete-chunked"]
	assert.Equal(t, "max_incomplete_chunked", chunked.KoanfKey,
		"the koanf key is what bringUpLocalWorker reads out of Extras")
	assert.Equal(t, "0", chunked.StrDefault,
		"0 must be the default so the worker applies contracts.DefaultMaxIncompleteChunked")
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
	t.Parallel()

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

// startForTest drives the production entry the way Start does -- the
// same preparation, the same root-lease acquisition -- so the tests
// exercise the lease-transfer contract every caller lives under.
func startForTest(ctx context.Context, cfg Config, d deps) (*Instance, error) {
	prepared, err := Prepare(cfg)
	if err != nil {
		return nil, err
	}
	root, err := lifecycle.AcquireDirectory(
		lifecycle.NewDirectoryOwner(prepared.role, prepared.dataDir), prepared.role, prepared.dataDir)
	if err != nil {
		return nil, err
	}
	return start(ctx, cfg, d, prepared, root)
}

// serveFailsImmediately stubs serveHub with a failure after releasing the Hub's
// resources through Serve with an already-cancelled context. That Serve never
// holds the lease, so only the Hub's exit can end Start's readiness wait.
//
// Returning without calling Serve at all is the obvious spelling and the wrong
// one: hub.NewServer has already bound both listeners, opened the store and
// loaded the keystore, and hub.Server exposes no Close, so only Serve's unwind
// gives them back. Every such test leaked an open SQLite pool and a bound socket
// or pipe handle for the life of the test binary, which on Windows also defeats
// the sandbox temp-dir cleanup.
func serveFailsImmediately(t *testing.T, d *testDeps, serveErr error) {
	t.Helper()
	d.stubServeHub(func(ctx context.Context, s *hub.Server) error {
		dead, cancel := context.WithCancel(ctx)
		cancel()
		_ = s.Serve(dead)
		return serveErr
	})
	// A successful connect can race Hub exit. Release readiness after the exit
	// is published so the startup verdict must detect it independently.
	d.waitReady = func(ctx context.Context, _ *hub.Server, _ string) error {
		<-ctx.Done()
		return nil
	}
}

// testDeps accumulates the seam substitutions for one test. It is passed to
// start() rather than written to package state, so a stub cannot outlive the test
// that installed it -- which is what the package-var seams could not promise: a
// leaked Start goroutine could still be reading one while t.Cleanup wrote it back.
//
// It does NOT make the Hub-starting tests parallel, and that is not what it is
// for. Those call soloStartEnv -> SandboxHome -> t.Setenv, which Go refuses to
// combine with t.Parallel at all, and several also swap os.Stderr through
// CaptureStderr. The seams were never the binding constraint; process-global HOME
// is. What this buys is that a stub is now scoped to the test that made it,
// mechanically rather than by discipline.
type testDeps struct {
	deps
}

func newTestDeps() *testDeps { return &testDeps{deps: defaultDeps()} }

// stubServeHub swaps the Hub-serving seam.
func (d *testDeps) stubServeHub(fn func(context.Context, *hub.Server) error) {
	d.serveHub = fn
}

// stubWorkerBringUp swaps the Worker bring-up seam, discarding the production
// wiring so each test states only what it does with the context and what it
// reports.
func (d *testDeps) stubWorkerBringUp(fn func(context.Context) error) {
	d.bringUpWorker = func(ctx context.Context, _ workerBringUp) error { return fn(ctx) }
}

// serveUntilKilled stubs serveHub with a REAL s.Serve that runs until kill(),
// and reports serveErr once it has stopped.
//
// This is what makes the startup-window tests ORDERED rather than raced: the Hub
// is provably serving when Start passes readiness, and provably finished
// afterwards. A stub that failed immediately could not distinguish "the gate saw
// a dead Hub" from "the gate was never reached".
//
// It FAILS THE TEST if Serve returned before kill() -- pinning serveErr would
// otherwise hide a Hub that never served behind exactly the error the test
// expects, which is the trap serveHub exists to close (see its doc: two tests
// once passed on a NewServer failure instead). A cleanup-time check rather than
// an inline one, because Serve returns on its own goroutine.
//
// kill is idempotent and never blocks, and t.Cleanup calls it, so a failed
// assertion cannot leave a Hub serving behind the test.
func serveUntilKilled(t *testing.T, d *testDeps, serveErr error) (kill func()) {
	t.Helper()
	killed := make(chan struct{})
	var servedEarly atomic.Pointer[error]
	d.stubServeHub(func(ctx context.Context, s *hub.Server) error {
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
		realErr := s.Serve(inner)
		select {
		case <-killed:
		default:
			// ctx is hubCtx: once it is cancelled somebody legitimately asked the
			// Hub to stop (Stop, or a failure path's join), which ends Serve just as
			// kill() would. Only a Serve that returned while nobody had asked means
			// the Hub was never really up.
			//
			// Records rather than fails: t.Error/t.Fatal from a non-test goroutine
			// is not safe, and this one is Start's Hub goroutine.
			if ctx.Err() == nil {
				servedEarly.Store(&realErr)
			}
		}
		return serveErr
	})
	var once sync.Once
	kill = func() { once.Do(func() { close(killed) }) }
	t.Cleanup(func() {
		kill()
		if early := servedEarly.Load(); early != nil {
			t.Errorf("the hub stopped serving before the test killed it, so this test "+
				"proved nothing about a Hub that was up; Serve returned: %v", *early)
		}
	})
	return kill
}

// startWhileHubDies runs Start in a goroutine, waits until it has entered the
// bring-up seam, then ends the Hub with endHub and returns what Start reported.
//
// It owns the goroutine so no caller can leak one. That matters more than the
// deduplication: a leaked Start keeps reading the serveHub/bringUpWorker package
// vars, which stubServeHub's and stubWorkerBringUp's cleanups WRITE at teardown,
// and -- since none of these tests are parallel -- it can still be running when
// the next test re-stubs them. Under -race that surfaces as a data race on the
// globals, masking whatever regression was actually being diagnosed. So even the
// two diagnostic cases below join before failing.
//
// Those cases are diagnostics, not assertions: done closing first means Start ran
// the REAL bring-up (the seam is not wired), and the budget expiring means Start
// never got there at all. Both would otherwise present as a test that hangs until
// the package deadline.
func startWhileHubDies(t *testing.T, d *testDeps, cfg Config, entered <-chan struct{}, endHub func()) (*Instance, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), soloTestTimeout)
	t.Cleanup(cancel)
	return startUntilTornDown(t, ctx, d, cfg, entered, endHub)
}

// startUntilTornDown is startWhileHubDies with the caller's context under the
// test's control, for the OTHER way a startup ends: the caller cancelling.
func startUntilTornDown(t *testing.T, ctx context.Context, d *testDeps, cfg Config, entered <-chan struct{}, tearDown func()) (*Instance, error) {
	t.Helper()
	var inst *Instance
	var err error
	done := make(chan struct{})
	go func() {
		defer close(done)
		inst, err = startForTest(ctx, cfg, d.deps)
	}()

	select {
	case <-entered:
	case <-done:
		t.Fatal("Start finished without reaching bring-up (is the seam wired?)")
	case <-time.After(soloTestTimeout):
		// tearDown unblocks a Start parked anywhere past the Hub goroutine, so the
		// join below terminates instead of hanging out the package deadline.
		tearDown()
		<-done
		t.Fatal("Start never reached bring-up")
	}

	tearDown()
	<-done
	return inst, err
}

// startFailureEnv points Start at a throwaway home with no TCP listener, so it
// gets all the way to Serve -- which the seam then fails -- instead of dying
// earlier in hub.NewServer.
//
// It returns the Config to hand Start. The --listen override naming a local
// IPC URL is what makes these tests independent of each other: SandboxHome
// redirects HOME, which is enough to move the Unix socket under a private
// directory, but the WINDOWS default is npipe:leapmux-hub-<SID> -- derived
// from the account, not the home -- so every Hub-starting test in this package
// bound the same process-global pipe name and the second one to reach it
// failed hub.NewServer with "Access is denied". That is what makes these three
// tests fail on Windows CI while passing on a developer's Unix machine.
func startFailureEnv(t *testing.T) Config {
	t.Helper()
	return soloStartEnv(t, false)
}

// soloStartEnv builds that environment for either mode. devMode is where the
// deferral case lives: dev uses real password auth rather than solo's injected
// soloUser, so on a fresh install there is no admin for the Worker to register
// under until somebody completes /setup.
func soloStartEnv(t *testing.T, devMode bool) Config {
	t.Helper()
	locallistentest.SandboxHome(t)
	return Config{
		NoTCP:   true,
		DevMode: devMode,
		// One local entry is the whole bind set; NoTCP drops the TCP half of
		// any default anyway.
		Args: []string{"--listen=" + locallistentest.UniqueListenURL(t, "leapmux-hub-solo")},
	}
}

// A Hub that dies WHILE SERVING is surfaced by Start with the "hub serve:"
// attribution, rather than as a generic readiness failure.
//
// The seam is what makes this reachable: hub.NewServer binds both listeners, so
// the pre-existing version of this test pointed at an unwritable socket path,
// failed in NewServer, and passed only because "create hub server" happens to
// contain the substring "hub serve".
func TestSoloStart_SurfacesAServeTimeFailure(t *testing.T) {
	cfg := startFailureEnv(t)
	d := newTestDeps()
	wantErr := errors.New("revocation watcher seed failed")
	serveFailsImmediately(t, d, wantErr)

	ctx, cancel := context.WithTimeout(context.Background(), soloTestTimeout)
	defer cancel()
	inst, err := startForTest(ctx, cfg, d.deps)

	require.NoError(t, ctx.Err(), "the caller must remain live through the startup verdict")
	require.Error(t, err)
	require.Nil(t, inst)
	assert.ErrorIs(t, err, wantErr)
	assert.True(t, strings.HasPrefix(err.Error(), "hub serve exited "),
		"the Serve case must attribute the failure and state the stage that saw it, "+
			"not merely mention it; got: "+err.Error())
}

// A Hub that dies on its way up has exactly ONE reporter: Start, which RETURNS
// the error. The watcher sees the same hubDone close and must stay quiet, or
// every failed launch is reported twice.
func TestSoloStart_DoesNotAlsoLogAServeTimeFailure(t *testing.T) {
	cfg := startFailureEnv(t)
	d := newTestDeps()
	serveFailsImmediately(t, d, errors.New("revocation watcher seed failed"))

	// Stderr, not slog.Default(): Start's own logging.Setup installs a fresh
	// default logger, so a captured handler would be replaced before the line
	// under test could reach it and this would assert on an empty buffer.
	var err error
	out := testutil.CaptureStderr(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), soloTestTimeout)
		defer cancel()
		_, err = startForTest(ctx, cfg, d.deps)
		assert.NoError(t, ctx.Err(), "the caller must remain live through the startup verdict")
	})

	require.Error(t, err)
	assert.NotContains(t, out, "hub error",
		"the failure Start returns must not also be logged; captured:\n"+out)
}

// The watcher's OTHER case: a Hub that dies after Start has handed off. The
// watcher must run the ordered teardown so the Worker is not left looping
// against a dead endpoint -- and must still say nothing, because Wait and Stop
// both hand the error to a caller that waitSolo documents as its single
// reporter.
func TestSolo_AHubThatDiesWhileServingTearsTheInstanceDownWithoutReporting(t *testing.T) {
	cfg := startFailureEnv(t)
	d := newTestDeps()

	wantErr := errors.New("hub died mid-session")
	serving := make(chan struct{})
	var killHub atomic.Pointer[context.CancelFunc]
	d.stubServeHub(func(ctx context.Context, s *hub.Server) error {
		inner, cancel := context.WithCancel(ctx)
		killHub.Store(&cancel)
		close(serving)
		_ = s.Serve(inner)
		return wantErr
	})

	var inst *Instance
	out := testutil.CaptureStderr(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), soloTestTimeout)
		defer cancel()
		var err error
		inst, err = startForTest(ctx, cfg, d.deps)
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
	d := newTestDeps()

	wantErr := errors.New("lease release failed")
	d.stubServeHub(func(ctx context.Context, s *hub.Server) error {
		_ = s.Serve(ctx)
		// The shape of a real teardown cleanup failure: Serve returns non-nil
		// after an ordinary, requested shutdown.
		return wantErr
	})

	var stopErr error
	out := testutil.CaptureStderr(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), soloTestTimeout)
		defer cancel()
		inst, err := startForTest(ctx, cfg, d.deps)
		require.NoError(t, err)
		stopErr = inst.Stop()
	})

	assert.ErrorIs(t, stopErr, wantErr, "Stop must still hand the error back")
	assert.NotContains(t, out, "hub error",
		"an exit Stop asked for must not also be logged; captured:\n"+out)
}

// startupFailure is the ONE predicate Start asks at every stage boundary, so an
// exited Hub outranks whatever that stage happened to observe. The property
// under test is TOTALITY: once hubDone is closed it never returns nil. The
// readiness select leans on that -- its hubDone case carries no error of its own
// -- so a hole here silently turns a dead Hub back into a healthy Instance.
//
// The select can wake on readiness or Hub exit. This table checks the published
// failure independently of that choice.
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
		// startupFailure must be safe on the zero-value Instance every shutdown trigger
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
			want:   "hub serve exited while the worker was starting: revocation watcher seed failed",
			wantIs: hubErr,
		},
		{
			// Totality: no error of its own, no cancelled caller, and it STILL must
			// not return nil. This is the case the empty select branch depends on.
			name: "an exited hub with no error of its own states the stage",
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
		{
			// The OTHER watcher trigger, and the half that used to be unreachable:
			// the caller cancelled, the watcher already cancelled workerCtx, and the
			// Hub is still serving because shutdown parks in workerDrained.Wait
			// before it ever reaches cancelHub. Answering nil here is what let the
			// Worker be blamed for the caller's Ctrl-C.
			name:   "a cancelled caller is reported even while the hub still serves",
			inst:   &Instance{hubDone: make(chan struct{})},
			ctx:    cancelledCtx,
			want:   "solo startup cancelled while the worker was starting: context canceled",
			wantIs: context.Canceled,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.inst.startupFailure(tt.ctx, "while the worker was starting")
			if tt.wantNil {
				assert.NoError(t, got)
				return
			}
			require.Error(t, got, "startupFailure must be total once hubDone is closed")
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
// The fixture's Serve never holds the lease, so with the caller context live
// only hubDone can end the readiness wait, and the verdict sees a published Hub
// exit.
func TestSoloStart_DoesNotBringTheWorkerUpAgainstAHubThatAlreadyExited(t *testing.T) {
	cfg := startFailureEnv(t)
	d := newTestDeps()
	wantErr := errors.New("revocation watcher seed failed")
	serveFailsImmediately(t, d, wantErr)

	var broughtUp atomic.Bool
	d.stubWorkerBringUp(func(context.Context) error {
		broughtUp.Store(true)
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), soloTestTimeout)
	defer cancel()
	inst, err := startForTest(ctx, cfg, d.deps)

	require.NoError(t, ctx.Err(), "the caller must remain live through the startup verdict")
	require.Error(t, err)
	require.Nil(t, inst)
	assert.ErrorIs(t, err, wantErr)
	assert.False(t, broughtUp.Load(),
		"a Hub already known to be gone must not have a Worker brought up against it")
}

// A Hub that exits without its lease is the readiness failure. The launch must
// stop before Worker bring-up, and only after the Hub finished its cleanup.
func TestSoloStart_ReadinessFailureStopsBeforeWorkerBringUp(t *testing.T) {
	cfg := startFailureEnv(t)
	d := newTestDeps()
	wantErr := errors.New("the Hub never held its lease")
	serveFailsImmediately(t, d, wantErr)

	stopped := make(chan struct{})
	serve := d.serveHub
	d.stubServeHub(func(ctx context.Context, server *hub.Server) error {
		defer close(stopped)
		return serve(ctx, server)
	})
	var broughtUp atomic.Bool
	d.stubWorkerBringUp(func(context.Context) error {
		broughtUp.Store(true)
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), soloTestTimeout)
	defer cancel()
	inst, err := startForTest(ctx, cfg, d.deps)
	if inst != nil {
		t.Cleanup(func() { _ = inst.Stop() })
	}

	require.NoError(t, ctx.Err(), "the caller must remain live through the startup verdict")
	require.ErrorIs(t, err, wantErr)
	require.Nil(t, inst)
	assert.False(t, broughtUp.Load(), "readiness failure must stop Worker bring-up")
	select {
	case <-stopped:
	default:
		t.Fatal("startup returned before the Hub finished cleanup")
	}
}

// A failed application-readiness wait stops Worker bring-up and joins Hub cleanup.
func TestSoloStart_ReadinessWaitFailureStopsBeforeWorkerBringUp(t *testing.T) {
	cfg := startFailureEnv(t)
	d := newTestDeps()
	wantErr := errors.New("application readiness failed")
	d.waitReady = func(context.Context, *hub.Server, string) error { return wantErr }

	stopped := make(chan struct{})
	serve := d.serveHub
	d.stubServeHub(func(ctx context.Context, server *hub.Server) error {
		defer close(stopped)
		return serve(ctx, server)
	})
	var broughtUp atomic.Bool
	d.stubWorkerBringUp(func(context.Context) error {
		broughtUp.Store(true)
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), soloTestTimeout)
	defer cancel()
	inst, err := startForTest(ctx, cfg, d.deps)
	if inst != nil {
		t.Cleanup(func() { _ = inst.Stop() })
	}

	require.NoError(t, ctx.Err(), "the caller must remain live through the startup verdict")
	require.ErrorIs(t, err, wantErr)
	require.Nil(t, inst)
	assert.False(t, broughtUp.Load(), "readiness failure must stop Worker bring-up")
	select {
	case <-stopped:
	default:
		t.Fatal("startup returned before the Hub finished cleanup")
	}
}

// The literal macOS CI failure from PR #364: a Hub that dies while the Worker is
// coming up was reported as "auto-register worker: create worker: context
// canceled". The Worker only said that BECAUSE the Hub died -- the watcher
// cancels workerCtx the moment it observes hubDone -- so blaming it sends every
// reader after the wrong bug.
func TestSoloStart_AHubThatDiesDuringBringUpIsNotBlamedOnTheWorker(t *testing.T) {
	cfg := startFailureEnv(t)
	d := newTestDeps()
	wantErr := errors.New("revocation watcher seed failed")
	kill := serveUntilKilled(t, d, wantErr)

	entered := make(chan struct{})
	d.stubWorkerBringUp(func(ctx context.Context) error {
		close(entered)
		// Returns only once the watcher has cancelled workerCtx, which it does
		// after observing hubDone -- so the gate provably runs against a Hub that
		// has already exited, with no sleep and no race.
		<-ctx.Done()
		return fmt.Errorf("create worker: %w", ctx.Err())
	})

	inst, err := startWhileHubDies(t, d, cfg, entered, kill)

	require.Error(t, err)
	require.Nil(t, inst)
	assert.ErrorIs(t, err, wantErr)
	assert.True(t, strings.HasPrefix(err.Error(), "hub serve exited while the worker was starting:"),
		"the Hub's death must be attributed to the Hub, at the stage that saw it; got: "+err.Error())
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
	d := newTestDeps()
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
			kill := serveUntilKilled(t, d, tt.serveErr)

			entered := make(chan struct{})
			d.stubWorkerBringUp(func(ctx context.Context) error {
				close(entered)
				<-ctx.Done()
				// Bring-up SUCCEEDED -- from a window in which the Hub had already
				// died. Nothing downstream of here asks again, so without the gate
				// Start hands back a healthy Instance for a stopped Hub.
				return nil
			})

			inst, err := startWhileHubDies(t, d, cfg, entered, kill)

			require.Error(t, err, "a dead Hub must not be reported as a successful launch")
			require.Nil(t, inst)
			if tt.serveErr != nil {
				assert.ErrorIs(t, err, tt.serveErr)
				assert.True(t, strings.HasPrefix(err.Error(), "hub serve exited while the worker was starting:"),
					"got: "+err.Error())
			} else {
				assert.Equal(t, "hub serve exited while the worker was starting", err.Error())
			}
		})
	}
}

// A Serve cancelled while it is still STARTING is an exit somebody asked for.
// hub.NewServer binds both listeners before Serve runs, so a caller's readiness
// probe succeeds -- and its Stop can land -- while Serve is still seeding the
// revocation cursor. Reporting that seed failure made `leapmux hub` and `leapmux
// solo` exit non-zero on an ordinary Ctrl-C during startup, and the sqlite
// spelling of it ("interrupted (9)") wraps no context error, so no downstream
// errors.Is filter could have caught it either.
//
// solo is where this lives because it is the only package that stands a real
// hub.Server up.
func TestHubServe_ACancelDuringItsOwnStartupIsACleanExit(t *testing.T) {
	cfg := startFailureEnv(t)
	d := newTestDeps()

	served := make(chan error, 1)
	d.stubServeHub(func(ctx context.Context, s *hub.Server) error {
		// Already cancelled, so Serve fails inside SeedCursor deterministically
		// rather than racing it. The real Serve still unwinds there, releasing
		// what NewServer acquired.
		dead, cancel := context.WithCancel(ctx)
		cancel()
		err := s.Serve(dead)
		served <- err
		return err
	})

	ctx, cancel := context.WithTimeout(context.Background(), soloTestTimeout)
	defer cancel()
	inst, _ := startForTest(ctx, cfg, d.deps)
	// Whether Start notices is deliberately NOT asserted: a clean exit is exactly
	// what its gates cannot distinguish from a Hub still coming up, and Serve's
	// unwind can outlast the whole launch. Start's own doc says as much -- the
	// gates close the wide windows, not every window. The property under test is
	// the Hub's, one layer down.
	if inst != nil {
		t.Cleanup(func() { _ = inst.Stop() })
	}

	select {
	case serveErr := <-served:
		assert.NoError(t, serveErr,
			"a Serve cancelled during its own startup must report a clean exit, not a "+
				"seed failure the user's Ctrl-C caused")
	case <-time.After(soloTestTimeout):
		t.Fatal("the hub goroutine never reported")
	}
}

// The deferral case, which had no coverage at all before the bring-up seam
// existed: in dev mode a missing admin is what /setup is for, so the launch must
// SUCCEED and leave a poller behind rather than fail.
func TestSoloStart_DevModeDefersWorkerRegistrationUntilAnAdminExists(t *testing.T) {
	cfg := soloStartEnv(t, true)
	d := newTestDeps()
	// A real Serve, whose terminal error is a SENTINEL rather than nil: Stop must
	// hand back exactly the Hub's error, and pinning it to nil would make the
	// assertion below hold no matter what Stop did. It also keeps the Hub's own
	// startup out of the result -- with bring-up stubbed, Start returns fast
	// enough that Stop's cancel can land inside SeedCursor and surface as a SQLite
	// "interrupted", a race that has nothing to do with the case under test.
	wantStopErr := errors.New("lease release failed")
	serveUntilKilled(t, d, wantStopErr)
	// Re-entrant: the poller calls this again on every tick.
	var bringUps atomic.Int32
	d.stubWorkerBringUp(func(context.Context) error {
		bringUps.Add(1)
		return errNoAdminYet
	})

	var inst *Instance
	var err error
	out := testutil.CaptureStderr(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), soloTestTimeout)
		defer cancel()
		inst, err = startForTest(ctx, cfg, d.deps)
	})
	// Unconditional, and BEFORE the requires below: workerCtx is detached from the
	// caller's, so nothing but Stop ever cancels the poller. A require that fired
	// first would leave it ticking against a deleted temp dir for the rest of the
	// run, racing every later test's stub.
	t.Cleanup(func() {
		if inst != nil {
			_ = inst.Stop()
		}
	})

	require.NoError(t, err, "a missing admin defers the Worker; it does not fail the launch; captured:\n"+out)
	require.NotNil(t, inst)
	assert.Contains(t, out, "deferring worker auto-registration",
		"the deferral is the user-visible half of this case; captured:\n"+out)
	assert.Equal(t, int32(1), bringUps.Load(),
		"the launch itself attempts bring-up exactly once; the retries are the poller's")

	// Stop is also the token-accounting assertion: the deferral hands a second
	// worker token pair to the poller while Start still holds its own, and an
	// imbalance on either side either wedges the teardown or trips drain.Counter's
	// negative-counter panic.
	assert.ErrorIs(t, inst.Stop(), wantStopErr,
		"Stop must hand back exactly the Hub's terminal error")
}

// The gate sits BEFORE the switch, so it covers the deferral case too. Without
// that placement a dev-mode launch whose Hub died during bring-up would return a
// healthy Instance AND leave a poller ticking every 2s against a Hub that is
// already gone.
func TestSoloStart_DoesNotDeferWorkerSetupOntoAHubThatDied(t *testing.T) {
	cfg := soloStartEnv(t, true)
	d := newTestDeps()
	wantErr := errors.New("revocation watcher seed failed")
	kill := serveUntilKilled(t, d, wantErr)

	entered := make(chan struct{})
	d.stubWorkerBringUp(func(ctx context.Context) error {
		close(entered)
		<-ctx.Done()
		// The case that would defer, if the gate let it get that far.
		return errNoAdminYet
	})

	var inst *Instance
	var err error
	out := testutil.CaptureStderr(t, func() {
		inst, err = startWhileHubDies(t, d, cfg, entered, kill)
	})

	require.Error(t, err)
	require.Nil(t, inst)
	assert.ErrorIs(t, err, wantErr)
	assert.True(t, strings.HasPrefix(err.Error(), "hub serve exited while the worker was starting:"),
		"got: "+err.Error())
	assert.NotContains(t, out, "deferring worker auto-registration",
		"a Hub that is gone has nothing to defer to; captured:\n"+out)
}

// The OTHER half of the same misattribution, and the one the first fix missed: a
// caller who cancels during bring-up. The watcher cancels workerCtx on EITHER
// trigger, so bring-up reports "context canceled" either way -- but on this path
// the Hub is still serving, because shutdown cancels the Worker and then parks in
// workerDrained.Wait behind the token Start holds across bring-up, so cancelHub
// has not run and hubDone is still open. A gate that asked only about hubDone
// answered nil here and let the switch blame the Worker for a Ctrl-C.
func TestSoloStart_ACallerCancelDuringBringUpIsNotBlamedOnTheWorker(t *testing.T) {
	cfg := soloStartEnv(t, false)
	d := newTestDeps()
	// A real, LIVE Hub for the whole test: the point is that the Hub is healthy
	// and the startup still has to end.
	serveUntilKilled(t, d, nil)

	ctx, cancel := context.WithTimeout(context.Background(), soloTestTimeout)
	t.Cleanup(cancel)

	entered := make(chan struct{})
	d.stubWorkerBringUp(func(bringUpCtx context.Context) error {
		close(entered)
		// Returns only once the watcher has cancelled workerCtx in response to the
		// caller's cancel -- the exact shape bringUpLocalWorker produces when a
		// store call is interrupted mid-registration.
		<-bringUpCtx.Done()
		return fmt.Errorf("create worker: %w", bringUpCtx.Err())
	})

	inst, err := startUntilTornDown(t, ctx, d, cfg, entered, cancel)

	require.Error(t, err)
	require.Nil(t, inst)
	assert.ErrorIs(t, err, context.Canceled)
	assert.NotContains(t, err.Error(), "auto-register worker",
		"the Worker only failed because the caller cancelled; got: "+err.Error())
	assert.Contains(t, err.Error(), "solo startup cancelled",
		"a cancelled startup must say so; got: "+err.Error())
}

func TestInitialBringUpCancellationPreservesJoinedIndependentFailure(t *testing.T) {
	cfg := soloStartEnv(t, false)
	d := newTestDeps()
	serveUntilKilled(t, d, nil)
	ctx, cancel := context.WithCancel(testutil.DeadlineContext(t))
	defer cancel()
	entered := make(chan struct{})
	failure := errors.New("initial worker database failed independently")
	d.stubWorkerBringUp(func(ctx context.Context) error {
		close(entered)
		<-ctx.Done()
		return errors.Join(ctx.Err(), failure)
	})
	inst, err := startUntilTornDown(t, ctx, d, cfg, entered, cancel)
	require.Nil(t, inst)
	require.ErrorIs(t, err, failure)
	require.ErrorIs(t, err, context.Canceled)
}

func TestStartupJoinPreservesLateIndependentWorkerFailure(t *testing.T) {
	cfg := soloStartEnv(t, false)
	d := newTestDeps()
	deadline := testutil.DeadlineContext(t)
	ctx, cancel := context.WithCancel(deadline)
	defer cancel()
	entered, releaseWorker, hubStopped := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseWorker) }) }
	failure := errors.New("worker failed during startup join")
	serve := d.serveHub
	d.serveHub = func(ctx context.Context, server *hub.Server) error {
		err := serve(ctx, server)
		close(hubStopped)
		return err
	}
	d.runWorker = func(ctx context.Context, cfg worker.RunConfig) error {
		defer func() { _ = cfg.Directory.Close() }()
		close(entered)
		<-ctx.Done()
		cfg.Drained()
		<-releaseWorker
		return failure
	}
	var inst *Instance
	var outcome error
	done := make(chan struct{})
	go func() { inst, outcome = startForTest(ctx, cfg, d.deps); close(done) }()
	t.Cleanup(func() {
		cancel()
		release()
		<-done
		if inst != nil {
			_ = inst.Stop()
		}
	})
	select {
	case <-entered:
	case <-deadline.Done():
		t.Fatal("worker initialization did not begin")
	}
	cancel()
	// The Hub cannot stop until startup releases its bring-up token. The
	// worker still holds its completion, so the startup cause predates it.
	select {
	case <-hubStopped:
	case <-deadline.Done():
		t.Fatal("startup cleanup did not stop the Hub")
	}
	release()
	select {
	case <-done:
	case <-deadline.Done():
		t.Fatal("startup cleanup did not join the worker")
	}
	require.Nil(t, inst)
	require.ErrorIs(t, outcome, failure)
}

// A cleanup failure during failed startup has no returned Instance through which
// callers could retrieve it. Start must preserve the cause after joining cleanup.
func TestSoloStart_ReportsACleanupFailureFromTheTeardownItTriggered(t *testing.T) {
	cfg := soloStartEnv(t, false)
	d := newTestDeps()
	// The shape of a real teardown cleanup failure: Serve returns non-nil after an
	// ordinary, requested shutdown -- here the one failStartup's join asks for.
	leaseErr := errors.New("release runtime lease failed")
	serveUntilKilled(t, d, leaseErr)

	bringUpErr := errors.New("generate composite keypair: no entropy")
	d.stubWorkerBringUp(func(context.Context) error { return bringUpErr })

	ctx, cancel := context.WithTimeout(context.Background(), soloTestTimeout)
	defer cancel()
	inst, err := startForTest(ctx, cfg, d.deps)

	require.Error(t, err)
	require.Nil(t, inst)
	assert.ErrorIs(t, err, bringUpErr, "the Worker's failure is still the cause")
	assert.ErrorIs(t, err, leaseErr,
		"the cleanup failure has no other reporter once Start returns a nil Instance")
}

// The cancellation the teardown itself issues must NOT be reported. failStartup's
// join is what cancels the Hub, so a Serve still working through its own startup
// comes back with that very cancel -- blaming the Hub for obeying us, and burying
// the Worker's real failure under it.
func TestSoloStart_DoesNotReportTheCancellationItsOwnTeardownCaused(t *testing.T) {
	cfg := soloStartEnv(t, false)
	d := newTestDeps()
	// No serveHub stub: the REAL Serve, whose startup (seeding the revocation
	// cursor) is still in flight when the join cancels it, and which therefore
	// returns a context.Canceled-rooted error on a perfectly ordinary teardown.
	bringUpErr := errors.New("generate composite keypair: no entropy")
	d.stubWorkerBringUp(func(context.Context) error { return bringUpErr })

	ctx, cancel := context.WithTimeout(context.Background(), soloTestTimeout)
	defer cancel()
	inst, err := startForTest(ctx, cfg, d.deps)

	require.Error(t, err)
	require.Nil(t, inst)
	assert.ErrorIs(t, err, bringUpErr)
	assert.NotContains(t, err.Error(), "hub serve",
		"the Hub was still serving until our own join stopped it; got: "+err.Error())
}

// The deferral poller is the one place the stage gates cannot reach: it outlives
// Start. When the teardown cancels it mid-setup, the error it gets back is the
// shutdown wearing the Worker's wrapper -- and logging it would make the poller a
// SECOND reporter of a shutdown Stop already reports, which is exactly what the
// watcher stays silent to avoid.
func TestSoloStart_TheDeferredPollerStaysQuietWhenTheTeardownCancelsIt(t *testing.T) {
	cfg := soloStartEnv(t, true)
	d := newTestDeps()
	serveUntilKilled(t, d, nil)

	clock := testutil.NewQuartzMock(t)
	d.clock = clock
	created := clock.Trap().NewTicker(workerSetupPollTag)
	defer created.Close()
	entered := make(chan struct{})
	// The initial call defers. The next call waits for requested shutdown.
	var calls atomic.Int32
	d.stubWorkerBringUp(func(ctx context.Context) error {
		if calls.Add(1) == 1 {
			return errNoAdminYet
		}
		close(entered)
		<-ctx.Done()
		return fmt.Errorf("look up saved worker owner: %w", ctx.Err())
	})

	var inst *Instance
	var err error
	out := testutil.CaptureStderr(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), soloTestTimeout)
		defer cancel()
		inst, err = startForTest(ctx, cfg, d.deps)
		require.NoError(t, err)
		require.NotNil(t, inst)
		created.MustWait(ctx).MustRelease(ctx)
		clock.Advance(workerSetupPollInterval).MustWait(ctx)
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("the deferred setup did not begin")
		}
		_ = inst.Stop()
	})

	assert.NotContains(t, out, "deferred worker setup failed",
		"a poller the teardown cancelled must not report it as a Worker failure; captured:\n"+out)
}

// The gate can fire when bring-up SUCCEEDED, and the real bringUpLocalWorker adds
// a SECOND token pair and launches worker.Run on that path. Everything else here
// stubs bring-up, so nothing else proves the release accounting survives a real
// Worker under the gate -- a leak would wedge join forever, a double release would
// panic the counter.
func TestSoloStart_TearsDownARealWorkerLaunchedJustBeforeTheHubDied(t *testing.T) {
	cfg := soloStartEnv(t, false)
	d := newTestDeps()
	wantErr := errors.New("revocation watcher seed failed")
	kill := serveUntilKilled(t, d, wantErr)

	entered := make(chan struct{})
	real := d.bringUpWorker
	d.bringUpWorker = func(ctx context.Context, p workerBringUp) error {
		// The REAL implementation, so the Worker goroutine and its own token pair
		// exist; then hold Start inside the window until the Hub is gone.
		if err := real(ctx, p); err != nil {
			return err
		}
		close(entered)
		<-ctx.Done()
		return nil
	}

	inst, err := startWhileHubDies(t, d, cfg, entered, kill)

	// Reaching here at all is half the assertion: failStartup's join waits out the
	// real worker.Run unwind, so a mislaid token would hang the test rather than
	// fail it.
	require.Error(t, err)
	require.Nil(t, inst)
	assert.ErrorIs(t, err, wantErr)
	assert.NotContains(t, err.Error(), "auto-register worker", "got: "+err.Error())
}

// Worker completion reaches the caller through startup or the combined outcome.
// An ordinary stop stays successful; an independent failure during stop survives.
func TestCombinedWorkerCompletionReportsItsCause(t *testing.T) {
	keysRefused := fmt.Errorf("%w: permission denied", workerhub.ErrKeysRefused)
	localFailure := errors.New("open worker db: disk I/O error")
	for _, test := range []struct {
		name string
		run  func(context.Context) error
		want error
	}{
		{"key refusal", func(context.Context) error { return keysRefused }, keysRefused},
		{"local failure", func(context.Context) error { return localFailure }, localFailure},
		{"ordinary cancellation", func(ctx context.Context) error { <-ctx.Done(); return nil }, nil},
		{"independent failure during cancellation", func(ctx context.Context) error { <-ctx.Done(); return localFailure }, localFailure},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := soloStartEnv(t, false)
			d := newTestDeps()
			d.runWorker = func(ctx context.Context, rc worker.RunConfig) error {
				defer func() { _ = rc.Directory.Close() }()
				defer rc.Drained()
				rc.OnReady()
				rc.OnConnectionState(worker.ConnectionState{Connected: true})
				return test.run(ctx)
			}
			ctx, cancel := context.WithCancel(testutil.DeadlineContext(t))
			defer cancel()
			inst, startErr := startForTest(ctx, cfg, d.deps)
			if inst != nil {
				t.Cleanup(func() { _ = inst.Stop() })
				startErr = inst.Stop()
			}
			if test.want == nil {
				require.NoError(t, startErr)
			} else {
				require.ErrorIs(t, startErr, test.want)
			}
		})
	}
}

// The over-reach guard on the gate above, and the first coverage of the switch's
// default case: with the Hub still serving, a bring-up failure is still the
// Worker's, reported with the Worker's attribution.
func TestSoloStart_AWorkerBringUpFailureIsStillTheWorkers(t *testing.T) {
	d := newTestDeps()
	tests := []struct {
		name       string
		bringUpErr error
	}{
		{"a generic failure", errors.New("generate composite keypair: no entropy")},
		// Outside dev mode there is nothing to defer to: no /setup flow will ever
		// create the admin this is waiting for, so it must fail the launch rather
		// than fall into the deferral case.
		{"no admin user outside dev mode", store.ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A real Serve that keeps serving, with its terminal error pinned: the
			// gate must not claim a failure that is not its own, and leaving the
			// Hub's own error unpinned would let a spontaneous startup failure in
			// this window flip the assertion below into a false red.
			cfg := startFailureEnv(t)
			serveUntilKilled(t, d, nil)
			d.stubWorkerBringUp(func(context.Context) error { return tt.bringUpErr })

			ctx, cancel := context.WithTimeout(context.Background(), soloTestTimeout)
			defer cancel()
			inst, err := startForTest(ctx, cfg, d.deps)

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
//
// No wall-clock budget: shutdown returning while teardown is still held proves
// it did not wait for the Worker's full exit, and the absent backstop warn
// proves WaitBounded took the done path rather than timer.C. A Less(elapsed,
// timeout) assertion duplicated that second claim and flaked on Windows CI
// when CaptureStderr and logging.Setup shared the same stopwatch.
func TestInstanceShutdown_StopsTheHubOnTheDrainNotTheWorkersFullExit(t *testing.T) {
	orig := workerDrainTimeout
	// Short only so a regression that waits out the backstop stays cheap; the
	// assertions below do not measure against it.
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

	// logging.Setup inside the capture, or the handler still points at the real
	// stderr and the NotContains below holds no matter what shutdown logs.
	logs := testutil.CaptureStderr(t, func() {
		logging.Setup()
		inst.shutdown()
	})

	assert.True(t, hubCancelled.Load(),
		"the hub must stop once the worker has drained")
	assert.NotContains(t, logs, "did not finish before the hub shutdown deadline",
		"a worker that drained promptly must not be reported as failing to; "+
			"WaitBounded only logs that line on timer.C, so its absence is the "+
			"proof the hub stopped on the drain signal rather than the backstop")
}

// TestSoloStart_TheLocalWorkerDialsTheIPCSocket pins the property that keeps
// the bundled Worker connected once the account holds a password.
//
// The solo hub authenticates a caller with no credentials only over local IPC.
// The Worker uses that transport so it does not depend on a TCP setup or
// sign-in state.
//
// It dials the IPC URL instead, which never asks. (It also presents an lmx_
// bearer, which the solo rung yields to; this pins the transport, because that
// is the half a listener change could break.)
func TestSoloStart_TheLocalWorkerDialsTheIPCSocket(t *testing.T) {
	cfg := soloStartEnv(t, false)
	d := newTestDeps()
	kill := serveUntilKilled(t, d, nil)
	t.Cleanup(kill)

	dialled := make(chan string, 1)
	d.bringUpWorker = func(_ context.Context, p workerBringUp) error {
		select {
		case dialled <- p.listenURL:
		default:
		}
		return nil
	}

	inst, err := startForTest(context.Background(), cfg, d.deps)
	require.NoError(t, err)
	t.Cleanup(func() { _ = inst.Stop() })

	var url string
	select {
	case url = <-dialled:
	case <-time.After(10 * time.Second):
		t.Fatal("the local worker never reported the address it dials")
	}

	require.NotEmpty(t, url)
	assert.True(t, locallisten.IsLocal(url),
		"the bundled Worker must dial the local IPC socket, not a TCP address: %q", url)
	assert.Equal(t, inst.LocalListenURL(), url,
		"and it must be the same socket the Hub reports, not a second one")
}

// startAcceptingPredecessor uses the platform transport and joins its accept loop.
func startAcceptingPredecessor(t *testing.T, url string) (func(), <-chan struct{}) {
	t.Helper()
	ln, err := locallisten.Listen(url)
	require.NoError(t, err)
	accepted := make(chan struct{})
	announce := sync.OnceFunc(func() { close(accepted) })
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
			announce()
		}
	}()
	closeListener := sync.OnceFunc(func() { _ = ln.Close(); <-done })
	t.Cleanup(closeListener)
	return closeListener, accepted
}

type logMessageObserver struct {
	slog.Handler
	message  string
	observed func()
}

func (h logMessageObserver) Handle(ctx context.Context, r slog.Record) error {
	err := h.Handler.Handle(ctx, r)
	if r.Message == h.message {
		h.observed()
	}
	return err
}

func (h logMessageObserver) WithAttrs(attrs []slog.Attr) slog.Handler {
	h.Handler = h.Handler.WithAttrs(attrs)
	return h
}

func (h logMessageObserver) WithGroup(group string) slog.Handler {
	h.Handler = h.Handler.WithGroup(group)
	return h
}

func observeLogMessage(message string) (<-chan struct{}, func()) {
	previous := slog.Default()
	observed := make(chan struct{})
	slog.SetDefault(slog.New(logMessageObserver{Handler: previous.Handler(), message: message,
		observed: sync.OnceFunc(func() { close(observed) })}))
	return observed, func() { slog.SetDefault(previous) }
}

func startPreviousHubWait(t *testing.T, urls []string, deadline time.Time) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	done := make(chan struct{})
	go func() { defer close(done); result <- waitForPreviousHub(ctx, urls, deadline) }()
	t.Cleanup(func() { cancel(); <-done })
	return cancel, result
}

func requirePreviousHubWaiting(t *testing.T, observed <-chan struct{}, result <-chan error) {
	t.Helper()
	select {
	case <-observed:
	case err := <-result:
		t.Fatalf("the predecessor wait ended before observing a held address: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("the predecessor probe never observed a held address")
	}
}

func TestWaitForPreviousHub(t *testing.T) {
	t.Run("no socket returns at once", func(t *testing.T) {
		_, done := startPreviousHubWait(t, nil, time.Now().Add(5*time.Second))
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(30 * time.Second):
			t.Fatal("a free address must return at once")
		}
	})
	t.Run("a live listener returns once it closes", func(t *testing.T) {
		url := locallistentest.UniqueListenURL(t, "prev-live")
		closeListener, accepted := startAcceptingPredecessor(t, url)
		observed, restore := observeLogMessage("waiting for the previous hub to release its address and lease")
		t.Cleanup(restore)
		_, done := startPreviousHubWait(t, []string{url}, time.Now().Add(time.Minute))
		requirePreviousHubWaiting(t, observed, done)
		<-accepted
		closeListener()
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(30 * time.Second):
			t.Fatal("the predecessor wait did not return after the listener closed")
		}
	})
	t.Run("a live listener at the deadline returns nil", func(t *testing.T) {
		url := locallistentest.UniqueListenURL(t, "prev-held")
		_, accepted := startAcceptingPredecessor(t, url)
		_, done := startPreviousHubWait(t, []string{url}, time.Now())
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(30 * time.Second):
			t.Fatal("the predecessor wait did not stop at its deadline")
		}
		<-accepted
	})
	t.Run("a cancel before the dial returns the context error", func(t *testing.T) {
		url := locallistentest.UniqueListenURL(t, "prev-cancelled-dial")
		startAcceptingPredecessor(t, url)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		require.ErrorIs(t, waitForPreviousHub(ctx, []string{url}, time.Now().Add(time.Minute)), context.Canceled)
	})
	t.Run("a cancel returns the context error", func(t *testing.T) {
		url := locallistentest.UniqueListenURL(t, "prev-cancel")
		startAcceptingPredecessor(t, url)
		observed, restore := observeLogMessage("waiting for the previous hub to release its address and lease")
		t.Cleanup(restore)
		cancel, done := startPreviousHubWait(t, []string{url}, time.Now().Add(time.Minute))
		requirePreviousHubWaiting(t, observed, done)
		cancel()
		select {
		case err := <-done:
			require.ErrorIs(t, err, context.Canceled)
		case <-time.After(30 * time.Second):
			t.Fatal("the predecessor wait did not return after cancellation")
		}
	})
}

// addressHeld classifies one dial. The timeout case uses a controlled dial;
// the successful connection uses the platform's local transport.
func TestAddressHeld(t *testing.T) {
	url := locallistentest.UniqueListenURL(t, "address-held")
	startAcceptingPredecessor(t, url)
	dial, err := locallisten.Dialer(url)
	require.NoError(t, err)
	for _, tc := range []struct {
		name string
		dial func(context.Context) (net.Conn, error)
		want bool
	}{
		{"a connection reports held", dial, true},
		{"a timed-out dial reports held", func(ctx context.Context) (net.Conn, error) { <-ctx.Done(); return nil, ctx.Err() }, true},
		{"a refused dial reports free", func(context.Context) (net.Conn, error) { return nil, syscall.ECONNREFUSED }, false},
	} {
		t.Run(tc.name, func(t *testing.T) { assert.Equal(t, tc.want, addressHeld(t.Context(), tc.dial)) })
	}
}

// A launch whose predecessor left a live lease row fails once the shortened
// wait runs out, with the store's own answer, and releases its listeners.
func TestSoloStart_FailsWhenTheLeaseStaysHeld(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix-socket-specific reproduction")
	}
	orig := previousHubWait
	previousHubWait = 300 * time.Millisecond
	t.Cleanup(func() { previousHubWait = orig })

	cfg, listen, hubStore := heldLeaseFixture(t, "leapmux-hub-held")
	require.NoError(t, hubStore.Close())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, startErr := Start(ctx, cfg)

	require.Error(t, startErr)
	assert.ErrorIs(t, startErr, store.ErrHubAlreadyRunning,
		"a held lease must surface the store's own answer")
	// The listeners are released: a second launch can bind what this one
	// failed on.
	_, err := os.Stat(strings.TrimPrefix(listen, "unix:"))
	assert.ErrorIs(t, err, os.ErrNotExist,
		"the failed launch must release its socket")
}

// A caller cancel during the lease wait ends promptly, and the rival row
// keeps its holder: nobody took the lease from underneath.
func TestSoloStart_CancelDuringTheLeaseWaitEndsPromptly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix-socket-specific reproduction")
	}
	cfg, _, hubStore := heldLeaseFixture(t, "leapmux-hub-cancel")

	ctx, cancel := context.WithCancel(t.Context())
	previousLogger := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	leaseRefused := make(chan struct{})
	announceRefusal := sync.OnceFunc(func() { close(leaseRefused) })
	d := defaultDeps()
	d.serveHub = func(ctx context.Context, srv *hub.Server) error {
		current := slog.Default()
		slog.SetDefault(slog.New(logMessageObserver{Handler: current.Handler(),
			message: "another Hub holds the runtime lease; waiting for it to release", observed: announceRefusal}))
		defer slog.SetDefault(current)
		return srv.Serve(ctx)
	}
	startErr := make(chan error, 1)
	startDone := make(chan struct{})
	go func() { defer close(startDone); _, err := startForTest(ctx, cfg, d); startErr <- err }()
	t.Cleanup(func() { cancel(); <-startDone })
	// Cancel only after the actual runtime-lease acquisition was refused.
	select {
	case <-leaseRefused:
	case err := <-startErr:
		t.Fatalf("the launch ended before its lease was refused: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("the launch never attempted to acquire the held runtime lease")
	}
	cancel()

	select {
	case err := <-startErr:
		require.ErrorIs(t, err, context.Canceled,
			"the cancelled wait must end with the context error")
	case <-time.After(30 * time.Second):
		t.Fatal("the cancelled launch never returned")
	}
	// The rival row keeps its holder: a third party's acquire is still
	// refused, so the cancelled launch took nothing.
	_, acquireErr := hubStore.RevocationEvents().AcquireHubRuntimeLease(context.Background(), store.AcquireHubRuntimeLeaseParams{
		HolderID: "third-party", LeaseDuration: time.Hour,
	})
	require.ErrorIs(t, acquireErr, store.ErrHubAlreadyRunning,
		"the rival must keep its hold")
	require.NoError(t, hubStore.Close())
}

func heldLeaseFixture(t *testing.T, socketPrefix string) (Config, string, store.Store) {
	t.Helper()
	sandbox := locallistentest.SandboxHome(t)
	listen := locallistentest.UniqueListenURL(t, socketPrefix)
	dataDir := filepath.Join(sandbox, "data")
	cfg := Config{NoTCP: true, Args: []string{"--listen=" + listen, "--data-dir=" + dataDir}}
	hubDB := filepath.Join(dataDir, "hub", "hub.db")
	require.NoError(t, os.MkdirAll(filepath.Dir(hubDB), 0o750))
	sqlDB, err := sqlitest.OpenDB(hubDB, sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, sqlitest.MigrateDB(sqlDB))
	hubStore, err := sqlitest.Open(hubDB, sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = hubStore.Close() })
	require.NoError(t, sqlDB.Close())
	_, err = hubStore.RevocationEvents().AcquireHubRuntimeLease(t.Context(), store.AcquireHubRuntimeLeaseParams{HolderID: "rival-holder", LeaseDuration: time.Hour})
	require.NoError(t, err)
	return cfg, listen, hubStore
}

// An embedded Worker whose database never opens must fail the STARTUP,
// not merely log after it. The base launched worker.Run in a goroutine,
// returned a healthy Instance, and reported "open worker db" only to the
// log -- a startup that said success and immediately had no local
// Worker. This regression records that: Start must still be waiting for
// the Worker's own readiness when the failure lands, and must return it
// as the startup verdict.
func TestSoloStart_WaitsForWorkerInitializationFailure(t *testing.T) {
	cfg := startFailureEnv(t)
	d := newTestDeps()
	kill := serveUntilKilled(t, d, nil)
	t.Cleanup(kill)

	entered := make(chan struct{})
	release := make(chan struct{})
	wantErr := errors.New("open worker db: permission denied")
	d.runWorker = func(ctx context.Context, rc worker.RunConfig) error {
		defer func() { _ = rc.Directory.Close() }()
		close(entered)
		// The failure lands only when the test says so, and a teardown
		// that cancelled the context must not wait on the gate either.
		// OnReady is never called: this worker's database never opens.
		select {
		case <-release:
		case <-ctx.Done():
		}
		return wantErr
	}
	// unrelease runs BEFORE any Stop the test registers, because a Stop
	// that joins the Worker must not wait on a stub the test still holds.
	var unreleaseOnce sync.Once
	unrelease := func() { unreleaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unrelease)

	ctx, cancel := context.WithTimeout(context.Background(), soloTestTimeout)
	t.Cleanup(cancel)

	type outcome struct {
		inst *Instance
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		inst, err := startForTest(ctx, cfg, d.deps)
		done <- outcome{inst: inst, err: err}
	}()

	// Wait until the Worker entry is PAUSED inside its initialization, then
	// release it with the failure. A start that returned early -- the defect
	// this regression records -- is caught by the verdict below, not here.
	select {
	case <-entered:
	case got := <-done:
		// The base returns here with a healthy Instance in hand while its
		// Worker has not even opened its database. Finish the assertions on
		// that verdict; the goroutine park is already released by cleanup.
		require.Error(t, got.err, "a worker database that never opens must fail the startup")
		assert.ErrorIs(t, got.err, wantErr)
		assert.Nil(t, got.inst, "a failed worker initialization must not produce an Instance")
		if got.inst != nil {
			_ = got.inst.Stop()
		}
		t.Fatal("Start returned before the embedded Worker finished initializing")
	case <-time.After(soloTestTimeout):
		t.Fatal("Start never reached the embedded Worker entry")
	}
	unrelease()

	var got outcome
	select {
	case got = <-done:
	case <-time.After(soloTestTimeout):
		t.Fatal("Start never returned after the Worker's initialization failed")
	}
	require.Error(t, got.err, "a worker database that never opens must fail the startup")
	assert.ErrorIs(t, got.err, wantErr)
	assert.Nil(t, got.inst, "a failed worker initialization must not produce an Instance")
	if got.inst != nil {
		t.Cleanup(func() { _ = got.inst.Stop() })
	}
}

// A locally initialized worker does not complete combined readiness
// until the local Hub ACCEPTS its identity: the embedded pair is ready
// as a pair, not as a hub plus a dialing worker.
func TestSoloReadyWaitsForAcceptedWorkerIdentity(t *testing.T) {
	cfg := soloStartEnv(t, false)
	d := newTestDeps()
	kill := serveUntilKilled(t, d, nil)
	t.Cleanup(kill)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	initialized := make(chan worker.RunConfig, 1)
	d.runWorker = func(ctx context.Context, rc worker.RunConfig) error {
		defer func() { _ = rc.Directory.Close() }()
		rc.OnReady()
		initialized <- rc
		<-ctx.Done()
		return nil
	}
	type outcome struct {
		inst *Instance
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		inst, err := startForTest(ctx, cfg, d.deps)
		done <- outcome{inst, err}
	}()
	var rc worker.RunConfig
	select {
	case rc = <-initialized:
	case <-time.After(soloTestTimeout):
		t.Fatal("worker initialization did not begin")
	}
	if !assert.NotNil(t, rc.OnConnectionState, "combined readiness must observe the accepted Hub identity") {
		cancel()
		got := <-done
		if got.inst != nil {
			_ = got.inst.Stop()
		}
		return
	}
	select {
	case got := <-done:
		cancel()
		if got.inst != nil {
			_ = got.inst.Stop()
		}
		t.Fatal("startup completed before the Hub accepted the worker identity")
	default:
	}
	rc.OnConnectionState(worker.ConnectionState{Connected: true})
	select {
	case got := <-done:
		require.NoError(t, got.err)
		require.NotNil(t, got.inst)
		require.NoError(t, got.inst.Stop())
	case <-time.After(soloTestTimeout):
		t.Fatal("startup did not finish after identity acceptance")
	}
}

// The full Stop joins the worker's database close after the drain: the
// hub outlives only the outbound drain, never the worker's local
// teardown.
func TestShutdownJoinsDatabaseAfterDrain(t *testing.T) {
	cfg := soloStartEnv(t, false)
	d := newTestDeps()
	drained := make(chan struct{})
	releaseDatabase := make(chan struct{})
	databaseClosed := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseDatabase) }) }
	t.Cleanup(release)
	d.runWorker = func(ctx context.Context, rc worker.RunConfig) error {
		defer func() { _ = rc.Directory.Close() }()
		if rc.OnReady != nil {
			rc.OnReady()
			if rc.OnConnectionState != nil {
				rc.OnConnectionState(worker.ConnectionState{Connected: true})
			}
		}
		<-ctx.Done()
		// Hold database teardown after drain, without a timing window.
		if rc.Drained != nil {
			rc.Drained()
		}
		close(drained)
		<-releaseDatabase
		close(databaseClosed)
		return nil
	}

	inst, err := startForTest(context.Background(), cfg, d.deps)
	require.NoError(t, err)
	t.Cleanup(func() { release(); _ = inst.Stop() })
	stopped := make(chan error, 1)
	go func() { stopped <- inst.Stop() }()
	select {
	case <-drained:
	case <-time.After(soloTestTimeout):
		t.Fatal("worker shutdown did not reach its drain")
	}
	select {
	case err := <-stopped:
		t.Fatalf("Stop returned while database teardown was held: %v", err)
	default:
	}
	assert.False(t, inst.rootLease.Closed())
	release()
	select {
	case err := <-stopped:
		require.NoError(t, err)
	case <-time.After(soloTestTimeout):
		t.Fatal("Stop did not finish after database close")
	}
	<-databaseClosed
	assert.True(t, inst.rootLease.Closed())
}

func TestUnexpectedWorkerExitEndsCombinedInstance(t *testing.T) {
	for _, ready := range []bool{false, true} {
		for _, workerErr := range []error{nil, errors.New("worker database failed")} {
			t.Run(fmt.Sprintf("ready=%t/error=%v", ready, workerErr), func(t *testing.T) {
				cfg := soloStartEnv(t, false)
				d := newTestDeps()
				ctx, cancel := context.WithCancel(testutil.DeadlineContext(t))
				defer cancel()
				entered, releaseWorker := make(chan struct{}), make(chan struct{})
				d.runWorker = func(workerCtx context.Context, rc worker.RunConfig) error {
					defer func() { _ = rc.Directory.Close() }()
					defer rc.Drained()
					if ready {
						rc.OnReady()
						rc.OnConnectionState(worker.ConnectionState{Connected: true})
					}
					close(entered)
					select {
					case <-releaseWorker:
						return workerErr
					case <-workerCtx.Done():
						return workerCtx.Err()
					}
				}
				type result struct {
					inst *Instance
					err  error
				}
				var started result
				startDone := make(chan struct{})
				go func() {
					started.inst, started.err = startForTest(ctx, cfg, d.deps)
					close(startDone)
				}()
				t.Cleanup(func() {
					cancel()
					<-startDone
					if started.inst != nil {
						_ = started.inst.Stop()
					}
				})
				select {
				case <-entered:
				case <-ctx.Done():
					t.Fatal("worker startup was not reached")
				}
				if ready {
					select {
					case <-startDone:
						require.NoError(t, started.err)
					case <-ctx.Done():
						t.Fatal("startup did not report worker readiness")
					}
				}
				close(releaseWorker)
				var outcome error
				if ready {
					done := make(chan error, 1)
					go func() { done <- started.inst.Wait() }()
					select {
					case outcome = <-done:
					case <-ctx.Done():
						t.Fatal("unexpected worker exit did not end the combined instance")
					}
				} else {
					select {
					case <-startDone:
						outcome = started.err
					case <-ctx.Done():
						t.Fatal("worker exit before readiness left startup waiting")
					}
				}
				if workerErr != nil {
					require.ErrorIs(t, outcome, workerErr)
				} else {
					require.ErrorContains(t, outcome, "unexpected")
				}
				require.NotErrorIs(t, outcome, context.Canceled)
				require.NotErrorIs(t, outcome, context.DeadlineExceeded)
			})
		}
	}
}

func TestDeferredDevSetupPublishesCompletionAndFailure(t *testing.T) {
	for _, failure := range []error{nil, errors.New("deferred worker database failed")} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			cfg := soloStartEnv(t, true)
			d := newTestDeps()
			clock := testutil.NewQuartzMock(t)
			d.clock = clock
			ctx, cancel := context.WithCancel(testutil.DeadlineContext(t))
			defer cancel()
			created := clock.Trap().NewTicker(workerSetupPollTag)
			defer created.Close()
			var attempts atomic.Int32
			completed := make(chan struct{})
			d.stubWorkerBringUp(func(context.Context) error {
				if attempts.Add(1) == 1 {
					return errNoAdminYet
				}
				close(completed)
				return failure
			})
			inst, err := startForTest(ctx, cfg, d.deps)
			require.NoError(t, err)
			t.Cleanup(func() { cancel(); _ = inst.Stop() })
			require.True(t, inst.Startup().WorkerPendingSetup)
			created.MustWait(ctx).MustRelease(ctx)
			clock.Advance(workerSetupPollInterval).MustWait(ctx)
			select {
			case <-completed:
			case <-ctx.Done():
				t.Fatal("the deferred setup attempt did not run")
			}
			if failure == nil {
				testutil.AssertEventually(t, func() bool {
					snapshot := inst.Startup()
					return snapshot.WorkerReady && !snapshot.WorkerPendingSetup
				})
			} else {
				done := make(chan error, 1)
				go func() { done <- inst.Wait() }()
				select {
				case err := <-done:
					require.ErrorIs(t, err, failure)
				case <-ctx.Done():
					t.Fatal("deferred setup failure did not end the combined instance")
				}
			}
		})
	}
}

func TestDeferredDevSetupPreservesIndependentFailureDuringStop(t *testing.T) {
	cfg := soloStartEnv(t, true)
	d := newTestDeps()
	clock := testutil.NewQuartzMock(t)
	d.clock = clock
	ctx := testutil.DeadlineContext(t)
	created := clock.Trap().NewTicker(workerSetupPollTag)
	defer created.Close()
	entered := make(chan struct{})
	failure := errors.New("deferred database cleanup failed")
	var attempts atomic.Int32
	d.stubWorkerBringUp(func(ctx context.Context) error {
		if attempts.Add(1) == 1 {
			return errNoAdminYet
		}
		close(entered)
		<-ctx.Done()
		return errors.Join(ctx.Err(), failure)
	})
	inst, err := startForTest(ctx, cfg, d.deps)
	require.NoError(t, err)
	t.Cleanup(func() { _ = inst.Stop() })
	created.MustWait(ctx).MustRelease(ctx)
	clock.Advance(workerSetupPollInterval).MustWait(ctx)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("deferred setup did not begin")
	}
	require.ErrorIs(t, inst.Stop(), failure)
}

// A fresh dev instance reports ready-for-setup while its worker waits
// for the first administrator, and the deferred worker's failure stays
// observable.
func TestSoloStart_DevModeReportsReadyForSetup(t *testing.T) {
	cfg := soloStartEnv(t, true)
	d := newTestDeps()
	kill := serveUntilKilled(t, d, nil)
	t.Cleanup(kill)

	d.stubWorkerBringUp(func(ctx context.Context) error {
		if ctx.Err() == nil {
			return errNoAdminYet
		}
		return ctx.Err()
	})

	inst, err := startForTest(context.Background(), cfg, d.deps)
	require.NoError(t, err)
	t.Cleanup(func() { _ = inst.Stop() })

	snap := inst.Startup()
	assert.True(t, snap.HubReady, "the hub serves /setup")
	assert.True(t, snap.WorkerPendingSetup, "a fresh dev instance reports ready for setup")
	assert.False(t, snap.WorkerReady, "the worker waits for the administrator")
}

func TestStartupSnapshotDerivesOneWorkerPhase(t *testing.T) {
	instance := &Instance{failureCh: make(chan struct{})}
	registration := lifecycle.RegistrationStatus{State: lifecycle.RegistrationRegistered, WorkerID: new("worker")}
	failure := errors.New("worker startup failed")
	for _, step := range []struct {
		transition               func()
		ready, pending, starting bool
	}{
		{func() {}, false, false, false},
		{instance.markWorkerPendingSetup, false, true, false},
		{func() { instance.markWorkerStarting(registration) }, false, false, true},
		{instance.markWorkerReady, true, false, false},
		{func() { instance.failWorker(failure) }, false, false, false},
	} {
		_, changed := instance.ObserveStartup()
		step.transition()
		snapshot := instance.Startup()
		assert.Equal(t, step.ready, snapshot.WorkerReady)
		assert.Equal(t, step.pending, snapshot.WorkerPendingSetup)
		assert.Equal(t, step.starting, snapshot.WorkerStarting)
		if step.ready || step.pending || step.starting || snapshot.Failure != nil {
			select {
			case <-changed:
			default:
				t.Fatal("state transition did not notify observers")
			}
		}
	}
	instance.markWorkerReady()
	instance.markWorkerPendingSetup()
	instance.markWorkerStarting(registration)
	instance.failWorker(assert.AnError)
	instance.markHubReady()
	instance.updateWorkerConnection(worker.ConnectionState{Connected: false, Err: assert.AnError})
	snapshot := instance.Startup()
	assert.True(t, snapshot.HubReady)
	assert.False(t, snapshot.WorkerReady)
	assert.False(t, snapshot.WorkerPendingSetup)
	assert.False(t, snapshot.WorkerStarting)
	assert.ErrorIs(t, snapshot.Failure, failure)
	assert.ErrorIs(t, instance.workerError(), failure)
	assert.Equal(t, lifecycle.ConnectionDisconnected, snapshot.Connection.State)
	*snapshot.Registration.WorkerID = "changed"
	*snapshot.Connection.LastError = "changed"
	fresh := instance.Startup()
	assert.Equal(t, "worker", *fresh.Registration.WorkerID)
	assert.Equal(t, assert.AnError.Error(), *fresh.Connection.LastError)
}

func TestConcurrentStartupCallbacksCannotReviveFailedWorker(t *testing.T) {
	instance := &Instance{failureCh: make(chan struct{})}
	failure := errors.New("independent worker failure")
	start := make(chan struct{})
	var callers sync.WaitGroup
	for range 32 {
		callers.Go(func() { <-start; instance.markWorkerReady() })
	}
	callers.Go(func() { <-start; instance.failWorker(failure) })
	close(start)
	callers.Wait()
	snapshot := instance.Startup()
	assert.ErrorIs(t, snapshot.Failure, failure)
	assert.False(t, snapshot.WorkerReady)
	assert.False(t, snapshot.WorkerPendingSetup)
	assert.False(t, snapshot.WorkerStarting)
	select {
	case <-instance.failureCh:
	default:
		t.Fatal("worker failure was not published")
	}
}
