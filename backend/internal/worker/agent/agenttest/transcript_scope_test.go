package agenttest

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
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
