package junie

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistrationRecognizesBraveMode(t *testing.T) {
	t.Parallel()

	registry, err := agent.NewRegistry(Registration())
	require.NoError(t, err)
	ids := registry.KnownOptionIDs(leapmuxv1.AgentProvider_AGENT_PROVIDER_JUNIE)
	assert.True(t, ids["brave_mode"])
	assert.True(t, ids["permissionMode"])
	assert.False(t, ids["brave-auto"])
}
