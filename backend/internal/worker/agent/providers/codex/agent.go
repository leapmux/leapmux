package codex

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// The approval policy a fresh Codex agent runs on. The ids and the defaults of the
// four other axes live in contracts/codex-protocol.json, because the browser seeds a
// new agent from the same values. This one stays here: LeapMux owns the approval
// axis for every provider, and no browser code reads Codex's default for it.
const DefaultApprovalPolicy = "on-request"

// The Codex CLI features the worker turns on at launch.
const (
	codexMemoriesFeature     = "memories"
	codexMultiAgentV2Feature = "multi_agent_v2"
)

// Codex sandbox policy values.
const (
	SandboxDangerFullAccess = "danger-full-access"
	SandboxWorkspaceWrite   = "workspace-write"
	SandboxReadOnly         = "read-only"
)

// Codex network access values.
const (
	NetworkRestricted = "restricted"
	NetworkEnabled    = "enabled"
)

// Codex collaboration mode values.
const (
	CollaborationDefault = "default"
	CollaborationPlan    = "plan"
)

// Codex service tier values.
const (
	ServiceTierFast = "fast"
)

// How long a `turn/start` waits for its `turn/started` ack before it reports
// the delivery unconfirmed.
//
// This timeout limits the DELIVERY, not the turn. Codex emits `turn/started` as soon as it
// accepts the request, so this only has to cover the process being busy, not
// any model work. It is well inside the client's own RPC deadline, so an
// unacknowledged send surfaces as a stated failure rather than as the client
// giving up on a message the worker did deliver.
const turnStartAckTimeout = 10 * time.Second

// Agent manages a single Codex app-server process.
type Agent struct {
	providerkit.JSONRPCProcess // shared process lifecycle + JSON-RPC plumbing
	outputMu                   sync.Mutex

	model      string
	effort     string
	workingDir string
	sink       agent.ProviderServices
	// resumingThread is true only while a thread/resume handshake is in flight.
	//
	// It is what tells a goal RESTATEMENT from a goal EVENT. Codex marks the
	// resume snapshot with a null turnId, but it uses null for every report made
	// outside a turn -- including the acknowledgement of a thread/goal/set this
	// worker issued -- so the null alone reads a user's own action as history.
	// Atomic because the read loop consults it from its own goroutine while the
	// handshake sets it.
	resumingThread atomic.Bool

	// Codex-specific state.
	threadID string // from thread/start response
	turnID   string // currently active turn ID
	// The `turn/started` notification closes this channel after Codex accepts a
	// `turn/start`. Response timing differs across Codex versions, but this
	// notification always identifies the accepted turn.
	turnStartAck chan struct{}
	// A contextCompaction item can confirm a compact request before its JSON-RPC
	// response arrives. Non-nil only while CompactContext waits for acceptance.
	compactionStartAck chan struct{}

	approvalPolicy    string // Codex approval policy (stored as-is from DB)
	sandboxPolicy     string // Codex sandbox policy (e.g. "workspace-write")
	networkAccess     string // Codex network access ("restricted" or "enabled")
	collaborationMode string // Codex collaboration mode ("default" or "plan")
	serviceTier       string // Codex service tier ("default" or "fast")
	turnSawPlan       bool   // whether the current turn produced a plan item
	turnPlanText      string // final text of the current turn's plan item
	turnAssistantText string // final assistant message text for the current turn
	// planPromptRequestID is the plan-approval card that is still open.
	//
	// ONE plan is stored for each agent, and UpdatePlan REPLACES it, so a card from
	// an earlier turn approves a plan it never showed. The card is keyed by turn, so
	// two of them can coexist -- and the composer renders the OLDEST, which makes the
	// newer one invisible. Guarded by a.Mu.
	planPromptRequestID string
	// reasoningStreamKind records, per reasoning itemId, which reasoning sub-stream
	// ("summary" or "raw") was seen first, so the token counter counts
	// only one of them. Codex can emit both summaryTextDelta and textDelta for the
	// SAME reasoning item (they are the same generation surfaced two ways), which
	// would otherwise double-count. Locking onto whichever arrives first keeps the
	// counter moving for models that stream only one of the two.
	reasoningStreamKind   map[string]string
	reasoningRetainedKind map[string]string
	reasoningSummaryIndex map[string]int
	reasoningSummarySeen  map[string]bool
	reasoningSummaryBreak map[string]bool
	generationBuffer      providerkit.GenerationBuffer
	incompleteTools       map[string]*codexIncompleteTool
	incompleteToolOrder   uint64
	availableModels       []*agent.ModelInfo
	// collabChildren is the durable in-process route from each Codex child
	// thread to its LeapMux transcript. The route survives completed runs, so
	// a follow-up turn reuses the same transcript. ClearContext removes it,
	// because the new root thread owns a different child tree.
	collabChildren map[string]*codexChildState
	// retiredCodexThreads prevents notifications from a replaced context from
	// creating routes or transcript rows in the new context.
	retiredCodexThreads map[string]struct{}
	// collabChildItems records the child thread that owns a tool item. Output
	// deltas carry only an item ID, so this index restores the transcript route.
	// Guarded by Mu.
	collabChildItems map[string]string
	// codexSpawnPrompts holds Multi-Agent V2 spawn arguments until the matching
	// subAgentActivity supplies the child thread ID.
	codexSpawnPrompts map[string]string
	// interruptCalls coalesces concurrent interrupts for one Codex turn. A
	// successful call stays cached until the turn ends, so a late retry cannot
	// send another request for an already interrupted turn. Guarded by Mu.
	interruptCalls map[codexInterruptKey]*codexInterruptCall
}

