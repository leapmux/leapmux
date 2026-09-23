package acp

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestACPMessageContentConformance(t *testing.T) {
	t.Parallel()
	agenttest.RunSupplementConformance(t, "acp_message_content_conformance.json", ResolveMessageContent)
}
