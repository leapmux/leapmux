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

	"github.com/coder/quartz"
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/util/testutil"
	agentapi "github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type codexResolvedControlSink struct {
	*agenttest.ControlSink
	publishedInstance          string
	canceledInstance           string
	unconditionalCancellations int
}

func TestCodexUnknownFramesPersist(t *testing.T) {
	t.Parallel()
	cases := []agenttest.UnknownFrameCase{
		{Name: "main thread", Frame: []byte(`{"method":"leapmux/probe","params":{"threadId":"main-thread","future":true}}`)},
		{Name: "child thread", Frame: []byte(`{"method":"leapmux/probe","params":{"threadId":"child-1","future":true}}`)},
		{Name: "another thread", Frame: []byte(`{"method":"leapmux/probe","params":{"threadId":"other-thread","future":true}}`), OtherSession: true},
	}
	agenttest.AssertUnknownFramePersists(t, cases, func(t *testing.T, tc agenttest.UnknownFrameCase) *agenttest.Sink {
		sink := &agenttest.Sink{}
		a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
		if tc.Name == "child thread" {
			feedCodexLines(a, `{"method":"item/started","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"spawn-1","kind":"started","agentThreadId":"child-1","agentPath":"/root/child"}}}`)
		}
		feedCodexLines(a, string(tc.Frame))
		if tc.Name == "child thread" {
			assert.Empty(t, sink.Messages(), "the child frame must not enter the parent transcript")
			return sink.Child("child-of-spawn-1")
		}
		return sink
	})
}

func (s *codexResolvedControlSink) PublishControlRequest(request agentapi.ControlRequest) error {
	s.publishedInstance = request.InstanceID
	return s.ControlSink.PublishControlRequest(request)
}

func (s *codexResolvedControlSink) CancelControlRequest(requestID string) {
	s.unconditionalCancellations++
	s.ControlSink.CancelControlRequest(requestID)
}

func (s *codexResolvedControlSink) CancelControlRequestInstance(requestID, instanceID string) {
	s.canceledInstance = instanceID
	s.ControlSink.CancelControlRequestInstance(requestID, instanceID)
}

func TestCodexResolvedNotificationKeepsReaderFinalizationOwnership(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"open", "answered", "unknown"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			a, controls, _ := newCodexControlAgent(t)
			sink := &codexResolvedControlSink{ControlSink: controls}
			a.sink = agentapi.NewProviderServices(sink)
			if state != "unknown" {
				handleCodexOutput(a, providerkit.ParseLine([]byte(codexCommandApprovalLine)))
			}
			if state == "answered" {
				require.NoError(t, a.SendRawInput([]byte(`{"jsonrpc":"2.0","id":7,"result":{"decision":"accept"}}`), agentapi.StopContext{}))
				assert.False(t, a.OutstandingControlForTest("jsonrpc:7"))
			}
			a.handleServerRequestResolved(json.RawMessage(`{"threadId":"main-thread","requestId":7}`))
			assert.Zero(t, sink.unconditionalCancellations)
			assert.False(t, a.OutstandingControlForTest("jsonrpc:7"))
			if state == "open" {
				require.NotEmpty(t, sink.publishedInstance)
				assert.Equal(t, sink.publishedInstance, sink.canceledInstance)
				assert.Equal(t, []string{"jsonrpc:7"}, sink.CanceledControls())
			} else {
				assert.Empty(t, sink.canceledInstance)
				assert.Empty(t, sink.CanceledControls(), "the service owns finalization after a reader answer")
			}
		})
	}
}

func TestCodexControlPublicationFailureReturnsProtocolError(t *testing.T) {
	t.Parallel()
	output := &agenttest.Stdin{}
	sink := &agenttest.ControlSink{PublicationError: errors.New("storage unavailable")}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	a.SetStdinForTest(agenttest.NopStdin(output))
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","id":37,"method":"item/tool/requestUserInput","params":{"threadId":"main-thread","questions":[]}}`)))
	// handleCodexOutput drains Codex stdout, so queue the failure response and return before waiting for the stdin write.
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

func (s *recordingCodexEnsureSink) EnsureChildAgent(spec agentapi.ChildAgentSpec) (string, error) {
	spawnSpanID, providerChildKey, title := spec.SpawnSpanID, spec.ProviderChildKey, spec.Title
	s.ensureCalls = append(s.ensureCalls, codexEnsureCall{
		spawnSpanID:      spawnSpanID,
		providerChildKey: providerChildKey,
		title:            title,
	})
	return s.Sink.EnsureChildAgent(spec)
}

func (s *recordingCodexEnsureSink) ChildSink(childAgentID string) agentapi.ProviderServices {
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

func (s *transientCodexEnsureFailureSink) EnsureChildAgent(spec agentapi.ChildAgentSpec) (string, error) {
	if s.ensureFailures > 0 {
		s.ensureFailures--
		return "", fmt.Errorf("transient child creation failure")
	}
	return s.Sink.EnsureChildAgent(spec)
}

func (s *blockingCodexEnsureSink) EnsureChildAgent(spec agentapi.ChildAgentSpec) (string, error) {
	close(s.started)
	<-s.release
	return s.Sink.EnsureChildAgent(spec)
}

func (s *notificationPersistGuardSink) PersistMessage(source leapmuxv1.MessageSource, content agentapi.MessageContent, span agentapi.SpanInfo) error {
	s.t.Fatalf("notification must not be persisted as a regular message: source=%v content=%s", source, string(content.Original))
	return nil
}

func newCodexAgentWithSink(sink agentapi.ProviderServices) *Agent {
	return newCodexAgentWithSinkAndClock(sink, nil)
}

// newCodexAgentWithSinkAndClock builds the agent of newCodexAgentWithSink on
// clock. A nil clock selects the real clock.
func newCodexAgentWithSinkAndClock(sink agentapi.ProviderServices, clock quartz.Clock) *Agent {
	a := &Agent{
		JSONRPCProcess: providerkit.JSONRPCProcess{Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{
			AgentID: "test-agent",
			Clock:   clock,
		})},
		sink:               sink,
		threadID:           "main-thread",
		turnRetrySupported: true,
		retryTextLocation:  time.Local,
	}
	a.sink = agentapi.NewModelProgressResetSink(a.sink)
	return a
}

func TestHandleCodexOutput_TurnStartedOpensTheTurn(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

	input := `{"jsonrpc":"2.0","method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"turn-42"}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

	statusActiveCount := sink.StatusActiveCount()
	sessionInfoCount := sink.SessionInfoCount()
	agent.Mu.Lock()
	turnID := agent.mainTurn.id
	agent.Mu.Unlock()
	assert.Equal(t, 0, statusActiveCount, "turn/started must NOT re-broadcast full status")
	assert.Equal(t, "turn-42", turnID, "interrupts and steering target this turn")
	assert.Equal(t, []bool{true}, sink.TurnActives(),
		"turn/started opens the Worker's activity state AND its input queue's turn")
	// The preceding browser activity heuristic also used a temporary session-info turn ID.
	// No current reader consumes that ID, so the provider sends no additional frame for it.
	assert.Equal(t, 0, sessionInfoCount, "turn/started broadcasts no session info")
}

func TestHandleCodexOutput_TurnStartedFallbackIsNoop(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

	// turn/started without turn.id supplies no turn-specific state to broadcast.
	// The sink refreshes git status at turn end.
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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

	input := `{"jsonrpc":"2.0","id":42,"method":"item/tool/requestUserInput","params":{"threadId":"t1","turnId":"turn1","itemId":"item1","questions":[{"id":"q1","header":"Header","question":"Which option?","options":[{"label":"A"}]}]}}`

	handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

	require.Equal(t, 1, sink.PublishedControlCount())

	rec := sink.LastPublishedControl()
	assert.Equal(t, "jsonrpc:42", rec.RequestID)

	// Verify that the payload preserves the original content.
	var parsed struct {
		Method string `json:"method"`
		ID     int    `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rec.Payload, &parsed))
	assert.Equal(t, "item/tool/requestUserInput", parsed.Method)
	assert.Equal(t, 42, parsed.ID)

	// The handler does not persist an ordinary message.
	assert.Equal(t, 0, sink.MessageCount())
}

func TestHandleCodexOutput_CommandExecutionApproval(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

	input := `{"jsonrpc":"2.0","id":7,"method":"item/commandExecution/requestApproval","params":{"command":"rm -rf /","reason":"cleanup"}}`

	handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

	require.Equal(t, 1, sink.PublishedControlCount())

	rec := sink.LastPublishedControl()
	assert.Equal(t, "jsonrpc:7", rec.RequestID)
}

func TestHandleCodexOutput_FileChangeApproval(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

	input := `{"jsonrpc":"2.0","id":8,"method":"item/fileChange/requestApproval","params":{"reason":"editing file"}}`

	handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

	require.Equal(t, 1, sink.PublishedControlCount())

	rec := sink.LastPublishedControl()
	assert.Equal(t, "jsonrpc:8", rec.RequestID)
}

func TestHandleCodexOutput_PermissionsApproval(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

	input := `{"jsonrpc":"2.0","id":9,"method":"item/permissions/requestApproval","params":{"reason":"needs access"}}`

	handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

	require.Equal(t, 1, sink.PublishedControlCount())

	rec := sink.LastPublishedControl()
	assert.Equal(t, "jsonrpc:9", rec.RequestID)
}

func TestHandleCodexOutput_ACompactionStartIsLiveStateOnly(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	feedCodexLines(a, `{"method":"item/started","params":{"item":{"type":"contextCompaction","id":"compact-1"},"threadId":"main-thread","turnId":"turn1"}}`)
	assert.Empty(t, sink.PersistedNotifications())
	assert.Empty(t, sink.Messages())
	assert.Contains(t, sink.SessionInfos(), map[string]interface{}{"compaction": map[string]interface{}{}})
}

func TestHandleCodexOutput_McpStartupNonFailuresDoNotReachTheTranscript(t *testing.T) {
	t.Parallel()

	for _, status := range []string{`"starting"`, `"ready"`, `"cancelled"`, `{"state":"ready"}`} {
		sink := &notificationPersistGuardSink{t: t}
		agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

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
		agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/mcpToolCall/progress","params":{"threadId":"main-thread","turnId":"turn-1","itemId":"mcp-1","message":"Working"}}`)))

	assert.Zero(t, sink.NotificationCount())
	assert.Zero(t, sink.MessageCount())
}

func TestHandleCodexOutput_McpOauthSuccessDoesNotReachTheTranscript(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"mcpServer/oauthLogin/completed","params":{"name":"docs","threadId":"main-thread","success":true}}`)))

	assert.Zero(t, sink.NotificationCount())
	assert.Zero(t, sink.MessageCount())
}

func TestHandleCodexOutput_McpOauthFailurePersistsAsAgent(t *testing.T) {
	t.Parallel()

	sink := &notificationPersistGuardSink{t: t}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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
			agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	failure := `{"method":"hook/completed","params":{"threadId":"child-1","turnId":"child-turn","run":{"id":"hook-1","status":"blocked","statusMessage":"policy blocked the hook"}}}`

	handleCodexOutput(agent, providerkit.ParseLine([]byte(failure)))
	assert.Zero(t, sink.MessageCount())
	assert.Zero(t, sink.NotificationCount())

	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"spawn-1","kind":"started","agentThreadId":"child-1","agentPath":"/root/reviewer"}}}`)))

	child := sink.Child("child-of-spawn-1")
	require.Equal(t, 1, child.NotificationCount())
	assert.JSONEq(t, failure, string(child.LastNotification().Content))
}

// Each RENDER notice persists its raw frame so the frontend words it.
func TestHandleCodexOutput_RenderNoticesPersistRaw(t *testing.T) {
	t.Parallel()

	for _, input := range []string{
		`{"method":"warning","params":{"threadId":"main-thread","message":"careful"}}`,
		`{"method":"configWarning","params":{"summary":"Error parsing rules; custom rules not applied.","details":"unexpected token","path":"/home/u/.codex/rules/x.rules"}}`,
		`{"method":"deprecationNotice","params":{"summary":"The foo option is deprecated","details":"Use bar instead."}}`,
		`{"method":"model/verification","params":{"threadId":"main-thread","turnId":"u1","verifications":["trustedAccessForCyber"]}}`,
		`{"method":"modelProvider/authRecoveryStarted","params":{"threadId":"main-thread","turnId":"u1","provider":"amazon-bedrock","message":"AWS session has expired. Reauthenticating..."}}`,
		`{"method":"modelProvider/authRecoveryCompleted","params":{"threadId":"main-thread","turnId":"u1","provider":"amazon-bedrock","message":"Reauthenticated."}}`,
	} {
		sink := &agenttest.Sink{}
		agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

		handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

		require.Equal(t, 1, sink.NotificationCount(), input)
		assert.Zero(t, sink.MessageCount(), input)
		assert.JSONEq(t, input, string(sink.LastNotification().Content), input)
	}
}

// A notice that repeats with the same text tells the reader nothing new, so
// each of the three repeating notices keeps one row per Codex process.
func TestHandleCodexOutput_NoticesDedupePerProcess(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		input   string
		changed string
		// The verification notice is one fixed sentence, so it stays deduped
		// even when the turn that carries it changes.
		changedAdds bool
	}{
		{
			name:        "configWarning",
			input:       `{"method":"configWarning","params":{"summary":"Error parsing rules","details":"unexpected token","path":"/home/u/.codex/rules/x.rules"}}`,
			changed:     `{"method":"configWarning","params":{"summary":"Error parsing rules","details":"a different detail","path":"/home/u/.codex/rules/x.rules"}}`,
			changedAdds: true,
		},
		{
			name:        "deprecationNotice",
			input:       `{"method":"deprecationNotice","params":{"summary":"The foo option is deprecated","details":"Use bar instead."}}`,
			changed:     `{"method":"deprecationNotice","params":{"summary":"The baz option is deprecated","details":"Use bar instead."}}`,
			changedAdds: true,
		},
		{
			name:    "model/verification",
			input:   `{"method":"model/verification","params":{"threadId":"main-thread","turnId":"u1","verifications":["trustedAccessForCyber"]}}`,
			changed: `{"method":"model/verification","params":{"threadId":"main-thread","turnId":"u2","verifications":["trustedAccessForCyber"]}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sink := &agenttest.Sink{}
			agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

			handleCodexOutput(agent, providerkit.ParseLine([]byte(tc.input)))
			handleCodexOutput(agent, providerkit.ParseLine([]byte(tc.input)))
			require.Equal(t, 1, sink.NotificationCount(), "one row per distinct text per process")

			handleCodexOutput(agent, providerkit.ParseLine([]byte(tc.changed)))
			if tc.changedAdds {
				require.Equal(t, 2, sink.NotificationCount(), "a different text is a new row")
			} else {
				require.Equal(t, 1, sink.NotificationCount(), "one fixed notice per process")
			}
		})
	}
}

// The dedupe key includes the method. A config warning and a deprecation notice
// with the same fields are two different notices, and each keeps its row.
func TestHandleCodexOutput_NoticeDedupeKeepsEachMethodApart(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	configWarning := `{"method":"configWarning","params":{"summary":"The foo option is deprecated","details":"Use bar instead."}}`
	deprecationNotice := `{"method":"deprecationNotice","params":{"summary":"The foo option is deprecated","details":"Use bar instead."}}`

	handleCodexOutput(agent, providerkit.ParseLine([]byte(configWarning)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(deprecationNotice)))

	notifications := sink.PersistedNotifications()
	require.Len(t, notifications, 2, "each method keeps its own row")
	assert.JSONEq(t, configWarning, string(notifications[0].Content))
	assert.JSONEq(t, deprecationNotice, string(notifications[1].Content))
}

// The verification notice draws text only for the trustedAccessForCyber flag.
// A verification frame without that flag must not use up the one row that the
// flagged frame needs.
func TestHandleCodexOutput_VerificationDedupeKeepsEachFlagSetApart(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	unflagged := `{"method":"model/verification","params":{"threadId":"main-thread","turnId":"u1","verifications":[]}}`
	flagged := `{"method":"model/verification","params":{"threadId":"main-thread","turnId":"u2","verifications":["trustedAccessForCyber"]}}`

	handleCodexOutput(agent, providerkit.ParseLine([]byte(unflagged)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(flagged)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(flagged)))

	notifications := sink.PersistedNotifications()
	require.Len(t, notifications, 2, "one row per distinct flag set per process")
	assert.JSONEq(t, flagged, string(notifications[1].Content))
}

// thread/compacted and thread/name/updated route by threadId, as every other
// stored frame does. A child's frame waits for its route and then reaches the
// child transcript, never the parent.
func TestHandleCodexOutput_UnknownMetadataRoutesByThreadID(t *testing.T) {
	t.Parallel()

	for _, method := range []string{contracts.CodexMethodThreadCompacted, contracts.CodexMethodThreadNameUpdated} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()

			sink := &agenttest.Sink{}
			agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
			main := fmt.Sprintf(`{"method":%q,"params":{"threadId":"main-thread","turnId":"root-turn"}}`, method)
			child := fmt.Sprintf(`{"method":%q,"params":{"threadId":"child-1","turnId":"child-turn"}}`, method)

			handleCodexOutput(agent, providerkit.ParseLine([]byte(main)))
			handleCodexOutput(agent, providerkit.ParseLine([]byte(child)))
			require.Equal(t, 1, sink.MessageCount(), "only the main frame reaches the parent")
			assert.JSONEq(t, main, string(sink.Messages()[0].Content))

			handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"spawn-1","kind":"started","agentThreadId":"child-1","agentPath":"/root/child"}}}`)))

			assert.Equal(t, 1, sink.MessageCount(), "the parent transcript stays unchanged")
			childSink := sink.Child("child-of-spawn-1")
			require.Equal(t, 1, childSink.MessageCount(), "the queued child frame reaches the child")
			assert.JSONEq(t, child, string(childSink.Messages()[0].Content))
		})
	}
}

// The guardian notice for an approved call repeats an outcome the tool card
// already shows.
func TestHandleCodexOutput_GuardianApprovedNoticeIsDropped(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"guardianWarning","params":{"threadId":"main-thread","message":"Automatic approval review approved (rm -rf build)"}}`)))

	assert.Zero(t, sink.NotificationCount())
	assert.Zero(t, sink.MessageCount())
}

// thread/closed has no transcript content. A main thread closing under the
// running worker stays in the log.
func TestHandleCodexOutput_ThreadClosedPersistsNothing(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"thread/closed","params":{"threadId":"main-thread"}}`)))

	assert.Zero(t, sink.NotificationCount())
	assert.Zero(t, sink.MessageCount())
	assert.Zero(t, sink.SessionInfoCount())
}

