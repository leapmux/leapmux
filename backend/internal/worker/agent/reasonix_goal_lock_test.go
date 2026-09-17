package agent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// goalApplyProbe runs a callback at the moment applyReasonixGoal reaches the sink.
//
// It wraps a real sink rather than replace one: every other facet still behaves,
// and the probe observes the one call the revision protocol depends on.
type goalApplyProbe struct {
	ProviderServices
	onApply func()
}

func (p *goalApplyProbe) ClearGoal(snapshot bool) {
	p.onApply()
	p.ProviderServices.ClearGoal(snapshot)
}

func (p *goalApplyProbe) UpsertGoal(update GoalUpdate) {
	p.onApply()
	p.ProviderServices.UpsertGoal(update)
}

// applyReasonixGoal must run INSIDE the section that bumps goalStatusRevision.
//
// confirmReasonixGoal uses the revision as a completion mark: it samples the
// counter, runs a status round trip, and stores the user's objective only when the
// counter did not move. An apply that runs after the unlock makes that mark lie.
// This handler bumps 5 to 6, the scheduler stops it, confirmReasonixGoal samples 6,
// round-trips, reads 6 again and reports SUCCESS -- and then this handler resumes
// and removes the objective it just confirmed.
//
// A test that constructs that interleaving is impossible, and the fix is what makes
// it impossible. The two goroutines can only meet inside the apply, and the correct
// code holds goalStatusMu there, so confirmReasonixGoal blocks at its first lock --
// before the round trip that would release the handler. Such a test deadlocks on
// the code it passes on. The invariant the protocol needs is what this pins instead.
func TestReasonixStatusUpdateAppliesTheGoalUnderTheRevisionLock(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		payload string
		clears  int
		upserts int
	}{
		{
			name:    "a removal",
			payload: `{"sessionId":"session","status":{"goal":{"status":"complete"}}}`,
			clears:  1,
		},
		{
			name:    "an objective",
			payload: `{"sessionId":"session","status":{"goal":{"status":"running","objective":"Ship the fix"}}}`,
			upserts: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sink := &testSink{}
			a := &ReasonixAgent{}
			a.sessionID = "session"
			applies, heldDuringApply := 0, 0
			a.sink = &goalApplyProbe{ProviderServices: sink, onApply: func() {
				applies++
				// TryLock reports the STATE of the mutex rather than its owner, so it
				// fails while this goroutine's own handler holds the lock. A success
				// says the apply left the section that bumps the revision.
				if a.goalStatusMu.TryLock() {
					a.goalStatusMu.Unlock()
					return
				}
				heldDuringApply++
			}}

			a.handleReasonixStatusUpdate(json.RawMessage(tc.payload))

			require.Equal(t, 1, applies, "the handler applied the status it read")
			assert.Equal(t, 1, heldDuringApply,
				"the goal write must sit inside the section that bumps goalStatusRevision")
			assert.Equal(t, tc.clears, sink.GoalClears())
			assert.Len(t, sink.Goals(), tc.upserts)
		})
	}
}

// The two reports that follow the goal stay OUTSIDE the lock. Each one reaches the
// sink, and Reasonix restates its whole status on every turn tick, so holding the
// mutex across them made every other reader of goalStatusMu wait on a database
// write and a broadcast to every connected tab.
func TestReasonixStatusUpdateReportsUsageAndPhaseOutsideTheRevisionLock(t *testing.T) {
	t.Parallel()

	sink := &recordingControlSink{}
	a := &ReasonixAgent{}
	a.sessionID = "session"
	free := 0
	a.sink = &sessionInfoProbe{ProviderServices: sink, onReport: func() {
		if a.goalStatusMu.TryLock() {
			a.goalStatusMu.Unlock()
			free++
		}
	}}

	a.handleReasonixStatusUpdate(json.RawMessage(`{"sessionId":"session","status":{
		"phase":"waiting_permission",
		"usage":{"cumulative":{"totalTokens":1500,"promptTokens":1200,"completionTokens":300}}}}`))

	assert.Equal(t, 2, free, "the usage broadcast and the phase row both run with the lock free")
}

// sessionInfoProbe runs a callback at each of the two reports that follow the goal.
type sessionInfoProbe struct {
	ProviderServices
	onReport func()
}

func (p *sessionInfoProbe) BroadcastSessionInfo(info map[string]interface{}) {
	p.onReport()
	p.ProviderServices.BroadcastSessionInfo(info)
}

func (p *sessionInfoProbe) PersistLeapMuxNotification(payload map[string]interface{}) {
	p.onReport()
	p.ProviderServices.PersistLeapMuxNotification(payload)
}
