package fastagent

import (
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFastAgentAudioPolicy(t *testing.T) {
	t.Parallel()
	registry := agenttest.MustNewRegistry(Registration())
	for _, mime := range []string{"audio/wav", "audio/mpeg", "audio/mp3", "audio/aac", "audio/ogg", "audio/flac", "audio/webm"} {
		_, err := registry.NormalizeAttachments(leapmuxv1.AgentProvider_AGENT_PROVIDER_FAST_AGENT, []*leapmuxv1.Attachment{{Filename: "voice", MimeType: mime, Data: []byte{0, 255, 1}}})
		require.NoError(t, err, mime)
	}
	for _, mime := range []string{"audio/mp4", "audio/x-wav", "audio/x-flac"} {
		_, err := registry.NormalizeAttachments(leapmuxv1.AgentProvider_AGENT_PROVIDER_FAST_AGENT, []*leapmuxv1.Attachment{{Filename: "voice", MimeType: mime, Data: []byte{1}}})
		require.Error(t, err, mime)
	}
}

func TestFastAgentAudioUsesTheRunningProvider(t *testing.T) {
	t.Parallel()
	const validStatus = "# model\nProvider: google.\nSelected model: gemini.\n"
	for name, report := range map[string]string{
		"google":                       "# model\n\nProvider: google.\n\nSelected model: gemini-2.5-pro.",
		"malformed duplicate provider": "# model\nProvider: google.\nSelected model: gemini.\nProvider: openai",
		"malformed duplicate model":    "# model\nProvider: google.\nSelected model: gemini.\nSelected model: text",
		"openai":                       "# model\n\nProvider: openai.\n\nSelected model: gpt.",
		"missing provider":             "# model\nSelected model: gemini.",
		"missing model":                "# model\nProvider: google.",
		"duplicate provider":           "# model\nProvider: google.\nProvider: openai.\nSelected model: gemini.",
		"empty model":                  "# model\nProvider: google.\nSelected model: .",
		"overflow":                     validStatus + strings.Repeat("x", 32769),
		"maximum status":               validStatus + strings.Repeat("x", 32768-len(validStatus)),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var a *Agent
			var requests func() []agenttest.RecordedRequest
			a, requests = acptest.NewAgentForRPCWithRequestResponder(t, func() *Agent { return &Agent{} }, func(a *Agent) *acp.Base { return &a.Base }, func(req agenttest.RecordedRequest) agenttest.RPCReply {
				if req.Method == acp.MethodSessionPrompt {
					encoded, err := json.Marshal(req.Params)
					require.NoError(t, err)
					if strings.Contains(string(encoded), `"text":"/model"`) {
						assert.Equal(t, "session-1", req.Params["sessionId"])
						update, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "session-1", "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": report}}}})
						a.HandleOutput(update)
					}
				}
				return agenttest.RPCReply{Result: json.RawMessage(`{"stopReason":"end_turn"}`)}
			})
			sink := &agenttest.Sink{}
			services := agent.NewProviderServices(sink)
			a.SetSinkForTest(services)
			*a.HooksForTest() = a.hooks("launch-model-is-not-authority", services)
			err := a.SendInput("", []*leapmuxv1.Attachment{{Filename: "voice.wav", MimeType: "audio/wav", Data: []byte{0, 255, 1}}})
			if name == "google" || name == "maximum status" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.False(t, a.PromptActive())
			}
			_, syncErr := a.SendRequest("test/sync", nil, a.APITimeout())
			require.NoError(t, syncErr)
			var prompts []agenttest.RecordedRequest
			for _, req := range requests() {
				if req.Method == acp.MethodSessionPrompt {
					prompts = append(prompts, req)
				}
			}
			if name == "google" || name == "maximum status" {
				require.Len(t, prompts, 2)
				encoded, marshalErr := json.Marshal(prompts[1].Params)
				require.NoError(t, marshalErr)
				assert.JSONEq(t, `{"sessionId":"session-1","prompt":[{"type":"resource","resource":{"uri":"voice.wav","mimeType":"audio/wav","blob":"AP8B"}}]}`, string(encoded))
			} else {
				require.Len(t, prompts, 1)
			}
			for _, message := range sink.Messages() {
				assert.NotContains(t, string(message.Content), "Provider:", "internal status output must not enter the transcript")
			}
		})
	}
}

