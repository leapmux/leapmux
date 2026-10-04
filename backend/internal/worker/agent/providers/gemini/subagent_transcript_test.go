package gemini

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/coder/quartz"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func geminiChildTranscriptFixture(t *testing.T) (*geminiChildTranscript, *agenttest.Sink, string) {
	t.Helper()
	query, directory, hash := geminiStoreFixture(t)
	writeGeminiSession(t, geminiFixtureSessionPath(directory, "root"), "root", hash, "main", "root prompt")
	path := filepath.Join(directory, "root", geminiNativeChildID+".jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	writeGeminiSession(t, path, geminiNativeChildID, hash, "subagent", "native child prompt")
	sink := &agenttest.Sink{}
	transcript := newGeminiChildTranscript(t.Context(), testutil.NewQuartzMock(t), agent.NewProviderServices(sink), query, "root", "root-agent")
	return transcript, sink, path
}

type geminiChildNoticeServices struct {
	agent.ProviderServices
	stored chan struct{}
}

func (services *geminiChildNoticeServices) ChildSink(id string) agent.ProviderServices {
	return &geminiChildNoticeSink{ProviderServices: services.ProviderServices.ChildSink(id), stored: services.stored}
}

type geminiChildNoticeSink struct {
	agent.ProviderServices
	stored chan struct{}
}

func (sink *geminiChildNoticeSink) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	if err := sink.ProviderServices.PersistMessage(source, content, span); err != nil {
		return err
	}
	if bytes.Contains(content.Original, []byte("native clock progress")) {
		select {
		case sink.stored <- struct{}{}:
		default:
		}
	}
	return nil
}

