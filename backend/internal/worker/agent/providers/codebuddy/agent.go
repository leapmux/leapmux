package codebuddy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Agent owns one CodeBuddy process and the session it serves.
type Agent struct {
	providerkit.Process

	sink agent.ProviderServices
	opts agent.Options
	// swarmMode is the immutable --swarm choice for this process.
	swarmMode bool

	mu             sync.Mutex
	sessionID      string
	resumeState    *codebuddyResumeState
	model          string
	models         []codebuddyModelInfo
	permissionMode string
	planReturnMode string
	effort         string
	outputStyle    string
	thinkingChoice string
	// A nil catalog means native initialization supplied no style metadata.
	outputStyles               []string
	catalogRefresh             providerkit.CoalescingWork
	active                     bool
	inputReply                 *inputReply
	compactionLive             bool
	manualCompaction           bool
	assistantTextStreamed      bool
	lastDiagnostic             *codebuddyDiagnostic
	pendingPlanModeTools       map[string]agent.PlanModeControlKind
	nativeModeRestartRequired  bool
	nativeInputRestartRequired bool
	turnOrder                  providerkit.TurnSeq
	activityRev                uint64
	turnToolCalls              map[string]struct{}
	openTools                  providerkit.ToolSpanLedger[[]byte]
	// modelStreams keeps the latest response per transcript owner. Empty identifies the root.
	// The map and every stream state are guarded by mu.
	modelStreams      map[string]*codebuddyModelStream
	interruptRequests map[uint64]struct{}
	interruptAttempt  uint64
	// controls holds each native request until its answer or withdrawal.
	controls     map[string]*nativeControl
	approvalWork providerkit.CoalescingWork
	// permissionInterruptPending holds the global stop until the main run ends.
	permissionInterruptPending bool
	permissionWriteMu          sync.Mutex
	// outputMu serializes stdout frame dispatch against idle interrupt and exit tool cleanup.
	// Lock order: outputMu -> mu (never hold permissionWriteMu when acquiring outputMu).
	outputMu sync.Mutex

	// pendingControl routes a control_response to the caller waiting on its
	// request id. See sendControlAndWait.
	pendingControlMu        sync.Mutex
	pendingControl          map[string]chan<- codebuddyControlResult
	pendingControlObservers map[string]func(codebuddyControlResult, error)
	steerDispatchMu         sync.Mutex
	inFlightSteer           *codebuddyPendingSteer
	pendingSteers           []*codebuddyPendingSteer
	tasks                   codebuddyTaskIndex
	archiveMu               sync.Mutex
	archiveJobs             map[string]*codebuddyArchiveJob
	archiveChildJobs        map[string]*codebuddyArchiveJob
	archiveStopped          bool
	archiveRetries          sync.WaitGroup

	// directory holds the startup settings file of the process, or is nil for a
	// process that starts with no settings. Wait removes it once the process
	// exited, so the file stays for the whole life of the process that reads it.
	directory *agentdir.Dir
}

var _ agent.Agent = (*Agent)(nil)
var _ agent.InputSteerer = (*Agent)(nil)
var _ agent.SteerEchoReporter = (*Agent)(nil)
var _ agent.InputReceiptSender = (*Agent)(nil)
var _ agent.NativeTurnRestarter = (*Agent)(nil)

// SteerRowAtEcho keeps a steered queue item until CodeBuddy emits its durable
// user echo after injecting the steer into the running turn.
func (*Agent) SteerRowAtEcho() bool { return true }

// codebuddyControlResult is the outcome of one pending control request.
type codebuddyControlResult struct {
	// Answered is true when CodeBuddy sent a control_response for the request.
	// A request with no answer may still have reached CodeBuddy.
	Answered              bool
	Success               bool
	Error                 string
	Mode                  string
	Model                 string
	Models                []codebuddyModelInfo
	HasModelCatalog       bool
	OutputStyle           string
	OutputStyles          []string
	HasOutputStyleCatalog bool
	ConfigUpdated         *codebuddyNativeSettings
	ConfigErrors          map[string]string
	// Steered and SteerReason are the answer to a steer request. Steered is
	// nil when the answer states no outcome.
	Steered     *bool
	SteerReason string
}

