package gemini

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/coder/quartz"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
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
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
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

type geminiCloseCountingServices struct {
	agent.ProviderServices
	mu         sync.Mutex
	closeCalls []bgtask.Status
	failNext   bool
}

func (s *geminiCloseCountingServices) CloseBackgroundTask(rowKey string, status bgtask.Status) error {
	s.mu.Lock()
	fail := s.failNext
	if fail {
		s.failNext = false
	}
	s.closeCalls = append(s.closeCalls, status)
	s.mu.Unlock()
	if fail {
		return errors.New("transient close error")
	}
	return s.ProviderServices.CloseBackgroundTask(rowKey, status)
}

func TestGeminiChildTranscriptDoesNotRecloseSucceededOrFlushedChildren(t *testing.T) {
	t.Parallel()

	t.Run("succeeded child is removed and not reclosed on final flush", func(t *testing.T) {
		t.Parallel()
		transcript, sink, path := geminiChildTranscriptFixture(t)
		counting := &geminiCloseCountingServices{ProviderServices: agent.NewProviderServices(sink)}
		transcript.services = counting

		appendGeminiChildRecord(t, path, `{"id":"native-complete-model","type":"gemini","content":"","toolCalls":[{"id":"complete_task__child-end","name":"complete_task","args":{"result":{"response":"done"}},"status":"success","result":[{"functionResponse":{"response":{"output":"ok"}}}]}]}`)
		transcript.flush(false, agent.MessageCompletionComplete)
		transcript.flush(true, agent.MessageCompletionInterrupted)

		counting.mu.Lock()
		calls := append([]bgtask.Status(nil), counting.closeCalls...)
		counting.mu.Unlock()
		assert.Equal(t, []bgtask.Status{bgtask.StatusSucceeded}, calls, "a succeeded child must not be re-closed on a later final flush")
	})

	t.Run("flushed incomplete child is removed and not closed twice", func(t *testing.T) {
		t.Parallel()
		transcript, sink, _ := geminiChildTranscriptFixture(t)
		counting := &geminiCloseCountingServices{ProviderServices: agent.NewProviderServices(sink)}
		transcript.services = counting

		transcript.flush(true, agent.MessageCompletionInterrupted)
		transcript.flush(true, agent.MessageCompletionError)

		counting.mu.Lock()
		calls := append([]bgtask.Status(nil), counting.closeCalls...)
		counting.mu.Unlock()
		assert.Equal(t, []bgtask.Status{bgtask.StatusInterrupted}, calls, "a child closed in final flush must not be closed again")
	})
}

func TestGeminiNormalPromptEndKeepsRunningChildOpenAndRetriesTransientCloseError(t *testing.T) {
	t.Parallel()

	transcript, sink, _ := geminiChildTranscriptFixture(t)
	counting := &geminiCloseCountingServices{ProviderServices: agent.NewProviderServices(sink), failNext: true}
	transcript.services = counting
	a := &Agent{}
	a.children = transcript

	// Non-final poll on normal prompt end keeps running child open.
	a.pollNativeChildren()
	row, found := sink.BackgroundTask(geminiNativeChildID)
	require.True(t, found)
	assert.Equal(t, bgtask.StatusRunning, row.Status)

	// First final flush hits transient error, so child stays in transcript.children for retry.
	a.finishNativeChildren(agent.MessageCompletionInterrupted)
	row, found = sink.BackgroundTask(geminiNativeChildID)
	require.True(t, found)
	assert.Equal(t, bgtask.StatusRunning, row.Status)

	// Second final flush succeeds and closes the child once.
	a.finishNativeChildren(agent.MessageCompletionInterrupted)
	row, found = sink.BackgroundTask(geminiNativeChildID)
	require.True(t, found)
	assert.Equal(t, bgtask.StatusInterrupted, row.Status)
}

func TestGeminiIdleInterruptFinishesNativeChildren(t *testing.T) {
	t.Parallel()

	transcript, sink, _ := geminiChildTranscriptFixture(t)
	a, _ := acptest.NewAgentForRPC(t, func() *Agent { return &Agent{} }, func(a *Agent) *acp.Base { return &a.Base })
	services := agent.NewProviderServices(sink)
	a.SetSinkForTest(services)
	*a.HooksForTest() = a.configure(t.Context(), agent.Options{}, services)
	a.children = transcript

	a.pollNativeChildren()
	row, found := sink.BackgroundTask(geminiNativeChildID)
	require.True(t, found)
	require.Equal(t, bgtask.StatusRunning, row.Status)

	require.NoError(t, a.Interrupt(agent.StopContext{}))

	row, found = sink.BackgroundTask(geminiNativeChildID)
	require.True(t, found)
	assert.Equal(t, bgtask.StatusInterrupted, row.Status, "idle Interrupt must finish lingering Gemini native children")
}

