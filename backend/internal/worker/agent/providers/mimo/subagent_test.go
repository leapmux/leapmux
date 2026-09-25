package mimo

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The events below follow the order MiMo 0.1.14 sent them in a real probe of a
// background spawn (probe/mimo-code/actor.sse.jsonl): the spawn call's running
// update names the actor before actor.status reports its first turn, and the
// actor's own messages follow.

const (
	spawnCallID = "call-main-01"
	actorID     = "general-1"
)

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
	childID := "child-of-" + spawnCallID
	require.Equal(t, []string{childID}, sink.ChildAgentIDs(), "the spawn call's span is the child's spawn span")
	row := backgroundTask(t, sink, spawnCallID)
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
	assert.Equal(t, "call-sub-01", childRows[2].SpanID)
	assert.True(t, childRows[3].Closing)
	_, text, _ = assembledText(t, childRows[4].Content)
	assert.Equal(t, "**Status**: success\n**Summary**: Ran the command.", text)

	for _, message := range sink.Messages() {
		assert.NotEqual(t, "call-sub-01", message.SpanID, "a subagent's call stays out of the parent transcript")
	}
	assert.Equal(t, bgtask.StatusRunning, backgroundTask(t, sink, spawnCallID).Status,
		"the parent's turn end does not end a background subagent")

	feed(a, actorStatusEvent(t, actorID, contracts.MiMoActorStatusIdle, contracts.MiMoActorOutcomeSuccess, 2, ""))
	assert.Equal(t, bgtask.StatusCompleted, backgroundTask(t, sink, spawnCallID).Status)
	reports := sink.LeapMuxNotifications()
	require.Len(t, reports, 1, "a background subagent reports its last text to the parent")
	assert.Equal(t, contracts.NotificationTypeSubagentReport, reports[0][contracts.NotificationFieldType])
	assert.Equal(t, "**Status**: success\n**Summary**: Ran the command.", reports[0][contracts.NotificationFieldText])
	assert.Equal(t, "Probe helper", reports[0][contracts.NotificationFieldLabel])
	assert.Equal(t, bgtask.StatusWire(bgtask.StatusCompleted), reports[0][contracts.NotificationFieldStatus])
}

func TestSpawnCallRowsStayInTheParent(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	spawnActor(t, a, contracts.MiMoActorActionSpawn, true)

	messages := sink.Messages()
	require.Len(t, messages, 2, "the spawn call opens and closes in the parent transcript")
	assert.Equal(t, spawnCallID, messages[0].SpanID)
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
	assert.Equal(t, bgtask.StatusCompleted, backgroundTask(t, sink, spawnCallID).Status)
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
	assert.Equal(t, bgtask.StatusFailed, backgroundTask(t, sink, spawnCallID).Status)
	childRows := sink.Child("child-of-" + spawnCallID).Messages()
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
		contracts.MiMoActorOutcomeSuccess:   bgtask.StatusCompleted,
		"":                                  bgtask.StatusCompleted,
		contracts.MiMoActorOutcomeFailure:   bgtask.StatusFailed,
		contracts.MiMoActorOutcomeCancelled: bgtask.StatusStopped,
		"timeout":                           bgtask.StatusCompleted,
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
	require.Equal(t, bgtask.StatusCompleted, backgroundTask(t, sink, spawnCallID).Status)

	feed(a, actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 1, ""))
	assert.Equal(t, []string{spawnCallID}, sink.RevivedTasks())
	assert.Equal(t, bgtask.StatusRunning, backgroundTask(t, sink, spawnCallID).Status)

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

	assert.ErrorIs(t, a.SendChildInput(spawnCallID, "more", nil), agent.ErrAgentBusy, "a running subagent holds the message for a steer")
	assert.Equal(t, agent.TurnState{Active: true, Steerable: true}, a.ActiveChildTurnState(spawnCallID))

	require.NoError(t, a.SteerChildInput(spawnCallID, "also this", nil))
	requests := server.requestsTo("POST /session/ses_test/prompt_async")
	require.Len(t, requests, 1)
	assert.JSONEq(t, `{"parts":[{"type":"text","text":"also this"}],"agentID":"general-1","model":{"providerID":"mock","modelID":"alpha"}}`,
		string(requests[0].Body), "a subagent message addresses the actor and states no primary agent")

	feed(a, actorStatusEvent(t, actorID, contracts.MiMoActorStatusIdle, contracts.MiMoActorOutcomeSuccess, 1, ""))
	assert.Equal(t, agent.TurnState{}, a.ActiveChildTurnState(spawnCallID))
	assert.ErrorIs(t, a.SteerChildInput(spawnCallID, "late", nil), agent.ErrNoActiveTurn)

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

	err := a.SendChildInput(spawnCallID, "next task", nil)
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

	err := a.SteerChildInput(spawnCallID, "also this", nil)
	require.Error(t, err)
	assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain)
}

// Each turn of a subagent is published on its own tab, so the tab's input queue
// holds a message while the turn runs and offers it as a steer.
func TestSubagentTurnIsPublishedOnItsOwnTab(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	spawnActor(t, a, contracts.MiMoActorActionSpawn, true)
	child := sink.Child("child-of-" + spawnCallID)
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
	assert.Equal(t, []bool{true, false}, sink.Child("child-of-"+spawnCallID).TurnActives())
}

