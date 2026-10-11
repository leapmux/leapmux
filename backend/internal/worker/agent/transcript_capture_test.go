package agent

import (
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

type captureOverride struct {
	TranscriptServices
	capture func(MessageContent, SpanInfo) MessageContent
	write   func(string, leapmuxv1.MessageSource, MessageContent, SpanInfo) (bool, error)
}

func TestCapturedTranscriptRejectsInFlightCopiesWithoutBlockingTheWriter(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	var record CapturedTranscript
	var first bool
	var nestedErr error
	var nestedDone chan error
	writer := &captureOverride{write: func(_ string, _ leapmuxv1.MessageSource, _ MessageContent, _ SpanInfo) (bool, error) {
		if first {
			return true, nil
		}
		first = true
		copy := record
		nestedDone = make(chan error, 1)
		go func() { nestedDone <- copy.PersistTurnEnd() }()
		select {
		case nestedErr = <-nestedDone:
			return true, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}}
	record = CaptureTranscript(writer, MessageContent{}, SpanInfo{})
	err := record.PersistTurnEnd()
	if err != nil {
		// The blocked baseline attempt exits before the test examines its copy.
		<-nestedDone
	}
	require.NoError(t, err, "the outer writer must receive an immediate result from copied-record reentry")
	require.ErrorIs(t, nestedErr, ErrCapturedTranscriptInFlight)
	require.NoError(t, record.PersistTurnEnd())
}

func (sink *captureOverride) CaptureMessage(content MessageContent, span SpanInfo) MessageContent {
	if sink.capture != nil {
		return sink.capture(content, span)
	}
	return content
}

func (sink *captureOverride) PersistMessage(source leapmuxv1.MessageSource, content MessageContent, span SpanInfo) error {
	_, err := sink.write("message", source, content, span)
	return err
}

func (sink *captureOverride) PersistNotification(source leapmuxv1.MessageSource, content MessageContent) (bool, error) {
	return sink.write("notification", source, content, SpanInfo{})
}

func (sink *captureOverride) PersistTurnEnd(content MessageContent, span SpanInfo) error {
	_, err := sink.write("turn end", leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, span)
	return err
}

func TestCapturedTranscriptRetainsExactOuterWriterBytesAndSource(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"message", "notification", "turn end"} {
		t.Run(operation, func(t *testing.T) {
			original := []byte(" {\"text\":\"한😀\"} \n")
			supplement := []byte(" {\"native\":0} ")
			metadata := []byte(" {\"duration_ms\":0} ")
			span := SpanInfo{SpanID: "child-tool", ParentSpanID: "parent-tool", Closing: true, SpanColor: 7}
			content := MessageContent{Original: original, Supplemental: supplement, Metadata: metadata,
				AgentSessionID: "original-session", IdempotencyKey: "original-key", Completion: MessageCompletionInterrupted}
			want := content.Clone()
			errRejected := errors.New("the outer writer refused the record")
			var attempts int
			outer := &captureOverride{write: func(actual string, source leapmuxv1.MessageSource, got MessageContent, geometry SpanInfo) (bool, error) {
				attempts++
				assert.Equal(t, operation, actual)
				assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, source)
				require.NotNil(t, got.WriteReceipt)
				got.WriteReceipt = nil
				assert.Equal(t, want, got)
				if actual != "notification" {
					assert.Equal(t, span, geometry)
				}
				if attempts == 1 {
					got.Original[0], got.Supplemental[0], got.Metadata[0] = 'x', 'y', 'z'
					return false, errRejected
				}
				return true, nil
			}}
			record := CaptureTranscript(outer, content, span)
			original[0], supplement[0], metadata[0] = 'a', 'b', 'c'
			persist := func(record CapturedTranscript) error {
				switch operation {
				case "message":
					return record.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT)
				case "notification":
					_, err := record.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT)
					return err
				default:
					return record.PersistTurnEnd()
				}
			}
			require.ErrorIs(t, persist(record), errRejected)
			require.NoError(t, persist(record))
			copy := record
			require.NoError(t, persist(copy))
			assert.Equal(t, 2, attempts)
			require.ErrorContains(t, record.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER), "operation or source changed")
			assert.Equal(t, 2, attempts)
		})
	}
}

