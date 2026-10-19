package codebuddy

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestCodebuddyHasNoGoalCommander(t *testing.T) {
	t.Parallel()
	for _, commands := range []string{`["compress","goal"]`, `["compress"]`, `[]`, `null`} {
		t.Run(commands, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			_, commander := any(a).(agent.GoalTextCommander)
			assert.False(t, commander)
			_, capable := any(a).(agent.GoalCapable)
			assert.False(t, capable)
			a.HandleOutput([]byte(`{"type":"system","subtype":"init","session_id":"session-1","slash_commands":` + commands + `}`))
			a.HandleOutput([]byte(`{"type":"system","subtype":"init","session_id":"session-1"}`))
			assert.Zero(t, sink.GoalCapabilityPublishes())
			_, hasGoal := sink.LastGoal()
			assert.False(t, hasGoal)
			assert.Zero(t, sink.GoalClears())
		})
	}
}

func TestCodebuddyGoalTextRemainsOrdinaryInput(t *testing.T) {
	t.Parallel()
	for _, text := range []string{
		"/goal Ship the\nrelease", "/goal all tests pass", "/goal clear",
		"/goal stop", "/goal off", "/goal reset", "/goal none", "/goal cancel",
		"/goal pause", "/goal resume", "/goal stop using deprecated API",
	} {
		t.Run(text, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			var input bytes.Buffer
			a.SetStdinForTest(agenttest.NopStdin(&input))
			require.NoError(t, a.SendInput(text, nil))
			var frame UserInputMessage
			require.NoError(t, json.Unmarshal(input.Bytes(), &frame))
			assert.Equal(t, text, frame.Message.Content)
			_, hasGoal := sink.LastGoal()
			assert.False(t, hasGoal)
			assert.Zero(t, sink.GoalClears())
			assert.Zero(t, sink.GoalCapabilityPublishes())
		})
	}
}
