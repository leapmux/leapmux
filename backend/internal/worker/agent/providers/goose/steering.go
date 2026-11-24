package goose

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

var _ agent.SteerEchoReporter = (*Agent)(nil)

// SteerRowAtEcho keeps a Goose steer in AWAITING_ECHO until Goose emits the
// steered `user_message_chunk` with `_meta.goose.steer == true` when
// SteerOperation drains it into the active run.
func (*Agent) SteerRowAtEcho() bool { return true }

type gooseSteerState struct {
	mu       sync.Mutex
	inFlight *gooseSteerDispatch
	pending  map[string]*gooseSteerDispatch
}

type gooseSteerDispatch struct {
	sessionID  string
	resolution agent.SteerResolution
	early      map[string]*gooseEarlySteer
}

type gooseEarlySteer struct {
	write    func() error
	publish  func() error
	accepted bool
	ready    atomic.Bool
}

func (e *gooseEarlySteer) run(agent.TranscriptSequence) error {
	if !e.ready.Load() {
		return agent.ErrOutputInFlight
	}
	if !e.accepted {
		return nil
	}
	return e.write()
}

func (e *gooseEarlySteer) finish(accepted bool) error {
	e.accepted = accepted
	e.ready.Store(true)
	return e.publish()
}

type gooseSteerReply struct {
	RunID     string `json:"runId"`
	MessageID string `json:"messageId"`
}

func (a *Agent) SteerInput(request agent.SteerRequest) error {
	content, attachments := request.Content, request.Attachments
	method, active, sessionID, runID := a.SteerTarget()
	if method == "" {
		return agent.ErrSteeringUnsupported
	}
	if !active || runID == "" {
		return agent.ErrNoActiveTurn
	}
	params, err := json.Marshal(map[string]any{
		"sessionId":     sessionID,
		"expectedRunId": runID,
		"prompt":        acp.BuildPromptBlocks(content, agent.ClassifyAttachments(attachments)),
	})
	if err != nil {
		return fmt.Errorf("marshal ACP steer params: %w", err)
	}
	if request.InputID == "" || request.AttemptID == "" {
		if _, err := a.SendRequest(method, params, a.APITimeout()); err != nil {
			return mapGooseSteerError(method, err)
		}
		if !a.SteerRunActive(runID) {
			return agent.ErrNoActiveTurn
		}
		return nil
	}

	dispatch := &gooseSteerDispatch{
		sessionID: sessionID,
		resolution: agent.SteerResolution{
			InputID:   request.InputID,
			AttemptID: request.AttemptID,
		},
	}
	a.steers.mu.Lock()
	if a.steers.inFlight != nil {
		a.steers.mu.Unlock()
		return agent.ErrAgentBusy
	}
	a.steers.inFlight = dispatch
	a.steers.mu.Unlock()
	defer a.cancelGooseSteer(dispatch)

	var replyErr error
	_, err = a.SendRequestObserved(method, params, a.APITimeout(), func(raw json.RawMessage, err error) {
		if err == nil {
			replyErr = a.acceptGooseSteerReply(dispatch, raw)
		}
	})
	if err != nil {
		return mapGooseSteerError(method, err)
	}
	if replyErr != nil {
		return replyErr
	}
	if !a.SteerRunActive(runID) {
		if a.discardPendingGooseSteer(dispatch) {
			return agent.ErrNoActiveTurn
		}
		return nil
	}
	return nil
}

func mapGooseSteerError(method string, err error) error {
	if providerkit.HasJSONRPCErrorCode(err, -32601) {
		return agent.ErrSteeringUnsupported
	}
	if providerkit.HasJSONRPCErrorCode(err, -32600, -32602) {
		return agent.ErrNoActiveTurn
	}
	return providerkit.ClassifyJSONRPCDeliveryError(method, err)
}

func (a *Agent) acceptGooseSteerReply(dispatch *gooseSteerDispatch, raw json.RawMessage) error {
	var reply gooseSteerReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		return fmt.Errorf("%w: read the Goose steer reply: %w", agent.ErrDeliveryUncertain, err)
	}
	if reply.MessageID == "" {
		return fmt.Errorf("%w: Goose queued a steer without a message ID", agent.ErrDeliveryUncertain)
	}
	a.steers.mu.Lock()
	if a.steers.inFlight != dispatch {
		a.steers.mu.Unlock()
		return fmt.Errorf("%w: the Goose steer reply arrived after its dispatch ended", agent.ErrDeliveryUncertain)
	}
	if a.steers.pending[reply.MessageID] != nil {
		a.steers.mu.Unlock()
		return fmt.Errorf("%w: Goose reused a pending steer message ID", agent.ErrDeliveryUncertain)
	}
	a.steers.inFlight = nil
	early := dispatch.early
	dispatch.early = nil
	if early[reply.MessageID] == nil {
		if a.steers.pending == nil {
			a.steers.pending = make(map[string]*gooseSteerDispatch)
		}
		a.steers.pending[reply.MessageID] = dispatch
	}
	a.steers.mu.Unlock()

	for id, event := range early {
		a.reportGooseSteerWrite(event.finish(id == reply.MessageID))
	}
	return nil
}

