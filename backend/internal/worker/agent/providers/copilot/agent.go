package copilot

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Agent owns one native connection and its current session.
type Agent struct {
	*copilotConnection
	sink agent.ProviderServices
	opts agent.Options

	// samplingOnce prevents a concurrent stop from creating a replacement sampling context.
	samplingOnce   sync.Once
	samplingCtx    context.Context
	samplingCancel context.CancelFunc
	steers         providerkit.SteerLedger

	sessionMu sync.RWMutex
	stateMu   sync.Mutex
	sessionID string
	options   optionmap.Map
	models    []*agent.ModelInfo
	active    bool
	// A compaction holds a queue turn but does not accept steering. Its request
	// revision also keeps a late reply from clearing a replacement session.
	compactingRevision uint64
	// turnCompaction is the revision of the manual compaction that the active
	// root turn belongs to, and zero for a conversation turn. The runtime can run
	// a model turn inside the compaction request, and the end of that turn
	// applies the compaction's verdict, not the classifier's. The mark is zero
	// whenever no root turn is active. stateMu guards it.
	turnCompaction uint64
	// compactionVerdictRevision is the revision of the last manual compaction
	// whose retry verdict applied, so that a turn end and the reply of one
	// compaction apply one verdict between them. stateMu guards it.
	compactionVerdictRevision uint64
	turnOrder                 providerkit.TurnSeq
	activityRevision          uint64

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
	// turnToolUses counts the tool calls that the current turn started in the
	// root transcript. The turn end states it and restarts it. outputMu guards it.
	turnToolUses int
	// turnFailure is what the root transcript of the current turn states about its
	// failure, and autoSwitch is the root auto-mode switch of that turn with its
	// answer. The turn end reads both for its retry verdict (retry.go). The root
	// assistant.turn_start, the turn end, and a session replacement clear both.
	// outputMu guards both.
	turnFailure copilotTurnFailure
	autoSwitch  copilotAutoSwitch
	// nativeText accumulates the assistant text the runtime STREAMS.
	//
	// The runtime marks every delta ephemeral and sends the assistant message only
	// once that message is complete, so a turn cut short stored nothing and the answer
	// the reader watched vanished. See closeStreamedNativeText and CP-015.
	nativeText providerkit.GenerationBuffer
	// nativeTextSegments identifies the transcript each accumulating segment belongs to, in
	// the order the runtime opened them, so a turn end writes its rows the way the
	// agent produced them.
	nativeTextSegments []copilotStreamedText
	// retries holds the retry state of each native agent. outputMu guards it.
	retries map[string]*copilotRetry
	// sqlTodos is the last accepted SQL snapshot. outputMu guards it.
	sqlTodos json.RawMessage
	// reasoningSeen prevents an embedded summary and a separate reasoning event
	// from storing the same thought twice. outputMu guards both fields.
	reasoningSeen  map[string]struct{}
	reasoningOrder []string

	controlMu sync.Mutex
	controls  map[string]*copilotPendingControl
	// workspaceTrust holds the folder-trust question that LeapMux asks for
	// Copilot (folder_trust.go).
	workspaceTrust providerkit.WorkspaceTrust

	// interestMu guards the event-log subscription handles of the CURRENT session.
	// The runtime returns a distinct handle for each registration and keeps the
	// subscription alive until its handle is released, so a replacement that
	// forgot them would leave the old session's interests registered for the life
	// of the process. See CP-009.
	interestMu sync.Mutex
	interests  []string

	goalMu sync.Mutex
	goal   copilotGoalSnapshot

	// backgroundWork coalesces the work offReader starts. It carries its OWN
	// mutex: its bookkeeping has nothing to do with the session snapshot, the
	// option map or the model catalog that stateMu guards, and riding that lock
	// only widened what a reader must hold in mind to order six of them.
	backgroundWork providerkit.CoalescingWork
}

var _ agent.Agent = (*Agent)(nil)
var _ agent.InputSteerer = (*Agent)(nil)
var _ agent.SteerEchoReporter = (*Agent)(nil)
var _ agent.SteerSurvivor = (*Agent)(nil)
var _ agent.ContextCompactor = (*Agent)(nil)

