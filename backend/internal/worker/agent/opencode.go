package agent

import (
	"context"
	"encoding/base64"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
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
	return acpStart(ctx, opts, sink, acpStartSpec[OpenCodeAgent]{
		providerName:   "opencode",
		binaryName:     "opencode",
		baseArgs:       []string{"acp"},
		rcMarkerEnvKey: "OPENCODE_CLIENT",
		sessionConfig:  acpSessionConfig{newMethod: acpMethodSessionNew, resumeMethod: openCodeMethodSessionResume},
		newAgent:       func() *OpenCodeAgent { return &OpenCodeAgent{} },
		base:           func(a *OpenCodeAgent) *acpBase { return &a.acpBase },
		configure: func(a *OpenCodeAgent) {
			a.modeChannel = modeChannelPrimaryAgent
			a.primaryAgentHiddenFilter = isHiddenPrimaryAgent
			a.reapplySettings = a.reapplyModelAndPrimaryAgent
			a.refreshFromSession = a.refreshModelAndPrimaryAgentFromSession
		},
		afterHandshake: func(a *OpenCodeAgent, handshake *acpSessionResult, opts Options) error {
			return a.applyPrimaryAgentStartup(handshake, opts, fallbackOpenCodePrimaryAgents(), OpenCodePrimaryAgentBuild, a.setModel)
		},
	})
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

func (a *OpenCodeAgent) AvailableOptionGroups() []*leapmuxv1.AvailableOptionGroup {
	return a.primaryAgentOptionGroups(fallbackOpenCodePrimaryAgents())
}

// buildACPPromptBlocks converts text + classified attachments into ACP prompt
// blocks compatible with ACP agents.
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
