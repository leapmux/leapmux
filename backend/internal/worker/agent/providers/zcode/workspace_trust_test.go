package zcode

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// zcodeTestBundleDigest is the bundle digest of the test's hook admission.
const zcodeTestBundleDigest = "419f0da95a3104f0fc406b13cbdf9f1a3fd29ec91c96b49c6a0ce171e6af260f"

// zcodeTestTopic is the conversation topic of the test session.
const zcodeTestTopic = zcodeConversationTopicPrefix + "sess-1"

// zcodeAdmissionLine is the hook admission of the test session.
func zcodeAdmissionLine(t *testing.T, seq int64, pending int) []byte {
	t.Helper()
	return zcodeEventLine(t, seq, contracts.ZCodeEventSessionUpdated, fmt.Sprintf(
		`{"pendingCount":%d,"bundleDigest":%q,"workspaceIdentity":"/tmp/zcode-workspace"}`, pending, zcodeTestBundleDigest))
}

// zcodeTestReviewItem is one item of a test review.
type zcodeTestReviewItem struct{ id, trustState string }

// zcodeTestReview is the pending interaction of a hook review, in the shape that
// ZCode 3.16.3 sends.
func zcodeTestReview(bundleDigest string, items ...zcodeTestReviewItem) map[string]any {
	reviewItems := make([]map[string]any, 0, len(items))
	for _, item := range items {
		reviewItems = append(reviewItems, map[string]any{
			"reviewItemId": item.id, "event": "UserPromptSubmit", "type": "command", "displayName": "UserPromptSubmit",
			"displayCommand": "echo ran", "sourcePath": ".zcode/config.json", "resolvedTimeoutMs": 60000,
			"resolvedMaxOutputBytes": 32768, "executionMode": "foreground", "configuredEnabled": true, "editable": true,
			"trustState": item.trustState,
		})
	}
	return map[string]any{
		"interactionId": "workspace-hook-interaction:1",
		"kind":          zcodeHookReviewKind,
		"anchorRowId":   nil,
		"createdAt":     1,
		"payload": map[string]any{
			"kind": zcodeHookReviewKind, "reviewFlowId": "workspace-hook-review:1", "generation": 1,
			"interactionId": "workspace-hook-interaction:1", "sessionId": "sess-1", "taskId": "sess-1",
			"runId": "workspace-hook-run:sess-1:1", "workspaceIdentity": "/tmp/zcode-workspace",
			"workspaceLabel": "zcode-workspace", "bundleDigest": bundleDigest, "createdAt": 1, "deadlineAt": 600001,
			"sourceFiles": []map[string]any{{"path": "/tmp/zcode-workspace/.zcode/config.json", "displayPath": ".zcode/config.json", "editable": true}},
			"summary":     map[string]any{"eventCount": 1, "hookCount": len(items), "pendingCount": len(items)},
			"items":       reviewItems,
			"warningCode": "workspace_hooks_execute_code",
		},
	}
}

// zcodeSnapshotFrame is a logical snapshot frame whose pending interactions are
// the list.
func zcodeSnapshotFrame(pending ...map[string]any) map[string]any {
	return map[string]any{
		"topic": zcodeTestTopic, "subscriptionId": "sub-1", "sentAt": 1, "fromSeq": 0, "toSeq": 1,
		"payload": map[string]any{"kind": "snapshot", "snapshot": map[string]any{
			"sessionId": "sess-1", "pendingInteractions": append([]map[string]any{}, pending...),
			"workspaceHookAdmission": map[string]any{"pendingCount": 1, "bundleDigest": zcodeTestBundleDigest},
		}},
	}
}

// zcodeDeltaFrame is a logical delta frame that patches the pending interactions.
func zcodeDeltaFrame(pending ...map[string]any) map[string]any {
	return map[string]any{
		"topic": zcodeTestTopic, "subscriptionId": "sub-1", "sentAt": 2, "fromSeq": 1, "toSeq": 2,
		"payload": map[string]any{"kind": "deltas", "deltas": []map[string]any{
			{"op": "row.appended", "row": map[string]any{"rowId": 1, "kind": "userInput"}},
			{"op": "state.updated", "patch": map[string]any{"pendingInteractions": append([]map[string]any{}, pending...)}},
		}},
	}
}

