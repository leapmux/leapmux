package claude

import (
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A `permission_check_status` frame is live state, not timeline content. It
// drives the "Checking permission..." badge on the tool card of the call it
// identifies, and it must never persist: a stored row would draw a no-display card
// for a fact the card already shows.

func TestHandleOutput_PermissionCheckStatusCheckingBroadcastsLive(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	sink.UpdateSessionID("root-session")
	sessionIDCalls := sink.SessionIDCount()

	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "permission_check_status",
		"tool_use_id": "toolu_1",
		"status": "checking",
		"uuid": "u-1",
		"session_id": "s-1"
	}`))

	update := runningToolUpdate(t, sink)
	assert.Equal(t, "toolu_1", update[contracts.RunningToolFieldSpanId])
	assert.Equal(t, "root-session", update[contracts.RunningToolFieldAgentSessionId],
		"the broadcast states the root session the sink stamps on its rows")
	assert.Equal(t, true, update[contracts.RunningToolFieldPermissionCheck])

	assert.Equal(t, 0, sink.MessageCount(), "permission_check_status must not be persisted")
	assert.Equal(t, 0, sink.NotificationCount(), "permission_check_status must not be notification-threaded")
	assert.Equal(t, sessionIDCalls, sink.SessionIDCount(), "permission_check_status must not re-fire session init")
}

func TestHandleOutput_PermissionCheckStatusDoneAndUnknownSendNull(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))

	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "permission_check_status",
		"tool_use_id": "toolu_1",
		"status": "done",
		"uuid": "u-1",
		"session_id": "s-1"
	}`))
	update := runningToolUpdate(t, sink)
	assert.Contains(t, update, contracts.RunningToolFieldPermissionCheck)
	assert.Nil(t, update[contracts.RunningToolFieldPermissionCheck], "done ends the check")

	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "permission_check_status",
		"tool_use_id": "toolu_1",
		"status": "finished",
		"uuid": "u-2",
		"session_id": "s-1"
	}`))
	update = runningToolUpdate(t, sink)
	assert.Contains(t, update, contracts.RunningToolFieldPermissionCheck,
		"the end travels as an explicit null, never as an absent key")
	assert.Nil(t, update[contracts.RunningToolFieldPermissionCheck],
		"any status but checking ends the check")
}

func TestHandleOutput_PermissionCheckStatusEmptyToolUseIDSendsNothing(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))

	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "permission_check_status",
		"status": "checking",
		"uuid": "u-1",
		"session_id": "s-1"
	}`))

	assert.Equal(t, 0, sink.SessionInfoCount(),
		"an empty tool_use_id identifies no call, so no badge exists to update")
}

func TestHandleOutput_PermissionCheckStatusForAKnownTaskBroadcastsOnTheChildSink(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	sink.UpdateSessionID("root-session")

	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_started",
		"task_id": "task-1",
		"tool_use_id": "tu-spawn",
		"task_type": "local_agent",
		"description": "d",
		"prompt": "p"
	}`))
	child := sink.Child("child-of-tu-spawn")
	child.UpdateSessionID("child-session")

	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "permission_check_status",
		"tool_use_id": "toolu_1",
		"agent_id": "task-1",
		"status": "checking",
		"uuid": "u-1",
		"session_id": "s-1"
	}`))

	value, ok := child.LastSessionInfoValue(contracts.SessionInfoKeyRunningTool)
	require.True(t, ok, "the broadcast lands on the child sink")
	update, ok := value.(map[string]interface{})
	require.True(t, ok, "running_tool must be an object, got %T", value)
	assert.Equal(t, "toolu_1", update[contracts.RunningToolFieldSpanId])
	assert.Equal(t, "child-session", update[contracts.RunningToolFieldAgentSessionId],
		"the broadcast states the child session the child sink stamps on its rows")
	assert.Equal(t, true, update[contracts.RunningToolFieldPermissionCheck])
	assert.Equal(t, 0, sink.SessionInfoCount(), "the root sink sees nothing")
}

