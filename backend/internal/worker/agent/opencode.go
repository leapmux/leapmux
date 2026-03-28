package agent

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/util/version"
)

const (
	OpenCodeExtraPrimaryAgent = "primaryAgent"
	OpenCodePrimaryAgentBuild = "build"
	OpenCodePrimaryAgentPlan  = "plan"
	openCodeHiddenCompaction  = "compaction"
	openCodeHiddenTitle       = "title"
	openCodeHiddenSummary     = "summary"
)

type openCodeModeInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// OpenCodeAgent manages a single OpenCode ACP process.
type OpenCodeAgent struct {
	processBase // shared process lifecycle (Stop, Wait, Stderr, etc.)

	model      string
	workingDir string
	sink       OutputSink

	// ACP-specific state.
	sessionID              string       // from newSession response
	nextReqID              atomic.Int64 // JSON-RPC request ID counter
	pendingReqs            sync.Map     // reqID (int64) -> chan json.RawMessage
	availableModels        []*leapmuxv1.AvailableModel
	currentPrimaryAgent    string
	availablePrimaryAgents []*leapmuxv1.AvailableOption
	turnAssistantText      strings.Builder // accumulated assistant text for the current turn
	turnThinkingText       strings.Builder // accumulated thinking text for the current turn
}

// StartOpenCode starts an OpenCode ACP agent process and performs the handshake.
func StartOpenCode(ctx context.Context, opts Options, sink OutputSink) (Provider, error) {
	ctx, cancel := context.WithCancel(ctx)

	cmd, preambleDelimiter, metaPrefix := buildShellWrappedCommand(
		ctx, opts.Shell, opts.LoginShell, "opencode", []string{"OPENCODE_CLIENT"}, []string{"acp"}, nil, opts.WorkingDir,
	)

	cmd.Env = filterEnv(cmd.Environ(), "OPENCODE_CLIENT")
	cmd.Env = append(cmd.Env, "LEAPMUX_WORKER=1")

	cmd.Cancel = func() error {
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = 5 * time.Second

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	a := &OpenCodeAgent{
		processBase: processBase{
			agentID:            opts.AgentID,
			cmd:                cmd,
			stdin:              stdin,
			ctx:                ctx,
			cancel:             cancel,
			stderrDone:         make(chan struct{}),
			processDone:        make(chan struct{}),
			preambleDelimiter:  preambleDelimiter,
			preambleMetaPrefix: metaPrefix,
			preambleMeta:       make(map[string]string),
		},
		model:      opts.Model,
		workingDir: opts.WorkingDir,
		sink:       sink,
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start opencode: %w", err)
	}

	// Drain stderr in background.
	a.drainStderr(stderrPipe)

	// Read stdout JSONL in background.
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	go a.readOutputLoop(scanner)

	cleanup := func() {
		a.Stop()
		_ = a.Wait()
	}

	timeout := opts.startupTimeout()

	// 1. Send "initialize" request.
	initParams, _ := json.Marshal(map[string]interface{}{
		"protocolVersion": 1,
		"clientInfo":      map[string]string{"name": "leapmux", "title": "LeapMux", "version": version.Value},
		"capabilities":    map[string]interface{}{},
	})
	if _, err := a.sendRequest("initialize", json.RawMessage(initParams), timeout); err != nil {
		cleanup()
		return nil, a.formatStartupError("initialize", err)
	}

	// 2. Send "session/resume" (if resuming) or "session/new" (fresh session).
	sessionMethod, sessionParams := buildSessionRequest(opts.ResumeSessionID, opts.WorkingDir)
	sessionResp, err := a.sendRequest(sessionMethod, json.RawMessage(sessionParams), timeout)
	if err != nil {
		if opts.ResumeSessionID != "" {
			// Resume failed — fall back to a fresh session so the agent
			// is still usable (e.g. the old session was garbage-collected).
			slog.Warn("session/resume failed, falling back to session/new",
				"agent_id", a.agentID, "session_id", opts.ResumeSessionID, "error", err)
			_, fallbackParams := buildSessionRequest("", opts.WorkingDir)
			sessionResp, err = a.sendRequest("session/new", json.RawMessage(fallbackParams), timeout)
		}
		if err != nil {
			cleanup()
			return nil, a.formatStartupError(sessionMethod, err)
		}
	}

	var session struct {
		SessionID string `json:"sessionId"`
		Models    struct {
			CurrentModelID  string `json:"currentModelId"`
			AvailableModels []struct {
				ModelID string `json:"modelId"`
				Name    string `json:"name"`
			} `json:"availableModels"`
		} `json:"models"`
		Modes *struct {
			CurrentModeID  string             `json:"currentModeId"`
			AvailableModes []openCodeModeInfo `json:"availableModes"`
		} `json:"modes"`
	}
	if err := json.Unmarshal(sessionResp, &session); err != nil {
		cleanup()
		return nil, a.formatStartupError("newSession parse", err)
	}
	if session.SessionID == "" {
		cleanup()
		return nil, a.formatStartupError("session/new", fmt.Errorf("response did not contain a session ID"))
	}

	a.sessionID = session.SessionID
	sink.UpdateSessionID(a.sessionID)
	sink.BroadcastStatusActive(a.sessionID)

	// Build available models from the session response.
	a.availableModels = buildOpenCodeModels(session.Models.AvailableModels, session.Models.CurrentModelID)

	// Use the model from the session response if not explicitly set.
	if a.model == "" && session.Models.CurrentModelID != "" {
		a.model = session.Models.CurrentModelID
	}

	var requestedPrimaryAgent string
	if opts.ExtraSettings != nil {
		requestedPrimaryAgent = opts.ExtraSettings[OpenCodeExtraPrimaryAgent]
	}
	if session.Modes != nil {
		if err := a.configurePrimaryAgents(session.Modes.AvailableModes, session.Modes.CurrentModeID, requestedPrimaryAgent); err != nil {
			cleanup()
			return nil, a.formatStartupError("session/set_mode", err)
		}
	} else {
		if err := a.configurePrimaryAgents(nil, "", ""); err != nil {
			cleanup()
			return nil, a.formatStartupError("session/set_mode", err)
		}
	}

	return a, nil
}

// buildSessionRequest returns the JSON-RPC method and params for starting or
// resuming an OpenCode session. When resumeSessionID is non-empty, it produces
// a "session/resume" request; otherwise a "session/new" request.
func buildSessionRequest(resumeSessionID, workingDir string) (method string, params []byte) {
	p := map[string]interface{}{
		"cwd":        workingDir,
		"mcpServers": []interface{}{},
	}
	method = "session/new"
	if resumeSessionID != "" {
		p["sessionId"] = resumeSessionID
		method = "session/resume"
	}
	params, _ = json.Marshal(p)
	return method, params
}

// buildOpenCodeModels converts the ACP newSession models list to proto models.
func buildOpenCodeModels(models []struct {
	ModelID string `json:"modelId"`
	Name    string `json:"name"`
}, currentModelID string) []*leapmuxv1.AvailableModel {
	var result []*leapmuxv1.AvailableModel
	for _, m := range models {
		result = append(result, &leapmuxv1.AvailableModel{
			Id:          m.ModelID,
			DisplayName: m.Name,
			IsDefault:   m.ModelID == currentModelID,
		})
	}
	return result
}

// capitalizeFirst returns s with its first rune upper-cased.
func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	for _, r := range s {
		return string(unicode.ToUpper(r)) + s[len(string(r)):]
	}
	return s
}

