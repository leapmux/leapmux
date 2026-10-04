package pi

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// piTimedConfirm is an extension's confirm that Pi answers itself, with its
// default, after 30 seconds.
const piTimedConfirm = `{"type":"extension_ui_request","id":"c1","method":"confirm","title":"Proceed?","timeout":30000}`

// newPiDeadlineFixture is an agent that reads a mock clock and writes to output.
func newPiDeadlineFixture(t *testing.T) (*Agent, *agenttest.ControlSink, *bytes.Buffer, context.Context, *quartz.Mock) {
	t.Helper()
	output := &bytes.Buffer{}
	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	a.SetStdinForTest(agenttest.NopStdin(output))
	clock := testutil.NewQuartzMock(t)
	a.clock = clock
	return a, sink, output, testutil.DeadlineContext(t), clock
}

// Pi answers a dialog that times out itself, with its default, and states nothing
// to the host. The worker withdraws the card when the deadline Pi stated passes,
// and sends Pi nothing, because Pi no longer waits.
func TestPiDialogIsWithdrawnWhenTheDeadlinePiStatedPasses(t *testing.T) {
	t.Parallel()
	a, sink, output, ctx, clock := newPiDeadlineFixture(t)
	a.handlePiExtensionUIRequest([]byte(piTimedConfirm))
	require.Len(t, sink.PublishedControls(), 1)

	clock.Advance(30*time.Second - time.Millisecond).MustWait(ctx)
	assert.Empty(t, sink.CanceledControls(), "the deadline has not passed")
	clock.Advance(time.Millisecond).MustWait(ctx)
	assert.Equal(t, []string{"c1"}, sink.CanceledControls())
	assert.Empty(t, output.String(), "Pi answered the dialog itself")
}

func TestPiAnsweredDialogIsNotWithdrawnAtItsDeadline(t *testing.T) {
	t.Parallel()
	a, sink, _, ctx, clock := newPiDeadlineFixture(t)
	a.handlePiExtensionUIRequest([]byte(piTimedConfirm))
	require.NoError(t, a.SendRawInput([]byte(`{"type":"extension_ui_response","id":"c1","confirmed":true}`)))

	clock.Advance(time.Minute).MustWait(ctx)
	assert.Empty(t, sink.CanceledControls())
}

// A dialog that could not be published was answered with a cancellation already.
func TestPiDialogThatCouldNotBePublishedIsNotWithdrawnAtItsDeadline(t *testing.T) {
	t.Parallel()
	a, sink, output, ctx, clock := newPiDeadlineFixture(t)
	sink.PublicationError = errors.New("the store is gone")
	a.handlePiExtensionUIRequest([]byte(piTimedConfirm))
	require.Contains(t, output.String(), `"cancelled":true`)

	clock.Advance(time.Minute).MustWait(ctx)
	assert.Empty(t, sink.CanceledControls())
}

// Pi states no deadline for a dialog that waits with no limit: a missing timeout,
// and one of zero.
func TestPiDialogWithoutADeadlineIsNeverWithdrawnByTheClock(t *testing.T) {
	t.Parallel()
	for _, frame := range []string{
		`{"type":"extension_ui_request","id":"c1","method":"confirm","title":"Proceed?"}`,
		`{"type":"extension_ui_request","id":"c1","method":"confirm","title":"Proceed?","timeout":0}`,
	} {
		a, sink, _, ctx, clock := newPiDeadlineFixture(t)
		a.handlePiExtensionUIRequest([]byte(frame))
		clock.Advance(time.Hour).MustWait(ctx)
		assert.Empty(t, sink.CanceledControls(), frame)
	}
}

// A question dialog that timed out is forgotten, as a cancelled one is, so a later
// answer with its id passes through unchanged.
func TestPiQuestionDialogThatTimedOutIsForgotten(t *testing.T) {
	t.Parallel()
	a, sink, output, ctx, clock := newPiDeadlineFixture(t)
	handlePiOutput(a, providerkit.ParseLine([]byte(`{"type":"tool_execution_start","toolCallId":"question","toolName":"ask_user_question","args":{"questions":[{"question":"Choose","options":[{"label":"A","description":"first"},{"label":"B","description":"second"}]}]}}`)))
	a.handlePiExtensionUIRequest([]byte(`{"type":"extension_ui_request","id":"select","method":"select","title":"Choose","options":["1. A — first","2. B — second","3. Type something."],"timeout":1000}`))
	clock.Advance(time.Second).MustWait(ctx)
	require.Equal(t, []string{"select"}, sink.CanceledControls())

	a.Mu.Lock()
	_, remembered := a.questionDialogs["select"]
	a.Mu.Unlock()
	assert.False(t, remembered)
	late := `{"type":"extension_ui_response","id":"select","value":"My custom answer"}`
	require.NoError(t, a.SendRawInput([]byte(late)))
	assert.Equal(t, late+"\n", output.String(), "no custom-answer exchange starts for a dialog Pi no longer waits for")
}

// A stopped agent withdraws its cards by its own path, so no deadline fires after it.
func TestPiStoppedAgentWithdrawsNoDialogAtItsDeadline(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	rig := newPiTestRig(t, agent.NewProviderServices(sink))
	clock := testutil.NewQuartzMock(t)
	ctx := testutil.DeadlineContext(t)
	rig.agent.clock = clock
	rig.agent.handlePiExtensionUIRequest([]byte(piTimedConfirm))
	require.Len(t, sink.PublishedControls(), 1)
	// No subprocess runs, so the exit comes first and Stop does not wait for it.
	rig.agent.SimulateExitForTest()
	rig.agent.Stop()

	clock.Advance(time.Minute).MustWait(ctx)
	assert.Empty(t, sink.CanceledControls())
}

func TestPiExtensionUIRequestHeaderDeadline(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		timeout float64
		want    time.Duration
	}{
		{timeout: 0, want: 0},
		{timeout: -5, want: 0},
		{timeout: 30000, want: 30 * time.Second},
		{timeout: 1.5, want: 1500 * time.Microsecond},
		{timeout: 1e300, want: 0},
		{timeout: maxPiDialogTimeoutMillis, want: 0},
	} {
		assert.Equal(t, tc.want, piExtensionUIRequestHeader{Timeout: tc.timeout}.deadline(), tc.timeout)
	}
	// Just below the largest wait that a time.Duration holds, the conversion
	// stays positive: it does not overflow into a negative wait, which would
	// read as no deadline.
	assert.Positive(t, piExtensionUIRequestHeader{Timeout: maxPiDialogTimeoutMillis - 1}.deadline())
}
