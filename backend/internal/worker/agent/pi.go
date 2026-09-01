package agent

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// piBinaryCandidates lists the executable names to probe for Pi.
var piBinaryCandidates = []string{"pi"}

// PiAgent manages a single `pi --mode rpc` process.
//
// Pi's wire format is JSONL with strict LF framing but it is NOT JSON-RPC 2.0:
// commands carry an opaque string `id`, and responses echo it on a flat
// {type:"response", command, success, data, error} envelope. PiAgent does
// not embed jsonrpcBase because the marshal/decode shape diverges; it
// shares only the pending-map mechanics via responseCorrelator[string].
type PiAgent struct {
	processBase
	responseCorrelator[string]

	// Pi's underlying LLM provider (e.g. "openai-codex"). Persisted via
	// options[PiOptionProvider] so model-switch RPCs round-trip with the
	// correct provider field across restarts.
	provider string

	model         string
	thinkingLevel string // stored as the agent's "effort"
	workingDir    string
	sink          OutputSink

	sessionID         string // Pi's runtime sessionId (rotates on new_session)
	sessionFile       string // Pi's persistent session file path (durable identifier)
	currentTurnActive bool   // true between agent_start and agent_end
	// thinkingTokens is the per-phase generated-token estimate driving the
	// thinking-indicator counter; see thinkingTokenEstimator and thinkingResetSink.
	thinkingTokens thinkingTokenEstimator

	// Pi exposes token/cost information in assistant messages and via
	// get_session_stats. Keep the latest normalized snapshot here so persisted
	// message_end / agent_end events can rehydrate the frontend after reconnect.
	sessionCostUsd     float64
	sessionCostKnown   bool
	latestContextUsage map[string]any
	usageGeneration    uint64

	availableModels []*ModelInfo
	// modelProviders maps modelID -> underlying provider (e.g.
	// "openai-codex"). Populated alongside availableModels so set_model RPCs
	// can ship the correct {provider, modelId} pair without round-tripping
	// the provider name through user-visible strings.
	modelProviders map[string]string

	// nextReqID mints monotonic ids; we stringify them at register time so
	// the correlator's key type stays narrow even though we generate from
	// an int64 atom.
	nextReqID atomic.Int64

	// toolCallDescriptions records toolCallId -> description (from the
	// tool_execution_start input) for the background-task registry title.
	// Guarded by mu. An entry is dropped on the matching tool_execution_end,
	// and the whole map is cleared when the session is replaced.
	toolCallDescriptions map[string]string
	// toolCallPrompts records toolCallId -> the spawn's FULL prompt (the
	// description above is a one-line label). Held until the background re-key
	// creates the child transcript, so a background Pi subagent's tab opens on
	// the instruction it was given. Dropped with the description on
	// tool_execution_end, and cleared when the session is replaced.
	toolCallPrompts pendingPrompts
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
// start; see resumeFailedError.
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
		return nil, resumeFailedError(resumeSessionID,
			fmt.Errorf("the stored Pi session handle is not valid: %w", err))
	}
	return []string{"--session", resolved}, nil
}

