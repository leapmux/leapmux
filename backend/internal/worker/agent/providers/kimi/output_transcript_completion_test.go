package kimi

import (
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKimiNativeFailureNoticeSettlesAutomaticRetry(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		retryable bool
		message   string
		schedules int
	}{
		{"retryable failure", true, "model busy", 1},
		{"permanent failure", false, "model busy", 0},
		{"different failure", true, "unrelated error", 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiOutputRig(t)
			rig.transcriptOps(t, 1, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"user"}}}`)
			rig.transcriptOps(t, 2, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"failed","origin":{"kind":"user"},"error":"model busy"}}`)
			notice := fmt.Sprintf(`{"op":"marker.upsert","item":{"kind":"marker","markerId":"live-m1","marker":"notice","payload":{"level":"error","message":%q,"event":{"agentId":"main","message":%q,"code":"overloaded","retryable":%t}},"at":"2026-11-15T03:00:00Z"}}`, test.message, test.message, test.retryable)
			rig.transcriptOps(t, 3, notice)
			assert.Equal(t, test.schedules, rig.sink.AutoScheduleCount())
			assert.Positive(t, rig.sink.AutoCancelCount(), "a native completion settles an older pending retry")
			if test.schedules > 0 {
				require.Positive(t, rig.sink.AutoScheduleCount())
				scheduled := rig.sink.LastAutoSchedule()
				assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR, scheduled.Kind)
				assert.Contains(t, string(scheduled.SourcePayload), `"marker":"notice"`)
			}
			rig.transcriptOps(t, 4, notice)
			assert.Equal(t, test.schedules, rig.sink.AutoScheduleCount(), "a repeated native notice must not schedule another retry")
			if test.message == "model busy" {
				assert.Empty(t, rig.sink.PersistedNotifications(), "the failed turn already contains the error message")
			} else {
				assert.Len(t, rig.sink.PersistedNotifications(), 1, "an unrelated error remains a standalone notice")
			}
		})
	}
}

func TestKimiNativeCompletionCancelsRetryAndRejectsAnOldFailureNotice(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"completed", "cancelled"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			rig := newKimiOutputRig(t)
			rig.transcriptOps(t, 1, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"failed","origin":{"kind":"user"},"error":"model busy"}}`)
			rig.transcriptOps(t, 2, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t1","state":"running","origin":{"kind":"user"}}}`)
			rig.transcriptOps(t, 3, fmt.Sprintf(`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t1","state":%q,"origin":{"kind":"user"}}}`, state))
			rig.transcriptOps(t, 4, `{"op":"marker.upsert","item":{"kind":"marker","markerId":"live-m1","marker":"notice","payload":{"level":"error","message":"model busy","event":{"agentId":"main","message":"model busy","retryable":true}}}}`)
			assert.Zero(t, rig.sink.AutoScheduleCount())
			assert.Positive(t, rig.sink.AutoCancelCount())
		})
	}
}

func TestKimiNativeChildFailureDoesNotScheduleTheMainRetry(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.transcriptOps(t, 1, `{"op":"frame.upsert","turnId":"parent","stepId":"parent.1","frame":{"kind":"tool","frameId":"spawn","toolCallId":"spawn","name":"Agent","state":"running","input":{},"agentRefs":[{"agentId":"agent-0","role":"child"}]}}`)
	rig.transcriptAgentOps(t, "agent-0", 1,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"failed","origin":{"kind":"other"},"error":"model busy"}}`,
		`{"op":"marker.upsert","item":{"kind":"marker","markerId":"live-m1","marker":"notice","payload":{"level":"error","message":"model busy","event":{"agentId":"agent-0","message":"model busy","retryable":true}}}}`)
	assert.Zero(t, rig.sink.AutoScheduleCount())
}

func TestKimiNativeFailureNoticeDistinguishesAnEmptyErrorFromNoError(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		errorField string
		code       string
		schedules  int
		notices    int
	}{
		{"empty error", `,"error":""`, "provider.overloaded", 1, 1},
		{"absent error", "", "provider.overloaded", 0, 1},
		{"null error", `,"error":null`, "provider.overloaded", 0, 1},
		{"empty diagnostic", `,"error":""`, "", 1, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiOutputRig(t)
			rig.transcriptOps(t, 1, fmt.Sprintf(`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"failed","origin":{"kind":"user"}%s}}`, test.errorField))
			notice := fmt.Sprintf(`{"op":"marker.upsert","item":{"kind":"marker","markerId":"live-m1","marker":"notice","payload":{"level":"error","message":"","event":{"agentId":"main","message":"","code":%q,"retryable":true}}}}`, test.code)
			rig.transcriptOps(t, 2, notice)
			rig.transcriptOps(t, 3, notice)
			assert.Equal(t, test.schedules, rig.sink.AutoScheduleCount())
			assert.Equal(t, test.notices, len(rig.sink.PersistedNotifications()))
		})
	}
}

func TestKimiNativeTurnEndPersistsContextUsageIncludingZero(t *testing.T) {
	t.Parallel()
	for _, tokens := range []int{0, 1200} {
		t.Run(fmt.Sprint(tokens), func(t *testing.T) {
			t.Parallel()
			rig := newKimiOutputRig(t)
			rig.agent.HandleOutput([]byte(fmt.Sprintf(`{"type":"usage","session_id":"session_1","agent_id":"main","context_tokens":%d,"max_context_tokens":262144}`, tokens)))
			rig.transcriptOps(t, 1, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"completed","origin":{"kind":"user"}}}`)
			messages := rig.sink.Messages()
			require.Len(t, messages, 1)
			usage, present := metadataField(t, messages[0], contracts.SessionInfoKeyContextUsage)
			require.True(t, present)
			assert.JSONEq(t, fmt.Sprintf(`{"context_tokens":%d,"context_window":262144,"input_tokens":0,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}`, tokens), string(usage))
			count, present := metadataField(t, messages[0], contracts.MessageMetadataFieldToolUses)
			require.True(t, present)
			assert.JSONEq(t, `0`, string(count))
		})
	}
}

func TestKimiNativeFailureNoticeSurvivesResetWithoutRepeatingRetry(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	failure := `{"kind":"turn","turnId":"t0","state":"failed","origin":{"kind":"user"},"error":"model busy"}`
	notice := `{"kind":"marker","markerId":"live-m1","marker":"notice","payload":{"level":"error","message":"model busy","event":{"agentId":"main","message":"model busy","retryable":true}},"at":"2026-11-15T03:00:00Z"}`
	rig.transcriptOps(t, 1, `{"op":"turn.upsert","turn":`+failure+`}`)
	rig.transcriptReset(t, 2, failure)
	rig.transcriptOps(t, 3, `{"op":"marker.upsert","item":`+notice+`}`)
	require.Equal(t, 1, rig.sink.AutoScheduleCount())
	rig.transcriptReset(t, 4, failure, notice)
	rig.transcriptOps(t, 5, `{"op":"marker.upsert","item":`+notice+`}`)
	assert.Equal(t, 1, rig.sink.AutoScheduleCount())
	assert.Empty(t, rig.sink.PersistedNotifications())
}
