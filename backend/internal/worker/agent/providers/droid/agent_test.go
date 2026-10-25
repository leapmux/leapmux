package droid

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

func TestSendInputCarriesImageBytes(t *testing.T) {
	t.Parallel()
	a, _, stdin := newSteerAgent(t)
	data := []byte{0x89, 'P', 'N', 'G', 0x00}

	require.NoError(t, a.SendInput("Describe this.", []*leapmuxv1.Attachment{{
		Filename: "shot.png", MimeType: "image/png", Data: data,
	}}))
	params := lastRequest(t, stdin)
	assert.Equal(t, "Describe this.", params["text"])
	assert.Equal(t, []any{map[string]any{
		"type": "base64", "mediaType": "image/png", "data": base64.StdEncoding.EncodeToString(data),
	}}, params["images"])
}

func installDroidPDFCatalog(t *testing.T, a *Agent, selected string, models []map[string]any) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"sessionId": a.mainSessionID(), "settings": map[string]string{"modelId": selected}, "availableModels": models,
	})
	require.NoError(t, err)
	require.NoError(t, a.adoptSessionID(&droidEnvelope{Result: raw}))
}

func TestSendInputCarriesPDFBytes(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"ordinary", "attachment only", "session checked", "steering"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			a, _, stdin := newSteerAgent(t)
			installDroidPDFCatalog(t, a, "pdf-model", []map[string]any{{"id": "pdf-model", "isCustom": true, "modelProvider": "anthropic"}})
			data := []byte{'%', 'P', 'D', 'F', '-', 0, 255}
			filename := "quoted \"雪\".pdf"
			attachments := []*leapmuxv1.Attachment{{Filename: filename, MimeType: "application/pdf", Data: data}}
			text := "Inspect this PDF."
			var err error
			switch mode {
			case "attachment only":
				text = ""
				err = a.SendInput(text, attachments)
			case "session checked":
				err = a.SendInputForSession("main-session", text, attachments)
			case "steering":
				a.startTurn()
				err = a.SteerInput(text, attachments)
			default:
				err = a.SendInput(text, attachments)
			}
			require.NoError(t, err)
			params := lastRequest(t, stdin)
			assert.Equal(t, text, params["text"])
			assert.Equal(t, "main-session", params["sessionId"])
			assert.Equal(t, "end_of_turn", params["queuePlacement"])
			assert.Equal(t, []any{map[string]any{"type": "base64", "mediaType": "application/pdf", "data": base64.StdEncoding.EncodeToString(data), "name": filename}}, params["files"])
			assert.NotContains(t, params, "images")
		})
	}
}

func TestPDFDeliveryUsesTheSelectedNativeModel(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		model map[string]any
		allow bool
	}{
		{name: "advertised builtin", model: map[string]any{"id": "model", "supportsPDFs": true}, allow: true},
		{name: "explicit refusal", model: map[string]any{"id": "model", "supportsPDFs": false, "isCustom": true, "modelProvider": "anthropic"}},
		{name: "custom Anthropic", model: map[string]any{"id": "model", "isCustom": true, "modelProvider": "anthropic"}, allow: true},
		{name: "custom OpenAI", model: map[string]any{"id": "model", "isCustom": true, "modelProvider": "openai"}, allow: true},
		{name: "custom generic", model: map[string]any{"id": "model", "isCustom": true, "modelProvider": "generic-chat-completion-api"}},
		{name: "custom Bedrock Converse", model: map[string]any{"id": "model", "isCustom": true, "modelProvider": "bedrock-converse"}},
		{name: "custom unknown", model: map[string]any{"id": "model", "isCustom": true, "modelProvider": "future"}},
		{name: "unadvertised builtin", model: map[string]any{"id": "model", "modelProvider": "anthropic"}},
		{name: "different selected model", model: map[string]any{"id": "other-model", "supportsPDFs": true}},
		{name: "empty catalog"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, _, stdin := newSteerAgent(t)
			var models []map[string]any
			if tc.model != nil {
				models = append(models, tc.model)
			}
			installDroidPDFCatalog(t, a, "model", models)
			err := a.SendInput("Inspect.", []*leapmuxv1.Attachment{{Filename: "sample.pdf", MimeType: "application/pdf", Data: []byte("%PDF")}})
			if tc.allow {
				require.NoError(t, err)
				assert.Contains(t, lastRequest(t, stdin), "files")
			} else {
				require.ErrorContains(t, err, "selected")
				assert.Empty(t, stdin.frames())
				assert.False(t, a.PublishTurnActive().Active)
			}
		})
	}
}

func TestPDFDeliveryFollowsNativeModelChanges(t *testing.T) {
	t.Parallel()
	a, _, stdin := newSteerAgent(t)
	installDroidPDFCatalog(t, a, "compatible", []map[string]any{
		{"id": "compatible", "supportsPDFs": true}, {"id": "incompatible", "supportsPDFs": false},
	})
	attachments := []*leapmuxv1.Attachment{{Filename: "sample.pdf", MimeType: "application/pdf", Data: []byte("%PDF")}}
	require.NoError(t, a.SendInput("First.", attachments))
	a.endTurn()
	a.onSettingsUpdated([]byte(`{"type":"settings_updated","settings":{"modelId":"incompatible"}}`))
	require.ErrorContains(t, a.SendInput("Unsent.", attachments), "selected")
	assert.Len(t, stdin.frames(), 1)
	assert.False(t, a.PublishTurnActive().Active)
	a.onSettingsUpdated([]byte(`{"type":"settings_updated","settings":{"modelId":"compatible"}}`))
	require.NoError(t, a.SendInput("Second.", attachments))
	assert.Len(t, stdin.frames(), 2)
}