func (a *Agent) cancelGooseSteer(dispatch *gooseSteerDispatch) {
	a.steers.mu.Lock()
	if a.steers.inFlight != dispatch {
		a.steers.mu.Unlock()
		return
	}
	a.steers.inFlight = nil
	early := dispatch.early
	dispatch.early = nil
	a.steers.mu.Unlock()
	for _, event := range early {
		a.reportGooseSteerWrite(event.finish(false))
	}
}

func (a *Agent) discardPendingGooseSteer(dispatch *gooseSteerDispatch) bool {
	a.steers.mu.Lock()
	defer a.steers.mu.Unlock()
	for id, candidate := range a.steers.pending {
		if candidate == dispatch {
			delete(a.steers.pending, id)
			return true
		}
	}
	return false
}

// observeGooseSteerChunk resolves a pending Goose steer when SteerOperation
// emits the steered UserMessageChunk with `_meta.goose.steer == true`.
func (a *Agent) observeGooseSteerChunk(metadata map[string]json.RawMessage, update json.RawMessage) bool {
	var gooseMeta struct {
		MessageID string `json:"messageId"`
		Steer     bool   `json:"steer"`
	}
	if json.Unmarshal(metadata[gooseSteerNamespace], &gooseMeta) != nil || !gooseMeta.Steer {
		return false
	}
	messageID := gooseMeta.MessageID
	if messageID == "" {
		var frame struct {
			MessageID string `json:"messageId"`
		}
		if json.Unmarshal(update, &frame) == nil {
			messageID = frame.MessageID
		}
	}
	if messageID == "" {
		return true
	}
	a.FlushBufferedOutput(agent.MessageCompletionComplete)
	a.resolveGooseSteer(messageID, agent.SteerOutcomeEchoed)
	return true
}

func (a *Agent) resolveGooseSteer(messageID string, outcome agent.SteerOutcome) {
	if messageID == "" {
		return
	}
	sessionID := a.CurrentSessionID()
	a.steers.mu.Lock()
	dispatch := a.steers.pending[messageID]
	if dispatch != nil && dispatch.sessionID != sessionID {
		a.steers.mu.Unlock()
		return
	}
	if dispatch != nil {
		delete(a.steers.pending, messageID)
	}
	inFlight := a.steers.inFlight
	if dispatch == nil && (inFlight == nil || inFlight.sessionID != sessionID || inFlight.early[messageID] != nil) {
		a.steers.mu.Unlock()
		return
	}
	a.steers.mu.Unlock()

	if dispatch != nil {
		a.retainGooseSteerWrite(a.captureGooseSteer(dispatch, messageID, outcome))
		return
	}

	event := &gooseEarlySteer{write: a.captureGooseSteer(inFlight, messageID, outcome)}
	publish, err := agent.ReserveTranscriptSequence(a.Sink(), "record early Goose steer outcome", event.run)
	if err != nil {
		a.reportGooseSteerWrite(err)
		return
	}
	event.publish = publish
	a.steers.mu.Lock()
	if a.steers.inFlight != inFlight {
		a.steers.mu.Unlock()
		a.reportGooseSteerWrite(event.finish(false))
		return
	}
	if inFlight.early == nil {
		inFlight.early = make(map[string]*gooseEarlySteer)
	}
	inFlight.early[messageID] = event
	a.steers.mu.Unlock()
}

func (a *Agent) captureGooseSteer(dispatch *gooseSteerDispatch, messageID string, outcome agent.SteerOutcome) func() error {
	report := dispatch.resolution
	report.Outcome = outcome
	if outcome == agent.SteerOutcomeEchoed {
		return a.CaptureRootSteer(dispatch.sessionID, messageID, report)
	}
	return a.Sink().CaptureSteerResolution(report)
}

func (a *Agent) retainGooseSteerWrite(write func() error) {
	publish, err := agent.ReserveTranscriptSequence(a.Sink(), "record Goose steer outcome", func(agent.TranscriptSequence) error {
		return write()
	})
	if err == nil {
		err = publish()
	}
	a.reportGooseSteerWrite(err)
}

func (a *Agent) dropPendingGooseSteers(outcome agent.SteerOutcome) {
	a.steers.mu.Lock()
	pending := a.steers.pending
	a.steers.pending = nil
	a.steers.mu.Unlock()
	for id, dispatch := range pending {
		a.retainGooseSteerWrite(a.captureGooseSteer(dispatch, id, outcome))
	}
}

func (a *Agent) reportGooseSteerWrite(err error) {
	if err != nil && !errors.Is(err, agent.ErrOutputInFlight) && !agent.OutputRetained(err) {
		slog.Error("record Goose steer outcome", "agent_id", a.AgentID(), "error", err)
	}
}