func TestHandleOutput_PermissionCheckStatusForAnUnknownTaskSendsNothing(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))

	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "permission_check_status",
		"tool_use_id": "toolu_1",
		"agent_id": "task-unknown",
		"status": "checking",
		"uuid": "u-1",
		"session_id": "s-1"
	}`))

	assert.Equal(t, 0, sink.SessionInfoCount(),
		"an unknown child resolves to no sink, so nothing is broadcast")
}

func TestHandleOutput_CanUseToolRequestClearsThePermissionCheck(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))

	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "permission_check_status",
		"tool_use_id": "toolu_1",
		"status": "checking",
		"uuid": "u-1",
		"session_id": "s-1"
	}`))
	a.HandleOutput([]byte(`{
		"type": "control_request",
		"request_id": "r1",
		"request": {
			"subtype": "can_use_tool",
			"tool_name": "Bash",
			"input": {"command": "rm -rf build"},
			"tool_use_id": "toolu_1"
		}
	}`))

	update := runningToolUpdate(t, sink)
	assert.Equal(t, "toolu_1", update[contracts.RunningToolFieldSpanId],
		"the clear identifies the escalated call's row")
	assert.Contains(t, update, contracts.RunningToolFieldPermissionCheck,
		"the end travels as an explicit null, never as an absent key")
	assert.Nil(t, update[contracts.RunningToolFieldPermissionCheck],
		"an escalated call is no longer waiting on its check")
}

func TestHandleOutput_PermissionDeniedClearsTheCheckAndPersists(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))

	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "permission_check_status",
		"tool_use_id": "toolu_1",
		"status": "checking",
		"uuid": "u-1",
		"session_id": "s-1"
	}`))
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "permission_denied",
		"tool_name": "Bash",
		"tool_use_id": "toolu_1",
		"decision_reason_type": "classifier",
		"decision_reason": "the command removes a directory",
		"uuid": "u-2",
		"session_id": "s-1"
	}`))

	update := runningToolUpdate(t, sink)
	assert.Equal(t, "toolu_1", update[contracts.RunningToolFieldSpanId],
		"the clear identifies the denied call's row")
	assert.Contains(t, update, contracts.RunningToolFieldPermissionCheck,
		"the end travels as an explicit null, never as an absent key")
	assert.Nil(t, update[contracts.RunningToolFieldPermissionCheck],
		"any other frame for the call ends the check")
	assert.Equal(t, 1, sink.MessageCount(), "the denial still persists")
}

// runningToolUpdates returns every running_tool payload broadcast so far, in
// send order. Each payload identifies the one tool call its update addresses.
func runningToolUpdates(t *testing.T, sink *outputTestSink) []map[string]interface{} {
	t.Helper()
	var updates []map[string]interface{}
	for _, value := range sink.SessionInfoValues(contracts.SessionInfoKeyRunningTool) {
		update, ok := value.(map[string]interface{})
		require.True(t, ok, "running_tool must be an object, got %T", value)
		updates = append(updates, update)
	}
	return updates
}

