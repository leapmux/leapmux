package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodexControlPublicationFailureReturnsProtocolError(t *testing.T) {
	t.Parallel()
	output := &agenttest.Stdin{}
	sink := &agenttest.ControlSink{PublicationError: errors.New("storage unavailable")}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	a.SetStdinForTest(agenttest.NopStdin(output))
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","id":37,"method":"item/tool/requestUserInput","params":{"threadId":"main-thread","questions":[]}}`)))
	// handleCodexOutput runs on the goroutine that drains Codex's stdout, so the
	// failure reply is QUEUED rather than written before it returns.
	var answer string
	require.Eventually(t, func() bool {
		answer = output.String()
		return answer != ""
	}, 2*time.Second, 5*time.Millisecond, "the publication failure is answered")
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":37,"error":{"code":-32603,"message":"LeapMux could not store this control request."}}`, answer)
	assert.Empty(t, sink.PublishedControls())
}

type notificationPersistGuardSink struct {
	agenttest.Sink
	t *testing.T
}

type transientCodexCloseFailureSink struct {
	*agenttest.Sink
	closeFailures int
	closeAttempts int
}

type transientCodexEnsureFailureSink struct {
	*agenttest.Sink
	ensureFailures int
}

type blockingCodexEnsureSink struct {
	*agenttest.Sink
	started chan struct{}
	release chan struct{}
}

type codexEnsureCall struct {
	spawnSpanID      string
	providerChildKey string
	title            string
}

type recordingCodexEnsureSink struct {
	*agenttest.Sink
	ensureCalls    []codexEnsureCall
	childSinkCalls int
}

func (s *recordingCodexEnsureSink) EnsureChildAgent(spawnSpanID, providerChildKey, title string) (string, error) {
	s.ensureCalls = append(s.ensureCalls, codexEnsureCall{
		spawnSpanID:      spawnSpanID,
		providerChildKey: providerChildKey,
		title:            title,
	})
	return s.Sink.EnsureChildAgent(spawnSpanID, providerChildKey, title)
}

func (s *recordingCodexEnsureSink) ChildSink(childAgentID string) agent.ProviderServices {
	s.childSinkCalls++
	return s.Sink.ChildSink(childAgentID)
}

func (s *transientCodexCloseFailureSink) CloseBackgroundTask(rowKey string, status bgtask.Status) error {
	s.closeAttempts++
	if s.closeFailures > 0 {
		s.closeFailures--
		return fmt.Errorf("transient registry close failure")
	}
	return s.Sink.CloseBackgroundTask(rowKey, status)
}

func (s *transientCodexEnsureFailureSink) EnsureChildAgent(spawnSpanID, providerChildKey, title string) (string, error) {
	if s.ensureFailures > 0 {
		s.ensureFailures--
		return "", fmt.Errorf("transient child creation failure")
	}
	return s.Sink.EnsureChildAgent(spawnSpanID, providerChildKey, title)
}

func (s *blockingCodexEnsureSink) EnsureChildAgent(spawnSpanID, providerChildKey, title string) (string, error) {
	close(s.started)
	<-s.release
	return s.Sink.EnsureChildAgent(spawnSpanID, providerChildKey, title)
}

func (s *notificationPersistGuardSink) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	s.t.Fatalf("notification must not be persisted as a regular message: source=%v content=%s", source, string(content.Original))
	return nil
}

func newCodexAgentWithSink(sink agent.ProviderServices) *Agent {
	a := &Agent{
		JSONRPCProcess: providerkit.JSONRPCProcess{Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{
			AgentID: "test-agent",
		})},
		sink:     sink,
		threadID: "main-thread",
	}
	a.sink = agent.NewModelProgressResetSink(a.sink)
	return a
}

func TestHandleCodexOutput_TurnStartedOpensTheTurn(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))

	input := `{"jsonrpc":"2.0","method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"turn-42"}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

	statusActiveCount := sink.StatusActiveCount()
	sessionInfoCount := sink.SessionInfoCount()
	agent.Mu.Lock()
	turnID := agent.turnID
	agent.Mu.Unlock()
	assert.Equal(t, 0, statusActiveCount, "turn/started must NOT re-broadcast full status")
	assert.Equal(t, "turn-42", turnID, "interrupts and steering target this turn")
	assert.Equal(t, []bool{true}, sink.TurnActives(),
		"turn/started opens the Worker's activity state AND its input queue's turn")
	// The turn id used to ride an ephemeral session-info frame as well, for a
	// browser-side working-state heuristic that no longer exists. Nothing reads
	// it now, so nothing sends it.
	assert.Equal(t, 0, sessionInfoCount, "turn/started broadcasts no session info")
}

func TestHandleCodexOutput_TurnStartedFallbackIsNoop(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))

	// turn/started with no turn.id has no per-turn state to broadcast;
	// git status now refreshes at turn-end via the sink layer.
	input := `{"jsonrpc":"2.0","method":"turn/started","params":{"threadId":"t1"}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

	statusActiveCount := sink.StatusActiveCount()
	sessionInfoCount := sink.SessionInfoCount()
	assert.Equal(t, 0, statusActiveCount, "turn/started fallback must NOT re-broadcast full status")
	assert.Equal(t, 0, sessionInfoCount, "turn/started fallback broadcasts no session info")
}

func TestHandleCodexOutput_RequestUserInput(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))

	input := `{"jsonrpc":"2.0","id":42,"method":"item/tool/requestUserInput","params":{"threadId":"t1","turnId":"turn1","itemId":"item1","questions":[{"id":"q1","header":"Header","question":"Which option?","options":[{"label":"A"}]}]}}`

	handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

	require.Equal(t, 1, sink.PublishedControlCount())

	rec := sink.LastPublishedControl()
	assert.Equal(t, "jsonrpc:42", rec.RequestID)

	// Verify payload is the original content.
	var parsed struct {
		Method string `json:"method"`
		ID     int    `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rec.Payload, &parsed))
	assert.Equal(t, "item/tool/requestUserInput", parsed.Method)
	assert.Equal(t, 42, parsed.ID)

	// Should NOT be persisted as a regular message.
	assert.Equal(t, 0, sink.MessageCount())
}

func TestHandleCodexOutput_CommandExecutionApproval(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))

	input := `{"jsonrpc":"2.0","id":7,"method":"item/commandExecution/requestApproval","params":{"command":"rm -rf /","reason":"cleanup"}}`

	handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

	require.Equal(t, 1, sink.PublishedControlCount())

	rec := sink.LastPublishedControl()
	assert.Equal(t, "jsonrpc:7", rec.RequestID)
}

func TestHandleCodexOutput_FileChangeApproval(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))

	input := `{"jsonrpc":"2.0","id":8,"method":"item/fileChange/requestApproval","params":{"reason":"editing file"}}`

	handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

	require.Equal(t, 1, sink.PublishedControlCount())

	rec := sink.LastPublishedControl()
	assert.Equal(t, "jsonrpc:8", rec.RequestID)
}

func TestHandleCodexOutput_PermissionsApproval(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))

	input := `{"jsonrpc":"2.0","id":9,"method":"item/permissions/requestApproval","params":{"reason":"needs access"}}`

	handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

	require.Equal(t, 1, sink.PublishedControlCount())

	rec := sink.LastPublishedControl()
	assert.Equal(t, "jsonrpc:9", rec.RequestID)
}

func TestHandleCodexOutput_ContextCompactionStartPersistsRawAsAgent(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))

	input := `{"method":"item/started","params":{"item":{"type":"contextCompaction","id":"compact-1"},"threadId":"main-thread","turnId":"turn1"}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

	require.Equal(t, 1, sink.NotificationCount())
	require.Equal(t, 0, sink.MessageCount())
	last := sink.LastNotification()
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, last.Source,
		"contextCompaction must persist as AGENT (Codex-emitted, not LeapMux-synthesized)")
	assert.JSONEq(t, input, string(last.Content),
		"raw JSON-RPC envelope must be preserved verbatim — synthesized {type:\"compacting\"} discarded item.id and threadId")
}

func TestHandleCodexOutput_McpStartupNonFailuresDoNotReachTheTranscript(t *testing.T) {
	t.Parallel()

	for _, status := range []string{`"starting"`, `"ready"`, `"cancelled"`, `{"state":"ready"}`} {
		sink := &notificationPersistGuardSink{t: t}
		agent := newCodexAgentWithSink(agent.NewProviderServices(sink))

		input := fmt.Sprintf(`{"method":"mcpServer/startupStatus/updated","params":{"name":"codex_apps","status":%s}}`, status)
		handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

		assert.Zero(t, sink.NotificationCount(), status)
		assert.Zero(t, sink.MessageCount(), status)
	}
}

func TestHandleCodexOutput_McpStartupFailuresPersistAsAgent(t *testing.T) {
	t.Parallel()

	for _, status := range []string{`"failed"`, `"futureFailure"`, `{"state":"failed","error":"nested failure"}`} {
		sink := &notificationPersistGuardSink{t: t}
		agent := newCodexAgentWithSink(agent.NewProviderServices(sink))

		input := fmt.Sprintf(`{"method":"mcpServer/startupStatus/updated","params":{"name":"codex_apps","status":%s,"error":"startup failed"}}`, status)
		handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

		require.Equal(t, 1, sink.NotificationCount(), status)
		require.Zero(t, sink.MessageCount(), status)
		last := sink.LastNotification()
		assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, last.Source)
		assert.JSONEq(t, input, string(last.Content), "the raw failure envelope stays available")
	}
}

func TestHandleCodexOutput_McpProgressDoesNotReachTheTranscript(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))

	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/mcpToolCall/progress","params":{"threadId":"main-thread","turnId":"turn-1","itemId":"mcp-1","message":"Working"}}`)))

	assert.Zero(t, sink.NotificationCount())
	assert.Zero(t, sink.MessageCount())
}

func TestHandleCodexOutput_McpOauthSuccessDoesNotReachTheTranscript(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))

	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"mcpServer/oauthLogin/completed","params":{"name":"docs","threadId":"main-thread","success":true}}`)))

	assert.Zero(t, sink.NotificationCount())
	assert.Zero(t, sink.MessageCount())
}

func TestHandleCodexOutput_McpOauthFailurePersistsAsAgent(t *testing.T) {
	t.Parallel()

	sink := &notificationPersistGuardSink{t: t}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	input := `{"method":"mcpServer/oauthLogin/completed","params":{"name":"docs","threadId":"main-thread","success":false,"error":"authorization failed"}}`

	handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

	require.Equal(t, 1, sink.NotificationCount())
	require.Zero(t, sink.MessageCount())
	assert.JSONEq(t, input, string(sink.LastNotification().Content))
}

func TestHandleCodexOutput_SubagentFailuresRouteToTheChildTranscript(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		input string
	}{
		{
			name:  "MCP OAuth",
			input: `{"method":"mcpServer/oauthLogin/completed","params":{"name":"docs","threadId":"child-1","success":false,"error":"authorization failed"}}`,
		},
		{
			name:  "MCP startup",
			input: `{"method":"mcpServer/startupStatus/updated","params":{"threadId":"child-1","name":"docs","status":"failed","error":"startup failed"}}`,
		},
		{
			name:  "hook",
			input: `{"method":"hook/completed","params":{"threadId":"child-1","turnId":"child-turn","run":{"id":"hook-1","eventName":"preToolUse","handlerType":"command","sourcePath":"/hooks/check.sh","status":"failed","statusMessage":"hook failed","entries":[{"kind":"error","text":"permission denied"}]}}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &agenttest.Sink{}
			agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
			handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"collabAgentToolCall","id":"spawn-1","tool":"spawnAgent","status":"inProgress","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":"inspect","agentsStates":{}}}}`)))
			parentCount := sink.MessageCount()

			handleCodexOutput(agent, providerkit.ParseLine([]byte(tc.input)))

			assert.Equal(t, parentCount, sink.MessageCount(), "the parent transcript must not receive the child failure")
			assert.Zero(t, sink.NotificationCount(), "the parent notification thread must stay unchanged")
			child := sink.Child("child-of-spawn-1")
			require.Equal(t, 1, child.NotificationCount())
			assert.JSONEq(t, tc.input, string(child.LastNotification().Content))
		})
	}
}

func TestHandleCodexOutput_SubagentFailureWaitsForItsRoute(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	failure := `{"method":"hook/completed","params":{"threadId":"child-1","turnId":"child-turn","run":{"id":"hook-1","status":"blocked","statusMessage":"policy blocked the hook"}}}`

	handleCodexOutput(agent, providerkit.ParseLine([]byte(failure)))
	assert.Zero(t, sink.MessageCount())
	assert.Zero(t, sink.NotificationCount())

	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"spawn-1","kind":"started","agentThreadId":"child-1","agentPath":"/root/reviewer"}}}`)))

	child := sink.Child("child-of-spawn-1")
	require.Equal(t, 1, child.NotificationCount())
	assert.JSONEq(t, failure, string(child.LastNotification().Content))
}

func TestHandleCodexOutput_MultiAgentV2PersistsPromptAndMirrorsFinalReport(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))

	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"rawResponseItem/completed","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"function_call","name":"spawn_agent","namespace":"collaboration","arguments":"{\"message\":\"Inspect the parser.\",\"task_name\":\"parser-review\"}","call_id":"spawn-1"}}}`)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"spawn-1","kind":"started","agentThreadId":"child-1","agentPath":"/root/parser-review"}}}`)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"child-1","turnId":"child-turn","item":{"type":"agentMessage","id":"report-1","text":"**Parser report**\n\n- Finding","phase":null}}}`)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"child-1","turn":{"id":"child-turn","status":"completed","items":[],"error":null}}}`)))

	child := sink.Child("child-of-spawn-1")
	childMessages := child.Messages()
	require.Len(t, childMessages, 3, "the prompt must precede the native final report and turn end")
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, childMessages[0].Source)
	assert.JSONEq(t, `{"content":"Inspect the parser."}`, string(childMessages[0].Content))
	assert.Contains(t, string(childMessages[1].Content), "Parser report")

	reports := sink.LeapMuxNotifications()
	require.Len(t, reports, 1, "the direct parent receives one report copy")
	assert.Equal(t, "subagent_report", reports[0]["type"])
	assert.Equal(t, "**Parser report**\n\n- Finding", reports[0]["text"])
	assert.Equal(t, "parser-review", reports[0]["label"])
}

func TestHandleCodexOutput_V1ChildMirrorsFinalReportWithoutAnAgentPath(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"collabAgentToolCall","id":"spawn-1","tool":"spawnAgent","status":"inProgress","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":"Inspect the parser.","agentsStates":{}}}}`)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"child-1","turnId":"child-turn","item":{"type":"agentMessage","id":"report-1","text":"Parser report","phase":"final_answer"}}}`)))

	reports := sink.LeapMuxNotifications()
	require.Len(t, reports, 1)
	assert.Equal(t, "Parser report", reports[0]["text"])
	assert.Equal(t, "Inspect the parser.", reports[0]["label"])
}

func TestHandleCodexOutput_PublishedChildReportDropsRetainedText(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"spawn-1","kind":"started","agentThreadId":"child-1","agentPath":"/root/reviewer"}}}`)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"child-1","turnId":"child-turn","item":{"type":"agentMessage","id":"report-1","text":"A long report","phase":"final_answer"}}}`)))

	agent.Mu.Lock()
	state := agent.collabChildren["child-1"]
	require.NotNil(t, state)
	assert.Empty(t, state.reportCandidateText)
	assert.Empty(t, state.reportCandidateItemID)
	agent.Mu.Unlock()
}

func TestHandleCodexOutput_ThreadNameUpdatedPersistsRawAsAgent(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))

	input := `{"method":"thread/name/updated","params":{"threadId":"thread-1","name":"Refactoring auth"}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

	require.Equal(t, 1, sink.NotificationCount(),
		"thread/name/updated must persist for reconnect rehydration")
	require.Equal(t, 0, sink.MessageCount(),
		"thread/name/updated must NOT fall through to the default AGENT branch")
	last := sink.LastNotification()
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, last.Source,
		"Codex-emitted lifecycle metadata must persist as AGENT")
	assert.JSONEq(t, input, string(last.Content),
		"raw envelope must be preserved so future renderers can read every field")
}

func TestHandleCodexOutput_SkillsChangedDoesNotReachTranscript(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))

	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"skills/changed","params":{}}`)))

	assert.Zero(t, sink.MessageCount())
	assert.Zero(t, sink.NotificationCount())
	assert.Zero(t, sink.SessionInfoCount())
	assert.Empty(t, sink.TurnActives())
}

