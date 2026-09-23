package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

const (
	PrimaryAgentBuild     = "build"
	PrimaryAgentPlan      = "plan"
	HiddenCompaction      = "compaction"
	openCodeHiddenTitle   = "title"
	openCodeHiddenSummary = "summary"
)

const (
	MethodSessionResume = "session/resume"
)

// FamilyBase is what OpenCode and Kilo share beyond the ACP base.
//
// Both daemons run the same two transports: the Agent Client Protocol stream that
// carries the session, and the daemon's own HTTP server that carries the questions
// the ACP adapter drops. openCodeQuestions states why that second transport exists.
type FamilyBase struct {
	acp.Base
	Questions openCodeQuestions
}

// SendRawInput answers a question through the daemon's HTTP server, and sends every
// other control answer to the ACP stream unchanged.
//
// The two answers cannot share one path. An ACP control request is a JSON-RPC request
// that LeapMux answers by id on the stream it arrived on; a question never reached
// that stream, so it has no id there to answer and the daemon reads its answer from
// a route instead.
func (b *FamilyBase) SendRawInput(raw []byte) error {
	if handled, err := b.Questions.answer(b.Context(), raw); handled {
		return err
	}
	return b.Base.SendRawInput(raw)
}

// SupportsSteering always reports true, and overrides the Base answer.
//
// This family steers with a plain second session/prompt on the SAME session, which
// is the Agent Client Protocol's own mechanism and not one provider's extension --
// so it needs no advertised steer method. The Base implementation reads
// b.steerMethod, which advertisedACPSteerMethod leaves empty for every provider
// except Goose and Reasonix.
//
// It sits on the FAMILY, not on Agent. Kilo runs the same daemon and the
// same session methods, and while this answer lived one type lower its Steer control
// was dead with nothing to say why.
func (b *FamilyBase) SupportsSteering() bool { return true }

// SteerInput sends the steer as a second prompt on the running session.
//
// A refusal reaches the READER, not the log alone. The steer is something the reader
// typed and watched for, and a daemon that declines a concurrent prompt -- which the
// protocol does not oblige it to accept -- otherwise swallowed those words with no
// trace anywhere the reader can see.
//
// A STOPPED agent states nothing: the reader ended the turn themselves, so a steer
// that did not land is the outcome they asked for.
func (b *FamilyBase) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	if !b.PromptActive() {
		return agent.ErrNoActiveTurn
	}
	return b.SendPromptDetached(content, attachments, func(_ json.RawMessage, err error) {
		if err == nil || b.IsStopped() {
			return
		}
		slog.Error("acp steer failed", "agent_id", b.AgentID(), "provider", b.ProviderName(), "error", err)
		b.Sink().PersistLeapMuxNotification(map[string]interface{}{
			contracts.NotificationFieldType:  contracts.NotificationTypeAgentError,
			contracts.NotificationFieldError: fmt.Sprintf("steer failed: %v", err),
		})
	})
}

// Agent manages a single OpenCode ACP process.
type Agent struct {
	FamilyBase
}

// Start starts an OpenCode ACP agent process and performs the handshake.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	return StartFamily(ctx, opts, sink, FamilySpec{
		Provider:            leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		Locator:             opencodeLocator,
		ProviderName:        "opencode",
		OptionGroups:        opencodeStaticOptionGroups,
		RCMarkerEnvKey:      "OPENCODE_CLIENT",
		QuestionToolEnv:     openCodeQuestionToolEnv,
		DefaultPrimaryAgent: PrimaryAgentBuild,
	}, func() *Agent { return &Agent{} }, func(a *Agent) *FamilyBase { return &a.FamilyBase })
}

func fallbackOpenCodePrimaryAgents() []*leapmuxv1.AvailableOption {
	return []*leapmuxv1.AvailableOption{
		{Id: PrimaryAgentBuild, Name: providerkit.TitleCaseID(PrimaryAgentBuild, "")},
		{Id: PrimaryAgentPlan, Name: providerkit.TitleCaseID(PrimaryAgentPlan, "")},
	}
}

// IsHiddenPrimaryAgent reports whether a primary-agent id is an internal
// pseudo-agent that must be hidden from the picker. These ids originate in
// OpenCode's protocol but are shared by every OpenCode-family ACP provider
// (Kilo included), so both inject this as their primaryAgentHiddenFilter.
func IsHiddenPrimaryAgent(id string) bool {
	switch id {
	case HiddenCompaction, openCodeHiddenTitle, openCodeHiddenSummary:
		return true
	default:
		return false
	}
}

