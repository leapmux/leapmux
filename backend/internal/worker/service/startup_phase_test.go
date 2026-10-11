package service

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/terminal"
)

// recordedCallbacks captures the order each callback fires in. The
// orchestration helpers (`runStartupPhase0`, `failStartup`) shape a
// specific sequence (label broadcast → mutation → rollback → persist
// → registry-fail → broadcast-failed) and the wrapper methods in
// agent.go / terminal.go rely on every step running in that order.
type recordedCallbacks struct {
	events []string
}

func (rc *recordedCallbacks) hooks() startupCallbacks {
	return startupCallbacks{
		setMessage:        func(label string) { rc.events = append(rc.events, "setMessage:"+label) },
		broadcastStarting: func(label string) { rc.events = append(rc.events, "broadcastStarting:"+label) },
		persistError:      func(errMsg string) { rc.events = append(rc.events, "persistError:"+errMsg) },
		broadcastFailed:   func(errMsg string) { rc.events = append(rc.events, "broadcastFailed:"+errMsg) },
		registryFail:      func(errMsg string) { rc.events = append(rc.events, "registryFail:"+errMsg) },
	}
}

// TestFailStartup_OrdersPersistRegistryBroadcast pins the contract that
// failStartup writes the startup_error column first, records the failure in
// the registry second, and broadcasts STARTUP_FAILED last.
//
// Each pair has its own reason:
//   - The column before the registry: fail() wakes a caller that joined the
//     startup, and the row that the caller then re-reads holds the error.
//   - The registry before the broadcast: ListAgents, ListTerminals and the
//     WatchEvents catch-up read the registry. A client that receives
//     STARTUP_FAILED and then reads the registry while it still says STARTING
//     keeps STARTING, and no later event corrects it.
func TestFailStartup_OrdersPersistRegistryBroadcast(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	defer drainAllInFlight(svc)

	rc := &recordedCallbacks{}
	// Empty gitModeResult => no rollback path, only the failure tail.
	svc.failStartup(gitModeResult{}, errors.New("boom"), rc.hooks())

	require.Equal(t, []string{
		"persistError:boom",
		"registryFail:boom",
		"broadcastFailed:boom",
	}, rc.events, "failure tail must run in DB-then-registry-then-broadcast order")
}

// TestFailStartup_RollbackLabelBroadcastsBeforePersist verifies that a
// gitModeResult with a partial mutation triggers a rollback-label
// STARTING broadcast and the rollback itself before the failure tail.
// Without the rollback ordering the UI flashes STARTUP_FAILED before
// the user sees the "rolling back…" message.
func TestFailStartup_RollbackLabelBroadcastsBeforePersist(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	defer drainAllInFlight(svc)

	rc := &recordedCallbacks{}
	gm := gitModeResult{
		Rollback: gitModeRollback{CreatedBranch: &rollbackBranch{
			WorkingDir:    t.TempDir(),
			CreatedBranch: "feat",
		}},
	}
	svc.failStartup(gm, errors.New("boom"), rc.hooks())

	// First two events must be the rollback label broadcast; persist /
	// registry-fail / broadcast-failed follow in their fixed order.
	require.GreaterOrEqual(t, len(rc.events), 5)
	assert.Contains(t, rc.events[0], "setMessage:")
	assert.Contains(t, rc.events[1], "broadcastStarting:")
	assert.Equal(t, []string{
		"persistError:boom",
		"registryFail:boom",
		"broadcastFailed:boom",
	}, rc.events[len(rc.events)-3:])
}

// TestRunStartupPhase0_NoLabel_SkipsBroadcast confirms that a plan
// whose PhaseLabel() returns "" (the no-op / passthrough mode) executes
// the git-mode mutation without firing a STARTING broadcast — the
// frontend would otherwise see a spurious "" status flash.
func TestRunStartupPhase0_NoLabel_SkipsBroadcast(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	defer drainAllInFlight(svc)

	rc := &recordedCallbacks{}
	// A zero-value plan has Mode == 0 → default branch in PhaseLabel,
	// returning "". executeGitMode no-ops on this shape (no mutation
	// fields populated), matching the "Current" mode the dialogs send
	// when the user picks the default radio.
	plan := gitModePlan{WorkingDir: t.TempDir()}
	require.Equal(t, "", plan.PhaseLabel(), "test premise: zero-value plan has no label")

	_, err := svc.runStartupPhase0(context.Background(), plan, rc.hooks())
	require.NoError(t, err)
	assert.Empty(t, rc.events, "phase-0 must not broadcast when PhaseLabel is empty")
}

// finalStatusReader is a watcher that runs a reader at the moment a broadcast
// delivers the final status that the test waits for.
//
// A broadcast calls SendStream on the goroutine that broadcasts, so read runs
// after the client received that status and before the broadcasting code takes
// its next step. That instant is the one that matters: a reader that sees
// STARTING then keeps STARTING, because no later event corrects it. The gate
// observes that instant directly and needs no sleep.
//
// read runs on the broadcasting goroutine, so it must not call t.FailNow. It
// hands its result to the test goroutine, which asserts.
type finalStatusReader struct {
	*testResponseWriter
	isFinal func(*leapmuxv1.WatchEventsResponse) bool
	read    func()
	once    sync.Once
}

