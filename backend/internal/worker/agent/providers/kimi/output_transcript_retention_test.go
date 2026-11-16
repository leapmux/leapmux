package kimi

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKimiNativeRetryPreservesReasoningBeforeTheAnswer(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.transcriptOps(t, 1,
		`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"thinking","frameId":"thought","text":"Consider the failure paths."}}`,
		`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"text","frameId":"answer","role":"assistant","text":"Check the error path."}}`)
	rig.transcriptOps(t, 2, kimiNativeRetryStep)
	messages := rig.sink.Messages()
	require.Len(t, messages, 2)
	assert.Contains(t, string(messages[0].Content), "Consider the failure paths.")
	assert.Contains(t, string(messages[1].Content), "Check the error path.")
}

func TestKimiNativeRetryPartialToolSnapshotKeepsTheRetryPending(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.transcriptOps(t, 1, kimiNativeRetryStep)
	rig.transcriptReset(t, 2, `{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"user"},"steps":[{"kind":"step","stepId":"t0.1","ordinal":1,"state":"running","retry":{"failedAttempt":1,"nextAttempt":2,"maxAttempts":10,"delayMs":545.13},"frames":[{"kind":"tool","frameId":"call-1","toolCallId":"call-1","name":"","state":"running","inputText":"{"}]}]}`)
	assert.True(t, rig.agent.run("main").retry.Pending())
	assert.Empty(t, rig.sink.Notifications())
	rig.transcriptOps(t, 3, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"call-1","toolCallId":"call-1","name":"","state":"running","inputText":"{\"path\":"}}`)
	assert.False(t, rig.agent.run("main").retry.Pending())
	assert.Len(t, rig.sink.Notifications(), 1)
	assert.Empty(t, rig.sink.Messages())
}

func TestKimiNativeRecordUsesTheDecodedSequence(t *testing.T) {
	t.Parallel()
	seq := int64(0)
	source := kimiFrame{Type: contracts.KimiEventTranscriptReset, SessionID: "session_1", transcriptSeq: &seq}
	// Encoding one operation needs no access to the full reset payload.
	source.Payload = json.RawMessage(`not retained by the operation encoder`)
	record, err := kimiTranscriptRecord(source, "main", kimiTranscriptOperation{
		Op:   contracts.KimiTranscriptOperationMarkerUpsert,
		Item: json.RawMessage(`{"kind":"marker","markerId":"m1","marker":"notice","payload":{"message":"hello"}}`),
	})
	require.NoError(t, err)
	var decoded struct {
		Payload struct {
			Seq *int64 `json:"seq"`
		} `json:"payload"`
	}
	require.NoError(t, json.Unmarshal(record, &decoded))
	require.NotNil(t, decoded.Payload.Seq)
	assert.Zero(t, *decoded.Payload.Seq)
}

func TestKimiNativeFinishedToolReleasesPersistedPayload(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.transcriptOps(t, 1, kimiNativeBashOpening)
	rig.transcriptOps(t, 2, nativeKimiProgressOperation("stdout", "partial"))
	rig.transcriptOps(t, 3, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"t0.1.call","toolCallId":"call","name":"Bash","state":"done","input":{},"output":"complete result"}}`)
	tool := rig.agent.run("main").transcript.turns["t0"].steps["t0.1"].tools["t0.1.call"]
	require.True(t, tool.finished)
	assert.Empty(t, tool.call.lastFrame)
	assert.Empty(t, tool.call.tail)
	assert.NotEmpty(t, tool.call.spanID, "late child linking still needs the span identity")
}

func TestKimiNativeEndedTurnReleasesOutputButKeepsToolIdentity(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.transcriptOps(t, 1, kimiNativeBashOpening,
		`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"text","frameId":"answer","role":"assistant","text":"The command is running."}}`)
	rig.transcriptOps(t, 2, nativeKimiProgressOperation("stdout", "partial"))
	rig.transcriptOps(t, 3, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"cancelled","origin":{"kind":"user"}}}`)
	turn := rig.agent.run("main").transcript.turns["t0"]
	require.True(t, turn.finished)
	step := turn.steps["t0.1"]
	assert.Empty(t, step.frames)
	assert.Empty(t, step.order)
	tool := step.tools["t0.1.call"]
	assert.Empty(t, tool.call.lastFrame)
	assert.Empty(t, tool.call.tail)
	assert.NotEmpty(t, tool.call.spanID)
}
