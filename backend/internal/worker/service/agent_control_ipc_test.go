package service

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// agentIPCRecorder mints a distinct token per agent spawn and records the order
// of mints and cleanups, so a test can pin that a relaunch retires the previous
// spawn's token before it takes ownership of the new one.
type agentIPCRecorder struct {
	mu sync.Mutex
	// events is the interleaved log: "mint:<token>" and "cleanup:<token>".
	events []string
	mints  int
	owners []userid.UserID
	// failWith, when non-nil, is returned instead of minting.
	failWith error
	// delegationRefs is the live per-user delegation reference count, and zeroed
	// counts how often it reached zero. The real store revokes the hub token at
	// zero, so a relaunch that lets it get there costs two round trips.
	delegationRefs int
	zeroed         int
}

func (f *agentIPCRecorder) AgentSpawning(info AgentSpawnInfo) ([]string, func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.owners = append(f.owners, info.UserID)
	envs, cleanup, err := f.mintLocked(info.TabID)
	if err != nil {
		return nil, nil, err
	}
	return append(envs, "LEAPMUX_CONTROL_AGENT_PROVIDER="+info.AgentProvider), cleanup, nil
}

// TerminalSpawning records through the same log as AgentSpawning, so a test can
// pin the terminal relaunch's swap order the way it pins the agent's. The two
// share one helper (remintControlIPC), so a fake that recorded only one kind
// would leave the other's order unpinned.
func (f *agentIPCRecorder) TerminalSpawning(info TerminalSpawnInfo) ([]string, func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mintLocked(info.TabID)
}

// mintLocked issues the next token and logs it. Caller holds f.mu.
func (f *agentIPCRecorder) mintLocked(tabID string) ([]string, func(), error) {
	if f.failWith != nil {
		return nil, nil, f.failWith
	}
	f.mints++
	// A spawn takes a delegation reference, like the real factory, and its own
	// cleanup gives it back.
	f.delegationRefs++
	token := fmt.Sprintf("token-%d", f.mints)
	f.events = append(f.events, "mint:"+token)
	// Both values are named rather than returned inline. gofmt indents a
	// multi-line composite literal inside a return LIST by one extra level up
	// to Go 1.26 and by none from Go 1.27, so the inline form is formatted two
	// ways and fails the linter on whichever toolchain did not write it.
	env := []string{
		"LEAPMUX_CONTROL_TAB_ID=" + tabID,
		"LEAPMUX_CONTROL_TOKEN=" + token,
	}
	cleanup := func() {
		f.mu.Lock()
		f.events = append(f.events, "cleanup:"+token)
		f.mu.Unlock()
		f.releaseDelegation()
	}
	return env, cleanup, nil
}

// HoldDelegation models the per-user delegation reference. delegationRefs is the
// live count and zeroed records whether it ever reached zero, which is the fact
// a relaunch must avoid: at zero the real store revokes the hub token through a
// blocking call, and the next call from the tab pays a second round trip.
func (f *agentIPCRecorder) HoldDelegation(userid.UserID) func() {
	f.mu.Lock()
	f.delegationRefs++
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.delegationRefs--
		if f.delegationRefs == 0 {
			f.zeroed++
		}
	}
}

// releaseDelegation is what a spawn's own cleanup does to the same count.
func (f *agentIPCRecorder) releaseDelegation() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.delegationRefs--
	if f.delegationRefs == 0 {
		f.zeroed++
	}
}

func (f *agentIPCRecorder) log() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.events...)
}

func (f *agentIPCRecorder) spawnOwners() []userid.UserID {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]userid.UserID(nil), f.owners...)
}

// exclusiveSocketIPC models locallisten's real refusal: one listener at a time
// per tab. A mint issued while the previous cleanup did not run yet answers an
// error, the way `unix listen: ... is already in use` does.
//
// It binds by TAB, not globally, because DefaultSocketPath is a pure function of
// (worker, kind, tab): two different tabs never collide, and two spawns of one
// tab always do. Agents and terminals share the rule, so both kinds route
// through the same bind.
type exclusiveSocketIPC struct {
	mu    sync.Mutex
	mints int
	bound map[string]bool
}

func (f *exclusiveSocketIPC) bind(tabID string) ([]string, func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.bound[tabID] {
		return nil, nil, fmt.Errorf("socket for %s is already in use", tabID)
	}
	if f.bound == nil {
		f.bound = map[string]bool{}
	}
	f.mints++
	f.bound[tabID] = true
	token := fmt.Sprintf("token-%d", f.mints)
	return []string{"LEAPMUX_CONTROL_TOKEN=" + token}, func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		delete(f.bound, tabID)
	}, nil
}

func (f *exclusiveSocketIPC) AgentSpawning(info AgentSpawnInfo) ([]string, func(), error) {
	return f.bind(info.TabID)
}

func (f *exclusiveSocketIPC) TerminalSpawning(info TerminalSpawnInfo) ([]string, func(), error) {
	return f.bind(info.TabID)
}

func (f *exclusiveSocketIPC) HoldDelegation(userid.UserID) func() { return func() {} }

