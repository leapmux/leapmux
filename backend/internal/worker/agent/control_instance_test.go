package agent_test

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
)

func TestControlInstanceRefusal(t *testing.T) {
	t.Parallel()
	assert.NoError(t, agent.ControlInstanceRefusal("instance-2", "instance-2"), "an answer to the current instance goes through")

	replaced := agent.ControlInstanceRefusal("instance-2", "instance-1")
	assert.ErrorIs(t, replaced, agent.ErrControlInstanceReplaced)
	assert.ErrorIs(t, replaced, agent.ErrControlRequestNotPending, "a replaced instance no longer waits for an answer")

	unstated := agent.ControlInstanceRefusal("instance-2", "")
	assert.ErrorIs(t, unstated, agent.ErrControlInstanceUnstated)
	assert.NotErrorIs(t, unstated, agent.ErrControlInstanceReplaced, "an answer that states no instance proves no replacement")
	assert.NotErrorIs(t, unstated, agent.ErrControlRequestNotPending)

	// An empty current instance equals an empty answered one, but the answer
	// still proves nothing about which instance it is for.
	assert.ErrorIs(t, agent.ControlInstanceRefusal("", ""), agent.ErrControlInstanceUnstated,
		"an answer that states no instance never goes through, even to a request whose instance is empty")
}
