package agent

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/config"
)

// idleAgent is the smallest Agent a permit test needs. It is defined here rather
// than reused from manager_test.go's stubProvider because that file is
// `//go:build unix`, and the startup permit pool has no platform behaviour --
// the Windows vet job must compile and run these cases too.
type idleAgent struct{}

func (idleAgent) AgentID() string                                 { return "idle" }
func (idleAgent) SendInput(string, []*leapmuxv1.Attachment) error { return nil }
func (idleAgent) SendRawInput([]byte) error                       { return nil }
func (idleAgent) Stop()                                           {}
func (idleAgent) IsStopped() bool                                 { return false }
func (idleAgent) DiscardOutput()                                  {}
func (idleAgent) ClearContext() (string, bool)                    { return "", false }
func (idleAgent) Wait() error                                     { return nil }
func (idleAgent) Stderr() string                                  { return "" }
func (idleAgent) HandleOutput([]byte)                             {}
func (idleAgent) OptionGroups() []*leapmuxv1.AvailableOptionGroup { return nil }
func (idleAgent) UpdateSettings(optionmap.Map) bool               { return true }
func (idleAgent) Interrupt() error                                { return nil }

// livingAgent is an idleAgent that stays registered: its Wait parks until Stop.
//
// A test that asserts the manager REGISTERED an agent needs one. startAgentWith
// registers the provider and then waits on it in a goroutine that deregisters
// it the moment Wait returns, so idleAgent -- whose Wait returns at once -- is
// reaped while the assertion is still being written. That race resolved the
// friendly way on every developer's machine and the other way on the Windows
// runner.
type livingAgent struct {
	idleAgent
	stop chan struct{}
	once sync.Once
}

func newLivingAgent() *livingAgent {
	return &livingAgent{stop: make(chan struct{})}
}

func (a *livingAgent) Stop()       { a.once.Do(func() { close(a.stop) }) }
func (a *livingAgent) Wait() error { <-a.stop; return nil }

// blockingStart is a startFunc that parks inside the "startup handshake" until
// the test releases it, so a test can observe how many spawns are in that
// window at once. It reports the high-water mark of concurrent entries, which
// is the property the permit pool exists to cap.
type blockingStart struct {
	entered  chan struct{}
	release  chan struct{}
	inFlight atomic.Int32
	peak     atomic.Int32
	// fail, when set, makes every start return an error AFTER it is released,
	// so a test can prove a failed start still gives its permit back.
	fail bool
}

func newBlockingStart(capacity int) *blockingStart {
	return &blockingStart{
		entered: make(chan struct{}, capacity),
		release: make(chan struct{}),
	}
}

func (b *blockingStart) fn(_ context.Context, opts Options, _ OutputSink) (Agent, error) {
	defer testutil.TrackPeak(&b.inFlight, &b.peak)()
	b.entered <- struct{}{}
	<-b.release
	if b.fail {
		return nil, fmt.Errorf("start refused for %s", opts.AgentID)
	}
	return idleAgent{}, nil
}

// waitForEntries blocks until n starts have entered, or fails the test.
func (b *blockingStart) waitForEntries(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-b.entered:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of %d starts entered", i, n)
		}
	}
}

// spawn launches count concurrent startAgentWith calls and returns a func that
// waits for all of them, collecting their errors.
func spawn(t *testing.T, m *Manager, start startFunc, count int, idPrefix string) func() []error {
	t.Helper()
	errs := make([]error, count)
	var wg sync.WaitGroup
	for i := range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = m.startAgentWith(context.Background(), Options{
				AgentID:    fmt.Sprintf("%s-%d", idPrefix, i),
				WorkingDir: t.TempDir(),
			}, noopSink{}, start, true)
		}()
	}
	return func() []error {
		wg.Wait()
		return errs
	}
}