func TestHandleCodexOutput_RemoteControlStatusChangedDoesNotReachTranscript(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))

	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"remoteControl/status/changed","params":{"status":"disabled","serverName":"OpenAI","installationId":"install-1","environmentId":null}}`)))

	assert.Zero(t, sink.MessageCount())
	assert.Zero(t, sink.NotificationCount())
	assert.Zero(t, sink.SessionInfoCount())
	assert.Empty(t, sink.TurnActives())
}

func TestHandleCodexOutput_RateLimitExceededSchedulesResume(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))

	input := `{"method":"account/rateLimits/updated","params":{"rateLimits":{"primary":{"usedPercent":100,"windowDurationMins":300,"resetsAt":1893456000},"secondary":{"usedPercent":20,"windowDurationMins":10080,"resetsAt":1894000000}}}}`
	handleCodexOutput(a, providerkit.ParseLine([]byte(input)))

	require.Equal(t, 1, sink.AutoScheduleCount())
	schedule := sink.LastAutoSchedule()
	require.Equal(t, agent.AutoContinueReasonRateLimit, schedule.Reason)
	require.True(t, schedule.DueAt.Equal(time.Unix(1893456000, 0).UTC()))

	assert.Zero(t, sink.NotificationCount(), "account state must not become transcript history")
}

// Codex reports the account rate limits after EVERY model call, so one ordinary turn
// with a tool call wrote the same sentence to the transcript twice. The snapshot is a
// state, not an event: an unchanged one states nothing the previous row did not.
func TestHandleCodexOutput_RateLimitsStayOutOfTheTranscript(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))

	input := `{"method":"account/rateLimits/updated","params":{"rateLimits":{"primary":{"usedPercent":92,"windowDurationMins":10080,"resetsAt":1893456000}}},"emittedAtMs":1}`
	repeat := `{"method":"account/rateLimits/updated","params":{"rateLimits":{"primary":{"usedPercent":92,"windowDurationMins":10080,"resetsAt":1893456000}}},"emittedAtMs":2}`
	changed := `{"method":"account/rateLimits/updated","params":{"rateLimits":{"primary":{"usedPercent":93,"windowDurationMins":10080,"resetsAt":1893456000}}},"emittedAtMs":3}`

	handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(repeat)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(changed)))

	require.Zero(t, sink.NotificationCount())
	// The live surfaces still see every report: a late subscriber reads the
	// broadcast, and the resume decision runs on each one.
	assert.Equal(t, 3, sink.SessionInfoCount(), "every report refreshes the popover")
}

// TestHandleCodexOutput_RateLimitBroadcastsSnakeCaseWire locks in the
// snake_case wire shape for Codex's session-info `rate_limits` payload.
// Both Codex and Claude broadcast the same tier shape so the frontend
// can consume one format regardless of provider.
func TestHandleCodexOutput_RateLimitBroadcastsSnakeCaseWire(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))

	input := `{"method":"account/rateLimits/updated","params":{"rateLimits":{"primary":{"usedPercent":85,"windowDurationMins":300,"resetsAt":1893456000},"secondary":{"usedPercent":10,"windowDurationMins":10080,"resetsAt":1894000000}}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

	require.Equal(t, 1, sink.SessionInfoCount())
	info := sink.LastSessionInfo()
	rateLimits, ok := info["rate_limits"].(map[string]interface{})
	require.True(t, ok, "broadcast must carry rate_limits in snake_case, got %#v", info)
	assert.Equal(t, "replace", rateLimits["mode"])
	rateLimits, ok = rateLimits["values"].(map[string]interface{})
	require.True(t, ok, "rate_limits must carry a values map")

	primary, ok := rateLimits["five_hour"].(map[string]interface{})
	require.True(t, ok, "primary tier should be keyed by rate_limit_type=five_hour")
	assert.Equal(t, "five_hour", primary["rate_limit_type"])
	assert.Equal(t, "allowed_warning", primary["status"])
	assert.Equal(t, 0.85, primary["utilization"])
	assert.Equal(t, int64(1893456000), primary["resets_at"])

	secondary, ok := rateLimits["seven_day"].(map[string]interface{})
	require.True(t, ok, "secondary tier should be keyed by rate_limit_type=seven_day")
	assert.Equal(t, "seven_day", secondary["rate_limit_type"])
}

func TestHandleCodexOutput_RateLimitClearCancelsResume(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))

	input := `{"method":"account/rateLimits/updated","params":{"rateLimits":{"primary":{"usedPercent":75,"windowDurationMins":300,"resetsAt":1893456000},"secondary":{"usedPercent":10,"windowDurationMins":10080,"resetsAt":1894000000}}}}`
	handleCodexOutput(a, providerkit.ParseLine([]byte(input)))

	require.Equal(t, 1, sink.AutoCancelCount())
	require.Equal(t, agent.AutoContinueReasonRateLimit, sink.LastAutoCancel())
}

// TestHandleCodexOutput_ReachedTypeRateLimitReachedSchedules verifies that newer
// Codex builds that emit the authoritative rateLimitReachedType schedule a resume
// when a time-windowed rate limit is reached.
func TestHandleCodexOutput_ReachedTypeRateLimitReachedSchedules(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))

	input := `{"method":"account/rateLimits/updated","params":{"rateLimits":{"rateLimitReachedType":"rate_limit_reached","primary":{"usedPercent":100,"windowDurationMins":300,"resetsAt":1893456000},"secondary":{"usedPercent":20,"windowDurationMins":10080,"resetsAt":1894000000}}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

	require.Equal(t, 1, sink.AutoScheduleCount())
	assert.True(t, sink.LastAutoSchedule().DueAt.Equal(time.Unix(1893456000, 0).UTC()))
}

// TestHandleCodexOutput_ReachedTypeCreditsDepletedCancels is the key carve-out:
// a credit-depletion block does NOT reset on the rolling-window timer, so even at
// 100% usage it must cancel rather than schedule a doomed-to-re-hit resume.
func TestHandleCodexOutput_ReachedTypeCreditsDepletedCancels(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))

	input := `{"method":"account/rateLimits/updated","params":{"rateLimits":{"rateLimitReachedType":"workspace_owner_credits_depleted","primary":{"usedPercent":100,"windowDurationMins":300,"resetsAt":1893456000}}}}`
	handleCodexOutput(a, providerkit.ParseLine([]byte(input)))

	assert.Equal(t, 0, sink.AutoScheduleCount(), "credit depletion must not schedule an auto-continue")
	require.Equal(t, 1, sink.AutoCancelCount())
	assert.Equal(t, agent.AutoContinueReasonRateLimit, sink.LastAutoCancel())
	require.Equal(t, 1, sink.SessionInfoCount())
	rateLimits, ok := sink.LastSessionInfo()["rate_limits"].(map[string]interface{})
	require.True(t, ok)
	rateLimits, ok = rateLimits["values"].(map[string]interface{})
	require.True(t, ok)
	accountBlock, ok := rateLimits["account_block"].(map[string]interface{})
	require.True(t, ok, "the live account block must remain visible outside the hidden transcript")
	assert.Equal(t, "workspace_owner_credits_depleted", accountBlock["rate_limit_type"])
	assert.Equal(t, "exceeded", accountBlock["status"])
}

// TestHandleCodexOutput_ReachedTypeUsageLimitReachedCancels verifies a usage cap
// (admin-set, not time-windowed) is treated like credit depletion: no resume.
func TestHandleCodexOutput_ReachedTypeUsageLimitReachedCancels(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))

	input := `{"method":"account/rateLimits/updated","params":{"rateLimits":{"rateLimitReachedType":"workspace_member_usage_limit_reached","primary":{"usedPercent":100,"windowDurationMins":300,"resetsAt":1893456000}}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

	assert.Equal(t, 0, sink.AutoScheduleCount())
	require.Equal(t, 1, sink.AutoCancelCount())
}

// TestHandleCodexOutput_ReachedTypeRoundingElevatesAndSchedules covers the case
// where the authoritative reached-type fires but integer-rounded usedPercent has
// not ticked to 100. The most-utilized window must both bind the resume time and
// surface as "exceeded" in the popover broadcast.
func TestHandleCodexOutput_ReachedTypeRoundingElevatesAndSchedules(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))

	input := `{"method":"account/rateLimits/updated","params":{"rateLimits":{"rateLimitReachedType":"rate_limit_reached","primary":{"usedPercent":99,"windowDurationMins":300,"resetsAt":1893456000},"secondary":{"usedPercent":20,"windowDurationMins":10080,"resetsAt":1894000000}}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

	require.Equal(t, 1, sink.AutoScheduleCount())
	assert.True(t, sink.LastAutoSchedule().DueAt.Equal(time.Unix(1893456000, 0).UTC()),
		"resume should bind to the most-utilized window's reset")

	require.Equal(t, 1, sink.SessionInfoCount())
	rateLimits, ok := sink.LastSessionInfo()["rate_limits"].(map[string]interface{})
	require.True(t, ok)
	rateLimits, ok = rateLimits["values"].(map[string]interface{})
	require.True(t, ok)
	primary, ok := rateLimits["five_hour"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "exceeded", primary["status"], "binding window must show as exceeded despite 99%%")
}

// TestHandleCodexOutput_ReachedTypeBindingWindowMissingResetFallsBack covers a
// time-windowed block whose most-utilized (binding) window carries NO resetsAt
// while a sibling window does. The resume must fall back to the sibling's reset
// rather than cancel and strand a block that WILL lift on the rolling-window timer.
func TestHandleCodexOutput_ReachedTypeBindingWindowMissingResetFallsBack(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))

	// primary (five_hour) is the most-utilized window (binds the resume) but reports
	// no resetsAt; secondary (seven_day) carries one. Before the fallback the nil
	// binding reset cancelled the resume entirely.
	input := `{"method":"account/rateLimits/updated","params":{"rateLimits":{"rateLimitReachedType":"rate_limit_reached","primary":{"usedPercent":99,"windowDurationMins":300},"secondary":{"usedPercent":20,"windowDurationMins":10080,"resetsAt":1894000000}}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

	require.Equal(t, 1, sink.AutoScheduleCount(), "a resumable time-window block must not be cancelled when the binding window lacks a reset")
	assert.Equal(t, 0, sink.AutoCancelCount())
	assert.True(t, sink.LastAutoSchedule().DueAt.Equal(time.Unix(1894000000, 0).UTC()),
		"resume falls back to the latest available window reset")
}

// TestSummarizeCodexRateLimits_ResumeFallsBackToLatestReset exercises the pure
// summarizer's elevate + resume edges directly (no agent), covering the
// binding-window-without-reset fallback that the handler test drives end to end.
func TestSummarizeCodexRateLimits_ResumeFallsBackToLatestReset(t *testing.T) {
	t.Parallel()

	resetSecondary := int64(1894000000)
	tiers := []*codexRateLimitTier{
		{UsedPercent: 99, WindowDurationMins: 300},                              // binding, no reset
		{UsedPercent: 20, WindowDurationMins: 10080, ResetsAt: &resetSecondary}, // sibling, has reset
	}
	s := summarizeCodexRateLimits(tiers, codexRateLimitReachedTimeWindow)

	require.Nil(t, s.bindingReset, "the most-utilized window carries no reset")
	require.NotNil(t, s.latestReset)
	assert.True(t, s.latestReset.Equal(time.Unix(resetSecondary, 0).UTC()))

	resume := codexRateLimitResumeReset(codexRateLimitReachedTimeWindow, s)
	require.NotNil(t, resume, "must resume via the sibling reset, not cancel")
	assert.True(t, resume.Equal(time.Unix(resetSecondary, 0).UTC()))

	// The rounding elevate still surfaces the most-utilized window as exceeded.
	five, ok := s.rateLimits["five_hour"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "exceeded", five["status"])
}

// TestSummarizeCodexRateLimits_ExceededWindowGatesElevate locks the elevate gate to
// the per-window status (any window at >=100% suppresses the elevate), matching the
// frontend replay path so the popover and the live broadcast can't disagree. The
// already-exceeded window keeps its status; a low sibling is never lifted.
func TestSummarizeCodexRateLimits_ExceededWindowGatesElevate(t *testing.T) {
	t.Parallel()

	tiers := []*codexRateLimitTier{
		{UsedPercent: 100, WindowDurationMins: 300},  // exceeded by status, no reset
		{UsedPercent: 20, WindowDurationMins: 10080}, // low sibling
	}
	s := summarizeCodexRateLimits(tiers, codexRateLimitReachedTimeWindow)

	five, ok := s.rateLimits["five_hour"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "exceeded", five["status"])
	seven, ok := s.rateLimits["seven_day"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "allowed", seven["status"], "a low sibling must not be elevated when another window is already exceeded")
}

func TestHandleCodexOutput_TurnFailedServerOverloadedSchedulesResume(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	ag := newCodexAgentWithSink(agent.NewProviderServices(sink))

	input := `{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"019d8b39-6599-7081-8901-53f80c6c56b7","items":[],"status":"failed","error":{"message":"Selected model is at capacity. Please try a different model.","codexErrorInfo":"serverOverloaded","additionalDetails":null}}}}`
	handleCodexOutput(ag, providerkit.ParseLine([]byte(input)))

	require.Equal(t, 1, sink.AutoScheduleCount())
	schedule := sink.LastAutoSchedule()
	require.Equal(t, agent.AutoContinueReasonAPIError, schedule.Reason)
	require.False(t, schedule.DueAt.IsZero())
	require.NotEmpty(t, schedule.SourcePayload)
}

func TestHandleCodexOutput_TurnFailedNonOverloadedCancelsAPIErrorResume(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))

	input := `{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","items":[],"status":"failed","error":{"message":"Something else failed","codexErrorInfo":"invalidRequest","additionalDetails":null}}}}`
	handleCodexOutput(a, providerkit.ParseLine([]byte(input)))

	require.Equal(t, 1, sink.AutoCancelCount())
	require.Equal(t, agent.AutoContinueReasonAPIError, sink.LastAutoCancel())
}

func TestHandleCodexOutput_TurnCompletedFailedRetryableSchedulesAPIError(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	a.threadID = "main-thread"

	input := `{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"failed","items":[],"error":{"message":"stream disconnected before completion: An error occurred while processing your request.","codexErrorInfo":"other","additionalDetails":null}}}}`
	handleCodexOutput(a, providerkit.ParseLine([]byte(input)))

	require.Equal(t, 1, sink.MessageCount())
	require.Equal(t, 1, sink.AutoScheduleCount())
	schedule := sink.LastAutoSchedule()
	require.Equal(t, agent.AutoContinueReasonAPIError, schedule.Reason)
	require.Equal(t, string(sink.Messages()[0].Content), string(schedule.SourcePayload))
	require.Equal(t, 0, sink.AutoCancelCount())
}

func TestIsRetryableCodexTurnFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		message string
		want    bool
	}{
		{"exact phrase", "stream disconnected before completion", true},
		{"colon suffix", "stream disconnected before completion: An error occurred while processing your request.", true},
		{"dash suffix", "stream disconnected before completion - upstream connection closed", true},
		{"double punctuation", "stream disconnected before completion:: retry later", true},
		{"alphanumeric suffix not matched", "stream disconnected before completionX", false},
		{"different message", "Request was aborted by the user.", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRetryableCodexTurnFailure(tt.message)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestHandleCodexOutput_TurnCompletedFailedNonRetryableCancelsAPIError(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	a.threadID = "main-thread"

	input := `{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"failed","items":[],"error":{"message":"Request was aborted by the user.","codexErrorInfo":"other","additionalDetails":null}}}}`
	handleCodexOutput(a, providerkit.ParseLine([]byte(input)))

	require.Equal(t, 0, sink.AutoScheduleCount())
	require.Equal(t, 1, sink.AutoCancelCount())
	require.Equal(t, agent.AutoContinueReasonAPIError, sink.LastAutoCancel())
}

func TestHandleCodexOutput_TurnCompletedSuccessCancelsAPIError(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	a.threadID = "main-thread"

	input := `{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"completed","items":[],"error":null}}}`
	handleCodexOutput(a, providerkit.ParseLine([]byte(input)))

	require.Equal(t, 0, sink.AutoScheduleCount())
	require.Equal(t, 1, sink.AutoCancelCount())
	require.Equal(t, agent.AutoContinueReasonAPIError, sink.LastAutoCancel())
}

// Current Codex models select Multi-Agent V2. That protocol does not emit a
// spawnAgent collab item. The parent receives a subAgentActivity pair first,
// then the child thread's own lifecycle, then a completed activity pair.
// Replaying that exact order must create one readable child, keep its output
// out of the parent transcript, and close its registry row.
func TestHandleCodexOutput_MultiAgentV2LifecycleOwnsTheChildTranscript(t *testing.T) {
	t.Parallel()

	sink := &recordingCodexEnsureSink{Sink: &agenttest.Sink{}}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))

	activityStarted := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"call-spawn","kind":"started","agentThreadId":"child-thread","agentPath":"/root/probe_child"}}}`
	activityStartedCompleted := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"call-spawn","kind":"started","agentThreadId":"child-thread","agentPath":"/root/probe_child"}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(activityStarted)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(activityStartedCompleted)))

	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1, "one activity pair creates one registry row")
	assert.Equal(t, "child-thread", rows[0].RowKey)
	assert.Equal(t, "probe_child", rows[0].Title, "the task path supplies the readable title")
	assert.Equal(t, "child-of-call-spawn", rows[0].ChildAgentID, "the row links to a transcript")
	assert.Equal(t, "test-agent", rows[0].ParentAgentID)
	assert.Equal(t, bgtask.StatusRunning, rows[0].Status)
	require.Len(t, sink.ensureCalls, 1)
	assert.Equal(t, codexEnsureCall{
		spawnSpanID:      "call-spawn",
		providerChildKey: "child-thread",
		title:            "probe_child",
	}, sink.ensureCalls[0], "child creation receives the same readable title")
	agent.Mu.Lock()
	childState := agent.collabChildren["child-thread"]
	agent.Mu.Unlock()
	assert.Equal(t, "call-spawn", childState.spawnCorrelationID)
	assert.Equal(t, "main-thread", childState.parentThreadID)
	assert.Equal(t, "/root/probe_child", childState.agentPath)

	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"child-thread","turn":{"id":"child-turn"}}}`)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"child-thread","turnId":"child-turn","item":{"type":"agentMessage","id":"child-message","text":"","phase":"final_answer"}}}`)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"child-thread","turnId":"child-turn","item":{"type":"agentMessage","id":"child-message","text":"CHILD_DONE","phase":"final_answer"}}}`)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"child-thread","turn":{"id":"child-turn","status":"completed","items":[],"error":null}}}`)))

	activityCompleted := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"subagent-completed-child-turn","kind":"completed","agentThreadId":"child-thread","agentPath":"/root/probe_child"}}}`
	activityCompletedCompleted := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"subagent-completed-child-turn","kind":"completed","agentThreadId":"child-thread","agentPath":"/root/probe_child"}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(activityCompleted)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(activityCompletedCompleted)))

	assert.Empty(t, sink.Messages(), "the parent transcript receives no child output")
	child := sink.Child("child-of-call-spawn")
	childMessages := child.Messages()
	require.Len(t, childMessages, 2, "the child receives its answer and turn end")
	assert.Contains(t, string(childMessages[0].Content), "CHILD_DONE")
	assert.True(t, childMessages[1].TurnEnd)

	rows = sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, bgtask.StatusCompleted, rows[0].Status)
}

func TestCodexChildRouteReusesTheResolvedSink(t *testing.T) {
	t.Parallel()

	sink := &recordingCodexEnsureSink{Sink: &agenttest.Sink{}}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"call-spawn","kind":"started","agentThreadId":"child-thread","agentPath":"/root/probe_child"}}}`)))
	sink.childSinkCalls = 0

	_, first := agent.lookupCodexChildRoute("child-thread")
	_, second := agent.lookupCodexChildRoute("child-thread")

	require.True(t, first)
	require.True(t, second)
	assert.Zero(t, sink.childSinkCalls, "a resolved active route must not rebuild its sink path per output event")

	agent.finishCollabChildRun("child-thread")
	sink.childSinkCalls = 0
	_, rebuilt := agent.lookupCodexChildRoute("child-thread")
	require.True(t, rebuilt)
	assert.Equal(t, 1, sink.childSinkCalls, "run completion must invalidate the sink that cleanup retires")
}

func TestHandleCodexOutput_MultiAgentV2DoesNotRegisterTheRootPath(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"root-call","kind":"started","agentThreadId":"main-thread","agentPath":"/root"}}}`)))

	assert.Empty(t, sink.BackgroundTasks(), "the canonical root path is the primary agent, not a subagent")
	assert.Empty(t, agent.collabChildren)
}

func TestHandleCodexOutput_MultiAgentV2FailedTurnClosesWithoutAnActivity(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"call-spawn","kind":"started","agentThreadId":"child-thread","agentPath":"/root/failing_child"}}}`)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"child-thread","turn":{"id":"child-turn"}}}`)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"child-thread","turn":{"id":"child-turn","status":"failed","items":[],"error":{"message":"child failed"}}}}`)))

	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, bgtask.StatusFailed, rows[0].Status,
		"V2 sends no failed activity, so the child turn closes the row")
	child := sink.Child("child-of-call-spawn")
	require.Len(t, child.Messages(), 1)
	assert.True(t, child.Messages()[0].TurnEnd)
}

func TestHandleCodexOutput_MultiAgentV2CompletedActivityClosesWithoutATurnEnd(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"call-spawn","kind":"started","agentThreadId":"child-thread","agentPath":"/root/completing_child"}}}`)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"child-thread","turn":{"id":"child-turn"}}}`)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"subagent-completed-child-turn","kind":"completed","agentThreadId":"child-thread","agentPath":"/root/completing_child"}}}`)))

	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, bgtask.StatusCompleted, rows[0].Status)
	assert.Empty(t, agent.childTurnID("child-thread"), "the final activity clears steering state")
	child := sink.Child("child-of-call-spawn")
	assert.Equal(t, []bool{true, false}, child.TurnActives())
}

func TestHandleCodexOutput_MultiAgentV2DuplicateCompletionRetriesARegistryFailure(t *testing.T) {
	t.Parallel()

	sink := &transientCodexCloseFailureSink{
		Sink:          &agenttest.Sink{},
		closeFailures: 1,
	}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	started := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"call-spawn","kind":"started","agentThreadId":"child-thread","agentPath":"/root/retry_child"}}}`
	completed := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"child-completed","kind":"completed","agentThreadId":"child-thread","agentPath":"/root/retry_child"}}}`

	handleCodexOutput(agent, providerkit.ParseLine([]byte(started)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(completed)))
	_, status, found, err := sink.LookupBackgroundTask("child-thread")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, bgtask.StatusRunning, status, "the failed close leaves the row active")

	handleCodexOutput(agent, providerkit.ParseLine([]byte(completed)))
	_, status, found, err = sink.LookupBackgroundTask("child-thread")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, bgtask.StatusCompleted, status, "the duplicate completion retries the close")
	assert.Equal(t, 2, sink.closeAttempts)
}

func TestHandleCodexOutput_MultiAgentV2LateDuplicateStartDoesNotReviveCompletedRun(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	started := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"spawn-call","kind":"started","agentThreadId":"child-thread","agentPath":"/root/idempotent_child"}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(started)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"child-thread","turn":{"id":"child-turn","status":"completed","items":[],"error":null}}}`)))

	handleCodexOutput(agent, providerkit.ParseLine([]byte(started)))

	_, status, found, err := sink.LookupBackgroundTask("child-thread")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, bgtask.StatusCompleted, status)
	assert.Empty(t, sink.RevivedTasks(), "a duplicate spawn activity cannot start a second run")
}

func TestHandleCodexOutput_MultiAgentV2ReplaysChildItemsAfterRouteRecovery(t *testing.T) {
	t.Parallel()

	sink := &transientCodexEnsureFailureSink{
		Sink:           &agenttest.Sink{},
		ensureFailures: 2,
	}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	started := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"call-spawn","kind":"started","agentThreadId":"child-thread","agentPath":"/root/recovered_child"}}}`
	childAnswer := `{"method":"item/completed","params":{"threadId":"child-thread","turnId":"child-turn","item":{"type":"agentMessage","id":"child-message","text":"RECOVERED_CHILD_DONE","phase":"final_answer"}}}`

	handleCodexOutput(agent, providerkit.ParseLine([]byte(started)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(childAnswer)))
	assert.Empty(t, sink.Messages(), "a child item must never fall through to the root after a route error")

	// The duplicate activity retries child creation. The retained child item
	// must then replay into the child transcript in provider order.
	handleCodexOutput(agent, providerkit.ParseLine([]byte(started)))
	child := sink.Child("child-of-call-spawn")
	require.Len(t, child.Messages(), 1)
	assert.Contains(t, string(child.Messages()[0].Content), "RECOVERED_CHILD_DONE")
}

func TestHandleCodexOutput_MultiAgentV2CompletionBeforeStartStillClosesTheRun(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	completed := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"completion-call","kind":"completed","agentThreadId":"child-thread","agentPath":"/root/reordered_child"}}}`
	started := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"spawn-call","kind":"started","agentThreadId":"child-thread","agentPath":"/root/reordered_child"}}}`

	handleCodexOutput(agent, providerkit.ParseLine([]byte(completed)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(started)))

	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, bgtask.StatusCompleted, rows[0].Status,
		"the authoritative start must apply a completion that arrived first")
	assert.Equal(t, "child-of-spawn-call", rows[0].ChildAgentID)
}

func TestHandleCodexOutput_MultiAgentV2StartOwnsIdentityAfterEarlierInteraction(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	interacted := `{"method":"item/completed","params":{"threadId":"unrelated-thread","turnId":"other-turn","item":{"type":"subAgentActivity","id":"interaction-call","kind":"interacted","agentThreadId":"child-thread","agentPath":"/root/identity_child"}}}`
	started := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"spawn-call","kind":"started","agentThreadId":"child-thread","agentPath":"/root/identity_child"}}}`

	handleCodexOutput(agent, providerkit.ParseLine([]byte(interacted)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(started)))

	agent.Mu.Lock()
	state := agent.collabChildren["child-thread"]
	agent.Mu.Unlock()
	assert.Equal(t, "spawn-call", state.spawnCorrelationID)
	assert.Equal(t, "main-thread", state.parentThreadID)
	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, "child-of-spawn-call", rows[0].ChildAgentID)
	assert.Equal(t, "test-agent", rows[0].ParentAgentID)
}

func TestHandleCodexOutput_MultiAgentV2CompletionClearsTurnWhenRouteRecoveryFails(t *testing.T) {
	t.Parallel()

	sink := &transientCodexEnsureFailureSink{
		Sink:           &agenttest.Sink{},
		ensureFailures: 3,
	}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	started := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"spawn-call","kind":"started","agentThreadId":"child-thread","agentPath":"/root/failing_route"}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(started)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"child-thread","turn":{"id":"child-turn"}}}`)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"completion-call","kind":"completed","agentThreadId":"child-thread","agentPath":"/root/failing_route"}}}`)))

	assert.Empty(t, agent.childTurnID("child-thread"),
		"a final activity must release child input even when child creation still fails")
}

func TestHandleCodexOutput_NestedV2CollaborationToolUsesTheChildTranscript(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"spawn-parent","kind":"started","agentThreadId":"parent-thread","agentPath":"/root/parent"}}}`)))
	waitStarted := `{"method":"item/started","params":{"threadId":"parent-thread","turnId":"parent-turn","item":{"type":"collabAgentToolCall","id":"wait-call","tool":"wait","status":"inProgress","senderThreadId":"parent-thread","receiverThreadIds":[],"prompt":null,"agentsStates":{}}}}`
	waitCompleted := `{"method":"item/completed","params":{"threadId":"parent-thread","turnId":"parent-turn","item":{"type":"collabAgentToolCall","id":"wait-call","tool":"wait","status":"completed","senderThreadId":"parent-thread","receiverThreadIds":[],"prompt":null,"agentsStates":{}}}}`

	handleCodexOutput(agent, providerkit.ParseLine([]byte(waitStarted)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(waitCompleted)))

	assert.Empty(t, sink.Messages())
	parent := sink.Child("child-of-spawn-parent")
	require.Len(t, parent.Messages(), 2, "the child transcript retains both collaboration tool boundaries")
	assert.Equal(t, []string{"wait-call"}, parent.ClosedSpans())
}

func TestHandleCodexOutput_UnresolvedChildGenerationHasAMemoryCap(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	chunk := strings.Repeat("x", 768<<10)
	for range 2 {
		raw, err := json.Marshal(map[string]any{
			"method": "item/agentMessage/delta",
			"params": map[string]any{
				"threadId": "unknown-child",
				"itemId":   "message-1",
				"delta":    chunk,
			},
		})
		require.NoError(t, err)
		handleCodexOutput(agent, providerkit.ParseLine(raw))
	}

	buffer := agent.codexChildGenerationBuffer("unknown-child")
	retained := buffer.RetainedTextLenForTest("message-1")
	assert.LessOrEqual(t, retained, 1<<20,
		"an unresolved child cannot retain model output without a fixed limit")
}

func TestCodex_ReplayReportsAFullPendingQueueWithNoRetainedEvent(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"spawn-call","kind":"started","agentThreadId":"child-thread","agentPath":"/root/limited_child"}}}`)))
	route, routed := agent.lookupCodexChildRoute("child-thread")
	require.True(t, routed)
	agent.Mu.Lock()
	agent.collabChildren["child-thread"].pendingOutputDropped = true
	agent.Mu.Unlock()

	agent.replayPendingCodexChildEvents("child-thread", route)

	agent.Mu.Lock()
	dropped := agent.collabChildren["child-thread"].pendingOutputDropped
	agent.Mu.Unlock()
	assert.False(t, dropped, "the route recovery must consume the overflow signal")
}

func TestHandleCodexOutput_MultiAgentV2ReusesTheChildTranscript(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	started := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"call-spawn","kind":"started","agentThreadId":"child-thread","agentPath":"/root/reuse_child"}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(started)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"child-thread","turn":{"id":"child-turn-1"}}}`)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"child-thread","turnId":"child-turn-1","item":{"type":"agentMessage","id":"child-message-1","text":"FIRST_DONE","phase":"final_answer"}}}`)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"child-thread","turn":{"id":"child-turn-1","status":"completed","items":[],"error":null}}}`)))

	interacted := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"child-input","kind":"interacted","agentThreadId":"child-thread","agentPath":"/root/reuse_child"}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(interacted)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"child-thread","turn":{"id":"child-turn-2"}}}`)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"child-thread","turnId":"child-turn-2","item":{"type":"agentMessage","id":"child-message-2","text":"SECOND_DONE","phase":"final_answer"}}}`)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"child-thread","turn":{"id":"child-turn-2","status":"completed","items":[],"error":null}}}`)))

	assert.Empty(t, sink.Messages())
	child := sink.Child("child-of-call-spawn")
	messages := child.Messages()
	require.Len(t, messages, 4, "both turns use one child transcript")
	assert.Contains(t, string(messages[0].Content), "FIRST_DONE")
	assert.True(t, messages[1].TurnEnd)
	assert.Contains(t, string(messages[2].Content), "SECOND_DONE")
	assert.True(t, messages[3].TurnEnd)
	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, "child-of-call-spawn", rows[0].ChildAgentID)
	assert.Equal(t, bgtask.StatusCompleted, rows[0].Status)
}

func TestHandleCodexOutput_MultiAgentV2NestedChildUsesItsDirectParent(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"call-parent","kind":"started","agentThreadId":"parent-thread","agentPath":"/root/parent_child"}}}`)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"parent-thread","turnId":"parent-turn","item":{"type":"subAgentActivity","id":"call-grandchild","kind":"started","agentThreadId":"grandchild-thread","agentPath":"/root/parent_child/grandchild"}}}`)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"grandchild-thread","turnId":"grandchild-turn","item":{"type":"agentMessage","id":"grandchild-message","text":"NESTED_DONE","phase":"final_answer"}}}`)))

	rows := sink.BackgroundTasks()
	require.Len(t, rows, 2)
	var grandchildRow bgtask.Item
	foundGrandchild := false
	for _, row := range rows {
		if row.RowKey == "grandchild-thread" {
			grandchildRow = row
			foundGrandchild = true
		}
	}
	require.True(t, foundGrandchild)
	assert.Equal(t, "grandchild", grandchildRow.Title)
	assert.Equal(t, "child-of-call-parent", grandchildRow.ParentAgentID)
	assert.Equal(t, "child-of-call-grandchild", grandchildRow.ChildAgentID)
	assert.Empty(t, sink.Messages(), "nested output does not reach the root")

	parent := sink.Child("child-of-call-parent")
	assert.Empty(t, parent.Messages(), "nested output does not reach the direct parent transcript")
	grandchild := parent.Child("child-of-call-grandchild")
	require.Len(t, grandchild.Messages(), 1)
	assert.Contains(t, string(grandchild.Messages()[0].Content), "NESTED_DONE")
}

