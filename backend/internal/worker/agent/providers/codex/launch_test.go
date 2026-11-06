package codex

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCodexBaseArgsAlwaysEnableRequiredFeatures(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{
		"--enable", "multi_agent_v2",
		"--enable", "memories",
		"--enable", "code_mode_interrupt",
		"app-server",
		"--listen", "stdio://",
	}, codexBaseArgs())
	// One independently owned app-server process per tab: never a daemon or
	// proxy lifecycle command, whose shared process no tab owns.
	assert.NotContains(t, codexBaseArgs(), "daemon")
	assert.NotContains(t, codexBaseArgs(), "proxy")
}