type codexInterruptKey struct {
	threadID string
	turnID   string
}

type codexInterruptCall struct {
	done chan struct{}
	err  error
}

// startOrResumeThread sends thread/start, or thread/resume when the launch
// carries a stored thread ID. It returns the thread ID and effective model
// reported by Codex. A resume that does not hold fails the whole start; see
// providerkit.ResumeFailedError.
func (a *Agent) startOrResumeThread(
	threadParams map[string]interface{}, resumeSessionID string, timeout time.Duration,
) (codexThreadResult, error) {
	if resumeSessionID != "" {
		threadParams["threadId"] = resumeSessionID
		// LeapMux reads transcript history from its own database.
		threadParams["excludeTurns"] = true
		return a.resumeThread(threadParams, resumeSessionID, timeout)
	}
	thread, err := a.startThread(threadParams, timeout)
	if err != nil {
		return codexThreadResult{}, err
	}
	if thread.ID == "" {
		return codexThreadResult{}, fmt.Errorf("codex thread/start: response did not contain a thread ID")
	}
	return thread, nil
}

type codexThreadResult struct {
	ID       string
	settings map[string]*string
}

type codexLifecyclePolicy uint8

const (
	codexLifecycleIgnored codexLifecyclePolicy = iota
	codexLifecycleAuthoritative
	codexLifecycleWhenAutomatic
)

func (a *Agent) applyThreadResult(result codexThreadResult) {
	for _, axis := range codexAxes {
		value, present := result.settings[axis.id]
		if !present || axis.lifecyclePolicy == codexLifecycleIgnored {
			continue
		}
		if axis.lifecyclePolicy == codexLifecycleWhenAutomatic {
			current := axis.get(a)
			if current != "" && current != agent.EffortAuto {
				continue
			}
		}
		if value == nil {
			if axis.lifecycleDefault != "" {
				axis.set(a, axis.lifecycleDefault)
			}
			continue
		}
		if *value != "" {
			axis.set(a, *value)
		}
	}
}

type codexThreadResponse struct {
	Thread struct {
		ID string `json:"id"`
	} `json:"thread"`
	Model          string          `json:"model"`
	Effort         json.RawMessage `json:"reasoningEffort"`
	ServiceTier    json.RawMessage `json:"serviceTier"`
	ApprovalPolicy json.RawMessage `json:"approvalPolicy"`
	Sandbox        json.RawMessage `json:"sandbox"`
}

// resumeThread sends `thread/resume` and returns the thread ID and effective
// model that Codex reported. An RPC error, a response that does not parse, and
// a response that carries no required field all fail: none of the three
// reopens the conversation.
func (a *Agent) resumeThread(
	threadParams map[string]interface{}, resumeSessionID string, timeout time.Duration,
) (codexThreadResult, error) {
	paramsJSON, err := json.Marshal(threadParams)
	if err != nil {
		return codexThreadResult{}, providerkit.ResumeFailedError(resumeSessionID, fmt.Errorf("marshal thread/resume params: %w", err))
	}
	resp, err := a.SendRequest("thread/resume", paramsJSON, timeout)
	if err != nil {
		return codexThreadResult{}, providerkit.ResumeFailedError(resumeSessionID, err)
	}
	threadResult, err := parseCodexThreadResponse("thread/resume", resp)
	if err != nil {
		return codexThreadResult{}, providerkit.ResumeFailedError(resumeSessionID, err)
	}
	return threadResult, nil
}

