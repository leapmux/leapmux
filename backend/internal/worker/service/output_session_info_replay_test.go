package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// replayedSessionInfo decodes the `info` map out of a replay frame.
func replayedSessionInfo(t *testing.T, event *leapmuxv1.AgentEvent) map[string]interface{} {
	t.Helper()
	msg := event.GetAgentMessage()
	require.NotNil(t, msg, "the replay must ship as an agent message")
	assert.EqualValues(t, -1, msg.GetSeq(), "session info stays ephemeral on the replay")

	decompressed, err := msgcodec.Decompress(msg.GetContent(), msg.GetContentCompression())
	require.NoError(t, err)
	var content struct {
		Type string                 `json:"type"`
		Info map[string]interface{} `json:"info"`
	}
	require.NoError(t, json.Unmarshal(decompressed, &content))
	assert.Equal(t, "agent_session_info", content.Type)
	return content.Info
}

// The gap this closes: agent_session_info never reaches a message row, so a
// browser reload replays every message and none of the counters. The goal card
// showed the objective with an empty progress row until the provider reported
// the next value, which for Codex is a whole turn away.
func TestSessionInfoReplay_RestoresTheCachedCounters(t *testing.T) {
	t.Parallel()

	svc, sink, _ := newSessionInfoServiceFixture(t)
	sink.BroadcastSessionInfo(map[string]interface{}{
		contracts.SessionInfoKeyGoalProgress: map[string]interface{}{
			contracts.GoalProgressFieldTokensUsed: 900,
			contracts.GoalProgressFieldIterations: 3,
		},
		contracts.SessionInfoKeyTotalCostUsd: 0.42,
	})

	event := svc.Output.SessionInfoReplayEvent("agent-1")
	require.NotNil(t, event, "a sink that broadcast counters has something to replay")
	assert.Equal(t, "agent-1", event.GetAgentId())

	info := replayedSessionInfo(t, event)
	assert.EqualValues(t, 0.42, info[contracts.SessionInfoKeyTotalCostUsd])
	progress, ok := info[contracts.SessionInfoKeyGoalProgress].(map[string]interface{})
	require.True(t, ok, "goal_progress replays as its own object")
	assert.EqualValues(t, 900, progress[contracts.GoalProgressFieldTokensUsed])
	assert.EqualValues(t, 3, progress[contracts.GoalProgressFieldIterations])
}

// The replay reads the same cache the dedup writes, so it must carry the LAST
// value of a key rather than the first. A stale replay is worse than none: the
// card would show a number the agent already moved past.
func TestSessionInfoReplay_CarriesTheLatestValueOfEachKey(t *testing.T) {
	t.Parallel()

	svc, sink, _ := newSessionInfoServiceFixture(t)
	sink.BroadcastSessionInfo(map[string]interface{}{contracts.SessionInfoKeyTotalCostUsd: 0.10})
	sink.BroadcastSessionInfo(map[string]interface{}{contracts.SessionInfoKeyTotalCostUsd: 0.20})

	info := replayedSessionInfo(t, svc.Output.SessionInfoReplayEvent("agent-1"))
	assert.EqualValues(t, 0.20, info[contracts.SessionInfoKeyTotalCostUsd])
}

// A running-tool update is never cached, so replay cannot restore a badge for
// a tool that already ended. The progress publisher supplies active counters.
func TestSessionInfoReplay_OmitsRunningToolsAndIncludesActiveProgress(t *testing.T) {
	t.Parallel()

	svc, sink, _ := newSessionInfoServiceFixture(t)
	sink.BroadcastSessionInfo(map[string]interface{}{
		contracts.SessionInfoKeyRunningTool:  map[string]interface{}{"span_id": "s-1"},
		contracts.SessionInfoKeyTotalCostUsd: 7.5,
	})
	sink.ReportProgress(agent.NativeTokenProgress("model", 42))

	info := replayedSessionInfo(t, svc.Output.SessionInfoReplayEvent("agent-1"))
	assert.EqualValues(t, 42, info[contracts.SessionInfoKeyThinkingTokens])
	assert.NotContains(t, info, contracts.SessionInfoKeyRunningTool)
	assert.EqualValues(t, 7.5, info[contracts.SessionInfoKeyTotalCostUsd], "a cached key still replays")
}

// An inactive agent has no live counters, and an agent whose process reported
// nothing yet has none either. Both answer nil, so the subscribe path sends no
// frame rather than an empty one.
func TestSessionInfoReplay_IsNilWithNothingToRestore(t *testing.T) {
	t.Parallel()

	svc, _, _ := newSessionInfoServiceFixture(t)
	assert.Nil(t, svc.Output.SessionInfoReplayEvent("no-such-agent"), "no process, no counters")
	assert.Nil(t, svc.Output.SessionInfoReplayEvent("agent-1"), "a silent sink has nothing to replay")
}

