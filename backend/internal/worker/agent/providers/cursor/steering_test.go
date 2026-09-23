package cursor

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
)

// Cursor has no steer method, so it must not implement InputSteerer. Copilot
// steers natively with `session.send` mode "immediate"; see
// TestNativeCopilotSteerSendsImmediateMode.
func TestCursorDoesNotSteer(t *testing.T) {
	t.Parallel()
	_, supports := any(&Agent{}).(agent.InputSteerer)
	assert.False(t, supports)
}
