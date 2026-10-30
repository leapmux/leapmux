package providerkit

import (
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientAutoApproveGroupKeepsTheClientKeyAndIndependentOptions(t *testing.T) {
	t.Parallel()
	first, second := ClientAutoApproveGroup(), ClientAutoApproveGroup()
	assert.Equal(t, contracts.ClientApprovalOptionAutoApprove, first.Id)
	assert.Equal(t, "false", first.CurrentValue)
	require.Len(t, first.Options, 2)
	assert.Equal(t, "false", first.Options[0].Id)
	assert.Equal(t, "false", first.DefaultValue)
	assert.Equal(t, "true", first.Options[1].Id)
	first.Options[0].Name = "Changed by another provider"
	first.CurrentValue = "true"
	assert.Equal(t, "Off", second.Options[0].Name)
	assert.Equal(t, "false", second.CurrentValue)
}
