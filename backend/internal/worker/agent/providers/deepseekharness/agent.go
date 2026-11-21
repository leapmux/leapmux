package deepseekharness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"google.golang.org/protobuf/proto"
)

// Agent owns one native Web process and one root Session.
type Agent struct {
	providerkit.Process
	sink         agent.ProviderServices
	rpc          remoteRPC
	workingDir   string
	opMu         sync.Mutex
	steerMu      sync.Mutex
	writeMu      sync.Mutex
	dispatchMu   sync.Mutex
	conn         *websocket.Conn
	streamCancel context.CancelFunc
	streamDone   chan struct{}
	ready        chan struct{}
	readyOnce    sync.Once
	discard      atomic.Bool
	sessionID    string
	clientID     string
	catalog      modelCatalog
	selection    modelSelection
	mode         string
	permissions  string
	// autoReview states whether the native permission catalog offers Auto.
	autoReview      bool
	active          bool
	streams         map[string]*sessionStream
	controls        map[string]*nativeControl
	approvalWork    providerkit.CoalescingWork
	children        map[string]*nativeChild
	childCatalog    map[string]nativeChildDescriptor
	workflows       map[string]nativeWorkflow
	directory       *agentdir.Dir
	imageReceipts   imageHookFiles
	streamFailure   error
	failureStopDone chan struct{}
	// dispatchMu protects errors until the native turn end supplies its diagnostic.
	pendingErrors   []string
	lastTurnError   string
	retryResolution providerkit.RetryResolution
	pendingSteers   []*pendingSteer
	// dispatchMu serializes reply ownership with the Remote event stream.
	replySubmissions map[string]*harnessReplySubmission
	replyTurn        *harnessReplyTurn
}

// pendingSteer keeps the original input until native history proves its delivery or removal.
// Mu protects its native identity and disposition.
type pendingSteer struct {
	// requestID is the native prompt request, which native history states.
	requestID string
	// itemID is the native inbox item, known once native history states it.
	itemID string
	// request is the Worker's steer, which removePendingSteer hands back
	// unchanged. Its attachments are copies, so a later change of the caller's
	// slice does not reach the hand-back.
	request   agent.SteerRequest
	delivered bool
	removed   bool
}

var _ agent.Agent = (*Agent)(nil)
var _ agent.InputReceiptSender = (*Agent)(nil)
var _ agent.InputSteerer = (*Agent)(nil)
var _ agent.SteerEchoReporter = (*Agent)(nil)
var _ agent.SteerSurvivor = (*Agent)(nil)
var _ agent.ContextCompactor = (*Agent)(nil)
var _ agent.GoalWriter = (*Agent)(nil)
var _ agent.ChildSteerer = (*Agent)(nil)
var _ agent.ChildInterrupter = (*Agent)(nil)

func newAgent(sink agent.ProviderServices, workingDir string) *Agent {
	return &Agent{sink: sink, workingDir: workingDir, streamDone: make(chan struct{}), ready: make(chan struct{}), streams: map[string]*sessionStream{}, controls: map[string]*nativeControl{}, children: map[string]*nativeChild{}, childCatalog: map[string]nativeChildDescriptor{}, workflows: map[string]nativeWorkflow{}, mode: "act", permissions: "workspace-write"}
}

func (a *Agent) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	_, err := a.sendInput("", content, attachments, nil, false)
	return err
}

func (a *Agent) SendInputWithReceipt(content string, attachments []*leapmuxv1.Attachment) (agent.NativeInputReceipt, error) {
	return a.sendInput("", content, attachments, nil, true)
}

func (a *Agent) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	if sessionID == "" {
		return agent.ErrInputSessionChanged
	}
	_, err := a.sendInput(sessionID, content, attachments, nil, false)
	return err
}

func (a *Agent) SteerInput(request agent.SteerRequest) error {
	_, err := a.sendInput("", request.Content, request.Attachments, &request, request.AttemptID != "")
	return err
}

// SteerRowAtEcho waits for the native event that identifies the consuming turn.
func (*Agent) SteerRowAtEcho() bool { return true }

// SteerSurvivesTurnEnd prevents the queue from resending an input that the native inbox retains.
func (*Agent) SteerSurvivesTurnEnd() bool { return true }

func (a *Agent) SupportsSteering() bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return !a.StoppedLocked() && a.streamFailure == nil && a.sessionID != ""
}

