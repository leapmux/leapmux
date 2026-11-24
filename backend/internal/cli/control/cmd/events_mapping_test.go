package cmd

import (
	"bytes"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/semanticevents"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

// directStream is an eventStream that a test drives without run: the test
// places the tabs, and the stream prints JSON into its buffers.
type directStream struct {
	*eventStream
	feed   *workerFeed
	out    bytes.Buffer
	errOut bytes.Buffer
}

// The tabs of a directStream: an agent and a terminal on worker-A in ws-1.
const (
	directAgent    = "ag-1"
	directTerminal = "tm-1"
)

func newDirectStream(t *testing.T, kinds ...string) *directStream {
	t.Helper()
	filter, err := parseKindFilter(kinds)
	require.NoError(t, err)
	d := &directStream{feed: &workerFeed{workerID: "worker-A"}}
	d.eventStream = newEventStream(eventStreamConfig{
		printer: &eventPrinter{p: &cli.Printer{Out: &d.out, Err: &d.errOut, Format: cli.FormatJSON}, filter: filter},
		clock:   testutil.NewQuartzMock(t),
	})
	d.state = crdt.NewState("u-1")
	d.ready = true
	d.hub = sourceHealth{live: true, everLive: true}
	d.workspaces = map[string]bool{"ws-1": true}
	d.tabs = map[string]scopedTab{
		directAgent:    {ID: directAgent, Type: leapmuxv1.TabType_TAB_TYPE_AGENT, WorkspaceID: "ws-1", WorkerID: "worker-A"},
		directTerminal: {ID: directTerminal, Type: leapmuxv1.TabType_TAB_TYPE_TERMINAL, WorkspaceID: "ws-1", WorkerID: "worker-A"},
	}
	d.feeds["worker-A"] = &feedState{
		feed: d.feed, settled: true, health: sourceHealth{live: true, everLive: true},
		interest: watchInterest{agents: []string{directAgent}, terminals: []string{directTerminal}},
	}
	return d
}

// agent feeds one agent event of the agent tab.
func (d *directStream) agent(t *testing.T, ev *leapmuxv1.AgentEvent) {
	t.Helper()
	if ev.AgentId == "" {
		ev.AgentId = directAgent
	}
	require.NoError(t, d.onFeed(feedOutput{feed: d.feed, kind: feedAgentEvent, agent: ev}))
}

// terminal feeds one terminal event of the terminal tab.
func (d *directStream) terminal(t *testing.T, ev *leapmuxv1.TerminalEvent) {
	t.Helper()
	if ev.TerminalId == "" {
		ev.TerminalId = directTerminal
	}
	require.NoError(t, d.onFeed(feedOutput{feed: d.feed, kind: feedTerminalEvent, terminal: ev}))
}

// events decodes and drains every event that the stream printed on stdout.
func (d *directStream) events(t *testing.T) []streamEvent {
	t.Helper()
	var out []streamEvent
	for line := range strings.SplitSeq(strings.TrimSpace(d.out.String()), "\n") {
		if line != "" {
			out = append(out, decodeEventLine(t, line))
		}
	}
	d.out.Reset()
	return out
}

// kinds lists the kind of each event.
func kindsOf(events []streamEvent) []string {
	out := make([]string, len(events))
	for i, ev := range events {
		out[i] = ev.Kind
	}
	return out
}

// oneofFields lists the fields of the oneof that m declares as name.
func oneofFields(m proto.Message, name string) []string {
	oneof := m.ProtoReflect().Descriptor().Oneofs().ByName(protoreflect.Name(name))
	var out []string
	for i := range oneof.Fields().Len() {
		out = append(out, string(oneof.Fields().Get(i).Name()))
	}
	return out
}

// TestEventMapping_EveryAgentEventHasADecision pins the mapping of each arm
// of AgentEvent: the kind that it prints, or "" for an arm that prints
// nothing. The case list must hold every arm, so a new arm fails here until
// someone decides its kind.
func TestEventMapping_EveryAgentEventHasADecision(t *testing.T) {
	ready := leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_READY
	cases := map[string]struct {
		event *leapmuxv1.AgentEvent
		kind  string
	}{
		// A notify watch receives no transcript content. The message follower
		// reports retractions to its own consumers.
		"agent_message":        {&leapmuxv1.AgentEvent{Event: &leapmuxv1.AgentEvent_AgentMessage{AgentMessage: &leapmuxv1.AgentChatMessage{Seq: 1}}}, ""},
		"messages_retracted":   {&leapmuxv1.AgentEvent{Event: &leapmuxv1.AgentEvent_MessagesRetracted{MessagesRetracted: &leapmuxv1.AgentMessagesRetracted{Seqs: []int64{1}}}}, ""},
		"catch_up_complete":    {&leapmuxv1.AgentEvent{Event: &leapmuxv1.AgentEvent_CatchUpComplete{CatchUpComplete: &leapmuxv1.CatchUpComplete{}}}, ""},
		"catch_up_start":       {&leapmuxv1.AgentEvent{Event: &leapmuxv1.AgentEvent_CatchUpStart{CatchUpStart: &leapmuxv1.CatchUpStart{}}}, ""},
		"input_queue_changed":  {&leapmuxv1.AgentEvent{Event: &leapmuxv1.AgentEvent_InputQueueChanged{InputQueueChanged: &leapmuxv1.AgentInputQueueChanged{}}}, kindAgentQueue},
		"goal_changed":         {&leapmuxv1.AgentEvent{Event: &leapmuxv1.AgentEvent_GoalChanged{GoalChanged: &leapmuxv1.AgentGoalSnapshot{}}}, kindAgentGoal},
		"input_queue_revision": {&leapmuxv1.AgentEvent{Event: &leapmuxv1.AgentEvent_InputQueueRevision{InputQueueRevision: 7}}, kindAgentQueue},
		"input_reply_changed":  {&leapmuxv1.AgentEvent{Event: &leapmuxv1.AgentEvent_InputReplyChanged{InputReplyChanged: &leapmuxv1.AgentInputReplyChanged{}}}, ""},
		// The progress of an answer's delivery: agent.request_ended reports
		// its outcome.
		"control_response_changed": {&leapmuxv1.AgentEvent{Event: &leapmuxv1.AgentEvent_ControlResponseChanged{ControlResponseChanged: &leapmuxv1.AgentControlRequest{ClaimToken: "h-1"}}}, ""},
		"status_change":            {&leapmuxv1.AgentEvent{Event: &leapmuxv1.AgentEvent_StatusChange{StatusChange: &leapmuxv1.AgentStatusChange{Status: statusActive}}}, kindAgentStatus},
		"control_request":          {&leapmuxv1.AgentEvent{Event: &leapmuxv1.AgentEvent_ControlRequest{ControlRequest: &leapmuxv1.AgentControlRequest{ClaimToken: "h-1", ResponseState: ready}}}, kindAgentRequest},
		"control_cancel":           {&leapmuxv1.AgentEvent{Event: &leapmuxv1.AgentEvent_ControlCancel{ControlCancel: &leapmuxv1.AgentControlCancelRequest{ClaimToken: "h-1"}}}, kindAgentRequestEnded},
		"todos_changed":            {&leapmuxv1.AgentEvent{Event: &leapmuxv1.AgentEvent_TodosChanged{TodosChanged: &leapmuxv1.AgentTodoSnapshot{}}}, kindAgentTodos},
		"turn_end":                 {&leapmuxv1.AgentEvent{Event: &leapmuxv1.AgentEvent_TurnEnd{TurnEnd: &leapmuxv1.AgentTurnEnd{}}}, kindAgentTurnEnd},
		"background_tasks_changed": {&leapmuxv1.AgentEvent{Event: &leapmuxv1.AgentEvent_BackgroundTasksChanged{BackgroundTasksChanged: &leapmuxv1.AgentBackgroundTaskSnapshot{}}}, kindAgentBackgroundTasks},
		"activity_changed":         {&leapmuxv1.AgentEvent{Event: &leapmuxv1.AgentEvent_ActivityChanged{ActivityChanged: &leapmuxv1.AgentActivityChanged{State: idle}}}, kindAgentActivity},
	}
	arms := oneofFields(&leapmuxv1.AgentEvent{}, "event")
	assert.ElementsMatch(t, arms, slices.Collect(maps.Keys(cases)), "every arm of AgentEvent needs a decision here")
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			d := newDirectStream(t)
			d.agent(t, tc.event)
			got := d.events(t)
			if tc.kind == "" {
				assert.Empty(t, got)
				return
			}
			require.Len(t, got, 1)
			assert.Equal(t, tc.kind, got[0].Kind)
			assert.Equal(t, directAgent, got[0].TabID)
			assert.Equal(t, "ws-1", got[0].WorkspaceID)
			assert.Equal(t, "worker-A", got[0].WorkerID)
			assert.Equal(t, "agent", got[0].TabType)
			assert.NotEmpty(t, got[0].Summary)
		})
	}
}