// TestEnsureAgentRunning_MintsAControlSocket is the guard for the capability
// this whole feature exists to restore. A relaunched agent that carries no
// LEAPMUX_CONTROL_* cannot run `leapmux control agent list` or `agent send`, so
// it can neither see nor reach the other agents on the machine -- and the boot
// time resume sweep would put every agent on the worker in that state.
func TestEnsureAgentRunning_MintsAControlSocket(t *testing.T) {
	t.Parallel()

	ipc := &agentIPCRecorder{}
	svc, _, _ := setupTestService(t, withRemoteIPC(ipc))
	rec := newStartRecorder()
	rec.install(svc)
	seedOpenAgent(t, svc, "agent-1", true)

	require.NoError(t, svc.ensureAgentRunning("agent-1", nil, interactiveStart))

	env := rec.envFor("agent-1")
	assert.Contains(t, env, "LEAPMUX_CONTROL_TAB_ID=agent-1")
	assert.Contains(t, env, "LEAPMUX_CONTROL_TOKEN=token-1")
	assert.Contains(t, env, "LEAPMUX_CONTROL_AGENT_PROVIDER=claude-code",
		"the CLI alias lets a child `tab open` default to the same provider")
}

// TestEnsureAgentRunning_MintsForTheWorkerOwner pins where the identity comes
// from on a path with no caller. Every agent handler is registered ownerOnly,
// so the registrant IS the user the open path names; a relaunch that guessed
// anything else would hand the agent a bearer for the wrong account.
func TestEnsureAgentRunning_MintsForTheWorkerOwner(t *testing.T) {
	t.Parallel()

	ipc := &agentIPCRecorder{}
	svc, _, _ := setupTestService(t, withRemoteIPC(ipc))
	newStartRecorder().install(svc)
	seedOpenAgent(t, svc, "agent-1", true)

	require.NoError(t, svc.ensureAgentRunning("agent-1", nil, interactiveStart))

	owners := ipc.spawnOwners()
	require.Len(t, owners, 1)
	assert.True(t, owners[0].Matches("user-1"), "the spawn must be scoped to the worker's registrant")
}

// TestEnsureAgentRunning_RetiresTheOldTokenBeforeMinting pins the swap ORDER,
// which is the whole reason this helper exists.
//
// controlipc.DefaultSocketPath is a pure function of (worker, kind, tab), so a
// relaunch's socket path is the one the previous spawn is still listening on,
// and locallisten refuses a path whose socket answers a dial. Minting first
// therefore failed every in-process relaunch DEGRADABLY: the tab came up with
// no LEAPMUX_CONTROL_* and `leapmux control` inside it reported "socket not
// configured" -- exactly the failure this whole change set out to remove.
func TestEnsureAgentRunning_RetiresTheOldTokenBeforeMinting(t *testing.T) {
	t.Parallel()

	ipc := &agentIPCRecorder{}
	svc, _, _ := setupTestService(t, withRemoteIPC(ipc))
	rec := newStartRecorder()
	rec.install(svc)
	seedOpenAgent(t, svc, "agent-1", true)

	require.NoError(t, svc.ensureAgentRunning("agent-1", nil, interactiveStart))
	require.NoError(t, svc.ensureAgentRunning("agent-1", nil, interactiveStart))

	assert.Equal(t, []string{"mint:token-1", "cleanup:token-1", "mint:token-2"}, ipc.log(),
		"the second relaunch must retire the first spawn's listener BEFORE it binds the same socket path again")
	assert.Contains(t, rec.envFor("agent-1"), "LEAPMUX_CONTROL_TOKEN=token-2",
		"the relaunched process must carry the NEW token")
}

// TestEnsureAgentRunning_RefusesASocketPathStillInUse is the end-to-end shape of
// the ordering bug, driven through a factory that behaves the way locallisten
// does: it refuses to bind a path whose previous listener was never retired.
// With the retire-first order this passes; with mint-first the second relaunch
// silently loses its control socket.
func TestEnsureAgentRunning_RefusesASocketPathStillInUse(t *testing.T) {
	t.Parallel()

	ipc := &exclusiveSocketIPC{}
	svc, _, _ := setupTestService(t, withRemoteIPC(ipc))
	rec := newStartRecorder()
	rec.install(svc)
	seedOpenAgent(t, svc, "agent-1", true)

	require.NoError(t, svc.ensureAgentRunning("agent-1", nil, interactiveStart))
	require.NoError(t, svc.ensureAgentRunning("agent-1", nil, interactiveStart))

	assert.Contains(t, rec.envFor("agent-1"), "LEAPMUX_CONTROL_TOKEN=token-2",
		"the relaunch must bind its own socket; a still-open listener means it came up with no remote control at all")
}

