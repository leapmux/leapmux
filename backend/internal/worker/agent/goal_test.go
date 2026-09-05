package agent

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func TestGoalStatusWire_RoundTripsEveryStatus(t *testing.T) {
	t.Parallel()
	for _, status := range []GoalStatus{
		GoalStatusNone, GoalStatusActive, GoalStatusPaused, GoalStatusBlocked, GoalStatusDone,
	} {
		assert.Equal(t, status, GoalStatusFromWire(GoalStatusWire(status)))
	}
}

// The column has a CHECK constraint over exactly these tokens, so an unmapped
// status would fail the write instead of storing something nothing reads back.
func TestGoalStatusWire_UsesTheTokensTheColumnAccepts(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "", GoalStatusWire(GoalStatusNone))
	assert.Equal(t, "active", GoalStatusWire(GoalStatusActive))
	assert.Equal(t, "paused", GoalStatusWire(GoalStatusPaused))
	assert.Equal(t, "blocked", GoalStatusWire(GoalStatusBlocked))
	assert.Equal(t, "done", GoalStatusWire(GoalStatusDone))
}

// A token this build cannot interpret must never read as active: the card would
// offer Pause for a state nothing can act on.
func TestGoalStatusFromWire_ReadsAnUnknownTokenAsNoGoal(t *testing.T) {
	t.Parallel()
	assert.Equal(t, GoalStatusNone, GoalStatusFromWire("supernova"))
	assert.Equal(t, leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_UNSPECIFIED,
		GoalStatusToProto(GoalStatusFromWire("supernova")))
}

// An objective is PROSE. StripUnreadable keeps its line breaks, because a rule
// that folded whitespace would reflow the paragraph the user typed.
func TestGoalUpdateClean_KeepsTheLineBreaksInAnObjective(t *testing.T) {
	t.Parallel()
	got := GoalUpdate{Objective: "Fix the flake\nthen ship it", Status: GoalStatusActive}.Clean()
	assert.Equal(t, "Fix the flake\nthen ship it", got.Objective)
}

// ONE invalid byte makes proto.Marshal fail for the WHOLE AgentGoalChanged
// message, and that message is the only way the panel ever populates -- so a bad
// byte from one provider would leave an empty panel forever with nothing logged.
func TestGoalUpdateClean_DropsBytesThatWouldFailProtoMarshal(t *testing.T) {
	t.Parallel()
	got := GoalUpdate{Objective: "ship \xff it\x00", StatusDetail: "ok\x07", Status: GoalStatusActive}.Clean()
	assert.True(t, utf8.ValidString(got.Objective), "an invalid byte must not reach proto.Marshal")
	assert.NotContains(t, got.Objective, "\x00")
	assert.NotContains(t, got.StatusDetail, "\x07")
}

func TestGoalUpdateClean_CapsBothProviderWrittenStrings(t *testing.T) {
	t.Parallel()
	got := GoalUpdate{
		Objective:    strings.Repeat("o", GoalObjectiveByteLimit*2),
		StatusDetail: strings.Repeat("d", GoalStatusDetailByteLimit*2),
		Status:       GoalStatusActive,
	}.Clean()
	assert.LessOrEqual(t, len(got.Objective), GoalObjectiveByteLimit)
	assert.LessOrEqual(t, len(got.StatusDetail), GoalStatusDetailByteLimit)
}

// GoalStatusNone means "no goal", so a report that states an objective AND no
// status says a goal exists and does not. Resolving it to blocked is what every
// provider already does for a status word it cannot read.
//
// It is not cosmetic: a stored objective with a blank status is the exact mark
// the applier reads as "this goal outlived a worker restart", so a provider able
// to write that state by hand would silence a real transition.
func TestGoalUpdateClean_RefusesAnObjectiveWithNoStatus(t *testing.T) {
	t.Parallel()
	got := GoalUpdate{Objective: "Ship it", Status: GoalStatusNone}.Clean()
	assert.Equal(t, GoalStatusBlocked, got.Status)
	assert.NotEmpty(t, GoalStatusWire(got.Status),
		"the stored token must never be the one the boot sweep writes")
}

// An EMPTY objective with no status is the honest spelling of "no goal" and must
// be left exactly as it is.
func TestGoalUpdateClean_LeavesAnEmptyReportAlone(t *testing.T) {
	t.Parallel()
	got := GoalUpdate{}.Clean()
	assert.Equal(t, GoalStatusNone, got.Status)
	assert.Empty(t, got.Objective)
}

func TestGoalActionFromProto_RejectsTheUnspecifiedAction(t *testing.T) {
	t.Parallel()
	_, ok := GoalActionFromProto(leapmuxv1.AgentGoalAction_AGENT_GOAL_ACTION_UNSPECIFIED)
	assert.False(t, ok, "the unspecified action has no meaning and must not default to one")

	for _, action := range []GoalAction{GoalActionSet, GoalActionClear, GoalActionPause, GoalActionResume} {
		back, ok := GoalActionFromProto(GoalActionToProto(action))
		assert.True(t, ok)
		assert.Equal(t, action, back)
	}
}