// zcodeCompleteWireLine is a frame notification that carries a whole logical frame.
func zcodeCompleteWireLine(t *testing.T, topic string, frame map[string]any) []byte {
	t.Helper()
	line, err := json.Marshal(map[string]any{"method": NotifyConversationFrame, "params": map[string]any{
		"wireVersion": 3, "kind": zcodeWireComplete, "deliveryKind": "online", "logicalFrameId": "lf-complete",
		"logicalFrameOrdinal": 1, "topic": topic, "subscriptionId": "sub-1", "frame": frame,
	}})
	require.NoError(t, err)
	return line
}

// zcodeFragmentWireLines splits a logical frame into count fragments, as ZCode
// splits a frame that exceeds its frame size.
func zcodeFragmentWireLines(t *testing.T, logicalFrameID string, frame map[string]any, count int) [][]byte {
	t.Helper()
	data, err := json.Marshal(frame)
	require.NoError(t, err)
	checksum := fmt.Sprintf("%08x", crc32.ChecksumIEEE(data))
	size := (len(data) + count - 1) / count
	lines := make([][]byte, 0, count)
	for index := range count {
		start, end := index*size, min((index+1)*size, len(data))
		line, err := json.Marshal(map[string]any{"method": NotifyConversationFrame, "params": map[string]any{
			"wireVersion": 3, "kind": zcodeWireFragment, "deliveryKind": "online", "logicalFrameId": logicalFrameID,
			"logicalFrameOrdinal": 1, "topic": zcodeTestTopic, "subscriptionId": "sub-1",
			"fragmentIndex": index, "fragmentCount": count, "logicalBytes": len(data),
			"checksum":   map[string]any{"algorithm": zcodeFrameChecksumAlgorithm, "value": checksum},
			"dataBase64": base64.StdEncoding.EncodeToString(data[start:end]),
		}})
		require.NoError(t, err)
		lines = append(lines, line)
	}
	return lines
}

// waitZCodeRequestNumber waits until stdin holds the request number n (from 0)
// for method, and returns it.
func waitZCodeRequestNumber(t *testing.T, stdin *zcodeRecordedStdin, method string, n int) zcodeSentRequest {
	t.Helper()
	var found zcodeSentRequest
	require.Eventually(t, func() bool {
		seen := 0
		for _, request := range stdin.Requests(t) {
			if request.Method != method {
				continue
			}
			if seen == n {
				found = request
				return true
			}
			seen++
		}
		return false
	}, zcodeTestRPCTimeout, 2*time.Millisecond, "no request %d for %s", n, method)
	return found
}

// zcodeCommandOf decodes the envelope of a v4 command request.
func zcodeCommandOf(t *testing.T, request zcodeSentRequest) (zcodeCommandEnvelope, map[string]any) {
	t.Helper()
	var envelope zcodeCommandEnvelope
	require.NoError(t, json.Unmarshal(request.Params, &envelope))
	var payload struct {
		Payload map[string]any `json:"payload"`
	}
	require.NoError(t, json.Unmarshal(request.Params, &payload))
	return envelope, payload.Payload
}

// ackZCodeHookCommand acknowledges a v4 command with the status.
func ackZCodeHookCommand(t *testing.T, a *Agent, request zcodeSentRequest, status string) {
	t.Helper()
	envelope, _ := zcodeCommandOf(t, request)
	ack, err := json.Marshal(map[string]any{"commandId": envelope.CommandID, "status": status, "revisionAtDecision": 9})
	require.NoError(t, err)
	a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, request), ack))
}

// zcodeHookReviewScript states what the scripted app-server sends during a trust.
type zcodeHookReviewScript struct {
	// snapshot holds the frames that follow the subscription's reply.
	snapshot [][]byte
	// requestStatus acknowledges requestWorkspaceHookReview.
	requestStatus string
	// review holds the frames that follow the acknowledgement of the request.
	review [][]byte
	// respondStatus acknowledges respondWorkspaceHookReview; empty when no
	// answer is expected.
	respondStatus string
}

