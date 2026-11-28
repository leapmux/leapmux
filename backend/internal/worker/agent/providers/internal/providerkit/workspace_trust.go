package providerkit

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// Workspace trust that LeapMux asks about.
//
// Some agents keep a list of trusted folders and load part of a project's
// configuration only from a trusted one, but ask nothing in the mode that
// LeapMux runs them in: GitHub Copilot in server mode is one. WorkspaceTrust asks
// the reader instead. It reads the agent's trust when a session opens, asks once
// for each session when the folder is untrusted, and on Trust lets the agent add
// the folder and load that configuration. A Reject leaves the folder untrusted,
// and the agent asks no more during its life. contracts/workspace-trust.json
// states the question and the answer.
//
// An agent may call Ask more than once for a session, as ZCode does from an event
// that can repeat. The question of a session is asked once all the same: an
// answered question stays asked, and only a question that Retire withdrew before
// an answer may be asked again.
//
// An agent that raises its own trust request, such as Grok Build, answers that
// request in its own protocol and does not use this.

// WorkspaceTrustHost is the agent's own trusted-folder list. Each call runs off
// the reader goroutine, while the provider holds its session lock for reading.
type WorkspaceTrustHost interface {
	// WorkspaceTrusted reports whether the agent trusts the working directory.
	WorkspaceTrusted() (bool, error)
	// TrustWorkspace adds the working directory to the agent's trusted list, and
	// loads the configuration that the trust controls. An error leaves the
	// question answerable.
	TrustWorkspace() error
}

// workspaceTrustRequestPrefix starts the request ID of every workspace-trust
// question, which is how a provider tells its answer from a native one.
const workspaceTrustRequestPrefix = "workspace-trust:"

// WorkspaceTrustRequestID identifies the question of one session.
func WorkspaceTrustRequestID(sessionID string) string {
	return workspaceTrustRequestPrefix + sessionID
}

// IsWorkspaceTrustRequestID reports whether requestID identifies a
// workspace-trust question.
func IsWorkspaceTrustRequestID(requestID string) bool {
	return strings.HasPrefix(requestID, workspaceTrustRequestPrefix)
}

// WorkspaceTrustRequestKey is the key that Ask publishes a stored
// workspace-trust question under (agent.Provider.ControlRequestKey): the
// request ID of the session that the question belongs to. The question's
// payload states no key of its own, so the session that the Worker stores with
// the question decides the key. ok is false for a payload that is not a
// workspace-trust question, and for a question of no session, which Ask never
// publishes.
func WorkspaceTrustRequestKey(payload []byte, agentSessionID string) (string, bool) {
	if !IsWorkspaceTrustQuestion(payload) || agentSessionID == "" {
		return "", false
	}
	return WorkspaceTrustRequestID(agentSessionID), true
}

// ErrWorkspaceTrustClosed refuses an answer to a question that is no longer
// open: its session went away, or another answer retired it.
var ErrWorkspaceTrustClosed = errors.New("the workspace-trust question is no longer open")

// WorkspaceTrust holds the open questions of one agent. The zero value is ready.
type WorkspaceTrust struct {
	mu   sync.Mutex
	cond *sync.Cond
	// declined is true once the reader rejected the trust.
	declined bool
	// retirementGeneration changes whenever Retire invalidates checks in flight.
	retirementGeneration uint64
	// open holds each open question by its request ID.
	open map[string]*workspaceTrustQuestion
	// asked holds the request ID of each question whose publication started and
	// was not withdrawn before an answer.
	asked map[string]struct{}
	// requestStates serializes replacements behind earlier sink calls for the
	// same request ID. Different request IDs remain independent.
	requestStates map[string]*workspaceTrustRequestState
}

// workspaceTrustRequestState counts the sink calls and waiting replacements for
// one request ID. A state with a waiter cannot be deleted and replaced, so a
// waiter never resumes against a new state with the same request ID.
type workspaceTrustRequestState struct {
	inFlight int
	waiting  int
}

// workspaceTrustQuestion is one open question. responding is true while an
// answer runs, so a second answer cannot run beside it. retired is true when
// Retire withdrew the question while its publication was still running.
type workspaceTrustQuestion struct {
	instanceID string
	responding bool
	retired    bool
}

type workspaceTrustRetirement struct {
	requestID string
	question  *workspaceTrustQuestion
	state     *workspaceTrustRequestState
}

