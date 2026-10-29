package pi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Agent manages a single pi --mode rpc process and its remote procedure call (RPC) transport.
//
// Pi uses JSON Lines (JSONL) with strict line feed (LF) framing.
// It does not use JSON-RPC 2.0. Commands carry an opaque string id.
// Responses echo that id in a flat {type:"response", command, success, data, error} envelope.
// Agent does not embed JSONRPCProcess because the marshal/decode shape differs.
// It shares only the pending-map mechanics through Correlator[string].
type Agent struct {
	providerkit.Process
	providerkit.Correlator[string]

	// Pi's underlying model provider, for example, "openai-codex".
	// options[OptionProvider] saves it so model-switch RPCs retain the correct provider across restarts.
	provider string

	model         string
	thinkingLevel string // stored as the agent's "effort"
	workingDir    string
	sink          agent.ProviderServices

	sessionID   string // Pi's runtime sessionId (rotates on new_session)
	sessionFile string // Pi's persistent session file path (durable identifier)
	sessionMu   sync.Mutex
	// currentTurnActive is true while the turn is open.
	// Each agent_end keeps it true until agent_settled ends all runs and compaction.
	// Interrupt remains available during that interval.
	currentTurnActive bool
	compactionLive    bool
	// Each write attempt retains its own delivery outcome. A failed write removes
	// only that attempt. Attempt IDs continue across turns.
	// Mu protects the ledger and counters. interruptOutput uses its own mutex.
	interruptRequests map[uint64]piInterruptDelivery
	interruptAttempt  uint64
	turnGeneration    uint64
	interruptOutput   piInterruptOutput
	heldDivider       *piHeldDivider
	steers            providerkit.SteerLedger
	// turnStartedAt records the first agent_start until agent_settled.
	// The final divider includes all runs and compaction in that interval.
	// A zero value means the worker did not observe the start, so the divider omits duration.
	turnStartedAt time.Time
	// Pi exposes token/cost information in assistant messages and via
	// get_session_stats. Keep the latest normalized snapshot here so persisted
	// message_end / agent_end events can rehydrate the frontend after reconnect.
	sessionCostUsd        float64
	sessionCostKnown      bool
	latestContextUsage    map[string]any
	usageGeneration       uint64
	sessionStatsMu        sync.Mutex
	generationBuffer      providerkit.GenerationBuffer
	toolStates            map[string]*piToolState
	nextToolOrder         uint64
	questionDialogs       map[string]*piQuestionSource
	customQuestionAnswers map[piQuestionKey]*piCustomQuestionAnswer
	questionGeneration    uint64
	// openDialogs is the published dialogs that Pi still waits on, so an interrupt
	// can answer each one (see settleOpenDialogs). Guarded by a.Mu.
	openDialogs    map[string]uint64
	dialogRevision uint64
	dialogCancelMu sync.Mutex
	// freshImplementationPending records that a plan menu was answered with
	// the fresh-implementation choice, so the settings dialog that follows is
	// answered by the worker rather than published. Guarded by a.Mu. See
	// fresh_implementation.go.
	freshImplementationPending bool
	goal                       piGoalSync
	extensionCommands          map[string]bool

	availableModels []*agent.ModelInfo
	// modelProviders maps modelID -> underlying provider (e.g.
	// "openai-codex"). Populated alongside availableModels so set_model RPCs
	// can ship the correct {provider, modelId} pair without round-tripping
	// the provider name through user-visible strings.
	modelProviders map[string]string

	// nextReqID mints monotonic ids; we stringify them at register time so
	// the correlator's key type stays narrow even though we generate from
	// an int64 atom.
	nextReqID atomic.Int64

	// toolCallPrompts records toolCallId -> the spawn's FULL prompt (the
	// description above is a one-line label). Held until the background re-key
	// creates the child transcript, so a background Pi subagent's tab opens on
	// the instruction it was given. Dropped with the description on
	// tool_execution_end, and cleared when the session is replaced.
	toolCallPrompts providerkit.PendingPrompts

	// dialogDeadlines withdraws a dialog whose deadline passed. See
	// publishPiDialog.
	dialogDeadlines providerkit.ControlDeadlines
}

