package service

import (
	"regexp"
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

// pooledTitleShape is what a title built from the pool reads like: the prefix,
// one space, and one capitalized word.
//
// It is a READABILITY property now, not a load-bearing one. Plan-mode
// auto-rename used to decide "may I overwrite this title?" by matching the
// rendered string against exactly this pattern, so a pooled name that stopped
// matching silently disabled the feature. The agents row records the answer
// instead (title_auto_generated), so the pool is free of that tie -- this
// keeps the shape the schema still states, and nothing depends on it.
var pooledTitleShape = regexp.MustCompile(`^` + regexp.QuoteMeta(contracts.AgentTitlePrefix) + ` [A-Z][A-Za-z]+$`)

func TestPickAgentTitle_EveryPooledNameKeepsTheTitleShape(t *testing.T) {
	t.Parallel()

	for _, name := range contracts.TabNames {
		title := contracts.AgentTitlePrefix + " " + name
		assert.Regexp(t, pooledTitleShape, title, "%q does not read as a pooled agent title", title)
	}
}

func TestPickAgentTitle_UsesTheContractPrefix(t *testing.T) {
	t.Parallel()

	title := pickAgentTitle()
	assert.True(t, strings.HasPrefix(title, contracts.AgentTitlePrefix+" "),
		"got %q, want the %q prefix", title, contracts.AgentTitlePrefix)
	assert.Regexp(t, pooledTitleShape, title)
}

func TestPickTerminalTitle_UsesTheContractPrefix(t *testing.T) {
	t.Parallel()

	title := pickTerminalTitle()
	assert.True(t, strings.HasPrefix(title, contracts.TerminalTitlePrefix+" "),
		"got %q, want the %q prefix", title, contracts.TerminalTitlePrefix)
	// A terminal title must not read as an agent title: the two prefixes are
	// what tell a reader which kind of tab a name belongs to, and the contract
	// check refuses a shared prefix for that reason.
	assert.NotRegexp(t, pooledTitleShape, title)
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
