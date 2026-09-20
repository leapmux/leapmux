package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
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

// openCodeFamilyBase is what OpenCode and Kilo share beyond the ACP base.
//
// Both daemons run the same two transports: the Agent Client Protocol stream that
// carries the session, and the daemon's own HTTP server that carries the questions
// the ACP adapter drops. openCodeQuestions states why that second transport exists.
type openCodeFamilyBase struct {
	acpBase
	questions openCodeQuestions
}

// SendRawInput answers a question through the daemon's HTTP server, and sends every
// other control answer to the ACP stream unchanged.
//
// The two answers cannot share one path. An ACP control request is a JSON-RPC request
// that LeapMux answers by id on the stream it arrived on; a question never reached
// that stream, so it has no id there to answer and the daemon reads its answer from
// a route instead.
func (b *openCodeFamilyBase) SendRawInput(raw []byte) error {
	if handled, err := b.questions.answer(b.ctx, raw); handled {
		return err
	}
	return b.acpBase.SendRawInput(raw)
}

// SupportsSteering always reports true, and overrides the acpBase answer.
//
// This family steers with a plain second session/prompt on the SAME session, which
// is the Agent Client Protocol's own mechanism and not one provider's extension --
// so it needs no advertised steer method. The acpBase implementation reads
// b.steerMethod, which advertisedACPSteerMethod leaves empty for every provider
// except Goose and Reasonix.
//
// It sits on the FAMILY, not on OpenCodeAgent. Kilo runs the same daemon and the
// same session methods, and while this answer lived one type lower its Steer control
// was dead with nothing to say why.
func (b *openCodeFamilyBase) SupportsSteering() bool { return true }

// SteerInput sends the steer as a second prompt on the running session.
//
// A refusal reaches the READER, not the log alone. The steer is something the reader
// typed and watched for, and a daemon that declines a concurrent prompt -- which the
// protocol does not oblige it to accept -- otherwise swallowed those words with no
// trace anywhere the reader can see.
//
// A STOPPED agent states nothing: the reader ended the turn themselves, so a steer
// that did not land is the outcome they asked for.
func (b *openCodeFamilyBase) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	b.mu.Lock()
	active := b.promptActive
	b.mu.Unlock()
	if !active {
		return ErrNoActiveTurn
	}
	return b.sendACPPromptDetached(content, attachments, func(_ json.RawMessage, err error) {
		if err == nil || b.IsStopped() {
			return
		}
		slog.Error("acp steer failed", "agent_id", b.agentID, "provider", b.providerName, "error", err)
		b.sink.PersistLeapMuxNotification(map[string]interface{}{
			contracts.NotificationFieldType:  contracts.NotificationTypeAgentError,
			contracts.NotificationFieldError: fmt.Sprintf("steer failed: %v", err),
		})
	})
}

// OpenCodeAgent manages a single OpenCode ACP process.
type OpenCodeAgent struct {
	openCodeFamilyBase
}

// StartOpenCode starts an OpenCode ACP agent process and performs the handshake.
func StartOpenCode(ctx context.Context, opts Options, sink ProviderServices) (Agent, error) {
	return acpStart(ctx, opts, sink, acpStartSpec[OpenCodeAgent]{
		provider:       leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		providerName:   "opencode",
		binaryName:     "opencode",
		baseArgs:       openCodeACPArgs(),
		rcMarkerEnvKey: "OPENCODE_CLIENT",
		pinnedEnv:      []string{openCodeQuestionToolEnv + "=1"},
		sessionConfig:  acpSessionConfig{newMethod: acpMethodSessionNew, resumeMethod: openCodeMethodSessionResume},
		newAgent:       func() *OpenCodeAgent { return &OpenCodeAgent{} },
		base:           func(a *OpenCodeAgent) *acpBase { return &a.acpBase },
		configure: func(a *OpenCodeAgent) {
			a.modeChannel = modeChannelPrimaryAgent
			a.primaryAgentHiddenFilter = isHiddenPrimaryAgent
			a.questions.configure(sink)
			// OpenCode omits child-session events from the root ACP stream. The
			// prompt and final task result still form an inspectable transcript.
			a.subagentFromToolCall = openCodeSubagentFromToolCall
			a.subagentFromToolCallUpdate = openCodeSubagentFromToolCallUpdate
		},
		afterHandshake: func(a *OpenCodeAgent, handshake *acpSessionResult, opts Options) error {
			// The launched process is the shell, which a POSIX shell replaces with the
			// daemon and PowerShell does not. Discovery walks below it for that case.
			a.questions.begin(a.ctx, a.agentID, a.cmd.Process.Pid)
			return a.applyPrimaryAgentStartup(handshake, opts, OpenCodePrimaryAgentBuild)
		},
	})
}

