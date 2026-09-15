package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
)

const copilotOptionSessionMode = contracts.CopilotOptionSessionMode

// copilotAgent owns one native connection and its current session.
type copilotAgent struct {
	*copilotConnection
	sink ProviderServices
	opts Options

	sessionMu        sync.RWMutex
	stateMu          sync.Mutex
	sessionID        string
	options          optionmap.Map
	models           []*ModelInfo
	active           bool
	turnOrder        turnSeqSource
	activityRevision uint64

	// outputMu serializes the reader goroutine against every caller that replaces
	// or ends the session. It also guards the three maps below, which only those
	// two paths touch.
	outputMu  sync.Mutex
	closing   bool
	children  map[string]*copilotNativeChild
	openTools map[string]*copilotOpenTool
	// nextNativeToolOrder numbers the open calls, so a turn that ends with several
	// of them closes each one in the order the runtime opened it.
	nextNativeToolOrder uint64
	// nativeText accumulates the assistant text the runtime STREAMS.
	//
	// The runtime marks every delta ephemeral and sends the assistant message only
	// once that message is complete, so a turn cut short stored nothing and the answer
	// the reader watched vanished. See closeStreamedNativeText and CP-015.
	nativeText GenerationBuffer
	// nativeTextSegments identifies the transcript each accumulating segment belongs to, in
	// the order the runtime opened them, so a turn end writes its rows the way the
	// agent produced them.
	nativeTextSegments []copilotStreamedText

	controlMu sync.Mutex
	controls  map[string]*copilotPendingControl

	// interestMu guards the event-log subscription handles of the CURRENT session.
	// The runtime returns a distinct handle for each registration and keeps the
	// subscription alive until its handle is released, so a replacement that
	// forgot them would leave the old session's interests registered for the life
	// of the process. See CP-009.
	interestMu sync.Mutex
	interests  []string

	goalMu sync.Mutex
	goal   copilotGoalSnapshot

	// backgroundReads coalesces the reads offReader starts. It carries its OWN
	// mutex: its bookkeeping has nothing to do with the session snapshot, the
	// option map or the model catalog that stateMu guards, and riding that lock
	// only widened what a reader must hold in mind to order six of them.
	backgroundReads coalescingRunner
}

// The keys of the background reads that offReader keeps apart.
const (
	copilotReadGoal     = "goal"
	copilotReadSettings = "settings"
)

// offReader runs work on its own goroutine, and keeps ONE run of each key in flight.
//
// A dispatch branch that needs a round trip cannot make it inline: the response
// arrives on the reader goroutine that runs the branch, so a direct call would wait
// for itself. The runtime also states that an axis MOVED without stating what it
// became, so a burst of changes would otherwise start a request for each one -- all
// reading the same answer, and finishing in an order the last write cannot be trusted
// to respect.
//
// A request that arrives while a run is in flight therefore does not start a second
// run. It marks the key instead, and one more run follows the current one. That last
// run reads a state no earlier request can precede, so a change the in-flight run
// already passed is never lost.
//
// Each run holds sessionMu for reading, so a session replacement cannot start under
// it, and it does nothing once the process is stopped.
func (a *copilotAgent) offReader(key string, work func()) {
	a.backgroundReads.run(key, func() { a.runUnderNativeSession(work) })
}

// coalescingRunner runs one goroutine for each key, and collapses every request
// that arrives while that key's run is in flight into ONE follow-up run.
//
// It is a general mechanism, so it owns its own mutex rather than riding a lock
// that guards unrelated state. The follow-up is what makes the last run read a
// state no pending request can precede.
type coalescingRunner struct {
	mu sync.Mutex
	// pending maps a key with a run in flight to whether another request arrived
	// during it. Absence means no run is in flight for that key.
	pending map[string]bool
}

// idle reports that no run is in flight for any key.
func (r *coalescingRunner) idle() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pending) == 0
}

// inFlight reports the run of each key, and whether one more request arrived
// during it. It copies the map, so a caller never reads the runner's own state
// without the lock.
func (r *coalescingRunner) inFlight() map[string]bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]bool, len(r.pending))
	for key, queued := range r.pending {
		out[key] = queued
	}
	return out
}

// run starts work for key, or marks the in-flight run to repeat once more.
func (r *coalescingRunner) run(key string, work func()) {
	r.mu.Lock()
	if _, running := r.pending[key]; running {
		r.pending[key] = true
		r.mu.Unlock()
		return
	}
	if r.pending == nil {
		r.pending = make(map[string]bool)
	}
	r.pending[key] = false
	r.mu.Unlock()
	go func() {
		for {
			work()
			r.mu.Lock()
			if !r.pending[key] {
				delete(r.pending, key)
				r.mu.Unlock()
				return
			}
			r.pending[key] = false
			r.mu.Unlock()
		}
	}()
}