// TestEnsureAgentRunning_ADegradedMintLeavesNoClaimBehind pins the other half of
// the retire-then-mint order. Retiring first opens a window for a concurrent
// close, which cleanupRegistry.claim covers -- but a factory that hands back no
// cleanup must clear that claim, or the NEXT close finds a claim with no
// cleanup, marks it closedWhileClaimed, and the relaunch after that retires its
// fresh socket the instant it registers it.
func TestEnsureAgentRunning_ADegradedMintLeavesNoClaimBehind(t *testing.T) {
	t.Parallel()

	ipc := &agentIPCRecorder{failWith: assert.AnError}
	svc, _, _ := setupTestService(t, withRemoteIPC(ipc))
	rec := newStartRecorder()
	rec.install(svc)
	seedOpenAgent(t, svc, "agent-1", true)

	require.NoError(t, svc.ensureAgentRunning("agent-1", nil, interactiveStart))

	// The factory recovers. The next relaunch must KEEP the socket it mints.
	ipc.mu.Lock()
	ipc.failWith = nil
	ipc.mu.Unlock()
	svc.Agents.StopAndWaitAgent("agent-1")
	require.NoError(t, svc.ensureAgentRunning("agent-1", nil, interactiveStart))

	assert.Equal(t, []string{"mint:token-1"}, ipc.log(),
		"a stranded claim would make register retire the fresh token at once, so the mint would be followed by its own cleanup")
	assert.Contains(t, rec.envFor("agent-1"), "LEAPMUX_CONTROL_TOKEN=token-1")
}

// TestEnsureAgentRunning_MissingIdentityRefusesTheSpawn pins that a mint which
// cannot name its user fails the relaunch instead of starting the agent as
// nobody. Starting it anyway surfaces to the user as an unrelated "socket not
// configured" error from `leapmux control`, with nothing naming the cause.
func TestEnsureAgentRunning_MissingIdentityRefusesTheSpawn(t *testing.T) {
	t.Parallel()

	ipc := &agentIPCRecorder{failWith: ErrMissingIdentity}
	svc, _, _ := setupTestService(t, withRemoteIPC(ipc))
	rec := newStartRecorder()
	rec.install(svc)
	seedOpenAgent(t, svc, "agent-1", true)

	err := svc.ensureAgentRunning("agent-1", nil, interactiveStart)
	require.ErrorIs(t, err, ErrMissingIdentity)
	assert.Empty(t, rec.ids(), "no process may start when its control socket cannot be scoped to a user")
}

// TestEnsureAgentRunning_DegradableFactoryFailureStillStarts pins the other half
// of spawnControlIPC's contract: every factory failure EXCEPT a missing identity
// costs remote control and keeps the tab. An agent the user can still talk to
// beats no agent at all.
func TestEnsureAgentRunning_DegradableFactoryFailureStillStarts(t *testing.T) {
	t.Parallel()

	ipc := &agentIPCRecorder{failWith: assert.AnError}
	svc, _, _ := setupTestService(t, withRemoteIPC(ipc))
	rec := newStartRecorder()
	rec.install(svc)
	seedOpenAgent(t, svc, "agent-1", true)

	require.NoError(t, svc.ensureAgentRunning("agent-1", nil, interactiveStart))
	assert.Equal(t, []string{"agent-1"}, rec.ids())
	assert.Empty(t, rec.envFor("agent-1"))
}

// TestClearContext_MintsAControlSocket covers the /clear relaunch, which stops
// the process and starts a new one under the same tab.
func TestClearContext_MintsAControlSocket(t *testing.T) {
	t.Parallel()

	ipc := &agentIPCRecorder{}
	svc, _, _ := setupTestService(t, withRemoteIPC(ipc))
	rec := newStartRecorder()
	rec.install(svc)
	seedOpenAgent(t, svc, "agent-1", true)

	svc.handleClearContext("agent-1")

	assert.Contains(t, rec.envFor("agent-1"), "LEAPMUX_CONTROL_TOKEN=token-1")
}

// TestApplySettingsViaRestart_MintsAControlSocket covers the relaunch a settings
// change forces when the CLI only honors the axis on a fresh launch.
func TestApplySettingsViaRestart_MintsAControlSocket(t *testing.T) {
	t.Parallel()

	ipc := &agentIPCRecorder{}
	svc, _, _ := setupTestService(t, withRemoteIPC(ipc))
	rec := newStartRecorder()
	rec.install(svc)
	seedOpenAgent(t, svc, "agent-1", true)

	svc.applySettingsViaRestart(requireAgentRow(t, svc, "agent-1"), OptionMap{"model": "opus"})

	assert.Contains(t, rec.envFor("agent-1"), "LEAPMUX_CONTROL_TOKEN=token-1")
}

// TestPlanExecutionRestart_MintsAControlSocket covers the plan-execution
// relaunch, the fourth path that builds launch options from scratch.
func TestPlanExecutionRestart_MintsAControlSocket(t *testing.T) {
	t.Parallel()

	ipc := &agentIPCRecorder{}
	svc, _, _ := setupTestService(t, withRemoteIPC(ipc))
	rec := newStartRecorder()
	rec.install(svc)
	seedOpenAgent(t, svc, "agent-1", true)

	svc.initiatePlanExecutionRestart("agent-1", "acceptEdits", requireAgentRow(t, svc, "agent-1"), "run the plan")

	assert.Contains(t, rec.envFor("agent-1"), "LEAPMUX_CONTROL_TOKEN=token-1")
}

