package agent

import (
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"sync/atomic"

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
	records := make([]*orderedOutputRecord, len(messages))
	writes := make([]OutputWrite, len(messages))
	for i, message := range messages {
		record := &orderedOutputRecord{source: message.Source}
		records[i] = record
		writes[i] = OutputWrite{Label: "persist transcript batch", Run: func() error {
			if !record.ready.Load() {
				return ErrOutputInFlight
			}
			return record.captured.PersistQueuedMessage(record.ticket, record.source)
		}}
	}
	tickets := queue.Append(writes...)
	for i, message := range messages {
		records[i].ticket = tickets[i]
		records[i].captured = CaptureTranscript(DirectTranscript(writer), message.Content, message.Span)
		records[i].ready.Store(true)
	}
	err := queue.Flush()
	if len(tickets) == 0 {
		return err
	}
	if tickets[len(tickets)-1].Done() {
		return nil
	}
	if err == nil {
		err = ErrOutputInFlight
	}
	return &PendingOutputError{Err: err}
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
		receipt.ordered = record
		record.ticket = writer.queue.Append(OutputWrite{Label: "persist ordered transcript", Run: func() error {
			if !record.ready.Load() {
				return ErrOutputInFlight
			}
			broadcast, err := record.captured.persistWithExecution(record.operation, record.source, &outputExecution{ticket: record.ticket, operation: record.operation, source: record.source})
			record.broadcast.Store(broadcast)
			return err
		}})[0]
	}
	receipt.orderedMu.Unlock()
	if record.queue != writer.queue || record.operation != operation || record.source != source {
		return false, errors.New("the ordered transcript operation, source or queue changed")
	}
	if first {
		record.captured = captureTranscriptWithReceipt(writer.TranscriptServices, content, span, receipt)
		record.ready.Store(true)
	}
	err := writer.queue.Flush()
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

func (services *orderedProviderServices) after(label string, effect func()) {
	captured := services.CaptureMessage(MessageContent{}, SpanInfo{})
	err := services.transcript.submit(label, func() error {
		if captured.Publication == nil || captured.Publication.Owner().IsCurrent() {
			effect()
		}
		return nil
	})
	if err != nil && !errors.Is(err, ErrOutputInFlight) {
		slog.Error("retain dependent output effect", "effect", label, "error", err)
	}
}

func (services *orderedProviderServices) SetTurnState(state TurnState, seq uint64) {
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
func (services *orderedProviderServices) ScheduleAutoContinue(schedule AutoContinueSchedule) {
	schedule.SourcePayload = slices.Clone(schedule.SourcePayload)
	services.after("schedule automatic continuation", func() { services.ProviderServices.ScheduleAutoContinue(schedule) })
}
func (services *orderedProviderServices) CancelAutoContinue(reason AutoContinueReason) {
	services.after("cancel automatic continuation", func() { services.ProviderServices.CancelAutoContinue(reason) })
}

// ChildSink gives each child a stable queue and retains the child's original writer.
func (services *orderedProviderServices) ChildSink(childAgentID string) ProviderServices {
	child := services.ProviderServices.ChildSink(childAgentID)
	return NewOrderedProviderServices(OutputQueueFor(child), child)
}
func (services *orderedProviderServices) PersistChildMessage(childAgentID string, source leapmuxv1.MessageSource, content []byte, span SpanInfo) error {
	return services.ChildSink(childAgentID).PersistMessage(source, MessageContent{Original: content}, span)
}
func (services *orderedProviderServices) PersistChildTurnEnd(childAgentID string, content MessageContent, span SpanInfo) error {
	return services.ChildSink(childAgentID).PersistTurnEnd(content, span)
}
