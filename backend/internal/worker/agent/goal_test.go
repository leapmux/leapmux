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
		GoalStatusDormant,
	} {
		assert.Equal(t, status, GoalStatusFromWire(GoalStatusWire(status)))
	}
}

// Every status must map to a token the column's CHECK constraint accepts. An
// unmapped status yields "", which the constraint also accepts, so the write
// succeeds and stores a status every reader takes as "no goal". This test is
// what keeps the map complete.
func TestGoalStatusWire_UsesTheTokensTheColumnAccepts(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "", GoalStatusWire(GoalStatusNone))
	assert.Equal(t, "active", GoalStatusWire(GoalStatusActive))
	assert.Equal(t, "paused", GoalStatusWire(GoalStatusPaused))
	assert.Equal(t, "blocked", GoalStatusWire(GoalStatusBlocked))
	assert.Equal(t, "done", GoalStatusWire(GoalStatusDone))
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
		Objective:    strings.Repeat("o", contracts.GoalObjectiveByteLimit*2),
		StatusDetail: strings.Repeat("d", contracts.GoalStatusDetailByteLimit*2),
		Status:       GoalStatusActive,
	}.Clean()
	assert.LessOrEqual(t, len(got.Objective), contracts.GoalObjectiveByteLimit)
	assert.LessOrEqual(t, len(got.StatusDetail), contracts.GoalStatusDetailByteLimit)
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
		"a stored objective must never carry the empty status token")
}

// The other direction, which the card renders as a goal with no text: an armed
// status dot and live Pause and Clear buttons above an empty line. Three routes
// reach it -- ZCode returns early only when BOTH halves are empty, Reasonix
// sends an absent objective as "", and StripUnreadable empties a string made
// only of control characters.
func TestGoalUpdateClean_RefusesAStatusWithNoObjective(t *testing.T) {
	t.Parallel()
	tokens := int64(900)
	got := GoalUpdate{Status: GoalStatusActive, StatusDetail: "active", TokensUsed: &tokens}.Clean()
	assert.Equal(t, GoalStatusNone, got.Status, "a goal with no text is no goal")
	assert.Empty(t, got.StatusDetail)
	assert.Nil(t, got.TokensUsed, "the counters measured a goal that does not exist")
}

// An objective made only of control characters is emptied by StripUnreadable,
// and the emptied report must resolve the same way as one that arrived empty.
func TestGoalUpdateClean_AnUnreadableObjectiveBecomesNoGoal(t *testing.T) {
	t.Parallel()
	got := GoalUpdate{Objective: "\x01\x02\x03", Status: GoalStatusActive}.Clean()
	assert.Empty(t, got.Objective)
	assert.Equal(t, GoalStatusNone, got.Status)
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

func TestFoldGoalObjective_CollapsesWhitespace(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "ship the release", foldGoalObjective("  ship\n\tthe  release  "))
}

func TestParseGoalCommandText_ClassifiesTheCompleteCommand(t *testing.T) {
	t.Parallel()
	clearArgs := []string{"clear", "off"}
	for _, test := range []struct {
		name      string
		text      string
		intent    goalTextIntent
		objective string
	}{
		{name: "other text", text: "please /goal ship", intent: goalTextNotCommand},
		{name: "bare query", text: "/goal", intent: goalTextBareQuery},
		{name: "empty argument", text: "/goal   ", intent: goalTextBareQuery},
		{name: "clear", text: "/goal CLEAR", intent: goalTextClear},
		{name: "set clear prefix", text: "/goal clear the queue", intent: goalTextSet, objective: "clear the queue"},
		{name: "set folded", text: "/goal ship\tthe release", intent: goalTextSet, objective: "ship the release"},
		{name: "literal space delimiter", text: "/goal\tship", intent: goalTextNotCommand},
		// A provider reads the remainder of the LINE, so a second line belongs
		// to no command and must not enter the objective. Storing it would
		// state a goal longer than the one the provider installed, and no text
		// route reports the goal back to correct the difference.
		{
			name: "second line excluded", text: "/goal keep the build green\nand update the changelog",
			intent: goalTextSet, objective: "keep the build green",
		},
		// The same rule for a clear: the first line is the whole command.
		{name: "clear with a second line", text: "/goal off\nthen rest", intent: goalTextClear},
		// A leading blank line still leaves the command on the first line that
		// TrimSpace exposes.
		{name: "leading blank line", text: "\n\n/goal ship it", intent: goalTextSet, objective: "ship it"},
		// The command must not match a line further down the message: a user
		// who quotes a command in a paragraph is not issuing it.
		{name: "command on a later line", text: "look at this:\n/goal ship it", intent: goalTextNotCommand},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			intent, objective := parseGoalCommandText(test.text, "/goal", clearArgs)
			assert.Equal(t, test.intent, intent)
			assert.Equal(t, test.objective, objective)
		})
	}
}
