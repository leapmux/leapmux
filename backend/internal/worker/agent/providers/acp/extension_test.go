package acp

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
)

// Hooks that state no sink and no model keep what the start gave the base, and
// every other hook takes effect.
func TestApplyHooksKeepsTheLaunchSinkAndModelWhenTheHooksStateNone(t *testing.T) {
	t.Parallel()

	launchSink := agent.NewProviderServices(&agenttest.Sink{})
	b := &Base{sink: launchSink, model: "launch-model"}
	b.applyHooks(Hooks{ModeChannel: ModeChannelPrimaryAgent, PreferredFirstMode: "safe"})

	// == compares the sink pointers. An equality of two empty sinks holds for two
	// different sinks too, so it would prove nothing.
	assert.True(t, b.sink == launchSink, "the launch sink stays")
	assert.Equal(t, "launch-model", b.model)
	assert.Equal(t, ModeChannelPrimaryAgent, b.hooks.ModeChannel)
	assert.Equal(t, "safe", b.hooks.PreferredFirstMode)
}

// A hook sink and an initial model replace the launch values. The kept hooks
// hold neither, because the base changes both fields after the start.
func TestApplyHooksReplacesTheSinkAndModelAndKeepsNoCopy(t *testing.T) {
	t.Parallel()

	launchSink := agent.NewProviderServices(&agenttest.Sink{})
	wrapped := agent.NewProviderServices(&agenttest.Sink{})
	b := &Base{sink: launchSink, model: "launch-model"}
	b.applyHooks(Hooks{Sink: wrapped, InitialModel: "normalized-model"})

	assert.True(t, b.sink == wrapped, "the hook sink replaces the launch sink")
	assert.Equal(t, "normalized-model", b.model)
	assert.Nil(t, b.hooks.Sink, "the kept hooks hold no copy of the sink")
	assert.Empty(t, b.hooks.InitialModel, "the kept hooks hold no copy of the model")
}

// SteerTarget reads the four values of a steer request together, and
// SteerRunActive answers true only for the run that still runs.
func TestSteerTargetAndRunActive(t *testing.T) {
	t.Parallel()

	b := &Base{steerMethod: "_vendor/session/steer", sessionID: "session-1"}
	method, active, sessionID, runID := b.SteerTarget()
	assert.Equal(t, "_vendor/session/steer", method)
	assert.False(t, active)
	assert.Equal(t, "session-1", sessionID)
	assert.Empty(t, runID)
	assert.False(t, b.SteerRunActive(""), "no prompt runs, so no run is active")

	b.promptActive = true
	b.SetSteerRunID("run-1")
	_, active, _, runID = b.SteerTarget()
	assert.True(t, active)
	assert.Equal(t, "run-1", runID)
	assert.True(t, b.SteerRunActive("run-1"))
	assert.False(t, b.SteerRunActive("run-2"), "a steer for a replaced run must not count as delivered")

	b.promptActive = false
	assert.False(t, b.SteerRunActive("run-1"), "a run whose prompt ended is not active")
	assert.False(t, b.PromptActive())
}

// The setters record what a provider wrote to the session itself, and
// AvailableModes returns the list that the session offers.
func TestProviderWritesToTheBaseState(t *testing.T) {
	t.Parallel()

	b := &Base{}
	b.SetCurrentModel("model-1")
	b.SetPermissionMode("normal")
	assert.Equal(t, "model-1", b.model)
	assert.Equal(t, "normal", b.permissionMode)

	assert.Empty(t, b.AvailableModes())
	modes := []*leapmuxv1.AvailableOption{{Id: "normal"}, {Id: "goal"}}
	b.availableModes = modes
	assert.Equal(t, modes, b.AvailableModes())
}
