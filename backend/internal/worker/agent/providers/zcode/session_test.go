package zcode

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

func TestZCodeIsSessionGoneRequiresTheCurrentMissingSessionError(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		err  error
		gone bool
	}{
		{name: "the session is absent", err: &zcodeError{Code: ErrSessionNotActive}, gone: true},
		{name: "a required method is absent", err: &zcodeError{Code: ErrMethodNotFound}},
		{name: "another native refusal", err: &zcodeError{Code: ErrInternal}},
		{name: "a transport failure", err: errors.New("transport closed")},
		{name: "no failure"},
	} {
		t.Run(test.name, func(t *testing.T) { t.Parallel(); assert.Equal(t, test.gone, zcodeIsSessionGone(test.err)) })
	}
}

func TestZCodeClearContextClosesBackgroundSubagentsAndShells(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	stdin := &zcodeRecordedStdin{}
	calls := &zcodeChildLifecycleCalls{ProviderServices: agent.NewProviderServices(sink)}
	a := newZCodeTestAgentWithStdin(t, calls, stdin)
	zcodeStartTestTurn(t, a, `{"turnNumber":1,"input":"hi"}`)

	// Open a background subagent (whose spawn tool call has already finished with launch ACK).
	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventToolUpdated, zcodeSpawnScheduled))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventSessionUpdated, zcodeBackgroundSubagentTask("running")))
	a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventToolUpdated,
		`{"kind":"result","toolCallId":"spawn-1","result":{"success":true,"content":"Async agent launched successfully. (This tool result is internal metadata — never quote or paste any part of it, including the agentId below, into a user-facing reply.)\nagentId: agent_1 (internal ID - do not mention to user. Use SendMessage with to: 'agent_1', summary: '<5-10 word recap>' to continue this agent.)\nThe agent is working in the background. You will be notified automatically when it completes. You know nothing about its results until that notification arrives — do not report, assume, or predict them; continue other work or respond to the user in the meantime.\nIn your own words, briefly tell the user what you launched — do not echo this tool result. Agent results will arrive in a subsequent message. If the user asks for progress, say the agent is still running."}}`))

	// Open a background shell task.
	a.HandleOutput(zcodeEventLine(t, 4, contracts.ZCodeEventSessionUpdated,
		`{"taskId":"shell-task-1","toolCallId":"shell-call-1","toolName":"Bash","taskKind":"bash","command":"sleep 60","status":"running"}`))

	subRow, ok := sink.BackgroundTask("sess_subagent_agent_1")
	require.True(t, ok)
	require.Equal(t, bgtask.StatusRunning, subRow.Status)
	shellRow, ok := sink.BackgroundTask("shell-call-1")
	require.True(t, ok)
	require.Equal(t, bgtask.StatusRunning, shellRow.Status)

	answerZCodeRequest(t, a, stdin, MethodSessionCreate, `{"session":{"sessionId":"sess-2"},"settings":{"mode":{"current":"build"}}}`)
	answerZCodeRequest(t, a, stdin, MethodSessionSubscribe, `{"sessionId":"sess-2","eventSeq":0,"events":[]}`)

	sessionID, err := a.ClearContext()
	require.NoError(t, err)
	assert.Equal(t, "sess-2", sessionID)

	subRow, ok = sink.BackgroundTask("sess_subagent_agent_1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusStopped, subRow.Status, "background subagent must close on ClearContext")

	shellRow, ok = sink.BackgroundTask("shell-call-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusStopped, shellRow.Status, "background shell must close on ClearContext")

	assert.Contains(t, calls.snapshot(), "cleanup", "background subagent child must be cleaned up on ClearContext")
}