// startThread sends `thread/start` and returns the new thread's effective model.
func (a *Agent) startThread(threadParams map[string]interface{}, timeout time.Duration) (codexThreadResult, error) {
	paramsJSON, err := json.Marshal(threadParams)
	if err != nil {
		return codexThreadResult{}, fmt.Errorf("marshal thread/start params: %w", err)
	}
	resp, err := a.SendRequest("thread/start", paramsJSON, timeout)
	if err != nil {
		return codexThreadResult{}, err
	}
	return parseCodexThreadResponse("thread/start", resp)
}

func parseCodexThreadResponse(method string, raw json.RawMessage) (codexThreadResult, error) {
	var response codexThreadResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return codexThreadResult{}, fmt.Errorf("codex %s: failed to parse response %q: %w", method, string(raw), err)
	}
	if response.Thread.ID == "" {
		return codexThreadResult{}, fmt.Errorf("codex %s: response %q carried no thread ID", method, string(raw))
	}
	if response.Model == "" {
		return codexThreadResult{}, fmt.Errorf("codex %s: response %q carried no effective model", method, string(raw))
	}
	return newCodexThreadResult(response)
}

func newCodexThreadResult(response codexThreadResponse) (codexThreadResult, error) {
	model := response.Model
	result := codexThreadResult{
		ID:       response.Thread.ID,
		settings: map[string]*string{agent.OptionIDModel: &model},
	}
	for _, target := range []struct {
		raw   json.RawMessage
		id    string
		label string
	}{
		{response.Effort, agent.OptionIDEffort, "reasoningEffort"},
		{response.ServiceTier, contracts.CodexOptionServiceTier, "serviceTier"},
		{response.ApprovalPolicy, agent.OptionIDPermissionMode, "approvalPolicy"},
	} {
		if len(target.raw) == 0 {
			continue
		}
		if string(target.raw) == "null" {
			result.settings[target.id] = nil
			continue
		}
		var value string
		if err := json.Unmarshal(target.raw, &value); err != nil {
			// Codex can return granular approval policy as an object. LeapMux stores
			// the simple policy string, so preserve the prior value for that form.
			if target.label == "approvalPolicy" {
				continue
			}
			return codexThreadResult{}, fmt.Errorf("codex thread response field %s was not a string: %w", target.label, err)
		}
		result.settings[target.id] = &value
	}
	sandboxSettings, err := decodeCodexThreadSandbox(response.Sandbox)
	if err != nil {
		return codexThreadResult{}, err
	}
	for id, value := range sandboxSettings {
		result.settings[id] = value
	}
	return result, nil
}

// decodeCodexThreadSandbox accepts the legacy string and the current tagged
// object. The current object also reports the effective network access.
func decodeCodexThreadSandbox(raw json.RawMessage) (map[string]*string, error) {
	settings := make(map[string]*string)
	if len(raw) == 0 {
		return settings, nil
	}
	if string(raw) == "null" {
		settings[contracts.CodexOptionSandboxPolicy] = nil
		return settings, nil
	}

	var policy string
	if json.Unmarshal(raw, &policy) == nil {
		settings[contracts.CodexOptionSandboxPolicy] = &policy
		return settings, nil
	}

	var sandbox struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &sandbox); err != nil {
		return nil, fmt.Errorf("codex thread response field sandbox was invalid: %w", err)
	}

	switch sandbox.Type {
	case "dangerFullAccess":
		policy = SandboxDangerFullAccess
		network := NetworkEnabled
		settings[contracts.CodexOptionNetworkAccess] = &network
	case "workspaceWrite":
		policy = SandboxWorkspaceWrite
	case "readOnly":
		policy = SandboxReadOnly
	case "externalSandbox":
		// LeapMux has no external-sandbox option. Preserve the requested values.
		return settings, nil
	default:
		// Preserve both stored values for a future Codex sandbox variant.
		return settings, nil
	}
	settings[contracts.CodexOptionSandboxPolicy] = &policy
	return settings, nil
}