// The automatic review holds one call before it runs. The hold is live state:
// the tool card's badge shows it, and no transcript row appears.
func TestHandleCodexOutput_AutoApprovalReviewStartedBroadcastsTheHold(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	agent.sink.UpdateSessionID("main-session")

	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/autoApprovalReview/started","params":{"threadId":"main-thread","turnId":"u1","startedAtMs":1792000000000,"reviewId":"r1","targetItemId":"call_1","review":{"status":"inProgress","riskLevel":null,"userAuthorization":null,"rationale":null},"action":{"type":"command","source":"shell","command":"rm -rf build","cwd":"/w"}}}`)))

	assert.Zero(t, sink.NotificationCount(), "the hold writes no row")
	assert.Zero(t, sink.MessageCount())
	require.Equal(t, 1, sink.SessionInfoCount())
	tool, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyRunningTool)
	require.True(t, ok)
	assert.Equal(t, map[string]interface{}{
		contracts.RunningToolFieldSpanId:          "call_1",
		contracts.RunningToolFieldAgentSessionId:  "main-session",
		contracts.RunningToolFieldPermissionCheck: true,
	}, tool, "the hold states the call, the session of its rows and the check")
}

// A review that identifies no call broadcasts nothing.
func TestHandleCodexOutput_AutoApprovalReviewWithoutATargetBroadcastsNothing(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/autoApprovalReview/started","params":{"threadId":"main-thread","turnId":"u1","reviewId":"r1","targetItemId":null,"review":{"status":"inProgress"}}}`)))

	assert.Zero(t, sink.SessionInfoCount())
}

// A denied review that identifies no call still states its verdict. It has no
// tool card to release, so it broadcasts nothing.
func TestHandleCodexOutput_AutoApprovalReviewCompletedWithoutATargetPersistsTheVerdict(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	input := `{"method":"item/autoApprovalReview/completed","params":{"threadId":"main-thread","turnId":"u1","reviewId":"r1","targetItemId":null,"decisionSource":"agent","review":{"status":"denied","rationale":"touches secrets"},"action":{"type":"command","source":"shell","command":"rm -rf build","cwd":"/w"}}}`

	handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

	require.Equal(t, 1, sink.NotificationCount())
	assert.JSONEq(t, input, string(sink.LastNotification().Content))
	assert.Zero(t, sink.SessionInfoCount(), "no call holds, so nothing ends")
}

// A child's review holds a call in the child transcript. The hold broadcasts
// on the child sink and carries the child's session, which the child's rows
// carry, so the child's tool card matches it.
func TestHandleCodexOutput_ChildAutoApprovalReviewBroadcastsOnTheChildSink(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	agent.sink.UpdateSessionID("main-session")
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"spawn-1","kind":"started","agentThreadId":"child-1","agentPath":"/root/child"}}}`)))
	child := sink.Child("child-of-spawn-1")
	child.UpdateSessionID("child-session")
	parentInfos := sink.SessionInfoCount()

	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/autoApprovalReview/started","params":{"threadId":"child-1","turnId":"child-turn","reviewId":"r1","targetItemId":"call_1","review":{"status":"inProgress"}}}`)))

	tool, ok := child.LastSessionInfoValue(contracts.SessionInfoKeyRunningTool)
	require.True(t, ok, "the child sink receives the hold")
	assert.Equal(t, map[string]interface{}{
		contracts.RunningToolFieldSpanId:          "call_1",
		contracts.RunningToolFieldAgentSessionId:  "child-session",
		contracts.RunningToolFieldPermissionCheck: true,
	}, tool)
	assert.Equal(t, parentInfos, sink.SessionInfoCount(), "the parent sink receives nothing")

	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/autoApprovalReview/completed","params":{"threadId":"child-1","turnId":"child-turn","reviewId":"r1","targetItemId":"call_1","review":{"status":"approved"},"action":{"type":"command","command":"ls"}}}`)))

	tool, ok = child.LastSessionInfoValue(contracts.SessionInfoKeyRunningTool)
	require.True(t, ok)
	assert.Equal(t, map[string]interface{}{
		contracts.RunningToolFieldSpanId:          "call_1",
		contracts.RunningToolFieldAgentSessionId:  "child-session",
		contracts.RunningToolFieldPermissionCheck: nil,
	}, tool, "the completed review ends the child's hold")
	assert.Equal(t, parentInfos, sink.SessionInfoCount(), "the parent sink receives nothing")
}

// A child whose route does not exist has no tool card to hold. The review
// broadcasts nothing, and above all nothing on the parent sink.
func TestHandleCodexOutput_UnroutedChildAutoApprovalReviewBroadcastsNothing(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/autoApprovalReview/started","params":{"threadId":"child-unknown","turnId":"child-turn","reviewId":"r1","targetItemId":"call_1","review":{"status":"inProgress"}}}`)))

	assert.Zero(t, sink.SessionInfoCount())
	assert.Zero(t, sink.NotificationCount())
}

// A review hold can be the first child frame that resolves a recovered route.
// It must replay the child frames that waited for that route first, as every
// other route site does, so the queued rows keep their order.
func TestHandleCodexOutput_ChildAutoApprovalReviewReplaysTheQueuedChildFrames(t *testing.T) {
	t.Parallel()

	sink := &transientCodexEnsureFailureSink{Sink: &agenttest.Sink{}, ensureFailures: 2}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	// The first creation attempt fails, so the route does not exist yet.
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"spawn-1","kind":"started","agentThreadId":"child-1","agentPath":"/root/child"}}}`)))
	// The second attempt fails too, so the warning waits in the child queue.
	warning := `{"method":"warning","params":{"threadId":"child-1","message":"careful"}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(warning)))
	require.Zero(t, sink.NotificationCount())

	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/autoApprovalReview/started","params":{"threadId":"child-1","turnId":"child-turn","reviewId":"r1","targetItemId":"call_1","review":{"status":"inProgress"}}}`)))

	child := sink.Child("child-of-spawn-1")
	require.Equal(t, 1, child.NotificationCount(), "the queued warning replays when the hold resolves the route")
	assert.JSONEq(t, warning, string(child.LastNotification().Content))
	assert.Zero(t, sink.NotificationCount(), "the parent transcript stays unchanged")
}

// Every review outcome ends the hold, a status from a later Codex release
// included. The auto-review tests own which reviews reach the transcript.
func TestHandleCodexOutput_AutoApprovalReviewCompletedEndsTheHold(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		status string
	}{
		{status: "denied"},
		{status: "timedOut"},
		{status: "approved"},
		{status: "aborted"},
		{status: "inProgress"},
		{status: "aStatusFromALaterRelease"},
		{status: ""},
	} {
		t.Run(tc.status, func(t *testing.T) {
			t.Parallel()

			sink := &agenttest.Sink{}
			agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
			input := fmt.Sprintf(`{"method":"item/autoApprovalReview/completed","params":{"threadId":"main-thread","turnId":"u1","startedAtMs":1792000000000,"completedAtMs":1792000004000,"reviewId":"r1","targetItemId":"call_1","decisionSource":"agent","review":{"status":%q,"riskLevel":"high","userAuthorization":null,"rationale":"touches secrets"},"action":{"type":"command","source":"shell","command":"rm -rf build","cwd":"/w"}}}`, tc.status)

			handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

			require.Equal(t, 1, sink.SessionInfoCount(), "every outcome ends the hold")
			tool, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyRunningTool)
			require.True(t, ok)
			fields := tool.(map[string]interface{})
			assert.Equal(t, "call_1", fields[contracts.RunningToolFieldSpanId])
			assert.Nil(t, fields[contracts.RunningToolFieldPermissionCheck], "the hold ends")
		})
	}
}

// The buffering notice persists raw and, while the answer can still be
// replayed, offers a retry on the faster model. Repeats of one turn fold into
// one row through the Classify key (see TestCodexClassifyFoldsRetryAndBufferingNotices).
func TestHandleCodexOutput_SafetyBufferingOffersAFasterRetry(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	a.model = "gpt-5.4"
	a.mainTurn.id = "turn-1"
	a.mainTurn.input = []map[string]interface{}{{"type": "text", "text": "Hello."}}
	input := `{"method":"model/safetyBuffering/updated","params":{"threadId":"main-thread","turnId":"turn-1","model":"gpt-5.4","useCases":["cyber"],"reasons":["probes"],"showBufferingUi":true,"fasterModel":"gpt-5.5"}}`

	handleCodexOutput(a, providerkit.ParseLine([]byte(input)))

	require.Equal(t, 1, sink.NotificationCount())
	assert.JSONEq(t, input, string(sink.LastNotification().Content))
	offer, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyTurnRetryOffer)
	require.True(t, ok)
	assert.Equal(t, map[string]interface{}{
		contracts.TurnRetryOfferFieldTurnId:       "turn-1",
		contracts.TurnRetryOfferFieldModel:        "gpt-5.5",
		contracts.TurnRetryOfferFieldLearnMoreUrl: codexTurnRetryLearnMoreURL,
	}, offer, "the offer carries exactly the contract fields")
}

// The Codex TUI offers the retry only while the answer can still be replayed.
// A condition that does not hold suppresses the offer and keeps the notice.
func TestHandleCodexOutput_SafetyBufferingOfferConditions(t *testing.T) {
	t.Parallel()

	const frame = `{"method":"model/safetyBuffering/updated","params":{"threadId":"main-thread","turnId":"turn-1","model":"gpt-5.4","showBufferingUi":true,"fasterModel":"%s"}}`

	for _, tc := range []struct {
		name    string
		frame   string
		prepare func(a *Agent)
	}{
		{
			name:  "no faster model",
			frame: `{"method":"model/safetyBuffering/updated","params":{"threadId":"main-thread","turnId":"turn-1","model":"gpt-5.4","showBufferingUi":true,"fasterModel":""}}`,
		},
		{
			name:  "the current model is the fastest",
			frame: `{"method":"model/safetyBuffering/updated","params":{"threadId":"main-thread","turnId":"turn-1","model":"gpt-5.4","showBufferingUi":true,"fasterModel":"gpt-5.4"}}`,
		},
		{
			name:  "a different turn",
			frame: `{"method":"model/safetyBuffering/updated","params":{"threadId":"main-thread","turnId":"turn-9","model":"gpt-5.4","showBufferingUi":true,"fasterModel":"gpt-5.5"}}`,
		},
		{
			name:  "no retained input",
			frame: fmt.Sprintf(frame, "gpt-5.5"),
			prepare: func(a *Agent) {
				a.mainTurn.input = nil
			},
		},
		{
			name:  "the answer already started",
			frame: fmt.Sprintf(frame, "gpt-5.5"),
			prepare: func(a *Agent) {
				a.mainTurn.agentMessageStarted = true
			},
		},
		{
			name:  "a retry is in flight",
			frame: fmt.Sprintf(frame, "gpt-5.5"),
			prepare: func(a *Agent) {
				a.retry = &codexTurnRetry{a: a}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sink := &agenttest.Sink{}
			a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
			a.model = "gpt-5.4"
			a.mainTurn.id = "turn-1"
			a.mainTurn.input = []map[string]interface{}{{"type": "text", "text": "Hello."}}
			if tc.prepare != nil {
				tc.prepare(a)
			}

			handleCodexOutput(a, providerkit.ParseLine([]byte(tc.frame)))

			require.Equal(t, 1, sink.NotificationCount(), "the notice persists")
			assert.Empty(t, sink.SessionInfoValues(contracts.SessionInfoKeyTurnRetryOffer),
				"no offer is made")
		})
	}
}

// The offer ends when the answer can no longer be replayed or the turn ends.
// Each trigger broadcasts the withdrawal.
func TestHandleCodexOutput_SafetyBufferingClearTriggers(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		trigger func(a *Agent)
	}{
		{
			name: "the buffering stops",
			trigger: func(a *Agent) {
				handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"model/safetyBuffering/updated","params":{"threadId":"main-thread","turnId":"turn-1","showBufferingUi":false}}`)))
			},
		},
		{
			name: "the first agent delta",
			trigger: func(a *Agent) {
				handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/agentMessage/delta","params":{"threadId":"main-thread","itemId":"m1","delta":"Hi"}}`)))
			},
		},
		{
			name: "the first agent item",
			trigger: func(a *Agent) {
				handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"agentMessage","id":"m1","text":""}}}`)))
			},
		},
		{
			name: "the turn ends",
			trigger: func(a *Agent) {
				handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"completed"}}}`)))
			},
		},
		{
			name: "the user stops",
			trigger: func(a *Agent) {
				// A stop with no running turn still withdraws the offer.
				a.Mu.Lock()
				a.mainTurn.id = ""
				a.Mu.Unlock()
				_ = a.Interrupt(agentapi.StopContext{})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sink := &agenttest.Sink{}
			a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
			armRetryableTurn(t, a, sink)
			require.Len(t, sink.SessionInfoValues(contracts.SessionInfoKeyTurnRetryOffer), 1)

			tc.trigger(a)

			values := sink.SessionInfoValues(contracts.SessionInfoKeyTurnRetryOffer)
			require.Len(t, values, 2, "the withdrawal broadcasts")
			assert.Nil(t, values[1])
			a.Mu.Lock()
			offer := a.retryOffer
			a.Mu.Unlock()
			assert.Nil(t, offer)
		})
	}
}

// A child thread persists its own buffering notice and holds no offer: the
// offer belongs to the running main turn.
func TestHandleCodexOutput_SafetyBufferingOnAChildMakesNoOffer(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	a.model = "gpt-5.4"
	a.mainTurn.id = "turn-1"
	a.mainTurn.input = []map[string]interface{}{{"type": "text", "text": "Hello."}}
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"spawn-1","kind":"started","agentThreadId":"child-1","agentPath":"/root/child"}}}`)))
	input := `{"method":"model/safetyBuffering/updated","params":{"threadId":"child-1","turnId":"child-turn","model":"gpt-5.4","showBufferingUi":true,"fasterModel":"gpt-5.5"}}`

	handleCodexOutput(a, providerkit.ParseLine([]byte(input)))

	child := sink.Child("child-of-spawn-1")
	require.Equal(t, 1, child.NotificationCount())
	assert.JSONEq(t, input, string(child.LastNotification().Content))
	assert.Zero(t, sink.NotificationCount(), "the parent transcript stays unchanged")
	assert.Empty(t, sink.SessionInfoValues(contracts.SessionInfoKeyTurnRetryOffer))
}

// A child's answer starts the child's own turn. The parent's offer identifies the
// parent turn and stays live: only the parent's own text ends it.
func TestHandleCodexOutput_ChildAgentTextKeepsTheParentRetryOffer(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	armRetryableTurn(t, a, sink)
	// Spawn child-1 so its frames route to its own transcript.
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"subAgentActivity","id":"spawn-1","kind":"started","agentThreadId":"child-1","agentPath":"/root/child"}}}`)))

	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/agentMessage/delta","params":{"threadId":"child-1","itemId":"m1","delta":"Child text"}}`)))
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"child-1","turnId":"child-turn","item":{"type":"agentMessage","id":"m2","text":""}}}`)))

	assert.Len(t, sink.SessionInfoValues(contracts.SessionInfoKeyTurnRetryOffer), 1,
		"the child text broadcasts no withdrawal")
	a.Mu.Lock()
	offer := a.retryOffer
	a.Mu.Unlock()
	require.NotNil(t, offer)
	assert.Equal(t, "turn-1", offer.turnID)
}

// The `error` frame carries its text in `params.error.message`. The old reader
// decoded `params.message`, which is always empty, so no error ever reached the
// transcript.
func TestHandleCodexOutput_ErrorWillRetryPersistsTheRawFrame(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	input := `{"jsonrpc":"2.0","method":"error","params":{"error":{"message":"Reconnecting... 2/5","codexErrorInfo":null,"additionalDetails":null},"willRetry":true,"threadId":"main-thread","turnId":"u1"}}`

	handleCodexOutput(a, providerkit.ParseLine([]byte(input)))

	require.Equal(t, 1, sink.NotificationCount(), "the reader must see that Codex keeps trying")
	assert.Zero(t, sink.MessageCount())
	assert.JSONEq(t, input, string(sink.LastNotification().Content))
}

func TestHandleCodexOutput_ErrorRoutesByThreadId(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	input := `{"jsonrpc":"2.0","method":"error","params":{"error":{"message":"Reconnecting... 1/5","codexErrorInfo":null,"additionalDetails":null},"willRetry":true,"threadId":"child-1","turnId":"u1"}}`

	// The frame arrives before the route exists and waits in the child queue.
	handleCodexOutput(a, providerkit.ParseLine([]byte(input)))
	assert.Zero(t, sink.MessageCount())
	assert.Zero(t, sink.NotificationCount(), "the parent transcript must not receive the child error")

	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"spawn-1","kind":"started","agentThreadId":"child-1","agentPath":"/root/child"}}}`)))

	child := sink.Child("child-of-spawn-1")
	require.Equal(t, 1, child.NotificationCount())
	assert.JSONEq(t, input, string(child.LastNotification().Content))
}

// A final error duplicates the failed turn's `turn/completed`, whose TurnError
// codexResultDivider already draws with its details.
func TestHandleCodexOutput_ErrorWithoutRetryPersistsNothing(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","method":"error","params":{"error":{"message":"This request has been flagged for potentially high-risk cyber activity.","codexErrorInfo":"cyberPolicy"},"willRetry":false,"threadId":"main-thread","turnId":"u1"}}`)))

	assert.Zero(t, sink.NotificationCount())
	assert.Zero(t, sink.MessageCount())
	assert.Empty(t, sink.LeapMuxNotifications())
}

// The default path stores each unclaimed frame as its own message row. Two
// plan updates in one notification-thread row freeze the plan card on the first
// plan: the row's classifier reads the wrapper's first message.
func TestHandleCodexOutput_DefaultPathFramesAreStandaloneMessageRows(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	first := `{"method":"turn/plan/updated","params":{"threadId":"main-thread","plan":[{"step":"Inspect","status":"inProgress"}]}}`
	second := `{"method":"turn/plan/updated","params":{"threadId":"main-thread","plan":[{"step":"Inspect","status":"completed"},{"step":"Write","status":"inProgress"}]}}`

	handleCodexOutput(agent, providerkit.ParseLine([]byte(first)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(second)))

	require.Equal(t, 2, sink.MessageCount(), "each frame is its own message row")
	assert.Zero(t, sink.NotificationCount(), "the default path joins no notification thread")
	messages := sink.Messages()
	assert.JSONEq(t, first, string(messages[0].Content))
	assert.JSONEq(t, second, string(messages[1].Content))
}

func TestHandleCodexOutput_AnOrphanReplyStoresNoRow(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	feedCodexLines(a, `{"id":7,"result":{}}`)
	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.PersistedNotifications())
}

func TestHandleCodexOutput_ACompletedUserMessageStoresNoRow(t *testing.T) {
	t.Parallel()
	for _, threadID := range []string{"main-thread", "child-1"} {
		t.Run(threadID, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
			feedCodexLines(a, `{"method":"item/started","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"spawn-1","kind":"started","agentThreadId":"child-1","agentPath":"/root/child"}}}`)
			feedCodexLines(a, fmt.Sprintf(`{"method":"item/completed","params":{"threadId":%q,"item":{"type":"userMessage","id":"input-1","content":[{"type":"text","text":"Hello"}]}}}`, threadID))
			assert.Empty(t, sink.Messages())
			assert.Empty(t, sink.PersistedNotifications())
			child := sink.Child("child-of-spawn-1")
			require.NotNil(t, child)
			assert.Empty(t, child.Messages())
			assert.Empty(t, child.PersistedNotifications())
		})
	}
}

