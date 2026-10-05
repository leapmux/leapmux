package ohmypi

import (
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The frames below are omp 18.2.11's own, from the chat-task probe, shortened
// where a field is irrelevant.
const (
	frameTaskStart = `{"type":"tool_execution_start","toolCallId":"call_task","toolName":"task","args":{"context":"# Goal\nProbe.","tasks":[{"name":"Probe","agent":"task","task":"SUBAGENT-PROBE-MARKER: reply with ok and yield the result."}]},"intent":"probe subagent storage"}`
	frameTaskEnd   = `{"type":"tool_execution_end","toolCallId":"call_task","toolName":"task","result":{"content":[{"type":"text","text":"Spawned agent ` + "`Probe`" + ` (job ` + "`Probe`" + `)."}],"details":{"results":[],"async":{"state":"running","jobId":"Probe","type":"task"}}},"isError":false}`
	frameStarted   = `{"type":"subagent_lifecycle","payload":{"id":"Probe","agent":"task","parentToolCallId":"call_task","detached":true,"agentSource":"bundled","status":"started","sessionFile":"/s/Probe.jsonl","index":0}}`
	frameCompleted = `{"type":"subagent_lifecycle","payload":{"id":"Probe","agent":"task","parentToolCallId":"call_task","detached":true,"agentSource":"bundled","status":"completed","sessionFile":"/s/Probe.jsonl","index":0}}`

	// A `bash` call that omp moved to the background: its result states the job.
	frameBackgroundBashStart = `{"type":"tool_execution_start","toolCallId":"call_b","toolName":"bash","args":{"command":"npm run build"}}`
	frameBackgroundBashEnd   = `{"type":"tool_execution_end","toolCallId":"call_b","toolName":"bash","result":{"content":[{"type":"text","text":"Moved to the background."}],"details":{"async":{"state":"running","jobId":"bash-1","type":"bash"}}},"isError":false}`
)

// subagentEvent wraps one frame of a subagent's stream in a subagent_event.
func subagentEvent(id, event string) string {
	return `{"type":"subagent_event","payload":{"id":"` + id + `","event":` + event + `}}`
}

// subagentRow is the registry row of the rig's subagent "Probe".
func subagentRow(t *testing.T, r *rig, id string) bgtask.Item {
	t.Helper()
	row, ok := r.sink.BackgroundTask(subagentRowKey(r.agent.sessionID, id))
	require.True(t, ok, "the subagent %q has a registry row", id)
	return row
}

// subagentReports returns the subagent_report notifications of one child
// transcript.
func subagentReports(r *rig, childID string) []map[string]interface{} {
	var out []map[string]interface{}
	for _, notification := range r.sink.Child(childID).LeapMuxNotifications() {
		if notification["type"] == "subagent_report" {
			out = append(out, notification)
		}
	}
	return out
}

func TestASubagentGetsARowAndATranscript(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(`{"type":"agent_start"}`, frameTaskStart, frameTaskEnd, frameAgentEnd, frameStarted)

	row := subagentRow(t, r, "Probe")
	assert.Equal(t, bgtask.KindSubagent, row.Kind)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.Equal(t, "Probe", row.Title)
	assert.Equal(t, "SUBAGENT-PROBE-MARKER: reply with ok and yield the result.", row.Description,
		"the row states the task the parent wrote")
	require.NotEmpty(t, row.ChildAgentID)
	assert.Contains(t, r.sink.ChildAgentIDs(), row.ChildAgentID)
}

func TestASubagentsEventsReachItsTranscript(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(frameTaskStart, frameTaskEnd, frameStarted)
	childID := subagentRow(t, r, "Probe").ChildAgentID
	r.emit(
		subagentEvent("Probe", `{"type":"advisor_cost_changed"}`),
		subagentEvent("Probe", `{"type":"agent_start"}`),
		subagentEvent("Probe", `{"type":"message_end","message":{"role":"user","content":[{"type":"text","text":"Complete assignment thoroughly:\n\nSUBAGENT-PROBE-MARKER"}],"attribution":"agent"}}`),
		subagentEvent("Probe", `{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"x"}}`),
		subagentEvent("Probe", `{"type":"message_end","message":{"role":"assistant","content":[{"type":"toolCall","id":"call_yield","name":"yield","arguments":{"data":{"result":"SUBAGENT-RESULT"}}}],"stopReason":"toolUse"}}`),
		subagentEvent("Probe", `{"type":"tool_execution_start","toolCallId":"call_yield","toolName":"yield","args":{"data":{"result":"SUBAGENT-RESULT"}}}`),
		subagentEvent("Probe", `{"type":"tool_execution_end","toolCallId":"call_yield","toolName":"yield","result":{"content":[{"type":"text","text":"Result submitted."}],"details":{"data":{"result":"SUBAGENT-RESULT"},"status":"success"}},"isError":false}`),
		subagentEvent("Probe", `{"type":"agent_end","messages":[]}`),
		frameCompleted,
	)

	child := r.sink.Child(childID)
	messages := child.Messages()
	require.GreaterOrEqual(t, len(messages), 5)
	assert.JSONEq(t, `{"content":"Complete assignment thoroughly:\n\nSUBAGENT-PROBE-MARKER"}`, string(messages[0].Content),
		"the parent's prompt opens the transcript")
	assert.Equal(t, []string{"", "message_end", "tool_execution_start", "tool_execution_end", "agent_end"}, persistedTypes(messages[:5]))
	assert.True(t, messages[4].TurnEnd, "the subagent's run ends with its own turn-end row")
	var metadata map[string]json.Number
	require.NoError(t, json.Unmarshal(messages[4].Metadata, &metadata))
	assert.Equal(t, json.Number("1"), metadata[contracts.MessageMetadataFieldToolUses])

	row := subagentRow(t, r, "Probe")
	assert.Equal(t, bgtask.StatusCompleted, row.Status)
	assert.Equal(t, []string{"tool_execution_start", "tool_execution_end"}, persistedTypes(r.sink.Messages()),
		"the parent transcript holds its own task call and nothing of the subagent")
	reports := subagentReports(r, childID)
	require.Len(t, reports, 1)
	assert.JSONEq(t, `{"result":"SUBAGENT-RESULT"}`, reports[0]["text"].(string), "a structured result is its JSON")
}

func TestASubagentsYieldIsItsReport(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(frameTaskStart, frameStarted)
	childID := subagentRow(t, r, "Probe").ChildAgentID
	r.emit(
		subagentEvent("Probe", `{"type":"tool_execution_start","toolCallId":"y","toolName":"yield","args":{}}`),
		subagentEvent("Probe", `{"type":"tool_execution_end","toolCallId":"y","toolName":"yield","result":{"content":[],"details":{"data":"The answer is 42.","status":"success"}},"isError":false}`),
		frameCompleted,
	)
	reports := subagentReports(r, childID)
	require.Len(t, reports, 1)
	assert.Equal(t, "The answer is 42.", reports[0]["text"], "a text result is the report as it is")
	assert.Equal(t, "Probe", reports[0]["label"])

	// The report's identity is stable, so a replayed end writes it once.
	_, err := r.sink.PersistChildSubagentReport(agent.ChildSubagentReportWrite{
		RowKey: subagentRowKey(r.agent.sessionID, "Probe"),
		Write:  agent.SubagentReportWrite{ReportID: "omp:" + subagentRowKey(r.agent.sessionID, "Probe"), Report: agent.SubagentReport{Text: "again"}},
	})
	require.NoError(t, err)
	assert.Len(t, subagentReports(r, childID), 1)
}

func TestASubagentsStructuredYieldIsItsJSON(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(frameTaskStart, frameStarted)
	childID := subagentRow(t, r, "Probe").ChildAgentID
	r.emit(
		subagentEvent("Probe", `{"type":"tool_execution_end","toolCallId":"y","toolName":"yield","result":{"content":[],"details":{"data":{"result":"SUBAGENT-RESULT"},"status":"success"}},"isError":false}`),
		frameCompleted,
	)
	reports := subagentReports(r, childID)
	require.Len(t, reports, 1)
	assert.JSONEq(t, `{"result":"SUBAGENT-RESULT"}`, reports[0]["text"].(string))
}

func TestASubagentWithNoYieldWritesNoReport(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(frameTaskStart, frameStarted)
	childID := subagentRow(t, r, "Probe").ChildAgentID
	r.emit(
		subagentEvent("Probe", `{"type":"tool_execution_end","toolCallId":"y","toolName":"yield","result":{"content":[],"details":{"data":null}},"isError":false}`),
		frameCompleted,
	)
	assert.Empty(t, subagentReports(r, childID))
}

func TestSubagentLifecycleStatuses(t *testing.T) {
	t.Parallel()
	for status, want := range map[string]bgtask.Status{
		"completed": bgtask.StatusCompleted,
		"failed":    bgtask.StatusFailed,
		"aborted":   bgtask.StatusStopped,
	} {
		t.Run(status, func(t *testing.T) {
			r := newRig(t)
			r.emit(frameTaskStart, frameStarted,
				subagentEvent("Probe", `{"type":"tool_execution_start","toolCallId":"b","toolName":"bash","args":{"command":"sleep 9"}}`),
				`{"type":"subagent_lifecycle","payload":{"id":"Probe","parentToolCallId":"call_task","status":"`+status+`","index":0}}`)
			row := subagentRow(t, r, "Probe")
			assert.Equal(t, want, row.Status)
			childMessages := r.sink.Child(row.ChildAgentID).Messages()
			require.NotEmpty(t, childMessages)
			last := childMessages[len(childMessages)-1]
			assert.True(t, last.Closing, "the call the subagent never ended is closed")
			assert.Equal(t, completionForStatus(want), last.Completion)
		})
	}
}

func TestAnUnknownLifecycleStatusChangesNothing(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(frameTaskStart, frameStarted, `{"type":"subagent_lifecycle","payload":{"id":"Probe","status":"hibernating"}}`)
	assert.Equal(t, bgtask.StatusRunning, subagentRow(t, r, "Probe").Status)
}

func TestALifecycleFrameWithNoIDIsDropped(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(`{"type":"subagent_lifecycle","payload":{"status":"started"}}`, `{"type":"subagent_lifecycle","payload":7}`)
	assert.Empty(t, r.sink.BackgroundTasks())
}

func TestASubagentThatStartsTwiceKeepsOneRow(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(frameTaskStart, frameStarted, frameStarted)
	assert.Len(t, r.sink.BackgroundTasks(), 1)
	assert.Len(t, r.sink.ChildAgentIDs(), 1)
}

func TestTheEndOfASubagentThisWorkerNeverSawClosesItsRow(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	rowKey := subagentRowKey(r.agent.sessionID, "Probe")
	require.NoError(t, r.sink.UpsertBackgroundTask(bgtask.Upsert{RowKey: rowKey, Kind: bgtask.KindSubagent, Title: "Probe", Status: bgtask.StatusRunning}))
	r.emit(frameCompleted)
	row, ok := r.sink.BackgroundTask(rowKey)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusCompleted, row.Status)
}