// TestPlanExecutionRestart_MissingIdentityRefusesTheSpawn pins that the shared
// failure branch really is shared: a failed mint must take the same exit as a
// failed start, clearing the stale session id rather than leaving the row
// pointing at a session no process holds.
func TestPlanExecutionRestart_MissingIdentityRefusesTheSpawn(t *testing.T) {
	t.Parallel()

	ipc := &agentIPCRecorder{failWith: ErrMissingIdentity}
	svc, _, _ := setupTestService(t, withRemoteIPC(ipc))
	rec := newStartRecorder()
	rec.install(svc)
	seedOpenAgent(t, svc, "agent-1", true)

	svc.initiatePlanExecutionRestart("agent-1", "acceptEdits", requireAgentRow(t, svc, "agent-1"), "run the plan")

	assert.Empty(t, rec.ids(), "no process may start when its control socket cannot be scoped to a user")
	assert.Empty(t, requireAgentRow(t, svc, "agent-1").AgentSessionID,
		"the stale session id must be cleared so the next send does not try to resume a session nobody holds")
}

// TestRemintAgentControlIPC_NoFactoryIsNotAFailure pins the disabled case: a
// worker wired without a control-IPC factory relaunches agents normally, just
// without remote control.
func TestRemintAgentControlIPC_NoFactoryIsNotAFailure(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	require.Nil(t, svc.ControlIPC)

	envs, err := svc.remintAgentControlIPC(agent.Options{
		AgentID:       "agent-1",
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, "resume")
	require.NoError(t, err)
	assert.Nil(t, envs)
}

// TestAgentResume_ResumedAgentsCarryAControlSocket ties the two halves
// together: the sweep exists so agents can reach each other again, which only
// holds if each resumed process gets its own control socket.
func TestAgentResume_ResumedAgentsCarryAControlSocket(t *testing.T) {
	t.Parallel()

	ipc := &agentIPCRecorder{}
	svc, _, _ := setupTestService(t, withRemoteIPC(ipc))
	rec := newStartRecorder()
	rec.install(svc)
	seedOpenAgent(t, svc, "agent-a", true)
	seedOpenAgent(t, svc, "agent-b", true)

	runSweep(t, svc)

	for _, id := range []string{"agent-a", "agent-b"} {
		assert.Contains(t, rec.envFor(id), "LEAPMUX_CONTROL_TAB_ID="+id,
			"each resumed agent needs its OWN socket, not a shared one")
	}
	assert.Equal(t, 2, ipc.mints)
}

// TestEnsureAgentRunning_RefusesAClosedRow pins the guard that keeps every
// auto-start path from spawning a process nothing will ever stop.
//
// GetAgentByID carries no closed_at predicate, so every caller reads a row a
// CloseAgent can invalidate before the spawn: the resume sweep from its own
// listing, and the three request-driven callers from their own earlier read. A
// process started for a closed tab holds a CLI and a control socket for the life
// of the worker, under a tab no client can see and no close path will reach.
func TestEnsureAgentRunning_RefusesAClosedRow(t *testing.T) {
	t.Parallel()

	ipc := &agentIPCRecorder{}
	svc, _, _ := setupTestService(t, withRemoteIPC(ipc))
	rec := newStartRecorder()
	rec.install(svc)
	seedOpenAgent(t, svc, "agent-1", true)
	_, err := svc.Queries.CloseAgent(context.Background(), "agent-1")
	require.NoError(t, err)

	require.Error(t, svc.ensureAgentRunning("agent-1", nil, interactiveStart))
	assert.Empty(t, rec.ids(), "a closed tab must never be given a process")
	assert.Empty(t, ipc.log(),
		"the refusal must come before the mint, so a closed tab leaves no listening socket behind")
}

// TestApplySettingsViaRestart_MintsUnderTheLifecycleLock pins that the settings
// relaunch holds the per-agent lifecycle lock across its mint, the way the other
// relaunch paths do.
//
// The mint retires the previous spawn's cleanup and registers a new one under
// the same tab id, and cleanupRegistry.register overwrites. Two relaunches for
// one tab that interleave their mints therefore drop one of the two cleanups,
// and nothing ever retires it: the socket stays bound and its delegation bearer
// unrevoked for the life of the worker.
func TestApplySettingsViaRestart_MintsUnderTheLifecycleLock(t *testing.T) {
	t.Parallel()

	ipc := &agentIPCRecorder{}
	svc, _, _ := setupTestService(t, withRemoteIPC(ipc))
	newStartRecorder().install(svc)
	seedOpenAgent(t, svc, "agent-1", true)

	// Hold the lock the mint must wait for, and prove it waits.
	unlock := svc.Agents.LockAgent("agent-1")
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.applySettingsViaRestart(requireAgentRow(t, svc, "agent-1"), OptionMap{"model": "opus"})
	}()

	select {
	case <-done:
		unlock()
		t.Fatal("the settings relaunch minted without the per-agent lifecycle lock; a concurrent relaunch or close can strand its cleanup")
	case <-time.After(150 * time.Millisecond):
	}
	require.Empty(t, ipc.log(), "no mint may happen while another lifecycle step holds the lock")

	unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the settings relaunch never completed after the lock was released")
	}
	assert.Equal(t, []string{"mint:token-1"}, ipc.log())
}