// runUnderNativeSession runs work while the session stays in place. A stopped process
// answers nothing, so the work does not start.
func (a *copilotAgent) runUnderNativeSession(work func()) {
	a.sessionMu.RLock()
	defer a.sessionMu.RUnlock()
	if a.IsStopped() {
		return
	}
	work()
}

func startNativeCopilot(ctx context.Context, opts Options, sink ProviderServices) (Agent, error) {
	a := &copilotAgent{
		sink: newModelProgressResetSink(sink), opts: opts,
		options: make(optionmap.Map),
	}
	connection, err := startCopilotConnection(ctx, opts)
	if err != nil {
		return nil, err
	}
	// Adopt the connection BEFORE the reader starts. The reader reaches this agent
	// through the embedded pointer, so a frame that arrived first would read a nil one.
	a.copilotConnection = connection
	connection.startReading(a.handleNativeOutput)
	if err := connection.verifyNativeProtocol(opts); err != nil {
		return nil, err
	}
	cleanup := func(err error) (Agent, error) {
		a.outputMu.Lock()
		a.closing = true
		a.outputMu.Unlock()
		a.releaseNativeControlEvents()
		connection.Stop()
		_ = connection.Wait()
		a.outputMu.Lock()
		a.clearNativeChildren()
		a.clearNativeControls()
		a.outputMu.Unlock()
		return nil, err
	}
	id := opts.ResumeSessionID
	if id == "" {
		generated, err := uuid.NewRandom()
		if err != nil {
			return cleanup(fmt.Errorf("create Copilot session ID: %w", err))
		}
		id = generated.String()
	}
	a.stateMu.Lock()
	a.sessionID = id
	a.stateMu.Unlock()
	if _, err := connection.openSession(opts, id, opts.ResumeSessionID != "", opts.startupTimeout()); err != nil {
		return cleanup(connection.formatStartupError("native session initialization", err))
	}
	a.sink.UpdateSessionID(id)
	// Startup subscribes and then READS the runtime's own settings, because a new
	// process states what it starts with. A session that opens AGAIN takes the same
	// two steps through prepareNativeSession, which RESTORES the stored values
	// instead: the session it replaces already settled them.
	if err := a.registerNativeControlEvents(); err != nil {
		return cleanup(err)
	}
	modelData, err := connection.requestSession(id, "model.list", nil, opts.startupTimeout())
	if err != nil {
		return cleanup(fmt.Errorf("read Copilot models: %w", err))
	}
	models, err := parseCopilotModels(modelData)
	if err != nil {
		return cleanup(err)
	}
	a.stateMu.Lock()
	a.models = append([]*ModelInfo{accountDefaultModelEntry("Use the model that Copilot selects for this account.")}, models...)
	a.stateMu.Unlock()
	if err := a.refreshNativeSettings(); err != nil {
		return cleanup(err)
	}
	startupSettings := optionmap.Map{}
	for _, key := range []string{copilotOptionSessionMode, OptionIDPermissionMode} {
		if value := opts.Get(key); value != "" {
			startupSettings[key] = value
		}
	}
	if len(startupSettings) > 0 {
		applied := a.UpdateSettings(startupSettings)
		for key := range startupSettings {
			if applied.Settlements[key].State != OptionSettlementConfirmed {
				return cleanup(fmt.Errorf("the Copilot runtime did not confirm the requested %s setting", key))
			}
		}
	}
	// A resumed session can already hold an objective. Read it before the first
	// broadcast so the goal card opens on the stored objective rather than empty.
	a.refreshNativeGoal(opts.ResumeSessionID != "")
	a.sink.BroadcastStatusActive(id)
	return a, nil
}

// forgetNativeSessionState drops everything the outgoing session owns, and gives the
// agent nextSessionID. An empty nextSessionID keeps the current identity, which is
// what the goal clear needs: it opens the SAME session again.
//
// A turn the replacement inherits would latch the agent busy for good, because no idle
// event can reach a session that no longer exists. A child transcript, an open tool
// call and a pending control request belong to that session too, and its event
// subscriptions die with it.
//
// The caller holds sessionMu for writing, so no input and no setting change can reach
// the session while this runs.
func (a *copilotAgent) forgetNativeSessionState(nextSessionID string) {
	a.setNativeTurnActive(false)
	a.outputMu.Lock()
	// Store and drop what the OUTGOING session produced before the identity moves, so
	// every row this sweep writes carries the session that produced it.
	a.clearNativeChildren()
	a.clearNativeControls()
	if nextSessionID != "" {
		a.stateMu.Lock()
		a.sessionID = nextSessionID
		a.stateMu.Unlock()
	}
	a.outputMu.Unlock()
	a.forgetNativeControlEvents()
}