func TestANestedSubagentSpawnsFromItsParentsTranscript(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(frameTaskStart, frameStarted)
	parentChild := subagentRow(t, r, "Probe").ChildAgentID
	r.emit(
		subagentEvent("Probe", `{"type":"tool_execution_start","toolCallId":"call_inner","toolName":"task","args":{"tasks":[{"name":"Inner","task":"Go deeper."}]}}`),
		`{"type":"subagent_lifecycle","payload":{"id":"Probe.Inner","parentToolCallId":"call_inner","status":"started","index":0}}`,
	)
	inner := subagentRow(t, r, "Probe.Inner")
	require.NotEmpty(t, inner.ChildAgentID)
	assert.Contains(t, r.sink.Child(parentChild).ChildAgentIDs(), inner.ChildAgentID,
		"the inner transcript hangs off the parent subagent's own transcript")
	assert.Equal(t, "Probe.Inner", inner.Title)
}

func TestSubagentProgressShowsTheLatestActivity(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(frameTaskStart, frameStarted)
	rowKey := subagentRowKey(r.agent.sessionID, "Probe")
	progress := func(tools, output string) string {
		return `{"type":"subagent_progress","payload":{"index":0,"progress":{"id":"Probe","status":"running","recentTools":` + tools + `,"recentOutput":` + output + `}}}`
	}
	activeForm := func() string {
		row, ok := r.sink.BackgroundTask(rowKey)
		require.True(t, ok)
		return row.ActiveForm
	}

	r.emit(progress(`[]`, `[]`))
	assert.Empty(t, activeForm(), "no activity yet")
	r.emit(progress(`[]`, `["Reading the tree\nmore"]`))
	assert.Equal(t, "Reading the tree", activeForm(), "the first line of the latest output")
	r.emit(progress(`[{"tool":"read","args":"src/main.go"}]`, `["x"]`))
	assert.Equal(t, "read src/main.go", activeForm(), "a tool call wins over output")
	r.emit(`{"type":"subagent_progress","payload":{"progress":{"id":"Stranger","recentOutput":["z"]}}}`, `{"type":"subagent_progress","payload":7}`)
	assert.Equal(t, "read src/main.go", activeForm(), "another subagent's progress and a malformed frame change nothing")
	assert.Equal(t, []bgtask.Status{bgtask.StatusRunning}, r.sink.BackgroundTaskStatuses(rowKey))
}

