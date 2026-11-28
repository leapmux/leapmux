package junie

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
)

func TestJunieProviderSupportsFork(t *testing.T) {
	t.Parallel()
	assert.True(t, Registration().Plugin.SupportsFork())
	var _ agent.ForkCapable = (*Agent)(nil)
}