// The keys of the background operations that offReader keeps apart.
const (
	copilotReadGoal           = "goal"
	copilotReadSettings       = "settings"
	copilotApprovePermissions = "approve-permissions"
	copilotCheckFolderTrust   = "folder-trust"
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
func (a *Agent) offReader(key string, work func()) {
	a.backgroundWork.Run(key, func() { a.runUnderNativeSession(work) })
}

// stopNativeConnection ends the process and drops the state of the session it served.
//
// A path that disposed of its session and could not open another one has nothing to
// roll back to. An agent that kept its process alive there would accept input that can
// never arrive anywhere.
func (a *Agent) stopNativeConnection() {
	a.outputMu.Lock()
	a.closing = true
	a.clearNativeChildren()
	a.clearNativeControls()
	a.outputMu.Unlock()
	a.forgetNativeControlEvents()
	a.clearNativeCompaction()
	a.copilotConnection.Stop()
}

// PublishTurnActive republishes the Worker-visible turn state.
//
// A model turn accepts session.send with mode:"immediate". A compaction holds
// the queue while its summary RPC runs, but it does not accept a steer.
func (a *Agent) PublishTurnActive() agent.TurnState {
	a.stateMu.Lock()
	state := a.nativeTurnStateLocked()
	sequence := a.turnOrder.NextTurnSeq()
	a.stateMu.Unlock()
	return providerkit.PublishTurnStateTo(a.sink, state, sequence)
}

// nativeTurnStateLocked separates a model turn from a compaction RPC. A model
// turn accepts an immediate steer; a compaction keeps the queue busy alone.
// The caller holds stateMu.
func (a *Agent) nativeTurnStateLocked() agent.TurnState {
	return agent.TurnState{
		Active:    a.active || a.compactingRevision != 0,
		Steerable: a.active && a.compactingRevision == 0,
	}
}

func (a *Agent) setNativeTurnActive(active bool) bool {
	a.stateMu.Lock()
	previous := a.active
	a.active = active
	a.activityRevision++
	switch {
	case !active:
		a.turnCompaction = 0
	case !previous:
		// A root turn that starts while a manual compaction runs is the
		// runtime's model turn for that compaction.
		a.turnCompaction = a.compactingRevision
	}
	state := a.nativeTurnStateLocked()
	sequence := a.turnOrder.NextTurnSeq()
	a.stateMu.Unlock()
	providerkit.PublishTurnStateTo(a.sink, state, sequence)
	return previous
}

func (a *Agent) rejectNativeInput(revision uint64) {
	a.stateMu.Lock()
	if revision != a.activityRevision {
		a.stateMu.Unlock()
		return
	}
	a.active = false
	a.turnCompaction = 0
	state := a.nativeTurnStateLocked()
	sequence := a.turnOrder.NextTurnSeq()
	a.stateMu.Unlock()
	providerkit.PublishTurnStateTo(a.sink, state, sequence)
}

// releaseNativeTurn makes the root turn idle at its session.idle. ended is
// false when no turn was active. verdict states the retry verdict of the turn
// end. A turn that belongs to a manual compaction claims the verdict of that
// compaction in the same critical section, so the reply of the compaction
// cannot apply it a second time.
func (a *Agent) releaseNativeTurn() (ended bool, verdict copilotTurnEndVerdict) {
	a.stateMu.Lock()
	ended = a.active
	compaction := a.turnCompaction
	a.active = false
	a.turnCompaction = 0
	a.activityRevision++
	verdict = copilotTurnEndClassified
	if ended && compaction != 0 {
		verdict = copilotTurnEndCompactionApplied
		if a.claimCompactionVerdictLocked(compaction) {
			verdict = copilotTurnEndCompaction
		}
	}
	state := a.nativeTurnStateLocked()
	sequence := a.turnOrder.NextTurnSeq()
	a.stateMu.Unlock()
	providerkit.PublishTurnStateTo(a.sink, state, sequence)
	return ended, verdict
}

// claimCompactionVerdictLocked reports whether the caller applies the retry
// verdict of the manual compaction of revision, and records that the verdict
// applied. The caller holds stateMu.
func (a *Agent) claimCompactionVerdictLocked(revision uint64) bool {
	if a.compactionVerdictRevision == revision {
		return false
	}
	a.compactionVerdictRevision = revision
	return true
}

func (a *Agent) clearNativeCompaction() {
	a.stateMu.Lock()
	a.compactingRevision = 0
	a.stateMu.Unlock()
}

// finishNativeCompaction ends the manual compaction of revision. It reports
// whether the caller applies the retry verdict of that compaction: this call
// ended the compaction, no model turn of the compaction still runs, and no
// turn end applied the verdict first. A model turn that outlives the reply
// applies the verdict at its own end, after its turn-end row.
func (a *Agent) finishNativeCompaction(revision uint64) bool {
	a.stateMu.Lock()
	if a.compactingRevision != revision {
		a.stateMu.Unlock()
		return false
	}
	a.compactingRevision = 0
	if a.activityRevision == revision {
		a.active = false
		a.turnCompaction = 0
	}
	applies := !a.active && a.claimCompactionVerdictLocked(revision)
	state := a.nativeTurnStateLocked()
	sequence := a.turnOrder.NextTurnSeq()
	a.stateMu.Unlock()
	providerkit.PublishTurnStateTo(a.sink, state, sequence)
	return applies
}

// copilotBlobAttachments encodes attachments as the inline `blob` attachments of
// `session.send`. The echo of the message states them again
// (reportUnreadDocuments).
func copilotBlobAttachments(attachments []*leapmuxv1.Attachment) []map[string]string {
	classified := agent.ClassifyAttachments(attachments)
	blobs := make([]map[string]string, 0, len(classified))
	for _, attachment := range classified {
		blobs = append(blobs, map[string]string{
			"type": copilotAttachmentBlob, "displayName": attachment.Filename, "mimeType": attachment.MIMEType,
			"data": base64.StdEncoding.EncodeToString(attachment.Data),
		})
	}
	return blobs
}

func (a *Agent) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInputForSession(nil, content, attachments)
}