// probeTaskLine is the description that the row of "Probe" shows before omp labels
// the task: the first line of the task that frameTaskStart states.
const probeTaskLine = "SUBAGENT-PROBE-MARKER: reply with ok and yield the result."

// labeledProgress is a subagent_progress frame of one subagent in the shape that
// omp 18.6.0 sends. The progress object carries `description` once omp's tiny
// model labels the task, and every later frame repeats it. omp omits the field
// before then, which an empty description here models.
func labeledProgress(t *testing.T, id, description string, recentOutput ...string) string {
	t.Helper()
	progress := map[string]any{"id": id, "status": "running", "recentTools": []any{}, "recentOutput": recentOutput}
	if recentOutput == nil {
		progress["recentOutput"] = []string{}
	}
	if description != "" {
		progress["description"] = description
	}
	return mustJSON(t, map[string]any{"type": "subagent_progress", "payload": map[string]any{"index": 0, "agent": "task", "progress": progress}})
}

// registryWriteSink counts the registry writes that a provider makes, on top of
// the recording sink, so a test can tell an unchanged frame from a repeated write.
type registryWriteSink struct {
	*agenttest.ControlSink
	mu       sync.Mutex
	upserts  []bgtask.Upsert
	statuses int
}

func (s *registryWriteSink) UpsertBackgroundTask(task bgtask.Upsert) error {
	s.mu.Lock()
	s.upserts = append(s.upserts, task)
	s.mu.Unlock()
	return s.ControlSink.UpsertBackgroundTask(task)
}

