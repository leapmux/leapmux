package ohmypi

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/coder/quartz"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Probe s2 (`tools.approvalMode: always-ask`): omp starts the call, then asks.
const (
	frameApprovedBashStart = `{"type":"tool_execution_start","toolCallId":"call_4","toolName":"bash","args":{"command":"echo approved-run"}}`
	frameApprovalDialog    = `{"type":"extension_ui_request","id":"158b2ba5001bfb93","method":"select","title":"Allow tool: bash\nCommand: echo approved-run","options":["Approve","Deny"]}`
)

func TestAToolApprovalIsPublishedWithItsCall(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(frameApprovedBashStart, frameApprovalDialog)

	controls := r.sink.PublishedControls()
	require.Len(t, controls, 1)
	assert.Equal(t, "158b2ba5001bfb93", controls[0].RequestID)
	assert.JSONEq(t, frameApprovalDialog, string(controls[0].Payload), "the whole dialog reaches the browser")
	assert.Equal(t, int64(1), controls[0].SourceSeq, "the request points at the row of the call it asks about")
}

func TestAnApprovalStatesNoCallWhenTwoCallsOfTheToolRun(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(
		frameApprovedBashStart,
		`{"type":"tool_execution_start","toolCallId":"call_9","toolName":"bash","args":{"command":"echo other"}}`,
		frameApprovalDialog,
	)
	controls := r.sink.PublishedControls()
	require.Len(t, controls, 1)
	assert.Zero(t, controls[0].SourceSeq, "no row rather than a wrong one")
}

func TestAnApprovalStatesNoCallWhenNoneRuns(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(frameApprovalDialog)
	controls := r.sink.PublishedControls()
	require.Len(t, controls, 1)
	assert.Zero(t, controls[0].SourceSeq)
}

