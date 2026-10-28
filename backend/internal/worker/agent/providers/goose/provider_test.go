package goose

import (
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
)

func TestGooseRefusesEmbeddedBlobAttachments(t *testing.T) {
	t.Parallel()
	provider := Registration().Plugin
	for _, kind := range []contracts.AttachmentMediaKind{contracts.AttachmentMediaKindText, contracts.AttachmentMediaKindImage} {
		assert.NoError(t, provider.ValidateAttachment(agent.ClassifiedAttachment{Kind: kind, Filename: "input"}), kind)
	}
	for _, kind := range []contracts.AttachmentMediaKind{contracts.AttachmentMediaKindPdf, contracts.AttachmentMediaKindBinary} {
		assert.ErrorContains(t, provider.ValidateAttachment(agent.ClassifiedAttachment{Kind: kind, Filename: "input"}), string(kind))
	}
}

// The plugin states the child capabilities that the agent type implements. A
// subagent tab reads them before its root runs.
func TestPluginStatesTheChildCapabilitiesOfTheAgent(t *testing.T) {
	t.Parallel()
	agenttest.AssertChildCapabilities(t, Registration().Plugin, (*Agent)(nil))
}
