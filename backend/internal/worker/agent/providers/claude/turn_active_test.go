package claude

import (
	"bytes"
	"testing"

	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newClaudeAgentWithStdin(sink agent.ProviderServices) (*Agent, *bytes.Buffer) {
	var buf bytes.Buffer
	a := &Agent{
		Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{
			AgentID: "test-agent",
			Stdin:   agenttest.NopStdin(&buf),
		}),
		sink: sink,
	}
	return a, &buf
}

func TestClaudeTurnActive_SendInputOpensAndResultCloses(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a, _ := newClaudeAgentWithStdin(agent.NewProviderServices(sink))

	require.NoError(t, a.SendInput("hello", nil))
	assert.Equal(t, []bool{true}, sink.TurnActives(), "the turn opens once the message is on stdin")

	a.HandleOutput([]byte(`{"type":"result","subtype":"success"}`))

	assert.Equal(t, []bool{true, false}, sink.TurnActives())
}

func TestClaudeTurnActive_ErroredAndInterruptedTurnsAlsoClose(t *testing.T) {
	t.Parallel()

	// Claude ends an interrupted or failed turn with a `result` envelope too, so
	// the one clear site covers those paths. Were that not true, cancelling a
	// runaway agent would leave it looking busy forever -- with the Interrupt
	// button still showing and nothing left to press it for.
	for _, envelope := range []string{
		`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"boom"}`,
		`{"type":"result","subtype":"success","result":"Interrupted by user"}`,
	} {
		sink := &agenttest.Sink{}
		a, _ := newClaudeAgentWithStdin(agent.NewProviderServices(sink))
		require.NoError(t, a.SendInput("hello", nil))

		a.HandleOutput([]byte(envelope))

		last, published := sink.LastTurnActive()
		require.True(t, published)
		assert.False(t, last, "envelope %s must end the turn", envelope)
	}
}

func TestClaudeTurnActive_FailedStdinWriteStartsNoTurn(t *testing.T) {
	t.Parallel()

	// A write that failed delivered nothing, so no turn began and no envelope is
	// coming to end one. Marking it busy here would latch the agent forever.
	sink := &agenttest.Sink{}
	a := &Agent{
		Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "test-agent", Stdin: agenttest.FailingStdin{}}),
		sink:    agent.NewProviderServices(sink),
	}

	require.Error(t, a.SendInput("hello", nil))

	assert.Equal(t, []bool{false}, sink.TurnActives(),
		"the publish re-reads the flag, so a failed send republishes idle rather than opening a turn")
}

func TestClaudeTurnActive_SubagentResultLeavesTheRootTurnOpen(t *testing.T) {
	t.Parallel()

	// A forwarded subagent envelope carries parent_tool_use_id and routes into
	// the child's transcript. The root is still working, and a child finishing
	// must not clear the root's turn.
	sink := &agenttest.Sink{}
	a, _ := newClaudeAgentWithStdin(agent.NewProviderServices(sink))
	require.NoError(t, a.SendInput("hello", nil))

	a.HandleOutput([]byte(`{"type":"result","parent_tool_use_id":"parent-1","subtype":"success"}`))

	assert.Equal(t, []bool{true}, sink.TurnActives(), "only the ROOT's own result ends the root's turn")
}

// reset_spans is in the sequence for the same reason turn_end is: the clear
// releases the Worker's input queue, so the next message dispatches on it and
// must find the finished turn's spans already reset. A provider that publishes
// the clear before ResetSpans draws the dead turn's bars beside that message.
func TestTurnEndPrecedesTheClear_Claude(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a, _ := newClaudeAgentWithStdin(agent.NewProviderServices(sink))
	require.NoError(t, a.SendInput("hello", nil))

	a.HandleOutput([]byte(`{"type":"result","subtype":"success","num_tool_uses":2}`))

	assert.Equal(t, []string{"turn_active:true", "turn_end", "reset_spans", "turn_active:false"}, sink.TurnLifecycle())
}

