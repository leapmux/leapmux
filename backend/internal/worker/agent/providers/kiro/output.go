package kiro

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// Kiro states the metadata of a session on `session_info_update`, under
// `_meta.kiro.kind`. These are the kinds that LeapMux reads. turn_end is in the
// contract, because the browser reads it too.
const (
	kiroKindTurnStart           = "turn_start"
	kiroKindTurnCompletion      = "turn_completion"
	kiroKindContextUsage        = "context_usage"
	kiroKindDisplayError        = "display_error"
	kiroKindInteractionResolved = "interaction_resolved"
	kiroKindSummarizationStart  = "summarization_started"
	kiroKindSummarizationDone   = "summarization_completed"
	kiroKindSummarizationFailed = "summarization_failed"
	kiroKindHookUpdate          = "hook_update"
	kiroKindRecap               = "recap"
)

// The kinds that state nothing a LeapMux transcript or card shows. Each one is
// listed, so a kind that a later build of Kiro adds is logged as unknown.
var kiroSilentKinds = map[string]string{
	"pending_interaction":      "the request itself follows it",
	"user_message_id_assigned": "LeapMux keeps its own message ids",
	"focus_update":             "LeapMux names its tabs itself and follows the turn state from the turn markers",
	"steering_queued":          "the input queue of the worker states a steer",
	"steering_injected":        "the input queue of the worker states a steer",
	"steering_cleared":         "the input queue of the worker states a steer",
	"steering_inclusion":       "the steering documents are Kiro's own context",
	"queued":                   "the turn waits inside Kiro, and its markers follow",
	"repositories_update":      "LeapMux reads the working directory itself",
	"summarization_separator":  "Kiro sends it only on a replay, which LeapMux turns off",
	"summary_message":          "Kiro sends it only on a replay, which LeapMux turns off",
}

// Kiro's stop reasons of a turn, beside the protocol's own end_turn and
// cancelled.
const (
	kiroStopMaxTokens = "max_tokens"
	kiroStopError     = "error"
)

// kiroMetaAgentInitiated marks each update of a turn that Kiro started by
// itself: a workflow that finished wakes its parent, a workflow step sends a
// warning, or a cancel leaves a workflow notice undelivered. Kiro copies the
// `_meta.kiro` of the prompt into every chunk and tool call of its turn, and
// its own prompt for such a turn states this key.
const kiroMetaAgentInitiated = "agentInitiated"

// kiroMetaReplayID identifies the answer of the model that one text or
// thought chunk belongs to. Every chunk of one answer states the same id, and
// the next answer states another.
const kiroMetaReplayID = "replayId"

// chunkMessageID reads the answer that one chunk belongs to, from the `_meta` of
// the update. It is "" for a chunk that states none.
func chunkMessageID(metadata map[string]json.RawMessage) string {
	return fieldString(kiroFields(metadata[contracts.KiroMetaNamespace]), kiroMetaReplayID)
}

// kiroFields reads the `_meta.kiro` object of one update into its fields. It is
// nil for an update that carries none.
func kiroFields(raw json.RawMessage) map[string]json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil
	}
	return fields
}

// fieldString reads one string field, or "" for a field that is absent or not a
// string.
func fieldString(fields map[string]json.RawMessage, key string) string {
	var value string
	if raw, ok := fields[key]; ok {
		_ = json.Unmarshal(raw, &value)
	}
	return value
}

// fieldBool reads one boolean field, or false for a field that is absent or
// not a boolean.
func fieldBool(fields map[string]json.RawMessage, key string) bool {
	var value bool
	if raw, ok := fields[key]; ok {
		_ = json.Unmarshal(raw, &value)
	}
	return value
}

// turnState follows the turns of the main session. Guarded by Agent.stateMu.
//
// Kiro brackets each execution with a turn_start and a turn_end marker,
// whoever started it. A prompt of LeapMux's can run more than one execution --
// a steer that the turn never read continues the same prompt -- and Kiro can
// start a turn of its own, with no prompt. The markers carry no mark of their
// owner. The updates of a turn that Kiro started carry `agentInitiated`, so a
// turn is Kiro's own when it opens while LeapMux runs no prompt, or when its
// updates say so.
type turnState struct {
	// agentTurn records that the base runs a turn that Kiro started, which
	// the next turn_end of the main session ends.
	agentTurn bool
	// heldError is the last display_error of a prompt of LeapMux's. A prompt
	// that fails reports its own failure through its JSON-RPC error, which
	// states the same text on every probed failure. So the display error waits:
	// a turn end that is not an error writes it, and otherwise the end of the
	// prompt decides (see handlePromptEnded).
	heldError string
}