// Ask publishes the question of the session sessionID when the agent does not
// trust path: once for each session, and not after the reader declined. A failed
// or unreadable check asks nothing, because the agent then keeps the controlled
// configuration unloaded, which is the safe side. configuration lists what the
// trust controls, in words that the browser shows.
//
// A question of no session is never asked. Its key derives from its session
// (WorkspaceTrustRequestKey), and the Worker stores a request of no session
// with the session that the sink holds, so the stored question would give
// another key than its own, and the Worker would refuse every answer to it.
func (w *WorkspaceTrust) Ask(sink agent.ProviderServices, host WorkspaceTrustHost, sessionID, path string, configuration []string) {
	if sessionID == "" {
		slog.Warn("Ask no workspace-trust question without a session", "path", path)
		return
	}
	requestID := WorkspaceTrustRequestID(sessionID)
	generation, mayAsk := w.generationForAsk(requestID)
	if !mayAsk {
		return
	}
	trusted, err := host.WorkspaceTrusted()
	if err != nil {
		slog.Warn("Read the workspace trust of the agent", "path", path, "error", err)
		return
	}
	if trusted {
		return
	}
	payload, err := json.Marshal(map[string]any{
		contracts.WorkspaceTrustFieldType:          contracts.WorkspaceTrustControlTypeQuestion,
		contracts.WorkspaceTrustFieldPath:          path,
		contracts.WorkspaceTrustFieldConfiguration: configuration,
	})
	if err != nil {
		slog.Error("Encode the workspace-trust question", "error", err)
		return
	}
	question := &workspaceTrustQuestion{instanceID: id.Generate()}
	state := w.beginPublication(requestID, generation, question)
	if state == nil {
		return
	}
	err = sink.PublishControlRequest(agent.ControlRequest{
		RequestID: requestID, InstanceID: question.instanceID, Payload: payload, AgentSessionID: sessionID,
	})
	if err != nil {
		slog.Error("Publish the workspace-trust question", "request_id", requestID, "error", err)
	}
	w.mu.Lock()
	if err != nil && w.open[requestID] == question {
		delete(w.open, requestID)
		delete(w.asked, requestID)
	}
	retired := question.retired
	w.mu.Unlock()
	if err == nil && retired {
		// Retire may have cancelled before the sink made the request visible.
		sink.CancelControlRequestInstance(requestID, question.instanceID)
	}
	w.finishRequestOperation(requestID, state)
}

// beginPublication registers a question after every earlier sink call for its
// request ID finishes. Waiting releases w.mu, and retirement invalidates the
// generation captured before the host check.
func (w *WorkspaceTrust) beginPublication(requestID string, generation uint64, question *workspaceTrustQuestion) *workspaceTrustRequestState {
	w.mu.Lock()
	state := w.requestStateLocked(requestID)
	state.waiting++
	cond := w.conditionLocked()
	cond.Broadcast()
	for state.inFlight > 0 && generation == w.retirementGeneration && w.mayAskLocked(requestID) {
		cond.Wait()
	}
	state.waiting--
	if generation != w.retirementGeneration || !w.mayAskLocked(requestID) {
		w.releaseRequestStateLocked(requestID, state)
		cond.Broadcast()
		w.mu.Unlock()
		return nil
	}
	if w.open == nil {
		w.open = make(map[string]*workspaceTrustQuestion)
		w.asked = make(map[string]struct{})
	}
	w.open[requestID] = question
	w.asked[requestID] = struct{}{}
	state.inFlight++
	cond.Broadcast()
	w.mu.Unlock()
	return state
}

// generationForAsk captures the retirement generation when the session may ask.
func (w *WorkspaceTrust) generationForAsk(requestID string) (uint64, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.retirementGeneration, w.mayAskLocked(requestID)
}

// mayAskLocked reports whether the session of requestID may still ask. The
// caller holds w.mu.
func (w *WorkspaceTrust) mayAskLocked(requestID string) bool {
	_, asked := w.asked[requestID]
	return !w.declined && !asked
}

func (w *WorkspaceTrust) conditionLocked() *sync.Cond {
	if w.cond == nil {
		w.cond = sync.NewCond(&w.mu)
	}
	return w.cond
}

func (w *WorkspaceTrust) requestStateLocked(requestID string) *workspaceTrustRequestState {
	if w.requestStates == nil {
		w.requestStates = make(map[string]*workspaceTrustRequestState)
	}
	state := w.requestStates[requestID]
	if state == nil {
		state = &workspaceTrustRequestState{}
		w.requestStates[requestID] = state
	}
	return state
}

func (w *WorkspaceTrust) beginRequestOperationLocked(requestID string) *workspaceTrustRequestState {
	state := w.requestStateLocked(requestID)
	state.inFlight++
	return state
}

func (w *WorkspaceTrust) finishRequestOperation(requestID string, state *workspaceTrustRequestState) {
	w.mu.Lock()
	state.inFlight--
	w.releaseRequestStateLocked(requestID, state)
	w.conditionLocked().Broadcast()
	w.mu.Unlock()
}

