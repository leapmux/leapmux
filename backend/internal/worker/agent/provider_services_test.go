package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	_ TranscriptServices     = (*testSink)(nil)
	_ TurnServices           = (*testSink)(nil)
	_ SpanServices           = (*testSink)(nil)
	_ ProgressServices       = (*testSink)(nil)
	_ ControlServices        = (*testSink)(nil)
	_ SessionServices        = (*testSink)(nil)
	_ PlanServices           = (*testSink)(nil)
	_ GoalServices           = (*testSink)(nil)
	_ AutoContinueServices   = (*testSink)(nil)
	_ ChildServices          = (*testSink)(nil)
	_ BackgroundTaskServices = (*testSink)(nil)
)

func TestProviderServicesKeepOneImplementationAcrossFacets(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	services := NewProviderServices(sink)
	composed, ok := services.(providerServices)
	require.True(t, ok)
	assert.Equal(t, sink, composed.TranscriptServices)
	assert.Equal(t, sink, composed.ProgressServices)
	assert.Equal(t, sink, composed.ChildServices)
}

type goalServicesRecorder struct {
	update GoalUpdate
}

func (s *goalServicesRecorder) UpsertGoal(update GoalUpdate) { s.update = update }
func (*goalServicesRecorder) ClearGoal(bool)                 {}
func (*goalServicesRecorder) PublishGoalCapabilities()       {}

func TestGoalTextRouteAcceptsOnlyGoalServices(t *testing.T) {
	t.Parallel()

	services := &goalServicesRecorder{}
	route := goalTextRoute{provider: "test", command: "/goal", clearArgs: []string{"clear"}}
	route.observe(services, GoalDeliverySend, "/goal Keep state coherent")
	assert.Equal(t, "Keep state coherent", services.update.Objective)
}
