package acp

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
)

// TestSecondaryFallbackFrom pins which fallback SecondaryFallbackFrom finds for a
// channel. Each ACP provider's own test pins the list that its registration
// seeds; see acptest.AssertSecondaryFallback.
func TestSecondaryFallbackFrom(t *testing.T) {
	t.Parallel()

	groups := []*leapmuxv1.AvailableOptionGroup{{Id: agent.OptionIDPermissionMode, Options: []*leapmuxv1.AvailableOption{{Id: "ask"}}}}
	assert.Nil(t, SecondaryFallbackFrom(nil, ModeChannelUnmapped),
		"the unmapped channel has no secondary fallback")
	assert.Nil(t, SecondaryFallbackFrom(groups, ModeChannelUnmapped),
		"the unmapped channel has no secondary fallback even when groups exist")
	assert.Nil(t, SecondaryFallbackFrom(nil, ModeChannelPermissionMode),
		"a provider with no static groups has no fallback")
	assert.Equal(t, groups[0].GetOptions(), SecondaryFallbackFrom(groups, ModeChannelPermissionMode),
		"a mapped channel serves the options of its own group")
	assert.Nil(t, SecondaryFallbackFrom(groups, ModeChannelPrimaryAgent),
		"a channel whose group is absent has no fallback")
}
