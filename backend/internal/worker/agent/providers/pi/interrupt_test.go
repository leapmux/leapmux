//go:build unix

package pi

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPiAgent_Interrupt_SendsAbortDuringActiveTurn(t *testing.T) {
	t.Parallel()

	rig := newPiTestRig(t, agenttest.Nop())
	rig.agent.Mu.Lock()
	rig.agent.currentTurnActive = true
	rig.agent.Mu.Unlock()
	rig.setResponder(func(req piRecordedRequest) (json.RawMessage, bool, string) {
		// Pi acks abort with success:true and a null payload.
		return json.RawMessage(`null`), true, ""
	})

	require.NoError(t, rig.agent.Interrupt(agent.StopContext{}))

	reqs := rig.requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, CommandAbort, reqs[0].Type)
	assert.NotEmpty(t, reqs[0].ID)
}

func TestPiAgent_Interrupt_NoActiveTurnIsNoop(t *testing.T) {
	t.Parallel()

	rig := newPiTestRig(t, agenttest.Nop())
	// currentTurnActive defaults to false.

	require.NoError(t, rig.agent.Interrupt(agent.StopContext{}))

	// One pipe carries every write in order, so a sentinel that reaches the recorder
	// first proves that Interrupt wrote nothing before it.
	_, err := rig.agent.sendPiCommand(CommandGetState, nil, time.Second)
	require.NoError(t, err)
	assert.Equal(t, []string{CommandGetState}, piRequestTypes(rig.requests()),
		"Interrupt with no active turn must not write any command")
}

// piRequestTypes lists the `type` of each recorded stdin line, in order.
func piRequestTypes(requests []piRecordedRequest) []string {
	types := make([]string, 0, len(requests))
	for _, request := range requests {
		types = append(types, request.Type)
	}
	return types
}

// piOpenSelect is an extension's select dialog with no deadline. The installed
// rpiv-ask-user-question extension asks this way, and passes Pi no abort signal.
func piOpenSelect(id string) []byte {
	return []byte(`{"type":"extension_ui_request","id":"` + id + `","method":"select","title":"Choose","options":["A","B"]}`)
}

// piCancellations lists the ids of the dialog cancellations among the recorded lines.
func piCancellations(requests []piRecordedRequest) []string {
	var ids []string
	for _, request := range requests {
		if request.Type == contracts.PiEventExtensionUIResponse && request.Payload["cancelled"] == true {
			ids = append(ids, request.ID)
		}
	}
	return ids
}

// Pi's abort waits until the agent is idle, and an extension that waits on a dialog
// with no abort signal keeps the agent busy. The abort alone then ends nothing: the
// turn stays open and the extension waits for ever. The interrupt answers each open
// dialog with a cancellation AFTER the abort, so the run is already aborted when the
// extension's tool returns, and withdraws the dialog's card.
func TestPiAgent_Interrupt_CancelsEachOpenDialogAfterTheAbort(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	rig := newPiTestRig(t, agent.NewProviderServices(sink))
	rig.agent.Mu.Lock()
	rig.agent.currentTurnActive = true
	rig.agent.Mu.Unlock()
	rig.agent.handlePiExtensionUIRequest(piOpenSelect("dialog-b"))
	rig.agent.handlePiExtensionUIRequest(piOpenSelect("dialog-a"))
	require.Len(t, sink.PublishedControls(), 2)

	require.NoError(t, rig.agent.Interrupt(agent.StopContext{}))
	// The recorder reads the pipe on its own goroutine. A sentinel's answer states that
	// it read every earlier line.
	_, err := rig.agent.sendPiCommand(CommandGetState, nil, time.Second)
	require.NoError(t, err)

	assert.Equal(t, []string{CommandAbort, contracts.PiEventExtensionUIResponse, contracts.PiEventExtensionUIResponse, CommandGetState}, piRequestTypes(rig.requests()))
	assert.Equal(t, []string{"dialog-a", "dialog-b"}, piCancellations(rig.requests()), "one cancellation for each dialog, in a stable order")
	assert.Equal(t, []string{"dialog-a", "dialog-b"}, sink.CanceledControls())

	// A second interrupt finds no open dialog: each one was answered once.
	require.NoError(t, rig.agent.Interrupt(agent.StopContext{}))
	_, err = rig.agent.sendPiCommand(CommandGetState, nil, time.Second)
	require.NoError(t, err)
	assert.Len(t, piCancellations(rig.requests()), 2)
	assert.Len(t, sink.CanceledControls(), 2)
}