func buildOpenCodePrimaryAgents(modes []openCodeModeInfo, currentModeID string) []*leapmuxv1.AvailableOption {
	result := make([]*leapmuxv1.AvailableOption, 0, len(modes))
	for _, mode := range modes {
		if isHiddenOpenCodePrimaryAgent(mode.ID) {
			continue
		}
		name := strings.TrimSpace(mode.Name)
		if name == "" || name == mode.ID {
			// Capitalize when the agent provides no separate display name.
			name = capitalizeFirst(mode.ID)
		}
		result = append(result, &leapmuxv1.AvailableOption{
			Id:          mode.ID,
			Name:        name,
			Description: mode.Description,
			IsDefault:   mode.ID == currentModeID,
		})
	}
	return result
}

func fallbackOpenCodePrimaryAgents() []*leapmuxv1.AvailableOption {
	return []*leapmuxv1.AvailableOption{
		{Id: OpenCodePrimaryAgentBuild, Name: capitalizeFirst(OpenCodePrimaryAgentBuild), IsDefault: true},
		{Id: OpenCodePrimaryAgentPlan, Name: capitalizeFirst(OpenCodePrimaryAgentPlan)},
	}
}

func isHiddenOpenCodePrimaryAgent(id string) bool {
	switch id {
	case openCodeHiddenCompaction, openCodeHiddenTitle, openCodeHiddenSummary:
		return true
	default:
		return false
	}
}