// A spawnAgent tool call owns NO span: the subagent's output lives in its own
// child transcript, so a rail held open for the whole run would only push every
// concurrent tool one column right. The row still carries the span id (the
// frontend pairs the started and completed rows by it) and the span type (which
// item/completed reads back).
func TestHandleCodexOutput_SpawnAgentStartedOpensNoSpan(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))

	input := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-1","tool":"spawnAgent","status":"inProgress","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":"do work","model":"gpt-5.4","reasoningEffort":"medium","agentsStates":{}}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

	assert.Empty(t, sink.OpenSpans(), "a spawn opens no span")
	assert.Equal(t, 0, sink.ClosedSpanCount())
	assert.Equal(t, "collabAgentToolCall", sink.GetSpanType("call-1"), "the span type is still recorded")

	messages := sink.Messages()
	require.Len(t, messages, 1)
	assert.Equal(t, "call-1", messages[0].SpanID, "the row still carries the span id")
	assert.Empty(t, messages[0].SpansOpenAtPersist, "nothing else was open, so the row draws no rail")
}

// The spawn's completed row closes nothing and still draws no rail.
func TestHandleCodexOutput_SpawnAgentCompletedLeavesNoSpan(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))

	started := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-1","tool":"spawnAgent","status":"inProgress","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":"do work","model":"gpt-5.4","reasoningEffort":"medium","agentsStates":{}}}}`
	completed := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-1","tool":"spawnAgent","status":"completed","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":"do work","model":"gpt-5.4","reasoningEffort":"medium","agentsStates":{"child-1":{"status":"completed","message":"done"}}}}}`

	handleCodexOutput(agent, providerkit.ParseLine([]byte(started)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(completed)))

	assert.Empty(t, sink.OpenSpans(), "neither row opens a span")

	messages := sink.Messages()
	require.Len(t, messages, 2)
	assert.True(t, messages[1].Closing, "the completed row is still a closer")
	assert.Empty(t, messages[1].SpansOpenAtPersist, "and it draws no rail")
}

// A collab child that runs again after its row went final. The upsert absorbs a
// non-final status against a final row -- deliberately, because a replayed
// snapshot cannot prove a restart -- so without the revive the sidebar row and
// the tab chip read "finished" for the whole second run.
func TestHandleCodexOutput_ARerunCollabChildReopensItsRow(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))

	started := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-1","tool":"spawnAgent","status":"inProgress","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":"do work","model":"gpt-5.4","reasoningEffort":"medium","agentsStates":{}}}}`
	completed := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-1","tool":"spawnAgent","status":"completed","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":"do work","model":"gpt-5.4","reasoningEffort":"medium","agentsStates":{"child-1":{"status":"completed","message":"done"}}}}}`
	// The root resumes the finished child: item/started re-registers the receiver,
	// which is the proof a replayed snapshot never carries. The child's own
	// turn/started then reports it working again.
	resumed := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn2","item":{"type":"collabAgentToolCall","id":"call-2","tool":"resumeAgent","status":"inProgress","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":"more work","model":"gpt-5.4","reasoningEffort":"medium","agentsStates":{}}}}`
	childTurn := `{"method":"turn/started","params":{"threadId":"child-1","turn":{"id":"turn-c2"}}}`

	handleCodexOutput(agent, providerkit.ParseLine([]byte(started)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(completed)))
	_, status, ok, _ := sink.LookupBackgroundTask("child-1")
	require.True(t, ok)
	require.True(t, status.IsFinished(), "the first run closed the row")

	handleCodexOutput(agent, providerkit.ParseLine([]byte(resumed)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(childTurn)))

	assert.Equal(t, []string{"child-1"}, sink.RevivedTasks(), "the re-registered child reopens its row")
	_, status, ok, _ = sink.LookupBackgroundTask("child-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, status)
}

// A replayed snapshot that still calls an old child running must not reopen it.
// A later child turn is live evidence and must reopen the same transcript.
func TestHandleCodexOutput_AReplayedRunningStateWaitsForALiveChildTurn(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))

	started := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-1","tool":"spawnAgent","status":"inProgress","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":"do work","model":"gpt-5.4","reasoningEffort":"medium","agentsStates":{}}}}`
	completed := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-1","tool":"spawnAgent","status":"completed","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":"do work","model":"gpt-5.4","reasoningEffort":"medium","agentsStates":{"child-1":{"status":"completed","message":"done"}}}}}`
	// A LATER collab call for a DIFFERENT child, whose snapshot still carries the
	// old child as running.
	stale := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn2","item":{"type":"collabAgentToolCall","id":"call-2","tool":"spawnAgent","status":"completed","senderThreadId":"main-thread","receiverThreadIds":["child-2"],"prompt":"other","model":"gpt-5.4","reasoningEffort":"medium","agentsStates":{"child-1":{"status":"running"},"child-2":{"status":"completed"}}}}}`

	handleCodexOutput(agent, providerkit.ParseLine([]byte(started)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(completed)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(stale)))

	assert.Empty(t, sink.RevivedTasks(), "a snapshot alone cannot prove a restart")
	_, status, ok, _ := sink.LookupBackgroundTask("child-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusCompleted, status, "the row keeps its final status")

	liveTurn := `{"method":"turn/started","params":{"threadId":"child-1","turn":{"id":"turn-c9"}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(liveTurn)))
	assert.Equal(t, []string{"child-1"}, sink.RevivedTasks(), "a live child turn reopens the row")
	_, status, ok, _ = sink.LookupBackgroundTask("child-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, status)
}

// A subagent spawn that starts while an unrelated command is running draws that
// command's rail and nothing more -- one column, not two.
func TestHandleCodexOutput_SpawnInsideOpenCommandDrawsOneColumn(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))

	cmdStarted := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"commandExecution","id":"cmd-1","status":"inProgress","command":"ls","cwd":"/tmp","processId":"123","commandActions":[]}}}`
	spawnStarted := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-1","tool":"spawnAgent","status":"inProgress","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":"do work","model":"gpt-5.4","reasoningEffort":"medium","agentsStates":{}}}}`

	handleCodexOutput(agent, providerkit.ParseLine([]byte(cmdStarted)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(spawnStarted)))

	open := sink.OpenSpans()
	require.Len(t, open, 1)
	assert.Equal(t, "cmd-1", open[0].SpanID, "only the command owns a span")

	messages := sink.Messages()
	require.Len(t, messages, 2)
	require.Len(t, messages[1].SpansOpenAtPersist, 1, "the spawn row draws exactly the command's rail")
	assert.Equal(t, "cmd-1", messages[1].SpansOpenAtPersist[0].SpanID)
}

func TestHandleCodexOutput_WaitIsAFlatToolSpan(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))

	spawnStarted := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-1","tool":"spawnAgent","status":"inProgress","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":"do work","model":"gpt-5.4","reasoningEffort":"medium","agentsStates":{}}}}`
	waitStarted := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-2","tool":"wait","status":"inProgress","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":null,"model":null,"reasoningEffort":null,"agentsStates":{}}}}`
	waitCompleted := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-2","tool":"wait","status":"completed","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":null,"model":null,"reasoningEffort":null,"agentsStates":{"child-1":{"status":"completed","message":"done"}}}}}`

	handleCodexOutput(agent, providerkit.ParseLine([]byte(spawnStarted)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(waitStarted)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(waitCompleted)))

	messages := sink.Messages()
	require.Len(t, messages, 3)
	// wait is a flat tool span: no parent nesting, no connector.
	require.Equal(t, "", messages[1].ParentSpanID, "wait started is flat")
	require.Equal(t, "", messages[2].ParentSpanID, "wait completed is flat")

	// Only the spawn loses its span. wait blocks on the subagent but stays an
	// ordinary tool span, so it opens one and closes it at completion.
	open := sink.OpenSpans()
	require.Len(t, open, 1, "the spawn opens nothing; wait opens one span")
	assert.Equal(t, "call-2", open[0].SpanID)
	assert.Equal(t, []string{"call-2"}, sink.ClosedSpans())
	// The closing row persists while its own span is still open, so it can draw
	// the connector_end on that column. The spawn contributes no second column.
	require.Len(t, messages[2].SpansOpenAtPersist, 1)
	assert.Equal(t, "call-2", messages[2].SpansOpenAtPersist[0].SpanID)
}

func TestHandleCodexOutput_SubagentCommandRoutesToChildTranscript(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))

	spawnStarted := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-1","tool":"spawnAgent","status":"inProgress","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":"do work","model":"gpt-5.4","reasoningEffort":"medium","agentsStates":{}}}}`
	cmdStarted := `{"method":"item/started","params":{"threadId":"child-1","turnId":"turn2","item":{"type":"commandExecution","id":"cmd-1","status":"inProgress","command":"ls","cwd":"/tmp","processId":"123","commandActions":[]}}}`
	cmdCompleted := `{"method":"item/completed","params":{"threadId":"child-1","turnId":"turn2","item":{"type":"commandExecution","id":"cmd-1","status":"completed","command":"ls","cwd":"/tmp","processId":"123","commandActions":[],"aggregatedOutput":"ok","exitCode":0,"durationMs":1}}}`

	handleCodexOutput(agent, providerkit.ParseLine([]byte(spawnStarted)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(cmdStarted)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(cmdCompleted)))

	// The parent transcript keeps only the spawn row; the child's command
	// routed to the child transcript.
	parentMessages := sink.Messages()
	require.Len(t, parentMessages, 1, "parent keeps only the spawn row")

	child := sink.Child("child-of-call-1")
	childMessages := child.Messages()
	require.Len(t, childMessages, 2, "child got started + completed")
}

func TestHandleCodexOutput_SpawnAgentCompletedClosesSpawnSpan(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))

	// Start the spawn span first.
	started := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-1","tool":"spawnAgent","status":"inProgress","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":"do work","model":"gpt-5.4","reasoningEffort":"medium","agentsStates":{}}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(started)))

	completed := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-1","tool":"spawnAgent","status":"completed","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":"do work","model":"gpt-5.4","reasoningEffort":"medium","agentsStates":{"child-1":{"status":"running","message":null}}}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(completed)))

	// The spawn span CLOSES at spawn completion (children route to their own
	// transcripts; they no longer nest under it).
	require.Contains(t, sink.ClosedSpans(), "call-1")
}

func TestHandleCodexOutput_SpawnAgentCompletedRegistersLateReceiverThreads(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))

	started := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-1","tool":"spawnAgent","status":"inProgress","senderThreadId":"main-thread","receiverThreadIds":[],"prompt":"do work","model":"gpt-5.4","reasoningEffort":"medium","agentsStates":{}}}}`
	completed := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-1","tool":"spawnAgent","status":"completed","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":"do work","model":"gpt-5.4","reasoningEffort":"medium","agentsStates":{"child-1":{"status":"running","message":null}}}}}`
	cmdStarted := `{"method":"item/started","params":{"threadId":"child-1","turnId":"turn2","item":{"type":"commandExecution","id":"cmd-1","status":"inProgress","command":"ls","cwd":"/tmp","processId":"123","commandActions":[]}}}`

	handleCodexOutput(agent, providerkit.ParseLine([]byte(started)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(completed)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(cmdStarted)))

	// The child's command routed to the child transcript (late receiver
	// registration at spawn completion made the child route resolve).
	parentMessages := sink.Messages()
	require.Len(t, parentMessages, 2, "parent keeps spawn started + completed")
	child := sink.Child("child-of-call-1")
	childMessages := child.Messages()
	require.Len(t, childMessages, 2, "child transcript opens on the spawn prompt, then the command")
	// The transcript opens on the instruction the subagent was given, so the tab
	// shows what was asked rather than starting mid-work.
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, childMessages[0].Source)
	assert.JSONEq(t, `{"content":"do work"}`, string(childMessages[0].Content))
	assert.Equal(t, "cmd-1", childMessages[1].SpanID, "then the child's own command")
}

func TestHandleCodexOutput_WaitCompletedClosesFinalSubagentSpan(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))

	started := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-2","tool":"wait","status":"inProgress","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":null,"model":null,"reasoningEffort":null,"agentsStates":{}}}}`
	completed := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-2","tool":"wait","status":"completed","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":null,"model":null,"reasoningEffort":null,"agentsStates":{"child-1":{"status":"completed","message":"done"}}}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(started)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(completed)))

	// wait is a flat tool span that CLOSES at completion (like every other collab
	// tool). The old code ran CloseSpan only for `collab.Tool == "spawnAgent"`,
	// leaving wait/sendInput/resumeAgent/closeAgent spans open until turn reset.
	require.Contains(t, sink.ClosedSpans(), "call-2", "wait span closes at completion")
}

func TestHandleCodexOutput_WaitCompletedDoesNotAffectSpawnSpan(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))

	// A wait completion with a non-final agent state must close the WAIT
	// tool span (its own lifecycle) but must NOT touch a spawn span. There is no
	// spawn here, so ClosedSpans contains only the wait span.
	input := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-2","tool":"wait","status":"completed","senderThreadId":"main-thread","receiverThreadIds":["child-1","child-2"],"prompt":null,"model":null,"reasoningEffort":null,"agentsStates":{"child-1":{"status":"running","message":null}}}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

	// The wait span closes at completion (its own lifecycle).
	require.Contains(t, sink.ClosedSpans(), "call-2")
	// No spawn span exists, so nothing else is closed.
	require.NotContains(t, sink.ClosedSpans(), "call-1")
}

func TestHandleCodexOutput_CloseAgentCompletedClosesSubagentSpan(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))

	started := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-3","tool":"closeAgent","status":"inProgress","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":null,"model":null,"reasoningEffort":null,"agentsStates":{}}}}`
	completed := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-3","tool":"closeAgent","status":"completed","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":null,"model":null,"reasoningEffort":null,"agentsStates":{"child-1":{"status":"shutdown","message":null}}}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(started)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(completed)))

	require.Contains(t, sink.ClosedSpans(), "call-3", "closeAgent span closes at completion")
}

func TestHandleCodexOutput_WaitCompletedClosesOnlyFinalReceivers(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))

	started := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-4","tool":"wait","status":"inProgress","senderThreadId":"main-thread","receiverThreadIds":["child-1","child-2","child-3"],"prompt":null,"model":null,"reasoningEffort":null,"agentsStates":{}}}}`
	completed := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-4","tool":"wait","status":"completed","senderThreadId":"main-thread","receiverThreadIds":["child-1","child-2","child-3"],"prompt":null,"model":null,"reasoningEffort":null,"agentsStates":{"child-1":{"status":"completed","message":"done"},"child-2":{"status":"running","message":null},"child-3":{"status":"notFound","message":null}}}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(started)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(completed)))

	// The collab span closes at completion regardless of which receivers are
	// finished -- the span lifecycle is about the tool_call, not the children.
	require.Contains(t, sink.ClosedSpans(), "call-4", "wait span closes at completion")
}

func TestHandleCodexOutput_WaitIsFlatNoDrain(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))

	spawnStarted := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-1","tool":"spawnAgent","status":"inProgress","senderThreadId":"main-thread","receiverThreadIds":["child-1","child-2"],"prompt":"do work","model":"gpt-5.4","reasoningEffort":"medium","agentsStates":{}}}}`
	waitCompletedFirst := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-2","tool":"wait","status":"completed","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":null,"model":null,"reasoningEffort":null,"agentsStates":{"child-1":{"status":"completed","message":"done"}}}}}`
	waitCompletedSecond := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-3","tool":"wait","status":"completed","senderThreadId":"main-thread","receiverThreadIds":["child-2"],"prompt":null,"model":null,"reasoningEffort":null,"agentsStates":{"child-2":{"status":"completed","message":"done"}}}}}`

	handleCodexOutput(agent, providerkit.ParseLine([]byte(spawnStarted)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(waitCompletedFirst)))

	// No drain: the spawn span is unaffected by wait completion.
	require.NotContains(t, sink.ClosedSpans(), "call-1")

	handleCodexOutput(agent, providerkit.ParseLine([]byte(waitCompletedSecond)))

	// Still no drain -- wait is a flat tool span, not a spawn-span closer.
	require.NotContains(t, sink.ClosedSpans(), "call-1")
}

