package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// facetStub retains facet identity and records the exact result-reader arguments.
type facetStub struct {
	ServiceFacets
	sequences []int64
	sessions  [][2]string
}

func (stub *facetStub) ReadToolResultBySeq(seq int64) (*StoredMessage, error) {
	stub.sequences = append(stub.sequences, seq)
	return nil, nil
}

func (stub *facetStub) ReadToolResultForSession(spanID, sessionID string) (*StoredMessage, error) {
	stub.sessions = append(stub.sessions, [2]string{spanID, sessionID})
	return nil, nil
}

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
	_, err := services.ReadToolResultBySeq(17)
	require.NoError(t, err)
	_, err = services.ReadToolResultForSession("original-span", "original-session")
	require.NoError(t, err)
	assert.Equal(t, []int64{17}, sink.sequences)
	assert.Equal(t, [][2]string{{"original-span", "original-session"}}, sink.sessions)
}
