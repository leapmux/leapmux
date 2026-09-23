package pi

import (
	"encoding/base64"
	"encoding/json"
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

// Agent manages a single `pi --mode rpc` process.
//
// Pi's wire format is JSONL with strict LF framing but it is NOT JSON-RPC 2.0:
// commands carry an opaque string `id`, and responses echo it on a flat
// {type:"response", command, success, data, error} envelope. Agent does
// not embed JSONRPCProcess because the marshal/decode shape diverges; it
// shares only the pending-map mechanics via Correlator[string].
type Agent struct {
	providerkit.Process
	providerkit.Correlator[string]

	// Pi's underlying LLM provider (e.g. "openai-codex"). Persisted via
	// options[OptionProvider] so model-switch RPCs round-trip with the
	// correct provider field across restarts.
	provider string

	model         string
	thinkingLevel string // stored as the agent's "effort"
	workingDir    string
	sink          agent.ProviderServices

	sessionID   string // Pi's runtime sessionId (rotates on new_session)
	sessionFile string // Pi's persistent session file path (durable identifier)
	sessionMu   sync.Mutex
	// currentTurnActive is true while the turn is open. A run that Pi will
	// auto-retry (agent_end with willRetry) keeps it true, because Pi restarts
	// that run itself: Interrupt must still send an abort, and a user message
	// must still steer rather than queue.
	currentTurnActive bool
	// interruptRequested records that the user stopped the running turn, so the
	// agent_end that ends it reads as an interruption rather than as the failure
	// its stop reason claims. Guarded by a.Mu. See noteInterruptRequested.
	interruptRequested bool
	// turnStartedAt marks when the turn's FIRST agent_start arrived, so
	// agent_end can report the turn's wall time. Pi's agent_end carries no
	// duration of its own. Zero between turns, and zero for a turn whose start
	// this worker never saw -- the divider then omits the duration.
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

	// nowFn supplies the clock that times a turn. Production leaves it nil; a
	// test installs a fixed clock so the reported duration is exact.
	nowFn func() time.Time
}

// now reads the agent's clock. The zero value must work, because the tests
// build an Agent as a struct literal and never reach Start.
func (a *Agent) now() time.Time {
	if a.nowFn != nil {
		return a.nowFn()
	}
	return time.Now()
}

// sessionHandleLocked returns the durable session identifier — preferring
// `sessionFile` (the path `pi --session` reopens across restarts) and falling
// back to the rotating runtime `sessionId`. Both shapes resume: `--session`
// matches a bare ID against this working directory's sessions. The file wins
// because it identifies the session from any directory, and it survives the ID
// rotation that new_session performs.
// Caller must hold a.Mu.
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
	// The ID and the path identify ONE session, so a new ID replaces both. Guarding
	// them apart retained the previous session's path whenever the reply carried a new
	// ID and no path: UpdateSessionID then persisted a resume handle for the session
	// that Pi replaced, and the goal reader's header check refused that file forever.
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

	classified := agent.ClassifyAttachments(attachments)

	var messageBuilder strings.Builder
	if content != "" {
		messageBuilder.WriteString(content)
	}
	images := make([]map[string]any, 0)
	for _, attachment := range classified {
		switch attachment.Kind {
		case agent.AttachmentKindText:
			if messageBuilder.Len() > 0 {
				messageBuilder.WriteString("\n\n")
			}
			messageBuilder.WriteString(providerkit.BuildInlineTextAttachmentBlock(attachment))
		case agent.AttachmentKindImage:
			images = append(images, map[string]any{
				"type":     "image",
				"data":     base64.StdEncoding.EncodeToString(attachment.Data),
				"mimeType": attachment.MIMEType,
			})
		case agent.AttachmentKindPDF, agent.AttachmentKindBinary:
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

	// The prompt response arrives at turn end. The queue needs only the stdin
	// write as delivery acceptance, so the response wait runs separately.
	return nil, a.sendPiCommandDetached(CommandPrompt, payload, func(err error) {
		if err != nil {
			a.handlePiPromptFailure(err, steer)
		}
	})
}

func (a *Agent) handlePiPromptFailure(err error, steer bool) {
	if a.IsStopped() {
		// Stop owns incomplete-output persistence. A detached waiter can fail
		// after the process closes, but that is not a second visible error.
		return
	}
	if !steer {
		a.flushPiGeneration(agent.MessageCompletionError)
		a.persistIncompletePiTools(agent.MessageCompletionError)
		a.sink.ReportProgress(agent.ResetProgress())
	}
	slog.Error("pi prompt failed", "agent_id", a.AgentID(), "steer", steer, "error", err)
	a.sink.PersistLeapMuxNotification(map[string]any{
		contracts.NotificationFieldType:  contracts.NotificationTypeAgentError,
		contracts.NotificationFieldError: err.Error(),
	})
}

// Stop sends an abort to the running turn (when one is in flight), then tears
// down the process via Process.Stop. Abort is issued synchronously (with a
// short timeout) before Process.Stop sets stopped=true and closes stdin —
// running it on a goroutine instead would race the stopped-check inside
// sendPiCommand and drop the abort in the common case.
func (a *Agent) Stop() {
	a.NoteIntentionalStop()
	a.stopPiGoalRefresh()
	a.Mu.Lock()
	stopped := a.StoppedLocked()
	turnActive := a.currentTurnActive
	a.clearPiQuestionStateLocked()
	a.Mu.Unlock()
	if !stopped && turnActive {
		// Best-effort. Failures (timeout, write error, server-side false)
		// fall through to the hard tear-down below.
		_, _ = a.sendPiCommand(CommandAbort, nil, 1*time.Second)
	}
	a.Process.Stop()
	a.flushPiGeneration(agent.MessageCompletionInterrupted)
	a.persistIncompletePiTools(agent.MessageCompletionInterrupted)
	a.sink.ReportProgress(agent.ResetProgress())
}

// Wait retains unfinished model output after an unexpected process exit.
func (a *Agent) Wait() error {
	err := a.Process.Wait()
	a.stopPiGoalRefresh()
	completion := a.ProcessExitCompletion()
	a.flushPiGeneration(completion)
	a.persistIncompletePiTools(completion)
	a.sink.ReportProgress(agent.ResetProgress())
	return err
}

// Interrupt aborts the running Pi turn by sending the `abort`
// command. Pi's wire format uses {type:"abort"} per the
// piProvider.IsInterrupt classifier; sendPiCommand applies the
// envelope.
//
// No-op when no turn is active so scripts can invoke this without
// probing currentTurnActive first.
func (a *Agent) Interrupt() error {
	a.noteInterruptRequested()
	a.Mu.Lock()
	stopped := a.StoppedLocked()
	turnActive := a.currentTurnActive
	a.clearPiQuestionStateLocked()
	a.Mu.Unlock()
	if stopped {
		return fmt.Errorf("agent is stopped")
	}
	if !turnActive {
		return nil
	}
	// Short timeout — Pi acks aborts quickly; longer waits would just
	// extend the apparent latency of a user-driven interrupt.
	_, err := a.sendPiCommand(CommandAbort, nil, 1*time.Second)
	return err
}

// noteInterruptRequested records that the user stopped the RUNNING turn.
//
// Pi spells one stop two ways. A turn it aborts cleanly reports
// `stopReason: "aborted"`, and a turn whose tool was still running reports
// `stopReason: "error"` with `errorMessage: "This operation was aborted"` -- the
// same shape as a genuine failure. The frame therefore cannot tell an
// interruption from a failure, and the second read as "Turn failed" in the
// danger color for a stop the reader asked for. LeapMux can tell them apart,
// because it sent the abort.
//
// The note is taken only while a turn runs. Pi acknowledges an abort sent
// outside a turn and ends no turn for it, so a note taken there would wait and
// then mislabel the NEXT turn.
func (a *Agent) noteInterruptRequested() {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	if a.currentTurnActive {
		a.interruptRequested = true
	}
}

// takeInterruptRequest reports whether the turn that is ending was interrupted,
// and clears the note. One agent_end ends one turn, so the note is spent there.
func (a *Agent) takeInterruptRequest() bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	interrupted := a.interruptRequested
	a.interruptRequested = false
	return interrupted
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
	stateRaw, err := a.sendPiCommand(CommandGetState, nil, a.APITimeout())
	if err != nil {
		return "", fmt.Errorf("read the new Pi session: %w", err)
	}
	a.flushPiGeneration(agent.MessageCompletionInterrupted)
	a.persistIncompletePiTools(agent.MessageCompletionInterrupted)
	a.applyStateResponse(stateRaw)
	a.Mu.Lock()
	a.currentTurnActive = false
	// Drop the turn's start mark with the session. agent_start takes a mark only
	// when the agent holds none, so that a retried run keeps the turn's original
	// start. A mark that survived the turn this clear replaced would then make
	// the NEXT turn's divider report the time since the old turn began.
	a.turnStartedAt = time.Time{}
	a.sessionCostUsd = 0
	a.sessionCostKnown = false
	a.latestContextUsage = nil
	a.usageGeneration++
	// Drop the per-tool-call side tables with the session. tool_execution_end is
	// their only other removal, and it never arrives for a call the replaced
	// session was still running -- so without this a spawn prompt is retained for
	// the life of the process, and a reused tool-call id would open the next
	// transcript on the previous session's instruction (mirrors
	// Base.ClearContext).
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
