package opencode

import (
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
)

func TestOpenCodeRefusesBinaryAttachments(t *testing.T) {
	t.Parallel()
	provider := Registration().Plugin
	for _, kind := range []contracts.AttachmentMediaKind{contracts.AttachmentMediaKindText, contracts.AttachmentMediaKindImage, contracts.AttachmentMediaKindPdf} {
		assert.NoError(t, provider.ValidateAttachment(agent.ClassifiedAttachment{Kind: kind, Filename: "input"}), kind)
	}
	assert.ErrorContains(t, provider.ValidateAttachment(agent.ClassifiedAttachment{Kind: contracts.AttachmentMediaKindBinary, Filename: "input.bin"}), "input.bin")
}

// The plugin states the child capabilities that the agent type implements. A
// subagent tab reads them before its root runs.
func TestPluginStatesTheChildCapabilitiesOfTheAgent(t *testing.T) {
	t.Parallel()
	// The family base stops a child session, and steers a running one.
	assert.Equal(t, agent.ChildCapabilities{AcceptsMessages: true, AcceptsInterrupt: true}, Registration().Plugin.ChildCapabilities(nil))
	agenttest.AssertChildCapabilities(t, Registration().Plugin, (*Agent)(nil))
}
