//go:build unix

// Depends on helpers in claude_test.go / goose_test.go that spawn /bin/sh.

package agent

import (
	"bufio"
	"context"
	"os/exec"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubProvider struct {
	groups         []*leapmuxv1.AvailableOptionGroup
	clearContextFn func() (string, bool)
}

func (s *stubProvider) AgentID() string                                 { return "stub" }
func (s *stubProvider) SendInput(string, []*leapmuxv1.Attachment) error { return nil }
func (s *stubProvider) SendRawInput([]byte) error                       { return nil }
func (s *stubProvider) Stop()                                           {}
func (s *stubProvider) IsStopped() bool                                 { return false }
func (s *stubProvider) DiscardOutput()                                  {}
func (s *stubProvider) ClearContext() (string, bool) {
	if s.clearContextFn != nil {
		return s.clearContextFn()
	}
	return "", false
}
func (s *stubProvider) Wait() error                                     { return nil }
func (s *stubProvider) Stderr() string                                  { return "" }
func (s *stubProvider) HandleOutput([]byte)                             {}
func (s *stubProvider) OptionGroups() []*leapmuxv1.AvailableOptionGroup { return s.groups }
func (s *stubProvider) SettingsSnapshot() SettingsApplyResult {
	return confirmedSettings(CurrentOptions(s.groups))
}
func (s *stubProvider) UpdateSettings(options optionmap.Map) SettingsApplyResult {
	return confirmedSettings(options)
}
func (s *stubProvider) Interrupt() error { return nil }

// startMockAgent wraps mockStart to satisfy the startFunc signature.
func startMockAgent(ctx context.Context, opts Options, sink OutputSink) (Agent, error) {
	return mockStart(ctx, opts, sink)
}

func TestManager_SetOnExit_FiresOnStop(t *testing.T) {
	m := NewManager(func(string, int, error, bool) {
		// Original handler: should be replaced by SetOnExit below.
		t.Error("original onExit should not be called after SetOnExit")
	})

	exited := make(chan string, 1)
	m.SetOnExit(func(agentID string, _ int, _ error, _ bool) {
		exited <- agentID
	})

	_, err := m.startAgentWith(context.Background(), Options{
		AgentID:    "s-exit",
		Options:    map[string]string{OptionIDModel: "test"},
		WorkingDir: t.TempDir(),
	}, noopSink{}, startMockAgent, false)
	require.NoError(t, err)

	m.StopAgent("s-exit")

	select {
	case got := <-exited:
		assert.Equal(t, "s-exit", got)
	case <-time.After(2 * time.Second):
		t.Fatal("exit handler did not fire after stop")
	}
}

func TestManager_StartAndStop(t *testing.T) {
	m := NewManager(nil)
	ctx := context.Background()

	_, err := m.startAgentWith(ctx, Options{
		AgentID:    "s1",
		Options:    map[string]string{OptionIDModel: "test"},
		WorkingDir: t.TempDir(),
	}, noopSink{}, startMockAgent, false)
	require.NoError(t, err, "StartAgent")

	assert.True(t, m.HasAgent("s1"), "expected HasAgent(s1) = true")

	// Duplicate start should fail.
	_, err = m.startAgentWith(ctx, Options{
		AgentID:    "s1",
		Options:    map[string]string{OptionIDModel: "test"},
		WorkingDir: t.TempDir(),
	}, noopSink{}, startMockAgent, false)
	assert.Error(t, err, "expected error for duplicate agent")

	// Stop and verify cleanup.
	m.StopAgent("s1")

	// Wait for the background goroutine to clean up.
	testutil.AssertEventually(t, func() bool {
		return !m.HasAgent("s1")
	}, "expected HasAgent(s1) = false after stop")
}

func TestManager_ClearContextWaitsForLifecycleLock(t *testing.T) {
	t.Parallel()

	m := NewManager(nil)
	entered := make(chan struct{}, 1)
	m.agents["locked"] = &stubProvider{clearContextFn: func() (string, bool) {
		entered <- struct{}{}
		return "thread-new", true
	}}

	unlock := m.LockAgent("locked")
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.ClearContext("locked")
	}()

	premature := false
	select {
	case <-entered:
		premature = true
	case <-time.After(50 * time.Millisecond):
	}
	unlock()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ClearContext did not continue after the lifecycle lock was released")
	}
	assert.False(t, premature, "ClearContext entered the provider while another lifecycle operation held the lock")
}

func TestManager_SendInput(t *testing.T) {
	m := NewManager(nil)
	ctx := context.Background()
	sink := &testSink{}

	_, err := m.startAgentWith(ctx, Options{
		AgentID:    "s2",
		Options:    map[string]string{OptionIDModel: "test"},
		WorkingDir: t.TempDir(),
	}, sink, startMockAgent, false)
	require.NoError(t, err, "StartAgent")
	defer m.StopAgent("s2")

	// SendInput sends a user JSON message; the mock echoes it back.
	// Since it's a simple user text echo, HandleOutput drops it.
	// Send raw assistant NDJSON to verify the full pipeline.
	require.NoError(t, m.SendRawInput("s2", []byte(`{"type":"assistant","message":{"role":"assistant","content":"hi"}}`+"\n")), "SendRawInput")

	testutil.AssertEventually(t, func() bool {
		return sink.MessageCount() > 0
	}, "expected output from agent")
}

func TestManager_SendInputUnknownAgent(t *testing.T) {
	m := NewManager(nil)

	assert.Error(t, m.SendInput("nonexistent", "hello", nil), "expected error for unknown agent")

	// The error path must release the lifecycle lock. It is taken before the
	// lookup, so a return that skipped the unlock would leave the agent id locked
	// forever -- every later send, restart, or auto-start for it would park on a
	// mutex nobody holds a reason for, and the agent would simply stop responding
	// with no error anywhere. A second send proves the lock came back.
	done := make(chan struct{})
	go func() {
		defer close(done)
		assert.Error(t, m.SendInput("nonexistent", "hello again", nil), "expected error for unknown agent")
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("second send blocked: the failed send did not release the lifecycle lock")
	}
}