func hasOpenCodePrimaryAgent(options []*leapmuxv1.AvailableOption, id string) bool {
	if id == "" {
		return false
	}
	for _, option := range options {
		if option != nil && option.Id == id {
			return true
		}
	}
	return false
}

func firstOpenCodePrimaryAgent(options []*leapmuxv1.AvailableOption) string {
	for _, option := range options {
		if option != nil && option.IsDefault && option.Id != "" {
			return option.Id
		}
	}
	for _, option := range options {
		if option != nil && option.Id != "" {
			return option.Id
		}
	}
	return ""
}

func (a *OpenCodeAgent) configurePrimaryAgents(modes []openCodeModeInfo, currentModeID, requestedPrimaryAgent string) error {
	available := buildOpenCodePrimaryAgents(modes, currentModeID)
	hasACPModeList := len(available) > 0
	current := currentModeID
	if !hasACPModeList {
		available = fallbackOpenCodePrimaryAgents()
		if current == "" {
			current = OpenCodePrimaryAgentBuild
		}
	}
	if current == "" {
		current = firstOpenCodePrimaryAgent(available)
	}

	a.mu.Lock()
	a.availablePrimaryAgents = available
	a.currentPrimaryAgent = current
	a.mu.Unlock()

	if hasACPModeList && requestedPrimaryAgent != "" && requestedPrimaryAgent != current && hasOpenCodePrimaryAgent(available, requestedPrimaryAgent) {
		if err := a.setPrimaryAgent(requestedPrimaryAgent); err != nil {
			return err
		}
	}

	return nil
}

// SendInput writes a user prompt to the agent. The ACP prompt RPC blocks until
// the LLM finishes, so it runs in a goroutine. Streaming output arrives via
// sessionUpdate notifications meanwhile.
func (a *OpenCodeAgent) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	a.mu.Lock()
	if a.stopped {
		a.mu.Unlock()
		return fmt.Errorf("agent is stopped")
	}
	sessionID := a.sessionID
	a.mu.Unlock()

	if sessionID == "" {
		return fmt.Errorf("opencode agent has no active session")
	}

	prompt := buildACPPromptBlocks(content, classifyAttachments(attachments))
	params, _ := json.Marshal(map[string]interface{}{
		"sessionId": sessionID,
		"prompt":    prompt,
	})

	go func() {
		resp, err := a.sendRequest("session/prompt", json.RawMessage(params), 10*time.Minute)
		if err != nil {
			if !a.IsStopped() {
				slog.Error("opencode prompt failed", "agent_id", a.agentID, "error", err)
				a.sink.BroadcastNotification(map[string]interface{}{
					"type":  "agent_error",
					"error": fmt.Sprintf("prompt failed: %v", err),
				})
			}
			return
		}
		a.handlePromptResponse(resp)
	}()

	return nil
}

// buildACPPromptBlocks converts text + classified attachments into ACP prompt
// blocks compatible with both OpenCode and Gemini CLI.
func buildACPPromptBlocks(content string, classified []classifiedAttachment) []map[string]interface{} {
	var prompt []map[string]interface{}
	if content != "" {
		prompt = append(prompt, map[string]interface{}{"type": "text", "text": content})
	}
	for _, attachment := range classified {
		if attachment.kind == attachmentKindImage {
			prompt = append(prompt, map[string]interface{}{
				"type":     "image",
				"mimeType": attachment.mimeType,
				"data":     base64.StdEncoding.EncodeToString(attachment.data),
				"uri":      attachment.filename,
			})
			continue
		}

		resource := map[string]interface{}{
			"uri":      attachment.filename,
			"mimeType": attachment.mimeType,
		}
		if attachment.kind == attachmentKindText {
			resource["text"] = string(attachment.data)
		} else {
			resource["blob"] = base64.StdEncoding.EncodeToString(attachment.data)
		}
		prompt = append(prompt, map[string]interface{}{
			"type":     "resource",
			"resource": resource,
		})
	}
	return prompt
}