// TestStartAgent_LimitsConcurrentStartups is the guard for the whole point of
// the permit pool: with a limit of N, only N BACKGROUND agent startups run at
// once. Without it the boot-time resume sweep starts one CLI per open tab
// simultaneously.
func TestStartAgent_LimitsConcurrentStartups(t *testing.T) {
	t.Parallel()

	const limit = 2
	const total = 5
	m := NewManager(nil)
	m.SetStartupConcurrency(limit)

	start := newBlockingStart(total)
	wait := spawn(t, m, start.fn, total, "cap")

	// Exactly `limit` starts get in. The rest must be parked on the permit.
	start.waitForEntries(t, limit)
	select {
	case <-start.entered:
		t.Fatalf("a %dth start entered while %d were already in flight -- the startup permit pool did not cap the spawns", limit+1, limit)
	case <-time.After(150 * time.Millisecond):
	}

	close(start.release)
	errs := wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	assert.LessOrEqual(t, int(start.peak.Load()), limit,
		"more startups overlapped than the configured concurrency allows")
	// The upper bound alone would also hold if the harness never observed any
	// overlap at all, so pin the floor too: waitForEntries returned only after
	// `limit` starts were simultaneously parked.
	assert.Equal(t, limit, int(start.peak.Load()),
		"the pool must ADMIT the configured number, not merely stay under it")
	// Every spawn ran: the cap delays a start, it never drops one.
	assert.Len(t, errs, total)
}

// TestStartAgent_InteractiveSpawnsTakeNoPermit is the guard for the split the
// pool exists to make.
//
// A spawn the user asked for must never queue. The send path calls the cold
// start INLINE, and the client gives that RPC about fifteen seconds, so a permit
// wait would fail a send whose message row is already durable -- and raising the
// configured number would not help, because it raises the background count just
// as much.
func TestStartAgent_InteractiveSpawnsTakeNoPermit(t *testing.T) {
	t.Parallel()

	m := NewManager(nil)
	m.SetStartupConcurrency(1)

	// Fill the pool with a background start and park it there.
	background := newBlockingStart(1)
	waitBackground := spawn(t, m, background.fn, 1, "sweep")
	background.waitForEntries(t, 1)

	// An interactive spawn must get straight through.
	interactive := newBlockingStart(1)
	done := make(chan error, 1)
	go func() {
		_, err := m.startAgentWith(context.Background(), Options{
			AgentID:    "interactive",
			WorkingDir: t.TempDir(),
		}, noopSink{}, interactive.fn, false)
		done <- err
	}()
	interactive.waitForEntries(t, 1)

	close(interactive.release)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("an interactive spawn waited on the background permit pool; the user's send would fail on the client timeout")
	}

	close(background.release)
	for _, err := range waitBackground() {
		require.NoError(t, err)
	}
}

// TestStartAgent_ConcurrencyOfOneSerializes covers the smallest useful setting,
// where the pool degenerates to a mutex. A capacity computed as N-1 (or an
// unbuffered channel) would admit nobody and wedge every spawn here.
func TestStartAgent_ConcurrencyOfOneSerializes(t *testing.T) {
	t.Parallel()

	m := NewManager(nil)
	m.SetStartupConcurrency(1)

	start := newBlockingStart(2)
	wait := spawn(t, m, start.fn, 2, "serial")

	start.waitForEntries(t, 1)
	select {
	case <-start.entered:
		t.Fatal("both starts ran at once under a concurrency of 1")
	case <-time.After(150 * time.Millisecond):
	}

	close(start.release)
	for _, err := range wait() {
		require.NoError(t, err)
	}
	assert.Equal(t, int32(1), start.peak.Load())
}

// TestStartAgent_FailedStartReleasesItsSlot pins that the permit is returned on
// the error branch too. A leak there shrinks the pool permanently: after enough
// failed spawns the worker would admit no startup at all, and the symptom is a
// hang with nothing logged.
func TestStartAgent_FailedStartReleasesItsSlot(t *testing.T) {
	t.Parallel()

	const limit = 2
	m := NewManager(nil)
	m.SetStartupConcurrency(limit)

	failing := newBlockingStart(limit)
	failing.fail = true
	waitFailing := spawn(t, m, failing.fn, limit, "boom")
	failing.waitForEntries(t, limit)
	close(failing.release)
	for _, err := range waitFailing() {
		require.Error(t, err)
	}

	// The pool must be whole again: `limit` fresh starts get in together.
	ok := newBlockingStart(limit)
	waitOK := spawn(t, m, ok.fn, limit, "after-boom")
	ok.waitForEntries(t, limit)
	close(ok.release)
	for _, err := range waitOK() {
		require.NoError(t, err)
	}
}

