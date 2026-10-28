package fastagent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFastagentUnsentFirstPromptKeepsTheFreshArchiveAllowance(t *testing.T) {
	a, sink := newFastagentRootSubject(t)
	require.ErrorContains(t, a.SendPreparedPrompt("rejected input", nil, func(string) error { return errors.New("preparation refused") }), "preparation refused")
	a.rootArchiveWG.Wait()
	root := openRootFixture(t, a)
	appendRootFixture(t, root, `{"live":"limited"}`)
	writeRootNativeHistory(t, a, rootNativeMessages("full first output"))
	finishRootFixture(t, a, root)
	rows := sink.Messages()
	require.Len(t, rows, 4, "a positively unsent request must not consume the first native history boundary")
	assert.Contains(t, string(rows[2].Content), "full first output")
}

func TestFastagentRootRefusesAnArchiveWithAnotherDispatchInput(t *testing.T) {
	a, sink := newFastagentRootSubject(t)
	root := openRootFixture(t, a)
	appendRootFixture(t, root, `{"live":"the actual dispatch output"}`)
	messages := rootNativeMessages("the wrong archive output")
	messages[0] = json.RawMessage(`{"role":"user","content":[{"type":"text","text":"another dispatch"}]}`)
	writeRootNativeHistory(t, a, messages)
	finishRootFixture(t, a, root)
	rows := sink.Messages()
	require.Len(t, rows, 1)
	assert.Contains(t, string(rows[0].Content), "actual dispatch output")
}

type rootEndingServices struct {
	agent.ProviderServices
	events []string
}

func (services *rootEndingServices) PersistWorkerNotification(content agent.MessageContent) (bool, error) {
	services.events = append(services.events, "warning")
	return services.ProviderServices.PersistWorkerNotification(content)
}

func (services *rootEndingServices) PersistTurnEnd(content agent.MessageContent, span agent.SpanInfo) error {
	services.events = append(services.events, "end")
	return services.ProviderServices.PersistTurnEnd(content, span)
}

func TestFastagentFallbackWarningPrecedesTheOriginalDivider(t *testing.T) {
	a, _ := newFastagentRootSubject(t)
	services := &rootEndingServices{ProviderServices: a.Sink()}
	a.SetSinkForTest(services)
	root := openRootFixture(t, a)
	appendRootFixture(t, root, `{"live":"output"}`)
	ending := root.Reserve()
	require.NoError(t, ending.Writer().PersistTurnEnd(agent.MessageContent{Original: []byte(`{"stopReason":"end_turn"}`)}, agent.SpanInfo{}))
	ending.Complete()
	finishRootFixture(t, a, root)
	assert.Equal(t, []string{"warning", "end"}, services.events)
}