// handlePromptResponse processes the prompt RPC response, persisting the
// accumulated assistant text and emitting a result divider.
func (a *OpenCodeAgent) handlePromptResponse(resp json.RawMessage) {
	if resp == nil {
		return
	}

	// Persist accumulated thinking and assistant text before the result divider.
	a.mu.Lock()
	thinkingText := a.turnThinkingText.String()
	a.turnThinkingText.Reset()
	assistantText := a.turnAssistantText.String()
	a.turnAssistantText.Reset()
	a.mu.Unlock()
	persistACPPromptResponse(a.agentID, a.sink, thinkingText, assistantText, resp, unwrapACPResult, func(resp json.RawMessage) json.RawMessage {
		return a.enrichWithToolUses(resp)
	})

	a.mu.Lock()
	a.turnToolUses = 0
	a.mu.Unlock()
}

// CurrentSettings returns the current settings for this agent.
func (a *OpenCodeAgent) CurrentSettings() *leapmuxv1.AgentSettings {
	a.mu.Lock()
	defer a.mu.Unlock()
	extra := map[string]string{}
	if a.currentPrimaryAgent != "" {
		extra[OpenCodeExtraPrimaryAgent] = a.currentPrimaryAgent
	}
	return &leapmuxv1.AgentSettings{
		Model:         a.model,
		ExtraSettings: extra,
	}
}

// AvailableModels returns the models reported by the OpenCode process.
func (a *OpenCodeAgent) AvailableModels() []*leapmuxv1.AvailableModel {
	return a.availableModels
}

// AvailableOptionGroups returns the available primary-agent group.
func (a *OpenCodeAgent) AvailableOptionGroups() []*leapmuxv1.AvailableOptionGroup {
	return a.availablePrimaryAgentGroup()
}

// UpdateSettings applies setting changes to a running agent.
func (a *OpenCodeAgent) UpdateSettings(s *leapmuxv1.AgentSettings) bool {
	if m := s.GetModel(); m != "" {
		if err := a.setModel(m); err != nil {
			slog.Warn("opencode session/set_model failed", "agent_id", a.agentID, "error", err)
			return false
		}
	}
	if primaryAgent := s.GetExtraSettings()[OpenCodeExtraPrimaryAgent]; primaryAgent != "" {
		if err := a.setPrimaryAgent(primaryAgent); err != nil {
			slog.Warn("opencode session/set_mode failed", "agent_id", a.agentID, "error", err)
			return false
		}
	}
	return true
}

func (a *OpenCodeAgent) setModel(model string) error {
	a.mu.Lock()
	sessionID := a.sessionID
	a.mu.Unlock()

	params, _ := json.Marshal(map[string]interface{}{
		"sessionId": sessionID,
		"modelId":   model,
	})
	resp, err := a.sendRequest("session/set_model", json.RawMessage(params), 10*time.Second)
	if err != nil {
		return err
	}
	if err := jsonRPCResultError(resp); err != nil {
		return err
	}

	a.mu.Lock()
	a.model = model
	a.mu.Unlock()
	return nil
}

func (a *OpenCodeAgent) setPrimaryAgent(agent string) error {
	a.mu.Lock()
	sessionID := a.sessionID
	available := a.availablePrimaryAgents
	a.mu.Unlock()

	if !hasOpenCodePrimaryAgent(available, agent) {
		return fmt.Errorf("unknown primary agent: %s", agent)
	}

	params, _ := json.Marshal(map[string]interface{}{
		"sessionId": sessionID,
		"modeId":    agent,
	})
	resp, err := a.sendRequest("session/set_mode", json.RawMessage(params), 10*time.Second)
	if err != nil {
		return err
	}
	if err := jsonRPCResultError(resp); err != nil {
		return err
	}

	a.mu.Lock()
	a.currentPrimaryAgent = agent
	a.mu.Unlock()
	return nil
}