// Only a select whose title states a tool name is a tool approval. Anything
// else states no call, even while a call runs.
func TestADialogThatIsNoApprovalStatesNoCall(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		frame string
	}{
		{name: "a confirm with the approval title", frame: `{"type":"extension_ui_request","id":"a1","method":"confirm","title":"Allow tool: bash"}`},
		{name: "a select of another title", frame: `{"type":"extension_ui_request","id":"a2","method":"select","title":"Run bash?","options":["Approve","Deny"]}`},
		{name: "an approval that states no tool", frame: `{"type":"extension_ui_request","id":"a3","method":"select","title":"Allow tool: \nCommand: echo approved-run","options":["Approve","Deny"]}`},
		{name: "an approval of a tool that does not run", frame: `{"type":"extension_ui_request","id":"a4","method":"select","title":"Allow tool: read","options":["Approve","Deny"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.emit(frameApprovedBashStart, tc.frame)
			controls := r.sink.PublishedControls()
			require.Len(t, controls, 1)
			assert.Zero(t, controls[0].SourceSeq)
		})
	}
}

// toolRequestSink is a sink whose store answers every tool-request read with a
// fixed result.
type toolRequestSink struct {
	*agenttest.ControlSink
	stored *agent.StoredMessage
	err    error
}

func (s toolRequestSink) ReadToolRequest(string) (*agent.StoredMessage, error) {
	return s.stored, s.err
}

// The approval still reaches the reader when the call's row cannot be read. It
// states no row rather than a wrong one.
func TestAnApprovalWhoseCallRowCannotBeReadStatesNoCall(t *testing.T) {
	t.Parallel()
	for name, sink := range map[string]toolRequestSink{
		"a store that fails":     {err: errors.New("the store is gone")},
		"a call with no row yet": {},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			sink.ControlSink = r.sink
			r.agent.sink = agent.NewProviderServices(sink)
			r.emit(frameApprovedBashStart, frameApprovalDialog)
			controls := r.sink.PublishedControls()
			require.Len(t, controls, 1)
			assert.Equal(t, "158b2ba5001bfb93", controls[0].RequestID)
			assert.Zero(t, controls[0].SourceSeq)
		})
	}
}

// The source row of a control request is a row of the session's own transcript.
// A subagent's call has its row in the child transcript, so the approval states
// no row for it rather than a row of another transcript.
func TestAnApprovalDoesNotPointAtASubagentsCall(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(frameTaskStart, frameStarted, subagentEvent("Probe", frameApprovedBashStart), frameApprovalDialog)
	controls := r.sink.PublishedControls()
	require.Len(t, controls, 1)
	assert.Zero(t, controls[0].SourceSeq)
}

func TestAnExtensionDialogIsPublishedAsItIs(t *testing.T) {
	t.Parallel()
	for _, frame := range []string{
		`{"type":"extension_ui_request","id":"c1","method":"confirm","title":"Proceed?","message":"This deletes the branch."}`,
		`{"type":"extension_ui_request","id":"i1","method":"input","title":"Branch name","placeholder":"main"}`,
		`{"type":"extension_ui_request","id":"e1","method":"editor","title":"Commit message","prefill":"fix: "}`,
		`{"type":"extension_ui_request","id":"s1","method":"select","title":"Pick one","options":["a","b"]}`,
	} {
		r := newRig(t)
		r.emit(frame)
		controls := r.sink.PublishedControls()
		require.Len(t, controls, 1, frame)
		assert.JSONEq(t, frame, string(controls[0].Payload))
	}
}

func TestADialogWithoutAnIDIsDropped(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(`{"type":"extension_ui_request","method":"confirm","title":"Proceed?"}`)
	assert.Zero(t, r.sink.PublishedControlCount())
}

// A request whose routing fields cannot be read cannot be answered either, so it
// is neither published nor persisted.
func TestAnUnreadableExtensionRequestIsDropped(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(`{"type":"extension_ui_request","id":7,"method":"confirm"}`, `{"type":"extension_ui_request","id":"x","method":"select","options":"a"}`)
	assert.Zero(t, r.sink.PublishedControlCount())
	assert.Zero(t, r.sink.NotificationCount())
	assert.Empty(t, r.sink.Messages())
}

func TestADialogThatCannotBePublishedIsCancelled(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.sink.PublicationError = errors.New("the store is gone")
	r.emit(frameApprovalDialog)

	answers := r.waitForCommand("extension_ui_response", 1)
	assert.Equal(t, "158b2ba5001bfb93", answers[0].Payload["id"])
	assert.Equal(t, true, answers[0].Payload["cancelled"], "omp stops waiting instead of blocking for good")
}

func TestOmpWithdrawsADialog(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(frameApprovalDialog, `{"type":"extension_ui_request","id":"x","method":"cancel","targetId":"158b2ba5001bfb93"}`)
	assert.Equal(t, []string{"158b2ba5001bfb93"}, r.sink.CanceledControls())

	r.emit(`{"type":"extension_ui_request","id":"y","method":"cancel"}`)
	assert.Len(t, r.sink.CanceledControls(), 1, "a cancel with no target withdraws nothing")
}

// frameTimedConfirm is an extension's confirm that omp answers itself, with its
// default, after 30 seconds.
const frameTimedConfirm = `{"type":"extension_ui_request","id":"c1","method":"confirm","title":"Proceed?","timeout":30000}`

// newDeadlineRig is a rig, with the context of its clock's waits and the clock.
func newDeadlineRig(t *testing.T) (*rig, context.Context, *quartz.Mock) {
	t.Helper()
	r := newRig(t)
	return r, testutil.DeadlineContext(t), r.clock
}

// omp answers a dialog that times out itself, with its default, and states nothing
// to the host. The worker withdraws the card when the deadline omp stated passes,
// and sends omp nothing, because omp no longer waits.
func TestADialogIsWithdrawnWhenTheDeadlineOmpStatedPasses(t *testing.T) {
	t.Parallel()
	r, ctx, clock := newDeadlineRig(t)
	r.emit(frameTimedConfirm)
	require.Equal(t, 1, r.sink.PublishedControlCount())

	clock.Advance(30*time.Second - time.Millisecond).MustWait(ctx)
	assert.Empty(t, r.sink.CanceledControls(), "the deadline has not passed")
	clock.Advance(time.Millisecond).MustWait(ctx)
	assert.Equal(t, []string{"c1"}, r.sink.CanceledControls())
	assert.Empty(t, r.commandsOfType("extension_ui_response"), "omp answered the dialog itself")
}

func TestAnAnsweredDialogIsNotWithdrawnAtItsDeadline(t *testing.T) {
	t.Parallel()
	r, ctx, clock := newDeadlineRig(t)
	r.emit(frameTimedConfirm)
	require.NoError(t, r.agent.SendRawInput([]byte(`{"type":"extension_ui_response","id":"c1","confirmed":true}`)))
	r.waitForCommand("extension_ui_response", 1)

	clock.Advance(time.Minute).MustWait(ctx)
	assert.Empty(t, r.sink.CanceledControls())
}

// A dialog that omp itself withdrew is withdrawn once, not again at its deadline.
func TestADialogThatOmpWithdrewIsNotWithdrawnAgainAtItsDeadline(t *testing.T) {
	t.Parallel()
	r, ctx, clock := newDeadlineRig(t)
	r.emit(frameTimedConfirm, `{"type":"extension_ui_request","id":"x","method":"cancel","targetId":"c1"}`)
	require.Equal(t, []string{"c1"}, r.sink.CanceledControls())

	clock.Advance(time.Minute).MustWait(ctx)
	assert.Equal(t, []string{"c1"}, r.sink.CanceledControls())
}

// A dialog that could not be published was answered with a cancellation already.
func TestADialogThatCouldNotBePublishedIsNotWithdrawnAtItsDeadline(t *testing.T) {
	t.Parallel()
	r, ctx, clock := newDeadlineRig(t)
	r.sink.PublicationError = errors.New("the store is gone")
	r.emit(frameTimedConfirm)
	r.waitForCommand("extension_ui_response", 1)

	clock.Advance(time.Minute).MustWait(ctx)
	assert.Empty(t, r.sink.CanceledControls())
}

// omp states no deadline for a dialog that waits with no limit: a missing timeout,
// and one of zero.
func TestADialogWithoutADeadlineIsNeverWithdrawnByTheClock(t *testing.T) {
	t.Parallel()
	for _, frame := range []string{
		`{"type":"extension_ui_request","id":"c1","method":"confirm","title":"Proceed?"}`,
		`{"type":"extension_ui_request","id":"c1","method":"confirm","title":"Proceed?","timeout":0}`,
	} {
		r, ctx, clock := newDeadlineRig(t)
		r.emit(frame)
		clock.Advance(time.Hour).MustWait(ctx)
		assert.Empty(t, r.sink.CanceledControls(), frame)
	}
}

// A stopped agent withdraws its cards by its own path, so no deadline fires after it.
func TestAStoppedAgentWithdrawsNoDialogAtItsDeadline(t *testing.T) {
	t.Parallel()
	r, ctx, clock := newDeadlineRig(t)
	r.emit(frameTimedConfirm)
	r.agent.Stop()

	clock.Advance(time.Minute).MustWait(ctx)
	assert.Empty(t, r.sink.CanceledControls())
}

func TestExtensionNoticesPersist(t *testing.T) {
	t.Parallel()
	frames := []string{
		`{"type":"extension_ui_request","id":"n1","method":"notify","message":"Indexing finished.","notifyType":"info"}`,
		`{"type":"extension_ui_request","id":"o1","method":"open_url","url":"https://example.com/login"}`,
		`{"type":"extension_ui_request","id":"z1","method":"hologram"}`,
	}
	r := newRig(t)
	r.emit(frames...)
	notifications := r.sink.PersistedNotifications()
	require.Len(t, notifications, len(frames))
	for i, notification := range notifications {
		assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, notification.Source)
		assert.JSONEq(t, frames[i], string(notification.Content))
	}
	assert.Zero(t, r.sink.PublishedControlCount())
}

func TestSendRawInputPassesADialogAnswerThrough(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	answer := `{"type":"extension_ui_response","id":"158b2ba5001bfb93","value":"Approve"}`
	require.NoError(t, r.agent.SendRawInput([]byte(answer)))

	answers := r.waitForCommand("extension_ui_response", 1)
	assert.JSONEq(t, answer, string(answers[0].Raw), "omp reads the browser's answer unchanged")
}

func TestSendRawInputFailsAfterExit(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	require.NoError(t, r.stdinR.Close())
	waitFor(t, func() bool {
		select {
		case <-r.agent.ProcessDone():
			return true
		default:
			return false
		}
	})
	err := r.agent.SendRawInput([]byte(`{"type":"extension_ui_response","id":"x","value":"Approve"}`))
	assert.Error(t, err)
}

func TestDialogHeaderDecodesTheRoutingFields(t *testing.T) {
	t.Parallel()
	var head dialogHeader
	require.NoError(t, json.Unmarshal([]byte(frameApprovalDialog), &head))
	assert.Equal(t, dialogHeader{
		ID: "158b2ba5001bfb93", Method: "select", Title: "Allow tool: bash\nCommand: echo approved-run", Options: []string{"Approve", "Deny"},
	}, head)
}

func TestDialogHeaderDeadline(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		timeout float64
		want    time.Duration
	}{
		{timeout: 0, want: 0},
		{timeout: -5, want: 0},
		{timeout: 30000, want: 30 * time.Second},
		{timeout: 1.5, want: 1500 * time.Microsecond},
		// Past what a time.Duration holds: no limit in practice.
		{timeout: 1e300, want: 0},
		{timeout: maxDialogTimeoutMillis, want: 0},
	} {
		assert.Equal(t, tc.want, dialogHeader{Timeout: tc.timeout}.deadline(), tc.timeout)
	}
}