// prepareNativeSession subscribes an open session to the control events and restores
// the settings the previous session carried.
//
// Every path that opens a session again needs both, in this order: the context clear,
// its own rollback, and the goal clear. The subscription comes first because a setting
// change can raise a control request, and a request that arrives before the
// subscription exists reaches no reader.
func (a *copilotAgent) prepareNativeSession(options optionmap.Map) error {
	if err := a.registerNativeControlEvents(); err != nil {
		return err
	}
	return a.restoreNativeSettings(options)
}

// stopNativeConnection ends the process and drops the state of the session it served.
//
// A path that disposed of its session and could not open another one has nothing to
// roll back to. An agent that kept its process alive there would accept input that can
// never arrive anywhere.
func (a *copilotAgent) stopNativeConnection() {
	a.outputMu.Lock()
	a.closing = true
	a.clearNativeChildren()
	a.clearNativeControls()
	a.outputMu.Unlock()
	a.forgetNativeControlEvents()
	a.copilotConnection.Stop()
}

func (a *copilotAgent) currentNativeSessionID() string {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	return a.sessionID
}

// requestNativeSession runs while the caller holds sessionMu.
func (a *copilotAgent) requestNativeSession(method string, values map[string]any) (json.RawMessage, error) {
	return a.requestSession(a.currentNativeSessionID(), method, values, a.APITimeout())
}

// PublishTurnActive republishes the Worker-visible turn state from a.active.
//
// Every active turn is STEERABLE: Copilot's session.send accepts
// mode:"immediate" while a turn runs (see SteerInput), so the input queue may
// offer Steer for any active turn.
func (a *copilotAgent) PublishTurnActive() TurnState {
	a.stateMu.Lock()
	active := a.active
	sequence := a.turnOrder.nextTurnSeq()
	a.stateMu.Unlock()
	return publishTurnStateTo(a.sink, TurnState{Active: active, Steerable: active}, sequence)
}

func (a *copilotAgent) setNativeTurnActive(active bool) bool {
	a.stateMu.Lock()
	previous := a.active
	a.active = active
	a.activityRevision++
	sequence := a.turnOrder.nextTurnSeq()
	a.stateMu.Unlock()
	publishTurnStateTo(a.sink, TurnState{Active: active, Steerable: active}, sequence)
	return previous
}

func (a *copilotAgent) rejectNativeInput(revision uint64) {
	a.stateMu.Lock()
	if revision != a.activityRevision {
		a.stateMu.Unlock()
		return
	}
	a.active = false
	sequence := a.turnOrder.nextTurnSeq()
	a.stateMu.Unlock()
	publishTurnStateTo(a.sink, TurnState{}, sequence)
}

func (a *copilotAgent) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInputForSession(nil, content, attachments)
}

func (a *copilotAgent) sendInputForSession(expected *string, content string, attachments []*leapmuxv1.Attachment) error {
	a.sessionMu.RLock()
	defer a.sessionMu.RUnlock()
	if a.IsStopped() {
		return fmt.Errorf("the Copilot process is stopped")
	}
	a.stateMu.Lock()
	if err := checkInputSession(expected, a.sessionID); err != nil {
		a.stateMu.Unlock()
		return err
	}
	if a.active {
		a.stateMu.Unlock()
		return ErrAgentBusy
	}
	if a.sessionID == "" {
		a.stateMu.Unlock()
		return fmt.Errorf("the Copilot session is unavailable")
	}
	a.active = true
	a.activityRevision++
	revision := a.activityRevision
	a.stateMu.Unlock()
	a.PublishTurnActive()
	values := map[string]any{"prompt": content}
	if len(attachments) > 0 {
		blobs := make([]map[string]string, 0, len(attachments))
		for _, attachment := range classifyAttachments(attachments) {
			blobs = append(blobs, map[string]string{
				"type": "blob", "displayName": attachment.filename, "mimeType": attachment.mimeType,
				"data": base64.StdEncoding.EncodeToString(attachment.data),
			})
		}
		values["attachments"] = blobs
	}
	raw, err := a.requestNativeSession("send", values)
	if err != nil {
		var rejection *jsonRPCResponseError
		if errors.As(err, &rejection) {
			a.rejectNativeInput(revision)
		}
		return classifyJSONRPCDeliveryError("session.send", err)
	}
	var response struct {
		MessageID string `json:"messageId"`
	}
	if json.Unmarshal(raw, &response) != nil || response.MessageID == "" {
		return fmt.Errorf("%w: Copilot omitted the delivered message ID", ErrDeliveryUncertain)
	}
	return nil
}

// SupportsSteering always reports true. Copilot's session.send takes a SendMode,
// and mode:"immediate" interjects the message during an in-progress turn, so the
// capability needs no handshake discovery.
func (a *copilotAgent) SupportsSteering() bool { return true }

