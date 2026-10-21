package zcode

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
)

// The app-server protocol offers no steering input, although the runtime
// inside the same bundle steers at the next tool boundary for its own
// in-process callers (the desktop host): the runtime admits a delivery of
// auto, steer_active_turn or guide against a steerable active turn, queues
// the rest as pending input drained at the next tool boundary
// (turn.steerQueued / turn.steerDrained), and injects a user_steer reminder
// there. None of that crosses the wire. session/send is the only input
// method, its schema is strict (no delivery, no intent), and its handler
// refuses with -32010 while a prompt runs, before any runtime admission.
// Verified against ZCode 3.16.2 (CLI 0.17.0) by disassembly of
// Resources/glm/zcode.cjs and by a live probe that held a turn inside a Bash
// call: a plain mid-turn session/send returned -32010, and sends carrying
// intent or delivery fields returned -32602 unrecognized keys with no
// steer events and no queued input left behind.
func TestZCodeDoesNotAdvertiseMidTurnSteeringWhenNativeSendIsBusy(t *testing.T) {
	t.Parallel()
	_, ok := any(&Agent{}).(agent.InputSteerer)
	assert.False(t, ok, "the installed app-server refuses session/send while a prompt runs")
}
