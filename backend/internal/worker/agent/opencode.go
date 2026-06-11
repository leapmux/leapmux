package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/envutil"
)

const (
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
}

// StartOpenCode starts an OpenCode ACP agent process and performs the handshake.
func StartOpenCode(ctx context.Context, opts Options, sink OutputSink) (Agent, error) {
	ctx, cancel := context.WithCancel(ctx)

	cmd, preambleDelimiter, metaPrefix := buildShellWrappedCommand(
		ctx, opts.Shell, opts.LoginShell, "opencode", []string{"OPENCODE_CLIENT"}, []string{"acp"}, nil, opts.WorkingDir,
	)

	cmd.Env = envutil.FilterEnv(cmd.Environ(), "OPENCODE_CLIENT")
	if opts.LoginShell {
		cmd.Env = append(cmd.Env, "OPENCODE_CLIENT=1")
	}
	cmd.Env = FinalizeAgentEnv(cmd.Env, opts)

	stdin, stdout, stderrPipe, err := setupProcessPipes(cmd, cancel)
	if err != nil {
		return nil, err
	}

	a := &OpenCodeAgent{
		acpBase: acpBase{
			jsonrpcBase: jsonrpcBase{processBase: newProcessBase(opts, "opencode", cmd, stdin, ctx, cancel, preambleDelimiter, metaPrefix)},
			sink:        sink,
			model:       opts.Model,
		},
	}
	a.modeChannel = modeChannelPrimaryAgent
	a.primaryAgentHiddenFilter = isHiddenPrimaryAgent
	a.promptFunc = a.doSendPrompt
	a.reapplySettings = a.reapplyModelAndPrimaryAgent
	a.refreshFromSession = a.refreshModelAndPrimaryAgentFromSession

	if err := a.startCmd(cmd, cancel); err != nil {
		return nil, err
	}

	initParams, err := acpStandardInitParams()
	if err != nil {
		return nil, err
	}
	handshake, err := a.startACPHandshake(stdout, stderrPipe, opts, initParams,
		acpSessionConfig{newMethod: acpMethodSessionNew, resumeMethod: openCodeMethodSessionResume})
	if err != nil {
		return nil, err
	}

	if err := a.applyPrimaryAgentStartup(handshake, opts, fallbackOpenCodePrimaryAgents(), OpenCodePrimaryAgentBuild, a.setModel); err != nil {
		return nil, err
	}

	return a, nil
}

func fallbackOpenCodePrimaryAgents() []*leapmuxv1.AvailableOption {
	return []*leapmuxv1.AvailableOption{
		{Id: OpenCodePrimaryAgentBuild, Name: titleCaseID(OpenCodePrimaryAgentBuild, ""), IsDefault: true},
		{Id: OpenCodePrimaryAgentPlan, Name: titleCaseID(OpenCodePrimaryAgentPlan, "")},
	}
}

// isHiddenPrimaryAgent reports whether a primary-agent id is an internal
// pseudo-agent that must be hidden from the picker. These ids originate in
// OpenCode's protocol but are shared by every OpenCode-family ACP provider
// (Kilo included), so both inject this as their primaryAgentHiddenFilter.
func isHiddenPrimaryAgent(id string) bool {
	switch id {
	case openCodeHiddenCompaction, openCodeHiddenTitle, openCodeHiddenSummary:
		return true
	default:
		return false
	}
}

func (a *OpenCodeAgent) doSendPrompt(content string, attachments []*leapmuxv1.Attachment) {
	a.doSendACPPrompt(content, attachments, func(resp json.RawMessage) {
		a.handleACPPromptResponse(resp, nil)
	})
}

func (a *OpenCodeAgent) AvailableOptionGroups() []*leapmuxv1.AvailableOptionGroup {
	return a.primaryAgentOptionGroups(fallbackOpenCodePrimaryAgents())
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

func init() {
	registerAgentFactory(
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		func(ctx context.Context, opts Options, sink OutputSink) (Agent, error) {
			return StartOpenCode(ctx, opts, sink)
		},
		nil, // models discovered dynamically from newSession
		[]*leapmuxv1.AvailableOptionGroup{{
			Key:     OptionGroupKeyPrimaryAgent,
			Label:   "Primary Agent",
			Options: fallbackOpenCodePrimaryAgents(),
		}},
		"LEAPMUX_OPENCODE_DEFAULT_MODEL",
		"LEAPMUX_OPENCODE_DEFAULT_EFFORT",
		"opencode",
	)
}
