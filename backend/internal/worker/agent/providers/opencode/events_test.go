package opencode

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenCodeEventPreservesNativeBytesAndZeroTime(t *testing.T) {
	t.Parallel()
	raw := []byte(`{ "id":"evt_1", "created":0, "type":"session.execution.started", "data":{"sessionID":"ses_owner"}, "durable":{"aggregateID":"ses_owner","seq":1,"version":1}, "extra":"preserved" }`)
	event, err := parseOpenCodeEvent(raw)
	require.NoError(t, err)
	assert.Equal(t, "ses_owner", event.sessionID)
	assert.Equal(t, time.Unix(0, 0), event.createdAt())
	assert.Equal(t, string(raw), string(event.Raw))
	raw[0] = 'x'
	assert.Equal(t, byte('{'), event.Raw[0], "retained native bytes must not alias the reader buffer")
	var data struct {
		SessionID string `json:"sessionID"`
	}
	require.NoError(t, event.decodeData(&data))
	assert.Equal(t, "ses_owner", data.SessionID)
}

func TestOpenCodeEventReadsGlobalAndSessionFormOwners(t *testing.T) {
	t.Parallel()
	for _, owner := range []string{"ses_child", "global"} {
		raw := []byte(`{"id":"evt_form","created":1,"type":"form.created","data":{"form":{"id":"frm_1","sessionID":"` + owner + `"}}}`)
		event, err := parseOpenCodeEvent(raw)
		require.NoError(t, err)
		assert.Equal(t, owner, event.sessionID)
		assert.Nil(t, event.Durable)
	}
}

func TestOpenCodeEventKeepsNonSessionAggregatesSeparate(t *testing.T) {
	t.Parallel()
	event, err := parseOpenCodeEvent([]byte(`{"id":"evt_project","created":0,"type":"project.updated","data":{"projectID":"project_1"},"durable":{"aggregateID":"project_1","seq":1,"version":1}}`))
	require.NoError(t, err)
	assert.Empty(t, event.sessionID)
	assert.Equal(t, "project_1", event.Durable.AggregateID)
}

func TestOpenCodeEventRejectsMissingAndConflictingIdentity(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		`{}`,
		`{"id":"evt_1","type":"session.execution.started","data":{"sessionID":"ses_owner"}}`,
		`{"id":"evt_1","created":0,"type":"session.execution.started","data":null}`,
		`{"id":"evt_1","created":0,"type":"session.execution.started","data":[]}`,
		`{"id":"evt_1","created":0,"type":"session.execution.started","data":{}}`,
		`{"id":"evt_1","created":0,"type":"session.execution.started","data":{"sessionID":"ses_owner"},"durable":{"aggregateID":"ses_other","seq":1,"version":1}}`,
		`{"id":"evt_1","created":0,"type":"session.execution.started","data":{"sessionID":"ses_owner"},"durable":{"aggregateID":"ses_owner","seq":-1,"version":1}}`,
		`{"id":"evt_1","created":0,"type":"session.execution.started","data":{"sessionID":"ses_owner"},"durable":{"aggregateID":"ses_owner","seq":1,"version":0}}`,
		`{"id":"evt_1","created":1e100,"type":"session.execution.started","data":{"sessionID":"ses_owner"}}`,
		`{"id":"evt_1","created":0,"type":"form.created","data":{}}`,
	} {
		t.Run(raw, func(t *testing.T) { t.Parallel(); _, err := parseOpenCodeEvent([]byte(raw)); require.Error(t, err) })
	}
}

