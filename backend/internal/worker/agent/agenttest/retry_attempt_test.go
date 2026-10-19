package agenttest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRetryAttemptSuiteAcceptsCorrectTextAndRejectsMissingOrDuplicateRows(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "frames.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("{\"type\":\"finish\"}\n"), 0o600))
	for _, count := range []int{0, 1, 2} {
		inner := &testing.T{}
		RunRetryAttemptCase(inner, RetryAttemptCase{
			Name: "toy", FramesPath: path,
			Want: []RetryAttemptText{{Text: "answer", Completion: agent.MessageCompletionComplete}},
			NewAgent: func(_ *testing.T, sink *Sink) func([]byte) {
				return func([]byte) {
					for range count {
						raw, err := agent.MarshalAssembledMessage(agent.AssembledMessageKindText, "answer", agent.MessageCompletionComplete)
						require.NoError(t, err)
						require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: raw}, agent.SpanInfo{}))
					}
				}
			},
		})
		assert.Equal(t, count != 1, inner.Failed(), "row count %d", count)
	}
}

func TestRetryAttemptSuiteRejectsDiscardedTextInANotification(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "frames.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("{}\n"), 0o600))
	inner := &testing.T{}
	RunRetryAttemptCase(inner, RetryAttemptCase{
		Name: "discarded", FramesPath: path, Discarded: []string{"cut\ntext"},
		NewAgent: func(_ *testing.T, sink *Sink) func([]byte) {
			return func([]byte) {
				_, err := sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"value":"cut\ntext"}`)})
				require.NoError(t, err)
			}
		},
	})
	assert.True(t, inner.Failed())
}

func TestRetryAttemptSuiteKeepsExplicitCompletionAndTextOrder(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "frames.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("{}\n{}\n"), 0o600))
	RunRetryAttemptCase(t, RetryAttemptCase{
		Name: "ordered", FramesPath: path,
		Want: []RetryAttemptText{{Text: "first", Completion: agent.MessageCompletionInterrupted}, {Text: "second", Completion: agent.MessageCompletionComplete}},
		NewAgent: func(t *testing.T, sink *Sink) func([]byte) {
			index := 0
			return func([]byte) {
				text, completion := "first", agent.MessageCompletionInterrupted
				if index > 0 {
					text, completion = "second", agent.MessageCompletionComplete
				}
				raw, err := agent.MarshalAssembledMessage(agent.AssembledMessageKindText, text, agent.MessageCompletionComplete)
				require.NoError(t, err)
				require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: raw, Completion: completion}, agent.SpanInfo{}))
				index++
			}
		},
	})
}

func TestRetryAttemptSuiteRejectsUnreadableEmptyAndMalformedFixtures(t *testing.T) {
	t.Parallel()
	for _, content := range []string{"", "invalid JSON"} {
		path := filepath.Join(t.TempDir(), "frames.jsonl")
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
		inner := &testing.T{}
		RunRetryAttemptCase(inner, RetryAttemptCase{FramesPath: path, NewAgent: func(*testing.T, *Sink) func([]byte) { return func([]byte) {} }})
		assert.True(t, inner.Failed())
	}
	inner := &testing.T{}
	RunRetryAttemptCase(inner, RetryAttemptCase{FramesPath: filepath.Join(t.TempDir(), "absent"), NewAgent: func(*testing.T, *Sink) func([]byte) { return func([]byte) {} }})
	assert.True(t, inner.Failed())
}

func TestRetryAttemptSuiteUsesTheCustomReaderAndPreservesFrameOrder(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "frames.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("{\"reply\":\"first\"}\n{\"reply\":\"second\"}\n"), 0o600))
	RunRetryAttemptCase(t, RetryAttemptCase{
		FramesPath: path,
		Want:       []RetryAttemptText{{Text: "first", Completion: agent.MessageCompletionComplete}, {Text: "second", Completion: agent.MessageCompletionComplete}},
		NewAgent: func(t *testing.T, sink *Sink) func([]byte) {
			return func(raw []byte) {
				require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: raw}, agent.SpanInfo{}))
			}
		},
		TextOf: func(row Message) (RetryAttemptText, bool) {
			var value struct {
				Reply string `json:"reply"`
			}
			require.NoError(t, json.Unmarshal(row.Content, &value))
			return RetryAttemptText{Text: value.Reply, Completion: agent.MessageCompletionComplete}, true
		},
	})
}
