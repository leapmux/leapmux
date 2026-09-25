package ohmypi

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// assistantWithUsage is an assistant message_end with the usage given.
func assistantWithUsage(usage string) string {
	return `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"ok"}],"usage":` + usage + `,"stopReason":"stop"}}`
}

func TestAssistantUsageAddsUpTheCost(t *testing.T) {
	t.Parallel()
	r := withCatalog(newRig(t))
	r.emit(
		assistantWithUsage(`{"input":1000,"output":20,"cacheRead":300,"cacheWrite":50,"cost":{"total":0.25}}`),
		assistantWithUsage(`{"input":1500,"output":40,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.5}}`),
	)
	info := r.sink.LastSessionInfo()
	assert.InDelta(t, 0.75, info[contracts.SessionInfoKeyTotalCostUsd], 1e-9, "the session's cost is the sum of its requests")
	context, ok := info[contracts.SessionInfoKeyContextUsage].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, int64(1500), context[contracts.ContextUsageFieldInputTokens], "the context is the LATEST request's")
	assert.Equal(t, int64(40), context[contracts.ContextUsageFieldOutputTokens])
	assert.Equal(t, int64(128000), context[contracts.ContextUsageFieldContextWindow], "the window comes from the catalog")

	var metadata map[string]any
	require.NoError(t, json.Unmarshal(r.sink.Messages()[1].Metadata, &metadata))
	assert.InDelta(t, 0.75, metadata[contracts.SessionInfoKeyTotalCostUsd], 1e-9, "the row carries the usage at its time")
}

func TestAMessageWithNoTokensKeepsTheContext(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(
		assistantWithUsage(`{"input":1000,"output":20,"cacheRead":0,"cacheWrite":0,"cost":{"total":0}}`),
		assistantWithUsage(`{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"cost":{"total":0}}`),
	)
	context, ok := r.sink.LastSessionInfo()[contracts.SessionInfoKeyContextUsage].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, int64(1000), context[contracts.ContextUsageFieldInputTokens], "an aborted request states no context")
	_, hasCost := r.sink.LastSessionInfo()[contracts.SessionInfoKeyTotalCostUsd]
	assert.False(t, hasCost, "a free model reports no cost rather than zero")
}

func TestAssistantUsageWithoutACatalogStatesNoWindow(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(assistantWithUsage(`{"input":1000,"output":20,"cacheRead":0,"cacheWrite":0,"cost":{"total":0}}`))
	context, ok := r.sink.LastSessionInfo()[contracts.SessionInfoKeyContextUsage].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, int64(1000), context[contracts.ContextUsageFieldInputTokens])
	assert.NotContains(t, context, contracts.ContextUsageFieldContextWindow, "no window is stated rather than a wrong one")
}

// Only the session's own assistant messages carry its usage. A subagent's
// usage is the subagent's.
func TestASubagentsUsageIsNotTheSessions(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(frameTaskStart, frameStarted,
		subagentEvent("Probe", assistantWithUsage(`{"input":5000,"output":20,"cacheRead":0,"cacheWrite":0,"cost":{"total":1.5}}`)))
	assert.Zero(t, r.sink.SessionInfoCount())
	child := r.sink.Child(subagentRow(t, r, "Probe").ChildAgentID).Messages()
	require.Len(t, child, 1)
	assert.Empty(t, child[0].Metadata, "the child row carries no session usage")
}

func TestAMessageWithoutUsageIsKeptAsItIs(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.emit(`{"type":"message_end","message":{"role":"assistant","content":[]}}`)
	require.Len(t, r.sink.Messages(), 1)
	assert.Empty(t, r.sink.Messages()[0].Metadata)
	assert.Zero(t, r.sink.SessionInfoCount())
}

func TestUsageMetadata(t *testing.T) {
	t.Parallel()
	assert.Nil(t, usageMetadata(usageSnapshot{}, nil), "nothing to state")
	duration := int64(1500)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(usageMetadata(usageSnapshot{TotalCostUsd: 0.1, HasTotalCost: true}, &duration), &fields))
	assert.Equal(t, map[string]any{contracts.SessionInfoKeyTotalCostUsd: 0.1, contracts.MessageMetadataFieldDurationMs: float64(1500)}, fields)
}

func sessionStatsFor(sessionID string, cost float64, tokens int64) sessionStats {
	stats := sessionStats{SessionID: sessionID, SessionFile: "/sessions/2026-09-23T18-11-57-284Z_" + sessionID + ".jsonl", Cost: cost}
	stats.ContextUsage = &struct {
		Tokens        *int64 `json:"tokens"`
		ContextWindow int64  `json:"contextWindow"`
	}{Tokens: &tokens, ContextWindow: 128000}
	return stats
}

