//go:build unix

package pi

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPiAgent_Interrupt_SendsAbortDuringActiveTurn(t *testing.T) {
	t.Parallel()

	rig := newPiTestRig(t, agent.NewProviderServices(agenttest.Nop{}))
	rig.agent.Mu.Lock()
	rig.agent.currentTurnActive = true
	rig.agent.Mu.Unlock()
	rig.setResponder(func(req piRecordedRequest) (json.RawMessage, bool, string) {
		// Pi acks abort with success:true and a null payload.
		return json.RawMessage(`null`), true, ""
	})

	require.NoError(t, rig.agent.Interrupt())

	reqs := rig.requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, CommandAbort, reqs[0].Type)
	assert.NotEmpty(t, reqs[0].ID)
}

func TestPiAgent_Interrupt_NoActiveTurnIsNoop(t *testing.T) {
	t.Parallel()

	rig := newPiTestRig(t, agent.NewProviderServices(agenttest.Nop{}))
	// currentTurnActive defaults to false.

	require.NoError(t, rig.agent.Interrupt())

	// Allow potential writes to drain before asserting.
	time.Sleep(50 * time.Millisecond)
	assert.Empty(t, rig.requests(),
		"Interrupt with no active turn must not write any command")
}

func TestPiAgent_Interrupt_AfterStopErrors(t *testing.T) {
	t.Parallel()

	rig := newPiTestRig(t, agent.NewProviderServices(agenttest.Nop{}))
	rig.agent.SetStoppedForTest(true)
	rig.agent.Mu.Lock()
	rig.agent.currentTurnActive = true
	rig.agent.Mu.Unlock()

	err := rig.agent.Interrupt()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stopped")
}

func TestInterrupt_PiWireFormatMatchesProviderClassifier(t *testing.T) {
	t.Parallel()

	// Pi's sendPiCommand wraps {type:"abort", id:...} — IsInterrupt
	// keys on type only.
	frame := `{"type":"abort","id":"leapmux-1"}`
	assert.True(t, piProvider{}.IsInterrupt(frame),
		"piProvider.IsInterrupt must recognise the frame Agent.Interrupt emits")
}
