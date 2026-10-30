package agent

import (
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"sync/atomic"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// PendingOutputError reports a write that the queue owns but did not finish.
// The caller must not submit a second copy. A later flush retries the retained write.
type PendingOutputError struct{ Err error }

func (err *PendingOutputError) Error() string {
	if err.Err == nil {
		return "output remains queued"
	}
	return "output remains queued: " + err.Err.Error()
}
func (err *PendingOutputError) Unwrap() error { return err.Err }

// OutputRetained reports whether the queue accepted this operation, including a deferred write.
func OutputRetained(err error) bool {
	var pending *PendingOutputError
	return err == nil || errors.As(err, &pending)
}

// outputExecution identifies the queue entry that already owns this forwarded write.
type outputExecution struct {
	ticket    *OutputTicket
	operation capturedOperation
	source    leapmuxv1.MessageSource
}

func (execution *outputExecution) active() bool {
	if execution == nil || execution.ticket == nil {
		return false
	}
	queue := execution.ticket.Queue()
	return queue != nil && queue.executes(execution.ticket)
}

// IsTranscriptWriteExecuting identifies an operation already admitted to its queue.
// A decorator can close fresh admission while preserving accepted immutable writes.
func IsTranscriptWriteExecuting(content MessageContent) bool {
	return content.outputExecution.active()
}

type orderedTranscript struct {
	TranscriptServices
	queue *OutputQueue
}

func validateOrderedContent(source leapmuxv1.MessageSource, content MessageContent) error {
	if source == leapmuxv1.MessageSource_MESSAGE_SOURCE_UNSPECIFIED {
		return errors.New("the transcript source is unspecified")
	}
	if _, known := leapmuxv1.MessageSource_name[int32(source)]; !known {
		return errors.New("the transcript source is unknown")
	}
	if err := ValidateMessageCompletion(content); err != nil {
		return err
	}
	return ValidateIncomingMessageMetadata(content.Metadata)
}

// FlushPendingOutput retries retained writes without submitting another copy.
func FlushPendingOutput(writer TranscriptServices) error {
	queue := OutputQueueFor(writer)
	if queue == nil {
		return nil
	}
	err := queue.Flush()
	if errors.Is(err, ErrOutputInFlight) {
		return nil
	}
	return err
}

// TranscriptMessage supplies one immutable message in an ordered batch.
type TranscriptMessage struct {
	Source  leapmuxv1.MessageSource
	Content MessageContent
	Span    SpanInfo
}

// PersistTranscriptBatch reserves every row before capture or persistence can call the sink.
// A retained error transfers retry ownership to the queue. The caller must not submit the batch again.
func PersistTranscriptBatch(writer TranscriptServices, messages []TranscriptMessage) error {
	queue := OutputQueueFor(writer)
	if queue == nil {
		return errors.New("the transcript batch has no output queue")
	}
	messages = slices.Clone(messages)
	for i := range messages {
		messages[i].Content = messages[i].Content.Clone()
	}
	for _, message := range messages {
		if err := validateOrderedContent(message.Source, message.Content); err != nil {
			return err
		}
	}
	return persistCapturedTranscriptBatch(writer, queue, func() []CapturedTranscriptOperation {
		operations := make([]CapturedTranscriptOperation, 0, len(messages))
		for _, message := range messages {
			operations = append(operations, CaptureTranscript(writer, message.Content, message.Span).MessageOperation(message.Source))
		}
		return operations
	})
}

// PersistCapturedTranscriptBatch reserves one queue position before preparation.
// prepare freezes an already selected representation against its original
// writers. It must not read native history or change the representation.
// No batch operation runs until every capture is ready. A retained failure
// retries the captures, without invoking prepare again.
func PersistCapturedTranscriptBatch(writer TranscriptServices, prepare func() []CapturedTranscriptOperation) error {
	return persistCapturedTranscriptBatch(writer, OutputQueueFor(writer), prepare)
}

func persistCapturedTranscriptBatch(writer TranscriptServices, queue *OutputQueue, prepare func() []CapturedTranscriptOperation) error {
	if prepare == nil {
		return errors.New("the transcript batch has no preparation function")
	}
	batch, err := reserveCapturedTranscriptBatch(writer, queue)
	if err != nil {
		return err
	}
	return batch.Prepare(prepare)
}

const (
	capturedBatchReserved uint32 = iota
	capturedBatchPreparing
	capturedBatchReady
	capturedBatchDiscarded
)

// CapturedTranscriptBatch owns an output position before its captures are ready.
// Prepare fills it once. Discard releases an unused reservation.
type CapturedTranscriptBatch struct {
	queue      *OutputQueue
	ticket     *OutputTicket
	owner      *OutputOwner
	state      atomic.Uint32
	operations []CapturedTranscriptOperation
	index      int // Only the queue's current callback reads or changes index.
	stream     *capturedTranscriptStream
	rejection  atomic.Pointer[capturedOutputRefusal]
}

type capturedTranscriptStream struct {
	next      func() (CapturedTranscriptOperation, bool, error)
	pending   *CapturedTranscriptOperation
	submitted bool
	done      bool
}

// ReserveCapturedTranscriptBatch prevents later rows or effects from overtaking
// an admitted group. The caller must eventually Prepare or Discard the group.
func ReserveCapturedTranscriptBatch(writer TranscriptServices) (*CapturedTranscriptBatch, error) {
	return reserveCapturedTranscriptBatch(writer, OutputQueueFor(writer))
}

func reserveCapturedTranscriptBatch(writer TranscriptServices, queue *OutputQueue) (*CapturedTranscriptBatch, error) {
	if queue == nil {
		return nil, errors.New("the transcript batch has no output queue")
	}
	batch := &CapturedTranscriptBatch{queue: queue, owner: NewOutputOwner()}
	tickets, err := writer.ReserveTranscriptOutput(queue, OutputWrite{Owner: batch.owner, Label: "persist captured transcript batch", Run: batch.persist})
	if err != nil {
		return nil, err
	}
	batch.ticket = tickets[0]
	return batch, nil
}

func (batch *CapturedTranscriptBatch) persist() error {
	if batch.state.Load() != capturedBatchReady {
		return ErrOutputInFlight
	}
	if batch.stream != nil {
		return batch.persistStream()
	}
	for batch.index < len(batch.operations) {
		operation := batch.operations[batch.index]
		_, err := operation.captured.persistWithExecution(operation.operation, operation.source,
			&outputExecution{ticket: batch.ticket, operation: operation.operation, source: operation.source})
		if err != nil {
			return err
		}
		batch.index++
	}
	return nil
}

// PrepareStream installs one completely selected representation. The caller
// validates the whole native candidate before selecting archive or live output.
// next returns done=true at the end. A capture error must keep its position.
// A storage error retries the retained capture without calling next again.
// The queue owns the iterator and its recovery data until completion or a permanent
// destination refusal. A permanent refusal releases the remaining stream after any
// accepted prefix and allows later writes to continue.
func (batch *CapturedTranscriptBatch) PrepareStream(next func() (CapturedTranscriptOperation, bool, error)) error {
	if batch == nil {
		return errors.New("the transcript batch has no reservation")
	}
	if next == nil {
		batch.Discard()
		return errors.New("the transcript stream has no iterator")
	}
	if !batch.state.CompareAndSwap(capturedBatchReserved, capturedBatchPreparing) {
		return errors.New("the transcript batch preparation already began or was discarded")
	}
	batch.stream = &capturedTranscriptStream{next: next}
	batch.state.Store(capturedBatchReady)
	return batch.flushPrepared()
}

func (batch *CapturedTranscriptBatch) persistStream() error {
	stream := batch.stream
	if stream.done {
		return nil
	}
	for {
		if stream.pending == nil {
			operation, done, err := stream.next()
			if err != nil {
				return err
			}
			if done {
				stream.done = true
				stream.next = nil
				return nil
			}
			// A rejected operation stays here so a retry cannot omit its row.
			stream.pending = &operation
			stream.submitted = false
		}
		operation := stream.pending
		if !stream.submitted {
			if err := batch.validateStreamOperation(*operation); err != nil {
				var refusal *capturedOutputRefusal
				if errors.As(err, &refusal) {
					// An expired destination cannot recover. Report partial failure and
					// release the remaining stream position so other transcripts can write.
					batch.rejection.Store(refusal)
					batch.state.Store(capturedBatchDiscarded)
					stream.pending = nil
					stream.next = nil
					batch.queue.ClearOwner(batch.owner)
				}
				return err
			}
			if err := operation.markSubmitted(); err != nil {
				return err
			}
			stream.submitted = true
		}
		_, err := operation.captured.persistWithExecution(operation.operation, operation.source,
			&outputExecution{ticket: batch.ticket, operation: operation.operation, source: operation.source})
		if err != nil {
			return err
		}
		stream.pending = nil
		stream.submitted = false
	}
}

func (batch *CapturedTranscriptBatch) validateStreamOperation(operation CapturedTranscriptOperation) error {
	if operation.err != nil {
		return operation.err
	}
	state := operation.captured.write
	if state == nil || state.writer == nil {
		return errors.New("the captured transcript write has no writer")
	}
	if err := validateOrderedContent(operation.source, state.content); err != nil {
		return err
	}
	switch operation.operation {
	case capturedMessage, capturedNotification:
	case capturedWorkerNotification:
		if operation.source != leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX {
			return errors.New("the worker notification has an invalid source")
		}
	case capturedTurnEnd:
		if operation.source != leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT {
			return errors.New("the transcript turn end has an invalid source")
		}
	default:
		return errors.New("the captured transcript operation is unknown")
	}
	if OutputQueueFor(state.writer) != batch.queue {
		return errors.New("the captured transcript operation belongs to another output queue")
	}
	if err := state.writer.ValidateTranscriptOutput(batch.ticket); err != nil {
		return &capturedOutputRefusal{err}
	}
	return nil
}

func (batch *CapturedTranscriptBatch) flushPrepared() error {
	err := batch.queue.FlushAfterReady(batch.ticket)
	if refusal := batch.rejection.Load(); refusal != nil {
		_ = batch.queue.Flush()
		return refusal
	}
	if batch.ticket.Done() {
		return nil
	}
	if err == nil {
		err = ErrOutputInFlight
	}
	return &PendingOutputError{Err: err}
}

// Discard releases a group only before preparation begins. It invokes no sink.
// A caller that has no other publication to flush must flush its queue afterward.
func (batch *CapturedTranscriptBatch) Discard() bool {
	if batch == nil || !batch.state.CompareAndSwap(capturedBatchReserved, capturedBatchDiscarded) {
		return false
	}
	batch.queue.ClearOwner(batch.owner)
	return true
}

// Prepare installs one complete selected representation. It can run after the
// admission call returns. No operation publishes until all captures are ready.
// A failure in storage keeps this same representation for retry.
func (batch *CapturedTranscriptBatch) Prepare(prepare func() []CapturedTranscriptOperation) error {
	if batch == nil {
		return errors.New("the transcript batch has no reservation")
	}
	if prepare == nil {
		batch.Discard()
		return errors.New("the transcript batch has no preparation function")
	}
	if !batch.state.CompareAndSwap(capturedBatchReserved, capturedBatchPreparing) {
		return errors.New("the transcript batch preparation already began or was discarded")
	}
	prepared := false
	defer func() {
		if !prepared {
			// Invalid or panicking preparation admits no batch row. Remove its
			// unready position without discarding any reentrant submission.
			batch.state.Store(capturedBatchDiscarded)
			batch.queue.ClearOwner(batch.owner)
			_ = batch.queue.Flush()
		}
	}()
	batch.operations = slices.Clone(prepare())
	for _, operation := range batch.operations {
		if operation.err != nil {
			return operation.err
		}
		if operation.captured.write == nil || operation.captured.write.writer == nil {
			return errors.New("the captured transcript write has no writer")
		}
		if OutputQueueFor(operation.captured.write.writer) != batch.queue {
			return errors.New("the captured transcript operation belongs to another output queue")
		}
		if err := operation.captured.write.writer.ValidateTranscriptOutput(batch.ticket); err != nil {
			return err
		}
	}
	for _, operation := range batch.operations {
		if err := operation.markSubmitted(); err != nil {
			return err
		}
	}
	batch.state.Store(capturedBatchReady)
	prepared = true
	return batch.flushPrepared()
}

// PersistPreparedMessage reserves the row before capture or preparation can yield to another writer.
// The template fixes its publication and metadata. prepare supplies only the final payload; nil omits the row.
// A persistence failure retains the prepared bytes. A retry never calls prepare again.
func PersistPreparedMessage(writer TranscriptServices, source leapmuxv1.MessageSource, template MessageContent, span SpanInfo, prepare func() []byte) error {
	if prepare == nil {
		return errors.New("the transcript message has no preparation function")
	}
	if err := validateOrderedContent(source, template); err != nil {
		return err
	}
	if prepared, ok := writer.(PreparedTranscriptWriter); ok {
		return prepared.PersistPreparedMessage(source, template, span, prepare)
	}
	if OutputQueueFor(writer) == nil {
		return errors.New("the transcript message has no output queue")
	}
	return PersistCapturedTranscriptBatch(writer, func() []CapturedTranscriptOperation {
		capture, present, err := CapturePreparedTranscript(writer, source, template, span, prepare)
		if err != nil {
			return []CapturedTranscriptOperation{{err: err}}
		}
		if !present {
			return nil
		}
		return []CapturedTranscriptOperation{capture.MessageOperation(source)}
	})
}

// OutputQueueFor returns the ordering queue of an ordered transcript writer.
func OutputQueueFor(writer TranscriptServices) *OutputQueue {
	switch value := writer.(type) {
	case *orderedProviderServices:
		return value.transcript.queue
	case orderedTranscript:
		return value.queue
	default:
		if owner, ok := writer.(interface{ OutputQueue() *OutputQueue }); ok {
			return owner.OutputQueue()
		}
		return nil
	}
}

// DirectTranscript preserves the decorated writer below the queue.
// A generation batch uses it after it reserves its own positions in that same queue.
func DirectTranscript(writer TranscriptServices) TranscriptServices {
	switch value := writer.(type) {
	case *orderedProviderServices:
		return value.ProviderServices
	case orderedTranscript:
		return value.TranscriptServices
	default:
		return writer
	}
}

// OrderedTranscript shares one output queue with the caller's generation buffers.
// It captures each native record before submission and delegates to the supplied writer.
func OrderedTranscript(queue *OutputQueue, writer TranscriptServices) TranscriptServices {
	return orderedTranscript{TranscriptServices: writer, queue: queue}
}

func (writer orderedTranscript) submit(label string, run func() error) error {
	ticket := writer.queue.Append(OutputWrite{Label: label, Run: run})[0]
	err := writer.queue.Flush()
	if ticket.Done() {
		return nil
	}
	if err == nil {
		err = ErrOutputInFlight
	}
	return &PendingOutputError{Err: err}
}

// orderedOutputRecord associates one caller receipt with one queued operation.
// A retry through an outer captured writer must find this operation instead of creating another one.
type orderedOutputRecord struct {
	queue     *OutputQueue
	operation capturedOperation
	source    leapmuxv1.MessageSource
	ticket    *OutputTicket
	captured  CapturedTranscript
	ready     atomic.Bool
	broadcast atomic.Bool
	reported  atomic.Bool
}

func (writer orderedTranscript) persist(operation capturedOperation, source leapmuxv1.MessageSource, content MessageContent, span SpanInfo) (bool, error) {
	if err := validateOrderedContent(source, content); err != nil {
		return false, err
	}
	if execution := content.outputExecution; execution.active() {
		if execution.operation != operation || execution.source != source {
			return false, errors.New("the forwarded output operation or source changed")
		}
		switch operation {
		case capturedMessage:
			return false, writer.TranscriptServices.PersistMessage(source, content, span)
		case capturedNotification:
			return writer.TranscriptServices.PersistNotification(source, content)
		case capturedWorkerNotification:
			return writer.TranscriptServices.PersistWorkerNotification(content)
		case capturedTurnEnd:
			return false, writer.TranscriptServices.PersistTurnEnd(content, span)
		}
	}
	content.outputExecution = nil
	receipt := content.WriteReceipt
	if receipt == nil {
		receipt = NewTranscriptWriteReceipt()
	}
	receipt.orderedMu.Lock()
	record := receipt.ordered
	first := record == nil
	if first {
		record = &orderedOutputRecord{queue: writer.queue, operation: operation, source: source}
		tickets, reservationErr := writer.ReserveTranscriptOutput(writer.queue, OutputWrite{Label: "persist ordered transcript", Run: func() error {
			if !record.ready.Load() {
				return ErrOutputInFlight
			}
			broadcast, err := record.captured.persistWithExecution(record.operation, record.source, &outputExecution{ticket: record.ticket, operation: record.operation, source: record.source})
			record.broadcast.Store(broadcast)
			return err
		}})
		if reservationErr != nil {
			receipt.orderedMu.Unlock()
			return false, reservationErr
		}
		record.ticket = tickets[0]
		receipt.ordered = record
	}
	receipt.orderedMu.Unlock()
	if record.queue != writer.queue || record.operation != operation || record.source != source {
		return false, errors.New("the ordered transcript operation, source or queue changed")
	}
	var err error
	if first {
		record.captured = captureTranscriptWithReceipt(writer.TranscriptServices, content, span, receipt)
		record.ready.Store(true)
		err = writer.queue.FlushAfterReady(record.ticket)
	} else {
		err = writer.queue.Flush()
	}
	if record.ticket.Done() {
		return record.broadcast.Load() && record.reported.CompareAndSwap(false, true), nil
	}
	if err == nil {
		err = ErrOutputInFlight
	}
	return false, &PendingOutputError{Err: err}
}

func (writer orderedTranscript) PersistMessage(source leapmuxv1.MessageSource, content MessageContent, span SpanInfo) error {
	_, err := writer.persist(capturedMessage, source, content, span)
	return err
}
func (writer orderedTranscript) PersistNotification(source leapmuxv1.MessageSource, content MessageContent) (bool, error) {
	return writer.persist(capturedNotification, source, content, SpanInfo{})
}
func (writer orderedTranscript) PersistTurnEnd(content MessageContent, span SpanInfo) error {
	_, err := writer.persist(capturedTurnEnd, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, span)
	return err
}
func (writer orderedTranscript) PersistWorkerNotification(content MessageContent) (bool, error) {
	return writer.persist(capturedWorkerNotification, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, content, SpanInfo{})
}

type orderedProviderServices struct {
	ProviderServices
	transcript orderedTranscript
	// latestTurnSeq includes queued inactive publications. A later start can
	// overtake them, so retry admission needs the submitted sequence as well.
	latestTurnSeq atomic.Uint64
}

// NewOrderedProviderServices orders transcript writes and their dependent lifecycle effects.
// Live progress and span tracking remain immediate. CaptureTranscript keeps each row's original span snapshot.
func NewOrderedProviderServices(queue *OutputQueue, inner ProviderServices) ProviderServices {
	if queue == nil {
		queue = &OutputQueue{}
	}
	if existing, ok := inner.(*orderedProviderServices); ok {
		if existing.transcript.queue == queue {
			return existing
		}
		if existing.transcript.queue.HasPending() {
			panic("cannot replace an output queue while it owns pending writes")
		}
		inner = existing.ProviderServices
	}
	return &orderedProviderServices{ProviderServices: inner, transcript: orderedTranscript{TranscriptServices: inner, queue: queue}}
}

func (services *orderedProviderServices) OutputQueue() *OutputQueue { return services.transcript.queue }

func (services *orderedProviderServices) PersistMessage(source leapmuxv1.MessageSource, content MessageContent, span SpanInfo) error {
	return services.transcript.PersistMessage(source, content, span)
}
func (services *orderedProviderServices) PersistNotification(source leapmuxv1.MessageSource, content MessageContent) (bool, error) {
	return services.transcript.PersistNotification(source, content)
}
func (services *orderedProviderServices) PersistTurnEnd(content MessageContent, span SpanInfo) error {
	return services.transcript.PersistTurnEnd(content, span)
}
func (services *orderedProviderServices) PersistWorkerNotification(content MessageContent) (bool, error) {
	return services.transcript.PersistWorkerNotification(content)
}
func (services *orderedProviderServices) PersistLeapMuxNotification(content map[string]interface{}) {
	raw, err := json.Marshal(content)
	if err == nil {
		_, err = services.PersistWorkerNotification(MessageContent{Original: raw})
	}
	if err != nil {
		slog.Error("persist ordered worker notification", "error", err)
	}
}

func (services *orderedProviderServices) submitEffect(label string, effect func() error) {
	err := services.transcript.submit(label, effect)
	if err != nil && !errors.Is(err, ErrOutputInFlight) {
		slog.Error("retain dependent output effect", "effect", label, "error", err)
	}
}

func (services *orderedProviderServices) SetTurnState(state TurnState, seq uint64) {
	for previous := services.latestTurnSeq.Load(); seq > previous; previous = services.latestTurnSeq.Load() {
		if services.latestTurnSeq.CompareAndSwap(previous, seq) {
			break
		}
	}
	if state.Active {
		services.ProviderServices.SetTurnState(state, seq)
		return
	}
	services.submitEffect("release turn", func() error { services.ProviderServices.SetTurnState(state, seq); return nil })
}
func (services *orderedProviderServices) ReportSteersEchoed(count int) {
	if count <= 0 {
		return
	}
	services.submitEffect("report steer echo", services.CaptureSteerReport(count, SteerOutcomeEchoed))
}
func (services *orderedProviderServices) ReportSteersDropped(count int) {
	if count <= 0 {
		return
	}
	services.submitEffect("report dropped steer", services.CaptureSteerReport(count, SteerOutcomeDropped))
}
func (services *orderedProviderServices) ReportSteersUncertain(count int) {
	if count <= 0 {
		return
	}
	services.submitEffect("report uncertain steer", services.CaptureSteerReport(count, SteerOutcomeUncertain))
}
func (services *orderedProviderServices) ScheduleUsageLimitRetry(resetAt time.Time, sourcePayload []byte) {
	services.CaptureAutoRetry().ScheduleUsageLimitRetry(resetAt, sourcePayload)
}
func (services *orderedProviderServices) ScheduleUsageLimitRetryWithKey(key string, resetAt time.Time, sourcePayload []byte) {
	services.CaptureAutoRetry().ScheduleUsageLimitRetryWithKey(key, resetAt, sourcePayload)
}
func (services *orderedProviderServices) ScheduleServerErrorRetry(sourcePayload []byte) {
	services.CaptureAutoRetry().ScheduleServerErrorRetry(sourcePayload)
}
func (services *orderedProviderServices) CancelUsageLimitRetry(reason string) {
	services.CaptureAutoRetry().CancelUsageLimitRetry(reason)
}
func (services *orderedProviderServices) CancelUsageLimitRetryWithKey(key, reason string) {
	services.CaptureAutoRetry().CancelUsageLimitRetryWithKey(key, reason)
}
func (services *orderedProviderServices) CancelServerErrorRetry(reason string) {
	services.CaptureAutoRetry().CancelServerErrorRetry(reason)
}

func (services *orderedProviderServices) CaptureAutoRetryOperation(completionSeq uint64, apply func(AutoRetryWriter)) func() error {
	return services.ProviderServices.CaptureAutoRetryOperation(completionSeq, apply)
}

func (services *orderedProviderServices) CaptureAutoRetry() AutoRetryWriter {
	return services.CaptureAutoRetryThroughTurn(services.latestTurnSeq.Load())
}

func (services *orderedProviderServices) CaptureAutoRetryThroughTurn(completionSeq uint64) AutoRetryWriter {
	inner := services.ProviderServices.CaptureAutoRetryThroughTurn(completionSeq)
	if ordered, ok := inner.(*orderedAutoRetryWriter); ok && ordered.services.transcript.queue == services.transcript.queue {
		inner = ordered.inner
	}
	return &orderedAutoRetryWriter{services: services, inner: inner}
}

// orderedAutoRetryWriter keeps observation-time authority while every later
// operation still follows the transcript rows in the same output queue.
type orderedAutoRetryWriter struct {
	services *orderedProviderServices
	inner    AutoRetryWriter
}

func (writer *orderedAutoRetryWriter) ScheduleUsageLimitRetry(resetAt time.Time, payload []byte) {
	payload = slices.Clone(payload)
	writer.services.submitEffect("schedule usage-limit retry", func() error {
		writer.inner.ScheduleUsageLimitRetry(resetAt, payload)
		return nil
	})
}

func (writer *orderedAutoRetryWriter) ScheduleUsageLimitRetryWithKey(key string, resetAt time.Time, payload []byte) {
	payload = slices.Clone(payload)
	writer.services.submitEffect("schedule keyed usage-limit retry", func() error {
		writer.inner.ScheduleUsageLimitRetryWithKey(key, resetAt, payload)
		return nil
	})
}

func (writer *orderedAutoRetryWriter) ScheduleServerErrorRetry(payload []byte) {
	payload = slices.Clone(payload)
	writer.services.submitEffect("schedule server-error retry", func() error {
		writer.inner.ScheduleServerErrorRetry(payload)
		return nil
	})
}

func (writer *orderedAutoRetryWriter) CancelUsageLimitRetry(reason string) {
	writer.services.submitEffect("cancel usage-limit retry", func() error {
		writer.inner.CancelUsageLimitRetry(reason)
		return nil
	})
}

func (writer *orderedAutoRetryWriter) CancelUsageLimitRetryWithKey(key, reason string) {
	writer.services.submitEffect("cancel keyed usage-limit retry", func() error {
		writer.inner.CancelUsageLimitRetryWithKey(key, reason)
		return nil
	})
}

func (writer *orderedAutoRetryWriter) CancelServerErrorRetry(reason string) {
	writer.services.submitEffect("cancel server-error retry", func() error {
		writer.inner.CancelServerErrorRetry(reason)
		return nil
	})
}

// ChildSink gives each child a stable queue and retains the child's original writer.
func (services *orderedProviderServices) ChildSink(childAgentID string) ProviderServices {
	child := services.ProviderServices.ChildSink(childAgentID)
	return NewOrderedProviderServices(OutputQueueFor(child), child)
}

func (services *orderedProviderServices) AcquireChildSink(childAgentID string) ChildSinkLease {
	lease := services.ProviderServices.AcquireChildSink(childAgentID)
	if lease.Sink != nil {
		lease.Sink = NewOrderedProviderServices(OutputQueueFor(lease.Sink), lease.Sink)
	}
	return lease
}
func (services *orderedProviderServices) PersistChildMessage(childAgentID string, source leapmuxv1.MessageSource, content []byte, span SpanInfo) error {
	return services.ChildSink(childAgentID).PersistMessage(source, MessageContent{Original: content}, span)
}
func (services *orderedProviderServices) PersistChildTurnEnd(childAgentID string, content MessageContent, span SpanInfo) error {
	return services.ChildSink(childAgentID).PersistTurnEnd(content, span)
}

// ValidateTranscriptOutputTicket checks that a reservation has an ordering queue.
func ValidateTranscriptOutputTicket(ticket *OutputTicket) error {
	if ticket == nil || ticket.Queue() == nil {
		return errors.New("the captured output has no reservation")
	}
	return nil
}

type capturedOutputRefusal struct{ err error }

func (refusal *capturedOutputRefusal) Error() string { return refusal.err.Error() }
func (refusal *capturedOutputRefusal) Unwrap() error { return refusal.err }

// TranscriptOutputPrecedes checks the queue order of two existing reservations.
func TranscriptOutputPrecedes(ticket, cutoff *OutputTicket) bool {
	if ticket == nil || cutoff == nil || ticket == cutoff {
		return false
	}
	queue := ticket.Queue()
	if queue == nil || cutoff.Queue() != queue {
		return false
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	found := false
	for _, pending := range queue.pending {
		if pending == cutoff {
			return found
		}
		if pending == ticket {
			found = true
		}
	}
	return false
}

// TranscriptWritePrecedes requires an active forwarded write before the cutoff.
func TranscriptWritePrecedes(content MessageContent, cutoff *OutputTicket) bool {
	execution := content.outputExecution
	return execution.active() && TranscriptOutputPrecedes(execution.ticket, cutoff)
}
