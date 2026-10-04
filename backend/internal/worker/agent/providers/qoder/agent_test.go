package qoder

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func configuredQoderAgent(t *testing.T, settings string) *Agent {
	t.Helper()
	home := t.TempDir()
	configDir := filepath.Join(home, ".qoder")
	require.NoError(t, os.MkdirAll(configDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "settings.json"), []byte(settings), 0o600))
	a := newOfflineAgent(t, &agenttest.Sink{})
	a.opts.HomeDir = home
	return a
}

func TestPlanModeChangedReportsNativeWorkingState(t *testing.T) {
	t.Parallel()
	a := newOfflineAgent(t, &agenttest.Sink{})
	a.permissionMode = contracts.QoderModeAcceptEdits
	mode := func() string { return a.SettingsSnapshot().ConfirmedOptions()[agent.OptionIDPermissionMode] }

	assert.Equal(t, contracts.QoderModeAcceptEdits, mode())
	a.HandleOutput([]byte(`{"type":"system","subtype":"plan_mode_changed","plan_mode":{"active":true}}`))
	assert.Equal(t, contracts.QoderModePlan, mode())
	a.HandleOutput([]byte(`{"type":"system","subtype":"plan_mode_changed","plan_mode":{}}`))
	assert.Equal(t, contracts.QoderModePlan, mode(), "a missing active value must not end Plan")
	a.HandleOutput([]byte(`{"type":"system","subtype":"plan_mode_changed","plan_mode":{"active":"false"}}`))
	assert.Equal(t, contracts.QoderModePlan, mode(), "an invalid active value must not end Plan")
	a.HandleOutput([]byte(`{"type":"system","subtype":"plan_mode_changed","plan_mode":{"active":false}}`))
	assert.Equal(t, contracts.QoderModeAcceptEdits, mode())
}

// modelControlStdin records a control request and acknowledges it on the same
// fake process channel. The real Qoder process answers on stdout instead.
type modelControlStdin struct {
	agent *Agent
	mu    sync.Mutex
	frame map[string]any
}

func (s *modelControlStdin) Write(data []byte) (int, error) {
	var frame struct {
		RequestID string         `json:"request_id"`
		Request   map[string]any `json:"request"`
	}
	if err := json.Unmarshal(data, &frame); err != nil {
		return 0, err
	}
	s.mu.Lock()
	s.frame = frame.Request
	s.mu.Unlock()
	s.agent.HandleOutput([]byte(fmt.Sprintf(`{"type":"control_response","response":{"subtype":"success","request_id":%q,"response":{}}}`, frame.RequestID)))
	return len(data), nil
}

func (*modelControlStdin) Close() error { return nil }

func (s *modelControlStdin) request() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.frame
}

func TestAvailableModelsUpdateOffersTheModelGroup(t *testing.T) {
	t.Parallel()
	a := newOfflineAgent(t, &agenttest.Sink{})
	a.HandleOutput([]byte(`{"type":"system","subtype":"available_models_update","models":[{"value":"model-a","displayName":"Model A"},{"value":"model-b","displayName":"Model B"}],"currentModel":"model-a"}`))

	var model *leapmuxv1.AvailableOptionGroup
	for _, group := range a.OptionGroups() {
		if group.GetId() == agent.OptionIDModel {
			model = group
		}
	}
	require.NotNil(t, model)
	assert.Equal(t, "model-a", model.GetCurrentValue())
	require.Len(t, model.GetOptions(), 2)
	assert.Equal(t, []string{"model-a", "model-b"}, []string{model.GetOptions()[0].GetId(), model.GetOptions()[1].GetId()})
}

func TestEmptyNativeCatalogUsesConfiguredModels(t *testing.T) {
	t.Parallel()
	settings := `{"providers":{"mockprov":{"models":[{"model":"first","displayName":"First Model"},{"model":"second","displayName":"Second Model"}]}}}`
	a := configuredQoderAgent(t, settings)
	a.model = "mockprov/first"
	a.HandleOutput([]byte(`{"type":"system","subtype":"available_models_update","models":[],"currentModel":"mockprov/first"}`))

	var model *leapmuxv1.AvailableOptionGroup
	for _, group := range a.OptionGroups() {
		if group.GetId() == agent.OptionIDModel {
			model = group
		}
	}
	require.NotNil(t, model)
	assert.Equal(t, "mockprov/first", model.GetCurrentValue())
	require.Len(t, model.GetOptions(), 2)
	assert.Equal(t, []string{"mockprov/first", "mockprov/second"}, []string{model.GetOptions()[0].GetId(), model.GetOptions()[1].GetId()})
	assert.Equal(t, []string{"First Model", "Second Model"}, []string{model.GetOptions()[0].GetName(), model.GetOptions()[1].GetName()})
}