// TestEventMapping_EveryTerminalEventHasADecision is the terminal sibling of
// the agent test above.
func TestEventMapping_EveryTerminalEventHasADecision(t *testing.T) {
	cases := map[string]struct {
		event *leapmuxv1.TerminalEvent
		kind  string
	}{
		"data":            {&leapmuxv1.TerminalEvent{Event: &leapmuxv1.TerminalEvent_Data{Data: &leapmuxv1.TerminalData{Data: []byte("ls\n")}}}, ""},
		"screen_geometry": {&leapmuxv1.TerminalEvent{Event: &leapmuxv1.TerminalEvent_ScreenGeometry{ScreenGeometry: &leapmuxv1.TerminalScreenGeometry{Cols: 120, Rows: 40}}}, ""},
		"closed":          {&leapmuxv1.TerminalEvent{Event: &leapmuxv1.TerminalEvent_Closed{Closed: &leapmuxv1.TerminalClosed{ExitCode: int32Ptr(3)}}}, kindTerminalExit},
		"status_change":   {&leapmuxv1.TerminalEvent{Event: &leapmuxv1.TerminalEvent_StatusChange{StatusChange: &leapmuxv1.TerminalStatusChange{Status: leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY}}}, kindTerminalStatus},
		"bell":            {&leapmuxv1.TerminalEvent{Event: &leapmuxv1.TerminalEvent_Bell{Bell: &leapmuxv1.TerminalBell{}}}, kindTerminalBell},
		"notification":    {&leapmuxv1.TerminalEvent{Event: &leapmuxv1.TerminalEvent_Notification{Notification: &leapmuxv1.TerminalNotification{Body: "done"}}}, kindTerminalNotification},
		"title_changed":   {&leapmuxv1.TerminalEvent{Event: &leapmuxv1.TerminalEvent_TitleChanged{TitleChanged: &leapmuxv1.TerminalTitleChanged{Title: "vim"}}}, kindTerminalTitle},
		"progress":        {&leapmuxv1.TerminalEvent{Event: &leapmuxv1.TerminalEvent_Progress{Progress: &leapmuxv1.TerminalProgress{State: leapmuxv1.TerminalProgress_STATE_NORMAL, Percent: 5}}}, kindTerminalProgress},
	}
	assert.ElementsMatch(t, oneofFields(&leapmuxv1.TerminalEvent{}, "event"), slices.Collect(maps.Keys(cases)),
		"every arm of TerminalEvent needs a decision here")
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			d := newDirectStream(t)
			d.terminal(t, tc.event)
			got := d.events(t)
			if tc.kind == "" {
				assert.Empty(t, got)
				return
			}
			require.Len(t, got, 1)
			assert.Equal(t, tc.kind, got[0].Kind)
			assert.Equal(t, directTerminal, got[0].TabID)
			assert.Equal(t, "terminal", got[0].TabType)
			assert.NotEmpty(t, got[0].Summary)
		})
	}
}

