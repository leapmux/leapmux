//go:build unix

package codex

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildCodexInputBlocks_imageAttachment(t *testing.T) {
	t.Parallel()

	data := []byte{0xFF, 0xD8, 0xFF, 0xE0}
	attachments := []*leapmuxv1.Attachment{
		{Filename: "photo.jpg", MimeType: "image/jpeg", Data: data},
	}
	blocks, err := buildCodexInputBlocks("describe this", agent.ClassifyAttachments(attachments))
	require.NoError(t, err)
	require.Len(t, blocks, 2)

	textBlock := blocks[0]
	assert.Equal(t, "text", textBlock["type"])

	imgBlock := blocks[1]
	assert.Equal(t, "image", imgBlock["type"])
	expectedURI := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data)
	assert.Equal(t, expectedURI, imgBlock["url"])
}

func TestBuildCodexInputBlocks_textAttachment(t *testing.T) {
	t.Parallel()

	attachments := []*leapmuxv1.Attachment{
		{Filename: "report.csv", MimeType: "", Data: []byte("name,value\nfoo,1\n")},
	}
	blocks, err := buildCodexInputBlocks("", agent.ClassifyAttachments(attachments))
	require.NoError(t, err)
	require.Len(t, blocks, 1)
	assert.Equal(t, "text", blocks[0]["type"])
	assert.Contains(t, blocks[0]["text"], `<attached-file name="report.csv" mime-type="text/csv">`)
	assert.Contains(t, blocks[0]["text"], "name,value")
}

func TestBuildCodexInputBlocks_refusesPDF(t *testing.T) {
	t.Parallel()

	attachments := []*leapmuxv1.Attachment{
		{Filename: "doc.pdf", MimeType: "application/pdf", Data: []byte("%PDF")},
	}
	blocks, err := buildCodexInputBlocks("", agent.ClassifyAttachments(attachments))
	require.ErrorContains(t, err, "does not support PDF")
	require.Empty(t, blocks)
}

func TestNormalizeAttachmentsForProvider_RejectsUnsupportedBinary(t *testing.T) {
	t.Parallel()

	attachments := []*leapmuxv1.Attachment{
		{Filename: "archive.bin", MimeType: "", Data: []byte{0xff, 0xfe, 0xfd}},
	}
	_, err := agenttest.MustNewRegistry(Registration()).NormalizeAttachments(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, attachments)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "codex does not support binary attachments")
}

