package cursor

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/tooltranscript"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The three frames Cursor sends, with the params its 2026.09 build writes. The
// toolCallId carries an embedded newline, which is Cursor's own form for a call id.
const (
	cursorProbeToolCallID  = "call-9ff1787e-0\nfc_442cb271_0"
	cursorProbeTodosParams = `{"toolCallId":"call-9ff1787e-0\nfc_442cb271_0",` +
		`"todos":[{"id":"1","content":"Create a.txt","status":"in_progress"},` +
		`{"id":"2","content":"Create b.txt","status":"pending"}],"merge":false}`
	cursorProbeMergeParams = `{"toolCallId":"call-9ff1787e-0\nfc_442cb271_0",` +
		`"todos":[{"id":"1","content":"Create a.txt","status":"completed"}],"merge":true}`
	cursorProbeTaskParams = `{"toolCallId":"call-9ff1787e-0\nfc_442cb271_0","description":"Explore",` +
		`"prompt":"Find the parser","subagentType":"explore","model":"composer-2.5","agentId":"a-1","durationMs":1200}`
	cursorProbeImageParams = `{"toolCallId":"call-9ff1787e-0\nfc_442cb271_0","description":"A cat",` +
		`"filePath":"/tmp/cat.png","referenceImagePaths":["/tmp/ref.png"]}`
)

// cursorTestToolSource is the transcript source of newCursorTranscriptAgent. It
// locates every session at once, and read supplies the store records of each
// pass. It reads the tool call id as the real Cursor source reads it.
type cursorTestToolSource struct {
	tooltranscript.SourceDefaults
	read func(pending map[string]agent.MessageContent, final bool) map[string][]byte
}

func (*cursorTestToolSource) ProviderName() string { return "Test" }

func (*cursorTestToolSource) Locate(sessionID string) tooltranscript.Location {
	return tooltranscript.Location{SessionKey: sessionID, Ready: true}
}

func (*cursorTestToolSource) ToolCallID(original []byte) string { return acp.ToolCallID(original) }

func (s *cursorTestToolSource) ReadSupplements(_ context.Context, _ string, pending map[string]agent.MessageContent, final bool) (map[string][]byte, error) {
	if s.read == nil {
		return nil, nil
	}
	return s.read(pending, final), nil
}

// newCursorTranscriptAgent builds a Cursor agent whose sink is a tool transcript, the
// way Start wires one. read supplies the store records of each pass.
func newCursorTranscriptAgent(t *testing.T, sink agent.ProviderServices, read func(map[string]agent.MessageContent, bool) map[string][]byte) (*Agent, *tooltranscript.Transcript) {
	t.Helper()
	source := &cursorTestToolSource{read: read}
	transcript := tooltranscript.New(t.Context(), sink, source)
	a := newCursorAgentWithSink(transcript)
	a.transcript = transcript
	return a, transcript
}

// persistCursorToolRow persists the row that CLOSES one Cursor tool call, which is the
// row an extension frame describes.
func persistCursorToolRow(t *testing.T, transcript *tooltranscript.Transcript, toolCallID string) {
	t.Helper()
	original, err := json.Marshal(map[string]any{
		"sessionUpdate": "tool_call_update", "toolCallId": toolCallID, "status": "completed",
	})
	require.NoError(t, err)
	require.NoError(t, transcript.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: original}, agent.SpanInfo{SpanID: toolCallID, Closing: true}))
}

func TestCursorExtensionFramesLandOnTheirToolRow(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		method string
		params string
	}{
		{contracts.CursorMethodUpdateTodos, cursorProbeTodosParams},
		{contracts.CursorMethodTask, cursorProbeTaskParams},
		{contracts.CursorMethodGenerateImage, cursorProbeImageParams},
	} {
		t.Run(tc.method, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a, transcript := newCursorTranscriptAgent(t, agent.NewProviderServices(sink), nil)
			persistCursorToolRow(t, transcript, cursorProbeToolCallID)

			require.True(t, a.handleCursorExtension(tc.method, json.RawMessage(tc.params)))

			messages := sink.Messages()
			require.Len(t, messages, 1)
			// The supplement IDENTIFIES the row it belongs to. Without the identity keys
			// the browser's shared gate refuses the envelope, and the checklist, the
			// picture or the task card never reaches the reader. The id carries a
			// newline on purpose, so the expectation is MARSHALLED rather than joined.
			want, err := json.Marshal(map[string]any{
				"sessionUpdate": "tool_call_update",
				"toolCallId":    cursorProbeToolCallID,
				"status":        "completed",
				contracts.CursorSupplementExtension: map[string]any{
					"method": tc.method, "params": json.RawMessage(tc.params),
				},
			})
			require.NoError(t, err)
			assert.JSONEq(t, string(want), string(messages[0].SupplementalContent))
		})
	}
}

