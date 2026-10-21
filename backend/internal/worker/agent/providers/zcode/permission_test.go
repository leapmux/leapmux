package zcode

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestZCodeBypassApprovesTheOriginalAnnouncementAndLaterTools(t *testing.T) {
	sink := &agenttest.ControlSink{}
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	a.SetAPITimeoutForTest(zcodeTestRPCTimeout)
	a.HandleOutput(zcodeRequestLine(t, 7, contracts.ZCodeMethodRequestPermission, zcodePermissionParams))
	a.HandleOutput(zcodeRequestLine(t, 99, contracts.ZCodeMethodRequestPermission, zcodePermissionParams))
	a.HandleOutput(zcodeRequestLine(t, 8, contracts.ZCodeMethodRequestUserInput, zcodeQuestionParams))
	published := sink.PublishedControlCount()
	done := make(chan agent.SettingsApplyResult, 1)
	go func() {
		done <- a.UpdateSettings(map[string]string{agent.OptionIDPermissionMode: contracts.ZCodeModeYolo})
	}()
	answerZCodeRequest(t, a, stdin, MethodSetMode, `{"session":{"sessionId":"sess-1"},"settings":{"mode":{"current":"yolo"},"permission":{"mode":"yolo"}}}`)
	result := <-done
	require.Equal(t, contracts.ZCodeModeYolo, result.ConfirmedOptions()[agent.OptionIDPermissionMode])
	require.Eventually(t, func() bool { return len(sink.CanceledControls()) == 1 }, 30*time.Second, time.Millisecond)
	assert.Equal(t, []string{"req-1"}, sink.CanceledControls())
	answers := func() []zcodeSentRequest {
		var replies []zcodeSentRequest
		for _, raw := range stdin.Frames() {
			var reply zcodeSentRequest
			if json.Unmarshal(raw, &reply) == nil && reply.Method == "" {
				replies = append(replies, reply)
			}
		}
		return replies
	}
	require.Len(t, answers(), 1)
	assert.Equal(t, "7", string(answers()[0].ID))
	assert.JSONEq(t, `{"decision":"allow","scope":"once"}`, string(answers()[0].Result))
	a.HandleOutput(zcodeRequestLine(t, 10, contracts.ZCodeMethodRequestPermission, strings.ReplaceAll(zcodePermissionParams, "req-1", "req-2")))
	require.Eventually(t, func() bool { return len(answers()) == 2 }, 30*time.Second, time.Millisecond)
	assert.Equal(t, published, sink.PublishedControlCount(), "later requests need no manual card")
}

func TestZCodeRefusesUnknownAndRepeatedNativeAnswers(t *testing.T) {
	sink := &agenttest.ControlSink{}
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	answer := []byte(`{"jsonrpc":"2.0","id":7,"result":{"decision":"allow","scope":"once"}}`)
	assert.Error(t, a.SendRawInput(answer, agent.StopContext{}))
	assert.Empty(t, stdin.Frames())
	a.HandleOutput(zcodeRequestLine(t, 7, contracts.ZCodeMethodRequestPermission, zcodePermissionParams))
	require.NoError(t, a.SendRawInput(answer, agent.StopContext{}))
	assert.Error(t, a.SendRawInput(answer, agent.StopContext{}))
	assert.Len(t, stdin.Frames(), 1)
}

func TestZCodeBypassRequiresAnObservedPlanFlag(t *testing.T) {
	sink := &agenttest.ControlSink{}
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	a.mode, a.modeObserved = contracts.ZCodeModeYolo, true
	a.HandleOutput(zcodeRequestLine(t, 7, contracts.ZCodeMethodRequestPermission, zcodePermissionParams))
	require.Eventually(t, a.approvalWork.Idle, 30*time.Second, time.Millisecond)
	assert.Empty(t, stdin.Frames())
	assert.Equal(t, 1, sink.PublishedControlCount())
	a.handleZCodeModeChanged(contracts.ZCodeModeYolo, false, "user")
	require.Eventually(t, a.approvalWork.Idle, 30*time.Second, time.Millisecond)
	assert.Len(t, stdin.Frames(), 1)
}

func TestZCodeChildRequestKeepsItsOwningRootDeliverySession(t *testing.T) {
	sink := &agenttest.ControlSink{}
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	params := strings.Replace(zcodePermissionParams, `"sessionId": "sess-1",`, `"sessionId": "sess-1", "origin":{"kind":"subagent"},`, 1)
	a.HandleOutput(zcodeRequestLine(t, 7, contracts.ZCodeMethodRequestPermission, params))
	assert.True(t, a.approvalWork.Idle())
	require.Equal(t, 1, sink.PublishedControlCount())
	assert.Equal(t, "sess-1", sink.LastPublishedControl().AgentSessionID)
	require.NoError(t, a.SendRawInput([]byte(`{"jsonrpc":"2.0","id":7,"result":{"decision":"allow","reason":"Approved once"}}`), agent.StopContext{}))
	assert.Len(t, stdin.Frames(), 1)
}