// sendInput sends one prompt to the native Session. steer is the Worker's
// request when the prompt steers the running turn, and nil when the prompt
// starts a turn. content and attachments are the request's own for a steer.
func (a *Agent) sendInput(expected, content string, attachments []*leapmuxv1.Attachment, steer *agent.SteerRequest, trackReply bool) (agent.NativeInputReceipt, error) {
	var inputReceipt agent.NativeInputReceipt
	delivery := "queue"
	if steer != nil {
		delivery = "steer"
	}
	a.opMu.Lock()
	defer a.opMu.Unlock()
	if delivery == "steer" {
		a.steerMu.Lock()
		defer a.steerMu.Unlock()
	}
	a.Mu.Lock()
	sessionID := a.sessionID
	if a.streamFailure != nil {
		failure := a.streamFailure
		a.Mu.Unlock()
		return inputReceipt, fmt.Errorf("DeepSeek Harness lost its native Remote stream: %w", failure)
	}
	if a.StoppedLocked() {
		a.Mu.Unlock()
		return inputReceipt, fmt.Errorf("DeepSeek Harness is stopped")
	}
	if sessionID == "" || expected != "" && expected != sessionID {
		a.Mu.Unlock()
		return inputReceipt, agent.ErrInputSessionChanged
	}
	if delivery == "queue" && a.active {
		a.Mu.Unlock()
		return inputReceipt, agent.ErrAgentBusy
	}
	if delivery == "steer" && !a.active {
		a.Mu.Unlock()
		return inputReceipt, agent.ErrNoActiveTurn
	}
	a.Mu.Unlock()
	if len(attachments) == 0 {
		handled, err := a.userCommand(sessionID, content)
		if handled || err != nil {
			if err == nil && steer != nil && steer.AttemptID != "" {
				// A local command can finish without submitting a native prompt.
				err = a.reportSteerResolution(*steer, agent.SteerOutcomeEchoed, inputReceipt)
				if agent.OutputRetained(err) {
					err = nil
				}
			}
			return inputReceipt, err
		}
	}
	parts, err := a.promptParts(sessionID, content, attachments)
	if err != nil {
		return inputReceipt, err
	}
	requestID := uuid.NewString()
	var submission *harnessReplySubmission
	if trackReply {
		inputReceipt = agent.NativeInputReceipt{SessionID: sessionID, InputID: requestID}
		submission = a.registerReplySubmission(inputReceipt, steer)
	}
	if delivery == "queue" {
		a.Mu.Lock()
		a.active = true
		seq := a.NextTurnSeq()
		a.Mu.Unlock()
		a.sink.SetTurnState(agent.TurnState{Active: true, Steerable: true}, seq)
	}
	var receipt struct {
		Accepted bool `json:"accepted"`
	}
	var pending *pendingSteer
	if steer != nil {
		pending = &pendingSteer{requestID: requestID, request: *steer}
		pending.request.Attachments = nil
		for _, attachment := range steer.Attachments {
			pending.request.Attachments = append(pending.request.Attachments, proto.CloneOf(attachment))
		}
		a.Mu.Lock()
		a.pendingSteers = append(a.pendingSteers, pending)
		a.Mu.Unlock()
	}
	err = a.rpc.request(a.Context(), "session/prompt", map[string]any{"requestId": requestID, "sessionId": sessionID, "mode": delivery, "content": parts}, &receipt)
	if err == nil && !receipt.Accepted {
		err = fmt.Errorf("DeepSeek Harness did not accept the prompt")
	}
	if err == nil {
		return inputReceipt, nil
	}
	if submission != nil {
		if a.replySubmissionEchoed(submission) {
			return inputReceipt, nil
		}
		inputReceipt = agent.NativeInputReceipt{}
	}
	if pending != nil {
		a.Mu.Lock()
		delivered := pending.delivered
		a.pendingSteers = slices.DeleteFunc(a.pendingSteers, func(candidate *pendingSteer) bool { return candidate == pending })
		a.Mu.Unlock()
		if delivered {
			return inputReceipt, nil
		}
	}
	var native *remoteFailure
	if errors.As(err, &native) {
		if delivery == "queue" {
			a.setTurnState(false)
		}
		return inputReceipt, err
	}
	return inputReceipt, fmt.Errorf("%w: DeepSeek Harness did not confirm the prompt: %w", agent.ErrDeliveryUncertain, err)
}

func (a *Agent) PublishTurnActive() agent.TurnState {
	a.Mu.Lock()
	state := agent.TurnState{Active: a.active, Steerable: a.active}
	seq := a.NextTurnSeq()
	a.Mu.Unlock()
	a.sink.SetTurnState(state, seq)
	return state
}

func (a *Agent) setTurnState(active bool) {
	a.Mu.Lock()
	a.active = active
	seq := a.NextTurnSeq()
	a.Mu.Unlock()
	a.sink.SetTurnState(agent.TurnState{Active: active, Steerable: active}, seq)
}