func TestCursorTaskStoreResultPersistsTheReportInTheChildTranscript(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCursorTestAgent(agent.NewProviderServices(sink))
	a.HandleToolCallForTest(json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"task-call","title":"Task: Inspect the parser","status":"pending","rawInput":{"_toolName":"task","prompt":"Find the parser.","description":"Inspect the parser"}}`))
	require.True(t, a.handleCursorExtension(contracts.CursorMethodTask,
		json.RawMessage(`{"toolCallId":"task-call","description":"Inspect the parser","prompt":"Find the parser.","agentId":"child"}`)))
	record := cursorToolRecord{content: json.RawMessage(`{
		"type":"tool-result",
		"toolCallId":"task-call",
		"toolName":"Task",
		"result":"This is the output of the subagent:\n\nresponse:\n<response>\nThe parser is in parser.go.\n</response>\n\nAgent ID: child"
	}`)}
	a.observeCursorTaskRecord("task-call", record)
	a.observeCursorTaskRecord("task-call", record)

	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	require.NotEmpty(t, rows[0].ChildAgentID)
	child := sink.Child(rows[0].ChildAgentID)
	require.Len(t, child.Messages(), 1)
	assert.JSONEq(t, `{"content":"Find the parser."}`, string(child.Messages()[0].Content))
	reports := child.LeapMuxNotifications()
	require.Len(t, reports, 1)
	assert.Equal(t, "The parser is in parser.go.", reports[0]["text"])
	assert.Empty(t, a.taskReports["task-call"].report,
		"a stored report retains no report text after persistence")
}

func TestCursorTaskStoreReplayDoesNotDuplicateAChildReport(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCursorTestAgent(agent.NewProviderServices(sink))
	a.HandleToolCallForTest(json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"task-call","title":"Task: Inspect","status":"pending","rawInput":{"_toolName":"task","prompt":"Inspect."}}`))
	a.observeCursorTaskRecord("task-call", cursorToolRecord{content: json.RawMessage(
		`{"type":"tool-result","toolCallId":"task-call","toolName":"Task","result":"<response>Report</response>"}`,
	)})

	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	child := sink.Child(rows[0].ChildAgentID)
	assert.Empty(t, child.LeapMuxNotifications(),
		"session/load replays the stored Task result but not cursor/task, so it must not copy the report again")
}

func TestCursorTaskStoreReplayKeepsPendingReportsBounded(t *testing.T) {
	t.Parallel()

	a := newCursorTestAgent(agent.NewProviderServices(&agenttest.Sink{}))
	for i := range cursorPendingTaskReportLimit + 20 {
		a.observeCursorTaskRecord(fmt.Sprintf("task-%d", i), cursorToolRecord{content: json.RawMessage(
			`{"type":"tool-result","toolName":"Task","result":"<response>Report</response>"}`,
		)})
	}
	assert.LessOrEqual(t, len(a.taskReports), cursorPendingTaskReportLimit)
}

func TestCursorTaskExtensionConsumesAReportThatTheStoreFoundFirst(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCursorTestAgent(agent.NewProviderServices(sink))
	a.HandleToolCallForTest(json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"task-call","title":"Task: Inspect","status":"pending","rawInput":{"_toolName":"task","prompt":"Inspect."}}`))
	a.observeCursorTaskRecord("task-call", cursorToolRecord{content: json.RawMessage(
		`{"type":"tool-result","toolCallId":"task-call","toolName":"Task","result":"<response>Report</response>"}`,
	)})
	require.True(t, a.handleCursorExtension(contracts.CursorMethodTask,
		json.RawMessage(`{"toolCallId":"task-call","description":"Inspect","prompt":"Inspect.","agentId":"child"}`)))

	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	child := sink.Child(rows[0].ChildAgentID)
	reports := child.LeapMuxNotifications()
	require.Len(t, reports, 1)
	assert.Equal(t, "Report", reports[0]["text"])
}

// The transcript's own store pass enriches the SAME row. It passes the revision this
// write left, so both supplements survive. A direct EnrichMessage would raise the
// revision under that pass, the pass would be refused for good, and the store output
// -- the whole diff, the whole search result -- would never reach the row.
func TestCursorExtensionKeepsTheStorePassEnrichmentAlive(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	var passes atomic.Int64
	a, transcript := newCursorTranscriptAgent(t, agent.NewProviderServices(sink), func(pending map[string]agent.MessageContent, _ bool) map[string][]byte {
		if len(pending) == 0 {
			return nil
		}
		passes.Add(1)
		return map[string][]byte{cursorProbeToolCallID: []byte(`{"rawOutput":{"content":[]}}`)}
	})
	persistCursorToolRow(t, transcript, cursorProbeToolCallID)

	require.True(t, a.handleCursorExtension(contracts.CursorMethodUpdateTodos, json.RawMessage(cursorProbeTodosParams)))
	require.NoError(t, transcript.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"done":true}`)}, agent.SpanInfo{}))

	require.Positive(t, passes.Load(), "the store pass must run")
	messages := sink.Messages()
	require.NotEmpty(t, messages)
	var supplement map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(messages[0].SupplementalContent, &supplement))
	assert.Contains(t, supplement, contracts.CursorSupplementExtension, "the extension frame survives the store pass")
	assert.Contains(t, supplement, "rawOutput", "the store records reach the row")
}