// TestRelaunchForStartupSettingsChange_MintsAControlSocket covers the relaunch
// path that had no mint at all.
//
// It is reached when a settings change lands inside the startup window and the
// CLI honors that axis only on a fresh launch. Its options come from the OPEN
// path and resolveConfirmedStartupSettings replaces only Options, so without a
// mint the new process inherits the previous process's LEAPMUX_CONTROL_* and
// talks to a socket the next relaunch retires under it.
func TestRelaunchForStartupSettingsChange_MintsAControlSocket(t *testing.T) {
	t.Parallel()

	ipc := &agentIPCRecorder{}
	svc, _, _ := setupTestService(t, withRemoteIPC(ipc))
	rec := newStartRecorder()
	rec.install(svc)
	seedOpenAgent(t, svc, "agent-1", true)

	row := requireAgentRow(t, svc, "agent-1")
	opts := svc.baseAgentOptions("agent-1", row.WorkingDir, row.AgentProvider)
	opts.Options = OptionMap{"model": "opus"}
	// The stale env vars the OPEN path left on these options.
	opts.ExtraEnv = []string{"LEAPMUX_CONTROL_TOKEN=stale-from-the-open-path"}

	svc.relaunchForStartupSettingsChange("agent-1", row.AgentProvider, opts, row)

	assert.Contains(t, rec.envFor("agent-1"), "LEAPMUX_CONTROL_TOKEN=token-1",
		"the startup-window relaunch carried the previous process's token instead of minting its own")
	assert.NotContains(t, rec.envFor("agent-1"), "LEAPMUX_CONTROL_TOKEN=stale-from-the-open-path")
}

// TestRemintControlIPC_ADegradedMintKeepsAnEarlierCloseMark pins that abandoning
// a claim does not erase the record of a close.
//
// replace deliberately preserves a mark a real close left, so the caller's
// register still honours it. abandonClaim used to delete that mark, so a
// degraded factory erased it -- and the NEXT relaunch, whose factory recovered,
// stored a live socket for a tab whose teardown already finished. That is the
// exact leak the mark exists to prevent.
func TestRemintControlIPC_ADegradedMintKeepsAnEarlierCloseMark(t *testing.T) {
	t.Parallel()

	ipc := &agentIPCRecorder{failWith: assert.AnError}
	svc, _, _ := setupTestService(t, withRemoteIPC(ipc))
	rec := newStartRecorder()
	rec.install(svc)
	seedOpenAgent(t, svc, "agent-1", true)

	// A spawn claimed the id, then a real close ran before the cleanup landed.
	svc.agentCleanups.claim("agent-1")
	svc.agentCleanups.closeTab("agent-1")

	// A relaunch whose factory degrades: it registers nothing and abandons.
	require.NoError(t, svc.ensureAgentRunning("agent-1", nil, interactiveStart))
	require.Empty(t, ipc.log(), "fixture check: the degraded factory minted nothing")

	// The factory recovers. The close mark must still be honoured.
	ipc.mu.Lock()
	ipc.failWith = nil
	ipc.mu.Unlock()
	svc.Agents.StopAndWaitAgent("agent-1")
	require.NoError(t, svc.ensureAgentRunning("agent-1", nil, interactiveStart))

	assert.Equal(t, []string{"mint:token-1", "cleanup:token-1"}, ipc.log(),
		"the degraded mint erased the close mark, so this socket was stored for a tab that is already gone")
}

// TestRemintControlIPC_DoesNotClaimOwnershipOfARetiredSocket pins that the
// ownership flag reports what the registry STORED.
//
// register retires the resource on the spot -- and stores nothing -- when it
// finds the mark a real close left. A caller that derived ownership from "the
// factory handed back a cleanup" would then run a deferred retire against an
// entry that is not there, and believe it had retired a socket it never owned.
func TestRemintControlIPC_DoesNotClaimOwnershipOfARetiredSocket(t *testing.T) {
	t.Parallel()

	ipc := &agentIPCRecorder{}
	svc, _, _ := setupTestService(t, withRemoteIPC(ipc))
	seedOpenAgent(t, svc, "agent-1", true)

	svc.agentCleanups.claim("agent-1")
	svc.agentCleanups.closeTab("agent-1")

	_, owned, err := svc.remintControlIPC(tabKindAgent, "agent-1", "restart", svc.RegisteredBy(),
		func() ([]string, func(), error) {
			return svc.ControlIPC.AgentSpawning(AgentSpawnInfo{
				UserID: svc.RegisteredBy(), WorkerID: svc.WorkerID, TabID: "agent-1",
			})
		})
	require.NoError(t, err)
	assert.False(t, owned,
		"the mint reported ownership of a socket register already retired; a deferred retire would find nothing")
	assert.Equal(t, []string{"mint:token-1", "cleanup:token-1"}, ipc.log())
}

