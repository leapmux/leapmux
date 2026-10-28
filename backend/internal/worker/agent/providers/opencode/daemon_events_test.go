package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/quartz"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDaemonEventOfAnotherSessionIgnored(t *testing.T) {
	t.Parallel()
	family := &FamilyBase{}
	family.SetSessionIDForTest("session-1")
	family.configureQuestions(&agenttest.ControlSink{})
	var observed []string
	family.MemberEvents = func(kind string, properties json.RawMessage) {
		assert.Equal(t, "session.updated", kind)
		observed = append(observed, string(properties))
	}
	for _, sessionID := range []string{"other-session", "", "session-1"} {
		raw := []byte(fmt.Sprintf(`{"type":"session.updated","properties":{"sessionID":%q,"info":{"id":%q}}}`, sessionID, sessionID))
		family.Questions.handleEvent(t.Context(), raw)
	}
	assert.Equal(t, []string{`{"sessionID":"session-1","info":{"id":"session-1"}}`}, observed)
}

type storedMessagePeer struct {
	mu             sync.Mutex
	body           string
	status         int
	paths          []string
	beforeResponse func(context.Context)
}

func (p *storedMessagePeer) reply(body string, status int) {
	p.mu.Lock()
	p.body, p.status = body, status
	p.mu.Unlock()
}

func newDaemonGenerationAgent(t *testing.T) (*Agent, *agenttest.Sink, *storedMessagePeer) {
	t.Helper()
	sink := &agenttest.Sink{}
	a := newOpenCodeAgentWithSink(agent.NewProviderServices(sink))
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{
		AgentID: "test-agent", ProviderName: "opencode", Ctx: t.Context(), Clock: testutil.NewQuartzMock(t),
	})
	*a.HooksForTest() = FamilyHooks(&a.FamilyBase)
	a.configureQuestions(a.Sink())
	peer := &storedMessagePeer{status: http.StatusOK}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer.mu.Lock()
		body, status, before := peer.body, peer.status, peer.beforeResponse
		peer.paths = append(peer.paths, r.URL.EscapedPath())
		peer.mu.Unlock()
		assert.Equal(t, http.MethodGet, r.Method)
		if before != nil {
			before(r.Context())
		}
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	a.Questions.baseURL = server.URL
	a.SetPromptActiveForTest(true)
	return a, sink, peer
}

func feedDaemonEvent(t *testing.T, a *Agent, kind string, fields map[string]any) {
	t.Helper()
	properties := map[string]any{"sessionID": "test-session"}
	for key, value := range fields {
		properties[key] = value
	}
	raw, err := json.Marshal(map[string]any{"type": kind, "properties": properties})
	require.NoError(t, err)
	a.Questions.handleEvent(t.Context(), raw)
}

func feedDaemonChunk(t *testing.T, a *Agent, kind, messageID, text string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{
		"sessionId": "test-session", "update": map[string]any{"sessionUpdate": kind, "messageId": messageID, "content": map[string]any{"type": "text", "text": text}},
	}})
	require.NoError(t, err)
	a.HandleOutput(raw)
}

func daemonGenerationRows(sink *agenttest.Sink) []string {
	var rows []string
	for _, message := range sink.Messages() {
		if kind, text, _, ok := agenttest.DecodeAssembledMessage(message.Content); ok {
			rows = append(rows, string(kind)+":"+text)
		}
	}
	return rows
}

func TestOpenCodeSummaryStaysOutOfTheTranscript(t *testing.T) {
	t.Parallel()
	for _, metadataFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(metadataFirst), func(t *testing.T) {
			a, sink, _ := newDaemonGenerationAgent(t)
			// Trimmed from opencode/logs/compact-1.log. The ACP chunk retains the native message ID.
			observe := func() {
				feedDaemonEvent(t, a, "message.updated", map[string]any{"info": map[string]any{"id": "summary", "role": "assistant", "summary": true, "mode": "compaction"}})
			}
			if metadataFirst {
				observe()
			}
			feedDaemonChunk(t, a, "agent_message_chunk", "summary", "SUMMARY-MARKER ")
			if !metadataFirst {
				observe()
			}
			feedDaemonChunk(t, a, "agent_message_chunk", "summary", "the summary text.")
			a.FinishPromptRequestForTest("test-session", json.RawMessage(`{"stopReason":"end_turn"}`), nil)
			assert.Empty(t, daemonGenerationRows(sink))
		})
	}
}