func TestOpenCodeAcceptsCapturedNativeSequenceZero(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"id":"evt_1d5e0ebaa001HO9d7b81v22W1m","created":1794589649834,"type":"session.created","location":{"directory":"/Users/trustin/Workspaces/leapmux-worktrees/nutty-colossal-disease/.tmp/task/provider-audit-tvN3fu/opencode-work"},"data":{"sessionID":"ses_e2a1f1457ffdyHfHjB8KXf6zoB","slug":"cosmic-pixel","version":"2.3.0","projectID":"85b1ea90e8397c595daf5fd7b3c4772fc32f7061","parentID":"ses_e2a1f1505ffe0n1Quly0uuLZhp","location":{"directory":"/Users/trustin/Workspaces/leapmux-worktrees/nutty-colossal-disease/.tmp/task/provider-audit-tvN3fu/opencode-work"},"subpath":".tmp/task/provider-audit-tvN3fu/opencode-work","title":"Native child probe","agent":"general","permissions":[{"action":"*","resource":"*","effect":"ask"}],"model":{"id":"glm-5.3-flash","providerID":"leapmux-e2e","variant":"default"}},"durable":{"aggregateID":"ses_e2a1f1457ffdyHfHjB8KXf6zoB","seq":0,"version":1}}`)
	event, err := parseOpenCodeEvent(raw)
	require.NoError(t, err)
	require.NotNil(t, event.Durable)
	assert.Zero(t, event.Durable.Seq)
	assert.Equal(t, raw, []byte(event.Raw))
}

func TestOpenCodeDurableSequenceMustBeExplicit(t *testing.T) {
	t.Parallel()
	for _, position := range []string{`{"aggregateID":"ses_1","version":1}`, `{"aggregateID":"ses_1","seq":null,"version":1}`} {
		raw := map[string]any{"id": "evt_1", "type": "session.execution.started", "created": 1794600000000, "data": map[string]any{"sessionID": "ses_1"}, "durable": json.RawMessage(position)}
		data, err := json.Marshal(raw)
		require.NoError(t, err)
		_, err = parseOpenCodeEvent(data)
		assert.Error(t, err)
	}
}

func openCodeSessionEventForTest(t *testing.T, kind, data string) openCodeEvent {
	t.Helper()
	event, err := parseOpenCodeEvent([]byte(`{"id":"evt_1","created":1,"type":"session.` + kind + `","data":{"sessionID":"ses_1",` + data + `}}`))
	require.NoError(t, err)
	return event
}

func TestOpenCodeTextEndWaitsForItsStepSettlement(t *testing.T) {
	t.Parallel()
	for _, completion := range []agent.MessageCompletion{agent.MessageCompletionComplete, agent.MessageCompletionInterrupted} {
		t.Run(string(completion), func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			sink.UpdateSessionID("ses_1")
			streams := newOpenCodeTextStreams("ses_1", sink)
			_, err := streams.observe(openCodeSessionEventForTest(t, "text.ended", `"assistantMessageID":"msg_1","ordinal":0,"text":"partial"`))
			require.NoError(t, err)
			assert.Empty(t, sink.Messages(), "native cancellation also flushes text.ended")
			require.NoError(t, streams.finish(completion))
			rows := sink.Messages()
			require.Len(t, rows, 1)
			assert.JSONEq(t, fmt.Sprintf(`{"type":"assembled_message","kind":"text","text":"partial","completion":%q}`, completion), string(rows[0].Content))
		})
	}
}

func TestOpenCodeTextDropsTheFailedAttemptWhenNativeRetryStarts(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	sink.UpdateSessionID("ses_1")
	streams := newOpenCodeTextStreams("ses_1", sink)
	for _, event := range []openCodeEvent{
		openCodeSessionEventForTest(t, "text.ended", `"assistantMessageID":"msg_failed","ordinal":0,"text":"partial failed attempt"`),
		openCodeSessionEventForTest(t, "step.failed", `"assistantMessageID":"msg_failed","error":{"type":"api","message":"Service unavailable"}`),
		openCodeSessionEventForTest(t, "retry.scheduled", `"assistantMessageID":"msg_failed","attempt":1,"at":0`),
		openCodeSessionEventForTest(t, "text.ended", `"assistantMessageID":"msg_good","ordinal":0,"text":"full answer"`),
		openCodeSessionEventForTest(t, "step.ended", `"assistantMessageID":"msg_good"`),
	} {
		_, err := streams.observe(event)
		require.NoError(t, err)
	}
	require.NoError(t, streams.finish(agent.MessageCompletionComplete))
	rows := sink.Messages()
	require.Len(t, rows, 1)
	assert.Contains(t, string(rows[0].Content), `"text":"full answer"`)
}

func TestOpenCodeTextZeroOrdinalAndFullNativeEnding(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	sink.UpdateSessionID("ses_1")
	streams := newOpenCodeTextStreams("ses_1", sink)
	for _, event := range []openCodeEvent{
		openCodeSessionEventForTest(t, "text.started", `"assistantMessageID":"msg_1","ordinal":0`),
		openCodeSessionEventForTest(t, "text.delta", `"assistantMessageID":"msg_1","ordinal":0,"delta":"incomplete"`),
		openCodeSessionEventForTest(t, "text.ended", `"assistantMessageID":"msg_1","ordinal":0,"text":"full native value"`),
	} {
		accepted, err := streams.observe(event)
		require.True(t, accepted)
		require.NoError(t, err)
	}
	_, err := streams.observe(openCodeSessionEventForTest(t, "step.ended", `"assistantMessageID":"msg_1"`))
	require.NoError(t, err)
	require.NoError(t, streams.finish(agent.MessageCompletionInterrupted))
	rows := sink.Messages()
	require.Len(t, rows, 1)
	assert.JSONEq(t, `{"type":"assembled_message","kind":"text","text":"full native value","completion":"complete"}`, string(rows[0].Content))
	restarted := newOpenCodeTextStreams("ses_1", sink)
	require.NoError(t, restarted.recover(storedAssistant(t, `{"id":"msg_1","type":"assistant","content":[{"type":"text","text":"full native value"}],"time":{"created":0,"completed":1}}`)))
	assert.Len(t, sink.Messages(), 1, "recovery after a worker restart must reuse the same native key")
}

func TestOpenCodeTextRecoveryIgnoresBufferedDeltasForCompletedSnapshots(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	sink.UpdateSessionID("ses_1")
	streams := newOpenCodeTextStreams("ses_1", sink)
	_, err := streams.observe(openCodeSessionEventForTest(t, "text.delta", `"assistantMessageID":"msg_1","ordinal":0,"delta":"hel"`))
	require.NoError(t, err)
	require.NoError(t, streams.recover(storedAssistant(t, `{"id":"msg_1","type":"assistant","content":[{"type":"text","text":"hello world"}],"time":{"created":0,"completed":1}}`)))
	_, err = streams.observe(openCodeSessionEventForTest(t, "text.delta", `"assistantMessageID":"msg_1","ordinal":0,"delta":"lo"`))
	require.NoError(t, err)
	_, err = streams.observe(openCodeSessionEventForTest(t, "text.ended", `"assistantMessageID":"msg_1","ordinal":0,"text":"hello world"`))
	require.NoError(t, err)
	require.NoError(t, streams.recover(storedAssistant(t, `{"id":"msg_1","type":"assistant","content":[{"type":"text","text":"hello world"}],"time":{"created":0,"completed":0}}`)))
	rows := sink.Messages()
	require.Len(t, rows, 1)
	assert.Contains(t, string(rows[0].Content), `"text":"hello world"`)
}

func TestOpenCodeTextPersistenceFailureRetainsOriginalCompletion(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{PersistErr: assert.AnError}
	sink.UpdateSessionID("ses_1")
	streams := newOpenCodeTextStreams("ses_1", sink)
	_, err := streams.observe(openCodeSessionEventForTest(t, "text.ended", `"assistantMessageID":"msg_1","ordinal":0,"text":"kept"`))
	require.NoError(t, err)
	_, err = streams.observe(openCodeSessionEventForTest(t, "step.ended", `"assistantMessageID":"msg_1"`))
	require.ErrorIs(t, err, assert.AnError)
	sink.PersistErr = nil
	require.NoError(t, streams.finish(agent.MessageCompletionInterrupted))
	rows := sink.Messages()
	require.Len(t, rows, 2)
	assert.Equal(t, string(rows[0].Content), string(rows[1].Content))
	assert.Contains(t, string(rows[1].Content), `"completion":"complete"`)
}

func TestOpenCodeTextRejectsMalformedIdentityBeforeCreatingSegments(t *testing.T) {
	t.Parallel()
	for _, data := range []string{`"ordinal":0,"delta":"x"`, `"assistantMessageID":"msg_1","delta":"x"`, `"assistantMessageID":"msg_1","ordinal":null,"delta":"x"`, `"assistantMessageID":"msg_1","ordinal":-1,"delta":"x"`, `"assistantMessageID":"msg_1","ordinal":0`, `"assistantMessageID":"msg_1","ordinal":0,"delta":false`} {
		t.Run(data, func(t *testing.T) {
			t.Parallel()
			streams := newOpenCodeTextStreams("ses_1", &agenttest.Sink{})
			_, err := streams.observe(openCodeSessionEventForTest(t, "text.delta", data))
			require.Error(t, err)
			assert.Empty(t, streams.segments)
		})
	}
}

func TestOpenCodeTextEmptyAndInterruptedScopes(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	sink.UpdateSessionID("ses_1")
	streams := newOpenCodeTextStreams("ses_1", sink)
	_, err := streams.observe(openCodeSessionEventForTest(t, "text.ended", `"assistantMessageID":"msg_empty","ordinal":0,"text":""`))
	require.NoError(t, err)
	_, err = streams.observe(openCodeSessionEventForTest(t, "reasoning.delta", `"assistantMessageID":"msg_1","ordinal":0,"delta":"partial"`))
	require.NoError(t, err)
	require.NoError(t, streams.finish(agent.MessageCompletionInterrupted))
	require.NoError(t, streams.finish(agent.MessageCompletionComplete))
	rows := sink.Messages()
	require.Len(t, rows, 1)
	assert.Contains(t, string(rows[0].Content), `"completion":"interrupted"`)
	_, err = streams.observe(openCodeSessionEventForTest(t, "text.delta", `"assistantMessageID":"msg_1","ordinal":0,"delta":"independent text"`))
	assert.NoError(t, err)
}

func TestOpenCodeTextRejectsForeignSessionWithoutChangingOutput(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	streams := newOpenCodeTextStreams("ses_other", sink)
	accepted, err := streams.observe(openCodeSessionEventForTest(t, "text.delta", `"assistantMessageID":"msg_1","ordinal":0,"delta":"foreign"`))
	require.True(t, accepted)
	require.Error(t, err)
	assert.Empty(t, streams.segments)
	assert.Empty(t, sink.Messages())
}

func TestOpenCodeTextRecoveryPreservesAnUnfinishedFinalSegment(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	sink.UpdateSessionID("ses_1")
	streams := newOpenCodeTextStreams("ses_1", sink)
	require.NoError(t, streams.recover(storedAssistant(t, `{"id":"msg_1","type":"assistant","content":[{"type":"text","text":"before tool"},{"type":"tool","id":"call"},{"type":"text","text":""}],"time":{"created":0}}`)))
	rows := sink.Messages()
	require.Len(t, rows, 1)
	assert.Contains(t, string(rows[0].Content), `"text":"before tool"`)
	_, err := streams.observe(openCodeSessionEventForTest(t, "text.delta", `"assistantMessageID":"msg_1","ordinal":1,"delta":"unfinished"`))
	require.NoError(t, err)
	require.NoError(t, streams.finish(agent.MessageCompletionInterrupted))
	rows = sink.Messages()
	require.Len(t, rows, 2)
	assert.Contains(t, string(rows[1].Content), `"text":"unfinished"`)
	assert.Contains(t, string(rows[1].Content), `"completion":"interrupted"`)
}

func TestOpenCodeTextKindIdentitySurvivesLiveAndSnapshotRecovery(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	sink.UpdateSessionID("ses_1")
	streams := newOpenCodeTextStreams("ses_1", sink)
	for _, part := range []struct {
		kind, text string
		ordinal    int
	}{
		{"reasoning", "first thought", 0}, {"text", "first answer", 0},
		{"reasoning", "second thought", 1}, {"text", "second answer", 1},
	} {
		_, err := streams.observe(openCodeSessionEventForTest(t, part.kind+".ended", fmt.Sprintf(`"assistantMessageID":"msg_1","ordinal":%d,"text":%q`, part.ordinal, part.text)))
		require.NoError(t, err)
	}
	_, err := streams.observe(openCodeSessionEventForTest(t, "step.ended", `"assistantMessageID":"msg_1"`))
	require.NoError(t, err)
	require.Len(t, sink.Messages(), 4)
	message := storedAssistant(t, `{"id":"msg_1","type":"assistant","content":[{"type":"reasoning","text":"first thought"},{"type":"tool","id":"call_1"},{"type":"text","text":"first answer"},{"type":"reasoning","text":"second thought"},{"type":"tool","id":"call_2"},{"type":"text","text":"second answer"}],"time":{"completed":1}}`)
	require.NoError(t, streams.recover(message))
	assert.Len(t, sink.Messages(), 4)
	replacement := newOpenCodeTextStreams("ses_1", sink)
	require.NoError(t, replacement.recover(message))
	assert.Len(t, sink.Messages(), 4)
}

func TestOpenCodeTextToolBeforeTextReusesLiveKey(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	sink.UpdateSessionID("ses_1")
	streams := newOpenCodeTextStreams("ses_1", sink)
	_, err := streams.observe(openCodeSessionEventForTest(t, "text.ended", `"assistantMessageID":"msg_1","ordinal":0,"text":"answer"`))
	require.NoError(t, err)
	message := storedAssistant(t, `{"id":"msg_1","type":"assistant","content":[{"type":"tool","id":"call_1"},{"type":"text","text":"answer"}],"time":{"completed":1}}`)
	require.NoError(t, streams.recover(message))
	require.NoError(t, newOpenCodeTextStreams("ses_1", sink).recover(message))
	assert.Len(t, sink.Messages(), 1)
}

func TestOpenCodeTextUnfinishedProjectionPreservesKnownDeltas(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"text", "reasoning"} {
		for _, ended := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/ended=%t", kind, ended), func(t *testing.T) {
				t.Parallel()
				sink := &agenttest.Sink{}
				sink.UpdateSessionID("ses_1")
				streams := newOpenCodeTextStreams("ses_1", sink)
				_, err := streams.observe(openCodeSessionEventForTest(t, kind+".delta", `"assistantMessageID":"msg_1","ordinal":0,"delta":"before"`))
				require.NoError(t, err)
				require.NoError(t, streams.recover(storedAssistant(t, fmt.Sprintf(`{"id":"msg_1","type":"assistant","content":[{"type":%q,"text":""}],"time":{"created":0}}`, kind))))
				_, err = streams.observe(openCodeSessionEventForTest(t, kind+".delta", `"assistantMessageID":"msg_1","ordinal":0,"delta":"after"`))
				require.NoError(t, err)
				expected, completion := "beforeafter", "interrupted"
				if ended {
					_, err = streams.observe(openCodeSessionEventForTest(t, kind+".ended", `"assistantMessageID":"msg_1","ordinal":0,"text":"authoritative answer"`))
					require.NoError(t, err)
					_, err = streams.observe(openCodeSessionEventForTest(t, "step.ended", `"assistantMessageID":"msg_1"`))
					require.NoError(t, err)
					expected, completion = "authoritative answer", "complete"
				}
				require.NoError(t, streams.finish(agent.MessageCompletionInterrupted))
				rows := sink.Messages()
				require.Len(t, rows, 1)
				assert.JSONEq(t, fmt.Sprintf(`{"type":"assembled_message","kind":%q,"text":%q,"completion":%q}`, kind, expected, completion), string(rows[0].Content))
			})
		}
	}
}

func TestOpenCodeTextLaterToolDoesNotFinishOpenReasoning(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	sink.UpdateSessionID("ses_1")
	streams := newOpenCodeTextStreams("ses_1", sink)
	_, err := streams.observe(openCodeSessionEventForTest(t, "reasoning.delta", `"assistantMessageID":"msg_1","ordinal":0,"delta":"partial"`))
	require.NoError(t, err)
	require.NoError(t, streams.recover(storedAssistant(t, `{"id":"msg_1","type":"assistant","content":[{"type":"reasoning","text":"","time":{"created":0}},{"type":"tool","id":"call_1"}],"time":{"created":0}}`)))
	assert.Empty(t, sink.Messages())
	_, err = streams.observe(openCodeSessionEventForTest(t, "reasoning.ended", `"assistantMessageID":"msg_1","ordinal":0,"text":"complete reasoning"`))
	require.NoError(t, err)
	_, err = streams.observe(openCodeSessionEventForTest(t, "step.ended", `"assistantMessageID":"msg_1"`))
	require.NoError(t, err)
	rows := sink.Messages()
	require.Len(t, rows, 1)
	assert.Contains(t, string(rows[0].Content), `"text":"complete reasoning"`)
	assert.Contains(t, string(rows[0].Content), `"completion":"complete"`)
}
