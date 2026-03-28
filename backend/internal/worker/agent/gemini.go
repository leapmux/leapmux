package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

const (
	GeminiCLIModeDefault  = "default"
	GeminiCLIModeAutoEdit = "autoEdit"
	GeminiCLIModeYolo     = "yolo"
	GeminiCLIModePlan     = "plan"
)

type geminiCLIModeInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type geminiCLIModelInfo struct {
	ModelID     string `json:"modelId"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// GeminiCLIAgent manages a single Gemini CLI ACP process.
type GeminiCLIAgent struct {
	processBase

	model          string
	permissionMode string
	workingDir     string
	sink           OutputSink

	sessionID         string
	useLegacyMethods  bool
	nextReqID         atomic.Int64
	pendingReqs       sync.Map
	availableModels   []*leapmuxv1.AvailableModel
	availableModes    []*leapmuxv1.AvailableOption
	turnAssistantText strings.Builder
	turnThinkingText  strings.Builder
}

// StartGeminiCLI starts a Gemini CLI ACP agent process and performs the handshake.
func StartGeminiCLI(ctx context.Context, opts Options, sink OutputSink) (Provider, error) {
	ctx, cancel := context.WithCancel(ctx)

	cmd, preambleDelimiter, metaPrefix := buildShellWrappedCommand(
		ctx, opts.Shell, opts.LoginShell, "gemini", []string{"GEMINI_CLI"}, []string{"--acp"}, nil, opts.WorkingDir,
	)

	cmd.Env = filterEnv(cmd.Environ(), "GEMINI_CLI")
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

	a := &GeminiCLIAgent{
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
		return nil, fmt.Errorf("start gemini: %w", err)
	}

	a.drainStderr(stderrPipe)

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	go a.readOutputLoop(scanner)

	cleanup := func() {
		a.Stop()
		_ = a.Wait()
	}

	timeout := opts.startupTimeout()

	initParams, _ := json.Marshal(map[string]interface{}{
		"protocolVersion": 1,
		"clientCapabilities": map[string]interface{}{
			"fs": map[string]bool{
				"readTextFile":  false,
				"writeTextFile": false,
			},
		},
	})
	if _, err := a.sendRequest("initialize", json.RawMessage(initParams), timeout); err != nil {
		cleanup()
		return nil, a.formatStartupError("initialize", err)
	}

	sessionMethod, sessionParams := buildGeminiSessionRequest(opts.ResumeSessionID, opts.WorkingDir)
	sessionResp, err := a.sendCompatibleRequest(sessionMethod, legacyGeminiMethod(sessionMethod), json.RawMessage(sessionParams), timeout)
	if err != nil {
		if opts.ResumeSessionID != "" {
			slog.Warn("loadSession failed, falling back to newSession",
				"agent_id", a.agentID, "session_id", opts.ResumeSessionID, "error", err)
			_, fallbackParams := buildGeminiSessionRequest("", opts.WorkingDir)
			sessionResp, err = a.sendCompatibleRequest("newSession", legacyGeminiMethod("newSession"), json.RawMessage(fallbackParams), timeout)
		}
		if err != nil {
			cleanup()
			return nil, a.formatStartupError(sessionMethod, err)
		}
	}

	var session struct {
		SessionID string `json:"sessionId"`
		Models    struct {
			CurrentModelID  string               `json:"currentModelId"`
			AvailableModels []geminiCLIModelInfo `json:"availableModels"`
		} `json:"models"`
		Modes struct {
			CurrentModeID  string              `json:"currentModeId"`
			AvailableModes []geminiCLIModeInfo `json:"availableModes"`
		} `json:"modes"`
	}
	if err := json.Unmarshal(sessionResp, &session); err != nil {
		cleanup()
		return nil, a.formatStartupError("newSession parse", err)
	}
	if session.SessionID == "" && opts.ResumeSessionID != "" && sessionMethod == "loadSession" {
		session.SessionID = opts.ResumeSessionID
	}
	if session.SessionID == "" {
		cleanup()
		return nil, a.formatStartupError(sessionMethod, fmt.Errorf("response did not contain a session ID"))
	}

	a.sessionID = session.SessionID
	sink.UpdateSessionID(a.sessionID)
	sink.BroadcastStatusActive(a.sessionID)

	a.availableModels = buildGeminiCLIModels(session.Models.AvailableModels, session.Models.CurrentModelID)
	if a.model == "" && session.Models.CurrentModelID != "" {
		a.model = session.Models.CurrentModelID
	}

	a.availableModes = buildGeminiCLIModes(session.Modes.AvailableModes, session.Modes.CurrentModeID)
	a.permissionMode = session.Modes.CurrentModeID
	if a.permissionMode == "" {
		a.permissionMode = GeminiCLIModeDefault
	}

	if requested := StringOrDefault(opts.PermissionMode, ""); requested != "" && requested != a.permissionMode {
		if err := a.setPermissionMode(requested); err != nil {
			cleanup()
			return nil, a.formatStartupError("setSessionMode", err)
		}
	}

	return a, nil
}

func buildGeminiSessionRequest(resumeSessionID, workingDir string) (method string, params []byte) {
	p := map[string]interface{}{
		"cwd":        workingDir,
		"mcpServers": []interface{}{},
	}
	method = "newSession"
	if resumeSessionID != "" {
		p["sessionId"] = resumeSessionID
		method = "loadSession"
	}
	params, _ = json.Marshal(p)
	return method, params
}

func buildGeminiCLIModels(models []geminiCLIModelInfo, currentModelID string) []*leapmuxv1.AvailableModel {
	result := make([]*leapmuxv1.AvailableModel, 0, len(models))
	for _, m := range models {
		result = append(result, &leapmuxv1.AvailableModel{
			Id:          m.ModelID,
			DisplayName: m.Name,
			Description: m.Description,
			IsDefault:   m.ModelID == currentModelID,
		})
	}
	return result
}

func buildGeminiCLIModes(modes []geminiCLIModeInfo, currentModeID string) []*leapmuxv1.AvailableOption {
	result := make([]*leapmuxv1.AvailableOption, 0, len(modes))
	for _, mode := range modes {
		if mode.ID == "" {
			continue
		}
		name := mode.Name
		if name == "" {
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

func fallbackGeminiCLIModes() []*leapmuxv1.AvailableOption {
	return []*leapmuxv1.AvailableOption{
		{Id: GeminiCLIModeDefault, Name: "Default", IsDefault: true},
		{Id: GeminiCLIModeAutoEdit, Name: "Auto Edit"},
		{Id: GeminiCLIModeYolo, Name: "YOLO"},
		{Id: GeminiCLIModePlan, Name: "Plan"},
	}
}

func hasGeminiCLIMode(options []*leapmuxv1.AvailableOption, id string) bool {
	for _, option := range options {
		if option != nil && option.Id == id {
			return true
		}
	}
	return false
}

func (a *GeminiCLIAgent) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	a.mu.Lock()
	if a.stopped {
		a.mu.Unlock()
		return fmt.Errorf("agent is stopped")
	}
	sessionID := a.sessionID
	a.mu.Unlock()

	if sessionID == "" {
		return fmt.Errorf("gemini cli agent has no active session")
	}

	prompt := buildACPPromptBlocks(content, classifyAttachments(attachments))
	params, _ := json.Marshal(map[string]interface{}{
		"sessionId": sessionID,
		"prompt":    prompt,
	})

	go func() {
		resp, err := a.sendCompatibleRequest("prompt", legacyGeminiMethod("prompt"), json.RawMessage(params), 10*time.Minute)
		if err != nil {
			if !a.IsStopped() {
				slog.Error("gemini prompt failed", "agent_id", a.agentID, "error", err)
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

func (a *GeminiCLIAgent) handlePromptResponse(resp json.RawMessage) {
	if resp == nil {
		return
	}

	a.mu.Lock()
	thinkingText := a.turnThinkingText.String()
	a.turnThinkingText.Reset()
	assistantText := a.turnAssistantText.String()
	a.turnAssistantText.Reset()
	a.mu.Unlock()
	broadcastGeminiQuotaSessionInfo(a.sink, resp)
	persistACPPromptResponse(a.agentID, a.sink, thinkingText, assistantText, resp, nil, func(resp json.RawMessage) json.RawMessage {
		return a.enrichWithToolUses(resp)
	})

	a.mu.Lock()
	a.turnToolUses = 0
	a.mu.Unlock()
}

func (a *GeminiCLIAgent) CurrentSettings() *leapmuxv1.AgentSettings {
	a.mu.Lock()
	defer a.mu.Unlock()
	return &leapmuxv1.AgentSettings{
		Model:          a.model,
		PermissionMode: a.permissionMode,
	}
}

func (a *GeminiCLIAgent) AvailableModels() []*leapmuxv1.AvailableModel {
	return a.availableModels
}

func (a *GeminiCLIAgent) AvailableOptionGroups() []*leapmuxv1.AvailableOptionGroup {
	a.mu.Lock()
	defer a.mu.Unlock()
	options := a.availableModes
	if len(options) == 0 {
		options = fallbackGeminiCLIModes()
	}
	return []*leapmuxv1.AvailableOptionGroup{{
		Key:     "permissionMode",
		Label:   "Permission Mode",
		Options: options,
	}}
}

func (a *GeminiCLIAgent) UpdateSettings(s *leapmuxv1.AgentSettings) bool {
	if model := s.GetModel(); model != "" {
		if err := a.setModel(model); err != nil {
			slog.Warn("gemini unstable_setSessionModel failed", "agent_id", a.agentID, "error", err)
			return false
		}
	}
	if mode := s.GetPermissionMode(); mode != "" {
		if err := a.setPermissionMode(mode); err != nil {
			slog.Warn("gemini setSessionMode failed", "agent_id", a.agentID, "error", err)
			return false
		}
	}
	return true
}

func (a *GeminiCLIAgent) setModel(model string) error {
	a.mu.Lock()
	sessionID := a.sessionID
	a.mu.Unlock()

	params, _ := json.Marshal(map[string]interface{}{
		"sessionId": sessionID,
		"modelId":   model,
	})
	resp, err := a.sendCompatibleRequest("unstable_setSessionModel", legacyGeminiMethod("unstable_setSessionModel"), json.RawMessage(params), 10*time.Second)
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

func (a *GeminiCLIAgent) setPermissionMode(mode string) error {
	a.mu.Lock()
	sessionID := a.sessionID
	available := a.availableModes
	a.mu.Unlock()

	if len(available) > 0 && !hasGeminiCLIMode(available, mode) {
		return fmt.Errorf("unknown permission mode: %s", mode)
	}

	params, _ := json.Marshal(map[string]interface{}{
		"sessionId": sessionID,
		"modeId":    mode,
	})
	resp, err := a.sendCompatibleRequest("setSessionMode", legacyGeminiMethod("setSessionMode"), json.RawMessage(params), 10*time.Second)
	if err != nil {
		return err
	}
	if err := jsonRPCResultError(resp); err != nil {
		return err
	}

	a.mu.Lock()
	a.permissionMode = mode
	a.mu.Unlock()
	return nil
}

func (a *GeminiCLIAgent) HandleOutput(content []byte) {
	handleGeminiCLIOutput(a, content)
}

func (a *GeminiCLIAgent) SendRawInput(data []byte) error {
	var msg struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(data, &msg); err == nil {
		switch msg.Method {
		case "cancel", "session/cancel":
			return a.cancelSession()
		}
	}
	return a.processBase.SendRawInput(data)
}

func (a *GeminiCLIAgent) sendRequest(method string, params json.RawMessage, timeout time.Duration) (json.RawMessage, error) {
	return sendACPRequest(a.stdin, &a.nextReqID, &a.pendingReqs, a.processDone, a.ctx, a.processExitError, method, params, timeout)
}

func (a *GeminiCLIAgent) sendNotification(method string, params json.RawMessage) error {
	return sendACPNotification(a.stdin, method, params)
}

func (a *GeminiCLIAgent) sendCompatibleRequest(method, legacyMethod string, params json.RawMessage, timeout time.Duration) (json.RawMessage, error) {
	if legacyMethod == "" || method == legacyMethod {
		return a.sendRequest(method, params, timeout)
	}
	if a.usesLegacyMethods() {
		return a.sendRequest(legacyMethod, params, timeout)
	}

	resp, err := a.sendRequest(method, params, timeout)
	if err != nil {
		return nil, err
	}
	if !isJSONRPCMethodNotFound(resp) {
		return resp, nil
	}

	legacyResp, legacyErr := a.sendRequest(legacyMethod, params, timeout)
	if legacyErr != nil {
		return nil, legacyErr
	}
	if jsonRPCResultError(legacyResp) == nil {
		a.setLegacyMethods(true)
	}
	return legacyResp, nil
}

func (a *GeminiCLIAgent) readOutputLoop(scanner *bufio.Scanner) {
	a.readOutput(scanner, a.handleJSONRPCResponse, a.HandleOutput)
}

func (a *GeminiCLIAgent) handleJSONRPCResponse(line []byte) bool {
	return handleACPJSONRPCResponse(&a.pendingReqs, line)
}

func (a *GeminiCLIAgent) cancelSession() error {
	a.mu.Lock()
	sessionID := a.sessionID
	a.mu.Unlock()

	params, _ := json.Marshal(map[string]interface{}{
		"sessionId": sessionID,
	})
	method := "cancel"
	if a.usesLegacyMethods() {
		method = legacyGeminiMethod(method)
	}
	return a.sendNotification(method, json.RawMessage(params))
}

func (a *GeminiCLIAgent) formatStartupError(phase string, err error) error {
	return a.processBase.formatStartupError(phase, err, a.PreambleOutput())
}

func legacyGeminiMethod(method string) string {
	switch method {
	case "newSession":
		return "session/new"
	case "loadSession":
		return "session/load"
	case "prompt":
		return "session/prompt"
	case "cancel":
		return "session/cancel"
	case "setSessionMode":
		return "session/set_mode"
	case "unstable_setSessionModel":
		return "session/set_model"
	default:
		return method
	}
}

func (a *GeminiCLIAgent) usesLegacyMethods() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.useLegacyMethods
}

func (a *GeminiCLIAgent) setLegacyMethods(enabled bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.useLegacyMethods = enabled
}
