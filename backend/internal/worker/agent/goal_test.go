package agent

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func TestGoalStatusWire_RoundTripsEveryStatus(t *testing.T) {
	t.Parallel()
	for _, status := range []GoalStatus{
		GoalStatusNone, GoalStatusActive, GoalStatusPaused, GoalStatusBlocked, GoalStatusDone,
		GoalStatusUnknown, GoalStatusDormant,
	} {
		assert.Equal(t, status, GoalStatusFromWire(GoalStatusWire(status)))
	}
}

func TestGoalUnknownStatusKeepsItsOwnTokenAndDormantOrdinal(t *testing.T) {
	t.Parallel()
	const unknown = GoalStatusUnknown
	assert.Equal(t, GoalStatus(5), unknown)
	assert.Equal(t, "unknown", GoalStatusWire(unknown))
	assert.Equal(t, unknown, GoalStatusFromWire("unknown"))
	assert.Equal(t, leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_UNKNOWN, GoalStatusToProto(unknown))
	assert.Equal(t, GoalStatus(6), GoalStatusDormant)
	assert.Equal(t, "dormant", GoalStatusWire(GoalStatus(6)))
	assert.Equal(t, GoalStatus(6), GoalStatusFromWire("dormant"))
	assert.Equal(t, unknown, (GoalUpdate{Objective: "Native objective", Status: unknown, StatusDetail: "futureState"}).Clean().Status)
}

// Every declared status needs its notification token.
// Storage holds enum ordinals, so its CHECK cannot detect an omitted payload token.
// This test keeps the payload mapping complete.
func TestGoalStatusWire_UsesTheTokensTheColumnAccepts(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "", GoalStatusWire(GoalStatusNone))
	assert.Equal(t, "active", GoalStatusWire(GoalStatusActive))
	assert.Equal(t, "paused", GoalStatusWire(GoalStatusPaused))
	assert.Equal(t, "blocked", GoalStatusWire(GoalStatusBlocked))
	assert.Equal(t, "done", GoalStatusWire(GoalStatusDone))
	assert.Equal(t, "unknown", GoalStatusWire(GoalStatusUnknown))
	assert.Equal(t, "dormant", GoalStatusWire(GoalStatusDormant))
}

// A token this build cannot interpret must never read as active: the card would
// offer Pause for a state nothing can act on.
func TestGoalStatusFromWire_ReadsAnUnknownTokenAsNoGoal(t *testing.T) {
	t.Parallel()
	assert.Equal(t, GoalStatusNone, GoalStatusFromWire("supernova"))
	assert.Equal(t, leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_UNSPECIFIED,
		GoalStatusToProto(GoalStatusFromWire("supernova")))
}

// StripUnreadable preserves the objective's line breaks because the objective is prose.
// Whitespace normalization would change the paragraph that the user supplies.
func TestGoalUpdateClean_KeepsTheLineBreaksInAnObjective(t *testing.T) {
	t.Parallel()
	got := GoalUpdate{Objective: "Fix the flake\nthen ship it", Status: GoalStatusActive}.Clean()
	assert.Equal(t, "Fix the flake\nthen ship it", got.Objective)
}

// One invalid byte makes proto.Marshal reject the complete AgentGoalChanged message.
// Without this repair, a provider byte could keep the panel empty without an explanatory log.
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
		Objective:    strings.Repeat("o", contracts.GoalObjectiveByteLimit*2),
		StatusDetail: strings.Repeat("d", contracts.GoalStatusDetailByteLimit*2),
		Status:       GoalStatusActive,
	}.Clean()
	assert.LessOrEqual(t, len(got.Objective), contracts.GoalObjectiveByteLimit)
	assert.LessOrEqual(t, len(got.StatusDetail), contracts.GoalStatusDetailByteLimit)
}

func TestGoalUpdateCleanKeepsUnknownAtMultibyteDetailLimit(t *testing.T) {
	t.Parallel()
	got := GoalUpdate{
		Objective: "Native objective", Status: GoalStatusUnknown,
		StatusDetail: strings.Repeat("한", contracts.GoalStatusDetailByteLimit),
	}.Clean()
	assert.Equal(t, GoalStatusUnknown, got.Status)
	assert.Equal(t, "Native objective", got.Objective)
	assert.True(t, utf8.ValidString(got.StatusDetail))
	assert.Equal(t, strings.Repeat("한", contracts.GoalStatusDetailByteLimit/len("한")), got.StatusDetail)
	assert.LessOrEqual(t, len(got.StatusDetail), contracts.GoalStatusDetailByteLimit)
}

// GoalStatusNone means no goal, so an objective with that status contradicts itself.
// The shared cleaner maps the missing status to Blocked.
// Unknown remains a distinct valid native state.
func TestGoalUpdateClean_RefusesAnObjectiveWithNoStatus(t *testing.T) {
	t.Parallel()
	got := GoalUpdate{Objective: "Ship it", Status: GoalStatusNone}.Clean()
	assert.Equal(t, GoalStatusBlocked, got.Status)
	assert.NotEmpty(t, GoalStatusWire(got.Status),
		"a stored objective must never carry the empty status token")
}

// A status without a readable objective must not show live controls for an unreadable goal.
// These inputs can produce the empty objective:
//   - A ZCode report with only its status present.
//   - A Reasonix report while its state machine starts.
//   - StripUnreadable removes every objective byte.
func TestGoalUpdateClean_RefusesAStatusWithNoObjective(t *testing.T) {
	t.Parallel()
	tokens := int64(900)
	got := GoalUpdate{Status: GoalStatusActive, StatusDetail: "active", TokensUsed: &tokens}.Clean()
	assert.Equal(t, GoalStatusNone, got.Status, "a goal with no text is no goal")
	assert.Empty(t, got.StatusDetail)
	assert.Nil(t, got.TokensUsed, "the counters measured a goal that does not exist")
}

// StripUnreadable removes an objective that contains only control characters.
// The cleaned empty report must behave as an originally empty report.
func TestGoalUpdateClean_AnUnreadableObjectiveBecomesNoGoal(t *testing.T) {
	t.Parallel()
	got := GoalUpdate{Objective: "\x01\x02\x03", Status: GoalStatusActive}.Clean()
	assert.Empty(t, got.Objective)
	assert.Equal(t, GoalStatusNone, got.Status)
}

// An empty objective with no status means no goal and must stay unchanged.
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
