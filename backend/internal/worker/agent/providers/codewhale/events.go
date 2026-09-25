package codewhale

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// The runtime states everything a thread does on one server-sent event stream:
// GET /v1/threads/{id}/events. This file owns that stream and the dispatch of
// each event. output.go, control.go, goal.go and usage.go hold the handlers.

// maxEventBytes limits one event. The largest event is a tool item that carries
// its output, which the runtime caps near 50 KB of shell output but not for
// every tool, so the limit is generous: an event larger than this ends the
// connection, and the reconnect resumes after the last event dispatched.
const maxEventBytes = 32 << 20

// Reconnect backoff for a stream that ended while the agent still runs.
const (
	streamRetryFirst = 100 * time.Millisecond
	streamRetryMax   = 5 * time.Second
)

// streamRetryTimerTag labels the reconnect timer for a test's clock trap.
const streamRetryTimerTag = "codewhale-stream-retry"

// codewhaleEnvelope is one runtime event, as the `data` of one server-sent
// event.
//
// `seq` numbers events across EVERY thread of the store, so gaps are normal.
// It still rises within one stream, which is what the dispatch deduplicates on:
// the server subscribes before it replays, so a reconnect can deliver one event
// twice, once from the replay and once live.
type codewhaleEnvelope struct {
	Seq      uint64          `json:"seq"`
	Event    string          `json:"event"`
	ThreadID string          `json:"thread_id"`
	TurnID   string          `json:"turn_id"`
	ItemID   string          `json:"item_id"`
	Payload  json.RawMessage `json:"payload"`
	// raw is the event's own bytes, which is what the worker persists.
	raw []byte
}

// parseEnvelope decodes one event. ok is false for data that is not an event.
func parseEnvelope(data []byte) (codewhaleEnvelope, bool) {
	var env codewhaleEnvelope
	if err := json.Unmarshal(data, &env); err != nil || env.Event == "" {
		return codewhaleEnvelope{}, false
	}
	env.raw = append([]byte(nil), data...)
	return env, true
}

// startEventStream starts the stream goroutine for the agent's thread. It runs
// until ctx ends or the process exits.
func (a *Agent) startEventStream(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	a.streamCancel = cancel
	a.streamDone = make(chan struct{})
	go a.runEventStream(ctx)
}

// runEventStream reads the stream and reconnects it after an end that the agent
// did not ask for. A reconnect asks for the events after the last one
// dispatched, so nothing is lost and nothing is dispatched twice.
func (a *Agent) runEventStream(ctx context.Context) {
	defer close(a.streamDone)
	delay := streamRetryFirst
	for {
		delivered, err := a.readEventStream(ctx)
		if ctx.Err() != nil || a.processExited() {
			return
		}
		if delivered {
			delay = streamRetryFirst
		}
		slog.Debug("codewhale event stream ended; reconnecting", "agent_id", a.AgentID(), "delay", delay, "error", err)
		timer := a.clock.NewTimer(delay, streamRetryTimerTag)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return
		case <-a.ProcessDone():
			timer.Stop()
			return
		}
		delay = min(delay*2, streamRetryMax)
	}
}

// processExited reports whether the process already exited.
func (a *Agent) processExited() bool {
	select {
	case <-a.ProcessDone():
		return true
	default:
		return false
	}
}

// readEventStream reads one connection of the stream until it ends. delivered
// reports whether the connection dispatched any event, which resets the
// reconnect backoff.
func (a *Agent) readEventStream(ctx context.Context) (delivered bool, err error) {
	threadID := a.currentThreadID()
	if threadID == "" {
		return false, errors.New("the agent has no thread to follow")
	}
	a.Mu.Lock()
	since := a.lastSeq
	a.Mu.Unlock()
	response, err := a.endpoint.OpenStreamQuery(ctx, http.MethodGet, threadPath(threadID, threadRouteEvents),
		url.Values{eventsQuerySinceSeq: {strconv.FormatUint(since, 10)}},
		http.Header{"Accept": {"text/event-stream"}})
	if err != nil {
		return false, err
	}
	defer func() { _ = response.Body.Close() }()
	err = providerkit.ReadSSE(response.Body, maxEventBytes, func(event providerkit.SSEEvent) {
		env, ok := parseEnvelope(event.Data)
		if !ok {
			slog.Debug("codewhale event is not an envelope", "agent_id", a.AgentID(), "event", event.Event, "len", len(event.Data))
			return
		}
		delivered = true
		a.dispatchEvent(env)
	})
	return delivered, err
}