// TestEventMapping_EveryHubFrameHasADecision pins what each arm of
// WatchUserEvent does on its own. The CRDT arms print tab events only at
// batch_end, which the projection tests cover.
func TestEventMapping_EveryHubFrameHasADecision(t *testing.T) {
	printsAlone := map[string]string{
		"initial": "", "batch": "", "entity_materialized": "", "entity_removed": "", "batch_end": "",
		"presence": "", "delta": "", "workers_changed": "",
		"created": kindWorkspaceCreated, "renamed": kindWorkspaceRenamed, "deleted": kindWorkspaceDeleted,
	}
	assert.ElementsMatch(t, oneofFields(&leapmuxv1.WatchUserEvent{}, "event"), slices.Collect(maps.Keys(printsAlone)),
		"every arm of WatchUserEvent needs a decision here")

	d := newDirectStream(t)
	d.workspace = ""
	require.NoError(t, d.onHubEvent(workspaceCreatedEvent("ws-2", "Backend")))
	require.NoError(t, d.onHubEvent(workspaceRenamedEvent("ws-2", "Backend API")))
	require.NoError(t, d.onHubEvent(workspaceDeletedEvent("ws-2")))
	require.NoError(t, d.onHubEvent(&leapmuxv1.WatchUserEvent{Event: &leapmuxv1.WatchUserEvent_Presence{Presence: &leapmuxv1.PresenceUpdate{WorkspaceId: "ws-1"}}}))
	require.NoError(t, d.onHubEvent(workersChangedEvent()))
	got := d.events(t)
	require.Equal(t, []string{kindWorkspaceCreated, kindWorkspaceRenamed, kindWorkspaceDeleted}, kindsOf(got))
	assert.Equal(t, "Backend", got[0].Title)
	assert.Equal(t, `"Backend"`, got[0].Summary)
	assert.Equal(t, "Backend API", got[1].Title)
	assert.Equal(t, `to "Backend API"`, got[1].Summary)
	assert.Equal(t, "ws-2", got[2].WorkspaceID)

	t.Run("a workspace announces its creation and its deletion once", func(t *testing.T) {
		require.NoError(t, d.onHubEvent(workspaceDeletedEvent("ws-2")))
		require.NoError(t, d.onHubEvent(workspaceCreatedEvent("ws-1", "First")))
		assert.Empty(t, d.events(t))
	})
}

