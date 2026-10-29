package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/modelvendor"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit/providerkittest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The frames below come verbatim from the probe runs that
// .tmp/retry-research/copilot-muse.md summarizes (.tmp/probe/copilot/runs).
// A name without a version is Copilot 1.0.87, the version that LeapMux runs
// with COPILOT_AUTO_UPDATE=false. A name with @1.0.103 is the newer runtime.
// A frame that no probe captured is composed by a helper below and says so.

// copilotRetryCase is one root turn end: the failure frames of the turn, in
// arrival order, and the session.idle that ends it.
type copilotRetryCase struct {
	name    string
	session string
	frames  []string
	idle    string
	want    providerkit.TurnFailure
	// switchedToAuto states that the runtime accepted the auto-mode switch, so
	// the turn end also cancels the usage-limit retry.
	switchedToAuto bool
}

// The verdicts that the probed failures give.
var (
	copilotNotRetryable   = providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable}
	copilotQueryTransient = providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Copilot reported a query error"}
)

func copilotUsageAt(cause, resetAt string) providerkit.TurnFailure {
	return providerkit.TurnFailure{
		Kind: providerkit.TurnFailureUsageLimited, ResetAt: copilotTime(resetAt),
		Cause: cause + " (the provider states when to retry)",
	}
}

func copilotTime(text string) time.Time {
	stamp, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		panic(err)
	}
	return stamp
}

// copilotTurnStartFrame composes the root assistant.turn_start that opens a
// turn. The probes captured it, but its fields do not reach the verdict.
func copilotTurnStartFrame(session string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":%q,"event":{"type":"assistant.turn_start","data":{"turnId":"0"},"ephemeral":true}}}`, session)
}

// copilotIdleFrame composes a root session.idle in the shape of the probed
// ones, for a run that the probe left waiting on an unanswered switch. Such a
// run captured no idle.
func copilotIdleFrame(session, timestamp string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":%q,"event":{"type":"session.idle","data":{"mode":"interactive"},"ephemeral":true,"id":"composed-idle","timestamp":%q}}}`, session, timestamp)
}

// copilotSwitchCompletedFrame composes the completion of an auto-mode switch in
// the shape that s429weeklyx1 captured, for a run that the probe never answered.
func copilotSwitchCompletedFrame(session, requestID, response, timestamp string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":%q,"event":{"type":"auto_mode_switch.completed","data":{"requestId":%q,"response":%q},"ephemeral":true,"id":"composed-completion","timestamp":%q}}}`, session, requestID, response, timestamp)
}

