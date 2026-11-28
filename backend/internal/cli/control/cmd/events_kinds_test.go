package cmd

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
)

// The vocabulary is a contract for scripts: each kind is a lowercase
// "<family>.<event>" that identifies its source by its family, appears once,
// and states what it means.
func TestEventKinds_TheVocabularyIsWellFormed(t *testing.T) {
	shape := regexp.MustCompile(`^[a-z]+\.[a-z_]+$`)
	familySource := map[string]eventSource{
		"workspace":    sourceHub,
		"tab":          sourceHub,
		"agent":        sourceWorker,
		"terminal":     sourceWorker,
		"notification": sourceWorker,
		"stream":       sourceStream,
	}
	workerTabKinds := map[string]bool{
		kindTabRenamed: true,
		kindTabStatus:  true,
	}
	seen := map[string]bool{}
	for _, k := range eventKinds {
		assert.Regexp(t, shape, k.name)
		assert.False(t, seen[k.name], "%s appears twice", k.name)
		seen[k.name] = true
		source, known := familySource[kindFamily(k.name)]
		if workerTabKinds[k.name] {
			source = sourceWorker
		}
		if assert.True(t, known, "%s has an unknown family", k.name) {
			assert.Equal(t, source, k.source, "%s comes from the source of its family", k.name)
		}
		assert.NotEmpty(t, k.help, "%s states what it means", k.name)
		if k.source == sourceStream {
			assert.False(t, k.reconciled, "%s is the stream's own, which no gap loses", k.name)
		}
	}
	assert.Equal(t, []string{"workspace", "tab", "agent", "terminal", "notification", "stream"}, eventFamilies())
}