func TestHandleOutput_PermissionCheckStatusKeepsOneRowPerCall(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	sink.UpdateSessionID("root-session")

	// Two calls wait on two checks at once. Every update for one call repeats
	// the same span id and session id, so the browser folds those updates into
	// one row. The clear of one call identifies only that call's row.
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "permission_check_status",
		"tool_use_id": "toolu_1",
		"status": "checking",
		"uuid": "u-1",
		"session_id": "s-1"
	}`))
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "permission_check_status",
		"tool_use_id": "toolu_2",
		"status": "checking",
		"uuid": "u-2",
		"session_id": "s-1"
	}`))
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "permission_check_status",
		"tool_use_id": "toolu_1",
		"status": "done",
		"uuid": "u-3",
		"session_id": "s-1"
	}`))

	updates := runningToolUpdates(t, sink)
	require.Len(t, updates, 3)
	assert.Equal(t, "toolu_1", updates[0][contracts.RunningToolFieldSpanId])
	assert.Equal(t, "toolu_2", updates[1][contracts.RunningToolFieldSpanId])
	assert.Equal(t, "toolu_1", updates[2][contracts.RunningToolFieldSpanId],
		"the clear ends the check of the call that it identifies")
	for index, update := range updates {
		assert.Equal(t, "root-session", update[contracts.RunningToolFieldAgentSessionId],
			"update %d states the session of the row that it folds into", index)
	}
	assert.Equal(t, true, updates[0][contracts.RunningToolFieldPermissionCheck])
	assert.Equal(t, true, updates[1][contracts.RunningToolFieldPermissionCheck],
		"the other call keeps its badge")
	assert.Contains(t, updates[2], contracts.RunningToolFieldPermissionCheck)
	assert.Nil(t, updates[2][contracts.RunningToolFieldPermissionCheck])
}

func TestHandleOutput_PermissionCheckStatusCheckingAgainRearmsTheBadge(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))

	// The frames state the wire value in arrival order. A late `checking` after
	// the end is a new wait for the same call, so its badge stands again.
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "permission_check_status",
		"tool_use_id": "toolu_1",
		"status": "checking",
		"uuid": "u-1",
		"session_id": "s-1"
	}`))
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "permission_check_status",
		"tool_use_id": "toolu_1",
		"status": "done",
		"uuid": "u-2",
		"session_id": "s-1"
	}`))
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "permission_check_status",
		"tool_use_id": "toolu_1",
		"status": "checking",
		"uuid": "u-3",
		"session_id": "s-1"
	}`))

	updates := runningToolUpdates(t, sink)
	require.Len(t, updates, 3)
	assert.Equal(t, true, updates[0][contracts.RunningToolFieldPermissionCheck])
	assert.Contains(t, updates[1], contracts.RunningToolFieldPermissionCheck)
	assert.Nil(t, updates[1][contracts.RunningToolFieldPermissionCheck], "the end clears the badge")
	assert.Equal(t, true, updates[2][contracts.RunningToolFieldPermissionCheck],
		"a new check arms the badge again")
}

func TestHandleOutput_PermissionDeniedRoutesTheClearToTheChildSink(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	sink.UpdateSessionID("root-session")

	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_started",
		"task_id": "task-1",
		"tool_use_id": "tu-spawn",
		"task_type": "local_agent",
		"description": "d",
		"prompt": "p"
	}`))
	child := sink.Child("child-of-tu-spawn")
	child.UpdateSessionID("child-session")
	childRowsBefore := len(child.Messages())

	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "permission_check_status",
		"tool_use_id": "toolu_1",
		"agent_id": "task-1",
		"status": "checking",
		"uuid": "u-1",
		"session_id": "s-1"
	}`))
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "permission_denied",
		"tool_name": "Bash",
		"tool_use_id": "toolu_1",
		"agent_id": "task-1",
		"decision_reason_type": "classifier",
		"decision_reason": "the command removes a directory",
		"uuid": "u-2",
		"session_id": "s-1"
	}`))

	value, ok := child.LastSessionInfoValue(contracts.SessionInfoKeyRunningTool)
	require.True(t, ok, "the clear lands on the child sink")
	update, ok := value.(map[string]interface{})
	require.True(t, ok, "running_tool must be an object, got %T", value)
	assert.Equal(t, "toolu_1", update[contracts.RunningToolFieldSpanId])
	assert.Equal(t, "child-session", update[contracts.RunningToolFieldAgentSessionId])
	assert.Contains(t, update, contracts.RunningToolFieldPermissionCheck)
	assert.Nil(t, update[contracts.RunningToolFieldPermissionCheck],
		"the denial ends the child call's check")
	assert.Equal(t, 0, sink.SessionInfoCount(), "the root sink sees nothing")

	// The denied call's row is in the child transcript, so the denial persists
	// there, under the span of the call. The spawn prompt is the row before it.
	childMessages := child.Messages()
	require.Len(t, childMessages, childRowsBefore+1, "the denial persists in the child transcript")
	denial := childMessages[len(childMessages)-1]
	assert.JSONEq(t, `{"type":"system","subtype":"permission_denied","tool_name":"Bash","tool_use_id":"toolu_1","agent_id":"task-1","decision_reason_type":"classifier","decision_reason":"the command removes a directory","uuid":"u-2","session_id":"s-1"}`,
		string(denial.Content))
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, denial.Source)
	assert.Equal(t, "toolu_1", denial.ParentSpanID, "the denial sits under the span of the call")
	assert.Equal(t, "child-session", denial.AgentSessionID)
	assert.Empty(t, sink.Messages(), "the root transcript holds no row for a child's call")
}

func TestHandleOutput_PermissionDeniedForAnUnknownTaskPersistsInTheRoot(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))

	// The frame identifies a task that this agent does not know. No child transcript
	// can take the row, so the root keeps it rather than losing the denial.
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "permission_denied",
		"tool_name": "Bash",
		"tool_use_id": "toolu_1",
		"agent_id": "task-unknown",
		"decision_reason_type": "classifier",
		"decision_reason": "the command removes a directory",
		"uuid": "u-1",
		"session_id": "s-1"
	}`))

	messages := sink.Messages()
	require.Len(t, messages, 1, "the denial persists in the root transcript")
	assert.Equal(t, "toolu_1", messages[0].ParentSpanID)
	assert.Equal(t, 0, sink.SessionInfoCount(),
		"an unknown child resolves to no sink, so no clear is broadcast")
}