// A message the main agent sends with `actor send` reaches the subagent's inbox,
// and MiMo's own copy of it is a user message the worker persists nowhere, so
// the call's result writes it into the subagent's transcript.
func TestParentMessageReachesTheChildTranscript(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	spawnActor(t, a, contracts.MiMoActorActionSpawn, true)
	send := map[string]any{"operation": map[string]any{"action": contracts.MiMoActorActionSend, "to_actor_id": actorID, "content": "Use the fast path."}}

	feed(a,
		toolPartEvent(t, "prt_s", "msg_p", contracts.MiMoToolActor, "call-send", toolState{Status: contracts.MiMoToolStatusRunning, Input: send}),
		toolPartEvent(t, "prt_s", "msg_p", contracts.MiMoToolActor, "call-send", toolState{Status: contracts.MiMoToolStatusCompleted, Input: send, Output: "sent"}),
	)
	childRows := sink.Child("child-of-" + spawnCallID).Messages()
	last := childRows[len(childRows)-1]
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, last.Source)
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_USER_MESSAGE, last.MarkType)
	assert.JSONEq(t, `{"content":"Use the fast path."}`, string(last.Content))

	before := len(childRows)
	unknown := map[string]any{"operation": map[string]any{"action": contracts.MiMoActorActionSend, "to_actor_id": "explore-9", "content": "Hi."}}
	feed(a, toolPartEvent(t, "prt_u", "msg_p", contracts.MiMoToolActor, "call-send-2", toolState{Status: contracts.MiMoToolStatusCompleted, Input: unknown}))
	assert.Len(t, sink.Child("child-of-"+spawnCallID).Messages(), before)
	assert.Len(t, sink.ChildAgentIDs(), 1, "a message to an actor the worker never saw opens no transcript")
}

// respondWithSpawnHistory makes the server hold a resumed session whose main
// agent spawned general-1 with spawnCallID, ran a command, and sent a message
// to an actor. The history also holds spawn calls that restore nothing: one
// that names no actor, and one that names the main agent.
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
	assert.Equal(t, []string{actorID}, sortedKeys(a.actors), "only a spawn call that names a subagent restores a link")
	assert.Equal(t, map[string]string{spawnCallID: actorID}, a.spawnActors)
	actor, ok := a.actorForRowKey(spawnCallID)
	require.True(t, ok)
	assert.Equal(t, actorID, actor.id)
	assert.True(t, actor.closed, "the new process runs no turn of it yet")
	assert.ErrorIs(t, a.SendChildInput(spawnCallID, "resume the work", nil), errSubagentIdle)
	assert.Equal(t, agent.TurnState{}, a.ActiveChildTurnState(spawnCallID))
	assert.InDelta(t, 0.25, a.usageSnapshot().costUSD, 1e-9, "the session's cost so far carries over, and only an assistant message costs")

	feed(a, actorStatusEvent(t, actorID, contracts.MiMoActorStatusRunning, "", 2, ""))
	assert.Equal(t, bgtask.StatusRunning, backgroundTask(t, sink, spawnCallID).Status, "a later turn opens the row that the spawn call keys")
	require.NoError(t, a.SteerChildInput(spawnCallID, "resume the work", nil))
	assert.JSONEq(t, `{"parts":[{"type":"text","text":"resume the work"}],"agentID":"general-1","model":{"providerID":"mock","modelID":"alpha"}}`,
		string(server.requestsTo("POST /session/ses_test/prompt_async")[0].Body))
}

func TestRestoreResumedSessionSurvivesAnUnreadableHistory(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)
	server.respond("GET /session/ses_test/message", http.StatusInternalServerError, `{}`)
	a.restoreResumedSession(a.Context(), testSessionID)
	_, ok := a.actorForRowKey(spawnCallID)
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
	assert.Equal(t, bgtask.StatusCompleted, backgroundTask(t, sink, spawnCallID).Status)

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
	assert.Equal(t, bgtask.StatusStopped, backgroundTask(t, sink, spawnCallID).Status)

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

func (childlessSink) EnsureChildAgent(string, string, string) (string, error) {
	return "", errors.New("the database is closed")
}

// A subagent that the worker cannot give a transcript still ran. Its rows go to
// the main transcript rather than nowhere, and no registry row names a
// transcript that does not exist.
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
	require.Len(t, messages, 1)
	_, text, _ := assembledText(t, messages[0].Content)
	assert.Equal(t, "Found two issues.", text)
	assert.Empty(t, sink.BackgroundTasks())
	assert.Empty(t, sink.ChildAgentIDs())
}

func TestChildSteerRefusalsSendNothing(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	spawnActor(t, a, contracts.MiMoActorActionSpawn, true)

	binary := []*leapmuxv1.Attachment{{Filename: "tool.bin", MimeType: "application/octet-stream", Data: []byte{0xff, 0x00}}}
	assert.ErrorContains(t, a.SteerChildInput(spawnCallID, "run it", binary), "tool.bin")

	a.sessionID = ""
	assert.ErrorContains(t, a.SteerChildInput(spawnCallID, "more", nil), "no MiMo session")

	a.sessionID = testSessionID
	a.SetStoppedForTest(true)
	assert.ErrorContains(t, a.SteerChildInput(spawnCallID, "more", nil), "stopped")
	assert.Empty(t, server.requestsTo("POST /session/ses_test/prompt_async"))
}

// Only a completed `actor send` with text for a subagent that the worker knows
// reaches that subagent's transcript.
func TestParentMessagesThatReachNoTranscript(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	spawnActor(t, a, contracts.MiMoActorActionSpawn, true)
	childID := "child-of-" + spawnCallID
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