func markDaemonRetry(t *testing.T, a *Agent) {
	t.Helper()
	feedDaemonEvent(t, a, "message.updated", map[string]any{"info": map[string]any{"id": "message-1", "role": "assistant"}})
	feedDaemonEvent(t, a, "session.status", map[string]any{"status": map[string]any{"type": "retry", "attempt": 1}})
}

func TestOpenCodeRetryDiscardUsesStoredParts(t *testing.T) {
	t.Parallel()
	a, sink, peer := newDaemonGenerationAgent(t)
	peer.reply(`{"info":{"id":"message-1","sessionID":"test-session"},"parts":[{"id":"failed","type":"text","text":""},{"id":"kept","type":"text","text":"ANSWER-AFTER-RETRY"}]}`, http.StatusOK)
	// The daemon retry status precedes the failed stdout chunk in r2-mockmid-retryable-1.log.
	markDaemonRetry(t, a)
	feedDaemonChunk(t, a, "agent_message_chunk", "message-1", "partial ")
	feedDaemonChunk(t, a, "agent_message_chunk", "message-1", "ANSWER-AFTER-RETRY")
	a.FinishPromptRequestForTest("test-session", json.RawMessage(`{"stopReason":"end_turn"}`), nil)
	assert.Equal(t, []string{"text:ANSWER-AFTER-RETRY"}, daemonGenerationRows(sink))
	peer.mu.Lock()
	defer peer.mu.Unlock()
	assert.Equal(t, []string{"/session/test-session/message/message-1"}, peer.paths)
}

func TestOpenCodeRetryDiscardReasoningByPartID(t *testing.T) {
	t.Parallel()
	a, sink, peer := newDaemonGenerationAgent(t)
	peer.reply(`{"info":{"id":"message-1","sessionID":"test-session"},"parts":[{"id":"failed","type":"reasoning","text":""},{"id":"kept","type":"reasoning","text":"clean thought"}]}`, http.StatusOK)
	markDaemonRetry(t, a)
	for _, part := range []string{"failed", "kept"} {
		feedDaemonEvent(t, a, "message.part.updated", map[string]any{"part": map[string]any{"id": part, "messageID": "message-1", "type": "reasoning"}})
	}
	feedDaemonChunk(t, a, "agent_thought_chunk", "failed", "failed thought")
	feedDaemonChunk(t, a, "agent_thought_chunk", "kept", "clean thought")
	a.FlushBufferedOutput(agent.MessageCompletionComplete)
	assert.Equal(t, []string{"reasoning:clean thought"}, daemonGenerationRows(sink))
}

func TestOpenCodeRetryCursorEmitsEachPartOnce(t *testing.T) {
	t.Parallel()
	a, sink, peer := newDaemonGenerationAgent(t)
	markDaemonRetry(t, a)
	peer.reply(`{"info":{"id":"message-1","sessionID":"test-session"},"parts":[{"id":"kept","type":"text","text":"first"}]}`, http.StatusOK)
	feedDaemonChunk(t, a, "agent_message_chunk", "message-1", "partial first")
	a.PrepareSteer()
	peer.reply(`{"info":{"id":"message-1","sessionID":"test-session"},"parts":[{"id":"kept","type":"text","text":"first second"}]}`, http.StatusOK)
	feedDaemonChunk(t, a, "agent_message_chunk", "message-1", " second")
	a.FlushBufferedOutput(agent.MessageCompletionComplete)
	assert.Equal(t, []string{"text:first", "text: second"}, daemonGenerationRows(sink))
}

