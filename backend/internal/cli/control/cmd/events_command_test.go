package cmd

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
)

// eventsOptionsFor parses args as the flags of `events`, and reads them as
// the command does before it sends anything.
func eventsOptionsFor(t *testing.T, args ...string) (eventsOptions, error) {
	t.Helper()
	cmd := newEventsCommand()
	require.NoError(t, cmd.ParseFlags(args))
	return eventsOptionsOf(&cli.Invocation{Command: cmd, Printer: &cli.Printer{}})
}

// TestEventsOptionsOf pins the limits of the flags of `events`. The command
// line test of the refusals runs the whole command, so it cannot show which
// value a flag accepted.
func TestEventsOptionsOf(t *testing.T) {
	t.Run("no flag watches every kind of every workspace without a limit", func(t *testing.T) {
		opts, err := eventsOptionsFor(t)
		require.NoError(t, err)
		assert.Nil(t, opts.filter.selected)
		assert.Zero(t, opts.limit)
		assert.Zero(t, opts.heartbeat)
		assert.Empty(t, opts.workspace)
	})
	t.Run("a heartbeat of exactly the shortest interval passes", func(t *testing.T) {
		opts, err := eventsOptionsFor(t, "--heartbeat", minEventHeartbeat.String())
		require.NoError(t, err)
		assert.Equal(t, minEventHeartbeat, opts.heartbeat)
	})
	t.Run("a heartbeat just below the shortest interval fails", func(t *testing.T) {
		_, err := eventsOptionsFor(t, "--heartbeat", (minEventHeartbeat - time.Millisecond).String())
		e := cli.FromError(err)
		require.NotNil(t, e)
		assert.Equal(t, cli.CodeUsage, e.Code)
		assert.Contains(t, e.Message, "--heartbeat must be 0 or at least 1s")
	})
	t.Run("a heartbeat of zero prints none", func(t *testing.T) {
		opts, err := eventsOptionsFor(t, "--heartbeat", "0s")
		require.NoError(t, err)
		assert.Zero(t, opts.heartbeat)
	})
	t.Run("a limit of zero runs without a limit", func(t *testing.T) {
		opts, err := eventsOptionsFor(t, "--limit", "0")
		require.NoError(t, err)
		assert.Zero(t, opts.limit)
	})
	t.Run("a limit of one ends after the first event", func(t *testing.T) {
		opts, err := eventsOptionsFor(t, "--limit", "1")
		require.NoError(t, err)
		assert.Equal(t, 1, opts.limit)
	})
	t.Run("kinds separated by commas and repeated flags add up", func(t *testing.T) {
		opts, err := eventsOptionsFor(t, "--kind", "agent.turn_end,terminal", "--kind", "stream.gap")
		require.NoError(t, err)
		for _, k := range eventKinds {
			want := k.name == kindAgentTurnEnd || k.name == kindStreamGap || kindFamily(k.name) == "terminal"
			assert.Equal(t, want, opts.filter.selects(k.name), k.name)
		}
	})
	t.Run("a stated workspace passes as it is", func(t *testing.T) {
		opts, err := eventsOptionsFor(t, "--workspace", "title:First")
		require.NoError(t, err)
		assert.Equal(t, "title:First", opts.workspace)
	})
	t.Run("an empty workspace fails", func(t *testing.T) {
		_, err := eventsOptionsFor(t, "--workspace", "")
		e := cli.FromError(err)
		require.NotNil(t, e)
		assert.Equal(t, cli.CodeUsage, e.Code)
		assert.Equal(t, "--workspace is empty", e.Message)
	})
	t.Run("quiet format is rejected", func(t *testing.T) {
		cmd := newEventsCommand()
		_, err := eventsOptionsOf(&cli.Invocation{Command: cmd, Printer: &cli.Printer{Format: cli.FormatQuiet}})
		e := cli.FromError(err)
		require.NotNil(t, e)
		assert.Equal(t, cli.CodeUsage, e.Code)
		assert.Contains(t, e.Message, "--quiet does not apply to events")
	})
}

// The help lists every kind of the vocabulary with its meaning, one bullet
// each and in the order of the table, so the reference page that the help
// generates lists them too.
func TestEventsLongHelp_ListsEveryKind(t *testing.T) {
	var bullets []string
	for line := range strings.SplitSeq(eventsLongHelp(), "\n") {
		if strings.HasPrefix(line, "  - ") {
			bullets = append(bullets, line)
		}
	}
	want := make([]string, len(eventKinds))
	for i, k := range eventKinds {
		want[i] = "  - " + k.name + ": " + k.help
	}
	assert.Equal(t, want, bullets)
}

func TestResolveEventScope_SupportsParentTab(t *testing.T) {
	hub := &recordingHub{}
	agents := []*leapmuxv1.AgentInfo{
		{Id: "parent-agent-1", Title: "Parent", WorkerId: "worker-A"},
		{Id: "child-agent-1", Title: "Child", ParentAgentId: "parent-agent-1", WorkerId: "worker-A"},
	}
	startSpawnIPC(t, hub, phase2aAgentDispatcher{agents: agents})
	t.Setenv(controlenv.TabIDVariable, "child-agent-1")
	t.Setenv(controlenv.TabTypeVariable, "agent")
	t.Setenv(controlenv.WorkerIDVariable, "worker-A")

	cmd := newEventsCommand()
	inv := &cli.Invocation{Context: t.Context(), Command: cmd, Printer: &cli.Printer{}}
	s, err := openStreamSession(inv)
	require.NoError(t, err)
	defer s.Close()

	wsID, tabID, err := resolveEventScope(s, eventsOptions{tab: "parent"})
	require.NoError(t, err)
	assert.Empty(t, wsID)
	assert.Equal(t, "parent-agent-1", tabID)
}