func TestHandleCodexOutput_CommandExecutionOutputDelta(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))

	input := `{"method":"item/commandExecution/outputDelta","params":{"itemId":"cmd-1","delta":"hello\n","threadId":"t1","turnId":"turn1"}}`
	handleCodexOutput(a, providerkit.ParseLine([]byte(input)))

	updates := sink.ProgressUpdates()
	require.Len(t, updates, 1)
	require.Equal(t, agent.ProgressOutputDelta, updates[0].Operation)
	require.Equal(t, "cmd-1", updates[0].ScopeID)
	require.Equal(t, int64(6), updates[0].Value)
	require.Equal(t, 0, sink.MessageCount())
}

func TestHandleCodexOutput_ReasoningPersistFailureKeepsLiveStream(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{PersistErr: fmt.Errorf("database unavailable")}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	key := codexReasoningKey("main-thread", "reason-1")
	agent.reasoningStreamKind = map[string]string{key: codexReasoningKindSummary}

	completedParams := `{"threadId":"main-thread","turnId":"turn1","item":{"type":"reasoning","id":"reason-1","summary":["durable summary"],"content":[]}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/completed","params":`+completedParams+`}`)))

	agent.Mu.Lock()
	_, stillLocked := agent.reasoningStreamKind[key]
	agent.Mu.Unlock()
	assert.False(t, stillLocked, "a completed reasoning item must release bookkeeping after a persist error")
}

func TestHandleCodexOutput_EmptyReasoningItemsDoNotPersist(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		input string
	}{
		{
			name:  "started item",
			input: `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"reasoning","id":"reason-1","summary":[],"content":[]}}}`,
		},
		{
			name:  "completed item with no text fields",
			input: `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"reasoning","id":"reason-1"}}}`,
		},
		{
			name:  "completed item with empty arrays",
			input: `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"reasoning","id":"reason-1","summary":[],"content":[]}}}`,
		},
		{
			name:  "completed item with blank entries",
			input: `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"reasoning","id":"reason-1","summary":[" ","\n"],"content":["\t"],"text":"  "}}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sink := &agenttest.Sink{}
			agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
			agent.threadID = "main-thread"

			handleCodexOutput(agent, providerkit.ParseLine([]byte(tc.input)))

			assert.Zero(t, sink.MessageCount())
			assert.Zero(t, sink.NotificationCount())
		})
	}
}

func TestHandleCodexOutput_ReasoningItemsWithVisibleTextPersist(t *testing.T) {
	t.Parallel()

	for _, item := range []string{
		`{"type":"reasoning","id":"reason-1","summary":["summary"],"content":[]}`,
		`{"type":"reasoning","id":"reason-1","summary":[" "],"content":["content"]}`,
		`{"type":"reasoning","id":"reason-1","summary":[],"content":[],"text":"legacy text"}`,
	} {
		sink := &agenttest.Sink{}
		agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
		agent.threadID = "main-thread"

		handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn-1","item":`+item+`}}`)))

		assert.Equal(t, 1, sink.MessageCount(), item)
	}
}

func TestHandleCodexOutput_FileChangeOutputDelta(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	ag := newCodexAgentWithSink(agent.NewProviderServices(sink))

	input := `{"method":"item/fileChange/outputDelta","params":{"itemId":"fc-1","delta":"diff --git a.txt b.txt\n","threadId":"t1","turnId":"turn1"}}`
	handleCodexOutput(ag, providerkit.ParseLine([]byte(input)))

	updates := sink.ProgressUpdates()
	require.Len(t, updates, 1)
	require.Equal(t, agent.OutputDeltaProgress("fc-1", 23), updates[0])
	require.Equal(t, 0, sink.MessageCount())
}

// Both image items are ordinary tool items and take the ordinary tool path.
// They used to fall to handleItemCompleted's default branch instead: the completed
// row persisted with a span id that nothing had opened and nothing then closed,
// so the transcript drew a rail that stayed open for the rest of the turn.
func TestHandleCodexOutput_ImageItemsOpenAndCloseASpan(t *testing.T) {
	t.Parallel()

	cases := []struct {
		itemType  string
		id        string
		started   string
		completed string
	}{
		{
			itemType:  "imageGeneration",
			id:        "img-1",
			started:   `{"method":"item/started","params":{"threadId":"main-thread","item":{"type":"imageGeneration","id":"img-1","status":"inProgress","prompt":"a cat"}}}`,
			completed: `{"method":"item/completed","params":{"threadId":"main-thread","item":{"type":"imageGeneration","id":"img-1","status":"completed","result":"aVZ","revisedPrompt":"a cat, photoreal"}}}`,
		},
		{
			itemType:  "imageView",
			id:        "view-1",
			started:   `{"method":"item/started","params":{"threadId":"main-thread","item":{"type":"imageView","id":"view-1","status":"inProgress","path":"/tmp/a.png"}}}`,
			completed: `{"method":"item/completed","params":{"threadId":"main-thread","item":{"type":"imageView","id":"view-1","status":"completed","path":"/tmp/a.png"}}}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.itemType, func(t *testing.T) {
			t.Parallel()

			sink := &agenttest.Sink{}
			agent := newCodexAgentWithSink(agent.NewProviderServices(sink))

			handleCodexOutput(agent, providerkit.ParseLine([]byte(tc.started)))
			handleCodexOutput(agent, providerkit.ParseLine([]byte(tc.completed)))

			open := sink.OpenSpans()
			require.Len(t, open, 1, "the started row opens one span")
			assert.Equal(t, tc.id, open[0].SpanID)
			assert.Equal(t, tc.itemType, sink.GetSpanType(tc.id))
			assert.Equal(t, []string{tc.id}, sink.ClosedSpans(), "and the completed row closes it")

			messages := sink.Messages()
			require.Len(t, messages, 2)
			assert.False(t, messages[0].Closing)
			assert.True(t, messages[1].Closing)
			// The tool_use row persists BEFORE its own span opens, and the
			// result row WHILE it is open -- that is what draws the connector.
			assert.Empty(t, messages[0].SpansOpenAtPersist)
			require.Len(t, messages[1].SpansOpenAtPersist, 1)
			assert.Equal(t, tc.id, messages[1].SpansOpenAtPersist[0].SpanID)
			// Neither item streams deltas keyed by its item id, so a stream-end
			// would end a stream that never started.
		})
	}
}

func TestHandleCodexOutput_ApprovalWithoutID(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))

	// Missing "id" field — should be ignored (logged as warning).
	input := `{"method":"item/tool/requestUserInput","params":{"questions":[]}}`

	handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

	assert.Equal(t, 0, sink.PublishedControlCount())
}

func TestHandleCodexOutput_TokenUsageUpdatedBroadcastsContextUsageWithoutPersisting(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))

	input := `{"method":"thread/tokenUsage/updated","params":{"threadId":"thread-1","turnId":"turn-1","tokenUsage":{"total":{"totalTokens":200,"inputTokens":100,"cachedInputTokens":25,"outputTokens":50,"reasoningOutputTokens":9},"last":{"totalTokens":23,"inputTokens":10,"cachedInputTokens":5,"outputTokens":7,"reasoningOutputTokens":1},"modelContextWindow":4096}}}`
	agent.threadID = "thread-1"
	handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

	require.Zero(t, sink.NotificationCount())
	require.Zero(t, sink.MessageCount())
	require.Equal(t, 1, sink.SessionInfoCount())

	info := sink.LastSessionInfo()
	usage, ok := info["context_usage"].(map[string]interface{})
	require.True(t, ok, "expected context_usage map, got %#v", info["context_usage"])
	require.Equal(t, int64(5), usage["input_tokens"])
	require.Equal(t, int64(0), usage["cache_creation_input_tokens"])
	require.Equal(t, int64(5), usage["cache_read_input_tokens"])
	require.Equal(t, int64(4096), usage["context_window"])
	// Codex's own total for the live request. The browser prefers it to the sum of
	// the counts, so the gauge states what Codex measured.
	require.Equal(t, int64(23), usage["context_tokens"])
}

func TestHandleCodexOutput_ThreadStatusChangedDoesNotReachTheTranscript(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	agent.threadID = "thread-1"

	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"thread/status/changed","params":{"threadId":"thread-1","status":{"type":"active","activeFlags":["waitingOnApproval"]}}}`)))

	assert.Zero(t, sink.MessageCount())
	assert.Zero(t, sink.NotificationCount())
	assert.Zero(t, sink.SessionInfoCount())
	assert.Empty(t, sink.TurnActives())
}

func TestHandleCodexOutput_SuccessfulHookLifecycleDoesNotReachTheTranscript(t *testing.T) {
	t.Parallel()

	for _, input := range []string{
		`{"method":"hook/started","params":{"threadId":"thread-1","turnId":"turn-1","run":{"id":"hook-1","status":"running"}}}`,
		`{"method":"hook/completed","params":{"threadId":"thread-1","turnId":"turn-1","run":{"id":"hook-1","status":"completed"}}}`,
	} {
		sink := &agenttest.Sink{}
		agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
		agent.threadID = "thread-1"

		handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

		assert.Zero(t, sink.MessageCount(), input)
		assert.Zero(t, sink.NotificationCount(), input)
	}
}

func TestHandleCodexOutput_UnsuccessfulHookCompletionPersists(t *testing.T) {
	t.Parallel()

	for _, status := range []string{"failed", "blocked", "stopped"} {
		sink := &agenttest.Sink{}
		agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
		agent.threadID = "thread-1"
		input := fmt.Sprintf(`{"method":"hook/completed","params":{"threadId":"thread-1","turnId":"turn-1","run":{"id":"hook-1","status":%q,"statusMessage":"hook did not complete"}}}`, status)

		handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

		require.Equal(t, 1, sink.NotificationCount(), status)
		assert.Zero(t, sink.MessageCount(), status)
		assert.JSONEq(t, input, string(sink.LastNotification().Content))
	}
}