// TestRestartTerminal_RetiresTheOldTokenBeforeMinting pins the swap ORDER on the
// TERMINAL path, which the agent tests above pin only for agents.
//
// Both paths share remintControlIPC, and both bind the same per-tab socket path
// on a relaunch: controlipc.DefaultSocketPath is a pure function of (worker,
// kind, tab), and locallisten refuses a path whose socket answers a dial. A mint
// that ran first would therefore fail every in-process terminal restart
// DEGRADABLY -- the shell comes up with no LEAPMUX_CONTROL_* and `leapmux
// control` inside it reports "socket not configured".
//
// The existing missing-identity and restart tests count mints and cleanups, and
// those counts are identical under either order, so nothing here failed if the
// order was reversed.
func TestRestartTerminal_RetiresTheOldTokenBeforeMinting(t *testing.T) {
	t.Parallel()

	ipc := &agentIPCRecorder{}
	svc, d, w := setupTestService(t, withRemoteIPC(ipc))
	defer drainAllInFlight(svc)

	terminalID := openTerminalViaRPC(t, svc, d, w, t.TempDir())
	svc.TerminalStartup.WaitForInFlight()
	require.Equal(t, []string{"mint:token-1"}, ipc.log(), "the open path mints the first token")

	exitTerminalAndWait(t, svc, d, terminalID, "")
	dispatchRestart(d, terminalID, newTestWriter())
	svc.TerminalStartup.WaitForInFlight()

	assert.Equal(t, []string{"mint:token-1", "cleanup:token-1", "mint:token-2"}, ipc.log(),
		"the restart must retire the previous spawn's listener BEFORE it binds the same socket path again")
}

// TestRestartTerminal_RefusesASocketPathStillInUse is the end-to-end shape of
// that ordering bug, driven through a factory that behaves the way locallisten
// does: it refuses to bind a path whose previous listener was never retired.
func TestRestartTerminal_RefusesASocketPathStillInUse(t *testing.T) {
	t.Parallel()

	ipc := &exclusiveSocketIPC{}
	svc, d, w := setupTestService(t, withRemoteIPC(ipc))
	defer drainAllInFlight(svc)

	terminalID := openTerminalViaRPC(t, svc, d, w, t.TempDir())
	exitTerminalAndWait(t, svc, d, terminalID, "")
	dispatchRestart(d, terminalID, newTestWriter())
	svc.TerminalStartup.WaitForInFlight()

	ipc.mu.Lock()
	mints := ipc.mints
	ipc.mu.Unlock()
	assert.Equal(t, 2, mints,
		"the restart must bind its own socket; a still-open listener means the shell came up with no remote control at all")
}

// TestRemintControlIPC_KeepsTheDelegationOffZeroAcrossTheSwap pins what makes
// retire-then-mint cheap.
//
// The retire releases the previous spawn's per-user delegation. For a user whose
// only live spawn is the one being replaced that reference count reaches zero,
// and the real store then revokes the hub token through a blocking call -- after
// which the next call from the relaunched tab pays a second round trip to mint a
// fresh one. Holding one reference across the swap keeps the count off zero.
func TestRemintControlIPC_KeepsTheDelegationOffZeroAcrossTheSwap(t *testing.T) {
	t.Parallel()

	ipc := &agentIPCRecorder{}
	svc, _, _ := setupTestService(t, withRemoteIPC(ipc))
	newStartRecorder().install(svc)
	seedOpenAgent(t, svc, "agent-1", true)

	require.NoError(t, svc.ensureAgentRunning("agent-1", nil, interactiveStart))
	svc.Agents.StopAndWaitAgent("agent-1")
	require.NoError(t, svc.ensureAgentRunning("agent-1", nil, interactiveStart))

	ipc.mu.Lock()
	zeroed, refs := ipc.zeroed, ipc.delegationRefs
	ipc.mu.Unlock()
	assert.Zero(t, zeroed,
		"the relaunch let the delegation reach zero; the hub revoked the token and the next call must mint a fresh one")
	assert.Equal(t, 1, refs, "exactly the live spawn's reference must remain")
}

// TestRemintControlIPC_AFailedMintStillReleasesTheDelegation is the other half.
// No spawn is left to use the token, so the count MUST reach zero and the revoke
// must happen -- holding a reference across a swap that produced nothing would
// keep a bearer alive for a tab that has none.
func TestRemintControlIPC_AFailedMintStillReleasesTheDelegation(t *testing.T) {
	t.Parallel()

	ipc := &agentIPCRecorder{}
	svc, _, _ := setupTestService(t, withRemoteIPC(ipc))
	newStartRecorder().install(svc)
	seedOpenAgent(t, svc, "agent-1", true)

	require.NoError(t, svc.ensureAgentRunning("agent-1", nil, interactiveStart))
	svc.Agents.StopAndWaitAgent("agent-1")

	ipc.mu.Lock()
	ipc.failWith = assert.AnError
	ipc.mu.Unlock()
	require.NoError(t, svc.ensureAgentRunning("agent-1", nil, interactiveStart))

	ipc.mu.Lock()
	zeroed, refs := ipc.zeroed, ipc.delegationRefs
	ipc.mu.Unlock()
	assert.Equal(t, 1, zeroed,
		"a swap that minted nothing must let the delegation reach zero, so the hub token is revoked")
	assert.Zero(t, refs)
}

