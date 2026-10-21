package agenttest

import (
	"bytes"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
)

// RetryAttemptText states one retained text row and its completion.
type RetryAttemptText struct {
	Text       string
	Completion agent.MessageCompletion
}

// RetryAttemptCase replays recorded native frames through a fresh provider.
type RetryAttemptCase struct {
	Name       string
	FramesPath string
	NewAgent   func(t *testing.T, sink *Sink) (feed func(frame []byte))
	TextOf     func(m Message) (RetryAttemptText, bool)
	Want       []RetryAttemptText
	Discarded  []string
}

// RunRetryAttemptCase checks retained text order and rejects discarded text in every stored row.
func RunRetryAttemptCase(t *testing.T, tc RetryAttemptCase) {
	t.Helper()
	if !assert.NotNil(t, tc.NewAgent, "%s: the retry suite needs an agent factory", tc.Name) {
		return
	}
	raw, err := os.ReadFile(tc.FramesPath)
	if !assert.NoError(t, err, tc.Name) {
		return
	}
	sink := &Sink{}
	feed := tc.NewAgent(t, sink)
	if !assert.NotNil(t, feed, "%s: the factory returned no feed function", tc.Name) {
		return
	}
	frames := 0
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if !assert.True(t, json.Valid(line), "%s: frame %d is not JSON", tc.Name, frames+1) {
			return
		}
		feed(slices.Clone(line))
		frames++
	}
	if !assert.Positive(t, frames, "%s: the retry fixture has no frames", tc.Name) {
		return
	}
	textOf := tc.TextOf
	if textOf == nil {
		textOf = retryAttemptTextOf
	}
	var got []RetryAttemptText
	for _, row := range sink.Messages() {
		if text, ok := textOf(row); ok {
			got = append(got, text)
		}
	}
	assert.True(t, slices.Equal(tc.Want, got), "%s: retained text differs: got %#v, want %#v", tc.Name, got, tc.Want)
	rows := append(sink.Messages(), sink.PersistedNotifications()...)
	for _, notice := range sink.LeapMuxNotifications() {
		content, err := json.Marshal(notice)
		if assert.NoError(t, err, tc.Name) {
			rows = append(rows, Message{Content: content})
		}
	}
	for _, discarded := range tc.Discarded {
		if !assert.NotEmpty(t, discarded, "%s: a discarded-text marker cannot be empty", tc.Name) {
			continue
		}
		for _, row := range rows {
			assert.False(t, retryRowContains(row.Content, discarded), "%s: a row contains discarded text %q: %s", tc.Name, discarded, row.Content)
		}
	}
}

func retryAttemptTextOf(row Message) (RetryAttemptText, bool) {
	kind, text, completion, ok := DecodeAssembledMessage(row.Content)
	if !ok || row.Source != leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT || kind != agent.AssembledMessageKindText {
		return RetryAttemptText{}, false
	}
	if row.Completion != "" {
		completion = row.Completion
	}
	return RetryAttemptText{Text: text, Completion: completion}, true
}

// retryRowContains also checks decoded strings so JSON escapes cannot hide discarded text.
func retryRowContains(raw []byte, needle string) bool {
	if bytes.Contains(raw, []byte(needle)) {
		return true
	}
	var value interface{}
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	var contains func(interface{}) bool
	contains = func(value interface{}) bool {
		switch value := value.(type) {
		case string:
			return strings.Contains(value, needle)
		case []interface{}:
			for _, item := range value {
				if contains(item) {
					return true
				}
			}
		case map[string]interface{}:
			for key, item := range value {
				if strings.Contains(key, needle) || contains(item) {
					return true
				}
			}
		}
		return false
	}
	return contains(value)
}