func (s *registryWriteSink) UpdateBackgroundTaskStatus(rowKey string, status bgtask.Status, activeForm string) error {
	s.mu.Lock()
	s.statuses++
	s.mu.Unlock()
	return s.ControlSink.UpdateBackgroundTaskStatus(rowKey, status, activeForm)
}

// writes returns how many upserts and status updates the provider made so far.
func (s *registryWriteSink) writes() (upserts []bgtask.Upsert, statuses int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.upserts), s.statuses
}

// newRegistryWriteRig is a rig whose registry writes a test can count.
func newRegistryWriteRig(t *testing.T) (*rig, *registryWriteSink) {
	t.Helper()
	r := newRig(t)
	sink := &registryWriteSink{ControlSink: r.sink}
	r.agent.sink = agent.NewProviderServices(sink)
	return r, sink
}

// omp labels a task with its tiny model after the subagent started, and states the
// label in the next progress frame. omp's own task view then shows the label in
// place of the assignment, and the row follows it.
func TestATaskLabelReplacesTheRowDescription(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(frameTaskStart, frameStarted)
	before := subagentRow(t, r, "Probe")
	require.Equal(t, probeTaskLine, before.Description, "the row states the task until omp labels it")

	r.emit(labeledProgress(t, "Probe", "Probe the subagent storage path"))

	row := subagentRow(t, r, "Probe")
	assert.Equal(t, "Probe the subagent storage path", row.Description)
	assert.Equal(t, "Probe", row.Title, "the label replaces the description, not omp's id")
	assert.Equal(t, before.ChildAgentID, row.ChildAgentID, "the row keeps its child transcript")
	assert.Equal(t, bgtask.KindSubagent, row.Kind)
	assert.Equal(t, []bgtask.Status{bgtask.StatusRunning}, r.sink.BackgroundTaskStatuses(subagentRowKey(r.agent.sessionID, "Probe")))
}

// With no tiny model, omp never labels the task, so the first task line stays.
func TestTheTaskLineStaysWhenNoLabelArrives(t *testing.T) {
	t.Parallel()
	r, sink := newRegistryWriteRig(t)
	r.emit(frameTaskStart, frameStarted)
	r.emit(labeledProgress(t, "Probe", ""), labeledProgress(t, "Probe", "", "Reading the tree"))

	row := subagentRow(t, r, "Probe")
	assert.Equal(t, probeTaskLine, row.Description)
	assert.Equal(t, "Reading the tree", row.ActiveForm, "the activity still reaches the row")
	upserts, _ := sink.writes()
	assert.Len(t, upserts, 1, "only the start wrote the row")
}

// omp sets the label only to a nonempty title, so an empty or blank description
// states no label. The row keeps the description that it shows.
func TestABlankLabelKeepsTheDescription(t *testing.T) {
	t.Parallel()
	for name, description := range map[string]string{
		"a blank label":               "   ",
		"a label of blank lines":      "\n \n\t",
		"a label of a no-break space": " ",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r, sink := newRegistryWriteRig(t)
			r.emit(frameTaskStart, frameStarted, labeledProgress(t, "Probe", description))

			assert.Equal(t, probeTaskLine, subagentRow(t, r, "Probe").Description)
			upserts, statuses := sink.writes()
			assert.Len(t, upserts, 1, "only the start wrote the row")
			assert.Zero(t, statuses)
		})
	}
	// A description of another type states no label. The rest of the frame still
	// counts, so the activity reaches the row.
	t.Run("a description that is not a string", func(t *testing.T) {
		t.Parallel()
		r, sink := newRegistryWriteRig(t)
		r.emit(frameTaskStart, frameStarted,
			`{"type":"subagent_progress","payload":{"progress":{"id":"Probe","description":7,"recentOutput":["Reading the tree"]}}}`)

		row := subagentRow(t, r, "Probe")
		assert.Equal(t, probeTaskLine, row.Description)
		assert.Equal(t, "Reading the tree", row.ActiveForm)
		upserts, statuses := sink.writes()
		assert.Len(t, upserts, 1, "only the start wrote the row")
		assert.Equal(t, 1, statuses)
	})
}

