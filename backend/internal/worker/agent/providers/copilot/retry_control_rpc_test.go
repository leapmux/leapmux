//go:build unix

package copilot

import (
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit/providerkittest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The control-path test drives the fake Copilot CLI, which runs only on Unix
// (see control_rpc_test.go).

// The browser's answer reaches the fake runtime through the control path, and the
// runtime's completion repeats it. A declined switch schedules the usage-limit
// retry at the stated reset, and an accepted one cancels it and schedules nothing.
func TestCopilotAutoModeSwitchAnswerDecidesTheTurnEnd(t *testing.T) {
	usageKind := leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT
	serverKind := leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR
	accepted := copilotSwitchedTimeline()
	for _, tc := range []struct {
		name     string
		decision map[string]any
		response string
		want     []agenttest.TimelineEntry
	}{
		{name: "no", decision: map[string]any{"behavior": "deny"}, response: "no", want: []agenttest.TimelineEntry{
			{Op: "turn_end"},
			{Op: "schedule_usage_limit", Kind: usageKind, ResetAt: copilotTime("2026-10-29T01:19:06.023Z")},
			{Op: "cancel_server_error", Kind: serverKind, Reason: "the turn ended on a usage limit that lifts at a stated time"},
		}},
		{name: "yes", decision: map[string]any{"behavior": "allow"}, response: "yes", want: accepted},
		{name: "yes_always", decision: map[string]any{"behavior": "allow", "scope": "session"}, response: "yes_always", want: accepted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, sink := startCopilotForControls(t)
			session := a.currentNativeSessionID()
			handleCopilotFrames(a,
				copilotTurnStartFrame(session),
				copilotEventFrame(session, "", contracts.CopilotEventSessionError, "2026-10-28T01:19:06.023Z", map[string]any{
					"errorType": "rate_limit", "statusCode": 429, "errorCode": "user_weekly_rate_limited", "eligibleForAutoSwitch": true,
					"message": "You've reached your weekly rate limit. Please wait for your limit to reset on October 29, 2026 at 1:19 AM or switch to auto model to continue.",
				}),
				copilotEventFrame(session, "", contracts.CopilotEventAutoModeSwitchRequested, "2026-10-28T01:19:06.023Z", map[string]any{
					"requestId": "switch", "errorCode": "user_weekly_rate_limited", "retryAfterSeconds": 86400,
				}),
			)
			request := sink.LastPublishedControl()
			resolution, claimed := copilotResolveControlAnswer(agent.ControlResponseContext{
				RequestID: request.RequestID, RequestPayload: request.Payload,
				ResponseContent: copilotAnswer(request.RequestID, tc.decision),
			})
			require.True(t, claimed)
			require.False(t, resolution.Withhold)
			// The runtime sends the completion before its receipt, so the record
			// holds the answer once the delivery returns.
			require.NoError(t, a.SendRawInput(resolution.Content, agent.StopContext{}))
			a.outputMu.Lock()
			response := a.autoSwitch.response
			a.outputMu.Unlock()
			require.Equal(t, tc.response, response)
			handleCopilotFrames(a, copilotEventFrame(session, "", contracts.CopilotEventSessionIdle, "2026-10-28T01:19:06.030Z", map[string]any{"mode": "interactive"}))
			assert.Equal(t, tc.want, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
			if tc.response == "no" {
				assert.Equal(t, 1, sink.AutoScheduleCount())
			} else {
				assert.Zero(t, sink.AutoScheduleCount(), "an accepted switch schedules nothing")
			}
		})
	}
}
