package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
)

// The pool now arrives from contracts/tab-names.json, so these assert the
// properties the Go side DEPENDS on rather than the literal list. The JSON
// Schema holds the same shape at generation time; this is the check that the
// generated table the worker actually links against still has it.
func TestTabNamePool(t *testing.T) {
	t.Parallel()

	require.NotEmpty(t, contracts.TabNames, "an empty pool makes pickTabName panic on the spawn hot path")

	seen := make(map[string]bool, len(contracts.TabNames))
	for _, name := range contracts.TabNames {
		assert.NotContains(t, name, " ", "a name with a space would make the title ambiguous to split")
		assert.False(t, seen[name], "duplicate name %q", name)
		seen[name] = true
	}
}

// The tie between the pool and plan-mode auto-rename. A pooled name that the
// pattern rejects would leave every tab it named un-renameable by plan mode,
// with nothing to report it -- the failure is silent by construction, so the
// whole pool is checked rather than a sample.
func TestPickAgentTitle_EveryPooledNameMatchesTheAutoTitlePattern(t *testing.T) {
	t.Parallel()

	for _, name := range contracts.TabNames {
		title := contracts.AgentTitlePrefix + " " + name
		assert.Regexp(t, agentAutoTitlePattern, title,
			"plan-mode auto-rename would never overwrite %q", title)
	}
}

func TestPickAgentTitle_UsesTheContractPrefix(t *testing.T) {
	t.Parallel()

	title := pickAgentTitle()
	assert.True(t, strings.HasPrefix(title, contracts.AgentTitlePrefix+" "),
		"got %q, want the %q prefix", title, contracts.AgentTitlePrefix)
	assert.Regexp(t, agentAutoTitlePattern, title)
}

func TestPickTerminalTitle_UsesTheContractPrefix(t *testing.T) {
	t.Parallel()

	title := pickTerminalTitle()
	assert.True(t, strings.HasPrefix(title, contracts.TerminalTitlePrefix+" "),
		"got %q, want the %q prefix", title, contracts.TerminalTitlePrefix)
	// A terminal title must NOT read as an auto-generated agent title, or
	// plan-mode auto-rename would treat a terminal's name as overwritable.
	assert.NotRegexp(t, agentAutoTitlePattern, title)
}

// pickTabName must reach the whole pool. A picker that returned one name (an
// off-by-one on the bound, or a lost rand call) still passes every prefix
// assertion above, so draw enough times that a collapsed range cannot hide.
func TestPickTabName_DrawsMoreThanOneName(t *testing.T) {
	t.Parallel()

	pool := make(map[string]bool, len(contracts.TabNames))
	for _, name := range contracts.TabNames {
		pool[name] = true
	}

	drawn := make(map[string]bool)
	for range 200 {
		name := pickTabName()
		require.True(t, pool[name], "pickTabName returned %q, which is not in the pool", name)
		drawn[name] = true
	}
	assert.Greater(t, len(drawn), 1, "200 draws returned one name; the pick is not random")
}
