package cmd

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/cli"
)

// The vocabulary is a contract for scripts: each kind is a lowercase
// "<family>.<event>" that names its source by its family, appears once, and
// states what it means.
func TestEventKinds_TheVocabularyIsWellFormed(t *testing.T) {
	shape := regexp.MustCompile(`^[a-z]+\.[a-z_]+$`)
	familySource := map[string]eventSource{
		"workspace": sourceHub,
		"tab":       sourceHub,
		"agent":     sourceWorker,
		"terminal":  sourceWorker,
		"stream":    sourceStream,
	}
	seen := map[string]bool{}
	for _, k := range eventKinds {
		assert.Regexp(t, shape, k.name)
		assert.False(t, seen[k.name], "%s appears twice", k.name)
		seen[k.name] = true
		source, known := familySource[kindFamily(k.name)]
		if assert.True(t, known, "%s has an unknown family", k.name) {
			assert.Equal(t, source, k.source, "%s comes from the source of its family", k.name)
		}
		assert.NotEmpty(t, k.help, "%s states what it means", k.name)
		if k.source == sourceStream {
			assert.False(t, k.reconciled, "%s is the stream's own, which no gap loses", k.name)
		}
	}
	assert.Equal(t, []string{"workspace", "tab", "agent", "terminal", "stream"}, eventFamilies())
}

// Every kind that the code prints is in the table, so the help, the
// completion and --kind know it.
func TestEventKinds_TheTableHoldsEveryKindThatTheStreamPrints(t *testing.T) {
	printed := []string{
		kindWorkspaceCreated, kindWorkspaceRenamed, kindWorkspaceDeleted,
		kindTabOpened, kindTabClosed, kindTabMoved,
		kindAgentActivity, kindAgentStatus, kindAgentTurnEnd, kindAgentRequest, kindAgentRequestEnded,
		kindAgentTodos, kindAgentBackgroundTasks,
		kindTerminalStatus, kindTerminalTitle, kindTerminalBell, kindTerminalNotification, kindTerminalProgress, kindTerminalExit,
		kindStreamReady, kindStreamUnavailable, kindStreamGap, kindStreamHeartbeat,
	}
	assert.ElementsMatch(t, printed, eventKindNames())
}

func TestParseKindFilter(t *testing.T) {
	t.Run("no value selects every kind", func(t *testing.T) {
		f, err := parseKindFilter(nil)
		require.NoError(t, err)
		for _, k := range eventKindNames() {
			assert.True(t, f.selects(k), k)
		}
	})
	t.Run("a kind selects itself alone", func(t *testing.T) {
		f, err := parseKindFilter([]string{"agent.request"})
		require.NoError(t, err)
		assert.True(t, f.selects(kindAgentRequest))
		assert.False(t, f.selects(kindAgentRequestEnded), "a kind is no prefix of a longer kind")
		assert.False(t, f.selects(kindStreamGap))
	})
	t.Run("a family selects each of its kinds", func(t *testing.T) {
		f, err := parseKindFilter([]string{"terminal", "tab.closed"})
		require.NoError(t, err)
		for _, k := range eventKinds {
			want := kindFamily(k.name) == "terminal" || k.name == kindTabClosed
			assert.Equal(t, want, f.selects(k.name), k.name)
		}
	})
	t.Run("white space around a value and a repeated value pass", func(t *testing.T) {
		f, err := parseKindFilter([]string{" agent.turn_end ", "agent.turn_end"})
		require.NoError(t, err)
		assert.True(t, f.selects(kindAgentTurnEnd))
		assert.Len(t, f.selected, 1)
	})
	for _, value := range []string{"agent.req", "agent.", "agents", "", "AGENT", "stream.*"} {
		t.Run("refuses "+value, func(t *testing.T) {
			_, err := parseKindFilter([]string{"agent", value})
			e := cli.FromError(err)
			require.NotNil(t, e)
			assert.Equal(t, cli.CodeUsage, e.Code)
			assert.Contains(t, e.Message, `"`+value+`"`)
			require.Len(t, e.Hints, 1)
			for _, known := range append(eventFamilies(), eventKindNames()...) {
				assert.Contains(t, e.Hints[0], known, "the hint lists every value that --kind accepts")
			}
		})
	}
}

func TestLostKinds(t *testing.T) {
	assert.Equal(t, []string{kindWorkspaceRenamed}, lostKinds(sourceHub, false),
		"a snapshot restores the tabs and the workspaces, but not a title")
	assert.Equal(t, []string{
		kindAgentTurnEnd, kindAgentRequest, kindAgentRequestEnded, kindAgentTodos, kindAgentBackgroundTasks,
		kindTerminalStatus, kindTerminalTitle, kindTerminalBell, kindTerminalNotification, kindTerminalProgress, kindTerminalExit,
	}, lostKinds(sourceWorker, false))
	withLevels := lostKinds(sourceWorker, true)
	assert.Contains(t, withLevels, kindAgentActivity, "a failed read loses the activity too")
	assert.Contains(t, withLevels, kindAgentStatus)
	assert.Len(t, withLevels, len(lostKinds(sourceWorker, false))+2)
}

func TestKindFlagValues_ListsTheFamiliesThenTheKinds(t *testing.T) {
	values := kindFlagValues()
	assert.Equal(t, eventFamilies(), values[:len(eventFamilies())])
	assert.Equal(t, eventKindNames(), values[len(eventFamilies()):])
	for _, v := range values {
		_, err := parseKindFilter([]string{v})
		assert.NoError(t, err, "--kind accepts every value that its completion offers: %s", v)
	}
}