// CompactContext asks the native runtime to summarize the current session.
// The RPC can take a model turn, so the response ends the active state after
// the request leaves this method. The runtime supplies the transcript notices.
func (a *Agent) CompactContext() error {
	a.sessionMu.RLock()
	defer a.sessionMu.RUnlock()
	if a.IsStopped() {
		return fmt.Errorf("the Copilot process is stopped")
	}
	a.stateMu.Lock()
	if a.active || a.compactingRevision != 0 {
		a.stateMu.Unlock()
		return agent.ErrAgentBusy
	}
	sessionID := a.sessionID
	if sessionID == "" {
		a.stateMu.Unlock()
		return fmt.Errorf("the Copilot session is unavailable")
	}
	a.active = true
	a.activityRevision++
	revision := a.activityRevision
	a.compactingRevision = revision
	a.turnCompaction = revision
	a.stateMu.Unlock()
	a.PublishTurnActive()

	// The two failures below leave before the request reaches the runtime. No
	// compaction turn started, so the dispatcher receives the error and no
	// retry verdict applies.
	params, err := json.Marshal(map[string]any{"sessionId": sessionID, "trigger": "manual"})
	if err != nil {
		a.finishNativeCompaction(revision)
		return fmt.Errorf("encode Copilot compaction request: %w", err)
	}
	err = a.SendDetachedRequest("session.history.compact", params, func(raw json.RawMessage, requestErr error) {
		a.endNativeCompaction(revision, sessionID, raw, requestErr)
	})
	if err != nil {
		providerkit.EndCompaction(a.sink)
		a.finishNativeCompaction(revision)
		return providerkit.ClassifyJSONRPCDeliveryError("session.history.compact", err)
	}
	return nil
}

// endNativeCompaction handles the reply to the session.history.compact request
// of revision, which sessionID sent. It reports a failed compaction, releases
// the queue, and then applies the compaction's retry verdict. A compaction never
// schedules a retry, whatever its outcome (CompactionEnd). The verdict follows
// the release, as it does at a root turn end. A reply that arrives after the
// process stopped or the session changed applies no verdict, as a turn that
// ends on those paths applies none.
func (a *Agent) endNativeCompaction(revision uint64, sessionID string, raw json.RawMessage, requestErr error) {
	a.sessionMu.RLock()
	defer a.sessionMu.RUnlock()
	if a.IsStopped() || a.currentNativeSessionID() != sessionID {
		a.finishNativeCompaction(revision)
		return
	}
	if err := copilotCompactionFailure(raw, requestErr); err != nil {
		a.reportNativeCompactionError(err)
	}
	if a.finishNativeCompaction(revision) {
		providerkit.ApplyTurnFailure(a.sink, providerkit.CompactionEnd(), nil)
	}
}