func TestFastAgentAudioStatusFailureAllowsRetry(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"rpc error", "cancelled", "malformed result", "missing report"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			var valid atomic.Bool
			var a *Agent
			a, requests := acptest.NewAgentForRPCWithRequestResponder(t, func() *Agent { return &Agent{} }, func(a *Agent) *acp.Base { return &a.Base }, func(req agenttest.RecordedRequest) agenttest.RPCReply {
				encoded, err := json.Marshal(req.Params)
				require.NoError(t, err)
				if req.Method == acp.MethodSessionPrompt && strings.Contains(string(encoded), `"text":"/model"`) {
					if valid.Load() || failure != "missing report" {
						a.HandleOutput(acptest.Chunk("session-1", contracts.ACPUpdateAgentMessageChunk, "# model\nProvider: google.\nSelected model: gemini.\n"))
					}
					if !valid.Load() {
						switch failure {
						case "rpc error":
							return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32601,"message":"unavailable"}`)}
						case "cancelled":
							return agenttest.RPCReply{Result: json.RawMessage(`{"stopReason":"cancelled"}`)}
						case "malformed result":
							return agenttest.RPCReply{Result: json.RawMessage(`{"stopReason":42}`)}
						}
					}
				}
				return agenttest.RPCReply{Result: json.RawMessage(`{"stopReason":"end_turn"}`)}
			})
			services := agent.NewProviderServices(&agenttest.Sink{})
			a.SetSinkForTest(services)
			*a.HooksForTest() = a.hooks("unused-launch-model", services)
			attachments := []*leapmuxv1.Attachment{{Filename: "voice.wav", MimeType: "audio/wav", Data: []byte{0, 255, 1}}}
			require.Error(t, a.SendInput("listen", attachments))
			assert.False(t, a.PromptActive())
			valid.Store(true)
			require.NoError(t, a.SendInput("retry", attachments))
			_, err := a.SendRequest("test/sync", nil, a.APITimeout())
			require.NoError(t, err)
			var originals int
			for _, req := range requests() {
				encoded, marshalErr := json.Marshal(req.Params)
				require.NoError(t, marshalErr)
				if strings.Contains(string(encoded), `"blob":"AP8B"`) {
					originals++
				}
			}
			assert.Equal(t, 1, originals, "only the successful retry may send the recording")
		})
	}
}

func TestFastAgentPlainPromptKeepsItsOutputWithoutAnAudioQuery(t *testing.T) {
	t.Parallel()
	var a *Agent
	a, requests := acptest.NewAgentForRPCWithRequestResponder(t, func() *Agent { return &Agent{} }, func(a *Agent) *acp.Base { return &a.Base }, func(req agenttest.RecordedRequest) agenttest.RPCReply {
		if req.Method == acp.MethodSessionPrompt {
			a.HandleOutput(acptest.Chunk("session-1", contracts.ACPUpdateAgentMessageChunk, "ordinary native answer"))
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{"stopReason":"end_turn"}`)}
	})
	sink := &agenttest.Sink{}
	services := agent.NewProviderServices(sink)
	a.SetSinkForTest(services)
	*a.HooksForTest() = a.hooks("model", services)
	require.NoError(t, a.SendInput("ordinary prompt", nil))
	testutil.RequireEventually(t, func() bool {
		for _, message := range sink.Messages() {
			if strings.Contains(string(message.Content), "ordinary native answer") {
				return true
			}
		}
		return false
	})
	_, err := a.SendRequest("test/sync", nil, a.APITimeout())
	require.NoError(t, err)
	var prompts int
	for _, req := range requests() {
		if req.Method == acp.MethodSessionPrompt {
			prompts++
		}
	}
	assert.Equal(t, 1, prompts)
}

func TestFastAgentAudioQueryPreservesUnrelatedNativeText(t *testing.T) {
	t.Parallel()
	var a *Agent
	a, requests := acptest.NewAgentForRPCWithRequestResponder(t, func() *Agent { return &Agent{} }, func(a *Agent) *acp.Base { return &a.Base }, func(req agenttest.RecordedRequest) agenttest.RPCReply {
		encoded, err := json.Marshal(req.Params)
		require.NoError(t, err)
		if req.Method == acp.MethodSessionPrompt && strings.Contains(string(encoded), `"text":"/model"`) {
			a.HandleOutput(acptest.Chunk("session-1", contracts.ACPUpdateAgentMessageChunk, "ordinary native text outside the model query"))
			a.HandleOutput(acptest.Chunk("session-1", contracts.ACPUpdateAgentMessageChunk, "# model\nProvider: google.\nSelected model: gemini.\n"))
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{"stopReason":"end_turn"}`)}
	})
	sink := &agenttest.Sink{}
	services := agent.NewProviderServices(sink)
	a.SetSinkForTest(services)
	*a.HooksForTest() = a.hooks("launch", services)
	require.NoError(t, a.SendInput("listen", []*leapmuxv1.Attachment{{Filename: "voice.wav", MimeType: "audio/wav", Data: []byte{0, 255, 1}}}))
	testutil.RequireEventually(t, func() bool {
		for _, message := range sink.Messages() {
			if strings.Contains(string(message.Content), "ordinary native text outside the model query") {
				return true
			}
		}
		return false
	})
	_, err := a.SendRequest("test/sync", nil, a.APITimeout())
	require.NoError(t, err)
	var prompts int
	for _, req := range requests() {
		if req.Method == acp.MethodSessionPrompt {
			prompts++
		}
	}
	assert.Equal(t, 2, prompts, "the internal query and original audio prompt both complete")
	for _, message := range sink.Messages() {
		assert.NotContains(t, string(message.Content), "Provider: google")
	}
}