// Every kind that the code prints is in the table, so the help, the
// completion and --kind know it.
func TestEventKinds_TheTableHoldsEveryKindThatTheStreamPrints(t *testing.T) {
	printed := []string{
		kindWorkspaceCreated, kindWorkspaceRenamed, kindWorkspaceDeleted,
		kindTabOpened, kindTabClosed, kindTabMoved, kindTabRenamed, kindTabStatus,
		kindAgentActivity, kindAgentStatus, kindAgentTurnEnd, kindAgentRequest, kindAgentRequestEnded,
		kindAgentTodos, kindAgentBackgroundTasks, kindAgentGoal, kindAgentQueue,
		kindTerminalStatus, kindTerminalTitle, kindTerminalBell, kindTerminalNotification, kindTerminalProgress, kindTerminalExit,
		kindNotificationCreated, kindNotificationCleared,
		kindStreamReady, kindStreamUnavailable, kindStreamGap, kindStreamHeartbeat, kindStreamRetired,
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
		kindTabRenamed, kindTabStatus,
		kindAgentTurnEnd, kindAgentRequest, kindAgentRequestEnded, kindAgentTodos, kindAgentBackgroundTasks,
		kindAgentGoal, kindAgentQueue,
		kindTerminalStatus, kindTerminalTitle, kindTerminalBell, kindTerminalNotification, kindTerminalProgress, kindTerminalExit,
		kindNotificationCreated, kindNotificationCleared,
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

func TestEventKinds_ReplayJournalCapabilities(t *testing.T) {
	liveOnly := map[string]bool{
		kindTabRenamed:          true,
		kindTabStatus:           true,
		kindAgentGoal:           true,
		kindAgentQueue:          true,
		kindNotificationCreated: true,
		kindNotificationCleared: true,
	}
	for _, k := range eventKinds {
		switch {
		case k.source == sourceStream || liveOnly[k.name]:
			assert.Equal(t, replayNone, k.journal, k.name)
		case k.source == sourceHub:
			assert.Equal(t, replayHubLayout, k.journal, k.name)
		case kindFamily(k.name) == "agent":
			assert.Equal(t, replayWorkerAgent, k.journal, k.name)
		case kindFamily(k.name) == "terminal":
			assert.Equal(t, replayWorkerTerminal, k.journal, k.name)
		default:
			t.Fatalf("unexpected kind %s", k.name)
		}
	}
	agentKinds := eventCheckpointKinds(kindFilter{}, leapmuxv1.TabType_TAB_TYPE_AGENT)
	assert.NotContains(t, agentKinds, kindAgentGoal)
	assert.NotContains(t, agentKinds, kindAgentQueue)
	assert.Contains(t, agentKinds, kindAgentTurnEnd)

	accountKinds := accountFactKinds(kindFilter{})
	for k := range liveOnly {
		assert.NotContains(t, accountKinds, k)
	}
}

func TestValidateCheckpointFilters(t *testing.T) {
	mustFilter := func(t *testing.T, values ...string) kindFilter {
		t.Helper()
		f, err := parseKindFilter(values)
		require.NoError(t, err)
		return f
	}

	t.Run("layout checkpoint accepts tab family and retains only Hub tab kinds", func(t *testing.T) {
		kinds, err := validateLayoutCheckpointFilter(mustFilter(t, "tab"))
		require.NoError(t, err)
		assert.Equal(t, []string{kindTabOpened, kindTabClosed, kindTabMoved}, kinds)
	})

	t.Run("agent tab checkpoint accepts agent family and excludes live-only agent kinds", func(t *testing.T) {
		kinds, err := validateTabCheckpointFilter(mustFilter(t, "agent"), leapmuxv1.TabType_TAB_TYPE_AGENT)
		require.NoError(t, err)
		assert.NotContains(t, kinds, kindAgentGoal)
		assert.NotContains(t, kinds, kindAgentQueue)
		assert.Contains(t, kinds, kindAgentRequest)
	})

	for _, liveOnly := range []string{"tab.renamed", "tab.status", "notification", "notification.created", "agent.goal", "agent.queue"} {
		t.Run("rejects live-only selection "+liveOnly+" across all checkpoint scopes", func(t *testing.T) {
			f := mustFilter(t, liveOnly)
			for _, err := range []error{
				func() error { _, err := validateLayoutCheckpointFilter(f); return err }(),
				func() error { _, err := validateTabCheckpointFilter(f, leapmuxv1.TabType_TAB_TYPE_AGENT); return err }(),
				func() error { _, err := validateAccountCheckpointFilter(f); return err }(),
			} {
				e := cli.FromError(err)
				require.NotNil(t, e)
				assert.Equal(t, cli.CodeUsage, e.Code)
				assert.Contains(t, e.Message, "live-only")
				require.NotEmpty(t, e.Hints)
				assert.Contains(t, e.Hints[0], "omit --checkpoint")
				assert.NotContains(t, e.Hints[0], "account checkpoint")
				assert.NotContains(t, e.Hints[0], "--tab")
			}
		})
	}

	t.Run("tab checkpoint rejects incompatible retained kinds with accurate hint", func(t *testing.T) {
		_, err := validateTabCheckpointFilter(mustFilter(t, "terminal.bell"), leapmuxv1.TabType_TAB_TYPE_AGENT)
		e := cli.FromError(err)
		require.NotNil(t, e)
		assert.Equal(t, cli.CodeUsage, e.Code)
		assert.Contains(t, e.Message, "terminal.bell")
		assert.Contains(t, e.Message, "agent tab checkpoint")
	})

	t.Run("layout checkpoint rejects Worker retained families", func(t *testing.T) {
		_, err := validateLayoutCheckpointFilter(mustFilter(t, "terminal"))
		e := cli.FromError(err)
		require.NotNil(t, e)
		assert.Equal(t, cli.CodeUsage, e.Code)
		assert.Contains(t, e.Message, "layout checkpoint")
	})

	t.Run("stream.retired is rejected outside tab checkpoints", func(t *testing.T) {
		_, err := validateLayoutCheckpointFilter(mustFilter(t, kindStreamRetired))
		require.Error(t, err)
		_, err = validateAccountCheckpointFilter(mustFilter(t, kindStreamRetired))
		require.Error(t, err)
		_, err = validateTabCheckpointFilter(mustFilter(t, kindStreamRetired), leapmuxv1.TabType_TAB_TYPE_TERMINAL)
		require.NoError(t, err)
	})
}