// The supplement worker runs between two frames of the reader goroutine, so a pass can
// enrich the row and drop its pending entry before the extension frame arrives. The
// row itself then holds the only record of the revision.
func TestCursorExtensionEnrichesARowThePassAlreadySettled(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a, transcript := newCursorTranscriptAgent(t, agent.NewProviderServices(sink), func(map[string]agent.MessageContent, bool) map[string][]byte {
		return map[string][]byte{cursorProbeToolCallID: []byte(`{"rawOutput":{"content":[]}}`)}
	})
	persistCursorToolRow(t, transcript, cursorProbeToolCallID)
	// The turn end runs the final pass and clears the pending set.
	require.NoError(t, transcript.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"done":true}`)}, agent.SpanInfo{}))
	require.Empty(t, transcript.PendingSpanIDsForTest())

	require.True(t, a.handleCursorExtension(contracts.CursorMethodGenerateImage, json.RawMessage(cursorProbeImageParams)))

	var supplement map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(sink.Messages()[0].SupplementalContent, &supplement))
	assert.Contains(t, supplement, contracts.CursorSupplementExtension)
	assert.Contains(t, supplement, "rawOutput")
}

// A frame with no toolCallId identifies no row. It is still answered, because Cursor keeps
// no state for an extension request and an error reply reads as a failed turn.
func TestCursorExtensionWithNoToolCallIsStillHandled(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a, transcript := newCursorTranscriptAgent(t, agent.NewProviderServices(sink), nil)
	persistCursorToolRow(t, transcript, cursorProbeToolCallID)

	require.True(t, a.handleCursorExtension(contracts.CursorMethodUpdateTodos, json.RawMessage(`{"todos":[]}`)))
	assert.Empty(t, sink.Messages()[0].SupplementalContent)
}

func TestCursorExtensionRefusesAMethodItDoesNotKnow(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a, _ := newCursorTranscriptAgent(t, agent.NewProviderServices(sink), nil)
	assert.False(t, a.handleCursorExtension("cursor/record_screen", json.RawMessage(`{"toolCallId":"call"}`)))
}

// The ack is queued, not written inline: HandleOutput runs on the goroutine that
// drains Cursor's stdout, and an ack is a write to a stdin Cursor may not read.
func TestCursorExtensionFrameIsAcknowledged(t *testing.T) {
	t.Parallel()
	output := &agenttest.Stdin{}
	sink := &agenttest.Sink{}
	a, _ := newCursorTranscriptAgent(t, agent.NewProviderServices(sink), nil)
	a.SetStdinForTest(agenttest.NopStdin(output))

	a.HandleOutput([]byte(`{"jsonrpc":"2.0","id":7,"method":"` + contracts.CursorMethodTask + `","params":` + cursorProbeTaskParams + `}`))

	var answer string
	require.Eventually(t, func() bool {
		answer = output.String()
		return answer != ""
	}, 2*time.Second, 5*time.Millisecond, "the extension frame is answered")
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":7,"result":{}}`, answer)
}

