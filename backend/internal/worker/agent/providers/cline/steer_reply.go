package cline

import (
	"encoding/json"
	"log/slog"
	"slices"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

type nativePendingPrompt struct {
	ID         string   `json:"id"`
	Prompt     string   `json:"prompt"`
	Delivery   string   `json:"delivery"`
	UserImages []string `json:"userImages"`
	UserFiles  []string `json:"userFiles"`
}

func (prompt nativePendingPrompt) matches(input clineInput) bool {
	return prompt.Delivery == deliverySteer && prompt.Prompt == input.prompt &&
		slices.Equal(prompt.UserImages, input.userImages) && slices.Equal(prompt.UserFiles, input.userFiles)
}

type steerDispatch struct {
	input         clineInput
	resolution    agent.SteerResolution
	admissionDone chan struct{}
	// admitted records the command's paired queue event. It can appear before
	// the command reply, even if the socket loses that reply.
	admitted bool
}

func (dispatch *steerDispatch) finishAdmission() {
	if dispatch.admissionDone != nil {
		close(dispatch.admissionDone)
		dispatch.admissionDone = nil
	}
}

type steerEventPair struct {
	sequence int64
	promptID string
	dispatch *steerDispatch
}

// steerProofs tracks the private local Hub's synchronous queue changes. The
// send handler emits its request ID before the local host enqueues and emits
// the queue. The queue controller emits removal before submission. Both
// pairs carry consecutive durable event sequences. Agent.Mu protects this
// state. See Cline cli-v3.0.74's run-handlers.ts and pending-prompt-service.ts.
type steerProofs struct {
	writes       map[string]*steerDispatch
	admission    *steerEventPair
	pending      map[string]*steerDispatch
	submission   *steerEventPair
	queue        map[string]struct{}
	lastSequence int64
}

func (proofs *steerProofs) register(dispatch *steerDispatch) {
	if proofs.writes == nil {
		proofs.writes = make(map[string]*steerDispatch)
	}
	proofs.writes[dispatch.resolution.Receipt.InputID] = dispatch
}

func (proofs *steerProofs) forget(dispatch *steerDispatch) {
	delete(proofs.writes, dispatch.resolution.Receipt.InputID)
	if proofs.admission != nil && proofs.admission.dispatch == dispatch {
		proofs.admission = nil
	}
}

type steerObservation struct {
	ownStart     bool
	uncertainRun bool
	sequenceGap  bool
	resolutions  []agent.SteerResolution
}

func (observation *steerObservation) uncertain(dispatch *steerDispatch) {
	dispatch.finishAdmission()
	resolution := dispatch.resolution
	resolution.Outcome = agent.SteerOutcomeUncertain
	resolution.Receipt = agent.NativeInputReceipt{}
	observation.resolutions = append(observation.resolutions, resolution)
}

func consecutiveSteerEvents(earlier, later int64) bool {
	return earlier > 0 && later > earlier && later-earlier == 1
}

func (proofs *steerProofs) observe(event hubEvent, clientID string) (observation steerObservation) {
	if last := proofs.lastSequence; last > 0 && !consecutiveSteerEvents(last, event.Sequence) {
		// The log sequence is global. A gap can hide a queue mutation,
		// including a second enqueue that kept an existing native ID.
		proofs.discardObserved(&observation)
		observation.sequenceGap = true
	}
	proofs.lastSequence = event.Sequence
	if pair := proofs.admission; pair != nil && (event.Event != eventPendingPrompts || !consecutiveSteerEvents(pair.sequence, event.Sequence)) {
		observation.uncertain(pair.dispatch)
		observation.uncertainRun = true
		proofs.admission = nil
	}
	if pair := proofs.submission; pair != nil && (event.Event != eventPendingPromptSubmitted || !consecutiveSteerEvents(pair.sequence, event.Sequence)) {
		observation.uncertain(pair.dispatch)
		proofs.submission = nil
	}
	switch event.Event {
	case eventRunStarted:
		var start struct {
			RequestID string `json:"requestId"`
			ClientID  string `json:"clientId"`
		}
		if json.Unmarshal(event.Payload, &start) != nil {
			return observation
		}
		if own := proofs.writes[start.RequestID]; own != nil && own.resolution.Receipt.SessionID == event.SessionID {
			delete(proofs.writes, start.RequestID)
			if event.Sequence > 0 && clientID != "" && start.ClientID == clientID {
				proofs.admission = &steerEventPair{sequence: event.Sequence, dispatch: own}
				observation.ownStart = true
			} else {
				observation.uncertain(own)
			}
		}
	case eventPendingPrompts:
		proofs.observeQueue(event, &observation)
	case eventPendingPromptSubmitted:
		if pair := proofs.submission; pair != nil {
			proofs.submission = nil
			var submitted struct {
				Prompt nativePendingPrompt `json:"prompt"`
			}
			if json.Unmarshal(event.Payload, &submitted) != nil || submitted.Prompt.ID != pair.promptID || !submitted.Prompt.matches(pair.dispatch.input) {
				observation.uncertain(pair.dispatch)
			} else {
				resolution := pair.dispatch.resolution
				resolution.Outcome = agent.SteerOutcomeEchoed
				observation.resolutions = append(observation.resolutions, resolution)
			}
		}
	}
	return observation
}

func (proofs *steerProofs) observeQueue(event hubEvent, observation *steerObservation) {
	var payload struct {
		Prompts []nativePendingPrompt `json:"prompts"`
	}
	admission := proofs.admission
	proofs.admission = nil
	valid := json.Unmarshal(event.Payload, &payload) == nil && payload.Prompts != nil && event.Sequence > 0
	next := make(map[string]struct{}, len(payload.Prompts))
	matched := make(map[string]bool, len(proofs.pending))
	added := 0
	for _, prompt := range payload.Prompts {
		if _, repeated := next[prompt.ID]; prompt.ID == "" || repeated {
			valid = false
		}
		if _, seen := proofs.queue[prompt.ID]; !seen {
			added++
		}
		next[prompt.ID] = struct{}{}
		if pending := proofs.pending[prompt.ID]; pending != nil {
			matched[prompt.ID] = prompt.matches(pending.input)
		}
	}
	removed := len(proofs.queue) + added - len(next)
	var owner *steerDispatch
	var ownerID string
	if admission != nil {
		if valid && removed == 0 && len(payload.Prompts) > 0 && payload.Prompts[0].matches(admission.dispatch.input) {
			owner, ownerID = admission.dispatch, payload.Prompts[0].ID
			owner.admitted = true
			owner.finishAdmission()
		} else {
			observation.uncertain(admission.dispatch)
			observation.uncertainRun = true
		}
	}
	for promptID, pending := range proofs.pending {
		_, present := next[promptID]
		if valid && admission == nil && removed == 1 && added == 0 && !present {
			proofs.submission = &steerEventPair{sequence: event.Sequence, promptID: promptID, dispatch: pending}
			delete(proofs.pending, promptID)
			continue
		}
		// A single insertion or removal preserves other entries. An
		// unpaired update can merge identical text into an existing ID.
		knownChange := owner != nil || admission == nil && added+removed == 1
		if !valid || !knownChange || promptID == ownerID || !matched[promptID] {
			observation.uncertain(pending)
			delete(proofs.pending, promptID)
		}
	}
	if owner != nil {
		if proofs.pending == nil {
			proofs.pending = make(map[string]*steerDispatch)
		}
		proofs.pending[ownerID] = owner
	}
	if valid {
		proofs.queue = next
	} else {
		proofs.queue = nil
	}
}

func (proofs *steerProofs) reset() []agent.SteerResolution {
	var observation steerObservation
	for _, pending := range proofs.writes {
		observation.uncertain(pending)
	}
	proofs.discardObserved(&observation)
	*proofs = steerProofs{}
	return observation.resolutions
}

func (proofs *steerProofs) discardObserved(observation *steerObservation) {
	for _, pending := range proofs.pending {
		observation.uncertain(pending)
	}
	if proofs.admission != nil {
		observation.uncertain(proofs.admission.dispatch)
		observation.uncertainRun = true
	}
	if proofs.submission != nil {
		observation.uncertain(proofs.submission.dispatch)
	}
	proofs.admission, proofs.submission, proofs.pending, proofs.queue = nil, nil, nil, nil
}

func (a *Agent) observeSteerEvent(event hubEvent) bool {
	if event.SessionID == "" {
		return false
	}
	a.Mu.Lock()
	observation := a.steerProofs.observe(event, a.clientID)
	reply := a.turn.inputReply
	if reply != nil && (observation.uncertainRun || (observation.sequenceGap && (reply.autonomous || reply.agentDone))) {
		reply.ambiguous = true
	}
	a.Mu.Unlock()
	for i := range observation.resolutions {
		resolution := &observation.resolutions[i]
		if resolution.Outcome == agent.SteerOutcomeEchoed {
			a.bindSubmittedSteer(&resolution.Receipt)
		}
	}
	a.retainSteerResolutions(observation.resolutions)
	return observation.ownStart
}

// bindSubmittedSteer uses the session status to distinguish a steer read by
// the current run from a prompt that the native queue drained into a new run.
// The caller holds dispatchMu.
func (a *Agent) bindSubmittedSteer(receipt *agent.NativeInputReceipt) {
	a.Mu.Lock()
	if a.StoppedLocked() || a.ctx.Err() != nil || a.turn.settling {
		a.Mu.Unlock()
		return
	}
	reply := a.turn.inputReply
	if a.turn.active && !a.turn.leadIdle {
		if reply.ownsOutput(receipt.SessionID) {
			if reply.agentDone || reply.end != nil {
				reply.ambiguous = true
			} else {
				receipt.TurnID = reply.receipt.TurnID
				if !reply.autonomous {
					reply.steers = append(reply.steers, agent.NativeReplyBoundary{
						InputID: receipt.InputID, AfterOrdinal: int64(len(reply.items)) + 1,
					})
				}
			}
		}
		a.Mu.Unlock()
		return
	}
	receipt.TurnID = receipt.InputID
	drained := &inputReply{receipt: *receipt, autonomous: true}
	if !a.turn.active {
		a.turn = a.drainedTurn(drained, a.clock.Now())
		a.Mu.Unlock()
		a.PublishTurnActive()
		return
	}
	reason, interrupted := a.turn.leadDoneReason, a.turn.interruptRequested
	a.Mu.Unlock()
	a.retireDetachedReply()
	a.endTurnWithSuccessor(completionOf(reason, interrupted), runEndRow(receipt.SessionID, reason, ""),
		leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED, drained)
	a.publishDetachedReply()
}

// drainedTurn waits for the session's running snapshot to start attributing
// output. Cline sends no separate run.started event for a drained prompt.
func (a *Agent) drainedTurn(reply *inputReply, startedAt time.Time) turnState {
	return turnState{active: true, steerable: true, startedAt: startedAt, sequence: a.NextTurnSeq(), inputReply: reply}
}

func (a *Agent) noteLeadDone(event hubEvent) {
	var payload struct {
		Reason string `json:"reason"`
	}
	if json.Unmarshal(event.Payload, &payload) != nil {
		return
	}
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if reply := a.turn.inputReply; reply.ownsOutput(event.SessionID) && a.out.lead != nil && len(a.out.lead.spawns) == 0 && !a.team.busy() {
		reply.agentDone = true
		a.turn.leadDoneReason = payload.Reason
	}
}

func (a *Agent) resetSteerProofs() {
	a.retireDetachedReply()
	a.Mu.Lock()
	resolutions := a.steerProofs.reset()
	a.Mu.Unlock()
	a.retainSteerResolutions(resolutions)
}

func (a *Agent) retainSteerResolutions(resolutions []agent.SteerResolution) {
	for _, resolution := range resolutions {
		if err := agent.RetainSteerResolution(a.sink, resolution); !agent.OutputRetained(err) {
			slog.Warn("retain the Cline steer outcome", "agent_id", a.AgentID(), "error", err)
			continue
		}
		if resolution.Outcome == agent.SteerOutcomeEchoed && resolution.Receipt.TurnID == "" {
			// The submitted ID proves delivery. A run without the lead's
			// correlated result leaves its exact reply unproved.
			a.retireInputReply(&inputReply{receipt: resolution.Receipt})
		}
	}
}