// serveZCodeHookReview plays the app-server's side of one trust. It returns the
// requests that the trust sent, once the trust unsubscribed.
func serveZCodeHookReview(t *testing.T, a *Agent, stdin *zcodeRecordedStdin, script zcodeHookReviewScript) <-chan []zcodeSentRequest {
	t.Helper()
	done := make(chan []zcodeSentRequest, 1)
	go func() {
		subscribe := waitZCodeRequestNumber(t, stdin, MethodConversationSubscribe, 0)
		a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, subscribe),
			json.RawMessage(`{"ack":{"subscriptionId":"sub-1","mode":"snapshot","logEpoch":"epoch-1"}}`)))
		for _, line := range script.snapshot {
			a.HandleOutput(line)
		}
		request := waitZCodeRequestNumber(t, stdin, MethodCommand, 0)
		ackZCodeHookCommand(t, a, request, script.requestStatus)
		for _, line := range script.review {
			a.HandleOutput(line)
		}
		if script.respondStatus != "" {
			ackZCodeHookCommand(t, a, waitZCodeRequestNumber(t, stdin, MethodCommand, 1), script.respondStatus)
		}
		unsubscribe := waitZCodeRequestNumber(t, stdin, MethodConversationUnsubscribe, 0)
		a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, unsubscribe), json.RawMessage(`{}`)))
		done <- stdin.Requests(t)
	}()
	return done
}

// newZCodeTrustAgent is a test agent whose session states pending hooks, and
// whose trust question is open.
func newZCodeTrustAgent(t *testing.T, pending int) (*Agent, *agenttest.ControlSink, *zcodeRecordedStdin) {
	t.Helper()
	sink := &agenttest.ControlSink{}
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	a.HandleOutput(zcodeAdmissionLine(t, 1, pending))
	require.Equal(t, 1, sink.PublishedControlCount())
	return a, sink, stdin
}

// zcodeTrustAnswer is the answer that the worker forwards for the question.
func zcodeTrustAnswer(t *testing.T, decision string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"type": "control_response", "response": map[string]any{
		"subtype": "success", "request_id": providerkit.WorkspaceTrustRequestID("sess-1"),
		"response": map[string]string{contracts.WorkspaceTrustFieldDecision: decision},
	}})
	require.NoError(t, err)
	return raw
}

func zcodeHooksTrusted(t *testing.T, a *Agent) bool {
	t.Helper()
	trusted, err := zcodeWorkspaceTrustHost{a}.WorkspaceTrusted()
	require.NoError(t, err)
	return trusted
}

// --- the question ---

// Hooks that wait for trust raise the shared question once for the session,
// and no notification: the question states them.
func TestZCodeHookAdmissionAsksTheTrustQuestionOnce(t *testing.T) {
	t.Parallel()
	a, sink, _ := newZCodeTrustAgent(t, 2)
	published := sink.LastPublishedControl()
	assert.Equal(t, providerkit.WorkspaceTrustRequestID("sess-1"), published.RequestID)
	assert.Equal(t, "sess-1", published.AgentSessionID)
	assert.JSONEq(t, `{"type":"leapmux_workspace_trust","path":"/tmp/zcode-workspace","configuration":["hooks"]}`, string(published.Payload))
	assert.False(t, zcodeHooksTrusted(t, a))

	a.HandleOutput(zcodeAdmissionLine(t, 2, 3))
	a.Mu.Lock()
	assert.Equal(t, 3, a.hookAdmission.pendingCount, "the newest admission counts")
	a.Mu.Unlock()
	assert.Equal(t, 1, sink.PublishedControlCount(), "one question for the session")
	assert.Zero(t, sink.NotificationCount())
}

// ZCode states the admission when the first turn starts. The question waits for
// the end of that turn, because an interrupt withdraws every control request
// that the turn published, and the hooks run only from the next prompt.
func TestZCodeHookAdmissionDuringATurnAsksAtItsEnd(t *testing.T) {
	t.Parallel()
	for name, end := range map[string]func(t *testing.T) []byte{
		"a completed turn": func(t *testing.T) []byte {
			return zcodeEventLine(t, 3, contracts.ZCodeEventTurnCompleted, `{"resultType":"success"}`)
		},
		"a failed turn": func(t *testing.T) []byte {
			return zcodeEventLine(t, 3, contracts.ZCodeEventTurnFailed, `{"error":{"message":"boom"}}`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.ControlSink{}
			a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
			a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventTurnStarted, `{}`))
			a.HandleOutput(zcodeAdmissionLine(t, 2, 1))
			assert.Zero(t, sink.PublishedControlCount(), "no question during the turn")
			assert.False(t, zcodeHooksTrusted(t, a))

			a.HandleOutput(end(t))
			require.Equal(t, 1, sink.PublishedControlCount())
			assert.Equal(t, providerkit.WorkspaceTrustRequestID("sess-1"), sink.LastPublishedControl().RequestID)
			a.PublishTurnActive()
			assert.Equal(t, 1, sink.PublishedControlCount(), "the deferred question is asked once")
		})
	}
}

