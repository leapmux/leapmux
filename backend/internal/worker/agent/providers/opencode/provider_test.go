package opencode

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
)

func TestNativeProviderClassifiesNotifications(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		raw  string
		want agent.NotificationClassification
	}{
		{`{"type":"session.retry.scheduled"}`, agent.APIRetryNotification()},
		{`{"type":"session.created"}`, agent.StatusNotification()},
		{`{"type":"session.forked"}`, agent.StatusNotification()},
		{`{"type":"session.renamed"}`, agent.StatusNotification()},
		{`{"type":"session.metadata.updated"}`, agent.StatusNotification()},
		{`{"type":"session.permissions"}`, agent.StatusNotification()},
		{`{"type":"session.viewed"}`, agent.StatusNotification()},
		{`{"type":"session.archived"}`, agent.StatusNotification()},
		{`{"type":"session.unarchived"}`, agent.StatusNotification()},
		{`{"type":"session.instructions.updated"}`, agent.StatusNotification()},
		{`{"type":"model-switched"}`, agent.StatusNotification()},
		{`{"type":"session.step.started"}`, agent.StatusNotification()},
		{`{"type":"session.step.first-token"}`, agent.StatusNotification()},
		{`{"type":"session.step.streamed"}`, agent.StatusNotification()},
		{`{"type":"session.step.ended"}`, agent.StatusNotification()},
		{`{"type":"session.step.failed"}`, agent.StatusNotification()},
		{`{"type":"session.usage.updated"}`, agent.StatusNotification()},
		{`{"type":"session.compaction.started"}`, agent.StatusNotification()},
		{`{"type":"session.compaction.delta"}`, agent.StatusNotification()},
		{`{"type":"session.compaction.ended"}`, agent.CompactionBoundaryNotification()},
		{`{"type":"session.compaction.failed"}`, agent.CompactionBoundaryNotification()},
		{`{"type":"compaction","status":"running"}`, agent.StatusNotification()},
		{`{"type":"compaction","status":"completed"}`, agent.CompactionBoundaryNotification()},
		{`{"type":"compaction","status":"failed"}`, agent.CompactionBoundaryNotification()},
		{`{"type":"session.execution.succeeded"}`, agent.NotificationClassification{}},
		{`{"type":"compaction","status":"unknown"}`, agent.NotificationClassification{}},
		{`{`, agent.NotificationClassification{}},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, Registration().Plugin.Classify(json.RawMessage(tc.raw)))
		})
	}
}

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
	// The native agent stops a child session and steers a running child.
	assert.Equal(t, agent.ChildCapabilities{AcceptsMessages: true, AcceptsInterrupt: true}, Registration().Plugin.ChildCapabilities(nil))
	agenttest.AssertChildCapabilities(t, Registration().Plugin, (*Agent)(nil))
}