// OpenCode and Kilo identify the native task before its arguments arrive.
// Other titles require the native prompt and subagent discriminator.
func SubagentFromToolCall(tc acp.ToolCallEnvelope) *acp.SubagentObservation {
	return openCodeSpawnObservation(tc.ToolCallID, tc.Title, tc.RawInput, tc.Title == "task" && tc.Kind == "think")
}

// Build the registry row from a known native task or its later arguments.
// Both event paths use the tool-call ID, so later arguments update the same row.
func openCodeSpawnObservation(toolCallID, callTitle string, rawInput json.RawMessage, knownTask bool) *acp.SubagentObservation {
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
	return &acp.SubagentObservation{
		RowKey:        toolCallID,
		Title:         title,
		Status:        bgtask.StatusRunning,
		ChildAgentKey: toolCallID,
		Prompt:        prompt,
		Spawns:        true,
	}
}

// SubagentFromToolCallUpdate closes the registry row on a final
// status, and when rawOutput.metadata.sessionId is present, re-keys the row to
// the child session id (the metadata surfaces only on the final update).
// The spawn row was opened under the toolCallId, so SpawnRowKey carries it to
// keep the close from leaking it as a Running row.
func SubagentFromToolCallUpdate(tcu acp.ToolCallUpdateEnvelope) *acp.SubagentObservation {
	if !acp.StatusIsFinal(tcu.Status) {
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
	return &acp.SubagentObservation{
		RowKey:        rowKey,
		RenameFrom:    renameFrom,
		ChildAgentKey: rowKey,
		Status:        acp.FinalStatus(tcu.Status),
		CloseRow:      true,
		Mode:          acp.ModeCloseOnly,
		ReportID:      tcu.ToolCallID,
		Report: agent.SubagentReport{
			Text: openCodeSubagentReport(acp.ToolCallText(tcu.Content), background),
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

// FamilyRegistration builds the Registration of an OpenCode-protocol provider
// (OpenCode, Kilo). The two run different daemons but share the SAME registration shape: a
// primaryAgent secondary channel with a per-daemon fallback agent list, dynamically-discovered
// models, and the server-driven "effort" config option (the daemon's per-model reasoning
// variants, surfaced under the well-known id). Only the provider enum, the plugin, the Start
// function, the locator, the fallback agents and the env keys vary -- so each provider's
// registration reduces to one call here, mirroring the frontend's
// registerOpenCodeProtocolProvider, instead of two near-identical registrations that can drift.
func FamilyRegistration(
	provider leapmuxv1.AgentProvider,
	plugin agent.Provider,
	start agent.StartFunc,
	locator launch.Locator,
	optionGroups []*leapmuxv1.AvailableOptionGroup,
	envModelKey, envEffortKey string,
) agent.Registration {
	return agent.Registration{
		Provider: provider,
		Plugin:   plugin,
		Start:    start,
		Locator:  locator,
		// Models are discovered dynamically from newSession.
		DefaultModels:       nil,
		OptionGroups:        optionGroups,
		AdditionalOptionIDs: []string{agent.OptionIDEffort},
		EnvModelKey:         envModelKey,
		EnvEffortKey:        envEffortKey,
	}
}

// opencodeStaticOptionGroups holds OpenCode's static primary-agent group. The
// factory registration and Start both read this one value.
var opencodeStaticOptionGroups = acp.StaticSecondaryGroup(acp.ModeChannelPrimaryAgent, fallbackOpenCodePrimaryAgents())

// Compile-time proof that Agent implements Agent. acp.Start is generic over
// T and can only assert this at runtime (any(a).(Agent)); this guard turns a
// dropped or renamed method into a build error rather than a launch-time
// "does not implement Agent".
var _ agent.Agent = (*Agent)(nil)

// Agent steers through the family base. Manager.SupportsSteering answers
// false, with no build error, for a provider that stops satisfying InputSteerer,
// so this assertion makes that regression a compile error.
var _ agent.InputSteerer = (*Agent)(nil)

// opencodeLocator finds the OpenCode CLI on the user's PATH.
var opencodeLocator = launch.Binaries("opencode")

// Registration states everything the worker knows about OpenCode before
// any of its agents runs.
func Registration() agent.Registration {
	return FamilyRegistration(
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		opencodeProvider{},
		Start,
		opencodeLocator,
		opencodeStaticOptionGroups,
		"LEAPMUX_OPENCODE_DEFAULT_MODEL", "LEAPMUX_OPENCODE_DEFAULT_EFFORT",
	)
}