func TestOpenCodeRetryGETFailureKeepsTheBuffer(t *testing.T) {
	t.Parallel()
	a, sink, peer := newDaemonGenerationAgent(t)
	markDaemonRetry(t, a)
	peer.reply(`unavailable`, http.StatusServiceUnavailable)
	feedDaemonChunk(t, a, "agent_message_chunk", "message-1", "first")
	a.PrepareSteer()
	peer.reply(`{"info":{"id":"message-1","sessionID":"test-session"},"parts":[{"id":"kept","type":"text","text":"first second"}]}`, http.StatusOK)
	feedDaemonChunk(t, a, "agent_message_chunk", "message-1", " second")
	a.FlushBufferedOutput(agent.MessageCompletionComplete)
	assert.Equal(t, []string{"text:first", "text: second"}, daemonGenerationRows(sink))
	peer.mu.Lock()
	defer peer.mu.Unlock()
	assert.Len(t, peer.paths, 1, "once streamed text persists, a later full snapshot must not repeat it")
}

func TestOpenCodeRetryLookupKeepsTheFinalRowAndCleanupBehindItsText(t *testing.T) {
	t.Parallel()
	a, sink, peer := newDaemonGenerationAgent(t)
	peer.reply(`{"info":{"id":"message-1","sessionID":"test-session"},"parts":[{"id":"kept","type":"text","text":"clean"}]}`, http.StatusOK)
	lookupStarted, releaseLookup := make(chan struct{}), make(chan struct{})
	var startOnce sync.Once
	resumeLookup := sync.OnceFunc(func() { close(releaseLookup) })
	peer.beforeResponse = func(ctx context.Context) {
		startOnce.Do(func() { close(lookupStarted) })
		select {
		case <-releaseLookup:
		case <-ctx.Done():
		}
	}
	t.Cleanup(resumeLookup)
	promptEnding, cleanupStarted := make(chan struct{}), make(chan struct{})
	releaseCleanup := make(chan struct{})
	resumeCleanup := sync.OnceFunc(func() { close(releaseCleanup) })
	t.Cleanup(resumeCleanup)
	hooks := a.HooksForTest()
	promptEnded := hooks.PromptEnded
	hooks.PromptEnded = func(err error, stopped bool) acp.PromptEnding {
		ending := promptEnded(err, stopped)
		close(promptEnding)
		return ending
	}
	captureCleanup := hooks.CaptureTurnOutputCleanup
	hooks.CaptureTurnOutputCleanup = func() func() {
		cleanup := captureCleanup()
		return func() {
			// The returned effect runs after ACP releases its update and turn locks.
			close(cleanupStarted)
			<-releaseCleanup
			cleanup()
		}
	}
	markDaemonRetry(t, a)
	feedDaemonChunk(t, a, "agent_message_chunk", "message-1", "partial clean")
	textDone := make(chan struct{})
	go func() { a.PrepareSteer(); close(textDone) }()
	guard := testutil.DeadlineContext(t)
	select {
	case <-lookupStarted:
	case <-guard.Done():
		t.Fatal("the stored-message read did not start")
	}
	// PrepareSteer owns the session-update lock until its read finishes.
	// The prompt callback waits for that lock, so the test must not wait for
	// the callback before releasing the stored-message response.
	promptDone := make(chan struct{})
	go func() {
		a.FinishPromptRequestForTest("test-session", json.RawMessage(`{"stopReason":"end_turn"}`), nil)
		close(promptDone)
	}()
	select {
	case <-promptEnding:
	case <-guard.Done():
		t.Fatal("the prompt callback did not start")
	}
	assert.Empty(t, sink.Messages(), "the turn end must wait for the selected text")
	resumeLookup()
	select {
	case <-textDone:
	case <-guard.Done():
		t.Fatal("the selected text did not finish")
	}
	select {
	case <-cleanupStarted:
	case <-guard.Done():
		t.Fatal("the cleanup effect did not start after the final row")
	}
	require.Len(t, sink.Messages(), 2)
	assert.Equal(t, []string{"text:clean"}, daemonGenerationRows(sink))
	assert.True(t, sink.Messages()[1].TurnEnd)
	a.daemonMu.Lock()
	oldBeforeCleanup := a.daemonMessages["message-1"]
	a.daemonMu.Unlock()
	assert.NotNil(t, oldBeforeCleanup, "the captured cleanup must still wait behind the final row")
	feedDaemonEvent(t, a, "message.updated", map[string]any{"info": map[string]any{"id": "next-summary", "role": "assistant", "summary": true}})
	a.daemonMu.Lock()
	nextSummary := a.daemonMessages["next-summary"]
	a.daemonMu.Unlock()
	require.NotNil(t, nextSummary)
	resumeCleanup()
	select {
	case <-promptDone:
	case <-guard.Done():
		t.Fatal("the prompt callback did not complete its captured cleanup")
	}
	a.daemonMu.Lock()
	oldAfterCleanup := a.daemonMessages["message-1"]
	nextAfterCleanup := a.daemonMessages["next-summary"]
	a.daemonMu.Unlock()
	assert.Nil(t, oldAfterCleanup, "the cleanup must remove its captured old message")
	assert.Same(t, nextSummary, nextAfterCleanup, "the cleanup must retain the later message's exact identity")
	feedDaemonChunk(t, a, "agent_message_chunk", "next-summary", "keep this summary hidden")
	a.FlushBufferedOutput(agent.MessageCompletionComplete)
	assert.Equal(t, []string{"text:clean"}, daemonGenerationRows(sink), "the old cleanup must retain later message identities")
}

