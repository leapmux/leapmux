package mimo

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The events below follow the order MiMo 0.1.14 sent them in a real probe of a
// background spawn (probe/mimo-code/actor.sse.jsonl).
// The spawn call's running update identifies the actor before its first actor.status.
// The actor's own messages follow.

const (
	spawnCallID = "call-main-01"
	spawnSpanID = "prt_a"
	actorID     = "general-1"
)

func TestActorCompletionMapperPreservesFinishedFinality(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		completion agent.MessageCompletion
		status     bgtask.Status
	}{
		{agent.MessageCompletionComplete, bgtask.StatusSucceeded},
		{agent.MessageCompletionError, bgtask.StatusFailed},
		{agent.MessageCompletionInterrupted, bgtask.StatusStopped},
		{agent.MessageCompletionFinished, bgtask.StatusEndedWithUnknownOutcome},
		{"", bgtask.StatusStopped},
		{"future", bgtask.StatusStopped},
	} {
		t.Run(string(tc.completion), func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.status, actorStatusForCompletion(tc.completion))
		})
	}
}

func spawnInputOf(action, description, prompt string) map[string]any {
	return map[string]any{"operation": map[string]any{
		"action": action, "subagent_type": "general", "description": description, "prompt": prompt,
	}}
}

func actorRegisteredEvent(t *testing.T, id string, background bool) []byte {
	t.Helper()
	return eventJSON(t, eventActorRegistered, map[string]any{
		"sessionID": testSessionID, "actorID": id, "mode": "subagent", "description": "Probe helper",
		"agent": "general", "background": background,
	})
}

func actorStatusEvent(t *testing.T, id, status, outcome string, turnCount int, failure string) []byte {
	t.Helper()
	properties := map[string]any{"sessionID": testSessionID, "actorID": id, "status": status, "turnCount": turnCount, "lastTurnTime": 1}
	if outcome != "" {
		properties["lastOutcome"] = outcome
	}
	if failure != "" {
		properties["error"] = failure
	}
	return eventJSON(t, eventActorStatus, properties)
}

// spawnActor feeds a background spawn of general-1 up to its first running
// status.
func spawnActor(t *testing.T, a *Agent, action string, background bool) {
	t.Helper()
	input := spawnInputOf(action, "Probe helper", "SUBAGENT-WORK please run one command")
	metadata := map[string]any{"sessionId": testSessionID, "actorId": actorID}
	feed(a,
		statusEvent(t, contracts.MiMoStatusTypeBusy),
		messageEvent(t, "msg_p", roleAssistant, mainActorID, false),
		toolPartEvent(t, "prt_a", "msg_p", contracts.MiMoToolActor, spawnCallID, toolState{Status: contracts.MiMoToolStatusPending}),
		actorRegisteredEvent(t, actorID, background),
		toolPartEvent(t, "prt_a", "msg_p", contracts.MiMoToolActor, spawnCallID, toolState{Status: contracts.MiMoToolStatusRunning, Input: input, Metadata: metadata}),
		toolPartEvent(t, "prt_a", "msg_p", contracts.MiMoToolActor, spawnCallID, toolState{Status: contracts.MiMoToolStatusRunning, Input: input, Metadata: metadata}),
	)
	if background {
		feed(a, toolPartEvent(t, "prt_a", "msg_p", contracts.MiMoToolActor, spawnCallID, toolState{
			Status: contracts.MiMoToolStatusCompleted, Input: input, Metadata: metadata,
			Output: "Background sub-session started. actor_id: general-1",
		}))
	}
	feed(a, actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 0, ""))
}

// runActorTurn feeds one turn of general-1's own work.
func runActorTurn(t *testing.T, a *Agent, final string) {
	t.Helper()
	feed(a,
		messageEvent(t, "msg_c1", roleUser, actorID, false),
		userTextPartEvent(t, "prt_c1", "msg_c1", "SUBAGENT-WORK please run one command"+returnFormatSuffix),
		messageEvent(t, "msg_c2", roleAssistant, actorID, false),
		textPartEvent(t, partTypeReasoning, "prt_c2", "msg_c2", "Sub reasoning here.", true),
		toolPartEvent(t, "prt_c3", "msg_c2", contracts.MiMoToolBash, "call-sub-01", toolState{Status: contracts.MiMoToolStatusRunning,
			Input: map[string]any{"command": "echo sub", "description": "Sub command"}}),
		toolPartEvent(t, "prt_c3", "msg_c2", contracts.MiMoToolBash, "call-sub-01", toolState{Status: contracts.MiMoToolStatusCompleted,
			Input: map[string]any{"command": "echo sub", "description": "Sub command"}, Output: "sub\n", Metadata: map[string]any{"output": "sub\n", "exit": 0}}),
		textPartEvent(t, partTypeText, "prt_c4", "msg_c2", final, true),
		messageEvent(t, "msg_c2", roleAssistant, actorID, true),
	)
}

func TestBackgroundSpawnGetsAChildTranscript(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)

	spawnActor(t, a, contracts.MiMoActorActionSpawn, true)
	childID := "child-of-" + spawnSpanID
	require.Equal(t, []string{childID}, sink.ChildAgentIDs(), "the spawn call's span is the child's spawn span")
	row := backgroundTask(t, sink, spawnSpanID)
	assert.Equal(t, bgtask.KindSubagent, row.Kind)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.Equal(t, childID, row.ChildAgentID)
	assert.Equal(t, "Probe helper", row.Title)

	runActorTurn(t, a, "**Status**: success\n**Summary**: Ran the command.")
	feed(a, statusEvent(t, contracts.MiMoStatusTypeIdle))

	child := sink.Child(childID)
	childRows := child.Messages()
	require.Len(t, childRows, 5, "prompt, reasoning, call opener, call closer, final text")
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, childRows[0].Source)
	assert.JSONEq(t, `{"content":"SUBAGENT-WORK please run one command"}`, string(childRows[0].Content),
		"the transcript opens on the spawn's own instruction, without MiMo's return-format suffix")
	_, text, _ := assembledText(t, childRows[1].Content)
	assert.Equal(t, "Sub reasoning here.", text)
	assert.Equal(t, "prt_c3", childRows[2].SpanID)
	assert.True(t, childRows[3].Closing)
	_, text, _ = assembledText(t, childRows[4].Content)
	assert.Equal(t, "**Status**: success\n**Summary**: Ran the command.", text)

	for _, message := range sink.Messages() {
		assert.NotEqual(t, "prt_c3", message.SpanID, "a subagent's call stays out of the parent transcript")
	}
	assert.Equal(t, bgtask.StatusRunning, backgroundTask(t, sink, spawnSpanID).Status,
		"the parent's turn end does not end a background subagent")

	feed(a, actorStatusEvent(t, actorID, contracts.MiMoActorStatusIdle, contracts.MiMoActorOutcomeSuccess, 2, ""))
	assert.Equal(t, bgtask.StatusSucceeded, backgroundTask(t, sink, spawnSpanID).Status)
	reports := sink.LeapMuxNotifications()
	require.Len(t, reports, 1, "a background subagent reports its last text to the parent")
	assert.Equal(t, contracts.NotificationTypeSubagentReport, reports[0][contracts.NotificationFieldType])
	assert.Equal(t, "**Status**: success\n**Summary**: Ran the command.", reports[0][contracts.NotificationFieldText])
	assert.Equal(t, "Probe helper", reports[0][contracts.NotificationFieldLabel])
	assert.Equal(t, bgtask.StatusWire(bgtask.StatusSucceeded), reports[0][contracts.NotificationFieldStatus])
}

func TestSpawnCallRowsStayInTheParent(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	spawnActor(t, a, contracts.MiMoActorActionSpawn, true)

	messages := sink.Messages()
	require.Len(t, messages, 2, "the spawn call opens and closes in the parent transcript")
	assert.Equal(t, spawnSpanID, messages[0].SpanID)
	assert.True(t, messages[0].NoSpan, "a spawn call owns no span: its child transcript is its body")
	assert.True(t, messages[1].Closing)
	assert.Empty(t, sink.OpenSpans())
}

