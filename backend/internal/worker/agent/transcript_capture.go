package agent

import (
	"errors"
	"slices"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// ErrCapturedTranscriptInFlight permits retry after the current attempt returns.
var ErrCapturedTranscriptInFlight = errors.New("the captured transcript write is in flight")

// Clone copies the mutable bytes and retains the original publication owner.
func (content MessageContent) Clone() MessageContent {
	content.Original = slices.Clone(content.Original)
	content.Supplemental = slices.Clone(content.Supplemental)
	content.Metadata = slices.Clone(content.Metadata)
	return content
}

// CapturedTranscript retains one immutable record and its exact outer writer.
// Copies share the success receipt, so a successful retry repeats no side effect.
type CapturedTranscript struct {
	write *capturedTranscriptWrite
}

type capturedOperation uint8

const (
	capturedMessage capturedOperation = iota + 1
	capturedNotification
	capturedTurnEnd
)

type capturedTranscriptWrite struct {
	mu        sync.Mutex
	writer    TranscriptServices
	content   MessageContent
	span      SpanInfo
	operation capturedOperation
	source    leapmuxv1.MessageSource
	succeeded bool
	inFlight  bool
}

// CaptureTranscript freezes one record before attribution or persistence delays it.
// Persistence delegates through the exact outer writer and preserves its overrides.
func CaptureTranscript(writer TranscriptServices, content MessageContent, span SpanInfo) CapturedTranscript {
	content = content.Clone()
	receipt := NewTranscriptWriteReceipt()
	content.WriteReceipt = receipt
	if writer != nil {
		content = writer.CaptureMessage(content, span).Clone()
	}
	content.WriteReceipt = receipt
	return CapturedTranscript{write: &capturedTranscriptWrite{writer: writer, content: content, span: span}}
}

func (write CapturedTranscript) PersistMessage(source leapmuxv1.MessageSource) error {
	_, err := write.persist(capturedMessage, source)
	return err
}

func (write CapturedTranscript) PersistNotification(source leapmuxv1.MessageSource) (bool, error) {
	return write.persist(capturedNotification, source)
}

func (write CapturedTranscript) PersistTurnEnd() error {
	_, err := write.persist(capturedTurnEnd, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT)
	return err
}

func (write CapturedTranscript) persist(operation capturedOperation, source leapmuxv1.MessageSource) (bool, error) {
	if write.write == nil || write.write.writer == nil {
		return false, errors.New("the captured transcript write has no writer")
	}
	state := write.write
	state.mu.Lock()
	if state.operation != 0 && (state.operation != operation || state.source != source) {
		state.mu.Unlock()
		return false, errors.New("the captured transcript operation or source changed")
	}
	state.operation = operation
	state.source = source
	if state.succeeded {
		state.mu.Unlock()
		return false, nil
	}
	if state.inFlight {
		state.mu.Unlock()
		return false, ErrCapturedTranscriptInFlight
	}
	state.inFlight = true
	state.mu.Unlock()

	content := state.content.Clone()
	var broadcast bool
	var err error
	completed := false
	defer func() {
		state.mu.Lock()
		state.inFlight = false
		if completed && err == nil {
			state.succeeded = true
		}
		state.mu.Unlock()
	}()
	switch operation {
	case capturedMessage:
		err = state.writer.PersistMessage(source, content, state.span)
	case capturedNotification:
		broadcast, err = state.writer.PersistNotification(source, content)
	case capturedTurnEnd:
		err = state.writer.PersistTurnEnd(content, state.span)
	default:
		err = errors.New("the captured transcript operation is invalid")
	}
	completed = true
	return broadcast, err
}
