package acp

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
)

// TestSecondaryFallbackFrom pins which fallback SecondaryFallbackFrom finds for a
// channel. Each ACP provider's own test pins the list that its registration
// seeds; see acptest.AssertSecondaryFallback. The unmapped channel is the
// permission-mode family -- it tracks the mode on the native modes channel -- so
// it reads the same group as ModeChannelPermissionMode.
func TestSecondaryFallbackFrom(t *testing.T) {
	t.Parallel()

	groups := []*leapmuxv1.AvailableOptionGroup{{Id: agent.OptionIDPermissionMode, Options: []*leapmuxv1.AvailableOption{{Id: "ask"}}}}
	assert.Equal(t, groups[0].GetOptions(), SecondaryFallbackFrom(groups, ModeChannelUnmapped),
		"the unmapped channel is the permission-mode family and reads its group")
	assert.Nil(t, SecondaryFallbackFrom(nil, ModeChannelUnmapped),
		"a provider with no static groups has no fallback")
	assert.Nil(t, SecondaryFallbackFrom(nil, ModeChannelPermissionMode),
		"a provider with no static groups has no fallback")
	assert.Equal(t, groups[0].GetOptions(), SecondaryFallbackFrom(groups, ModeChannelPermissionMode),
		"a mapped channel serves the options of its own group")
	assert.Nil(t, SecondaryFallbackFrom(groups, ModeChannelPrimaryAgent),
		"a channel whose group is absent has no fallback")
}
