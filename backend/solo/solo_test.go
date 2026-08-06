package solo

import (
	"context"
	"errors"
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
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/locallisten/locallistentest"
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

// stubServeHub swaps the Hub-serving seam for the duration of the test.
func stubServeHub(t *testing.T, fn func(context.Context, *hub.Server) error) {
	t.Helper()
	prev := serveHub
	serveHub = fn
	t.Cleanup(func() { serveHub = prev })
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
// exactly the duplicate report startupDone removes for startup.
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
