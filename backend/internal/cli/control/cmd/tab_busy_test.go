package cmd

import (
	"encoding/json"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// The two gates are mutually exclusive and the worktree one wins: its forced
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
	assert.Empty(t, agentBusyDetail(&leapmuxv1.AgentInfo{Busy: false, ActiveBackgroundTasks: 3}),
		"an idle agent is idle whatever its registry holds")
	assert.Equal(t, "agent turn is in progress", agentBusyDetail(&leapmuxv1.AgentInfo{Busy: true}))
	assert.Equal(t, "agent turn is in progress, 1 background task active",
		agentBusyDetail(&leapmuxv1.AgentInfo{Busy: true, ActiveBackgroundTasks: 1}))
	assert.Equal(t, "agent turn is in progress, 2 background tasks active",
		agentBusyDetail(&leapmuxv1.AgentInfo{Busy: true, ActiveBackgroundTasks: 2}))
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
