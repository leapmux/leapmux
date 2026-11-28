package qoder

import (
	"encoding/json"
	"fmt"
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

func customCatalogQoderAgent(t *testing.T, raw string) *Agent {
	t.Helper()
	a := newOfflineAgent(t, &agenttest.Sink{})
	models, err := qoderCustomModels(json.RawMessage(raw))
	require.NoError(t, err)
	a.customModels = models
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
	a.HandleOutput([]byte(`{"type":"system","subtype":"available_models_update","models":[{"value":"model-a","displayName":"Model A","isEnabled":true},{"value":"model-b","displayName":"Model B","isEnabled":true}],"currentModel":"model-a"}`))

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

func TestCustomCatalogRemainsWhenNativeCatalogIsEmpty(t *testing.T) {
	t.Parallel()
	a := customCatalogQoderAgent(t, `{"configs":[{"providerId":"mockprov","models":[{"id":"first","displayName":"First Model"},{"id":"second","displayName":"Second Model"}]}]}`)
	a.model = "mockprov/first"
	a.HandleOutput([]byte(`{"type":"system","subtype":"available_models_update","models":[],"currentModel":"mockprov/first"}`))
	assert.Equal(t, []string{"mockprov/first", "mockprov/second"}, qoderModelIDs(a))
	for _, group := range a.OptionGroups() {
		if group.Id == agent.OptionIDModel {
			assert.Equal(t, "mockprov/first", group.CurrentValue)
			assert.Equal(t, "First Model", group.Options[0].Name)
			assert.Equal(t, "Second Model", group.Options[1].Name)
		}
	}
}

func TestCustomCatalogComplementsNativeModels(t *testing.T) {
	t.Parallel()
	a := customCatalogQoderAgent(t, `{"configs":[{"providerId":"mockprov","models":[{"id":"first"},{"id":"second"}]}]}`)
	a.HandleOutput([]byte(`{"type":"system","subtype":"available_models_update","models":[{"value":"native-one","displayName":"Native One","isEnabled":true},{"value":"native-two","displayName":"Native Two","isEnabled":true}],"currentModel":"native-one"}`))
	assert.Equal(t, []string{"native-one", "native-two", "mockprov/first", "mockprov/second"}, qoderModelIDs(a))
}

func TestCustomModelsDeduplicateAndSortProviders(t *testing.T) {
	t.Parallel()
	models, err := qoderCustomModels(json.RawMessage(`{"configs":[{"providerId":"zeta","models":[{"id":"m","displayName":"Zeta"},{"id":"m","displayName":"Duplicate"}]},{"providerId":"alpha","models":[{"id":"first","displayName":"First"},{"id":"implicit"}]}]}`))
	require.NoError(t, err)
	require.Len(t, models, 3)
	assert.Equal(t, []string{"alpha/first", "alpha/implicit", "zeta/m"}, []string{models[0].id, models[1].id, models[2].id})
	assert.Equal(t, "Zeta", models[2].displayName)
	assert.Equal(t, "implicit", models[1].displayName)
}

func TestCustomCatalogRejectsMalformedReplies(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{`{`, `null`, `{}`, `{"configs":null}`, `{"configs":{}}`, `{"configs":[{"providerId":1}]}`, `{"configs":[{"providerId":"p","models":[{"id":false}]}]}`} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			_, err := qoderCustomModels(json.RawMessage(raw))
			require.Error(t, err)
		})
	}
	empty, err := qoderCustomModels(json.RawMessage(`{"configs":[]}`))
	require.NoError(t, err)
	assert.Empty(t, empty)
	ignored, err := qoderCustomModels(json.RawMessage(`{"configs":[{"providerId":"","models":[{"id":"m"}]},{"providerId":"p","models":[{"id":""},{"id":"  "}]}]}`))
	require.NoError(t, err)
	assert.Empty(t, ignored)
}

func TestUpdateSettingsSetsTheModelOnTheRunningSession(t *testing.T) {
	t.Parallel()
	a := newOfflineAgent(t, &agenttest.Sink{})
	a.HandleOutput([]byte(`{"type":"system","subtype":"available_models_update","models":[{"value":"model-a","displayName":"Model A","isEnabled":true},{"value":"model-b","displayName":"Model B","isEnabled":true}],"currentModel":"model-a"}`))
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
		return `{"type":"user","session_id":"session-1","message":{"role":"user","content":` + content + `}}`
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

			receipt, err := a.SendInput(tc.text, tc.attachments)
			require.NoError(t, err)
			require.NotEmpty(t, receipt.InputID)
			var want map[string]any
			require.NoError(t, json.Unmarshal([]byte(tc.want), &want))
			want["uuid"] = receipt.InputID
			wantRaw, err := json.Marshal(want)
			require.NoError(t, err)
			assert.JSONEq(t, string(wantRaw), sentUserFrame(t, stdin))
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

			require.NoError(t, agenttest.SteerInput(a, agent.SteerRequest{Content: tc.text, Attachments: tc.attachments}))
			assert.JSONEq(t, tc.want, sentUserFrame(t, stdin))
		})
	}
}

func TestSendInputRejectsAnUnsupportedAttachmentBeforeTheTurnStarts(t *testing.T) {
	t.Parallel()
	a := newOfflineAgent(t, &agenttest.Sink{})
	stdin := &agenttest.Stdin{}
	a.SetStdinForTest(stdin)

	err := agenttest.SendInput(a, "Read both.", []*leapmuxv1.Attachment{testPNGAttachment(), testPDFAttachment()})
	require.ErrorContains(t, err, "paper.pdf")
	assert.Empty(t, stdin.String(), "a rejected prompt must write no partial frame")
	a.mu.Lock()
	active := a.active
	a.mu.Unlock()
	assert.False(t, active, "a rejected prompt must not start a turn")

	receipt, err := a.SendInput("Describe this.", nil)
	require.NoError(t, err, "the next prompt must not see a busy agent")
	assert.JSONEq(t, fmt.Sprintf(`{"type":"user","uuid":%q,"session_id":"session-1","message":{"role":"user","content":"Describe this."}}`, receipt.InputID), sentUserFrame(t, stdin))
}

func TestSteerInputRejectsAnUnsupportedAttachment(t *testing.T) {
	t.Parallel()
	a := newOfflineAgent(t, &agenttest.Sink{})
	stdin := &agenttest.Stdin{}
	a.SetStdinForTest(stdin)
	a.mu.Lock()
	a.active = true
	a.mu.Unlock()

	err := agenttest.SteerInput(a, agent.SteerRequest{Content: "Read both.", Attachments: []*leapmuxv1.Attachment{testPNGAttachment(), testPDFAttachment()}})
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

	err := agenttest.SteerInput(a, agent.SteerRequest{Content: "Look at this too.", Attachments: []*leapmuxv1.Attachment{testPNGAttachment()}})
	require.ErrorIs(t, err, agent.ErrNoActiveTurn)
	assert.Empty(t, stdin.String())
}

func TestProviderRejectsPDFAndBinaryAttachments(t *testing.T) {
	t.Parallel()
	for _, kind := range []contracts.AttachmentMediaKind{contracts.AttachmentMediaKindPdf, contracts.AttachmentMediaKindBinary} {
		err := qoderProvider{}.ValidateAttachment(agent.ClassifiedAttachment{Filename: "file", Kind: kind})
		require.Error(t, err, "Qoder's stream user frame cannot carry %s bytes", kind)
	}
}
