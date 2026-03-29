package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"syscall"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/util/version"
)

const (
	CopilotCLIModeAgent     = "https://agentclientprotocol.com/protocol/session-modes#agent"
	CopilotCLIModePlan      = "https://agentclientprotocol.com/protocol/session-modes#plan"
	CopilotCLIModeAutopilot = "https://agentclientprotocol.com/protocol/session-modes#autopilot"
)

type copilotCLIModeInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type copilotCLIModelInfo struct {
	ModelID     string `json:"modelId"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type copilotCLIConfigOption struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	CurrentValue string `json:"currentValue"`
	Options      []struct {
		Value string `json:"value"`
		Name  string `json:"name"`
	} `json:"options"`
}

// CopilotCLIAgent manages a single Copilot CLI ACP process.
type CopilotCLIAgent struct {
	acpBase

	model          string
	permissionMode string
	workingDir     string

	availableModels []*leapmuxv1.AvailableModel
	availableModes  []*leapmuxv1.AvailableOption
}

// StartCopilotCLI starts a Copilot CLI ACP agent process and performs the handshake.
func StartCopilotCLI(ctx context.Context, opts Options, sink OutputSink) (Provider, error) {
	ctx, cancel := context.WithCancel(ctx)

	cmd, preambleDelimiter, metaPrefix := buildShellWrappedCommand(
		ctx, opts.Shell, opts.LoginShell, "copilot", nil, []string{"--acp", "--stdio"}, nil, opts.WorkingDir,
	)

	cmd.Env = append(cmd.Environ(), "LEAPMUX_WORKER=1")
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

	a := &CopilotCLIAgent{
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
		return nil, fmt.Errorf("start copilot: %w", err)
	}

	a.drainStderr(stderrPipe)

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	go a.readOutputLoop(scanner, a.handleOutput)

	cleanup := func() {
		a.Stop()
		_ = a.Wait()
	}

	timeout := opts.startupTimeout()

	initParams, _ := json.Marshal(map[string]interface{}{
		"protocolVersion": 1,
		"clientInfo":      map[string]string{"name": "leapmux", "title": "LeapMux", "version": version.Value},
		"capabilities":    map[string]interface{}{},
	})
	initResp, err := a.sendRequest(acpMethodInitialize, json.RawMessage(initParams), timeout)
	if err != nil {
		cleanup()
		return nil, a.formatStartupError("initialize", err)
	}
	if err := jsonRPCResultError(initResp); err != nil {
		cleanup()
		return nil, a.formatStartupError("initialize", err)
	}

	sessionMethod, sessionParams := buildCopilotSessionRequest(opts.ResumeSessionID, opts.WorkingDir)
	sessionResp, err := a.sendRequest(sessionMethod, json.RawMessage(sessionParams), timeout)
	if err != nil {
		if opts.ResumeSessionID != "" {
			slog.Warn("session/load failed, falling back to session/new",
				"agent_id", a.agentID, "session_id", opts.ResumeSessionID, "error", err)
			_, fallbackParams := buildCopilotSessionRequest("", opts.WorkingDir)
			sessionResp, err = a.sendRequest(acpMethodSessionNew, json.RawMessage(fallbackParams), timeout)
		}
		if err != nil {
			cleanup()
			return nil, a.formatStartupError(sessionMethod, err)
		}
	}
	if err := jsonRPCResultError(sessionResp); err != nil {
		cleanup()
		return nil, a.formatStartupError(sessionMethod, err)
	}

	var session struct {
		SessionID string `json:"sessionId"`
		Models    struct {
			CurrentModelID  string                `json:"currentModelId"`
			AvailableModels []copilotCLIModelInfo `json:"availableModels"`
		} `json:"models"`
		Modes struct {
			CurrentModeID  string               `json:"currentModeId"`
			AvailableModes []copilotCLIModeInfo `json:"availableModes"`
		} `json:"modes"`
		ConfigOptions []copilotCLIConfigOption `json:"configOptions"`
	}
	if err := json.Unmarshal(sessionResp, &session); err != nil {
		cleanup()
		return nil, a.formatStartupError("session/new parse", err)
	}
	if session.SessionID == "" && opts.ResumeSessionID != "" && sessionMethod == acpMethodSessionLoad {
		session.SessionID = opts.ResumeSessionID
	}
	if session.SessionID == "" {
		cleanup()
		return nil, a.formatStartupError(sessionMethod, fmt.Errorf("response did not contain a session ID"))
	}

	a.sessionID = session.SessionID
	sink.UpdateSessionID(a.sessionID)
	sink.BroadcastStatusActive(a.sessionID)

	a.availableModels = buildCopilotCLIModels(session.Models.AvailableModels, session.Models.CurrentModelID)
	if a.model == "" && session.Models.CurrentModelID != "" {
		a.model = session.Models.CurrentModelID
	}

	a.availableModes = buildCopilotCLIModes(session.Modes.AvailableModes, session.Modes.CurrentModeID)
	a.permissionMode = session.Modes.CurrentModeID
	if a.permissionMode == "" {
		a.permissionMode = CopilotCLIModeAgent
	}
	a.syncConfigOptions(session.ConfigOptions)

	if requested := StringOrDefault(opts.PermissionMode, ""); requested != "" && requested != a.permissionMode {
		if err := a.setPermissionMode(requested); err != nil {
			cleanup()
			return nil, a.formatStartupError("session/set_mode", err)
		}
	}
	if requested := StringOrDefault(opts.Model, ""); requested != "" && requested != a.model {
		if err := a.setModel(requested); err != nil {
			cleanup()
			return nil, a.formatStartupError("session/set_model", err)
		}
	}

	return a, nil
}

func buildCopilotSessionRequest(resumeSessionID, workingDir string) (method string, params []byte) {
	return buildACPSessionRequest(resumeSessionID, workingDir, acpMethodSessionNew, acpMethodSessionLoad)
}

func buildCopilotCLIModels(models []copilotCLIModelInfo, currentModelID string) []*leapmuxv1.AvailableModel {
	result := make([]*leapmuxv1.AvailableModel, 0, len(models))
	for _, m := range models {
		if m.ModelID == "" {
			continue
		}
		result = append(result, &leapmuxv1.AvailableModel{
			Id:          m.ModelID,
			DisplayName: m.Name,
			Description: m.Description,
			IsDefault:   m.ModelID == currentModelID,
		})
	}
	return result
}

func buildCopilotCLIModes(modes []copilotCLIModeInfo, currentModeID string) []*leapmuxv1.AvailableOption {
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

func fallbackCopilotCLIModes() []*leapmuxv1.AvailableOption {
	return []*leapmuxv1.AvailableOption{
		{Id: CopilotCLIModeAgent, Name: "Agent", IsDefault: true},
		{Id: CopilotCLIModePlan, Name: "Plan"},
		{Id: CopilotCLIModeAutopilot, Name: "Autopilot"},
	}
}

func (a *CopilotCLIAgent) doSendPrompt(content string, attachments []*leapmuxv1.Attachment) {
	a.sendPrompt(content, attachments,
		func(params json.RawMessage) (json.RawMessage, error) {
			return a.sendRequest(acpMethodSessionPrompt, params, 10*time.Minute)
		},
		a.handlePromptResponse,
	)
}

func (a *CopilotCLIAgent) handlePromptResponse(resp json.RawMessage) {
	a.handleACPPromptResponse(resp, nil)
}

func (a *CopilotCLIAgent) CurrentSettings() *leapmuxv1.AgentSettings {
	a.mu.Lock()
	defer a.mu.Unlock()
	return &leapmuxv1.AgentSettings{
		Model:          a.model,
		PermissionMode: a.permissionMode,
	}
}

func (a *CopilotCLIAgent) AvailableModels() []*leapmuxv1.AvailableModel {
	return a.availableModels
}

func (a *CopilotCLIAgent) AvailableOptionGroups() []*leapmuxv1.AvailableOptionGroup {
	a.mu.Lock()
	defer a.mu.Unlock()
	options := a.availableModes
	if len(options) == 0 {
		options = fallbackCopilotCLIModes()
	}
	return []*leapmuxv1.AvailableOptionGroup{{
		Key:     "permissionMode",
		Label:   "Mode",
		Options: options,
	}}
}

func (a *CopilotCLIAgent) UpdateSettings(s *leapmuxv1.AgentSettings) bool {
	if model := s.GetModel(); model != "" {
		if err := a.setModel(model); err != nil {
			slog.Warn("copilot session/set_model failed", "agent_id", a.agentID, "error", err)
			return false
		}
	}
	if mode := s.GetPermissionMode(); mode != "" {
		if err := a.setPermissionMode(mode); err != nil {
			slog.Warn("copilot session/set_mode failed", "agent_id", a.agentID, "error", err)
			return false
		}
	}
	return true
}

func (a *CopilotCLIAgent) setModel(model string) error {
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

func (a *CopilotCLIAgent) setPermissionMode(mode string) error {
	a.mu.Lock()
	sessionID := a.sessionID
	available := a.availableModes
	a.mu.Unlock()

	if len(available) > 0 && !hasACPOption(available, mode) {
		return fmt.Errorf("unknown mode: %s", mode)
	}

	params, _ := json.Marshal(map[string]interface{}{
		"sessionId": sessionID,
		"modeId":    mode,
	})
	resp, err := a.sendRequest(acpMethodSessionSetMode, json.RawMessage(params), 10*time.Second)
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

func (a *CopilotCLIAgent) cancelSession() error {
	a.mu.Lock()
	sessionID := a.sessionID
	a.mu.Unlock()

	params, _ := json.Marshal(map[string]interface{}{
		"sessionId": sessionID,
	})
	return a.sendNotification(acpMethodSessionCancel, json.RawMessage(params))
}

func (a *CopilotCLIAgent) handleOutput(line *parsedLine) {
	handleCopilotCLIOutput(a, line)
}

func (a *CopilotCLIAgent) HandleOutput(content []byte) {
	handleCopilotCLIOutput(a, parseLine(content))
}

func (a *CopilotCLIAgent) syncConfigOptions(options []copilotCLIConfigOption) {
	if len(options) == 0 {
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	for _, option := range options {
		switch option.ID {
		case "model":
			if option.CurrentValue != "" {
				a.model = option.CurrentValue
			}
			if len(option.Options) > 0 {
				models := make([]*leapmuxv1.AvailableModel, 0, len(option.Options))
				for _, candidate := range option.Options {
					if candidate.Value == "" {
						continue
					}
					name := candidate.Name
					if name == "" {
						name = candidate.Value
					}
					models = append(models, &leapmuxv1.AvailableModel{
						Id:          candidate.Value,
						DisplayName: name,
						IsDefault:   candidate.Value == option.CurrentValue,
					})
				}
				if len(models) > 0 {
					a.availableModels = models
				}
			}
		case "mode":
			if option.CurrentValue != "" {
				a.permissionMode = option.CurrentValue
			}
			if len(option.Options) > 0 {
				modes := make([]*leapmuxv1.AvailableOption, 0, len(option.Options))
				for _, candidate := range option.Options {
					if candidate.Value == "" {
						continue
					}
					name := candidate.Name
					if name == "" {
						name = candidate.Value
					}
					modes = append(modes, &leapmuxv1.AvailableOption{
						Id:        candidate.Value,
						Name:      name,
						IsDefault: candidate.Value == option.CurrentValue,
					})
				}
				if len(modes) > 0 {
					a.availableModes = modes
				}
			}
		}
	}
}