// Interrupt aborts the active Codex turn by sending a `turn/interrupt`
// JSON-RPC request with the current threadId and turnId. Codex responds to
// this request only after it emits TurnAborted, so this method waits for the
// response that confirms the turn stopped. A notification is ignored by
// Codex because the app server handles this method only as a request.
//
// Returns nil when there's nothing to interrupt (no active turn) so callers
// can invoke this safely without tracking turn lifecycle.
func (a *Agent) Interrupt() error {
	a.Mu.Lock()
	stopped := a.StoppedLocked()
	threadID := a.threadID
	turnID := a.turnID
	a.Mu.Unlock()
	if stopped {
		return fmt.Errorf("agent is stopped")
	}
	// The answers go FIRST, as Base.Interrupt sends them: a request the runtime
	// still waits on outlives the turn that raised it. Codex is the one publisher
	// that registers a real cancel answer -- the MCP elicitation at
	// output.go's MCPElicitationMethodCodex case -- because Codex retires its
	// own approval requests but defines no outcome for an elicitation the client
	// withdraws. Without this drain that answer can never be delivered: the
	// elicitation stays blocked inside the CLI for the rest of the session, well
	// past the point the user believes the turn ended.
	a.AnswerOutstandingControlRequests(a.sink)
	if threadID == "" || turnID == "" {
		// No active turn — nothing to interrupt. Treat as benign so
		// scripts can call Interrupt unconditionally without first
		// probing turn state.
		return nil
	}
	if err := a.interruptCodexTurn(threadID, turnID); err != nil {
		return err
	}
	// The ANSWERLESS requests go last, and only once the interrupt is accepted.
	// Codex's four approval methods register a nil cancel answer, because Codex
	// retires its own approval requests -- so withdrawing one sends the CLI nothing
	// and only deletes the reader's card. Retiring them before `turn/interrupt` meant
	// that a turn/interrupt which timed out or was refused left the CLI still blocked
	// on an approval with the card already gone: the reader had no control that could
	// answer it, no later SendRawInput could route one, and the thinking indicator
	// never stopped.
	a.WithdrawAllControlRequests(a.sink)
	return nil
}

// Stop retains unfinished model output before it stops the process.
func (a *Agent) Stop() {
	a.Process.Stop()
	a.outputMu.Lock()
	a.flushAllCodexGeneration(agent.MessageCompletionInterrupted)
	a.persistIncompleteCodexTools("", true, agent.MessageCompletionInterrupted)
	a.outputMu.Unlock()
	a.sink.ReportProgress(agent.ResetProgress())
}

// Wait retains unfinished model output after an unexpected process exit.
func (a *Agent) Wait() error {
	err := a.Process.Wait()
	completion := a.ProcessExitCompletion()
	a.outputMu.Lock()
	a.flushAllCodexGeneration(completion)
	a.persistIncompleteCodexTools("", true, completion)
	a.outputMu.Unlock()
	a.sink.ReportProgress(agent.ResetProgress())
	return err
}

func (a *Agent) interruptCodexTurn(threadID, turnID string) error {
	if threadID == "" || turnID == "" {
		return nil
	}
	key := codexInterruptKey{threadID: threadID, turnID: turnID}
	a.Mu.Lock()
	if a.interruptCalls == nil {
		a.interruptCalls = make(map[codexInterruptKey]*codexInterruptCall)
	}
	if existing := a.interruptCalls[key]; existing != nil {
		done := existing.done
		a.Mu.Unlock()
		<-done
		return existing.err
	}
	call := &codexInterruptCall{done: make(chan struct{})}
	a.interruptCalls[key] = call
	a.Mu.Unlock()

	params, err := json.Marshal(map[string]string{"threadId": threadID, "turnId": turnID})
	if err == nil {
		_, err = a.SendRequest("turn/interrupt", params, a.APITimeout())
	}
	if err != nil {
		err = fmt.Errorf("turn/interrupt: %w", err)
	}

	a.Mu.Lock()
	call.err = err
	close(call.done)
	if err != nil {
		delete(a.interruptCalls, key)
	}
	a.Mu.Unlock()
	return err
}

func (a *Agent) clearInterruptCallsForThread(threadID string) {
	a.Mu.Lock()
	for key := range a.interruptCalls {
		if key.threadID == threadID {
			delete(a.interruptCalls, key)
		}
	}
	a.Mu.Unlock()
}

