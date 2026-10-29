package kiro

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
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
	kiroKindHookUpdate          = "hook_update"
	kiroKindRecap               = "recap"
	kiroKindSteeringInjected    = "steering_injected"
	kiroKindSteeringCleared     = "steering_cleared"
)

// The kinds that state nothing a LeapMux transcript or card shows. Each one is
// listed, so a kind that a later build of Kiro adds is logged as unknown.
var kiroSilentKinds = map[string]string{
	"pending_interaction":      "the request itself follows it",
	"user_message_id_assigned": "LeapMux keeps its own message ids",
	"focus_update":             "LeapMux names its tabs itself and follows the turn state from the turn markers",
	// steering_queued states the queue the worker already knows; the
	// injected and cleared kinds drive the steer ledger instead.
	"steering_queued":         "the input queue of the worker states a steer",
	"steering_inclusion":      "the steering documents are Kiro's own context",
	"queued":                  "the turn waits inside Kiro, and its markers follow",
	"repositories_update":     "LeapMux reads the working directory itself",
	"summarization_separator": "Kiro sends it only on a replay, which LeapMux turns off",
	"summary_message":         "Kiro sends it only on a replay, which LeapMux turns off",
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
func chunkMessageID(_ agent.AssembledMessageKind, metadata map[string]json.RawMessage) string {
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
	// heldEndFrame is the native error turn end of a prompt of LeapMux's,
	// which the failed prompt writes as its divider (see handlePromptEnded).
	heldEndFrame json.RawMessage
}

// handleSessionMetadata reads the `_meta` of one update of the main session.
// It answers true for an update that it consumed: each session_info_update of
// a kind it knows, because Kiro sends one for each marker and each metadata
// change, and none of them is conversation. An unknown kind answers false, so
// the update persists where the reader can see what a later build states.
func (a *Agent) handleSessionMetadata(updateType string, metadata map[string]json.RawMessage, update json.RawMessage) bool {
	raw := metadata[contracts.KiroMetaNamespace]
	fields := kiroFields(raw)
	if updateType != "session_info_update" {
		// Kiro copies the kiro metadata of the prompt into every chunk of its
		// turn, so a main-session text chunk with none at all is the answer of
		// an execution that Kiro aborted: its text reaches no reader.
		if updateType == "agent_message_chunk" && len(metadata) == 0 {
			return true
		}
		// A content-filter refusal rides a text chunk of its own: the answer
		// keeps the text before it, and the refusal frame becomes its own row.
		if updateType == "agent_message_chunk" && len(pickRefusal(fields)) > 0 {
			a.FlushBufferedOutput(agent.MessageCompletionComplete)
			if err := a.persistNativeNotification(update); err != nil {
				slog.Error("kiro persist refusal chunk", "agent_id", a.AgentID(), "error", err)
			}
			return true
		}
		a.observeConversationUpdate(fields)
		return false
	}
	kind := fieldString(fields, contracts.KiroMetaKind)
	switch kind {
	case kiroKindTurnStart:
		a.handleTurnStart()
	case contracts.KiroKindTurnEnd:
		a.handleTurnEnd(fieldString(fields, contracts.KiroMetaStopReason), update)
		a.requestUsageRefresh()
	case kiroKindTurnCompletion:
		a.logTurnCompletion(raw)
	case kiroKindContextUsage:
		a.broadcastContextUsage(raw)
	case kiroKindDisplayError:
		a.handleDisplayError(raw)
	case kiroKindInteractionResolved:
		a.handleInteractionResolved(raw)
	case kiroKindSummarizationStart, contracts.KiroKindSummarizationCompleted, contracts.KiroKindSummarizationFailed:
		a.reportSummarization(kind, raw, update)
	case kiroKindHookUpdate:
		a.reportHook(raw, update)
	case kiroKindRecap:
		a.reportRecap(raw)
	case kiroKindSteeringInjected:
		a.handleSteeringInjected(fieldString(fields, "messageId"))
	case kiroKindSteeringCleared:
		a.handleSteeringCleared(fields["messageIds"])
	default:
		if reason, silent := kiroSilentKinds[kind]; silent {
			slog.Debug("kiro session info not shown", "agent_id", a.AgentID(), "kind", kind, "reason", reason)
			return true
		}
		// An unknown kind persists its update, so the reader can see what a
		// later Kiro build states (decision 1). The base's own
		// session_info_update case consumes silently, so this handler writes
		// the row itself.
		slog.Warn("kiro session info of an unknown kind", "agent_id", a.AgentID(), "kind", kind)
		if err := a.persistNativeNotification(update); err != nil {
			slog.Error("kiro persist unknown session info", "agent_id", a.AgentID(), "kind", kind, "error", err)
		}
		return true
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
		return
	}
	// An error end of a prompt of LeapMux's keeps its native frame for the
	// divider of that prompt: the error row lands above it (see
	// handlePromptEnded).
	if stopReason == kiroStopError {
		a.stateMu.Lock()
		a.turns.heldEndFrame = append(json.RawMessage(nil), update...)
		a.stateMu.Unlock()
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
// reports a percentage and a breakdown whose categories each state their own
// token count; the meter sums them, because the whole window is what fills.
// The window size arrives only when the current model states one, which the
// reply of a compaction refreshes.
func (a *Agent) broadcastContextUsage(raw json.RawMessage) {
	var usage struct {
		UsagePercentage *float64 `json:"usagePercentage"`
		ContextUsage    struct {
			UsagePercentage *float64 `json:"usagePercentage"`
		} `json:"contextUsage"`
		Breakdown json.RawMessage `json:"breakdown"`
	}
	if json.Unmarshal(raw, &usage) != nil {
		return
	}
	var categories map[string]struct {
		Tokens float64 `json:"tokens"`
	}
	_ = json.Unmarshal(usage.Breakdown, &categories)
	percent := usage.ContextUsage.UsagePercentage
	if percent == nil {
		percent = usage.UsagePercentage
	}
	if percent == nil || *percent < 0 {
		return
	}
	info := map[string]any{contracts.ContextUsageFieldUsagePercent: *percent}
	var tokens float64
	for _, category := range categories {
		tokens += category.Tokens
	}
	if tokens > 0 {
		info[contracts.ContextUsageFieldContextTokens] = tokens
	}
	if window := a.contextWindow(); window > 0 {
		info[contracts.ContextUsageFieldContextWindow] = window
	}
	a.Sink().BroadcastSessionInfo(map[string]any{
		contracts.SessionInfoKeyContextUsage: info,
	})
}

// persistNativeNotification retains one native main-session notification.
func (a *Agent) persistNativeNotification(update json.RawMessage) error {
	_, err := a.Sink().PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: update, AgentSessionID: a.CurrentSessionID()})
	return err
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
// error is the only record of the reason. Every end returns the retry verdict
// of the prompt's error.
func (a *Agent) handlePromptEnded(err error, stopped bool) acp.PromptEnding {
	a.requestUsageRefresh()
	a.stateMu.Lock()
	held := a.turns.heldError
	a.turns.heldError = ""
	frame := a.turns.heldEndFrame
	a.turns.heldEndFrame = nil
	a.stateMu.Unlock()
	failure, payload := kiroTurnFailure(err, stopped)
	failed := err != nil && !stopped
	// The base writes the prompt's own error as its failure note, so a held
	// error with the same text is written once.
	statedByPrompt := failed && strings.Contains(err.Error(), held)
	if held != "" && !statedByPrompt {
		a.persistAgentError(held)
	}
	ending := acp.PromptEnding{Retry: &acp.TurnVerdict{Failure: failure, Payload: payload}}
	// The failed prompt writes the native error turn end as its divider, so
	// the reader sees why the turn ended beside the error itself, whichever
	// note states that error.
	if failed && len(frame) > 0 {
		ending.EndFrame = frame
	}
	return ending
}

// Kiro states the class of a failed prompt in the `data` of its JSON-RPC
// error: errorType gives the failure, and retryErrorType gives the class that
// Kiro's own retries read. These are the errorType values that LeapMux reads.
const (
	kiroErrorUsageLimitReached    = "UsageLimitReachedError"
	kiroErrorOverageLimitReached  = "OverageLimitReachedError"
	kiroErrorProxyAuthRequired    = "ProxyAuthenticationRequiredError"
	kiroErrorClientThrottle       = "ClientThrottleError"
	kiroRetryErrorTypeTransient   = "TRANSIENT"
	kiroRetryErrorTypeServerError = "SERVER_ERROR"
	kiroRetryErrorTypeThrottling  = "THROTTLING"
)

// kiroBillingErrorTypes are the account blocks that no wait lifts.
var kiroBillingErrorTypes = map[string]bool{
	"AccountNotSupportedError":  true,
	"NewUserAccessPausedError":  true,
	"TemporarilySuspendedError": true,
}

// kiroTurnFailure classifies the end of one prompt. payload is the JSON-RPC
// error that carries the failure, or nil.
func kiroTurnFailure(err error, stopped bool) (failure providerkit.TurnFailure, payload []byte) {
	notRetryable := providerkit.TurnFailure{Kind: providerkit.TurnFailureNotRetryable}
	if stopped || err == nil {
		return notRetryable, nil
	}
	rpcErr, ok := providerkit.AsJSONRPCResponseError(err)
	if !ok {
		return notRetryable, nil
	}
	payload = rpcErr.WireJSON()
	// Data of another shape, such as the `details` of a -32603, states no
	// class and stays at the zero value.
	var data struct {
		ErrorType      string `json:"errorType"`
		RetryErrorType string `json:"retryErrorType"`
	}
	_ = json.Unmarshal(rpcErr.Data, &data)
	switch {
	case kiroBillingErrorTypes[data.ErrorType]:
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureBillingBlocked, Cause: "Kiro reported " + data.ErrorType}, payload
	case data.ErrorType == kiroErrorUsageLimitReached:
		// The message can state the window: hourly, daily, weekly or
		// monthly.
		return providerkit.TurnFailure{
			Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowOf(rpcErr.Message),
			Cause: "Kiro reported its usage limit",
		}, payload
	case data.ErrorType == kiroErrorOverageLimitReached:
		return providerkit.TurnFailure{
			Kind: providerkit.TurnFailureUsageLimited, Window: providerkit.UsageWindowUnstated,
			Cause: "Kiro reported its overage limit",
		}, payload
	case data.ErrorType == kiroErrorProxyAuthRequired:
		// Kiro classifies a proxy that wants credentials as TRANSIENT, but
		// only a change of the proxy settings lifts it.
		return notRetryable, payload
	case data.RetryErrorType == kiroRetryErrorTypeThrottling, data.ErrorType == kiroErrorClientThrottle:
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureRateLimited, Cause: "Kiro reported " + data.ErrorType}, payload
	case data.RetryErrorType == kiroRetryErrorTypeTransient, data.RetryErrorType == kiroRetryErrorTypeServerError:
		return providerkit.TurnFailure{Kind: providerkit.TurnFailureTransient, Cause: "Kiro reported " + data.ErrorType}, payload
	}
	return notRetryable, payload
}