// omp repeats the label in every progress frame after it set it. The row is
// written once for it, and the activity of the later frames still reaches it.
func TestARepeatedLabelWritesTheRowOnce(t *testing.T) {
	t.Parallel()
	r, sink := newRegistryWriteRig(t)
	r.emit(frameTaskStart, frameStarted)
	r.emit(
		labeledProgress(t, "Probe", "Probe the subagent storage path"),
		labeledProgress(t, "Probe", "Probe the subagent storage path"),
		labeledProgress(t, "Probe", "Probe the subagent storage path", "Reading the tree"),
	)

	upserts, statuses := sink.writes()
	require.Len(t, upserts, 2, "the start and the first frame with the label")
	assert.Equal(t, "Probe the subagent storage path", upserts[1].Description)
	assert.Equal(t, 1, statuses, "the later activity is a status update alone")
	row := subagentRow(t, r, "Probe")
	assert.Equal(t, "Probe the subagent storage path", row.Description)
	assert.Equal(t, "Reading the tree", row.ActiveForm)
}

// A label and an activity in one frame reach the row in one write, and a later
// label that differs replaces the earlier one.
func TestALabelAndAnActivityInOneFrameWriteTheRowOnce(t *testing.T) {
	t.Parallel()
	r, sink := newRegistryWriteRig(t)
	r.emit(frameTaskStart, frameStarted)
	r.emit(labeledProgress(t, "Probe", "Probe the subagent storage path\nand report", "Reading the tree"))

	upserts, statuses := sink.writes()
	require.Len(t, upserts, 2)
	assert.Zero(t, statuses, "the upsert carries the activity")
	row := subagentRow(t, r, "Probe")
	assert.Equal(t, "Probe the subagent storage path", row.Description, "the first line of the label")
	assert.Equal(t, "Reading the tree", row.ActiveForm)

	r.emit(labeledProgress(t, "Probe", "Check the storage path", "Reading the tree"))
	assert.Equal(t, "Check the storage path", subagentRow(t, r, "Probe").Description)
	upserts, _ = sink.writes()
	assert.Len(t, upserts, 3)
}

// A label that the start already stated, as a caller-supplied label does, writes
// nothing again.
func TestALabelThatTheStartStatedWritesNothing(t *testing.T) {
	t.Parallel()
	r, sink := newRegistryWriteRig(t)
	r.emit(frameTaskStart, `{"type":"subagent_lifecycle","payload":{"id":"Probe","parentToolCallId":"call_task","status":"started","description":"Probe the subagent storage path","index":0}}`)
	require.Equal(t, "Probe the subagent storage path", subagentRow(t, r, "Probe").Description)

	r.emit(labeledProgress(t, "Probe", "Probe the subagent storage path"))

	upserts, statuses := sink.writes()
	assert.Len(t, upserts, 1)
	assert.Zero(t, statuses)
}

// omp drops a progress frame of a subagent that did not start or that ended. A
// label for one changes no row, and a finished row keeps its status.
func TestALabelForAnUnknownOrFinishedSubagentChangesNothing(t *testing.T) {
	t.Parallel()
	r, sink := newRegistryWriteRig(t)
	r.emit(labeledProgress(t, "Stranger", "Do something else"))
	assert.Empty(t, r.sink.BackgroundTasks(), "a label opens no row")

	r.emit(frameTaskStart, frameStarted, frameCompleted)
	r.emit(labeledProgress(t, "Probe", "Probe the subagent storage path"))

	row := subagentRow(t, r, "Probe")
	assert.Equal(t, probeTaskLine, row.Description)
	assert.Equal(t, bgtask.StatusCompleted, row.Status)
	upserts, statuses := sink.writes()
	assert.Len(t, upserts, 1, "only the start wrote the row")
	assert.Zero(t, statuses)
}

func TestAnEventOfAnUnknownSubagentIsDropped(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(subagentEvent("Ghost", `{"type":"message_end","message":{"role":"assistant","content":[]}}`), `{"type":"subagent_event","payload":{"id":"Ghost"}}`)
	assert.Empty(t, r.sink.Messages())
	assert.Empty(t, r.sink.ChildAgentIDs())
}