func TestFastagentDiscardedRootPublishesNeitherRepresentation(t *testing.T) {
	a, sink := newFastagentRootSubject(t)
	root := openRootFixture(t, a)
	appendRootFixture(t, root, `{"live":"discarded"}`)
	writeRootNativeHistory(t, a, rootNativeMessages("also discarded"))
	require.NoError(t, root.Finish(acp.RootTranscriptCompletion{Reason: acp.RootTranscriptPromptEnded, Completion: agent.MessageCompletionComplete, Discard: true}))
	a.rootArchiveWG.Wait()
	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.LeapMuxNotifications())
	require.NoError(t, a.Sink().PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"live":"healthy"}`)}, agent.SpanInfo{}))
	require.Len(t, sink.Messages(), 1)
	assert.False(t, sink.OutputQueue().HasPending())
}

// Count only retained payloads. Metadata and the caller's own buffers do not
// supply the bytes needed to reconstruct the selected fallback after mutation.
func retainedRootPayloadBytes(group *rootTranscriptGroup) int {
	seen := make(map[uintptr]bool)
	var visit func(reflect.Value) int
	visit = func(value reflect.Value) int {
		if !value.IsValid() {
			return 0
		}
		switch value.Kind() {
		case reflect.Pointer:
			if value.IsNil() || seen[value.Pointer()] {
				return 0
			}
			seen[value.Pointer()] = true
			if value.Type() == reflect.TypeFor[*rootTranscriptGroup]() || value.Type() == reflect.TypeFor[*rootTranscript]() {
				return 0
			}
			return visit(value.Elem())
		case reflect.Slice:
			if value.Type().Elem().Kind() == reflect.Uint8 {
				return value.Len()
			}
			total := 0
			for i := 0; i < value.Len(); i++ {
				total += visit(value.Index(i))
			}
			return total
		case reflect.Struct:
			total := 0
			for i := 0; i < value.NumField(); i++ {
				total += visit(value.Field(i))
			}
			return total
		default:
			return 0
		}
	}
	value := reflect.ValueOf(group).Elem()
	return visit(value.FieldByName("positions")) + visit(value.FieldByName("entries"))
}

func TestFastagentRootDoesNotRetainTheWholeTurnPayloadInMemory(t *testing.T) {
	a, sink := newFastagentRootSubject(t)
	root := openRootFixture(t, a)
	const count = 12
	for i := 0; i < count; i++ {
		payload := []byte(`{"text":"` + strings.Repeat("x", 1<<20) + `"}`)
		position := root.Reserve()
		require.NoError(t, position.Writer().PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: payload}, agent.SpanInfo{}))
		position.Complete()
		payload[0] = 'x'
	}
	assert.LessOrEqual(t, retainedRootPayloadBytes(root.group), 8<<20, "the complete buffered turn needs durable storage, not unlimited payload retention")
	assert.Empty(t, sink.Messages())
	finishRootFixture(t, a, root)
	rows := sink.Messages()
	require.Len(t, rows, count)
	for _, row := range rows {
		assert.Equal(t, byte('{'), row.Content[0])
		assert.Len(t, row.Content, (1<<20)+11)
	}
}

type rootFailOnceScope struct {
	agent.TranscriptScope
	calls int
}

func (scope *rootFailOnceScope) CaptureTranscript(content agent.MessageContent, span agent.SpanInfo) (agent.CapturedTranscript, error) {
	scope.calls++
	if scope.calls == 2 {
		return agent.CapturedTranscript{}, errors.New("the capture was temporarily refused")
	}
	return scope.TranscriptScope.CaptureTranscript(content, span)
}

func TestFastagentRootCaptureFailureRetainsRetryOwnership(t *testing.T) {
	a, sink := newFastagentRootSubject(t)
	root := openRootFixture(t, a)
	root.group.scope = &rootFailOnceScope{TranscriptScope: root.group.scope}
	appendRootFixture(t, root, `{"live":"first"}`)
	appendRootFixture(t, root, `{"live":"second"}`)
	finishRootFixture(t, a, root)
	require.True(t, sink.OutputQueue().HasPending())
	err := a.Sink().PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"live":"later healthy row"}`)}, agent.SpanInfo{})
	require.NoError(t, err, "a retryable capture must not strand an unready queue position")
	rows := sink.Messages()
	require.Len(t, rows, 3)
	for i, text := range []string{"first", "second", "later healthy row"} {
		assert.Contains(t, string(rows[i].Content), text)
	}
	assert.False(t, sink.OutputQueue().HasPending())
}

func newFastagentRootSubject(t *testing.T) (*Agent, *agenttest.Sink) {
	t.Helper()
	home, cwd := t.TempDir(), t.TempDir()
	a, _ := acptest.NewAgentForRPC(t, func() *Agent {
		return &Agent{home: home, workingDir: cwd, nativeCommands: true, bypassPermissions: true}
	}, func(a *Agent) *acp.Base { return &a.Base })
	sink := &agenttest.Sink{}
	sink.UpdateSessionID("session-1")
	services := agent.NewOrderedProviderServices(sink.OutputQueue(), agent.NewProviderServices(sink))
	a.SetSinkForTest(services)
	*a.HooksForTest() = a.hooks("", services)
	a.ObserveCurrentMode("agent")
	require.NoError(t, a.initializeRootArchive(false))
	return a, sink
}

func writeRootNativeHistory(t *testing.T, a *Agent, messages []json.RawMessage) {
	t.Helper()
	directory := writeFastagentRootArchive(t, a.home, a.CurrentSessionID(), a.workingDir, nil)
	raw, err := json.Marshal(map[string]any{"messages": messages})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "history_agent.json"), raw, 0o600))
}

func openRootFixture(t *testing.T, a *Agent) *rootTranscript {
	t.Helper()
	root, ok := a.openRootTranscript(acp.RootTranscriptStart{SessionID: a.CurrentSessionID(), NativeModeID: "agent"}, a.Sink()).(*rootTranscript)
	require.True(t, ok)
	require.NoError(t, root.Prepare())
	require.NoError(t, root.PrepareInput([]json.RawMessage{json.RawMessage(`{"type":"text","text":"current input"}`)}))
	return root
}

func appendRootFixture(t *testing.T, root *rootTranscript, text string) {
	t.Helper()
	position := root.Reserve()
	require.NoError(t, position.Writer().PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(text)}, agent.SpanInfo{}))
	position.Complete()
}

