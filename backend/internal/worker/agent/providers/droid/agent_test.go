package droid

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func TestSendInputCarriesImageBytes(t *testing.T) {
	t.Parallel()
	a, _, stdin := newSteerAgent(t)
	data := []byte{0x89, 'P', 'N', 'G', 0x00}

	require.NoError(t, a.SendInput("Describe this.", []*leapmuxv1.Attachment{{
		Filename: "shot.png", MimeType: "image/png", Data: data,
	}}))
	params := lastRequest(t, stdin)
	assert.Equal(t, "Describe this.", params["text"])
	assert.Equal(t, []any{map[string]any{
		"type": "base64", "mediaType": "image/png", "data": base64.StdEncoding.EncodeToString(data),
	}}, params["images"])
}

func TestSteerInputWritesNativeSteeringPlacement(t *testing.T) {
	t.Parallel()
	a, _, stdin := newSteerAgent(t)
	a.startTurn()

	require.NoError(t, a.SteerInput("guide this turn", nil))
	params := lastRequest(t, stdin)
	assert.Equal(t, "end_of_turn", params["queuePlacement"], "Droid processes this placement during a running turn")
	assert.True(t, a.PublishTurnActive().Active)
}

func TestTurnStartPublishesSteeringAvailability(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	a.startTurn()
	a.startTurn()
	a.endTurn()
	assert.Equal(t, []leapmuxv1.AgentInputKind{
		leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_UNSPECIFIED,
	}, sink.TurnKinds())
}