func TestSessionInfoReplay_UsesTheChildProgressPublisher(t *testing.T) {
	t.Parallel()

	svc, root, _ := newSessionInfoServiceFixture(t)
	child := root.ChildSink("child-1")
	root.ReportProgress(agent.NativeTokenProgress("root-model", 40))
	child.ReportProgress(agent.NativeTokenProgress("child-model", 7))

	event := svc.Output.SessionInfoReplayEvent("child-1")
	require.NotNil(t, event)
	assert.Equal(t, "child-1", event.GetAgentId())
	info := replayedSessionInfo(t, event)
	assert.EqualValues(t, 7, info[contracts.SessionInfoKeyThinkingTokens])
}

func TestSessionInfoReplay_SurvivesControlRequestCleanup(t *testing.T) {
	t.Parallel()

	svc, sink, _ := newSessionInfoServiceFixture(t)
	sink.ReportProgress(agent.NativeTokenProgress("model", 42))
	svc.deleteControlRequest("agent-1",
		controlResponseRequestMetadata{RequestID: "request-1"}, false)

	event := svc.Output.SessionInfoReplayEvent("agent-1")
	require.NotNil(t, event)
	info := replayedSessionInfo(t, event)
	assert.EqualValues(t, 42, info[contracts.SessionInfoKeyThinkingTokens])
}

func TestNewSinkClosesTheReplacedProgressTree(t *testing.T) {
	t.Parallel()

	svc, sink, broadcasts := newSessionInfoServiceFixture(t)
	root := requireRootOutputSink(t, svc.Output, "agent-1")
	child := requireChildOutputSink(t, root, "child-1")
	sink.ReportProgress(agent.NativeTokenProgress("model", 42))
	child.ReportProgress(agent.OutputDeltaProgress("tool", 512))

	svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_PI)
	replacementRoot := requireRootOutputSink(t, svc.Output, "agent-1")
	require.NotSame(t, root, replacementRoot)

	for _, publisher := range []*generationProgressPublisher{root.progress, child.progress} {
		publisher.mu.Lock()
		closed := publisher.closed
		publisher.mu.Unlock()
		assert.True(t, closed, "a replaced process must stop every old progress publisher")
	}
	infos := broadcasts.snapshot()
	require.NotEmpty(t, infos)
	last := infos[len(infos)-1]
	assert.EqualValues(t, 0, last[contracts.SessionInfoKeyThinkingTokens])
	assert.EqualValues(t, 0, last[contracts.SessionInfoKeyOutputBytes])
}

func TestCleanupAgentClosesNestedProgressPublishers(t *testing.T) {
	t.Parallel()

	for _, cleanupID := range []string{"agent-1", "child-1"} {
		cleanupID := cleanupID
		t.Run(cleanupID, func(t *testing.T) {
			t.Parallel()
			svc, _, _ := newSessionInfoServiceFixture(t)
			root := requireRootOutputSink(t, svc.Output, "agent-1")
			child := requireChildOutputSink(t, root, "child-1")
			grandchild := requireChildOutputSink(t, child, "grandchild-1")
			grandchild.ReportProgress(agent.NativeTokenProgress("model", 9))

			svc.Output.CleanupAgent(cleanupID)

			for _, publisher := range []*generationProgressPublisher{child.progress, grandchild.progress} {
				publisher.mu.Lock()
				closed := publisher.closed
				publisher.mu.Unlock()
				assert.True(t, closed, "cleanup must stop each publisher below its target")
			}
		})
	}
}

// The whole path, not only the builder: a client that subscribes must receive
// the counters in its catch-up burst. Pins the wiring in replayAgentCatchUp,
// which is what a browser reload actually exercises -- the unit tests above
// would all pass with the call site absent.
func TestWatchEvents_CatchUpReplaysTheSessionCounters(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: "/tmp", HomeDir: "/tmp",
	}))

	// The running process reports its counters BEFORE this client subscribes,
	// which is the reload it stands for: the frame went out to nobody.
	sink := svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_PI)
	sink.BroadcastSessionInfo(map[string]interface{}{
		contracts.SessionInfoKeyGoalProgress: map[string]interface{}{
			contracts.GoalProgressFieldIterations: 4,
		},
	})

	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1", Mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}},
	}, w)
	require.Eventually(t, func() bool {
		return countCatchUpCompletes(w) == 1
	}, 5*time.Second, 20*time.Millisecond, "the catch-up burst must finish")

	var replayed map[string]interface{}
	for _, event := range decodeAgentEvents(w) {
		msg := event.GetAgentMessage()
		if msg == nil || msg.GetSeq() != -1 {
			continue
		}
		decompressed, err := msgcodec.Decompress(msg.GetContent(), msg.GetContentCompression())
		require.NoError(t, err)
		var content struct {
			Type string                 `json:"type"`
			Info map[string]interface{} `json:"info"`
		}
		if json.Unmarshal(decompressed, &content) != nil || content.Type != "agent_session_info" {
			continue
		}
		replayed = content.Info
	}

	require.NotNil(t, replayed, "the catch-up burst must carry an agent_session_info frame")
	progress, ok := replayed[contracts.SessionInfoKeyGoalProgress].(map[string]interface{})
	require.True(t, ok, "goal_progress must survive the replay")
	assert.EqualValues(t, 4, progress[contracts.GoalProgressFieldIterations])
}