func (w *finalStatusReader) SendStream(m *leapmuxv1.InnerStreamMessage) error {
	var resp leapmuxv1.WatchEventsResponse
	if proto.Unmarshal(m.GetPayload(), &resp) == nil && w.isFinal(&resp) {
		w.once.Do(w.read)
	}
	return w.testResponseWriter.SendStream(m)
}

// awaitListed returns the writer that a finalStatusReader filled, or fails the
// test when the final status never reached the watcher.
func awaitListed(t *testing.T, ctx context.Context, listed <-chan *testResponseWriter) *testResponseWriter {
	t.Helper()
	select {
	case w := <-listed:
		require.Empty(t, w.errors)
		require.Len(t, w.responses, 1)
		return w
	case <-ctx.Done():
		require.FailNow(t, "the final status never reached the watcher")
		return nil
	}
}

// TestFailStartup_ListAgentsReportsTheFailureThatTheClientReceived pins the
// order of failStartup through the real open path of an agent: when a client
// receives STARTUP_FAILED, ListAgents already reports it.
//
// The order used to be persist, broadcast, registry. Between the broadcast and
// the registry write, ListAgents and the WatchEvents catch-up read STARTING
// from the registry. A client that reloads in that window shows a startup that
// never ends.
func TestFailStartup_ListAgentsReportsTheFailureThatTheClientReceived(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t)
	defer drainAllInFlight(svc)
	ctx := testutil.DeadlineContext(t)
	release := make(chan struct{})
	svc.startAgentFn = func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
		<-release
		return nil, errors.New("claude: command not found")
	}

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, w)
	ids := collectAgentIDs(w)
	require.Len(t, ids, 1)
	agentID := ids[0]

	listed := make(chan *testResponseWriter, 1)
	registerAgentWatch(svc, testChannelID, agentID, leapmuxv1.WatchMode_WATCH_MODE_FULL, &finalStatusReader{
		testResponseWriter: newTestWriter(),
		isFinal: func(resp *leapmuxv1.WatchEventsResponse) bool {
			return resp.GetAgentEvent().GetStatusChange().GetStatus() == leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED
		},
		read: func() {
			lw := newTestWriter()
			dispatch(d, "ListAgents", &leapmuxv1.ListAgentsRequest{TabIds: []string{agentID}}, lw)
			listed <- lw
		},
	})
	close(release)

	lw := awaitListed(t, ctx, listed)
	var resp leapmuxv1.ListAgentsResponse
	require.NoError(t, proto.Unmarshal(lw.responses[0].GetPayload(), &resp))
	require.Len(t, resp.GetAgents(), 1)
	info := resp.GetAgents()[0]
	// Compared as names, so a failure states STARTING rather than a number.
	assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED.String(), info.GetStatus().String(),
		"the client received STARTUP_FAILED, and ListAgents still reported the startup in progress")
	assert.Equal(t, "claude: command not found", info.GetStartupError())
}

// TestFailStartup_ListTerminalsReportsTheFailureThatTheClientReceived is the
// terminal form of the test above. Terminals share failStartup, and
// ListTerminals reads the same registry.
func TestFailStartup_ListTerminalsReportsTheFailureThatTheClientReceived(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t)
	defer drainAllInFlight(svc)
	ctx := testutil.DeadlineContext(t)
	release := make(chan struct{})
	svc.startTerminalFn = func(context.Context, terminal.Options, terminal.OutputHandler, terminal.ExitHandler) error {
		<-release
		return errors.New("no such shell")
	}

	dispatch(d, "OpenTerminal", &leapmuxv1.OpenTerminalRequest{
		WorkingDir: t.TempDir(),
		Shell:      testutil.TestShell(),
	}, w)
	ids := collectTerminalIDs(w)
	require.Len(t, ids, 1)
	terminalID := ids[0]

	listed := make(chan *testResponseWriter, 1)
	registerTerminalWatch(svc, testChannelID, terminalID, leapmuxv1.WatchMode_WATCH_MODE_FULL, &finalStatusReader{
		testResponseWriter: newTestWriter(),
		isFinal: func(resp *leapmuxv1.WatchEventsResponse) bool {
			return resp.GetTerminalEvent().GetStatusChange().GetStatus() == leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED
		},
		read: func() {
			lw := newTestWriter()
			dispatch(d, "ListTerminals", &leapmuxv1.ListTerminalsRequest{TabIds: []string{terminalID}}, lw)
			listed <- lw
		},
	})
	close(release)

	lw := awaitListed(t, ctx, listed)
	var resp leapmuxv1.ListTerminalsResponse
	require.NoError(t, proto.Unmarshal(lw.responses[0].GetPayload(), &resp))
	require.Len(t, resp.GetTerminals(), 1)
	info := resp.GetTerminals()[0]
	assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED.String(), info.GetStatus().String(),
		"the client received STARTUP_FAILED, and ListTerminals still reported the startup in progress")
	assert.Contains(t, info.GetStartupError(), "no such shell")
}