func TestPDFDeliveryNeedsANativeSelectedModelIdentity(t *testing.T) {
	t.Parallel()
	for _, selected := range []string{"", "   "} {
		t.Run("selection-"+selected, func(t *testing.T) {
			t.Parallel()
			a, _, stdin := newSteerAgent(t)
			installDroidPDFCatalog(t, a, selected, []map[string]any{{"id": selected, "supportsPDFs": true}})
			err := a.SendInput("Unsent.", []*leapmuxv1.Attachment{{Filename: "sample.pdf", MimeType: "application/pdf", Data: []byte("%PDF")}})
			require.ErrorContains(t, err, "selected")
			assert.Empty(t, stdin.frames())
			assert.False(t, a.PublishTurnActive().Active)
		})
	}
}

func TestPDFModelMetadataRejectsInvalidTypes(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"supportsPDFs", "modelProvider", "isCustom"} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			a, sink, _ := newSteerAgent(t)
			raw, err := json.Marshal(map[string]any{
				"sessionId": "main-session", "settings": map[string]string{"modelId": "model"},
				"availableModels": []map[string]any{{"id": "model", field: 123}},
			})
			require.NoError(t, err)
			require.ErrorContains(t, a.adoptSessionID(&droidEnvelope{Result: raw}), "read droid startup result")
			assert.Empty(t, sink.SessionIDs())
			assert.Empty(t, a.catalog.models)
		})
	}
}

func TestPDFModelDescriptionIdentifiesCompatibleChoices(t *testing.T) {
	t.Parallel()
	a, _, _ := newSteerAgent(t)
	installDroidPDFCatalog(t, a, "compatible", []map[string]any{
		{"id": "compatible", "displayName": "PDF model", "supportsPDFs": true},
		{"id": "incompatible", "displayName": "Text model", "supportsPDFs": false},
	})
	for _, group := range a.OptionGroups() {
		if group.GetId() != agent.OptionIDModel {
			continue
		}
		require.Len(t, group.GetOptions(), 2)
		assert.Equal(t, "PDF model", group.GetOptions()[0].GetName())
		assert.Equal(t, "Supports PDF attachments up to 3 MiB", group.GetOptions()[0].GetDescription())
		assert.Empty(t, group.GetOptions()[1].GetDescription())
		return
	}
	t.Fatal("the native catalog has no model group")
}

func TestPDFDeliveryKeepsInputGuards(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"different session", "busy", "stopped"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			a, _, stdin := newSteerAgent(t)
			installDroidPDFCatalog(t, a, "incompatible", []map[string]any{{"id": "incompatible", "supportsPDFs": false}})
			sessionID := "main-session"
			want := agent.ErrInputSessionChanged
			switch state {
			case "different session":
				sessionID = "other-session"
			case "busy":
				a.startTurn()
				want = agent.ErrAgentBusy
			case "stopped":
				a.stopped = true
				want = errAgentStopped
			}
			before := a.PublishTurnActive().Active
			err := a.SendInputForSession(sessionID, "Unsent.", []*leapmuxv1.Attachment{{Filename: "sample.pdf", MimeType: "application/pdf", Data: []byte("%PDF")}})
			require.ErrorIs(t, err, want)
			assert.Empty(t, stdin.frames())
			assert.Equal(t, before, a.PublishTurnActive().Active)
		})
	}
}

func TestInputAdmissionHoldsSettingsOrder(t *testing.T) {
	t.Parallel()
	a, _, stdin := newSteerAgent(t)
	original := stdin.respond
	observed := false
	stdin.respond = func(request droidEnvelope) {
		if request.Method == droidMethodAddUserMessage {
			observed = true
			acquired := a.settingsApplyMu.TryLock()
			if acquired {
				a.settingsApplyMu.Unlock()
			}
			assert.False(t, acquired, "native input admission must retain the model validated for its attachments")
		}
		original(request)
	}
	require.NoError(t, a.SendInput("One ordered input.", nil))
	assert.True(t, observed)
	require.True(t, a.settingsApplyMu.TryLock(), "input acknowledgment must release the settings transaction")
	a.settingsApplyMu.Unlock()
}

func TestSteerInputWritesNativeSteeringPlacement(t *testing.T) {
	t.Parallel()
	a, _, stdin := newSteerAgent(t)
	a.startTurn()

	require.NoError(t, a.SteerInput("guide this turn", nil))
	params := lastRequest(t, stdin)
	assert.Equal(t, "end_of_turn", params["queuePlacement"], "Droid processes this placement during a running turn")
	assert.True(t, a.PublishTurnActive().Active)
}

func TestTurnStartPublishesSteeringAvailability(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	a.startTurn()
	a.startTurn()
	a.endTurn()
	assert.Equal(t, []leapmuxv1.AgentInputKind{
		leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_UNSPECIFIED,
	}, sink.TurnKinds())
}