// copilotCompactionFailure returns the failure that the reply to
// session.history.compact states, and nil for a compaction that succeeded.
func copilotCompactionFailure(raw json.RawMessage, requestErr error) error {
	if requestErr != nil {
		return requestErr
	}
	var result struct {
		Success *bool `json:"success"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Success == nil {
		return fmt.Errorf("the Copilot runtime returned no valid compaction result")
	}
	if !*result.Success {
		return fmt.Errorf("the Copilot runtime did not compact the context")
	}
	return nil
}

func (a *Agent) reportNativeCompactionError(err error) {
	providerkit.EndCompaction(a.sink)
	a.sink.PersistLeapMuxNotification(map[string]any{
		contracts.NotificationFieldType:  contracts.NotificationTypeAgentError,
		contracts.NotificationFieldError: fmt.Sprintf("Copilot could not compact the context: %v", err),
	})
}

func (a *Agent) sendInputForSession(expected *string, content string, attachments []*leapmuxv1.Attachment) error {
	a.sessionMu.RLock()
	defer a.sessionMu.RUnlock()
	if a.IsStopped() {
		return fmt.Errorf("the Copilot process is stopped")
	}
	a.stateMu.Lock()
	if err := providerkit.CheckInputSession(expected, a.sessionID); err != nil {
		a.stateMu.Unlock()
		return err
	}
	if a.active || a.compactingRevision != 0 {
		a.stateMu.Unlock()
		return agent.ErrAgentBusy
	}
	if a.sessionID == "" {
		a.stateMu.Unlock()
		return fmt.Errorf("the Copilot session is unavailable")
	}
	a.active = true
	// A conversation turn belongs to no compaction.
	a.turnCompaction = 0
	a.activityRevision++
	revision := a.activityRevision
	fleet := a.fleetOnLocked()
	a.stateMu.Unlock()
	a.PublishTurnActive()
	values := map[string]any{"prompt": content}
	if len(attachments) > 0 {
		values["attachments"] = copilotBlobAttachments(attachments)
	}
	if fleet {
		return a.startFleetTurn(values, revision)
	}
	raw, err := a.requestNativeSession("send", values)
	if err != nil {
		var rejection *providerkit.JSONRPCResponseError
		if errors.As(err, &rejection) {
			a.rejectNativeInput(revision)
		}
		return providerkit.ClassifyJSONRPCDeliveryError("session.send", err)
	}
	var response struct {
		MessageID string `json:"messageId"`
	}
	if json.Unmarshal(raw, &response) != nil || response.MessageID == "" {
		return fmt.Errorf("%w: Copilot omitted the delivered message ID", agent.ErrDeliveryUncertain)
	}
	return nil
}

// startFleetTurn starts a new turn as a fleet request, which tells the model to
// dispatch subagents in parallel to do the work. Copilot answers whether the
// request started; a request that did not start ends the turn that the send
// began. The caller holds sessionMu for reading.
func (a *Agent) startFleetTurn(values map[string]any, revision uint64) error {
	raw, err := a.requestNativeSession("fleet.start", values)
	if err != nil {
		err = providerkit.ClassifyJSONRPCDeliveryError("session.fleet.start", err)
		if !errors.Is(err, agent.ErrDeliveryUncertain) {
			a.rejectNativeInput(revision)
		}
		return err
	}
	var response struct {
		Started *bool `json:"started"`
	}
	if json.Unmarshal(raw, &response) != nil || response.Started == nil {
		return fmt.Errorf("%w: Copilot omitted whether the fleet request started", agent.ErrDeliveryUncertain)
	}
	if !*response.Started {
		a.rejectNativeInput(revision)
		return fmt.Errorf("copilot did not start the fleet request")
	}
	return nil
}

// SupportsSteering reports true outside compaction. An ordinary Copilot turn
// accepts session.send with mode:"immediate"; a summary RPC does not.
func (a *Agent) SupportsSteering() bool {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	return a.compactingRevision == 0
}

// SteerRowAtEcho writes the queue row when Copilot echoes the prompt.
func (*Agent) SteerRowAtEcho() bool { return true }

// SteerSurvivesTurnEnd keeps a late steer in Copilot's native queue.
func (*Agent) SteerSurvivesTurnEnd() bool { return true }

// SteerInput injects a user message into the RUNNING turn with Copilot's
// SendMode "immediate".
//
// The turn's activity bookkeeping is NOT touched: the turn this steers is the
// one already recorded active, and a rejected steer must not read as the turn
// ending. Contrast sendInputForSession, which owns the idle->active transition
// and therefore owns the revision it can roll back.
func (a *Agent) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
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
	compacting := a.compactingRevision != 0
	a.stateMu.Unlock()
	if compacting {
		return agent.ErrAgentBusy
	}
	if !active {
		return agent.ErrNoActiveTurn
	}
	values := map[string]any{"prompt": content, "mode": "immediate"}
	if len(attachments) > 0 {
		values["attachments"] = copilotBlobAttachments(attachments)
	}
	ticket := a.steers.Add(content)
	accepted := false
	defer func() {
		if !accepted {
			a.steers.Forget(ticket)
			a.steers.Settle(a.sink)
		}
	}()
	raw, err := a.requestNativeSession("send", values)
	if err != nil {
		return providerkit.ClassifyJSONRPCDeliveryError("session.send", err)
	}
	var response struct {
		MessageID string `json:"messageId"`
	}
	if json.Unmarshal(raw, &response) != nil || response.MessageID == "" {
		return fmt.Errorf("%w: Copilot omitted the delivered message ID", agent.ErrDeliveryUncertain)
	}
	a.steers.SetID(ticket, response.MessageID)
	accepted = true
	return nil
}

// Interrupt aborts the running turn with Copilot's own `session.abort`.
//
// A stopped agent REFUSES, the way every other provider in the roster refuses.
// The worker's InterruptAgent handler reads that refusal as "not running" and
// says so; a nil here reported a stop that never happened, and the handler then
// withdrew the prompts of a process that had already gone.
func (a *Agent) Interrupt(stop agent.StopContext) error {
	a.sessionMu.RLock()
	defer a.sessionMu.RUnlock()
	if a.IsStopped() {
		return fmt.Errorf("agent is stopped")
	}
	for _, pending := range a.steers.PendingEntries() {
		if pending.ID == "" {
			continue
		}
		raw, err := a.requestNativeSession("queue.withdrawMessage", map[string]any{
			"messageId": pending.ID, "expectedPrompt": pending.Key,
		})
		if err != nil {
			slog.Warn("withdraw a Copilot steer before the stop", "agent_id", a.AgentID(), "error", err)
			continue
		}
		var response struct {
			Removed *bool `json:"removed"`
		}
		if json.Unmarshal(raw, &response) != nil || response.Removed == nil {
			slog.Warn("Copilot returned no steer withdrawal result", "agent_id", a.AgentID())
			continue
		}
		if *response.Removed {
			a.steers.Drop(pending.Ticket)
		}
	}
	a.steers.Settle(a.sink)
	_, err := a.requestNativeSession("abort", nil)
	return err
}

func (a *Agent) Stop() {
	a.NoteIntentionalStop()
	a.samplingContext()
	a.samplingCancel()
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
			_, err = a.SendRequest("session.suspend", params, 2*time.Second)
		}
		if err != nil {
			slog.Debug("Suspend Copilot before process exit", "agent_id", a.AgentID(), "error", err)
		}
	}
	a.copilotConnection.Stop()
	a.outputMu.Lock()
	a.clearNativeChildren()
	a.clearNativeControls()
	a.outputMu.Unlock()
	a.clearNativeCompaction()
	a.setNativeTurnActive(false)
}

func (a *Agent) Wait() error {
	err := a.copilotConnection.Wait()
	a.outputMu.Lock()
	a.closing = true
	a.clearNativeChildren()
	a.clearNativeControls()
	a.outputMu.Unlock()
	a.forgetNativeControlEvents()
	a.clearNativeCompaction()
	a.setNativeTurnActive(false)
	return err
}

func (a *Agent) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInputForSession(&sessionID, content, attachments)
}

// samplingContext supplies one cancellation scope for sampling on this process.
// Stop cancels it before it takes sessionMu. Native shutdown keeps the process context.
func (a *Agent) samplingContext() context.Context {
	a.samplingOnce.Do(func() {
		a.samplingCtx, a.samplingCancel = context.WithCancel(a.Context())
	})
	return a.samplingCtx
}