// A stopped agent asks nothing at the end of its turn.
func TestZCodeHookTrustOfAStoppedAgentAsksNothing(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventTurnStarted, `{}`))
	a.HandleOutput(zcodeAdmissionLine(t, 2, 1))
	a.SetStoppedForTest(true)
	a.Mu.Lock()
	a.turnActive = false
	a.Mu.Unlock()
	a.PublishTurnActive()
	assert.Zero(t, sink.PublishedControlCount())
}

// An admission with nothing pending, and an admission of another session, ask
// nothing.
func TestZCodeHookAdmissionWithoutPendingHooksAsksNothing(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.HandleOutput(zcodeAdmissionLine(t, 1, 0))
	assert.True(t, zcodeHooksTrusted(t, a))

	a.observeZCodeHookAdmission("another-session", zcodeSessionUpdatedPayload{
		PendingCount: json.RawMessage(`3`), BundleDigest: json.RawMessage(`"d"`),
	})
	a.observeZCodeHookAdmission("sess-1", zcodeSessionUpdatedPayload{PendingCount: json.RawMessage(`"many"`)})
	assert.True(t, zcodeHooksTrusted(t, a))
	assert.Zero(t, sink.PublishedControlCount())
	assert.Zero(t, sink.NotificationCount())
}

// The event dispatcher admits an event while LeapMux knows no session yet. The
// question then belongs to the session that the event states.
func TestZCodeHookAdmissionBeforeTheSessionIsKnownAsks(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.Mu.Lock()
	a.sessionID = ""
	a.Mu.Unlock()
	a.HandleOutput(zcodeAdmissionLine(t, 1, 1))
	require.Equal(t, 1, sink.PublishedControlCount())
	assert.Equal(t, "sess-1", sink.LastPublishedControl().AgentSessionID)
	assert.False(t, zcodeHooksTrusted(t, a))

	a.Mu.Lock()
	a.sessionID = "sess-1"
	a.Mu.Unlock()
	assert.False(t, zcodeHooksTrusted(t, a), "the admission belongs to the session that the event states")
}

// A new session's question comes from its own admission: the old session's
// admission states nothing about it.
func TestZCodeHookAdmissionBelongsToItsSession(t *testing.T) {
	t.Parallel()
	a, _, _ := newZCodeTrustAgent(t, 1)
	a.Mu.Lock()
	a.sessionID = "sess-2"
	a.Mu.Unlock()
	assert.True(t, zcodeHooksTrusted(t, a))
}

// The question goes with its session's controls.
func TestZCodeWithdrawingTheControlsRetiresTheTrustQuestion(t *testing.T) {
	t.Parallel()
	a, sink, _ := newZCodeTrustAgent(t, 1)
	a.withdrawPendingControls()
	assert.Contains(t, sink.CanceledControls(), providerkit.WorkspaceTrustRequestID("sess-1"))
}

// The browser's decision on the question becomes the saved answer, and never a
// ZCode frame.
func TestZCodeResolvesTheTrustAnswer(t *testing.T) {
	t.Parallel()
	payload, err := json.Marshal(map[string]any{
		contracts.WorkspaceTrustFieldType:          contracts.WorkspaceTrustControlTypeQuestion,
		contracts.WorkspaceTrustFieldPath:          "/tmp/zcode-workspace",
		contracts.WorkspaceTrustFieldConfiguration: zcodeTrustedConfiguration,
	})
	require.NoError(t, err)
	requestID := providerkit.WorkspaceTrustRequestID("sess-1")
	for behavior, decision := range map[string]string{
		agent.ControlBehaviorAllow: contracts.WorkspaceTrustDecisionTrust,
		agent.ControlBehaviorDeny:  contracts.WorkspaceTrustDecisionReject,
	} {
		t.Run(behavior, func(t *testing.T) {
			t.Parallel()
			content, err := json.Marshal(map[string]any{"type": "control_response", "response": map[string]any{
				"subtype": "success", "request_id": requestID, "response": map[string]any{"behavior": behavior},
			}})
			require.NoError(t, err)
			result := zcodeProvider{}.ResolveControlResponse(agent.ControlResponseContext{
				RequestID: requestID, RequestPayload: payload, ResponseContent: content,
			})
			require.Empty(t, result.Refusal())
			var answer struct {
				Response struct {
					RequestID string            `json:"request_id"`
					Response  map[string]string `json:"response"`
				} `json:"response"`
			}
			require.NoError(t, json.Unmarshal(result.Content, &answer))
			assert.Equal(t, requestID, answer.Response.RequestID)
			assert.Equal(t, decision, answer.Response.Response[contracts.WorkspaceTrustFieldDecision])
		})
	}
}