func TestClaudeTurnActive_RootAssistantOutputArmsATurnTheWorkerDidNotStart(t *testing.T) {
	t.Parallel()

	// Claude Code emits no turn-start frame, so the Worker used to learn of a
	// turn only from its own SendInput. A turn the CLI runs by itself -- one it
	// continues after the `result` the Worker already consumed -- then left the
	// flag clear, and the next queued message went straight into that running
	// turn.
	sink := &agenttest.Sink{}
	a, _ := newClaudeAgentWithStdin(agent.NewProviderServices(sink))

	a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"still working"}]}}`))

	assert.Equal(t, []bool{true}, sink.TurnActives(), "root assistant output means a turn is in flight")
	assert.ErrorIs(t, a.SendInput("second", nil), agent.ErrAgentBusy)

	a.HandleOutput([]byte(`{"type":"result","subtype":"success"}`))
	// The refused send republishes the unchanged flag, which is what reconciles
	// a queue that dispatched into it. The clear that follows is the result's.
	assert.Equal(t, []bool{true, true, false}, sink.TurnActives(),
		"the CLI's own result still ends the turn")
}

func TestClaudeTurnActive_RootAssistantOutputPublishesOncePerTurn(t *testing.T) {
	t.Parallel()

	// Every assistant message of one turn reaches this path. Only the first
	// arms anything, so a streaming turn does not republish -- and does not
	// reconcile the Worker's input queue -- once per message block.
	sink := &agenttest.Sink{}
	a, _ := newClaudeAgentWithStdin(agent.NewProviderServices(sink))

	for range 3 {
		a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"chunk"}]}}`))
	}

	assert.Equal(t, []bool{true}, sink.TurnActives())
}