func TestHandleCodexOutput_AUserMessageCompletionCannotResolveAModelRetry(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	feedCodexLines(a,
		`{"method":"error","params":{"threadId":"main-thread","willRetry":true,"error":{"message":"retry"}}}`,
		`{"method":"item/completed","params":{"threadId":"main-thread","item":{"type":"userMessage","id":"input-1","content":[]}}}`,
	)
	assert.Empty(t, sink.LeapMuxNotifications(), "a user input is not model output")
}

// The child transcript stores a child's default-path frames with the same
// primitive as the main thread's: one message row per frame, whether the frame
// waited for its route or arrived after the route existed.
func TestHandleCodexOutput_ChildDefaultPathFramesAreStandaloneMessageRows(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	first := `{"method":"turn/plan/updated","params":{"threadId":"child-1","plan":[{"step":"Inspect","status":"inProgress"}]}}`
	second := `{"method":"turn/plan/updated","params":{"threadId":"child-1","plan":[{"step":"Inspect","status":"completed"},{"step":"Write","status":"inProgress"}]}}`

	// The first frame waits in the child queue; the second arrives after the
	// route exists.
	handleCodexOutput(agent, providerkit.ParseLine([]byte(first)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"spawn-1","kind":"started","agentThreadId":"child-1","agentPath":"/root/child"}}}`)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(second)))

	assert.Zero(t, sink.MessageCount(), "the parent transcript stays unchanged")
	assert.Zero(t, sink.NotificationCount(), "the parent notification thread stays unchanged")
	child := sink.Child("child-of-spawn-1")
	require.Equal(t, 2, child.MessageCount(), "each child frame is its own message row")
	assert.Zero(t, child.NotificationCount())
	messages := child.Messages()
	assert.JSONEq(t, first, string(messages[0].Content))
	assert.JSONEq(t, second, string(messages[1].Content))
}

// A frame that identifies a child thread belongs to the child's transcript,
// whether the default path stores it (the plan) or a claimed case stores it
// (the warning). The parent's to-do list reads only the parent's own rows, so a
// child plan in the parent replaced the parent's list.
func TestHandleCodexOutput_QueuedChildFramesRouteToTheChildTranscript(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	plan := `{"method":"turn/plan/updated","params":{"threadId":"child-1","plan":[{"step":"child step","status":"inProgress"}]}}`
	warning := `{"method":"warning","params":{"threadId":"child-1","message":"careful"}}`

	// Both frames arrive before the route exists and wait in the child queue.
	handleCodexOutput(agent, providerkit.ParseLine([]byte(plan)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(warning)))
	assert.Zero(t, sink.MessageCount(), "the parent transcript must not receive a queued child frame")
	assert.Zero(t, sink.NotificationCount(), "the parent notification thread must stay unchanged")

	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"spawn-1","kind":"started","agentThreadId":"child-1","agentPath":"/root/child"}}}`)))

	assert.Zero(t, sink.MessageCount(), "the parent transcript must not receive the child frame")
	assert.Zero(t, sink.NotificationCount(), "the parent notification thread must stay unchanged")
	child := sink.Child("child-of-spawn-1")
	stored := append(append([]agenttest.Message{}, child.Messages()...), child.PersistedNotifications()...)
	require.Len(t, stored, 2, "the child transcript owns both frames")
	assert.JSONEq(t, plan, string(stored[0].Content))
	assert.JSONEq(t, warning, string(stored[1].Content))
}

func TestHandleCodexOutput_MultiAgentV2PersistsPromptAndMirrorsFinalReport(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

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

func TestHandleCodexOutput_RawResponseMirrorsAddNoTranscriptRows(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	for _, raw := range []string{
		`{"method":"rawResponseItem/completed","params":{"threadId":"main-thread","item":{"type":"function_call","name":"exec_command","arguments":"{}","call_id":"call-1"}}}`,
		`{"method":"rawResponseItem/completed","params":{"threadId":"main-thread","item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}}}`,
	} {
		handleCodexOutput(a, providerkit.ParseLine([]byte(raw)))
	}

	assert.Zero(t, sink.MessageCount())
	assert.Zero(t, sink.NotificationCount())
}

func TestHandleCodexOutput_V1ChildMirrorsFinalReportWithoutAnAgentPath(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

	input := `{"method":"thread/name/updated","params":{"threadId":"main-thread","name":"Refactoring auth"}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

	require.Equal(t, 1, sink.MessageCount(), "an unclaimed method retains its raw frame")
	assert.Zero(t, sink.NotificationCount(), "an unclaimed method joins no notification thread")
	last := sink.Messages()[0]
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, last.Source,
		"Codex-emitted lifecycle metadata must persist as AGENT")
	assert.JSONEq(t, input, string(last.Content),
		"raw envelope must be preserved so future renderers can read every field")
}

// --- Rate-limit snapshots and the automatic-retry verdict ---

// codexRateLimitTestNow precedes the January 2030 resets of the snapshot
// fixtures below by one to seven days, so every one of those resets can
// schedule a retry.
var codexRateLimitTestNow = time.Date(2029, 12, 31, 0, 0, 0, 0, time.UTC)

// newCodexAgentAt builds the agent of newCodexAgentWithSink on a mock clock
// set to now, so a verdict reads every reset against a fixed instant.
func newCodexAgentAt(t *testing.T, sink agentapi.ProviderServices, now time.Time) *Agent {
	t.Helper()
	clock := testutil.NewQuartzMock(t)
	clock.Set(now).MustWait(testutil.DeadlineContext(t))
	return newCodexAgentWithSinkAndClock(sink, clock)
}

var (
	codexTurnEndRow           = agenttest.TimelineEntry{Op: "turn_end"}
	codexServerErrorScheduled = agenttest.TimelineEntry{
		Op: "schedule_server_error", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR,
	}
	codexServerErrorCancelled = agenttest.TimelineEntry{
		Op: "cancel_server_error", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR,
		Reason: providerkit.TurnEndedWithoutRetryableFailure,
	}
	// codexServerErrorCancelledForAStatedReset follows a usage-limit schedule
	// at a stated reset.
	codexServerErrorCancelledForAStatedReset = agenttest.TimelineEntry{
		Op: "cancel_server_error", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR,
		Reason: "the turn ended on a usage limit that lifts at a stated time",
	}
	// codexServerErrorCancelledForNoReset ends a usage-limit turn whose reset
	// nothing states. It makes no usage-limit call.
	codexServerErrorCancelledForNoReset = agenttest.TimelineEntry{
		Op: "cancel_server_error", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR,
		Reason: "the turn ended on a usage limit that states no reset within a week",
	}
)

func codexUsageLimitScheduledAt(resetAt time.Time) agenttest.TimelineEntry {
	return agenttest.TimelineEntry{
		Op: "schedule_usage_limit", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT, ResetAt: resetAt,
	}
}

func codexUsageLimitCancelled(reason string) agenttest.TimelineEntry {
	return agenttest.TimelineEntry{
		Op: "cancel_usage_limit", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT, Reason: reason,
	}
}

// codexCodexWindowCleared is the reason of a cancel by a `codex` snapshot
// whose windows are all below their limits.
const codexCodexWindowCleared = `Codex reported that no usage window of the "codex" limit is exceeded`

// codexRateLimitValues returns the values of the last rate-limit broadcast.
func codexRateLimitValues(t *testing.T, sink *agenttest.Sink) map[string]interface{} {
	t.Helper()
	rateLimits, ok := sink.LastSessionInfo()["rate_limits"].(map[string]interface{})
	require.True(t, ok, "the broadcast carries rate_limits")
	values, ok := rateLimits["values"].(map[string]interface{})
	require.True(t, ok, "rate_limits carries a values map")
	return values
}

// codexBlockedSnapshot is a `codex` snapshot whose weekly window is used up
// while the credits serve no request.
const codexBlockedSnapshot = `{"method":"account/rateLimits/updated","params":{"rateLimits":{"limitId":"codex","primary":{"usedPercent":100,"windowDurationMins":10080,"resetsAt":1893456000},"secondary":null,"credits":{"hasCredits":false,"unlimited":false,"balance":"0"},"rateLimitReachedType":null}}}`

var codexBlockedSnapshotReset = time.Unix(1893456000, 0).UTC()

// The real usage-limit sequence of 2026-10-28 03:09:07 KST: rollout 6f1e64 of
// R/transcripts-codex-leapmuxdb.md (.tmp/transcripts/codex/timeline_run1.log).
// The rollout stores each snapshot in its snake_case form. The app server
// sends the same values in the camelCase form of DB #5, so the two snapshot
// frames carry the rollout values in that form.
const (
	codexRealCodexSnapshot   = `{"method":"account/rateLimits/updated","params":{"rateLimits":{"limitId":"codex","limitName":null,"normalModelSlug":null,"primary":{"usedPercent":100,"windowDurationMins":10080,"resetsAt":1793599238},"secondary":null,"credits":{"hasCredits":true,"unlimited":false,"balance":"0.5712300000"},"individualLimit":null,"spendControlReached":null,"planType":"prolite","rateLimitReachedType":null}}}`
	codexRealPremiumSnapshot = `{"method":"account/rateLimits/updated","params":{"rateLimits":{"limitId":"premium","limitName":null,"normalModelSlug":null,"primary":null,"secondary":null,"credits":{"hasCredits":false,"unlimited":false,"balance":"0"},"individualLimit":null,"spendControlReached":null,"planType":"prolite","rateLimitReachedType":null}}}`
	// DB #4 of R/transcripts-codex-leapmuxdb.md, verbatim, in its notification.
	codexRealUsageLimitTurn = `{"method":"turn/completed","params":{"threadId": "01a1793c-17f5-7a53-ab3c-da74216f1e64", "turn": {"id": "01a1793c-7ca1-7d00-a9ad-019d20f7ee9d", "rootTurnId": "01a1793c-7ca1-7d00-a9ad-019d20f7ee9d", "items": [], "itemsView": "notLoaded", "status": "failed", "error": {"message": "You’ve hit your usage limit. Visit https://chatgpt.com/settings/usage to purchase more credits or try again at Nov 2nd, 2026 3:00 PM.", "codexErrorInfo": "usageLimitExceeded", "additionalDetails": null, "misalignment": null}, "startedAt": 1793035369, "completedAt": 1793124547, "durationMs": 89177771}}}`
	codexRealThreadID       = "01a1793c-17f5-7a53-ab3c-da74216f1e64"
)

var (
	// codexRealUsageLimitAt is the completedAt of the turn.
	codexRealUsageLimitAt = time.Unix(1793124547, 0).UTC()
	// codexRealCodexReset is 2026-11-02 15:00:38 KST. The message truncates it
	// to "3:00 PM".
	codexRealCodexReset = time.Unix(1793599238, 0).UTC()
)

// At the real usage limit, the `codex` window read 100% while credits still
// served requests, and the `premium` snapshot carried no window. Neither
// snapshot blocks or clears anything. The turn then failed, and its one usage
// schedule lands at the `codex` reset, which is more exact than the minute in
// the message.
func TestHandleCodexOutput_TheRealUsageLimitSequenceSchedulesOnceAtTheCodexReset(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentAt(t, agentapi.NewProviderServices(sink), codexRealUsageLimitAt)
	a.threadID = codexRealThreadID

	handleCodexOutput(a, providerkit.ParseLine([]byte(codexRealCodexSnapshot)))
	handleCodexOutput(a, providerkit.ParseLine([]byte(codexRealPremiumSnapshot)))
	require.Empty(t, sink.Timeline(), "credits served the used-up window, and the premium snapshot carries no window")
	seven, ok := codexRateLimitValues(t, sink)["seven_day"].(map[string]interface{})
	require.True(t, ok, "the display keeps the codex bucket after the premium snapshot")
	assert.Equal(t, 1.0, seven["utilization"])

	handleCodexOutput(a, providerkit.ParseLine([]byte(codexRealUsageLimitTurn)))

	assert.Equal(t, []agenttest.TimelineEntry{
		codexTurnEndRow, codexUsageLimitScheduledAt(codexRealCodexReset), codexServerErrorCancelledForAStatedReset,
	}, sink.Timeline())
}

func TestHandleCodexOutput_RateLimitExceededSchedulesResume(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentAt(t, agentapi.NewProviderServices(sink), codexRateLimitTestNow)

	input := `{"method":"account/rateLimits/updated","params":{"rateLimits":{"primary":{"usedPercent":100,"windowDurationMins":300,"resetsAt":1893456000},"secondary":{"usedPercent":20,"windowDurationMins":10080,"resetsAt":1894000000},"credits":{"hasCredits":false,"unlimited":false,"balance":"0"}}}}`
	handleCodexOutput(a, providerkit.ParseLine([]byte(input)))

	require.Equal(t, 1, sink.AutoScheduleCount())
	schedule := sink.LastAutoSchedule()
	require.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT, schedule.Kind)
	require.True(t, schedule.ResetAt.Equal(time.Unix(1893456000, 0).UTC()))
	assert.JSONEq(t, input, string(schedule.SourcePayload), "the retry carries the snapshot")

	assert.Zero(t, sink.NotificationCount(), "account state must not become transcript history")
}

// A used-up window blocks only when the bucket's credits serve no request.
func TestHandleCodexOutput_AUsedUpWindowBlocksOnlyWithoutCredits(t *testing.T) {
	t.Parallel()

	window := `"primary":{"usedPercent":100,"windowDurationMins":10080,"resetsAt":1893456000}`
	for _, tc := range []struct {
		name    string
		credits string
		want    []agenttest.TimelineEntry
	}{
		{"credits that hold a balance serve the request", `,"credits":{"hasCredits":true,"unlimited":false,"balance":"52.15"}`, nil},
		{"unlimited credits serve the request", `,"credits":{"hasCredits":false,"unlimited":true,"balance":null}`, nil},
		{"credits that no snapshot stated prove no block", ``, nil},
		{"credits that state no balance block", `,"credits":{"hasCredits":false,"unlimited":false,"balance":"0"}`, []agenttest.TimelineEntry{codexUsageLimitScheduledAt(codexBlockedSnapshotReset)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newCodexAgentAt(t, agentapi.NewProviderServices(sink), codexRateLimitTestNow)

			handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"account/rateLimits/updated","params":{"rateLimits":{"limitId":"codex",`+window+tc.credits+`}}}`)))

			assert.Equal(t, tc.want, sink.Timeline())
		})
	}
}

// Codex states that a null in an update does not clear a value that an
// earlier update gave. A snapshot without the window therefore keeps the
// earlier window and its credits: it never cancels, and the merged block
// states the same reset again.
func TestHandleCodexOutput_ASparseUpdateKeepsTheEarlierWindow(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentAt(t, agentapi.NewProviderServices(sink), codexRateLimitTestNow)
	scheduled := codexUsageLimitScheduledAt(codexBlockedSnapshotReset)

	handleCodexOutput(a, providerkit.ParseLine([]byte(codexBlockedSnapshot)))
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"account/rateLimits/updated","params":{"rateLimits":{"limitId":"codex","primary":null,"secondary":null,"credits":null,"rateLimitReachedType":null}}}`)))
	require.Equal(t, []agenttest.TimelineEntry{scheduled, scheduled}, sink.Timeline(),
		"a snapshot without the window cancels nothing")
	seven, ok := codexRateLimitValues(t, sink)["seven_day"].(map[string]interface{})
	require.True(t, ok, "the display keeps the merged window")
	assert.Equal(t, int64(1893456000), seven["resets_at"])

	// A window without its reset keeps the merged reset, and the merged credits
	// still block.
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"account/rateLimits/updated","params":{"rateLimits":{"limitId":"codex","primary":{"usedPercent":100,"windowDurationMins":10080,"resetsAt":null},"secondary":null,"credits":null}}}`)))

	assert.Equal(t, []agenttest.TimelineEntry{scheduled, scheduled, scheduled}, sink.Timeline())
}

// Credits that an earlier snapshot stated still decide a later window of the
// same bucket.
func TestHandleCodexOutput_MergedCreditsDecideALaterWindow(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentAt(t, agentapi.NewProviderServices(sink), codexRateLimitTestNow)

	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"account/rateLimits/updated","params":{"rateLimits":{"limitId":"codex","primary":{"usedPercent":50,"windowDurationMins":10080,"resetsAt":1893456000},"credits":{"hasCredits":false,"unlimited":false,"balance":"0"}}}}`)))
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"account/rateLimits/updated","params":{"rateLimits":{"limitId":"codex","primary":{"usedPercent":100,"windowDurationMins":10080,"resetsAt":1893456000}}}}`)))

	assert.Equal(t, []agenttest.TimelineEntry{
		codexUsageLimitCancelled(codexCodexWindowCleared), codexUsageLimitScheduledAt(codexBlockedSnapshotReset),
	}, sink.Timeline())
}

// A window of another duration in the same slot is another window, so it
// does not inherit the merged reset.
func TestHandleCodexOutput_AWindowOfAnotherDurationReplacesTheMergedOne(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentAt(t, agentapi.NewProviderServices(sink), codexRateLimitTestNow)

	handleCodexOutput(a, providerkit.ParseLine([]byte(codexBlockedSnapshot)))
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"account/rateLimits/updated","params":{"rateLimits":{"limitId":"codex","primary":{"usedPercent":100,"windowDurationMins":300,"resetsAt":null}}}}`)))

	assert.Equal(t, []agenttest.TimelineEntry{codexUsageLimitScheduledAt(codexBlockedSnapshotReset)}, sink.Timeline(),
		"the five-hour window states no reset, so it schedules nothing")
	values := codexRateLimitValues(t, sink)
	five, ok := values["five_hour"].(map[string]interface{})
	require.True(t, ok)
	assert.NotContains(t, five, "resets_at", "the weekly reset does not move to the five-hour window")
	assert.NotContains(t, values, "seven_day", "the five-hour window replaced the weekly one in the slot")
}

// A reset that already passed would schedule a retry that fires at once and
// fails again, and one more than 31 days ahead is not plausible.
func TestHandleCodexOutput_AnUnusableResetSchedulesNothing(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		now  time.Time
	}{
		{"a reset in the past", codexBlockedSnapshotReset.Add(time.Hour)},
		{"a reset at now", codexBlockedSnapshotReset},
		{"a reset more than 31 days ahead", codexBlockedSnapshotReset.Add(-32 * 24 * time.Hour)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newCodexAgentAt(t, agentapi.NewProviderServices(sink), tc.now)

			handleCodexOutput(a, providerkit.ParseLine([]byte(codexBlockedSnapshot)))
			handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"account/rateLimits/updated","params":{"rateLimits":{"limitId":"codex","rateLimitReachedType":"rate_limit_reached"}}}`)))

			assert.Empty(t, sink.Timeline(), "the block keeps whatever retry is pending")
		})
	}
}