// --workspace watches one workspace, so the creation, the renaming and the
// deletion of another one print nothing, and neither does a frame without a
// workspace. The lifecycle of the watched workspace prints.
func TestEventMapping_AWatchedWorkspaceHidesTheLifecycleOfTheOthers(t *testing.T) {
	d := newDirectStream(t)
	d.workspace = "ws-1"
	for _, evt := range []*leapmuxv1.WatchUserEvent{
		workspaceCreatedEvent("ws-2", "Other"),
		workspaceRenamedEvent("ws-2", "Other API"),
		workspaceDeletedEvent("ws-2"),
		workspaceCreatedEvent("", "No id"),
		workspaceRenamedEvent("", "No id"),
		workspaceRenamedEvent("ws-1", "Watched"),
	} {
		require.NoError(t, d.onHubEvent(evt))
	}

	got := d.events(t)
	require.Equal(t, []string{kindWorkspaceRenamed}, kindsOf(got))
	assert.Equal(t, "ws-1", got[0].WorkspaceID)
	assert.Equal(t, "Watched", got[0].Title)
	assert.Equal(t, map[string]bool{"ws-1": true}, d.workspaces, "a workspace outside the scope never joins it")
}

// The fields of each agent event, and the repeats that the stream drops.
func TestEventMapping_AgentEvents(t *testing.T) {
	t.Run("activity: the state, the tool calls, and no repeat", func(t *testing.T) {
		d := newDirectStream(t)
		three := int32(3)
		d.agent(t, &leapmuxv1.AgentEvent{Event: &leapmuxv1.AgentEvent_ActivityChanged{ActivityChanged: &leapmuxv1.AgentActivityChanged{State: working}}})
		d.agent(t, &leapmuxv1.AgentEvent{Event: &leapmuxv1.AgentEvent_ActivityChanged{ActivityChanged: &leapmuxv1.AgentActivityChanged{State: working}}})
		d.agent(t, &leapmuxv1.AgentEvent{Event: &leapmuxv1.AgentEvent_ActivityChanged{ActivityChanged: &leapmuxv1.AgentActivityChanged{State: idle, NumToolUses: &three}}})
		d.agent(t, &leapmuxv1.AgentEvent{Event: &leapmuxv1.AgentEvent_ActivityChanged{ActivityChanged: &leapmuxv1.AgentActivityChanged{}}})
		d.agent(t, &leapmuxv1.AgentEvent{Event: &leapmuxv1.AgentEvent_ActivityChanged{ActivityChanged: &leapmuxv1.AgentActivityChanged{State: waiting}}})
		got := d.events(t)
		require.Len(t, got, 3, "a repeated state and an unstated one print nothing")
		assert.Equal(t, "working", got[0].State)
		assert.Equal(t, "working", got[0].Summary)
		assert.Nil(t, got[0].ToolCalls, "a turn that starts reports no count")
		assert.Equal(t, "idle", got[1].State)
		require.NotNil(t, got[1].ToolCalls)
		assert.Equal(t, int32(3), *got[1].ToolCalls)
		assert.Equal(t, "idle (3 tool calls)", got[1].Summary)
		assert.Equal(t, "waiting_for_user", got[2].State)
		assert.Equal(t, "waiting for user", got[2].Summary)
	})
	t.Run("status: a change of the status, with the startup error", func(t *testing.T) {
		d := newDirectStream(t)
		status := func(s leapmuxv1.AgentStatus, startupError string) {
			d.agent(t, &leapmuxv1.AgentEvent{Event: &leapmuxv1.AgentEvent_StatusChange{StatusChange: &leapmuxv1.AgentStatusChange{Status: s, StartupError: startupError}}})
		}
		status(statusStarting, "")
		status(statusStarting, "")
		status(leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED, "claude: command not found")
		status(leapmuxv1.AgentStatus_AGENT_STATUS_UNSPECIFIED, "")
		got := d.events(t)
		require.Len(t, got, 2, "a status change for the options or the git status repeats the status")
		assert.Equal(t, "starting", got[0].Status)
		assert.Equal(t, "startup_failed", got[1].Status)
		assert.Equal(t, "claude: command not found", got[1].Error)
		assert.Equal(t, "startup failed: claude: command not found", got[1].Summary)
	})
	t.Run("requests: each instance once, and its end", func(t *testing.T) {
		d := newDirectStream(t)
		request := func(handle string, state leapmuxv1.ControlResponseState, nonBlocking bool) {
			d.agent(t, &leapmuxv1.AgentEvent{Event: &leapmuxv1.AgentEvent_ControlRequest{ControlRequest: &leapmuxv1.AgentControlRequest{
				RequestId: "req-" + handle, ClaimToken: handle, ResponseState: state, NonBlocking: nonBlocking,
				Prompt: &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{Title: "Bash", Command: "rm -rf build"}}},
			}}})
		}
		ready := leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_READY
		request("handle-0001", ready, false)
		request("handle-0001", ready, false)
		request("handle-0002", leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_PENDING, false)
		request("handle-0003", ready, true)
		d.agent(t, &leapmuxv1.AgentEvent{Event: &leapmuxv1.AgentEvent_ControlCancel{ControlCancel: &leapmuxv1.AgentControlCancelRequest{
			RequestId: "req-handle-0001", ClaimToken: "handle-0001", ResponseState: leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED,
		}}})
		request("handle-0001", ready, false)
		got := d.events(t)
		require.Equal(t, []string{kindAgentRequest, kindAgentRequest, kindAgentRequestEnded, kindAgentRequest}, kindsOf(got),
			"a repeated announcement and one whose answer is in delivery print nothing; an ended request can print again")
		assert.Equal(t, "handle-0001", got[0].Request, "the handle is what agent approve takes")
		assert.Equal(t, "req-handle-0001", got[0].RequestID)
		assert.Equal(t, "permission", got[0].Prompt)
		require.NotNil(t, got[0].Blocking)
		assert.True(t, *got[0].Blocking)
		assert.Equal(t, "permission: Bash", got[0].Summary)
		require.NotNil(t, got[1].Blocking)
		assert.False(t, *got[1].Blocking)
		assert.Equal(t, "permission: Bash (the agent goes on working)", got[1].Summary)
		assert.Equal(t, "handle-0001", got[2].Request)
		assert.Equal(t, "completed", got[2].State)
		assert.Equal(t, "request handle-0 ended (completed)", got[2].Summary)
	})
	t.Run("todos: a change of an item or its status", func(t *testing.T) {
		d := newDirectStream(t)
		todos := func(items ...*leapmuxv1.TodoItem) {
			d.agent(t, &leapmuxv1.AgentEvent{Event: &leapmuxv1.AgentEvent_TodosChanged{TodosChanged: &leapmuxv1.AgentTodoSnapshot{Todos: items}}})
		}
		item := func(content string, status leapmuxv1.TodoStatus, activeForm string) *leapmuxv1.TodoItem {
			return &leapmuxv1.TodoItem{Content: content, Status: status, ActiveForm: activeForm}
		}
		todos(item("Run the tests", leapmuxv1.TodoStatus_TODO_STATUS_IN_PROGRESS, "Running the tests"),
			item("Fix the bug", leapmuxv1.TodoStatus_TODO_STATUS_COMPLETED, ""),
			item("Old idea", leapmuxv1.TodoStatus_TODO_STATUS_DELETED, ""),
			item("Write docs", leapmuxv1.TodoStatus_TODO_STATUS_PENDING, ""))
		todos(item("Run the tests", leapmuxv1.TodoStatus_TODO_STATUS_IN_PROGRESS, "Running the tests again"),
			item("Fix the bug", leapmuxv1.TodoStatus_TODO_STATUS_COMPLETED, ""),
			item("Old idea", leapmuxv1.TodoStatus_TODO_STATUS_DELETED, ""),
			item("Write docs", leapmuxv1.TodoStatus_TODO_STATUS_PENDING, ""))
		todos()
		got := d.events(t)
		require.Len(t, got, 2, "a new activity text alone prints nothing")
		assert.Equal(t, &todoCounts{Total: 3, Completed: 1, InProgress: 1}, got[0].Todos, "a deleted item does not count")
		assert.Equal(t, "1 of 3 done; in progress: Running the tests", got[0].Summary)
		assert.Equal(t, &todoCounts{}, got[1].Todos, "an empty list prints its zero counts")
		assert.Equal(t, "0 of 0 done", got[1].Summary)
	})
	t.Run("background tasks: a change of a task or its status", func(t *testing.T) {
		d := newDirectStream(t)
		tasks := func(items ...*leapmuxv1.BackgroundTaskItem) {
			d.agent(t, &leapmuxv1.AgentEvent{Event: &leapmuxv1.AgentEvent_BackgroundTasksChanged{BackgroundTasksChanged: &leapmuxv1.AgentBackgroundTaskSnapshot{Tasks: items}}})
		}
		running := leapmuxv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_RUNNING
		tasks(&leapmuxv1.BackgroundTaskItem{Id: "t1", Status: running, ActiveForm: "Building"},
			&leapmuxv1.BackgroundTaskItem{Id: "t2", Status: leapmuxv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_SUCCEEDED, EndedAt: "2026-11-19T10:00:00Z"})
		tasks(&leapmuxv1.BackgroundTaskItem{Id: "t1", Status: running, ActiveForm: "Linking"},
			&leapmuxv1.BackgroundTaskItem{Id: "t2", Status: leapmuxv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_SUCCEEDED, EndedAt: "2026-11-19T10:00:00Z"})
		tasks(&leapmuxv1.BackgroundTaskItem{Id: "t1", Status: leapmuxv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_FAILED, EndedAt: "2026-11-19T10:01:00Z"})
		got := d.events(t)
		require.Len(t, got, 2, "a new activity text alone prints nothing")
		assert.Equal(t, &taskCounts{Total: 2, Active: 1}, got[0].Tasks)
		assert.Equal(t, "1 active of 2", got[0].Summary)
		assert.Equal(t, &taskCounts{Total: 1, Active: 0}, got[1].Tasks)
	})
	t.Run("an event of an agent outside the scope prints nothing", func(t *testing.T) {
		d := newDirectStream(t)
		d.agent(t, &leapmuxv1.AgentEvent{AgentId: "ag-elsewhere", Event: &leapmuxv1.AgentEvent_TurnEnd{TurnEnd: &leapmuxv1.AgentTurnEnd{}}})
		d.agent(t, &leapmuxv1.AgentEvent{AgentId: directTerminal, Event: &leapmuxv1.AgentEvent_TurnEnd{TurnEnd: &leapmuxv1.AgentTurnEnd{}}})
		assert.Empty(t, d.events(t), "an id that is no agent of the scope is no agent event")
	})
	t.Run("an event from another Worker than the tab's prints nothing", func(t *testing.T) {
		d := newDirectStream(t)
		other := &workerFeed{workerID: "worker-B"}
		d.feeds["worker-B"] = &feedState{feed: other, settled: true}
		require.NoError(t, d.onFeed(feedOutput{feed: other, kind: feedAgentEvent, agent: &leapmuxv1.AgentEvent{
			AgentId: directAgent, Event: &leapmuxv1.AgentEvent_TurnEnd{TurnEnd: &leapmuxv1.AgentTurnEnd{}},
		}}))
		assert.Empty(t, d.events(t))
	})
	t.Run("the output of a watch that the stream stopped prints nothing", func(t *testing.T) {
		d := newDirectStream(t)
		stale := &workerFeed{workerID: "worker-A"}
		require.NoError(t, d.onFeed(feedOutput{feed: stale, kind: feedAgentEvent, agent: &leapmuxv1.AgentEvent{
			AgentId: directAgent, Event: &leapmuxv1.AgentEvent_TurnEnd{TurnEnd: &leapmuxv1.AgentTurnEnd{}},
		}}))
		assert.Empty(t, d.events(t))
	})
}