// Codex reports a cache WRITE beside the cache read, and it reached nothing: the
// breakdown showed a flat zero for it on every Codex turn while every other provider
// reported one.
func TestHandleCodexOutput_TokenUsageCarriesTheCacheWrite(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	agent.threadID = "thread-1"
	input := `{"method":"thread/tokenUsage/updated","params":{"threadId":"thread-1","tokenUsage":{"last":{"totalTokens":140,"inputTokens":100,"cachedInputTokens":20,"cacheWriteInputTokens":30,"outputTokens":40,"reasoningOutputTokens":12},"modelContextWindow":4096}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

	usage := sink.LastSessionInfo()["context_usage"].(map[string]interface{})
	assert.Equal(t, int64(30), usage["cache_creation_input_tokens"], "the cache write Codex reported")
	assert.Equal(t, int64(80), usage["input_tokens"], "the cached part alone comes off the input")
	assert.Equal(t, int64(20), usage["cache_read_input_tokens"])
	assert.Equal(t, int64(40), usage["output_tokens"], "reasoning is INSIDE the output, never a count beside it")
	assert.Equal(t, int64(140), usage["context_tokens"])
}

// The session's CUMULATIVE spend grows past the context window and answers a
// different question than the live occupancy, so the gauge must never read it.
func TestHandleCodexOutput_TokenUsageIgnoresTheCumulativeTotal(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	agent.threadID = "thread-1"
	input := `{"method":"thread/tokenUsage/updated","params":{"threadId":"thread-1","tokenUsage":{"total":{"totalTokens":999999,"inputTokens":900000,"outputTokens":99999},"last":{"totalTokens":23,"inputTokens":10,"cachedInputTokens":5,"outputTokens":7},"modelContextWindow":4096}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

	usage := sink.LastSessionInfo()["context_usage"].(map[string]interface{})
	assert.Equal(t, int64(23), usage["context_tokens"], "the LIVE request, not the session total")
	assert.Equal(t, int64(5), usage["input_tokens"])
}

// A frame with no total at all states no `context_tokens`, so the browser falls back
// to summing the counts rather than reading a zero as an empty context.
func TestHandleCodexOutput_TokenUsageOmitsAnAbsentTotal(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	agent.threadID = "thread-1"
	input := `{"method":"thread/tokenUsage/updated","params":{"threadId":"thread-1","tokenUsage":{"last":{"inputTokens":10,"cachedInputTokens":5,"outputTokens":7},"modelContextWindow":4096}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

	usage := sink.LastSessionInfo()["context_usage"].(map[string]interface{})
	_, stated := usage["context_tokens"]
	assert.False(t, stated, "no total reported, so none is broadcast")
}

func TestHandleCodexOutput_TokenUsageUpdatedFallsBackToModelContextWindow(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	agent.model = "gpt-5.4"
	agent.availableModels = codexDefaultModels
	agent.threadID = "thread-1"

	input := `{"method":"thread/tokenUsage/updated","params":{"threadId":"thread-1","turnId":"turn-1","tokenUsage":{"total":{"totalTokens":200,"inputTokens":100,"cachedInputTokens":25,"outputTokens":50,"reasoningOutputTokens":9},"last":{"totalTokens":23,"inputTokens":10,"cachedInputTokens":5,"outputTokens":7,"reasoningOutputTokens":1},"modelContextWindow":null}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

	info := sink.LastSessionInfo()
	usage, ok := info["context_usage"].(map[string]interface{})
	require.True(t, ok, "expected context_usage map, got %#v", info["context_usage"])
	require.Equal(t, int64(1_050_000), usage["context_window"])
}

func TestHandleCodexOutput_TokenUsageUpdatedIgnoresSubagentThreads(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	agent.threadID = "main-thread"

	input := `{"method":"thread/tokenUsage/updated","params":{"threadId":"child-thread","turnId":"turn-1","tokenUsage":{"total":{"totalTokens":200,"inputTokens":100,"cachedInputTokens":25,"outputTokens":50,"reasoningOutputTokens":9},"last":{"totalTokens":23,"inputTokens":10,"cachedInputTokens":5,"outputTokens":7,"reasoningOutputTokens":1},"modelContextWindow":4096}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

	require.Equal(t, 0, sink.NotificationCount())
	require.Equal(t, 0, sink.SessionInfoCount())
}

func TestHandleCodexOutput_TurnCompletedIgnoresSubagentThreads(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	agent.threadID = "main-thread"

	input := `{"method":"turn/completed","params":{"threadId":"child-thread","turn":{"id":"turn-1","status":"completed","items":[],"error":null}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

	require.Equal(t, 0, sink.MessageCount())
	require.Equal(t, 0, sink.ResetSpanCount())
	require.Equal(t, 0, sink.SessionInfoCount())
}

// TestHandleCodexOutput_TurnCompletedChildPersistsChildTurnEnd verifies a
// registered child thread's turn/completed persists a turn-end divider into the
// CHILD transcript (mirrors the main-thread PersistTurnEnd), rather than being
// a silent no-op. The child must be registered first via a spawnAgent
// item/started so the route can resolve the child agent ID.
func TestHandleCodexOutput_TurnCompletedChildPersistsChildTurnEnd(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	agent.threadID = "main-thread"

	// Register child-1 -> call-1 in the child route (spawnAgent item/started).
	spawnStarted := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-1","tool":"spawnAgent","status":"inProgress","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":"do work","model":"gpt-5.4","reasoningEffort":"medium","agentsStates":{}}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(spawnStarted)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"child-1","turn":{"id":"turn-1"}}}`)))

	childTurnCompleted := `{"method":"turn/completed","params":{"threadId":"child-1","turn":{"id":"turn-1","status":"completed","items":[],"error":null}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(childTurnCompleted)))

	// The turn-end divider lands in the CHILD transcript, not the parent's.
	child := sink.Child("child-of-call-1")
	turnEnds := 0
	for _, m := range child.Messages() {
		if m.TurnEnd {
			turnEnds++
		}
	}
	assert.Equal(t, 1, turnEnds,
		"child turn/completed must persist a turn-end divider into the child transcript")
	// publishTurnActive covers the MAIN thread alone, so a collab child's turn
	// is published against the CHILD's sink -- which is what the child tab's
	// own input queue follows.
	assert.Equal(t, []bool{true, false}, child.TurnActives(),
		"the child's own turn opens and releases the child input queue")
	assert.Equal(t, []leapmuxv1.AgentInputKind{
		leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_UNSPECIFIED,
		leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_UNSPECIFIED,
	}, child.TurnKinds(), "a Multi-Agent V2 child reports activity without direct-input steering")
}

func TestHandleCodexOutput_InterruptedChildTurnPersistsBufferedText(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	a.threadID = "main-thread"

	spawnStarted := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-1","tool":"spawnAgent","status":"inProgress","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":"do work","agentsStates":{}}}}`
	handleCodexOutput(a, providerkit.ParseLine([]byte(spawnStarted)))
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"child-1","turn":{"id":"turn-1"}}}`)))
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/agentMessage/delta","params":{"threadId":"child-1","itemId":"message-1","delta":"partial child answer"}}`)))
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"child-1","turn":{"id":"turn-1","status":"interrupted","items":[]}}}`)))

	child := sink.Child("child-of-call-1")
	require.NotEmpty(t, child.Messages())
	var bufferedMessage []byte
	for _, message := range child.Messages() {
		if strings.Contains(string(message.Content), "partial child answer") {
			bufferedMessage = message.Content
			break
		}
	}
	require.NotEmpty(t, bufferedMessage)
	assert.JSONEq(t, `{
		"type":"assembled_message",
		"kind":"text",
		"text":"partial child answer",
		"completion":"interrupted"
	}`, string(bufferedMessage))
	assert.Equal(t, agent.ProgressSnapshot{}, child.ProgressSnapshot(),
		"the completed child turn must not replay an active token count")
}

func TestFlushCodexGenerationHonorsDiscardOutput(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	a.generationBuffer.Append("message-1", agent.AssembledMessageKindText, "restart noise", providerkit.JoinVerbatim)
	a.DiscardOutput()
	a.flushCodexGeneration(agent.MessageCompletionInterrupted)

	assert.Empty(t, sink.Messages())
}

func TestCodexWaitMarksAnIntentionalStopAsInterrupted(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	a.SimulateExitForTest()
	a.SetStoppedForTest(true)
	a.generationBuffer.Append("message-1", agent.AssembledMessageKindText, "partial answer", providerkit.JoinVerbatim)

	require.NoError(t, a.Wait())
	require.Len(t, sink.Messages(), 1)
	assert.Contains(t, string(sink.Messages()[0].Content), `"completion":"interrupted"`)
}

func TestHandleCodexOutput_InterruptedTurnPersistsIncompleteCommandOutput(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	ag := newCodexAgentWithSink(agent.NewProviderServices(sink))
	ag.threadID = "main-thread"
	handleCodexOutput(ag, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"commandExecution","id":"command-1","status":"inProgress","command":"printf partial"}}}`)))
	handleCodexOutput(ag, providerkit.ParseLine([]byte(`{"method":"item/commandExecution/outputDelta","params":{"itemId":"command-1","delta":"partial output"}}`)))
	handleCodexOutput(ag, providerkit.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"interrupted","items":[]}}}`)))

	require.GreaterOrEqual(t, sink.MessageCount(), 3)
	result := sink.Messages()[1]
	assert.True(t, result.Closing)
	// The row is the agent's own item/started frame, byte for byte. The output is a
	// run of delta events that LeapMux joined, so the joined text is recovered
	// provider data and rides in the supplement.
	assert.JSONEq(t, `{
		"threadId":"main-thread",
		"turnId":"turn-1",
		"item":{"type":"commandExecution","id":"command-1","status":"inProgress","command":"printf partial"}
	}`, string(result.Content))
	assert.JSONEq(t, `{
		"itemId":"command-1",
		"itemType":"commandExecution",
		"aggregatedOutput":"partial output"
	}`, string(result.SupplementalContent))
	assert.Equal(t, agent.MessageCompletionInterrupted, result.Completion)

	// Both halves read back as one item, so every extractor sees the output.
	assert.JSONEq(t, `{
		"threadId":"main-thread",
		"turnId":"turn-1",
		"item":{"type":"commandExecution","id":"command-1","status":"inProgress","command":"printf partial","aggregatedOutput":"partial output"}
	}`, string(Registration().Plugin.ResolveProviderData(agent.MessageContent{
		Original: result.Content, Supplemental: result.SupplementalContent,
	})))
}

// A supplement that identifies another item cannot reach this row's output.
func TestCodexResolveProviderData_RefusesASupplementForAnotherItem(t *testing.T) {
	t.Parallel()

	original := []byte(`{"item":{"type":"commandExecution","id":"command-1","command":"ls"}}`)
	for _, supplement := range []string{
		`{"itemId":"command-2","itemType":"commandExecution","aggregatedOutput":"other"}`,
		`{"itemId":"command-1","itemType":"fileChange","aggregatedOutput":"other"}`,
		`{"itemId":"command-1","itemType":"commandExecution"}`,
	} {
		assert.JSONEq(t, string(original),
			string(Registration().Plugin.ResolveProviderData(agent.MessageContent{
				Original: original, Supplemental: []byte(supplement),
			})), supplement)
	}
}

func TestHandleCodexOutput_InterruptedReasoningPreservesSummaryParts(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	agent.threadID = "main-thread"
	for _, raw := range []string{
		`{"method":"item/reasoning/summaryTextDelta","params":{"threadId":"main-thread","itemId":"reason-1","summaryIndex":0,"delta":"**Verifying terminal release synchronization"}}`,
		`{"method":"item/reasoning/summaryPartAdded","params":{"threadId":"main-thread","itemId":"reason-1","summaryIndex":1}}`,
		`{"method":"item/reasoning/summaryTextDelta","params":{"threadId":"main-thread","itemId":"reason-1","summaryIndex":1,"delta":"Analyzing lock acquisition order and concurrency**"}}`,
		`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"interrupted","items":[]}}}`,
	} {
		handleCodexOutput(agent, providerkit.ParseLine([]byte(raw)))
	}

	require.NotEmpty(t, sink.Messages())
	assert.Contains(t, string(sink.Messages()[0].Content),
		`"text":"**Verifying terminal release synchronization\n\nAnalyzing lock acquisition order and concurrency**"`)
}

func TestHandleCodexOutput_DuplicateSummaryPartDoesNotAddAParagraph(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	agent.threadID = "main-thread"
	for _, raw := range []string{
		`{"method":"item/reasoning/summaryTextDelta","params":{"threadId":"main-thread","itemId":"reason-1","summaryIndex":0,"delta":"one"}}`,
		`{"method":"item/reasoning/summaryPartAdded","params":{"threadId":"main-thread","itemId":"reason-1","summaryIndex":0}}`,
		`{"method":"item/reasoning/summaryTextDelta","params":{"threadId":"main-thread","itemId":"reason-1","summaryIndex":0,"delta":"two"}}`,
		`{"method":"item/reasoning/summaryPartAdded","params":{"threadId":"main-thread","itemId":"reason-1","summaryIndex":1}}`,
		`{"method":"item/reasoning/summaryTextDelta","params":{"threadId":"main-thread","itemId":"reason-1","summaryIndex":1,"delta":"three"}}`,
		`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"interrupted","items":[]}}}`,
	} {
		handleCodexOutput(agent, providerkit.ParseLine([]byte(raw)))
	}

	require.NotEmpty(t, sink.Messages())
	assert.Contains(t, string(sink.Messages()[0].Content), `"text":"onetwo\n\nthree"`)
}

func TestHandleCodexOutput_KeepsReasoningTextDeltasVerbatim(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	agent.threadID = "main-thread"
	for _, raw := range []string{
		`{"method":"item/reasoning/textDelta","params":{"threadId":"main-thread","itemId":"reason-1","delta":"**Verifying terminal release synchronization"}}`,
		`{"method":"item/reasoning/textDelta","params":{"threadId":"main-thread","itemId":"reason-1","delta":"Analyzing lock acquisition order and concurrency**"}}`,
		`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"interrupted","items":[]}}}`,
	} {
		handleCodexOutput(agent, providerkit.ParseLine([]byte(raw)))
	}

	require.NotEmpty(t, sink.Messages())
	assert.Contains(t, string(sink.Messages()[0].Content),
		`"text":"**Verifying terminal release synchronizationAnalyzing lock acquisition order and concurrency**"`)
}

func TestHandleCodexOutput_InterruptedReasoningPrefersLateSummary(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	agent.threadID = "main-thread"
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/reasoning/textDelta","params":{"threadId":"main-thread","itemId":"reason-1","delta":"raw detail"}}`)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/reasoning/summaryTextDelta","params":{"threadId":"main-thread","itemId":"reason-1","summaryIndex":0,"delta":"summary"}}`)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"interrupted","items":[]}}}`)))

	require.NotEmpty(t, sink.Messages())
	assert.Contains(t, string(sink.Messages()[0].Content), `"text":"summary"`)
	assert.NotContains(t, string(sink.Messages()[0].Content), "raw detail")
}

func TestHandleCodexOutput_InterruptedMCPItemGetsAClosingRowAndToolCount(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	a.threadID = "main-thread"
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","item":{"type":"mcpToolCall","id":"mcp-1","status":"inProgress","server":"docs","tool":"search"}}}`)))
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"completed","items":[]}}}`)))

	require.GreaterOrEqual(t, sink.MessageCount(), 3)
	assert.True(t, sink.Messages()[1].Closing)
	assert.Equal(t, agent.MessageCompletionInterrupted, sink.Messages()[1].Completion)
	assert.Contains(t, string(sink.Messages()[2].Metadata), `"num_tool_uses":1`)
}

func TestCodexTurnCounterPreservesOriginalBytes(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	a.threadID = "main-thread"
	a.TurnToolUses = 2
	raw := json.RawMessage(`{"threadId":"main-thread", "turn":{"id":"turn-1","status":"completed","items":[]}, "future":9007199254740993}`)
	a.handleTurnCompleted(raw)
	messages := sink.Messages()
	require.NotEmpty(t, messages)
	last := messages[len(messages)-1]
	assert.Equal(t, []byte(raw), last.Content)
	assert.Contains(t, string(last.Metadata), `"num_tool_uses":2`)
}

func TestHandleCodexOutput_LateChildRegistrationMovesBufferedText(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	agent.threadID = "main-thread"
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/agentMessage/delta","params":{"threadId":"child-1","itemId":"message-1","delta":"early child text"}}`)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"main-thread","item":{"type":"collabAgentToolCall","id":"spawn-1","tool":"spawnAgent","status":"completed","receiverThreadIds":["child-1"],"prompt":"work","agentsStates":{"child-1":{"status":"running"}}}}}`)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"child-1","turn":{"id":"child-turn","status":"interrupted","items":[]}}}`)))

	for _, message := range sink.Messages() {
		assert.NotContains(t, string(message.Content), "early child text")
	}
	child := sink.Child("child-of-spawn-1")
	combined := ""
	for _, message := range child.Messages() {
		combined += string(message.Content)
	}
	assert.Contains(t, combined, "early child text")
}

func TestHandleCodexOutput_CompletedItemDiscardsTheIdlessDeltaFallback(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	agent.threadID = "main-thread"
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/agentMessage/delta","params":{"threadId":"main-thread","delta":"complete answer"}}`)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"main-thread","item":{"type":"agentMessage","id":"message-1","text":"complete answer"}}}`)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"completed","items":[]}}}`)))

	assembledRows := 0
	for _, message := range sink.Messages() {
		var value struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(message.Content, &value) == nil && value.Type == contracts.AssembledMessageType {
			assembledRows++
		}
	}
	assert.Zero(t, assembledRows, "the completed provider item must remain the only assistant row")
}

func TestHandleCodexOutput_TurnCompletedPlanModePersistsRealPlanAndPrompts(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	agent.threadID = "main-thread"
	agent.collaborationMode = CollaborationPlan

	planStarted := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"plan","id":"plan-1"}}}`
	planCompleted := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"plan","id":"plan-1","text":"# Design Doc: Rendering fixes\n\n- first\n"}}}`
	turnCompleted := `{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"completed","items":[],"error":null}}}`

	handleCodexOutput(agent, providerkit.ParseLine([]byte(planStarted)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(planCompleted)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(turnCompleted)))

	require.Equal(t, 1, sink.PlanUpdateCount())
	plan := sink.LastPlanUpdate()
	decoded, err := msgcodec.Decompress(plan.Content, plan.Compression)
	require.NoError(t, err)
	require.Equal(t, "# Design Doc: Rendering fixes\n\n- first\n", string(decoded))
	require.Equal(t, "Rendering fixes", plan.Title)
	require.Equal(t, 1, sink.PublishedControlCount())
}

// Two plan turns share ONE stored plan, because UpdatePlan replaces it. The card
// is keyed by TURN, so a second plan turn leaves two cards open -- and the composer
// renders the OLDEST, so the newer one is invisible. Approving the card the reader
// can see would then execute the plan it never showed.
func TestHandleCodexOutput_ASecondPlanPromptRetiresTheFirst(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	agent.threadID = "main-thread"
	agent.collaborationMode = CollaborationPlan

	for _, turn := range []string{"turn-1", "turn-2"} {
		handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"`+turn+`","item":{"type":"plan","id":"plan-`+turn+`"}}}`)))
		handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"main-thread","turnId":"`+turn+`","item":{"type":"plan","id":"plan-`+turn+`","text":"# Plan `+turn+`\n\n- step\n"}}}`)))
		handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"`+turn+`","status":"completed","items":[],"error":null}}}`)))
	}

	require.Equal(t, 2, sink.PublishedControlCount())
	assert.Equal(t, []string{"codex-plan-prompt-turn-1"}, sink.CanceledControls(),
		"the superseded card would have approved the second turn's plan")
}

func TestHandleCodexOutput_TurnCompletedPlanModeIgnoresAssistantTextWithoutPlanItem(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	agent.threadID = "main-thread"
	agent.collaborationMode = CollaborationPlan

	assistantCompleted := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"agentMessage","id":"msg-1","text":"Revised plan:\n- not a real plan item"}}}`
	turnCompleted := `{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"completed","items":[],"error":null}}}`

	handleCodexOutput(agent, providerkit.ParseLine([]byte(assistantCompleted)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(turnCompleted)))

	require.Equal(t, 0, sink.PlanUpdateCount())
	require.Equal(t, 0, sink.PublishedControlCount())
}

func TestHandleCodexOutput_TurnCompletedPlanModeWithoutRealPlanDoesNotPrompt(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	agent.threadID = "main-thread"
	agent.collaborationMode = CollaborationPlan

	turnCompleted := `{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"completed","items":[],"error":null}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(turnCompleted)))

	require.Equal(t, 0, sink.PlanUpdateCount())
	require.Equal(t, 0, sink.PublishedControlCount())
}

func TestHandleCodexOutput_TurnCompletedPlanModeWithEmptyPlanTextDoesNotPersist(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	agent.threadID = "main-thread"
	agent.collaborationMode = CollaborationPlan

	planCompleted := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"plan","id":"plan-1"}}}`
	turnCompleted := `{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"completed","items":[],"error":null}}}`

	handleCodexOutput(agent, providerkit.ParseLine([]byte(planCompleted)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(turnCompleted)))

	require.Equal(t, 0, sink.PlanUpdateCount())
	require.Equal(t, 0, sink.PublishedControlCount())
}

func TestHandleCodexOutput_AgentMessageDeltaAccumulatesThinkingTokens(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))

	// 8-char delta -> 8/4 = 2 tokens; a second 8-char delta accumulates to
	// 16/4 = 4. The estimate climbs cumulatively across deltas of the same phase.
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/agentMessage/delta","params":{"delta":"abcdefgh"}}`)))
	assert.Equal(t, int64(2), sink.LastThinkingTokens())
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/agentMessage/delta","params":{"delta":"ijklmnop"}}`)))
	assert.Equal(t, int64(4), sink.LastThinkingTokens())

	// A live estimate is broadcast, never persisted to the timeline.
	assert.Equal(t, 0, sink.MessageCount(), "thinking_tokens deltas must not persist")
	assert.Equal(t, 0, sink.NotificationCount())
}