func TestClaudeTurnActive_EveryRootFrameOfALiveTurnArmsIt(t *testing.T) {
	t.Parallel()

	// The frames that prove a live turn EARLIEST are the ones that return before
	// the persist path: the thinking-token telemetry, and a root user
	// tool_result. Arming from the assistant message alone left the whole
	// extended-thinking window of a CLI-run turn invisible, and a message the
	// user sent inside it went straight into that turn.
	for _, tc := range []struct {
		name string
		line string
	}{
		{
			name: "thinking tokens",
			line: `{"type":"system","subtype":"thinking_tokens","thinking_tokens":120}`,
		},
		{
			name: "root user tool_result",
			line: `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`,
		},
		{
			name: "root user text echo",
			line: `{"type":"user","message":{"role":"user","content":"hello"}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sink := &agenttest.Sink{}
			a, _ := newClaudeAgentWithStdin(agent.NewProviderServices(sink))

			a.HandleOutput([]byte(tc.line))

			assert.Equal(t, []bool{true}, sink.TurnActives(),
				"a root frame only a live turn produces arms the turn")
			assert.ErrorIs(t, a.SendInput("second", nil), agent.ErrAgentBusy)
		})
	}
}

func TestClaudeTurnActive_TheWorkersOwnTrafficArmsNothing(t *testing.T) {
	t.Parallel()

	// A result ENDS a turn, and the control frames are the Worker talking to
	// itself. Neither is evidence that the CLI is working.
	for _, tc := range []struct {
		name string
		line string
	}{
		{name: "result", line: `{"type":"result","subtype":"success"}`},
		{name: "control_response", line: `{"type":"control_response","response":{"request_id":"r1","subtype":"success"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sink := &agenttest.Sink{}
			a, _ := newClaudeAgentWithStdin(agent.NewProviderServices(sink))

			a.HandleOutput([]byte(tc.line))

			assert.NotContains(t, sink.TurnActives(), true,
				"no turn is in flight because of this frame")
		})
	}
}

func TestClaudeTurnActive_SubagentAssistantOutputArmsNoRootTurn(t *testing.T) {
	t.Parallel()

	// A forwarded subagent envelope carries parent_tool_use_id and belongs to
	// the child's transcript. It says nothing about the root, which may well be
	// idle while a restarted subagent runs on.
	sink := &agenttest.Sink{}
	a, _ := newClaudeAgentWithStdin(agent.NewProviderServices(sink))

	a.HandleOutput([]byte(`{"type":"assistant","parent_tool_use_id":"parent-1","message":{"role":"assistant","content":[{"type":"text","text":"child"}]}}`))

	assert.Empty(t, sink.TurnActives(), "only ROOT output arms the root's turn")
}

func TestClaudeTurnActive_ASystemFrameOutsideATurnArmsNothing(t *testing.T) {
	t.Parallel()

	// `system` carries four families and only the thinking-token telemetry
	// reports the root's own work. Arming from the whole type latched a turn
	// that no `result` ever ends, and BOTH consumers of the flag then broke for
	// the rest of the process: the input queue held every message the user sent
	// -- unpaused, with nothing to drain it -- and the client ran a thinking
	// indicator for an agent that does nothing.
	//
	// The startup init frame is the one every session emits, so a new agent tab
	// reached that state before the user typed anything.
	for _, tc := range []struct {
		name string
		line string
	}{
		{
			name: "startup init",
			line: `{"type":"system","subtype":"init","session_id":"s-1","slash_commands":["clear","compact"]}`,
		},
		{
			name: "background task progress",
			line: `{"type":"system","subtype":"task_progress","task_id":"t-1","description":"npm test"}`,
		},
		{
			name: "background task notification",
			line: `{"type":"system","subtype":"task_notification","task_id":"t-1","status":"completed"}`,
		},
		{
			name: "background tasks changed",
			line: `{"type":"system","subtype":"background_tasks_changed","tasks":[]}`,
		},
		{
			name: "notification-threaded status",
			line: `{"type":"system","subtype":"status","status":"compacting"}`,
		},
		{
			name: "status clear",
			line: `{"type":"system","subtype":"status","status":""}`,
		},
		{
			name: "api retry",
			line: `{"type":"system","subtype":"api_retry","attempt":2}`,
		},
		{
			name: "compaction boundary",
			line: `{"type":"system","subtype":"compact_boundary","compact_metadata":{"trigger":"auto"}}`,
		},
		{
			name: "unknown subtype",
			line: `{"type":"system","subtype":"some_future_event"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sink := &agenttest.Sink{}
			a, _ := newClaudeAgentWithStdin(agent.NewProviderServices(sink))

			a.HandleOutput([]byte(tc.line))

			assert.NotContains(t, sink.TurnActives(), true, "this frame runs no turn")
			assert.NoError(t, a.SendInput("first", nil), "the next message must reach the CLI")
		})
	}
}

func TestClaudeTurnActive_TheCLIsOwnSessionStateDecides(t *testing.T) {
	t.Parallel()

	// With claudeSessionStateEnv set, Claude Code publishes its own turn state.
	// That frame is authoritative and the output heuristic never sees it:
	// `running` opens the turn before any assistant message, `requires_action`
	// reports a turn that a permission prompt blocks, and `idle` ends it after
	// the last result flushes.
	for _, tc := range []struct {
		name string
		line string
		want []bool
	}{
		{
			name: "running opens the turn",
			line: `{"type":"system","subtype":"session_state_changed","state":"running"}`,
			want: []bool{true},
		},
		{
			name: "requires_action is a turn a prompt blocks",
			line: `{"type":"system","subtype":"session_state_changed","state":"requires_action"}`,
			want: []bool{true},
		},
		{
			name: "an unknown state decides nothing",
			line: `{"type":"system","subtype":"session_state_changed","state":"quiescing"}`,
			want: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sink := &agenttest.Sink{}
			a, _ := newClaudeAgentWithStdin(agent.NewProviderServices(sink))

			a.HandleOutput([]byte(tc.line))

			assert.Equal(t, tc.want, sink.TurnActives())
		})
	}
}

func TestClaudeTurnActive_SessionStateIdleEndsATurnWithNoResult(t *testing.T) {
	t.Parallel()

	// The idle frame is the recovery this provider otherwise has none of. A turn
	// the CLI runs is normally ended by its `result`, and a turn armed with no
	// result behind it would hold the input queue until the process exits. The
	// CLI's own idle answers that case.
	sink := &agenttest.Sink{}
	a, _ := newClaudeAgentWithStdin(agent.NewProviderServices(sink))

	a.HandleOutput([]byte(`{"type":"system","subtype":"session_state_changed","state":"running"}`))
	require.ErrorIs(t, a.SendInput("second", nil), agent.ErrAgentBusy)

	a.HandleOutput([]byte(`{"type":"system","subtype":"session_state_changed","state":"idle"}`))

	assert.Equal(t, []bool{true, true, false}, sink.TurnActives(),
		"the refused send republishes the flag; idle then clears it")
	assert.NoError(t, a.SendInput("second", nil), "the queue dispatches again")
}

func TestClaudeLaunchEnv_CarriesTheTurnSignalOptIn(t *testing.T) {
	t.Parallel()

	// Claude Code publishes session_state_changed for this variable alone. A
	// launch that drops it silently falls back to the output heuristic, and
	// nothing else reports the loss.
	t.Run("a plain launch", func(t *testing.T) {
		t.Parallel()

		env := claudeAgentEnv([]string{"PATH=/usr/bin"}, false)

		assert.Contains(t, env, "CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS=1")
		assert.Contains(t, env, "CLAUDE_CODE_ENTRYPOINT=cli")
		assert.Contains(t, env, "PATH=/usr/bin", "the inherited environment survives")
		assert.False(t, envutil.HasKey(env, "CLAUDECODE"),
			"only a login shell needs the rc-file marker")
	})

	t.Run("a login shell", func(t *testing.T) {
		t.Parallel()

		env := claudeAgentEnv([]string{"PATH=/usr/bin"}, true)

		assert.Contains(t, env, "CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS=1")
		assert.Equal(t, []string{"1"}, envutil.ValuesFor(env, "CLAUDECODE"))
	})

	// A worker that a Claude Code session itself launched inherits all three
	// markers, and each inherited value is the PARENT's. A pin that layers
	// instead of replacing still reaches the CLI with the right value, because
	// exec resolves a duplicate last-wins -- and leaves an environment that says
	// two things, which is what makes the layering invisible until something
	// else reads it.
	t.Run("an inherited value is replaced, not layered", func(t *testing.T) {
		t.Parallel()

		env := claudeAgentEnv([]string{
			"PATH=/usr/bin",
			"CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS=0",
			"CLAUDE_CODE_ENTRYPOINT=sdk-ts",
			"CLAUDECODE=1",
		}, false)

		assert.Equal(t, []string{"1"}, envutil.ValuesFor(env, "CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS"))
		assert.Equal(t, []string{"cli"}, envutil.ValuesFor(env, "CLAUDE_CODE_ENTRYPOINT"))
		assert.Empty(t, envutil.ValuesFor(env, "CLAUDECODE"),
			"the inherited marker is stripped, and only a login shell re-adds it")
	})

	// The inherited environment is the caller's slice. Building the launch env
	// by appending to it writes into the spare capacity that slice owns, so a
	// second launch overwrites what the first one holds.
	t.Run("two launches do not share a backing array", func(t *testing.T) {
		t.Parallel()

		inherited := make([]string, 0, 8)
		inherited = append(inherited, "PATH=/usr/bin")

		plain := claudeAgentEnv(inherited, false)
		login := claudeAgentEnv(inherited, true)

		assert.Contains(t, plain, "CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS=1")
		assert.False(t, envutil.HasKey(plain, "CLAUDECODE"), "the second launch wrote over the first")
		assert.Equal(t, []string{"1"}, envutil.ValuesFor(login, "CLAUDECODE"))
		assert.Equal(t, []string{"PATH=/usr/bin"}, inherited, "the caller's environment is unchanged")
	})
}

func TestClaudeTurnActive_ResultStillEndsATurnTheCLIOpened(t *testing.T) {
	t.Parallel()

	// Retiring the output heuristic must not retire the falling edge. `result`
	// ends every Claude turn and arrives BEFORE the idle that reports it, so the
	// Worker's input queue opens on the result -- one frame earlier than the
	// CLI's own report, and on the frame every build sends.
	sink := &agenttest.Sink{}
	a, _ := newClaudeAgentWithStdin(agent.NewProviderServices(sink))

	a.HandleOutput([]byte(`{"type":"system","subtype":"session_state_changed","state":"running"}`))
	a.HandleOutput([]byte(`{"type":"result","subtype":"success"}`))

	assert.Equal(t, []bool{true, false}, sink.TurnActives())
	assert.NoError(t, a.SendInput("next", nil), "the queue dispatches on the result")
}

func TestClaudeTurnActive_AForwardedChildStateDecidesNothing(t *testing.T) {
	t.Parallel()

	// A forwarded subagent envelope carries parent_tool_use_id, and the child's
	// turn is not the root's: a restarted subagent runs on while the root sits
	// idle. So a child's copy of the frame decides nothing -- for the idle, the
	// clear it would publish is exactly the failure the turn flag exists to
	// prevent, because the input queue would then dispatch into the root's
	// running turn.
	t.Run("a child idle leaves the root's turn running", func(t *testing.T) {
		t.Parallel()

		sink := &agenttest.Sink{}
		a, _ := newClaudeAgentWithStdin(agent.NewProviderServices(sink))

		a.HandleOutput([]byte(`{"type":"system","subtype":"session_state_changed","state":"running"}`))
		a.HandleOutput([]byte(`{"type":"system","parent_tool_use_id":"p1","subtype":"session_state_changed","state":"idle"}`))

		assert.Equal(t, []bool{true}, sink.TurnActives(), "only the root's own state moves the flag")
		assert.ErrorIs(t, a.SendInput("next", nil), agent.ErrAgentBusy)
	})

	t.Run("a child state does not retire the heuristic", func(t *testing.T) {
		t.Parallel()

		sink := &agenttest.Sink{}
		a, _ := newClaudeAgentWithStdin(agent.NewProviderServices(sink))

		a.HandleOutput([]byte(`{"type":"system","parent_tool_use_id":"p1","subtype":"session_state_changed","state":"running"}`))
		require.Empty(t, sink.TurnActives(), "a child's frame arms nothing")

		// The ROOT has still stated nothing, so its assistant message is still
		// the evidence that a turn runs. A child frame that retired the
		// heuristic would leave this build with no rising edge at all.
		a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`))

		assert.Equal(t, []bool{true}, sink.TurnActives())
	})
}