func TestNativeCatalogOverridesConfiguredModels(t *testing.T) {
	t.Parallel()
	a := configuredQoderAgent(t, `{"providers":{"mockprov":{"models":[{"model":"first"},{"model":"second"}]}}}`)
	a.model = "mockprov/first"
	a.HandleOutput([]byte(`{"type":"system","subtype":"available_models_update","models":[{"value":"native-one","displayName":"Native One"},{"value":"native-two","displayName":"Native Two"}],"currentModel":"native-one"}`))

	var model *leapmuxv1.AvailableOptionGroup
	for _, group := range a.OptionGroups() {
		if group.GetId() == agent.OptionIDModel {
			model = group
		}
	}
	require.NotNil(t, model)
	require.Len(t, model.GetOptions(), 2)
	assert.Equal(t, []string{"native-one", "native-two"}, []string{model.GetOptions()[0].GetId(), model.GetOptions()[1].GetId()})
}

func TestConfiguredModelsDeduplicateAndSortProviders(t *testing.T) {
	t.Parallel()
	a := configuredQoderAgent(t, `{"providers":{"zeta":{"models":[{"model":"m","displayName":"Zeta"},{"model":"m","displayName":"Duplicate"}]},"alpha":{"model":"implicit","models":[{"model":"first","displayName":"First"}]}}}`)
	models, err := readQoderConfiguredModels(a.opts)
	require.NoError(t, err)
	require.Len(t, models, 3)
	assert.Equal(t, []string{"alpha/first", "alpha/implicit", "zeta/m"}, []string{models[0].id, models[1].id, models[2].id})
	assert.Equal(t, "Zeta", models[2].displayName)
}

func TestConfiguredModelsRejectOversizedSettings(t *testing.T) {
	t.Parallel()
	a := configuredQoderAgent(t, string(bytes.Repeat([]byte(" "), qoderSettingsLimit+1)))
	_, err := readQoderConfiguredModels(a.opts)
	require.ErrorContains(t, err, "exceed")
}

func TestUpdateSettingsSetsTheModelOnTheRunningSession(t *testing.T) {
	t.Parallel()
	a := newOfflineAgent(t, &agenttest.Sink{})
	a.HandleOutput([]byte(`{"type":"system","subtype":"available_models_update","models":[{"value":"model-a","displayName":"Model A"},{"value":"model-b","displayName":"Model B"}],"currentModel":"model-a"}`))
	stdin := &modelControlStdin{agent: a}
	a.SetStdinForTest(stdin)

	result := a.UpdateSettings(map[string]string{agent.OptionIDModel: "model-b"})
	request := stdin.request()
	require.NotNil(t, request, "a model choice must reach Qoder's control channel")
	assert.Equal(t, "set_model", request["subtype"])
	assert.Equal(t, "model-b", request["model"])
	assert.Equal(t, "model-b", result.SurfacedOptions[agent.OptionIDModel])
	assert.True(t, result.AppliedLive)
}

func sentUserContent(t *testing.T, stdin *agenttest.Stdin) []map[string]any {
	t.Helper()
	var frame struct {
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdin.String()), &frame))
	var blocks []map[string]any
	require.NoError(t, json.Unmarshal(frame.Message.Content, &blocks))
	return blocks
}

func TestSendInputCarriesTextAttachmentBytes(t *testing.T) {
	t.Parallel()
	a := newOfflineAgent(t, &agenttest.Sink{})
	stdin := &agenttest.Stdin{}
	a.SetStdinForTest(stdin)

	require.NoError(t, a.SendInput("Read the note.", []*leapmuxv1.Attachment{{
		Filename: "notes.txt", MimeType: "text/plain", Data: []byte("unique-note-42"),
	}}))
	blocks := sentUserContent(t, stdin)
	require.Len(t, blocks, 2)
	assert.Equal(t, map[string]any{"type": "text", "text": "Read the note."}, blocks[0])
	assert.Equal(t, "text", blocks[1]["type"])
	assert.Contains(t, blocks[1]["text"], "notes.txt")
	assert.Contains(t, blocks[1]["text"], "unique-note-42")
}

func TestSendInputCarriesImageBytes(t *testing.T) {
	t.Parallel()
	a := newOfflineAgent(t, &agenttest.Sink{})
	stdin := &agenttest.Stdin{}
	a.SetStdinForTest(stdin)
	data := []byte{0x89, 'P', 'N', 'G', 0x00}

	require.NoError(t, a.SendInput("Describe this.", []*leapmuxv1.Attachment{{
		Filename: "shot.png", MimeType: "image/png", Data: data,
	}}))
	blocks := sentUserContent(t, stdin)
	require.Len(t, blocks, 2)
	assert.Equal(t, map[string]any{"type": "text", "text": "Describe this."}, blocks[0])
	assert.Equal(t, "image", blocks[1]["type"])
	assert.Equal(t, map[string]any{
		"type": "base64", "media_type": "image/png", "data": base64.StdEncoding.EncodeToString(data),
	}, blocks[1]["source"])
}

func TestProviderRejectsPDFAndBinaryAttachments(t *testing.T) {
	t.Parallel()
	for _, kind := range []agent.AttachmentKind{agent.AttachmentKindPDF, agent.AttachmentKindBinary} {
		err := qoderProvider{}.ValidateAttachment(agent.ClassifiedAttachment{Filename: "file", Kind: kind})
		require.Error(t, err, "Qoder's stream user frame cannot carry %s bytes", kind)
	}
}