func TestHandleCodexOutput_ReasoningAndPlanDeltasAccumulateThinkingTokens(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		input string
	}{
		{"reasoning text", `{"method":"item/reasoning/textDelta","params":{"itemId":"r1","delta":"%s"}}`},
		{"reasoning summary", `{"method":"item/reasoning/summaryTextDelta","params":{"itemId":"r1","delta":"%s"}}`},
		{"plan", `{"method":"item/plan/delta","params":{"delta":"%s"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &agenttest.Sink{}
			agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
			agent.threadID = "main-thread"

			handleCodexOutput(agent, providerkit.ParseLine([]byte(fmt.Sprintf(tc.input, "abcdefgh"))))
			assert.Equal(t, int64(2), sink.LastThinkingTokens())
			handleCodexOutput(agent, providerkit.ParseLine([]byte(fmt.Sprintf(tc.input, "ijklmnop"))))
			assert.Equal(t, int64(4), sink.LastThinkingTokens())
		})
	}
}

func TestHandleCodexOutput_ReasoningSummaryAndRawCountOncePerItem(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	agent.threadID = "main-thread"

	// Codex can stream BOTH a summary and the raw reasoning for one reasoning item
	// (same itemId, same generation surfaced twice). The summary arrives first and
	// locks item r1 onto "summary" (8 chars -> 2 tokens).
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/reasoning/summaryTextDelta","params":{"itemId":"r1","delta":"abcdefgh","threadId":"main-thread"}}`)))
	require.Equal(t, int64(2), sink.LastThinkingTokens())

	// Raw reasoning for the SAME item must NOT be counted -- it would double the
	// estimate for the same underlying generation.
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/reasoning/textDelta","params":{"itemId":"r1","delta":"ZZZZZZZZZZZZZZZZ","threadId":"main-thread"}}`)))
	assert.Equal(t, int64(2), sink.LastThinkingTokens(), "raw reasoning for the locked item is not double-counted")

	// More summary deltas for the locked item still climb (16 chars -> 4 tokens).
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/reasoning/summaryTextDelta","params":{"itemId":"r1","delta":"ijklmnop","threadId":"main-thread"}}`)))
	assert.Equal(t, int64(4), sink.LastThinkingTokens())

	// A different reasoning item locks independently: raw arrives first for r2.
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/reasoning/textDelta","params":{"itemId":"r2","delta":"qrstuvwx","threadId":"main-thread"}}`)))
	assert.Equal(t, int64(6), sink.LastThinkingTokens(), "a separate item counts its own first-seen kind (24 chars -> 6)")
}

func TestHandleCodexOutput_ReasoningCountsWhicheverStreamArrivesFirst(t *testing.T) {
	t.Parallel()

	// A model that streams only one reasoning kind must still move the counter, so
	// the estimator locks onto whichever arrives first rather than preferring one.
	for _, tc := range []struct {
		name   string
		method string
	}{
		{"raw only", "item/reasoning/textDelta"},
		{"summary only", "item/reasoning/summaryTextDelta"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &agenttest.Sink{}
			agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
			agent.threadID = "main-thread"

			handleCodexOutput(agent, providerkit.ParseLine([]byte(fmt.Sprintf(
				`{"method":%q,"params":{"itemId":"r1","delta":"abcdefgh","threadId":"main-thread"}}`, tc.method))))
			assert.Equal(t, int64(2), sink.LastThinkingTokens(), "a single reasoning stream still counts")
		})
	}
}

func TestHandleCodexOutput_ReasoningItemCompletedReleasesStreamLock(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	agent.threadID = "main-thread"

	// Item r1 locks onto "summary".
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/reasoning/summaryTextDelta","params":{"itemId":"r1","delta":"abcdefgh","threadId":"main-thread"}}`)))
	require.Equal(t, int64(2), sink.LastThinkingTokens())

	// Completing the reasoning item resets the estimate (a main-scope AGENT commit)
	// AND releases r1's stream-kind lock so the map does not grow.
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"main-thread","item":{"type":"reasoning","id":"r1","summary":[{"text":"done"}]}}}`)))
	agent.Mu.Lock()
	_, stillLocked := agent.reasoningStreamKind[codexReasoningKey("main-thread", "r1")]
	agent.Mu.Unlock()
	assert.False(t, stillLocked, "the completed item's stream-kind lock is released")
}

func TestHandleCodexOutput_ItemCompletedResetsThinkingTokens(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	agent.threadID = "main-thread"

	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/agentMessage/delta","params":{"delta":"abcdefghijklmnop"}}`)))
	require.Equal(t, int64(4), sink.LastThinkingTokens())

	// Committing the message is a phase boundary the frontend clears on; the
	// backend estimate must reset so the next phase restarts from zero.
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"main-thread","item":{"type":"agentMessage","id":"m1","text":"hi"}}}`)))

	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/agentMessage/delta","params":{"delta":"abcdefgh"}}`)))
	assert.Equal(t, int64(2), sink.LastThinkingTokens(), "next phase restarts at 8/4, not the cumulative total")
}

func TestHandleCodexOutput_ItemStartedAndApprovalResetThinkingTokens(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		reset string
	}{
		// A started tool item commits an AGENT message the frontend clears on.
		{"item started", `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"commandExecution","id":"cmd-1","status":"inProgress","command":"ls","cwd":"/tmp","processId":"123","commandActions":[]}}}`},
		// An approval request is a live control request the frontend clears on.
		{"approval request", `{"jsonrpc":"2.0","id":7,"method":"item/commandExecution/requestApproval","params":{"command":"ls","reason":"x"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &agenttest.ControlSink{}
			agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
			agent.threadID = "main-thread"

			handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/agentMessage/delta","params":{"delta":"abcdefghijklmnop"}}`)))
			require.Equal(t, int64(4), sink.LastThinkingTokens())

			handleCodexOutput(agent, providerkit.ParseLine([]byte(tc.reset)))

			handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/agentMessage/delta","params":{"delta":"abcdefgh"}}`)))
			assert.Equal(t, int64(2), sink.LastThinkingTokens(), "the boundary restarts the estimate")
		})
	}
}

func TestHandleCodexOutput_ChildThreadTurnStartedDoesNotResetThinkingTokens(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink)) // threadID = "main-thread"

	// Accumulate a main-thread estimate (16 chars -> 4 tokens).
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/agentMessage/delta","params":{"delta":"abcdefghijklmnop","threadId":"main-thread"}}`)))
	require.Equal(t, int64(4), sink.LastThinkingTokens())

	// A collab subagent's turn/started carries its own child threadId. It must NOT
	// reset the primary agent's estimate -- the frontend has no counter clear for
	// turn/started, so an ungated reset would spin the odometer backward mid-phase.
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"child-1","turn":{"id":"child-turn"}}}`)))

	// The next main-thread delta keeps climbing from the cumulative total (24/4=6),
	// proving the child-thread turn/started did not reset the estimate.
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/agentMessage/delta","params":{"delta":"qrstuvwx","threadId":"main-thread"}}`)))
	assert.Equal(t, int64(6), sink.LastThinkingTokens(), "a child-thread turn/started must not reset the primary estimate")
}

func TestHandleCodexOutput_ChildThreadTurnStartedDoesNotReplaceInterruptTurn(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))

	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"main-turn"}}}`)))
	require.Equal(t, []bool{true}, sink.TurnActives())

	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"child-1","turn":{"id":"child-turn"}}}`)))

	agent.Mu.Lock()
	turnID := agent.turnID
	agent.Mu.Unlock()
	assert.Equal(t, "main-turn", turnID, "interrupts and steering must keep targeting the active main-thread turn")
	// A collab child's run is its background-task registry row, which the Worker
	// reads for the child tab. It never touches the root's turn, so the root
	// publishes nothing here.
	assert.Equal(t, []bool{true}, sink.TurnActives(), "a child turn must not republish the root's turn state")
}

func TestHandleCodexOutput_TurnStartedClearsReasoningStreamLocksOnMainThreadOnly(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		turnStarted string
		wantCleared bool
	}{
		{"main thread clears", `{"method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"turn-2"}}}`, true},
		{"child thread keeps", `{"method":"turn/started","params":{"threadId":"child-1","turn":{"id":"child-turn"}}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &agenttest.Sink{}
			agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
			agent.threadID = "main-thread"

			// Lock reasoning item r1 onto its first-seen sub-stream kind.
			handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/reasoning/summaryTextDelta","params":{"itemId":"r1","delta":"abcdefgh","threadId":"main-thread"}}`)))
			agent.Mu.Lock()
			key := codexReasoningKey("main-thread", "r1")
			_, locked := agent.reasoningStreamKind[key]
			agent.Mu.Unlock()
			require.True(t, locked, "the reasoning item is locked before the turn boundary")

			// A new turn on the main thread drops stale per-item locks so itemIds can't
			// leak across turns (e.g. a reasoning item left open by an abort); a
			// child-thread turn/started belongs to a collab subagent and must leave the
			// primary agent's locks intact, mirroring the main-thread gate on the reset.
			handleCodexOutput(agent, providerkit.ParseLine([]byte(tc.turnStarted)))

			agent.Mu.Lock()
			_, stillLocked := agent.reasoningStreamKind[key]
			agent.Mu.Unlock()
			assert.Equal(t, !tc.wantCleared, stillLocked)
		})
	}
}

func TestHandleCodexOutput_ReasoningItemCompletedResetsThinkingTokens(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	agent.threadID = "main-thread"

	// Reasoning streams and climbs (16 chars -> 4 tokens).
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/reasoning/textDelta","params":{"itemId":"r1","delta":"abcdefghijklmnop","threadId":"main-thread"}}`)))
	require.Equal(t, int64(4), sink.LastThinkingTokens())

	// Codex has no explicit reasoning->answer hand-off (unlike ACP); it relies on
	// the reasoning item/completed persisting an AGENT message the frontend clears
	// on, which the decorator mirrors with a reset. Without it, the reasoning chars
	// would stack onto the following answer's count.
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"main-thread","item":{"type":"reasoning","id":"r1","summary":[{"text":"done"}]}}}`)))

	// The answer phase restarts from zero (8/4 = 2), not the reasoning total.
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/agentMessage/delta","params":{"delta":"abcdefgh","threadId":"main-thread"}}`)))
	assert.Equal(t, int64(2), sink.LastThinkingTokens(), "reasoning item/completed resets so the answer restarts from zero")
}

func TestHandleCodexOutput_TurnBoundariesResetThinkingTokens(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		reset string
	}{
		{"turn started", `{"method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"turn-2"}}}`},
		{"turn completed", `{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"completed","items":[],"error":null}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &agenttest.Sink{}
			agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
			agent.threadID = "main-thread"

			handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/agentMessage/delta","params":{"delta":"abcdefghijklmnop"}}`)))
			require.Equal(t, int64(4), sink.LastThinkingTokens())

			handleCodexOutput(agent, providerkit.ParseLine([]byte(tc.reset)))

			handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/agentMessage/delta","params":{"delta":"abcdefgh"}}`)))
			assert.Equal(t, int64(2), sink.LastThinkingTokens(), "the new turn restarts the estimate")
		})
	}
}

// TestCodexCollabStatusToRegistry_ResumableInterrupted verifies that a Codex
// collab "interrupted" status maps to a NON-final registry state. An
// interrupted child is resumable (resumeAgent restarts it), so it must not
// collapse to the final StatusInterrupted -- the registry's monotonic-final
// guard would then absorb the later "running" upsert on resume and leave the row
// stuck at Interrupted forever. StatusInterrupted is reserved for the boot sweep
// that marks tasks left active by a crashed worker.
func TestCodexCollabStatusToRegistry_ResumableInterrupted(t *testing.T) {
	t.Parallel()

	cases := []struct {
		status       string
		wantStatus   bgtask.Status
		wantFinal    bool
		wantNonBlank bool
	}{
		{"running", bgtask.StatusRunning, false, false},
		{"pendingInit", bgtask.StatusRunning, false, false},
		{"completed", bgtask.StatusCompleted, true, false},
		{"errored", bgtask.StatusFailed, true, false},
		{"notFound", bgtask.StatusFailed, true, false},
		{"shutdown", bgtask.StatusStopped, true, false},
		// The fix: interrupted stays Running (resumable), NOT final.
		{"interrupted", bgtask.StatusRunning, false, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.status, func(t *testing.T) {
			t.Parallel()
			transition := codexCollabTransition(tc.status)
			assert.Equal(t, tc.wantStatus, transition.status)
			assert.Equal(t, tc.wantFinal, transition.finished())
			if tc.wantNonBlank {
				assert.NotEmpty(t, transition.activity, "resumable interrupted carries a paused activity line")
			}
			// The mapped status must NOT be final for a resumable interrupt.
			if !tc.wantFinal {
				assert.False(t, transition.status.IsFinished(), "%s must not map to a final status", tc.status)
			}
		})
	}
}

func TestCodexChildTurnRegistryStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		providerStatus string
		wantStatus     bgtask.Status
		wantFinished   bool
		wantActivity   string
	}{
		{providerStatus: "completed", wantStatus: bgtask.StatusCompleted, wantFinished: true},
		{providerStatus: "failed", wantStatus: bgtask.StatusFailed, wantFinished: true},
		{providerStatus: "cancelled", wantStatus: bgtask.StatusRunning, wantActivity: "paused"},
		{providerStatus: "canceled", wantStatus: bgtask.StatusRunning, wantActivity: "paused"},
		{providerStatus: "interrupted", wantStatus: bgtask.StatusRunning, wantActivity: "paused"},
		{providerStatus: "aborted", wantStatus: bgtask.StatusRunning, wantActivity: "paused"},
		{providerStatus: "futureStatus", wantStatus: bgtask.StatusRunning},
	}
	for _, test := range tests {
		test := test
		t.Run(test.providerStatus, func(t *testing.T) {
			t.Parallel()
			params := json.RawMessage(`{"turn":{"status":"` + test.providerStatus + `"}}`)
			transition := codexChildTurnTransition(params)
			assert.Equal(t, test.wantStatus, transition.status)
			assert.Equal(t, test.wantFinished, transition.finished())
			assert.Equal(t, test.wantActivity, transition.activity)
		})
	}

	transition := codexChildTurnTransition(json.RawMessage(`{"turn":`))
	assert.Equal(t, bgtask.StatusRunning, transition.status)
	assert.False(t, transition.finished())
	assert.Empty(t, transition.activity)
}

// Codex uses the activity item as its own liveness signal. Interrupted stops
// liveness just like completed, so the registry must release the active count.
func TestCodexSubAgentActivity_InterruptedFailsTheRun(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	started := json.RawMessage(`{"type":"subAgentActivity","id":"spawn-1","agentThreadId":"thr-1","agentPath":"/root/reviewer","kind":"started"}`)
	assert.True(t, a.handleCodexSubAgentActivity(started, "main-thread"))
	item := json.RawMessage(`{"type":"subAgentActivity","id":"interrupt-1","agentThreadId":"thr-1","agentPath":"/root/reviewer","kind":"interrupted"}`)
	assert.True(t, a.handleCodexSubAgentActivity(item, "main-thread"), "consumed the activity item")
	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, bgtask.StatusFailed, rows[0].Status)
	assert.True(t, rows[0].Status.IsFinished(), "the interrupted run no longer counts as active")
}

// subAgentActivity is the THIRD writer that reports a collab child active again,
// and it had no coverage for the reopen at all. The upsert absorbs a non-final
// status against a final row, so without the reopen an `interacted` lands its
// "received input" activity on a row that still reads "completed" -- the sidebar
// then shows a finished subagent that just took a message.
func TestHandleCodexOutput_ASubAgentActivityReopensAFinishedChild(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))

	started := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-1","tool":"spawnAgent","status":"inProgress","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":"do work","model":"gpt-5.4","reasoningEffort":"medium","agentsStates":{}}}}`
	completed := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-1","tool":"spawnAgent","status":"completed","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":"do work","model":"gpt-5.4","reasoningEffort":"medium","agentsStates":{"child-1":{"status":"completed","message":"done"}}}}}`
	resumed := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn2","item":{"type":"collabAgentToolCall","id":"call-2","tool":"resumeAgent","status":"inProgress","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":"more work","model":"gpt-5.4","reasoningEffort":"medium","agentsStates":{}}}}`
	interacted := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn2","item":{"type":"subAgentActivity","agentThreadId":"child-1","kind":"interacted"}}}`

	handleCodexOutput(agent, providerkit.ParseLine([]byte(started)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(completed)))
	_, status, ok, _ := sink.LookupBackgroundTask("child-1")
	require.True(t, ok)
	require.True(t, status.IsFinished(), "the first run closed the row")

	handleCodexOutput(agent, providerkit.ParseLine([]byte(resumed)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(interacted)))

	assert.Equal(t, []string{"child-1"}, sink.RevivedTasks(), "the activity reopens the re-registered child")
	_, status, ok, _ = sink.LookupBackgroundTask("child-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, status)
}