// A blocking run reports its result through the call's own result row, so no
// separate report reaches the parent.
func TestBlockingRunEndsWithTheCall(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)

	spawnActor(t, a, contracts.MiMoActorActionRun, false)
	runActorTurn(t, a, "**Status**: success\n**Summary**: 42.")
	feed(a,
		actorStatusEvent(t, actorID, contracts.MiMoActorStatusIdle, contracts.MiMoActorOutcomeSuccess, 1, ""),
		toolPartEvent(t, "prt_a", "msg_p", contracts.MiMoToolActor, spawnCallID, toolState{Status: contracts.MiMoToolStatusCompleted,
			Input:    spawnInputOf(contracts.MiMoActorActionRun, "Probe helper", "SUBAGENT-WORK please run one command"),
			Metadata: map[string]any{"sessionId": testSessionID, "actorId": actorID}, Output: "actor_id: general-1\n42"}),
	)
	assert.Equal(t, bgtask.StatusSucceeded, backgroundTask(t, sink, spawnSpanID).Status)
	assert.Empty(t, sink.LeapMuxNotifications())
}

func TestFailedSubagentStatesTheFailureInItsTranscript(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)

	spawnActor(t, a, contracts.MiMoActorActionSpawn, true)
	feed(a,
		messageEvent(t, "msg_c2", roleAssistant, actorID, false),
		textPartEvent(t, partTypeText, "prt_c4", "msg_c2", "", false),
		deltaEvent(t, "prt_c4", "msg_c2", "Partial"),
		actorStatusEvent(t, actorID, contracts.MiMoActorStatusIdle, contracts.MiMoActorOutcomeFailure, 1, "model refused"),
	)
	assert.Equal(t, bgtask.StatusFailed, backgroundTask(t, sink, spawnSpanID).Status)
	childRows := sink.Child("child-of-" + spawnSpanID).Messages()
	require.Len(t, childRows, 3, "prompt, the unfinished text, the failure")
	_, text, completion := assembledText(t, childRows[1].Content)
	assert.Equal(t, "Partial", text)
	assert.Equal(t, string(agent.MessageCompletionError), completion)
	_, text, completion = assembledText(t, childRows[2].Content)
	assert.Equal(t, "model refused", text)
	assert.Equal(t, string(agent.MessageCompletionError), completion)
}

func TestActorOutcomeStatus(t *testing.T) {
	t.Parallel()
	for outcome, want := range map[string]bgtask.Status{
		contracts.MiMoActorOutcomeSuccess:   bgtask.StatusSucceeded,
		"":                                  bgtask.StatusSucceeded,
		contracts.MiMoActorOutcomeFailure:   bgtask.StatusFailed,
		contracts.MiMoActorOutcomeCancelled: bgtask.StatusStopped,
		"timeout":                           bgtask.StatusSucceeded,
	} {
		status, _ := actorOutcomeStatus(outcome)
		assert.Equal(t, want, status, "outcome %q", outcome)
	}
}

// An actor runs again when it receives more work. The running status after an
// idle is the proof of a restart that a revive requires.
func TestSubagentTurnAfterAnIdleRevivesItsRow(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)

	spawnActor(t, a, contracts.MiMoActorActionSpawn, true)
	feed(a, actorStatusEvent(t, actorID, contracts.MiMoActorStatusIdle, contracts.MiMoActorOutcomeSuccess, 1, ""))
	require.Equal(t, bgtask.StatusSucceeded, backgroundTask(t, sink, spawnSpanID).Status)

	feed(a, actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 1, ""))
	assert.Equal(t, []string{spawnSpanID}, sink.RevivedTasks())
	assert.Equal(t, bgtask.StatusRunning, backgroundTask(t, sink, spawnSpanID).Status)

	feed(a, actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 1, ""))
	assert.Len(t, sink.RevivedTasks(), 1, "a repeated running status is the same turn")
}

// A workflow's actor has no spawn call. It gets a transcript on its first
// status, keyed by the session and its id, and opens on its first instruction.
//
// The instruction is the text of the actor's first user message, which MiMo
// sends with no time, and to which MiMo appends its return-format instruction.
// The transcript opens on the task alone, as it does for a spawned actor.
func TestActorWithoutASpawnCall(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)

	feed(a,
		eventJSON(t, eventWorkflowStarted, map[string]any{"sessionID": testSessionID, "runID": "wf_1", "name": "review"}),
		actorRegisteredEvent(t, "reviewer-1", false),
		actorStatusEvent(t, "reviewer-1", contracts.MiMoActorStatusRunning, "", 0, ""),
		messageEvent(t, "msg_w1", roleUser, "reviewer-1", false),
		userTextPartEvent(t, "prt_w1", "msg_w1", "Review the diff."+returnFormatSuffix),
	)
	rowKey := testSessionID + "/reviewer-1"
	row := backgroundTask(t, sink, rowKey)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.Equal(t, "mimo-workflow:wf_1", row.GroupKey, "an actor that registers while one workflow runs belongs to it")
	assert.Equal(t, "review", row.GroupLabel)

	childRows := sink.Child(row.ChildAgentID).Messages()
	require.Len(t, childRows, 1, "the first user message opens the transcript")
	assert.JSONEq(t, `{"content":"Review the diff."}`, string(childRows[0].Content))
	assert.Equal(t, agent.TurnState{Active: true, Steerable: true}, a.ActiveChildTurnState(rowKey),
		"the tab of an actor that no call started finds the actor by its own row key")

	// A later message of the actor is no opening instruction, and adds no row.
	feed(a,
		messageEvent(t, "msg_w2", roleUser, "reviewer-1", false),
		userTextPartEvent(t, "prt_w2", "msg_w2", "Also check the tests."),
	)
	assert.Len(t, sink.Child(row.ChildAgentID).Messages(), 1)
	assert.Empty(t, a.parts, "a user message's parts arrive whole, and none waits for an end")
}

func TestReviewLateUserActorKeepsTheNativeInstruction(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	feed(a, actorRegisteredEvent(t, "reviewer-late", false), actorStatusEvent(t, "reviewer-late", contracts.MiMoActorStatusRunning, "", 0, ""),
		messageEvent(t, "msg_late_instruction", roleUser, "", false),
		userTextPartEvent(t, "part_late_instruction", "msg_late_instruction", "Review the actual native instruction."+returnFormatSuffix),
		messageEvent(t, "msg_late_instruction", roleUser, "reviewer-late", false))
	ids := sink.ChildAgentIDs()
	require.Len(t, ids, 1)
	rows := sink.Child(ids[0]).Messages()
	require.Len(t, rows, 1, "the instruction needs no repeated native part")
	assert.JSONEq(t, `{"content":"Review the actual native instruction."}`, string(rows[0].Content))
	feed(a, messageEvent(t, "msg_late_instruction", roleUser, "reviewer-late", false))
	assert.Len(t, sink.Child(ids[0]).Messages(), 1)
	assert.Empty(t, assembledRows(sink))
}

func TestSubagentInstruction(t *testing.T) {
	t.Parallel()
	for input, want := range map[string]string{
		"Review the diff." + returnFormatSuffix: "Review the diff.",
		"  Review the diff.  ":                  "Review the diff.",
		// A task that quotes the heading keeps its own text: only the last one,
		// which MiMo appends, is cut.
		"Explain" + returnFormatSuffix + "above." + returnFormatSuffix: "Explain" + returnFormatSuffix + "above.",
		returnFormatSuffix: "",
		"":                 "",
	} {
		assert.Equal(t, want, subagentInstruction(input), "input %q", input)
	}
}

func TestSubagentEventsOfAnotherSessionAreIgnored(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	feed(a,
		eventJSON(t, eventActorRegistered, map[string]any{"sessionID": "ses_peer", "actorID": "ses_peer", "mode": "peer", "agent": "general"}),
		eventJSON(t, eventActorStatus, map[string]any{"sessionID": "ses_peer", "actorID": "ses_peer", "status": "running", "turnCount": 0}),
		eventJSON(t, eventActorRegistered, map[string]any{"sessionID": testSessionID, "actorID": mainActorID, "mode": "main"}),
	)
	assert.Empty(t, sink.ChildAgentIDs())
	assert.Empty(t, sink.BackgroundTasks())
}

