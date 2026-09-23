package pi

import (
	"cmp"
	"context"
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
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// piBinaryCandidates lists the executable names to probe for Pi.
var piBinaryCandidates = []string{"pi"}

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

// piResumeArgs builds the `--session` argument that reopens a prior Pi session,
// or nothing when there is no handle to reopen.
//
// Resume happens at LAUNCH and not through a switch_session RPC after startup,
// for two reasons. `--session` reaches Pi's own resolver, which takes either
// shape of handle -- a session file path, or a bare session ID that it matches
// against the sessions of this working directory -- while the RPC takes a path
// and nothing else. And the RPC does not fail on a path that identifies no file: Pi
// starts an EMPTY session at that path and answers success, so a handle that
// identified no session became a new file in the working directory and looked like a
// resume.
//
// The value this reads is NOT the one OpenAgent validated.
// agentOutputSink.UpdateSessionID writes whatever Pi reports into the
// `agent_session_id` column, and resolveResumeSessionID hands that column back
// here on every restart. So the rule runs again at the argv sink, which is the
// same split claudeResumeArgs documents. A handle that fails the rule fails the
// start; see providerkit.ResumeFailedError.
//
// It sends the handle that ResolveResumeHandle RETURNS, never the stored one.
// The path rule normalizes as it checks -- it drops control characters, trims
// edge whitespace, expands `~` and cleans the path -- and Pi opens a session
// file without requiring that it exists, so sending the stored string started
// an EMPTY session at a filename nobody typed whenever the two differed.
//
// One case reaches Pi and this cannot answer it: a bare session ID that matches
// no session of THIS working directory, but does match one somewhere else. Pi
// then asks on stdin whether to fork it, nothing answers in RPC mode, and the
// startup handshake fails on the get_state timeout. That failure is visible,
// unlike the empty session the RPC path created.
func piResumeArgs(resumeSessionID, homeDir string) ([]string, error) {
	if resumeSessionID == "" {
		return nil, nil
	}
	resolved, err := (piProvider{}).ResolveResumeHandle(resumeSessionID, homeDir)
	if err != nil {
		return nil, providerkit.ResumeFailedError(resumeSessionID,
			fmt.Errorf("the stored Pi session handle is not valid: %w", err))
	}
	return []string{"--session", resolved}, nil
}

// Start starts a `pi --mode rpc` process and performs the startup handshake.
//
// A resume Pi cannot honour fails the WHOLE start, and that is deliberate.
// `--session` is on the launch line rather than in a post-startup RPC, so Pi
// settles the session before it enters RPC mode: it exits 1 when no session
// matches the handle, and exits 1 in RPC mode when the session file identifies a
// working directory that no longer exists — which happens here whenever a git
// worktree is removed. The switch_session step this replaced warned and
// continued on a fresh session instead; the launch flag cannot, because the
// process is already gone by the time the handshake times out.
//
// The failure is visible, which the RPC path's was not: switch_session answered
// SUCCESS for a path that named no file, so Pi wrote a new empty session there
// and the user saw a resumed tab with no history. Nothing clears
// `agent_session_id` after a failed start, so a stored handle that Pi refuses
// keeps failing until `/clear` replaces it -- which is what providerkit.ResumeFailedError
// tells the user to send.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	ctx, cancel := context.WithCancel(ctx)

	launchSpec, err := providerkit.ResolveLaunch(ctx, opts, leapmuxv1.AgentProvider_AGENT_PROVIDER_PI, piLocator)
	if err != nil {
		cancel()
		return nil, err
	}
	resumeArgs, err := piResumeArgs(opts.ResumeSessionID, opts.HomeDir)
	if err != nil {
		cancel()
		return nil, err
	}
	// Pi has no --working-dir flag (it uses the process cwd). Wrap
	// already sets cmd.Dir to opts.WorkingDir, so the agent picks up the right
	// directory implicitly.
	cmd, preambleDelimiter, metaPrefix := launch.Wrap(ctx, launch.WrapSpec{
		Shell:      opts.Shell,
		LoginShell: opts.LoginShell,
		Launch:     launchSpec,
		BaseArgs:   append([]string{"--mode", "rpc"}, resumeArgs...),
		WorkingDir: opts.WorkingDir,
	})
	cmd.Env = providerkit.FinalizeAgentEnv(cmd.Environ(), opts)

	stdin, stdout, stderrPipe, err := providerkit.SetupProcessPipes(cmd, cancel)
	if err != nil {
		return nil, err
	}

	a := &Agent{
		Process:       providerkit.NewProcess(opts, "pi", cmd, stdin, ctx, cancel, preambleDelimiter, metaPrefix),
		model:         opts.Model(),
		thinkingLevel: opts.Effort(),
		provider:      cmp.Or(opts.Options[OptionProvider], DefaultProvider),
		workingDir:    opts.WorkingDir,
		sink:          sink,
	}
	a.sink = agent.NewModelProgressResetSink(newPiToolTranscript(ctx, a.sink))

	if err := a.StartCmd(cmd, cancel); err != nil {
		return nil, err
	}
	a.DrainStderr(stderrPipe)

	scanner := agent.NewStdoutScanner(stdout)
	go a.ReadOutput(scanner, a.handlePiResponse, a.handleOutput)

	cleanup := func() {
		a.Stop()
		_ = a.Wait()
	}

	timeout := opts.EffectiveStartupTimeout()

	// 1. get_state — confirms the process is alive and yields the session
	//    handle plus the in-process model/thinking values that act as the
	//    starting point for any opts overrides. A resume already happened:
	//    `--session` selected the session before Pi entered RPC mode, so this
	//    reports the resumed session's file and needs no follow-up.
	stateRaw, err := a.sendPiCommand(CommandGetState, nil, timeout)
	if err != nil {
		cleanup()
		return nil, a.FormatStartupError(CommandGetState, err)
	}
	a.applyStateResponse(stateRaw)
	commandsKnown := a.refreshPiCommands(timeout)
	a.schedulePiGoalRefresh(true)

	// 2. get_available_models — best-effort; failure logs and continues.
	modelsRaw, err := a.sendPiCommand(CommandGetAvailableModels, nil, timeout)
	if err != nil {
		slog.Warn("pi get_available_models failed", "agent_id", a.AgentID(), "error", err)
	} else {
		a.applyAvailableModels(modelsRaw)
	}

	// 3. set_model if the requested model differs from current.
	if model := opts.Model(); model != "" && model != a.model {
		if err := a.applyModel(model, a.providerForModel(model), timeout); err != nil {
			slog.Warn("pi set_model on startup failed", "agent_id", a.AgentID(), "model", model, "error", err)
		}
	}

	// 4. set_thinking_level if the requested effort is concrete.
	if effort := opts.Effort(); effort != "" && effort != agent.EffortAuto && effort != a.thinkingLevel {
		if err := a.applyThinkingLevel(effort, timeout); err != nil {
			slog.Warn("pi set_thinking_level on startup failed", "agent_id", a.AgentID(), "level", effort, "error", err)
		}
	}

	a.Mu.Lock()
	sessionHandle := a.sessionHandleLocked()
	a.Mu.Unlock()
	sink.UpdateSessionID(sessionHandle)
	sink.BroadcastStatusActive(sessionHandle)
	// Best-effort: hydrate cost/context for resumed Pi sessions immediately
	// on a goroutine so startup readiness is not gated on a usage RPC.
	// Failures are non-fatal; message_end / agent_end keep updating usage.
	go func() {
		// One failed catalog read at startup would otherwise switch goal control and
		// the extension-command dispatch off for the life of the process, because
		// nothing else asks again until the session changes.
		if !commandsKnown {
			a.refreshPiGoalControl()
		}
		_, _ = a.refreshPiSessionStats(piSessionStatsTimeout(timeout))
	}()

	return a, nil
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

// piLocator finds the Pi CLI on the user's PATH, the preferred name first.
var piLocator = launch.Binaries(piBinaryCandidates...)

// Registration states everything the worker knows about Pi before any of its
// agents runs.
func Registration() agent.Registration {
	return agent.Registration{
		Provider:      leapmuxv1.AgentProvider_AGENT_PROVIDER_PI,
		Plugin:        piProvider{},
		Start:         Start,
		Locator:       piLocator,
		DefaultModels: piDefaultModels,
		// No static option groups; thinking levels live on each model.
		OptionGroups: nil,
		// Pi's model-dependent group is its thinking level, labeled "Thinking Level"
		// rather than the default "Effort" -- so the not-running static fallback and the
		// model-switch sub_groups match the live OptionGroups (see Agent.OptionGroups).
		ModelSubGroups: agent.EffortSubGroupsLabeled(ThinkingLevelLabel),
		// model + effort (the "Thinking Level" axis). Pi has no permission-mode axis.
		AdditionalOptionIDs: []string{agent.OptionIDEffort},
		// pi_provider (the underlying LLM provider Pi folds into its model selection) is
		// persisted by LeapMux but never surfaced as a group, so its absence from a confirmed
		// catalog is by design -- confirmedOptions preserves it rather than reconciling it away.
		PersistedOnlyOptionIDs: []string{OptionProvider},
		EnvModelKey:            "LEAPMUX_PI_DEFAULT_MODEL",
		EnvEffortKey:           "LEAPMUX_PI_DEFAULT_EFFORT",
	}
}

func (a *Agent) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInputForSession(&sessionID, content, attachments, false)
}