// handleSessionMetadata reads the `_meta` of one update of the main session.
// It answers true for an update that it consumed: every session_info_update,
// because Kiro sends one for each marker and each metadata change, and none of
// them is conversation.
func (a *Agent) handleSessionMetadata(updateType string, metadata map[string]json.RawMessage, update json.RawMessage) bool {
	raw := metadata[contracts.KiroMetaNamespace]
	fields := kiroFields(raw)
	if updateType != "session_info_update" {
		a.observeConversationUpdate(fields)
		return false
	}
	kind := fieldString(fields, contracts.KiroMetaKind)
	switch kind {
	case kiroKindTurnStart:
		a.handleTurnStart()
	case contracts.KiroKindTurnEnd:
		a.handleTurnEnd(fieldString(fields, contracts.KiroMetaStopReason), update)
	case kiroKindTurnCompletion:
		a.logTurnCompletion(raw)
	case kiroKindContextUsage:
		a.broadcastContextUsage(raw)
	case kiroKindDisplayError:
		a.handleDisplayError(raw)
	case kiroKindInteractionResolved:
		a.handleInteractionResolved(raw)
	case kiroKindSummarizationStart, kiroKindSummarizationDone, kiroKindSummarizationFailed:
		a.reportSummarization(kind, raw)
	case kiroKindHookUpdate:
		a.reportHook(raw)
	case kiroKindRecap:
		a.reportRecap(raw)
	default:
		if reason, silent := kiroSilentKinds[kind]; silent {
			slog.Debug("kiro session info not shown", "agent_id", a.AgentID(), "kind", kind, "reason", reason)
		} else {
			slog.Warn("kiro session info of an unknown kind", "agent_id", a.AgentID(), "kind", kind)
		}
	}
	return true
}

// observeConversationUpdate reads what one conversation update of the main
// session states beside its conversation: the mark of a turn that Kiro
// started.
func (a *Agent) observeConversationUpdate(fields map[string]json.RawMessage) {
	if fieldBool(fields, kiroMetaAgentInitiated) {
		a.beginAgentTurn()
	}
}

// handleTurnStart reads the start of one execution. With no prompt of
// LeapMux's running, it is a turn that Kiro started by itself, and the base
// opens it: the reader sees the agent working, and the worker queues input
// behind it rather than sending a prompt that would abort it.
//
// While a prompt of LeapMux's runs, the start opens either a continuation of
// that prompt or a turn that Kiro started the moment the prompt ended. Kiro's
// own turn marks its updates, and observeConversationUpdate opens it at the
// first of them.
//
// A display error that the prompt holds stays held through the start: it can
// come from the preparation of the turn, and the end of the prompt still
// settles it.
func (a *Agent) handleTurnStart() {
	if !a.PromptActive() {
		a.beginAgentTurn()
	}
}

// beginAgentTurn opens a turn that Kiro started, once.
func (a *Agent) beginAgentTurn() {
	a.stateMu.Lock()
	begun := a.turns.agentTurn
	a.stateMu.Unlock()
	if begun {
		return
	}
	// Outside the lock: the base publishes the turn state, which broadcasts.
	if !a.BeginAgentTurn() {
		return
	}
	a.stateMu.Lock()
	a.turns.agentTurn = true
	a.stateMu.Unlock()
}

// handleTurnEnd reads the end of one execution. It ends a turn that Kiro
// started. The response of a prompt of LeapMux's ends that prompt, after every
// execution of it.
//
// The update itself is the turn-end row of a turn that Kiro started. The
// browser plugin reads its stop reason into the divider, as it reads the
// prompt response of a turn that LeapMux started.
func (a *Agent) handleTurnEnd(stopReason string, update json.RawMessage) {
	a.stateMu.Lock()
	agentTurn := a.turns.agentTurn
	a.turns.agentTurn = false
	// An error end of a prompt of LeapMux's keeps the display error for the
	// end of the prompt, whose own error can state the same text. Any other
	// end leaves the display error the only record of it.
	heldError := ""
	if agentTurn || stopReason != kiroStopError {
		heldError = a.turns.heldError
		a.turns.heldError = ""
	}
	a.stateMu.Unlock()

	if heldError != "" {
		a.persistAgentError(heldError)
	}
	if stopReason == kiroStopMaxTokens {
		a.persistStatus("The response stopped at the output token limit of the model")
	}
	if agentTurn {
		a.EndAgentTurn(append(json.RawMessage(nil), update...))
	}
}

// logTurnCompletion logs the credits that one turn used. Kiro bills in
// credits and reports no token count, and LeapMux has no field for a credit.
func (a *Agent) logTurnCompletion(raw json.RawMessage) {
	var completion struct {
		Status    string `json:"status"`
		Summaries []struct {
			Usage float64 `json:"usage"`
			Unit  string  `json:"unitPlural"`
		} `json:"promptTurnSummaries"`
	}
	if json.Unmarshal(raw, &completion) != nil {
		return
	}
	for _, summary := range completion.Summaries {
		slog.Debug("kiro turn usage", "agent_id", a.AgentID(), "status", completion.Status, "usage", summary.Usage, "unit", summary.Unit)
	}
}