type geminiConcurrentObserveServices struct {
	agent.ProviderServices
	once     sync.Once
	onUpsert func()
}

func (services *geminiConcurrentObserveServices) UpsertBackgroundTask(upsert bgtask.Upsert) error {
	if err := services.ProviderServices.UpsertBackgroundTask(upsert); err != nil {
		return err
	}
	services.once.Do(services.onUpsert)
	return nil
}

func TestGeminiChildTranscriptIdleStartupClosesHistoricalNonGoalRow(t *testing.T) {
	t.Parallel()
	transcript, sink, path := geminiChildTranscriptFixture(t)
	goalChildID := "22222222-2222-4222-8222-222222222222"
	goalPath := filepath.Join(filepath.Dir(path), goalChildID+".jsonl")
	_, hash := geminiProjectIdentity(transcript.query.WorkingDir)
	writeGeminiSession(t, goalPath, goalChildID, hash, "subagent", "completed goal child")
	appendGeminiChildRecord(t, goalPath, `{"id":"goal-complete-model","type":"gemini","content":"","toolCalls":[{"id":"complete_task__goal-end","name":"complete_task","args":{"result":{"response":"done"}},"status":"success","result":[{"functionResponse":{"response":{"output":"Output submitted and task completed."}}}]}]}`)

	transcript.start()
	defer transcript.stop(agent.MessageCompletionInterrupted)

	testutil.RequireEventually(t, func() bool {
		row, found := sink.BackgroundTask(geminiNativeChildID)
		return found && row.Status == bgtask.StatusEndedWithUnknownOutcome
	})
	goalRow, found := sink.BackgroundTask(goalChildID)
	require.True(t, found)
	assert.Equal(t, bgtask.StatusSucceeded, goalRow.Status)
}

func TestGeminiChildTranscriptConcurrentObserveDuringStartupKeepsChildRunning(t *testing.T) {
	t.Parallel()
	transcript, sink, _ := geminiChildTranscriptFixture(t)
	clock := transcript.clock.(*quartz.Mock)
	tickerTrap := clock.Trap().NewTicker("gemini", "child-transcript")
	defer tickerTrap.Close()

	transcript.services = &geminiConcurrentObserveServices{
		ProviderServices: agent.NewProviderServices(sink),
		onUpsert: func() {
			transcript.observe("root", json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"invoke_agent__concurrent","status":"in_progress"}`))
		},
	}

	transcript.start()
	defer transcript.stop(agent.MessageCompletionInterrupted)

	ctx := testutil.DeadlineContext(t)
	assert.Equal(t, geminiChildPollInterval, testutil.WaitForTimer(t, ctx, tickerTrap))
	row, found := sink.BackgroundTask(geminiNativeChildID)
	require.True(t, found)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
}

func TestGeminiChildTranscriptMidTurnStatusClosesAfterGraceTick(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name               string
		fastBeforeStart    bool
		finalFrameStatus   string
		appendCompleteTask bool
		wantStatus         bgtask.Status
	}{
		{
			name:             "slow completed without complete_task",
			finalFrameStatus: "completed",
			wantStatus:       bgtask.StatusSucceeded,
		},
		{
			name:               "late complete_task wins during grace wait",
			finalFrameStatus:   "failed",
			appendCompleteTask: true,
			wantStatus:         bgtask.StatusSucceeded,
		},
		{
			name:             "fast start and finish before start wakes",
			fastBeforeStart:  true,
			finalFrameStatus: "completed",
			wantStatus:       bgtask.StatusSucceeded,
		},
		{
			name:             "mid-turn failed",
			finalFrameStatus: "failed",
			wantStatus:       bgtask.StatusFailed,
		},
		{
			name:             "mid-turn cancelled",
			finalFrameStatus: "cancelled",
			wantStatus:       bgtask.StatusStopped,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			transcript, sink, path := geminiChildTranscriptFixture(t)
			clock := transcript.clock.(*quartz.Mock)
			tickerTrap := clock.Trap().NewTicker("gemini", "child-transcript")
			defer tickerTrap.Close()
			graceTrap := clock.Trap().NewTimer("gemini", "child-transcript-grace")
			defer graceTrap.Close()

			ctx := testutil.DeadlineContext(t)
			startFrame := json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"invoke_agent__midturn","status":"in_progress"}`)
			endFrame := json.RawMessage(`{"sessionUpdate":"tool_call_update","toolCallId":"invoke_agent__midturn","status":"` + tc.finalFrameStatus + `"}`)

			if tc.fastBeforeStart {
				transcript.observe("root", startFrame)
				transcript.observe("root", endFrame)
				transcript.start()
				defer transcript.stop(agent.MessageCompletionInterrupted)
			} else {
				transcript.observe("root", startFrame)
				transcript.start()
				defer transcript.stop(agent.MessageCompletionInterrupted)
				assert.Equal(t, geminiChildPollInterval, testutil.WaitForTimer(t, ctx, tickerTrap))
				transcript.observe("root", endFrame)
			}

			assert.Equal(t, geminiChildPollInterval, testutil.WaitForTimer(t, ctx, graceTrap))
			row, found := sink.BackgroundTask(geminiNativeChildID)
			require.True(t, found)
			assert.Equal(t, bgtask.StatusRunning, row.Status, "child must stay running during the grace tick")

			if tc.appendCompleteTask {
				appendGeminiChildRecord(t, path, `{"id":"late-complete-model","type":"gemini","content":"","toolCalls":[{"id":"complete_task__late-end","name":"complete_task","args":{"result":{"response":"late findings"}},"status":"success","result":[{"functionResponse":{"response":{"output":"Output submitted and task completed."}}}]}]}`)
			}
			clock.Advance(geminiChildPollInterval).MustWait(ctx)
			testutil.RequireEventually(t, func() bool {
				got, ok := sink.BackgroundTask(geminiNativeChildID)
				return ok && got.Status == tc.wantStatus
			})
		})
	}
}

