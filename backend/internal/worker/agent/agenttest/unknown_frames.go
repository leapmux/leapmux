package agenttest

import (
	"slices"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
)

// UnknownFrameCase states the native frame and the bytes that its provider stores.
type UnknownFrameCase struct {
	Name         string
	Frame        []byte
	Stored       []byte
	OtherSession bool
}

// AssertUnknownFramePersists requires exactly one unchanged row for a current-session frame.
// The feed function creates a fresh agent and sink for each case.
func AssertUnknownFramePersists(t *testing.T, cases []UnknownFrameCase, feed func(t *testing.T, tc UnknownFrameCase) *Sink) {
	t.Helper()
	if !assert.NotNil(t, feed, "the unknown-frame suite needs a feed function") {
		return
	}
	currentCases := 0
	for _, tc := range cases {
		expected := tc.Stored
		if expected == nil {
			expected = tc.Frame
		}
		expected = slices.Clone(expected)
		if !tc.OtherSession {
			currentCases++
		}
		sink := feed(t, tc)
		if !assert.NotNil(t, sink, "%s: the feed returned no sink", tc.Name) {
			continue
		}
		rows := append(sink.Messages(), sink.PersistedNotifications()...)
		assert.Empty(t, sink.LeapMuxNotifications(), "%s: an unknown frame must not create a worker notification", tc.Name)
		if tc.OtherSession {
			assert.Empty(t, rows, "%s: another session must store no row", tc.Name)
			continue
		}
		if !assert.Len(t, rows, 1, "%s: an unknown current-session frame needs one row", tc.Name) {
			continue
		}
		assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, rows[0].Source, tc.Name)
		assert.True(t, agent.JSONCanonicalEqual(expected, rows[0].Content), "%s: the stored frame differs: got %s, want %s", tc.Name, rows[0].Content, expected)
	}
	assert.Positive(t, currentCases, "the unknown-frame suite needs a current-session case")
}
