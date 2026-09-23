//go:build unix

package reasonix

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeAttachmentsForProvider_ReasonixAcceptsTextOnly(t *testing.T) {
	t.Parallel()

	attachments := []*leapmuxv1.Attachment{
		{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("hello")},
		{Filename: "config.json", MimeType: "application/json", Data: []byte("{}")},
	}

	normalized, err := agenttest.MustNewRegistry(Registration()).NormalizeAttachments(leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX, attachments)
	require.NoError(t, err)
	require.Len(t, normalized, 2)
}

func TestNormalizeAttachmentsForProvider_ReasonixRejectsNonText(t *testing.T) {
	t.Parallel()

	// Reasonix is text-only (it advertises image:false and drops non-text
	// blocks), so image, PDF, and binary attachments are rejected up front.
	cases := map[string]*leapmuxv1.Attachment{
		"image":  {Filename: "diagram.png", MimeType: "image/png", Data: []byte{0x89, 0x50}},
		"pdf":    {Filename: "spec.pdf", MimeType: "application/pdf", Data: []byte("%PDF")},
		"binary": {Filename: "archive.bin", MimeType: "application/octet-stream", Data: []byte{0xff, 0x00}},
	}
	for kind, att := range cases {
		t.Run(kind, func(t *testing.T) {
			_, err := agenttest.MustNewRegistry(Registration()).NormalizeAttachments(leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX, []*leapmuxv1.Attachment{att})
			require.Error(t, err, "reasonix must reject a %s attachment", kind)
			assert.Contains(t, err.Error(), "reasonix only supports text attachments")
		})
	}
}