func TestCloseSubagentsEndsEveryOpenSubagent(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(frameTaskStart, frameStarted,
		`{"type":"subagent_lifecycle","payload":{"id":"Other","parentToolCallId":"call_task","status":"started","index":1}}`)
	r.agent.closeSubagents(bgtask.StatusStopped)
	assert.Equal(t, bgtask.StatusStopped, subagentRow(t, r, "Probe").Status)
	assert.Equal(t, bgtask.StatusStopped, subagentRow(t, r, "Other").Status)
	r.emit(frameCompleted)
	assert.Equal(t, bgtask.StatusStopped, subagentRow(t, r, "Probe").Status, "a closed row stays closed")
}

func TestTheSubagentRowKeyIncludesTheSession(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "s1/Probe", subagentRowKey("s1", "Probe"))
	assert.Equal(t, "Probe", subagentRowKey("", "Probe"))
	assert.Equal(t, "bash:s1/job-3", shellRowKey("s1", "job-3"))
}

func TestSpawnsAreForgottenWhenTheyCannotStart(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(frameTaskStart, `{"type":"tool_execution_end","toolCallId":"call_task","toolName":"task","result":{"content":[{"type":"text","text":"Invalid task"}]},"isError":true}`)
	r.agent.Mu.Lock()
	_, kept := r.agent.spawns["call_task"]
	r.agent.Mu.Unlock()
	assert.False(t, kept, "a failed call starts nothing more")

	r.emit(frameTaskStart, frameTaskEnd)
	r.agent.Mu.Lock()
	_, kept = r.agent.spawns["call_task"]
	r.agent.Mu.Unlock()
	assert.True(t, kept, "a background subagent starts after its call ended")
	r.emit(frameStarted)
	r.agent.Mu.Lock()
	_, kept = r.agent.spawns["call_task"]
	r.agent.Mu.Unlock()
	assert.False(t, kept, "every task started")
}

func TestASingleTaskCallLabelsItsRow(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(`{"type":"tool_execution_start","toolCallId":"call_task","toolName":"task","args":{"agent":"task","task":"Summarize the README.\nIn detail."}}`, frameStarted)
	assert.Equal(t, "Summarize the README.", subagentRow(t, r, "Probe").Description)
}

func TestABackgroundShellGetsARow(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(frameBackgroundBashStart, frameBackgroundBashEnd)
	rowKey := shellRowKey(r.agent.sessionID, "bash-1")
	row, ok := r.sink.BackgroundTask(rowKey)
	require.True(t, ok)
	assert.Equal(t, bgtask.KindShell, row.Kind)
	assert.Equal(t, "npm run build", row.Title, "the command comes from the call's start frame")
	assert.True(t, row.TitleIsCommand, "the title is the command itself")
	assert.Equal(t, "Background job bash-1 of call call_b", row.Description)
	assert.Equal(t, bgtask.StatusRunning, row.Status)

	r.emit(`{"type":"message_end","message":{"role":"custom","customType":"async-result","content":"Job bash-1 finished.","display":true,"details":{"jobs":[{"jobId":"bash-1"}]}}}`)
	row, _ = r.sink.BackgroundTask(rowKey)
	assert.Equal(t, bgtask.StatusCompleted, row.Status, "the delivered result closes the row")
}

func TestAForegroundShellGetsNoRow(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(frameBashStart, frameBashEnd,
		`{"type":"tool_execution_start","toolCallId":"call_c","toolName":"bash","args":{"command":"x"}}`,
		`{"type":"tool_execution_end","toolCallId":"call_c","toolName":"bash","result":{"content":[],"details":{"async":{"state":"completed","jobId":"bash-2","type":"bash"}}},"isError":false}`,
	)
	assert.Empty(t, r.sink.BackgroundTasks())
}

func TestCloseAllShellsStopsEveryShell(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(frameBackgroundBashStart, frameBackgroundBashEnd)
	r.agent.closeAllShells()
	row, ok := r.sink.BackgroundTask(shellRowKey(r.agent.sessionID, "bash-1"))
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusStopped, row.Status)
}

// failingChildSink is a sink whose store cannot create a child transcript.
type failingChildSink struct {
	*agenttest.ControlSink
}

func (failingChildSink) EnsureChildAgent(agent.ChildAgentSpec) (string, error) {
	return "", errors.New("the store is gone")
}