func TestOpenCodeRetryStoredMessageValidation(t *testing.T) {
	t.Parallel()
	for _, body := range []string{
		`invalid`,
		`{"info":{"id":"other","sessionID":"test-session"},"parts":[]}`,
		`{"info":{"id":"message-1","sessionID":"other"},"parts":[]}`,
		`{"info":{"id":"message-1","sessionID":"test-session"}}`,
		`{"info":{"id":"message-1","sessionID":"test-session"},"parts":null}`,
		`{"info":{"id":"message-1","sessionID":"test-session"},"parts":[{"id":"p","type":"text","text":7}]}`,
		`{"info":{"id":"message-1","sessionID":"test-session"},"parts":[{"type":"text","text":"unidentified"}]}`,
		`{"info":{"id":"message-1","sessionID":"test-session"},"parts":[{"id":"p","type":"text","text":"one"},{"id":"p","type":"text","text":"two"}]}`,
	} {
		a, sink, peer := newDaemonGenerationAgent(t)
		markDaemonRetry(t, a)
		peer.reply(body, http.StatusOK)
		feedDaemonChunk(t, a, "agent_message_chunk", "message-1", "retain this text")
		a.FlushBufferedOutput(agent.MessageCompletionComplete)
		assert.Equal(t, []string{"text:retain this text"}, daemonGenerationRows(sink), body)
	}
}

func TestOpenCodeRetryEmptyStoredPartsDiscardTheAttempt(t *testing.T) {
	t.Parallel()
	a, sink, peer := newDaemonGenerationAgent(t)
	markDaemonRetry(t, a)
	peer.reply(`{"info":{"id":"message-1","sessionID":"test-session"},"parts":[]}`, http.StatusOK)
	feedDaemonChunk(t, a, "agent_message_chunk", "message-1", "discard this text")
	a.FlushBufferedOutput(agent.MessageCompletionComplete)
	assert.Empty(t, daemonGenerationRows(sink))
}

