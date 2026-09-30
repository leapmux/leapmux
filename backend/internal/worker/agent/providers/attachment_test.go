package providers

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"

	"github.com/stretchr/testify/require"
)

func attachmentSet() []*leapmuxv1.Attachment {
	return []*leapmuxv1.Attachment{
		{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("hello")},
		{Filename: "diagram.png", MimeType: "image/png", Data: []byte{0x89, 0x50}},
		{Filename: "spec.pdf", MimeType: "application/pdf", Data: []byte("%PDF")},
		{Filename: "archive.bin", MimeType: "application/octet-stream", Data: []byte{0xff, 0x00}},
	}
}

func TestNormalizeAttachmentsForProvider_DefaultAcceptsEverything(t *testing.T) {
	t.Parallel()

	registry := Registry()
	attachments := attachmentSet()

	// Grok Build and an unknown provider inherit the default policy.
	for _, provider := range []leapmuxv1.AgentProvider{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GROK_BUILD,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED,
	} {
		t.Run(provider.String(), func(t *testing.T) {
			normalized, err := registry.NormalizeAttachments(provider, attachments)
			require.NoError(t, err)
			require.Len(t, normalized, 4)
		})
	}
}

func TestNormalizeAttachmentsForProvider_RestrictedProviders(t *testing.T) {
	t.Parallel()

	registry := Registry()
	attachments := attachmentSet()
	for _, tc := range []struct {
		provider leapmuxv1.AgentProvider
		allowed  int
	}{
		{provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR, allowed: 2},
		{provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE, allowed: 2},
		{provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO, allowed: 3},
		{provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, allowed: 3},
		{provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_QWEN_CODE, allowed: 3},
	} {
		t.Run(tc.provider.String(), func(t *testing.T) {
			normalized, err := registry.NormalizeAttachments(tc.provider, attachments[:tc.allowed])
			require.NoError(t, err)
			require.Len(t, normalized, tc.allowed)

			normalized, err = registry.NormalizeAttachments(tc.provider, attachments[:tc.allowed+1])
			require.ErrorContains(t, err, attachments[tc.allowed].Filename)
			require.Nil(t, normalized)
		})
	}
}