// ClearContext sends a new thread/start on the running Codex process,
// replacing the current thread with a fresh one.
func (a *Agent) ClearContext() (string, error) {
	a.Mu.Lock()
	oldThreadID := a.threadID
	approvalPolicy := a.approvalPolicy
	sandboxPolicy := a.sandboxPolicy
	serviceTier := a.serviceTier
	model := a.model
	workingDir := a.workingDir
	a.Mu.Unlock()

	threadParams := codexThreadParams(model, workingDir, approvalPolicy, sandboxPolicy, serviceTier)
	// Codex uses this source to distinguish a deliberate context clear from an
	// unrelated new conversation. Both sources start with empty model history.
	threadParams["sessionStartSource"] = "clear"

	thread, err := a.startThread(threadParams, a.APITimeout())
	if err != nil {
		return "", err
	}
	if thread.ID == "" {
		return "", fmt.Errorf("the new Codex thread has no ID")
	}
	a.outputMu.Lock()
	a.flushAllCodexGeneration(agent.MessageCompletionInterrupted)
	a.persistIncompleteCodexTools("", true, agent.MessageCompletionInterrupted)

	a.Mu.Lock()
	if a.retiredCodexThreads == nil {
		a.retiredCodexThreads = make(map[string]struct{})
	}
	if oldThreadID != "" {
		a.retiredCodexThreads[oldThreadID] = struct{}{}
	}
	for childThreadID := range a.collabChildren {
		a.retiredCodexThreads[childThreadID] = struct{}{}
	}
	a.applyThreadResult(thread)
	a.threadID = thread.ID
	a.turnID = ""
	a.turnSawPlan = false
	a.turnPlanText = ""
	a.turnAssistantText = ""
	clear(a.reasoningStreamKind)
	clear(a.reasoningRetainedKind)
	clear(a.reasoningSummaryIndex)
	clear(a.reasoningSummarySeen)
	clear(a.reasoningSummaryBreak)
	// Clear the child routes: a new root thread owns a different child tree. A
	// completed run keeps its route only while its root thread lives.
	clear(a.collabChildren)
	clear(a.collabChildItems)
	clear(a.codexSpawnPrompts)
	clear(a.incompleteTools)
	a.incompleteToolOrder = 0
	a.Mu.Unlock()
	a.generationBuffer.Reset()
	a.outputMu.Unlock()
	a.PublishTurnActive()
	a.sink.ReportProgress(agent.ResetProgress())

	// A goal belongs to a THREAD, and this call replaced the thread. Codex
	// sends thread/goal/cleared only after a real removal and on a resume, so a
	// fresh thread/start reports nothing at all -- the stored goal would stay,
	// and the card would offer Pause and Clear for a thread that has no goal.
	a.sink.ClearGoal(false)

	a.sink.UpdateSessionID(thread.ID)
	return thread.ID, nil
}

// PublishTurnActive republishes the Worker-visible turn state from turnID, the
// single source. Call it after EVERY critical section that writes turnID.
//
// It re-reads rather than taking a value, so a caller cannot publish something
// the field does not say, and the sink deduplicates, so a redundant call costs
// nothing. A MISSING call is the only way the two can drift -- which is why
// this is a re-read and not an argument.
//
// Never called with a.Mu held: the sink broadcasts, and a broadcast can block
// on a slow transport.
func (a *Agent) PublishTurnActive() agent.TurnState {
	a.Mu.Lock()
	active := a.turnID != ""
	seq := a.NextTurnSeq()
	a.Mu.Unlock()
	// Each child route tracks its own turn. The background-task registry supplies
	// that state to the child tab. This method reports the main thread only.
	return providerkit.PublishSteerableTurnActiveTo(a.sink, active, seq)
}

// SendInput starts a new turn with the current settings. It refuses an active
// turn because only SteerInput can add input to that turn.
//
// SendInput sends the text and nothing else. It reads no command out of the
// text. The queue classifies "/compact" and "/summarize" before dispatch and
// calls CompactContext instead, so CompactContext is the single entry point to
// a native compaction.
//
// Do not add a second command check here. It disagrees with the one in the
// queue, which classifies only a plain user message: a control response whose
// text is "/compact" keeps its kind, reaches this method, and a check here
// starts a compaction in place of the answer that the agent waits for.
func (a *Agent) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInputForSession(nil, content, attachments)
}

