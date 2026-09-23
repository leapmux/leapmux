package codex

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestCodexMessageContentConformance(t *testing.T) {
	t.Parallel()
	agenttest.RunSupplementConformance(t, "codex_message_content_conformance.json", codexProvider{}.ResolveProviderData)
}
