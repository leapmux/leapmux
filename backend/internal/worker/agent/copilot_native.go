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
	// nativeTextSegments names the transcript each accumulating segment belongs to, in
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
	// goalRefreshing keeps one background objective read in flight. The runtime
	// reports a change without its new state, and a burst of changes would
	// otherwise start a request for each one -- all reading the same answer, and
	// finishing in an order the last write cannot be trusted to respect.
	goalRefreshing bool
}

func startNativeCopilot(ctx context.Context, opts Options, sink ProviderServices) (Agent, error) {
	a := &copilotAgent{
		sink: newModelProgressResetSink(sink), opts: opts,
		options: make(optionmap.Map),
	}
	connection, err := startCopilotConnection(ctx, opts, a.handleNativeOutput)
	if err != nil {
		return nil, err
	}
	a.copilotConnection = connection
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

func (a *copilotAgent) currentNativeSessionID() string {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	return a.sessionID
}

// requestNativeSession runs while the caller holds sessionMu.
func (a *copilotAgent) requestNativeSession(method string, values map[string]any) (json.RawMessage, error) {
	return a.requestSession(a.currentNativeSessionID(), method, values, a.APITimeout())
}

func (a *copilotAgent) PublishTurnActive() TurnState {
	a.stateMu.Lock()
	active := a.active
	sequence := a.turnOrder.nextTurnSeq()
	a.stateMu.Unlock()
	return publishTurnStateTo(a.sink, TurnState{Active: active}, sequence)
}

func (a *copilotAgent) setNativeTurnActive(active bool) bool {
	a.stateMu.Lock()
	previous := a.active
	a.active = active
	a.activityRevision++
	sequence := a.turnOrder.nextTurnSeq()
	a.stateMu.Unlock()
	publishTurnStateTo(a.sink, TurnState{Active: active}, sequence)
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