// A running subagent takes a message as a steer: MiMo joins it into the
// actor's running loop, and the actor's idle status ends that turn.
func TestChildInput(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	spawnActor(t, a, contracts.MiMoActorActionSpawn, true)

	assert.ErrorIs(t, a.SendChildInput(spawnSpanID, "more", nil), agent.ErrAgentBusy, "a running subagent holds the message for a steer")
	assert.Equal(t, agent.TurnState{Active: true, Steerable: true}, a.ActiveChildTurnState(spawnSpanID))

	require.NoError(t, a.SteerChildInput(spawnSpanID, "also this", nil))
	requests := server.requestsTo("POST /session/ses_test/prompt_async")
	require.Len(t, requests, 1)
	assert.JSONEq(t, `{"parts":[{"type":"text","text":"also this"}],"agentID":"general-1","model":{"providerID":"mock","modelID":"alpha"}}`,
		string(requests[0].Body), "a subagent message addresses the actor and states no primary agent")

	feed(a, actorStatusEvent(t, actorID, contracts.MiMoActorStatusIdle, contracts.MiMoActorOutcomeSuccess, 1, ""))
	assert.Equal(t, agent.TurnState{}, a.ActiveChildTurnState(spawnSpanID))
	assert.ErrorIs(t, a.SteerChildInput(spawnSpanID, "late", nil), agent.ErrNoActiveTurn)

	assert.ErrorContains(t, a.SendChildInput("call-unknown", "hello", nil), "no subagent")
	assert.Equal(t, agent.TurnState{}, a.ActiveChildTurnState("call-unknown"))
	assert.Len(t, server.requestsTo("POST /session/ses_test/prompt_async"), 1, "only the steer reached MiMo")
}

// MiMo runs a message to an idle subagent as a turn that no event reports: no
// actor.status starts it or ends it. So the message is refused, with the reason,
// before any request, and the queue records a failure that the user can read.
func TestChildInputToAnIdleSubagentIsRefused(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	spawnActor(t, a, contracts.MiMoActorActionSpawn, true)
	feed(a, actorStatusEvent(t, actorID, contracts.MiMoActorStatusIdle, contracts.MiMoActorOutcomeSuccess, 1, ""))

	err := a.SendChildInput(spawnSpanID, "next task", nil)
	require.ErrorIs(t, err, errSubagentIdle)
	assert.NotErrorIs(t, err, agent.ErrAgentBusy, "no turn runs that could deliver it later")
	assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain, "nothing reached MiMo")
	assert.ErrorContains(t, err, "only while")
	assert.Empty(t, server.requestsTo("POST /session/ses_test/prompt_async"))
}

func TestChildSteerReportsARefusal(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	spawnActor(t, a, contracts.MiMoActorActionSpawn, true)
	server.respond("POST /session/ses_test/prompt_async", http.StatusBadRequest, `{"name":"BadRequest"}`)

	err := a.SteerChildInput(spawnSpanID, "also this", nil)
	require.Error(t, err)
	assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain)
}

// Each child turn reaches its own tab.
// The tab's input queue holds a message while the turn runs and offers it as a steer.
func TestSubagentTurnIsPublishedOnItsOwnTab(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	spawnActor(t, a, contracts.MiMoActorActionSpawn, true)
	child := sink.Child("child-of-" + spawnSpanID)
	require.Equal(t, []bool{true}, child.TurnActives())
	assert.Equal(t, []leapmuxv1.AgentInputKind{leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE}, child.TurnKinds(),
		"a running subagent takes a steer")

	feed(a, actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 0, ""))
	assert.Equal(t, []bool{true}, child.TurnActives(), "a repeated running status is the same turn")

	feed(a, actorStatusEvent(t, actorID, contracts.MiMoActorStatusIdle, contracts.MiMoActorOutcomeSuccess, 1, ""))
	feed(a, actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 1, ""))
	assert.Equal(t, []bool{true, false, true}, child.TurnActives())
	seqs := child.TurnSeqs()
	for i := 1; i < len(seqs); i++ {
		assert.Greater(t, seqs[i], seqs[i-1], "each publish is newer than the one before it")
	}
	assert.Equal(t, []bool{true}, sink.TurnActives(), "the subagent's turn edges reach its own tab only")

	_, err := a.ClearContext()
	require.NoError(t, err)
	assert.Equal(t, []bool{true, false, true, false}, child.TurnActives(), "a session that the agent leaves ends the subagent's turn")
}

// A process that ends ends the turn of each subagent that still runs.
func TestStopEndsTheSubagentsTurn(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	a.SimulateExitForTest()
	spawnActor(t, a, contracts.MiMoActorActionSpawn, true)

	a.Stop()
	assert.Equal(t, []bool{true, false}, sink.Child("child-of-"+spawnSpanID).TurnActives())
}

// The parent's actor send command reaches the child's inbox.
// MiMo's user-message copy produces no new row.
// The call's result writes that message into the child transcript.
func TestParentMessageReachesTheChildTranscript(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	spawnActor(t, a, contracts.MiMoActorActionSpawn, true)
	send := map[string]any{"operation": map[string]any{"action": contracts.MiMoActorActionSend, "to_actor_id": actorID, "content": "Use the fast path."}}

	feed(a,
		toolPartEvent(t, "prt_s", "msg_p", contracts.MiMoToolActor, "call-send", toolState{Status: contracts.MiMoToolStatusRunning, Input: send}),
		toolPartEvent(t, "prt_s", "msg_p", contracts.MiMoToolActor, "call-send", toolState{Status: contracts.MiMoToolStatusCompleted, Input: send, Output: "sent"}),
	)
	childRows := sink.Child("child-of-" + spawnSpanID).Messages()
	last := childRows[len(childRows)-1]
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, last.Source)
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_USER_MESSAGE, last.MarkType)
	assert.JSONEq(t, `{"content":"Use the fast path."}`, string(last.Content))

	before := len(childRows)
	unknown := map[string]any{"operation": map[string]any{"action": contracts.MiMoActorActionSend, "to_actor_id": "explore-9", "content": "Hi."}}
	feed(a, toolPartEvent(t, "prt_u", "msg_p", contracts.MiMoToolActor, "call-send-2", toolState{Status: contracts.MiMoToolStatusCompleted, Input: unknown}))
	assert.Len(t, sink.Child("child-of-"+spawnSpanID).Messages(), before)
	assert.Len(t, sink.ChildAgentIDs(), 1, "a message to an actor the worker never saw opens no transcript")
}

// respondWithSpawnHistory supplies a resumed session whose main actor spawned
// general-1 with spawnSpanID. The history includes a command and a message to an actor.
// Spawn calls without an actor or with the main actor restore no child.
func respondWithSpawnHistory(t *testing.T, server *fakeServer) {
	t.Helper()
	toolPart := func(id, tool, callID string, input, metadata map[string]any) map[string]any {
		state := map[string]any{"status": "completed", "input": input}
		if metadata != nil {
			state["metadata"] = metadata
		}
		return map[string]any{"id": id, "messageID": "msg_p", "type": "tool", "tool": tool, "callID": callID, "state": state}
	}
	history := []map[string]any{
		{"info": map[string]any{"id": "msg_u", "sessionID": testSessionID, "role": roleUser, "agentID": mainActorID, "cost": 9.0},
			"parts": []any{}},
		{"info": map[string]any{"id": "msg_p", "sessionID": testSessionID, "role": roleAssistant, "agentID": mainActorID, "cost": 0.25},
			"parts": []any{
				toolPart("prt_a", contracts.MiMoToolActor, spawnCallID, spawnInputOf(contracts.MiMoActorActionSpawn, "Probe helper", "work"),
					map[string]any{"actorId": actorID}),
				toolPart("prt_b", contracts.MiMoToolBash, "call-x", map[string]any{"command": "ls"}, nil),
				toolPart("prt_c", contracts.MiMoToolActor, "call-send",
					map[string]any{"operation": map[string]any{"action": contracts.MiMoActorActionSend, "to_actor_id": "explore-2", "content": "Hi."}},
					map[string]any{"actorId": "explore-2"}),
				toolPart("prt_d", contracts.MiMoToolActor, "call-no-actor", spawnInputOf(contracts.MiMoActorActionRun, "Lost", "work"), nil),
				toolPart("prt_e", contracts.MiMoToolActor, "call-main", spawnInputOf(contracts.MiMoActorActionRun, "Self", "work"),
					map[string]any{"actorId": mainActorID}),
			}},
	}
	raw, err := json.Marshal(history)
	require.NoError(t, err)
	server.respond("GET /session/ses_test/message", http.StatusOK, string(raw))
}