// A subagent whose transcript cannot be created still gets its registry row,
// so the reader sees that it runs. Its events have no transcript to reach.
func TestASubagentWithNoTranscriptStillGetsARow(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		failing bool
		started string
	}{
		{name: "a subagent that no call started", started: `{"type":"subagent_lifecycle","payload":{"id":"Probe","status":"started","description":"Scout the repo\nin detail","index":0}}`},
		{name: "a transcript the store refused", failing: true, started: frameStarted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			if tc.failing {
				r.agent.sink = agent.NewProviderServices(failingChildSink{r.sink})
			}
			r.emit(frameTaskStart, tc.started)
			row := subagentRow(t, r, "Probe")
			assert.Empty(t, row.ChildAgentID)
			assert.Equal(t, bgtask.StatusRunning, row.Status)
			assert.Empty(t, r.sink.ChildAgentIDs())
			parentRows := len(r.sink.Messages())

			r.emit(
				subagentEvent("Probe", `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"ok"}],"stopReason":"stop"}}`),
				subagentEvent("Probe", `{"type":"tool_execution_end","toolCallId":"y","toolName":"yield","result":{"content":[],"details":{"data":"The answer."}},"isError":false}`),
				frameCompleted,
			)
			assert.Len(t, r.sink.Messages(), parentRows, "no event of the subagent reaches the parent transcript")
			assert.Equal(t, bgtask.StatusCompleted, subagentRow(t, r, "Probe").Status)
		})
	}
}

func TestTheLifecycleDescriptionWinsOverTheTask(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(frameTaskStart, `{"type":"subagent_lifecycle","payload":{"id":"Probe","parentToolCallId":"call_task","status":"started","description":"\n  Probe the storage  \nand report","index":0}}`)
	assert.Equal(t, "Probe the storage", subagentRow(t, r, "Probe").Description)
}

// A lifecycle index outside the call's task list states no task, and the start
// still counts toward the call.
func TestALifecycleIndexOutsideTheTasksLabelsNothing(t *testing.T) {
	t.Parallel()
	for _, index := range []string{"-1", "1", "99"} {
		t.Run(index, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.emit(frameTaskStart, `{"type":"subagent_lifecycle","payload":{"id":"Probe","parentToolCallId":"call_task","status":"started","index":`+index+`}}`)
			assert.Empty(t, subagentRow(t, r, "Probe").Description)
			r.agent.Mu.Lock()
			defer r.agent.Mu.Unlock()
			assert.NotContains(t, r.agent.spawns, "call_task", "the call's one task started")
		})
	}
}

// A subagent that a message from its parent wakes runs again. Each run ends with
// its own turn-end row, timed and counted on its own.
func TestEachSubagentRunEndsWithItsOwnTimedRow(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := testutil.DeadlineContext(t)
	start := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	r.clock.Set(start).MustWait(ctx)
	r.emit(frameTaskStart, frameStarted)
	childID := subagentRow(t, r, "Probe").ChildAgentID

	r.emit(
		subagentEvent("Probe", `{"type":"agent_start"}`),
		subagentEvent("Probe", `{"type":"tool_execution_start","toolCallId":"b1","toolName":"bash","args":{"command":"ls"}}`),
		subagentEvent("Probe", `{"type":"tool_execution_end","toolCallId":"b1","toolName":"bash","result":{"content":[]},"isError":false}`),
	)
	r.clock.Set(start.Add(3 * time.Second)).MustWait(ctx)
	r.emit(subagentEvent("Probe", `{"type":"agent_end","messages":[]}`))
	r.clock.Set(start.Add(10 * time.Second)).MustWait(ctx)
	r.emit(subagentEvent("Probe", `{"type":"agent_start"}`))
	r.clock.Set(start.Add(11 * time.Second)).MustWait(ctx)
	r.emit(subagentEvent("Probe", `{"type":"agent_end","messages":[]}`))
	// An end whose run start the worker never saw states no duration.
	r.emit(subagentEvent("Probe", `{"type":"agent_end","messages":[]}`))

	ends := turnEndRows(r.sink.Child(childID).Messages())
	require.Len(t, ends, 3)
	for i, want := range []map[string]json.Number{
		{contracts.MessageMetadataFieldDurationMs: "3000", contracts.MessageMetadataFieldToolUses: "1"},
		{contracts.MessageMetadataFieldDurationMs: "1000", contracts.MessageMetadataFieldToolUses: "0"},
		{contracts.MessageMetadataFieldToolUses: "0"},
	} {
		var metadata map[string]json.Number
		require.NoError(t, json.Unmarshal(ends[i].Metadata, &metadata))
		assert.Equal(t, want, metadata, "run %d", i+1)
	}
	assert.Empty(t, turnEndRows(r.sink.Messages()), "the parent's turn does not end with the subagent's run")
	assert.Equal(t, bgtask.StatusRunning, subagentRow(t, r, "Probe").Status, "only the lifecycle frame ends the row")
}

