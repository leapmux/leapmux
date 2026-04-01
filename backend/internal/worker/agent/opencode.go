package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

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

const (
	openCodeMethodSessionResume = "session/resume"
)

// OpenCodeAgent manages a single OpenCode ACP process.
type OpenCodeAgent struct {
	acpBase

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

	stdin, stdout, stderrPipe, err := setupProcessPipes(cmd, cancel)
	if err != nil {
		return nil, err
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
				apiTimeout:         opts.apiTimeout(),
			}},
			sink:         sink,
			providerName: "opencode",
			model:        opts.Model,
		},
	}
	a.promptFunc = a.doSendPrompt

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start opencode: %w", err)
	}

	initParams, err := json.Marshal(map[string]interface{}{
		"protocolVersion": 1,
		"clientInfo":      map[string]string{"name": "leapmux", "title": "LeapMux", "version": version.Value},
		"capabilities":    map[string]interface{}{},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal initialize params: %w", err)
	}
	handshake, err := a.startACPHandshake(stdout, stderrPipe, opts, initParams,
		acpSessionConfig{newMethod: acpMethodSessionNew, resumeMethod: openCodeMethodSessionResume})
	if err != nil {
		return nil, err
	}

	a.availableModels = buildACPModels(handshake.Models, handshake.CurrentModelID, nil)
	if a.model == "" && handshake.CurrentModelID != "" {
		a.model = handshake.CurrentModelID
	}

	cleanup := func() {
		a.Stop()
		_ = a.Wait()
	}
	var requestedPrimaryAgent string
	if opts.ExtraSettings != nil {
		requestedPrimaryAgent = opts.ExtraSettings[OpenCodeExtraPrimaryAgent]
	}
	if err := a.configurePrimaryAgents(handshake.Modes, handshake.CurrentModeID, requestedPrimaryAgent); err != nil {
		cleanup()
		return nil, a.formatStartupError(acpMethodSessionSetMode, err)
	}

	return a, nil
}

func buildOpenCodePrimaryAgents(modes []acpModeInfo, currentModeID string) []*leapmuxv1.AvailableOption {
	// Copy then normalize names: OpenCode agents often report name == id or whitespace-only names.
	normalized := make([]acpModeInfo, len(modes))
	copy(normalized, modes)
	for i := range normalized {
		name := strings.TrimSpace(normalized[i].Name)
		if name == "" || name == normalized[i].ID {
			normalized[i].Name = ""
		} else {
			normalized[i].Name = name
		}
	}
	return buildACPModes(normalized, currentModeID, isHiddenOpenCodePrimaryAgent)
}

func fallbackOpenCodePrimaryAgents() []*leapmuxv1.AvailableOption {
	return []*leapmuxv1.AvailableOption{
		{Id: OpenCodePrimaryAgentBuild, Name: titleCaseID(OpenCodePrimaryAgentBuild, ""), IsDefault: true},
		{Id: OpenCodePrimaryAgentPlan, Name: titleCaseID(OpenCodePrimaryAgentPlan, "")},
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

func (a *OpenCodeAgent) configurePrimaryAgents(modes []acpModeInfo, currentModeID, requestedPrimaryAgent string) error {
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

func (a *OpenCodeAgent) doSendPrompt(content string, attachments []*leapmuxv1.Attachment) {
	a.doSendACPPrompt(content, attachments, func(resp json.RawMessage) {
		a.handleACPPromptResponse(resp, nil)
	})
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

func (a *OpenCodeAgent) setPrimaryAgent(agent string) error {
	a.mu.Lock()
	available := a.availablePrimaryAgents
	a.mu.Unlock()

	if err := a.acpSetMode(agent, available); err != nil {
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

func init() {
	registerProvider(
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		func(ctx context.Context, opts Options, sink OutputSink) (Provider, error) {
			return StartOpenCode(ctx, opts, sink)
		},
		nil, // models discovered dynamically from newSession
		[]*leapmuxv1.AvailableOptionGroup{{
			Key:   OpenCodeExtraPrimaryAgent,
			Label: "Primary Agent",
			Options: []*leapmuxv1.AvailableOption{
				{Id: OpenCodePrimaryAgentBuild, Name: "Build", IsDefault: true},
				{Id: OpenCodePrimaryAgentPlan, Name: "Plan"},
			},
		}},
		"LEAPMUX_OPENCODE_DEFAULT_MODEL",
		"LEAPMUX_OPENCODE_DEFAULT_EFFORT",
		"opencode",
	)
}