func fallbackOpenCodePrimaryAgents() []*leapmuxv1.AvailableOption {
	return []*leapmuxv1.AvailableOption{
		{Id: OpenCodePrimaryAgentBuild, Name: titleCaseID(OpenCodePrimaryAgentBuild, "")},
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

// OpenCode and Kilo identify the native task before its arguments arrive.
// Other titles require the native prompt and subagent discriminator.
func openCodeSubagentFromToolCall(tc acpToolCallEnvelope) *acpSubagentObservation {
	return openCodeSpawnObservation(tc.ToolCallID, tc.Title, tc.RawInput, tc.Title == "task" && tc.Kind == "think")
}

// Build the registry row from a known native task or its later arguments.
// Both event paths use the tool-call ID, so later arguments update the same row.
func openCodeSpawnObservation(toolCallID, callTitle string, rawInput json.RawMessage, knownTask bool) *acpSubagentObservation {
	var input struct {
		Description  string          `json:"description"`
		Prompt       json.RawMessage `json:"prompt"`
		SubagentType string          `json:"subagent_type"`
		// Some builds spell it with a different key; the shape still carries a
		// prompt + a type, so detect on the union.
		SubagentID string `json:"subagentID"`
	}
	if len(rawInput) == 0 && !knownTask {
		return nil
	}
	if err := json.Unmarshal(rawInput, &input); err != nil && !knownTask {
		return nil
	}
	// An unidentified tool requires a prompt and a subagent discriminator.
	if !knownTask && (len(input.Prompt) == 0 || (input.SubagentType == "" && input.SubagentID == "")) {
		return nil
	}
	title := callTitle
	if title == "" || title == "task" {
		title = input.Description
	}
	if title == "" {
		title = input.SubagentType
	}
	if title == "" {
		title = "Subagent"
	}
	prompt := ""
	_ = json.Unmarshal(input.Prompt, &prompt)
	return &acpSubagentObservation{
		RowKey:        toolCallID,
		Title:         title,
		Status:        bgtask.StatusRunning,
		ChildAgentKey: toolCallID,
		Prompt:        prompt,
		Spawns:        true,
	}
}

// openCodeSubagentFromToolCallUpdate closes the registry row on a final
// status, and when rawOutput.metadata.sessionId is present, re-keys the row to
// the child session id (the metadata surfaces only on the final update).
// The spawn row was opened under the toolCallId, so SpawnRowKey carries it to
// keep the close from leaking it as a Running row.
func openCodeSubagentFromToolCallUpdate(tcu acpToolCallUpdateEnvelope) *acpSubagentObservation {
	if !acpStatusIsFinal(tcu.Status) {
		// Not final: this is where Kilo first reveals the spawn shape (its
		// tool_call carries `rawInput: {}`), so run the same detection here.
		// Without it a Kilo spawn produced no registry row at all -- the
		// final update below then closed a row that was never opened.
		//
		// A spawn-shaped update that arrives AFTER the final one re-creates the
		// row under the toolCallId, because the final update already renamed the
		// original to the child session id. A `session/load` history replay then
		// redelivers the final update, whose rename collides with the surviving
		// session-id row. RenameBackgroundTask resolves that collision by dropping
		// the re-created duplicate, so the replay converges on one row instead of
		// leaving a Running row that no later event closes.
		return openCodeSpawnObservation(tcu.ToolCallID, tcu.Title, tcu.RawInput, false)
	}
	// The final rawOutput may carry the child session id under metadata.
	rowKey := tcu.ToolCallID
	renameFrom := ""
	background := false
	if len(tcu.RawOutput) > 0 {
		var out struct {
			Metadata struct {
				SessionID  string `json:"sessionId"`
				Background bool   `json:"background"`
			} `json:"metadata"`
		}
		if json.Unmarshal(tcu.RawOutput, &out) == nil {
			background = out.Metadata.Background
		}
		if out.Metadata.SessionID != "" {
			// Rename the spawn row (toolCallId) to the child session id so one
			// row tracks the lifecycle, then give a final status to it.
			rowKey = out.Metadata.SessionID
			renameFrom = tcu.ToolCallID
		}
	}
	return &acpSubagentObservation{
		RowKey:        rowKey,
		RenameFrom:    renameFrom,
		ChildAgentKey: rowKey,
		Status:        acpFinalStatus(tcu.Status),
		CloseRow:      true,
		Mode:          acpModeCloseOnly,
		Report: subagentReport{
			Text: openCodeSubagentReport(acpToolCallText(tcu.Content), background),
		},
	}
}

func openCodeSubagentReport(text string, background bool) string {
	if background {
		return ""
	}
	text = strings.TrimSpace(text)
	const start = "<task_result>"
	const end = "</task_result>"
	if _, after, ok := strings.Cut(text, start); ok {
		if report, _, found := strings.Cut(after, end); found {
			return strings.TrimSpace(report)
		}
	}
	return text
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

// registerOpenCodeFamilyProvider registers an OpenCode-protocol provider (OpenCode, Kilo). The
// two run different daemons but share the SAME registration shape: a primaryAgent secondary
// channel with a per-daemon fallback agent list, dynamically-discovered models, and the
// server-driven "effort" config option (the daemon's per-model reasoning variants, surfaced under
// the well-known id). Only the provider enum, Start function, fallback agents, env keys, and
// binary name vary -- so each init() reduces to one call here, mirroring the frontend's
// registerOpenCodeProtocolProvider, instead of two near-identical registration blocks that can drift.
func registerOpenCodeFamilyProvider(
	provider leapmuxv1.AgentProvider,
	start startFunc,
	fallbackPrimaryAgents []*leapmuxv1.AvailableOption,
	envModelKey, envEffortKey, binaryName string,
) {
	registerAgentFactory(
		provider,
		start,
		nil, // models discovered dynamically from newSession
		staticSecondaryGroup(modeChannelPrimaryAgent, fallbackPrimaryAgents),
		envModelKey,
		envEffortKey,
		binaryName,
	)
	setAdditionalOptionIDs(provider, OptionIDEffort)
}

func init() {
	registerOpenCodeFamilyProvider(
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		StartOpenCode,
		fallbackOpenCodePrimaryAgents(),
		"LEAPMUX_OPENCODE_DEFAULT_MODEL", "LEAPMUX_OPENCODE_DEFAULT_EFFORT", "opencode",
	)
}
