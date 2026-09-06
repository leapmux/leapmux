package cmd

import (
	"encoding/json"
	"errors"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// busyTerminal is a terminal the worker reports as running work.
func busyTerminal(id string, procs ...*leapmuxv1.TerminalProcess) *leapmuxv1.TerminalProcesses {
	return &leapmuxv1.TerminalProcesses{TerminalId: id, Processes: procs, TotalCount: int32(len(procs))}
}

func busyCloseHub() *recordingHub {
	return &recordingHub{
		listWorkers: []string{"worker-A"},
		materialized: newStateBuilder().
			workspace("ws-1", "root-1").
			leafNode("root-1", "", "m").
			tab("term-2", "root-1", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
			st,
	}
}

func runBusyTabClose(t *testing.T, disp *closeDispatcher, hub *recordingHub, extraArgs ...string) []byte {
	t.Helper()
	startSpawnIPC(t, hub, disp)
	args := append([]string{
		// term-2, not the caller's own agent-1: guardTabClose refuses a
		// self-close without --force, which would mask the refusal under test.
		"--tab-id", "term-2", "--tab-type", "terminal",
		"--workspace-id", "ws-1", "--worker-id", "worker-A",
	}, extraArgs...)
	return withCapturedStdout(t, func() { _ = RunTabClose(fakeCmdCtx{}, args) })
}

// A close that would interrupt running work must be refused BEFORE the CRDT
// tombstone. After the tombstone the tab is destroyed and the refusal has
// nowhere to go -- the same ordering rule the blocked-discard gate follows.
func TestRunTabClose_BusyTabRefusedBeforeTheTombstone(t *testing.T) {
	disp := &closeDispatcher{
		inspect:   &leapmuxv1.InspectLastTabCloseResponse{ShouldPrompt: false},
		terminals: []*leapmuxv1.TerminalProcesses{busyTerminal("term-2", &leapmuxv1.TerminalProcess{Pid: 51234, Name: "node"})},
	}
	hub := busyCloseHub()

	out := runBusyTabClose(t, disp, hub)

	var env struct {
		Error map[string]any `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	require.NotNil(t, env.Error, "a busy tab must fail the command")
	// A distinct code, so a script pattern-matches instead of grepping an
	// invalid_request blob.
	assert.Equal(t, "tab_busy_refused", env.Error["code"])
	assert.Contains(t, env.Error["message"], "term-2")
	assert.Contains(t, env.Error["message"], "node pid 51234",
		"the refusal must name what it is refusing to interrupt")
	assert.Contains(t, env.Error["message"], "pass --allow-busy to close anyway")

	assert.NotContains(t, hub.called(), "SubmitOps",
		"the refusal must land BEFORE the CRDT tombstone")
	assert.NotContains(t, disp.called(), "CloseTerminal",
		"and before the worker teardown")
}

// The mirror case. A gate that refused every close would pass the test above and
// break the feature, so this pins that the override actually overrides.
func TestRunTabClose_AllowBusyProceedsToTheTombstone(t *testing.T) {
	disp := &closeDispatcher{
		inspect:   &leapmuxv1.InspectLastTabCloseResponse{ShouldPrompt: false},
		terminals: []*leapmuxv1.TerminalProcesses{busyTerminal("term-2", &leapmuxv1.TerminalProcess{Pid: 51234, Name: "node"})},
	}
	hub := busyCloseHub()

	_ = runBusyTabClose(t, disp, hub, "--allow-busy")

	assert.Contains(t, hub.called(), "SubmitOps",
		"--allow-busy is an override; the close must reach the tombstone")
}

// An idle tab must not be asked about twice, nor refused.
func TestRunTabClose_IdleTabIsNotRefused(t *testing.T) {
	disp := &closeDispatcher{inspect: &leapmuxv1.InspectLastTabCloseResponse{ShouldPrompt: false}}
	hub := busyCloseHub()

	_ = runBusyTabClose(t, disp, hub)

	assert.Contains(t, hub.called(), "SubmitOps")
	assert.Contains(t, disp.called(), "InspectTerminalProcesses",
		"the guard still asks; it just finds nothing running")
}

// The two guards are mutually exclusive and the worktree one wins: its forced
// --worktree choice already makes the user state an intent for this close, and
// stacking a second required flag would send them back twice for one close.
func TestRunTabClose_LastTabPromptWinsOverTheBusyGate(t *testing.T) {
	disp := &closeDispatcher{
		inspect: &leapmuxv1.InspectLastTabCloseResponse{
			ShouldPrompt: true,
			Target:       leapmuxv1.LastTabCloseTarget_LAST_TAB_CLOSE_TARGET_WORKTREE,
			WorktreePath: "/tmp/wt",
		},
		terminals: []*leapmuxv1.TerminalProcesses{busyTerminal("term-2", &leapmuxv1.TerminalProcess{Pid: 1, Name: "node"})},
	}
	hub := busyCloseHub()

	out := runBusyTabClose(t, disp, hub)

	var env struct {
		Error map[string]any `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	assert.Equal(t, "invalid_request", env.Error["code"])
	assert.Contains(t, env.Error["message"], "pass --worktree=keep|push|discard")
	assert.NotContains(t, disp.called(), "InspectTerminalProcesses",
		"the busy probe must not even run when the worktree gate fires")
}

func TestAgentBusyDetail(t *testing.T) {
	t.Parallel()

	assert.Empty(t, agentBusyDetail(nil))
	assert.Empty(t, agentBusyDetail(&leapmuxv1.AgentInfo{ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_IDLE, ActiveBackgroundTasks: 3}),
		"an idle agent is idle whatever its registry holds")
	assert.Equal(t, "agent turn is in progress", agentBusyDetail(&leapmuxv1.AgentInfo{ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING}))

	// The turn is claimed only when nothing else explains the busy state. The
	// Worker reports a root busy for a turn OR for a running background task and
	// sends only the answer, so a turn that ended while a subagent kept running
	// would otherwise be reported as in progress after it finished.
	assert.Equal(t, "agent is working, 1 background task active",
		agentBusyDetail(&leapmuxv1.AgentInfo{ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING, ActiveBackgroundTasks: 1}))
	assert.Equal(t, "agent is working, 2 background tasks active",
		agentBusyDetail(&leapmuxv1.AgentInfo{ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING, ActiveBackgroundTasks: 2}))

	// WAITING_FOR_USER is the state the old single boolean could not express. The
	// indicator does not spin -- the user is looking straight at the prompt --
	// but the turn IS in flight, and closing the tab kills it along with every
	// background task under it. Reading one "not working" flag let that close
	// through unwarned.
	assert.Equal(t, "agent turn is waiting for a permission answer",
		agentBusyDetail(&leapmuxv1.AgentInfo{
			ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER,
		}))
	assert.Equal(t, "agent turn is waiting for a permission answer, 2 background tasks active",
		agentBusyDetail(&leapmuxv1.AgentInfo{
			ActivityState:         leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER,
			ActiveBackgroundTasks: 2,
		}))

	// A SUBAGENT tab closes in the UI only: CloseAgent on a child keeps the row
	// and the transcript and stops no process. Nothing is interrupted, whatever
	// the child's own registry row says, so refusing the close would fail a
	// script over work the close never touches. The browser's probe carves out
	// the same case.
	assert.Empty(t, agentBusyDetail(&leapmuxv1.AgentInfo{
		ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING, ActiveBackgroundTasks: 1, ParentAgentId: "root-1",
	}), "closing a subagent tab stops nothing")
}

func TestTerminalBusyDetail(t *testing.T) {
	t.Parallel()

	assert.Empty(t, terminalBusyDetail(busyTerminal("t1")))
	assert.Equal(t, "1 process running (node pid 7)",
		terminalBusyDetail(busyTerminal("t1", &leapmuxv1.TerminalProcess{Pid: 7, Name: "node"})))
	assert.Equal(t, "2 processes running (node pid 7, esbuild pid 8)",
		terminalBusyDetail(busyTerminal("t1",
			&leapmuxv1.TerminalProcess{Pid: 7, Name: "node"},
			&leapmuxv1.TerminalProcess{Pid: 8, Name: "esbuild"})))

	// macOS resolves a long name through a second syscall that fails for another
	// user's process, so the worker reports the pid alone.
	assert.Equal(t, "1 process running (unnamed pid 9)",
		terminalBusyDetail(busyTerminal("t1", &leapmuxv1.TerminalProcess{Pid: 9})))

	capped := busyTerminal("t1", &leapmuxv1.TerminalProcess{Pid: 7, Name: "cc"})
	capped.TotalCount = 48
	assert.Equal(t, "1 process running (cc pid 7) and 47 more", terminalBusyDetail(capped))
}

func TestErrTabBusyRefused_NamesEveryBusyTab(t *testing.T) {
	t.Parallel()

	out := withCapturedStdout(t, func() {
		_ = errTabBusyRefused([]tabBusyReason{
			{tabID: "agent-1", detail: "agent turn is in progress"},
			{tabID: "term-2", detail: "1 process running (node pid 7)"},
		})
	})

	var env struct {
		Error map[string]any `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	assert.Equal(t, "tab_busy_refused", env.Error["code"])
	msg, _ := env.Error["message"].(string)
	assert.Contains(t, msg, "2 busy tabs")
	assert.Contains(t, msg, "agent-1 (agent turn is in progress)")
	assert.Contains(t, msg, "term-2 (1 process running (node pid 7))")
	assert.Contains(t, msg, "pass --allow-busy to close anyway")
}

// The agent half of the same guard. `tab close` on an AGENT reads AgentInfo.busy
// off the row ListAgents already returns, so a busy agent takes an entirely
// different round trip from a busy terminal and needs its own end-to-end case.
func TestRunTabClose_BusyAgentTabRefused(t *testing.T) {
	disp := &closeDispatcher{
		inspect: &leapmuxv1.InspectLastTabCloseResponse{ShouldPrompt: false},
		agents: []*leapmuxv1.AgentInfo{
			{Id: "agent-2", ActivityState: leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING, ActiveBackgroundTasks: 2},
		},
	}
	hub := &recordingHub{
		listWorkers: []string{"worker-A"},
		materialized: newStateBuilder().
			workspace("ws-1", "root-1").
			leafNode("root-1", "", "m").
			tab("agent-2", "root-1", leapmuxv1.TabType_TAB_TYPE_AGENT).
			st,
	}
	startSpawnIPC(t, hub, disp)

	// agent-2, not the caller's own agent-1: guardTabClose refuses a self-close
	// without --force, which would mask the refusal under test.
	out := withCapturedStdout(t, func() {
		_ = RunTabClose(fakeCmdCtx{}, []string{
			"--tab-id", "agent-2", "--tab-type", "agent",
			"--workspace-id", "ws-1", "--worker-id", "worker-A",
		})
	})

	var env struct {
		Error map[string]any `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	require.NotNil(t, env.Error)
	assert.Equal(t, "tab_busy_refused", env.Error["code"])
	assert.Contains(t, env.Error["message"], "agent is working, 2 background tasks active")
	assert.NotContains(t, hub.called(), "SubmitOps",
		"the refusal must land BEFORE the CRDT tombstone")
}

// FAILS OPEN. A guard that cannot get an answer must let the close through: the
// alternative strands a tab the user has no other way to shut. Both halves are
// covered, because each is a separate call that can fail on its own.
func TestInspectTabsBusy_FailsOpenWhenTheWorkerCannotAnswer(t *testing.T) {
	t.Parallel()

	var asked []string
	call := func(method string, _, _ proto.Message) error {
		asked = append(asked, method)
		return errors.New("worker unreachable")
	}

	busy := inspectTabsBusy(call, []tabRef{
		{tabType: leapmuxv1.TabType_TAB_TYPE_AGENT, tabID: "agent-2"},
		{tabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, tabID: "term-2"},
	})

	assert.Empty(t, busy, "an unanswerable probe reports nothing busy")
	assert.ElementsMatch(t, []string{"ListAgents", "InspectTerminalProcesses"}, asked)
}

// A viewer tab stops nothing when it closes, so the guard spends no round trip
// on one.
func TestInspectTabsBusy_SkipsViewerTabs(t *testing.T) {
	t.Parallel()

	var asked []string
	call := func(method string, _, _ proto.Message) error {
		asked = append(asked, method)
		return nil
	}

	busy := inspectTabsBusy(call, []tabRef{
		{tabType: leapmuxv1.TabType_TAB_TYPE_FILE, tabID: "file-1"},
		{tabType: leapmuxv1.TabType_TAB_TYPE_IMAGE, tabID: "img-1"},
	})

	assert.Empty(t, busy)
	assert.Empty(t, asked, "a FILE or IMAGE tab is a viewer; nothing to ask about")
}

// --- tile close ------------------------------------------------------------
//
// `tile close --with-tabs=close` tombstones every tab on the tile in the CRDT
// and NEVER calls the worker, so nothing else on its path would notice running
// work. It is the CLI's analogue of the browser's tile close, which asks once
// up front about every busy tab -- and this pair pins that it asks in the same
// place, before any op is submitted.

// tabOnWorker is `tab` plus the worker id. guardTabsBusy groups its questions
// per worker and skips a tab whose worker is unknown, so a tile-close test must
// state it; the plain helper leaves it nil, which is right for the preflight
// tests that never ask a worker anything.
func (b *stateBuilder) tabOnWorker(tabID, tileID, workerID string, tabType leapmuxv1.TabType) *stateBuilder {
	b.tab(tabID, tileID, tabType)
	b.st.Tabs[tabID].WorkerId = &leapmuxv1.LWWString{Value: workerID}
	return b
}

func busyTileHub() *recordingHub {
	return &recordingHub{
		listWorkers: []string{"worker-A"},
		materialized: newStateBuilder().
			workspace("ws-1", "root-1").
			splitNode("root-1", "", "m").
			// leaf-2 holds the tabs; the caller's own agent-1 sits elsewhere, or
			// guardTileClose would refuse the self-close first and mask this gate.
			leafNode("leaf-1", "root-1", "h").
			leafNode("leaf-2", "root-1", "s").
			tabOnWorker("agent-1", "leaf-1", "worker-A", leapmuxv1.TabType_TAB_TYPE_AGENT).
			tabOnWorker("term-2", "leaf-2", "worker-A", leapmuxv1.TabType_TAB_TYPE_TERMINAL).
			st,
	}
}

func runBusyTileClose(t *testing.T, disp *closeDispatcher, hub *recordingHub, extraArgs ...string) []byte {
	t.Helper()
	startSpawnIPC(t, hub, disp)
	args := append([]string{
		"--tile-id", "leaf-2", "--workspace-id", "ws-1", "--with-tabs", "close",
	}, extraArgs...)
	return withCapturedStdout(t, func() { _ = RunTileClose(fakeCmdCtx{}, args) })
}

func TestRunTileClose_BusyTabRefusedBeforeAnyOp(t *testing.T) {
	disp := &closeDispatcher{
		terminals: []*leapmuxv1.TerminalProcesses{busyTerminal("term-2", &leapmuxv1.TerminalProcess{Pid: 51234, Name: "node"})},
	}
	hub := busyTileHub()

	out := runBusyTileClose(t, disp, hub)

	var env struct {
		Error map[string]any `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	require.NotNil(t, env.Error, "a tile holding running work must fail the command")
	assert.Equal(t, "tab_busy_refused", env.Error["code"])
	assert.Contains(t, env.Error["message"], "term-2")
	assert.Contains(t, env.Error["message"], "node pid 51234")
	assert.NotContains(t, hub.called(), "SubmitOps",
		"the refusal must land before the tile and its tabs are tombstoned")
}

func TestRunTileClose_AllowBusyProceeds(t *testing.T) {
	disp := &closeDispatcher{
		terminals: []*leapmuxv1.TerminalProcesses{busyTerminal("term-2", &leapmuxv1.TerminalProcess{Pid: 51234, Name: "node"})},
	}
	hub := busyTileHub()

	_ = runBusyTileClose(t, disp, hub, "--allow-busy")

	assert.Contains(t, hub.called(), "SubmitOps",
		"--allow-busy is an override; the close must reach the tombstone")
}

// --with-tabs=move migrates the tabs to the heir tile: every agent keeps running
// and every PTY survives, so there is nothing to interrupt and nothing to ask.
func TestRunTileClose_MoveSkipsTheBusyGate(t *testing.T) {
	disp := &closeDispatcher{
		terminals: []*leapmuxv1.TerminalProcesses{busyTerminal("term-2", &leapmuxv1.TerminalProcess{Pid: 51234, Name: "node"})},
	}
	hub := busyTileHub()

	startSpawnIPC(t, hub, disp)
	_ = withCapturedStdout(t, func() {
		_ = RunTileClose(fakeCmdCtx{}, []string{
			"--tile-id", "leaf-2", "--workspace-id", "ws-1", "--with-tabs", "move",
		})
	})

	assert.NotContains(t, disp.called(), "InspectTerminalProcesses",
		"a move interrupts nothing, so the guard must not even ask")
}
