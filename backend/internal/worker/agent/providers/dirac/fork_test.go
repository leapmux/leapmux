package dirac

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
)

var _ agent.ForkCapable = (*Agent)(nil)

func TestDiracProviderSupportsFork(t *testing.T) {
	t.Parallel()
	assert.True(t, Registration().Plugin.SupportsFork())
	var fc agent.ForkCapable = &Agent{}
	assert.False(t, fc.SupportsNativeFork(), "native fork capability must be false before ACP initialize advertises it")
}
