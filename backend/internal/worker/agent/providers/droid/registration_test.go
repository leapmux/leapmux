package droid

import (
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultModeDescriptionStatesApproval(t *testing.T) {
	t.Parallel()
	var description string
	for _, option := range permissionModeGroup.GetOptions() {
		if option.GetId() == contracts.DroidModeDefault {
			description = option.GetDescription()
			break
		}
	}
	require.NotEmpty(t, description)
	assert.NotContains(t, strings.ToLower(description), "read-only")
	assert.Contains(t, description, "Ask before")
}