// A dialog can wait outside a turn, as one from an extension command does. No abort
// settles it, so the interrupt answers it with no abort.
func TestPiAgent_Interrupt_CancelsAnOpenDialogWithNoTurn(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	rig := newPiTestRig(t, agent.NewProviderServices(sink))
	rig.agent.handlePiExtensionUIRequest(piOpenSelect("dialog-1"))

	require.NoError(t, rig.agent.Interrupt(agent.StopContext{}))

	_, err := rig.agent.sendPiCommand(CommandGetState, nil, time.Second)
	require.NoError(t, err)
	assert.Equal(t, []string{contracts.PiEventExtensionUIResponse, CommandGetState}, piRequestTypes(rig.requests()))
	assert.Equal(t, []string{"dialog-1"}, piCancellations(rig.requests()))
	assert.Equal(t, []string{"dialog-1"}, sink.CanceledControls())
}

func TestPiAgent_Interrupt_KeepsADialogWhenItsCancellationFails(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	a.SetStdinForTest(agenttest.FailingStdin{})
	a.handlePiExtensionUIRequest(piOpenSelect("dialog-1"))

	require.Error(t, a.Interrupt(agent.StopContext{}), "the interrupt must report the failed cancellation")
	assert.Empty(t, sink.CanceledControls(), "the reader can still answer the open dialog")
	a.Mu.Lock()
	_, open := a.openDialogs["dialog-1"]
	a.Mu.Unlock()
	assert.True(t, open, "a later interrupt must still find the open dialog")
}

// Pi no longer waits on a dialog that the reader answered, that Pi answered at its
// own deadline, or that LeapMux cancelled when it could not publish it. The interrupt
// answers none of them again.
func TestPiAgent_Interrupt_LeavesADialogThatPiNoLongerWaitsOn(t *testing.T) {
	t.Parallel()

	for name, settle := range map[string]func(t *testing.T, a *Agent, sink *agenttest.ControlSink){
		"the reader answered it": func(t *testing.T, a *Agent, _ *agenttest.ControlSink) {
			a.handlePiExtensionUIRequest(piOpenSelect("dialog-1"))
			require.NoError(t, a.SendRawInput([]byte(`{"type":"extension_ui_response","id":"dialog-1","value":"A"}`), agent.StopContext{}))
		},
		"its deadline passed": func(t *testing.T, a *Agent, sink *agenttest.ControlSink) {
			clock := testutil.NewQuartzMock(t)
			a.clock = clock
			a.handlePiExtensionUIRequest([]byte(`{"type":"extension_ui_request","id":"dialog-1","method":"confirm","title":"Proceed?","timeout":1000}`))
			clock.Advance(time.Second).MustWait(testutil.DeadlineContext(t))
			require.Equal(t, []string{"dialog-1"}, sink.CanceledControls())
			sink.ResetCanceledControls()
		},
		"it could not be published": func(t *testing.T, a *Agent, sink *agenttest.ControlSink) {
			sink.PublicationError = errors.New("the store is gone")
			a.handlePiExtensionUIRequest(piOpenSelect("dialog-1"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.ControlSink{}
			rig := newPiTestRig(t, agent.NewProviderServices(sink))
			// The recorder reads the pipe on its own goroutine. A sentinel's answer
			// states that it read every earlier line.
			flush := func() {
				_, err := rig.agent.sendPiCommand(CommandGetState, nil, time.Second)
				require.NoError(t, err)
			}
			settle(t, rig.agent, sink)
			flush()
			before := len(piCancellations(rig.requests()))

			require.NoError(t, rig.agent.Interrupt(agent.StopContext{}))

			flush()
			assert.Len(t, piCancellations(rig.requests()), before, "the interrupt sends no cancellation")
			assert.Empty(t, sink.CanceledControls(), "the interrupt withdraws no card")
		})
	}
}

func TestPiAgent_Interrupt_AfterStopErrors(t *testing.T) {
	t.Parallel()

	rig := newPiTestRig(t, agenttest.Nop())
	rig.agent.SetStoppedForTest(true)
	rig.agent.Mu.Lock()
	rig.agent.currentTurnActive = true
	rig.agent.Mu.Unlock()

	err := rig.agent.Interrupt(agent.StopContext{})
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