func (a *Agent) sendInputForSession(expected *string, content string, attachments []*leapmuxv1.Attachment) error {
	// Read shared state under lock, then release before the blocking RPC.
	a.Mu.Lock()
	if err := providerkit.CheckInputSession(expected, a.threadID); err != nil {
		a.Mu.Unlock()
		return err
	}
	if a.StoppedLocked() {
		a.Mu.Unlock()
		return fmt.Errorf("agent is stopped")
	}
	threadID := a.threadID
	turnID := a.turnID
	model := a.model
	effort := a.effort
	approvalPolicy := a.approvalPolicy
	sandboxPolicy := a.sandboxPolicy
	networkAccess := a.networkAccess
	collaborationMode := a.collaborationMode
	serviceTier := a.serviceTier
	a.Mu.Unlock()

	if threadID == "" {
		return fmt.Errorf("codex agent has no active thread")
	}

	input := buildCodexInputBlocks(content, agent.ClassifyAttachments(attachments))

	// Normal queue dispatch never changes the active turn. Steering is an
	// explicit queue operation through SteerInput.
	if turnID != "" {
		// Manager.SendInput republishes the flag for this refusal. See
		// Agent.PublishTurnActive for why the rule lives there.
		return fmt.Errorf("%w: %s", agent.ErrAgentBusy, turnID)
	}

	return a.sendTurnStart(threadID, input, turnSettings{
		model:             model,
		effort:            effort,
		approvalPolicy:    approvalPolicy,
		sandboxPolicy:     sandboxPolicy,
		networkAccess:     networkAccess,
		collaborationMode: collaborationMode,
		serviceTier:       serviceTier,
	})
}

func (a *Agent) CompactContext() error {
	a.Mu.Lock()
	if a.StoppedLocked() {
		a.Mu.Unlock()
		return fmt.Errorf("agent is stopped")
	}
	threadID := a.threadID
	if threadID == "" {
		a.Mu.Unlock()
		return fmt.Errorf("codex agent has no active thread")
	}
	if a.compactionStartAck != nil {
		a.Mu.Unlock()
		return fmt.Errorf("context compaction request is already pending")
	}
	ack := make(chan struct{})
	a.compactionStartAck = ack
	a.Mu.Unlock()
	clearAck := func() {
		a.Mu.Lock()
		if a.compactionStartAck == ack {
			a.compactionStartAck = nil
		}
		a.Mu.Unlock()
	}
	params, err := json.Marshal(map[string]interface{}{"threadId": threadID})
	if err != nil {
		clearAck()
		return fmt.Errorf("marshal thread/compact/start params: %w", err)
	}
	response := make(chan error, 1)
	go func() {
		_, requestErr := a.SendRequest("thread/compact/start", params, 0)
		response <- requestErr
	}()
	timer := time.NewTimer(turnStartAckTimeout)
	defer timer.Stop()
	select {
	case <-ack:
		return nil
	case requestErr := <-response:
		clearAck()
		if requestErr != nil {
			return classifyCodexCompactionRequestError(requestErr)
		}
		return nil
	case <-timer.C:
		clearAck()
		return classifyCodexCompactionRequestError(fmt.Errorf("no response or contextCompaction start within %s", turnStartAckTimeout))
	}
}

func classifyCodexCompactionRequestError(err error) error {
	return providerkit.ClassifyJSONRPCDeliveryError("thread/compact/start", err)
}

// Agent steers. Manager.SupportsSteering answers false, with no build error, for a
// provider that stops satisfying InputSteerer, so this assertion makes that
// regression a compile error.
var _ agent.InputSteerer = (*Agent)(nil)

// SupportsSteering always reports true. The Codex app-server accepts turn/steer
// for any active turn, so the capability needs no handshake discovery.
func (a *Agent) SupportsSteering() bool { return true }

func (a *Agent) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	a.Mu.Lock()
	threadID, turnID := a.threadID, a.turnID
	stopped := a.StoppedLocked()
	a.Mu.Unlock()
	if stopped {
		return fmt.Errorf("agent is stopped")
	}
	if threadID == "" || turnID == "" {
		return agent.ErrNoActiveTurn
	}
	if err := a.sendTurnSteer(threadID, turnID, buildCodexInputBlocks(content, agent.ClassifyAttachments(attachments))); err != nil {
		return err
	}
	a.Mu.Lock()
	stillActive := a.turnID == turnID
	a.Mu.Unlock()
	if !stillActive {
		return agent.ErrNoActiveTurn
	}
	return nil
}

// buildCodexInputBlocks converts text + classified attachments into Codex's
// input format. Images use data URI format; text attachments are inlined.
func buildCodexInputBlocks(content string, classified []agent.ClassifiedAttachment) []map[string]interface{} {
	var input []map[string]interface{}
	if content != "" {
		input = append(input, map[string]interface{}{"type": "text", "text": content})
	}
	for _, attachment := range classified {
		switch attachment.Kind {
		case agent.AttachmentKindText:
			input = append(input, map[string]interface{}{
				"type": "text",
				"text": providerkit.BuildInlineTextAttachmentBlock(attachment),
			})
		case agent.AttachmentKindImage:
			input = append(input, map[string]interface{}{
				"type": "image",
				"url":  providerkit.EncodeDataURI(attachment.MIMEType, attachment.Data),
			})
		case agent.AttachmentKindPDF, agent.AttachmentKindBinary:
			// Codex's input items have no representation for either, so the
			// attachment is omitted from the turn rather than sent as junk text.
		}
	}
	return input
}