// broadcastContextUsage broadcasts the fill of the context window. Kiro
// reports it as a percentage and states no token count.
func (a *Agent) broadcastContextUsage(raw json.RawMessage) {
	var usage struct {
		UsagePercentage *float64 `json:"usagePercentage"`
		ContextUsage    struct {
			UsagePercentage *float64 `json:"usagePercentage"`
		} `json:"contextUsage"`
	}
	if json.Unmarshal(raw, &usage) != nil {
		return
	}
	percent := usage.ContextUsage.UsagePercentage
	if percent == nil {
		percent = usage.UsagePercentage
	}
	if percent == nil || *percent < 0 {
		return
	}
	a.Sink().BroadcastSessionInfo(map[string]any{
		contracts.SessionInfoKeyContextUsage: map[string]any{contracts.ContextUsageFieldUsagePercent: *percent},
	})
}

// handleDisplayError states an error that Kiro shows its own reader: a model
// call that failed after its retries, an MCP server that did not connect or
// needs authorization. During a prompt of LeapMux's it waits for the end of
// the turn (see handleTurnEnd). Otherwise it reaches the transcript at once.
func (a *Agent) handleDisplayError(raw json.RawMessage) {
	var displayError struct {
		DisplayError struct {
			Message   string `json:"message"`
			ErrorType string `json:"errorType"`
		} `json:"displayError"`
	}
	if json.Unmarshal(raw, &displayError) != nil {
		return
	}
	message := strings.TrimSpace(displayError.DisplayError.Message)
	if message == "" {
		message = strings.TrimSpace(displayError.DisplayError.ErrorType)
	}
	if message == "" {
		return
	}
	if a.PromptActive() && !a.AgentTurnActive() {
		a.stateMu.Lock()
		held := a.turns.heldError
		a.turns.heldError = message
		a.stateMu.Unlock()
		// Two errors in one turn: the first one is not the prompt's failure.
		if held != "" {
			a.persistAgentError(held)
		}
		return
	}
	a.persistAgentError(message)
}

// handlePromptEnded settles the display error that the ended prompt holds
// (Hooks.PromptEnded). It writes the error unless the prompt's own error
// states the same text: the base writes that error as the failure note of the
// prompt, and the reader then reads the reason once. A prompt that returned a
// result, or that the reader stopped, writes no failure note, so the display
// error is the only record of the reason.
func (a *Agent) handlePromptEnded(err error, stopped bool) {
	a.stateMu.Lock()
	held := a.turns.heldError
	a.turns.heldError = ""
	a.stateMu.Unlock()
	if held == "" {
		return
	}
	if err != nil && !stopped && strings.Contains(err.Error(), held) {
		return
	}
	a.persistAgentError(held)
}

// reportSummarization states a compaction of the context in the transcript.
// Kiro compacts on request and on its own at a threshold, and the reader sees
// neither in the conversation otherwise.
func (a *Agent) reportSummarization(kind string, raw json.RawMessage) {
	var summarization struct {
		Summarization struct {
			Status string `json:"status"`
		} `json:"summarization"`
	}
	_ = json.Unmarshal(raw, &summarization)
	switch kind {
	case kiroKindSummarizationStart:
		a.persistCompacting()
	case kiroKindSummarizationDone:
		a.noteCompactionSummarized()
		a.persistStatus("Context compacted")
	case kiroKindSummarizationFailed:
		a.noteCompactionSummarized()
		reason := strings.TrimSpace(strings.ReplaceAll(summarization.Summarization.Status, "_", " "))
		if reason == "" {
			a.persistStatus("Context compaction failed")
			return
		}
		a.persistStatus("Context compaction failed: " + reason)
	}
}

// reportHook states the end of a hook that Kiro ran. A hook is the user's own
// automation, so its end is the reader's to see, and a failure most of all.
// A running hook states nothing yet, and a hook that waits for approval raises
// a permission request of its own.
func (a *Agent) reportHook(raw json.RawMessage) {
	var update struct {
		Hook struct {
			Name   string `json:"name"`
			Status string `json:"status"`
			Output string `json:"output"`
		} `json:"hook"`
	}
	if json.Unmarshal(raw, &update) != nil {
		return
	}
	name := strings.TrimSpace(update.Hook.Name)
	if name == "" {
		name = "a hook"
	}
	output := strings.TrimSpace(update.Hook.Output)
	switch update.Hook.Status {
	case "completed":
		a.persistStatus(fmt.Sprintf("Hook %s completed", name))
	case "failed":
		text := fmt.Sprintf("Hook %s failed", name)
		if output != "" {
			text += ": " + output
		}
		a.persistStatus(text)
	case "canceled":
		a.persistStatus(fmt.Sprintf("Hook %s canceled", name))
	}
}