func TestGeminiChildTranscriptNewCallDuringGraceCancelsPendingClose(t *testing.T) {
	t.Parallel()
	transcript, sink, _ := geminiChildTranscriptFixture(t)
	clock := transcript.clock.(*quartz.Mock)
	tickerTrap := clock.Trap().NewTicker("gemini", "child-transcript")
	defer tickerTrap.Close()
	graceTrap := clock.Trap().NewTimer("gemini", "child-transcript-grace")
	defer graceTrap.Close()

	ctx := testutil.DeadlineContext(t)
	transcript.observe("root", json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"invoke_agent__first","status":"in_progress"}`))
	transcript.start()
	defer transcript.stop(agent.MessageCompletionInterrupted)

	assert.Equal(t, geminiChildPollInterval, testutil.WaitForTimer(t, ctx, tickerTrap))
	transcript.observe("root", json.RawMessage(`{"sessionUpdate":"tool_call_update","toolCallId":"invoke_agent__first","status":"completed"}`))
	assert.Equal(t, geminiChildPollInterval, testutil.WaitForTimer(t, ctx, graceTrap))

	transcript.observe("root", json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"invoke_agent__second","status":"in_progress"}`))
	assert.Equal(t, geminiChildPollInterval, testutil.WaitForTimer(t, ctx, tickerTrap))
	row, found := sink.BackgroundTask(geminiNativeChildID)
	require.True(t, found)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
}

func TestGeminiFinishNativeChildrenClearsActiveCallsAndStopsTicker(t *testing.T) {
	t.Parallel()
	transcript, sink, _ := geminiChildTranscriptFixture(t)
	clock := transcript.clock.(*quartz.Mock)
	tickerTrap := clock.Trap().NewTicker("gemini", "child-transcript")
	defer tickerTrap.Close()

	a := &Agent{children: transcript}
	transcript.observe("root", json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"invoke_agent__cancelled","status":"in_progress"}`))
	transcript.start()
	defer transcript.stop(agent.MessageCompletionInterrupted)

	ctx := testutil.DeadlineContext(t)
	assert.Equal(t, geminiChildPollInterval, testutil.WaitForTimer(t, ctx, tickerTrap))

	a.finishNativeChildren(agent.MessageCompletionInterrupted)
	assert.False(t, transcript.active())
	row, found := sink.BackgroundTask(geminiNativeChildID)
	require.True(t, found)
	assert.Equal(t, bgtask.StatusInterrupted, row.Status)
	testutil.RequireEventually(t, func() bool {
		_, pending := clock.Peek()
		return !pending
	})
}
