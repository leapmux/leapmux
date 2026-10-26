package opencode

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const familyAudioCatalog = `{"all":[{"id":"private","models":{"voice":{"id":"voice","providerID":"private","api":{"id":"gemini","npm":"@ai-sdk/google"},"capabilities":{"input":{"audio":true}}},"text":{"id":"text","providerID":"private","api":{"id":"text","npm":"@ai-sdk/google"},"capabilities":{"input":{"audio":false}}}}}]}`

func newFamilyAudioAgent(t *testing.T, status int, catalog string) (*Agent, func() []agenttest.RecordedRequest, *atomic.Int64) {
	t.Helper()
	calls := &atomic.Int64{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/provider", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, err := w.Write([]byte(catalog))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	a, requests := acptest.NewAgentForRPCWithResponder(t, func() *Agent { return &Agent{} }, func(a *Agent) *acp.Base { return &a.Base }, func(string) agenttest.RPCReply {
		return agenttest.RPCReply{Result: json.RawMessage(`{"stopReason":"end_turn"}`)}
	})
	services := agent.NewProviderServices(&agenttest.Sink{})
	a.SetSinkForTest(services)
	a.configureQuestions(services)
	a.Questions.baseURL = server.URL
	*a.HooksForTest() = FamilyHooks(&a.FamilyBase)
	a.SetCurrentModel("private/voice")
	return a, requests, calls
}

func audioPromptRequests(t *testing.T, a *Agent, requests func() []agenttest.RecordedRequest) []agenttest.RecordedRequest {
	t.Helper()
	_, err := a.SendRequest("test/sync", nil, a.APITimeout())
	require.NoError(t, err)
	var prompts []agenttest.RecordedRequest
	for _, req := range requests() {
		if req.Method == acp.MethodSessionPrompt {
			prompts = append(prompts, req)
		}
	}
	return prompts
}

func TestFamilyAudioRequiresTheSelectedModelAndGoogleAdapter(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		catalog string
		status  int
		accepts bool
	}{
		"google audio":       {familyAudioCatalog, http.StatusOK, true},
		"no audio modality":  {strings.ReplaceAll(familyAudioCatalog, `"audio":true`, `"audio":false`), http.StatusOK, false},
		"another adapter":    {strings.ReplaceAll(familyAudioCatalog, "@ai-sdk/google", "@ai-sdk/openai-compatible"), http.StatusOK, false},
		"unknown model":      {strings.ReplaceAll(familyAudioCatalog, `"voice"`, `"another"`), http.StatusOK, false},
		"unknown provider":   {strings.ReplaceAll(familyAudioCatalog, `"private"`, `"another"`), http.StatusOK, false},
		"malformed modality": {strings.ReplaceAll(familyAudioCatalog, `"audio":true`, `"audio":"true"`), http.StatusOK, false},
		"missing catalog":    {`{}`, http.StatusOK, false},
		"malformed catalog":  {`invalid`, http.StatusOK, false},
		"native failure":     {`{"error":"unavailable"}`, http.StatusServiceUnavailable, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, requests, calls := newFamilyAudioAgent(t, tc.status, tc.catalog)
			err := a.SendInput("listen", []*leapmuxv1.Attachment{{Filename: "voice.wav", MimeType: "audio/wav", Data: []byte{0, 255, 1}}})
			if tc.accepts {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.False(t, a.PromptActive())
			}
			prompts := audioPromptRequests(t, a, requests)
			assert.EqualValues(t, 1, calls.Load())
			if tc.accepts {
				require.Len(t, prompts, 1)
				raw, marshalErr := json.Marshal(prompts[0].Params)
				require.NoError(t, marshalErr)
				assert.JSONEq(t, `{"sessionId":"session-1","prompt":[{"type":"text","text":"listen"},{"type":"resource","resource":{"uri":"voice.wav","mimeType":"audio/wav","blob":"AP8B"}}]}`, string(raw))
			} else {
				assert.Empty(t, prompts)
			}
		})
	}
}

