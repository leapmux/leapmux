package agent

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"syscall"
	"time"

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

// OpenCode-specific method names.
const (
	openCodeMethodSessionResume = "session/resume"
)

type openCodeModeInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// OpenCodeAgent manages a single OpenCode ACP process.
type OpenCodeAgent struct {
	acpBase

	model      string
	workingDir string

	availableModels        []*leapmuxv1.AvailableModel
	currentPrimaryAgent    string
	availablePrimaryAgents []*leapmuxv1.AvailableOption
}

// StartOpenCode starts an OpenCode ACP agent process and performs the handshake.
func StartOpenCode(ctx context.Context, opts Options, sink OutputSink) (Provider, error) {
	ctx, cancel := context.WithCancel(ctx)

	cmd, preambleDelimiter, metaPrefix := buildShellWrappedCommand(
		ctx, opts.Shell, opts.LoginShell, "opencode", []string{"OPENCODE_CLIENT"}, []string{"acp"}, nil, opts.WorkingDir,
	)

	cmd.Env = filterEnv(cmd.Environ(), "OPENCODE_CLIENT")
	cmd.Env = append(cmd.Env, "LEAPMUX_WORKER=1")
	if opts.LoginShell {
		cmd.Env = append(cmd.Env, "OPENCODE_CLIENT=1")
	}

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
		acpBase: acpBase{
			jsonrpcBase: jsonrpcBase{processBase: processBase{
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
			}},
			sink: sink,
		},
		model:      opts.Model,
		workingDir: opts.WorkingDir,
	}
	a.promptFunc = a.doSendPrompt

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start opencode: %w", err)
	}

	// Drain stderr in background.
	a.drainStderr(stderrPipe)

	// Read stdout JSONL in background.
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	go a.readOutputLoop(scanner, a.HandleOutput)

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
	if _, err := a.sendRequest(acpMethodInitialize, json.RawMessage(initParams), timeout); err != nil {
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
			sessionResp, err = a.sendRequest(acpMethodSessionNew, json.RawMessage(fallbackParams), timeout)
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

func buildSessionRequest(resumeSessionID, workingDir string) (method string, params []byte) {
	return buildACPSessionRequest(resumeSessionID, workingDir, acpMethodSessionNew, openCodeMethodSessionResume)
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

	if hasACPModeList && requestedPrimaryAgent != "" && requestedPrimaryAgent != current && hasACPOption(available, requestedPrimaryAgent) {
		if err := a.setPrimaryAgent(requestedPrimaryAgent); err != nil {
			return err
		}
	}

	return nil
}

// doSendPrompt sends a single prompt RPC and processes the response. Called by
// jsonrpcBase.runPrompt on a goroutine.
func (a *OpenCodeAgent) doSendPrompt(content string, attachments []*leapmuxv1.Attachment) {
	a.mu.Lock()
	sessionID := a.sessionID
	a.mu.Unlock()

	a.sendPrompt(sessionID, content, attachments,
		func(params json.RawMessage) (json.RawMessage, error) {
			return a.sendRequest(acpMethodSessionPrompt, params, 10*time.Minute)
		},
		a.handlePromptResponse,
	)
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

func (a *OpenCodeAgent) handlePromptResponse(resp json.RawMessage) {
	a.handleACPPromptResponse(resp, nil)
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
	resp, err := a.sendRequest(acpMethodSessionSetModel, json.RawMessage(params), 10*time.Second)
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

	if !hasACPOption(available, agent) {
		return fmt.Errorf("unknown primary agent: %s", agent)
	}

	params, _ := json.Marshal(map[string]interface{}{
		"sessionId": sessionID,
		"modeId":    agent,
	})
	resp, err := a.sendRequest(acpMethodSessionSetMode, json.RawMessage(params), 10*time.Second)
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

// formatStartupError includes stderr and preamble output for diagnostics.
func (a *OpenCodeAgent) formatStartupError(phase string, err error) error {
	return a.processBase.formatStartupError(phase, err, a.PreambleOutput())
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