// turnSettings groups the per-turn settings snapshotted from agent state.
type turnSettings struct {
	model             string
	effort            string
	approvalPolicy    string
	sandboxPolicy     string
	networkAccess     string
	collaborationMode string
	serviceTier       string
}

// sendTurnStart sends a turn/start request with all current settings.
func (a *Agent) sendTurnStart(
	threadID string,
	input []map[string]interface{},
	s turnSettings,
) error {
	params := map[string]interface{}{
		"threadId": threadID,
		"input":    input,
	}
	if !agent.UsesAccountDefaultModel(s.model) {
		params["model"] = s.model
	}
	if e, ok := codexEffortValue(s.effort); ok {
		params["effort"] = e
	}
	if s.approvalPolicy != "" {
		params["approvalPolicy"] = s.approvalPolicy
	}
	if sp := codexSandboxPolicyObject(s.sandboxPolicy, s.networkAccess); sp != nil {
		params["sandboxPolicy"] = sp
	}
	if cm := codexCollaborationModeObject(s.collaborationMode, s.model, s.effort); cm != nil {
		params["collaborationMode"] = cm
	}
	if st := codexServiceTierValue(s.serviceTier); st != nil {
		params["serviceTier"] = *st
	}
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal turn/start params: %w", err)
	}

	// SendInput returns when Codex accepts the message. `turn/started` supplies
	// that acceptance independent of the response timing in the Codex version.
	// Register the channel before the request to prevent a missed notification.
	ack := make(chan struct{})
	a.Mu.Lock()
	a.turnStartAck = ack
	a.Mu.Unlock()
	clearAck := func() {
		a.Mu.Lock()
		if a.turnStartAck == ack {
			a.turnStartAck = nil
		}
		a.Mu.Unlock()
	}

	// turn/started owns the delivery deadline. Keep the request correlated until
	// Codex responds or the process exits, but do not hold the caller or a lock.
	requestErr := make(chan error, 1)
	go func() {
		if _, err := a.SendRequest("turn/start", paramsJSON, 0); err != nil {
			slog.Error("codex turn/start failed", "agent_id", a.AgentID(), "error", err)
			requestErr <- err
		}
	}()

	timer := time.NewTimer(turnStartAckTimeout)
	defer timer.Stop()
	select {
	case <-ack:
		// `turn/started` also set turnID for explicit steering.
		return nil
	case err := <-requestErr:
		clearAck()
		return classifyCodexTurnStartRequestError("turn/start", err)
	case <-a.ProcessDone():
		clearAck()
		return classifyCodexTurnStartRequestError("turn/start", a.ProcessExitError())
	case <-timer.C:
		// The request can still start a turn. Report uncertain delivery and stop
		// waiting for its acceptance notification.
		clearAck()
		return fmt.Errorf("%w: turn/start received no turn/started within %s", agent.ErrDeliveryUncertain, turnStartAckTimeout)
	}
}

func classifyCodexTurnStartRequestError(operation string, err error) error {
	return providerkit.ClassifyJSONRPCDeliveryError(operation, err)
}

// sendTurnSteer steers the active turn with additional user input.
func (a *Agent) sendTurnSteer(threadID, turnID string, input []map[string]interface{}) error {
	params := map[string]interface{}{
		"threadId":       threadID,
		"expectedTurnId": turnID,
		"input":          input,
	}
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal turn/steer params: %w", err)
	}

	// Codex versions differ in response timing. Keep the request correlated
	// until Codex responds or the process exits.
	_, err = a.SendRequest("turn/steer", paramsJSON, 0)
	if err != nil {
		if providerkit.HasJSONRPCErrorCode(err, -32600, -32602) {
			return agent.ErrNoActiveTurn
		}
		return providerkit.ClassifyJSONRPCDeliveryError("turn/steer", err)
	}
	return nil
}

