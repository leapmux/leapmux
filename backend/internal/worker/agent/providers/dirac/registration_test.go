package dirac

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

func TestRegistrationRecognizesNativeYolo(t *testing.T) {
	t.Parallel()
	registry, err := agent.NewRegistry(Registration())
	require.NoError(t, err)
	assert.True(t, registry.KnownOptionIDs(leapmuxv1.AgentProvider_AGENT_PROVIDER_DIRAC)["yolo"])
}
