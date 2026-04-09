package agent

import (
	"context"
	"encoding/json"
	"fmt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/util/version"
)

const KiloPrimaryAgentCode = "code"

// KiloAgent manages a single Kilo ACP process.
type KiloAgent struct {
	acpBase
}

// StartKilo starts a Kilo ACP agent process and performs the handshake.
func StartKilo(ctx context.Context, opts Options, sink OutputSink) (Provider, error) {
	ctx, cancel := context.WithCancel(ctx)

	cmd, preambleDelimiter, metaPrefix := buildShellWrappedCommand(
		ctx, opts.Shell, opts.LoginShell, "kilo", []string{"KILO_CLIENT"}, []string{"acp"}, nil, opts.WorkingDir,
	)

	cmd.Env = filterEnv(cmd.Environ(), "KILO_CLIENT")
	cmd.Env = append(cmd.Env, "LEAPMUX_WORKER=1")
	if opts.LoginShell {
		cmd.Env = append(cmd.Env, "KILO_CLIENT=1")
	}

	stdin, stdout, stderrPipe, err := setupProcessPipes(cmd, cancel)
	if err != nil {
		return nil, err
	}

	a := &KiloAgent{
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
			providerName: "kilo",
			model:        opts.Model,
		},
	}
	a.promptFunc = a.doSendPrompt
	a.reapplySettings = a.reapplyModelAndPrimaryAgent

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start kilo: %w", err)
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
	if requested := StringOrDefault(opts.Model, ""); requested != "" && requested != a.model {
		if err := a.setModel(requested); err != nil {
			cleanup()
			return nil, a.formatStartupError(acpMethodSessionSetModel, err)
		}
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

func fallbackKiloPrimaryAgents() []*leapmuxv1.AvailableOption {
	return []*leapmuxv1.AvailableOption{
		{Id: KiloPrimaryAgentCode, Name: titleCaseID(KiloPrimaryAgentCode, ""), IsDefault: true},
		{Id: OpenCodePrimaryAgentPlan, Name: titleCaseID(OpenCodePrimaryAgentPlan, "")},
	}
}

func (a *KiloAgent) configurePrimaryAgents(modes []acpModeInfo, currentModeID, requestedPrimaryAgent string) error {
	available := buildOpenCodePrimaryAgents(modes, currentModeID)
	hasACPModeList := len(available) > 0
	current := currentModeID
	if !hasACPModeList {
		available = fallbackKiloPrimaryAgents()
		if current == "" {
			current = KiloPrimaryAgentCode
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

func (a *KiloAgent) doSendPrompt(content string, attachments []*leapmuxv1.Attachment) {
	a.doSendACPPrompt(content, attachments, func(resp json.RawMessage) {
		a.handleACPPromptResponse(resp, nil)
	})
}

func (a *KiloAgent) CurrentSettings() *leapmuxv1.AgentSettings {
	return a.primaryAgentCurrentSettings()
}

func (a *KiloAgent) AvailableOptionGroups() []*leapmuxv1.AvailableOptionGroup {
	return a.primaryAgentOptionGroups(fallbackKiloPrimaryAgents())
}

func (a *KiloAgent) UpdateSettings(s *leapmuxv1.AgentSettings) bool {
	return a.primaryAgentUpdateSettings(s)
}

func init() {
	registerProvider(
		leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO,
		func(ctx context.Context, opts Options, sink OutputSink) (Provider, error) {
			return StartKilo(ctx, opts, sink)
		},
		nil, // models discovered dynamically from newSession
		[]*leapmuxv1.AvailableOptionGroup{{
			Key:   OpenCodeExtraPrimaryAgent,
			Label: "Primary Agent",
			Options: []*leapmuxv1.AvailableOption{
				{Id: KiloPrimaryAgentCode, Name: "Code", IsDefault: true},
				{Id: OpenCodePrimaryAgentPlan, Name: "Plan"},
			},
		}},
		"LEAPMUX_KILO_DEFAULT_MODEL",
		"LEAPMUX_KILO_DEFAULT_EFFORT",
		"kilo",
	)
}
