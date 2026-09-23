//go:build unix

package zcode

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ZCode's normalizer knows image, video, file and audio -- and a PDF, having no kind of
// its own, arrives as a generic `file`: a small one is decoded as text and reaches the
// model as binary garbage, and a large one is dropped with no message. So both are
// refused at the boundary, before a send that would silently lose the file.
//
// An IMAGE passes here on purpose. Whether it is acceptable depends on the current
// model's declared input modalities, which this stateless check cannot know, so that
// refusal lives on the send path (see attachments_test.go).
func TestNormalizeAttachmentsForProvider_ZCodeRejectsPDFAndBinaryButNotImages(t *testing.T) {
	t.Parallel()

	accepted := []*leapmuxv1.Attachment{
		{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("hello")},
		{Filename: "diagram.png", MimeType: "image/png", Data: []byte{0x89, 0x50}},
	}
	normalized, err := agenttest.MustNewRegistry(Registration()).NormalizeAttachments(leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE, accepted)
	require.NoError(t, err)
	require.Len(t, normalized, 2)

	refused := map[string]*leapmuxv1.Attachment{
		"pdf":    {Filename: "spec.pdf", MimeType: "application/pdf", Data: []byte("%PDF")},
		"binary": {Filename: "archive.bin", MimeType: "application/octet-stream", Data: []byte{0xff, 0x00}},
	}
	for name, attachment := range refused {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := agenttest.MustNewRegistry(Registration()).NormalizeAttachments(
				leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE, []*leapmuxv1.Attachment{attachment})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "zcode does not support")
			assert.Contains(t, err.Error(), attachment.GetFilename(),
				"the message must name the file, because the user picked it by name")
		})
	}
}