// codexSandboxPolicyObject converts a simple sandbox policy string
// (e.g. "danger-full-access") to the tagged object format expected by
// turn/start's sandboxPolicy field (e.g. {"type": "dangerFullAccess"}).
// networkAccess is included as a boolean for workspaceWrite/readOnly or
// as a string ("restricted"/"enabled") for dangerFullAccess.
// Returns nil if the policy is empty or unrecognized.
func codexSandboxPolicyObject(policy, networkAccess string) map[string]interface{} {
	var obj map[string]interface{}
	switch policy {
	case SandboxDangerFullAccess:
		obj = map[string]interface{}{"type": "dangerFullAccess"}
	case SandboxWorkspaceWrite:
		obj = map[string]interface{}{"type": "workspaceWrite"}
	case SandboxReadOnly:
		obj = map[string]interface{}{"type": "readOnly"}
	default:
		return nil
	}
	obj["networkAccess"] = networkAccess == NetworkEnabled
	return obj
}

// codexCollaborationModeObject converts a simple collaboration mode string to
// the object format expected by turn/start's collaborationMode field.
// We send developer_instructions: null so Codex applies its built-in mode
// instructions, matching the native Codex TUI/Desktop behavior.
func codexCollaborationModeObject(mode, model, effort string) map[string]interface{} {
	if mode == "" {
		return nil
	}
	switch mode {
	case CollaborationDefault, CollaborationPlan:
	default:
		return nil
	}
	// EffortAuto (and empty) send null so the CLI applies whatever default its
	// version supports; a concrete tier is passed through.
	reasoningEffort := interface{}(nil)
	if e, ok := codexEffortValue(effort); ok {
		reasoningEffort = e
	}
	// settings.model is a required non-empty string: Codex answers an omitted field
	// with "missing field 'model'", a null with "invalid type: null, expected a
	// string", and an empty string with the same unknown-model error as any other
	// unknown id. So this field cannot carry the account-default rule that
	// codexThreadParams applies. The caller must supply a concrete model, which
	// UpdateSettings enforces by requiring a relaunch to reach the account default.
	return map[string]interface{}{
		"mode": mode,
		"settings": map[string]interface{}{
			"model":                  model,
			"reasoning_effort":       reasoningEffort,
			"developer_instructions": nil,
		},
	}
}

// codexEffortValue normalizes a stored effort for the Codex wire. EffortAuto (and
// empty) mean "let Codex pick its own default effort", so they map to ("", false)
// -- the caller omits the field / sends null; a concrete tier maps to (tier, true).
// Single source of the auto-means-omit rule for both turn/start's top-level effort
// and the nested collaborationMode reasoning_effort.
func codexEffortValue(effort string) (string, bool) {
	if effort == "" || effort == agent.EffortAuto {
		return "", false
	}
	return effort, true
}

// codexThreadParams builds the params shared by thread/start and thread/resume.
// Start and ClearContext both call it, so the launch path and the
// clear-context path construct the thread the same way and a new thread field is
// added once, here.
//
// It omits an account-default model so Codex can resolve it. It includes a concrete
// model so a resumed thread keeps its effective or user-selected model. It never
// sets threadId: startOrResumeThread adds that for the resume case.
func codexThreadParams(model, cwd, approvalPolicy, sandboxPolicy, serviceTier string) map[string]interface{} {
	params := map[string]interface{}{
		"cwd":            cwd,
		"approvalPolicy": approvalPolicy,
		"sandbox":        sandboxPolicy,
		// Request detailed summaries so app-server emits reasoning summary items.
		"config": map[string]interface{}{
			"model_reasoning_summary": "detailed",
		},
	}
	if !agent.UsesAccountDefaultModel(model) {
		params["model"] = model
	}
	if st := codexServiceTierValue(serviceTier); st != nil {
		params["serviceTier"] = *st
	}
	return params
}

// codexServiceTierValue converts a stored service tier to the turn/thread
// wire value. A nil return omits the field and keeps Codex's normal tier.
func codexServiceTierValue(tier string) *string {
	// Only the explicit "fast" tier is sent on the wire; "", the default tier, and any unknown
	// value all omit the field (nil) and keep Codex's normal tier.
	if tier == ServiceTierFast {
		return &tier
	}
	return nil
}

// childTurnSeq issues the ordering token for a collab CHILD's turn flag. Both
// edges arrive on the one reader goroutine, so the token only has to be
// monotonic -- the counter the main thread shares supplies that, and the Worker
// tracks the last token for each agent id separately.
func (a *Agent) childTurnSeq() uint64 {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return a.NextTurnSeq()
}

func (a *Agent) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInputForSession(&sessionID, content, attachments)
}
