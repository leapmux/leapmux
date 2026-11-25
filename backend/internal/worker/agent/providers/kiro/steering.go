package kiro

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

type kiroSteerState struct {
	mu       sync.Mutex
	inFlight *kiroSteerDispatch
	pending  map[string]*kiroSteerDispatch
}

type kiroSteerDispatch struct {
	sessionID  string
	resolution agent.SteerResolution
	early      map[string]*kiroEarlySteer
}

type kiroEarlySteer struct {
	write    func() error
	publish  func() error
	accepted bool
	ready    atomic.Bool
}

func (e *kiroEarlySteer) run(agent.TranscriptSequence) error {
	if !e.ready.Load() {
		return agent.ErrOutputInFlight
	}
	if !e.accepted {
		return nil
	}
	return e.write()
}

func (e *kiroEarlySteer) finish(accepted bool) error {
	e.accepted = accepted
	e.ready.Store(true)
	return e.publish()
}

// kiroSteerReply is Kiro's answer to a steer.
type kiroSteerReply struct {
	Queued    *bool  `json:"queued"`
	MessageID string `json:"messageId"`
	Dropped   string `json:"dropped"`
}

// SteerInput sends text into the running turn. Kiro queues it and injects it
// before its next model call. If that turn ended, the Worker keeps the input
// for the next prompt. Kiro accepts text alone through this route.
func (a *Agent) SteerInput(request agent.SteerRequest) (agent.NativeInputReceipt, error) {
	if err := providerkit.TextOnlySteer("Kiro", request.Attachments); err != nil {
		return agent.NativeInputReceipt{}, err
	}
	err := a.WithSessionID(func(sessionID string) error {
		if !a.PromptActive() {
			return agent.ErrNoActiveTurn
		}
		if request.InputID == "" {
			return errors.New("a Kiro steer requires an input ID")
		}
		params, err := json.Marshal(map[string]string{"sessionId": sessionID, "message": request.Content})
		if err != nil {
			return fmt.Errorf("marshal the Kiro steer: %w", err)
		}
		// The Worker writes a control reply at dispatch. That reply has no
		// echo reservation to resolve when Kiro repeats the text.
		if request.AttemptID == "" {
			raw, err := a.SendRequest(kiroSteerMethod, params, a.APITimeout())
			if err != nil {
				return providerkit.ClassifyJSONRPCDeliveryError(kiroSteerMethod, err)
			}
			_, err = readKiroSteerReply(raw)
			return err
		}
		dispatch := &kiroSteerDispatch{
			sessionID: sessionID,
			resolution: agent.SteerResolution{
				InputID: request.InputID, AttemptID: request.AttemptID,
			},
		}
		a.steers.mu.Lock()
		if a.steers.inFlight != nil {
			a.steers.mu.Unlock()
			return agent.ErrAgentBusy
		}
		a.steers.inFlight = dispatch
		a.steers.mu.Unlock()
		defer a.cancelKiroSteer(dispatch)

		var replyErr error
		_, err = a.SendRequestObserved(kiroSteerMethod, params, a.APITimeout(), func(raw json.RawMessage, err error) {
			if err == nil {
				replyErr = a.acceptKiroSteerReply(dispatch, raw)
			}
		})
		if err != nil {
			return providerkit.ClassifyJSONRPCDeliveryError(kiroSteerMethod, err)
		}
		return replyErr
	})
	return agent.NativeInputReceipt{}, err
}

func readKiroSteerReply(raw json.RawMessage) (kiroSteerReply, error) {
	var reply kiroSteerReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		return reply, fmt.Errorf("%w: read the Kiro steer reply: %w", agent.ErrDeliveryUncertain, err)
	}
	if reply.Queued == nil {
		return reply, fmt.Errorf("%w: the Kiro steer reply omitted its queue decision", agent.ErrDeliveryUncertain)
	}
	if !*reply.Queued {
		return reply, fmt.Errorf("%w: Kiro dropped the steer (%s)", agent.ErrNoActiveTurn, reply.Dropped)
	}
	return reply, nil
}

