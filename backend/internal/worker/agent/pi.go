package agent

import (
	"bufio"
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
	// extra_settings[PiExtraProvider] so model-switch RPCs round-trip with the
	// correct provider field across restarts.
	provider string

	model         string
	thinkingLevel string // stored as the agent's "effort"
	workingDir    string
	sink          OutputSink

	sessionID         string // Pi's runtime sessionId (rotates on new_session)
	sessionFile       string // Pi's persistent session file path (durable identifier)
	currentTurnActive bool   // true between agent_start and agent_end

	// Pi exposes token/cost information in assistant messages and via
	// get_session_stats. Keep the latest normalized snapshot here so persisted
	// message_end / agent_end events can rehydrate the frontend after reconnect.
	sessionCostUsd     float64
	sessionCostKnown   bool
	latestContextUsage map[string]any
	usageGeneration    uint64

	availableModels []*leapmuxv1.AvailableModel
	// modelProviders maps modelID -> underlying provider (e.g.
	// "openai-codex"). Populated alongside availableModels so set_model RPCs
	// can ship the correct {provider, modelId} pair without round-tripping
	// the provider name through user-visible strings.
	modelProviders map[string]string

	// nextReqID mints monotonic ids; we stringify them at register time so
	// the correlator's key type stays narrow even though we generate from
	// an int64 atom.
	nextReqID atomic.Int64
}

// StartPi starts a `pi --mode rpc` process and performs the startup handshake.
func StartPi(ctx context.Context, opts Options, sink OutputSink) (Agent, error) {
	ctx, cancel := context.WithCancel(ctx)

	binary := resolveBinaryName(ctx, opts.Shell, opts.LoginShell, piBinaryCandidates)
	// Pi has no --working-dir flag (it uses the process cwd). buildShellWrappedCommand
	// already sets cmd.Dir to opts.WorkingDir, so the agent picks up the right
	// directory implicitly.
	cmd, preambleDelimiter, metaPrefix := buildShellWrappedCommand(
		ctx, opts.Shell, opts.LoginShell, binary, nil,
		[]string{"--mode", "rpc"},
		nil, opts.WorkingDir,
	)
	cmd.Env = append(cmd.Environ(), "LEAPMUX_WORKER=1")

	stdin, stdout, stderrPipe, err := setupProcessPipes(cmd, cancel)
	if err != nil {
		return nil, err
	}

	a := &PiAgent{
		processBase: processBase{
			agentID:            opts.AgentID,
			providerName:       "pi",
			cmd:                cmd,
			stdin:              stdin,
			ctx:                ctx,
			cancel:             cancel,
			stderrDone:         make(chan struct{}),
			processDone:        make(chan struct{}),
			preambleDelimiter:  preambleDelimiter,
			preambleMetaPrefix: metaPrefix,
			preambleMeta:       make(map[string]string),
			apiTimeout:         opts.apiTimeout(),
		},
		model:         opts.Model,
		thinkingLevel: opts.Effort,
		provider:      cmp.Or(opts.ExtraSettings[PiExtraProvider], PiDefaultProvider),
		workingDir:    opts.WorkingDir,
		sink:          sink,
	}

	if err := a.startCmd(cmd, cancel); err != nil {
		return nil, err
	}
	a.drainStderr(stderrPipe)

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	go a.readOutput(scanner, a.handlePiResponse, a.handleOutput)

	cleanup := func() {
		a.Stop()
		_ = a.Wait()
	}

	timeout := opts.startupTimeout()

	// 1. get_state — confirms the process is alive and yields the session
	//    handle plus the in-process model/thinking values that act as the
	//    starting point for any opts overrides.
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

	// 3. set_model if opts.Model differs from current.
	if opts.Model != "" && opts.Model != a.model {
		if err := a.applyModel(opts.Model, a.providerForModel(opts.Model), timeout); err != nil {
			slog.Warn("pi set_model on startup failed", "agent_id", a.agentID, "model", opts.Model, "error", err)
		}
	}

	// 4. set_thinking_level if opts.Effort is concrete.
	if opts.Effort != "" && opts.Effort != EffortAuto && opts.Effort != a.thinkingLevel {
		if err := a.applyThinkingLevel(opts.Effort, timeout); err != nil {
			slog.Warn("pi set_thinking_level on startup failed", "agent_id", a.agentID, "level", opts.Effort, "error", err)
		}
	}

	// 5. switch_session if a prior session file path was supplied.
	if opts.ResumeSessionID != "" && opts.ResumeSessionID != a.sessionFile {
		params := map[string]any{"sessionPath": opts.ResumeSessionID}
		if _, err := a.sendPiCommand(PiCommandSwitchSession, params, timeout); err != nil {
			slog.Warn("pi switch_session failed; continuing with fresh session",
				"agent_id", a.agentID, "session_path", opts.ResumeSessionID, "error", err)
		} else if stateRaw, err := a.sendPiCommand(PiCommandGetState, nil, timeout); err == nil {
			a.applyStateResponse(stateRaw)
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
// `sessionFile` (the path Pi accepts as `sessionPath` for switch_session
// across restarts) and falling back to the rotating runtime `sessionId`.
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
	handle := a.sessionHandleLocked()
	a.mu.Unlock()
	if handle == "" {
		return "", false
	}
	a.sink.UpdateSessionID(handle)
	return handle, true
}

func init() {
	registerAgentFactory(
		leapmuxv1.AgentProvider_AGENT_PROVIDER_PI,
		func(ctx context.Context, opts Options, sink OutputSink) (Agent, error) {
			return StartPi(ctx, opts, sink)
		},
		piDefaultModels,
		nil, // no static option groups; thinking levels live on each model
		"LEAPMUX_PI_DEFAULT_MODEL",
		"LEAPMUX_PI_DEFAULT_EFFORT",
		piBinaryCandidates...,
	)
}