func TestHandleOutput_CanUseToolRequestRoutesTheClearToTheChildSink(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newTestAgent(agent.NewProviderServices(sink))
	sink.UpdateSessionID("root-session")

	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "task_started",
		"task_id": "task-1",
		"tool_use_id": "tu-spawn",
		"task_type": "local_agent",
		"description": "d",
		"prompt": "p"
	}`))
	child := sink.Child("child-of-tu-spawn")
	child.UpdateSessionID("child-session")

	// Claude Code 2.1.300 states `agent_id` on a can_use_tool request that a
	// subagent's call raises, as it does on permission_check_status.
	a.HandleOutput([]byte(`{
		"type": "control_request",
		"request_id": "r1",
		"request": {
			"subtype": "can_use_tool",
			"tool_name": "Bash",
			"input": {"command": "rm -rf build"},
			"tool_use_id": "toolu_1",
			"agent_id": "task-1"
		}
	}`))

	value, ok := child.LastSessionInfoValue(contracts.SessionInfoKeyRunningTool)
	require.True(t, ok, "the clear lands on the child sink")
	update, ok := value.(map[string]interface{})
	require.True(t, ok, "running_tool must be an object, got %T", value)
	assert.Equal(t, "toolu_1", update[contracts.RunningToolFieldSpanId])
	assert.Equal(t, "child-session", update[contracts.RunningToolFieldAgentSessionId],
		"the clear states the session of the child row that holds the badge")
	assert.Contains(t, update, contracts.RunningToolFieldPermissionCheck,
		"the end travels as an explicit null, never as an absent key")
	assert.Nil(t, update[contracts.RunningToolFieldPermissionCheck])
	assert.Equal(t, 0, sink.SessionInfoCount(), "the root sink sees nothing")
	require.Len(t, sink.PublishedControls(), 1, "the request still reaches the reader")
}

func TestHandleOutput_CanUseToolRequestForAnUnknownTaskClearsNothing(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newTestAgent(agent.NewProviderServices(sink))

	a.HandleOutput([]byte(`{
		"type": "control_request",
		"request_id": "r1",
		"request": {
			"subtype": "can_use_tool",
			"tool_name": "Bash",
			"input": {"command": "rm -rf build"},
			"tool_use_id": "toolu_1",
			"agent_id": "task-unknown"
		}
	}`))

	assert.Equal(t, 0, sink.SessionInfoCount(),
		"an unknown child resolves to no sink, so nothing is broadcast")
	require.Len(t, sink.PublishedControls(), 1, "the request still reaches the reader")
}

func TestHandleOutput_AStatusFrameNeverDrivesThePermissionCheck(t *testing.T) {
	t.Parallel()

	sink := &outputTestSink{}
	a := newTestAgent(agent.NewProviderServices(sink))

	// A `status` frame carries a status word and may name a tool call. It
	// reports an event, never a check verdict, so it must not drive the badge.
	a.HandleOutput([]byte(`{
		"type": "system",
		"subtype": "status",
		"status": "checking",
		"tool_use_id": "toolu_1",
		"uuid": "u-1",
		"session_id": "s-1"
	}`))

	assert.Equal(t, 0, sink.SessionInfoCount(),
		"only a permission_check_status frame drives the badge")
	assert.Equal(t, 1, sink.NotificationCount(),
		"the frame still reports as the notification it is")
}