// TestStartAgent_CancelledContextGivesUpTheQueue pins that a spawn whose tab is
// closed while it waits for a permit abandons the queue instead of spawning a
// process nobody wants once its turn arrives.
func TestStartAgent_CancelledContextGivesUpTheQueue(t *testing.T) {
	t.Parallel()

	m := NewManager(nil)
	m.SetStartupConcurrency(1)

	holder := newBlockingStart(1)
	waitHolder := spawn(t, m, holder.fn, 1, "holder")
	holder.waitForEntries(t, 1)

	var queuedStarted atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	queuedErr := make(chan error, 1)
	go func() {
		_, err := m.startAgentWith(ctx, Options{AgentID: "queued", WorkingDir: t.TempDir()}, noopSink{},
			func(context.Context, Options, OutputSink) (Agent, error) {
				queuedStarted.Store(true)
				return idleAgent{}, nil
			}, true)
		queuedErr <- err
	}()

	// Give the queued spawn time to reach the permit wait, then cancel it.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-queuedErr:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled spawn stayed parked on the permit instead of giving up")
	}
	assert.False(t, queuedStarted.Load(),
		"the cancelled spawn ran its start func; a closed tab must not spawn a process when its turn comes")

	close(holder.release)
	for _, err := range waitHolder() {
		require.NoError(t, err)
	}
}

// TestResolveStartupConcurrency_FollowsTheCPUBudgetNotTheCoreCount pins WHICH
// runtime number the default derives from, and that the manager's pool is sized
// from it. Both answers agree on a normal laptop, so the machine running this
// test cannot tell them apart -- lower GOMAXPROCS and only the correct source
// moves.
//
// A quota-limited container is exactly this shape: NumCPU keeps reporting the
// host's cores while the scheduler runs the process on a fraction of one, and a
// default read from NumCPU would run four CPU-bound handshakes there.
//
// The rule itself lives in the config package (config.ResolveStartupConcurrency)
// and is covered there; this asserts the manager's pool follows it.
func TestResolveStartupConcurrency_FollowsTheCPUBudgetNotTheCoreCount(t *testing.T) {
	// Not parallel: GOMAXPROCS is process-global state.
	prev := runtime.GOMAXPROCS(1)
	t.Cleanup(func() { runtime.GOMAXPROCS(prev) })

	m := NewManager(nil)
	m.SetStartupConcurrency(0)
	assert.Equal(t, 1, m.StartupConcurrency(),
		"the default must follow the CPU budget; reading runtime.NumCPU() would report the host's cores here")
}

// TestNewManager_HasAUsablePoolBeforeConfiguration pins that a manager built
// before any configuration is read still spawns. hub.New constructs the manager
// long before bootstrap.Wire applies the configured concurrency, and a nil or
// zero-capacity permit channel there would block every spawn for ever with
// nothing logged.
func TestNewManager_HasAUsablePoolBeforeConfiguration(t *testing.T) {
	t.Parallel()

	// livingAgent, so HasAgent asks about an agent that is still running. An
	// agent whose Wait returns at once is deregistered correctly and at once,
	// and the assertion would be racing that cleanup rather than reading the
	// registration.
	agent := newLivingAgent()
	t.Cleanup(agent.Stop)

	m := NewManager(nil)
	_, err := m.startAgentWith(context.Background(), Options{
		AgentID:    "unconfigured",
		WorkingDir: t.TempDir(),
	}, noopSink{}, func(context.Context, Options, OutputSink) (Agent, error) {
		return agent, nil
	}, true)
	require.NoError(t, err)
	assert.True(t, m.HasAgent("unconfigured"))
}

// TestSetStartupConcurrency_ZeroRestoresTheDefault pins that the "0 means auto"
// convention survives the whole config chain: every entry point passes the
// loaded value straight through, and an unset worker.yaml key arrives here as 0.
func TestSetStartupConcurrency_ZeroRestoresTheDefault(t *testing.T) {
	t.Parallel()

	m := NewManager(nil)
	m.SetStartupConcurrency(1)
	m.SetStartupConcurrency(0)

	assert.Equal(t, config.ResolveStartupConcurrency(0), m.StartupConcurrency())
}
