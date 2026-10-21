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
	capturedWorkerNotification
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
	return captureTranscriptWithReceipt(writer, content, span, NewTranscriptWriteReceipt())
}

func captureTranscriptWithReceipt(writer TranscriptServices, content MessageContent, span SpanInfo, receipt *TranscriptWriteReceipt) CapturedTranscript {
	content = content.Clone()
	content.WriteReceipt = receipt
	if writer != nil {
		content = writer.CaptureMessage(content, span).Clone()
	}
	content.WriteReceipt = receipt
	return frozenTranscript(writer, content, span)
}

// frozenTranscript owns content that already carries its capture-time publication and receipt.
func frozenTranscript(writer TranscriptServices, content MessageContent, span SpanInfo) CapturedTranscript {
	return CapturedTranscript{write: &capturedTranscriptWrite{writer: writer, content: content, span: span}}
}

func (write CapturedTranscript) PersistMessage(source leapmuxv1.MessageSource) error {
	_, err := write.persist(capturedMessage, source)
	return err
}

// FrozenContent returns the captured record with its publication owner. A
// reader that builds one capture-time operation from the record (the captured
// goal writer) reads this; the answer stays shared, so a caller that mutates
// must Clone first.
func (write CapturedTranscript) FrozenContent() MessageContent {
	if write.write == nil {
		return MessageContent{}
	}
	return write.write.content
}

func (write CapturedTranscript) PersistNotification(source leapmuxv1.MessageSource) (bool, error) {
	return write.persist(capturedNotification, source)
}

func (write CapturedTranscript) PersistWorkerNotification() (bool, error) {
	return write.persist(capturedWorkerNotification, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX)
}

func (write CapturedTranscript) PersistTurnEnd() error {
	_, err := write.persist(capturedTurnEnd, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT)
	return err
}

// PersistQueuedMessage writes the current queue entry through its original outer writer.
// Transparent decorators keep this admission when they forward to an ordered inner sink.
func (write CapturedTranscript) PersistQueuedMessage(ticket *OutputTicket, source leapmuxv1.MessageSource) error {
	_, err := write.persistWithExecution(capturedMessage, source, &outputExecution{ticket: ticket, operation: capturedMessage, source: source})
	return err
}

func (write CapturedTranscript) persist(operation capturedOperation, source leapmuxv1.MessageSource) (bool, error) {
	return write.persistWithExecution(operation, source, nil)
}

func (write CapturedTranscript) persistWithExecution(operation capturedOperation, source leapmuxv1.MessageSource, execution *outputExecution) (bool, error) {
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
	if execution != nil {
		content.outputExecution = execution
	}
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
	case capturedWorkerNotification:
		broadcast, err = state.writer.PersistWorkerNotification(content)
	case capturedTurnEnd:
		err = state.writer.PersistTurnEnd(content, state.span)
	default:
		err = errors.New("the captured transcript operation is invalid")
	}
	completed = true
	return broadcast, err
}