// reportRecap states the recap that Kiro writes when the reader returns to a
// session after a pause.
func (a *Agent) reportRecap(raw json.RawMessage) {
	var recap struct {
		Recap struct {
			Text string `json:"text"`
		} `json:"recap"`
	}
	if json.Unmarshal(raw, &recap) != nil {
		return
	}
	if text := strings.TrimSpace(recap.Recap.Text); text != "" {
		a.persistStatus("Recap: " + text)
	}
}

// persistCompacting states that a compaction of the context started.
func (a *Agent) persistCompacting() {
	a.Sink().PersistLeapMuxNotification(map[string]any{
		contracts.NotificationFieldType: contracts.NotificationTypeCompacting,
	})
}

// persistStatus states one live status of Kiro's in the transcript.
func (a *Agent) persistStatus(text string) {
	a.Sink().PersistLeapMuxNotification(map[string]any{
		contracts.NotificationFieldType: contracts.NotificationTypeAgentStatus,
		contracts.NotificationFieldText: text,
	})
}

// persistAgentError states one error of Kiro's in the transcript.
func (a *Agent) persistAgentError(text string) {
	a.Sink().PersistLeapMuxNotification(map[string]any{
		contracts.NotificationFieldType:  contracts.NotificationTypeAgentError,
		contracts.NotificationFieldError: text,
	})
}

// toolOutputState holds the live output of the running commands. Guarded by
// Agent.stateMu.
//
// Kiro streams a running command's output on `_kiro/tools/content_chunk`, one
// piece at a time, outside the tool call's own updates. Only this agent knows
// where the earlier pieces ended, and the running row draws what the command
// printed so far. The row is in the transcript of the session that runs the
// call: the main one, or the tab of a workflow step (see handleContentChunk).
type toolOutputState struct {
	// running holds the output of each running call that streamed a piece, by
	// tool-call id. Kiro gives each call an id of its own across its sessions.
	running map[string]*liveOutput
}

// liveOutput is what one running command printed so far.
type liveOutput struct {
	bytes int64
	tail  string
	// tailLost says tail dropped the start of the output.
	tailLost bool
}

// kiroLiveOutputLimit is the longest live text that one Kiro command keeps
// between chunks. The sink caps the tail again before it broadcasts. This cap
// keeps the joined text from growing without limit inside the agent.
const kiroLiveOutputLimit = 8192

// forgetToolOutput drops the live output of a call that ended.
func (a *Agent) forgetToolOutput(toolCallID string) {
	a.stateMu.Lock()
	delete(a.output.running, toolCallID)
	a.stateMu.Unlock()
}

// handleContentChunk adds one piece of a running command's output to its row.
//
// The row is in the transcript that holds the call open: the main one for a
// call of the session that this agent serves, and the step's tab for a call of
// a workflow step, whose session a registry row routes. A chunk of any other
// call streams nowhere.
func (a *Agent) handleContentChunk(params json.RawMessage) {
	var chunk struct {
		SessionID  string `json:"sessionId"`
		ToolCallID string `json:"toolCallId"`
		Content    struct {
			Type    string `json:"type"`
			Content struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"content"`
	}
	if err := json.Unmarshal(params, &chunk); err != nil {
		slog.Warn("kiro content chunk unreadable", "agent_id", a.AgentID(), "error", err)
		return
	}
	text := chunk.Content.Content.Text
	if chunk.ToolCallID == "" || text == "" || chunk.Content.Content.Type != "text" {
		return
	}
	sink, open := a.OpenToolSink(chunk.SessionID, chunk.ToolCallID)
	if !open {
		return
	}
	a.stateMu.Lock()
	output := a.output.running[chunk.ToolCallID]
	if output == nil {
		if a.output.running == nil {
			a.output.running = make(map[string]*liveOutput)
		}
		output = &liveOutput{}
		a.output.running[chunk.ToolCallID] = output
	}
	output.bytes = agent.SaturatingAdd(output.bytes, int64(len(text)))
	var clipped bool
	output.tail, clipped = agent.ClipTailBytes(output.tail+text, kiroLiveOutputLimit)
	if clipped {
		output.tailLost = true
	}
	total, tail, lost := output.bytes, output.tail, output.tailLost
	a.stateMu.Unlock()
	sink.ReportProgress(agent.OutputTotalProgress(chunk.ToolCallID, total, false))
	sink.ReportProgress(agent.OutputTailProgress(chunk.ToolCallID, tail, lost))
}