// A snapshot cancels only for its own bucket: a clear of another limit does
// not lift the block of `codex`.
func TestHandleCodexOutput_OnlyAClearOfTheBlockedBucketCancels(t *testing.T) {
	t.Parallel()

	t.Run("a clear of the same bucket cancels", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		a := newCodexAgentAt(t, agentapi.NewProviderServices(sink), codexRateLimitTestNow)

		handleCodexOutput(a, providerkit.ParseLine([]byte(codexBlockedSnapshot)))
		handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"account/rateLimits/updated","params":{"rateLimits":{"limitId":"codex","primary":{"usedPercent":0,"windowDurationMins":10080,"resetsAt":1894000000},"secondary":null,"credits":null}}}`)))

		assert.Equal(t, []agenttest.TimelineEntry{
			codexUsageLimitScheduledAt(codexBlockedSnapshotReset), codexUsageLimitCancelled(codexCodexWindowCleared),
		}, sink.Timeline())
	})
	t.Run("a clear of another bucket does not cancel", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		a := newCodexAgentAt(t, agentapi.NewProviderServices(sink), codexRateLimitTestNow)

		handleCodexOutput(a, providerkit.ParseLine([]byte(codexBlockedSnapshot)))
		handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"account/rateLimits/updated","params":{"rateLimits":{"limitId":"premium","primary":{"usedPercent":0,"windowDurationMins":10080,"resetsAt":1894000000},"secondary":null,"credits":{"hasCredits":false,"unlimited":false,"balance":"0"}}}}`)))

		assert.Equal(t, []agenttest.TimelineEntry{codexUsageLimitScheduledAt(codexBlockedSnapshotReset)}, sink.Timeline())
	})
	t.Run("a clear of another bucket keeps a retry while codex is used up with credits", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		a := newCodexAgentAt(t, agentapi.NewProviderServices(sink), codexRateLimitTestNow)

		// The real sequence reads `codex` at 100% with credits, and the turn then
		// fails on that limit and schedules its retry. A third bucket that clears
		// must not cancel it.
		handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"account/rateLimits/updated","params":{"rateLimits":{"limitId":"codex","primary":{"usedPercent":100,"windowDurationMins":10080,"resetsAt":1893456000},"credits":{"hasCredits":true,"unlimited":false,"balance":"0.4"}}}}`)))
		handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"account/rateLimits/updated","params":{"rateLimits":{"limitId":"codex_spark","primary":{"usedPercent":3,"windowDurationMins":10080,"resetsAt":1894000000}}}}`)))

		assert.Empty(t, sink.Timeline())
	})
	t.Run("a clear of another bucket cancels once no bucket blocks", func(t *testing.T) {
		t.Parallel()
		sink := &agenttest.Sink{}
		a := newCodexAgentAt(t, agentapi.NewProviderServices(sink), codexRateLimitTestNow)

		handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"account/rateLimits/updated","params":{"rateLimits":{"limitId":"codex","primary":{"usedPercent":40,"windowDurationMins":10080,"resetsAt":1893456000}}}}`)))
		handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"account/rateLimits/updated","params":{"rateLimits":{"limitId":"premium","primary":{"usedPercent":0,"windowDurationMins":10080,"resetsAt":1894000000}}}}`)))

		assert.Equal(t, []agenttest.TimelineEntry{
			codexUsageLimitCancelled(codexCodexWindowCleared),
			codexUsageLimitCancelled(`Codex reported that no usage window of the "premium" limit is exceeded`),
		}, sink.Timeline())
	})
}

// A snapshot without a `limitId`, or with an empty one, describes the `codex`
// limit.
func TestHandleCodexOutput_ASnapshotWithoutALimitIDUpdatesTheCodexBucket(t *testing.T) {
	t.Parallel()

	for name, blocked := range map[string]string{
		"an absent ID": `{"method":"account/rateLimits/updated","params":{"rateLimits":{"primary":{"usedPercent":100,"windowDurationMins":10080,"resetsAt":1893456000},"credits":{"hasCredits":false,"unlimited":false}}}}`,
		"a null ID":    `{"method":"account/rateLimits/updated","params":{"rateLimits":{"limitId":null,"primary":{"usedPercent":100,"windowDurationMins":10080,"resetsAt":1893456000},"credits":{"hasCredits":false,"unlimited":false}}}}`,
		"an empty ID":  `{"method":"account/rateLimits/updated","params":{"rateLimits":{"limitId":"","primary":{"usedPercent":100,"windowDurationMins":10080,"resetsAt":1893456000},"credits":{"hasCredits":false,"unlimited":false}}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newCodexAgentAt(t, agentapi.NewProviderServices(sink), codexRateLimitTestNow)

			handleCodexOutput(a, providerkit.ParseLine([]byte(blocked)))
			handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"account/rateLimits/updated","params":{"rateLimits":{"limitId":"codex","primary":{"usedPercent":5,"windowDurationMins":10080}}}}`)))

			assert.Equal(t, []agenttest.TimelineEntry{
				codexUsageLimitScheduledAt(codexBlockedSnapshotReset), codexUsageLimitCancelled(codexCodexWindowCleared),
			}, sink.Timeline())
		})
	}
}

// The display shows the `codex` bucket when it holds a window, or else the
// first bucket seen that holds one.
func TestHandleCodexOutput_TheDisplayShowsTheCodexBucketFirst(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentAt(t, agentapi.NewProviderServices(sink), codexRateLimitTestNow)
	displayedUtilization := func() interface{} {
		seven, ok := codexRateLimitValues(t, sink)["seven_day"].(map[string]interface{})
		require.True(t, ok)
		return seven["utilization"]
	}

	handleCodexOutput(a, providerkit.ParseLine([]byte(codexRealPremiumSnapshot)))
	assert.Equal(t, map[string]interface{}{"account_block": map[string]interface{}{}}, codexRateLimitValues(t, sink),
		"no bucket holds a window yet")

	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"account/rateLimits/updated","params":{"rateLimits":{"limitId":"alpha","primary":{"usedPercent":30,"windowDurationMins":10080}}}}`)))
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"account/rateLimits/updated","params":{"rateLimits":{"limitId":"beta","primary":{"usedPercent":60,"windowDurationMins":10080}}}}`)))
	assert.Equal(t, 0.3, displayedUtilization(), "the first bucket seen with a window")

	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"account/rateLimits/updated","params":{"rateLimits":{"limitId":"codex","primary":{"usedPercent":10,"windowDurationMins":10080}}}}`)))
	assert.Equal(t, 0.1, displayedUtilization(), "the codex bucket")

	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"account/rateLimits/updated","params":{"rateLimits":{"limitId":"beta","primary":{"usedPercent":90,"windowDurationMins":10080}}}}`)))
	assert.Equal(t, 0.1, displayedUtilization(), "another bucket's update keeps the codex bucket on display")
}

// Codex reports account rate limits after every model call.
// One turn with a tool call formerly persisted the same rate-limit sentence twice.
// A repeated unchanged snapshot supplies no new state and therefore needs no transcript row.
func TestHandleCodexOutput_RateLimitsStayOutOfTheTranscript(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

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

// TestHandleCodexOutput_RateLimitBroadcastsSnakeCaseWire verifies the snake_case rate_limits shape in Codex session information.
// Codex and Claude broadcast the same tier fields, so the frontend reads one format.
func TestHandleCodexOutput_RateLimitBroadcastsSnakeCaseWire(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

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

// A window that states no duration keeps a display key of its own.
func TestHandleCodexOutput_AWindowWithoutADurationKeepsItsOwnKey(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"account/rateLimits/updated","params":{"rateLimits":{"primary":{"usedPercent":10,"windowDurationMins":null},"secondary":{"usedPercent":20}}}}`)))

	values := codexRateLimitValues(t, sink)
	assert.Contains(t, values, "window_1")
	assert.Contains(t, values, "window_2")
}

// A clear cancels the resume that an earlier block of the same bucket
// scheduled.
func TestHandleCodexOutput_RateLimitClearCancelsResume(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentAt(t, agentapi.NewProviderServices(sink), codexRateLimitTestNow)

	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"account/rateLimits/updated","params":{"rateLimits":{"primary":{"usedPercent":100,"windowDurationMins":300,"resetsAt":1893456000},"secondary":{"usedPercent":10,"windowDurationMins":10080,"resetsAt":1894000000},"credits":{"hasCredits":false,"unlimited":false}}}}`)))
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"account/rateLimits/updated","params":{"rateLimits":{"primary":{"usedPercent":75,"windowDurationMins":300,"resetsAt":1893456000},"secondary":{"usedPercent":10,"windowDurationMins":10080,"resetsAt":1894000000}}}}`)))

	assert.Equal(t, []agenttest.TimelineEntry{
		codexUsageLimitScheduledAt(time.Unix(1893456000, 0).UTC()), codexUsageLimitCancelled(codexCodexWindowCleared),
	}, sink.Timeline())
}

// TestHandleCodexOutput_ReachedTypeRateLimitReachedSchedules covers a native rateLimitReachedType that confirms a timed rate-limit window.
// Schedule a resume when that window blocks the request.
func TestHandleCodexOutput_ReachedTypeRateLimitReachedSchedules(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentAt(t, agentapi.NewProviderServices(sink), codexRateLimitTestNow)

	input := `{"method":"account/rateLimits/updated","params":{"rateLimits":{"rateLimitReachedType":"rate_limit_reached","primary":{"usedPercent":100,"windowDurationMins":300,"resetsAt":1893456000},"secondary":{"usedPercent":20,"windowDurationMins":10080,"resetsAt":1894000000}}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

	require.Equal(t, 1, sink.AutoScheduleCount())
	assert.True(t, sink.LastAutoSchedule().ResetAt.Equal(time.Unix(1893456000, 0).UTC()))
}

// TestHandleCodexOutput_ReachedTypeCreditsDepletedCancels verifies that credit depletion has no rolling-window reset.
// Cancel automatic resume even at 100% usage because another timed attempt cannot restore those credits.
func TestHandleCodexOutput_ReachedTypeCreditsDepletedCancels(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

	input := `{"method":"account/rateLimits/updated","params":{"rateLimits":{"rateLimitReachedType":"workspace_owner_credits_depleted","primary":{"usedPercent":100,"windowDurationMins":300,"resetsAt":1893456000}}}}`
	handleCodexOutput(a, providerkit.ParseLine([]byte(input)))

	assert.Equal(t, 0, sink.AutoScheduleCount(), "credit depletion must not schedule an automatic retry")
	require.Equal(t, 1, sink.AutoCancelCount())
	assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT, sink.LastAutoCancel())
	assert.Equal(t, `Codex reported the block "workspace_owner_credits_depleted", which only a billing or usage change lifts`,
		sink.LastAutoCancelReason())
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

// TestHandleCodexOutput_ReachedTypeUsageLimitReachedCancels verifies an administrator's usage cap without a timed reset.
// As with credit depletion, schedule no resume.
func TestHandleCodexOutput_ReachedTypeUsageLimitReachedCancels(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

	input := `{"method":"account/rateLimits/updated","params":{"rateLimits":{"rateLimitReachedType":"workspace_member_usage_limit_reached","primary":{"usedPercent":100,"windowDurationMins":300,"resetsAt":1893456000}}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

	assert.Equal(t, 0, sink.AutoScheduleCount())
	require.Equal(t, 1, sink.AutoCancelCount())
	assert.Equal(t, `Codex reported the block "workspace_member_usage_limit_reached", which only a billing or usage change lifts`,
		sink.LastAutoCancelReason())
}

// TestHandleCodexOutput_ReachedTypeRoundingElevatesAndSchedules covers a confirmed native rate limit while rounded usedPercent remains below 100.
// Use the window with highest usage to determine resume time and mark that window exceeded in the popover broadcast.
func TestHandleCodexOutput_ReachedTypeRoundingElevatesAndSchedules(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentAt(t, agentapi.NewProviderServices(sink), codexRateLimitTestNow)

	input := `{"method":"account/rateLimits/updated","params":{"rateLimits":{"rateLimitReachedType":"rate_limit_reached","primary":{"usedPercent":99,"windowDurationMins":300,"resetsAt":1893456000},"secondary":{"usedPercent":20,"windowDurationMins":10080,"resetsAt":1894000000}}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

	require.Equal(t, 1, sink.AutoScheduleCount())
	assert.True(t, sink.LastAutoSchedule().ResetAt.Equal(time.Unix(1893456000, 0).UTC()),
		"resume should bind to the most-utilized window's reset")

	require.Equal(t, 1, sink.SessionInfoCount())
	primary, ok := codexRateLimitValues(t, sink)["five_hour"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "exceeded", primary["status"], "binding window must show as exceeded despite 99%%")
}

// The reached type confirms the most-used window of its own bucket only. A
// `premium` snapshot that reaches its limit does not mark the displayed `codex`
// window.
func TestHandleCodexOutput_AReachedTypeOfAnotherBucketMarksNoDisplayedWindow(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentAt(t, agentapi.NewProviderServices(sink), codexRateLimitTestNow)

	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"account/rateLimits/updated","params":{"rateLimits":{"limitId":"codex","primary":{"usedPercent":40,"windowDurationMins":10080,"resetsAt":1893456000}}}}`)))
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"account/rateLimits/updated","params":{"rateLimits":{"limitId":"premium","rateLimitReachedType":"rate_limit_reached","primary":{"usedPercent":99,"windowDurationMins":300,"resetsAt":1893456000}}}}`)))

	seven, ok := codexRateLimitValues(t, sink)["seven_day"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "allowed", seven["status"])
	assert.Equal(t, []agenttest.TimelineEntry{
		codexUsageLimitCancelled(codexCodexWindowCleared), codexUsageLimitScheduledAt(time.Unix(1893456000, 0).UTC()),
	}, sink.Timeline(), "the premium block still schedules at its own reset")
}

// TestHandleCodexOutput_ReachedTypeBindingWindowMissingResetFallsBack covers a timed block whose highest-usage window omits resetsAt while another window supplies it.
// Use the other window's reset instead of cancelling resume for a limit that still resets on its timer.
func TestHandleCodexOutput_ReachedTypeBindingWindowMissingResetFallsBack(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentAt(t, agentapi.NewProviderServices(sink), codexRateLimitTestNow)

	// primary, the five_hour window, has highest usage but no resetsAt.
	// secondary, the seven_day window, supplies a reset.
	// The preceding implementation cancelled resume when the selected window's reset was absent.
	input := `{"method":"account/rateLimits/updated","params":{"rateLimits":{"rateLimitReachedType":"rate_limit_reached","primary":{"usedPercent":99,"windowDurationMins":300},"secondary":{"usedPercent":20,"windowDurationMins":10080,"resetsAt":1894000000}}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

	require.Equal(t, 1, sink.AutoScheduleCount(), "a resumable time-window block must not be cancelled when the binding window lacks a reset")
	assert.Equal(t, 0, sink.AutoCancelCount())
	assert.True(t, sink.LastAutoSchedule().ResetAt.Equal(time.Unix(1894000000, 0).UTC()),
		"resume falls back to the latest available window reset")
}

// TestHandleCodexOutput_BlockWithoutAResetKeepsThePendingResume covers a
// snapshot that reports a timed block but gives no reset instant. The account
// is still blocked, so the snapshot supplies no new deadline, and the retry
// that an earlier snapshot scheduled keeps its own. Claude keeps a pending
// resume for an undated block by the same rule.
func TestHandleCodexOutput_BlockWithoutAResetKeepsThePendingResume(t *testing.T) {
	t.Parallel()
	for name, input := range map[string]string{
		"a reached time window": `{"method":"account/rateLimits/updated","params":{"rateLimits":{"rateLimitReachedType":"rate_limit_reached","primary":{"usedPercent":99,"windowDurationMins":300},"secondary":{"usedPercent":20,"windowDurationMins":10080}}}}`,
		"an exhausted window":   `{"method":"account/rateLimits/updated","params":{"rateLimits":{"primary":{"usedPercent":100,"windowDurationMins":300},"secondary":{"usedPercent":20,"windowDurationMins":10080},"credits":{"hasCredits":false,"unlimited":false}}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

			handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

			assert.Zero(t, sink.AutoScheduleCount(), "a block without a reset instant supplies no deadline")
			assert.Zero(t, sink.AutoCancelCount(), "a block without a reset instant must not cancel the pending resume")
		})
	}
}

// TestReadCodexRateLimitBucket reads the resets that the decisions use from
// one merged bucket, without an agent.
func TestReadCodexRateLimitBucket(t *testing.T) {
	t.Parallel()

	now := codexRateLimitTestNow
	soon := now.Add(time.Hour).Unix()
	later := now.Add(2 * time.Hour).Unix()
	past := now.Add(-time.Hour).Unix()
	at := func(unix int64) *time.Time {
		instant := time.Unix(unix, 0).UTC()
		return &instant
	}
	for _, tc := range []struct {
		name   string
		bucket codexRateLimitBucket
		want   codexRateLimitReading
	}{
		{
			name: "the most-used window binds, and a window without a reset gives none",
			bucket: codexRateLimitBucket{
				primary:   &codexRateLimitTier{UsedPercent: 99, WindowDurationMins: 300},
				secondary: &codexRateLimitTier{UsedPercent: 20, WindowDurationMins: 10080, ResetsAt: &later},
			},
			want: codexRateLimitReading{latestReset: at(later)},
		},
		{
			name: "a used-up window gives the exceeded reset",
			bucket: codexRateLimitBucket{
				primary:   &codexRateLimitTier{UsedPercent: 100, WindowDurationMins: 300, ResetsAt: &soon},
				secondary: &codexRateLimitTier{UsedPercent: 20, WindowDurationMins: 10080, ResetsAt: &later},
				credits:   &codexRateLimitCredits{},
			},
			want: codexRateLimitReading{
				latestExceededReset: at(soon), bindingReset: at(soon), latestReset: at(later),
				anyExceeded: true, creditsBlock: true,
			},
		},
		{
			name: "a past reset reads as absent",
			bucket: codexRateLimitBucket{
				primary: &codexRateLimitTier{UsedPercent: 100, WindowDurationMins: 300, ResetsAt: &past},
			},
			want: codexRateLimitReading{anyExceeded: true},
		},
		{
			name:   "an empty bucket reads nothing",
			bucket: codexRateLimitBucket{},
			want:   codexRateLimitReading{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, readCodexRateLimitBucket(&tc.bucket, now))
		})
	}
}