// The summary of a request states what it asks, in one line of at most
// promptDetailLength characters after its kind.
func TestPromptDetail(t *testing.T) {
	question := func(texts ...string) *leapmuxv1.ControlPrompt {
		q := &leapmuxv1.QuestionPrompt{}
		for _, text := range texts {
			q.Questions = append(q.Questions, &leapmuxv1.ControlQuestion{Question: text})
		}
		return &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Question{Question: q}}
	}
	cases := []struct {
		name   string
		prompt *leapmuxv1.ControlPrompt
		want   string
	}{
		{"a permission with a title", &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{Title: "Edit", Command: "x"}}}, "Edit"},
		{"a permission with a command alone", &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{Command: "make test"}}}, "make test"},
		{"a plan", &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Plan{Plan: &leapmuxv1.PlanPrompt{Text: "1. Do it"}}}, "approve the plan"},
		{"one question", question("Which database?"), "Which database?"},
		{"three questions", question("Which database?", "Which port?", "Which user?"), "Which database? (and 2 more questions)"},
		{"no question", question(), ""},
		{"an elicitation", &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Elicitation{Elicitation: &leapmuxv1.ElicitationPrompt{Message: "Sign in"}}}, "Sign in"},
		{"a dialog", &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Dialog{Dialog: &leapmuxv1.DialogPrompt{Message: "Continue?"}}}, "Continue?"},
		{"an unreadable request", &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Unreadable{Unreadable: leapmuxv1.ControlPayloadFault_CONTROL_PAYLOAD_FAULT_MALFORMED}}, "LeapMux cannot read it (malformed)"},
		{"an unrecognized request", &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Unreadable{Unreadable: leapmuxv1.ControlPayloadFault_CONTROL_PAYLOAD_FAULT_UNRECOGNIZED}}, "LeapMux cannot read it (unrecognized)"},
		{"no prompt", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, semanticevents.PromptDetail(tc.prompt))
		})
	}

	t.Run("a long command is cut", func(t *testing.T) {
		long := strings.Repeat("x", 500)
		ev := agentRequestEvent(scopedTab{ID: "ag-1"}, &leapmuxv1.AgentControlRequest{
			ClaimToken: "h", Prompt: &leapmuxv1.ControlPrompt{Kind: &leapmuxv1.ControlPrompt_Permission{Permission: &leapmuxv1.PermissionPrompt{Command: long + "\nrm -rf /"}}},
		})
		detail := strings.TrimPrefix(ev.Summary, "permission: ")
		assert.Len(t, []rune(detail), 120)
		assert.True(t, strings.HasSuffix(detail, "…"))
		assert.NotContains(t, ev.Summary, "\n")
	})
	t.Run("a request with no prompt states its kind alone", func(t *testing.T) {
		ev := agentRequestEvent(scopedTab{ID: "ag-1"}, &leapmuxv1.AgentControlRequest{ClaimToken: "h"})
		assert.Equal(t, controlKindUnknown, ev.Summary)
	})
}