// --- Trust ---

// Trust subscribes, asks for the review, trusts the items that wait for trust
// and no other, and unsubscribes. The hooks are then trusted and the question
// is answered.
func TestZCodeTrustAnswersTheHookReview(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newZCodeTrustAgent(t, 2)
	review := zcodeTestReview(zcodeTestBundleDigest,
		zcodeTestReviewItem{"workspace-hook-0-UserPromptSubmit-0-0", "pending_trust"},
		zcodeTestReviewItem{"workspace-hook-0-Stop-0-0", "trusted_persistent"},
		zcodeTestReviewItem{"workspace-hook-0-PreToolUse-0-0", "stale_digest"},
	)
	served := serveZCodeHookReview(t, a, stdin, zcodeHookReviewScript{
		snapshot:      [][]byte{zcodeCompleteWireLine(t, zcodeTestTopic, zcodeSnapshotFrame())},
		requestStatus: zcodeCommandAccepted,
		review:        [][]byte{zcodeCompleteWireLine(t, zcodeTestTopic, zcodeDeltaFrame(review))},
		respondStatus: zcodeCommandAccepted,
	})
	require.NoError(t, a.SendRawInput(zcodeTrustAnswer(t, contracts.WorkspaceTrustDecisionTrust), agent.StopContext{}))
	requests := <-served

	var methods []string
	for _, request := range requests {
		methods = append(methods, request.Method)
	}
	assert.Equal(t, []string{MethodConversationSubscribe, MethodCommand, MethodCommand, MethodConversationUnsubscribe}, methods)
	assert.JSONEq(t, `{"topic":"conversation/sess-1","connectionId":"leapmux","clientMode":"web-remote-replayable","workflowRunDeltas":true}`, string(requests[0].Params))
	request, payload := zcodeCommandOf(t, requests[1])
	assert.Equal(t, zcodeCommandRequestWorkspaceHookReview, request.Type)
	assert.Equal(t, "sess-1", request.SessionID)
	assert.Equal(t, map[string]any{"sessionId": "sess-1", "workspaceIdentity": "/tmp/zcode-workspace", "bundleDigest": zcodeTestBundleDigest}, payload)
	respond, payload := zcodeCommandOf(t, requests[2])
	assert.Equal(t, zcodeCommandRespondWorkspaceHookReview, respond.Type)
	assert.Equal(t, map[string]any{
		"sessionId": "sess-1", "taskId": "sess-1", "runId": "workspace-hook-run:sess-1:1",
		"workspaceIdentity": "/tmp/zcode-workspace", "bundleDigest": zcodeTestBundleDigest,
		"reviewFlowId": "workspace-hook-review:1", "generation": float64(1), "interactionId": "workspace-hook-interaction:1",
		"decision": map[string]any{"action": "trust_selected", "reviewItemIds": []any{
			"workspace-hook-0-UserPromptSubmit-0-0", "workspace-hook-0-PreToolUse-0-0",
		}},
	}, payload, "the answer repeats the review's identity and trusts only the items that wait for trust")
	assert.JSONEq(t, `{"topic":"conversation/sess-1","subscriptionId":"sub-1","connectionId":"leapmux"}`, string(requests[3].Params))

	assert.True(t, zcodeHooksTrusted(t, a))
	assert.Equal(t, []string{providerkit.WorkspaceTrustRequestID("sess-1")}, sink.CanceledControls())
	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Nil(t, a.hookReview, "the trust leaves no wait behind")
}