// orderedSink keeps native rows and turn-release effects behind pending generation writes.
func (a *Agent) orderedSink() agent.ProviderServices {
	queue := agent.OutputQueueFor(a.sink)
	if queue == nil {
		queue = a.generationBuffer.OutputQueue()
	}
	return agent.NewOrderedProviderServices(queue, a.sink)
}

// sessionHandleLocked returns the durable session identifier.
// It prefers sessionFile, which pi --session reopens from any directory and retains across native ID changes.
// Without a file, it returns sessionId. Pi resolves that ID within the working directory.
// The caller must hold a.Mu.
func (a *Agent) sessionHandleLocked() string {
	if a.sessionFile != "" {
		return a.sessionFile
	}
	return a.sessionID
}

// applyStateResponse populates session/model fields from a get_state response.
func (a *Agent) applyStateResponse(raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	var state struct {
		Model struct {
			ID       string `json:"id"`
			Provider string `json:"provider"`
		} `json:"model"`
		ThinkingLevel string `json:"thinkingLevel"`
		SessionID     string `json:"sessionId"`
		SessionFile   string `json:"sessionFile"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		slog.Warn("pi get_state unmarshal failed", "agent_id", a.AgentID(), "error", err)
		return
	}
	a.goal.publishMu.Lock()
	defer a.goal.publishMu.Unlock()
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if state.Model.ID != "" {
		a.model = state.Model.ID
	}
	if state.Model.Provider != "" {
		a.provider = state.Model.Provider
	}
	if state.ThinkingLevel != "" {
		a.thinkingLevel = state.ThinkingLevel
	}
	a.applyPiSessionIdentityLocked(state.SessionID, state.SessionFile)
}

// applyPiSessionIdentityLocked shares native identity handling between state and usage replies.
// The caller holds a.Mu and goal.publishMu.
func (a *Agent) applyPiSessionIdentityLocked(sessionID, sessionFile string) bool {
	changed := (sessionID != "" && sessionID != a.sessionID) || (sessionFile != "" && sessionFile != a.sessionFile)
	// The ID and path identify one session, so a new ID replaces both.
	// Separate guards retained the old path when a reply carried a new ID without a path.
	// UpdateSessionID then saved the old session's resume handle.
	// The goal reader's header check rejected that old file indefinitely.
	switch {
	case sessionID != "" && sessionID != a.sessionID:
		a.sessionID = sessionID
		a.sessionFile = sessionFile
	case sessionFile != "":
		a.sessionFile = sessionFile
	}
	if changed {
		a.goal.revision++
	}
	return changed
}

// SendInput starts a regular Pi prompt. SteerInput sends explicit guidance
// with streamingBehavior:"steer" during an active turn.
func (a *Agent) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInput(content, attachments, false)
}

// Agent steers. Manager.SupportsSteering answers false, with no build error, for a
// provider that stops satisfying InputSteerer, so this assertion makes that
// regression a compile error.
var _ agent.InputSteerer = (*Agent)(nil)
var _ agent.SteerEchoReporter = (*Agent)(nil)
var _ agent.SteerSurvivor = (*Agent)(nil)

func (*Agent) SteerRowAtEcho() bool { return true }

func (*Agent) SteerSurvivesTurnEnd() bool { return true }

// SupportsSteering always reports true. Pi accepts a message with
// streamingBehavior:"steer" during any turn, so the capability needs no
// handshake discovery.
func (a *Agent) SupportsSteering() bool { return true }

func (a *Agent) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInput(content, attachments, true)
}

func (a *Agent) sendInput(content string, attachments []*leapmuxv1.Attachment, steer bool) error {
	return a.sendInputForSession(nil, content, attachments, steer)
}

func (a *Agent) sendInputForSession(expected *string, content string, attachments []*leapmuxv1.Attachment, steer bool) error {
	wait, err := a.preparePiInput(expected, content, attachments, steer)
	if err != nil {
		return err
	}
	if wait != nil {
		return wait()
	}
	return nil
}

// preparePiInput holds the session lock through validation and the command write, never through its response wait.
func (a *Agent) preparePiInput(expected *string, content string, attachments []*leapmuxv1.Attachment, steer bool) (func() error, error) {
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()
	a.Mu.Lock()
	if err := providerkit.CheckInputSession(expected, a.sessionHandleLocked()); err != nil {
		a.Mu.Unlock()
		return nil, err
	}
	if a.StoppedLocked() {
		a.Mu.Unlock()
		return nil, fmt.Errorf("agent is stopped")
	}
	turnActive := a.currentTurnActive
	a.Mu.Unlock()
	if steer && !turnActive {
		return nil, agent.ErrNoActiveTurn
	}
	if !steer && turnActive {
		return nil, agent.ErrAgentBusy
	}
	if instructions, compact := piCompactInstruction(content); compact && !steer {
		if len(attachments) > 0 {
			return nil, fmt.Errorf("pi compaction does not accept attachments")
		}
		payload := map[string]any{}
		if instructions != "" {
			payload["customInstructions"] = instructions
		}
		wait, err := a.beginPiCommand(CommandCompact, payload)
		if err != nil {
			return nil, err
		}
		return func() error {
			_, err := wait(0)
			if err != nil {
				a.endPiCompaction()
			}
			// A compaction never schedules a retry, whatever its outcome. The
			// command writes no turn-end row, and its one verdict precedes the
			// release, as at a settled turn (settleHeldPiTurnAt).
			providerkit.ApplyTurnFailure(a.orderedSink(), providerkit.CompactionEnd(), nil)
			// Compaction ends without agent_end. Publish the idle turn after the
			// native compact response so queued input can proceed.
			a.PublishTurnActive()
			return err
		}, nil
	}

	classified := agent.ClassifyAttachments(attachments)

	var messageBuilder strings.Builder
	if content != "" {
		messageBuilder.WriteString(content)
	}
	images := make([]map[string]any, 0)
	for _, attachment := range classified {
		switch attachment.Kind {
		case contracts.AttachmentMediaKindText:
			if messageBuilder.Len() > 0 {
				messageBuilder.WriteString("\n\n")
			}
			messageBuilder.WriteString(providerkit.BuildInlineTextAttachmentBlock(attachment))
		case contracts.AttachmentMediaKindImage:
			images = append(images, map[string]any{
				"type":     "image",
				"data":     base64.StdEncoding.EncodeToString(attachment.Data),
				"mimeType": attachment.MIMEType,
			})
		case contracts.AttachmentMediaKindPdf, contracts.AttachmentMediaKindBinary, contracts.AttachmentMediaKindAudio:
			// Pi's prompt payload carries text and images only, so the
			// attachment is omitted rather than sent as junk text.
		}
	}

	payload := map[string]any{
		"message": messageBuilder.String(),
	}
	if len(images) > 0 {
		payload["images"] = images
	}
	if steer {
		payload["streamingBehavior"] = StreamingBehaviorSteer
	} else {
		payload["streamingBehavior"] = StreamingBehaviorFollowUp
	}
	if steer {
		ticket := a.steers.Add(messageBuilder.String())
		wait, err := a.beginPiCommand(CommandPrompt, payload)
		if err != nil {
			a.forgetPiSteer(ticket)
			return nil, err
		}
		return func() error {
			raw, err := wait(a.APITimeout())
			if err != nil {
				a.forgetPiSteer(ticket)
				var refusal *piCommandRefusal
				if errors.As(err, &refusal) {
					return err
				}
				return fmt.Errorf("%w: Pi did not confirm steer delivery: %v", agent.ErrDeliveryUncertain, err)
			}
			if piPromptDisposition(raw) == "handled" {
				a.steers.Echo(ticket)
				a.steers.Settle(a.orderedSink())
			}
			return nil
		}, nil
	}
	if a.isPiExtensionCommand(messageBuilder.String()) {
		wait, err := a.beginPiCommand(CommandPrompt, payload)
		if err != nil {
			return nil, err
		}
		return func() error {
			_, err := wait(0)
			// Commands can finish without agent_end. Settle this dispatch before returning.
			a.PublishTurnActive()
			return err
		}, nil
	}

	// Pi answers preflight with started, queued, or handled. It sends no second reply.
	// The stdin write confirms delivery, so the response wait runs separately.
	return nil, a.sendPiCommandDetached(CommandPrompt, payload, func(raw json.RawMessage, err error) {
		if err != nil {
			a.handlePiPromptFailure(err)
		} else if piPromptDisposition(raw) == "handled" {
			a.PublishTurnActive()
		}
	})
}

func (a *Agent) forgetPiSteer(ticket providerkit.SteerTicket) {
	a.steers.Forget(ticket)
	a.steers.Settle(a.orderedSink())
}

func (a *Agent) handlePiPromptFailure(err error) {
	if a.IsStopped() {
		// Stop owns incomplete-output persistence. A detached waiter can fail
		// after the process closes, but that is not a second visible error.
		return
	}
	// A preflight refusal owns no output. Another native run may already be active.
	slog.Error("pi prompt failed", "agent_id", a.AgentID(), "error", err)
	a.orderedSink().PersistLeapMuxNotification(map[string]any{
		contracts.NotificationFieldType:  contracts.NotificationTypeAgentError,
		contracts.NotificationFieldError: err.Error(),
	})
	a.PublishTurnActive()
}

// ClearContext starts a fresh Pi session in-place.
//
// Pi's new_session response only includes a cancellation flag; we follow it
// with a get_state to pick up the new sessionFile path.
func (a *Agent) ClearContext() (string, error) {
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()
	raw, err := a.sendPiCommand(CommandNewSession, nil, a.APITimeout())
	if err != nil {
		return "", err
	}
	var response struct {
		Cancelled *bool `json:"cancelled"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return "", fmt.Errorf("read Pi new_session response: %w", err)
	}
	if response.Cancelled == nil {
		return "", fmt.Errorf("the Pi new_session response has no cancellation status")
	}
	if *response.Cancelled {
		return "", agent.ErrContextClearCancelled
	}
	a.steers.DropAll()
	a.steers.Settle(a.orderedSink())
	a.endPiCompaction()
	stateRaw, err := a.sendPiCommand(CommandGetState, nil, a.APITimeout())
	if err != nil {
		return "", fmt.Errorf("read the new Pi session: %w", err)
	}
	a.flushPiGeneration(agent.MessageCompletionInterrupted)
	a.persistIncompletePiTools(agent.MessageCompletionInterrupted)
	a.settleHeldPiTurn(agent.MessageCompletionInterrupted)
	a.applyStateResponse(stateRaw)
	a.Mu.Lock()
	a.currentTurnActive = false
	a.interruptRequests = nil
	a.turnGeneration++
	// Drop the turn's start mark with the session. agent_start takes a mark only
	// when the agent holds none, so that a retried run keeps the turn's original
	// start. A mark that survived the turn this clear replaced would then make
	// the NEXT turn's divider report the time since the old turn began.
	a.turnStartedAt = time.Time{}
	a.sessionCostUsd = 0
	a.sessionCostKnown = false
	a.latestContextUsage = nil
	a.usageGeneration++
	// Drop the per-tool-call side tables with the session.
	// Only tool_execution_end also removes those records.
	// It never arrives for a call that the replaced session still ran.
	// Without this reset, the process retains that spawn prompt indefinitely.
	// A reused tool-call ID would also open the new transcript with the old instruction.
	clear(a.toolStates)
	a.clearPiQuestionStateLocked()
	// The plan menu this mark refers to died with the replaced session.
	a.freshImplementationPending = false
	a.nextToolOrder = 0
	a.toolCallPrompts.Clear()
	handle := a.sessionHandleLocked()
	a.Mu.Unlock()
	a.PublishTurnActive()
	// The session was replaced. Clear all live progress before the next turn.
	a.sink.ReportProgress(agent.ResetProgress())
	if handle == "" {
		return "", fmt.Errorf("the new Pi session has no handle")
	}
	a.sink.UpdateSessionID(handle)
	// The replacement session can load a different extension set, so the catalog is
	// stale with the session.
	go a.refreshPiGoalControl()
	return handle, nil
}

func (a *Agent) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInputForSession(&sessionID, content, attachments, false)
}
