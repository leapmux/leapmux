package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
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

// A dedup-exempt key is never cached, and the replay must not resurrect one.
// Both are per-turn state that the frontend drops at boundaries the worker
// cannot all observe, so a replayed value restores a counter, or a tool badge,
// for work that ended.
func TestSessionInfoReplay_OmitsTheDedupExemptKeys(t *testing.T) {
	t.Parallel()

	svc, sink, _ := newSessionInfoServiceFixture(t)
	sink.BroadcastSessionInfo(map[string]interface{}{
		contracts.SessionInfoKeyThinkingTokens: 42,
		contracts.SessionInfoKeyRunningTool:    map[string]interface{}{"span_id": "s-1"},
		contracts.SessionInfoKeyTotalCostUsd:   7.5,
	})

	info := replayedSessionInfo(t, svc.Output.SessionInfoReplayEvent("agent-1"))
	assert.NotContains(t, info, contracts.SessionInfoKeyThinkingTokens)
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