func (a *Agent) Interrupt(stop agent.StopContext) error {
	// A compaction command holds opMu until it ends. Its cancel must run before that reply.
	a.steerMu.Lock()
	defer a.steerMu.Unlock()
	a.Mu.Lock()
	sessionID, active := a.sessionID, a.active
	steers := slices.Clone(a.pendingSteers)
	a.Mu.Unlock()
	if active {
		if err := a.rpc.request(a.Context(), "session/cancel", map[string]string{"sessionId": sessionID}, nil); err != nil {
			return err
		}
	}
	var failures error
	for _, steer := range steers {
		failures = errors.Join(failures, a.removePendingSteer(sessionID, steer))
	}
	return failures
}

func (a *Agent) removePendingSteer(sessionID string, steer *pendingSteer) error {
	a.Mu.Lock()
	id, delivered, removed := steer.itemID, steer.delivered, steer.removed
	a.Mu.Unlock()
	if delivered || id == "" {
		return nil
	}
	if !removed {
		var receipt struct {
			Accepted bool `json:"accepted"`
		}
		err := a.rpc.request(a.Context(), "session/updateQueue", map[string]any{"sessionId": sessionID, "itemId": id, "action": map[string]string{"kind": "remove"}}, &receipt)
		var native *remoteFailure
		if errors.As(err, &native) && native.Code == "session/queue-item-not-found" {
			a.forgetPendingSteer(steer)
			return nil
		}
		if err != nil {
			return err
		}
		if !receipt.Accepted {
			return fmt.Errorf("DeepSeek Harness did not remove the pending steer")
		}
		a.Mu.Lock()
		steer.removed = true
		a.Mu.Unlock()
	}
	if steer.request.AttemptID != "" {
		a.dispatchMu.Lock()
		delete(a.replySubmissions, steer.requestID)
		a.dispatchMu.Unlock()
		err := a.reportSteerResolution(steer.request, agent.SteerOutcomeDropped, agent.NativeInputReceipt{})
		if !agent.OutputRetained(err) {
			return err
		}
	} else {
		if err := a.sink.RequeueDroppedInput(steer.request); err != nil {
			return err
		}
	}
	a.forgetPendingSteer(steer)
	return nil
}

func (a *Agent) forgetPendingSteer(steer *pendingSteer) {
	a.Mu.Lock()
	a.pendingSteers = slices.DeleteFunc(a.pendingSteers, func(candidate *pendingSteer) bool { return candidate == steer })
	a.Mu.Unlock()
}

// The Web API has no synchronous Session disposal operation. The Worker
// replaces the process so reset stops its descendants and retains no old Agent.
func (a *Agent) ClearContext() (string, error) { return "", agent.ErrContextClearUnsupported }

func (a *Agent) CompactContext() error {
	a.opMu.Lock()
	defer a.opMu.Unlock()
	a.Mu.Lock()
	sessionID := a.sessionID
	active := a.active
	a.Mu.Unlock()
	if active {
		return agent.ErrAgentBusy
	}
	return a.compact(sessionID, "/compact")
}

func (a *Agent) DiscardOutput() { a.discard.Store(true); a.Process.DiscardOutput() }

func (a *Agent) SendRawInput(input agent.RawInput) error { return a.answerControl(input.Data) }

// HandleOutput accepts one native mux frame for provider tests.
func (a *Agent) HandleOutput(raw []byte) {
	if err := a.handleFrame(raw); err != nil {
		a.reportStreamFailure(err)
	}
}

func (a *Agent) Stop() {
	a.NoteIntentionalStop()
	a.Process.Stop()
	a.stopConnection()
}

func (a *Agent) Wait() error {
	err := a.Process.Wait()
	a.stopConnection()
	a.dispatchMu.Lock()
	a.retireUnfinishedReplies()
	a.dispatchMu.Unlock()
	a.Mu.Lock()
	failure, stopDone := a.streamFailure, a.failureStopDone
	a.Mu.Unlock()
	if stopDone != nil {
		<-stopDone
	}
	return errors.Join(err, failure, a.directory.Close())
}

func (a *Agent) ProcessExitCompletion() agent.MessageCompletion {
	a.Mu.Lock()
	failed := a.streamFailure != nil
	a.Mu.Unlock()
	if failed {
		return agent.MessageCompletionError
	}
	return a.Process.ProcessExitCompletion()
}

func (a *Agent) stopConnection() {
	if a.streamCancel != nil {
		a.streamCancel()
	}
	if a.conn != nil {
		_ = a.conn.CloseNow()
	}
	if a.rpc.endpoint != nil {
		a.rpc.endpoint.Close()
	}
	if a.streamDone != nil && a.conn != nil {
		<-a.streamDone
	}
}

func (a *Agent) session() string { a.Mu.Lock(); defer a.Mu.Unlock(); return a.sessionID }

func encodeJSON(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode the DeepSeek Harness payload: %w", err)
	}
	return raw, nil
}