// TestCodexUsageLimitRetryFor states what each kind of snapshot does to the
// pending usage-limit retry: which reset it resumes at, when it keeps the
// retry, and the reason of each cancellation.
func TestCodexUsageLimitRetryFor(t *testing.T) {
	t.Parallel()
	exceededReset := time.Unix(1894000300, 0).UTC()
	bindingReset := time.Unix(1894000200, 0).UTC()
	latestReset := time.Unix(1894000100, 0).UTC()
	for _, tc := range []struct {
		name              string
		reachedType       string
		carriedWindow     bool
		reading           codexRateLimitReading
		otherBucketBlocks bool
		resumeAt          *time.Time
		cancelReason      string
	}{
		{
			name:          "a window that blocks resumes at its reset",
			carriedWindow: true,
			reading:       codexRateLimitReading{anyExceeded: true, creditsBlock: true, latestExceededReset: &exceededReset, bindingReset: &bindingReset, latestReset: &latestReset},
			resumeAt:      &exceededReset,
		},
		{
			name:          "a used-up window that credits serve keeps the retry",
			carriedWindow: true,
			reading:       codexRateLimitReading{anyExceeded: true, latestExceededReset: &exceededReset},
		},
		{
			name:          "a window that blocks without a reset keeps the retry",
			carriedWindow: true,
			reading:       codexRateLimitReading{anyExceeded: true, creditsBlock: true, bindingReset: &bindingReset, latestReset: &latestReset},
		},
		{
			name:          "no used-up window cancels, whatever the other windows reset at",
			carriedWindow: true,
			reading:       codexRateLimitReading{bindingReset: &bindingReset, latestReset: &latestReset},
			cancelReason:  codexCodexWindowCleared,
		},
		{
			name:              "a clear keeps the retry while another bucket blocks",
			carriedWindow:     true,
			reading:           codexRateLimitReading{bindingReset: &bindingReset, latestReset: &latestReset},
			otherBucketBlocks: true,
		},
		{
			name:    "a snapshot without a window keeps the retry",
			reading: codexRateLimitReading{bindingReset: &bindingReset, latestReset: &latestReset},
		},
		{
			name:        "a reached time window resumes at the exceeded reset first",
			reachedType: codexRateLimitReachedTimeWindow,
			reading:     codexRateLimitReading{latestExceededReset: &exceededReset, bindingReset: &bindingReset, latestReset: &latestReset},
			resumeAt:    &exceededReset,
		},
		{
			name:        "a reached time window resumes at the binding reset next",
			reachedType: codexRateLimitReachedTimeWindow,
			reading:     codexRateLimitReading{bindingReset: &bindingReset, latestReset: &latestReset},
			resumeAt:    &bindingReset,
		},
		{
			name:        "a reached time window resumes at the latest reset last",
			reachedType: codexRateLimitReachedTimeWindow,
			reading:     codexRateLimitReading{latestReset: &latestReset},
			resumeAt:    &latestReset,
		},
		{
			name:          "a reached time window without any reset keeps the retry",
			reachedType:   codexRateLimitReachedTimeWindow,
			carriedWindow: true,
		},
		{
			name:          "a billing block cancels, even with a window that blocks and its reset",
			reachedType:   "workspace_owner_credits_depleted",
			carriedWindow: true,
			reading:       codexRateLimitReading{anyExceeded: true, creditsBlock: true, latestExceededReset: &exceededReset},
			cancelReason:  `Codex reported the block "workspace_owner_credits_depleted", which only a billing or usage change lifts`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			retry := codexUsageLimitRetryFor(codexRateLimitUpdate{
				bucketID: codexDefaultRateLimitID, reachedType: tc.reachedType, carriedWindow: tc.carriedWindow,
				bucket: tc.reading, otherBucketBlocks: tc.otherBucketBlocks,
			})
			if tc.resumeAt == nil {
				assert.Nil(t, retry.resumeAt)
			} else if assert.NotNil(t, retry.resumeAt) {
				assert.True(t, retry.resumeAt.Equal(*tc.resumeAt), "resumes at %v, want %v", *retry.resumeAt, *tc.resumeAt)
			}
			assert.Equal(t, tc.cancelReason, retry.cancelReason)
		})
	}
}

// TestCodexRateLimitDisplay_AnExceededWindowPreventsElevation verifies that
// any window at or above 100% suppresses additional elevation. The frontend
// replay applies the same rule, keeping its popover consistent with live
// broadcasts. Preserve the already-exceeded window's status and never raise a
// lower-usage sibling's status.
func TestCodexRateLimitDisplay_AnExceededWindowPreventsElevation(t *testing.T) {
	t.Parallel()

	values := codexRateLimitDisplay([]*codexRateLimitTier{
		{UsedPercent: 100, WindowDurationMins: 300},  // exceeded by status, no reset
		{UsedPercent: 20, WindowDurationMins: 10080}, // low sibling
	}, codexRateLimitReachedTimeWindow, true)

	five, ok := values["five_hour"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "exceeded", five["status"])
	seven, ok := values["seven_day"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "allowed", seven["status"], "a low sibling must not be elevated when another window is already exceeded")
}

func TestHandleCodexOutput_TurnFailedServerOverloadedSchedulesResume(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	ag := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

	input := `{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"019d8b39-6599-7081-8901-53f80c6c56b7","items":[],"status":"failed","error":{"message":"Selected model is at capacity. Please try a different model.","codexErrorInfo":"serverOverloaded","additionalDetails":null}}}}`
	handleCodexOutput(ag, providerkit.ParseLine([]byte(input)))

	require.Equal(t, 1, sink.AutoScheduleCount())
	schedule := sink.LastAutoSchedule()
	require.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, schedule.Kind)
	require.True(t, schedule.ResetAt.IsZero(), "the queue derives a server-error deadline; the provider supplies no instant")
	require.NotEmpty(t, schedule.SourcePayload)
}

func TestHandleCodexOutput_TurnFailedNonOverloadedCancelsAPIErrorResume(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

	input := `{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","items":[],"status":"failed","error":{"message":"Something else failed","codexErrorInfo":"invalidRequest","additionalDetails":null}}}}`
	handleCodexOutput(a, providerkit.ParseLine([]byte(input)))

	require.Equal(t, 1, sink.AutoCancelCount())
	require.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, sink.LastAutoCancel())
}

func TestHandleCodexOutput_TurnCompletedFailedRetryableSchedulesAPIError(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	a.threadID = "main-thread"

	input := `{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"failed","items":[],"error":{"message":"stream disconnected before completion: An error occurred while processing your request.","codexErrorInfo":"other","additionalDetails":null}}}}`
	handleCodexOutput(a, providerkit.ParseLine([]byte(input)))

	require.Equal(t, 1, sink.MessageCount())
	require.Equal(t, 1, sink.AutoScheduleCount())
	schedule := sink.LastAutoSchedule()
	require.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, schedule.Kind)
	require.Equal(t, string(sink.Messages()[0].Content), string(schedule.SourcePayload))
	require.Equal(t, 0, sink.AutoCancelCount())
}

// An object variant of codexErrorInfo carries the upstream HTTP status. A
// field that decoded only a string failed the whole decode, so the turn read
// as one without a status and its retry decision never ran.
func TestHandleCodexOutput_AnObjectErrorInfoDecidesTheRetry(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		info string
		want agenttest.TimelineEntry
	}{
		{"a disconnected stream with a 502 schedules", `{"responseStreamDisconnected":{"httpStatusCode":502}}`, codexServerErrorScheduled},
		{"a failed connection with no status schedules", `{"httpConnectionFailed":{"httpStatusCode":null}}`, codexServerErrorScheduled},
		{"a failed connection with a 401 cancels", `{"httpConnectionFailed":{"httpStatusCode":401}}`, codexServerErrorCancelled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

			input := `{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"failed","items":[],"error":{"message":"error sending request","codexErrorInfo":` + tc.info + `,"additionalDetails":null}}}}`
			handleCodexOutput(a, providerkit.ParseLine([]byte(input)))

			assert.Equal(t, []agenttest.TimelineEntry{codexTurnEndRow, tc.want}, sink.Timeline())
		})
	}
}

// A rate limit that outlasted Codex's own retries states no reset, so it
// schedules a server-error retry (decision 3).
func TestHandleCodexOutput_ARateLimitExceededTurnSchedulesAServerErrorRetry(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

	input := `{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"failed","items":[],"error":{"message":"exceeded retry limit, last status: 429 Too Many Requests","codexErrorInfo":"rateLimitExceeded","additionalDetails":null}}}}`
	handleCodexOutput(a, providerkit.ParseLine([]byte(input)))

	assert.Equal(t, []agenttest.TimelineEntry{codexTurnEndRow, codexServerErrorScheduled}, sink.Timeline())
	require.Equal(t, 1, sink.AutoScheduleCount())
	assert.JSONEq(t, string(sink.Messages()[0].Content), string(sink.LastAutoSchedule().SourcePayload), "the retry carries the turn end")
}

// The plan prompt reads the same decode, so an object variant must not hide
// it.
func TestHandleCodexOutput_AnObjectErrorInfoStillPublishesThePlanPrompt(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	a.collaborationMode = CollaborationPlan

	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"plan","id":"plan-1"}}}`)))
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"plan","id":"plan-1","text":"# Plan\n\n- step\n"}}}`)))
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"completed","items":[],"error":{"message":"","codexErrorInfo":{"activeTurnNotSteerable":{"turnKind":"review"}}}}}}`)))

	require.Equal(t, 1, sink.PublishedControlCount(), "the plan prompt fires")
	assert.Equal(t, "codex-plan-prompt-turn-1", sink.LastPublishedControl().RequestID)
	assert.Equal(t, 1, sink.PlanUpdateCount())
}

func TestHandleCodexOutput_TurnCompletedFailedNonRetryableCancelsAPIError(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	a.threadID = "main-thread"

	input := `{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"failed","items":[],"error":{"message":"Request was aborted by the user.","codexErrorInfo":"other","additionalDetails":null}}}}`
	handleCodexOutput(a, providerkit.ParseLine([]byte(input)))

	require.Equal(t, 0, sink.AutoScheduleCount())
	require.Equal(t, 1, sink.AutoCancelCount())
	require.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, sink.LastAutoCancel())
}

func TestHandleCodexOutput_TurnCompletedSuccessCancelsAPIError(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	a.threadID = "main-thread"

	input := `{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"completed","items":[],"error":null}}}`
	handleCodexOutput(a, providerkit.ParseLine([]byte(input)))

	require.Equal(t, 0, sink.AutoScheduleCount())
	require.Equal(t, 1, sink.AutoCancelCount())
	require.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, sink.LastAutoCancel())
	assert.Equal(t, providerkit.TurnEndedWithoutRetryableFailure, sink.LastAutoCancelReason())
}

// Every turn end applies one verdict. A turn/completed whose turn the decode
// cannot read still ends the turn, so it still cancels the server-error retry.
func TestHandleCodexOutput_AnUnreadableTurnEndStillAppliesOneVerdict(t *testing.T) {
	t.Parallel()

	for name, input := range map[string]string{
		"no turn":           `{"method":"turn/completed","params":{"threadId":"main-thread"}}`,
		"a turn of strings": `{"method":"turn/completed","params":{"threadId":"main-thread","turn":"failed"}}`,
		"no status":         `{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","items":[],"error":{"message":"Selected model is at capacity. Please try a different model.","codexErrorInfo":"serverOverloaded"}}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

			handleCodexOutput(a, providerkit.ParseLine([]byte(input)))

			assert.Equal(t, []agenttest.TimelineEntry{codexTurnEndRow, codexServerErrorCancelled}, sink.Timeline())
		})
	}
}

// A turn that LeapMux asked Codex to interrupt is never retried, even when
// Codex states that it failed on a retryable error before it read the stop.
func TestHandleCodexOutput_AStoppedTurnCancelsTheServerErrorRetry(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	a.interruptCalls = map[codexInterruptKey]*codexInterruptCall{
		{threadID: "main-thread", turnID: "turn-1"}: {done: make(chan struct{})},
	}

	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"failed","items":[],"error":{"message":"Selected model is at capacity. Please try a different model.","codexErrorInfo":"serverOverloaded"}}}}`)))

	assert.Equal(t, []agenttest.TimelineEntry{codexTurnEndRow, codexServerErrorCancelled}, sink.Timeline())
	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Empty(t, a.interruptCalls, "the turn end still clears the interrupt")
}

// An interrupt of an earlier turn does not mark this one.
func TestHandleCodexOutput_AnInterruptOfAnotherTurnKeepsTheRetry(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	a.interruptCalls = map[codexInterruptKey]*codexInterruptCall{
		{threadID: "main-thread", turnID: "turn-0"}: {done: make(chan struct{})},
	}

	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"failed","items":[],"error":{"message":"Selected model is at capacity. Please try a different model.","codexErrorInfo":"serverOverloaded"}}}}`)))

	assert.Equal(t, []agenttest.TimelineEntry{codexTurnEndRow, codexServerErrorScheduled}, sink.Timeline())
}

// A child thread's turn end is not a turn end of the session, so it applies
// no verdict.
func TestHandleCodexOutput_AChildTurnFailureAppliesNoVerdict(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"call-spawn","kind":"started","agentThreadId":"child-thread","agentPath":"/root/probe_child"}}}`)))
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"call-spawn","kind":"started","agentThreadId":"child-thread","agentPath":"/root/probe_child"}}}`)))
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"child-thread","turn":{"id":"child-turn"}}}`)))
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"child-thread","turn":{"id":"child-turn","status":"failed","items":[],"error":{"message":"Selected model is at capacity. Please try a different model.","codexErrorInfo":"serverOverloaded"}}}}`)))

	assert.Contains(t, sink.Child("child-of-call-spawn").Timeline(), codexTurnEndRow, "the child transcript records its turn end")
	assert.Zero(t, sink.AutoScheduleCount())
	assert.Zero(t, sink.AutoCancelCount())
	assert.Zero(t, sink.Child("child-of-call-spawn").AutoScheduleCount())
	assert.Zero(t, sink.Child("child-of-call-spawn").AutoCancelCount())
}

// A turn that fails on a usage limit is not a server error. The turn end
// schedules the usage-limit retry at the time in the message, and it cancels
// only the server-error retry. A command whose completion arrives after that
// turn ended cancels nothing, so the usage-limit retry survives both.
func TestHandleCodexOutput_UsageLimitTurnFailureAndALateCommandLeaveTheUsageLimitRetry(t *testing.T) {
	t.Parallel()

	seoul, err := time.LoadLocation("Asia/Seoul")
	require.NoError(t, err)
	sink := &agenttest.Sink{}
	a := newCodexAgentAt(t, agentapi.NewProviderServices(sink), codexRealUsageLimitAt)
	a.threadID = "main-thread"
	a.retryTextLocation = seoul

	failed := `{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"failed","items":[],"error":{"message":"You've hit your usage limit. Try again at Nov 2nd, 2026 3:00 PM.","codexErrorInfo":"usageLimitExceeded","additionalDetails":null}}}}`
	handleCodexOutput(a, providerkit.ParseLine([]byte(failed)))

	late := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"commandExecution","id":"exec-1","status":"completed","command":"gh run watch","cwd":"/tmp","processId":"123","commandActions":[],"aggregatedOutput":"EXIT: 1","exitCode":0,"durationMs":5754717}}}`
	handleCodexOutput(a, providerkit.ParseLine([]byte(late)))

	timeline := sink.Timeline()
	require.NotEmpty(t, timeline)
	assert.Equal(t, []agenttest.TimelineEntry{
		codexTurnEndRow,
		codexUsageLimitScheduledAt(time.Date(2026, 11, 2, 6, 1, 0, 0, time.UTC)),
		codexServerErrorCancelledForAStatedReset,
	}, timeline[:3])
	for _, entry := range timeline[3:] {
		assert.NotContains(t, entry.Op, "_usage_limit", "a late command completion schedules or cancels no retry")
		assert.NotContains(t, entry.Op, "_server_error", "a late command completion schedules or cancels no retry")
	}
}

// Without a usable reset in the `codex` bucket, the turn reads the local time
// in the message. Codex writes that time in its own process's zone, so the
// test pins the zone. The test stays parallel because it sets the agent's own
// field, not time.Local.
func TestHandleCodexOutput_AUsageLimitTurnReadsTheTimeInTheMessage(t *testing.T) {
	t.Parallel()

	seoul, err := time.LoadLocation("Asia/Seoul")
	require.NoError(t, err)
	for _, tc := range []struct {
		name    string
		now     time.Time
		message string
		want    []agenttest.TimelineEntry
	}{
		{
			name:    "the dated form with a typographic apostrophe",
			now:     codexRealUsageLimitAt,
			message: "You’ve hit your usage limit. Visit https://chatgpt.com/settings/usage to purchase more credits or try again at Nov 2nd, 2026 3:00 PM.",
			want: []agenttest.TimelineEntry{
				codexTurnEndRow, codexUsageLimitScheduledAt(time.Date(2026, 11, 2, 6, 1, 0, 0, time.UTC)), codexServerErrorCancelledForAStatedReset,
			},
		},
		{
			// The text of Codex 0.153.1. Its clock is shifted to the day before
			// the stated time.
			name:    "the dated form with an ASCII apostrophe",
			now:     time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC),
			message: "You've hit your usage limit. Visit https://chatgpt.com/codex/settings/usage to purchase more credits or try again at Sep 11th, 2026 10:18 PM.",
			want: []agenttest.TimelineEntry{
				codexTurnEndRow, codexUsageLimitScheduledAt(time.Date(2026, 9, 11, 13, 19, 0, 0, time.UTC)), codexServerErrorCancelledForAStatedReset,
			},
		},
		{
			// 10:00 in Seoul, so 3:00 PM is later on the same local day.
			name:    "the same-day form",
			now:     time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC),
			message: "You’ve hit your usage limit. Visit https://chatgpt.com/settings/usage to purchase more credits or try again at 3:00 PM.",
			want: []agenttest.TimelineEntry{
				codexTurnEndRow, codexUsageLimitScheduledAt(time.Date(2026, 10, 28, 6, 1, 0, 0, time.UTC)), codexServerErrorCancelledForAStatedReset,
			},
		},
		{
			// 23:30 in Seoul on Oct 27, which is 14:30 UTC: the local day
			// decides the date, not the UTC one.
			name:    "the same-day form near midnight",
			now:     time.Date(2026, 10, 27, 14, 30, 0, 0, time.UTC),
			message: "You’ve hit your usage limit. Try again at 11:45 PM.",
			want: []agenttest.TimelineEntry{
				codexTurnEndRow, codexUsageLimitScheduledAt(time.Date(2026, 10, 27, 14, 46, 0, 0, time.UTC)), codexServerErrorCancelledForAStatedReset,
			},
		},
		{
			name:    "a time that already passed",
			now:     time.Date(2026, 10, 28, 7, 0, 0, 0, time.UTC),
			message: "You’ve hit your usage limit. Try again at 3:00 PM.",
			want:    []agenttest.TimelineEntry{codexTurnEndRow, codexServerErrorCancelledForNoReset},
		},
		{
			name:    "a time more than 31 days ahead",
			now:     codexRealUsageLimitAt,
			message: "You’ve hit your usage limit. Try again at Jan 2nd, 2027 3:00 PM.",
			want:    []agenttest.TimelineEntry{codexTurnEndRow, codexServerErrorCancelledForNoReset},
		},
		{
			name:    "a text without a time",
			now:     codexRealUsageLimitAt,
			message: "You’ve hit your usage limit. Upgrade to Pro to continue.",
			want:    []agenttest.TimelineEntry{codexTurnEndRow, codexServerErrorCancelledForNoReset},
		},
		{
			name:    "a day that the month lacks",
			now:     time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC),
			message: "You’ve hit your usage limit. Try again at Feb 30th, 2027 3:00 PM.",
			want:    []agenttest.TimelineEntry{codexTurnEndRow, codexServerErrorCancelledForNoReset},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newCodexAgentAt(t, agentapi.NewProviderServices(sink), tc.now)
			a.retryTextLocation = seoul

			frame, err := json.Marshal(map[string]any{
				"method": "turn/completed",
				"params": map[string]any{"threadId": "main-thread", "turn": map[string]any{
					"id": "turn-1", "status": "failed", "items": []any{},
					"startedAt": tc.now.Add(-time.Second).Unix(), "completedAt": tc.now.Unix(),
					"error": map[string]any{"message": tc.message, "codexErrorInfo": "usageLimitExceeded", "additionalDetails": nil},
				}},
			})
			require.NoError(t, err)
			handleCodexOutput(a, providerkit.ParseLine(frame))

			assert.Equal(t, tc.want, sink.Timeline())
		})
	}
}

// A reset of the `codex` bucket that already passed is not used: the turn
// falls back to the time in its message.
func TestHandleCodexOutput_AUsageLimitTurnSkipsAPastBucketReset(t *testing.T) {
	t.Parallel()

	seoul, err := time.LoadLocation("Asia/Seoul")
	require.NoError(t, err)
	sink := &agenttest.Sink{}
	a := newCodexAgentAt(t, agentapi.NewProviderServices(sink), codexRealUsageLimitAt)
	a.threadID = codexRealThreadID
	a.retryTextLocation = seoul

	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"account/rateLimits/updated","params":{"rateLimits":{"limitId":"codex","primary":{"usedPercent":100,"windowDurationMins":10080,"resetsAt":1792887890},"credits":{"hasCredits":true,"unlimited":false}}}}`)))
	handleCodexOutput(a, providerkit.ParseLine([]byte(codexRealUsageLimitTurn)))

	assert.Equal(t, []agenttest.TimelineEntry{
		codexTurnEndRow, codexUsageLimitScheduledAt(time.Date(2026, 11, 2, 6, 1, 0, 0, time.UTC)), codexServerErrorCancelledForAStatedReset,
	}, sink.Timeline())
}