func TestGeminiChildTranscriptReadsLiveRecordsThroughTheProcessClock(t *testing.T) {
	t.Parallel()
	transcript, sink, path := geminiChildTranscriptFixture(t)
	clock := transcript.clock.(*quartz.Mock)
	trap := clock.Trap().NewTicker("gemini", "child-transcript")
	defer trap.Close()
	stored := make(chan struct{}, 1)
	transcript.services = &geminiChildNoticeServices{ProviderServices: agent.NewProviderServices(sink), stored: stored}
	transcript.observe("root", json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"invoke_agent__native-clock","status":"in_progress"}`))
	transcript.start()
	defer transcript.stop(agent.MessageCompletionInterrupted)
	ctx := testutil.DeadlineContext(t)
	assert.Equal(t, geminiChildPollInterval, testutil.WaitForTimer(t, ctx, trap))
	appendGeminiChildRecord(t, path, `{"id":"native-clock-model","type":"gemini","content":"native clock progress"}`)
	clock.Advance(geminiChildPollInterval).MustWait(ctx)
	select {
	case <-stored:
	case <-ctx.Done():
		t.Fatal("the process clock tick did not persist the native child record")
	}
}

func TestGeminiChildTranscriptRejectsStartAfterStopAndJoinsConcurrentStops(t *testing.T) {
	t.Parallel()
	transcript, sink, _ := geminiChildTranscriptFixture(t)
	transcript.stop(agent.MessageCompletionInterrupted)
	transcript.start()
	assert.Empty(t, sink.ChildAgentIDs())
	transcript.lifecycleMu.Lock()
	assert.False(t, transcript.started)
	assert.True(t, transcript.closed)
	transcript.lifecycleMu.Unlock()
	var stopped sync.WaitGroup
	for range 4 {
		stopped.Go(func() { transcript.stop(agent.MessageCompletionInterrupted) })
	}
	stopped.Wait()
}

func appendGeminiChildRecord(t *testing.T, path, record string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = file.WriteString(record + "\n")
	require.NoError(t, err)
	require.NoError(t, file.Close())
}

func TestGeminiChildTranscriptKeepsNativeUUIDAndStoresLiveRowsBeforeCompletion(t *testing.T) {
	t.Parallel()
	transcript, sink, path := geminiChildTranscriptFixture(t)
	appendGeminiChildRecord(t, path, `{"id":"native-live-model","type":"gemini","content":"native child progress","thoughts":[{"subject":"Inspect","description":"native child thought"}],"toolCalls":[{"id":"read_file__child-call","name":"read_file","args":{"file_path":"/native/file"},"status":"success","result":[{"functionResponse":{"response":{"output":"actual native file bytes"}}}]}]}`)
	transcript.flush(false, agent.MessageCompletionComplete)
	row, found := sink.BackgroundTask(geminiNativeChildID)
	require.True(t, found)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.Equal(t, bgtask.KindSubagent, row.Kind)
	span, err := sink.ChildSpawnSpan(row.ChildAgentID)
	require.NoError(t, err)
	assert.Empty(t, span, "the native archive provides no direct spawn span")
	child := sink.Child(row.ChildAgentID)
	require.NotNil(t, child)
	require.Len(t, child.Messages(), 4)
	for _, message := range child.Messages() {
		var frame map[string]any
		require.NoError(t, json.Unmarshal(message.Content, &frame))
		assert.NotContains(t, frame, "sessionUpdate")
	}
	appendGeminiChildRecord(t, path, `{"id":"native-complete-model","type":"gemini","content":"","toolCalls":[{"id":"complete_task__child-end","name":"complete_task","args":{"result":{"response":"native final findings"}},"status":"success","result":[{"functionResponse":{"response":{"output":"Output submitted and task completed."}}}]}]}`)
	transcript.flush(true, agent.MessageCompletionComplete)
	row, found = sink.BackgroundTask(geminiNativeChildID)
	require.True(t, found)
	assert.Equal(t, bgtask.StatusCompleted, row.Status)
	assert.Len(t, child.Messages(), 6)
}

func TestGeminiChildTranscriptReplayDoesNotDuplicateImmutableNativeRows(t *testing.T) {
	t.Parallel()
	transcript, sink, _ := geminiChildTranscriptFixture(t)
	transcript.flush(false, agent.MessageCompletionComplete)
	ids := sink.ChildAgentIDs()
	require.Len(t, ids, 1)
	child := sink.Child(ids[0])
	require.NotNil(t, child)
	before := child.Messages()
	replay := newGeminiChildTranscript(t.Context(), testutil.NewQuartzMock(t), agent.NewProviderServices(sink), transcript.query, "root", "root-agent")
	replay.flush(false, agent.MessageCompletionComplete)
	assert.Equal(t, before, child.Messages())
	assert.Equal(t, ids, sink.ChildAgentIDs())
}

func TestGeminiChildTranscriptClosesAnIncompleteTaskWithTheActualParentOutcome(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		completion agent.MessageCompletion
		status     bgtask.Status
	}{{agent.MessageCompletionInterrupted, bgtask.StatusInterrupted}, {agent.MessageCompletionComplete, bgtask.StatusFailed}, {agent.MessageCompletionError, bgtask.StatusFailed}} {
		transcript, sink, _ := geminiChildTranscriptFixture(t)
		transcript.flush(true, test.completion)
		row, found := sink.BackgroundTask(geminiNativeChildID)
		require.True(t, found)
		assert.Equal(t, test.status, row.Status)
	}
}

func TestGeminiChildTranscriptUsesOnlyTheExactNativeRootInvocation(t *testing.T) {
	t.Parallel()
	transcript, _, _ := geminiChildTranscriptFixture(t)
	update := json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"invoke_agent__call","status":"in_progress"}`)
	transcript.observe("foreign", update)
	assert.False(t, transcript.active())
	transcript.observe("root", update)
	assert.True(t, transcript.active())
	transcript.observe("root", json.RawMessage(`{"sessionUpdate":"tool_call_update","toolCallId":"invoke_agent__call","status":"completed"}`))
	assert.False(t, transcript.active())
}

func TestGeminiChildTranscriptStopBeforeStartDoesNotWaitForAnAbsentLoop(t *testing.T) {
	t.Parallel()
	transcript, _, _ := geminiChildTranscriptFixture(t)
	stopped := make(chan struct{})
	go func() {
		transcript.stop(agent.MessageCompletionInterrupted)
		close(stopped)
	}()
	ctx := testutil.DeadlineContext(t)
	select {
	case <-stopped:
	case <-ctx.Done():
		transcript.start()
		<-stopped
		t.Fatal("the stop waited for a child loop that did not start")
	}
}
