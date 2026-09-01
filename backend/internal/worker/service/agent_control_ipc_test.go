package service

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/userid"
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
}

func (f *agentIPCRecorder) AgentSpawning(info AgentSpawnInfo) ([]string, func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.owners = append(f.owners, info.UserID)
	if f.failWith != nil {
		return nil, nil, f.failWith
	}
	f.mints++
	token := fmt.Sprintf("token-%d", f.mints)
	f.events = append(f.events, "mint:"+token)
	envs := []string{
		"LEAPMUX_CONTROL_TAB_ID=" + info.TabID,
		"LEAPMUX_CONTROL_TOKEN=" + token,
		"LEAPMUX_CONTROL_AGENT_PROVIDER=" + info.AgentProvider,
	}
	return envs, func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.events = append(f.events, "cleanup:"+token)
	}, nil
}

func (f *agentIPCRecorder) TerminalSpawning(TerminalSpawnInfo) ([]string, func(), error) {
	return nil, func() {}, nil
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
// per tab. A mint while the previous cleanup has not run answers an error, the
// way `unix listen: ... is already in use` does.
type exclusiveSocketIPC struct {
	mu    sync.Mutex
	mints int
	bound bool
}

func (f *exclusiveSocketIPC) AgentSpawning(AgentSpawnInfo) ([]string, func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.bound {
		return nil, nil, fmt.Errorf("socket is already in use")
	}
	f.mints++
	f.bound = true
	token := fmt.Sprintf("token-%d", f.mints)
	return []string{"LEAPMUX_CONTROL_TOKEN=" + token}, func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.bound = false
	}, nil
}

func (f *exclusiveSocketIPC) TerminalSpawning(TerminalSpawnInfo) ([]string, func(), error) {
	return nil, func() {}, nil
}

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

	require.NoError(t, svc.ensureAgentRunning(t.Context(), "agent-1", nil))

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

	require.NoError(t, svc.ensureAgentRunning(t.Context(), "agent-1", nil))

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

	require.NoError(t, svc.ensureAgentRunning(t.Context(), "agent-1", nil))
	require.NoError(t, svc.ensureAgentRunning(t.Context(), "agent-1", nil))

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

	require.NoError(t, svc.ensureAgentRunning(t.Context(), "agent-1", nil))
	require.NoError(t, svc.ensureAgentRunning(t.Context(), "agent-1", nil))

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

	require.NoError(t, svc.ensureAgentRunning(t.Context(), "agent-1", nil))

	// The factory recovers. The next relaunch must KEEP the socket it mints.
	ipc.mu.Lock()
	ipc.failWith = nil
	ipc.mu.Unlock()
	svc.Agents.StopAndWaitAgent("agent-1")
	require.NoError(t, svc.ensureAgentRunning(t.Context(), "agent-1", nil))

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

	err := svc.ensureAgentRunning(t.Context(), "agent-1", nil)
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

	require.NoError(t, svc.ensureAgentRunning(t.Context(), "agent-1", nil))
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

	envs, err := svc.remintAgentControlIPC("agent-1", t.TempDir(),
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, "resume")
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

	require.NoError(t, svc.ensureAgentRunning(t.Context(), "agent-1", nil))

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
	svc.agentCleanups.run("agent-1")

	require.NoError(t, svc.ensureAgentRunning(t.Context(), "agent-1", nil))

	assert.Equal(t, []string{"mint:token-1", "cleanup:token-1"}, ipc.log(),
		"a socket minted for a tab that is already closed must be retired at once, not left listening")
}