// TestCodexTurnFailure classifies each failed turn that Codex can state, with
// no usage-limit reset to find.
func TestCodexTurnFailure(t *testing.T) {
	t.Parallel()

	now := codexRealUsageLimitAt
	transient := providerkit.TurnFailureTransient
	notRetryable := providerkit.TurnFailureNotRetryable
	for _, tc := range []struct {
		name    string
		info    string
		message string
		want    providerkit.TurnFailureKind
	}{
		{"an overload", `"serverOverloaded"`, "Selected model is at capacity. Please try a different model.", transient},
		{"an internal server error", `"internalServerError"`, "", transient},
		{"an unavailable flex tier", `"flexUnavailable"`, "", transient},
		{"a stream that disconnected, as a string", `"responseStreamDisconnected"`, "", transient},
		{"a stream that disconnected with a 502", `{"responseStreamDisconnected":{"httpStatusCode":502}}`, "", transient},
		{"a stream that disconnected with a 400", `{"responseStreamDisconnected":{"httpStatusCode":400}}`, "", transient},
		{"a failed connection with a 503", `{"httpConnectionFailed":{"httpStatusCode":503}}`, "", transient},
		{"a failed connection with a 429", `{"httpConnectionFailed":{"httpStatusCode":429}}`, "", transient},
		{"a failed connection with a null status", `{"httpConnectionFailed":{"httpStatusCode":null}}`, "", transient},
		{"a failed connection with no status", `{"httpConnectionFailed":{}}`, "", transient},
		{"a failed connection with a 401", `{"httpConnectionFailed":{"httpStatusCode":401}}`, "", notRetryable},
		{"a failed connection with a 499", `{"httpConnectionFailed":{"httpStatusCode":499}}`, "", notRetryable},
		{"a failed connection with a 600", `{"httpConnectionFailed":{"httpStatusCode":600}}`, "", notRetryable},
		{"a failed stream connection with a 500", `{"responseStreamConnectionFailed":{"httpStatusCode":500}}`, "", transient},
		{"a failed stream connection with a 404", `{"responseStreamConnectionFailed":{"httpStatusCode":404}}`, "", notRetryable},
		{"too many failed attempts with a 599", `{"responseTooManyFailedAttempts":{"httpStatusCode":599}}`, "", transient},
		{"too many failed attempts with a 400", `{"responseTooManyFailedAttempts":{"httpStatusCode":400}}`, "", notRetryable},
		{"a string form of a kind that carries a status", `"httpConnectionFailed"`, "", transient},
		{"a rate limit", `"rateLimitExceeded"`, "", providerkit.TurnFailureRateLimited},
		{"a usage limit without a reset", `"usageLimitExceeded"`, "You’ve hit your usage limit.", providerkit.TurnFailureUsageLimited},
		// The real "other" texts of R/transcripts-codex-leapmuxdb.md.
		{"a stream that disconnected, as other", `"other"`, "stream disconnected before completion: An error occurred while processing your request. You can retry your request, or contact us through our help center at help.openai.com if the error persists. Please include the request ID abc in your message.", transient},
		{"the disconnect phrase alone", `"other"`, "stream disconnected before completion", transient},
		{"the disconnect phrase with a dash", `"other"`, "stream disconnected before completion - upstream connection closed", transient},
		{"the disconnect phrase with doubled punctuation", `"other"`, "stream disconnected before completion:: retry later", transient},
		{"the disconnect phrase with an alphanumeric suffix", `"other"`, "stream disconnected before completionX", notRetryable},
		{"a model at capacity, as other", `"other"`, "Selected model is at capacity. Please try a different model.", transient},
		{"high demand, as other", `"other"`, "We're currently experiencing high demand, which may cause temporary errors.", transient},
		{"an invalid request, as other", `"other"`, `{"type":"error","status":400,"error":{"type":"invalid_request_error","message":"The 'gpt-5.6-sol' model is not supported when using Codex with a ChatGPT account."}}`, notRetryable},
		{"an abort, as other", `"other"`, "Request was aborted by the user.", notRetryable},
		// A Codex without codexErrorInfo still retries a disconnect.
		{"a disconnect without an error kind", `null`, "stream disconnected before completion: error decoding response body", transient},
		{"a disconnect with an absent error kind", ``, "stream disconnected before completion: error decoding response body", transient},
		{"an indicative vendor verdict, as other", `"other"`, `unexpected status 503: {"error":{"code":"1305","message":"The service may be temporarily overloaded, please try again later"}}`, transient},
		{"a vendor billing block outranks the kind", `"serverOverloaded"`, `{"error":{"message":"You exceeded your current quota, please check your plan and billing details.","type":"insufficient_quota","code":"insufficient_quota"}}`, providerkit.TurnFailureBillingBlocked},
		{"an expired refresh token", `"unauthorized"`, "Your access token could not be refreshed because your refresh token was revoked. Please log out and sign in again.", notRetryable},
		{"a context window that is full", `"contextWindowExceeded"`, "", notRetryable},
		{"a disconnect text of a kind that decides", `"badRequest"`, "stream disconnected before completion", notRetryable},
		{"a kind that this build does not know", `"somethingNew"`, "stream disconnected before completion", notRetryable},
		{"an object of two keys", `{"httpConnectionFailed":{},"other":{}}`, "", notRetryable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			outcome := codexTurnOutcome{status: "failed", message: tc.message, errorInfo: decodeCodexErrorInfo(json.RawMessage(tc.info))}
			assert.Equal(t, tc.want, codexTurnFailure(outcome, codexUsageLimitSources{}, now).Kind)
		})
	}

	t.Run("a turn that did not fail is not retried", func(t *testing.T) {
		t.Parallel()
		for _, status := range []string{"completed", "interrupted", "inProgress", ""} {
			outcome := codexTurnOutcome{status: status, message: "Selected model is at capacity.", errorInfo: codexErrorInfo{kind: "serverOverloaded"}}
			assert.Equal(t, notRetryable, codexTurnFailure(outcome, codexUsageLimitSources{}, now).Kind, status)
		}
	})
	t.Run("a stopped turn is not retried", func(t *testing.T) {
		t.Parallel()
		outcome := codexTurnOutcome{status: "failed", errorInfo: codexErrorInfo{kind: "serverOverloaded"}, stopRequested: true}
		assert.Equal(t, notRetryable, codexTurnFailure(outcome, codexUsageLimitSources{}, now).Kind)
	})
	t.Run("a compaction turn ends on the compaction verdict, whatever its outcome", func(t *testing.T) {
		t.Parallel()
		reset := now.Add(time.Hour)
		for _, outcome := range []codexTurnOutcome{
			{status: "completed"},
			{status: "interrupted"},
			{status: "failed", errorInfo: codexErrorInfo{kind: "internalServerError"}},
			{status: "failed", errorInfo: codexErrorInfo{kind: "rateLimitExceeded"}},
			{status: "failed", errorInfo: codexErrorInfo{kind: "usageLimitExceeded"}, message: "You’ve hit your usage limit."},
			{status: "failed", errorInfo: codexErrorInfo{kind: "serverOverloaded"}, stopRequested: true},
		} {
			outcome.compaction = true
			assert.Equal(t, providerkit.CompactionEnd(), codexTurnFailure(outcome, codexUsageLimitSources{bucketReset: &reset}, now), "%+v", outcome)
		}
	})
}

func TestDecodeCodexErrorInfo(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		raw  string
		want codexErrorInfo
	}{
		{"a string", `"serverOverloaded"`, codexErrorInfo{kind: "serverOverloaded"}},
		{"an object with a status", `{"responseStreamDisconnected":{"httpStatusCode":502}}`, codexErrorInfo{kind: "responseStreamDisconnected", httpStatus: 502}},
		{"an object with a null status", `{"httpConnectionFailed":{"httpStatusCode":null}}`, codexErrorInfo{kind: "httpConnectionFailed"}},
		{"an object without a status", `{"activeTurnNotSteerable":{"turnKind":"review"}}`, codexErrorInfo{kind: "activeTurnNotSteerable"}},
		{"an object whose value is not an object", `{"httpConnectionFailed":"502"}`, codexErrorInfo{}},
		{"an object of two keys", `{"a":{},"b":{}}`, codexErrorInfo{}},
		{"an empty object", `{}`, codexErrorInfo{}},
		{"null", `null`, codexErrorInfo{}},
		{"an absent field", ``, codexErrorInfo{}},
		{"a number", `7`, codexErrorInfo{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, decodeCodexErrorInfo(json.RawMessage(tc.raw)))
		})
	}
}

func TestParseCodexRetryText(t *testing.T) {
	t.Parallel()

	seoul, err := time.LoadLocation("Asia/Seoul")
	require.NoError(t, err)
	now := time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC) // 10:00 in Seoul
	for _, tc := range []struct {
		name    string
		message string
		want    time.Time
		ok      bool
	}{
		{"a dated PM time", "or try again at Nov 2nd, 2026 3:00 PM.", time.Date(2026, 11, 2, 6, 1, 0, 0, time.UTC), true},
		{"a dated AM time", "or try again at Oct 25th, 2026 9:24 AM.", time.Date(2026, 10, 25, 0, 25, 0, 0, time.UTC), true},
		{"the first of a month", "or try again at Dec 1st, 2026 1:05 PM.", time.Date(2026, 12, 1, 4, 6, 0, 0, time.UTC), true},
		{"the third of a month", "or try again at Nov 3rd, 2026 12:00 PM.", time.Date(2026, 11, 3, 3, 1, 0, 0, time.UTC), true},
		{"half past midnight", "or try again at Nov 3rd, 2026 12:30 AM.", time.Date(2026, 11, 2, 15, 31, 0, 0, time.UTC), true},
		{"a same-day time", "or try again at 3:00 PM.", time.Date(2026, 10, 28, 6, 1, 0, 0, time.UTC), true},
		{"a capitalized verb", "Try again at 11:59 PM.", time.Date(2026, 10, 28, 15, 0, 0, 0, time.UTC), true},
		{"a day that the month lacks", "or try again at Feb 30th, 2027 3:00 PM.", time.Time{}, false},
		{"a month that does not exist", "or try again at Foo 2nd, 2026 3:00 PM.", time.Time{}, false},
		{"an hour of 13", "or try again at 13:00 PM.", time.Time{}, false},
		{"a 24-hour time", "or try again at 15:00.", time.Time{}, false},
		{"no time", "You’ve hit your usage limit.", time.Time{}, false},
		{"an empty message", "", time.Time{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := parseCodexRetryText(tc.message, codexRetryTextObservation{location: seoul, startedAt: &now, completedAt: &now}, now)
			assert.Equal(t, tc.ok, ok)
			assert.True(t, tc.want.Equal(got), "got %v, want %v", got, tc.want)
		})
	}

	t.Run("an explicit local zone uses its own calendar", func(t *testing.T) {
		t.Parallel()
		got, ok := parseCodexRetryText("or try again at Nov 2nd, 2026 3:00 PM.", codexRetryTextObservation{location: time.Local}, now)
		require.True(t, ok)
		assert.True(t, time.Date(2026, 11, 2, 15, 1, 0, 0, time.Local).Equal(got))
	})
}

// Current Codex models use Multi-Agent V2 without a spawnAgent collaboration item.
// The parent first receives subAgentActivity, followed by the child's lifecycle and a completed activity pair.
// Replay that order and require these results:
//   - Create one readable child transcript.
//   - Keep child output outside the parent transcript.
//   - Close the child registry row.
func TestHandleCodexOutput_MultiAgentV2LifecycleOwnsTheChildTranscript(t *testing.T) {
	t.Parallel()

	sink := &recordingCodexEnsureSink{Sink: &agenttest.Sink{}}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

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
	assert.Equal(t, bgtask.StatusSucceeded, rows[0].Status)
}

func TestCodexChildRouteReusesTheResolvedSink(t *testing.T) {
	t.Parallel()

	sink := &recordingCodexEnsureSink{Sink: &agenttest.Sink{}}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"root-call","kind":"started","agentThreadId":"main-thread","agentPath":"/root"}}}`)))

	assert.Empty(t, sink.BackgroundTasks(), "the canonical root path is the primary agent, not a subagent")
	assert.Empty(t, agent.collabChildren)
}

func TestHandleCodexOutput_MultiAgentV2FailedTurnClosesWithoutAnActivity(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"call-spawn","kind":"started","agentThreadId":"child-thread","agentPath":"/root/completing_child"}}}`)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"child-thread","turn":{"id":"child-turn"}}}`)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"subagent-completed-child-turn","kind":"completed","agentThreadId":"child-thread","agentPath":"/root/completing_child"}}}`)))

	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, bgtask.StatusSucceeded, rows[0].Status)
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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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
	assert.Equal(t, bgtask.StatusSucceeded, status, "the duplicate completion retries the close")
	assert.Equal(t, 2, sink.closeAttempts)
}

func TestHandleCodexOutput_MultiAgentV2CompletionRetryKeepsTheFailedOutcome(t *testing.T) {
	t.Parallel()

	sink := &transientCodexCloseFailureSink{
		Sink:          &agenttest.Sink{},
		closeFailures: 1,
	}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"call-spawn","kind":"started","agentThreadId":"child-thread","agentPath":"/root/failing_child"}}}`)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"child-thread","turn":{"id":"child-turn"}}}`)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"child-thread","turn":{"id":"child-turn","status":"failed","items":[],"error":{"message":"child failed"}}}}`)))

	_, status, found, err := sink.LookupBackgroundTask("child-thread")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, bgtask.StatusRunning, status, "the failed first close leaves the row open")
	require.Equal(t, 1, sink.closeAttempts)
	child := sink.Child("child-of-call-spawn")
	beforeRetry := child.Messages()
	require.Len(t, beforeRetry, 1)
	require.True(t, beforeRetry[0].TurnEnd)

	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"child-completed","kind":"completed","agentThreadId":"child-thread","agentPath":"/root/failing_child"}}}`)))

	_, status, found, err = sink.LookupBackgroundTask("child-thread")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, bgtask.StatusFailed, status, "the completed activity must preserve the failed child outcome")
	assert.Equal(t, 2, sink.closeAttempts)
	assert.Equal(t, beforeRetry, child.Messages(), "the registry retry must not repeat transcript finalization")
	assert.Equal(t, []bool{true, false}, child.TurnActives())
	assert.Len(t, sink.BackgroundTasks(), 1)
}