// A worker restart empties the spawn links. The resumed session's history
// restores them, so the actor's later rows reach its existing tab and row.
func TestRestoreActorLinks(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a, server := newTestAgent(t, agent.NewProviderServices(sink))
	respondWithSpawnHistory(t, server)

	a.restoreResumedSession(a.Context(), testSessionID)
	assert.Equal(t, []string{actorID}, sortedKeys(a.actors), "only a spawn call that identifies a subagent restores a link")
	assert.Equal(t, map[string]string{spawnSpanID: actorID}, a.spawnActors)
	actor, _, ok := a.actorForRowKey(spawnSpanID)
	require.True(t, ok)
	assert.Equal(t, actorID, actor.id)
	assert.True(t, actor.closed, "the new process runs no turn of it yet")
	assert.ErrorIs(t, a.SendChildInput(spawnSpanID, "resume the work", nil), errSubagentIdle)
	assert.Equal(t, agent.TurnState{}, a.ActiveChildTurnState(spawnSpanID))
	assert.InDelta(t, 0.25, a.usageSnapshot().costUSD, 1e-9, "the session's cost so far carries over, and only an assistant message costs")

	feed(a, actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 2, ""))
	assert.Equal(t, bgtask.StatusRunning, backgroundTask(t, sink, spawnSpanID).Status, "a later turn opens the row that the spawn call keys")
	require.NoError(t, a.SteerChildInput(spawnSpanID, "resume the work", nil))
	assert.JSONEq(t, `{"parts":[{"type":"text","text":"resume the work"}],"agentID":"general-1","model":{"providerID":"mock","modelID":"alpha"}}`,
		string(server.requestsTo("POST /session/ses_test/prompt_async")[0].Body))
}

func TestRestoreResumedSessionSurvivesAnUnreadableHistory(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)
	server.respond("GET /session/ses_test/message", http.StatusInternalServerError, `{}`)
	a.restoreResumedSession(a.Context(), testSessionID)
	_, _, ok := a.actorForRowKey(spawnSpanID)
	assert.False(t, ok)
}

// A restored actor starts closed. A status that ends a turn it never ran closes
// nothing, and a pending actor opens no row until its turn runs.
func TestActorStatusesThatOpenAndCloseNoRow(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	var closed []string
	sink.OnCloseBackgroundTask = func(rowKey string, _ bgtask.Status) { closed = append(closed, rowKey) }
	a, server := newTestAgent(t, agent.NewProviderServices(sink))
	respondWithSpawnHistory(t, server)
	a.restoreResumedSession(a.Context(), testSessionID)

	feed(a,
		actorStatusEvent(t, actorID, contracts.MiMoActorStatusIdle, contracts.MiMoActorOutcomeSuccess, 2, ""),
		actorStatusEvent(t, "explore-2", "pending", "", 0, ""),
		actorStatusEvent(t, mainActorID, contracts.MiMoActorStatusRunning, "", 0, ""),
		eventJSON(t, eventActorStatus, map[string]any{"sessionID": testSessionID, "status": contracts.MiMoActorStatusRunning}),
		eventJSON(t, eventActorStatus, "not an object"),
	)
	assert.Empty(t, closed)
	assert.Empty(t, sink.BackgroundTasks())
	assert.Empty(t, sink.ChildAgentIDs(), "no transcript opens for an actor that ran no turn")
	assert.Empty(t, sink.LeapMuxNotifications())
}

// A background subagent reports its last text of each turn. A turn with no
// text reports nothing, and the next turn reports only its own text.
func TestBackgroundSubagentReportsEachTurnsOwnText(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	spawnActor(t, a, contracts.MiMoActorActionSpawn, true)

	feed(a, actorStatusEvent(t, actorID, contracts.MiMoActorStatusIdle, contracts.MiMoActorOutcomeSuccess, 1, ""))
	assert.Empty(t, sink.LeapMuxNotifications(), "a turn that wrote no text has nothing to report")
	assert.Equal(t, bgtask.StatusSucceeded, backgroundTask(t, sink, spawnSpanID).Status)

	feed(a,
		actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 1, ""),
		messageEvent(t, "msg_c5", roleAssistant, actorID, false),
		textPartEvent(t, partTypeText, "prt_c5", "msg_c5", "Second answer.", true),
		actorStatusEvent(t, actorID, contracts.MiMoActorStatusIdle, contracts.MiMoActorOutcomeCancelled, 2, ""),
	)
	reports := sink.LeapMuxNotifications()
	require.Len(t, reports, 1)
	assert.Equal(t, "Second answer.", reports[0][contracts.NotificationFieldText])
	assert.Equal(t, bgtask.StatusWire(bgtask.StatusStopped), reports[0][contracts.NotificationFieldStatus], "a cancelled turn reports a stop")
	assert.Equal(t, bgtask.StatusStopped, backgroundTask(t, sink, spawnSpanID).Status)

	feed(a,
		actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 2, ""),
		actorStatusEvent(t, actorID, contracts.MiMoActorStatusIdle, contracts.MiMoActorOutcomeSuccess, 3, ""),
	)
	assert.Len(t, sink.LeapMuxNotifications(), 1, "a report is not repeated for a later turn with no text")
}

// childlessSink is a recording sink that cannot create a child transcript.
type childlessSink struct {
	*agenttest.Sink
}

func (childlessSink) EnsureChildAgent(agent.ChildAgentSpec) (string, error) {
	return "", errors.New("the database is closed")
}

// A subagent that the worker cannot give a transcript still ran.
// The main transcript keeps its rows.
// No registry row identifies a transcript that does not exist.
func TestSubagentWithoutATranscriptWritesToTheMainTranscript(t *testing.T) {
	t.Parallel()
	sink := childlessSink{Sink: &agenttest.Sink{}}
	a, _ := newTestAgent(t, agent.NewProviderServices(sink))

	feed(a,
		actorRegisteredEvent(t, "reviewer-1", false),
		actorStatusEvent(t, "reviewer-1", contracts.MiMoActorStatusRunning, "", 0, ""),
		messageEvent(t, "msg_w2", roleAssistant, "reviewer-1", false),
		textPartEvent(t, partTypeText, "prt_w2", "msg_w2", "Found two issues.", true),
		actorStatusEvent(t, "reviewer-1", contracts.MiMoActorStatusIdle, contracts.MiMoActorOutcomeFailure, 1, "model refused"),
	)
	messages := sink.Messages()
	require.Len(t, messages, 2)
	_, text, _ := assembledText(t, messages[0].Content)
	assert.Equal(t, "Found two issues.", text)
	_, text, completion := assembledText(t, messages[1].Content)
	assert.Equal(t, "model refused", text)
	assert.Equal(t, string(agent.MessageCompletionError), completion)
	assert.Empty(t, sink.BackgroundTasks())
	assert.Empty(t, sink.ChildAgentIDs())
}

func TestChildSteerRefusalsSendNothing(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	spawnActor(t, a, contracts.MiMoActorActionSpawn, true)

	binary := []*leapmuxv1.Attachment{{Filename: "tool.bin", MimeType: "application/octet-stream", Data: []byte{0xff, 0x00}}}
	assert.ErrorContains(t, a.SteerChildInput(spawnSpanID, "run it", binary), "tool.bin")

	a.sessionID = ""
	assert.ErrorContains(t, a.SteerChildInput(spawnSpanID, "more", nil), "no MiMo session")

	a.sessionID = testSessionID
	a.SetStoppedForTest(true)
	assert.ErrorContains(t, a.SteerChildInput(spawnSpanID, "more", nil), "stopped")
	assert.Empty(t, server.requestsTo("POST /session/ses_test/prompt_async"))
}

