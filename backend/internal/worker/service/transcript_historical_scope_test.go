package service

import (
	"github.com/leapmux/leapmux/internal/worker/bgtask"

	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func TestHistoricalTranscriptScopePreservesLiveState(t *testing.T) {
	t.Parallel()
	for _, child := range []bool{false, true} {
		for _, known := range []bool{false, true} {
			t.Run(fmt.Sprintf("child=%t/known_session=%t", child, known), func(t *testing.T) {
				t.Parallel()
				rootID := fmt.Sprintf("historical-%t-%t", child, known)
				svc, root := setupRootSink(t, rootID)
				sessionID := ""
				if known {
					sessionID = "native-session"
				}
				selected, id := root, rootID
				physical := requireRootOutputSink(t, svc.Output, rootID)
				if child {
					root.UpdateSessionID("parent-session")
					childID, err := root.EnsureChildAgent(agent.ChildAgentSpec{
						RegistryRole: bgtask.RoleChildTask,
						SpawnSpanID:  "spawn", ProviderChildKey: rootID + "/child", AgentSessionID: sessionID})
					require.NoError(t, err)
					lease := root.AcquireChildSink(childID)
					require.NotNil(t, lease.Sink)
					lease.Activate()
					selected, id = lease.Sink, childID
					physical = requireChildOutputSink(t, physical, childID)
				} else if known {
					selected.UpdateSessionID(sessionID)
				}
				services := agent.NewModelProgressResetSink(selected)
				services.SetTurnState(agent.TurnState{Active: true}, 1)
				services.OpenSpan("live-span", "")
				services.ReportProgress(agent.NativeTokenProgress("live-model", 17))
				services.ReportProgress(agent.OutputDeltaProgress("live-tool", 8))
				stopCapturedProgressTimers(physical.progress)
				before := physical.progress.snapshotInfo()
				writer := &testResponseWriter{channelID: rootID + "-watch"}
				registerAgentWatch(svc, writer.channelID, id, leapmuxv1.WatchMode_WATCH_MODE_FULL, writer)
				anchor := agent.CaptureTranscript(services, agent.MessageContent{}, agent.SpanInfo{})
				original := anchor.FrozenContent().Publication.Owner().(*transcriptOwner)
				require.True(t, original.IsCurrent())
				scope, err := services.HistoricalTranscriptScopeFor(anchor)
				require.NoError(t, err)
				raw, err := agent.MarshalAssembledMessage(agent.AssembledMessageKindText, "Historical task failure.", agent.MessageCompletionError)
				require.NoError(t, err)
				capture, err := scope.CaptureTranscript(agent.MessageContent{Original: raw, IdempotencyKey: "historical-error"}, agent.SpanInfo{})
				require.NoError(t, err)
				owner := capture.FrozenContent().Publication.Owner().(*transcriptOwner)
				assert.False(t, owner.IsCurrent())
				assert.True(t, owner.IsSessionCurrent())
				assert.Same(t, original.session, owner.session)
				assert.Same(t, original.activity, owner.activity)
				assert.NotSame(t, original.scope, owner.scope)
				assert.NotSame(t, original.thread, owner.thread)
				assert.JSONEq(t, "[]", owner.spanLines)
				assert.JSONEq(t, "[]", owner.passthroughLines)
				assert.True(t, original.IsCurrent(), "constructing history must not replace live authority")
				require.NoError(t, capture.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT))
				capture.FrozenContent().Publication.ReportProgress(agent.ResetProgress())
				assert.False(t, owner.PublishSessionInfo(map[string]any{"historical_only": true}))
				divider, err := scope.CaptureTranscript(agent.WithToolUseCount(agent.MessageContent{Original: []byte(`{"type":"result","num_tool_uses":4}`)}, 4), agent.SpanInfo{})
				require.NoError(t, err)
				require.NoError(t, divider.PersistTurnEnd())
				assert.Equal(t, before, physical.progress.snapshotInfo())
				activity := svc.Output.activityFor(id, rootID)
				activity.mu.Lock()
				assert.True(t, activity.turnActive)
				assert.Nil(t, activity.settledToolUses)
				activity.mu.Unlock()
				live := physical.currentSpanTracker().ActiveSpans()
				require.Len(t, live, 1)
				assert.Equal(t, "live-span", live[0].SpanID)
				rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: id})
				require.NoError(t, err)
				require.Len(t, rows, 2)
				for _, row := range rows {
					assert.True(t, row.TranscriptOnly)
					assert.Equal(t, sessionID, row.AgentSessionID)
				}
				var messages, completions int
				for _, stream := range writer.streamsSnapshot() {
					event := decodeWatchAgentEvent(t, stream)
					if event.GetTurnEnd() != nil {
						completions++
					}
					if message := event.GetAgentMessage(); message != nil && message.Seq > 0 {
						messages++
						assert.True(t, message.TranscriptOnly)
					}
				}
				assert.Equal(t, 2, messages)
				assert.Zero(t, completions)
				services.SetTurnState(agent.TurnState{}, 2)
				services.SetTurnState(agent.TurnState{Active: true}, 3)
				assert.False(t, owner.IsCurrent(), "a private historical scope must never become live")
				assert.Equal(t, int64(17), before[contracts.SessionInfoKeyThinkingTokens])
			})
		}
	}
}