func copilotRetryCases() []copilotRetryCase {
	return []copilotRetryCase{
		{
			// The runtime retried the two 500s itself and then answered, so the
			// turn reports no session.error.
			name: "a 500 that the runtime's own retry recovered is not retryable @1.0.103", session: "b4c7fbf3-ec98-49d8-b0d2-076bdcf35116",
			frames: []string{
				`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"b4c7fbf3-ec98-49d8-b0d2-076bdcf35116","event":{"type":"model.call_failure","data":{"model":"gpt-probe","statusCode":500,"durationMs":0,"apiEndpoint":"/chat/completions","transport":"http","requestBodyBytes":118041,"requestBodyWireBytes":118041,"requestBodyEncoding":"identity","retryAttempt":0,"failureKind":"api","maxPromptTokens":100000,"maxOutputTokens":4096,"isByok":false,"isAuto":false,"reasoningEffort":"medium","interactionType":"conversation-agent","rte":false,"source":"top_level","errorMessage":"{\"message\":\"Internal Server Error\",\"code\":\"server_error\",\"type\":\"server_error\"}","errorCode":"server_error","errorType":"server_error"},"ephemeral":true,"id":"7944d71b-917f-4248-a01e-0347018a4d91","timestamp":"2026-10-28T01:19:13.089Z","parentId":"b5e919bb-9314-4631-a4f8-340b409c5491"}}}`,
				`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"b4c7fbf3-ec98-49d8-b0d2-076bdcf35116","event":{"type":"model.call_failure","data":{"model":"gpt-probe","statusCode":500,"durationMs":1,"apiEndpoint":"/chat/completions","transport":"http","requestBodyBytes":118041,"requestBodyWireBytes":118041,"requestBodyEncoding":"identity","retryAttempt":1,"failureKind":"api","maxPromptTokens":100000,"maxOutputTokens":4096,"isByok":false,"isAuto":false,"reasoningEffort":"medium","interactionType":"conversation-agent","rte":false,"source":"top_level","errorMessage":"{\"message\":\"Internal Server Error\",\"code\":\"server_error\",\"type\":\"server_error\"}","errorCode":"server_error","errorType":"server_error"},"ephemeral":true,"id":"8875d4eb-c52d-42b5-a857-4a2a09d159e6","timestamp":"2026-10-28T01:19:13.681Z","parentId":"b5e919bb-9314-4631-a4f8-340b409c5491"}}}`,
			},
			idle: `{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"b4c7fbf3-ec98-49d8-b0d2-076bdcf35116","event":{"type":"session.idle","data":{"mode":"interactive"},"ephemeral":true,"id":"a244b822-b82b-4c9e-8695-713ada4a443b","timestamp":"2026-10-28T01:19:14.803Z","parentId":"675a885a-ed13-44c6-9fa6-f97bfad5d9da"}}}`,
			want: copilotNotRetryable,
		},
		{
			name: "a bodyless 400 is not retryable @1.0.103", session: "a0a2da0b-abc1-4f70-8ece-4b7f6e6ea23f",
			frames: []string{`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"a0a2da0b-abc1-4f70-8ece-4b7f6e6ea23f","event":{"type":"session.error","data":{"errorType":"query","message":"Execution failed: 400 Bad Request","statusCode":400,"originatingMessageId":"7c16a527-0a0f-4ce9-b06c-b724ac57fe3a"},"id":"f75e0f96-ad8b-4c3d-bab0-e7b79c1bd620","timestamp":"2026-10-28T01:01:49.619Z","parentId":"fdb666cc-7cab-4b5b-b04a-c9c9c814dfd4"}}}`},
			idle:   `{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"a0a2da0b-abc1-4f70-8ece-4b7f6e6ea23f","event":{"type":"session.idle","data":{"mode":"interactive"},"ephemeral":true,"id":"8a96467a-9f65-42f3-baad-66f2cb88c08c","timestamp":"2026-10-28T01:01:49.620Z","parentId":"f75e0f96-ad8b-4c3d-bab0-e7b79c1bd620"}}}`,
			want:   copilotNotRetryable,
		},
		{
			name: "an invalid request body is not retryable @1.0.103", session: "e0bbe90f-ade9-4787-9ec9-53c8ddd31fcd",
			frames: []string{`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"e0bbe90f-ade9-4787-9ec9-53c8ddd31fcd","event":{"type":"session.error","data":{"errorType":"query","message":"Execution failed: 400 The request body is invalid.","statusCode":400,"originatingMessageId":"ab2bf551-ab94-4691-b0a9-5ef86a0be6f4"},"id":"3dd1c4d5-e9ef-485c-9f4c-af6f5170cba8","timestamp":"2026-10-28T01:01:50.618Z","parentId":"8ea1b771-5f5b-4963-be45-5dfc5f8b40c2"}}}`},
			idle:   `{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"e0bbe90f-ade9-4787-9ec9-53c8ddd31fcd","event":{"type":"session.idle","data":{"mode":"interactive"},"ephemeral":true,"id":"0020b4b9-c4d5-44be-88df-32f52d93005a","timestamp":"2026-10-28T01:01:50.619Z","parentId":"3dd1c4d5-e9ef-485c-9f4c-af6f5170cba8"}}}`,
			want:   copilotNotRetryable,
		},
		{
			name: "a filtered prompt is not retryable @1.0.103", session: "f51385b4-a4cf-45de-97e3-2eef43c3d3cb",
			frames: []string{`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"f51385b4-a4cf-45de-97e3-2eef43c3d3cb","event":{"type":"session.error","data":{"errorType":"query","message":"Execution failed: 400 The response was filtered due to the prompt triggering content management policy.","statusCode":400,"originatingMessageId":"f3b03450-0d1b-4660-9847-5bd7a74da906"},"id":"c1f47ef9-4f35-488c-90cb-9f3bb17f10a2","timestamp":"2026-10-28T01:01:55.669Z","parentId":"08865e34-b160-4d7a-84e5-f83f0267d4b2"}}}`},
			idle:   `{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"f51385b4-a4cf-45de-97e3-2eef43c3d3cb","event":{"type":"session.idle","data":{"mode":"interactive"},"ephemeral":true,"id":"2546d6e4-716c-4bd6-a5db-1f5a898f4fa1","timestamp":"2026-10-28T01:01:55.671Z","parentId":"c1f47ef9-4f35-488c-90cb-9f3bb17f10a2"}}}`,
			want:   copilotNotRetryable,
		},
		{
			name: "an expired credential is not retryable @1.0.103", session: "738339eb-ae6f-4994-9d7a-88adf852e63b",
			frames: []string{`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"738339eb-ae6f-4994-9d7a-88adf852e63b","event":{"type":"session.error","data":{"errorType":"authorization","message":"Authorization error. Your credentials may be expired or invalid.","statusCode":401,"remediation":"sign_in","originatingMessageId":"46761474-c3cc-41c4-a91a-fb3c82ce8ece"},"id":"e2e3a6fe-aa70-40d6-9232-4c2966c04394","timestamp":"2026-10-28T01:01:53.646Z","parentId":"a2855689-5137-44c1-bf72-bdc020fc4258"}}}`},
			idle:   `{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"738339eb-ae6f-4994-9d7a-88adf852e63b","event":{"type":"session.idle","data":{"mode":"interactive"},"ephemeral":true,"id":"59cf2251-9896-48c7-9ea8-c22a19f80f6f","timestamp":"2026-10-28T01:01:53.646Z","parentId":"e2e3a6fe-aa70-40d6-9232-4c2966c04394"}}}`,
			want:   copilotNotRetryable,
		},
		{
			name: "a forbidden request is not retryable @1.0.103", session: "a86ffa3a-9322-4ff5-94fb-d9055ab7a796",
			frames: []string{`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"a86ffa3a-9322-4ff5-94fb-d9055ab7a796","event":{"type":"session.error","data":{"errorType":"authorization","message":"Authorization error. Your credentials may be expired or invalid.","statusCode":403,"remediation":"sign_in","originatingMessageId":"dd6c7856-78bc-487f-bcdc-f005cd4ca54c"},"id":"52ee8d05-965f-4fcf-bc11-d060fbeefefe","timestamp":"2026-10-28T01:01:54.685Z","parentId":"e59a09ac-47c5-466d-aba6-fda5db61ce00"}}}`},
			idle:   `{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"a86ffa3a-9322-4ff5-94fb-d9055ab7a796","event":{"type":"session.idle","data":{"mode":"interactive"},"ephemeral":true,"id":"950ba7fe-9220-4740-a0e7-9d89be5d048d","timestamp":"2026-10-28T01:01:54.686Z","parentId":"52ee8d05-965f-4fcf-bc11-d060fbeefefe"}}}`,
			want:   copilotNotRetryable,
		},
		{
			name: "billing that is not configured is a billing block @1.0.103", session: "2c442f6d-0b10-4402-b289-04caa64938e7",
			frames: []string{`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"2c442f6d-0b10-4402-b289-04caa64938e7","event":{"type":"session.error","data":{"errorType":"quota","message":"Billing is not configured.","statusCode":402,"errorCode":"billing_not_configured","originatingMessageId":"01e8a5dd-cd1a-4e03-a6f8-d085d74477b3"},"id":"2b7fda05-d944-4ba6-94de-73ef3121446c","timestamp":"2026-10-28T01:22:22.972Z","parentId":"05e2deae-a103-443b-ab62-7627e96a9e1a"}}}`},
			idle:   `{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"2c442f6d-0b10-4402-b289-04caa64938e7","event":{"type":"session.idle","data":{"mode":"interactive"},"ephemeral":true,"id":"fca65d61-be77-4a9c-b72d-6213064bce3c","timestamp":"2026-10-28T01:22:22.975Z","parentId":"2b7fda05-d944-4ba6-94de-73ef3121446c"}}}`,
			want:   providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "Copilot reported a quota error (billing_not_configured)"},
		},
		{
			name: "a session spending limit is a billing block @1.0.103", session: "f4909dfc-da6a-4e20-9191-57f680041d76",
			frames: []string{`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"f4909dfc-da6a-4e20-9191-57f680041d76","event":{"type":"session.error","data":{"errorType":"quota","message":"You have reached the spending limit for this session.","statusCode":402,"errorCode":"session_quota_exceeded","originatingMessageId":"b5f6be30-5321-493e-9622-7864286c5b44"},"id":"b92afa65-66ec-4999-8fd2-aac9df68fb05","timestamp":"2026-10-28T01:22:24.275Z","parentId":"331cd4b3-1940-47fe-9378-df57da90cda3"}}}`},
			idle:   `{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"f4909dfc-da6a-4e20-9191-57f680041d76","event":{"type":"session.idle","data":{"mode":"interactive"},"ephemeral":true,"id":"2d46b3ca-3837-4f5e-88b7-a372f1c49b9e","timestamp":"2026-10-28T01:22:24.278Z","parentId":"b92afa65-66ec-4999-8fd2-aac9df68fb05"}}}`,
			want:   providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "Copilot reported a quota error (session_quota_exceeded)"},
		},
		{
			name: "an exceeded quota is a usage limit longer than a week", session: "a61fdc74-ec87-4fc7-8fb9-a236bf5a65b6",
			frames: []string{`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"a61fdc74-ec87-4fc7-8fb9-a236bf5a65b6","event":{"type":"session.error","data":{"errorType":"quota","message":"You have exceeded your quota.","statusCode":402,"errorCode":"quota_exceeded"},"id":"3060c747-5050-4796-bdd1-58330de2245e","timestamp":"2026-10-28T01:21:02.616Z","parentId":"15f3916a-1d17-421b-a210-ba84b8023790"}}}`},
			idle:   `{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"a61fdc74-ec87-4fc7-8fb9-a236bf5a65b6","event":{"type":"session.idle","data":{"mode":"interactive"},"ephemeral":true,"id":"ef819202-efe3-48b5-8c81-7fc54be14e6a","timestamp":"2026-10-28T01:21:02.617Z","parentId":"3060c747-5050-4796-bdd1-58330de2245e"}}}`,
			want:   providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowLonger, Cause: "Copilot reported a quota error (quota_exceeded)"},
		},
		{
			name: "an exceeded quota is a usage limit longer than a week @1.0.103", session: "de77473c-21e7-48f7-89b6-b782c20725a9",
			frames: []string{`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"de77473c-21e7-48f7-89b6-b782c20725a9","event":{"type":"session.error","data":{"errorType":"quota","message":"You have exceeded your quota.","statusCode":402,"errorCode":"quota_exceeded","originatingMessageId":"f8abaad6-ffa3-4567-9735-9eb8e70e634b"},"id":"70d72986-18d0-40ad-a487-87b19b36c3fd","timestamp":"2026-10-28T01:01:56.681Z","parentId":"abc8930e-571e-4f52-99c9-b1adf7a53a67"}}}`},
			idle:   `{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"de77473c-21e7-48f7-89b6-b782c20725a9","event":{"type":"session.idle","data":{"mode":"interactive"},"ephemeral":true,"id":"d585a8f3-58f2-4b8e-8ce5-ee852062942a","timestamp":"2026-10-28T01:01:56.682Z","parentId":"70d72986-18d0-40ad-a487-87b19b36c3fd"}}}`,
			want:   providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowLonger, Cause: "Copilot reported a quota error (quota_exceeded)"},
		},
		{
			name: "an additional spend limit is a usage limit longer than a week @1.0.103", session: "9ec88eb1-f019-4692-a107-1149a67832be",
			frames: []string{`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"9ec88eb1-f019-4692-a107-1149a67832be","event":{"type":"session.error","data":{"errorType":"quota","message":"You have reached your additional spend limit.","statusCode":402,"errorCode":"additional_spend_limit_reached","originatingMessageId":"bed460a0-2ecb-492e-8465-ef11456a24ff"},"id":"30b16ec5-16ec-4f03-8f98-b7d6d9382348","timestamp":"2026-10-28T01:01:57.667Z","parentId":"0dd7dc43-481d-4539-b60e-3f60a7ee713d"}}}`},
			idle:   `{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"9ec88eb1-f019-4692-a107-1149a67832be","event":{"type":"session.idle","data":{"mode":"interactive"},"ephemeral":true,"id":"dd4eec87-be0a-460f-aabf-ff9a21b1baba","timestamp":"2026-10-28T01:01:57.668Z","parentId":"30b16ec5-16ec-4f03-8f98-b7d6d9382348"}}}`,
			want:   providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowLonger, Cause: "Copilot reported a quota error (additional_spend_limit_reached)"},
		},
		{
			name: "a session rate limit without a switch is a usage limit within a week", session: "eeadd210-61bf-4ab7-b70a-f813a14c6e78",
			frames: []string{`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"eeadd210-61bf-4ab7-b70a-f813a14c6e78","event":{"type":"session.error","data":{"errorType":"rate_limit","message":"You've hit your session rate limit. Please wait for your limit to reset in 1 hour or switch to auto model to continue. Learn More (https://docs.github.com/copilot/concepts/rate-limits).","statusCode":429,"errorCode":"user_global_rate_limited","eligibleForAutoSwitch":false},"id":"821fd2ad-5865-46ac-a522-8cb92998d836","timestamp":"2026-10-28T01:21:04.480Z","parentId":"c02b6582-5352-4407-a4f1-75c16dab4807"}}}`},
			idle:   `{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"eeadd210-61bf-4ab7-b70a-f813a14c6e78","event":{"type":"session.idle","data":{"mode":"interactive"},"ephemeral":true,"id":"b230345b-e580-4552-b544-fe7795a2f705","timestamp":"2026-10-28T01:21:04.482Z","parentId":"821fd2ad-5865-46ac-a522-8cb92998d836"}}}`,
			want:   providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowWeekOrLess, Cause: "Copilot reported a rate_limit error (user_global_rate_limited)"},
		},
		{
			name: "a session rate limit without a switch is a usage limit within a week @1.0.103", session: "df89dda5-7c0d-421b-ba8d-8d2a9a390689",
			frames: []string{`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"df89dda5-7c0d-421b-ba8d-8d2a9a390689","event":{"type":"session.error","data":{"errorType":"rate_limit","message":"You've hit your session rate limit. Please wait for your limit to reset in 1 hour or switch to auto model to continue. Learn More (https://docs.github.com/copilot/concepts/rate-limits).","statusCode":429,"errorCode":"user_global_rate_limited","eligibleForAutoSwitch":false,"originatingMessageId":"3d0e07cf-d895-4050-9969-6d21d6f12088"},"id":"afbec133-0640-488d-9c28-ada616de131f","timestamp":"2026-10-28T01:17:02.695Z","parentId":"aff76aa9-de39-439e-bfc8-ba191e0897af"}}}`},
			idle:   `{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"df89dda5-7c0d-421b-ba8d-8d2a9a390689","event":{"type":"session.idle","data":{"mode":"interactive"},"ephemeral":true,"id":"13804fd3-c10c-454d-a98e-bf18a250480f","timestamp":"2026-10-28T01:17:02.696Z","parentId":"afbec133-0640-488d-9c28-ada616de131f"}}}`,
			want:   providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowWeekOrLess, Cause: "Copilot reported a rate_limit error (user_global_rate_limited)"},
		},
		{
			name: "a declined switch of a weekly limit schedules at its reset", session: "b69ec80b-5495-4f93-ae4d-ec113e9f2de0",
			frames: []string{
				`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"b69ec80b-5495-4f93-ae4d-ec113e9f2de0","event":{"type":"session.error","data":{"errorType":"rate_limit","message":"You've reached your weekly rate limit. Please wait for your limit to reset on October 29, 2026 at 1:21 AM or switch to auto model to continue. Learn More (https://docs.github.com/copilot/concepts/rate-limits).","statusCode":429,"errorCode":"user_weekly_rate_limited","eligibleForAutoSwitch":true},"id":"dd4173ef-5d7c-4ab1-8361-7d71cd756ad1","timestamp":"2026-10-28T01:21:03.570Z","parentId":"77fb79a0-1f1c-4682-9c07-38bcec2d895b"}}}`,
				`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"b69ec80b-5495-4f93-ae4d-ec113e9f2de0","event":{"type":"auto_mode_switch.requested","data":{"requestId":"19a66d9c-3fad-4b28-937c-5d017257a4b2","errorCode":"user_weekly_rate_limited","retryAfterSeconds":86400},"ephemeral":true,"id":"854e86ec-0383-4137-bc8e-b8f25d28eea0","timestamp":"2026-10-28T01:21:03.571Z","parentId":"dd4173ef-5d7c-4ab1-8361-7d71cd756ad1"}}}`,
				`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"b69ec80b-5495-4f93-ae4d-ec113e9f2de0","event":{"type":"auto_mode_switch.completed","data":{"requestId":"19a66d9c-3fad-4b28-937c-5d017257a4b2","response":"no"},"ephemeral":true,"id":"e3487b9f-54c8-4c63-aace-a9abe211f497","timestamp":"2026-10-28T01:21:03.572Z","parentId":"dd4173ef-5d7c-4ab1-8361-7d71cd756ad1"}}}`,
			},
			idle: `{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"b69ec80b-5495-4f93-ae4d-ec113e9f2de0","event":{"type":"session.idle","data":{"mode":"interactive"},"ephemeral":true,"id":"f6c29176-f893-4226-ac00-4b3f6ed6c383","timestamp":"2026-10-28T01:21:03.572Z","parentId":"dd4173ef-5d7c-4ab1-8361-7d71cd756ad1"}}}`,
			want: copilotUsageAt("Copilot reported a rate_limit error (user_weekly_rate_limited)", "2026-10-29T01:21:03.571Z"),
		},
		{
			name: "a declined switch of a weekly limit schedules at its reset @1.0.103", session: "477b49d9-aae5-4283-91aa-97972ed5bd63",
			frames: []string{
				`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"477b49d9-aae5-4283-91aa-97972ed5bd63","event":{"type":"session.error","data":{"errorType":"rate_limit","message":"You've reached your weekly rate limit. Please wait for your limit to reset on October 29, 2026 at 1:19 AM or switch to auto model to continue. Learn More (https://docs.github.com/copilot/concepts/rate-limits).","statusCode":429,"errorCode":"user_weekly_rate_limited","eligibleForAutoSwitch":true,"originatingMessageId":"b6a996dd-ccd3-45d2-8aaf-aefbaa4c8e62"},"id":"901785db-1345-4cff-9067-c90cd77bb3a0","timestamp":"2026-10-28T01:19:06.023Z","parentId":"9080c215-bd85-45a9-989e-9a12bddaadee"}}}`,
				`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"477b49d9-aae5-4283-91aa-97972ed5bd63","event":{"type":"auto_mode_switch.requested","data":{"requestId":"4687f94a-b93e-42fa-954a-7e8afdd392de","errorCode":"user_weekly_rate_limited","retryAfterSeconds":86400},"ephemeral":true,"id":"b41b4e3c-802d-449c-8f63-f142271c6521","timestamp":"2026-10-28T01:19:06.023Z","parentId":"901785db-1345-4cff-9067-c90cd77bb3a0"}}}`,
				`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"477b49d9-aae5-4283-91aa-97972ed5bd63","event":{"type":"auto_mode_switch.completed","data":{"requestId":"4687f94a-b93e-42fa-954a-7e8afdd392de","response":"no"},"ephemeral":true,"id":"ac38e12a-7f4a-425b-be27-d2e98073d86b","timestamp":"2026-10-28T01:19:06.024Z","parentId":"901785db-1345-4cff-9067-c90cd77bb3a0"}}}`,
			},
			idle: `{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"477b49d9-aae5-4283-91aa-97972ed5bd63","event":{"type":"session.idle","data":{"mode":"interactive"},"ephemeral":true,"id":"b466cc49-f7ff-4bce-b8c1-1c7b5744b0e7","timestamp":"2026-10-28T01:19:06.030Z","parentId":"901785db-1345-4cff-9067-c90cd77bb3a0"}}}`,
			want: copilotUsageAt("Copilot reported a rate_limit error (user_weekly_rate_limited)", "2026-10-29T01:19:06.023Z"),
		},
		{
			name: "an accepted switch is not retryable @1.0.103", session: "7536f4ed-9813-47f1-a0de-1bf68cc39b29",
			frames: []string{
				`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"7536f4ed-9813-47f1-a0de-1bf68cc39b29","event":{"type":"session.error","data":{"errorType":"rate_limit","message":"You've reached your weekly rate limit. Please wait for your limit to reset on October 29, 2026 at 1:19 AM or switch to auto model to continue. Learn More (https://docs.github.com/copilot/concepts/rate-limits).","statusCode":429,"errorCode":"user_weekly_rate_limited","eligibleForAutoSwitch":true,"originatingMessageId":"6460a4f6-500d-461a-a7f1-a9df758422ad"},"id":"b25f40db-fbc6-4a38-96e5-4a9d8a781f52","timestamp":"2026-10-28T01:19:15.839Z","parentId":"f847e8f9-f764-4fb5-8ea1-49d837a31ee9"}}}`,
				`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"7536f4ed-9813-47f1-a0de-1bf68cc39b29","event":{"type":"auto_mode_switch.requested","data":{"requestId":"a85cba2b-499b-44b9-a6b7-56b7e8d68347","errorCode":"user_weekly_rate_limited","retryAfterSeconds":86400},"ephemeral":true,"id":"96634156-75a8-4d6b-9ffb-8c599c006e12","timestamp":"2026-10-28T01:19:15.839Z","parentId":"b25f40db-fbc6-4a38-96e5-4a9d8a781f52"}}}`,
				`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"7536f4ed-9813-47f1-a0de-1bf68cc39b29","event":{"type":"auto_mode_switch.completed","data":{"requestId":"a85cba2b-499b-44b9-a6b7-56b7e8d68347","response":"yes"},"ephemeral":true,"id":"f1e020c0-c311-4117-ad7a-8d5f16ed64fe","timestamp":"2026-10-28T01:19:15.840Z","parentId":"b25f40db-fbc6-4a38-96e5-4a9d8a781f52"}}}`,
			},
			idle:           `{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"7536f4ed-9813-47f1-a0de-1bf68cc39b29","event":{"type":"session.idle","data":{"mode":"interactive"},"ephemeral":true,"id":"0e611715-a78c-4fe7-9b28-39acea688606","timestamp":"2026-10-28T01:19:15.841Z","parentId":"c6c5718e-e6cc-42e6-a4ac-0a4110b3a552"}}}`,
			want:           copilotNotRetryable,
			switchedToAuto: true,
		},
		{
			name: "a declined switch of a model limit schedules at its reset @1.0.103", session: "760ec5e0-162c-401b-9efb-58251511eacb",
			frames: []string{
				`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"760ec5e0-162c-401b-9efb-58251511eacb","event":{"type":"session.error","data":{"errorType":"rate_limit","message":"You've hit the rate limit for this model. Please switch models or wait for your limit to reset in 1 hour. Learn More (https://docs.github.com/copilot/concepts/rate-limits).","statusCode":429,"errorCode":"user_model_rate_limited","eligibleForAutoSwitch":true,"originatingMessageId":"bba968d3-63e4-4822-a982-43e276cf296c"},"id":"029d8f99-c50c-4d48-88d3-051cb1a8b752","timestamp":"2026-10-28T01:17:03.727Z","parentId":"96808f12-c3e8-44d9-afa2-1d00f3c71d81"}}}`,
				`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"760ec5e0-162c-401b-9efb-58251511eacb","event":{"type":"auto_mode_switch.requested","data":{"requestId":"87fab062-ec74-46d6-8e05-57715f07100c","errorCode":"user_model_rate_limited","retryAfterSeconds":3600},"ephemeral":true,"id":"c61612a0-a256-4b9a-80b1-875fd038f15f","timestamp":"2026-10-28T01:17:03.727Z","parentId":"029d8f99-c50c-4d48-88d3-051cb1a8b752"}}}`,
				copilotSwitchCompletedFrame("760ec5e0-162c-401b-9efb-58251511eacb", "87fab062-ec74-46d6-8e05-57715f07100c", "no", "2026-10-28T01:17:04.000Z"),
			},
			idle: copilotIdleFrame("760ec5e0-162c-401b-9efb-58251511eacb", "2026-10-28T01:17:04.001Z"),
			want: copilotUsageAt("Copilot reported a rate_limit error (user_model_rate_limited)", "2026-10-28T02:17:03.727Z"),
		},
		{
			name: "a declined switch of a rate limit schedules at its reset @1.0.103", session: "fdad8ffb-351d-402c-94fe-56a9606fd161",
			frames: []string{
				`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"fdad8ffb-351d-402c-94fe-56a9606fd161","event":{"type":"session.error","data":{"errorType":"rate_limit","message":"You've hit your rate limit. Please wait for your limit to reset in under a minute or switch to auto model to continue. Learn More (https://docs.github.com/copilot/concepts/rate-limits).","statusCode":429,"errorCode":"rate_limited","eligibleForAutoSwitch":true,"originatingMessageId":"f006148d-d32e-4ac9-98f1-dbdb43386516"},"id":"7b73c69b-22cd-4a00-a5ed-e5143a3ee907","timestamp":"2026-10-28T01:08:46.541Z","parentId":"e0036875-3cba-4b33-93b5-ce3cec481456"}}}`,
				`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"fdad8ffb-351d-402c-94fe-56a9606fd161","event":{"type":"auto_mode_switch.requested","data":{"requestId":"d36ceb48-0ba9-45b0-bc09-a74c9c0bb826","errorCode":"rate_limited","retryAfterSeconds":20},"ephemeral":true,"id":"f7a4476b-676f-4ca1-af11-a1451666c0b4","timestamp":"2026-10-28T01:08:46.541Z","parentId":"7b73c69b-22cd-4a00-a5ed-e5143a3ee907"}}}`,
				copilotSwitchCompletedFrame("fdad8ffb-351d-402c-94fe-56a9606fd161", "d36ceb48-0ba9-45b0-bc09-a74c9c0bb826", "no", "2026-10-28T01:08:47.000Z"),
			},
			idle: copilotIdleFrame("fdad8ffb-351d-402c-94fe-56a9606fd161", "2026-10-28T01:08:47.001Z"),
			want: copilotUsageAt("Copilot reported a rate_limit error (rate_limited)", "2026-10-28T01:09:06.541Z"),
		},
		{
			name: "a declined switch of a rate limit without a wait is rate limited @1.0.103", session: "78f1379c-d6f1-4706-a973-c10ee5a0acd6",
			frames: []string{
				`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"78f1379c-d6f1-4706-a973-c10ee5a0acd6","event":{"type":"session.error","data":{"errorType":"rate_limit","message":"You've hit your rate limit. Please wait for your limit to reset or switch to auto model to continue. Learn More (https://docs.github.com/copilot/concepts/rate-limits).","statusCode":429,"errorCode":"rate_limited","eligibleForAutoSwitch":true,"originatingMessageId":"148df89e-9f76-4039-923f-dd1cf459cbe6"},"id":"557390e3-67c7-4d1b-8fed-3476ac194fb6","timestamp":"2026-10-28T01:02:54.806Z","parentId":"b94a9794-72b0-4317-8e45-a3d2c8bd10c1"}}}`,
				`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"78f1379c-d6f1-4706-a973-c10ee5a0acd6","event":{"type":"auto_mode_switch.requested","data":{"requestId":"3f0f8500-686e-499a-ae52-d0b1212ae643","errorCode":"rate_limited","retryAfterSeconds":null},"ephemeral":true,"id":"f6cfc3b0-f966-4eca-a33f-c420388c6944","timestamp":"2026-10-28T01:02:54.807Z","parentId":"557390e3-67c7-4d1b-8fed-3476ac194fb6"}}}`,
				copilotSwitchCompletedFrame("78f1379c-d6f1-4706-a973-c10ee5a0acd6", "3f0f8500-686e-499a-ae52-d0b1212ae643", "no", "2026-10-28T01:02:55.000Z"),
			},
			idle: copilotIdleFrame("78f1379c-d6f1-4706-a973-c10ee5a0acd6", "2026-10-28T01:02:55.001Z"),
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "Copilot reported a rate_limit error (rate_limited)"},
		},
		{
			name: "a 500 after the runtime's own retries is transient", session: "925876b4-d967-48d7-979b-a31c45f268bd",
			frames: []string{
				`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"925876b4-d967-48d7-979b-a31c45f268bd","event":{"type":"model.call_failure","data":{"model":"gpt-probe","statusCode":500,"durationMs":1,"apiEndpoint":"/chat/completions","transport":"http","failureKind":"api","maxPromptTokens":100000,"maxOutputTokens":4096,"isByok":false,"isAuto":false,"reasoningEffort":"medium","interactionType":"conversation-agent","rte":false,"source":"top_level","errorMessage":"{\"message\":\"Internal Server Error\",\"code\":\"server_error\",\"type\":\"server_error\"}"},"ephemeral":true,"id":"59b48b81-0c2b-4607-b12d-752ba81c4faa","timestamp":"2026-10-28T01:20:29.339Z","parentId":"5ae21732-1a50-424c-bf8e-b85c58cb9194"}}}`,
				`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"925876b4-d967-48d7-979b-a31c45f268bd","event":{"type":"session.error","data":{"errorType":"query","message":"Execution failed: Failed to get response from the AI model; retried 5 times (total retry wait time: 15.28 seconds) Last error: 500 Internal Server Error","statusCode":500},"id":"ff091359-6d68-4883-8de5-216b727480eb","timestamp":"2026-10-28T01:20:29.341Z","parentId":"65439d53-e2ac-4903-80a6-febeee96d634"}}}`,
			},
			idle: `{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"925876b4-d967-48d7-979b-a31c45f268bd","event":{"type":"session.idle","data":{"mode":"interactive"},"ephemeral":true,"id":"c9b52845-b341-4e35-82e5-410977a6376c","timestamp":"2026-10-28T01:20:29.343Z","parentId":"ff091359-6d68-4883-8de5-216b727480eb"}}}`,
			want: copilotQueryTransient,
		},
		{
			name: "a 529 after the runtime's own retries is transient @1.0.103", session: "fdd84bec-347f-4a23-a233-9f42eb3e5335",
			frames: []string{
				`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"fdd84bec-347f-4a23-a233-9f42eb3e5335","event":{"type":"model.call_failure","data":{"model":"gpt-probe","statusCode":529,"durationMs":1,"apiEndpoint":"/chat/completions","transport":"http","requestBodyBytes":118039,"requestBodyWireBytes":118039,"requestBodyEncoding":"identity","retryAttempt":5,"failureKind":"api","maxPromptTokens":100000,"maxOutputTokens":4096,"isByok":false,"isAuto":false,"reasoningEffort":"medium","interactionType":"conversation-agent","rte":false,"source":"top_level","errorMessage":"{\"message\":\"The model is overloaded\",\"code\":\"model_overloaded\",\"type\":\"model_overloaded\"}","errorCode":"model_overloaded","errorType":"model_overloaded"},"ephemeral":true,"id":"3a78958d-4105-4f7e-abd7-6606ef0ac007","timestamp":"2026-10-28T00:55:30.481Z","parentId":"0fd8ab6e-8466-40ef-ae7f-c71e8503b53f"}}}`,
				`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"fdd84bec-347f-4a23-a233-9f42eb3e5335","event":{"type":"session.error","data":{"errorType":"query","message":"Execution failed: Failed to get response from the AI model; retried 5 times (total retry wait time: 12.86 seconds) Last error: 529 The model is overloaded","statusCode":529,"originatingMessageId":"1f9b9faa-13d5-4963-9527-3accb0d881ee"},"id":"df372466-c500-4eb6-9540-5d5db306b628","timestamp":"2026-10-28T00:55:30.483Z","parentId":"c0e3ed15-73d9-4508-b32f-ff81f4b13ed5"}}}`,
			},
			idle: `{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"fdd84bec-347f-4a23-a233-9f42eb3e5335","event":{"type":"session.idle","data":{"mode":"interactive"},"ephemeral":true,"id":"85b05816-0198-4bea-8c57-ddfc9a82e020","timestamp":"2026-10-28T00:55:30.486Z","parentId":"df372466-c500-4eb6-9540-5d5db306b628"}}}`,
			want: copilotQueryTransient,
		},
		{
			name: "a 502 without a body is transient @1.0.103", session: "f55384ed-5fcf-442b-9bed-851721a3e171",
			frames: []string{`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"f55384ed-5fcf-442b-9bed-851721a3e171","event":{"type":"session.error","data":{"errorType":"query","message":"Execution failed: Failed to get response from the AI model; retried 5 times (total retry wait time: 14.69 seconds) Last error: 502 Bad Gateway","statusCode":502,"originatingMessageId":"48defb16-ac4e-4cc2-92af-27ca6ecee9ad"},"id":"02c35cef-5b8d-4c10-9ddb-039b880714af","timestamp":"2026-10-28T00:56:05.159Z","parentId":"4d98e074-05e2-4ab2-bbd1-9a3699fac33c"}}}`},
			idle:   `{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"f55384ed-5fcf-442b-9bed-851721a3e171","event":{"type":"session.idle","data":{"mode":"interactive"},"ephemeral":true,"id":"875ff4de-aeb3-4cd7-bcf1-503a69baf1fd","timestamp":"2026-10-28T00:56:05.160Z","parentId":"02c35cef-5b8d-4c10-9ddb-039b880714af"}}}`,
			want:   copilotQueryTransient,
		},
		{
			name: "a broken stream is transient", session: "ced5a632-2bb9-4ae6-b3fe-bd199ce115ea",
			frames: []string{
				`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"ced5a632-2bb9-4ae6-b3fe-bd199ce115ea","event":{"type":"model.call_failure","data":{"model":"gpt-probe","durationMs":1,"apiEndpoint":"/chat/completions","transport":"http","failureKind":"api","maxPromptTokens":100000,"maxOutputTokens":4096,"isByok":false,"isAuto":false,"reasoningEffort":"medium","interactionType":"conversation-agent","rte":false,"source":"top_level","errorMessage":"{\"message\":\"The stream broke.\",\"code\":\"server_error\"}"},"ephemeral":true,"id":"eb8f0276-2e3f-4ccb-9dab-e449eac27f29","timestamp":"2026-10-28T01:20:52.729Z","parentId":"1de1643e-7a8b-4b17-94d8-86b3d7a95ffa"}}}`,
				`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"ced5a632-2bb9-4ae6-b3fe-bd199ce115ea","event":{"type":"session.error","data":{"errorType":"query","message":"Execution failed: Failed to get response from the AI model; retried 5 times (total retry wait time: 16.28 seconds) Last error: The stream broke."},"id":"8b09c0c4-0c5d-44e0-88d8-f34bf387f251","timestamp":"2026-10-28T01:20:52.731Z","parentId":"02b59b23-62cb-43f9-be85-04fd002fd46e"}}}`,
			},
			idle: `{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"ced5a632-2bb9-4ae6-b3fe-bd199ce115ea","event":{"type":"session.idle","data":{"mode":"interactive"},"ephemeral":true,"id":"b9ba3b19-c5b0-4523-9b50-0ba9d4821972","timestamp":"2026-10-28T01:20:52.732Z","parentId":"8b09c0c4-0c5d-44e0-88d8-f34bf387f251"}}}`,
			want: copilotQueryTransient,
		},
		{
			name: "a broken stream is transient @1.0.103", session: "f232e879-cc84-4ffa-86ff-bca30eea7f62",
			frames: []string{
				`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"f232e879-cc84-4ffa-86ff-bca30eea7f62","event":{"type":"model.call_failure","data":{"model":"gpt-probe","durationMs":2,"apiEndpoint":"/chat/completions","transport":"http","requestBodyBytes":118039,"requestBodyWireBytes":118039,"requestBodyEncoding":"identity","retryAttempt":5,"failureKind":"api","maxPromptTokens":100000,"maxOutputTokens":4096,"isByok":false,"isAuto":false,"reasoningEffort":"medium","interactionType":"conversation-agent","rte":false,"source":"top_level","errorMessage":"{\"message\":\"The stream broke.\",\"code\":\"server_error\"}","errorCode":"server_error"},"ephemeral":true,"id":"e6243e41-c855-4352-8558-80ff3bfe6f82","timestamp":"2026-10-28T01:01:42.571Z","parentId":"a90d108d-76cd-4eb5-b7eb-ae0d6b7a3d5d"}}}`,
				`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"f232e879-cc84-4ffa-86ff-bca30eea7f62","event":{"type":"session.error","data":{"errorType":"query","message":"Execution failed: Failed to get response from the AI model; retried 5 times (total retry wait time: 17.84 seconds) Last error: The stream broke.","originatingMessageId":"71aee65a-e122-46a2-9b02-365d06c84f12"},"id":"4d7dfc9a-a877-4c58-af44-fc0873067a6d","timestamp":"2026-10-28T01:01:42.573Z","parentId":"4100a212-436d-4fc5-a7f1-de745a75a0bd"}}}`,
			},
			idle: `{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"f232e879-cc84-4ffa-86ff-bca30eea7f62","event":{"type":"session.idle","data":{"mode":"interactive"},"ephemeral":true,"id":"6dd6bfb5-d686-4678-9aa7-35edaa55d212","timestamp":"2026-10-28T01:01:42.574Z","parentId":"4d7dfc9a-a877-4c58-af44-fc0873067a6d"}}}`,
			want: copilotQueryTransient,
		},
		{
			name: "a reset connection is transient", session: "bab1e0cd-659e-4bc9-bf2c-813162697972",
			frames: []string{
				`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"bab1e0cd-659e-4bc9-bf2c-813162697972","event":{"type":"model.call_failure","data":{"model":"gpt-probe","durationMs":1,"apiEndpoint":"/chat/completions","transport":"http","failureKind":"transport","maxPromptTokens":100000,"maxOutputTokens":4096,"isByok":false,"isAuto":false,"reasoningEffort":"medium","interactionType":"conversation-agent","rte":false,"source":"top_level","errorMessage":"\"Failed native model HTTP request: error decoding response body: request or response body error: error reading a body from connection: Connection reset by peer (os error 54) [ECONNRESET]\""},"ephemeral":true,"id":"c5f3e321-d0e5-4a59-bb2c-50c16b73a0ed","timestamp":"2026-10-28T01:20:35.408Z","parentId":"14509546-a876-49f9-a043-db4498135334"}}}`,
				`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"bab1e0cd-659e-4bc9-bf2c-813162697972","event":{"type":"session.error","data":{"errorType":"query","message":"Execution failed: Failed to get response from the AI model; retried 5 times (total retry wait time: 5.00 seconds) Last error: Failed native model HTTP request: error decoding response body: request or response body error: error reading a body from connection: Connection reset by peer (os error 54) [ECONNRESET]"},"id":"823c238b-bd24-4458-9a24-e5d0bbdcafb4","timestamp":"2026-10-28T01:20:35.409Z","parentId":"4abdd002-41d3-46cf-ac08-661b2aa07510"}}}`,
			},
			idle: `{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"bab1e0cd-659e-4bc9-bf2c-813162697972","event":{"type":"session.idle","data":{"mode":"interactive"},"ephemeral":true,"id":"2b5488bd-e7a9-4e67-941f-0eac1a89c041","timestamp":"2026-10-28T01:20:35.411Z","parentId":"823c238b-bd24-4458-9a24-e5d0bbdcafb4"}}}`,
			want: copilotQueryTransient,
		},
		{
			name: "a reset connection is transient @1.0.103", session: "0e7a5071-db67-4c92-bc09-ad222453e4ab",
			frames: []string{
				`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"0e7a5071-db67-4c92-bc09-ad222453e4ab","event":{"type":"model.call_failure","data":{"model":"gpt-probe","durationMs":1,"apiEndpoint":"/chat/completions","transport":"http","requestBodyBytes":118041,"requestBodyWireBytes":118041,"requestBodyEncoding":"identity","retryAttempt":5,"failureKind":"transport","maxPromptTokens":100000,"maxOutputTokens":4096,"isByok":false,"isAuto":false,"reasoningEffort":"medium","interactionType":"conversation-agent","rte":false,"source":"top_level","errorMessage":"\"Failed native model HTTP request: error decoding response body: error reading a body from connection: Connection reset by peer (os error 54) [ECONNRESET]\""},"ephemeral":true,"id":"0c9c3697-30b6-45c1-8410-58d34a0b5c76","timestamp":"2026-10-28T01:19:12.102Z","parentId":"d8373d0b-82e2-402e-9483-0cebb8aaff6d"}}}`,
				`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"0e7a5071-db67-4c92-bc09-ad222453e4ab","event":{"type":"session.error","data":{"errorType":"query","message":"Execution failed: Failed to get response from the AI model; retried 5 times (total retry wait time: 5.00 seconds) Last error: Failed native model HTTP request: error decoding response body: error reading a body from connection: Connection reset by peer (os error 54) [ECONNRESET]","originatingMessageId":"1d20e13b-fe56-4913-a160-54c70679cd78"},"id":"6f709f81-e596-4702-9294-1e4f494d00ac","timestamp":"2026-10-28T01:19:12.105Z","parentId":"1657034e-5124-487d-95c5-1f44f9282162"}}}`,
			},
			idle: `{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"0e7a5071-db67-4c92-bc09-ad222453e4ab","event":{"type":"session.idle","data":{"mode":"interactive"},"ephemeral":true,"id":"8ab1574e-ebf6-4fbb-854c-c6d2aef706a4","timestamp":"2026-10-28T01:19:12.106Z","parentId":"6f709f81-e596-4702-9294-1e4f494d00ac"}}}`,
			want: copilotQueryTransient,
		},
	}
}