func finishRootFixture(t *testing.T, a *Agent, root *rootTranscript) {
	t.Helper()
	require.NoError(t, root.Finish(acp.RootTranscriptCompletion{Reason: acp.RootTranscriptPromptEnded, Completion: agent.MessageCompletionComplete}))
	a.rootArchiveWG.Wait()
}

func TestFastagentRootChoosesTheCompleteArchiveBeforePublication(t *testing.T) {
	a, sink := newFastagentRootSubject(t)
	root := openRootFixture(t, a)
	appendRootFixture(t, root, `{"live":"Before"}`)
	appendRootFixture(t, root, `{"live":"limited shell tail"}`)
	assert.Empty(t, sink.Messages())
	writeRootNativeHistory(t, a, rootNativeMessages("complete output"))
	finishRootFixture(t, a, root)
	rows := sink.Messages()
	require.Len(t, rows, 4)
	assert.Contains(t, string(rows[2].Content), "complete output")
	for _, row := range rows {
		assert.NotContains(t, string(row.Content), `"live"`)
		assert.Equal(t, "session-1", row.AgentSessionID)
	}
	finishRootFixture(t, a, root)
	assert.Equal(t, rows, sink.Messages(), "a repeated completion does not replay the group")
}

func TestFastagentRootArchiveFailureKeepsEveryLiveRowInOrder(t *testing.T) {
	for _, failure := range []string{"missing", "incomplete", "malformed later result"} {
		t.Run(failure, func(t *testing.T) {
			a, sink := newFastagentRootSubject(t)
			root := openRootFixture(t, a)
			appendRootFixture(t, root, `{"live":"before"}`)
			appendRootFixture(t, root, `{"live":"tool"}`)
			appendRootFixture(t, root, `{"live":"after"}`)
			if failure != "missing" {
				messages := rootNativeMessages("candidate output")
				if failure == "incomplete" {
					messages = messages[:2]
				} else {
					messages[2] = json.RawMessage(`{"role":"user","tool_results":{"model-call":{"content":null}}}`)
				}
				writeRootNativeHistory(t, a, messages)
			}
			finishRootFixture(t, a, root)
			rows := sink.Messages()
			require.Len(t, rows, 3)
			for i, text := range []string{"before", "tool", "after"} {
				assert.JSONEq(t, `{"live":"`+text+`"}`, string(rows[i].Content))
			}
			require.Len(t, sink.LeapMuxNotifications(), 1)
			assert.Contains(t, sink.LeapMuxNotifications()[0]["text"], "complete live transcript")
		})
	}
}

func TestFastagentRootResumeRetiresHistoricalMessages(t *testing.T) {
	a, sink := newFastagentRootSubject(t)
	old := rootNativeMessages("old output")
	writeRootNativeHistory(t, a, old)
	require.NoError(t, a.initializeRootArchive(true))
	root := openRootFixture(t, a)
	writeRootNativeHistory(t, a, append(old, rootNativeMessages("new output")...))
	finishRootFixture(t, a, root)
	rows := sink.Messages()
	require.Len(t, rows, 4)
	assert.Contains(t, string(rows[2].Content), "new output")
	assert.NotContains(t, string(rows[2].Content), "old output")
}

func TestFastagentRootMissingBaselineIsFreshOnlyOnce(t *testing.T) {
	a, sink := newFastagentRootSubject(t)
	first := openRootFixture(t, a)
	appendRootFixture(t, first, `{"live":"first"}`)
	finishRootFixture(t, a, first)
	next := openRootFixture(t, a)
	appendRootFixture(t, next, `{"live":"second"}`)
	writeRootNativeHistory(t, a, rootNativeMessages("unproved suffix"))
	finishRootFixture(t, a, next)
	rows := sink.Messages()
	require.Len(t, rows, 2)
	assert.Contains(t, string(rows[1].Content), "second")
	assert.NotContains(t, string(rows[1].Content), "unproved suffix")
}

