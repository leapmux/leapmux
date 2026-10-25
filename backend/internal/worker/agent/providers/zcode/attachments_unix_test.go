//go:build unix

package zcode

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ZCode's normalizer keeps the bytes of an image and of a PDF, and has no kind that
// keeps the bytes of a binary file. So a binary is refused at the boundary, before a
// send that would lose the file.
//
// An IMAGE and a PDF pass here on purpose. Whether one is acceptable depends on the
// current model's declared input modalities, which this stateless check cannot know,
// so that refusal lives on the send path (see attachments_test.go).
func TestNormalizeAttachmentsForProvider_ZCodeRejectsBinaryButNotImagesOrPDFs(t *testing.T) {
	t.Parallel()

	accepted := []*leapmuxv1.Attachment{
		{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("hello")},
		{Filename: "diagram.png", MimeType: "image/png", Data: []byte{0x89, 0x50}},
		{Filename: "spec.pdf", MimeType: "application/pdf", Data: []byte("%PDF")},
	}
	normalized, err := agenttest.MustNewRegistry(Registration()).NormalizeAttachments(leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE, accepted)
	require.NoError(t, err)
	require.Len(t, normalized, 3)

	binary := &leapmuxv1.Attachment{Filename: "archive.bin", MimeType: "application/octet-stream", Data: []byte{0xff, 0x00}}
	_, err = agenttest.MustNewRegistry(Registration()).NormalizeAttachments(
		leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE, []*leapmuxv1.Attachment{binary})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "zcode does not support")
	assert.Contains(t, err.Error(), binary.GetFilename(),
		"the message must name the file, because the user picked it by name")
}