func (a *OpenCodeAgent) availablePrimaryAgentGroup() []*leapmuxv1.AvailableOptionGroup {
	a.mu.Lock()
	defer a.mu.Unlock()
	options := a.availablePrimaryAgents
	if len(options) == 0 {
		options = fallbackOpenCodePrimaryAgents()
	}
	return []*leapmuxv1.AvailableOptionGroup{{
		Key:     OpenCodeExtraPrimaryAgent,
		Label:   "Primary Agent",
		Options: options,
	}}
}

// HandleOutput processes a single JSONL notification from OpenCode.
func (a *OpenCodeAgent) HandleOutput(content []byte) {
	handleOpenCodeOutput(a, content)
}

// --- JSON-RPC helpers ---

// sendRequest sends a JSON-RPC request and waits for the response.
func (a *OpenCodeAgent) sendRequest(method string, params json.RawMessage, timeout time.Duration) (json.RawMessage, error) {
	return sendACPRequest(a.stdin, &a.nextReqID, &a.pendingReqs, a.processDone, a.ctx, a.processExitError, method, params, timeout)
}

// sendNotification sends a JSON-RPC notification (no id, no response expected).
func (a *OpenCodeAgent) sendNotification(method string, params json.RawMessage) error {
	return sendACPNotification(a.stdin, method, params)
}

// readOutputLoop reads JSONL lines from stdout and dispatches them.
func (a *OpenCodeAgent) readOutputLoop(scanner *bufio.Scanner) {
	a.readOutput(scanner, a.handleJSONRPCResponse, a.HandleOutput)
}

// handleJSONRPCResponse checks if a line is a JSON-RPC response and routes it.
// Responses have a numeric "id" but no "method". Server-initiated requests
// have both "id" and "method" (e.g. requestPermission) — those are passed
// through to HandleOutput.
func (a *OpenCodeAgent) handleJSONRPCResponse(line []byte) bool {
	return handleACPJSONRPCResponse(&a.pendingReqs, line)
}

// cancelSession sends a cancel notification for the current session.
func (a *OpenCodeAgent) cancelSession() error {
	a.mu.Lock()
	sessionID := a.sessionID
	a.mu.Unlock()

	params, _ := json.Marshal(map[string]interface{}{
		"sessionId": sessionID,
	})
	return a.sendNotification("session/cancel", json.RawMessage(params))
}

// formatStartupError includes stderr and preamble output for diagnostics.
func (a *OpenCodeAgent) formatStartupError(phase string, err error) error {
	return a.processBase.formatStartupError(phase, err, a.PreambleOutput())
}

// unwrapACPResult extracts the inner content from an ACP result message.
// Some OpenCode versions return session/prompt results in the format:
//
//	{id, role: "result", seq, created_at, content: {stopReason, usage, ...}}
//
// The frontend classifier expects stopReason at the top level, so we unwrap
// the content and merge any top-level metadata (_meta) into it.
func unwrapACPResult(resp json.RawMessage) json.RawMessage {
	var wrapper struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(resp, &wrapper) != nil || wrapper.Role != "result" || len(wrapper.Content) == 0 {
		return resp
	}
	return wrapper.Content
}

func jsonRPCResultError(resp json.RawMessage) error {
	if len(resp) == 0 || string(resp) == "null" {
		return nil
	}
	var rpcErr struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(resp, &rpcErr); err != nil {
		return nil
	}
	if rpcErr.Message == "" {
		return nil
	}
	return fmt.Errorf("json-rpc error %d: %s", rpcErr.Code, rpcErr.Message)
}

func isJSONRPCMethodNotFound(resp json.RawMessage) bool {
	if len(resp) == 0 || string(resp) == "null" {
		return false
	}
	var rpcErr struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(resp, &rpcErr); err != nil {
		return false
	}
	return rpcErr.Code == -32601
}