func (a *Agent) acceptKiroSteerReply(dispatch *kiroSteerDispatch, raw json.RawMessage) error {
	reply, err := readKiroSteerReply(raw)
	if err != nil {
		return err
	}
	if reply.MessageID == "" {
		return fmt.Errorf("%w: Kiro queued a steer without a message ID", agent.ErrDeliveryUncertain)
	}

	a.steers.mu.Lock()
	if a.steers.inFlight != dispatch {
		a.steers.mu.Unlock()
		return fmt.Errorf("%w: the Kiro steer reply arrived after its dispatch ended", agent.ErrDeliveryUncertain)
	}
	if a.steers.pending[reply.MessageID] != nil {
		a.steers.mu.Unlock()
		return fmt.Errorf("%w: Kiro reused a pending steer message ID", agent.ErrDeliveryUncertain)
	}
	a.steers.inFlight = nil
	early := dispatch.early
	dispatch.early = nil
	if early[reply.MessageID] == nil {
		if a.steers.pending == nil {
			a.steers.pending = make(map[string]*kiroSteerDispatch)
		}
		a.steers.pending[reply.MessageID] = dispatch
	}
	a.steers.mu.Unlock()

	for id, event := range early {
		a.reportKiroSteerWrite(event.finish(id == reply.MessageID))
	}
	return nil
}

func (a *Agent) cancelKiroSteer(dispatch *kiroSteerDispatch) {
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
		a.reportKiroSteerWrite(event.finish(false))
	}
}

// handleSteeringInjected runs on the reader at Kiro's model input boundary.
// Reserve the row there even if the RPC reply has yet to give us the ID.
func (a *Agent) handleSteeringInjected(messageID string) {
	if messageID == "" {
		return
	}
	a.FlushBufferedOutput(agent.MessageCompletionComplete)
	a.resolveKiroSteer(messageID, agent.SteerOutcomeEchoed)
}

func (a *Agent) handleSteeringCleared(raw json.RawMessage) {
	var cleared []string
	if json.Unmarshal(raw, &cleared) != nil {
		return
	}
	for _, id := range cleared {
		a.resolveKiroSteer(id, agent.SteerOutcomeDropped)
	}
}

func (a *Agent) resolveKiroSteer(messageID string, outcome agent.SteerOutcome) {
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
		write := a.captureKiroSteer(dispatch, messageID, outcome)
		a.retainKiroSteerWrite(write)
		return
	}

	event := &kiroEarlySteer{write: a.captureKiroSteer(inFlight, messageID, outcome)}
	publish, err := agent.ReserveTranscriptSequence(a.Sink(), "record early Kiro steer outcome", event.run)
	if err != nil {
		a.reportKiroSteerWrite(err)
		return
	}
	event.publish = publish
	a.steers.mu.Lock()
	if a.steers.inFlight != inFlight {
		a.steers.mu.Unlock()
		a.reportKiroSteerWrite(event.finish(false))
		return
	}
	if inFlight.early == nil {
		inFlight.early = make(map[string]*kiroEarlySteer)
	}
	inFlight.early[messageID] = event
	a.steers.mu.Unlock()
}

func (a *Agent) captureKiroSteer(dispatch *kiroSteerDispatch, messageID string, outcome agent.SteerOutcome) func() error {
	report := dispatch.resolution
	report.Outcome = outcome
	if outcome == agent.SteerOutcomeEchoed {
		return a.CaptureRootSteer(dispatch.sessionID, messageID, report)
	}
	return a.Sink().CaptureSteerResolution(report)
}

func (a *Agent) retainKiroSteerWrite(write func() error) {
	publish, err := agent.ReserveTranscriptSequence(a.Sink(), "record Kiro steer outcome", func(agent.TranscriptSequence) error {
		return write()
	})
	if err == nil {
		err = publish()
	}
	a.reportKiroSteerWrite(err)
}

func (a *Agent) dropPendingSteers() {
	a.steers.mu.Lock()
	pending := a.steers.pending
	a.steers.pending = nil
	a.steers.mu.Unlock()
	for id, dispatch := range pending {
		a.retainKiroSteerWrite(a.captureKiroSteer(dispatch, id, agent.SteerOutcomeDropped))
	}
}

func (a *Agent) reportKiroSteerWrite(err error) {
	if err != nil && !agent.OutputRetained(err) {
		slog.Error("record Kiro steer outcome", "agent_id", a.AgentID(), "error", err)
	}
}
