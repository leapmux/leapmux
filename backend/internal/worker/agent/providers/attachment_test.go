package providers

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"

	"github.com/stretchr/testify/require"
)

func TestNormalizeAttachmentsForProvider_DefaultAcceptsEverything(t *testing.T) {
	t.Parallel()

	registry := Registry()

	// Cursor, Kilo, Goose, OpenCode (all ACP with no restrictive hook) and an unknown/UNSPECIFIED
	// provider (via the ProviderDefaults that Registry.Plugin answers) accept the full text+image+PDF+binary set -- the
	// switch-default behavior preserved after moving policy behind the Provider interface.
	fullSet := []*leapmuxv1.Attachment{
		{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("hello")},
		{Filename: "diagram.png", MimeType: "image/png", Data: []byte{0x89, 0x50}},
		{Filename: "spec.pdf", MimeType: "application/pdf", Data: []byte("%PDF")},
		{Filename: "archive.bin", MimeType: "application/octet-stream", Data: []byte{0xff, 0x00}},
	}
	for _, provider := range []leapmuxv1.AgentProvider{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED,
	} {
		t.Run(provider.String(), func(t *testing.T) {
			normalized, err := registry.NormalizeAttachments(provider, fullSet)
			require.NoError(t, err)
			require.Len(t, normalized, 4)
		})
	}
}