// A message that arrives while the agent restarts must wait for the new
// process, not fail against the old one.
//
// The window is a settings change the provider cannot apply live: the service
// takes the lifecycle lock, stops the old process, and starts a new one. A send
// that lands in it must not reach the stopped process and fail the queue item.
func TestManager_SendInputWaitsForRestartToFinish(t *testing.T) {
	m := NewManager(nil)
	ctx := context.Background()
	const agentID = "s-send-during-restart"
	opts := Options{
		AgentID:    agentID,
		Options:    map[string]string{OptionIDModel: "test"},
		WorkingDir: t.TempDir(),
	}

	_, err := m.startAgentWith(ctx, opts, noopSink{}, startMockAgent, false)
	require.NoError(t, err, "StartAgent")

	// Open the restart window by hand -- lock held, old process gone, new one not
	// started -- which is what RestartAgent holds across its stop and its start.
	unlock := m.LockAgent(agentID)
	require.True(t, m.StopAndWaitAgent(agentID), "expected the running agent to stop")

	sendErr := make(chan error, 1)
	go func() {
		sendErr <- m.SendInput(agentID, "sent while the agent was restarting", nil)
	}()

	// Nothing can deliver this yet, so the send must still be in flight. A send
	// that resolves here resolved against no process at all, which is the bug.
	select {
	case err := <-sendErr:
		t.Fatalf("send resolved inside the restart window: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	// Finish the restart, then release the lock the way the service does.
	_, err = m.startAgentWith(ctx, opts, noopSink{}, startMockAgent, false)
	require.NoError(t, err, "restart")
	unlock()
	defer m.StopAgent(agentID)

	select {
	case err := <-sendErr:
		assert.NoError(t, err, "the waiting send must land on the restarted agent")
	case <-time.After(5 * time.Second):
		t.Fatal("send never completed after the restart finished")
	}
}

// A message to a SUBAGENT tab reaches the owner process, so it meets the same
// restart window a root send does -- and used to fail the same way, with the
// child's queue item could fail for a restart that the user did not request.
// Each input path into the process must wait for the restart.
func TestManager_SendChildInputWaitsForRestartToFinish(t *testing.T) {
	m := NewManager(nil)
	ctx := context.Background()
	const agentID = "s-child-send-during-restart"
	opts := Options{
		AgentID:    agentID,
		Options:    map[string]string{OptionIDModel: "test"},
		WorkingDir: t.TempDir(),
	}

	_, err := m.startAgentWith(ctx, opts, noopSink{}, startMockAgent, false)
	require.NoError(t, err, "StartAgent")

	// The restart window, held open by hand the way RestartAgent holds it.
	unlock := m.LockAgent(agentID)
	require.True(t, m.StopAndWaitAgent(agentID), "expected the running agent to stop")

	sendErr := make(chan error, 1)
	go func() {
		sendErr <- m.SendChildInput(agentID, "child-key", "sent while restarting", nil)
	}()

	// Nothing can deliver this yet. A send that resolves here resolved against
	// no process at all, which is the bug.
	select {
	case err := <-sendErr:
		t.Fatalf("child send resolved inside the restart window: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	_, err = m.startAgentWith(ctx, opts, noopSink{}, startMockAgent, false)
	require.NoError(t, err, "restart")
	unlock()
	defer m.StopAgent(agentID)

	select {
	case err := <-sendErr:
		// The mock provider steers no child, so the ERROR is expected. What
		// matters is that the call got past the lock to a live process at all,
		// rather than resolving against the one the restart was destroying.
		assert.ErrorIs(t, err, ErrChildSteeringUnsupported)
	case <-time.After(5 * time.Second):
		t.Fatal("child send never completed after the restart finished")
	}
}

// The lifecycle lock covers the map READ, never the provider write.
//
// A provider's SendInput blocks for as long as the turn takes -- Codex's
// turn/start has no timeout at all. Holding a lifecycle lock across that would
// park every restart, /clear, plan execution and auto-start for the agent
// behind one message, and would defeat mid-turn steering, which needs a second
// send to reach the provider WHILE the first turn runs.
func TestManager_SendInputDoesNotHoldTheLifecycleLockAcrossTheWrite(t *testing.T) {
	m := NewManager(nil)
	ctx := context.Background()
	const agentID = "s-send-releases-lock"

	release := make(chan struct{})
	entered := make(chan struct{})
	start := func(ctx context.Context, opts Options, sink OutputSink) (Agent, error) {
		base, err := mockStart(ctx, opts, sink)
		if err != nil {
			return nil, err
		}
		return &blockingSendAgent{Agent: base, entered: entered, release: release}, nil
	}
	_, err := m.startAgentWith(ctx, Options{
		AgentID:    agentID,
		Options:    map[string]string{OptionIDModel: "test"},
		WorkingDir: t.TempDir(),
	}, noopSink{}, start, false)
	require.NoError(t, err, "StartAgent")

	sendDone := make(chan error, 1)
	go func() { sendDone <- m.SendInput(agentID, "a message that blocks", nil) }()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the provider write never started")
	}

	// The write is in flight. A lifecycle operation must still be able to take
	// the lock -- that is the whole difference between a slow send and a wedged
	// agent.
	locked := make(chan struct{})
	go func() {
		unlock := m.LockAgent(agentID)
		close(locked)
		unlock()
	}()
	select {
	case <-locked:
	case <-time.After(2 * time.Second):
		t.Fatal("a lifecycle operation blocked behind an in-flight send")
	}

	close(release)
	require.NoError(t, <-sendDone)
}

// blockingSendAgent parks in SendInput until it is released, so a test can hold
// a provider write open and inspect what the manager does meanwhile. Every
// other method is the wrapped mock's.
type blockingSendAgent struct {
	Agent
	entered chan struct{}
	release chan struct{}
}

func (a *blockingSendAgent) SendInput(string, []*leapmuxv1.Attachment) error {
	close(a.entered)
	<-a.release
	return nil
}

func TestManager_StopAll(t *testing.T) {
	m := NewManager(nil)
	ctx := context.Background()

	for _, id := range []string{"a", "b", "c"} {
		_, err := m.startAgentWith(ctx, Options{
			AgentID:    id,
			Options:    map[string]string{OptionIDModel: "test"},
			WorkingDir: t.TempDir(),
		}, noopSink{}, startMockAgent, false)
		require.NoError(t, err, "StartAgent(%s)", id)
	}

	m.StopAll()

	// Wait for background cleanup goroutines.
	for _, id := range []string{"a", "b", "c"} {
		id := id
		testutil.AssertEventually(t, func() bool {
			return !m.HasAgent(id)
		}, "HasAgent(%s) = true after StopAll", id)
	}
}

func TestManager_StopAndWaitAgent(t *testing.T) {
	m := NewManager(nil)
	ctx := context.Background()

	_, err := m.startAgentWith(ctx, Options{
		AgentID:    "s1",
		Options:    map[string]string{OptionIDModel: "test"},
		WorkingDir: t.TempDir(),
	}, noopSink{}, startMockAgent, false)
	require.NoError(t, err, "StartAgent")

	// StopAndWaitAgent should block until the agent is fully removed.
	assert.True(t, m.StopAndWaitAgent("s1"), "expected StopAndWaitAgent to return true")
	assert.False(t, m.HasAgent("s1"), "expected HasAgent(s1) = false immediately after StopAndWaitAgent")

	// A new agent with the same ID should start successfully.
	_, err = m.startAgentWith(ctx, Options{
		AgentID:    "s1",
		Options:    map[string]string{OptionIDModel: "test"},
		WorkingDir: t.TempDir(),
	}, noopSink{}, startMockAgent, false)
	require.NoError(t, err, "StartAgent after StopAndWaitAgent should succeed")
	m.StopAgent("s1")
}

func TestManager_StopAndWaitAgent_NotRunning(t *testing.T) {
	m := NewManager(nil)
	assert.False(t, m.StopAndWaitAgent("nonexistent"), "expected false for non-running agent")
}

// blockingStub is a stubProvider whose Wait blocks until waitCh is closed, so a test can
// control exactly when the exit goroutine runs its cleanup.
type blockingStub struct {
	stubProvider
	waitCh chan struct{}
}

func (b *blockingStub) Wait() error { <-b.waitCh; return nil }

func TestManager_ExitCallbackRunsBeforeSlotRelease(t *testing.T) {
	t.Parallel()

	m := NewManager(nil)
	registeredDuringExit := make(chan bool, 1)
	m.SetOnExit(func(agentID string, _ int, _ error, _ bool) {
		registeredDuringExit <- m.HasAgent(agentID)
	})
	provider := &blockingStub{waitCh: make(chan struct{})}
	_, err := m.startAgentWith(context.Background(), Options{
		AgentID: "exiting", WorkingDir: t.TempDir(),
	}, noopSink{}, func(context.Context, Options, OutputSink) (Agent, error) { return provider, nil }, false)
	require.NoError(t, err)

	close(provider.waitCh)
	select {
	case registered := <-registeredDuringExit:
		assert.True(t, registered, "the exit callback must pause the queue before the slot permits a restart")
	case <-time.After(2 * time.Second):
		t.Fatal("exit callback did not run")
	}
	require.Eventually(t, func() bool { return !m.HasAgent("exiting") }, time.Second, 10*time.Millisecond)
}

// TestManager_ExitGoroutineHonorsIdentityGuard verifies the stop-restart race fix: the old
// provider's background Wait goroutine, when it unblocks AFTER a new provider has taken the
// agent's slot (the restart case), must NOT delete the new provider's map entry or its
// cache. Without the identity check, the stale goroutine would orphan the just-restarted
// agent (SendInput -> ErrAgentNotFound) and wipe its cache.
func TestManager_ExitGoroutineHonorsIdentityGuard(t *testing.T) {
	m := NewManager(nil)
	exited := make(chan struct{})
	m.SetOnExit(func(string, int, error, bool) { close(exited) })

	// Provider A blocks in Wait until released; it is registered with a cache entry.
	old := &blockingStub{
		stubProvider: stubProvider{groups: []*leapmuxv1.AvailableOptionGroup{{Id: OptionIDModel, CurrentValue: "a"}}},
		waitCh:       make(chan struct{}),
	}
	_, err := m.startAgentWith(context.Background(), Options{
		AgentID:    "r",
		Options:    map[string]string{OptionIDModel: "a"},
		WorkingDir: t.TempDir(),
	}, noopSink{}, func(context.Context, Options, OutputSink) (Agent, error) { return old, nil }, false)
	require.NoError(t, err)
	require.True(t, m.HasAgent("r"))

	// Simulate a restart that already replaced A with a new provider B (and its cache)
	// while A's Wait goroutine is still blocked.
	newProvider := &blockingStub{
		stubProvider: stubProvider{groups: []*leapmuxv1.AvailableOptionGroup{{Id: OptionIDModel, CurrentValue: "b"}}},
		waitCh:       make(chan struct{}),
	}
	m.mu.Lock()
	m.agents["r"] = newProvider
	m.cachedOptionGroups["r"] = cachedCatalog{groups: newProvider.groups, model: "b"}
	m.mu.Unlock()

	// Release A; its goroutine fires onExit, then runs the identity-guarded delete.
	close(old.waitCh)
	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatal("exit goroutine did not run")
	}

	m.mu.RLock()
	got, ok := m.agents["r"]
	cached, cacheOk := m.cachedOptionGroups["r"]
	m.mu.RUnlock()
	assert.True(t, ok, "restarted provider B must survive the old provider's exit")
	assert.True(t, got == Agent(newProvider), "the slot must still hold B, not be deleted by A's stale goroutine")
	assert.True(t, cacheOk, "B's cache entry must survive A's exit")
	assert.Equal(t, "b", cached.model, "B's cache must not be clobbered by A's goroutine")

	close(newProvider.waitCh)
}

// stopSignalStub is a blockingStub that signals when Stop() is called. stopAndWait reads the
// manager maps (capturing the exit-done channel) BEFORE it calls Stop(), so a test can wait on
// `stopped` to know stopAndWait has captured the channel before releasing Wait -- making the
// stop/exit ordering deterministic instead of racing the exit goroutine's slot delete.
type stopSignalStub struct {
	blockingStub
	stopped chan struct{}
}

func (s *stopSignalStub) Stop() { close(s.stopped) }

// TestManager_StopAndWaitWaitsForOnExit is the regression guard for the onExit restart race:
// stopAndWait must not return until the exiting process's background goroutine -- including its
// onExit cleanup (ClearPendingControlRequests, which deletes by agent id alone) -- has fully
// finished. Otherwise a new provider registered right after a restart's stopAndWait could have
// its freshly-persisted control requests wiped by the old process's late onExit. Waiting makes
// the old process's teardown happen-before any new provider is registered.
func TestManager_StopAndWaitWaitsForOnExit(t *testing.T) {
	m := NewManager(nil)
	onExitStarted := make(chan struct{})
	releaseOnExit := make(chan struct{})
	m.SetOnExit(func(string, int, error, bool) {
		close(onExitStarted)
		<-releaseOnExit // hold onExit open so the test can observe stopAndWait still blocked
	})

	old := &stopSignalStub{
		blockingStub: blockingStub{waitCh: make(chan struct{})},
		stopped:      make(chan struct{}),
	}
	_, err := m.startAgentWith(context.Background(), Options{
		AgentID:    "w",
		Options:    map[string]string{OptionIDModel: "a"},
		WorkingDir: t.TempDir(),
	}, noopSink{}, func(context.Context, Options, OutputSink) (Agent, error) { return old, nil }, false)
	require.NoError(t, err)

	stopReturned := make(chan struct{})
	go func() {
		m.StopAndWaitAgent("w")
		close(stopReturned)
	}()

	// Wait until stopAndWait has captured the exit-done channel (it calls Stop() right after),
	// THEN release Wait so the exit goroutine can't delete the slot before stopAndWait sees it.
	<-old.stopped
	close(old.waitCh)

	// The exit goroutine reaches onExit and blocks there. stopAndWait MUST still be blocked --
	// it waits on the exit goroutine's done channel, which is closed only after onExit returns.
	<-onExitStarted
	select {
	case <-stopReturned:
		t.Fatal("stopAndWait returned while the exit goroutine's onExit was still running")
	case <-time.After(100 * time.Millisecond):
	}

	// Let onExit finish; stopAndWait then unblocks and returns.
	close(releaseOnExit)
	select {
	case <-stopReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("stopAndWait did not return after onExit completed")
	}
}

// TestManager_OptionGroupsForRow_SurfacesModelFromRow guards S5: a NOT-running dynamic-model ACP
// provider with no persisted catalog but a model on the row (a LEAPMUX_*_DEFAULT_MODEL override
// that never ran, so no model list was ever discovered) still surfaces a read-only model group, so
// the remote CLI's by-id model read reports the stored model instead of "".
func TestManager_OptionGroupsForRow_SurfacesModelFromRow(t *testing.T) {
	m := NewManager(nil)
	const opencode = leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE

	groups := m.OptionGroupsForRow("not-running", opencode, "anthropic/claude-sonnet-4", nil)

	mg := optionids.GroupByID(groups, OptionIDModel)
	require.NotNil(t, mg, "a model group is surfaced from the row's model even with no discovered catalog")
	assert.Equal(t, "anthropic/claude-sonnet-4", mg.GetCurrentValue())
	assert.False(t, mg.GetMutable(), "the synthesized model group is read-only (there is no selectable list)")

	// A no-op when no model is known: nothing to surface.
	none := m.OptionGroupsForRow("not-running-2", opencode, "", nil)
	assert.Nil(t, optionids.GroupByID(none, OptionIDModel), "no model group is invented when the row has no model")
}

// TestManager_OptionGroupsRefreshesCacheFromLive verifies the running cache is refreshed
// from the live catalog, so a model discovered after start (or a live setting change)
// survives a transiently EMPTY live read instead of falling back to the start-time
// (here: never-seeded) catalog.
func TestManager_OptionGroupsRefreshesCacheFromLive(t *testing.T) {
	m := NewManager(nil)
	const copilot = leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT

	// Provider starts with an EMPTY catalog, so StartAgent seeds no cache entry.
	p := &blockingStub{waitCh: make(chan struct{})}
	_, err := m.startAgentWith(context.Background(), Options{
		AgentID:    "c",
		Options:    map[string]string{OptionIDModel: "x"},
		WorkingDir: t.TempDir(),
	}, noopSink{}, func(context.Context, Options, OutputSink) (Agent, error) { return p, nil }, false)
	require.NoError(t, err)
	m.mu.RLock()
	_, seeded := m.cachedOptionGroups["c"]
	m.mu.RUnlock()
	require.False(t, seeded, "an empty start-time catalog seeds no cache entry")

	// The agent now reports a richer catalog (model + a server-driven config option).
	p.groups = []*leapmuxv1.AvailableOptionGroup{
		{Id: OptionIDModel, CurrentValue: "x", Options: []*leapmuxv1.AvailableOption{{Id: "x"}}},
		{Id: "reasoning_effort", Mutable: true, Options: []*leapmuxv1.AvailableOption{{Id: "high"}, {Id: "low"}}},
	}
	// A read while running returns the live catalog AND refreshes the cache.
	require.NotNil(t, optionids.GroupByID(m.OptionGroups("c", copilot, "x"), "reasoning_effort"))

	// The live catalog then goes transiently empty; the read must fall back to the
	// refreshed cache (carrying reasoning_effort), not a degenerate static fallback.
	p.groups = nil
	assert.NotNil(t, optionids.GroupByID(m.OptionGroups("c", copilot, "x"), "reasoning_effort"),
		"a transient empty live catalog serves the freshest cached catalog")

	close(p.waitCh)
}

func TestManager_LockAgent_ComposesStopAndStart(t *testing.T) {
	m := NewManager(nil)
	ctx := context.Background()

	_, err := m.startAgentWith(ctx, Options{AgentID: "r1", Options: map[string]string{OptionIDModel: "test"}, WorkingDir: t.TempDir()}, noopSink{}, startMockAgent, false)
	require.NoError(t, err)

	unlock := m.LockAgent("r1")
	m.stopAndWait("r1", false)
	_, err = m.startAgentWith(ctx, Options{AgentID: "r1", Options: map[string]string{OptionIDModel: "test"}, WorkingDir: t.TempDir()}, noopSink{}, startMockAgent, false)
	unlock()
	require.NoError(t, err, "restart composed under LockAgent should succeed")
	assert.True(t, m.HasAgent("r1"))
	m.StopAgent("r1")
}

func TestManager_LockAgent_SerializesConcurrentRestarts(t *testing.T) {
	m := NewManager(nil)
	ctx := context.Background()

	_, err := m.startAgentWith(ctx, Options{AgentID: "race", Options: map[string]string{OptionIDModel: "test"}, WorkingDir: t.TempDir()}, noopSink{}, startMockAgent, false)
	require.NoError(t, err)

	restart := func() error {
		unlock := m.LockAgent("race")
		defer unlock()
		m.stopAndWait("race", false)
		_, err := m.startAgentWith(ctx, Options{AgentID: "race", Options: map[string]string{OptionIDModel: "test"}, WorkingDir: t.TempDir()}, noopSink{}, startMockAgent, false)
		return err
	}

	errCh := make(chan error, 2)
	go func() { errCh <- restart() }()
	go func() { errCh <- restart() }()

	// Both must succeed: the lock serializes them so neither trips the
	// "agent already running" guard.
	assert.NoError(t, <-errCh)
	assert.NoError(t, <-errCh)
	m.StopAgent("race")
}

func TestManager_StopUnknownAgent(t *testing.T) {
	m := NewManager(nil)
	// Should not panic.
	m.StopAgent("nonexistent")
}

func TestManager_AgentExitCleanup(t *testing.T) {
	m := NewManager(nil)
	ctx := context.Background()

	// Start an agent that will exit on its own when stdin is closed.
	_, err := m.startAgentWith(ctx, Options{
		AgentID:    "auto-exit",
		Options:    map[string]string{OptionIDModel: "test"},
		WorkingDir: t.TempDir(),
	}, noopSink{}, func(ctx context.Context, opts Options, sink OutputSink) (Agent, error) {
		// Create a process that exits immediately.
		ctx2, cancel := context.WithCancel(ctx)
		cmd := exec.CommandContext(ctx2, "true")
		cmd.Dir = opts.WorkingDir

		stdin, _ := cmd.StdinPipe()
		stdout, _ := cmd.StdoutPipe()
		cmd.Stderr = nil

		a := &ClaudeCodeAgent{
			processBase: processBase{
				agentID:     opts.AgentID,
				cmd:         cmd,
				stdin:       stdin,
				ctx:         ctx2,
				cancel:      cancel,
				processDone: make(chan struct{}),
				stderrDone:  make(chan struct{}),
			},
			model:          opts.Model(),
			workingDir:     opts.WorkingDir,
			sink:           sink,
			pendingControl: make(map[string]chan<- claudeCodeControlResult),
		}
		close(a.stderrDone)

		if err := cmd.Start(); err != nil {
			cancel()
			return nil, err
		}

		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
		go a.readOutputLoop(scanner)
		return a, nil
	}, false)
	require.NoError(t, err, "StartAgent")

	// Wait for the process to exit and cleanup to happen.
	testutil.AssertEventually(t, func() bool {
		return !m.HasAgent("auto-exit")
	}, "expected agent to be cleaned up after exit")
}

func TestManager_AvailableOptionGroupsPrefersRuntimeGroups(t *testing.T) {
	m := NewManager(nil)
	runtimeGroups := []*leapmuxv1.AvailableOptionGroup{{
		Id:    OptionIDPrimaryAgent,
		Label: "Primary Agent",
		Options: []*leapmuxv1.AvailableOption{
			{Id: "build", Name: "build"},
			{Id: "architect", Name: "architect"},
		},
	}}

	m.mu.Lock()
	m.agents["runtime-agent"] = &stubProvider{groups: runtimeGroups}
	m.mu.Unlock()

	assert.Equal(t, runtimeGroups, m.OptionGroups("runtime-agent", leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, ""))

	staticGroups := m.OptionGroups("missing-agent", leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, "")
	require.Len(t, staticGroups, 1)
	assert.Equal(t, OptionIDPrimaryAgent, staticGroups[0].GetId())
	assert.Equal(t, OpenCodePrimaryAgentBuild, staticGroups[0].Options[0].Id)
}

func TestManager_CurrentSettings(t *testing.T) {
	m := NewManager(nil)

	assert.Nil(t, m.CurrentSettings("missing-agent").SurfacedOptions)

	// Registered agent → the provider's in-memory confirmed option values, letting
	// callers read back the effort the agent actually confirmed (e.g. an
	// ultracode request downgraded to xhigh) without a DB round-trip.
	m.mu.Lock()
	m.agents["running-agent"] = &stubProvider{groups: []*leapmuxv1.AvailableOptionGroup{
		{Id: OptionIDEffort, CurrentValue: "xhigh"},
	}}
	m.mu.Unlock()

	got := m.CurrentSettings("running-agent")
	require.NotNil(t, got.SurfacedOptions)
	assert.Equal(t, "xhigh", got.SurfacedOptions[OptionIDEffort])
	assert.Equal(t, OptionSettlementConfirmed, got.Settlements[OptionIDEffort].State)
}

func TestManager_PreloadCache(t *testing.T) {
	m := NewManager(nil)

	groups := []*leapmuxv1.AvailableOptionGroup{
		{Id: "thinkingBudget", Label: "Thinking Budget", Options: []*leapmuxv1.AvailableOption{
			{Id: "low", Name: "Low"},
			{Id: "high", Name: "High"},
		}},
	}

	// Preload cache for a non-running agent.
	m.PreloadCache("preloaded-agent", groups)

	// OptionGroups should return preloaded groups (not static defaults).
	gotGroups := m.OptionGroups("preloaded-agent", leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR, "")
	require.Len(t, gotGroups, 1)
	assert.Equal(t, "thinkingBudget", gotGroups[0].GetId())
	assert.Len(t, gotGroups[0].GetOptions(), 2)
}

// TestManager_OptionGroupsForRowUsesRowSnapshotNotSharedCache guards S4: for a not-running agent
// the row is authoritative (the cache entry was dropped on exit and is only ever re-seeded from
// per-caller row snapshots), so OptionGroupsForRow builds from the CALLER'S snapshot rather than
// the shared cache. A stale catalog a concurrent reader's older snapshot left in the shared cache
// must NOT be served in place of the caller's fresher one.
func TestManager_OptionGroupsForRowUsesRowSnapshotNotSharedCache(t *testing.T) {
	m := NewManager(nil)
	cursor := leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR // no model-dependent groups: cache served as-is

	stale := []*leapmuxv1.AvailableOptionGroup{
		{Id: "thinkingBudget", Label: "Thinking Budget", Options: []*leapmuxv1.AvailableOption{{Id: "low", Name: "Low"}}},
	}
	fresh := []*leapmuxv1.AvailableOptionGroup{
		{Id: "thinkingBudget", Label: "Thinking Budget", Options: []*leapmuxv1.AvailableOption{
			{Id: "low", Name: "Low"}, {Id: "high", Name: "High"},
		}},
	}

	// A concurrent reader's OLDER snapshot lands in the shared cache.
	m.PreloadCache("a", stale)

	// This reader holds the FRESHER row snapshot; OptionGroupsForRow must serve it, not the stale cache.
	got := m.OptionGroupsForRow("a", cursor, "", fresh)
	budget := optionids.GroupByID(got, "thinkingBudget")
	require.NotNil(t, budget)
	assert.Len(t, budget.GetOptions(), 2, "the caller's fresh snapshot is served, not the stale shared cache")
}

func TestManager_PreloadCacheSkipsEmpty(t *testing.T) {
	m := NewManager(nil)

	// Preload with nil slice — should not populate cache.
	m.PreloadCache("empty-agent", nil)

	// Should fall back to static defaults: the Cursor model group's default is "auto".
	groups := m.OptionGroups("empty-agent", leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR, "")
	require.NotEmpty(t, groups)
	modelGroup := optionids.GroupByID(groups, OptionIDModel)
	require.NotNil(t, modelGroup)
	assert.Equal(t, "auto", modelGroup.GetOptions()[0].GetId(), "should fall back to static Cursor defaults")
}

// TestManager_PreloadCacheSkipsRunningAgent is the regression guard for [A11]: a running
// (or concurrently-starting) agent owns its cache -- seeded model-correct by StartAgent and
// refreshed in OptionGroups -- so PreloadCache must NOT overwrite it with the persisted-row
// snapshot. optionGroupsView gates on HasAgent first, but that check and the write aren't
// atomic, so a StartAgent landing in between would otherwise be reverted to the stale stamp.
func TestManager_PreloadCacheSkipsRunningAgent(t *testing.T) {
	m := NewManager(nil)
	// Seed a fresh, model-correct cache as StartAgent would, then register the running agent.
	live := []*leapmuxv1.AvailableOptionGroup{
		{Id: OptionIDModel, Label: "Model", CurrentValue: "new-model", Options: []*leapmuxv1.AvailableOption{{Id: "new-model"}}},
	}
	m.cachedOptionGroups["live"] = cachedCatalog{groups: live, model: "new-model"}
	m.agents["live"] = &stubProvider{groups: live}

	// A racing PreloadCache with the stale persisted snapshot must be a no-op.
	persisted := []*leapmuxv1.AvailableOptionGroup{
		{Id: OptionIDModel, Label: "Model", CurrentValue: "old-model", Options: []*leapmuxv1.AvailableOption{{Id: "old-model"}}},
	}
	m.PreloadCache("live", persisted)

	assert.Equal(t, "new-model", m.cachedOptionGroups["live"].model,
		"PreloadCache must not clobber a running agent's cache with the stale persisted stamp")
}

func TestManager_AvailableOptionGroupsCachedFallback(t *testing.T) {
	m := NewManager(nil)

	cachedGroups := []*leapmuxv1.AvailableOptionGroup{{
		Id:    "thinkingBudget",
		Label: "Thinking Budget",
		Options: []*leapmuxv1.AvailableOption{
			{Id: "low", Name: "Low"},
		},
	}}

	m.mu.Lock()
	m.cachedOptionGroups["cached-agent"] = cachedCatalog{groups: cachedGroups}
	m.mu.Unlock()

	// Agent is not running — should return cached groups, not static defaults.
	got := m.OptionGroups("cached-agent", leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, "")
	require.Len(t, got, 1)
	assert.Equal(t, "thinkingBudget", got[0].GetId())
}

// TestManager_CachedCatalogServedByModelStamp verifies how the cache is served relative to the
// requested model for a model-dependent provider (Claude): a matching or unknown model serves
// the cache verbatim, while a since-changed model (an offline edit that rewrote options.model
// but not the persisted catalog) REBUILDS the per-model groups for the new model yet PRESERVES
// any model-INDEPENDENT discovered group (e.g. Output Style) instead of dropping it to the bare
// static fallback.
func TestManager_CachedCatalogServedByModelStamp(t *testing.T) {
	m := NewManager(nil)
	// The model group's current value ("sonnet") stamps the cache; outputStyle is a model-
	// INDEPENDENT cache-only group the Claude static fallback never reproduces (it is surfaced
	// only at runtime from availableOutputStyles).
	groups := []*leapmuxv1.AvailableOptionGroup{
		{Id: OptionIDModel, Label: "Model", CurrentValue: "sonnet", Options: []*leapmuxv1.AvailableOption{{Id: "sonnet"}, {Id: "haiku"}}},
		{Id: "outputStyle", Label: "Output Style", Options: []*leapmuxv1.AvailableOption{{Id: "default"}}},
	}
	m.PreloadCache("a1", groups)

	const claude = leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
	// Requested model matches the stamp -> cache served verbatim (cached model group + outputStyle).
	matched := m.OptionGroups("a1", claude, "sonnet")
	assert.Equal(t, "sonnet", optionids.GroupByID(matched, OptionIDModel).GetCurrentValue(),
		"a matching model serves the cached model group verbatim")
	assert.NotNil(t, optionids.GroupByID(matched, "outputStyle"), "a matching model serves the cached catalog")
	// Unknown requested model -> trust the cache as-is.
	assert.NotNil(t, optionids.GroupByID(m.OptionGroups("a1", claude, ""), "outputStyle"),
		"an unknown model trusts the cache")
	// Requested model differs from the stamp -> the per-model groups are rebuilt for the new
	// model (the model group loses its stale cached current), but the model-independent discovered
	// group survives instead of being dropped.
	changed := m.OptionGroups("a1", claude, "haiku")
	assert.Equal(t, "", optionids.GroupByID(changed, OptionIDModel).GetCurrentValue(),
		"a since-changed model rebuilds the model group (no stale cached current)")
	assert.NotNil(t, optionids.GroupByID(changed, "outputStyle"),
		"a model-independent discovered group survives a model edit instead of being dropped")
}

// TestManager_CachedGenericGroupsSurviveModelChangeForACPProvider verifies the C19 fix:
// a provider with no model-dependent groups (the ACP permission-mode / primary-agent
// providers, whose reasoning/effort axes are model-independent server-driven config options)
// keeps serving its cached catalog across a since-changed model, instead of falling
// through to a degenerate static fallback that would drop the option groups. The Claude
// model-stamp fall-through (above) must NOT apply here.
func TestManager_CachedGenericGroupsSurviveModelChangeForACPProvider(t *testing.T) {
	m := NewManager(nil)
	const copilot = leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT
	require.False(t, providerHasModelDependentGroups(copilot),
		"precondition: Copilot has no model-dependent groups")

	// The running agent reported a model group (stamps the cache at "gpt-5") plus a
	// server-driven reasoning_effort config option that the Copilot static fallback never
	// reproduces (its static groups are only the permission-mode "Mode" group).
	groups := []*leapmuxv1.AvailableOptionGroup{
		{Id: OptionIDModel, Label: "Model", CurrentValue: "gpt-5", Options: []*leapmuxv1.AvailableOption{{Id: "gpt-5"}, {Id: "gpt-4"}}},
		{Id: "reasoning_effort", Label: "Reasoning Effort", Mutable: true, Options: []*leapmuxv1.AvailableOption{{Id: "high"}, {Id: "low"}}},
	}
	m.PreloadCache("a1", groups)

	// An offline model edit changes the requested model away from the stamp. The cache
	// (with reasoning_effort) must still be served -- the model-independent option
	// can't be rebuilt from the static fallback.
	assert.NotNil(t, optionids.GroupByID(m.OptionGroups("a1", copilot, "gpt-4"), "reasoning_effort"),
		"a since-changed model still serves the cached option group for a provider with no model-dependent groups")
}

// TestManager_UnstampedCacheRebuiltForModelDependentProvider verifies the S1 fix: an
// UNSTAMPED cache (model current == "") for a model-dependent provider is treated as stale-by-
// model when the requested model is known -- the per-model groups are rebuilt for that model
// rather than served from the cache (whose dependent groups were built for some other / default
// model) -- while a model-INDEPENDENT discovered group (Output Style) is preserved. An unknown
// requested model still trusts the cache. (A non-model-dependent ACP provider keeps serving its
// cache wholesale; see the test above.)
func TestManager_UnstampedCacheRebuiltForModelDependentProvider(t *testing.T) {
	m := NewManager(nil)
	const claude = leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
	require.True(t, providerHasModelDependentGroups(claude), "precondition: Claude is model-dependent")

	// An unstamped cache: the model group carries NO current value (model == "" stamp), plus a
	// model-independent cache-only group the Claude static fallback never reproduces.
	groups := []*leapmuxv1.AvailableOptionGroup{
		{Id: OptionIDModel, Label: "Model", Options: []*leapmuxv1.AvailableOption{{Id: "sonnet"}, {Id: "haiku"}}},
		{Id: "outputStyle", Label: "Output Style", Options: []*leapmuxv1.AvailableOption{{Id: "default"}}},
	}
	m.PreloadCache("a1", groups)
	require.Equal(t, "", m.cachedOptionGroups["a1"].model, "precondition: the cache is unstamped")

	// A known requested model rebuilds the per-model groups but PRESERVES the discovered group.
	rebuilt := m.OptionGroups("a1", claude, "haiku")
	assert.NotNil(t, optionids.GroupByID(rebuilt, "outputStyle"),
		"a model-independent discovered group survives even when the per-model groups are rebuilt")
	// The model group is rebuilt from the real Claude catalog, not served from the 2-option
	// cached stub -- proving the stale per-model groups were not served.
	assert.Greater(t, len(optionids.GroupByID(rebuilt, OptionIDModel).GetOptions()), 2,
		"the per-model groups are rebuilt from the static catalog, not served from the stale cache")
	// An unknown requested model still trusts the cache (can't tell it's stale).
	assert.NotNil(t, optionids.GroupByID(m.OptionGroups("a1", claude, ""), "outputStyle"),
		"an unknown requested model trusts the unstamped cache")
}

func TestManager_AvailableModelsFallsBackToCursorDefaults(t *testing.T) {
	m := NewManager(nil)

	groups := m.OptionGroups("missing-agent", leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR, "")
	modelGroup := optionids.GroupByID(groups, OptionIDModel)
	require.NotNil(t, modelGroup)
	require.NotEmpty(t, modelGroup.GetOptions())
	assert.Equal(t, "auto", modelGroup.GetOptions()[0].GetId())
	assert.Equal(t, "auto", modelGroup.GetDefaultValue())
}

// TestModelGroupDefault_PreservesACPCurrentModel verifies that for a provider with
// no configured default (registered with nil defaultModels, like the ACP providers
// that self-mark the currently-selected model in buildACPModels), the ladder leaves
// the per-agent default in place instead of promoting the first entry. Regression
// guard: defaultModelIDForList must return "" rather than falling through to "mark
// the first entry" when there is no configured default to anchor on.
func TestModelGroupDefault_PreservesACPCurrentModel(t *testing.T) {
	t.Setenv("LEAPMUX_OPENCODE_DEFAULT_MODEL", "") // hermetic: ignore any ambient override
	opencode := leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE
	require.Empty(t, DefaultModel(opencode), "precondition: opencode has no configured default")

	// buildACPModels marks the active model; here that's the 2nd entry.
	models := []*ModelInfo{
		{Id: "anthropic/claude-x", DisplayName: "Claude X"},
		{Id: "openai/gpt-y", DisplayName: "GPT Y", IsDefault: true},
		{Id: "xai/grok-z", DisplayName: "Grok Z"},
	}

	assert.Equal(t, "openai/gpt-y", derivedModelDefault(t, models, opencode),
		"the current model keeps the badge; the first entry must not be promoted")

	// An operator override still wins and moves the badge to the override target.
	t.Setenv("LEAPMUX_OPENCODE_DEFAULT_MODEL", "xai/grok-z")
	assert.Equal(t, "xai/grok-z", derivedModelDefault(t, models, opencode),
		"the override clears the per-agent badge and marks its target")
}

// TestModelGroupDefault_ToleratesNilAndHiddenEntries covers the reduction
// withModelGroupDefaultMarked performs on a catalog carrying nil and hidden
// entries: modelOptionGroup drops them, so "the highest-preference entry present"
// is the first VISIBLE model, and a nil-bearing catalog must not panic.
func TestModelGroupDefault_ToleratesNilAndHiddenEntries(t *testing.T) {
	t.Setenv("LEAPMUX_CODEX_DEFAULT_MODEL", "")
	codex := leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX
	require.NotEmpty(t, DefaultModel(codex), "precondition: codex has a configured default")

	// No entry designates a default and the configured default is absent, so the
	// ladder falls back to the first entry the group actually carries.
	assert.Equal(t, "visible-a", derivedModelDefault(t, []*ModelInfo{
		nil,
		{Id: "hidden-one", Hidden: true},
		{Id: "visible-a"},
		nil,
		{Id: "visible-b"},
	}, codex), "nil and hidden entries are skipped when picking the fallback")

	// A designated default deeper in the list still wins over that fallback.
	assert.Equal(t, "visible-b", derivedModelDefault(t, []*ModelInfo{
		nil,
		{Id: "visible-a"},
		{Id: "visible-b", IsDefault: true},
	}, codex), "the designated default outranks the first-entry fallback")
}

// TestModelGroupDefault_SentinelIsClaudeOnly verifies that only Claude Code treats
// an entry with the id "default" as the sentinel and prefers it. For every other
// provider "default" is an ordinary model id, so the self-marked current model
// must keep the badge.
func TestModelGroupDefault_SentinelIsClaudeOnly(t *testing.T) {
	t.Setenv("LEAPMUX_OPENCODE_DEFAULT_MODEL", "") // hermetic: ignore any ambient override
	t.Setenv("LEAPMUX_CLAUDE_DEFAULT_MODEL", "")
	opencode := leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE
	require.Empty(t, DefaultModel(opencode), "precondition: opencode has no configured default")

	// An ACP list containing a model id'd "default" with a DIFFERENT self-marked
	// current model. The self-marked model must keep the badge.
	models := []*ModelInfo{
		{Id: "default", DisplayName: "Some Local Default Model"},
		{Id: "openai/gpt-y", DisplayName: "GPT Y", IsDefault: true},
	}
	assert.Equal(t, "openai/gpt-y", derivedModelDefault(t, models, opencode),
		"a non-Claude 'default'-id model is not the sentinel; the self-marked current model keeps the badge")

	// Same list under Claude Code: the sentinel rule DOES apply, so the "default"
	// entry is badged.
	claude := leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
	assert.Equal(t, DefaultModelSentinel, derivedModelDefault(t, models, claude),
		"Claude Code badges the 'default' sentinel entry")
}

// TestModelGroupDefault_PreservesProviderDefaultWhenConfiguredAbsent verifies that
// for a provider WITH a configured default (e.g. Codex), when that configured
// default is absent from an account-specific list, the ladder respects a default the
// provider already designated on the list itself (Codex's queryAvailableModels
// copies the CLI's isDefault) rather than promoting the first entry. Regression
// guard: the step-3 fallback must not move the badge off the model the CLI marked
// just because the registry default isn't offered.
func TestModelGroupDefault_PreservesProviderDefaultWhenConfiguredAbsent(t *testing.T) {
	t.Setenv("LEAPMUX_CODEX_DEFAULT_MODEL", "") // hermetic: ignore any ambient override
	codex := leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX
	configured := DefaultModel(codex)
	require.NotEmpty(t, configured, "precondition: codex has a configured default")

	// An account-specific list that does NOT contain the configured default, with
	// the CLI's own default marked on the 2nd (non-first) entry.
	models := []*ModelInfo{
		{Id: "codex-mini", DisplayName: "Mini"},
		{Id: "codex-pro", DisplayName: "Pro", IsDefault: true},
	}
	require.Nil(t, FindAvailableModel(models, configured), "precondition: configured default absent from list")

	assert.Equal(t, "codex-pro", derivedModelDefault(t, models, codex),
		"the provider-marked default keeps its badge; the first entry must not be promoted")

	// Sanity: with NO entry pre-marked, the badge still falls back to the first
	// entry so the picker always shows a default.
	unmarked := []*ModelInfo{{Id: "codex-mini"}, {Id: "codex-pro"}}
	assert.Equal(t, "codex-mini", derivedModelDefault(t, unmarked, codex),
		"no designated default -> first entry marked")
}

// TestModelGroupDefault_EnvOverrideAbsentFallsThrough verifies that an operator
// default-model override pointing at a model the (account-specific) list does not
// contain -- or naming it with a different spelling than the catalog stores -- does
// NOT leave the group badging nothing. defaultModelIDForList honors the override only
// when it resolves to a model in the list (by exact id or provider-normalized alias);
// otherwise it falls through the ladder so the picker still shows a default.
func TestModelGroupDefault_EnvOverrideAbsentFallsThrough(t *testing.T) {
	claude := leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE

	// A Claude list with the account-default sentinel present.
	models := []*ModelInfo{
		{Id: DefaultModelSentinel, DisplayName: "Default (recommended)", IsDefault: true},
		{Id: "sonnet", DisplayName: "Sonnet"},
	}

	// Override names a model the account does not offer -> falls through to the
	// sentinel (Claude's list-designated default); the badge is preserved.
	t.Setenv("LEAPMUX_CLAUDE_DEFAULT_MODEL", "opus[1m]")
	assert.Equal(t, DefaultModelSentinel, derivedModelDefault(t, models, claude),
		"an absent override falls through to the sentinel rather than badging nothing")

	// Override names a PRESENT model with a fully-qualified spelling: it resolves to
	// the catalog's normalized alias and wins, moving the badge off the sentinel.
	present := []*ModelInfo{
		{Id: DefaultModelSentinel, DisplayName: "Default (recommended)", IsDefault: true},
		{Id: "opus[1m]", DisplayName: "Opus (1M context)"},
	}
	t.Setenv("LEAPMUX_CLAUDE_DEFAULT_MODEL", "claude-opus-4-8[1m]")
	assert.Equal(t, "opus[1m]", derivedModelDefault(t, present, claude),
		"a fully-qualified override matches the normalized opus[1m] and takes the badge off the sentinel")
}

// TestWithModelGroupDefaultMarked_ReDerivesProtoModelGroupDefault is the [V26] guard for the
// proto-shape default-marking path run on every OptionGroups read: it re-derives the model group's
// DefaultValue via the defaultModelIDForList ladder, straight off the group's own options, and
// leaves non-model groups untouched by reference.
func TestWithModelGroupDefaultMarked_ReDerivesProtoModelGroupDefault(t *testing.T) {
	claude := leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
	other := selectGroup("fastMode", "Fast Mode", OptionOrderProviderSecond, "off", []optDef{
		{Id: "on", Name: "On"}, {Id: "off", Name: "Off", Default: true},
	})
	// A fully-qualified operator override resolves to the normalized catalog alias opus[1m].
	t.Setenv("LEAPMUX_CLAUDE_DEFAULT_MODEL", "claude-opus-4-8[1m]")

	// (1) Re-derivation: the model group's default moves off the sentinel onto opus[1m]; the
	// non-model group is returned by the same reference (only the model group is re-cloned).
	stale := []*leapmuxv1.AvailableOptionGroup{
		selectGroup(OptionIDModel, "Model", OptionOrderModel, DefaultModelSentinel, []optDef{
			{Id: DefaultModelSentinel, Name: "Default (recommended)", Default: true},
			{Id: "opus[1m]", Name: "Opus (1M context)"},
		}),
		other,
	}
	got := withModelGroupDefaultMarked(stale, claude)
	require.Len(t, got, 2)
	assert.Equal(t, "opus[1m]", optionids.GroupByID(got, OptionIDModel).GetDefaultValue(),
		"the model group's default is re-derived to the override's normalized id")
	assert.Same(t, other, got[1], "a non-model group is returned by the same reference, untouched")

	// (2) Already-correct fast path: when the model group's default already matches the derived
	// id, the input is returned unchanged (the model group is not re-cloned).
	correct := []*leapmuxv1.AvailableOptionGroup{
		selectGroup(OptionIDModel, "Model", OptionOrderModel, "opus[1m]", []optDef{
			{Id: DefaultModelSentinel, Name: "Default (recommended)"},
			{Id: "opus[1m]", Name: "Opus (1M context)", Default: true},
		}),
		other,
	}
	result := withModelGroupDefaultMarked(correct, claude)
	assert.Same(t, correct[0], result[0], "an already-correct default returns the input unchanged, not a re-clone")
}

// TestModelGroupDefault_ClaudeNoSentinelFallsBackToFirst covers the documented
// Claude branch of defaultModelIDForList step 3: a Claude CLI reporting concrete
// models but NO "default" sentinel (and no operator override) falls back to badging
// the first model so the picker still shows a default.
func TestModelGroupDefault_ClaudeNoSentinelFallsBackToFirst(t *testing.T) {
	t.Setenv("LEAPMUX_CLAUDE_DEFAULT_MODEL", "")
	claude := leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
	require.Equal(t, DefaultModelSentinel, DefaultModel(claude), "precondition: Claude's configured default is the sentinel")

	models := []*ModelInfo{
		{Id: "opus", DisplayName: "Opus"},
		{Id: "sonnet", DisplayName: "Sonnet"},
	}
	require.Nil(t, FindAvailableModel(models, DefaultModelSentinel), "precondition: no sentinel in the list")

	assert.Equal(t, "opus", derivedModelDefault(t, models, claude),
		"no sentinel -> first concrete model badged")
}

// TestModelGroupDefault_EmptyAndAllNilCatalogs covers the degenerate catalogs the
// live path must survive: modelOptionGroup drops nil entries, so an all-nil (or
// empty) catalog projects to NO model group at all, and withModelGroupDefaultMarked
// must hand such a group set back untouched rather than deref a missing group.
func TestModelGroupDefault_EmptyAndAllNilCatalogs(t *testing.T) {
	claude := leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE

	assert.Nil(t, modelOptionGroup(nil, "", nil), "an empty catalog projects to no model group")
	assert.Nil(t, modelOptionGroup([]*ModelInfo{nil, nil}, "", nil),
		"an all-nil catalog projects to no model group, no panic")

	other := selectGroup("fastMode", "Fast Mode", OptionOrderProviderSecond, "off", []optDef{
		{Id: "on", Name: "On"}, {Id: "off", Name: "Off", Default: true},
	})
	groups := []*leapmuxv1.AvailableOptionGroup{other}
	got := withModelGroupDefaultMarked(groups, claude)
	require.Len(t, got, 1)
	assert.Same(t, other, got[0], "no model group -> the input is returned untouched")
}

// TestReportsDefaultModelSentinel pins the per-provider answer that
// defaultModelIDForList dispatches on. Only Claude Code's own catalog lists the
// sentinel as a selectable entry, so only Claude gives it the default badge; for
// every other provider "default" is an ordinary model id.
func TestReportsDefaultModelSentinel(t *testing.T) {
	t.Parallel()

	assert.True(t, claudeProvider{}.ReportsDefaultModelSentinel(), "the Claude CLI reports a \"default\" entry itself")
	assert.False(t, codexProvider{}.ReportsDefaultModelSentinel(), "Codex resolves the sentinel to a concrete model at startup")
	assert.False(t, zcodeProvider{}.ReportsDefaultModelSentinel(), "ZCode names every model explicitly")
	assert.False(t, noopProvider{}.ReportsDefaultModelSentinel(), "a provider must opt in, not inherit Claude's answer")

	// The dispatcher must agree with the plugin for every registered provider, so a
	// new provider cannot be added to the enum and miss the decision.
	for _, provider := range []leapmuxv1.AgentProvider{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
	} {
		want := provider == leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
		assert.Equal(t, want, ProviderFor(provider).ReportsDefaultModelSentinel(), "provider %s", provider)
	}
}

// TestModelGroupDefault_CodexBadgesTheSentinelOnceReconciled covers the badge for
// a RUNNING Codex agent. reconcileModelCatalog puts the account-default row back
// into the live catalog that model/list omits, and DefaultModel(CODEX) is that
// same sentinel, so the ladder badges it at the configured-default step -- ahead
// of the concrete model the CLI marked isDefault. Before the reconciliation the
// sentinel was absent from a live list and the badge landed on that concrete
// model instead, so this pins which of the two the picker highlights.
func TestModelGroupDefault_CodexBadgesTheSentinelOnceReconciled(t *testing.T) {
	t.Setenv("LEAPMUX_CODEX_DEFAULT_MODEL", "") // hermetic: ignore any ambient override
	codex := leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX
	require.Equal(t, DefaultModelSentinel, DefaultModel(codex), "precondition: Codex's configured default is the sentinel")

	reconciled := []*ModelInfo{
		accountDefaultModelEntry("Use the account's default Codex model"),
		{Id: "gpt-5.6-sol", DisplayName: "GPT-5.6-Sol", IsDefault: true},
		{Id: "gpt-5.4", DisplayName: "GPT-5.4"},
	}
	assert.Equal(t, DefaultModelSentinel, derivedModelDefault(t, reconciled, codex),
		"the account default carries the badge once the sentinel is listed")

	// A live list the reconciliation never touched (it no-ops on an empty catalog,
	// and an older persisted catalog can lack the row) still badges the model the
	// CLI marked, rather than badging nothing.
	rawLive := []*ModelInfo{
		{Id: "gpt-5.6-sol", DisplayName: "GPT-5.6-Sol", IsDefault: true},
		{Id: "gpt-5.4", DisplayName: "GPT-5.4"},
	}
	assert.Equal(t, "gpt-5.6-sol", derivedModelDefault(t, rawLive, codex),
		"with no sentinel listed the CLI's own default keeps the badge")
}
