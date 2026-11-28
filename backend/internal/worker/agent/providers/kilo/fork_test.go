package kilo

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
)

var _ agent.ForkCapable = (*Agent)(nil)

func TestKiloProviderSupportsFork(t *testing.T) {
	t.Parallel()
	assert.True(t, Registration().Plugin.SupportsFork())
	var fc agent.ForkCapable = newAgent(false)
	assert.False(t, fc.SupportsNativeFork(), "native fork capability must be false before ACP initialize advertises it")
}