// newCopilotRetryAgent builds an agent that serves one session and records
// every operation on a timeline.
func newCopilotRetryAgent(session string) (*Agent, *agenttest.ControlSink) {
	sink := &agenttest.ControlSink{}
	a := newCopilotControlAgent(agent.NewProviderServices(sink))
	a.sessionID = session
	return a, sink
}

// copilotSwitchedTimeline is the timeline of a root turn end after an accepted
// auto-mode switch.
func copilotSwitchedTimeline() []agenttest.TimelineEntry {
	return []agenttest.TimelineEntry{
		{Op: "turn_end"},
		{Op: "cancel_usage_limit", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT, Reason: copilotAutoModelReason},
		{Op: "cancel_server_error", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, Reason: providerkit.TurnEndedWithoutRetryableFailure},
	}
}

// handleCopilotFrames feeds the frames in order.
func handleCopilotFrames(a *Agent, frames ...string) {
	for _, frame := range frames {
		a.HandleOutput([]byte(frame))
	}
}

// copilotSessionErrorFrame returns the session.error among the frames, the
// payload that a scheduled retry carries.
func copilotSessionErrorFrame(t *testing.T, frames []string) []byte {
	t.Helper()
	for _, frame := range frames {
		if _, ok := copilotEventOfType([]byte(frame), contracts.CopilotEventSessionError); ok {
			return []byte(frame)
		}
	}
	return nil
}