func TestClaudeTurnActive_AStaleSessionIdleDoesNotOpenTheNextTurn(t *testing.T) {
	t.Parallel()

	// The CLI emits idle when its run loop stops, BEFORE it reads the next
	// message. The Worker dispatches that message off the turn end it already
	// saw, so its send can land between the two frames -- and an idle applied
	// after it would clear the turn that message just opened. The input queue
	// follows this flag, so the next message would then dispatch INTO the
	// running turn.
	sink := &agenttest.Sink{}
	a, _ := newClaudeAgentWithStdin(agent.NewProviderServices(sink))

	require.NoError(t, a.SendInput("first", nil))
	a.HandleOutput([]byte(`{"type":"result","subtype":"success"}`))
	// The queue drains on that turn end and hands the CLI the next message.
	require.NoError(t, a.SendInput("second", nil))

	// The previous turn's idle arrives now.
	a.HandleOutput([]byte(`{"type":"system","subtype":"session_state_changed","state":"idle"}`))

	assert.Equal(t, []bool{true, false, true}, sink.TurnActives(),
		"the stale idle publishes nothing")
	assert.ErrorIs(t, a.SendInput("third", nil), agent.ErrAgentBusy,
		"the second turn is still in flight")

	// The result for the second message ends it, and a later idle is current
	// again. The refused send above republished the unchanged flag, which is
	// what reconciles a queue that dispatched into the turn.
	a.HandleOutput([]byte(`{"type":"result","subtype":"success"}`))
	a.HandleOutput([]byte(`{"type":"system","subtype":"session_state_changed","state":"idle"}`))
	assert.Equal(t, []bool{true, false, true, true, false, false}, sink.TurnActives())
}