func TestOpenCodeStoredMessageReadUsesTheInjectedDeadline(t *testing.T) {
	t.Parallel()
	a, _, peer := newDaemonGenerationAgent(t)
	clock := a.Clock().(*quartz.Mock)
	trap := clock.Trap().AfterFunc(StoredMessageReadTimerTag)
	t.Cleanup(trap.Close)
	started := make(chan struct{})
	peer.beforeResponse = func(ctx context.Context) { close(started); <-ctx.Done() }
	done := make(chan error, 1)
	go func() {
		_, err := a.Questions.readStoredMessage(a.Context(), clock, "test-session", "message-1")
		done <- err
	}()
	guard := testutil.DeadlineContext(t)
	assert.Equal(t, 5*time.Second, testutil.WaitForTimer(t, guard, trap))
	select {
	case <-started:
	case <-guard.Done():
		t.Fatal("the stored-message read did not start")
	}
	clock.Advance(5 * time.Second).MustWait(guard)
	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.DeadlineExceeded)
	case <-guard.Done():
		t.Fatal("the stored-message read did not stop at its deadline")
	}
}

func TestOpenCodeCleanupKeepsANewTrackerWithTheSameMessageID(t *testing.T) {
	t.Parallel()
	a, sink, _ := newDaemonGenerationAgent(t)
	markDaemonRetry(t, a)
	cleanup := a.captureDaemonCleanup()
	a.HooksForTest().PromptPrepared()
	feedDaemonEvent(t, a, "message.updated", map[string]any{"info": map[string]any{"id": "message-1", "role": "assistant", "summary": true}})
	cleanup()
	feedDaemonChunk(t, a, "agent_message_chunk", "message-1", "new summary")
	a.FlushBufferedOutput(agent.MessageCompletionComplete)
	assert.Empty(t, daemonGenerationRows(sink))
}