// A V2 interaction is itself live restart evidence. It does not need a legacy
// resumeAgent collab item before it can reopen the durable child route.
func TestHandleCodexOutput_ASubAgentActivityReopensWithoutALegacyCollabItem(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))

	started := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-1","tool":"spawnAgent","status":"inProgress","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":"do work","model":"gpt-5.4","reasoningEffort":"medium","agentsStates":{}}}}`
	completed := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-1","tool":"spawnAgent","status":"completed","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":"do work","model":"gpt-5.4","reasoningEffort":"medium","agentsStates":{"child-1":{"status":"completed","message":"done"}}}}}`
	interacted := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn2","item":{"type":"subAgentActivity","agentThreadId":"child-1","kind":"interacted"}}}`

	handleCodexOutput(agent, providerkit.ParseLine([]byte(started)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(completed)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(interacted)))

	assert.Equal(t, []string{"child-1"}, sink.RevivedTasks(), "the activity reopens the child")
	_, status, ok, _ := sink.LookupBackgroundTask("child-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, status)
}

func TestHandleCodexOutput_ChildTurnEndSurvivesALostSpanIndex(t *testing.T) {
	t.Parallel()

	// The child's turn end is the only event that clears its input queue. The
	// durable child ID must still route that event when the spawn correlation is
	// missing. Otherwise, the child holds all later input behind a stale turn.
	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	agent.threadID = "main-thread"

	spawnStarted := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-1","tool":"spawnAgent","status":"inProgress","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":"do work","model":"gpt-5.4","reasoningEffort":"medium","agentsStates":{}}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(spawnStarted)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"child-1","turn":{"id":"turn-1"}}}`)))
	child := sink.Child("child-of-call-1")
	require.Equal(t, []bool{true}, child.TurnActives(), "the child's turn opened")

	// Lose only the spawn correlation. The durable child ID still routes the
	// turn end, which is the split state this regression covers.
	agent.Mu.Lock()
	state := agent.collabChildren["child-1"]
	state.spawnCorrelationID = ""
	agent.Mu.Unlock()

	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"child-1","turn":{"id":"turn-1","status":"completed","items":[],"error":null}}}`)))

	assert.Equal(t, []bool{true, false}, child.TurnActives(),
		"the child's turn still ends, so its input queue is released")
}

func TestCodexResolveProviderData_PreservesUnknownFieldsAndLargeNumbers(t *testing.T) {
	t.Parallel()
	resolved := Registration().Plugin.ResolveProviderData(agent.MessageContent{
		Original:     []byte(`{"item":{"id":"call","type":"commandExecution","counter":9007199254740993,"_leapmux":"provider"}}`),
		Supplemental: []byte(`{"itemId":"call","itemType":"commandExecution","aggregatedOutput":"partial"}`),
	})
	assert.Contains(t, string(resolved), `"counter":9007199254740993`)
	assert.Contains(t, string(resolved), `"_leapmux":"provider"`)
	assert.Contains(t, string(resolved), `"aggregatedOutput":"partial"`)
}

func TestCodexResolveProviderData_KeepsAFrameItCannotRead(t *testing.T) {
	t.Parallel()
	for _, original := range []string{`null`, `{}`, `{"item":null}`, `{"item":[]}`, `not json`} {
		assert.Equal(t, original,
			string(Registration().Plugin.ResolveProviderData(agent.MessageContent{
				Original:     []byte(original),
				Supplemental: []byte(`{"itemId":"call","itemType":"commandExecution","aggregatedOutput":"partial"}`),
			})), original)
	}
}

// A call that produced nothing still closes its span, and its row is the frame
// alone. An earlier build dropped the row when it could not build one, which left
// the tool card open for good.
func TestHandleCodexOutput_InterruptedToolWithNoOutputStoresTheFrameAlone(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	agent.threadID = "main-thread"
	started := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"commandExecution","id":"command-1","status":"inProgress","command":"printf partial"}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(started)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"interrupted","items":[]}}}`)))

	require.GreaterOrEqual(t, sink.MessageCount(), 2)
	result := sink.Messages()[1]
	assert.True(t, result.Closing)
	assert.Equal(t, "command-1", result.SpanID)
	assert.Empty(t, result.SupplementalContent, "no output means no recovered data")
	assert.JSONEq(t, `{
		"threadId":"main-thread",
		"turnId":"turn-1",
		"item":{"type":"commandExecution","id":"command-1","status":"inProgress","command":"printf partial"}
	}`, string(result.Content))
}

// An inbound REQUEST the dispatcher does not recognize needs an answer. Codex
// sends its approval requests as JSON-RPC requests with an "id", so one whose
// method is unknown arrives here carrying a runtime that WAITS -- and a transcript
// row alone left it waiting for its own timeout.
//
// Codex was the fourth JSON-RPC dispatcher, and the only one that answered nothing
// at all until the refusal moved onto JSONRPCProcess.
func TestHandleCodexOutput_AnswersAnUnsupportedRequest(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	stdin := &agenttest.Stdin{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	agent.SetStdinForTest(agenttest.NopStdin(stdin))

	raw := []byte(`{"jsonrpc":"2.0","id":9007199254740993,"method":"item/somethingNew","params":{}}`)
	handleCodexOutput(agent, providerkit.ParseLine(raw))

	// The answer is written on its OWN goroutine, so that a reply to a runtime that
	// is not draining its stdin cannot stall the loop that must keep draining its
	// stdout.
	var answer string
	require.Eventually(t, func() bool {
		answer = stdin.String()
		return strings.Contains(answer, `"code":-32601`)
	}, 2*time.Second, 5*time.Millisecond, "an unsupported Codex request must be answered")
	assert.Contains(t, answer, "Method not supported: item/somethingNew")
	assert.Contains(t, answer, `"id":9007199254740993`,
		"the identifier returns exactly as it arrived, above the range a float keeps")
	require.Len(t, sink.Messages(), 1, "the frame still reaches the transcript")
}

// A NOTIFICATION carries no id and needs no answer. Writing one would put a
// response with a null identifier on the wire, which the runtime cannot route.
func TestHandleCodexOutput_AnswersNoUnsupportedNotification(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	stdin := &agenttest.Stdin{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	agent.SetStdinForTest(agenttest.NopStdin(stdin))

	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","method":"item/somethingNew","params":{}}`)))

	require.Len(t, sink.Messages(), 1)
	assert.Empty(t, stdin.String(), "a notification needs no answer")
}

// An interrupt that FAILS must leave the reader a control that can still answer.
//
// Codex's four approval methods register a nil cancel answer, because Codex retires
// its own approval requests. Withdrawing one therefore sends the CLI nothing and only
// deletes the reader's card -- so retiring them BEFORE turn/interrupt left a refused
// or timed-out interrupt with the CLI still blocked on an approval and the card
// already gone. Nothing could answer it and the thinking indicator never stopped.
func TestCodexInterruptKeepsAnApprovalCardWhenTheInterruptFails(t *testing.T) {
	sink := &agenttest.ControlSink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	// A closed pipe: the turn/interrupt request cannot be written, so Interrupt fails
	// exactly as it does when the app-server refuses it or the request times out.
	a.SetContextForTest(context.Background())
	a.SetStdinForTest(agenttest.NopStdin(refusingWriter{}))
	a.Mu.Lock()
	a.threadID, a.turnID = "thread", "turn"
	a.Mu.Unlock()

	a.HandleOutput([]byte(`{"jsonrpc":"2.0","id":7,"method":"item/commandExecution/requestApproval","params":{}}`))
	request := sink.LastPublishedControl()
	require.NotEmpty(t, request.RequestID, "the approval card is published")

	require.Error(t, a.Interrupt(), "the interrupt cannot be written")

	assert.NotContains(t, sink.CanceledControls(), request.RequestID,
		"an interrupt that failed must not delete the only control that can answer")
	assert.True(t, a.OutstandingControlForTest(request.RequestID),
		"and the record stays, so a later answer still routes")
}

// The elicitation is the opposite case, and it must be released FIRST.
//
// It registers a real cancel answer, because Codex defines no outcome for an
// elicitation the client withdraws. Nothing else delivers that answer, so the
// elicitation stays blocked inside the CLI for the rest of the session.
func TestCodexInterruptAnswersAnElicitationBeforeItInterrupts(t *testing.T) {
	sink := &agenttest.ControlSink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	a.SetContextForTest(context.Background())
	a.SetStdinForTest(agenttest.NopStdin(refusingWriter{}))
	a.Mu.Lock()
	a.threadID, a.turnID = "thread", "turn"
	a.Mu.Unlock()

	a.HandleOutput([]byte(`{"jsonrpc":"2.0","id":7,"method":"` + contracts.MCPElicitationMethodCodex + `","params":{}}`))
	request := sink.LastPublishedControl()
	require.NotEmpty(t, request.RequestID)

	require.Error(t, a.Interrupt())

	assert.Contains(t, sink.CanceledControls(), request.RequestID,
		"an elicitation is released even when the interrupt then fails")
	assert.False(t, a.OutstandingControlForTest(request.RequestID))
}

// refusingWriter is a stdin that refuses every write, which is what a closed pipe
// gives a request the runtime can no longer receive.
type refusingWriter struct{}

func (refusingWriter) Write([]byte) (int, error) { return 0, errors.New("stdin is closed") }

// A running command's own output reaches the card while it runs. Codex sends
// DELTAS, so the tail carries everything the call printed so far -- only this
// agent knows where the earlier deltas ended.
func TestHandleCodexOutput_OutputDeltaReportsTheAccumulatedTail(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))

	start := `{"method":"item/started","params":{"item":{"id":"cmd-1","type":"commandExecution","command":"ls","status":"inProgress"},"threadId":"main-thread","turnId":"turn1"}}`
	handleCodexOutput(a, providerkit.ParseLine([]byte(start)))
	for _, delta := range []string{"first\n", "second\n"} {
		line := fmt.Sprintf(`{"method":"item/commandExecution/outputDelta","params":{"itemId":"cmd-1","delta":%q,"threadId":"main-thread","turnId":"turn1"}}`, delta)
		handleCodexOutput(a, providerkit.ParseLine([]byte(line)))
	}

	var tails []agent.ProgressUpdate
	for _, update := range sink.ProgressUpdates() {
		if update.Operation == agent.ProgressOutputTail {
			tails = append(tails, update)
		}
	}
	require.Len(t, tails, 2)
	assert.Equal(t, agent.OutputTailProgress("cmd-1", "first\n", false), tails[0])
	assert.Equal(t, agent.OutputTailProgress("cmd-1", "first\nsecond\n", false), tails[1])
}

// Codex moves a thread's own settings for reasons LeapMux never asked for: a
// `/model` typed into the composer, a preset's collaboration mode, an effort the
// model clamps. The picker used to go on showing what LeapMux last requested.
func TestHandleCodexOutput_ThreadSettingsUpdatedRefreshesTheSettings(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	agent.model, agent.effort, agent.collaborationMode = "gpt-5", "medium", contracts.CodexOptionDefaultCollaborationMode

	line := `{"method":"thread/settings/updated","params":{"threadId":"main-thread","threadSettings":{"model":"gpt-5.4","effort":"high","collaborationMode":"pair"}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(line)))

	require.Equal(t, 1, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	assert.Equal(t, "gpt-5.4", refresh.Model)
	assert.Equal(t, "high", refresh.Effort)
	assert.Equal(t, "pair", refresh.Options[contracts.CodexOptionCollaborationMode])
	// The frame is a settings fact, never a transcript row.
	assert.Equal(t, 0, sink.MessageCount())
}

// An axis the frame omits keeps the value the reader chose. Writing "" for it
// would clear a setting the app server said nothing about.
func TestHandleCodexOutput_ThreadSettingsUpdatedKeepsAnAxisItOmits(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agent.NewProviderServices(sink))
	agent.model, agent.effort, agent.collaborationMode = "gpt-5", "medium", "pair"

	line := `{"method":"thread/settings/updated","params":{"threadId":"main-thread","threadSettings":{"model":"gpt-5.4"}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(line)))

	refresh := sink.LastSettingsRefresh()
	assert.Equal(t, "gpt-5.4", refresh.Model)
	assert.Equal(t, "medium", refresh.Effort)
	assert.Equal(t, "pair", refresh.Options[contracts.CodexOptionCollaborationMode])
}

// The live tail keeps advancing after the retention cap stops recording the output.
//
// The cap restricts what the FINISHED row keeps. The tail is what a reader watches
// while the call runs, and the byte counter beside it keeps climbing on every delta,
// so a tail that froze at the cap showed text from megabytes ago under a number that
// still moved.
func TestAppendCodexToolEventAdvancesTheTailPastTheRetentionCap(t *testing.T) {
	t.Parallel()

	a := newCodexAgentWithSink(agent.NewProviderServices(&agenttest.Sink{}))
	a.Mu.Lock()
	a.incompleteTools = map[string]*codexIncompleteTool{"cmd-1": {}}
	a.appendCodexToolEventLocked("cmd-1", strings.Repeat("x", codexIncompleteOutputLimit), false)
	a.Mu.Unlock()
	capped, _ := a.codexToolOutputSoFar("cmd-1")

	a.Mu.Lock()
	a.appendCodexToolEventLocked("cmd-1", "the newest line\n", false)
	a.Mu.Unlock()

	tail, truncated := a.codexToolOutputSoFar("cmd-1")
	assert.NotEqual(t, capped, tail, "the tail moved with the output the cap refused")
	assert.True(t, strings.HasSuffix(tail, "the newest line\n"),
		"the tail carries the last bytes the call printed, whatever the cap decided")
	assert.LessOrEqual(t, len(tail), codexLiveTailLimit)
	assert.True(t, truncated, "output was lost, and the card states it")
}

// The retention cap cuts at a BYTE index into a UTF-8 stream, so it steps back to
// the start of the rune it lands inside. A partial rune renders as a replacement
// character in the row that Codex's own output writes.
func TestAppendCodexToolEventCutsTheCapAtARuneBoundary(t *testing.T) {
	t.Parallel()

	a := newCodexAgentWithSink(agent.NewProviderServices(&agenttest.Sink{}))
	a.Mu.Lock()
	// Four bytes of budget remain, and each Hangul syllable is three bytes, so the
	// cap lands inside the second one.
	a.incompleteTools = map[string]*codexIncompleteTool{
		"cmd-1": {outputBytes: codexIncompleteOutputLimit - 4},
	}
	a.appendCodexToolEventLocked("cmd-1", "가나", false)
	events := a.incompleteTools["cmd-1"].outputEvents
	truncated := a.incompleteTools["cmd-1"].outputTruncated
	a.Mu.Unlock()

	require.Len(t, events, 1)
	assert.Equal(t, "가", events[0].text)
	assert.True(t, utf8.ValidString(events[0].text), "the stored text holds no partial rune")
	assert.True(t, truncated)

	tail, _ := a.codexToolOutputSoFar("cmd-1")
	assert.Equal(t, "가나", tail, "the tail precedes the cap, so it keeps both runes")
	assert.True(t, utf8.ValidString(tail))
}

// A budget too small for even one rune records NO event, rather than an empty one.
func TestAppendCodexToolEventRecordsNothingWhenNoRuneFits(t *testing.T) {
	t.Parallel()

	a := newCodexAgentWithSink(agent.NewProviderServices(&agenttest.Sink{}))
	a.Mu.Lock()
	a.incompleteTools = map[string]*codexIncompleteTool{
		"cmd-1": {outputBytes: codexIncompleteOutputLimit - 2},
	}
	a.appendCodexToolEventLocked("cmd-1", "가", false)
	tool := a.incompleteTools["cmd-1"]
	events, bytes, truncated := tool.outputEvents, tool.outputBytes, tool.outputTruncated
	a.Mu.Unlock()

	assert.Empty(t, events)
	assert.Equal(t, codexIncompleteOutputLimit-2, bytes, "nothing was stored, so nothing was counted")
	assert.True(t, truncated)
}