func TestClaudeTurnActive_TheHeuristicRetiresOnceTheCLIStatesItsOwnTurn(t *testing.T) {
	t.Parallel()

	// The output heuristic exists for a CLI that publishes no
	// session_state_changed frame. A CLI that publishes one has stated the
	// answer, so nothing else is read as evidence for the life of the process --
	// and the arming surface shrinks to the named signals, which is what keeps a
	// frame the vendor adds later inert.
	sink := &agenttest.Sink{}
	a, _ := newClaudeAgentWithStdin(agent.NewProviderServices(sink))

	a.HandleOutput([]byte(`{"type":"system","subtype":"session_state_changed","state":"idle"}`))
	require.Equal(t, []bool{false}, sink.TurnActives())

	// A root assistant frame is the heuristic's strongest evidence. It arms
	// nothing here: this CLI reports `running` when a turn starts.
	a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`))

	assert.Equal(t, []bool{false}, sink.TurnActives(), "the CLI's own state is the only source now")
	assert.NoError(t, a.SendInput("next", nil), "the queue still dispatches")
}

func TestClaudeTurnActive_IssuesRisingOrderingTokens(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a, _ := newClaudeAgentWithStdin(agent.NewProviderServices(sink))
	agenttest.AssertRisingTurnTokens(t, sink, a)
}
