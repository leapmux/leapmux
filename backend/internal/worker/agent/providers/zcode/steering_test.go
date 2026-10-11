package zcode

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
)

func TestZCodeDoesNotAdvertiseMidTurnSteeringWhenNativeSendIsBusy(t *testing.T) {
	t.Parallel()
	_, ok := any(&Agent{}).(agent.InputSteerer)
	assert.False(t, ok, "the installed app-server refuses session/send while a prompt runs")
}