func TestZCodeControlRequestOfARetiredSessionIsDeclined(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ method, params string }{
		{contracts.ZCodeMethodRequestPermission, zcodePermissionParams},
		{contracts.ZCodeMethodRequestUserInput, zcodePlanParams},
		{contracts.ZCodeMethodRequestUserInput, zcodeQuestionParams},
	} {
		sink := &agenttest.ControlSink{}
		stdin := &zcodeRecordedStdin{}
		a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
		a.HandleOutput(zcodeRequestLine(t, 7, tc.method, strings.ReplaceAll(tc.params, "sess-1", "retired")))
		assert.Empty(t, sink.PublishedControls())
		assert.Zero(t, sink.MessageCount())
		assert.Empty(t, a.pendingControls)
		replies := stdin.Requests(t)
		require.Len(t, replies, 1)
		assert.Equal(t, "7", string(replies[0].ID))
		if tc.method == contracts.ZCodeMethodRequestPermission {
			assert.Contains(t, string(replies[0].Result), `"decision":"deny"`)
		} else {
			assert.JSONEq(t, `{"action":"decline"}`, string(replies[0].Result))
		}
	}
}

func TestZCodeClearContextKeepsControlsFromTheNewSubscription(t *testing.T) {
	sink := &agenttest.ControlSink{}
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	a.SetAPITimeoutForTest(zcodeTestRPCTimeout)
	go func() {
		create := waitZCodeRequest(t, stdin, MethodSessionCreate)
		a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, create), json.RawMessage(`{"session":{"sessionId":"sess-fresh"},"runtime":{"eventSeq":0},"settings":{"mode":{"current":"build"}}}`)))
		sub := waitZCodeRequest(t, stdin, MethodSessionSubscribe)
		a.HandleOutput(zcodeRequestLine(t, 7, contracts.ZCodeMethodRequestPermission, strings.ReplaceAll(zcodePermissionParams, "sess-1", "sess-fresh")))
		a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, sub), json.RawMessage(`{"eventSeq":0}`)))
	}()
	session, err := a.ClearContext()
	require.NoError(t, err)
	assert.Equal(t, "sess-fresh", session)
	assert.NotEmpty(t, a.pendingZCodeControlPayload("req-1"))
	assert.Empty(t, sink.CanceledControls())
}

func TestZCodeAutomaticApprovalUsesOnlyAnUnambiguousOnceOption(t *testing.T) {
	for _, tc := range []struct {
		name, options string
		allowed       bool
	}{
		{"native once", `[{"optionId":"allow_once","kind":"allow_once","response":{"decision":"allow","reason":"Approved once"}}]`, true},
		{"session grant", `[{"optionId":"allowSession","kind":"allow_session","response":{"decision":"allow"}}]`, false},
		{"project grant", `[{"optionId":"allow_project","response":{"decision":"allow","permissionUpdates":[{"type":"addRules"}]}}]`, false},
		{"updates on once", `[{"optionId":"allow_once","response":{"decision":"allow","permissionUpdates":[{}]}}]`, false},
		{"denial", `[{"optionId":"allow_once","response":{"decision":"deny"}}]`, false},
		{"ambiguous", `[{"optionId":"allow_once","response":{"decision":"allow"}},{"optionId":"allow_once","response":{"decision":"allow"}}]`, false},
		{"empty", `[]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var options []zcodePermissionOption
			require.NoError(t, json.Unmarshal([]byte(tc.options), &options))
			result := oncePermissionResult(options)
			if tc.allowed {
				assert.JSONEq(t, `{"decision":"allow","reason":"Approved once"}`, string(result))
			} else {
				assert.Nil(t, result)
			}
		})
	}
}

func TestZCodeFailedAutomaticWriteKeepsAManualFallback(t *testing.T) {
	sink := &agenttest.ControlSink{}
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	a.handleZCodeModeChanged(contracts.ZCodeModeYolo, false, "user")
	a.SetStdinForTest(agenttest.FailingStdin{})
	a.HandleOutput(zcodeRequestLine(t, 7, contracts.ZCodeMethodRequestPermission, zcodePermissionParams))
	require.Eventually(t, a.approvalWork.Idle, 30*time.Second, time.Millisecond)
	require.Equal(t, 1, sink.PublishedControlCount())
	a.queuePermissionApprovals()
	require.Eventually(t, a.approvalWork.Idle, 30*time.Second, time.Millisecond)
	assert.Equal(t, 1, sink.PublishedControlCount())
	a.SetStdinForTest(stdin)
	require.NoError(t, a.SendRawInput([]byte(`{"jsonrpc":"2.0","id":7,"result":{"decision":"allow","reason":"Approved once"}}`), agent.StopContext{}))
	assert.Len(t, stdin.Frames(), 1)
	a.HandleOutput(zcodeRequestLine(t, 99, contracts.ZCodeMethodRequestPermission, zcodePermissionParams))
	assert.Len(t, stdin.Frames(), 1)
	assert.Equal(t, 1, sink.PublishedControlCount(), "an accepted answer cannot reappear during a stale reannouncement")
}