// TestEnsureAgentRunning_RefusesWhileAnotherStartupHoldsTheAgent pins the claim
// AgentStartup.begin makes, on the one path that reaches it: a startup that
// holds the agent for longer than the caller can wait.
//
// An OpenAgent startup holds no manager entry until its final handoff, so
// HasAgent is false while one is in flight -- and the send gate refuses only a
// PERMANENT startup failure. A message that lands in that window used to spawn a
// second process for the same tab, and its begin() overwrote the open's registry
// entry, so a later CloseAgent cancelled the wrong context. The claim is what
// stops that; this test drives the wait past its bound to reach it.
func TestEnsureAgentRunning_RefusesWhileAnotherStartupHoldsTheAgent(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	rec := newStartRecorder()
	rec.install(svc)
	seedOpenAgent(t, svc, "agent-1", true)
	// A fake clock, so the give-up costs no wall time and the assertion below
	// about the caller WAITING first cannot lose to a real timer.
	clock := newFakeStartupClock()
	svc.AgentStartup.clock = clock

	// Stand in for the open path's in-flight startup.
	openCtx, openCancel := context.WithCancel(context.Background())
	t.Cleanup(openCancel)
	openHandle := svc.AgentStartup.begin("agent-1", openCancel)
	require.NotNil(t, openHandle)
	t.Cleanup(func() { svc.AgentStartup.finishEntry(openHandle) })

	errCh := make(chan error, 1)
	go func() { errCh <- svc.ensureAgentRunning("agent-1", nil, interactiveStart) }()

	// The CLIENT's budget, not the process's. Every interactive caller holds
	// its RPC response open across this wait, and the client abandons that RPC
	// at roughly 1.5x the API timeout -- so a wait armed for the five-minute
	// startup timeout would report a send as failed that this worker goes on to
	// deliver, under a Retry button that then sends it twice.
	assert.Equal(t, svc.agentAPITimeout(), clock.waitArmed(t),
		"the wait must end inside the budget the client gives the RPC that holds it")
	assert.Less(t, svc.agentAPITimeout(), svc.agentStartupTimeout(),
		"a wait as long as the whole startup budget is the defect this assertion exists to prevent")
	select {
	case <-errCh:
		require.FailNow(t, "refused while the startup was still in flight; the user's message had a process coming")
	default:
	}

	clock.fire(t)
	select {
	case err := <-errCh:
		require.Error(t, err,
			"a cold start ran while another startup held the agent; that spawns a second process for one tab")
	case <-time.After(10 * time.Second):
		require.FailNow(t, "the wait outlived its own limit")
	}
	assert.Empty(t, rec.ids())

	// The open's handle must still be the registered one, so a close reaches IT.
	svc.AgentStartup.cancelAndClear("agent-1", keepWorktreeOnClose)
	assert.ErrorIs(t, openCtx.Err(), context.Canceled,
		"the auto-start displaced the open's registry entry; a close then cancels the wrong context")
}

// TestEnsureAgentRunning_WaitsForTheStartupThatHoldsTheAgent is the other half,
// and it is the one a user feels.
//
// A message sent inside the open path's startup window takes the auto-start
// path, because HasAgent is false until that startup's final handoff. Refusing
// there loses the message outright: SendAgentMessage records "agent is not
// running" on the row, the open path brings the CLI up a second later, and
// nothing ever hands it what the user typed. Joining the startup that is already
// running is what keeps that message.
func TestEnsureAgentRunning_WaitsForTheStartupThatHoldsTheAgent(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	rec := newStartRecorder()
	rec.install(svc)
	seedOpenAgent(t, svc, "agent-1", true)
	clock := newFakeStartupClock()
	svc.AgentStartup.clock = clock

	openHandle := svc.AgentStartup.begin("agent-1", func() {})
	require.NotNil(t, openHandle)

	errCh := make(chan error, 1)
	go func() { errCh <- svc.ensureAgentRunning("agent-1", nil, interactiveStart) }()

	clock.waitArmed(t)
	assert.Empty(t, rec.ids(), "it must not spawn a second process for the tab that is already starting")

	// The open path finishes.
	svc.AgentStartup.succeed("agent-1", nil)
	svc.AgentStartup.finishEntry(openHandle)

	select {
	case err := <-errCh:
		require.NoError(t, err, "the startup it waited for finished, so the message has a process to reach")
	case <-time.After(10 * time.Second):
		require.FailNow(t, "the caller never resumed after the startup it waited for finished")
	}
	assert.Equal(t, []string{"agent-1"}, rec.ids(),
		"the agent must be running once the wait is over, or the message has nowhere to go")
}

// TestEnsureAgentRunning_BackgroundStartDoesNotWaitForAnotherStartup pins the
// asymmetry. The resume sweep has no message to lose: the startup it would wait
// for produces the very process it wants, so skipping is the same outcome
// sooner -- and a sweep worker parked on that wait holds up the Shutdown that
// joins the sweep.
func TestEnsureAgentRunning_BackgroundStartDoesNotWaitForAnotherStartup(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	rec := newStartRecorder()
	rec.install(svc)
	seedOpenAgent(t, svc, "agent-1", true)
	clock := newFakeStartupClock()
	svc.AgentStartup.clock = clock

	openHandle := svc.AgentStartup.begin("agent-1", func() {})
	require.NotNil(t, openHandle)
	t.Cleanup(func() { svc.AgentStartup.finishEntry(openHandle) })

	require.Error(t, svc.ensureAgentRunning("agent-1", nil, backgroundStart))
	assert.Empty(t, rec.ids())

	clock.mu.Lock()
	defer clock.mu.Unlock()
	assert.Empty(t, clock.timers, "the sweep must not wait on a startup that is already doing its work")
}

