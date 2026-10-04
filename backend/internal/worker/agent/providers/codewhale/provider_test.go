package codewhale

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestProviderClassifiesStatusNotices(t *testing.T) {
	t.Parallel()
	provider := codewhaleProvider{}

	status := provider.Classify(itemEvent(3, "item.completed", "item_1", contracts.CodewhaleItemKindStatus, "Checkpoint saved", nil))
	assert.Equal(t, agent.NotificationClassification{Kind: agent.NotificationKindStatus, Key: "codewhale:status"}, status)

	for name, raw := range map[string][]byte{
		"a compaction":    itemEvent(3, "item.completed", "item_1", contracts.CodewhaleItemKindContextCompaction, "done", nil),
		"a failed status": itemEvent(3, "item.failed", "item_1", contracts.CodewhaleItemKindStatus, "x", nil),
		"another event":   runtimeEvent(3, "sandbox.denied", testTurnID, "", map[string]any{"tool_name": "bash"}),
		"a broken frame":  []byte("{not json"),
	} {
		assert.Equal(t, agent.NotificationClassification{}, provider.Classify(raw), name)
	}
}

func TestProviderRecognizesTheInterruptFrame(t *testing.T) {
	t.Parallel()
	provider := codewhaleProvider{}
	assert.True(t, provider.IsInterrupt(`{"frame":"interrupt"}`))
	assert.False(t, provider.IsInterrupt(`{"frame":"approval"}`))
	assert.False(t, provider.IsInterrupt(`interrupt`))
	assert.False(t, provider.IsInterrupt(``))
}

func TestProviderValidatesAttachments(t *testing.T) {
	t.Parallel()
	provider := codewhaleProvider{}
	assert.NoError(t, provider.ValidateAttachment(agent.ClassifiedAttachment{Kind: agent.AttachmentKindText, Filename: "a.txt"}))
	assert.NoError(t, provider.ValidateAttachment(agent.ClassifiedAttachment{Kind: agent.AttachmentKindImage, Filename: "a.png"}))
	assert.ErrorContains(t, provider.ValidateAttachment(agent.ClassifiedAttachment{Kind: agent.AttachmentKindPDF, Filename: "a.pdf"}), "Codewhale does not support PDF attachments: a.pdf")
	assert.ErrorContains(t, provider.ValidateAttachment(agent.ClassifiedAttachment{Kind: agent.AttachmentKindBinary, Filename: "a.bin"}), "Codewhale does not support binary attachments: a.bin")
	// The static check knows no model and no message, so an image passes it at
	// any size. buildTurnInput applies the size limits when a message is sent.
	assert.NoError(t, provider.ValidateAttachment(agent.ClassifiedAttachment{Kind: agent.AttachmentKindImage, Filename: "big.png", Data: make([]byte, maxTurnImageBytes+1)}))
}

func TestProviderConformance(t *testing.T) {
	t.Parallel()
	provider := Registration().Plugin
	t.Run("keeps a response that has no request", func(t *testing.T) {
		t.Parallel()
		agenttest.AssertPreservesTheResponseWithoutARequest(t, provider)
	})
	t.Run("withholds a response to a malformed request", func(t *testing.T) {
		t.Parallel()
		agenttest.AssertWithholdsTheResponseForAMalformedRequest(t, provider)
	})
	t.Run("follows the token resume rule", func(t *testing.T) {
		t.Parallel()
		agenttest.AssertTokenResumeRule(t, provider)
	})
}

func TestProviderResolvesAThreadIDAsItsOwnHandle(t *testing.T) {
	t.Parallel()
	resolved, err := codewhaleProvider{}.ResolveResumeHandle(testThreadID, "")
	require.NoError(t, err)
	assert.Equal(t, testThreadID, resolved)
}

// The plugin states the child capabilities that the agent type implements. A
// subagent tab reads them before its root runs.
func TestPluginStatesTheChildCapabilitiesOfTheAgent(t *testing.T) {
	t.Parallel()
	agenttest.AssertChildCapabilities(t, Registration().Plugin, (*Agent)(nil))
}