// The records that the dispatch keeps give the verdict that each probe implies.
func TestCopilotTurnVerdict(t *testing.T) {
	t.Parallel()
	for _, tc := range copilotRetryCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, _ := newCopilotRetryAgent(tc.session)
			handleCopilotFrames(a, append([]string{copilotTurnStartFrame(tc.session)}, tc.frames...)...)
			idle, err := decodeCopilotSessionEvent(copilotEnvelopeParams(t, tc.idle), tc.session)
			require.NoError(t, err)
			got := copilotTurnVerdict(a.turnFailure, a.autoSwitch, false, a.nativeEventTime(idle))
			assert.Equal(t, tc.want, got)
		})
	}
}

// copilotEnvelopeParams returns the params of a session.event frame.
func copilotEnvelopeParams(t *testing.T, frame string) json.RawMessage {
	t.Helper()
	var envelope struct {
		Params json.RawMessage `json:"params"`
	}
	require.NoError(t, json.Unmarshal([]byte(frame), &envelope))
	return envelope.Params
}

// The real dispatch writes the turn-end row and then exactly the retry
// operations of the verdict, with the session.error as the payload.
func TestCopilotTurnEndAppliesTheVerdict(t *testing.T) {
	t.Parallel()
	for _, tc := range copilotRetryCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink := newCopilotRetryAgent(tc.session)
			handleCopilotFrames(a, append(append([]string{copilotTurnStartFrame(tc.session)}, tc.frames...), tc.idle)...)
			want := providerkittest.TurnEndThen(tc.want)
			if tc.switchedToAuto {
				want = copilotSwitchedTimeline()
			}
			assert.Equal(t, want, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
			if sink.AutoScheduleCount() > 0 {
				assert.Equal(t, copilotSessionErrorFrame(t, tc.frames), sink.LastAutoSchedule().SourcePayload)
			}
			assert.Equal(t, copilotTurnFailure{}, a.turnFailure, "the turn end consumes the failure record")
			assert.Equal(t, copilotAutoSwitch{}, a.autoSwitch, "the turn end consumes the switch record")
		})
	}
}