// StartPi starts a `pi --mode rpc` process and performs the startup handshake.
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
// keeps failing until `/clear` replaces it -- which is what resumeFailedError
// tells the user to send.
func StartPi(ctx context.Context, opts Options, sink OutputSink) (Agent, error) {
	ctx, cancel := context.WithCancel(ctx)

	launch, err := resolveProviderLaunch(ctx, opts.Shell, opts.LoginShell, leapmuxv1.AgentProvider_AGENT_PROVIDER_PI)
	if err != nil {
		cancel()
		return nil, err
	}
	resumeArgs, err := piResumeArgs(opts.ResumeSessionID, opts.HomeDir)
	if err != nil {
		cancel()
		return nil, err
	}
	// Pi has no --working-dir flag (it uses the process cwd). buildShellWrappedCommand
	// already sets cmd.Dir to opts.WorkingDir, so the agent picks up the right
	// directory implicitly.
	cmd, preambleDelimiter, metaPrefix := buildShellWrappedCommand(ctx, shellWrapSpec{
		Shell:      opts.Shell,
		LoginShell: opts.LoginShell,
		Launch:     launch,
		BaseArgs:   append([]string{"--mode", "rpc"}, resumeArgs...),
		WorkingDir: opts.WorkingDir,
	})
	cmd.Env = FinalizeAgentEnv(cmd.Environ(), opts)

	stdin, stdout, stderrPipe, err := setupProcessPipes(cmd, cancel)
	if err != nil {
		return nil, err
	}

	a := &PiAgent{
		processBase:   newProcessBase(opts, "pi", cmd, stdin, ctx, cancel, preambleDelimiter, metaPrefix),
		model:         opts.Model(),
		thinkingLevel: opts.Effort(),
		provider:      cmp.Or(opts.Options[PiOptionProvider], PiDefaultProvider),
		workingDir:    opts.WorkingDir,
		sink:          sink,
	}
	// Reset the thinking-token estimate centrally at every frontend-clear boundary.
	a.sink = newThinkingResetSink(a.sink, &a.thinkingTokens)

	if err := a.startCmd(cmd, cancel); err != nil {
		return nil, err
	}
	a.drainStderr(stderrPipe)

	scanner := newStdoutScanner(stdout)
	go a.readOutput(scanner, a.handlePiResponse, a.handleOutput)

	cleanup := func() {
		a.Stop()
		_ = a.Wait()
	}

	timeout := opts.startupTimeout()

	// 1. get_state — confirms the process is alive and yields the session
	//    handle plus the in-process model/thinking values that act as the
	//    starting point for any opts overrides. A resume already happened:
	//    `--session` selected the session before Pi entered RPC mode, so this
	//    reports the resumed session's file and needs no follow-up.
	stateRaw, err := a.sendPiCommand(PiCommandGetState, nil, timeout)
	if err != nil {
		cleanup()
		return nil, a.formatStartupError(PiCommandGetState, err)
	}
	a.applyStateResponse(stateRaw)

	// 2. get_available_models — best-effort; failure logs and continues.
	modelsRaw, err := a.sendPiCommand(PiCommandGetAvailableModels, nil, timeout)
	if err != nil {
		slog.Warn("pi get_available_models failed", "agent_id", a.agentID, "error", err)
	} else {
		a.applyAvailableModels(modelsRaw)
	}

	// 3. set_model if the requested model differs from current.
	if model := opts.Model(); model != "" && model != a.model {
		if err := a.applyModel(model, a.providerForModel(model), timeout); err != nil {
			slog.Warn("pi set_model on startup failed", "agent_id", a.agentID, "model", model, "error", err)
		}
	}

	// 4. set_thinking_level if the requested effort is concrete.
	if effort := opts.Effort(); effort != "" && effort != EffortAuto && effort != a.thinkingLevel {
		if err := a.applyThinkingLevel(effort, timeout); err != nil {
			slog.Warn("pi set_thinking_level on startup failed", "agent_id", a.agentID, "level", effort, "error", err)
		}
	}

	a.mu.Lock()
	sessionHandle := a.sessionHandleLocked()
	a.mu.Unlock()
	sink.UpdateSessionID(sessionHandle)
	sink.BroadcastStatusActive(sessionHandle)
	// Best-effort: hydrate cost/context for resumed Pi sessions immediately
	// on a goroutine so startup readiness is not gated on a usage RPC.
	// Failures are non-fatal; message_end / agent_end keep updating usage.
	go func() {
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
// Caller must hold a.mu.
func (a *PiAgent) sessionHandleLocked() string {
	if a.sessionFile != "" {
		return a.sessionFile
	}
	return a.sessionID
}

// applyStateResponse populates session/model fields from a get_state response.
func (a *PiAgent) applyStateResponse(raw json.RawMessage) {
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
		slog.Warn("pi get_state unmarshal failed", "agent_id", a.agentID, "error", err)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if state.Model.ID != "" {
		a.model = state.Model.ID
	}
	if state.Model.Provider != "" {
		a.provider = state.Model.Provider
	}
	if state.ThinkingLevel != "" {
		a.thinkingLevel = state.ThinkingLevel
	}
	if state.SessionID != "" {
		a.sessionID = state.SessionID
	}
	if state.SessionFile != "" {
		a.sessionFile = state.SessionFile
	}
}

// SendInput forwards a user message to the running Pi agent.
//
// If a turn is already streaming, sets streamingBehavior:"steer" so the
// message is queued and delivered after the current assistant turn finishes
// executing its tool calls (Pi's "all" steering mode by default).
func (a *PiAgent) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	a.mu.Lock()
	if a.stopped {
		a.mu.Unlock()
		return fmt.Errorf("agent is stopped")
	}
	turnActive := a.currentTurnActive
	a.mu.Unlock()

	classified := classifyAttachments(attachments)

	var messageBuilder strings.Builder
	if content != "" {
		messageBuilder.WriteString(content)
	}
	images := make([]map[string]any, 0)
	for _, attachment := range classified {
		switch attachment.kind {
		case attachmentKindText:
			if messageBuilder.Len() > 0 {
				messageBuilder.WriteString("\n\n")
			}
			messageBuilder.WriteString(buildInlineTextAttachmentBlock(attachment))
		case attachmentKindImage:
			images = append(images, map[string]any{
				"type":     "image",
				"data":     base64.StdEncoding.EncodeToString(attachment.data),
				"mimeType": attachment.mimeType,
			})
		case attachmentKindPDF, attachmentKindBinary:
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
	if turnActive {
		payload["streamingBehavior"] = PiStreamingBehaviorSteer
	}

	// Pi blocks for the duration of a turn before responding to `prompt`, so
	// fire it from a goroutine so SendInput can return promptly. No timeout
	// on the RPC itself: the turn unblocks via response, process exit, or
	// ctx cancel (the user interrupting). A wall-clock cap would just kill
	// long-but-legitimate turns.
	go func() {
		if _, err := a.sendPiCommand(PiCommandPrompt, payload, 0); err != nil {
			slog.Error("pi prompt failed", "agent_id", a.agentID, "error", err)
			a.sink.PersistLeapMuxNotification(map[string]any{
				"type":  NotificationTypeAgentError,
				"error": err.Error(),
			})
		}
	}()

	return nil
}

// Stop sends an abort to the running turn (when one is in flight), then tears
// down the process via processBase.Stop. Abort is issued synchronously (with a
// short timeout) before processBase.Stop sets stopped=true and closes stdin —
// running it on a goroutine instead would race the stopped-check inside
// sendPiCommand and drop the abort in the common case.
func (a *PiAgent) Stop() {
	a.mu.Lock()
	stopped := a.stopped
	turnActive := a.currentTurnActive
	a.mu.Unlock()
	if !stopped && turnActive {
		// Best-effort. Failures (timeout, write error, server-side false)
		// fall through to the hard tear-down below.
		_, _ = a.sendPiCommand(PiCommandAbort, nil, 1*time.Second)
	}
	a.processBase.Stop()
}

// Interrupt aborts the running Pi turn by sending the `abort`
// command. Pi's wire format uses {type:"abort"} per the
// piProvider.IsInterrupt classifier; sendPiCommand applies the
// envelope.
//
// No-op when no turn is active so scripts can invoke this without
// probing currentTurnActive first.
func (a *PiAgent) Interrupt() error {
	a.mu.Lock()
	stopped := a.stopped
	turnActive := a.currentTurnActive
	a.mu.Unlock()
	if stopped {
		return fmt.Errorf("agent is stopped")
	}
	if !turnActive {
		return nil
	}
	// Short timeout — Pi acks aborts quickly; longer waits would just
	// extend the apparent latency of a user-driven interrupt.
	_, err := a.sendPiCommand(PiCommandAbort, nil, 1*time.Second)
	return err
}

// ClearContext starts a fresh Pi session in-place.
//
// Pi's new_session response only includes a cancellation flag; we follow it
// with a get_state to pick up the new sessionFile path.
func (a *PiAgent) ClearContext() (string, bool) {
	if _, err := a.sendPiCommand(PiCommandNewSession, nil, a.APITimeout()); err != nil {
		slog.Error("pi ClearContext: new_session failed", "agent_id", a.agentID, "error", err)
		return "", false
	}
	stateRaw, err := a.sendPiCommand(PiCommandGetState, nil, a.APITimeout())
	if err != nil {
		slog.Error("pi ClearContext: get_state failed", "agent_id", a.agentID, "error", err)
		return "", false
	}
	a.applyStateResponse(stateRaw)
	a.mu.Lock()
	a.currentTurnActive = false
	a.sessionCostUsd = 0
	a.sessionCostKnown = false
	a.latestContextUsage = nil
	a.usageGeneration++
	// Drop the per-tool-call side tables with the session. tool_execution_end is
	// their only other removal, and it never arrives for a call the replaced
	// session was still running -- so without this a spawn prompt is retained for
	// the life of the process, and a reused tool-call id would open the next
	// transcript on the previous session's instruction (mirrors
	// acpBase.ClearContext).
	clear(a.toolCallDescriptions)
	a.toolCallPrompts.clear()
	handle := a.sessionHandleLocked()
	a.mu.Unlock()
	// The session was replaced; drop any in-flight thinking-token estimate so it
	// doesn't leak into the new context (mirrors acpBase.ClearContext). The next
	// agent_start also resets, but resetting here keeps every provider's context
	// clear consistent rather than relying on that follow-up.
	a.thinkingTokens.reset()
	if handle == "" {
		return "", false
	}
	a.sink.UpdateSessionID(handle)
	return handle, true
}

func init() {
	registerAgentFactory(
		leapmuxv1.AgentProvider_AGENT_PROVIDER_PI,
		StartPi,
		piDefaultModels,
		nil, // no static option groups; thinking levels live on each model
		"LEAPMUX_PI_DEFAULT_MODEL",
		"LEAPMUX_PI_DEFAULT_EFFORT",
		piBinaryCandidates...,
	)
	// Pi's model-dependent group is its thinking level, labeled "Thinking Level"
	// rather than the default "Effort" -- so the not-running static fallback and the
	// model-switch sub_groups match the live OptionGroups (see PiAgent.OptionGroups).
	setModelSubGroups(leapmuxv1.AgentProvider_AGENT_PROVIDER_PI, effortSubGroupsFunc(PiThinkingLevelLabel))
	// model + effort (the "Thinking Level" axis). Pi has no permission-mode axis.
	setAdditionalOptionIDs(leapmuxv1.AgentProvider_AGENT_PROVIDER_PI, OptionIDEffort)
	// pi_provider (the underlying LLM provider Pi folds into its model selection) is
	// persisted by LeapMux but never surfaced as a group, so its absence from a confirmed
	// catalog is by design -- confirmedOptions preserves it rather than reconciling it away.
	setPersistedOnlyOptionIDs(leapmuxv1.AgentProvider_AGENT_PROVIDER_PI, PiOptionProvider)
}
