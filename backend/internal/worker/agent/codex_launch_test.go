package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCodexBaseArgsAlwaysEnableRequiredFeatures(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{
		"--enable", "multi_agent_v2",
		"--enable", "memories",
		"app-server",
	}, codexBaseArgs())
}