func TestFastagentRootPositionsRetainAdmissionOrderAndOmittedPreparation(t *testing.T) {
	a, sink := newFastagentRootSubject(t)
	root := openRootFixture(t, a)
	first, second, omitted := root.Reserve(), root.Reserve(), root.Reserve()
	require.NoError(t, second.Writer().PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"order":2}`)}, agent.SpanInfo{}))
	second.Complete()
	prepared := 0
	require.NoError(t, agent.PersistPreparedMessage(omitted.Writer(), leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{}`)}, agent.SpanInfo{}, func() []byte { prepared++; return nil }))
	omitted.Complete()
	require.NoError(t, first.Writer().PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"order":1}`)}, agent.SpanInfo{}))
	first.Complete()
	first.Complete()
	finishRootFixture(t, a, root)
	rows := sink.Messages()
	require.Len(t, rows, 2)
	assert.JSONEq(t, `{"order":1}`, string(rows[0].Content))
	assert.JSONEq(t, `{"order":2}`, string(rows[1].Content))
	assert.Equal(t, 1, prepared)
}

func TestFastagentPendingToolInputUsesTheExactReceiptAndRevision(t *testing.T) {
	a, sink := newFastagentRootSubject(t)
	root := openRootFixture(t, a)
	position := root.Reserve()
	writer := position.Writer()
	overlay := writer.(acp.PendingToolRequestWriter)
	first, other := agent.NewTranscriptWriteReceipt(), agent.NewTranscriptWriteReceipt()
	for _, receipt := range []*agent.TranscriptWriteReceipt{first, other} {
		require.NoError(t, writer.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"request":true}`), WriteReceipt: receipt}, agent.SpanInfo{SpanID: "same", SpanType: "read"}))
	}
	position.Complete()
	snapshot, found, err := overlay.ReadPendingToolRequest(first)
	require.NoError(t, err)
	require.True(t, found)
	snapshot.Content.Original[0] = 'x'
	updated, err := overlay.EnrichPendingToolRequest(first, 0, []byte(`{"request":true}`), []byte(`{"path":"first"}`))
	require.NoError(t, err)
	require.True(t, updated)
	updated, err = overlay.EnrichPendingToolRequest(first, 0, []byte(`{"request":true}`), []byte(`{"path":"stale"}`))
	require.NoError(t, err)
	assert.False(t, updated)
	snapshot, found, err = overlay.ReadPendingToolRequest(other)
	require.NoError(t, err)
	require.True(t, found)
	assert.Empty(t, snapshot.Content.Supplemental)
	finishRootFixture(t, a, root)
	rows := sink.Messages()
	require.Len(t, rows, 2)
	assert.JSONEq(t, `{"path":"first"}`, string(rows[0].SupplementalContent))
	assert.Empty(t, rows[1].SupplementalContent)
	_, committed := first.StoredMessageSequence()
	assert.True(t, committed)
	updated, err = overlay.EnrichPendingToolRequest(first, 1, []byte(`{"request":true}`), []byte(`{"path":"late"}`))
	require.NoError(t, err)
	assert.False(t, updated)
}

func TestFastagentOverlappingLogicalTurnsShareOneCompleteLiveGroup(t *testing.T) {
	a, sink := newFastagentRootSubject(t)
	first := openRootFixture(t, a)
	appendRootFixture(t, first, `{"turn":"first"}`)
	second := a.openRootTranscript(acp.RootTranscriptStart{SessionID: "session-1", AgentInitiated: true, Preceding: first}, a.Sink()).(*rootTranscript)
	require.NoError(t, second.Prepare())
	appendRootFixture(t, second, `{"turn":"second"}`)
	finishRootFixture(t, a, first)
	assert.Empty(t, sink.Messages())
	appendRootFixture(t, second, `{"turn":"second end"}`)
	finishRootFixture(t, a, second)
	rows := sink.Messages()
	require.Len(t, rows, 3)
	assert.Contains(t, string(rows[0].Content), "first")
	assert.Contains(t, string(rows[1].Content), "second")
	assert.Contains(t, string(rows[2].Content), "second end")
}

func TestFastagentRootSelectsCompleteNativeInterruptedSuffix(t *testing.T) {
	a, sink := newFastagentRootSubject(t)
	root := openRootFixture(t, a)
	appendRootFixture(t, root, `{"live":"pending opener only"}`)
	messages := rootNativeMessages("")[:3]
	messages[2] = json.RawMessage(`{"role":"user","content":[{"type":"text","text":"**The user interrupted this tool call**"}],"tool_results":{"model-call":{"content":[{"type":"text","text":"**The user interrupted this tool call**"}],"isError":true}}}`)
	writeRootNativeHistory(t, a, messages)
	require.NoError(t, root.Finish(acp.RootTranscriptCompletion{Reason: acp.RootTranscriptPromptEnded, Completion: agent.MessageCompletionInterrupted}))
	a.rootArchiveWG.Wait()
	rows := sink.Messages()
	require.Len(t, rows, 4)
	assert.NotContains(t, string(rows[0].Content), `"live"`)
	assert.Contains(t, string(rows[2].Content), "The user interrupted this tool call")
	assert.True(t, rows[2].Closing)
	assert.Equal(t, agent.MessageCompletionInterrupted, rows[2].Completion)
	assert.Empty(t, sink.LeapMuxNotifications())
}