// A review that is already pending appears only in the subscription's snapshot,
// because ZCode raises no new delta for it.
func TestZCodeTrustReadsAReviewThatIsAlreadyPending(t *testing.T) {
	t.Parallel()
	a, _, stdin := newZCodeTrustAgent(t, 1)
	review := zcodeTestReview(zcodeTestBundleDigest, zcodeTestReviewItem{"workspace-hook-0-UserPromptSubmit-0-0", "pending_trust"})
	served := serveZCodeHookReview(t, a, stdin, zcodeHookReviewScript{
		snapshot:      [][]byte{zcodeCompleteWireLine(t, zcodeTestTopic, zcodeSnapshotFrame(review))},
		requestStatus: zcodeCommandAccepted,
		respondStatus: zcodeCommandAccepted,
	})
	require.NoError(t, a.SendRawInput(zcodeTrustAnswer(t, contracts.WorkspaceTrustDecisionTrust), agent.StopContext{}))
	<-served
	assert.True(t, zcodeHooksTrusted(t, a))
}

// A review that arrives in fragments, in any order, is read once the last
// fragment arrives. A frame of another topic and a review of another bundle are
// not this trust's.
func TestZCodeTrustReadsAFragmentedReview(t *testing.T) {
	t.Parallel()
	a, _, stdin := newZCodeTrustAgent(t, 1)
	review := zcodeTestReview(zcodeTestBundleDigest, zcodeTestReviewItem{"workspace-hook-0-UserPromptSubmit-0-0", "pending_trust"})
	fragments := zcodeFragmentWireLines(t, "lf-review", zcodeDeltaFrame(review), 3)
	served := serveZCodeHookReview(t, a, stdin, zcodeHookReviewScript{
		requestStatus: zcodeCommandAccepted,
		review: [][]byte{
			zcodeCompleteWireLine(t, zcodeConversationTopicPrefix+"another-session", zcodeDeltaFrame(review)),
			zcodeCompleteWireLine(t, zcodeTestTopic, zcodeDeltaFrame(zcodeTestReview("another-bundle",
				zcodeTestReviewItem{"workspace-hook-9-Stop-0-0", "pending_trust"}))),
			fragments[2], fragments[0], fragments[1],
		},
		respondStatus: zcodeCommandAccepted,
	})
	require.NoError(t, a.SendRawInput(zcodeTrustAnswer(t, contracts.WorkspaceTrustDecisionTrust), agent.StopContext{}))
	requests := <-served
	_, payload := zcodeCommandOf(t, requests[2])
	assert.Equal(t, map[string]any{"action": "trust_selected", "reviewItemIds": []any{"workspace-hook-0-UserPromptSubmit-0-0"}}, payload["decision"])
}

// A review whose items all hold trust already needs no answer.
func TestZCodeTrustOfAReviewWithNothingPendingSendsNoAnswer(t *testing.T) {
	t.Parallel()
	a, _, stdin := newZCodeTrustAgent(t, 1)
	review := zcodeTestReview(zcodeTestBundleDigest, zcodeTestReviewItem{"workspace-hook-0-Stop-0-0", "trusted_persistent"})
	served := serveZCodeHookReview(t, a, stdin, zcodeHookReviewScript{
		requestStatus: zcodeCommandAccepted,
		review:        [][]byte{zcodeCompleteWireLine(t, zcodeTestTopic, zcodeDeltaFrame(review))},
	})
	require.NoError(t, a.SendRawInput(zcodeTrustAnswer(t, contracts.WorkspaceTrustDecisionTrust), agent.StopContext{}))
	requests := <-served
	assert.Len(t, requests, 3, "a subscription, the request for the review, and the end of the subscription")
	assert.True(t, zcodeHooksTrusted(t, a))
}

