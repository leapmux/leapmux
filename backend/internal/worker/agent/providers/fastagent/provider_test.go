package fastagent

import (
	"testing"

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
	for _, kind := range []agent.AttachmentKind{agent.AttachmentKindText, agent.AttachmentKindImage, agent.AttachmentKindPDF} {
		assert.NoError(t, provider.ValidateAttachment(agent.ClassifiedAttachment{Filename: "file", Kind: kind}))
	}
	require.Error(t, provider.ValidateAttachment(agent.ClassifiedAttachment{Filename: "file.bin", Kind: agent.AttachmentKindBinary}))
}
