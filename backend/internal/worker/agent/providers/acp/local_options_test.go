package acp

import (
	"errors"
	"sync"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// localOptions is a provider-kept option group, as Grok Build keeps its
// approval mode.
type localOptions struct {
	mu      sync.Mutex
	current string
	fail    bool
	applied []string
}

func (l *localOptions) groups() []*leapmuxv1.AvailableOptionGroup {
	l.mu.Lock()
	defer l.mu.Unlock()
	return []*leapmuxv1.AvailableOptionGroup{{
		Id: "approval", Label: "Approval", CurrentValue: l.current, Mutable: true,
		Options: []*leapmuxv1.AvailableOption{{Id: "ask"}, {Id: "always"}},
	}}
}

func (l *localOptions) apply(id, value string) (bool, error) {
	if id != "approval" {
		return false, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fail {
		return true, errors.New("refused")
	}
	l.applied = append(l.applied, value)
	l.current = value
	return true, nil
}

func TestLocalOptions_ServedAndAppliedBesideTheSessionGroups(t *testing.T) {
	t.Parallel()
	a, requests := newTestAgentForRPC(t)
	local := &localOptions{current: "ask"}
	a.hooks.LocalOptionGroups = local.groups
	a.hooks.ApplyLocalOption = local.apply

	groups := a.OptionGroups()
	require.NotEmpty(t, groups)
	last := groups[len(groups)-1]
	assert.Equal(t, "approval", last.GetId())
	assert.Equal(t, "ask", last.GetCurrentValue())

	result := a.UpdateSettings(map[string]string{"approval": "always"})
	assert.Equal(t, []string{"always"}, local.applied)
	assert.Equal(t, agent.OptionSettlementConfirmed, result.Settlements["approval"].State)
	require.NotNil(t, result.Settlements["approval"].Value)
	assert.Equal(t, "always", *result.Settlements["approval"].Value)
	assert.Empty(t, requests(), "a local option reaches no RPC of the session")

	// An unchanged value writes nothing.
	a.UpdateSettings(map[string]string{"approval": "always"})
	assert.Equal(t, []string{"always"}, local.applied)
}

func TestLocalOptions_ARefusedWriteNeedsARestart(t *testing.T) {
	t.Parallel()
	a, _ := newTestAgentForRPC(t)
	local := &localOptions{current: "ask", fail: true}
	a.hooks.LocalOptionGroups = local.groups
	a.hooks.ApplyLocalOption = local.apply

	result := a.UpdateSettings(map[string]string{"approval": "always"})

	assert.Equal(t, agent.OptionSettlementUnresolved, result.Settlements["approval"].State)
}

func TestLocalOptions_AnUnownedIdIsAFailure(t *testing.T) {
	t.Parallel()
	a, _ := newTestAgentForRPC(t)
	a.hooks.LocalOptionGroups = (&localOptions{current: "ask"}).groups
	a.hooks.ApplyLocalOption = func(string, string) (bool, error) { return false, nil }

	result := a.UpdateSettings(map[string]string{"approval": "always"})

	assert.Equal(t, agent.OptionSettlementUnresolved, result.Settlements["approval"].State)
}

// An update that carries no value for a local group, or an empty one, writes
// nothing: the service hands UpdateSettings the whole option map, and an absent
// or empty value states no change.
func TestLocalOptions_AnEmptyOrAbsentValueWritesNothing(t *testing.T) {
	t.Parallel()
	a, requests := newTestAgentForRPC(t)
	local := &localOptions{current: "ask"}
	a.hooks.LocalOptionGroups = local.groups
	a.hooks.ApplyLocalOption = local.apply

	for _, options := range []map[string]string{{"approval": ""}, {"other": "always"}, {}} {
		result := a.UpdateSettings(options)
		assert.Equal(t, agent.OptionSettlementConfirmed, result.Settlements["approval"].State)
		require.NotNil(t, result.Settlements["approval"].Value)
		assert.Equal(t, "ask", *result.Settlements["approval"].Value)
	}

	assert.Empty(t, local.applied)
	assert.Empty(t, requests())
}
