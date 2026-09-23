package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestAccountDefaultModelEntry pins the shape every provider's account-default
// row must have. The absent SupportedEfforts is load-bearing, not an oversight:
// EffortGroupForModel returns nil on an empty effort list, which is what hides
// the effort menu until the CLI resolves a concrete model -- and that in turn is
// what keeps a fresh launch from forwarding an effort the resolved model may not
// offer. One helper makes that omission impossible for a new provider to lose.
func TestAccountDefaultModelEntry(t *testing.T) {
	t.Parallel()

	entry := AccountDefaultModelEntry("Use the account's default model")

	assert.Equal(t, DefaultModelSentinel, entry.Id)
	assert.Equal(t, "Default (recommended)", entry.DisplayName, "every provider shows one label")
	assert.Equal(t, "Use the account's default model", entry.Description)
	assert.True(t, entry.IsDefault, "a new tab starts on the account default")
	assert.Empty(t, entry.SupportedEfforts, "the effort menu appears only once the model resolves")
	assert.Zero(t, entry.ContextWindow, "an unresolved model has no context window to report")
	assert.False(t, entry.Hidden, "the account default must be selectable")
}

// TestFindAvailableModel verifies the lookup matches by id and, critically,
// tolerates nil entries in the slice (its callers treat the catalog as possibly
// nil-bearing, so the lookup must not panic on one).
func TestFindAvailableModel(t *testing.T) {
	t.Parallel()

	models := []*ModelInfo{
		nil,
		{Id: "opus"},
		nil,
		{Id: "sonnet"},
	}

	assert.Equal(t, "sonnet", FindAvailableModel(models, "sonnet").GetId())
	assert.Equal(t, "opus", FindAvailableModel(models, "opus").GetId())
	assert.Nil(t, FindAvailableModel(models, "missing"), "no match returns nil")
	assert.Nil(t, FindAvailableModel([]*ModelInfo{nil, nil}, "x"), "all-nil slice does not panic")
	assert.Nil(t, FindAvailableModel(nil, "x"))
}