// Only a completed `actor send` with text for a subagent that the worker knows
// reaches that subagent's transcript.
func TestParentMessagesThatReachNoTranscript(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	spawnActor(t, a, contracts.MiMoActorActionSpawn, true)
	childID := "child-of-" + spawnSpanID
	before := len(sink.Child(childID).Messages())
	send := func(to, content string) map[string]any {
		return map[string]any{"operation": map[string]any{"action": contracts.MiMoActorActionSend, "to_actor_id": to, "content": content}}
	}

	feed(a,
		toolPartEvent(t, "prt_s1", "msg_p", contracts.MiMoToolActor, "call-send-1", toolState{Status: contracts.MiMoToolStatusError,
			Input: send(actorID, "Use the fast path."), Error: "the actor is gone"}),
		toolPartEvent(t, "prt_s2", "msg_p", contracts.MiMoToolActor, "call-send-2", toolState{Status: contracts.MiMoToolStatusCompleted,
			Input: send(actorID, "   ")}),
		toolPartEvent(t, "prt_s3", "msg_p", contracts.MiMoToolActor, "call-send-3", toolState{Status: contracts.MiMoToolStatusCompleted,
			Input: send(mainActorID, "Hello, me.")}),
		toolPartEvent(t, "prt_s4", "msg_p", contracts.MiMoToolActor, "call-send-4", toolState{Status: contracts.MiMoToolStatusCompleted,
			Input: map[string]any{"operation": map[string]any{"action": "list"}}}),
	)
	assert.Len(t, sink.Child(childID).Messages(), before)
	assert.Equal(t, []string{childID}, sink.ChildAgentIDs())
}

func TestMiMoActorTitle(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", 100)
	for _, tc := range []struct {
		name  string
		actor mimoActor
		want  string
	}{
		{name: "the description's first line", actor: mimoActor{id: actorID, agentType: "general", description: " Probe helper\nmore detail "},
			want: "Probe helper"},
		{name: "the agent type for a blank description", actor: mimoActor{id: actorID, agentType: "general", description: "  "}, want: "general"},
		{name: "the id when nothing else is known", actor: mimoActor{id: actorID}, want: actorID},
		{name: "a fixed label for an actor with no id", actor: mimoActor{}, want: "MiMo subagent"},
		{name: "at most 80 runes", actor: mimoActor{description: long}, want: strings.Repeat("x", 80)},
	} {
		assert.Equal(t, tc.want, tc.actor.title(), tc.name)
	}
}

func TestSpawnTitle(t *testing.T) {
	t.Parallel()
	part := func(input map[string]any, title string) mimoPart {
		raw, err := json.Marshal(input)
		require.NoError(t, err)
		return mimoPart{Tool: contracts.MiMoToolActor, State: &mimoToolState{Input: raw, Title: title}}
	}

	assert.Equal(t, "Probe helper", spawnTitle(part(spawnInputOf(contracts.MiMoActorActionSpawn, " Probe helper ", "work"), "Ignored")))
	assert.Equal(t, "Run the probe", spawnTitle(part(spawnInputOf(contracts.MiMoActorActionSpawn, "", "work"), " Run the probe ")),
		"the call's title serves when the input states no description")
	assert.Equal(t, "general", spawnTitle(part(spawnInputOf(contracts.MiMoActorActionSpawn, "", "work"), "")),
		"the subagent type serves when nothing else does")
	assert.Empty(t, spawnTitle(mimoPart{Tool: contracts.MiMoToolActor}))
}

func TestIsSpawnCall(t *testing.T) {
	t.Parallel()
	part := func(tool string, input map[string]any) mimoPart {
		raw, err := json.Marshal(input)
		require.NoError(t, err)
		return mimoPart{Tool: tool, State: &mimoToolState{Input: raw}}
	}

	assert.True(t, isSpawnCall(part(contracts.MiMoToolActor, spawnInputOf(contracts.MiMoActorActionSpawn, "", ""))))
	assert.True(t, isSpawnCall(part(contracts.MiMoToolActor, spawnInputOf(contracts.MiMoActorActionRun, "", ""))))
	assert.False(t, isSpawnCall(part(contracts.MiMoToolActor, spawnInputOf(contracts.MiMoActorActionSend, "", ""))),
		"a send addresses an actor that already exists")
	assert.False(t, isSpawnCall(part(contracts.MiMoToolBash, spawnInputOf(contracts.MiMoActorActionSpawn, "", ""))),
		"only the actor tool starts a subagent")
	assert.False(t, isSpawnCall(part(contracts.MiMoToolActor, map[string]any{"operation": "spawn"})), "an input that is no operation")
	assert.False(t, isSpawnCall(mimoPart{Tool: contracts.MiMoToolActor}), "a call with no state")
}

func backgroundTask(t *testing.T, sink *agenttest.Sink, rowKey string) bgtask.Item {
	t.Helper()
	for _, item := range sink.BackgroundTasks() {
		if item.RowKey == rowKey {
			return item
		}
	}
	require.Failf(t, "no registry row", "row key %q in %v", rowKey, agenttest.RowKeys(sink))
	return bgtask.Item{}
}

func TestReviewUnresolvedUserInstructionSurvivesParentSettlement(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy),
		actorRegisteredEvent(t, actorID, true), actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 0, ""),
		messageEvent(t, "msg_delayed_actor", roleUser, "", false),
		userTextPartEvent(t, "part_delayed_actor", "msg_delayed_actor", "Keep the native child task."),
		statusEvent(t, contracts.MiMoStatusTypeIdle),
		messageEvent(t, "msg_delayed_actor", roleUser, actorID, false))
	ids := sink.ChildAgentIDs()
	require.Len(t, ids, 1)
	rows := sink.Child(ids[0]).Messages()
	require.Len(t, rows, 1)
	assert.JSONEq(t, `{"content":"Keep the native child task."}`, string(rows[0].Content))
}

type controlledInstructionSink struct {
	agent.ProviderServices
	reject  int
	targets []string
	prompts []string
}

func (s *controlledInstructionSink) PersistChildPrompt(childID, prompt string) error {
	s.targets = append(s.targets, childID)
	s.prompts = append(s.prompts, prompt)
	if s.reject > 0 {
		s.reject--
		return errors.New("the native instruction write failed")
	}
	return s.ProviderServices.PersistChildPrompt(childID, prompt)
}

func TestReviewUserInstructionRetriesOnItsCapturedChild(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	controlled := &controlledInstructionSink{ProviderServices: a.sink, reject: 10}
	a.sink = controlled
	feed(a, actorRegisteredEvent(t, actorID, true), actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 0, ""),
		messageEvent(t, "msg_old_instruction", roleUser, actorID, false),
		userTextPartEvent(t, "part_old_instruction", "msg_old_instruction", "The old native instruction."))
	ids := sink.ChildAgentIDs()
	require.Len(t, ids, 1)
	oldID := ids[0]
	require.Empty(t, sink.Child(oldID).Messages())
	sessionID, err := a.ClearContext()
	require.NoError(t, err)
	controlled.reject = 0
	feed(a, eventWithSession(t, actorRegisteredEvent(t, actorID, true), sessionID),
		eventWithSession(t, actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 0, ""), sessionID),
		eventWithSession(t, messageEvent(t, "msg_new_instruction", roleUser, actorID, false), sessionID),
		eventWithSession(t, userTextPartEvent(t, "part_new_instruction", "msg_new_instruction", "The new native instruction."), sessionID))
	a.flushUnfinishedOutput(agent.MessageCompletionComplete)
	rows := sink.Child(oldID).Messages()
	require.Len(t, rows, 1)
	assert.JSONEq(t, `{"content":"The old native instruction."}`, string(rows[0].Content))
	newID := a.actors[actorID].childAgentID
	require.NotEmpty(t, newID)
	require.NotEqual(t, oldID, newID)
	newRows := sink.Child(newID).Messages()
	require.Len(t, newRows, 1)
	assert.JSONEq(t, `{"content":"The new native instruction."}`, string(newRows[0].Content))
	assert.Equal(t, oldID, controlled.targets[len(controlled.targets)-1])
	assert.NotContains(t, a.parts, "part_old_instruction")
	assert.Empty(t, assembledRows(sink))
}

func TestReviewExactMainUserInstructionAddsNoConversationRow(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	feed(a, messageEvent(t, "msg_native_user", roleUser, mainActorID, false),
		userTextPartEvent(t, "part_native_user", "msg_native_user", "The worker already stores this input."))
	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.ChildAgentIDs())
	assert.Empty(t, a.parts)
	require.Contains(t, a.messages, "msg_native_user")
	_, err := a.ClearContext()
	require.NoError(t, err)
	assert.NotContains(t, a.messages, "msg_native_user", "closure releases the proved main copy's identity")
	assert.Empty(t, a.parts)
	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.ChildAgentIDs())
}

