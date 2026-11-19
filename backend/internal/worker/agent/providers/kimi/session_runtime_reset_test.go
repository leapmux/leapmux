package kimi

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKimiClearContextRequiresPrivateRuntimeReplacement(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	sessionID := rig.sessionID()
	before := rig.fake.routes()
	got, err := rig.agent.ClearContext()
	require.ErrorIs(t, err, agent.ErrContextClearUnsupported)
	assert.Empty(t, got)
	assert.Equal(t, sessionID, rig.sessionID(), "only the Worker replaces the runtime and its durable session")
	assert.Equal(t, before, rig.fake.routes(), "an unsupported in-process clear must not start or abandon another native session")
}