// codebuddyModelInfo is one get_available_models entry.
type codebuddyModelInfo struct {
	ID          string `json:"modelId"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// NativeTurnRestartRequired reports when the next prompt needs a new process
// to apply native Plan settings or restore input after a rejected plan.
func (a *Agent) NativeTurnRestartRequired() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.nativeModeRestartRequired || a.nativeInputRestartRequired
}

// PublishTurnActive republishes the turn flag through the sink.
func (a *Agent) PublishTurnActive() agent.TurnState {
	a.mu.Lock()
	active := a.active
	steerable := active && !a.manualCompaction
	seq := a.turnOrder.NextTurnSeq()
	a.mu.Unlock()
	return providerkit.PublishTurnStateTo(a.sink, agent.TurnState{Active: active, Steerable: steerable}, seq)
}

func (a *Agent) setTurnActive(active bool) {
	a.mu.Lock()
	var retired []string
	var unfinished *inputReply
	var unconfirmedSteers []*codebuddyPendingSteer
	if active && !a.active {
		a.turnToolCalls = nil
		a.interruptRequests = nil
	}
	a.active = active
	endCompaction := !active && a.manualCompaction
	if !active {
		unfinished, a.inputReply = a.inputReply, nil
		unconfirmedSteers, a.pendingSteers = a.pendingSteers, nil
		a.closeModelStreamLocked("")
		a.lastDiagnostic = nil
		if endCompaction {
			a.manualCompaction = false
			a.compactionLive = false
		}
		for id, record := range a.controls {
			if record.rootPermission {
				delete(a.controls, id)
				if record.published {
					retired = append(retired, id)
				}
				record.publicationOnce.Do(func() { close(record.publicationDone) })
			}
		}
		a.permissionInterruptPending = false
	}
	a.activityRev++
	seq := a.turnOrder.NextTurnSeq()
	steerable := active && !a.manualCompaction
	a.mu.Unlock()
	a.resolvePendingSteers(unconfirmedSteers, agent.SteerOutcomeUncertain)
	a.retireInputReply(unfinished)
	if endCompaction {
		providerkit.EndCompaction(a.sink)
	}
	for _, id := range retired {
		a.sink.CancelControlRequest(id)
	}
	providerkit.PublishTurnStateTo(a.sink, agent.TurnState{Active: active, Steerable: steerable}, seq)
}

// SendInput delivers one user message over stdin. The write IS the delivery.
func (a *Agent) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInputForSession(nil, content, attachments, nil)
}

// SendInputWithReceipt supplies the turn ID that CodeBuddy will adopt for the input.
func (a *Agent) SendInputWithReceipt(content string, attachments []*leapmuxv1.Attachment) (agent.NativeInputReceipt, error) {
	var receipt agent.NativeInputReceipt
	err := a.sendInputForSession(nil, content, attachments, &receipt)
	return receipt, err
}

// SendInputForSession validates the session while constructing the request.
func (a *Agent) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInputForSession(&sessionID, content, attachments, nil)
}

func (a *Agent) sendInputForSession(expected *string, content string, attachments []*leapmuxv1.Attachment, receipt *agent.NativeInputReceipt) error {
	if a.IsStopped() {
		return fmt.Errorf("the CodeBuddy process is stopped")
	}
	words := strings.Fields(content)
	manualCompaction := len(attachments) == 0 && len(words) > 0 && words[0] == "/compact"
	var meta *inputMeta
	a.mu.Lock()
	if err := providerkit.CheckInputSession(expected, a.sessionID); err != nil {
		a.mu.Unlock()
		return err
	}
	if a.active {
		a.mu.Unlock()
		return agent.ErrAgentBusy
	}
	if receipt != nil && !manualCompaction && a.sessionID != "" {
		turnID, err := newInputTurnID()
		if err != nil {
			a.mu.Unlock()
			return err
		}
		*receipt = agent.NativeInputReceipt{SessionID: a.sessionID, InputID: turnID, TurnID: turnID}
		a.inputReply = &inputReply{receipt: *receipt}
		meta = &inputMeta{ConversationRequestID: turnID}
	}
	a.active = true
	a.manualCompaction = manualCompaction
	if manualCompaction {
		a.compactionLive = true
	}
	a.assistantTextStreamed = false
	a.lastDiagnostic = nil
	a.turnToolCalls = nil
	a.interruptRequests = nil
	a.activityRev++
	a.mu.Unlock()
	a.PublishTurnActive()
	if manualCompaction {
		// The manual command runs its compact agent without a native start status.
		// Publish before the write so an early result can end this state.
		providerkit.PublishCompaction(a.sink, "manual")
	}

	msg := UserInputMessage{
		Type: MessageTypeUser,
		Meta: meta,
		Message: UserInputContent{
			Role:    "user",
			Content: codebuddyUserContent(content, attachments),
		},
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		a.setTurnActive(false)
		return err
	}
	if err := a.SendRawInput(agent.RawInput{Data: raw}); err != nil {
		a.setTurnActive(false)
		return providerkit.ClassifyJSONRPCDeliveryError("user", err)
	}
	return nil
}

// SupportsSteering reports true: CodeBuddy carries a `steer` control request
// that injects user content into a running turn.
func (a *Agent) SupportsSteering() bool { return true }

// SteerInput injects content into the RUNNING turn through control_request/steer,
// and it returns the outcome that CodeBuddy answers.
//
// The steer drain of CodeBuddy keeps the text of the blocks only (ny and ly in
// 2.160.0). An image, a PDF or a binary file would lose its bytes there, and its
// label would still tell the model that a file is attached. SteerInput
// therefore refuses an input that carries any file but a text file as
// unsupported, and the worker sends that input as the next turn, files and all.
// The refusal depends on the input alone, so it comes before the turn check.
//
// See codebuddySteerOutcome for how the answer of CodeBuddy maps to the result.
func (a *Agent) SteerInput(request agent.SteerRequest) error {
	content, attachments := request.Content, request.Attachments
	if a.IsStopped() {
		return fmt.Errorf("the CodeBuddy process is stopped")
	}
	classified := agent.ClassifyAttachments(attachments)
	for _, attachment := range classified {
		if attachment.Kind != contracts.AttachmentMediaKindText {
			return fmt.Errorf("%w: CodeBuddy steers a running turn with text only, so %q goes in the next turn",
				agent.ErrSteeringUnsupported, attachment.Filename)
		}
	}
	a.steerDispatchMu.Lock()
	defer a.steerDispatchMu.Unlock()

	a.mu.Lock()
	active := a.active
	sessionID := a.sessionID
	var turnID string
	if a.inputReply != nil && a.inputReply.receipt.SessionID == sessionID {
		turnID = a.inputReply.receipt.TurnID
	}
	var tracked *codebuddyPendingSteer
	if active && request.InputID != "" && request.AttemptID != "" {
		tracked = &codebuddyPendingSteer{
			sessionID: sessionID,
			turnID:    turnID,
			resolution: agent.SteerResolution{
				InputID:   request.InputID,
				AttemptID: request.AttemptID,
			},
		}
		a.inFlightSteer = tracked
	}
	a.mu.Unlock()
	if !active {
		return agent.ErrNoActiveTurn
	}
	if tracked != nil {
		defer a.cancelInFlightSteer(tracked)
	}
	payload := map[string]any{
		"subtype":        "steer",
		"session_id":     sessionID,
		"content_blocks": codebuddyContentBlocks(content, classified),
	}
	if turnID != "" {
		payload["expected_request_id"] = turnID
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var observer func(codebuddyControlResult, error)
	if tracked != nil {
		observer = func(resp codebuddyControlResult, respErr error) {
			a.finishInFlightSteer(tracked, codebuddySteerOutcome(resp, respErr) == nil)
		}
	}
	return codebuddySteerOutcome(a.sendControlAndWaitObserved(string(body), a.APITimeout(), observer))
}

// codebuddySteerOutcome maps the answer of CodeBuddy to a steer request (its
// steer handler q4 in 2.160.0) to the error that the input queue of the worker
// reads:
//   - steered:true. The drain holds the steer for the running turn. The steer
//     succeeds.
//   - steered:false with the reason "idle" or "stale". The turn that the steer
//     was for is over. ErrNoActiveTurn makes the queue send the input as the
//     next turn.
//   - steered:false with any other reason, or with none. CodeBuddy refuses the
//     content. It refuses a steer whose text starts with a slash command. It
//     refuses a steer with no text. ErrSteeringUnsupported makes the queue send
//     the input as the next turn, where a slash command runs.
//   - An error answer. CodeBuddy refused the request before its drain took the
//     steer. The steer fails with the message of CodeBuddy.
//   - No answer, or an answer that states no outcome. The drain may hold the
//     steer, so a resend could duplicate it. ErrDeliveryUncertain makes the
//     queue ask before it sends the input again.
func codebuddySteerOutcome(resp codebuddyControlResult, err error) error {
	switch {
	case err != nil && !resp.Answered:
		return fmt.Errorf("%w: CodeBuddy did not answer the steer: %w", agent.ErrDeliveryUncertain, err)
	case err != nil:
		return fmt.Errorf("CodeBuddy refused the steer request: %w", err)
	case resp.Steered == nil:
		return fmt.Errorf("%w: CodeBuddy answered the steer without its outcome", agent.ErrDeliveryUncertain)
	case *resp.Steered:
		return nil
	case resp.SteerReason == codebuddySteerReasonIdle || resp.SteerReason == codebuddySteerReasonStale:
		return fmt.Errorf("%w: CodeBuddy found no running turn for the steer (%s)", agent.ErrNoActiveTurn, resp.SteerReason)
	case resp.SteerReason != "":
		return fmt.Errorf("%w: CodeBuddy refused to add the steer to the running turn (%s)", agent.ErrSteeringUnsupported, resp.SteerReason)
	default:
		return fmt.Errorf("%w: CodeBuddy refused to add the steer to the running turn", agent.ErrSteeringUnsupported)
	}
}

// Interrupt stops the turn through a waiting permission or a control request.
// CodeBuddy's permission refusal carries interrupt:true and stops the native run.
// Send the global stop after the native run ends, before the next input enters.
func (a *Agent) Interrupt(stop agent.StopContext) error {
	requested, err := a.sendInterrupt(stop)
	if err != nil {
		return err
	}
	if !requested {
		a.closeOpenTools(agent.MessageCompletionInterrupted, true, true)
	}
	return nil
}

func (a *Agent) sendInterrupt(agent.StopContext) (bool, error) {
	a.permissionWriteMu.Lock()
	defer a.permissionWriteMu.Unlock()
	if a.IsStopped() {
		return false, fmt.Errorf("the CodeBuddy process is stopped")
	}
	a.mu.Lock()
	if a.permissionInterruptPending {
		a.mu.Unlock()
		return true, nil
	}
	sessionID := a.sessionID
	revision := a.activityRev
	requested := a.active
	permissionStop := requested && a.hasOpenRootPermissionLocked()
	a.permissionInterruptPending = permissionStop
	a.interruptAttempt++
	attempt := a.interruptAttempt
	if requested {
		if a.interruptRequests == nil {
			a.interruptRequests = make(map[uint64]struct{})
		}
		a.interruptRequests[attempt] = struct{}{}
	}
	a.mu.Unlock()
	forgetFailedAttempt := func() {
		a.mu.Lock()
		if requested && a.active && a.activityRev == revision {
			delete(a.interruptRequests, attempt)
			a.permissionInterruptPending = false
		}
		a.mu.Unlock()
	}
	if permissionStop {
		refused, err := a.refuseOpenPermissions()
		if err != nil && !refused {
			forgetFailedAttempt()
		}
		if err != nil || refused {
			return requested, err
		}
		a.mu.Lock()
		a.permissionInterruptPending = false
		a.mu.Unlock()
	}
	if err := a.sendInterruptControl(sessionID); err != nil {
		forgetFailedAttempt()
		return requested, err
	}
	return requested, nil
}

func codebuddyBuildClosingRow(entry providerkit.OpenToolEntry[[]byte]) (agent.MessageContent, agent.SpanInfo, bool) {
	if len(entry.Data) == 0 {
		return agent.MessageContent{}, agent.SpanInfo{}, false
	}
	return agent.MessageContent{Original: bytes.Clone(entry.Data)}, agent.SpanInfo{
		SpanID:   entry.SpanID,
		SpanType: entry.SpanType,
		Closing:  true,
	}, true
}

func (a *Agent) closeOpenTools(completion agent.MessageCompletion, idleOnly, resetSpans bool) {
	a.outputMu.Lock()
	defer a.outputMu.Unlock()
	a.closeOpenToolsLocked(completion, idleOnly, resetSpans)
}

func (a *Agent) closeOpenToolsLocked(completion agent.MessageCompletion, idleOnly, resetSpans bool) {
	a.mu.Lock()
	if idleOnly && (a.active || a.StoppedLocked()) {
		a.mu.Unlock()
		return
	}
	entries := a.openTools.DrainAll()
	a.mu.Unlock()
	if a.sink == nil {
		return
	}
	if !a.IsDiscardingOutput() && len(entries) > 0 {
		providerkit.CloseToolSpans(a.sink, entries, completion, codebuddyBuildClosingRow)
	}
	if resetSpans {
		providerkit.ResetSpansAndProgress(a.sink)
	}
}

// Stop terminates the process.
func (a *Agent) Stop() {
	a.NoteIntentionalStop()
	a.Process.Stop()
	a.cleanupAfterProcessExit()
}

// Wait waits for the process to exit, and then removes the agent's private
// directory.
func (a *Agent) Wait() error {
	err := a.Process.Wait()
	a.cleanupAfterProcessExit()
	providerkit.RemoveAgentDir("codebuddy", a.AgentID(), a.directory)
	return err
}

// cleanupAfterProcessExit stops archive retries and clears controls, open
// tools, open foreground children, and turn state.
func (a *Agent) cleanupAfterProcessExit() {
	a.stopWorkflowArchiveRetries()
	a.withdrawControls()
	a.closeOpenTools(a.ProcessExitCompletion(), false, true)
	a.closeForegroundChildren(agent.IncompleteTaskStatus(a.ProcessExitCompletion()))
	a.setTurnActive(false)
}

// ClearContext starts a fresh session. CodeBuddy keeps no in-process clear, so
// the service restarts the agent.
func (a *Agent) ClearContext() (string, error) {
	return "", agent.ErrContextClearUnsupported
}