// The verdicts that the probes reach do not show each operation of the queue,
// so these literal timelines state them.
func TestCopilotTurnEndRetryOperations(t *testing.T) {
	t.Parallel()
	byName := make(map[string]copilotRetryCase)
	for _, tc := range copilotRetryCases() {
		byName[tc.name] = tc
	}
	usageKind := leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT
	serverKind := leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR
	for _, tc := range []struct {
		name string
		want []agenttest.TimelineEntry
	}{
		{name: "a declined switch of a weekly limit schedules at its reset", want: []agenttest.TimelineEntry{
			{Op: "turn_end"},
			{Op: "schedule_usage_limit", Kind: usageKind, ResetAt: copilotTime("2026-10-29T01:21:03.571Z")},
			{Op: "cancel_server_error", Kind: serverKind, Reason: "the turn ended on a usage limit that lifts at a stated time"},
		}},
		{name: "a broken stream is transient", want: []agenttest.TimelineEntry{
			{Op: "turn_end"},
			{Op: "schedule_server_error", Kind: serverKind},
		}},
		{name: "a session spending limit is a billing block @1.0.103", want: []agenttest.TimelineEntry{
			{Op: "turn_end"},
			{Op: "cancel_usage_limit", Kind: usageKind, Reason: "Copilot reported a quota error (session_quota_exceeded), which only a billing or plan change lifts"},
			{Op: "cancel_server_error", Kind: serverKind, Reason: "Copilot reported a quota error (session_quota_exceeded), which only a billing or plan change lifts"},
		}},
		{name: "an exceeded quota is a usage limit longer than a week", want: []agenttest.TimelineEntry{
			{Op: "turn_end"},
			{Op: "cancel_server_error", Kind: serverKind, Reason: "the turn ended on a usage limit that states no reset within a week"},
		}},
		{name: "a declined switch of a rate limit without a wait is rate limited @1.0.103", want: []agenttest.TimelineEntry{
			{Op: "turn_end"},
			{Op: "schedule_server_error", Kind: serverKind},
		}},
		{name: "a forbidden request is not retryable @1.0.103", want: []agenttest.TimelineEntry{
			{Op: "turn_end"},
			{Op: "cancel_server_error", Kind: serverKind, Reason: providerkit.TurnEndedWithoutRetryableFailure},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			probe, ok := byName[tc.name]
			require.True(t, ok, "the probe case exists")
			a, sink := newCopilotRetryAgent(probe.session)
			handleCopilotFrames(a, append(append([]string{copilotTurnStartFrame(probe.session)}, probe.frames...), probe.idle)...)
			assert.Equal(t, tc.want, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
		})
	}
}

// copilotEventFrame composes one root or child session event for the edge
// cases that no probe reached. An empty timestamp leaves the field out.
func copilotEventFrame(session, agentID, eventType, timestamp string, data any) string {
	event := map[string]any{"type": eventType, "data": data, "id": "composed-" + eventType}
	if agentID != "" {
		event["agentId"] = agentID
	}
	if timestamp != "" {
		event["timestamp"] = timestamp
	}
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "session.event", "params": map[string]any{"sessionId": session, "event": event}})
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// The composed frames of a weekly limit whose switch the answer declined. The
// request asks for wait seconds, and the idle arrives at idleAt.
func copilotDeclinedWeeklyLimit(session string, wait any, idleAt string) []string {
	return []string{
		copilotTurnStartFrame(session),
		copilotEventFrame(session, "", contracts.CopilotEventSessionError, "2026-10-28T01:19:06.023Z", map[string]any{
			"errorType": "rate_limit", "statusCode": 429, "errorCode": "user_weekly_rate_limited", "eligibleForAutoSwitch": true,
			"message": "You've reached your weekly rate limit. Please wait for your limit to reset on October 29, 2026 at 1:19 AM or switch to auto model to continue.",
		}),
		copilotEventFrame(session, "", contracts.CopilotEventAutoModeSwitchRequested, "2026-10-28T01:19:06.023Z", map[string]any{
			"requestId": "switch", "errorCode": "user_weekly_rate_limited", "retryAfterSeconds": wait,
		}),
		copilotEventFrame(session, "", contracts.CopilotEventAutoModeSwitchCompleted, "2026-10-28T01:19:06.024Z", map[string]any{
			"requestId": "switch", "response": "no",
		}),
		copilotEventFrame(session, "", contracts.CopilotEventSessionIdle, idleAt, map[string]any{"mode": "interactive"}),
	}
}

