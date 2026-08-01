package agent

// derivedModelDefault lives here rather than in manager_test.go because that
// file is `//go:build unix` (it depends on helpers that spawn /bin/sh) while
// this helper is pure data -- and claude_catalog_test.go, which runs on every
// platform, calls it. A unix-only helper referenced from an untagged test file
// is exactly the shape that broke the Windows build once already.

import (
	"testing"

	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
)

// derivedModelDefault runs a model catalog through the live default-badge path --
// modelOptionGroup's projection into the "model" option group, then
// withModelGroupDefaultMarked's re-derivation of that group's DefaultValue via the
// defaultModelIDForList ladder -- and returns the id the ladder badges. This is the
// path OptionGroups takes on every read, so the ladder tests below exercise the
// production entry point rather than a test-only adapter.
func derivedModelDefault(t *testing.T, models []*ModelInfo, provider leapmuxv1.AgentProvider) string {
	t.Helper()
	group := modelOptionGroup(models, "", nil)
	require.NotNil(t, group, "precondition: the catalog projects to a model group")
	got := withModelGroupDefaultMarked([]*leapmuxv1.AvailableOptionGroup{group}, provider)
	return optionids.GroupByID(got, OptionIDModel).GetDefaultValue()
}
