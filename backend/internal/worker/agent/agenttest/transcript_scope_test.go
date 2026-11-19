package agenttest

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTranscriptScopeRetainsTheSameFactsAsTheWorkerScope(t *testing.T) {
	t.Parallel()
	base := &Sink{}
	base.UpdateSessionID("original")
	services := agent.NewOrderedProviderServices(base.OutputQueue(), agent.NewProviderServices(base))
	anchor := agent.CaptureTranscript(services, agent.MessageContent{}, agent.SpanInfo{})
	base.UpdateSessionID("replacement")
	base.OpenSpan("live", "")
	scope, err := services.TranscriptScopeFor(anchor)
	require.NoError(t, err)
	opening, err := scope.CaptureTranscript(agent.MessageContent{Original: []byte("{\"native\":\"opening\"}")}, agent.SpanInfo{SpanID: "archive", SpanType: "bash"})
	require.NoError(t, err)
	scope.OpenSpan("archive", "")
	result, err := scope.CaptureTranscript(agent.MessageContent{Original: []byte("{\"native\":\"result\"}")}, agent.SpanInfo{SpanID: "archive", Closing: true})
	require.NoError(t, err)
	scope.CloseSpan("archive")
	require.NoError(t, agent.PersistCapturedTranscriptBatch(services, func() []agent.CapturedTranscriptOperation {
		return []agent.CapturedTranscriptOperation{opening.MessageOperation(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT), result.MessageOperation(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT)}
	}))
	rows := base.Messages()
	require.Len(t, rows, 2)
	assert.Empty(t, rows[0].SpansOpenAtPersist)
	assert.Equal(t, []SpanOpen{{SpanID: "archive"}}, rows[1].SpansOpenAtPersist)
	for _, row := range rows {
		assert.Equal(t, "original", row.AgentSessionID)
		assert.True(t, row.TranscriptOnly)
	}
	assert.Equal(t, []SpanOpen{{SpanID: "live"}}, base.liveSpansLocked())
	_, err = scope.CaptureTranscript(agent.MessageContent{AgentSessionID: "wrong"}, agent.SpanInfo{})
	require.Error(t, err)
	_, err = scope.CaptureTranscript(anchor.FrozenContent(), agent.SpanInfo{})
	require.Error(t, err)
	_, err = (&Sink{}).TranscriptScopeFor(anchor)
	require.Error(t, err)
	_, err = Nop().TranscriptScopeFor(anchor)
	require.Error(t, err)
}

func TestHistoricalTranscriptScopeMatchesWorkerOwnership(t *testing.T) {
	t.Parallel()
	for _, sessionID := range []string{"", "native"} {
		t.Run(sessionID, func(t *testing.T) {
			t.Parallel()
			base := &Sink{}
			if sessionID != "" {
				base.UpdateSessionID(sessionID)
			}
			services := agent.NewModelProgressResetSink(agent.NewOrderedProviderServices(base.OutputQueue(), agent.NewProviderServices(base)))
			services.SetTurnState(agent.TurnState{Active: true}, 1)
			services.OpenSpan("live", "")
			services.ReportProgress(agent.NativeTokenProgress("live-model", 17))
			before := base.ProgressSnapshot()
			anchor := agent.CaptureTranscript(services, agent.MessageContent{}, agent.SpanInfo{})
			scope, err := services.HistoricalTranscriptScopeFor(anchor)
			require.NoError(t, err)
			raw, err := agent.MarshalAssembledMessage(agent.AssembledMessageKindText, "historical", agent.MessageCompletionError)
			require.NoError(t, err)
			capture, err := scope.CaptureTranscript(agent.MessageContent{Original: raw}, agent.SpanInfo{})
			require.NoError(t, err)
			require.NoError(t, capture.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT))
			assert.Equal(t, before, base.ProgressSnapshot())
			rows := base.Messages()
			require.Len(t, rows, 1)
			assert.True(t, rows[0].TranscriptOnly)
			assert.Empty(t, rows[0].SpansOpenAtPersist)
			assert.Equal(t, sessionID, rows[0].AgentSessionID)
			assert.True(t, anchor.FrozenContent().Publication.Owner().IsCurrent())
			assert.False(t, capture.FrozenContent().Publication.Owner().IsCurrent())
			_, err = (&Sink{}).HistoricalTranscriptScopeFor(anchor)
			require.Error(t, err)
			_, err = Nop().HistoricalTranscriptScopeFor(anchor)
			require.Error(t, err)
		})
	}
}

func TestCapturedChildStateKeepsItsRootNativeSession(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"ordinary turn", "same ID update", "different session", "fresh same ID", "A B A"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			root := &Sink{}
			root.UpdateSessionID("native")
			childID, err := root.EnsureChildAgent(agent.ChildAgentSpec{RegistryRole: bgtask.RoleChildActivity, ProviderChildKey: "child", Title: "Child"})
			require.NoError(t, err)
			child := root.Child(childID)
			captured := agent.CaptureTranscript(child, agent.MessageContent{}, agent.SpanInfo{})
			writer, err := child.PlanWriterFor(captured)
			require.NoError(t, err)
			switch change {
			case "ordinary turn":
				root.SetTurnState(agent.TurnState{Active: true}, 1)
				root.SetTurnState(agent.TurnState{}, 2)
			case "same ID update":
				root.UpdateSessionID("native")
			case "different session":
				root.UpdateSessionID("replacement")
			case "fresh same ID":
				root.AdoptNativeSession("native")
			case "A B A":
				root.UpdateSessionID("replacement")
				root.UpdateSessionID("native")
			}
			current := change == "ordinary turn" || change == "same ID update"
			assert.True(t, captured.FrozenContent().Publication.Owner().IsSessionCurrent(), "root adoption does not replace the independent child's own transcript session")
			err = writer.UpdatePlan([]byte("# Child plan"), leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE, "Child plan")
			if current {
				require.NoError(t, err)
				assert.Equal(t, 1, child.PlanUpdateCount())
			} else {
				require.Error(t, err)
				assert.Zero(t, child.PlanUpdateCount(), "expired native state cannot change the saved child plan")
			}
		})
	}
}