func TestReviewSessionEndDropsAnUnresolvedUserInstruction(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	feed(a, messageEvent(t, "msg_unresolved_user", roleUser, "", false),
		userTextPartEvent(t, "part_unresolved_user", "msg_unresolved_user", "An unknown instruction owner."))
	require.Contains(t, a.parts, "part_unresolved_user")
	_, err := a.ClearContext()
	require.NoError(t, err)
	require.Contains(t, a.parts, "part_unresolved_user")
	part := a.parts["part_unresolved_user"].final
	require.NotNil(t, part)
	assert.Equal(t, "An unknown instruction owner.", part.Text)
	assert.Equal(t, testSessionID, a.messages["msg_unresolved_user"].sessionID)
	assert.Empty(t, sink.ChildAgentIDs())
	assert.Empty(t, assembledRows(sink))
}

func TestReviewConfirmedUserActorCannotChangeItsInstructionOwner(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	controlled := &controlledInstructionSink{ProviderServices: a.sink, reject: 1}
	a.sink = controlled
	feed(a, messageEvent(t, "msg_fixed_instruction", roleUser, actorID, false),
		userTextPartEvent(t, "part_fixed_instruction", "msg_fixed_instruction", "Keep the exact first owner."),
		messageEvent(t, "msg_fixed_instruction", roleUser, "conflicting-child", false))
	assert.Equal(t, actorID, a.messages["msg_fixed_instruction"].actorID)
	a.flushUnfinishedOutput(agent.MessageCompletionComplete)
	assert.NotContains(t, a.actors, "conflicting-child")
	ids := sink.ChildAgentIDs()
	require.Len(t, ids, 1)
	rows := sink.Child(ids[0]).Messages()
	require.Len(t, rows, 1)
	assert.JSONEq(t, `{"content":"Keep the exact first owner."}`, string(rows[0].Content))
}

func TestReviewWholeInstructionWaitsForRoleAfterAFailedMessageRead(t *testing.T) {
	t.Parallel()
	for _, settle := range []bool{false, true} {
		t.Run(fmt.Sprint(settle), func(t *testing.T) {
			t.Parallel()
			a, sink, server := newSinkTestAgent(t)
			server.respond("GET /session/ses_test/message/msg_unresolved_role", http.StatusInternalServerError, `{}`)
			feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy),
				actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 0, ""),
				userTextPartEvent(t, "part_unresolved_role", "msg_unresolved_role", "The native whole instruction."+returnFormatSuffix))
			if settle {
				feed(a, statusEvent(t, contracts.MiMoStatusTypeIdle))
			}
			feed(a, messageEvent(t, "msg_unresolved_role", roleUser, actorID, false))
			ids := sink.ChildAgentIDs()
			require.Len(t, ids, 1)
			rows := sink.Child(ids[0]).Messages()
			require.Len(t, rows, 1)
			assert.JSONEq(t, `{"content":"The native whole instruction."}`, string(rows[0].Content))
			feed(a, messageEvent(t, "msg_unresolved_role", roleUser, actorID, false))
			assert.Len(t, sink.Child(ids[0]).Messages(), 1)
			assert.Empty(t, assembledRows(sink))
		})
	}
}

func TestReviewUnresolvedInstructionsKeepTheirNativeObservationOrder(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	controlled := &controlledInstructionSink{ProviderServices: a.sink}
	a.sink = controlled
	server.respond("GET /session/ses_test/message/msg_ordered_instructions", http.StatusInternalServerError, `{}`)
	feed(a, actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 0, ""))
	var instructions []string
	for index := range 100 {
		instruction := fmt.Sprintf("Native instruction %03d.", index)
		instructions = append(instructions, instruction)
		feed(a, userTextPartEvent(t, fmt.Sprintf("part_instruction_%03d", index), "msg_ordered_instructions", instruction))
	}
	feed(a, messageEvent(t, "msg_ordered_instructions", roleUser, actorID, false))
	assert.Equal(t, instructions, controlled.prompts)
	ids := sink.ChildAgentIDs()
	require.Len(t, ids, 1)
	rows := sink.Child(ids[0]).Messages()
	require.Len(t, rows, 1)
	assert.JSONEq(t, `{"content":"Native instruction 000."}`, string(rows[0].Content))
}

func TestReviewRetainedInstructionPrecedesTheChildAnswer(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	controlled := &controlledInstructionSink{ProviderServices: a.sink, reject: 1}
	a.sink = controlled
	feed(a, actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 0, ""),
		messageEvent(t, "msg_first_instruction", roleUser, actorID, false),
		userTextPartEvent(t, "part_first_instruction", "msg_first_instruction", "Keep this native instruction first."),
		messageEvent(t, "msg_later_answer", roleAssistant, actorID, false),
		textPartEvent(t, partTypeText, "part_later_answer", "msg_later_answer", "The native child answer.", true))
	ids := sink.ChildAgentIDs()
	require.Len(t, ids, 1)
	rows := sink.Child(ids[0]).Messages()
	require.Len(t, rows, 2)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, rows[0].Source)
	assert.JSONEq(t, `{"content":"Keep this native instruction first."}`, string(rows[0].Content))
	_, text, _ := assembledText(t, rows[1].Content)
	assert.Equal(t, "The native child answer.", text)
}

type controlledChildPromptSink struct {
	agent.ProviderServices
	failures atomic.Int32
}

func (s *controlledChildPromptSink) PersistChildPrompt(childID, prompt string) error {
	if s.failures.Load() > 0 {
		s.failures.Add(-1)
		return errors.New("the controlled child prompt write failed")
	}
	return s.ProviderServices.PersistChildPrompt(childID, prompt)
}

func TestFailedChildSetupRetainsItsOriginalSpawnPrompt(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"creation", "prompt write"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newSinkTestAgent(t)
			creation := &recoveringChildPartSink{ProviderServices: a.sink}
			prompt := &controlledChildPromptSink{ProviderServices: creation}
			a.sink = prompt
			if failure == "creation" {
				creation.creationFails.Store(true)
			} else {
				prompt.failures.Store(10)
			}
			spawnActor(t, a, contracts.MiMoActorActionSpawn, true)
			creation.creationFails.Store(false)
			prompt.failures.Store(0)
			childID, ok := a.ensureActorTranscript(actorID)
			require.True(t, ok)
			rows := sink.Child(childID).Messages()
			require.Len(t, rows, 1, "native spawn completion must not discard an unwritten original task")
			assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, rows[0].Source)
			assert.JSONEq(t, `{"content":"SUBAGENT-WORK please run one command"}`, string(rows[0].Content))
			_, ok = a.ensureActorTranscript(actorID)
			require.True(t, ok)
			assert.Len(t, sink.Child(childID).Messages(), 1)
		})
	}
}

func TestFailedActorFailureWriteRetainsItsOriginalChildTarget(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	controlled := &controlledPartSink{ProviderServices: a.sink, rejectText: "The original native actor failure."}
	a.sink = controlled
	spawnActor(t, a, contracts.MiMoActorActionSpawn, true)
	controlled.reject.Store(10)
	feed(a, actorStatusEvent(t, actorID, contracts.MiMoActorStatusIdle, contracts.MiMoActorOutcomeFailure, 1, "The original native actor failure."))
	newSession, err := a.ClearContext()
	require.NoError(t, err)
	feed(a, eventWithSession(t, actorRegisteredEvent(t, actorID, false), newSession),
		eventWithSession(t, actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 0, ""), newSession))
	replacementID := a.actors[actorID].childAgentID
	controlled.reject.Store(0)
	a.flushUnfinishedOutput(agent.MessageCompletionComplete)
	assert.Empty(t, sink.Child(replacementID).Messages())
	rows := assembledRows(sink.Child("child-of-" + spawnSpanID))
	require.Len(t, rows, 1, "a failed actor failure row must survive its session clear")
	_, text, completion := assembledText(t, rows[0].Content)
	assert.Equal(t, "The original native actor failure.", text)
	assert.Equal(t, string(agent.MessageCompletionError), completion)
	assert.Equal(t, testSessionID, rows[0].AgentSessionID)
	assert.Empty(t, assembledRows(sink))
}

