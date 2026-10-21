package agenttest

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnknownFrameSuiteAcceptsOneRowAndRejectsMissingOrDuplicateRows(t *testing.T) {
	t.Parallel()
	for _, count := range []int{0, 1, 2} {
		inner := &testing.T{}
		AssertUnknownFramePersists(inner, []UnknownFrameCase{{Name: "current", Frame: []byte(`{"type":"future","value":1}`)}}, func(_ *testing.T, tc UnknownFrameCase) *Sink {
			sink := &Sink{}
			for range count {
				require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: tc.Frame}, agent.SpanInfo{}))
			}
			return sink
		})
		assert.Equal(t, count != 1, inner.Failed(), "row count %d", count)
	}
}

func TestUnknownFrameSuiteAcceptsCanonicalNotificationContent(t *testing.T) {
	t.Parallel()
	AssertUnknownFramePersists(t, []UnknownFrameCase{{Name: "nested", Frame: []byte(`{"payload":{"a":1,"b":2}}`), Stored: []byte(`{"a":1,"b":2}`)}}, func(t *testing.T, _ UnknownFrameCase) *Sink {
		sink := &Sink{}
		_, err := sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"b":2,"a":1}`)})
		require.NoError(t, err)
		return sink
	})
}

func TestUnknownFrameSuiteRejectsMutationOfTheInputBytes(t *testing.T) {
	t.Parallel()
	inner := &testing.T{}
	AssertUnknownFramePersists(inner, []UnknownFrameCase{{Frame: []byte(`{"type":"future"}`)}}, func(_ *testing.T, tc UnknownFrameCase) *Sink {
		copy(tc.Frame, []byte(`{"type":"change"}`))
		sink := &Sink{}
		require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: tc.Frame}, agent.SpanInfo{}))
		return sink
	})
	assert.True(t, inner.Failed())
}

func TestUnknownFrameSuiteRejectsAnotherSessionRow(t *testing.T) {
	t.Parallel()
	for _, stores := range []bool{false, true} {
		inner := &testing.T{}
		AssertUnknownFramePersists(inner, []UnknownFrameCase{
			{Name: "current", Frame: []byte(`{"type":"future"}`)},
			{Name: "other", OtherSession: true, Frame: []byte(`{"type":"future"}`)},
		}, func(_ *testing.T, tc UnknownFrameCase) *Sink {
			sink := &Sink{}
			if !tc.OtherSession || stores {
				require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: tc.Frame}, agent.SpanInfo{}))
			}
			return sink
		})
		assert.Equal(t, stores, inner.Failed())
	}
}

func TestUnknownFrameSuiteRequiresACurrentCase(t *testing.T) {
	t.Parallel()
	inner := &testing.T{}
	AssertUnknownFramePersists(inner, []UnknownFrameCase{{OtherSession: true}}, func(*testing.T, UnknownFrameCase) *Sink { return &Sink{} })
	assert.True(t, inner.Failed())
}