// A failed trust leaves the hooks untrusted, ends its subscription, and keeps
// the question open for another answer.
func TestZCodeTrustFailures(t *testing.T) {
	t.Parallel()
	review := zcodeTestReview(zcodeTestBundleDigest, zcodeTestReviewItem{"workspace-hook-0-UserPromptSubmit-0-0", "pending_trust"})
	for _, tc := range []struct {
		name   string
		script func(t *testing.T) zcodeHookReviewScript
		want   string
	}{
		{"a refused request for the review", func(*testing.T) zcodeHookReviewScript {
			return zcodeHookReviewScript{requestStatus: "rejected"}
		}, "ZCode did not start the hook review: rejected"},
		{"a refused answer", func(t *testing.T) zcodeHookReviewScript {
			return zcodeHookReviewScript{
				requestStatus: zcodeCommandAccepted,
				review:        [][]byte{zcodeCompleteWireLine(t, zcodeTestTopic, zcodeDeltaFrame(review))},
				respondStatus: "stale",
			}
		}, "ZCode did not trust the hooks: stale"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink, stdin := newZCodeTrustAgent(t, 1)
			served := serveZCodeHookReview(t, a, stdin, tc.script(t))
			err := a.SendRawInput(zcodeTrustAnswer(t, contracts.WorkspaceTrustDecisionTrust), agent.StopContext{})
			require.ErrorContains(t, err, tc.want)
			requests := <-served
			assert.Equal(t, MethodConversationUnsubscribe, requests[len(requests)-1].Method)
			assert.False(t, zcodeHooksTrusted(t, a))
			assert.Empty(t, sink.CanceledControls(), "the question stays open")
		})
	}
}

// A subscription that ZCode refuses sends no command.
func TestZCodeTrustWithARefusedSubscriptionSendsNoCommand(t *testing.T) {
	t.Parallel()
	a, _, stdin := newZCodeTrustAgent(t, 1)
	refuseZCodeRequest(t, a, stdin, MethodConversationSubscribe, -32601, "method not found")
	err := a.SendRawInput(zcodeTrustAnswer(t, contracts.WorkspaceTrustDecisionTrust), agent.StopContext{})
	require.ErrorContains(t, err, "subscribe to the ZCode conversation")
	for _, request := range stdin.Requests(t) {
		assert.NotEqual(t, MethodCommand, request.Method)
	}
	assert.False(t, zcodeHooksTrusted(t, a))
}

// A review that never arrives ends the wait at the API timeout, on the process
// clock, and the subscription still ends.
func TestZCodeTrustWithoutAReviewTimesOut(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	trap := clock.Trap().NewTimer(zcodeHookReviewTimerTag)
	defer trap.Close()
	sink := &agenttest.ControlSink{}
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{
		AgentID: "test-agent", Ctx: a.Context(), Clock: clock, Stdin: stdin, ProcessDone: make(chan struct{}),
	})
	a.HandleOutput(zcodeAdmissionLine(t, 1, 1))
	require.Equal(t, 1, sink.PublishedControlCount())

	served := serveZCodeHookReview(t, a, stdin, zcodeHookReviewScript{requestStatus: zcodeCommandAccepted})
	result := make(chan error, 1)
	go func() {
		result <- a.SendRawInput(zcodeTrustAnswer(t, contracts.WorkspaceTrustDecisionTrust), agent.StopContext{})
	}()
	ctx := testutil.DeadlineContext(t)
	assert.Equal(t, a.APITimeout(), testutil.WaitForTimer(t, ctx, trap))
	clock.Advance(a.APITimeout()).MustWait(ctx)
	select {
	case err := <-result:
		require.ErrorContains(t, err, "ZCode raised no hook review in time")
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	requests := <-served
	assert.Equal(t, MethodConversationUnsubscribe, requests[len(requests)-1].Method)
	assert.False(t, zcodeHooksTrusted(t, a))
}

// Reject sends ZCode nothing, so the hooks stay blocked, and answers the question.
func TestZCodeRejectLeavesTheHooksBlocked(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newZCodeTrustAgent(t, 1)
	require.NoError(t, a.SendRawInput(zcodeTrustAnswer(t, contracts.WorkspaceTrustDecisionReject), agent.StopContext{}))
	assert.Empty(t, stdin.Requests(t))
	assert.False(t, zcodeHooksTrusted(t, a))
	assert.Equal(t, []string{providerkit.WorkspaceTrustRequestID("sess-1")}, sink.CanceledControls())
}

// A trust with no pending admission has nothing to trust.
func TestZCodeTrustWithNothingPendingSendsNothing(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.ControlSink{}), stdin)
	require.NoError(t, zcodeWorkspaceTrustHost{a}.TrustWorkspace())
	assert.Empty(t, stdin.Requests(t))
}