func TestPendingChildOpeningKeepsItsRegistryAndLiveTurn(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	controlled := &controlledChildPromptSink{ProviderServices: a.sink}
	controlled.failures.Store(10)
	a.sink = controlled
	spawnActor(t, a, contracts.MiMoActorActionSpawn, true)
	row := backgroundTask(t, sink, spawnSpanID)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.Equal(t, "child-of-"+spawnSpanID, row.ChildAgentID)
	active, published := sink.Child(row.ChildAgentID).LastTurnActive()
	assert.True(t, published, "opening-row persistence cannot hide actual native activity")
	assert.True(t, active)
	assert.Empty(t, sink.Child(row.ChildAgentID).Messages())
}

func TestLateAssistantParentLinkKeepsTheFirstNativeInstruction(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	feed(a, actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 0, ""),
		messageEvent(t, "msg_parent_link_user", roleUser, "", false),
		userTextPartEvent(t, "part_parent_link_user", "msg_parent_link_user", "The native linked instruction."),
		eventJSON(t, eventMessageUpdated, map[string]any{"info": map[string]any{
			"id": "msg_parent_link_answer", "sessionID": testSessionID, "role": roleAssistant,
			"agentID": actorID, "parentID": "msg_parent_link_user",
		}}),
		textPartEvent(t, partTypeText, "part_parent_link_answer", "msg_parent_link_answer", "The native linked answer.", true),
		messageEvent(t, "msg_parent_link_user", roleUser, actorID, false))
	ids := sink.ChildAgentIDs()
	require.Len(t, ids, 1)
	rows := sink.Child(ids[0]).Messages()
	require.Len(t, rows, 2)
	assert.JSONEq(t, `{"content":"The native linked instruction."}`, string(rows[0].Content))
	_, text, _ := assembledText(t, rows[1].Content)
	assert.Equal(t, "The native linked answer.", text)
}

func TestLateActorFailureWithZeroTurnCountRetriesOnce(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	controlled := &controlledPartSink{ProviderServices: a.sink, rejectText: "The zero-count native failure."}
	a.sink = controlled
	spawnActor(t, a, contracts.MiMoActorActionSpawn, true)
	controlled.reject.Store(10)
	failure := actorStatusEvent(t, actorID, contracts.MiMoActorStatusIdle, contracts.MiMoActorOutcomeFailure, 0, "The zero-count native failure.")
	feed(a, failure, failure)
	controlled.reject.Store(0)
	a.flushUnfinishedOutput(agent.MessageCompletionComplete)
	a.flushUnfinishedOutput(agent.MessageCompletionComplete)
	rows := assembledRows(sink.Child("child-of-" + spawnSpanID))
	require.Len(t, rows, 1)
	_, text, completion := assembledText(t, rows[0].Content)
	assert.Equal(t, "The zero-count native failure.", text)
	assert.Equal(t, string(agent.MessageCompletionError), completion)
	assert.Equal(t, testSessionID, rows[0].AgentSessionID)
}

func TestLateCapturedActorSetupCannotPublishAReplacementRow(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	old := &mimoActor{id: actorID, rowKey: "old-row", running: true}
	replacement := &mimoActor{id: actorID, rowKey: "replacement-row", childAgentID: "replacement-child", closed: true}
	a.actors[actorID] = replacement
	childID, ok := a.ensureActorRecordTranscript(old, "old-session")
	require.True(t, ok)
	require.NotEmpty(t, childID)
	for _, row := range sink.BackgroundTasks() {
		assert.NotEqual(t, replacement.rowKey, row.RowKey)
	}
	assert.True(t, replacement.closed)
	assert.Equal(t, "replacement-child", replacement.childAgentID)
}

func TestLateChildInputKeepsTheCapturedNativeSession(t *testing.T) {
	t.Parallel()
	a, _, server := newSinkTestAgent(t)
	spawnActor(t, a, contracts.MiMoActorActionSpawn, true)
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- a.sendChildInputBuilt(spawnSpanID, true, func() ([]mimoPromptPart, error) {
			close(entered)
			<-release
			return buildPromptParts("The original child delivery.", nil)
		})
	}()
	select {
	case <-entered:
	case <-time.After(testTimeout):
		t.Fatal("the child input did not reach prompt construction")
	}
	sessionID, err := a.ClearContext()
	require.NoError(t, err)
	feed(a, eventWithSession(t, actorRegisteredEvent(t, actorID, true), sessionID),
		eventWithSession(t, actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 0, ""), sessionID))
	close(release)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(testTimeout):
		t.Fatal("the captured child delivery did not finish")
	}
	assert.Len(t, server.requestsTo("POST /session/"+testSessionID+"/prompt_async"), 1)
	assert.Empty(t, server.requestsTo("POST /session/"+sessionID+"/prompt_async"))
}

func TestLateParentLinkPrioritizesAnAlreadyPendingAnswer(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	server.respond("GET /session/ses_test/message/msg_delayed_user", http.StatusInternalServerError, `{}`)
	server.respond("GET /session/ses_test/message/msg_delayed_answer", http.StatusInternalServerError, `{}`)
	feed(a, actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 0, ""),
		userTextPartEvent(t, "part_delayed_user", "msg_delayed_user", "The original native instruction."),
		nativeAbortMarker(t, "session", "msg_delayed_answer"),
		textPartEvent(t, partTypeText, "part_delayed_answer", "msg_delayed_answer", "The retained native answer.", true))
	feed(a, eventJSON(t, eventMessageUpdated, map[string]any{"info": map[string]any{
		"id": "msg_delayed_answer", "sessionID": testSessionID, "role": roleAssistant, "agentID": actorID,
		"parentID": "msg_delayed_user", "time": map[string]any{"completed": 2},
	}}), messageEvent(t, "msg_delayed_user", roleUser, actorID, false))
	ids := sink.ChildAgentIDs()
	require.Len(t, ids, 1)
	rows := sink.Child(ids[0]).Messages()
	require.Len(t, rows, 2)
	assert.JSONEq(t, `{"content":"The original native instruction."}`, string(rows[0].Content))
	_, text, _ := assembledText(t, rows[1].Content)
	assert.Equal(t, "The retained native answer.", text)
	assert.Empty(t, assembledRows(sink))
}

func TestLateConvertedUserOrderPrecedesAHeldKnownChildAnswer(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	controlled := &controlledPartSink{ProviderServices: a.sink, rejectText: "The held known child answer."}
	a.sink = controlled
	server.respond("GET /session/ses_test/message/msg_conversion_user", http.StatusInternalServerError, `{}`)
	feed(a, actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 0, ""),
		userTextPartEvent(t, "part_conversion_user", "msg_conversion_user", "The first native converted instruction."),
		messageEvent(t, "msg_conversion_answer", roleAssistant, actorID, false))
	controlled.reject.Store(10)
	feed(a, textPartEvent(t, partTypeText, "part_conversion_answer", "msg_conversion_answer", "The held known child answer.", true),
		messageEvent(t, "msg_conversion_user", roleUser, "", false))
	feed(a, messageEvent(t, "msg_independent_root", roleAssistant, mainActorID, false),
		textPartEvent(t, partTypeText, "part_independent_root", "msg_independent_root", "The independent root answer.", true),
		messageEvent(t, "msg_independent_child", roleAssistant, "other-child", false),
		textPartEvent(t, partTypeText, "part_independent_child", "msg_independent_child", "The independent child answer.", true))
	rootRows := assembledRows(sink)
	require.Len(t, rootRows, 1)
	_, independent, _ := assembledText(t, rootRows[0].Content)
	assert.Equal(t, "The independent root answer.", independent)
	otherRows := assembledRows(sink.Child(a.actors["other-child"].childAgentID))
	require.Len(t, otherRows, 1)
	_, independent, _ = assembledText(t, otherRows[0].Content)
	assert.Equal(t, "The independent child answer.", independent)
	controlled.reject.Store(0)
	feed(a, eventJSON(t, eventMessageUpdated, map[string]any{"info": map[string]any{
		"id": "msg_conversion_answer", "sessionID": testSessionID, "role": roleAssistant, "agentID": actorID,
		"parentID": "msg_conversion_user", "time": map[string]any{"completed": 2},
	}}))
	require.Len(t, sink.ChildAgentIDs(), 2)
	rows := sink.Child(a.actors[actorID].childAgentID).Messages()
	require.Len(t, rows, 2)
	assert.JSONEq(t, `{"content":"The first native converted instruction."}`, string(rows[0].Content))
	_, text, _ := assembledText(t, rows[1].Content)
	assert.Equal(t, "The held known child answer.", text)
}