// TestEnsureAgentRunning_RefusesWhenACloseEndedTheStartupItWaitedFor pins the
// one outcome of the wait that must NOT produce a process.
//
// cancelAndClear wakes every waiter as its FIRST teardown step -- several steps
// before the close stamps closed_at. A waiter that read only "the startup
// settled" therefore found HasAgent false, read a row whose ClosedAt is still
// NULL, and cold-started a tab the user had just closed. Nothing stops such a
// process: the close it raced already ran its own teardown.
func TestEnsureAgentRunning_RefusesWhenACloseEndedTheStartupItWaitedFor(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	rec := newStartRecorder()
	rec.install(svc)
	seedOpenAgent(t, svc, "agent-1", true)
	clock := newFakeStartupClock()
	svc.AgentStartup.clock = clock

	// Stand in for the open path's in-flight startup.
	openHandle := svc.AgentStartup.begin("agent-1", func() {})
	require.NotNil(t, openHandle)
	t.Cleanup(func() { svc.AgentStartup.finishEntry(openHandle) })

	errCh := make(chan error, 1)
	go func() { errCh <- svc.ensureAgentRunning("agent-1", nil, interactiveStart) }()
	clock.waitArmed(t)

	// The user closes the tab. The row still reads OPEN at this point, exactly
	// as it does for the whole of the close's own teardown.
	svc.AgentStartup.cancelAndClear("agent-1", keepWorktreeOnClose)
	row := requireAgentRow(t, svc, "agent-1")
	require.False(t, row.ClosedAt.Valid,
		"the premise: the close has not stamped closed_at yet, so that guard cannot carry this")

	select {
	case err := <-errCh:
		require.Error(t, err, "a close ended the startup, so there is no process to wait for and none to start")
	case <-time.After(10 * time.Second):
		require.FailNow(t, "the caller never resumed after the close released it")
	}
	assert.Empty(t, rec.ids(),
		"it started a process for a tab the user closed; the close already ran its teardown, so nothing stops it")
}

// requireAgentRow is a small readability helper for the tests above.
func requireAgentRow(t *testing.T, svc *Service, agentID string) db.Agent {
	t.Helper()
	row, err := svc.Queries.GetAgentByID(context.Background(), agentID)
	require.NoError(t, err)
	return row
}

// TestRemintControlIPC_ARelaunchRacingASpawnClaimKeepsItsSocket pins the one
// thing cleanupRegistry.replace does that run()+claim() does not.
//
// A tab's row exists before its cleanup is registered (the open path claims in
// between), and a relaunch can land in that window. run() reads a claim with no
// cleanup as "the tab was closed while starting" and leaves a
// closedWhileClaimed mark -- so the relaunch's own register would retire the
// socket it just minted, and the process would come up with no remote control:
// the exact failure this helper exists to prevent, reintroduced by the fix for
// it.
func TestRemintControlIPC_ARelaunchRacingASpawnClaimKeepsItsSocket(t *testing.T) {
	t.Parallel()

	ipc := &agentIPCRecorder{}
	svc, _, _ := setupTestService(t, withRemoteIPC(ipc))
	rec := newStartRecorder()
	rec.install(svc)
	seedOpenAgent(t, svc, "agent-1", true)

	// The open path's window: the row is durable, the cleanup is not in yet.
	svc.agentCleanups.claim("agent-1")

	require.NoError(t, svc.ensureAgentRunning("agent-1", nil, interactiveStart))

	assert.Equal(t, []string{"mint:token-1"}, ipc.log(),
		"the relaunch retired its own fresh socket; a spawn claim was misread as a close")
	assert.Contains(t, rec.envFor("agent-1"), "LEAPMUX_CONTROL_TOKEN=token-1")
}

// TestRemintControlIPC_ARelaunchAfterARealCloseRetiresItsSocket is the other
// side of that rule: a mark left by an actual close must still be honoured, so
// the relaunch does not leave a listening socket behind for a tab that is gone.
func TestRemintControlIPC_ARelaunchAfterARealCloseRetiresItsSocket(t *testing.T) {
	t.Parallel()

	ipc := &agentIPCRecorder{}
	svc, _, _ := setupTestService(t, withRemoteIPC(ipc))
	rec := newStartRecorder()
	rec.install(svc)
	seedOpenAgent(t, svc, "agent-1", true)

	// A spawn claimed the id, then a close ran before the cleanup landed. That
	// is what leaves a closedWhileClaimed mark.
	svc.agentCleanups.claim("agent-1")
	svc.agentCleanups.closeTab("agent-1")

	require.NoError(t, svc.ensureAgentRunning("agent-1", nil, interactiveStart))

	assert.Equal(t, []string{"mint:token-1", "cleanup:token-1"}, ipc.log(),
		"a socket minted for a tab that is already closed must be retired at once, not left listening")
}