// reportSummarization states a compaction of the context in the transcript.
// Kiro compacts on request and on its own at a threshold, and the reader sees
// neither in the conversation otherwise.
func (a *Agent) reportSummarization(kind string, raw json.RawMessage, update json.RawMessage) {
	switch kind {
	case kiroKindSummarizationStart:
		providerkit.PublishCompaction(a.Sink(), "auto")
	case contracts.KiroKindSummarizationCompleted, contracts.KiroKindSummarizationFailed:
		a.noteCompactionSummarized()
		a.FlushBufferedOutput(agent.MessageCompletionComplete)
		if err := a.persistNativeNotification(update); err != nil {
			slog.Error("kiro persist summarization end", "agent_id", a.AgentID(), "error", err)
		}
		providerkit.EndCompaction(a.Sink())
	}
}

// reportHook reads the end of a hook that Kiro ran. A v2 completed hook
// states nothing a reader can use: a failure looks the same as a success, so
// Kiro itself cannot be asked which happened. A v1 failed or canceled hook
// carries its command result, and its native frame becomes the row the hook
// entry renders. A running hook states nothing yet, and a hook that waits for
// approval raises a permission request of its own.
func (a *Agent) reportHook(raw json.RawMessage, update json.RawMessage) {
	var hook struct {
		Hook struct {
			Status string `json:"status"`
		} `json:"hook"`
	}
	if json.Unmarshal(raw, &hook) != nil {
		return
	}
	switch hook.Hook.Status {
	case "failed", "canceled":
		if err := a.persistNativeNotification(update); err != nil {
			slog.Error("kiro persist hook update", "agent_id", a.AgentID(), "error", err)
		}
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

// handleSteeringInjected reports one steer as read by the model. The flush
// runs first, so the text that the model wrote before the injection lands
// above the steer row. An injection whose steer reply has not stated its id
// yet waits in the early set; the reply completes it.
func (a *Agent) handleSteeringInjected(messageID string) {
	if messageID == "" {
		return
	}
	a.FlushBufferedOutput(agent.MessageCompletionComplete)
	if !a.steers.EchoID(messageID) {
		a.stateMu.Lock()
		if a.earlySteerEchoes == nil {
			a.earlySteerEchoes = make(map[string]providerkit.SteerTicket)
		}
		a.earlySteerEchoes[messageID] = 0
		a.stateMu.Unlock()
		return
	}
	a.steers.Settle(a.Sink())
}

// handleSteeringCleared drops each steer that Kiro cleared without injecting:
// the turn it belonged to is over, and Kiro itself states that the model never
// read it.
func (a *Agent) handleSteeringCleared(raw json.RawMessage) {
	var cleared []string
	if json.Unmarshal(raw, &cleared) != nil {
		return
	}
	dropped := false
	for _, id := range cleared {
		a.stateMu.Lock()
		_, early := a.earlySteerEchoes[id]
		delete(a.earlySteerEchoes, id)
		a.stateMu.Unlock()
		if early {
			// The injection never reached its own steer entry, so nothing is
			// pending under this id. The early marker alone ends here.
			continue
		}
		if a.steers.DropID(id) {
			dropped = true
		}
	}
	if dropped {
		a.steers.Settle(a.Sink())
	}
}

// pickRefusal reads the refusal object of one kiro metadata block, or nil.
func pickRefusal(fields map[string]json.RawMessage) json.RawMessage {
	if fields == nil {
		return nil
	}
	return fields[contracts.KiroMetaRefusal]
}