func TestFamilyAudioChildUsesItsLoadedModelAndKeepsTheParentRunning(t *testing.T) {
	t.Parallel()
	for _, childAccepts := range []bool{false, true} {
		t.Run(map[bool]string{false: "child refuses", true: "child accepts"}[childAccepts], func(t *testing.T) {
			t.Parallel()
			a, requests, _ := newFamilyAudioAgent(t, http.StatusOK, familyAudioCatalog)
			root, child := "private/voice", "private/text"
			if childAccepts {
				root, child = child, root
			}
			a.SetCurrentModel(root)
			a.childSessions = map[string]*openCodeChildSession{"task": {id: "child-session", loaded: true, modelID: child}}
			require.True(t, a.BeginAgentTurn())
			err := a.SteerChildInput("task", "", []*leapmuxv1.Attachment{{Filename: "voice.flac", MimeType: "audio/flac", Data: []byte{0, 255, 1}}})
			prompts := audioPromptRequests(t, a, requests)
			if childAccepts {
				require.NoError(t, err)
				require.Len(t, prompts, 1)
				assert.Equal(t, "child-session", prompts[0].Params["sessionId"])
			} else {
				require.ErrorContains(t, err, "does not accept audio attachments")
				assert.Empty(t, prompts)
			}
			assert.True(t, a.PromptActive())
			assert.Equal(t, root, a.CurrentModel())
		})
	}
}

func TestFamilyPlainPromptDoesNotQueryTheAudioCatalog(t *testing.T) {
	t.Parallel()
	a, requests, calls := newFamilyAudioAgent(t, http.StatusServiceUnavailable, `{}`)
	require.NoError(t, a.SendInput("plain", nil))
	require.Len(t, audioPromptRequests(t, a, requests), 1)
	assert.Zero(t, calls.Load())
}

func TestFamilyAudioRefusalAllowsARecordingRetryAfterTheModelChanges(t *testing.T) {
	t.Parallel()
	a, requests, calls := newFamilyAudioAgent(t, http.StatusOK, familyAudioCatalog)
	a.SetCurrentModel("private/text")
	attachments := []*leapmuxv1.Attachment{{Filename: "voice.wav", MimeType: "audio/wav", Data: []byte{0, 255, 1}}}
	require.ErrorContains(t, a.SendInput("listen", attachments), "does not accept audio attachments")
	assert.Empty(t, audioPromptRequests(t, a, requests))
	assert.False(t, a.PromptActive())
	a.SetCurrentModel("private/voice")
	require.NoError(t, a.SendInput("retry", attachments))
	require.Len(t, audioPromptRequests(t, a, requests), 1)
	assert.EqualValues(t, 2, calls.Load())
}

func TestFamilyAudioSteerRefusalKeepsTheParentTurnActive(t *testing.T) {
	t.Parallel()
	a, requests, _ := newFamilyAudioAgent(t, http.StatusOK, familyAudioCatalog)
	a.SetCurrentModel("private/text")
	require.True(t, a.BeginAgentTurn())
	require.ErrorContains(t, a.SteerInput("listen", []*leapmuxv1.Attachment{{Filename: "voice.wav", MimeType: "audio/wav", Data: []byte{1}}}), "does not accept audio attachments")
	assert.Empty(t, audioPromptRequests(t, a, requests))
	assert.True(t, a.PromptActive())
}

func TestFamilyAudioRejectsAnUnknownModelAndDuplicateCatalogProviders(t *testing.T) {
	t.Parallel()
	for _, model := range []string{"", "voice", "/voice", "private/"} {
		require.Error(t, validateFamilyAudioModel([]byte(familyAudioCatalog), model))
	}
	provider := strings.TrimSuffix(strings.TrimPrefix(familyAudioCatalog, `{"all":[`), `]}`)
	require.ErrorContains(t, validateFamilyAudioModel([]byte(`{"all":[`+provider+`,`+provider+`]}`), "private/voice"), "cannot verify")
}

func TestFamilyAudioFormatPolicy(t *testing.T) {
	t.Parallel()
	provider := FamilyProvider{}
	for _, mime := range []string{"audio/wav", "audio/mpeg", "audio/mp3", "audio/aac", "audio/ogg", "audio/flac", "audio/webm"} {
		require.NoError(t, provider.ValidateAttachment(agent.ClassifiedAttachment{Kind: "audio", MIMEType: mime, Data: []byte{1}}), mime)
		require.Error(t, provider.ValidateAttachment(agent.ClassifiedAttachment{Kind: "audio", MIMEType: mime}), mime)
	}
	for _, mime := range []string{"audio/mp4", "audio/x-flac", "audio/x-wav", "audio/unknown"} {
		require.Error(t, provider.ValidateAttachment(agent.ClassifiedAttachment{Kind: "audio", MIMEType: mime, Data: []byte{1}}), mime)
	}
}