func TestHandleCodexOutput_MultiAgentV2LateDuplicateStartDoesNotReviveCompletedRun(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	started := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"spawn-call","kind":"started","agentThreadId":"child-thread","agentPath":"/root/idempotent_child"}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(started)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"child-thread","turn":{"id":"child-turn","status":"completed","items":[],"error":null}}}`)))

	handleCodexOutput(agent, providerkit.ParseLine([]byte(started)))

	_, status, found, err := sink.LookupBackgroundTask("child-thread")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, bgtask.StatusSucceeded, status)
	assert.Empty(t, sink.RevivedTasks(), "a duplicate spawn activity cannot start a second run")
}

func TestHandleCodexOutput_MultiAgentV2ReplaysChildItemsAfterRouteRecovery(t *testing.T) {
	t.Parallel()

	sink := &transientCodexEnsureFailureSink{
		Sink:           &agenttest.Sink{},
		ensureFailures: 2,
	}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	completed := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"completion-call","kind":"completed","agentThreadId":"child-thread","agentPath":"/root/reordered_child"}}}`
	started := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"spawn-call","kind":"started","agentThreadId":"child-thread","agentPath":"/root/reordered_child"}}}`

	handleCodexOutput(agent, providerkit.ParseLine([]byte(completed)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(started)))

	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, bgtask.StatusSucceeded, rows[0].Status,
		"the authoritative start must apply a completion that arrived first")
	assert.Equal(t, "child-of-spawn-call", rows[0].ChildAgentID)
}

func TestHandleCodexOutput_MultiAgentV2StartOwnsIdentityAfterEarlierInteraction(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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
	assert.Equal(t, bgtask.StatusSucceeded, rows[0].Status)
}

func TestHandleCodexOutput_MultiAgentV2NestedChildUsesItsDirectParent(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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

// A spawnAgent call owns no span because child output belongs to its separate transcript.
// Keeping its rail through the run would move concurrent tools one additional column right.
// The row still retains its span ID for frontend pairing and its span type for item/completed.
func TestHandleCodexOutput_SpawnAgentStartedOpensNoSpan(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

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

// A collaboration child can start another run after its row becomes final.
// An upsert deliberately preserves that final state because a replayed snapshot proves no restart.
// Explicit revival is therefore required to show the second run as active in the sidebar and tab chip.
func TestHandleCodexOutput_ARerunCollabChildReopensItsRow(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

	started := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-1","tool":"spawnAgent","status":"inProgress","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":"do work","model":"gpt-5.4","reasoningEffort":"medium","agentsStates":{}}}}`
	completed := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-1","tool":"spawnAgent","status":"completed","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":"do work","model":"gpt-5.4","reasoningEffort":"medium","agentsStates":{"child-1":{"status":"completed","message":"done"}}}}}`
	// The root resumes the ended child through item/started, which registers the receiver again and supplies proof absent from a replayed snapshot.
	// The child's turn/started then reports active work.
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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

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
	assert.Equal(t, bgtask.StatusSucceeded, status, "the row keeps its final status")

	liveTurn := `{"method":"turn/started","params":{"threadId":"child-1","turn":{"id":"turn-c9"}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(liveTurn)))
	assert.Equal(t, []string{"child-1"}, sink.RevivedTasks(), "a live child turn reopens the row")
	_, status, ok, _ = sink.LookupBackgroundTask("child-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, status)
}

// A spawn during another active command draws only that command's rail, with one column rather than two.
func TestHandleCodexOutput_SpawnInsideOpenCommandDrawsOneColumn(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

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

	// Only the spawn releases its span.
	// wait blocks for the child but retains an ordinary tool span until its own completion.
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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

	// Start the spawn span first.
	started := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-1","tool":"spawnAgent","status":"inProgress","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":"do work","model":"gpt-5.4","reasoningEffort":"medium","agentsStates":{}}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(started)))

	completed := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-1","tool":"spawnAgent","status":"completed","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":"do work","model":"gpt-5.4","reasoningEffort":"medium","agentsStates":{"child-1":{"status":"running","message":null}}}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(completed)))

	// The spawn span closes when the spawn call completes.
	// Child output belongs to its separate transcript and does not nest under that span.
	require.Contains(t, sink.ClosedSpans(), "call-1")
}

func TestHandleCodexOutput_SpawnAgentCompletedRegistersLateReceiverThreads(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

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
	// The child transcript opens on its instruction so the reader sees the assigned task before the child's reply.
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, childMessages[0].Source)
	assert.JSONEq(t, `{"content":"do work"}`, string(childMessages[0].Content))
	assert.Equal(t, "cmd-1", childMessages[1].SpanID, "then the child's own command")
}

func TestHandleCodexOutput_WaitCompletedClosesFinalSubagentSpan(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

	started := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-2","tool":"wait","status":"inProgress","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":null,"model":null,"reasoningEffort":null,"agentsStates":{}}}}`
	completed := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-2","tool":"wait","status":"completed","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":null,"model":null,"reasoningEffort":null,"agentsStates":{"child-1":{"status":"completed","message":"done"}}}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(started)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(completed)))

	// wait closes its ordinary tool span at completion, as every collaboration tool does.
	// The preceding code closed only spawnAgent and left these spans open until turn reset:
	//   - wait.
	//   - sendInput.
	//   - resumeAgent.
	//   - closeAgent.
	require.Contains(t, sink.ClosedSpans(), "call-2", "wait span closes at completion")
}

func TestHandleCodexOutput_WaitCompletedDoesNotAffectSpawnSpan(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

	// A wait result closes its own tool span even when the reported child state remains non-final.
	// It must close no spawn span.
	// This fixture opens no spawn span, so ClosedSpans contains only wait.
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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

	started := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-3","tool":"closeAgent","status":"inProgress","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":null,"model":null,"reasoningEffort":null,"agentsStates":{}}}}`
	completed := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-3","tool":"closeAgent","status":"completed","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":null,"model":null,"reasoningEffort":null,"agentsStates":{"child-1":{"status":"shutdown","message":null}}}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(started)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(completed)))

	require.Contains(t, sink.ClosedSpans(), "call-3", "closeAgent span closes at completion")
}

func TestHandleCodexOutput_WaitCompletedClosesOnlyFinalReceivers(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

	started := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-4","tool":"wait","status":"inProgress","senderThreadId":"main-thread","receiverThreadIds":["child-1","child-2","child-3"],"prompt":null,"model":null,"reasoningEffort":null,"agentsStates":{}}}}`
	completed := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-4","tool":"wait","status":"completed","senderThreadId":"main-thread","receiverThreadIds":["child-1","child-2","child-3"],"prompt":null,"model":null,"reasoningEffort":null,"agentsStates":{"child-1":{"status":"completed","message":"done"},"child-2":{"status":"running","message":null},"child-3":{"status":"notFound","message":null}}}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(started)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(completed)))

	// Close the collaboration tool span when that call completes, regardless of receiver state.
	// Its lifecycle belongs to the tool call independently of the children.
	require.Contains(t, sink.ClosedSpans(), "call-4", "wait span closes at completion")
}

func TestHandleCodexOutput_WaitIsFlatNoDrain(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

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
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

	input := `{"method":"item/commandExecution/outputDelta","params":{"itemId":"cmd-1","delta":"hello\n","threadId":"t1","turnId":"turn1"}}`
	handleCodexOutput(a, providerkit.ParseLine([]byte(input)))

	updates := sink.ProgressUpdates()
	require.Len(t, updates, 1)
	require.Equal(t, agentapi.ProgressOutputDelta, updates[0].Operation)
	require.Equal(t, "cmd-1", updates[0].ScopeID)
	require.Equal(t, int64(6), updates[0].Value)
	require.Equal(t, 0, sink.MessageCount())
}

func TestHandleCodexOutput_ReasoningPersistFailureKeepsLiveStream(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{PersistErr: fmt.Errorf("database unavailable")}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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
			agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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
		agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
		agent.threadID = "main-thread"

		handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn-1","item":`+item+`}}`)))

		assert.Equal(t, 1, sink.MessageCount(), item)
	}
}

func TestHandleCodexOutput_FileChangeOutputDelta(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	ag := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

	input := `{"method":"item/fileChange/outputDelta","params":{"itemId":"fc-1","delta":"diff --git a.txt b.txt\n","threadId":"t1","turnId":"turn1"}}`
	handleCodexOutput(ag, providerkit.ParseLine([]byte(input)))

	updates := sink.ProgressUpdates()
	require.Len(t, updates, 1)
	require.Equal(t, agentapi.OutputDeltaProgress("fc-1", 23), updates[0])
	require.Equal(t, 0, sink.MessageCount())
}

// Both image item types use the ordinary tool path.
// The preceding handleItemCompleted default persisted their results without opening or closing the corresponding span.
// The transcript then displayed a rail that remained open through the turn.
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
			agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

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
			// Persist tool_use before opening its span and persist the result while that span remains open.
			// That order produces the correct connector.
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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

	// Ignore a missing id and log the warning.
	input := `{"method":"item/tool/requestUserInput","params":{"questions":[]}}`

	handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

	assert.Equal(t, 0, sink.PublishedControlCount())
}

func TestHandleCodexOutput_TokenUsageUpdatedBroadcastsContextUsageWithoutPersisting(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

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

// A successful hook run has no outcome worth a row. `initialize` opts `hook/started`
// out, so only the completion can arrive empty.
func TestHandleCodexOutput_SuccessfulHookCompletionDoesNotReachTheTranscript(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	agent.threadID = "thread-1"

	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"hook/completed","params":{"threadId":"thread-1","turnId":"turn-1","run":{"id":"hook-1","status":"completed"}}}`)))

	assert.Zero(t, sink.MessageCount())
	assert.Zero(t, sink.NotificationCount())
}

func TestHandleCodexOutput_UnsuccessfulHookCompletionPersists(t *testing.T) {
	t.Parallel()

	for _, status := range []string{"failed", "blocked", "stopped"} {
		sink := &agenttest.Sink{}
		agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
		agent.threadID = "thread-1"
		input := fmt.Sprintf(`{"method":"hook/completed","params":{"threadId":"thread-1","turnId":"turn-1","run":{"id":"hook-1","status":%q,"statusMessage":"hook did not complete"}}}`, status)

		handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

		require.Equal(t, 1, sink.NotificationCount(), status)
		assert.Zero(t, sink.MessageCount(), status)
		assert.JSONEq(t, input, string(sink.LastNotification().Content))
	}
}

// Codex reports cache writes as well as reads.
// The preceding mapping omitted writes, so every Codex turn displayed zero cache-write usage while other providers supplied a value.
func TestHandleCodexOutput_TokenUsageCarriesTheCacheWrite(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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

// Cumulative session usage can exceed the context window and does not measure live occupancy.
// The context gauge must never use that cumulative value.
func TestHandleCodexOutput_TokenUsageIgnoresTheCumulativeTotal(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	agent.threadID = "main-thread"

	input := `{"method":"thread/tokenUsage/updated","params":{"threadId":"child-thread","turnId":"turn-1","tokenUsage":{"total":{"totalTokens":200,"inputTokens":100,"cachedInputTokens":25,"outputTokens":50,"reasoningOutputTokens":9},"last":{"totalTokens":23,"inputTokens":10,"cachedInputTokens":5,"outputTokens":7,"reasoningOutputTokens":1},"modelContextWindow":4096}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

	require.Equal(t, 0, sink.NotificationCount())
	require.Equal(t, 0, sink.SessionInfoCount())
}

func TestHandleCodexOutput_TurnCompletedIgnoresSubagentThreads(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	agent.threadID = "main-thread"

	input := `{"method":"turn/completed","params":{"threadId":"child-thread","turn":{"id":"turn-1","status":"completed","items":[],"error":null}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(input)))

	require.Equal(t, 0, sink.MessageCount())
	require.Equal(t, 0, sink.ResetSpanCount())
	require.Equal(t, 0, sink.SessionInfoCount())
}

// TestHandleCodexOutput_TurnCompletedChildPersistsChildTurnEnd verifies a registered child thread's turn/completed boundary.
// Persist the turn-end divider in its child transcript through the same PersistTurnEnd behavior as the main thread.
// First register the child through spawnAgent item/started so its route can resolve the child agent ID.
func TestHandleCodexOutput_TurnCompletedChildPersistsChildTurnEnd(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	agent.threadID = "main-thread"

	// Register child-1 -> call-1 in the child route (spawnAgent item/started).
	spawnStarted := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn1","item":{"type":"collabAgentToolCall","id":"call-1","tool":"spawnAgent","status":"inProgress","senderThreadId":"main-thread","receiverThreadIds":["child-1"],"prompt":"do work","model":"gpt-5.4","reasoningEffort":"medium","agentsStates":{}}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(spawnStarted)))
	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"child-1","turn":{"id":"turn-1"}}}`)))

	childTurnCompleted := `{"method":"turn/completed","params":{"threadId":"child-1","turn":{"id":"turn-1","status":"completed","items":[],"error":null}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(childTurnCompleted)))

	// The CHILD transcript stores the turn-end divider, not the parent's.
	child := sink.Child("child-of-call-1")
	turnEnds := 0
	for _, m := range child.Messages() {
		if m.TurnEnd {
			turnEnds++
		}
	}
	assert.Equal(t, 1, turnEnds,
		"child turn/completed must persist a turn-end divider into the child transcript")
	// publishTurnActive handles only the main thread.
	// Publish the collaboration child's turn through its own sink, which supplies the child tab's input queue.
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
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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
	assert.Equal(t, agentapi.ProgressSnapshot{}, child.ProgressSnapshot(),
		"the completed child turn must not replay an active token count")
}

func TestFlushCodexGenerationHonorsDiscardOutput(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	a.generationBuffer.Append("message-1", agentapi.AssembledMessageKindText, "restart noise", providerkit.JoinVerbatim)
	a.DiscardOutput()
	a.flushCodexGeneration(agentapi.MessageCompletionInterrupted)

	assert.Empty(t, sink.Messages())
}

func TestCodexWaitMarksAnIntentionalStopAsInterrupted(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	a.SimulateExitForTest()
	a.SetStoppedForTest(true)
	a.generationBuffer.Append("message-1", agentapi.AssembledMessageKindText, "partial answer", providerkit.JoinVerbatim)

	require.NoError(t, a.Wait())
	require.Len(t, sink.Messages(), 1)
	assert.Contains(t, string(sink.Messages()[0].Content), `"completion":"interrupted"`)
}

func TestHandleCodexOutput_InterruptedTurnPersistsIncompleteCommandOutput(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	ag := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	ag.threadID = "main-thread"
	handleCodexOutput(ag, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"commandExecution","id":"command-1","status":"inProgress","command":"printf partial"}}}`)))
	handleCodexOutput(ag, providerkit.ParseLine([]byte(`{"method":"item/commandExecution/outputDelta","params":{"itemId":"command-1","delta":"partial output"}}`)))
	handleCodexOutput(ag, providerkit.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"interrupted","items":[]}}}`)))

	require.GreaterOrEqual(t, sink.MessageCount(), 3)
	result := sink.Messages()[1]
	assert.True(t, result.Closing)
	// The row preserves the exact item/started frame.
	// LeapMux assembles output deltas into recovered provider text and stores that text in the supplement.
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
	assert.Equal(t, agentapi.MessageCompletionInterrupted, result.Completion)

	// Both halves read back as one item, so every extractor sees the output.
	assert.JSONEq(t, `{
		"threadId":"main-thread",
		"turnId":"turn-1",
		"item":{"type":"commandExecution","id":"command-1","status":"inProgress","command":"printf partial","aggregatedOutput":"partial output"}
	}`, string(Registration().Plugin.ResolveProviderData(agentapi.MessageContent{
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
			string(Registration().Plugin.ResolveProviderData(agentapi.MessageContent{
				Original: original, Supplemental: []byte(supplement),
			})), supplement)
	}
}

func TestHandleCodexOutput_InterruptedReasoningPreservesSummaryParts(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	a.threadID = "main-thread"
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","item":{"type":"mcpToolCall","id":"mcp-1","status":"inProgress","server":"docs","tool":"search"}}}`)))
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"turn/completed","params":{"threadId":"main-thread","turn":{"id":"turn-1","status":"completed","items":[]}}}`)))

	require.GreaterOrEqual(t, sink.MessageCount(), 3)
	assert.True(t, sink.Messages()[1].Closing)
	assert.Equal(t, agentapi.MessageCompletionInterrupted, sink.Messages()[1].Completion)
	assert.Contains(t, string(sink.Messages()[2].Metadata), `"num_tool_uses":1`)
}

func TestCodexTurnCounterPreservesOriginalBytes(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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

// UpdatePlan replaces one stored plan across multiple plan turns.
// Keying approval cards by turn formerly left two cards open, while the composer displayed only the oldest.
// Approving that visible card could then execute the newer plan without showing it.
func TestHandleCodexOutput_ASecondPlanPromptRetiresTheFirst(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

	// An eight-character delta estimates two tokens through 8/4.
	// A second delta increases the same phase's cumulative estimate to four tokens through 16/4.
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
			agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	agent.threadID = "main-thread"

	// Codex can report summary text and raw reasoning for the same item and generation.
	// The first summary selects the summary source for r1.
	// Its eight characters estimate two tokens.
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
			agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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
			agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink)) // threadID = "main-thread"

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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"main-thread","turn":{"id":"main-turn"}}}`)))
	require.Equal(t, []bool{true}, sink.TurnActives())

	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"turn/started","params":{"threadId":"child-1","turn":{"id":"child-turn"}}}`)))

	agent.Mu.Lock()
	turnID := agent.mainTurn.id
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
			agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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
			agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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
		{"completed", bgtask.StatusSucceeded, true, false},
		{"errored", bgtask.StatusFailed, true, false},
		{"notFound", bgtask.StatusFailed, true, false},
		{"shutdown", bgtask.StatusStopped, true, false},
		{"interrupted", bgtask.StatusPaused, false, true},
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
		{providerStatus: "completed", wantStatus: bgtask.StatusSucceeded, wantFinished: true},
		{providerStatus: "failed", wantStatus: bgtask.StatusFailed, wantFinished: true},
		{providerStatus: "cancelled", wantStatus: bgtask.StatusPaused, wantActivity: "paused"},
		{providerStatus: "canceled", wantStatus: bgtask.StatusPaused, wantActivity: "paused"},
		{providerStatus: "interrupted", wantStatus: bgtask.StatusPaused, wantActivity: "paused"},
		{providerStatus: "aborted", wantStatus: bgtask.StatusPaused, wantActivity: "paused"},
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

// An interrupted activity pauses the row. A later interaction resumes it.
func TestCodexSubAgentActivity_InterruptedPausesTheRun(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	started := json.RawMessage(`{"type":"subAgentActivity","id":"spawn-1","agentThreadId":"thr-1","agentPath":"/root/reviewer","kind":"started"}`)
	assert.True(t, a.handleCodexSubAgentActivity(started, "main-thread"))
	item := json.RawMessage(`{"type":"subAgentActivity","id":"interrupt-1","agentThreadId":"thr-1","agentPath":"/root/reviewer","kind":"interrupted"}`)
	assert.True(t, a.handleCodexSubAgentActivity(item, "main-thread"), "consumed the activity item")
	rows := sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, bgtask.StatusPaused, rows[0].Status)
	assert.False(t, rows[0].Status.IsFinished(), "the interrupted run stays open")
	assert.False(t, rows[0].Status.IsWorking(), "the interrupted run is idle")

	interacted := json.RawMessage(`{"type":"subAgentActivity","id":"interact-1","agentThreadId":"thr-1","agentPath":"/root/reviewer","kind":"interacted"}`)
	assert.True(t, a.handleCodexSubAgentActivity(interacted, "main-thread"))
	rows = sink.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, bgtask.StatusRunning, rows[0].Status)
	assert.True(t, rows[0].Status.IsWorking())
}

// subAgentActivity is the THIRD writer that reports a collab child active again,
// and it had no coverage for the reopen at all. The upsert absorbs a non-final
// status against a final row, so without the reopen an `interacted` lands its
// "received input" activity on a row that still reads "completed" -- the sidebar
// then shows a finished subagent that just took a message.
func TestHandleCodexOutput_ASubAgentActivityReopensAFinishedChild(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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
	resolved := Registration().Plugin.ResolveProviderData(agentapi.MessageContent{
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
			string(Registration().Plugin.ResolveProviderData(agentapi.MessageContent{
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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
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
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	// A closed pipe: the turn/interrupt request cannot be written, so Interrupt fails
	// exactly as it does when the app-server refuses it or the request times out.
	a.SetContextForTest(context.Background())
	a.SetStdinForTest(agenttest.NopStdin(refusingWriter{}))
	a.Mu.Lock()
	a.threadID, a.mainTurn.id = "thread", "turn"
	a.Mu.Unlock()

	a.HandleOutput([]byte(`{"jsonrpc":"2.0","id":7,"method":"item/commandExecution/requestApproval","params":{}}`))
	request := sink.LastPublishedControl()
	require.NotEmpty(t, request.RequestID, "the approval card is published")

	require.Error(t, a.Interrupt(agentapi.StopContext{}), "the interrupt cannot be written")

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
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	a.SetContextForTest(context.Background())
	a.SetStdinForTest(agenttest.NopStdin(refusingWriter{}))
	a.Mu.Lock()
	a.threadID, a.mainTurn.id = "thread", "turn"
	a.Mu.Unlock()

	a.HandleOutput([]byte(`{"jsonrpc":"2.0","id":7,"method":"` + contracts.MCPElicitationMethodCodex + `","params":{}}`))
	request := sink.LastPublishedControl()
	require.NotEmpty(t, request.RequestID)

	require.Error(t, a.Interrupt(agentapi.StopContext{}))

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
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

	start := `{"method":"item/started","params":{"item":{"id":"cmd-1","type":"commandExecution","command":"ls","status":"inProgress"},"threadId":"main-thread","turnId":"turn1"}}`
	handleCodexOutput(a, providerkit.ParseLine([]byte(start)))
	for _, delta := range []string{"first\n", "second\n"} {
		line := fmt.Sprintf(`{"method":"item/commandExecution/outputDelta","params":{"itemId":"cmd-1","delta":%q,"threadId":"main-thread","turnId":"turn1"}}`, delta)
		handleCodexOutput(a, providerkit.ParseLine([]byte(line)))
	}

	var tails []agentapi.ProgressUpdate
	for _, update := range sink.ProgressUpdates() {
		if update.Operation == agentapi.ProgressOutputTail {
			tails = append(tails, update)
		}
	}
	require.Len(t, tails, 2)
	assert.Equal(t, agentapi.OutputTailProgress("cmd-1", "first\n", false), tails[0])
	assert.Equal(t, agentapi.OutputTailProgress("cmd-1", "first\nsecond\n", false), tails[1])
}

// Codex moves a thread's own settings for reasons LeapMux never asked for: a
// `/model` typed into the composer, a preset's collaboration mode, an effort the
// model clamps. The picker used to go on showing what LeapMux last requested.
func TestHandleCodexOutput_ThreadSettingsUpdatedRefreshesTheSettings(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	agent.model, agent.effort, agent.collaborationMode = "gpt-5", "medium", contracts.CodexOptionDefaultCollaborationMode

	line := `{"method":"thread/settings/updated","params":{"threadId":"main-thread","threadSettings":{"model":"gpt-5.4","effort":"high","collaborationMode":{"mode":"plan","settings":{"model":"gpt-5.4","reasoning_effort":"high","developer_instructions":null}}}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(line)))

	require.Equal(t, 1, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	assert.Equal(t, "gpt-5.4", refresh.Model)
	assert.Equal(t, "high", refresh.Effort)
	assert.Equal(t, "plan", refresh.Options[contracts.CodexOptionCollaborationMode])
	// The frame is a settings fact, never a transcript row.
	assert.Equal(t, 0, sink.MessageCount())
}

