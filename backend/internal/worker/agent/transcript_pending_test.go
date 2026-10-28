package agent

import (
	"errors"
	"sync"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type pendingCaptureWriter struct {
	*captureOverride
	queue OutputQueue
}

func (writer *pendingCaptureWriter) OutputQueue() *OutputQueue { return &writer.queue }

func TestCapturedOperationSupplementKeepsItsExactWriteIdentity(t *testing.T) {
	t.Parallel()
	count := 0
	var stored MessageContent
	writer := &pendingCaptureWriter{captureOverride: &captureOverride{
		capture: func(content MessageContent, _ SpanInfo) MessageContent {
			count++
			content.AgentSessionID = "original"
			return content
		},
		write: func(_ string, _ leapmuxv1.MessageSource, content MessageContent, _ SpanInfo) (bool, error) {
			stored = content
			content.WriteReceipt.RecordStoredWrite(true)
			return true, nil
		},
	}}
	receipt := NewTranscriptWriteReceipt()
	original := []byte("{\"native\":9007199254740993}")
	span := SpanInfo{SpanID: "span", SpanType: "bash", SpanColor: 4, Closing: true}
	captured, err := CaptureTranscriptWithReceipt(writer, MessageContent{Original: original, WriteReceipt: receipt}, span)
	require.NoError(t, err)
	operation := captured.MessageOperation(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT)
	supplemental := []byte("{\"late\":{\"value\":9007199254740993}}")
	updated, err := operation.WithSupplementalContent(supplemental)
	require.NoError(t, err)
	supplemental[0] = '!'
	original[0] = '!'
	assert.Equal(t, 1, count)
	assert.Same(t, writer, updated.captured.TranscriptWriter())
	assert.Same(t, receipt, updated.FrozenContent().WriteReceipt)
	assert.Equal(t, "original", updated.FrozenContent().AgentSessionID)
	assert.Equal(t, span, updated.captured.write.span)
	assert.Equal(t, "{\"native\":9007199254740993}", string(updated.FrozenContent().Original))
	assert.JSONEq(t, "{\"late\":{\"value\":9007199254740993}}", string(updated.FrozenContent().Supplemental))
	require.ErrorIs(t, PersistCapturedTranscriptBatch(writer, func() []CapturedTranscriptOperation { return []CapturedTranscriptOperation{operation} }), ErrCapturedTranscriptImmutable)
	require.NoError(t, PersistCapturedTranscriptBatch(writer, func() []CapturedTranscriptOperation { return []CapturedTranscriptOperation{updated} }))
	assert.JSONEq(t, "{\"late\":{\"value\":9007199254740993}}", string(stored.Supplemental))
	assert.Equal(t, 1, count)
}

func TestCapturedOperationCannotChangeAfterSubmissionOrAnAttempt(t *testing.T) {
	for _, mode := range []string{"submitted", "in flight", "committed"} {
		t.Run(mode, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			failure := errors.New("store unavailable")
			writer := &pendingCaptureWriter{captureOverride: &captureOverride{
				write: func(_ string, _ leapmuxv1.MessageSource, content MessageContent, _ SpanInfo) (bool, error) {
					if mode == "in flight" {
						once.Do(func() { close(entered) })
						<-release
					}
					if mode == "submitted" {
						return false, failure
					}
					content.WriteReceipt.RecordStoredWrite(true)
					return true, nil
				},
			}}
			captured := CaptureTranscript(writer, MessageContent{Original: []byte("{\"native\":\"original\"}")}, SpanInfo{})
			operation := captured.MessageOperation(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT)
			finished := make(chan error, 1)
			if mode == "in flight" {
				go func() { finished <- captured.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT) }()
				ctx := testutil.DeadlineContext(t)
				select {
				case <-entered:
				case <-ctx.Done():
					t.Fatal("the capture did not enter storage")
				}
			} else {
				err := PersistCapturedTranscriptBatch(writer, func() []CapturedTranscriptOperation { return []CapturedTranscriptOperation{operation} })
				if mode == "submitted" {
					require.ErrorIs(t, err, failure)
				} else {
					require.NoError(t, err)
				}
			}
			_, err := operation.WithSupplementalContent([]byte("{\"late\":true}"))
			require.ErrorIs(t, err, ErrCapturedTranscriptImmutable)
			if mode == "in flight" {
				close(release)
				require.NoError(t, <-finished)
			}
			if mode == "submitted" {
				writer.write = func(_ string, _ leapmuxv1.MessageSource, _ MessageContent, _ SpanInfo) (bool, error) {
					return false, nil
				}
				require.NoError(t, writer.queue.Flush())
			}
		})
	}
}

func TestCapturedOperationInvalidSupplementDoesNotInvalidateTheOriginal(t *testing.T) {
	writer := &pendingCaptureWriter{captureOverride: &captureOverride{write: func(_ string, _ leapmuxv1.MessageSource, _ MessageContent, _ SpanInfo) (bool, error) {
		return false, nil
	}}}
	_, err := CaptureTranscriptWithReceipt(writer, MessageContent{}, SpanInfo{})
	require.Error(t, err)
	operation := CaptureTranscript(writer, MessageContent{Original: []byte("native")}, SpanInfo{}).MessageOperation(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT)
	_, err = operation.WithSupplementalContent([]byte("{"))
	require.Error(t, err)
	require.NoError(t, PersistCapturedTranscriptBatch(writer, func() []CapturedTranscriptOperation { return []CapturedTranscriptOperation{operation} }))
}

func TestCapturedOperationDerivationAndSubmissionHaveOneWinner(t *testing.T) {
	for range 50 {
		writes := 0
		writer := &pendingCaptureWriter{captureOverride: &captureOverride{write: func(_ string, _ leapmuxv1.MessageSource, _ MessageContent, _ SpanInfo) (bool, error) {
			writes++
			return false, nil
		}}}
		original := CaptureTranscript(writer, MessageContent{Original: []byte("native")}, SpanInfo{}).MessageOperation(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT)
		start := make(chan struct{})
		submitted := make(chan error, 1)
		type derivedResult struct {
			operation CapturedTranscriptOperation
			err       error
		}
		derived := make(chan derivedResult, 1)
		go func() {
			<-start
			submitted <- PersistCapturedTranscriptBatch(writer, func() []CapturedTranscriptOperation { return []CapturedTranscriptOperation{original} })
		}()
		go func() {
			<-start
			operation, err := original.WithSupplementalContent([]byte("{\"late\":true}"))
			derived <- derivedResult{operation, err}
		}()
		close(start)
		result, submitErr := <-derived, <-submitted
		if result.err == nil {
			require.ErrorIs(t, submitErr, ErrCapturedTranscriptImmutable)
			require.NoError(t, PersistCapturedTranscriptBatch(writer, func() []CapturedTranscriptOperation { return []CapturedTranscriptOperation{result.operation} }))
		} else {
			require.ErrorIs(t, result.err, ErrCapturedTranscriptImmutable)
			require.NoError(t, submitErr)
		}
		assert.Equal(t, 1, writes)
	}
}
