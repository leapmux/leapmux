package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// facetStub implements every facet through a nil embedded interface. The test
// below compares identities only, so no method of the stub runs.
type facetStub struct{ ServiceFacets }

// NewProviderServices must route every facet to the one value that it
// received. assert.Same compares the pointers: an equality check of two
// empty stubs holds for two different stubs too, so it proves nothing.
func TestProviderServicesKeepOneImplementationAcrossFacets(t *testing.T) {
	t.Parallel()

	sink := &facetStub{}
	services := NewProviderServices(sink)
	composed, ok := services.(providerServices)
	require.True(t, ok)
	assert.Same(t, sink, composed.TranscriptServices)
	assert.Same(t, sink, composed.TurnServices)
	assert.Same(t, sink, composed.SpanServices)
	assert.Same(t, sink, composed.ProgressServices)
	assert.Same(t, sink, composed.ControlServices)
	assert.Same(t, sink, composed.SessionServices)
	assert.Same(t, sink, composed.PlanServices)
	assert.Same(t, sink, composed.GoalServices)
	assert.Same(t, sink, composed.AutoContinueServices)
	assert.Same(t, sink, composed.ChildServices)
	assert.Same(t, sink, composed.BackgroundTaskServices)
}