func TestCapturedNotificationPreservesIdentityInputsUntilAdmission(t *testing.T) {
	t.Parallel()
	for _, original := range notificationIdentityCorpus() {
		t.Run(original.IdempotencyKey, func(t *testing.T) {
			t.Parallel()
			content := original.Clone()
			content.AgentSessionID = "captured-native-session"
			want := content.Clone()
			refused := errors.New("the delegate refused admission")
			attempts := 0
			writer := &captureOverride{write: func(operation string, source leapmuxv1.MessageSource, received MessageContent, _ SpanInfo) (bool, error) {
				attempts++
				assert.Equal(t, "notification", operation)
				assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, source)
				require.NotNil(t, received.WriteReceipt)
				received.WriteReceipt = nil
				assert.Equal(t, want, received)
				for _, part := range [][]byte{received.Original, received.Supplemental, received.Metadata} {
					if len(part) != 0 {
						part[0] ^= 0xff
					}
				}
				if attempts == 1 {
					return false, refused
				}
				return true, nil
			}}
			record := CaptureTranscript(writer, content, SpanInfo{})
			for _, part := range [][]byte{content.Original, content.Supplemental, content.Metadata} {
				if len(part) != 0 {
					part[0] ^= 0xff
				}
			}
			_, err := record.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX)
			require.ErrorIs(t, err, refused)
			broadcast, err := record.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX)
			require.NoError(t, err)
			assert.True(t, broadcast)
			_, err = record.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX)
			require.NoError(t, err)
			assert.Equal(t, 2, attempts)
		})
	}
}

func TestCapturedTranscriptKeepsInheritedCaptureAndOuterOperation(t *testing.T) {
	t.Parallel()
	var captures, writes int
	inner := &captureOverride{capture: func(content MessageContent, _ SpanInfo) MessageContent {
		captures++
		content.AgentSessionID = "child-session"
		return content
	}}
	outer := &captureOverride{TranscriptServices: inner, capture: inner.CaptureMessage,
		write: func(_ string, _ leapmuxv1.MessageSource, content MessageContent, _ SpanInfo) (bool, error) {
			writes++
			assert.Equal(t, "child-session", content.AgentSessionID)
			return true, nil
		}}
	record := CaptureTranscript(outer, MessageContent{Original: []byte("{}")}, SpanInfo{})
	require.NoError(t, record.PersistTurnEnd())
	assert.Equal(t, 1, captures)
	assert.Equal(t, 1, writes)
}

func TestCapturedTranscriptRejectsEmptyWriterAndChangedOperation(t *testing.T) {
	t.Parallel()
	var empty CapturedTranscript
	require.ErrorContains(t, empty.PersistTurnEnd(), "no writer")
	require.ErrorContains(t, CaptureTranscript(nil, MessageContent{}, SpanInfo{}).PersistTurnEnd(), "no writer")
	errRejected := errors.New("the write failed")
	var attempts int
	writer := &captureOverride{write: func(_ string, _ leapmuxv1.MessageSource, _ MessageContent, _ SpanInfo) (bool, error) {
		attempts++
		return false, errRejected
	}}
	record := CaptureTranscript(writer, MessageContent{}, SpanInfo{})
	require.ErrorIs(t, record.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER), errRejected)
	require.ErrorContains(t, record.PersistTurnEnd(), "operation or source changed")
	_, err := record.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER)
	require.ErrorContains(t, err, "operation or source changed")
	assert.Equal(t, 1, attempts)
}

func TestCapturedTranscriptConcurrentSuccessfulCopiesWriteOnce(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var attempts int
	writer := &captureOverride{write: func(_ string, _ leapmuxv1.MessageSource, _ MessageContent, _ SpanInfo) (bool, error) {
		mu.Lock()
		attempts++
		mu.Unlock()
		return true, nil
	}}
	record := CaptureTranscript(writer, MessageContent{}, SpanInfo{})
	require.NoError(t, record.PersistTurnEnd())
	var group sync.WaitGroup
	for range 16 {
		group.Go(func() { assert.NoError(t, record.PersistTurnEnd()) })
	}
	group.Wait()
	mu.Lock()
	assert.Equal(t, 1, attempts)
	mu.Unlock()
}
