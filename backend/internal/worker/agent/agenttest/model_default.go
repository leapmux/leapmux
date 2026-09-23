package agenttest

import (
	"testing"

	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// DerivedModelDefault runs a model catalog through the live default-badge path of
// a manager on registry r, and returns the id that the ladder badges.
//
// A running agent reports the catalog as its model group, through
// agent.ModelOptionGroup's projection. The manager then re-derives that group's
// DefaultValue through the defaultModelIDForList ladder, as it does on every
// OptionGroups read of a running agent. So a ladder test exercises the production
// entry point rather than a test-only adapter.
func DerivedModelDefault(t *testing.T, r *agent.Registry, models []*agent.ModelInfo, provider leapmuxv1.AgentProvider) string {
	t.Helper()
	group := agent.ModelOptionGroup(models, "", nil)
	require.NotNil(t, group, "precondition: the catalog projects to a model group")
	m := agent.NewManager(r, nil)
	m.PutAgentForTest("catalog-probe", &GroupsAgent{groups: []*leapmuxv1.AvailableOptionGroup{group}})
	return optionids.GroupByID(m.OptionGroups("catalog-probe", provider, ""), agent.OptionIDModel).GetDefaultValue()
}
