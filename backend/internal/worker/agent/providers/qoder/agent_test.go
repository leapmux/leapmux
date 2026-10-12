package qoder

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// sentUserFrame returns the one stream-json line that the agent wrote.
func sentUserFrame(t *testing.T, stdin *agenttest.Stdin) string {
	t.Helper()
	written := stdin.String()
	require.True(t, strings.HasSuffix(written, "\n"), "a stream-json frame must end its line")
	lines := strings.Split(strings.TrimSuffix(written, "\n"), "\n")
	require.Len(t, lines, 1, "one prompt must write exactly one stream-json line")
	return lines[0]
}

func testPNGAttachment() *leapmuxv1.Attachment {
	return &leapmuxv1.Attachment{Filename: "shot.png", MimeType: "image/png", Data: []byte{0x89, 'P', 'N', 'G', 0x00}}
}

func testPDFAttachment() *leapmuxv1.Attachment {
	return &leapmuxv1.Attachment{Filename: "paper.pdf", MimeType: "application/pdf", Data: []byte("%PDF-1.7")}
}

// userContentOrderCase is one prompt and the exact stream-json user frame
// that Qoder must receive for it.
type userContentOrderCase struct {
	name        string
	text        string
	attachments []*leapmuxv1.Attachment
	want        string
}

// userContentOrderCases pins the content order that qoderUserContent states:
// every attachment first, in the order that the user attached it, and the
// prompt text last, also when the prompt text is empty.
func userContentOrderCases() []userContentOrderCase {
	gif := &leapmuxv1.Attachment{Filename: "chart.gif", MimeType: "image/gif", Data: []byte("GIF89a")}
	note := &leapmuxv1.Attachment{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("unique-note-42")}
	const pngBlock = `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORwA="}}`
	const gifBlock = `{"type":"image","source":{"type":"base64","media_type":"image/gif","data":"R0lGODlh"}}`
	const noteBlock = `{"type":"text","text":"<attached-file name=\"notes.txt\" mime-type=\"text/plain\">\nunique-note-42\n</attached-file>"}`
	frame := func(content string) string {
		return `{"type":"user","message":{"role":"user","content":` + content + `}}`
	}
	return []userContentOrderCase{
		{
			name: "text only stays a plain string",
			text: "Describe this.",
			want: frame(`"Describe this."`),
		},
		{
			name:        "nil attachments leave a plain string",
			text:        "Describe this.",
			attachments: []*leapmuxv1.Attachment{nil, nil},
			want:        frame(`"Describe this."`),
		},
		{
			name:        "an image precedes the text",
			text:        "Describe this.",
			attachments: []*leapmuxv1.Attachment{testPNGAttachment()},
			want:        frame(`[` + pngBlock + `,{"type":"text","text":"Describe this."}]`),
		},
		{
			name:        "a text attachment precedes the text",
			text:        "Read the note.",
			attachments: []*leapmuxv1.Attachment{note},
			want:        frame(`[` + noteBlock + `,{"type":"text","text":"Read the note."}]`),
		},
		{
			name:        "mixed attachments keep their order before the text",
			text:        "Compare them.",
			attachments: []*leapmuxv1.Attachment{testPNGAttachment(), nil, note, gif},
			want:        frame(`[` + pngBlock + `,` + noteBlock + `,` + gifBlock + `,{"type":"text","text":"Compare them."}]`),
		},
		{
			name:        "images without text still end with the text block",
			text:        "",
			attachments: []*leapmuxv1.Attachment{testPNGAttachment(), gif},
			want:        frame(`[` + pngBlock + `,` + gifBlock + `,{"type":"text","text":""}]`),
		},
	}
}

func TestSendInputPutsThePromptTextLast(t *testing.T) {
	t.Parallel()
	for _, tc := range userContentOrderCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := newOfflineAgent(t, &agenttest.Sink{})
			stdin := &agenttest.Stdin{}
			a.SetStdinForTest(stdin)

			require.NoError(t, a.SendInput(tc.text, tc.attachments))
			assert.JSONEq(t, tc.want, sentUserFrame(t, stdin))
		})
	}
}

// TestSteerInputPutsThePromptTextLast requires the steering frame to carry the
// same content as a new prompt. Qoder queues a user frame that arrives during a
// turn with its content unchanged, and reads that content again when the queued
// prompt starts a turn of its own.
func TestSteerInputPutsThePromptTextLast(t *testing.T) {
	t.Parallel()
	for _, tc := range userContentOrderCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := newOfflineAgent(t, &agenttest.Sink{})
			stdin := &agenttest.Stdin{}
			a.SetStdinForTest(stdin)
			a.mu.Lock()
			a.active = true
			a.mu.Unlock()

			require.NoError(t, a.SteerInput(tc.text, tc.attachments))
			assert.JSONEq(t, tc.want, sentUserFrame(t, stdin))
		})
	}
}

func TestSendInputRejectsAnUnsupportedAttachmentBeforeTheTurnStarts(t *testing.T) {
	t.Parallel()
	a := newOfflineAgent(t, &agenttest.Sink{})
	stdin := &agenttest.Stdin{}
	a.SetStdinForTest(stdin)

	err := a.SendInput("Read both.", []*leapmuxv1.Attachment{testPNGAttachment(), testPDFAttachment()})
	require.ErrorContains(t, err, "paper.pdf")
	assert.Empty(t, stdin.String(), "a rejected prompt must write no partial frame")
	a.mu.Lock()
	active := a.active
	a.mu.Unlock()
	assert.False(t, active, "a rejected prompt must not start a turn")

	require.NoError(t, a.SendInput("Describe this.", nil), "the next prompt must not see a busy agent")
	assert.JSONEq(t, `{"type":"user","message":{"role":"user","content":"Describe this."}}`, sentUserFrame(t, stdin))
}

func TestSteerInputRejectsAnUnsupportedAttachment(t *testing.T) {
	t.Parallel()
	a := newOfflineAgent(t, &agenttest.Sink{})
	stdin := &agenttest.Stdin{}
	a.SetStdinForTest(stdin)
	a.mu.Lock()
	a.active = true
	a.mu.Unlock()

	err := a.SteerInput("Read both.", []*leapmuxv1.Attachment{testPNGAttachment(), testPDFAttachment()})
	require.ErrorContains(t, err, "paper.pdf", "a steer must not drop an attachment that Qoder cannot read")
	assert.Empty(t, stdin.String(), "a rejected steer must write no partial frame")
	a.mu.Lock()
	active := a.active
	a.mu.Unlock()
	assert.True(t, active, "a rejected steer must leave the running turn alone")
}

func TestSteerInputWithoutATurnWritesNothing(t *testing.T) {
	t.Parallel()
	a := newOfflineAgent(t, &agenttest.Sink{})
	stdin := &agenttest.Stdin{}
	a.SetStdinForTest(stdin)

	err := a.SteerInput("Look at this too.", []*leapmuxv1.Attachment{testPNGAttachment()})
	require.ErrorIs(t, err, agent.ErrNoActiveTurn)
	assert.Empty(t, stdin.String())
}

func TestProviderRejectsPDFAndBinaryAttachments(t *testing.T) {
	t.Parallel()
	for _, kind := range []agent.AttachmentKind{agent.AttachmentKindPDF, agent.AttachmentKindBinary} {
		err := qoderProvider{}.ValidateAttachment(agent.ClassifiedAttachment{Filename: "file", Kind: kind})
		require.Error(t, err, "Qoder's stream user frame cannot carry %s bytes", kind)
	}
}
