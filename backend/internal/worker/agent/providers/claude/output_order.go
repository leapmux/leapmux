package claude

import (
	"errors"
	"sync"
	"sync/atomic"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// claudeOutputDecision resolves once. A different transcript can wait for the
// decision without blocking stdout or polling a storage queue.
type claudeOutputDecision struct {
	mu        sync.Mutex
	settled   bool
	accepted  bool
	listeners []func()
}

func (d *claudeOutputDecision) resolve(accepted bool) {
	d.mu.Lock()
	if d.settled {
		d.mu.Unlock()
		return
	}
	d.settled, d.accepted = true, accepted
	listeners := d.listeners
	d.listeners = nil
	d.mu.Unlock()
	for _, listener := range listeners {
		listener()
	}
}

func (d *claudeOutputDecision) result() (accepted, settled bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.accepted, d.settled
}

func (d *claudeOutputDecision) afterResolved(listener func()) {
	d.mu.Lock()
	if !d.settled {
		d.listeners = append(d.listeners, listener)
		d.mu.Unlock()
		return
	}
	d.mu.Unlock()
	listener()
}

func afterClaudeOutputDecisions(decisions map[*claudeOutputDecision]struct{}, ready func()) {
	if len(decisions) == 0 {
		ready()
		return
	}
	var remaining atomic.Int64
	remaining.Store(int64(len(decisions)))
	for decision := range decisions {
		decision.afterResolved(func() {
			if remaining.Add(-1) == 0 {
				ready()
			}
		})
	}
}

// claudeOutputOrder preserves native routing order across transcript queues.
// A task start waits for its captured SendMessage sender. Later frames keep
// their place while the routing decision is pending.
type claudeOutputOrder struct {
	mu      sync.Mutex
	senders map[string]*claudeOutputDecision
	routes  agent.OutputQueue
}

func (output *claudeOutputOrder) captureSender(frame *claudeConversationFrame) func() {
	if frame.msgType != claudeMsgTypeAssistant || frame.envelope.IsAPIErrorMessage {
		return nil
	}
	var calls []string
	for _, block := range frame.envelope.ContentBlocks() {
		if block.Type == "tool_use" && block.Name == ToolNameSendMessage && block.ID != "" {
			calls = append(calls, block.ID)
		}
	}
	if len(calls) == 0 {
		return nil
	}
	decision := &claudeOutputDecision{}
	output.mu.Lock()
	if output.senders == nil {
		output.senders = make(map[string]*claudeOutputDecision)
	}
	for _, call := range calls {
		if output.senders[call] == nil {
			output.senders[call] = decision
		}
	}
	output.mu.Unlock()
	return func() {
		output.mu.Lock()
		for _, call := range calls {
			if output.senders[call] == decision {
				delete(output.senders, call)
			}
		}
		output.mu.Unlock()
		decision.resolve(true)
	}
}

// reserveRoute publishes a routing position before it runs. A pending
// dependency wakes the same position without blocking the process reader.
func (output *claudeOutputOrder) reserveRoute(label string, run func(wake func()) error) func() error {
	var reservation atomic.Pointer[agent.OutputTicket]
	drain := func() error { return output.routes.FlushAfterReady(reservation.Load()) }
	ticket := output.routes.Append(agent.OutputWrite{Label: label, Run: func() error {
		if reservation.Load() == nil {
			return agent.ErrOutputInFlight
		}
		return run(func() { _ = drain() })
	}})[0]
	reservation.Store(ticket)
	return drain
}

func (a *Agent) routeClaudeOutput(effect func()) {
	drain := a.outputOrder.reserveRoute("route Claude output", func(func()) error {
		effect()
		return nil
	})
	_ = drain()
}

type claudeRootSequence func(agent.TranscriptSequence) error

// reserveRoutedClaudeRootOutput admits a Worker position in stdout order.
// Routing can still wait for a different transcript before preparing its work.
func (a *Agent) reserveRoutedClaudeRootOutput(label string, prepare func(func()) claudeRootSequence) (func(), error) {
	var prepared atomic.Pointer[claudeRootSequence]
	drainWrite, err := agent.ReserveTranscriptSequence(a.sink, label, func(sequence agent.TranscriptSequence) error {
		write := prepared.Load()
		if write == nil {
			return agent.ErrOutputInFlight
		}
		if *write == nil {
			return nil
		}
		return (*write)(sequence)
	})
	if err != nil {
		return nil, err
	}
	wake := func() {
		if prepared.Load() != nil {
			a.reportClaudeSequenceError(label, drainWrite())
		}
	}
	drainRoute := a.outputOrder.reserveRoute(label, func(func()) error {
		write := prepare(wake)
		prepared.Store(&write)
		wake()
		return nil
	})
	return func() { _ = drainRoute() }, nil
}

func (a *Agent) routeClaudeRootSequence(label string, prepare func(func()) claudeRootSequence) {
	route, err := a.reserveRoutedClaudeRootOutput(label, prepare)
	if err == nil {
		route()
	}
	a.reportClaudeSequenceError(label, err)
}

type claudeRootMessage struct {
	content      []byte
	source       leapmuxv1.MessageSource
	envelope     *messageEnvelope
	notification bool
	resetSpans   bool
	apiRetry     bool
}

func (a *Agent) prepareClaudeRootMessage(message claudeRootMessage) claudeRootSequence {
	destination := a.rootTranscript()
	content := destination.capture(agent.MessageContent{WriteReceipt: agent.NewTranscriptWriteReceipt()}, agent.SpanInfo{})
	content.Original = message.content
	spans := agent.TranscriptFrameSpans(content, destination.sink)
	if spans == nil {
		return func(agent.TranscriptSequence) error {
			return errors.New("the Claude notification has no retained span authority")
		}
	}
	var operation *agent.CapturedTranscriptOperation
	return func(sequence agent.TranscriptSequence) error {
		if operation == nil {
			if message.resetSpans {
				spans.ResetSpans()
				message.resetSpans = false
			}
			var span agent.SpanInfo
			if env := message.envelope; env != nil {
				parentSpanID := env.ParentToolUseID
				if parentSpanID == "" {
					parentSpanID = env.ToolUseID
				}
				span = claudeSpanInfoFor(spans, claudeMsgTypeSystem, env, parentSpanID)
			}
			captured, err := agent.CaptureTranscriptFrame(destination.sink, content, span)
			if err != nil {
				return err
			}
			var write agent.CapturedTranscriptOperation
			if message.notification {
				write = captured.NotificationOperation(message.source)
			} else {
				write = captured.MessageOperation(message.source)
			}
			operation = &write
		}
		if _, err := sequence.Persist(*operation); err != nil {
			return err
		}
		if message.apiRetry {
			destination.apiRetry.NoteRetry()
		}
		return nil
	}
}

func (a *Agent) routeClaudeRootMessage(label string, message claudeRootMessage) {
	a.routeClaudeRootSequence(label, func(func()) claudeRootSequence {
		return a.prepareClaudeRootMessage(message)
	})
}

func (a *Agent) dispatchClaudeTaskEvent(event *claudeTaskEnvelope) {
	output := &a.outputOrder
	var sender *claudeOutputDecision
	senderCaptured := false
	drain := output.reserveRoute("apply Claude task event", func(wake func()) error {
		if event.Subtype == contracts.ClaudeHiddenSystemSubtypeTaskStarted && !senderCaptured {
			senderCaptured = true
			output.mu.Lock()
			sender = output.senders[event.ToolUseID]
			output.mu.Unlock()
			if sender != nil {
				sender.afterResolved(wake)
			}
		}
		if sender != nil {
			if _, settled := sender.result(); !settled {
				return agent.ErrOutputInFlight
			}
		}
		switch event.Subtype {
		case contracts.ClaudeHiddenSystemSubtypeTaskStarted:
			a.handleClaudeTaskStarted(event)
		case contracts.ClaudeHiddenSystemSubtypeTaskProgress:
			a.handleClaudeTaskProgress(event)
		case contracts.ClaudeHiddenSystemSubtypeTaskNotification:
			a.handleClaudeTaskNotification(event)
		}
		return nil
	})
	_ = drain()
}
