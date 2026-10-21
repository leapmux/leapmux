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

	event := svc.Output.SessionInfoReplayEvents("agent-1")[0]
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

	info := replayedSessionInfo(t, svc.Output.SessionInfoReplayEvents("agent-1")[0])
	assert.EqualValues(t, 0.20, info[contracts.SessionInfoKeyTotalCostUsd])
}

func TestSessionInfoReplay_RestoresAPermissionCheck(t *testing.T) {
	t.Parallel()
	svc, sink, _ := newSessionInfoServiceFixture(t)
	sink.BroadcastSessionInfo(map[string]interface{}{
		contracts.SessionInfoKeyTotalCostUsd: 1,
		contracts.SessionInfoKeyRunningTool: map[string]interface{}{
			contracts.RunningToolFieldSpanId:          "s-1",
			contracts.RunningToolFieldAgentSessionId:  "native",
			contracts.RunningToolFieldPermissionCheck: true,
		},
	})
	events := svc.Output.SessionInfoReplayEvents("agent-1")
	require.Len(t, events, 2)
	info := replayedSessionInfo(t, events[1])
	assert.Contains(t, info, contracts.SessionInfoKeyRunningTool)
}

// Only permission checks enter the cache. The progress publisher supplies active counters.
func TestSessionInfoReplay_ReplaysActivePermissionChecksOnly(t *testing.T) {
	t.Parallel()

	svc, sink, _ := newSessionInfoServiceFixture(t)
	sink.BroadcastSessionInfo(map[string]interface{}{
		contracts.SessionInfoKeyRunningTool:  map[string]interface{}{"span_id": "s-1"},
		contracts.SessionInfoKeyTotalCostUsd: 7.5,
	})
	sink.ReportProgress(agent.NativeTokenProgress("model", 42))
	sink.BroadcastSessionInfo(permissionCheckInfo("native", "s-2", true))
	sink.BroadcastSessionInfo(permissionCheckInfo("native", "s-2", false))
	// An ordinary tool update must not erase an active check.
	sink.BroadcastSessionInfo(map[string]interface{}{
		contracts.SessionInfoKeyRunningTool: map[string]interface{}{"span_id": "s-2"},
	})

	events := svc.Output.SessionInfoReplayEvents("agent-1")
	require.Len(t, events, 2)
	info := replayedSessionInfo(t, events[0])
	assert.EqualValues(t, 42, info[contracts.SessionInfoKeyThinkingTokens])
	assert.NotContains(t, info, contracts.SessionInfoKeyRunningTool)
	assert.EqualValues(t, 7.5, info[contracts.SessionInfoKeyTotalCostUsd], "a cached key still replays")
	check := replayedSessionInfo(t, events[1])[contracts.SessionInfoKeyRunningTool].(map[string]interface{})
	assert.Equal(t, "s-2", check[contracts.RunningToolFieldSpanId])
	assert.Equal(t, true, check[contracts.RunningToolFieldPermissionCheck])
	sink.BroadcastSessionInfo(permissionCheckInfo("native", "s-2", nil))
	require.Len(t, svc.Output.SessionInfoReplayEvents("agent-1"), 1)
}

func permissionCheckInfo(sessionID, spanID string, value interface{}) map[string]interface{} {
	return map[string]interface{}{
		contracts.SessionInfoKeyRunningTool: map[string]interface{}{
			contracts.RunningToolFieldAgentSessionId:  sessionID,
			contracts.RunningToolFieldSpanId:          spanID,
			contracts.RunningToolFieldPermissionCheck: value,
		},
	}
}

