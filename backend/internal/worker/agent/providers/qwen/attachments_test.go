package qwen

import (
	"encoding/json"
	"sync/atomic"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const audioModelStatus = `{"initialized":true,"current":{"modelId":"voice(openai)"},"providers":[{"kind":"model_provider","status":"ok","models":[{"modelId":"voice(openai)","modalities":{"audio":true}}]}]}`

func TestQwenAudioPolicy(t *testing.T) {
	t.Parallel()
	registry := agenttest.MustNewRegistry(Registration())
	for _, mime := range []string{"audio/wav", "audio/x-wav", "audio/wave", "audio/vnd.wave", "audio/mpeg", "audio/mp3", "audio/mp4", "audio/m4a", "audio/x-m4a", "audio/ogg", "audio/flac", "audio/x-flac"} {
		_, err := registry.NormalizeAttachments(leapmuxv1.AgentProvider_AGENT_PROVIDER_QWEN_CODE, []*leapmuxv1.Attachment{{Filename: "voice", MimeType: mime, Data: []byte{0, 255, 1}}})
		require.NoError(t, err, mime)
	}
	for _, file := range []*leapmuxv1.Attachment{
		{Filename: "empty.wav", MimeType: "audio/wav"},
		{Filename: "voice.webm", MimeType: "audio/webm", Data: []byte{1}},
		{Filename: "voice.aac", MimeType: "audio/aac", Data: []byte{1}},
		{Filename: "binary", MimeType: "application/octet-stream", Data: []byte{0, 255}},
	} {
		_, err := registry.NormalizeAttachments(leapmuxv1.AgentProvider_AGENT_PROVIDER_QWEN_CODE, []*leapmuxv1.Attachment{file})
		require.Error(t, err, file.Filename)
	}
}

func TestQwenAudioPromptUsesTheNativeModelCapability(t *testing.T) {
	t.Parallel()
	for name, status := range map[string]string{
		"audio model":      audioModelStatus,
		"missing":          `{}`,
		"malformed":        `{"initialized":true,"providers":"bad"}`,
		"not initialized":  `{"initialized":false,"current":{"modelId":"voice"}}`,
		"no modality":      `{"initialized":true,"current":{"modelId":"voice"},"providers":[{"kind":"model_provider","status":"ok","models":[{"modelId":"voice"}]}]}`,
		"other model":      `{"initialized":true,"current":{"modelId":"text"},"providers":[{"kind":"model_provider","status":"ok","models":[{"modelId":"voice","modalities":{"audio":true}}]}]}`,
		"duplicate model":  `{"initialized":true,"current":{"modelId":"voice"},"providers":[{"kind":"model_provider","status":"ok","models":[{"modelId":"voice","modalities":{"audio":true}},{"modelId":"voice","modalities":{"audio":true}}]}]}`,
		"failed provider":  `{"initialized":true,"current":{"modelId":"voice"},"providers":[{"kind":"model_provider","status":"error","models":[{"modelId":"voice","modalities":{"audio":true}}]}]}`,
		"invalid modality": `{"initialized":true,"current":{"modelId":"voice"},"providers":[{"kind":"model_provider","status":"ok","models":[{"modelId":"voice","modalities":{"audio":"true"}}]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, _, requests := newQwenAgent(t, nil, func(request agenttest.RecordedRequest) agenttest.RPCReply {
				if request.Method == "qwen/status/workspace/providers" {
					assert.Equal(t, map[string]any{"sessionId": "session-1"}, request.Params)
					return agenttest.RPCReply{Result: json.RawMessage(status)}
				}
				return agenttest.RPCReply{Result: json.RawMessage(`{"stopReason":"end_turn"}`)}
			})
			a.SetCurrentModel("voice(openai)")
			err := a.SendInput("listen", []*leapmuxv1.Attachment{{Filename: "voice.wav", MimeType: "audio/wav", Data: []byte{0, 255, 1}}})
			if name == "audio model" {
				require.NoError(t, err)
				syncPeer(t, a)
				prompts := requestsFor(requests(), acp.MethodSessionPrompt)
				require.Len(t, prompts, 1)
				encoded, marshalErr := json.Marshal(prompts[0].Params)
				require.NoError(t, marshalErr)
				assert.JSONEq(t, `{"sessionId":"session-1","prompt":[{"type":"text","text":"listen"},{"type":"audio","mimeType":"audio/wav","data":"AP8B","uri":"voice.wav"}]}`, string(encoded))
			} else {
				require.Error(t, err)
				syncPeer(t, a)
				assert.Empty(t, requestsFor(requests(), acp.MethodSessionPrompt))
				assert.False(t, a.PromptActive())
			}
		})
	}
}

func TestQwenPlainPromptDoesNotQueryAudioAndAudioSteerStaysInTheWorkerQueue(t *testing.T) {
	t.Parallel()
	a, _, requests := newQwenAgent(t, nil, nil)
	require.NoError(t, a.SendInput("text", nil))
	syncPeer(t, a)
	assert.Empty(t, requestsFor(requests(), "qwen/status/workspace/providers"))
	err := a.SteerInput("audio", []*leapmuxv1.Attachment{{Filename: "voice.wav", MimeType: "audio/wav", Data: []byte{1}}})
	require.ErrorIs(t, err, agent.ErrSteeringUnsupported)
	a.stateMu.Lock()
	assert.Empty(t, a.steer.items)
	a.stateMu.Unlock()
}

func TestQwenAudioStatusFailureRetainsInputForAnAttachmentOnlyRetry(t *testing.T) {
	t.Parallel()
	var accepts atomic.Bool
	a, _, requests := newQwenAgent(t, nil, func(request agenttest.RecordedRequest) agenttest.RPCReply {
		if request.Method == qwenProvidersStatusMethod {
			if !accepts.Load() {
				return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32601,"message":"unavailable"}`)}
			}
			return agenttest.RPCReply{Result: json.RawMessage(audioModelStatus)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{"stopReason":"end_turn"}`)}
	})
	a.SetCurrentModel("voice(openai)")
	attachments := []*leapmuxv1.Attachment{{Filename: "voice.flac", MimeType: "audio/flac", Data: []byte{0, 255, 1}}}
	require.ErrorContains(t, a.SendInput("", attachments), "check Qwen Code audio model")
	assert.False(t, a.PromptActive())
	assert.Empty(t, requestsFor(requests(), acp.MethodSessionPrompt))
	accepts.Store(true)
	require.NoError(t, a.SendInput("", attachments))
	syncPeer(t, a)
	prompts := requestsFor(requests(), acp.MethodSessionPrompt)
	require.Len(t, prompts, 1)
	encoded, err := json.Marshal(prompts[0].Params)
	require.NoError(t, err)
	assert.JSONEq(t, `{"sessionId":"session-1","prompt":[{"type":"audio","mimeType":"audio/flac","data":"AP8B","uri":"voice.flac"}]}`, string(encoded))
}

func TestQwenAudioRejectsTheSessionModelWhenTheWorkspaceDefaultAcceptsAudio(t *testing.T) {
	t.Parallel()
	a, _, requests := newQwenAgent(t, nil, func(request agenttest.RecordedRequest) agenttest.RPCReply {
		if request.Method == qwenProvidersStatusMethod {
			return agenttest.RPCReply{Result: json.RawMessage(`{"initialized":true,"current":{"modelId":"voice(openai)"},"providers":[{"kind":"model_provider","status":"ok","models":[{"modelId":"voice(openai)","modalities":{"audio":true}},{"modelId":"text(openai)","modalities":{"audio":false}}]}]}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{"stopReason":"end_turn"}`)}
	})
	a.SetCurrentModel("text(openai)")
	require.ErrorContains(t, a.SendInput("listen", []*leapmuxv1.Attachment{{Filename: "voice.wav", MimeType: "audio/wav", Data: []byte{1}}}), "does not accept audio attachments")
	syncPeer(t, a)
	assert.Empty(t, requestsFor(requests(), acp.MethodSessionPrompt))
}