func TestNormalizeAttachmentsForProvider_CodexRejectsPDFAndBinary(t *testing.T) {
	t.Parallel()

	// Audio does not widen the PDF or unrelated-binary policy.
	cases := map[string]struct {
		att  *leapmuxv1.Attachment
		want string
	}{
		"pdf":    {&leapmuxv1.Attachment{Filename: "spec.pdf", MimeType: "application/pdf", Data: []byte("%PDF")}, "codex does not support PDF attachments"},
		"binary": {&leapmuxv1.Attachment{Filename: "archive.bin", MimeType: "application/octet-stream", Data: []byte{0xff, 0x00}}, "codex does not support binary attachments"},
	}
	for kind, tc := range cases {
		t.Run(kind, func(t *testing.T) {
			_, err := agenttest.MustNewRegistry(Registration()).NormalizeAttachments(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, []*leapmuxv1.Attachment{tc.att})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestBuildCodexInputBlocksAudio(t *testing.T) {
	t.Parallel()
	data := []byte{0, 1, 2, 3}
	attachments := []*leapmuxv1.Attachment{{Filename: "sample.wav", MimeType: "audio/wav", Data: data}}
	blocks, err := buildCodexInputBlocks("Listen.", agent.ClassifyAttachments(attachments))
	require.NoError(t, err)
	require.Len(t, blocks, 2)
	assert.Equal(t, "text", blocks[0]["type"])
	assert.Equal(t, "audio", blocks[1]["type"])
	assert.Equal(t, "data:audio/wav;base64,"+base64.StdEncoding.EncodeToString(data), blocks[1]["url"])
	_, err = agenttest.MustNewRegistry(Registration()).NormalizeAttachments(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, attachments)
	require.NoError(t, err)
}

func TestCodexAudioValidationRejectsEmptyAndUnrelatedBinary(t *testing.T) {
	t.Parallel()
	provider := Registration().Plugin
	for _, mime := range []string{"audio/wav", "audio/x-wav", "audio/wave", "audio/vnd.wave", "audio/mpeg", "audio/mp3", "audio/mp4", "audio/m4a", "audio/x-m4a", "audio/webm", "audio/ogg"} {
		attachment := agent.ClassifiedAttachment{Kind: contracts.AttachmentMediaKindAudio, MIMEType: mime, Filename: "audio", Data: []byte{0, 1}}
		require.NoError(t, provider.ValidateAttachment(attachment))
		attachment.Data = nil
		require.ErrorContains(t, provider.ValidateAttachment(attachment), "empty")
	}
	for _, mime := range []string{"audio/flac", "application/zip", "application/octet-stream", ""} {
		require.Error(t, provider.ValidateAttachment(agent.ClassifiedAttachment{Kind: contracts.AttachmentMediaKindBinary, MIMEType: mime, Data: []byte{0, 1}}))
	}
}

func TestCodexAudioModelValidation(t *testing.T) {
	t.Parallel()
	a := &Agent{availableModels: []*agent.ModelInfo{nil, {Id: "audio", SupportsAudioInput: true}, {Id: "text"}}}
	input := []map[string]interface{}{{"type": "audio", "url": "data:audio/wav;base64,AAE="}}
	require.NoError(t, a.validateCodexAudioInputLocked(input, "audio"))
	for _, model := range []string{"text", "", "unknown"} {
		require.ErrorContains(t, a.validateCodexAudioInputLocked(input, model), "does not accept audio")
	}
	require.NoError(t, a.validateCodexAudioInputLocked(nil, "unknown"))
	require.NoError(t, a.validateCodexAudioInputLocked([]map[string]interface{}{{"type": "text", "text": "Hello"}}, "text"))
}

func TestCodexAudioDispatchAndSteerKeepTheRunningModel(t *testing.T) {
	t.Parallel()
	var subject *Agent
	a, _, requests := newCodexAgentForRPC(t, func(method string) agenttest.RPCReply {
		if method == "turn/start" {
			subject.handleMainTurnStarted("audio-turn")
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	subject = a
	a.threadID = "audio-thread"
	a.model = "audio-model"
	a.availableModels = []*agent.ModelInfo{{Id: "audio-model", SupportsAudioInput: true}, {Id: "text-model"}}
	attachments := []*leapmuxv1.Attachment{{Filename: "sample.wav", MimeType: "audio/wav", Data: []byte{0, 1}}}
	require.NoError(t, a.SendInput("Listen.", attachments))
	assert.Equal(t, "audio-model", a.mainTurn.model)
	a.model = "text-model"
	require.NoError(t, a.SteerInput("Listen again.", attachments))
	recorded := requests()
	require.Len(t, recorded, 2)
	assert.Equal(t, "turn/start", recorded[0].Method)
	assert.Equal(t, "turn/steer", recorded[1].Method)
	for _, request := range recorded {
		input, ok := request.Params["input"].([]interface{})
		require.True(t, ok)
		require.Len(t, input, 2)
		assert.Equal(t, map[string]interface{}{"type": "audio", "url": "data:audio/wav;base64,AAE="}, input[1])
	}
}

func TestCodexRefusesAudioBeforeNativeDispatch(t *testing.T) {
	t.Parallel()
	for _, current := range []string{"text-model", "unknown", ""} {
		a, _, requests := newCodexAgentForRPC(t, func(string) agenttest.RPCReply { return agenttest.RPCReply{Result: json.RawMessage(`{}`)} })
		a.threadID = "audio-thread"
		a.model = current
		a.availableModels = []*agent.ModelInfo{{Id: "audio-model", SupportsAudioInput: true}, {Id: "text-model"}}
		attachments := []*leapmuxv1.Attachment{{Filename: "sample.wav", MimeType: "audio/wav", Data: []byte{0, 1}}}
		require.ErrorContains(t, a.SendInput("Listen.", attachments), "does not accept audio")
		assert.Nil(t, a.pendingStart)
		a.mainTurn = codexMainTurn{id: "text-turn", model: current}
		a.model = "audio-model"
		require.ErrorContains(t, a.SteerInput("Listen.", attachments), "does not accept audio")
		assert.Zero(t, a.mainTurn.steers)
		assert.Empty(t, a.pendingSteerCalls)
		assert.Empty(t, requests())
	}
}

func TestCodexInputEntryPointsRefuseInvalidAttachmentsBeforeNativeDispatch(t *testing.T) {
	t.Parallel()
	for _, entry := range []string{"send", "session send", "steer", "session steer"} {
		for _, attachment := range []*leapmuxv1.Attachment{
			{Filename: "unsupported.flac", MimeType: "audio/flac", Data: []byte{0, 1}},
			{Filename: "empty.wav", MimeType: "audio/wav"},
			{Filename: "document.pdf", MimeType: "application/pdf", Data: []byte("%PDF")},
			{Filename: "binary.bin", MimeType: "application/octet-stream", Data: []byte{0, 255}},
		} {
			t.Run(entry+"/"+attachment.Filename, func(t *testing.T) {
				t.Parallel()
				var subject *Agent
				a, _, requests := newCodexAgentForRPC(t, func(method string) agenttest.RPCReply {
					if method == "turn/start" {
						subject.handleMainTurnStarted("unexpected-turn")
					}
					return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
				})
				subject = a
				a.threadID, a.model = "audio-thread", "audio-model"
				a.availableModels = []*agent.ModelInfo{{Id: "audio-model", SupportsAudioInput: true}}
				originalInput := []map[string]interface{}{{"type": "text", "text": "original"}}
				if entry == "steer" || entry == "session steer" {
					a.mainTurn = codexMainTurn{id: "audio-turn", model: "audio-model", input: originalInput}
				}
				var err error
				attachments := []*leapmuxv1.Attachment{attachment}
				switch entry {
				case "send":
					err = a.SendInput("Keep the attachment.", attachments)
				case "session send":
					err = a.SendInputForSession("audio-thread", "Keep the attachment.", attachments)
				case "steer":
					err = a.SteerInput("Keep the attachment.", attachments)
				case "session steer":
					err = a.SteerInputForSession("audio-thread", "Keep the attachment.", attachments)
				}
				assert.Error(t, err)
				assert.Empty(t, requests())
				assert.Nil(t, a.pendingStart)
				assert.Zero(t, a.mainTurn.steers)
				assert.Empty(t, a.pendingSteerCalls)
				if entry == "steer" || entry == "session steer" {
					assert.Equal(t, originalInput, a.mainTurn.input)
				}
			})
		}
	}
}
