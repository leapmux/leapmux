package kiro

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

func TestKiroPromptParamsCarryImageBytesWithoutURI(t *testing.T) {
	t.Parallel()
	imageBytes := []byte{0x89, 'P', 'N', 'G', 0x00, 0xFE}
	blocks := acp.BuildPromptBlocks("Inspect the attached files.", []agent.ClassifiedAttachment{
		{Filename: "shot.png", MIMEType: "image/png", Kind: agent.AttachmentKindImage, Data: imageBytes},
		{Filename: "notes.txt", MIMEType: "text/plain", Kind: agent.AttachmentKindText, Data: []byte("unique-note-42")},
		{Filename: "document.pdf", MIMEType: "application/pdf", Kind: agent.AttachmentKindPDF, Data: []byte("%PDF-1.1")},
	})
	params := map[string]any{"prompt": blocks}
	hooks := (&Agent{}).configure(agent.Options{})
	require.NotNil(t, hooks.PromptParams)
	hooks.PromptParams(params)

	got, ok := params["prompt"].([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, got, 4)
	assert.Equal(t, map[string]interface{}{"type": "text", "text": "Inspect the attached files."}, got[0])
	assert.Equal(t, "image", got[1]["type"])
	assert.Equal(t, "image/png", got[1]["mimeType"])
	assert.Equal(t, base64.StdEncoding.EncodeToString(imageBytes), got[1]["data"])
	assert.NotContains(t, got[1], "uri", "Kiro v3 drops an image block that also states a URI")
	assert.Equal(t, map[string]interface{}{
		"type": "resource",
		"resource": map[string]interface{}{
			"uri": "notes.txt", "mimeType": "text/plain", "text": "unique-note-42",
		},
	}, got[2])
	assert.Equal(t, map[string]interface{}{
		"type": "resource",
		"resource": map[string]interface{}{
			"uri": "document.pdf", "mimeType": "application/pdf", "blob": base64.StdEncoding.EncodeToString([]byte("%PDF-1.1")),
		},
	}, got[3])
}

func TestKiroPromptParamsAcceptAbsentPromptAndImageURI(t *testing.T) {
	t.Parallel()
	hooks := (&Agent{}).configure(agent.Options{})
	require.NotNil(t, hooks.PromptParams)
	for _, params := range []map[string]any{
		{},
		{"prompt": "not blocks"},
		{"prompt": []map[string]interface{}{{"type": "image", "mimeType": "image/png", "data": "aW1hZ2U="}}},
	} {
		assert.NotPanics(t, func() { hooks.PromptParams(params) })
	}
}
