//go:build unix

package cursor

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManager_AvailableModelsFallsBackToCursorDefaults(t *testing.T) {
	m := agent.NewManager(agenttest.MustNewRegistry(Registration()), nil)

	groups := m.OptionGroups("missing-agent", leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR, "")
	modelGroup := optionids.GroupByID(groups, agent.OptionIDModel)
	require.NotNil(t, modelGroup)
	require.NotEmpty(t, modelGroup.GetOptions())
	assert.Equal(t, "auto", modelGroup.GetOptions()[0].GetId())
	assert.Equal(t, "auto", modelGroup.GetDefaultValue())
}