// The fields of each terminal event, and the repeats that the stream drops.
func TestEventMapping_TerminalEvents(t *testing.T) {
	d := newDirectStream(t)
	status := func(s leapmuxv1.TerminalStatus, startupError string) {
		d.terminal(t, &leapmuxv1.TerminalEvent{Event: &leapmuxv1.TerminalEvent_StatusChange{StatusChange: &leapmuxv1.TerminalStatusChange{Status: s, StartupError: startupError}}})
	}
	status(leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTING, "")
	status(leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTING, "")
	status(leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY, "")
	d.terminal(t, &leapmuxv1.TerminalEvent{Event: &leapmuxv1.TerminalEvent_TitleChanged{TitleChanged: &leapmuxv1.TerminalTitleChanged{Title: "make test"}}})
	d.terminal(t, &leapmuxv1.TerminalEvent{Event: &leapmuxv1.TerminalEvent_TitleChanged{TitleChanged: &leapmuxv1.TerminalTitleChanged{Title: "make test"}}})
	d.terminal(t, &leapmuxv1.TerminalEvent{Event: &leapmuxv1.TerminalEvent_TitleChanged{TitleChanged: &leapmuxv1.TerminalTitleChanged{Title: ""}}})
	d.terminal(t, &leapmuxv1.TerminalEvent{Event: &leapmuxv1.TerminalEvent_Notification{Notification: &leapmuxv1.TerminalNotification{Title: "Build", Body: "done"}}})
	d.terminal(t, &leapmuxv1.TerminalEvent{Event: &leapmuxv1.TerminalEvent_Notification{Notification: &leapmuxv1.TerminalNotification{Body: "done"}}})
	for _, p := range []*leapmuxv1.TerminalProgress{
		{State: leapmuxv1.TerminalProgress_STATE_NORMAL, Percent: 42},
		{State: leapmuxv1.TerminalProgress_STATE_ERROR, Percent: 42},
		{State: leapmuxv1.TerminalProgress_STATE_PAUSED, Percent: 7},
		{State: leapmuxv1.TerminalProgress_STATE_INDETERMINATE},
		{},
	} {
		d.terminal(t, &leapmuxv1.TerminalEvent{Event: &leapmuxv1.TerminalEvent_Progress{Progress: p}})
	}
	d.terminal(t, &leapmuxv1.TerminalEvent{Event: &leapmuxv1.TerminalEvent_Closed{Closed: &leapmuxv1.TerminalClosed{ExitCode: int32Ptr(0)}}})
	status(leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED, "")
	status(leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED, "zsh: not found")

	got := d.events(t)
	summaries := make([]string, len(got))
	for i, ev := range got {
		summaries[i] = ev.Kind + " " + ev.Summary
	}
	assert.Equal(t, []string{
		"terminal.status starting",
		"terminal.status ready",
		`terminal.title "make test"`,
		`terminal.title ""`,
		"terminal.notification Build: done",
		"terminal.notification done",
		"terminal.progress 42%",
		"terminal.progress error at 42%",
		"terminal.progress paused at 7%",
		"terminal.progress in progress",
		"terminal.progress cleared",
		"terminal.exit the shell exited with code 0",
		"terminal.status startup failed: zsh: not found",
	}, summaries, "a repeated status and a repeated title print nothing, and the exit stands for the exited status")
	exit := got[11]
	require.NotNil(t, exit.ExitCode, "an exit code of 0 is a fact, so it prints")
	assert.Equal(t, int32(0), *exit.ExitCode)
	assert.Equal(t, &progressReport{State: "error", Percent: 42}, got[7].Progress)
	assert.Equal(t, &progressReport{}, got[10].Progress)
	assert.Equal(t, "zsh: not found", got[12].Error)
}

