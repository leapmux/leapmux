package dirac

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

func TestPromptParamsCarriesTextBytesAndPreservesImages(t *testing.T) {
	t.Parallel()
	blocks := acp.BuildPromptBlocks("Read the note and picture.", []agent.ClassifiedAttachment{
		{Filename: "note.txt", MIMEType: "text/plain", Kind: agent.AttachmentKindText, Data: []byte("unique-note-42")},
		{Filename: "shot.png", MIMEType: "image/png", Kind: agent.AttachmentKindImage, Data: []byte{1, 2, 3}},
	})
	params := map[string]any{"prompt": blocks}

	diracPromptParams(params)
	got, ok := params["prompt"].([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, got, 3)
	assert.Equal(t, "text", got[1]["type"])
	assert.Contains(t, got[1]["text"], "note.txt")
	assert.Contains(t, got[1]["text"], "unique-note-42")
	assert.Equal(t, blocks[2], got[2], "the image keeps its ACP content block")
}

func TestProviderRejectsPDFAndBinaryAttachments(t *testing.T) {
	t.Parallel()
	for _, kind := range []agent.AttachmentKind{agent.AttachmentKindPDF, agent.AttachmentKindBinary} {
		err := diracProvider{}.ValidateAttachment(agent.ClassifiedAttachment{Filename: "file", Kind: kind})
		require.Error(t, err)
	}
}