func TestSessionInfoReplay_TurnEndClearsPermissionChecks(t *testing.T) {
	t.Parallel()
	svc, sink, _ := newSessionInfoServiceFixture(t)
	sink.BroadcastSessionInfo(permissionCheckInfo("native", "s-1", true))
	require.Len(t, svc.Output.SessionInfoReplayEvents("agent-1"), 1)
	require.NoError(t, sink.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"type":"agent_end"}`)}, agent.SpanInfo{}))
	assert.Empty(t, svc.Output.SessionInfoReplayEvents("agent-1"))
}

func TestSessionInfoReplay_SessionChangeDropsOtherSessionChecks(t *testing.T) {
	t.Parallel()
	svc, sink, _ := newSessionInfoServiceFixture(t)
	sink.BroadcastSessionInfo(permissionCheckInfo("old", "s-1", true))
	sink.BroadcastSessionInfo(permissionCheckInfo("new", "s-1", true))
	sink.UpdateSessionID("new")
	events := svc.Output.SessionInfoReplayEvents("agent-1")
	require.Len(t, events, 1)
	check := replayedSessionInfo(t, events[0])[contracts.SessionInfoKeyRunningTool].(map[string]interface{})
	assert.Equal(t, "new", check[contracts.RunningToolFieldAgentSessionId])
}

func TestSessionInfoReplay_PermissionChecksSortBySessionAndSpan(t *testing.T) {
	t.Parallel()
	svc, sink, _ := newSessionInfoServiceFixture(t)
	for _, key := range [][2]string{{"b", "a"}, {"a", "z"}, {"a", "a"}} {
		sink.BroadcastSessionInfo(permissionCheckInfo(key[0], key[1], true))
	}
	var keys [][2]string
	for _, event := range svc.Output.SessionInfoReplayEvents("agent-1") {
		check := replayedSessionInfo(t, event)[contracts.SessionInfoKeyRunningTool].(map[string]interface{})
		keys = append(keys, [2]string{check[contracts.RunningToolFieldAgentSessionId].(string), check[contracts.RunningToolFieldSpanId].(string)})
	}
	assert.Equal(t, [][2]string{{"a", "a"}, {"a", "z"}, {"b", "a"}}, keys)
}

func TestSessionInfoReplay_RefusesInvalidPermissionChecks(t *testing.T) {
	t.Parallel()
	svc, sink, _ := newSessionInfoServiceFixture(t)
	for _, value := range []interface{}{false, "true", 1, nil} {
		sink.BroadcastSessionInfo(permissionCheckInfo("native", "s-1", value))
	}
	sink.BroadcastSessionInfo(permissionCheckInfo("native", "", true))
	assert.Empty(t, svc.Output.SessionInfoReplayEvents("agent-1"))
}

func TestSessionInfoReplay_CarriesTheLiveCompaction(t *testing.T) {
	t.Parallel()
	svc, sink, _ := newSessionInfoServiceFixture(t)
	sink.BroadcastSessionInfo(map[string]interface{}{
		contracts.SessionInfoKeyCompaction: map[string]interface{}{contracts.CompactionFieldTrigger: "auto"},
	})
	info := replayedSessionInfo(t, svc.Output.SessionInfoReplayEvents("agent-1")[0])
	assert.Equal(t, map[string]interface{}{contracts.CompactionFieldTrigger: "auto"}, info[contracts.SessionInfoKeyCompaction])
	sink.BroadcastSessionInfo(map[string]interface{}{contracts.SessionInfoKeyCompaction: nil})
	info = replayedSessionInfo(t, svc.Output.SessionInfoReplayEvents("agent-1")[0])
	assert.Contains(t, info, contracts.SessionInfoKeyCompaction)
	assert.Nil(t, info[contracts.SessionInfoKeyCompaction])
}

func TestProcessExitEndsTheLiveCompaction(t *testing.T) {
	t.Parallel()
	svc, root, _ := newSessionInfoServiceFixture(t)
	child := root.ChildSink("child-1")
	for _, sink := range []agent.ProviderServices{root, child} {
		sink.BroadcastSessionInfo(map[string]interface{}{contracts.SessionInfoKeyCompaction: map[string]interface{}{}})
	}
	svc.Output.NoteAgentProcessExited("agent-1")
	svc.Output.WaitActivityRefreshes()
	for _, id := range []string{"agent-1", "child-1"} {
		info := replayedSessionInfo(t, svc.Output.SessionInfoReplayEvents(id)[0])
		assert.Contains(t, info, contracts.SessionInfoKeyCompaction)
		assert.Nil(t, info[contracts.SessionInfoKeyCompaction])
	}
}

// An inactive agent has no live counters, and an agent whose process reported
// nothing yet has none either. Both answer nil, so the subscribe path sends no
// frame rather than an empty one.
func TestSessionInfoReplay_IsNilWithNothingToRestore(t *testing.T) {
	t.Parallel()

	svc, _, _ := newSessionInfoServiceFixture(t)
	assert.Nil(t, svc.Output.SessionInfoReplayEvents("no-such-agent"), "no process, no counters")
	assert.Nil(t, svc.Output.SessionInfoReplayEvents("agent-1"), "a silent sink has nothing to replay")
}

func TestSessionInfoReplay_UsesTheChildProgressPublisher(t *testing.T) {
	t.Parallel()

	svc, root, _ := newSessionInfoServiceFixture(t)
	child := root.ChildSink("child-1")
	root.ReportProgress(agent.NativeTokenProgress("root-model", 40))
	child.ReportProgress(agent.NativeTokenProgress("child-model", 7))

	event := svc.Output.SessionInfoReplayEvents("child-1")[0]
	require.NotNil(t, event)
	assert.Equal(t, "child-1", event.GetAgentId())
	info := replayedSessionInfo(t, event)
	assert.EqualValues(t, 7, info[contracts.SessionInfoKeyThinkingTokens])
}

func TestSessionInfoReplay_SurvivesControlRequestCleanup(t *testing.T) {
	t.Parallel()

	svc, sink, _ := newSessionInfoServiceFixture(t)
	sink.ReportProgress(agent.NativeTokenProgress("model", 42))
	sink.CancelControlRequest("request-1")

	event := svc.Output.SessionInfoReplayEvents("agent-1")[0]
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
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
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
	sink.BroadcastSessionInfo(permissionCheckInfo("native", "s-b", true))
	sink.BroadcastSessionInfo(permissionCheckInfo("native", "s-a", true))

	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: "agent-1", Mode: leapmuxv1.WatchMode_WATCH_MODE_FULL, ReplayId: 1}},
	}, w)
	require.Eventually(t, func() bool {
		return countCatchUpCompletes(w) == 1
	}, 5*time.Second, 20*time.Millisecond, "the catch-up burst must finish")

	var replayed map[string]interface{}
	var replayedChecks []string
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
		if check, ok := content.Info[contracts.SessionInfoKeyRunningTool].(map[string]interface{}); ok {
			require.NotNil(t, replayed, "the counters must precede the checks")
			assert.Equal(t, true, check[contracts.RunningToolFieldPermissionCheck])
			replayedChecks = append(replayedChecks, check[contracts.RunningToolFieldSpanId].(string))
		} else {
			replayed = content.Info
		}
	}

	require.NotNil(t, replayed, "the catch-up burst must carry an agent_session_info frame")
	progress, ok := replayed[contracts.SessionInfoKeyGoalProgress].(map[string]interface{})
	require.True(t, ok, "goal_progress must survive the replay")
	assert.EqualValues(t, 4, progress[contracts.GoalProgressFieldIterations])
	assert.Equal(t, []string{"s-a", "s-b"}, replayedChecks)
}