func (w *WorkspaceTrust) releaseRequestStateLocked(requestID string, state *workspaceTrustRequestState) {
	if state.inFlight == 0 && state.waiting == 0 && w.requestStates[requestID] == state {
		delete(w.requestStates, requestID)
	}
}

// Answer carries out one answer that the worker forwards. input.Data is the shared
// control response envelope, {response: {request_id, response: {decision}}}.
// handled is false for an answer to another request, which the provider then
// answers itself. An answer that fails keeps the question open.
func (w *WorkspaceTrust) Answer(sink agent.ProviderServices, host WorkspaceTrustHost, input agent.RawInput) (handled bool, err error) {
	var envelope struct {
		Response struct {
			RequestID string          `json:"request_id"`
			Response  json.RawMessage `json:"response"`
		} `json:"response"`
	}
	if json.Unmarshal(input.Data, &envelope) != nil || !IsWorkspaceTrustRequestID(envelope.Response.RequestID) {
		return false, nil
	}
	requestID := envelope.Response.RequestID
	w.mu.Lock()
	question := w.open[requestID]
	if question == nil || question.responding {
		w.mu.Unlock()
		return true, ErrWorkspaceTrustClosed
	}
	if err := agent.ControlInstanceRefusal(question.instanceID, input.ControlInstanceID); err != nil {
		w.mu.Unlock()
		return true, err
	}
	question.responding = true
	w.mu.Unlock()

	decision := StringField(envelope.Response.Response, contracts.WorkspaceTrustFieldDecision)
	switch decision {
	case contracts.WorkspaceTrustDecisionTrust:
		err = host.TrustWorkspace()
	case contracts.WorkspaceTrustDecisionReject:
	default:
		err = fmt.Errorf("the workspace-trust answer states neither %s nor %s",
			contracts.WorkspaceTrustDecisionTrust, contracts.WorkspaceTrustDecisionReject)
	}
	w.mu.Lock()
	if err != nil {
		question.responding = false
		w.mu.Unlock()
		return true, err
	}
	if decision == contracts.WorkspaceTrustDecisionReject {
		w.declined = true
	}
	current := w.open[requestID] == question
	var state *workspaceTrustRequestState
	if current {
		delete(w.open, requestID)
		state = w.beginRequestOperationLocked(requestID)
	}
	w.mu.Unlock()
	if current {
		sink.CancelControlRequestInstance(requestID, question.instanceID)
		w.finishRequestOperation(requestID, state)
	}
	return true, nil
}

// Retire withdraws every open question and invalidates every check in flight. A
// provider calls it when its session goes away, because the question belongs to
// that session, and when its process stops. The next session asks again unless
// the reader declined.
func (w *WorkspaceTrust) Retire(sink agent.ProviderServices) {
	w.mu.Lock()
	w.retirementGeneration++
	retirements := make([]workspaceTrustRetirement, 0, len(w.open))
	for requestID, question := range w.open {
		question.retired = true
		retirements = append(retirements, workspaceTrustRetirement{
			requestID: requestID,
			question:  question,
			state:     w.beginRequestOperationLocked(requestID),
		})
		// The reader never answered it, so its session may ask again.
		delete(w.asked, requestID)
	}
	clear(w.open)
	w.conditionLocked().Broadcast()
	w.mu.Unlock()
	for _, retirement := range retirements {
		sink.CancelControlRequestInstance(retirement.requestID, retirement.question.instanceID)
		w.finishRequestOperation(retirement.requestID, retirement.state)
	}
}

// ResolveWorkspaceTrustResponse turns the neutral allow or deny on a
// workspace-trust question (AnswerWorkspaceTrust) into the saved answer,
// {decision}. It reports false
// for a stored request that is not a workspace-trust question, which the
// provider then resolves itself.
func ResolveWorkspaceTrustResponse(ctx agent.ControlResponseContext) (agent.ControlResponseResolution, bool) {
	if !IsWorkspaceTrustQuestion(ctx.RequestPayload) {
		return agent.ControlResponseResolution{}, false
	}
	result := agent.DefaultControlResponseResolution(ctx)
	_, behavior, _, decoded := agent.DecodeControlBehavior(ctx.ResponseContent)
	if !decoded || (behavior != contracts.ControlAnswerBehaviorAllow && behavior != contracts.ControlAnswerBehaviorDeny) {
		result.Refuse(agent.RefusalNoDecision)
		return result, true
	}
	decision := contracts.WorkspaceTrustDecisionReject
	if behavior == contracts.ControlAnswerBehaviorAllow {
		decision = contracts.WorkspaceTrustDecisionTrust
	}
	result.Content = agent.ControlResponseEnvelope(ctx.RequestID, map[string]string{contracts.WorkspaceTrustFieldDecision: decision})
	return result, true
}