func TestSessionStatsSetTheUsage(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	sessionID := r.agent.sessionID
	r.agent.Mu.Lock()
	generation := r.agent.usage.generation
	r.agent.Mu.Unlock()
	r.agent.applySessionStats(sessionStatsFor(sessionID, 1.5, 1200), generation, sessionID)

	info := r.sink.LastSessionInfo()
	assert.InDelta(t, 1.5, info[contracts.SessionInfoKeyTotalCostUsd], 1e-9, "omp's own total replaces the sum")
	context := info[contracts.SessionInfoKeyContextUsage].(map[string]any)
	assert.Equal(t, int64(1200), context[contracts.ContextUsageFieldContextTokens])
	assert.Equal(t, int64(128000), context[contracts.ContextUsageFieldContextWindow])
	assert.Zero(t, r.sink.SessionIDCount(), "the session is the same")
}

func TestSessionStatsOvertakenByAMessageKeepTheMessagesContext(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	sessionID := r.agent.sessionID
	r.agent.Mu.Lock()
	generation := r.agent.usage.generation
	r.agent.Mu.Unlock()
	r.emit(assistantWithUsage(`{"input":3000,"output":10,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.1}}`))
	r.agent.applySessionStats(sessionStatsFor(sessionID, 2, 1200), generation, sessionID)

	info := r.sink.LastSessionInfo()
	assert.InDelta(t, 2.0, info[contracts.SessionInfoKeyTotalCostUsd], 1e-9)
	context := info[contracts.SessionInfoKeyContextUsage].(map[string]any)
	assert.Equal(t, int64(3000), context[contracts.ContextUsageFieldInputTokens], "the newer message wins")
}

func TestSessionStatsOfANewSessionResetTheUsage(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	sessionID := r.agent.sessionID
	r.emit(assistantWithUsage(`{"input":3000,"output":10,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.1}}`))
	r.agent.applySessionStats(sessionStatsFor("s-new", 0, 50), 0, sessionID)

	assert.Equal(t, "/sessions/2026-09-23T18-11-57-284Z_s-new.jsonl", r.sink.LastSessionID(), "an extension replaced the session")
	info := r.sink.LastSessionInfo()
	_, hasCost := info[contracts.SessionInfoKeyTotalCostUsd]
	assert.False(t, hasCost, "the new session's usage starts over")
	assert.Equal(t, int64(50), info[contracts.SessionInfoKeyContextUsage].(map[string]any)[contracts.ContextUsageFieldContextTokens])
}

func TestSessionStatsWithANewFileKeepTheUsage(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	sessionID := r.agent.sessionID
	r.emit(assistantWithUsage(`{"input":3000,"output":10,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.1}}`))
	stats := sessionStatsFor(sessionID, 0, 50)
	stats.SessionFile = "/elsewhere/moved.jsonl"
	r.agent.applySessionStats(stats, 0, sessionID)

	assert.Equal(t, "/elsewhere/moved.jsonl", r.sink.LastSessionID(), "the resume handle follows the file")
	info := r.sink.LastSessionInfo()
	assert.InDelta(t, 0.1, info[contracts.SessionInfoKeyTotalCostUsd], 1e-9, "the same session keeps its usage")
	assert.Equal(t, int64(3000), info[contracts.SessionInfoKeyContextUsage].(map[string]any)[contracts.ContextUsageFieldInputTokens])
}

// The stats state the context as one total. A total of zero, or none, states
// nothing, and the context that a message stated stays.
func TestSessionStatsWithoutTokensKeepTheContext(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	sessionID := r.agent.sessionID
	r.emit(assistantWithUsage(`{"input":3000,"output":10,"cacheRead":0,"cacheWrite":0,"cost":{"total":0.1}}`))
	r.agent.Mu.Lock()
	generation := r.agent.usage.generation
	r.agent.Mu.Unlock()

	noTokens := sessionStatsFor(sessionID, 0.5, 0)
	noContext := sessionStatsFor(sessionID, 0.75, 0)
	noContext.ContextUsage = nil
	for _, stats := range []sessionStats{noTokens, noContext} {
		r.agent.applySessionStats(stats, generation, sessionID)
		info := r.sink.LastSessionInfo()
		assert.InDelta(t, stats.Cost, info[contracts.SessionInfoKeyTotalCostUsd], 1e-9)
		assert.Equal(t, int64(3000), info[contracts.SessionInfoKeyContextUsage].(map[string]any)[contracts.ContextUsageFieldInputTokens])
	}
}

