package agent

import (
	"errors"
	"slices"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// ErrCapturedTranscriptInFlight permits retry after the current attempt returns.
var ErrCapturedTranscriptInFlight = errors.New("the captured transcript write is in flight")

var ErrCapturedTranscriptImmutable = errors.New("the captured transcript can no longer change")

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

// TranscriptWriter returns the exact decorated writer retained by this capture.
func (write CapturedTranscript) TranscriptWriter() TranscriptServices {
	if write.write == nil {
		return nil
	}
	return write.write.writer
}

// CapturedTranscriptOperation fixes the operation and source of an immutable
// capture. Construct it through the capture's operation methods.
type CapturedTranscriptOperation struct {
	captured  CapturedTranscript
	operation capturedOperation
	source    leapmuxv1.MessageSource
	err       error
}

// FrozenContent returns the captured record. Clone before changing byte slices.
func (operation CapturedTranscriptOperation) FrozenContent() MessageContent {
	return operation.captured.FrozenContent()
}

// WithSupplementalContent replaces an unsubmitted operation without recapturing
// its original writer, publication or span. The prior operation cannot persist.
func (operation CapturedTranscriptOperation) WithSupplementalContent(supplemental []byte) (CapturedTranscriptOperation, error) {
	if operation.err != nil {
		return CapturedTranscriptOperation{}, operation.err
	}
	state := operation.captured.write
	if state == nil || state.writer == nil {
		return CapturedTranscriptOperation{}, errors.New("the captured transcript write has no writer")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.inFlight || state.succeeded || state.submitted || state.superseded {
		return CapturedTranscriptOperation{}, ErrCapturedTranscriptImmutable
	}
	if _, committed := state.content.WriteReceipt.StoredMessageSequence(); committed {
		return CapturedTranscriptOperation{}, ErrCapturedTranscriptImmutable
	}
	content := state.content.Clone()
	content.Supplemental = slices.Clone(supplemental)
	if _, err := EncodeMessageSupplement(content); err != nil {
		return CapturedTranscriptOperation{}, err
	}
	next := &capturedTranscriptWrite{writer: state.writer, content: content, span: state.span,
		operation: operation.operation, source: operation.source}
	state.superseded = true
	return CapturedTranscriptOperation{captured: CapturedTranscript{write: next}, operation: operation.operation, source: operation.source}, nil
}

func (operation CapturedTranscriptOperation) markSubmitted() error {
	state := operation.captured.write
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.superseded {
		return ErrCapturedTranscriptImmutable
	}
	state.submitted = true
	return nil
}

// MessageOperation selects a message write without capturing or persisting again.
func (write CapturedTranscript) MessageOperation(source leapmuxv1.MessageSource) CapturedTranscriptOperation {
	return write.selectOperation(capturedMessage, source)
}

// NotificationOperation preserves the native notification's reduction semantics.
func (write CapturedTranscript) NotificationOperation(source leapmuxv1.MessageSource) CapturedTranscriptOperation {
	return write.selectOperation(capturedNotification, source)
}

// WorkerNotificationOperation selects the worker notification write path.
func (write CapturedTranscript) WorkerNotificationOperation() CapturedTranscriptOperation {
	return write.selectOperation(capturedWorkerNotification, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX)
}

// TurnEndOperation preserves the divider's completion effects.
func (write CapturedTranscript) TurnEndOperation() CapturedTranscriptOperation {
	return write.selectOperation(capturedTurnEnd, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT)
}

func (write CapturedTranscript) selectOperation(operation capturedOperation, source leapmuxv1.MessageSource) CapturedTranscriptOperation {
	selected := CapturedTranscriptOperation{captured: write, operation: operation, source: source}
	state := write.write
	if state == nil || state.writer == nil {
		selected.err = errors.New("the captured transcript write has no writer")
		return selected
	}
	if err := validateOrderedContent(source, state.content); err != nil {
		selected.err = err
		return selected
	}
	state.mu.Lock()
	if state.superseded {
		state.mu.Unlock()
		selected.err = ErrCapturedTranscriptImmutable
		return selected
	}
	if state.operation != 0 && (state.operation != operation || state.source != source) {
		selected.err = errors.New("the captured transcript operation or source changed")
	} else {
		state.operation, state.source = operation, source
	}
	state.mu.Unlock()
	return selected
}

type capturedOperation uint8

const (
	capturedMessage capturedOperation = iota + 1
	capturedNotification
	capturedTurnEnd
	capturedWorkerNotification
)

type capturedTranscriptWrite struct {
	mu         sync.Mutex
	writer     TranscriptServices
	content    MessageContent
	span       SpanInfo
	operation  capturedOperation
	source     leapmuxv1.MessageSource
	succeeded  bool
	inFlight   bool
	submitted  bool
	superseded bool
}

// CaptureTranscript freezes one record before attribution or persistence delays it.
// Persistence delegates through the exact outer writer and preserves its overrides.
func CaptureTranscript(writer TranscriptServices, content MessageContent, span SpanInfo) CapturedTranscript {
	return captureTranscriptWithReceipt(writer, content, span, NewTranscriptWriteReceipt())
}

// CaptureTranscriptWithReceipt freezes a record under its caller's write identity.
func CaptureTranscriptWithReceipt(writer TranscriptServices, content MessageContent, span SpanInfo) (CapturedTranscript, error) {
	if content.WriteReceipt == nil {
		return CapturedTranscript{}, errors.New("the transcript capture has no write receipt")
	}
	return captureTranscriptWithReceipt(writer, content, span, content.WriteReceipt), nil
}

// CapturePreparedTranscript freezes publication before prepare selects the
// payload. The result retains the exact writer without capturing again.
// A nil selected payload returns present=false. An empty payload remains a row.
func CapturePreparedTranscript(writer TranscriptServices, source leapmuxv1.MessageSource, template MessageContent, span SpanInfo, prepare func() []byte) (capture CapturedTranscript, present bool, err error) {
	if prepare == nil {
		return CapturedTranscript{}, false, errors.New("the transcript message has no preparation function")
	}
	if err := validateOrderedContent(source, template); err != nil {
		return CapturedTranscript{}, false, err
	}
	if writer == nil {
		return CapturedTranscript{}, false, errors.New("the prepared transcript message has no writer")
	}
	content := CaptureTranscript(writer, template, span).FrozenContent()
	selected := prepare()
	if selected == nil {
		return CapturedTranscript{}, false, nil
	}
	content.Original = slices.Clone(selected)
	return frozenTranscript(writer, content, span), true, nil
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
	if state.superseded {
		state.mu.Unlock()
		return false, ErrCapturedTranscriptImmutable
	}
	if state.operation != 0 && (state.operation != operation || state.source != source) {
		state.mu.Unlock()
		return false, errors.New("the captured transcript operation or source changed")
	}
	state.operation = operation
	state.source = source
	state.submitted = true
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
