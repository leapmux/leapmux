package pi

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
)

// Every tier of Pi's catalog must agree with the shared table on its label. This is the
// drift the table exists to remove: three files spelled "xhigh" out by hand and a
// fourth special-cased it.
func TestPiEffortsUseSharedLabels(t *testing.T) {
	t.Parallel()

	for _, tier := range piDefaultEfforts {
		assert.Equal(t, providerkit.EffortLabel(tier.Id), tier.Name, "effort %q must use the shared label", tier.Id)
	}
}

// The trimmed Pi list is a second catalog of its own, so it needs its own check.
func TestPiNonReasoningEffortsUseSharedLabels(t *testing.T) {
	t.Parallel()

	for _, tier := range piNonReasoningEfforts {
		assert.Equal(t, providerkit.EffortLabel(tier.Id), tier.Name, "effort %q must use the shared label", tier.Id)
	}
}