func TestHistoricalTranscriptScopeRetainsUnknownAndReplacedSessions(t *testing.T) {
	t.Parallel()
	for _, sessionID := range []string{"", "native-old"} {
		t.Run(fmt.Sprintf("session=%q", sessionID), func(t *testing.T) {
			t.Parallel()
			svc, services := setupRootSink(t, "historical-session")
			if sessionID != "" {
				services.UpdateSessionID(sessionID)
			}
			anchor := agent.CaptureTranscript(services, agent.MessageContent{}, agent.SpanInfo{})
			services.UpdateSessionID("native-new")
			scope, err := services.HistoricalTranscriptScopeFor(anchor)
			require.NoError(t, err)
			capture, err := scope.CaptureTranscript(agent.MessageContent{Original: []byte(`{"text":"old native session"}`)}, agent.SpanInfo{})
			require.NoError(t, err)
			require.NoError(t, capture.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT))
			rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: "historical-session"})
			require.NoError(t, err)
			require.Len(t, rows, 1)
			assert.Equal(t, sessionID, rows[0].AgentSessionID)
			assert.True(t, rows[0].TranscriptOnly)
		})
	}
}

func TestHistoricalTranscriptScopePreservesOverridesAndRejectsForeignAuthority(t *testing.T) {
	t.Parallel()
	svc, services := setupRootSink(t, "historical-override")
	services.UpdateSessionID("native")
	writer := &scopeCaptureOverride{ProviderServices: services}
	anchor := agent.CaptureTranscript(writer, agent.MessageContent{}, agent.SpanInfo{})
	scope, err := writer.HistoricalTranscriptScopeFor(anchor)
	require.NoError(t, err)
	other, err := writer.HistoricalTranscriptScopeFor(anchor)
	require.NoError(t, err)
	first, err := scope.CaptureTranscript(agent.MessageContent{Original: []byte(`{"text":"historical"}`)}, agent.SpanInfo{})
	require.NoError(t, err)
	second, err := other.CaptureTranscript(agent.MessageContent{Original: []byte(`{"text":"independent"}`)}, agent.SpanInfo{})
	require.NoError(t, err)
	assert.Same(t, writer, first.TranscriptWriter())
	assert.JSONEq(t, `{"capture":"outer"}`, string(first.FrozenContent().Supplemental))
	a := first.FrozenContent().Publication.Owner().(*transcriptOwner)
	b := second.FrozenContent().Publication.Owner().(*transcriptOwner)
	assert.NotSame(t, a.scope, b.scope)
	assert.NotSame(t, a.thread, b.thread)
	_, err = services.HistoricalTranscriptScopeFor(agent.CapturedTranscript{})
	require.Error(t, err)
	childID, err := services.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole: bgtask.RoleChildTask,
		SpawnSpanID:  "spawn", ProviderChildKey: "foreign-history"})
	require.NoError(t, err)
	foreign := agent.CaptureTranscript(services.ChildSink(childID), agent.MessageContent{}, agent.SpanInfo{})
	_, err = services.HistoricalTranscriptScopeFor(foreign)
	require.Error(t, err)
	writer.foreign = foreign.FrozenContent().Publication
	_, err = scope.CaptureTranscript(agent.MessageContent{}, agent.SpanInfo{})
	require.Error(t, err)
	_, err = scope.CaptureTranscript(anchor.FrozenContent(), agent.SpanInfo{})
	require.Error(t, err)
	_, err = scope.CaptureTranscript(agent.MessageContent{AgentSessionID: "different"}, agent.SpanInfo{})
	require.Error(t, err)
	assert.Empty(t, requireRootOutputSink(t, svc.Output, "historical-override").currentSpanTracker().ActiveSpans())
}