func TestOpenCodeCompactionRow(t *testing.T) {
	t.Parallel()
	a, sink, _ := newDaemonGenerationAgent(t)
	feedDaemonChunk(t, a, "agent_message_chunk", "answer", "before")
	for range 2 {
		feedDaemonEvent(t, a, "message.updated", map[string]any{"info": map[string]any{"id": "summary", "role": "assistant", "summary": true}})
	}
	assert.Empty(t, sink.PersistedNotifications())
	assert.Equal(t, []interface{}{map[string]interface{}{}}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
	feedDaemonEvent(t, a, "session.compacted", nil)
	require.Len(t, sink.PersistedNotifications(), 1)
	notice := sink.PersistedNotifications()[0]
	assert.JSONEq(t, `{"type":"session.compacted","properties":{"sessionID":"test-session"}}`, string(notice.Content))
	assert.Equal(t, agent.CompactionBoundaryNotification(), Registration().Plugin.Classify(notice.Content))
	assert.Equal(t, []string{"text:before"}, daemonGenerationRows(sink))
	assert.Equal(t, []interface{}{map[string]interface{}{}, nil}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
}

func TestOpenCodeCompactionOfAnotherSessionChangesNothing(t *testing.T) {
	t.Parallel()
	a, sink, _ := newDaemonGenerationAgent(t)
	feedDaemonEvent(t, a, "message.updated", map[string]any{"sessionID": "other", "info": map[string]any{"id": "summary", "role": "assistant", "summary": true}})
	feedDaemonEvent(t, a, "session.compacted", map[string]any{"sessionID": "other"})
	assert.Empty(t, sink.PersistedNotifications())
	assert.Empty(t, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
}

func TestOpenCodeCompletedSummaryDoesNotStartLiveCompaction(t *testing.T) {
	t.Parallel()
	a, sink, _ := newDaemonGenerationAgent(t)
	feedDaemonEvent(t, a, "message.updated", map[string]any{"info": map[string]any{
		"id": "summary", "role": "assistant", "summary": true, "time": map[string]any{"completed": 1},
	}})
	assert.Empty(t, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
}

func TestOpenCodeIdleEndsLiveCompaction(t *testing.T) {
	t.Parallel()
	a, sink, _ := newDaemonGenerationAgent(t)
	providerkit.PublishCompaction(a.Sink(), "")
	feedDaemonEvent(t, a, "session.status", map[string]any{"status": map[string]any{"type": "idle"}})
	value, found := sink.LastSessionInfoValue(contracts.SessionInfoKeyCompaction)
	require.True(t, found)
	assert.Nil(t, value)
}

func TestOpenCodeFailedPromptEndsLiveCompaction(t *testing.T) {
	t.Parallel()
	for _, stopped := range []bool{false, true} {
		t.Run(fmt.Sprint(stopped), func(t *testing.T) {
			a, sink, _ := newDaemonGenerationAgent(t)
			providerkit.PublishCompaction(a.Sink(), "")
			a.SetStoppedForTest(stopped)
			a.FinishPromptRequestForTest("test-session", nil, assert.AnError)
			value, found := sink.LastSessionInfoValue(contracts.SessionInfoKeyCompaction)
			require.True(t, found)
			assert.Nil(t, value)
		})
	}
}

func TestOpenCodeRetryRowFolds(t *testing.T) {
	t.Parallel()
	a, sink, _ := newDaemonGenerationAgent(t)
	// Trimmed from opencode/logs/rate-1.log, native attempts 1 and 2.
	for _, attempt := range []int{1, 2} {
		feedDaemonEvent(t, a, "session.status", map[string]any{"status": map[string]any{
			"type": "retry", "attempt": attempt, "message": "probe stub: rate limit exceeded",
		}})
	}
	notices := sink.PersistedNotifications()
	require.Len(t, notices, 2)
	for _, notice := range notices {
		assert.Equal(t, agent.APIRetryNotification(), Registration().Plugin.Classify(notice.Content))
	}
	assert.Empty(t, sink.LeapMuxNotifications())
	for range 2 {
		feedDaemonEvent(t, a, "message.part.delta", map[string]any{"messageID": "answer", "partID": "part", "field": "text", "delta": "answer"})
	}
	assert.Equal(t, []map[string]interface{}{{contracts.NotificationFieldType: contracts.NotificationTypeApiRetryResolved}}, sink.LeapMuxNotifications())
}

func TestOpenCodeRetryRowResetsAtIdleAndPromptFailure(t *testing.T) {
	t.Parallel()
	for _, end := range []string{"idle", "failure", "new-prompt", "clear"} {
		t.Run(end, func(t *testing.T) {
			a, sink, _ := newDaemonGenerationAgent(t)
			feedDaemonEvent(t, a, "session.status", map[string]any{"status": map[string]any{"type": "retry", "attempt": 1}})
			require.Len(t, sink.PersistedNotifications(), 1)
			switch end {
			case "idle":
				feedDaemonEvent(t, a, "session.status", map[string]any{"status": map[string]any{"type": "idle"}})
			case "failure":
				a.HooksForTest().PromptEnded(assert.AnError, false)
			case "new-prompt":
				a.HooksForTest().PromptPrepared()
			case "clear":
				a.HooksForTest().ClearProviderState()
			}
			feedDaemonEvent(t, a, "message.part.delta", map[string]any{"messageID": "answer", "partID": "part", "field": "text", "delta": "answer"})
			assert.Empty(t, sink.LeapMuxNotifications())
		})
	}
}

func TestOpenCodeRetryRowIgnoresForeignAndEmptyOutput(t *testing.T) {
	t.Parallel()
	a, sink, _ := newDaemonGenerationAgent(t)
	feedDaemonEvent(t, a, "session.status", map[string]any{"sessionID": "other", "status": map[string]any{"type": "retry"}})
	assert.Empty(t, sink.PersistedNotifications())
	feedDaemonEvent(t, a, "session.status", map[string]any{"status": map[string]any{"type": "retry"}})
	require.Len(t, sink.PersistedNotifications(), 1)
	for _, fields := range []map[string]any{
		{"sessionID": "other", "messageID": "answer", "partID": "part", "field": "text", "delta": "answer"},
		{"messageID": "answer", "partID": "part", "field": "text", "delta": ""},
		{"messageID": "answer", "partID": "part", "field": "other", "delta": "value"},
	} {
		feedDaemonEvent(t, a, "message.part.delta", fields)
	}
	assert.Empty(t, sink.LeapMuxNotifications())
}
