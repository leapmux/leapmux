package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCodexBaseArgsAlwaysEnableMultiAgentV2(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"--enable", "multi_agent_v2", "app-server"}, codexBaseArgs())
}