func TestLateSpawnInstructionKeepsItsFirstTextBeforeAnAnswer(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	controlled := &controlledChildPromptSink{ProviderServices: a.sink}
	controlled.failures.Store(10)
	a.sink = controlled
	a.linkSpawn("part_first_spawn", actorID, "The child", "The first native spawn instruction.")
	a.linkSpawn("part_first_spawn", actorID, "The child", "A later duplicate instruction.")
	feed(a, messageEvent(t, "msg_spawn_answer", roleAssistant, actorID, false),
		textPartEvent(t, partTypeText, "part_spawn_answer", "msg_spawn_answer", "The answer after the spawn instruction.", true))
	childID := a.actors[actorID].childAgentID
	assert.Empty(t, sink.Child(childID).Messages())
	controlled.failures.Store(0)
	a.flushPendingMessage("msg_spawn_answer", "")
	rows := sink.Child(childID).Messages()
	require.Len(t, rows, 2)
	assert.JSONEq(t, `{"content":"The first native spawn instruction."}`, string(rows[0].Content))
	_, text, _ := assembledText(t, rows[1].Content)
	assert.Equal(t, "The answer after the spawn instruction.", text)
	a.linkSpawn("part_empty_spawn", "empty-child", "The empty child", "")
	assert.Empty(t, sink.Child(a.actors["empty-child"].childAgentID).Messages())
}

func TestLateParentLinkPreservesKnownActorAndSession(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name           string
		known, foreign bool
	}{
		{name: "known other child", known: true}, {name: "retained foreign session", foreign: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newSinkTestAgent(t)
			owner := ""
			if tc.known {
				owner = "other-child"
			}
			feed(a, messageEvent(t, "msg_guard_parent", roleUser, owner, false),
				userTextPartEvent(t, "part_guard_parent", "msg_guard_parent", "The original owner's instruction."))
			if tc.foreign {
				_, err := a.ClearContext()
				require.NoError(t, err)
			}
			session := a.sessionID
			feed(a, eventJSON(t, eventMessageUpdated, map[string]any{"info": map[string]any{
				"id": "msg_guard_answer", "sessionID": session, "role": roleAssistant, "agentID": actorID, "parentID": "msg_guard_parent",
			}}))
			if tc.known {
				assert.Equal(t, "other-child", a.messages["msg_guard_parent"].actorID)
			}
			if record := a.messages["msg_guard_parent"]; record != nil {
				assert.NotEqual(t, actorID, record.actorID)
			}
			for _, id := range sink.ChildAgentIDs() {
				if id == a.actors[actorID].childAgentID {
					assert.Empty(t, sink.Child(id).Messages())
				}
			}
		})
	}
}

func TestLateMainSpawnAndRunKeepTheirRootToolCount(t *testing.T) {
	t.Parallel()
	for _, action := range []string{contracts.MiMoActorActionSpawn, contracts.MiMoActorActionRun} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newSinkTestAgent(t)
			spawnActor(t, a, action, true)
			runActorTurn(t, a, "The child final.")
			feed(a, statusEvent(t, contracts.MiMoStatusTypeIdle))
			var dividers []agenttest.Message
			for _, row := range sink.Messages() {
				if row.TurnEnd {
					dividers = append(dividers, row)
				}
			}
			require.Len(t, dividers, 1)
			var metadata map[string]any
			require.NoError(t, json.Unmarshal(dividers[0].Metadata, &metadata))
			assert.EqualValues(t, 1, metadata[contracts.MessageMetadataFieldToolUses])
			child := sink.Child("child-of-" + spawnSpanID)
			rows := assembledRows(child)
			require.NotEmpty(t, rows)
			_, text, _ := assembledText(t, rows[len(rows)-1].Content)
			assert.Equal(t, "The child final.", text)
		})
	}
}

func TestLateChildCreationStoresItsCapturedNativeSession(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	feed(a, actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 0, ""))
	originalActor := a.actors[actorID]
	originalChild := originalActor.childAgentID
	require.NotEmpty(t, originalChild)
	assert.Equal(t, testSessionID, sink.Child(originalChild).LastSessionID())
	sessionID, err := a.ClearContext()
	require.NoError(t, err)
	feed(a, eventWithSession(t, actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 0, ""), sessionID))
	replacementChild := a.actors[actorID].childAgentID
	require.NotEmpty(t, replacementChild)
	assert.NotEqual(t, originalChild, replacementChild)
	assert.Equal(t, sessionID, sink.Child(replacementChild).LastSessionID())
	assert.Equal(t, testSessionID, sink.Child(originalChild).LastSessionID())
	retainedChild, ok := a.ensureActorRecordTranscript(originalActor, testSessionID)
	require.True(t, ok)
	assert.Equal(t, originalChild, retainedChild)
	assert.Equal(t, testSessionID, sink.Child(retainedChild).LastSessionID())
}

func TestLateRetainedActorFailureKeepsReplacementProgress(t *testing.T) {
	t.Parallel()
	for _, fallback := range []bool{false, true} {
		t.Run(fmt.Sprint(fallback), func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newSinkTestAgent(t)
			recovering := &recoveringChildPartSink{ProviderServices: a.sink}
			recovering.creationFails.Store(fallback)
			controlled := &controlledPartSink{ProviderServices: recovering, rejectText: "The old actor failure."}
			controlled.reject.Store(10)
			a.sink = controlled
			feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy),
				actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 0, ""))
			originalActor := a.actors[actorID]
			feed(a, actorStatusEvent(t, actorID, contracts.MiMoActorStatusIdle, contracts.MiMoActorOutcomeFailure, 1, "The old actor failure."))
			var originalMessage string
			for messageID, record := range a.messages {
				if record.actor == originalActor && len(record.pendingParts) > 0 && record.pendingParts[0].kind == mimoPendingActorFailure {
					originalMessage = messageID
				}
			}
			require.NotEmpty(t, originalMessage)
			sessionID, err := a.ClearContext()
			require.NoError(t, err)
			beginPartialAnswerInSession(t, a, sessionID, "msg_actor_replacement_root", "part_actor_replacement_root", "NEWNEWNEWNEW")
			feed(a,
				eventWithSession(t, actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 0, ""), sessionID),
				eventWithSession(t, messageEvent(t, "msg_actor_replacement_child", roleAssistant, actorID, false), sessionID),
				eventWithSession(t, textPartEvent(t, partTypeText, "part_actor_replacement_child", "msg_actor_replacement_child", "", false), sessionID),
				eventWithSession(t, deltaEvent(t, "part_actor_replacement_child", "msg_actor_replacement_child", "CHILDCHILD"), sessionID))
			assert.NotSame(t, originalActor, a.actors[actorID])
			replacementSink := sink
			originalSink := sink
			if !fallback {
				replacementSink = sink.Child(a.actors[actorID].childAgentID)
				originalSink = sink.Child(originalActor.childAgentID)
			}
			beforeRoot, beforeReplacement := sink.ProgressSnapshot(), replacementSink.ProgressSnapshot()
			if fallback {
				require.EqualValues(t, 5, beforeRoot.ThinkingTokens)
			} else {
				require.EqualValues(t, 3, beforeRoot.ThinkingTokens)
				require.EqualValues(t, 2, beforeReplacement.ThinkingTokens)
			}
			controlled.reject.Store(0)
			require.True(t, a.flushPendingMessage(originalMessage, ""))
			assert.Equal(t, beforeRoot, sink.ProgressSnapshot(), "the old actor row cannot reset the replacement root")
			if !fallback {
				assert.Equal(t, beforeReplacement, replacementSink.ProgressSnapshot(), "the old actor row cannot reset the replacement child")
			}
			rows := assembledRows(originalSink)
			require.Len(t, rows, 1)
			_, text, completion := assembledText(t, rows[0].Content)
			assert.Equal(t, "The old actor failure.", text)
			assert.Equal(t, string(agent.MessageCompletionError), completion)
			assert.Equal(t, testSessionID, rows[0].AgentSessionID)
		})
	}
}
