package agent

import (
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
		model: opts.Model,
	}
	a.promptFunc = a.doSendPrompt

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start copilot: %w", err)
	}

	initParams, _ := json.Marshal(map[string]interface{}{
		"protocolVersion": 1,
		"clientInfo":      map[string]string{"name": "leapmux", "title": "LeapMux", "version": version.Value},
		"capabilities":    map[string]interface{}{},
	})
	handshake, err := a.startACPHandshake(stdout, stderrPipe, a.handleOutput, opts, initParams,
		acpSessionConfig{newMethod: acpMethodSessionNew, resumeMethod: acpMethodSessionLoad})
	if err != nil {
		return nil, err
	}

	a.availableModels = buildACPModels(handshake.Models, handshake.CurrentModelID)
	if a.model == "" && handshake.CurrentModelID != "" {
		a.model = handshake.CurrentModelID
	}

	a.availableModes = buildACPModes(handshake.Modes, handshake.CurrentModeID, nil)
	a.permissionMode = handshake.CurrentModeID
	if a.permissionMode == "" {
		a.permissionMode = CopilotCLIModeAgent
	}

	// Parse Copilot-specific configOptions from the raw session response.
	var copilotSession struct {
		ConfigOptions []copilotCLIConfigOption `json:"configOptions"`
	}
	_ = json.Unmarshal(handshake.Raw, &copilotSession)
	a.syncConfigOptions(copilotSession.ConfigOptions)

	cleanup := func() {
		a.Stop()
		_ = a.Wait()
	}
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
	a.mu.Lock()
	defer a.mu.Unlock()
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
	if err := a.acpSetModel(model); err != nil {
		return err
	}
	a.mu.Lock()
	a.model = model
	a.mu.Unlock()
	return nil
}

func (a *CopilotCLIAgent) setPermissionMode(mode string) error {
	a.mu.Lock()
	available := a.availableModes
	a.mu.Unlock()

	if err := a.acpSetMode(mode, available); err != nil {
		return err
	}
	a.mu.Lock()
	a.permissionMode = mode
	a.mu.Unlock()
	return nil
}

func (a *CopilotCLIAgent) cancelSession() error {
	return a.acpCancelSession()
}

func (a *CopilotCLIAgent) handleOutput(line *parsedLine) {
	handleCopilotCLIOutput(a, line)
}

func (a *CopilotCLIAgent) HandleOutput(content []byte) {
	handleCopilotCLIOutput(a, parseLine(content))
}

// syncConfigOptions updates the agent's model and mode from the given config options.
// It returns the updated mode value, or "" if no mode was found.
func (a *CopilotCLIAgent) syncConfigOptions(options []copilotCLIConfigOption) string {
	if len(options) == 0 {
		return ""
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	var updatedMode string
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
				updatedMode = option.CurrentValue
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
	return updatedMode
}