// HandleOutput dispatches one event envelope, as the stream would. The stream
// is the production path; this entry point serves tests and out-of-band feeds.
func (a *Agent) HandleOutput(content []byte) {
	env, ok := parseEnvelope(content)
	if !ok {
		slog.Warn("codewhale output is not an event envelope", "agent_id", a.AgentID(), "len", len(content))
		return
	}
	a.dispatchEvent(env)
}

// dispatchEvent routes one event to its handler.
//
// The switch lists every event the worker knows, including the ones it
// ignores, each with its reason. A new event therefore reaches the default
// branch and is logged instead of being absorbed by a branch that looks
// deliberate.
func (a *Agent) dispatchEvent(env codewhaleEnvelope) {
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	if a.IsDiscardingOutput() {
		return
	}
	a.Mu.Lock()
	if env.ThreadID != "" && a.threadID != "" && env.ThreadID != a.threadID {
		a.Mu.Unlock()
		return
	}
	if env.Seq != 0 {
		if env.Seq <= a.lastSeq {
			a.Mu.Unlock()
			return
		}
		a.lastSeq = env.Seq
	}
	a.Mu.Unlock()

	switch env.Event {
	case eventTurnStarted:
		a.handleTurnStarted(env)
	case contracts.CodewhaleEventTurnCompleted:
		a.handleTurnCompleted(env)
	case eventTurnUsage:
		a.handleTurnUsage(env)
	case eventTurnSteered:
		a.handleSteerDelivered(env)
	case contracts.CodewhaleEventTurnSteerDropped:
		a.handleSteerDropped(env)

	case contracts.CodewhaleEventItemStarted:
		a.handleItemStarted(env)
	case eventItemDelta:
		a.handleItemDelta(env)
	case contracts.CodewhaleEventItemCompleted, contracts.CodewhaleEventItemFailed,
		contracts.CodewhaleEventItemInterrupted, contracts.CodewhaleEventItemCanceled:
		a.handleItemFinished(env)

	case contracts.CodewhaleEventApprovalRequired:
		a.handleApprovalRequired(env)
	case eventApprovalDecided:
		a.handleApprovalDecided(env)
	case contracts.CodewhaleEventApprovalTimeout:
		// The runtime denied the call because nobody answered in time. The
		// decision that follows retires the card; this row states why.
		a.persistNotification(env)
	case contracts.CodewhaleEventUserInputRequired:
		a.handleUserInputRequired(env)
	case eventUserInputAnswered:
		a.handleUserInputSettled(env)
	case contracts.CodewhaleEventUserInputCanceled:
		a.handleUserInputSettled(env)

	case eventThreadUpdated:
		a.handleThreadUpdated(env)
	case eventGoalUpdated:
		a.handleGoalUpdated(env)
	case eventGoalCleared:
		a.handleGoalCleared(env)

	case contracts.CodewhaleEventSandboxDenied, contracts.CodewhaleEventStoreFailure:
		a.persistNotification(env)

	case eventThreadStarted, eventThreadForked:
		// The create and resume replies already stated the thread.
	case eventTurnLifecycle:
		// It repeats turn.started for a turn that is already running.
	case eventTurnInterruptRequested:
		// The interrupted turn end that follows states the outcome.
	case eventModelToolsSnapshot:
		// A request-surface diagnostic of the runtime, with no conversation.

	default:
		for _, prefix := range ignoredEventPrefixes {
			if strings.HasPrefix(env.Event, prefix) {
				return
			}
		}
		slog.Debug("codewhale unknown event", "agent_id", a.AgentID(), "event", env.Event)
	}
}
