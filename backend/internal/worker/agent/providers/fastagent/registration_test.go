package fastagent

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// A session that carries no stored mode takes the one mode the default setup
// reports, so the settings panel always has a selection to show.
func TestFastagentRegistrationStatesItsPermissionFallback(t *testing.T) {
	t.Parallel()
	assert.Equal(t, contracts.FastagentModeAgent, Registration().PermissionDefaults.Fallback)
	assert.Equal(t, []string{agent.OptionIDPermissionMode}, Registration().AdditionalOptionIDs)
}
