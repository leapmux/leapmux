package fastagent

import (
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// The plugin states the child capabilities that the agent type implements. A
// subagent tab reads them before its root runs.
func TestPluginStatesTheChildCapabilitiesOfTheAgent(t *testing.T) {
	t.Parallel()
	agenttest.AssertChildCapabilities(t, Registration().Plugin, (*Agent)(nil))
}

func TestProviderRejectsOtherBinaryAttachments(t *testing.T) {
	t.Parallel()
	provider := fastagentProvider{}
	for _, kind := range []contracts.AttachmentMediaKind{contracts.AttachmentMediaKindText, contracts.AttachmentMediaKindImage, contracts.AttachmentMediaKindPdf} {
		assert.NoError(t, provider.ValidateAttachment(agent.ClassifiedAttachment{Filename: "file", Kind: kind}))
	}
	require.Error(t, provider.ValidateAttachment(agent.ClassifiedAttachment{Filename: "file.bin", Kind: contracts.AttachmentMediaKindBinary}))
}