func TestSessionStatsOfAStoppedOrDiscardingAgentAreDropped(t *testing.T) {
	t.Parallel()
	for name, halt := range map[string]func(*Agent){
		"stopped":    func(a *Agent) { a.SetStoppedForTest(true) },
		"discarding": func(a *Agent) { a.DiscardOutput() },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			sessionID := r.agent.sessionID
			halt(r.agent)
			r.agent.applySessionStats(sessionStatsFor("s-new", 9, 9), 0, sessionID)
			assert.Zero(t, r.sink.SessionInfoCount())
			assert.Zero(t, r.sink.SessionIDCount())
			r.agent.Mu.Lock()
			defer r.agent.Mu.Unlock()
			assert.Equal(t, sessionID, r.agent.sessionID, "the identity stays")
		})
	}
}

func TestRefreshSessionStatsReadsNothingForAStoppedAgent(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.agent.SetStoppedForTest(true)
	r.agent.refreshSessionStatsAsync()
	r.agent.Mu.Lock()
	running := r.agent.usage.statsRunning
	r.agent.Mu.Unlock()
	assert.False(t, running, "no read starts")
	assert.Empty(t, r.commandsOfType(CommandGetSessionStats))
}

func TestSessionStatsOfASessionTheAgentLeftAreDropped(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.agent.Mu.Lock()
	r.agent.sessionID = "s-now"
	r.agent.Mu.Unlock()
	r.agent.applySessionStats(sessionStatsFor("s-old", 9, 9), 0, "s-old")
	assert.Zero(t, r.sink.SessionInfoCount())
	assert.Zero(t, r.sink.SessionIDCount())
}

func TestRefreshSessionStatsRunsOneReadAtATime(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	release := make(chan struct{})
	r.respond(func(command recordedCommand) *rigReply {
		if command.Type != CommandGetSessionStats {
			return nil
		}
		<-release
		return &rigReply{Data: json.RawMessage(`{"sessionId":"01a0cf77-9ae4-72d8-9a42-665c431d3beb","cost":0.5,"contextUsage":{"tokens":900,"contextWindow":128000}}`)}
	})
	r.agent.refreshSessionStatsAsync()
	r.waitForCommand(CommandGetSessionStats, 1)
	r.agent.refreshSessionStatsAsync()
	close(release)
	waitFor(t, func() bool {
		r.agent.Mu.Lock()
		defer r.agent.Mu.Unlock()
		return !r.agent.usage.statsRunning
	})
	assert.Len(t, r.commandsOfType(CommandGetSessionStats), 1, "a request while a read runs is answered by that read")
	assert.InDelta(t, 0.5, r.sink.LastSessionInfo()[contracts.SessionInfoKeyTotalCostUsd], 1e-9)
}

// A session-stats read waits for omp at most sessionStatsMaxWait, or the API
// timeout when that is shorter. A read that times out ends, so the next run's
// read can start.
func TestTheSessionStatsReadWaitsAtMostItsLimit(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		apiTimeout time.Duration
		want       time.Duration
	}{
		{name: "a longer API timeout is cut to the limit", apiTimeout: time.Minute, want: sessionStatsMaxWait},
		{name: "a shorter API timeout applies", apiTimeout: time.Second, want: time.Second},
		{name: "no API timeout waits for the limit", apiTimeout: 0, want: sessionStatsMaxWait},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			ctx := testutil.DeadlineContext(t)
			r.agent.SetAPITimeoutForTest(tc.apiTimeout)
			read := r.clock.Trap().NewTimer(providerkit.AwaitResponseTimerTag, CommandGetSessionStats)
			defer read.Close()
			r.respond(func(command recordedCommand) *rigReply {
				if command.Type == CommandGetSessionStats {
					return &rigReply{Skip: true}
				}
				return nil
			})
			running := func() bool {
				r.agent.Mu.Lock()
				defer r.agent.Mu.Unlock()
				return r.agent.usage.statsRunning
			}

			r.agent.refreshSessionStatsAsync()
			assert.Equal(t, tc.want, testutil.WaitForTimer(t, ctx, read))
			r.clock.Advance(tc.want - time.Nanosecond).MustWait(ctx)
			assert.True(t, running(), "the read waits up to its limit")
			r.clock.Advance(time.Nanosecond).MustWait(ctx)
			r.awaitStatsRead()

			r.agent.refreshSessionStatsAsync()
			assert.Equal(t, tc.want, testutil.WaitForTimer(t, ctx, read), "a read that timed out lets the next one run")
			r.waitForCommand(CommandGetSessionStats, 2)
			assert.Zero(t, r.sink.SessionInfoCount(), "a read that timed out changes nothing")
		})
	}
}