func TestCopilotTurnEndEdgeCases(t *testing.T) {
	t.Parallel()
	const session = "session"
	weekly := providerkit.TurnFailure{Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowWeekOrLess, Cause: "Copilot reported a rate_limit error (user_weekly_rate_limited)"}
	brokenStream := map[string]any{"errorType": "query", "message": "Execution failed: Failed to get response from the AI model; retried 5 times (total retry wait time: 16.28 seconds) Last error: The stream broke."}
	for _, tc := range []struct {
		name   string
		frames []string
		want   providerkit.TurnFailure
	}{
		{
			// The reset instant passed while the switch waited for an answer.
			name:   "a reset that passed before the turn end falls back to the weekly window",
			frames: copilotDeclinedWeeklyLimit(session, 3600, "2026-10-28T06:19:06.030Z"),
			want:   weekly,
		},
		{
			name:   "a reset more than 31 days ahead falls back to the weekly window",
			frames: copilotDeclinedWeeklyLimit(session, 40*24*60*60, "2026-10-28T01:19:06.030Z"),
			want:   weekly,
		},
		{
			name:   "a zero wait falls back to the weekly window",
			frames: copilotDeclinedWeeklyLimit(session, 0, "2026-10-28T01:19:06.030Z"),
			want:   weekly,
		},
		{
			name:   "a negative wait falls back to the weekly window",
			frames: copilotDeclinedWeeklyLimit(session, -60, "2026-10-28T01:19:06.030Z"),
			want:   weekly,
		},
		{
			name:   "a wait that is not a number falls back to the weekly window",
			frames: copilotDeclinedWeeklyLimit(session, "soon", "2026-10-28T01:19:06.030Z"),
			want:   weekly,
		},
		{
			name:   "a wait too large for a duration falls back to the weekly window",
			frames: copilotDeclinedWeeklyLimit(session, 1e300, "2026-10-28T01:19:06.030Z"),
			want:   weekly,
		},
		{
			name:   "a fractional wait schedules at its reset",
			frames: copilotDeclinedWeeklyLimit(session, 90.5, "2026-10-28T01:19:06.030Z"),
			want:   copilotUsageAt("Copilot reported a rate_limit error (user_weekly_rate_limited)", "2026-10-28T01:20:36.523Z"),
		},
		{
			name: "a completion of another request records no answer",
			frames: []string{
				copilotTurnStartFrame(session),
				copilotEventFrame(session, "", contracts.CopilotEventSessionError, "2026-10-28T01:19:06.023Z", map[string]any{"errorType": "rate_limit", "statusCode": 429, "errorCode": "user_weekly_rate_limited"}),
				copilotEventFrame(session, "", contracts.CopilotEventAutoModeSwitchRequested, "2026-10-28T01:19:06.023Z", map[string]any{"requestId": "switch", "retryAfterSeconds": 86400}),
				copilotEventFrame(session, "", contracts.CopilotEventAutoModeSwitchCompleted, "2026-10-28T01:19:06.024Z", map[string]any{"requestId": "other", "response": "yes"}),
				copilotEventFrame(session, "", contracts.CopilotEventSessionIdle, "2026-10-28T01:19:06.030Z", map[string]any{}),
			},
			want: weekly,
		},
		{
			// The stop wins over the 529 that came before it.
			name: "a stopped turn is not retryable",
			frames: []string{
				copilotTurnStartFrame(session),
				copilotEventFrame(session, "", contracts.CopilotEventSessionError, "2026-10-28T00:55:30.483Z", map[string]any{"errorType": "query", "statusCode": 529, "message": "Execution failed: 529 The model is overloaded"}),
				copilotEventFrame(session, "", contracts.CopilotEventSessionIdle, "2026-10-28T00:55:30.486Z", map[string]any{"aborted": true}),
			},
			want: copilotNotRetryable,
		},
		{
			name: "a child failure does not decide the root turn",
			frames: []string{
				copilotTurnStartFrame(session),
				copilotEventFrame(session, "child", contracts.CopilotEventModelCallFailure, "2026-10-28T01:20:52.729Z", map[string]any{"failureKind": "api"}),
				copilotEventFrame(session, "child", contracts.CopilotEventSessionError, "2026-10-28T01:20:52.731Z", brokenStream),
				copilotEventFrame(session, "child", contracts.CopilotEventAutoModeSwitchRequested, "2026-10-28T01:20:52.731Z", map[string]any{"requestId": "child-switch", "retryAfterSeconds": 60}),
				copilotEventFrame(session, "", contracts.CopilotEventSessionIdle, "2026-10-28T01:20:52.732Z", map[string]any{}),
			},
			want: copilotNotRetryable,
		},
		{
			name: "a child call failure does not stand in for the root one",
			frames: []string{
				copilotTurnStartFrame(session),
				copilotEventFrame(session, "child", contracts.CopilotEventModelCallFailure, "2026-10-28T01:20:52.729Z", map[string]any{"failureKind": "api"}),
				copilotEventFrame(session, "", contracts.CopilotEventSessionError, "2026-10-28T01:20:52.731Z", brokenStream),
				copilotEventFrame(session, "", contracts.CopilotEventSessionIdle, "2026-10-28T01:20:52.732Z", map[string]any{}),
			},
			want: copilotNotRetryable,
		},
		{
			name: "a query error without a status and without a call failure is not retryable",
			frames: []string{
				copilotTurnStartFrame(session),
				copilotEventFrame(session, "", contracts.CopilotEventSessionError, "2026-10-28T01:20:52.731Z", brokenStream),
				copilotEventFrame(session, "", contracts.CopilotEventSessionIdle, "2026-10-28T01:20:52.732Z", map[string]any{}),
			},
			want: copilotNotRetryable,
		},
		{
			name: "a query error without a status after a call failure with one is not retryable",
			frames: []string{
				copilotTurnStartFrame(session),
				copilotEventFrame(session, "", contracts.CopilotEventModelCallFailure, "2026-10-28T01:20:52.729Z", map[string]any{"failureKind": "api", "statusCode": 400}),
				copilotEventFrame(session, "", contracts.CopilotEventSessionError, "2026-10-28T01:20:52.731Z", brokenStream),
				copilotEventFrame(session, "", contracts.CopilotEventSessionIdle, "2026-10-28T01:20:52.732Z", map[string]any{}),
			},
			want: copilotNotRetryable,
		},
		{
			// The call of the first step failed and recovered. The next step starts
			// again, so its error without a status has no call failure to lean on.
			name: "a call failure that an earlier step recovered from does not count",
			frames: []string{
				copilotTurnStartFrame(session),
				copilotEventFrame(session, "", contracts.CopilotEventModelCallFailure, "2026-10-28T01:20:50.000Z", map[string]any{"failureKind": "transport"}),
				copilotTurnStartFrame(session),
				copilotEventFrame(session, "", contracts.CopilotEventSessionError, "2026-10-28T01:20:52.731Z", brokenStream),
				copilotEventFrame(session, "", contracts.CopilotEventSessionIdle, "2026-10-28T01:20:52.732Z", map[string]any{}),
			},
			want: copilotNotRetryable,
		},
		{
			name: "a status that is not an integer is neither a 5xx nor absent",
			frames: []string{
				copilotTurnStartFrame(session),
				copilotEventFrame(session, "", contracts.CopilotEventModelCallFailure, "2026-10-28T01:20:52.729Z", map[string]any{"failureKind": "transport"}),
				copilotEventFrame(session, "", contracts.CopilotEventSessionError, "2026-10-28T01:20:52.731Z", map[string]any{"errorType": "query", "statusCode": "500", "message": "Execution failed"}),
				copilotEventFrame(session, "", contracts.CopilotEventSessionIdle, "2026-10-28T01:20:52.732Z", map[string]any{}),
			},
			want: copilotNotRetryable,
		},
		{
			name: "a session.error whose data is not an object is not retryable",
			frames: []string{
				copilotTurnStartFrame(session),
				copilotEventFrame(session, "", contracts.CopilotEventSessionError, "2026-10-28T01:20:52.731Z", "The stream broke."),
				copilotEventFrame(session, "", contracts.CopilotEventSessionIdle, "2026-10-28T01:20:52.732Z", map[string]any{}),
			},
			want: copilotNotRetryable,
		},
		{
			// Constructed: the Copilot SDK lists the code, and no probe raised it.
			name: "an integration rate limit schedules a server-error retry",
			frames: []string{
				copilotTurnStartFrame(session),
				copilotEventFrame(session, "", contracts.CopilotEventSessionError, "2026-10-28T01:22:22.972Z", map[string]any{"errorType": "rate_limit", "statusCode": 429, "errorCode": contracts.CopilotErrorCodeIntegrationRateLimited, "message": "The integration reached its rate limit."}),
				copilotEventFrame(session, "", contracts.CopilotEventSessionIdle, "2026-10-28T01:22:22.975Z", map[string]any{}),
			},
			want: providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "Copilot reported a rate_limit error (integration_rate_limited)"},
		},
		{
			name: "an unknown quota code is not retryable",
			frames: []string{
				copilotTurnStartFrame(session),
				copilotEventFrame(session, "", contracts.CopilotEventSessionError, "2026-10-28T01:22:22.972Z", map[string]any{"errorType": "quota", "statusCode": 402, "errorCode": "future_quota_code", "message": "A quota applies."}),
				copilotEventFrame(session, "", contracts.CopilotEventSessionIdle, "2026-10-28T01:22:22.975Z", map[string]any{}),
			},
			want: copilotNotRetryable,
		},
		{
			// The brief's collision: a Z.AI 1302 text is an indicative rate limit,
			// so the stated deadline still decides.
			name: "a declined switch with a vendor rate-limit text schedules at the deadline",
			frames: []string{
				copilotTurnStartFrame(session),
				copilotEventFrame(session, "", contracts.CopilotEventSessionError, "2026-10-28T01:08:46.541Z", map[string]any{
					"errorType": "rate_limit", "statusCode": 429, "errorCode": "rate_limited",
					"message": `{"error":{"code":"1302","message":"Rate limit reached for requests"}}`,
				}),
				copilotEventFrame(session, "", contracts.CopilotEventAutoModeSwitchRequested, "2026-10-28T01:08:46.541Z", map[string]any{"requestId": "switch", "retryAfterSeconds": 60}),
				copilotEventFrame(session, "", contracts.CopilotEventAutoModeSwitchCompleted, "2026-10-28T01:08:47.000Z", map[string]any{"requestId": "switch", "response": "no"}),
				copilotEventFrame(session, "", contracts.CopilotEventSessionIdle, "2026-10-28T01:08:47.001Z", map[string]any{}),
			},
			want: copilotUsageAt("Copilot reported a rate_limit error (rate_limited)", "2026-10-28T01:09:46.541Z"),
		},
		{
			name: "a definitive vendor billing text wins over a 5xx",
			frames: []string{
				copilotTurnStartFrame(session),
				copilotEventFrame(session, "", contracts.CopilotEventSessionError, "2026-10-28T00:55:30.483Z", map[string]any{
					"errorType": "query", "statusCode": 500, "message": `Execution failed: 500 {"error":{"code":"insufficient_quota","message":"You exceeded your current quota"}}`,
				}),
				copilotEventFrame(session, "", contracts.CopilotEventSessionIdle, "2026-10-28T00:55:30.486Z", map[string]any{}),
			},
			want: copilotVendorVerdict(t, `Execution failed: 500 {"error":{"code":"insufficient_quota","message":"You exceeded your current quota"}}`, "2026-10-28T00:55:30.486Z", providerkit.TurnFailureBillingBlocked),
		},
		{
			name: "an indicative vendor text decides an otherwise unretryable error",
			frames: []string{
				copilotTurnStartFrame(session),
				copilotEventFrame(session, "", contracts.CopilotEventSessionError, "2026-10-28T00:55:30.483Z", map[string]any{
					"errorType": "query", "statusCode": 400, "message": `Execution failed: 400 {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
				}),
				copilotEventFrame(session, "", contracts.CopilotEventSessionIdle, "2026-10-28T00:55:30.486Z", map[string]any{}),
			},
			want: copilotVendorVerdict(t, `Execution failed: 400 {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, "2026-10-28T00:55:30.486Z", providerkit.TurnFailureTransient),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink := newCopilotRetryAgent(session)
			handleCopilotFrames(a, tc.frames...)
			assert.Equal(t, providerkittest.TurnEndThen(tc.want), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
		})
	}
}

// copilotVendorVerdict returns the vendor parser's verdict for text, after it
// checks that the parser gives the kind that the case relies on.
func copilotVendorVerdict(t *testing.T, text, observedAt string, kind providerkit.TurnFailureKind) providerkit.TurnFailure {
	t.Helper()
	verdict, ok := modelvendor.ClassifyError(text, copilotTime(observedAt))
	require.True(t, ok, "the vendor parser reads %q", text)
	require.Equal(t, kind, verdict.Failure.Kind, "the vendor parser's kind for %q", text)
	return verdict.Failure
}

// An idle that ends no turn applies no verdict, and it drops the record, so the
// next turn cannot read a failure of an earlier one.
func TestCopilotIdleWithoutATurnDropsTheFailureRecord(t *testing.T) {
	t.Parallel()
	const session = "session"
	a, sink := newCopilotRetryAgent(session)
	handleCopilotFrames(a,
		copilotEventFrame(session, "", contracts.CopilotEventModelCallFailure, "2026-10-28T00:55:30.481Z", map[string]any{"failureKind": "transport"}),
		copilotEventFrame(session, "", contracts.CopilotEventSessionError, "2026-10-28T00:55:30.483Z", map[string]any{"errorType": "query", "statusCode": 529}),
		copilotEventFrame(session, "", contracts.CopilotEventSessionIdle, "2026-10-28T00:55:30.486Z", map[string]any{}),
	)
	assert.Empty(t, providerkittest.TurnEndAndRetryOps(sink.Timeline()), "an idle that ends no turn writes no row and applies no verdict")
	assert.Equal(t, copilotTurnFailure{}, a.turnFailure)
}

// A failed turn schedules a retry, and the next turn, which succeeds, decides on
// its own records.
func TestCopilotNextTurnDoesNotReadAnEarlierFailure(t *testing.T) {
	t.Parallel()
	const session = "session"
	a, sink := newCopilotRetryAgent(session)
	handleCopilotFrames(a,
		copilotTurnStartFrame(session),
		copilotEventFrame(session, "", contracts.CopilotEventSessionError, "2026-10-28T00:55:30.483Z", map[string]any{"errorType": "query", "statusCode": 529}),
		copilotEventFrame(session, "", contracts.CopilotEventSessionIdle, "2026-10-28T00:55:30.486Z", map[string]any{}),
		copilotTurnStartFrame(session),
		copilotEventFrame(session, "", contracts.CopilotEventSessionIdle, "2026-10-28T00:56:30.486Z", map[string]any{}),
	)
	want := append(providerkittest.TurnEndThen(copilotQueryTransient), providerkittest.TurnEndThen(copilotNotRetryable)...)
	assert.Equal(t, want, providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}

// A session replacement drops the records of the outgoing turn.
func TestCopilotSessionReplacementDropsTheFailureRecord(t *testing.T) {
	t.Parallel()
	const session = "session"
	a, _ := newCopilotRetryAgent(session)
	handleCopilotFrames(a,
		copilotTurnStartFrame(session),
		copilotEventFrame(session, "", contracts.CopilotEventSessionError, "2026-10-28T01:19:06.023Z", map[string]any{"errorType": "rate_limit", "errorCode": "user_weekly_rate_limited"}),
		copilotEventFrame(session, "", contracts.CopilotEventAutoModeSwitchRequested, "2026-10-28T01:19:06.023Z", map[string]any{"requestId": "switch", "retryAfterSeconds": 86400}),
	)
	require.NotNil(t, a.turnFailure.frame)
	require.Equal(t, "switch", a.autoSwitch.requestID)
	a.sessionMu.Lock()
	a.forgetNativeSessionState("replacement")
	a.sessionMu.Unlock()
	assert.Equal(t, copilotTurnFailure{}, a.turnFailure)
	assert.Equal(t, copilotAutoSwitch{}, a.autoSwitch)
}

// An event without a timestamp reads the agent clock: the idle for the
// verdict, and the request for its reset instant.
func TestCopilotEventsWithoutATimestampReadTheAgentClock(t *testing.T) {
	t.Parallel()
	const session = "session"
	clock := testutil.NewQuartzMock(t)
	clock.Set(copilotTime("2026-10-28T01:19:06.023Z"))
	sink := &agenttest.ControlSink{}
	a := &Agent{
		copilotConnection: &copilotConnection{JSONRPCProcess: providerkit.JSONRPCProcess{
			Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{Ctx: context.Background(), Stdin: agenttest.FailingStdin{}, Clock: clock}),
		}},
		sink: agent.NewProviderServices(sink), sessionID: session,
	}
	handleCopilotFrames(a,
		copilotTurnStartFrame(session),
		copilotEventFrame(session, "", contracts.CopilotEventSessionError, "", map[string]any{"errorType": "rate_limit", "statusCode": 429, "errorCode": "user_weekly_rate_limited"}),
		copilotEventFrame(session, "", contracts.CopilotEventAutoModeSwitchRequested, "", map[string]any{"requestId": "switch", "retryAfterSeconds": 86400}),
		copilotEventFrame(session, "", contracts.CopilotEventAutoModeSwitchCompleted, "", map[string]any{"requestId": "switch", "response": "no"}),
		// A stamp of another JSON type reads as no stamp.
		`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"session","event":{"type":"session.idle","data":{},"timestamp":17}}}`,
	)
	want := copilotUsageAt("Copilot reported a rate_limit error (user_weekly_rate_limited)", "2026-10-29T01:19:06.023Z")
	assert.Equal(t, providerkittest.TurnEndThen(want), providerkittest.TurnEndAndRetryOps(sink.Timeline()))
}