func TestCursorExtractTodoEventReadsAReplacementAsASnapshot(t *testing.T) {
	t.Parallel()
	event, ok := cursorProvider{}.ExtractTodoEvent("", cursorStoredTodoContent(t, cursorProbeTodosParams), nil)
	require.True(t, ok)
	assert.Equal(t, todoevents.KindSnapshot, event.Kind)
	assert.Equal(t, []todoevents.Item{
		{ID: "1", Content: "Create a.txt", Status: todoevents.StatusInProgress},
		{ID: "2", Content: "Create b.txt", Status: todoevents.StatusPending},
	}, event.Snapshot)
}

// Reading a merge frame as a snapshot would delete every row it stayed silent about.
// Cursor sends the full list once and the changed subset on every later frame.
func TestCursorExtractTodoEventReadsASubsetAsAMerge(t *testing.T) {
	t.Parallel()
	event, ok := cursorProvider{}.ExtractTodoEvent("", cursorStoredTodoContent(t, cursorProbeMergeParams), nil)
	require.True(t, ok)
	assert.Equal(t, todoevents.KindMerge, event.Kind)
	assert.Equal(t, []todoevents.Item{{ID: "1", Content: "Create a.txt", Status: todoevents.StatusCompleted}}, event.Items)
	assert.Empty(t, event.Snapshot)
}

func TestCursorExtractTodoEventReadsCancelledAsTheTombstone(t *testing.T) {
	t.Parallel()
	params := `{"toolCallId":"call","todos":[{"id":"1","content":"Stop","status":"cancelled"}],"merge":false}`
	event, ok := cursorProvider{}.ExtractTodoEvent("", cursorStoredTodoContent(t, params), nil)
	require.True(t, ok)
	assert.Equal(t, []todoevents.Item{{ID: "1", Content: "Stop", Status: todoevents.StatusDeleted}}, event.Snapshot)
}

func TestCursorExtractTodoEventDistinguishesAnEmptyMergeFromAnEmptyList(t *testing.T) {
	t.Parallel()
	_, ok := cursorProvider{}.ExtractTodoEvent("",
		cursorStoredTodoContent(t, `{"toolCallId":"call","todos":[],"merge":true}`), nil)
	assert.False(t, ok, "an empty merge states that nothing changed")

	event, ok := cursorProvider{}.ExtractTodoEvent("",
		cursorStoredTodoContent(t, `{"toolCallId":"call","todos":[],"merge":false}`), nil)
	require.True(t, ok, "an empty replacement states that the list is now empty")
	assert.Equal(t, todoevents.KindSnapshot, event.Kind)
	assert.Empty(t, event.Snapshot)
}

// A frame that listed rows and yielded none says nothing about the list. Every row it
// carried had no id, which the extractor skips, and reading the result as a
// REPLACEMENT deleted every row and broadcast an empty checklist -- so one malformed
// frame wiped a list the agent was still working through.
func TestCursorExtractTodoEventRefusesAFrameWhoseRowsAllLackAnID(t *testing.T) {
	t.Parallel()
	for name, params := range map[string]string{
		"replacement": `{"toolCallId":"call","todos":[{"content":"Create a.txt","status":"pending"}],"merge":false}`,
		"merge":       `{"toolCallId":"call","todos":[{"content":"Create a.txt","status":"pending"}],"merge":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, ok := cursorProvider{}.ExtractTodoEvent("", cursorStoredTodoContent(t, params), nil)
			assert.False(t, ok)
		})
	}
}

// A frame that carries one usable row beside an unusable one still states the list.
// The guard above must test what the frame LISTED, not only what it yielded.
func TestCursorExtractTodoEventKeepsTheRowsThatCarryAnID(t *testing.T) {
	t.Parallel()
	params := `{"toolCallId":"call","todos":[{"content":"No id","status":"pending"},` +
		`{"id":"2","content":"Create b.txt","status":"pending"}],"merge":false}`
	event, ok := cursorProvider{}.ExtractTodoEvent("", cursorStoredTodoContent(t, params), nil)
	require.True(t, ok)
	assert.Equal(t, todoevents.KindSnapshot, event.Kind)
	assert.Equal(t, []todoevents.Item{{ID: "2", Content: "Create b.txt", Status: todoevents.StatusPending}}, event.Snapshot)
}

// A `todos` value that is not a list of rows refuses the frame, exactly as a frame
// with a broken outer shape does.
//
// contracts.CursorExtensionParams keeps `Todos` raw, because the item's own three
// fields are the SHARED to-do vocabulary rather than Cursor's, so the rows decode in a
// second step. A failure there must refuse the frame: read as an EMPTY list, a
// replacement would delete every row and broadcast an empty checklist.
func TestCursorExtractTodoEventRefusesAFrameWhoseRowsDoNotDecode(t *testing.T) {
	t.Parallel()
	for name, params := range map[string]string{
		"an object where the list belongs": `{"toolCallId":"call","todos":{"id":"1"},"merge":false}`,
		"a row that is not an object":      `{"toolCallId":"call","todos":["Create a.txt"],"merge":false}`,
		"an id that is not a string":       `{"toolCallId":"call","todos":[{"id":5}],"merge":false}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, ok := cursorProvider{}.ExtractTodoEvent("", cursorStoredTodoContent(t, params), nil)
			assert.False(t, ok)
		})
	}
}