// An exit with no code on record prints no exit code: 0 would claim a clean
// exit that the Worker never saw.
func TestEventMapping_TerminalExitWithoutARecordedCode(t *testing.T) {
	d := newDirectStream(t)
	d.terminal(t, &leapmuxv1.TerminalEvent{Event: &leapmuxv1.TerminalEvent_Closed{Closed: &leapmuxv1.TerminalClosed{}}})

	got := d.events(t)
	require.Len(t, got, 1)
	assert.Equal(t, kindTerminalExit, got[0].Kind)
	assert.Nil(t, got[0].ExitCode)
	assert.Equal(t, "the shell exited; the Worker holds no record of its exit code", got[0].Summary)
}

// A tab that a Worker refused to watch prints one stream.unavailable for
// each reason. A failed lookup is the subscription's to retry.
func TestEventMapping_Rejections(t *testing.T) {
	d := newDirectStream(t)
	reject := func(id string, reason leapmuxv1.WatchRejectionReason, terminal bool) {
		require.NoError(t, d.onFeed(feedOutput{feed: d.feed, kind: feedRejected, rejectedTerminal: terminal,
			rejection: &leapmuxv1.WatchRejection{EntityId: id, Reason: reason}}))
	}
	reject(directAgent, leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_LOOKUP_FAILED, false)
	reject(directAgent, leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_NOT_FOUND, false)
	reject(directAgent, leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_NOT_FOUND, false)
	reject(directTerminal, leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_FORBIDDEN, true)
	reject(directAgent, leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_UNSPECIFIED, false)
	reject("tab-elsewhere", leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_NOT_FOUND, false)

	got := d.events(t)
	require.Len(t, got, 3, "a repeated refusal, a lookup to retry and a tab outside the scope print nothing")
	for _, ev := range got {
		assert.Equal(t, kindStreamUnavailable, ev.Kind)
		assert.Equal(t, "worker", ev.Source)
		assert.Equal(t, "worker-A", ev.WorkerID)
		require.NotNil(t, ev.Retrying)
		assert.False(t, *ev.Retrying)
	}
	assert.Equal(t, directAgent, got[0].TabID)
	assert.Equal(t, string(cli.CodeNotFound), got[0].Code)
	assert.Equal(t, "the worker holds no agent ag-1", got[0].Message)
	assert.Equal(t, directTerminal, got[1].TabID)
	assert.Equal(t, string(cli.CodePermissionDenied), got[1].Code)
	assert.Contains(t, got[1].Message, "terminal tm-1")
	assert.Equal(t, string(cli.CodeInternal), got[2].Code, "the protocol reads an unknown reason as durable")
}
