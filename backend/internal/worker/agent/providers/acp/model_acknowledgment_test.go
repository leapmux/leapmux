package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelAcknowledgmentPrecedesAudioPreparation(t *testing.T) {
	t.Parallel()
	var nativeModelChanged atomic.Bool
	b, requests := acptest.NewAgentForRPCWithRequestResponder(t, func() *Base { return &Base{} }, func(b *Base) *Base { return b }, func(request agenttest.RecordedRequest) agenttest.RPCReply {
		if request.Method == MethodSessionSetConfigOption {
			if request.Params["configId"] == ConfigOptionIDModel {
				nativeModelChanged.Store(true)
				return agenttest.RPCReply{Result: json.RawMessage(`{"configOptions":[{"id":"thinking_effort","category":"thought_level","type":"select","name":"Effort","currentValue":"none","options":[{"value":"none","name":"None"},{"value":"high","name":"High"}]}]}`)}
			}
			return agenttest.RPCReply{Result: json.RawMessage(`{"configOptions":[{"id":"thinking_effort","category":"thought_level","type":"select","name":"Effort","currentValue":"high","options":[{"value":"none","name":"None"},{"value":"high","name":"High"}]}]}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{"stopReason":"end_turn"}`)}
	})
	b.SetSinkForTest(agent.NewProviderServices(&agenttest.Sink{}))
	b.SetCurrentModel("audio")
	b.hooks.EffortConfigID = "thinking_effort"
	b.hooks.PreparePrompt = func(_ string, _ []agent.ClassifiedAttachment, _ []map[string]any) error {
		if b.CurrentModel() != "audio" {
			return fmt.Errorf("the selected model does not accept audio attachments")
		}
		return nil
	}
	b.optionWriteMu.Lock()
	locked := true
	defer func() {
		if locked {
			b.optionWriteMu.Unlock()
		}
	}()
	modelDone := make(chan error, 1)
	go func() { modelDone <- b.setModel("text") }()
	testutil.RequireEventually(t, func() bool {
		b.Mu.Lock()
		defer b.Mu.Unlock()
		return b.options.values["thinking_effort"] == "none"
	})
	assert.True(t, nativeModelChanged.Load())
	inputDone := make(chan error, 1)
	go func() {
		inputDone <- b.SendInput("listen", []*leapmuxv1.Attachment{{Filename: "voice.wav", MimeType: "audio/wav", Data: []byte{0, 255, 1}}})
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	inputJoined := false
	select {
	case err := <-inputDone:
		inputJoined = true
		assert.ErrorContains(t, err, "does not accept audio attachments")
	case <-ctx.Done():
		t.Error("audio preparation could not finish while the effort write waited")
	}
	b.optionWriteMu.Unlock()
	locked = false
	require.NoError(t, <-modelDone)
	if !inputJoined {
		<-inputDone
	}
	_, err := b.SendRequest("test/sync", nil, b.APITimeout())
	require.NoError(t, err)
	for _, request := range requests() {
		assert.NotEqual(t, MethodSessionPrompt, request.Method, "the native text model must never receive the recording")
	}
	assert.Equal(t, "text", b.CurrentModel())
}

func TestModelAcknowledgmentStoresTheCanonicalIDAndPreservesFailures(t *testing.T) {
	t.Parallel()
	for name, reply := range map[string]agenttest.RPCReply{
		"accepted":   {Result: json.RawMessage(`{}`)},
		"refused":    {Error: json.RawMessage(`{"code":-32602,"message":"unsupported model"}`)},
		"non-object": {Result: json.RawMessage(`[]`)},
		"null":       {Result: json.RawMessage(`null`)},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			b, requests := acptest.NewAgentForRPCWithResponder(t, func() *Base { return &Base{} }, func(b *Base) *Base { return b }, func(string) agenttest.RPCReply { return reply })
			b.SetCurrentModel("previous")
			err := b.SetModelViaConfigOption("default", "canonical-model")
			if name == "accepted" {
				require.NoError(t, err)
				assert.Equal(t, "canonical-model", b.CurrentModel())
			} else {
				require.Error(t, err)
				assert.Equal(t, "previous", b.CurrentModel())
			}
			require.Len(t, requests(), 1)
			assert.Equal(t, "default", requests()[0].Params["value"])
		})
	}
}