// An axis the frame omits keeps the value the reader chose. Writing "" for it
// would clear a setting the app server said nothing about.
func TestHandleCodexOutput_ThreadSettingsUpdatedKeepsAnAxisItOmits(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	agent.model, agent.effort, agent.collaborationMode = "gpt-5", "medium", "plan"

	line := `{"method":"thread/settings/updated","params":{"threadId":"main-thread","threadSettings":{"model":"gpt-5.4"}}}`
	handleCodexOutput(agent, providerkit.ParseLine([]byte(line)))

	refresh := sink.LastSettingsRefresh()
	assert.Equal(t, "gpt-5.4", refresh.Model)
	assert.Equal(t, "medium", refresh.Effort)
	assert.Equal(t, "plan", refresh.Options[contracts.CodexOptionCollaborationMode])
}

// The live tail keeps advancing after the retention cap stops recording the output.
//
// The cap restricts what the FINISHED row keeps. The tail is what a reader watches
// while the call runs, and the byte counter beside it keeps climbing on every delta,
// so a tail that froze at the cap showed text from megabytes ago under a number that
// still moved.
func TestAppendCodexToolEventAdvancesTheTailPastTheRetentionCap(t *testing.T) {
	t.Parallel()

	a := newCodexAgentWithSink(agentapi.NewProviderServices(&agenttest.Sink{}))
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
// the start of the rune that contains the cut. A partial rune renders as a replacement
// character in the row that Codex's own output writes.
func TestAppendCodexToolEventCutsTheCapAtARuneBoundary(t *testing.T) {
	t.Parallel()

	a := newCodexAgentWithSink(agentapi.NewProviderServices(&agenttest.Sink{}))
	a.Mu.Lock()
	// Four bytes of budget remain, and each Hangul syllable is three bytes, so the
	// cap falls inside the second one.
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

	a := newCodexAgentWithSink(agentapi.NewProviderServices(&agenttest.Sink{}))
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

func TestCodexThreadSettingsReadsCapturedCollaborationObject(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	a.model, a.effort, a.collaborationMode = "old", "low", "default"
	a.permissionMode, a.permissionProfile = contracts.CodexPermissionModeApproveForMe, contracts.CodexPermissionProfileWorkspace
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"thread/settings/updated","params":{"threadId":"main-thread","threadSettings":{"disabledPluginIds":[],"cwd":"/Users/trustin/Workspaces/leapmux/.tmp/codex159-raw-exec-probe/workspace","approvalPolicy":"never","approvalsReviewer":"user","sandboxPolicy":{"type":"dangerFullAccess"},"activePermissionProfile":{"id":":danger-full-access","extends":null},"model":"gpt-5.6-luna","modelProvider":"private-probe","serviceTier":null,"effort":"medium","summary":null,"collaborationMode":{"mode":"plan","settings":{"model":"gpt-5.6-luna","reasoning_effort":"medium","developer_instructions":null}},"multiAgentMode":"explicitRequestOnly","personality":null}},"emittedAtMs":1790872800652}`)))
	require.Equal(t, 1, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	assert.Equal(t, "gpt-5.6-luna", refresh.Model)
	assert.Equal(t, "medium", refresh.Effort)
	assert.Equal(t, "plan", refresh.Options[contracts.CodexOptionCollaborationMode])
	assert.Empty(t, sink.Messages())
	// The echo of a turn/start that carried an older snapshot can race a settings
	// change, so the frame never moves the permission axes. LeapMux sends them on
	// every request.
	assert.Equal(t, contracts.CodexPermissionModeApproveForMe, a.permissionMode)
	assert.Equal(t, contracts.CodexPermissionProfileWorkspace, a.permissionProfile)
}

func TestCodexThreadSettingsIgnoresForeignThread(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	a.model = "main-model"
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"thread/settings/updated","params":{"threadId":"foreign-thread","threadSettings":{"model":"foreign-model"}}}`)))
	assert.Equal(t, "main-model", a.model)
	assert.Zero(t, sink.SettingsRefreshCount())
}

func TestCodexThreadSettingsKeepsAxesWhenNativeObjectIsPartial(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	a.model, a.effort, a.collaborationMode = "model", "high", "plan"
	handleCodexOutput(a, providerkit.ParseLine([]byte(`{"method":"thread/settings/updated","params":{"threadId":"main-thread","threadSettings":{"collaborationMode":{"mode":"default","settings":{"model":"ignored-inner-model"}}}}}`)))
	require.Equal(t, 1, sink.SettingsRefreshCount())
	assert.Equal(t, "model", a.model)
	assert.Equal(t, "high", a.effort)
	assert.Equal(t, "default", a.collaborationMode)
}

func TestCodexThreadSettingsRejectsMalformedNativeMode(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{`"obsolete-string"`, `{"mode":4}`, `[]`} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
			a.model, a.effort, a.collaborationMode = "model", "high", "plan"
			raw := []byte(`{"method":"thread/settings/updated","params":{"threadId":"main-thread","threadSettings":{"model":"new-model","collaborationMode":` + mode + `}}}`)
			handleCodexOutput(a, providerkit.ParseLine(raw))
			assert.Equal(t, "model", a.model)
			assert.Equal(t, "plan", a.collaborationMode)
			assert.Zero(t, sink.SettingsRefreshCount())
		})
	}
}

// The Worker stores a native file item as Codex sent it. The item's own status
// is its completion, so neither row states a Worker completion, and the stored
// completion reads UNSPECIFIED. The E2E proof of an applied file change
// (frontend/tests/e2e/codex/appliedFileChange.ts) reads exactly this shape.
func TestHandleCodexOutput_FileChangeRowsStoreNoWorkerCompletion(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

	started := `{"method":"item/started","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"fileChange","id":"patch-1","status":"inProgress","changes":[]}}}`
	completed := `{"method":"item/completed","params":{"threadId":"main-thread","turnId":"turn-1","item":{"type":"fileChange","id":"patch-1","status":"completed","changes":[{"path":"/work/a.txt","kind":{"type":"add"},"diff":"a\n"}]}}}`
	handleCodexOutput(a, providerkit.ParseLine([]byte(started)))
	handleCodexOutput(a, providerkit.ParseLine([]byte(completed)))

	var rows []agenttest.Message
	for _, message := range sink.Messages() {
		if message.SpanID == "patch-1" {
			rows = append(rows, message)
		}
	}
	require.Len(t, rows, 2, "one started row and one completed row")
	assert.False(t, rows[0].Closing)
	assert.True(t, rows[1].Closing)
	for _, row := range rows {
		assert.Equal(t, contracts.CodexItemTypeFileChange, row.SpanType)
		assert.Empty(t, row.Completion, "the native item states its own completion")
		_, stored := agentapi.MessageMetadata(agentapi.MessageContent{Original: row.Content, Completion: row.Completion})
		assert.Equal(t, leapmuxv1.MessageCompletion_MESSAGE_COMPLETION_UNSPECIFIED, stored)
	}
}

const (
	codexReviewStartedLine   = `{"method":"item/autoApprovalReview/started","params":{"threadId":"main-thread","turnId":"turn-1","reviewId":"review-1","targetItemId":"item-1","startedAtMs":1000,"review":{"status":"inProgress","riskLevel":null,"userAuthorization":null,"rationale":null},"action":{"type":"command","source":"shell","command":"make clean","cwd":"/work"}}}`
	codexReviewCompletedLine = `{"method":"item/autoApprovalReview/completed","params":{"threadId":"main-thread","turnId":"turn-1","reviewId":"review-1","targetItemId":"item-1","startedAtMs":1000,"completedAtMs":2000,"decisionSource":"agent","review":{"status":"denied","riskLevel":"high","userAuthorization":"low","rationale":"Untrusted host."},"action":{"type":"command","source":"shell","command":"make clean","cwd":"/work"}}}`
	codexPerReviewWarning    = `{"method":"guardianWarning","params":{"threadId":"main-thread","message":"Automatic approval review denied (risk: high, authorization: low): Untrusted host."}}`
	codexDenialLimitWarning  = `{"method":"guardianWarning","params":{"threadId":"main-thread","message":"Automatic approval review rejected too many approval requests for this turn (3 consecutive, 3 in the last 50 reviews); interrupting the turn."}}`
	codexStrictReviewNotice  = `{"method":"autoApprovalReview/strictReviewRequired","params":{"threadId":"main-thread","turnId":"turn-1","startedAtMs":1000}}`
)

// TestHandleCodexOutput_AutoReviewFramesKeptInTheTranscript pins which auto-review
// frames the transcript keeps: the completed review, the denial-limit warning and the
// strict-review notice. The started frame and the warning that repeats each finished
// review add nothing that the completed review does not state.
func TestHandleCodexOutput_AutoReviewFramesKeptInTheTranscript(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		line string
		kept bool
	}{
		{name: "the started review", line: codexReviewStartedLine, kept: false},
		{name: "the completed review", line: codexReviewCompletedLine, kept: true},
		{name: "the warning that repeats one review", line: codexPerReviewWarning, kept: false},
		{name: "the denial-limit warning", line: codexDenialLimitWarning, kept: true},
		{name: "the strict-review notice", line: codexStrictReviewNotice, kept: true},
		{name: "a warning with no message", line: `{"method":"guardianWarning","params":{"threadId":"main-thread"}}`, kept: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))

			handleCodexOutput(agent, providerkit.ParseLine([]byte(tc.line)))

			require.Zero(t, sink.MessageCount())
			if !tc.kept {
				assert.Zero(t, sink.NotificationCount())
				return
			}
			require.Equal(t, 1, sink.NotificationCount())
			assert.JSONEq(t, tc.line, string(sink.LastNotification().Content))
		})
	}
}

func TestHandleCodexOutput_SubagentAutoReviewRoutesToTheChildTranscript(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	agent := newCodexAgentWithSink(agentapi.NewProviderServices(sink))
	review := strings.ReplaceAll(codexReviewCompletedLine, `"threadId":"main-thread"`, `"threadId":"child-1"`)

	// The route is not ready yet, so the review waits for it.
	handleCodexOutput(agent, providerkit.ParseLine([]byte(review)))
	assert.Zero(t, sink.NotificationCount())

	handleCodexOutput(agent, providerkit.ParseLine([]byte(`{"method":"item/started","params":{"threadId":"main-thread","turnId":"root-turn","item":{"type":"subAgentActivity","id":"spawn-1","kind":"started","agentThreadId":"child-1","agentPath":"/root/reviewer"}}}`)))

	assert.Zero(t, sink.NotificationCount(), "the parent transcript must not receive the child's review")
	child := sink.Child("child-of-spawn-1")
	require.Equal(t, 1, child.NotificationCount())
	assert.JSONEq(t, review, string(child.LastNotification().Content))
}

func TestCodexRetryTextResolvesRepeatedAndMissingLocalMinutes(t *testing.T) {
	t.Parallel()
	newYork, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	lordHowe, err := time.LoadLocation("Australia/Lord_Howe")
	require.NoError(t, err)
	for _, tc := range []struct {
		label    string
		location *time.Location
		now      time.Time
		text     string
		want     time.Time
		ok       bool
	}{
		{"before a repeated hour waits for its later occurrence", newYork, time.Date(2026, 11, 1, 5, 15, 0, 0, time.UTC), "Try again at 1:30 AM.", time.Date(2026, 11, 1, 6, 31, 0, 0, time.UTC), true},
		{"inside the second repeated hour", newYork, time.Date(2026, 11, 1, 6, 15, 0, 0, time.UTC), "Try again at 1:30 AM.", time.Date(2026, 11, 1, 6, 31, 0, 0, time.UTC), true},
		{"after both occurrences remains a past reset", newYork, time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC), "Try again at 1:30 AM.", time.Date(2026, 11, 1, 6, 31, 0, 0, time.UTC), true},
		{"an explicit date also resolves both occurrences", newYork, time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC), "Try again at Nov 1st, 2026 1:30 AM.", time.Date(2026, 11, 1, 6, 31, 0, 0, time.UTC), true},
		{"a thirty minute change", lordHowe, time.Date(2026, 4, 4, 14, 30, 0, 0, time.UTC), "Try again at 1:45 AM.", time.Date(2026, 4, 4, 15, 16, 0, 0, time.UTC), true},
		{"a missing local minute", newYork, time.Date(2026, 3, 8, 6, 0, 0, 0, time.UTC), "Try again at 2:30 AM.", time.Time{}, false},
	} {
		t.Run(tc.label, func(t *testing.T) {
			t.Parallel()
			got, ok := parseCodexRetryText(tc.text, codexRetryTextObservation{location: tc.location, startedAt: &tc.now, completedAt: &tc.now}, tc.now)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestCodexUsageLimitTextUsesTheNativeCalendarDate(t *testing.T) {
	t.Parallel()
	observed := time.Date(2026, 10, 29, 0, 0, 1, 0, time.UTC)
	previousStart := observed.Add(-31 * time.Second).Unix()
	previousEnd := observed.Add(-2 * time.Second).Unix()
	currentStart, currentEnd := observed.Add(-time.Second).Unix(), observed.Unix()
	for _, tc := range []struct {
		label              string
		now                time.Time
		started, completed any
		includeTimes       bool
		text               string
		wantReset          time.Time
	}{
		{label: "delayed handling after midnight keeps the previous native date", started: previousStart, completed: previousEnd, includeTimes: true},
		{label: "a new native day can still state a late reset", started: currentStart, completed: currentEnd, includeTimes: true, wantReset: time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)},
		{label: "a native interval spanning midnight proves no date", started: previousStart, completed: currentEnd, includeTimes: true},
		{label: "missing native times prove no date"},
		{label: "null native times prove no date", includeTimes: true},
		{label: "completion alone proves no date", completed: currentEnd, includeTimes: true},
		{label: "an unreadable start preserves the usage verdict", started: "not a timestamp", completed: currentEnd, includeTimes: true},
		{label: "a fractional start preserves the usage verdict", started: 1.5, completed: currentEnd, includeTimes: true},
		{label: "a reversed interval proves no date", started: currentEnd, completed: currentStart, includeTimes: true},
		{label: "future native timestamps prove no date", started: currentEnd + 1, completed: currentEnd + 2, includeTimes: true},
		{label: "an explicit date needs no inferred day", started: previousStart, completed: currentEnd, includeTimes: true, text: "Try again at Oct 30th, 2026 3:00 PM.", wantReset: time.Date(2026, 10, 30, 15, 1, 0, 0, time.UTC)},
		{label: "zero is a valid native Unix timestamp", now: time.Unix(1, 0).UTC(), started: int64(0), completed: int64(0), includeTimes: true, text: "Try again at 3:00 PM.", wantReset: time.Date(1970, 1, 1, 15, 1, 0, 0, time.UTC)},
		{label: "negative native Unix timestamps remain valid", now: time.Unix(-1800, 0).UTC(), started: int64(-3600), completed: int64(-1800), includeTimes: true, text: "Try again at 11:45 PM.", wantReset: time.Date(1969, 12, 31, 23, 46, 0, 0, time.UTC)},
	} {
		t.Run(tc.label, func(t *testing.T) {
			t.Parallel()
			now := tc.now
			if now.IsZero() {
				now = observed
			}
			text := tc.text
			if text == "" {
				text = "Try again at 11:59 PM."
			}
			sink := &agenttest.Sink{}
			a := newCodexAgentAt(t, agentapi.NewProviderServices(sink), now)
			a.retryTextLocation = time.UTC
			turn := map[string]any{"id": "turn-1", "status": "failed", "items": []any{}, "error": map[string]any{"message": text, "codexErrorInfo": "usageLimitExceeded"}}
			if tc.includeTimes {
				turn["startedAt"], turn["completedAt"] = tc.started, tc.completed
			}
			frame, err := json.Marshal(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "main-thread", "turn": turn}})
			require.NoError(t, err)
			handleCodexOutput(a, providerkit.ParseLine(frame))
			want := []agenttest.TimelineEntry{codexTurnEndRow, codexServerErrorCancelledForNoReset}
			if !tc.wantReset.IsZero() {
				want = []agenttest.TimelineEntry{codexTurnEndRow, codexUsageLimitScheduledAt(tc.wantReset), codexServerErrorCancelledForAStatedReset}
			}
			assert.Equal(t, want, sink.Timeline())
			require.Len(t, sink.Messages(), 1)
			assert.True(t, sink.Messages()[0].TurnEnd)
		})
	}
}
