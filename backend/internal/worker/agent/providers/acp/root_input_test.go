package acp

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type acpInputProofRecorder struct {
	writer agent.TranscriptServices
	blocks []json.RawMessage
}

func (*acpInputProofRecorder) Prepare() error { return nil }
func (recorder *acpInputProofRecorder) PrepareInput(blocks []json.RawMessage) error {
	for _, block := range blocks {
		recorder.blocks = append(recorder.blocks, slices.Clone(block))
		block[0] = 'x'
	}
	return nil
}
func (recorder *acpInputProofRecorder) Reserve() RootTranscriptCapture {
	return acpInputProofPosition{writer: recorder.writer}
}
func (*acpInputProofRecorder) Finish(RootTranscriptCompletion) error { return nil }

type acpInputProofPosition struct{ writer agent.TranscriptServices }

func (position acpInputProofPosition) Writer() agent.TranscriptServices { return position.writer }
func (acpInputProofPosition) Complete()                                 {}

func TestRootInputProofUsesTheFinalSerializedPromptParams(t *testing.T) {
	out := &agenttest.Stdin{}
	b, _ := newACPTurnBase(t, out)
	recorder := &acpInputProofRecorder{writer: b.sink}
	b.hooks.OpenRootTranscript = func(RootTranscriptStart, agent.ProviderServices) RootTranscript { return recorder }
	b.hooks.PreparePrompt = func(_ string, _ []agent.ClassifiedAttachment, blocks []map[string]any) error {
		blocks[0]["text"] = "the prepared value"
		return nil
	}
	b.hooks.PromptParams = func(fields map[string]any) {
		fields["prompt"] = []map[string]any{{"type": "text", "text": "the final packet"}}
	}
	require.NoError(t, b.SendInput("the original value", nil))
	require.Len(t, recorder.blocks, 1)
	assert.JSONEq(t, `{"type":"text","text":"the final packet"}`, string(recorder.blocks[0]))
	var request struct {
		Params struct {
			Prompt []json.RawMessage `json:"prompt"`
		} `json:"params"`
	}
	require.NoError(t, json.Unmarshal([]byte(out.String()), &request))
	require.Len(t, request.Params.Prompt, 1)
	assert.JSONEq(t, `{"type":"text","text":"the final packet"}`, string(request.Params.Prompt[0]), "the provider's proof hook cannot mutate the dispatched packet")
}

func TestRootInputProofRejectsAForeignSessionBeforeWriting(t *testing.T) {
	out := &agenttest.Stdin{}
	b, _ := newACPTurnBase(t, out)
	recorder := &acpInputProofRecorder{writer: b.sink}
	b.hooks.OpenRootTranscript = func(RootTranscriptStart, agent.ProviderServices) RootTranscript { return recorder }
	b.hooks.PromptParams = func(fields map[string]any) { fields["sessionId"] = "foreign-session" }
	require.ErrorContains(t, b.SendInput("the actual value", nil), "another session")
	assert.Empty(t, out.String())
	assert.Empty(t, recorder.blocks)
}