// SteerInput injects a user message into the RUNNING turn with Copilot's
// SendMode "immediate".
//
// The turn's activity bookkeeping is NOT touched: the turn this steers is the
// one already recorded active, and a rejected steer must not read as the turn
// ending. Contrast sendInputForSession, which owns the idle->active transition
// and therefore owns the revision it can roll back.
func (a *copilotAgent) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	a.sessionMu.RLock()
	defer a.sessionMu.RUnlock()
	if a.IsStopped() {
		return fmt.Errorf("agent is stopped")
	}
	a.stateMu.Lock()
	if a.sessionID == "" {
		a.stateMu.Unlock()
		return fmt.Errorf("the Copilot session is unavailable")
	}
	active := a.active
	a.stateMu.Unlock()
	if !active {
		return ErrNoActiveTurn
	}
	values := map[string]any{"prompt": content, "mode": "immediate"}
	if len(attachments) > 0 {
		blobs := make([]map[string]string, 0, len(attachments))
		for _, attachment := range classifyAttachments(attachments) {
			blobs = append(blobs, map[string]string{
				"type": "blob", "displayName": attachment.filename, "mimeType": attachment.mimeType,
				"data": base64.StdEncoding.EncodeToString(attachment.data),
			})
		}
		values["attachments"] = blobs
	}
	raw, err := a.requestNativeSession("send", values)
	if err != nil {
		return classifyJSONRPCDeliveryError("session.send", err)
	}
	var response struct {
		MessageID string `json:"messageId"`
	}
	if json.Unmarshal(raw, &response) != nil || response.MessageID == "" {
		return fmt.Errorf("%w: Copilot omitted the delivered message ID", ErrDeliveryUncertain)
	}
	return nil
}

// Interrupt aborts the running turn with Copilot's own `session.abort`.
//
// A stopped agent REFUSES, the way every other provider in the roster refuses.
// The worker's InterruptAgent handler reads that refusal as "not running" and
// says so; a nil here reported a stop that never happened, and the handler then
// withdrew the prompts of a process that had already gone.
func (a *copilotAgent) Interrupt() error {
	a.sessionMu.RLock()
	defer a.sessionMu.RUnlock()
	if a.IsStopped() {
		return fmt.Errorf("agent is stopped")
	}
	_, err := a.requestNativeSession("abort", nil)
	return err
}

func (a *copilotAgent) Stop() {
	a.noteIntentionalStop()
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()
	a.outputMu.Lock()
	a.closing = true
	a.outputMu.Unlock()
	if !a.IsStopped() && a.currentNativeSessionID() != "" {
		// Release the subscriptions before the suspend, while the session still
		// accepts session work. A suspended session accepts a release as well
		// (CP-009), but the order that needs no exception is the simpler rule.
		a.releaseNativeControlEvents()
		params, err := json.Marshal(map[string]string{"sessionId": a.currentNativeSessionID()})
		if err == nil {
			_, err = a.sendRequest("session.suspend", params, 2*time.Second)
		}
		if err != nil {
			slog.Debug("Suspend Copilot before process exit", "agent_id", a.agentID, "error", err)
		}
	}
	a.copilotConnection.Stop()
	a.outputMu.Lock()
	a.clearNativeChildren()
	a.clearNativeControls()
	a.outputMu.Unlock()
	a.setNativeTurnActive(false)
}

func (a *copilotAgent) Wait() error {
	err := a.copilotConnection.Wait()
	a.outputMu.Lock()
	a.closing = true
	a.clearNativeChildren()
	a.clearNativeControls()
	a.outputMu.Unlock()
	a.forgetNativeControlEvents()
	a.setNativeTurnActive(false)
	return err
}

func (a *copilotAgent) HandleOutput(content []byte) {
	line := &parsedLine{Raw: content}
	if err := json.Unmarshal(content, line); err != nil {
		a.persistNativeFrame(content, SpanInfo{})
		return
	}
	a.handleNativeOutput(line)
}

func (a *copilotAgent) handleNativeOutput(line *parsedLine) {
	a.outputMu.Lock()
	defer a.outputMu.Unlock()
	if line.Method != copilotMethodSessionEvent {
		a.refuseUnsupportedRequest(line)
		// An unrecognized method still reaches the transcript: a frame that carries
		// conversation is worse lost than shown as raw JSON.
		if !copilotMethodIsTelemetry(line.Method) {
			a.persistNativeFrame(line.Raw, SpanInfo{})
		}
		return
	}
	event, err := decodeCopilotSessionEvent(line.Params, a.currentNativeSessionID())
	if err != nil {
		slog.Debug("Skip Copilot event with an invalid session", "error", err)
		return
	}
	a.handleNativeEvent(line.Raw, event)
}

func (a *copilotAgent) persistNativeFrame(raw []byte, span SpanInfo) {
	a.persistNativeFrameTo(a.sink, raw, span)
}
