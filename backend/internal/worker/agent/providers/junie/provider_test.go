package junie

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

func TestProviderRejectsPDFAndBinaryAttachments(t *testing.T) {
	t.Parallel()
	provider := junieProvider{}
	for _, kind := range []agent.AttachmentKind{agent.AttachmentKindText, agent.AttachmentKindImage} {
		assert.NoError(t, provider.ValidateAttachment(agent.ClassifiedAttachment{Filename: "file", Kind: kind}))
	}
	for _, kind := range []agent.AttachmentKind{agent.AttachmentKindPDF, agent.AttachmentKindBinary} {
		require.Error(t, provider.ValidateAttachment(agent.ClassifiedAttachment{Filename: "file", Kind: kind}))
	}
}