// Cursor speaks ACP, so every message that is not a stored extension frame still reads
// through the shared ACP extractor.
func TestCursorExtractTodoEventDelegatesToACP(t *testing.T) {
	t.Parallel()
	plan := []byte(`{"sessionUpdate":"plan","entries":[{"content":"Ship it","status":"pending"}]}`)
	event, ok := cursorProvider{}.ExtractTodoEvent("", plan, nil)
	require.True(t, ok)
	assert.Equal(t, todoevents.KindSnapshot, event.Kind)
	assert.Equal(t, []todoevents.Item{{Content: "Ship it", Status: todoevents.StatusPending}}, event.Snapshot)

	_, ok = cursorProvider{}.ExtractTodoEvent("", []byte(`{"sessionUpdate":"tool_call","toolCallId":"call"}`), nil)
	assert.False(t, ok)
}

// cursorProbeRowFrame is the frame of the row an extension supplement names.
var cursorProbeRowFrame = []byte(`{"sessionUpdate":"tool_call_update","toolCallId":"call","status":"completed"}`)

func TestCursorResolveProviderDataCarriesTheStoredExtension(t *testing.T) {
	t.Parallel()
	supplement, err := cursorExtensionSupplement(cursorProbeRowFrame, contracts.CursorMethodUpdateTodos, json.RawMessage(cursorProbeTodosParams))
	require.NoError(t, err)
	resolved := cursorProvider{}.ResolveProviderData(agent.MessageContent{Original: cursorProbeRowFrame, Supplemental: supplement})
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(resolved, &fields))
	assert.Contains(t, fields, contracts.CursorSupplementExtension)
	assert.Contains(t, fields, "toolCallId", "the ACP fields of the original stay")
}

func TestCursorResolveProviderDataLeavesEveryOtherRowAlone(t *testing.T) {
	t.Parallel()
	original := cursorProbeRowFrame
	assert.Equal(t, original, cursorProvider{}.ResolveProviderData(agent.MessageContent{Original: original}))
	assert.Equal(t, original, cursorProvider{}.ResolveProviderData(agent.MessageContent{
		Original: original, Supplemental: []byte(`{"sessionUpdate":"tool_call_update","toolCallId":"call","status":"completed"}`),
	}))
}

// An extension stored beside ANOTHER call cannot reach this row, and one that identifies no
// call at all cannot either. The browser applies the same gate, so a supplement either
// side takes must be one both take.
func TestCursorResolveProviderDataRefusesAnExtensionForAnotherRow(t *testing.T) {
	t.Parallel()
	for _, frame := range [][]byte{
		[]byte(`{"sessionUpdate":"tool_call_update","toolCallId":"other","status":"completed"}`),
		[]byte(`{"sessionUpdate":"tool_call_update","toolCallId":"call","status":"pending"}`),
		[]byte(`{}`),
	} {
		supplement, err := cursorExtensionSupplement(frame, contracts.CursorMethodUpdateTodos, json.RawMessage(cursorProbeTodosParams))
		require.NoError(t, err)
		resolved := cursorProvider{}.ResolveProviderData(agent.MessageContent{Original: cursorProbeRowFrame, Supplemental: supplement})
		assert.Equal(t, cursorProbeRowFrame, resolved, "a supplement that identifies %s must reach no row", frame)
	}
}

// cursorStoredTodoContent builds the resolved content of a row that carries one stored
// cursor/update_todos frame.
func cursorStoredTodoContent(t *testing.T, params string) []byte {
	t.Helper()
	supplement, err := cursorExtensionSupplement(cursorProbeRowFrame, contracts.CursorMethodUpdateTodos, json.RawMessage(params))
	require.NoError(t, err)
	return cursorProvider{}.ResolveProviderData(agent.MessageContent{
		Original:     []byte(`{"sessionUpdate":"tool_call_update","toolCallId":"call","status":"completed"}`),
		Supplemental: supplement,
	})
}
