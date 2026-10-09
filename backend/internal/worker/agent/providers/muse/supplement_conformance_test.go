package muse

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestMuseMessageContentConformance(t *testing.T) {
	t.Parallel()
	agenttest.RunSupplementConformance(t, "muse_message_content_conformance.json", museProvider{}.ResolveProviderData)
}