// A subagent's injected messages and its background jobs belong to the
// subagent: none of them reaches the child transcript or the registry.
func TestASubagentsCustomMessagesAndBackgroundJobsStayWithTheSubagent(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(frameTaskStart, frameStarted)
	childID := subagentRow(t, r, "Probe").ChildAgentID
	r.emit(
		subagentEvent("Probe", `{"type":"message_end","message":{"role":"custom","customType":"async-result","content":"Job done.","display":true,"details":{"jobs":[]}}}`),
		subagentEvent("Probe", frameBackgroundBashStart),
		subagentEvent("Probe", frameBackgroundBashEnd),
	)
	assert.Equal(t, []string{"tool_execution_start", "tool_execution_end"}, persistedTypes(r.sink.Child(childID).Messages()))
	assert.Len(t, r.sink.BackgroundTasks(), 1, "the subagent's own row, and no shell row")
}

func TestASubagentsPromptOpensItsTranscriptOnce(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(frameTaskStart, frameStarted)
	childID := subagentRow(t, r, "Probe").ChildAgentID
	r.emit(
		subagentEvent("Probe", `{"type":"message_end","message":{"role":"user","content":"   "}}`),
		subagentEvent("Probe", `{"type":"message_end","message":{"role":"user","content":"Do the work."}}`),
		subagentEvent("Probe", `{"type":"message_end","message":{"role":"user","content":[{"type":"text","text":"A later message."}]}}`),
	)
	messages := r.sink.Child(childID).Messages()
	require.Len(t, messages, 1, "a blank prompt opens nothing, and a later message is not the prompt")
	assert.JSONEq(t, `{"content":"Do the work."}`, string(messages[0].Content))
}

// omp's end frame carries no arguments, so the shell's title comes from the
// start frame. With no start frame and no command, the job id is the title.
func TestABackgroundShellWithNoCommandIsTitledByItsJob(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(`{"type":"tool_execution_end","toolCallId":"call_x","toolName":"bash","result":{"content":[],"details":{"async":{"state":"running","jobId":"bash-7","type":"bash"}}},"isError":false}`)
	row, ok := r.sink.BackgroundTask(shellRowKey(r.agent.sessionID, "bash-7"))
	require.True(t, ok)
	assert.Equal(t, "bash-7", row.Title)
	assert.False(t, row.TitleIsCommand, "a job id is not a command")
}

func TestABashCallThatOpensNoShell(t *testing.T) {
	t.Parallel()
	for name, end := range map[string]string{
		"a failed call":         `{"type":"tool_execution_end","toolCallId":"call_b","toolName":"bash","result":{"content":[],"details":{"async":{"state":"running","jobId":"bash-1","type":"bash"}}},"isError":true}`,
		"a job of another type": `{"type":"tool_execution_end","toolCallId":"call_b","toolName":"bash","result":{"content":[],"details":{"async":{"state":"running","jobId":"bash-1","type":"task"}}},"isError":false}`,
		"a job with no id":      `{"type":"tool_execution_end","toolCallId":"call_b","toolName":"bash","result":{"content":[],"details":{"async":{"state":"running","type":"bash"}}},"isError":false}`,
		"a garbled result":      `{"type":"tool_execution_end","toolCallId":"call_b","toolName":"bash","result":"moved","isError":false}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.emit(frameBackgroundBashStart, end)
			assert.Empty(t, r.sink.BackgroundTasks())
		})
	}
}

func TestAnAsyncResultClosesOnlyTheJobsItDelivers(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(frameBackgroundBashStart, frameBackgroundBashEnd)
	rowKey := shellRowKey(r.agent.sessionID, "bash-1")
	r.emit(
		`{"type":"message_end","message":{"role":"custom","customType":"async-result","content":"Job ghost done.","display":true,"details":{"jobs":[{"jobId":"ghost"}]}}}`,
		`{"type":"message_end","message":{"role":"custom","customType":"async-result","content":"?","display":true,"details":"garbled"}}`,
	)
	row, ok := r.sink.BackgroundTask(rowKey)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, row.Status)

	// A result that omp hides still closes the row of the job it delivers.
	r.emit(`{"type":"message_end","message":{"role":"custom","customType":"async-result","content":"Job bash-1 done.","display":false,"details":{"jobs":[{"jobId":"bash-1"}]}}}`)
	row, _ = r.sink.BackgroundTask(rowKey)
	assert.Equal(t, bgtask.StatusCompleted, row.Status)
}

func TestSubagentStatusForCompletion(t *testing.T) {
	t.Parallel()
	assert.Equal(t, bgtask.StatusFailed, subagentStatusForCompletion(agent.MessageCompletionError))
	assert.Equal(t, bgtask.StatusCompleted, subagentStatusForCompletion(agent.MessageCompletionComplete))
	assert.Equal(t, bgtask.StatusStopped, subagentStatusForCompletion(agent.MessageCompletionInterrupted))
	assert.Equal(t, agent.MessageCompletionError, completionForStatus(bgtask.StatusFailed))
	assert.Equal(t, agent.MessageCompletionInterrupted, completionForStatus(bgtask.StatusStopped))
}
